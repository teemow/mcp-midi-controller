// Command auv3-probe is a standalone off-daemon LAN receiver for AUv3
// parameter-tree dumps (design build-order step 8; see
// docs/research/auv3-feedback.md). The iOS auv3-probe app
// (github.com/teemow/auv3-probe) walks each plugin's AUParameterTree and POSTs
// the JSON here; this receiver validates it and stages it on disk for the
// daemon to ingest via the import_auv3_probe MCP tool.
//
// The same receiver is now built into the daemon (it serves on
// config.AUv3ReceiverAddr unless disabled), so this command is only needed if
// you want to run the receiver separately from the daemon — e.g. on a different
// host, or staging into a custom directory. The shared implementation lives in
// internal/auv3receiver.
//
// Endpoints:
//
//	POST /auv3-probe   decode+validate a device.ProbeDump, write <ProbeID>.json
//	GET  /healthz      liveness, so the iOS app can test connectivity first
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/teemow/mcp-midi-controller/internal/auv3receiver"
	"github.com/teemow/mcp-midi-controller/internal/config"
)

func main() {
	listen := flag.String("listen", ":7800", "LAN bind address for the probe receiver (NOT the MCP endpoint; LAN is fine here)")
	out := flag.String("out", config.AUv3ProbesDir(), "directory to stage received dumps in")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("POST your AUParameterTree dump to http://<this-host>%s/auv3-probe", *listen)
	if err := auv3receiver.Serve(ctx, *listen, *out, nil); err != nil && err != context.Canceled {
		log.Fatalf("auv3-probe: serve: %v", err)
	}
}
