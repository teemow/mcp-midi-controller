// Command auv3-probe is a standalone off-daemon LAN receiver for the auv3-probe
// iPad app (github.com/teemow/auv3-probe). It serves two sibling surfaces on one
// listener:
//
//   - AUv3 parameter-tree dumps (internal/auv3receiver): the app walks each
//     plugin's AUParameterTree and POSTs the JSON here; this stages it on disk
//     for the daemon to ingest via the import_auv3_probe MCP tool.
//   - AUM sessions (internal/aumreceiver): the app uploads .aumproj /
//     .aum_midimap bytes it read out of AUM's folder and downloads sessions the
//     aum tools authored/edited so it can write them back into AUM.
//
// The same two receivers are also built into the daemon (it serves them on
// config.AUv3ReceiverAddr unless disabled), so this command is only needed to
// run the receiver separately from the daemon — e.g. on a different host, or
// staging into custom directories. The shared implementations live in
// internal/auv3receiver and internal/aumreceiver.
//
// Endpoints:
//
//	POST /auv3-probe              decode+validate a device.ProbeDump, stage it
//	POST /auv3-probe/diagnostics  record a full probe-run report (incl. failures)
//	POST /aum-session?name=<file> stage one uploaded .aumproj (raw bplist bytes)
//	GET  /aum-session             manifest of stageable sessions/midimaps (JSON)
//	GET  /aum-session/{file}      download a staged .aumproj / .aum_midimap
//	GET  /healthz                 liveness, so the app can test connectivity first
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/aumreceiver"
	"github.com/teemow/mcp-midi-controller/internal/auv3receiver"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
)

func main() {
	listen := flag.String("listen", ":7800", "LAN bind address for the receiver (NOT the MCP endpoint; LAN is fine here)")
	probes := flag.String("out", config.AUv3ProbesDir(), "directory to stage received AUv3 probe dumps in")
	sessions := flag.String("sessions", config.AUMSessionsDir(), "directory to stage uploaded / downloadable AUM sessions in")
	seed := flag.Bool("seed", true, "when the sessions dir is empty, seed a synthetic template.aumproj so the app has something to list/download")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for _, d := range []string{*probes, *sessions} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("auv3-probe: create dir %s: %v", d, err)
		}
	}

	if *seed {
		if err := seedTemplateSession(*sessions); err != nil {
			log.Printf("auv3-probe: seed template session: %v (continuing)", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", lanhttp.Healthz)
	auv3receiver.Register(mux, *probes, nil)
	aumreceiver.Register(mux, *sessions, nil, nil)

	log.Printf("auv3-probe receiver listening on %s (auv3 probes -> %s, aum sessions -> %s)", *listen, *probes, *sessions)
	log.Printf("POST your AUParameterTree dump to http://<this-host>%s/auv3-probe", *listen)
	log.Printf("POST/GET AUM sessions at http://<this-host>%s/aum-session", *listen)
	if err := lanhttp.Serve(ctx, *listen, mux); err != nil && err != context.Canceled {
		log.Fatalf("auv3-probe: serve: %v", err)
	}
}

// seedTemplateSession writes a synthetic, fully-public template.aumproj into dir
// when no session/midimap is staged yet, so a fresh receiver has something for
// the app to list and download (the manifest is otherwise empty until the app
// uploads). The template comes from aum.Template() — built in code precisely so
// no real (private) session is ever committed. A non-empty dir is left untouched.
func seedTemplateSession(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if aum.FileKind(e.Name()) != "" {
			return nil // already has stageable files; don't seed
		}
	}
	data, err := aum.Template().Encode()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "template.aumproj")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	log.Printf("seeded synthetic template session -> %s (%d bytes)", path, len(data))
	return nil
}
