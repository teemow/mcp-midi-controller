// Command mcp-midi-controller runs the MIDI/OSC rig controller as a persistent
// local daemon, exposing MCP over streamable-HTTP bound to loopback. It is
// intended to run as a systemd user unit.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/aumreceiver"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/diagnostics"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/mcpserver"
	"github.com/teemow/mcp-midi-controller/internal/mdns"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
	"github.com/teemow/midi-transport/auv3"
	"github.com/teemow/midi-transport/auv3midi"
	"github.com/teemow/midi-transport/blemidi"
	"github.com/teemow/midi-transport/midicontrol"
	"github.com/teemow/midi-transport/osc"
	"github.com/teemow/midi-transport/usbhid"
	"github.com/teemow/midi-transport/usbmidi"
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

	// A SIGINT/SIGTERM cancels this context, which propagates to the inbound
	// listeners, the AUv3 receiver, and (via the goroutine below) the HTTP
	// server for a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, err := device.LoadBundled()
	if err != nil {
		log.Fatalf("load bundled device types: %v", err)
	}
	// A malformed user device type must not gate the daemon from starting: a bad
	// file is logged and skipped inside LoadDir. Only a directory-level read
	// error is surfaced here, and even then we serve the bundled set.
	if err := reg.LoadDir(config.DeviceTypesDir()); err != nil {
		log.Printf("load user device types: %v (serving bundled set)", err)
	}

	// The MIDI control hub holds the live ProbeMidiBrain channel (the agent's
	// "hands"): the daemon pushes note/CC/PC/transport commands the brain AUv3
	// emits on its host MIDI-out. It backs the auv3midi transport (so device
	// types declaring transport: auv3midi route over it) and the play_notes /
	// send_midi / set_transport tools, and is fed by the LAN receiver below.
	midiHub := midicontrol.NewHub()

	transports := []transport.Transport{
		mustTransport(blemidi.New()),
		mustTransport(osc.New()),
		mustTransport(usbmidi.New()),
		mustTransport(usbhid.New()),
		auv3midi.New(midiHub),
	}

	eng := engine.New(reg, transports...)
	// Share the USB write gate with the engine so patch-level scene recall obeys
	// the same usb_allow_writes master switch as the MCP write tools.
	eng.SetUSBAllowWrites(cfg.USBAllowWrites)

	// Restore the persisted desired-state so the daemon resumes the last applied
	// values, and keep writing it back after each change.
	if err := eng.EnableStatePersistence(config.DesiredStatePath()); err != nil {
		log.Printf("restore desired-state: %v", err)
	}

	// Restore the rig-as-code devices so the daemon comes back up with the same
	// logical devices (and their control_<logical> tools) it had before. A
	// malformed file must not stop the daemon: log it and start with no devices
	// (they can be re-created via the authoring tools).
	devices, err := engine.LoadDevicesFile(config.DevicesPath())
	if err != nil {
		log.Printf("load devices: %v (starting with no devices)", err)
	}
	for _, d := range devices {
		if err := eng.Bind(d); err != nil {
			log.Printf("skip device %q: %v", d.Name, err)
		}
	}

	// The audio tap registry holds the live ProbeAudioTap streams in memory (RAM
	// only — audio is a private, volatile rig signal): one named per-tap store
	// per concurrently-connected insert. It backs the read-only get_audio_tap
	// MCP tool and is fed by the LAN receiver below.
	audioRegistry := audiotap.NewRegistry()

	// The host-diagnostics store holds the latest snapshot an auv3-probe
	// extension reports (the live view of "what can the appex see about its
	// host?"). It backs the read-only get_host_diagnostics MCP tool and is fed
	// by the LAN receiver below (RAM only — a volatile rig signal).
	diagStore := diagnostics.NewStore()

	srv := mcpserver.New(eng,
		mcpserver.WithUSBAllowWrites(cfg.USBAllowWrites),
		mcpserver.WithAudioTap(audioRegistry),
		mcpserver.WithDiagnostics(diagStore),
		mcpserver.WithMidiControl(midiHub),
		mcpserver.WithAUMAutoImport(cfg.AUMAutoImport),
	)

	// Run the iPad receiver as a SINGLE SEPARATE LAN listener (the loopback-only
	// MCP endpoint above is unreachable from the iPad). One listener carries the
	// AUv3-probe surface (stage parameter-tree dumps for the
	// list/get/import_auv3_probe tools), the AUM-session surface (ferry
	// .aumproj/.aum_midimap files in and out for the aum tools), and the
	// ProbeAudioTap audio-stream WebSocket (live levels for get_audio_tap). None
	// touch hardware. When data lands it notifies connected MCP clients so an
	// agent sees it arrive. Disabled when auv3_receiver_addr is "".
	if cfg.AUv3ReceiverAddr != "" {
		go func() {
			if err := serveLANReceiver(ctx, cfg.AUv3ReceiverAddr, srv, audioRegistry, diagStore, midiHub); err != nil {
				log.Printf("iPad receiver: %v", err)
			}
		}()
		// Announce the receiver on the LAN (Bonjour _mcpmidi._tcp via Avahi) so
		// the iPad app and its AUv3 extensions auto-discover the daemon. A
		// failure must not gate startup: log it with the manual fallback.
		go func() {
			port, err := receiverPort(cfg.AUv3ReceiverAddr)
			if err != nil {
				log.Printf("mdns: %v (iPad discovery needs the manual-host override)", err)
				return
			}
			txt := []string{"version=" + version, "capabilities=audio,midi,sessions,diagnostics"}
			if err := mdns.Announce(ctx, "mcp-midi-controller", port, txt); err != nil {
				log.Printf("%v (iPad discovery needs avahi-publish-service or the manual-host override)", err)
			}
		}()
	}

	// Begin listening for inbound MIDI on every bound endpoint so observed-state
	// and the feedback tools work without an explicit learn_start. This runs in
	// the background: connecting to BLE endpoints can block (or fail) when the
	// hardware is off, and that must never gate the loopback MCP endpoint from
	// coming up. Endpoints that are not reachable now are retried on demand by
	// verify/learn/probe.
	go func() {
		if err := eng.StartInboundForDevices(ctx); err != nil {
			log.Printf("inbound listeners: %v", err)
		}
	}()

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// On signal, give in-flight MCP requests a brief grace period to drain,
	// then close the listener so ListenAndServe returns.
	go func() {
		<-ctx.Done()
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}()

	log.Printf("mcp-midi-controller listening on http://%s (loopback)", cfg.ListenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}

	// Tear down transports/listeners (disconnect BLE, stop pumps) once the
	// server has stopped accepting requests.
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Close(closeCtx); err != nil {
		log.Printf("engine close: %v", err)
	}
}

// serveLANReceiver runs the combined iPad-facing LAN receiver (AUv3 probes +
// AUM sessions) on one listener until ctx is cancelled, then shuts it down
// gracefully. Both surfaces and the shared /healthz are mounted on a single
// mux. The onStaged callbacks notify connected MCP clients new data arrived.
func serveLANReceiver(ctx context.Context, addr string, srv *mcpserver.Server, audioRegistry *audiotap.Registry, diagStore *diagnostics.Store, midiHub *midicontrol.Hub) error {
	probesDir := config.AUv3ProbesDir()
	sessionsDir := config.AUMSessionsDir()
	for _, d := range []string{probesDir, sessionsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", lanhttp.Healthz)
	auv3.Register(mux, probesDir, func(dump device.ProbeDump, res auv3.Result) {
		srv.NotifyAUv3Probe(res.ID, dump.Name, res.Params, res.Writable)
	})
	aumreceiver.Register(mux, sessionsDir,
		func(res aumreceiver.Result) {
			srv.NotifyAUMSession(res.ID, res.Title, res.Version, res.Channels, res.Mappings)
		},
		// A session download is the signal it is about to be loaded into AUM:
		// track it as the current session and (config-gated) auto-import its rig.
		srv.OnAUMSessionDownloaded,
	)
	audiotap.Register(mux, audioRegistry,
		func(name, remote string) { srv.NotifyAudioTap(true, name, remote) },
		func(name, remote string) { srv.NotifyAudioTap(false, name, remote) },
	)
	diagnostics.Register(mux, diagStore,
		func(remote string) { srv.NotifyHostDiagnostics(true, remote) },
		func(remote string) { srv.NotifyHostDiagnostics(false, remote) },
	)
	midicontrol.Register(mux, midiHub, midicontrol.Callbacks{
		OnConnect: func(remote string) {
			srv.NotifyMidiControl(true, remote)
			// Re-import the current session's rig and push the control-surface
			// manifest to the freshly connected brain (config-gated, async).
			srv.OnMidiControlConnected()
		},
		OnDisconnect: func(remote string) { srv.NotifyMidiControl(false, remote) },
		// The brain's switcher row was tapped: follow the session switch
		// (update the current session, re-import, re-push). Async inside.
		OnSessionSwitch: srv.OnBrainSessionSwitch,
	})

	log.Printf("iPad receiver listening on %s (auv3 probes -> %s, aum sessions -> %s, audio tap -> ws /audio-stream, diagnostics -> ws /diagnostics, midi control -> ws /midi-control)", addr, probesDir, sessionsDir)
	return lanhttp.Serve(ctx, addr, mux)
}

func mustTransport(t transport.Transport, err error) transport.Transport {
	if err != nil {
		log.Fatalf("init transport: %v", err)
	}
	return t
}

// receiverPort extracts the LAN receiver's TCP port for the mDNS announcement.
func receiverPort(addr string) (uint16, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("receiver addr %q: %w", addr, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("receiver addr %q: %w", addr, err)
	}
	return uint16(port), nil
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
