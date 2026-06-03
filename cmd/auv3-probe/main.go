// Command auv3-probe is a throwaway off-daemon LAN receiver for AUv3
// parameter-tree dumps (design build-order step 8; see
// docs/research/auv3-feedback.md). The iOS auv3-probe app walks each plugin's
// AUParameterTree and POSTs the JSON here; this receiver validates it and
// stages it on disk for the daemon to ingest via the import_auv3_probe MCP
// tool. It is NOT part of the shipped daemon.
//
// Why a separate receiver and not the daemon: the daemon's MCP endpoint is
// loopback-only and enforced (cmd/mcp-midi-controller/main.go log.Fatalf on a
// non-loopback bind), so the iPad cannot POST to it. This utility binds the LAN
// instead and only ever writes JSON files into the staging dir — it never
// touches the daemon. The daemon then reads those files from disk.
//
// Endpoints:
//
//	POST /auv3-probe   decode+validate a device.ProbeDump, write <ProbeID>.json
//	GET  /healthz      liveness, so the iOS app can test connectivity first
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
)

func main() {
	listen := flag.String("listen", ":7800", "LAN bind address for the probe receiver (NOT the MCP endpoint; LAN is fine here)")
	out := flag.String("out", config.AUv3ProbesDir(), "directory to stage received dumps in")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("auv3-probe: create out dir %s: %v", *out, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/auv3-probe", handleProbe(*out))

	log.Printf("auv3-probe receiver listening on %s, staging dumps in %s", *listen, *out)
	log.Printf("POST your AUParameterTree dump to http://<this-host>%s/auv3-probe", *listen)
	srv := &http.Server{Addr: *listen, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("auv3-probe: serve: %v", err)
	}
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}

// handleProbe decodes and validates a ProbeDump, derives its id, and stages it
// as <id>.json in the out dir. Validation is intentionally light (the dump is
// authoring input, not a control surface): a dump needs an id source and at
// least one parameter.
func handleProbe(outDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		var dump device.ProbeDump
		if err := json.NewDecoder(r.Body).Decode(&dump); err != nil {
			httpError(w, http.StatusBadRequest, "decode dump: %v", err)
			return
		}

		id := device.ProbeID(dump)
		if id == "" {
			httpError(w, http.StatusBadRequest, "dump has no id source (empty component.subtype and name)")
			return
		}
		if len(dump.Parameters) == 0 {
			httpError(w, http.StatusBadRequest, "dump %q has no parameters", id)
			return
		}

		b, err := json.MarshalIndent(dump, "", "  ")
		if err != nil {
			httpError(w, http.StatusInternalServerError, "re-encode dump: %v", err)
			return
		}
		path := filepath.Join(outDir, id+".json")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			httpError(w, http.StatusInternalServerError, "write %s: %v", path, err)
			return
		}

		writable := 0
		for _, p := range dump.Parameters {
			if p.Writable {
				writable++
			}
		}
		log.Printf("staged %s -> %s: %d params, %d writable", dump.Name, path, len(dump.Parameters), writable)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       id,
			"name":     dump.Name,
			"params":   len(dump.Parameters),
			"writable": writable,
			"staged":   path,
		})
	}
}

func httpError(w http.ResponseWriter, code int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("auv3-probe: %s", msg)
	http.Error(w, msg, code)
}
