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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/sanitize"
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
	Title    string `json:"title,omitempty"`
	Version  int    `json:"version"`
	Channels int    `json:"channels"`
	Mappings int    `json:"mappings"`
	Bytes    int    `json:"bytes"`
	Staged   string `json:"staged"`
}

// ManifestEntry is one staged session/midimap in the GET /aum-session listing.
type ManifestEntry struct {
	ID       string `json:"id"`
	File     string `json:"file"`
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
	Dir      string          `json:"dir"`
	Sessions []ManifestEntry `json:"sessions"`
}

// Register adds the AUM-session routes (NOT /healthz) to mux. The daemon uses
// this to mount the session surface alongside the AUv3-probe surface on one
// shared LAN listener; Handler wraps it for standalone use / tests.
//
// onStaged (may be nil) is invoked after each upload is written. onDownloaded
// (may be nil) is invoked with the bare staged filename after the iPad
// downloads it — the surest signal that session is about to be loaded into
// AUM, which the daemon uses to track its "current session" and auto-import
// the session rig. Both run synchronously after the response is served.
//
// Routes:
//
//	POST   /aum-session           stage one uploaded .aumproj (raw bplist bytes)
//	GET    /aum-session           manifest of stageable sessions/midimaps (JSON)
//	GET    /aum-session/{file}    download a staged .aumproj / .aum_midimap
//	DELETE /aum-session/{file}    remove one staged file
//	DELETE /aum-session           clear all staged files
func Register(mux *http.ServeMux, outDir string, onStaged func(Result), onDownloaded func(file string)) {
	mux.HandleFunc("POST /aum-session", handleUpload(outDir, onStaged))
	mux.HandleFunc("GET /aum-session", handleManifest(outDir))
	mux.HandleFunc("GET /aum-session/{file}", handleDownload(outDir, onDownloaded))
	mux.HandleFunc("DELETE /aum-session/{file}", handleDelete(outDir))
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
// NSKeyedArchiver session, derives an id (the ?name= filename, else the session
// title, else a timestamp), and stages them as <id>.aumproj. Validation rejects
// anything that is not a decodable session so the staging dir only ever holds
// files the aum tools can read back.
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

		id := deriveID(r.URL.Query().Get("name"), sess.Title())
		if id == "" {
			resp.Error(w, http.StatusBadRequest, "could not derive an id (provide ?name=<filename>)")
			return
		}

		if err := os.MkdirAll(outDir, 0o755); err != nil {
			resp.Error(w, http.StatusInternalServerError, "create out dir %s: %v", outDir, err)
			return
		}
		path := filepath.Join(outDir, id+".aumproj")
		if err := lanhttp.WriteFileAtomic(path, data, 0o644); err != nil {
			resp.Error(w, http.StatusInternalServerError, "write %s: %v", path, err)
			return
		}
		// Preserve the uploader's original modified time when supplied, so the
		// manifest date matches what the device shows for the same file.
		if mod := r.URL.Query().Get("modified"); mod != "" {
			if t, perr := time.Parse(time.RFC3339, mod); perr == nil {
				_ = os.Chtimes(path, t, t)
			}
		}

		res := Result{
			ID:       id,
			Title:    sess.Title(),
			Version:  sm.Version,
			Channels: len(sm.Channels),
			Mappings: len(sm.Mappings),
			Bytes:    len(data),
			Staged:   path,
		}
		log.Printf("staged AUM session %q -> %s: v%d, %d channels, %d mappings, %d bytes",
			res.Title, path, res.Version, res.Channels, res.Mappings, res.Bytes)
		if onStaged != nil {
			onStaged(res)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// handleManifest lists every staged .aumproj / .aum_midimap so the iPad can see
// which sessions are available to download (the ones it uploaded and the ones
// the aum tools authored/edited). Each entry's session summary is best-effort:
// an unreadable file is still listed with its error so it is visible, not
// silently dropped.
func handleManifest(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		man := Manifest{Dir: outDir, Sessions: []ManifestEntry{}}
		entries, err := os.ReadDir(outDir)
		if err != nil {
			if !os.IsNotExist(err) {
				resp.Error(w, http.StatusInternalServerError, "read out dir %s: %v", outDir, err)
				return
			}
			// No dir yet: an empty manifest is the correct, non-error answer.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(man)
			return
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			kind := aum.FileKind(e.Name())
			if kind == "" {
				continue
			}
			entry := ManifestEntry{
				ID:   aum.StripExt(e.Name()),
				File: e.Name(),
				Kind: kind,
			}
			if info, ierr := e.Info(); ierr == nil {
				entry.Bytes = info.Size()
				entry.Modified = info.ModTime().UTC().Format(time.RFC3339)
			}
			summarize(filepath.Join(outDir, e.Name()), &entry)
			man.Sessions = append(man.Sessions, entry)
		}
		sort.Slice(man.Sessions, func(i, j int) bool { return man.Sessions[i].File < man.Sessions[j].File })

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

// handleDownload serves one staged file by name. The {file} segment is
// client-supplied, so it is constrained to a bare .aumproj / .aum_midimap
// filename: anything with a path separator or ".." could escape the staging dir
// and read an arbitrary file. After the bytes are served, onDownloaded (when
// non-nil) is told which file the iPad fetched.
func handleDownload(outDir string, onDownloaded func(file string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if !safeStagedName(name) {
			resp.Error(w, http.StatusBadRequest, "invalid file name %q", name)
			return
		}
		path := filepath.Join(outDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				resp.Error(w, http.StatusNotFound, "no staged file %q", name)
				return
			}
			resp.Error(w, http.StatusInternalServerError, "read %s: %v", path, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		_, _ = w.Write(data)
		if onDownloaded != nil {
			onDownloaded(name)
		}
	}
}

// handleDelete removes one staged file. The {file} segment is client-supplied,
// so it is constrained to a bare .aumproj / .aum_midimap filename (no traversal),
// exactly like handleDownload. Deleting a missing file is a 404, not a silent ok.
func handleDelete(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if !safeStagedName(name) {
			resp.Error(w, http.StatusBadRequest, "invalid file name %q", name)
			return
		}
		path := filepath.Join(outDir, name)
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				resp.Error(w, http.StatusNotFound, "no staged file %q", name)
				return
			}
			resp.Error(w, http.StatusInternalServerError, "delete %s: %v", path, err)
			return
		}
		log.Printf("deleted staged AUM session %s", path)
		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteAllResult reports how many staged files a clear-all removed.
type DeleteAllResult struct {
	Deleted int `json:"deleted"`
}

// handleDeleteAll removes every staged .aumproj / .aum_midimap (leaving any
// other files and subdirs untouched). A missing dir is not an error: there is
// simply nothing to clear.
func handleDeleteAll(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		entries, err := os.ReadDir(outDir)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, DeleteAllResult{Deleted: 0})
				return
			}
			resp.Error(w, http.StatusInternalServerError, "read out dir %s: %v", outDir, err)
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() || aum.FileKind(e.Name()) == "" {
				continue
			}
			path := filepath.Join(outDir, e.Name())
			if err := os.Remove(path); err != nil {
				resp.Error(w, http.StatusInternalServerError, "delete %s: %v", path, err)
				return
			}
			deleted++
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

// safeStagedName reports whether name is a bare .aumproj / .aum_midimap file
// (no path component, no traversal) safe to read from the staging dir.
func safeStagedName(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		return false
	}
	return aum.FileKind(name) != ""
}
