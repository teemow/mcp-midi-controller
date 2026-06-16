// Package aumreceiver is the off-MCP LAN listener that moves AUM session files
// between the iPad and the daemon. It is the AUM-session sibling of
// internal/auv3receiver: the auv3-probe iPad app POSTs raw .aumproj bytes it
// read out of AUM's folder, the daemon stages them on disk for the aum MCP
// tools (list/get/diff/import_aum_session), and the same app downloads sessions
// the tools authored or edited so it can write them back into AUM.
//
// Like the probe receiver it is a SEPARATE HTTP listener from the daemon's MCP
// endpoint (which is loopback-only and so unreachable from the iPad) and binds
// the LAN instead. The app stays a thin byte-ferry — Go owns all AUM
// serialization — so this receiver only validates that an upload decodes as an
// NSKeyedArchiver session and stages it; it never touches the engine,
// transports, or hardware.
package aumreceiver

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/midi-device/device/sanitize"
)

// resp renders this receiver's LAN errors without leaking internal detail.
var resp = lanhttp.Responder{Prefix: "aum-session receiver"}

// maxBodyBytes bounds a single upload. The receiver binds the LAN, so an
// unbounded body would let any host on the network drive the daemon out of
// memory. A real corpus session tops out well under this (the largest observed
// sessions are a few hundred KiB); the cap is generous.
const maxBodyBytes = 32 << 20 // 32 MiB

// Result summarizes a successfully staged session (also returned to the iPad as
// JSON so the app can confirm the round-trip).
type Result struct {
	ID       string `json:"id"`
	Path     string `json:"path,omitempty"` // staging-dir-relative path (mirrors the iPad's AUM tree)
	Title    string `json:"title,omitempty"`
	Version  int    `json:"version"`
	Channels int    `json:"channels"`
	Mappings int    `json:"mappings"`
	Bytes    int    `json:"bytes"`
	Staged   string `json:"staged"`
}

// ManifestEntry is one staged session/midimap in the GET /aum-session listing.
type ManifestEntry struct {
	ID   string `json:"id"`
	File string `json:"file"`
	// Path is the file's slash-separated path relative to the staging dir.
	// It mirrors the file's location in the iPad's AUM folder (e.g.
	// "Live sets/Set.aumproj"), so the app can write a download back into the
	// same subfolder instead of dropping it in AUM's root. Equals File for
	// files staged at the top level.
	Path     string `json:"path"`
	Kind     string `json:"kind"` // "session" (.aumproj) | "midimap" (.aum_midimap)
	Title    string `json:"title,omitempty"`
	Version  int    `json:"version,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Mappings int    `json:"mappings,omitempty"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified"`
	Error    string `json:"error,omitempty"`
}

// Manifest is the GET /aum-session response: every stageable file the iPad can
// download.
type Manifest struct {
	Dir string `json:"dir"`
	// Rev is the staging dir's monotonic change counter (aum.StagingRev),
	// bumped on every write — receiver uploads/deletes AND MCP-tool
	// author/edit/instrument/export. The app polls `GET /aum-session?rev=<n>`
	// with the last rev it saw and gets a 304 when nothing changed, so an
	// idle poll never walks or re-parses the staged tree.
	Rev      int64           `json:"rev"`
	Sessions []ManifestEntry `json:"sessions"`
}

// Register adds the AUM-session routes (NOT /healthz) to mux. The daemon uses
// this to mount the session surface alongside the AUv3-probe surface on one
// shared LAN listener; Handler wraps it for standalone use / tests.
//
// onStaged (may be nil) is invoked after each upload is written. onDownloaded
// (may be nil) is invoked with the staging-dir-relative path after the iPad
// downloads a file — the surest signal that session is about to be loaded into
// AUM, which the daemon uses to track its "current session" and auto-import
// the session rig. Both run synchronously after the response is served.
//
// Routes:
//
//	POST   /aum-session            stage one uploaded .aumproj (raw bplist bytes)
//	GET    /aum-session            manifest of stageable sessions/midimaps (JSON;
//	                               ?rev=<n> answers 304 when the staging rev matches)
//	GET    /aum-session/{file...}  download a staged .aumproj / .aum_midimap
//	DELETE /aum-session/{file...}  remove one staged file
//	DELETE /aum-session            clear all staged files
//
// {file...} is a staging-dir-relative path, so files staged in subfolders
// (mirroring the iPad's AUM tree) are addressable too.
func Register(mux *http.ServeMux, outDir string, onStaged func(Result), onDownloaded func(file string)) {
	mux.HandleFunc("POST /aum-session", handleUpload(outDir, onStaged))
	mux.HandleFunc("GET /aum-session", handleManifest(outDir))
	mux.HandleFunc("GET /aum-session/{file...}", handleDownload(outDir, onDownloaded))
	mux.HandleFunc("DELETE /aum-session/{file...}", handleDelete(outDir))
	mux.HandleFunc("DELETE /aum-session", handleDeleteAll(outDir))
}

// Handler builds the standalone receiver: the AUM-session routes plus a
// /healthz liveness endpoint. Sessions are staged in outDir. onStaged and
// onDownloaded behave as documented on Register.
func Handler(outDir string, onStaged func(Result), onDownloaded func(file string)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", lanhttp.Healthz)
	Register(mux, outDir, onStaged, onDownloaded)
	return mux
}

// handleUpload reads raw .aumproj bytes, validates them by decoding the
// NSKeyedArchiver session, and stages them. With ?path=<relative path> the file
// is staged VERBATIM at that path under the staging dir — subfolders and the
// original filename are preserved, so the staged tree mirrors the iPad's AUM
// folder exactly (the whole point: a later write-back can land in the same
// subfolder instead of AUM's root). Without ?path it falls back to the legacy
// flat staging: an id derived from ?name= / the session title / a timestamp,
// staged as <id>.aumproj at the top level. Validation rejects anything that is
// not a decodable session so the staging dir only ever holds files the aum
// tools can read back.
func handleUpload(outDir string, onStaged func(Result)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		defer func() { _ = r.Body.Close() }()

		data, err := io.ReadAll(r.Body)
		if err != nil {
			resp.Error(w, lanhttp.DecodeErrStatus(err), "read body: %v", err)
			return
		}
		if len(data) == 0 {
			resp.Error(w, http.StatusBadRequest, "empty body (expected .aumproj bytes)")
			return
		}

		// Validate by decoding the session graph; a malformed upload is a 400,
		// not a staged file we later fail to read.
		sess, err := aum.Open(data)
		if err != nil {
			resp.Error(w, http.StatusBadRequest, "decode session: %v", err)
			return
		}
		sm := sess.Map()

		var rel string
		if p := r.URL.Query().Get("path"); p != "" {
			// The upload route only stages sessions (the body must decode as
			// one), so the path must be a .aumproj too — not just any staged
			// kind.
			var ok bool
			if rel, ok = aum.SafeRelPath(p); !ok || aum.FileKind(rel) != aum.KindSession {
				resp.Error(w, http.StatusBadRequest, "invalid path %q (must be a relative .aumproj path, no traversal)", p)
				return
			}
		} else {
			id := deriveID(r.URL.Query().Get("name"), sess.Title())
			if id == "" {
				resp.Error(w, http.StatusBadRequest, "could not derive an id (provide ?name=<filename> or ?path=<relative path>)")
				return
			}
			rel = id + ".aumproj"
		}

		dest := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			resp.Error(w, http.StatusInternalServerError, "create out dir %s: %v", filepath.Dir(dest), err)
			return
		}
		if err := lanhttp.WriteFileAtomic(dest, data, 0o644); err != nil {
			resp.Error(w, http.StatusInternalServerError, "write %s: %v", dest, err)
			return
		}
		// Preserve the uploader's original modified time when supplied, so the
		// manifest date matches what the device shows for the same file.
		if mod := r.URL.Query().Get("modified"); mod != "" {
			if t, perr := time.Parse(time.RFC3339, mod); perr == nil {
				_ = os.Chtimes(dest, t, t)
			}
		}
		aum.BumpStagingRev(outDir)

		res := Result{
			ID:       aum.StripExt(rel),
			Path:     rel,
			Title:    sess.Title(),
			Version:  sm.Version,
			Channels: len(sm.Channels),
			Mappings: len(sm.Mappings),
			Bytes:    len(data),
			Staged:   dest,
		}
		log.Printf("staged AUM session %q -> %s: v%d, %d channels, %d mappings, %d bytes",
			res.Title, dest, res.Version, res.Channels, res.Mappings, res.Bytes)
		if onStaged != nil {
			onStaged(res)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// handleManifest lists every staged .aumproj / .aum_midimap — including files
// in subfolders, since staging mirrors the iPad's AUM tree — so the iPad can
// see which sessions are available to download (the ones it uploaded and the
// ones the aum tools authored/edited). Each entry's session summary is
// best-effort: an unreadable file is still listed with its error so it is
// visible, not silently dropped.
//
// With ?rev=<n> the handler answers 304 Not Modified when n equals the current
// staging rev, before walking (or parsing) anything — the cheap poll the iPad's
// sync engine runs while foregrounded. The rev is read BEFORE the walk so a
// write racing the walk yields a stale rev (and a prompt re-fetch), never a
// fresh rev on stale content.
func handleManifest(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rev := aum.StagingRev(outDir)
		if q := r.URL.Query().Get("rev"); q != "" {
			if seen, err := strconv.ParseInt(q, 10, 64); err == nil && seen == rev {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		man := Manifest{Dir: outDir, Rev: rev, Sessions: []ManifestEntry{}}
		err := aum.WalkStaged(outDir, func(rel, full, kind string, info fs.FileInfo) {
			entry := ManifestEntry{
				ID:   aum.StripExt(rel),
				File: path.Base(rel),
				Path: rel,
				Kind: kind,
			}
			if info != nil {
				entry.Bytes = info.Size()
				entry.Modified = info.ModTime().UTC().Format(time.RFC3339)
			}
			summarize(full, &entry)
			man.Sessions = append(man.Sessions, entry)
		})
		if err != nil && !os.IsNotExist(err) {
			resp.Error(w, http.StatusInternalServerError, "read out dir %s: %v", outDir, err)
			return
		}
		// A missing dir yields an empty manifest: the correct, non-error answer.
		sort.Slice(man.Sessions, func(i, j int) bool { return man.Sessions[i].Path < man.Sessions[j].Path })

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(man)
	}
}

// summarize fills the parsed session/midimap fields of a manifest entry from the
// file on disk, recording a decode failure on the entry rather than failing the
// whole manifest.
func summarize(path string, entry *ManifestEntry) {
	sum := aum.SummarizeFile(path)
	if sum.Err != nil {
		entry.Error = sum.Err.Error()
		return
	}
	entry.Title = sum.Title
	entry.Version = sum.Version
	entry.Channels = sum.Channels
	entry.Mappings = sum.Mappings
}

// handleDownload serves one staged file by its staging-dir-relative path. The
// {file...} segment is client-supplied, so it is validated by aum.SafeRelPath:
// anything absolute or containing ".." could escape the staging dir and read
// an arbitrary file. After the bytes are served, onDownloaded (when non-nil)
// is told which file the iPad fetched (the relative path, so the consumer
// knows the exact staged location).
func handleDownload(outDir string, onDownloaded func(file string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, ok := aum.SafeRelPath(r.PathValue("file"))
		if !ok {
			resp.Error(w, http.StatusBadRequest, "invalid file path %q", r.PathValue("file"))
			return
		}
		full := filepath.Join(outDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			if os.IsNotExist(err) {
				resp.Error(w, http.StatusNotFound, "no staged file %q", rel)
				return
			}
			resp.Error(w, http.StatusInternalServerError, "read %s: %v", full, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(rel)))
		_, _ = w.Write(data)
		if onDownloaded != nil {
			onDownloaded(rel)
		}
	}
}

// handleDelete removes one staged file by its staging-dir-relative path,
// validated by aum.SafeRelPath exactly like handleDownload. Deleting a missing
// file is a 404, not a silent ok. Subfolders left empty by the delete are
// pruned so the staged tree keeps mirroring the iPad's.
func handleDelete(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, ok := aum.SafeRelPath(r.PathValue("file"))
		if !ok {
			resp.Error(w, http.StatusBadRequest, "invalid file path %q", r.PathValue("file"))
			return
		}
		full := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.Remove(full); err != nil {
			if os.IsNotExist(err) {
				resp.Error(w, http.StatusNotFound, "no staged file %q", rel)
				return
			}
			resp.Error(w, http.StatusInternalServerError, "delete %s: %v", full, err)
			return
		}
		pruneEmptyDirs(outDir, filepath.Dir(full))
		aum.BumpStagingRev(outDir)
		log.Printf("deleted staged AUM session %s", full)
		w.WriteHeader(http.StatusNoContent)
	}
}

// pruneEmptyDirs removes dir and its now-empty parents up to (excluding) root.
// os.Remove refuses to delete a non-empty directory, so the loop naturally
// stops at the first parent that still holds files.
func pruneEmptyDirs(root, dir string) {
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(os.PathSeparator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

// DeleteAllResult reports how many staged files a clear-all removed.
type DeleteAllResult struct {
	Deleted int `json:"deleted"`
}

// handleDeleteAll removes every staged .aumproj / .aum_midimap at any depth
// (leaving any other files untouched), then prunes the subfolders the deletes
// emptied. A missing dir is not an error: there is simply nothing to clear.
func handleDeleteAll(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		deleted := 0
		var dirs []string
		walkErr := aum.WalkStaged(outDir, func(rel, full, kind string, _ fs.FileInfo) {
			if err := os.Remove(full); err == nil {
				deleted++
				dirs = append(dirs, filepath.Dir(full))
			}
		})
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				writeJSON(w, DeleteAllResult{Deleted: 0})
				return
			}
			resp.Error(w, http.StatusInternalServerError, "read out dir %s: %v", outDir, walkErr)
			return
		}
		for _, d := range dirs {
			pruneEmptyDirs(outDir, d)
		}
		if deleted > 0 {
			aum.BumpStagingRev(outDir)
		}
		log.Printf("cleared %d staged AUM session(s) from %s", deleted, outDir)
		writeJSON(w, DeleteAllResult{Deleted: deleted})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// deriveID turns an upload's ?name= filename (preferred) or session title into a
// safe staging id. The name is stripped of its extension first so
// "New Fast Forward.aumproj" becomes "new_fast_forward". A timestamp is the
// final fallback so an untitled, unnamed upload is still stageable.
func deriveID(name, title string) string {
	if id := sanitize.ID(aum.StripExt(name)); id != "" {
		return id
	}
	if id := sanitize.ID(title); id != "" {
		return id
	}
	return "session_" + time.Now().UTC().Format("20060102T150405")
}
