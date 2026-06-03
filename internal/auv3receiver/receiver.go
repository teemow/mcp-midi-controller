// Package auv3receiver is the off-MCP LAN listener that ingests AUv3
// parameter-tree dumps POSTed by the auv3-probe iPad app
// (github.com/teemow/auv3-probe) and stages them on disk for the
// list_auv3_probes / get_auv3_probe / import_auv3_probe tools.
//
// It is deliberately a SEPARATE HTTP listener from the daemon's MCP endpoint.
// The MCP endpoint is loopback-only (enforced in cmd/mcp-midi-controller), so
// the iPad cannot POST to it; this receiver must bind the LAN instead. Its
// surface is intentionally tiny and write-only: it validates a dump and writes
// <ProbeID>.json into the staging dir. It never touches the engine, transports,
// or hardware, so binding it on the LAN does not widen the control surface.
package auv3receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

// maxBodyBytes bounds a single request body. The receiver binds the LAN, so an
// unbounded JSON body would let any host on the network drive the daemon out of
// memory. A few MiB is generous for a parameter-tree dump or a probe-run report.
const maxBodyBytes = 8 << 20 // 8 MiB

// Result summarizes a successfully staged dump (also returned to the iPad as
// JSON so the app can confirm the round-trip).
type Result struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Params   int    `json:"params"`
	Writable int    `json:"writable"`
	Staged   string `json:"staged"`
}

// Handler builds the receiver's HTTP routes. Dumps are staged in outDir. If
// onStaged is non-nil it is invoked (synchronously) after each dump is written,
// e.g. so the daemon can notify connected MCP clients that new data arrived.
//
// Routes:
//
//	POST /auv3-probe              stage one plugin's parameter-tree dump
//	POST /auv3-probe/diagnostics  record a full probe-run report (incl. failures)
//	GET  /healthz                 liveness, so the app can test connectivity
func Handler(outDir string, onStaged func(device.ProbeDump, Result)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/auv3-probe/diagnostics", handleDiagnostics(outDir))
	mux.HandleFunc("/auv3-probe", handleProbe(outDir, onStaged))
	return mux
}

// Serve runs the receiver on addr until ctx is cancelled, then shuts it down
// gracefully. outDir is created if missing. A blank addr is treated as an error
// by the caller; the daemon disables the receiver before calling Serve in that
// case.
func Serve(ctx context.Context, addr, outDir string, onStaged func(device.ProbeDump, Result)) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir %s: %w", outDir, err)
	}

	// This listener faces the LAN, so set conservative timeouts and header
	// limits to bound the resources a slow or hostile client can tie up.
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(outDir, onStaged),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("auv3-probe receiver listening on %s, staging dumps in %s", addr, outDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}

// handleProbe decodes and validates a ProbeDump, derives its id, and stages it
// as <id>.json in outDir. Validation is intentionally light (the dump is
// authoring input, not a control surface): a dump only needs an id source.
//
// A dump with zero parameters is accepted and staged, not rejected: a plugin
// legitimately exposing no AUM-mappable parameters is useful diagnostic data
// (it tells an agent there is nothing to map), and rejecting it with a 400 was
// a large, spurious source of "errors" when probing every installed plugin.
func handleProbe(outDir string, onStaged func(device.ProbeDump, Result)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		defer r.Body.Close()

		var dump device.ProbeDump
		if err := json.NewDecoder(r.Body).Decode(&dump); err != nil {
			httpError(w, decodeErrStatus(err), "decode dump: %v", err)
			return
		}

		id := device.ProbeID(dump)
		if id == "" {
			httpError(w, http.StatusBadRequest, "dump has no id source (empty component.subtype and name)")
			return
		}

		b, err := json.MarshalIndent(dump, "", "  ")
		if err != nil {
			httpError(w, http.StatusInternalServerError, "re-encode dump: %v", err)
			return
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			httpError(w, http.StatusInternalServerError, "create out dir %s: %v", outDir, err)
			return
		}
		path := filepath.Join(outDir, id+".json")
		if err := writeFileAtomic(path, b, 0o644); err != nil {
			httpError(w, http.StatusInternalServerError, "write %s: %v", path, err)
			return
		}

		writable := 0
		for _, p := range dump.Parameters {
			if p.Writable {
				writable++
			}
		}
		res := Result{ID: id, Name: dump.Name, Params: len(dump.Parameters), Writable: writable, Staged: path}
		log.Printf("staged %s -> %s: %d params, %d writable", dump.Name, path, res.Params, res.Writable)
		if onStaged != nil {
			onStaged(dump, res)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// DiagnosticsResult summarizes a stored probe-run report (returned to the app).
type DiagnosticsResult struct {
	Total  int    `json:"total"`
	Sent   int    `json:"sent"`
	Empty  int    `json:"empty"`
	Failed int    `json:"failed"`
	Stored string `json:"stored"`
}

// handleDiagnostics records a full probe-run report — including the plugins
// that failed to instantiate or had no parameter tree, which never produce a
// dump — under outDir/_diagnostics/<timestamp>.json. This is what makes "all
// diagnostic data and errors" land on the receiver instead of staying only in
// the iPad app's UI.
func handleDiagnostics(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		defer r.Body.Close()

		var report device.ProbeReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			httpError(w, decodeErrStatus(err), "decode report: %v", err)
			return
		}

		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			httpError(w, http.StatusInternalServerError, "re-encode report: %v", err)
			return
		}
		diagDir := filepath.Join(outDir, "_diagnostics")
		if err := os.MkdirAll(diagDir, 0o755); err != nil {
			httpError(w, http.StatusInternalServerError, "create diagnostics dir %s: %v", diagDir, err)
			return
		}
		// Nanosecond precision so two reports posted in the same second do not
		// overwrite each other.
		name := time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
		path := filepath.Join(diagDir, name)
		if err := writeFileAtomic(path, b, 0o644); err != nil {
			httpError(w, http.StatusInternalServerError, "write %s: %v", path, err)
			return
		}

		total, sent, empty, failed := report.Summary()
		log.Printf("probe run report: %d plugins (%d sent, %d empty, %d failed) -> %s",
			total, sent, empty, failed, path)
		for _, res := range report.Results {
			if res.Status == "failed" {
				log.Printf("  probe failed: %s (%s): %s", res.Name, res.ID, res.Error)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DiagnosticsResult{
			Total: total, Sent: sent, Empty: empty, Failed: failed, Stored: path,
		})
	}
}

// httpError logs the detailed cause server-side but returns only a generic,
// status-derived message to the (untrusted LAN) client, so server filesystem
// paths and internal error strings are never leaked over the network.
func httpError(w http.ResponseWriter, code int, format string, args ...any) {
	log.Printf("auv3-probe receiver: %s", fmt.Sprintf(format, args...))
	http.Error(w, clientMessage(code), code)
}

// decodeErrStatus maps a body-decode error to a status: a body that exceeds the
// MaxBytesReader cap is 413 Request Entity Too Large, anything else is a 400.
func decodeErrStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func clientMessage(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "bad request"
	case http.StatusRequestEntityTooLarge:
		return "request too large"
	default:
		return "internal error"
	}
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it into place, so a concurrent reader (or a second POST for the same id)
// never observes a half-written file. The temp file is cleaned up on error.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
