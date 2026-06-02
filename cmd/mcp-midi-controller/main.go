// Command mcp-midi-controller runs the MIDI/OSC rig controller as a persistent
// local daemon, exposing MCP over streamable-HTTP bound to loopback. It is
// intended to run as a systemd user unit.
package main

import (
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/mcpserver"
	"github.com/teemow/mcp-midi-controller/internal/transport"
	"github.com/teemow/mcp-midi-controller/internal/transport/blemidi"
	"github.com/teemow/mcp-midi-controller/internal/transport/osc"
	"github.com/teemow/mcp-midi-controller/internal/transport/usbmidi"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(0)
	log.Printf("mcp-midi-controller %s", version)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if !isLoopback(cfg.ListenAddr) {
		log.Fatalf("refusing non-loopback listen address %q (loopback only)", cfg.ListenAddr)
	}

	reg, err := device.LoadBundled()
	if err != nil {
		log.Fatalf("load bundled definitions: %v", err)
	}
	if err := reg.LoadDir(config.DevicesDir()); err != nil {
		log.Fatalf("load user definitions: %v", err)
	}

	transports := []transport.Transport{
		mustTransport(blemidi.New()),
		mustTransport(osc.New()),
		mustTransport(usbmidi.New()),
	}

	eng := engine.New(reg, transports...)

	// Restore the rig-as-code bindings so the daemon comes back up with the same
	// logical devices (and their control_<logical> tools) it had before.
	// TODO: restore desired-state from the state dir.
	bindings, err := engine.LoadBindingsFile(config.BindingsPath())
	if err != nil {
		log.Fatalf("load bindings: %v", err)
	}
	for _, b := range bindings {
		if err := eng.Bind(b); err != nil {
			log.Printf("skip binding %q: %v", b.Logical, err)
		}
	}

	srv := mcpserver.New(eng)

	log.Printf("mcp-midi-controller listening on http://%s (loopback)", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.Handler()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func mustTransport(t transport.Transport, err error) transport.Transport {
	if err != nil {
		log.Fatalf("init transport: %v", err)
	}
	return t
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
