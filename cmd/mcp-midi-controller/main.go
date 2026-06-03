// Command mcp-midi-controller runs the MIDI/OSC rig controller as a persistent
// local daemon, exposing MCP over streamable-HTTP bound to loopback. It is
// intended to run as a systemd user unit.
package main

import (
	"context"
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
	"github.com/teemow/mcp-midi-controller/internal/transport/usbhid"
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
		mustTransport(usbhid.New()),
	}

	eng := engine.New(reg, transports...)

	// Restore the persisted desired-state so the daemon resumes the last applied
	// values, and keep writing it back after each change.
	if err := eng.EnableStatePersistence(config.DesiredStatePath()); err != nil {
		log.Printf("restore desired-state: %v", err)
	}

	// Restore the rig-as-code bindings so the daemon comes back up with the same
	// logical devices (and their control_<logical> tools) it had before.
	bindings, err := engine.LoadBindingsFile(config.BindingsPath())
	if err != nil {
		log.Fatalf("load bindings: %v", err)
	}
	for _, b := range bindings {
		if err := eng.Bind(b); err != nil {
			log.Printf("skip binding %q: %v", b.Logical, err)
		}
	}

	srv := mcpserver.New(eng, mcpserver.WithUSBAllowWrites(cfg.USBAllowWrites))

	// Begin listening for inbound MIDI on every bound endpoint so observed-state
	// and the feedback tools work without an explicit learn_start. This runs in
	// the background: connecting to BLE endpoints can block (or fail) when the
	// hardware is off, and that must never gate the loopback MCP endpoint from
	// coming up. Endpoints that are not reachable now are retried on demand by
	// verify/learn/probe.
	go func() {
		if err := eng.StartInboundForBindings(context.Background()); err != nil {
			log.Printf("inbound listeners: %v", err)
		}
	}()

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
