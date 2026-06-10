// Package mcpserver is the thin MCP layer on top of the engine. It exposes the
// global tools and generates one control_<logical> tool per bound device, using
// the official github.com/modelcontextprotocol/go-sdk over streamable-HTTP.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/diagnostics"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
	"github.com/teemow/mcp-midi-controller/internal/scene"
	"github.com/teemow/mcp-midi-controller/internal/webui"
)

// Version is reported to MCP clients.
const Version = "0.0.1-scaffold"

// Server wraps the engine and an mcp.Server.
type Server struct {
	eng    *engine.Engine
	mcp    *mcp.Server
	scenes *scene.Store

	// audio is the in-memory ProbeAudioTap registry behind the get_audio_tap
	// tool: one named per-tap store per concurrently-connected insert. nil when
	// no audio receiver is wired (the tool is then not registered).
	audio *audiotap.Registry

	// diag is the in-memory host-diagnostics state behind the
	// get_host_diagnostics tool (the live view of "what can the appex see about
	// its host?"). nil when no diagnostics receiver is wired (the tool is then
	// not registered).
	diag *diagnostics.Store

	// midi is the ProbeMidiBrain control channel behind the play_notes /
	// send_midi / set_transport tools (the agent's "hands"). nil when no brain
	// receiver is wired (the tools then have no LAN target and fall back to a
	// hardware transport only).
	midi *midicontrol.Hub

	// aumAutoImport enables the automatic session-rig import (config
	// aum_auto_import): OnAUMSessionDownloaded / OnMidiControlConnected re-run
	// import_aum_session for the current session and push the control-surface
	// manifest to the brain. See aum_autoimport.go.
	aumAutoImport bool

	// aumImportMu serializes session-rig imports: the auto-import callbacks
	// (session download, brain connect) can race a tool-driven
	// import_aum_session, and the replace-then-create lifecycle must not
	// interleave.
	aumImportMu sync.Mutex

	// usbAllowWrites is the daemon's master USB write gate (config
	// usb_allow_writes). With it off, USB write tools (set_param, write_pattern,
	// recall_pattern, select_preset) are never registered and a real usb_write
	// is refused; only reads/identify/list/monitor are exposed. See
	// WithUSBAllowWrites and docs/usb-tools.md.
	usbAllowWrites bool

	// drafts holds in-progress device types being authored via
	// create_device_type / add_control, keyed by draft (device-type) id,
	// until save_device_type persists them. Guarded by draftsMu.
	draftsMu sync.Mutex
	drafts   map[string]*device.DeviceType

	// audioSnaps holds labeled audio snapshots for A/B comparison via
	// capture_audio_snapshot / compare_audio, and lastProbe is the most recent
	// probe_sound snapshot so probe_sound can auto-report a delta vs the
	// previous probe. Both guarded by audioSnapsMu. Snapshots are volatile rig
	// signal and live only in RAM (like the audio store itself).
	audioSnapsMu sync.Mutex
	audioSnaps   map[string]audiotap.Snapshot
	lastProbe    *audiotap.Snapshot

	// probeMu serializes probe_sound for the whole excite→settle→capture cycle
	// so two probes never overlap on the shared audio tap. This is what makes
	// each probe analyse a clean, isolated segment (and replaces the harness's
	// old manual window-clear sleeps).
	probeMu sync.Mutex
}

// Option configures a Server at construction.
type Option func(*Server)

// WithUSBAllowWrites sets the daemon's master USB write gate (config
// usb_allow_writes). Default (no option) is false: read-only over USB.
func WithUSBAllowWrites(allow bool) Option {
	return func(s *Server) { s.usbAllowWrites = allow }
}

// WithAudioTap attaches the ProbeAudioTap registry so the read-only
// get_audio_tap tool is registered. Without it the tool is omitted.
func WithAudioTap(reg *audiotap.Registry) Option {
	return func(s *Server) { s.audio = reg }
}

// resolveTap picks the per-tap store an audio tool should read: the tap named
// by the caller (matched by registry identity or format Source label) when name
// is non-empty, otherwise the most-recently-active tap. ok is false when no tap
// matches (or none has ever connected). It is safe to call only when s.audio is
// non-nil (the tools are gated on that).
func (s *Server) resolveTap(name string) (*audiotap.Store, bool) {
	if strings.TrimSpace(name) != "" {
		return s.audio.Get(name)
	}
	return s.audio.Active()
}

// WithDiagnostics attaches the host-diagnostics state store so the read-only
// get_host_diagnostics tool is registered. Without it the tool is omitted.
func WithDiagnostics(store *diagnostics.Store) Option {
	return func(s *Server) { s.diag = store }
}

// WithMidiControl attaches the ProbeMidiBrain control hub so the play_notes /
// send_midi / set_transport tools target the LAN brain channel as their primary
// path. Without it those tools still register but can only reach a hardware
// transport (BLE) via an explicit endpoint.
func WithMidiControl(hub *midicontrol.Hub) Option {
	return func(s *Server) { s.midi = hub }
}

// New builds the MCP server, registers global tools, and generates a tool for
// each currently bound device.
func New(eng *engine.Engine, opts ...Option) *Server {
	s := &Server{
		eng:        eng,
		mcp:        mcp.NewServer(&mcp.Implementation{Name: "mcp-midi-controller", Version: Version}, nil),
		scenes:     scene.NewStore(config.ScenesDir()),
		drafts:     map[string]*device.DeviceType{},
		audioSnaps: map[string]audiotap.Snapshot{},
	}
	for _, o := range opts {
		o(s)
	}
	s.registerGlobalTools()
	s.registerWIDITools()
	for _, d := range eng.Devices() {
		s.addToolsForDevice(d)
	}
	// Stream inbound MIDI (reverse-mapped) to clients as log notifications so an
	// agent can watch the rig react in real time (hand-tweaks, echoes).
	eng.SetInboundHook(s.notifyInbound)
	return s
}

// broadcast sends one info-level MCP log notification (under logger) to every
// connected session. Clients receive it only after setting a logging level (per
// the MCP spec). It is the shared body of every notify* method below.
func (s *Server) broadcast(logger string, data map[string]any) {
	p := &mcp.LoggingMessageParams{Level: "info", Logger: logger, Data: data}
	ctx := context.Background()
	for sess := range s.mcp.Sessions() {
		_ = sess.Log(ctx, p)
	}
}

// broadcastConnState broadcasts a connected/disconnected transition for a LAN
// channel, picking connHint or goneHint for the "hint" field. It is the shared
// body of the symmetric NotifyAudioTap / NotifyHostDiagnostics / NotifyMidiControl
// notifiers.
func (s *Server) broadcastConnState(logger string, connected bool, remote, connHint, goneHint string) {
	state, hint := "connected", connHint
	if !connected {
		state, hint = "disconnected", goneHint
	}
	s.broadcast(logger, map[string]any{"state": state, "remote": remote, "hint": hint})
}

// notifyInbound broadcasts a decoded inbound event (and any controls it
// reverse-mapped to) to every connected session as an MCP log notification.
func (s *Server) notifyInbound(in engine.InboundEvent, obs []engine.Observation) {
	s.broadcast("inbound", map[string]any{
		"transport": in.Transport,
		"endpoint":  in.Endpoint,
		"kind":      in.Kind,
		"channel":   in.Channel,
		"number":    in.Number,
		"value":     in.Value,
		"observed":  obs,
	})
}

// NotifyAUv3Probe broadcasts to every connected session that a fresh AUv3
// parameter-tree dump was staged by the receiver, so an agent watching the
// rig sees newly probed plugins arrive without polling list_auv3_probes. Like
// notifyInbound, clients receive it only after setting a logging level.
func (s *Server) NotifyAUv3Probe(id, name string, params, writable int) {
	s.broadcast("auv3-probe", map[string]any{
		"id":       id,
		"name":     name,
		"params":   params,
		"writable": writable,
		"hint":     "inspect with get_auv3_probe, scaffold a device type with import_auv3_probe",
	})
}

// NotifyAUMSession broadcasts to every connected session that an AUM session
// file was staged by the aum receiver (uploaded from the iPad), so an agent
// watching the rig sees newly captured sessions arrive without polling
// list_aum_sessions. Like notifyInbound, clients receive it only after setting
// a logging level.
func (s *Server) NotifyAUMSession(id, title string, version, channels, mappings int) {
	s.broadcast("aum-session", map[string]any{
		"id":       id,
		"title":    title,
		"version":  version,
		"channels": channels,
		"mappings": mappings,
		"hint":     "inspect with get_aum_session, compare with diff_aum_session, import devices with import_aum_session",
	})
}

// NotifyAudioTap broadcasts to every connected session that a ProbeAudioTap
// audio stream connected or disconnected, so an agent watching the rig knows it
// has (or lost) "ears" without polling get_audio_tap. Like notifyInbound,
// clients receive it only after setting a logging level. Per-frame levels are
// intentionally NOT broadcast (they arrive ~10 Hz) — poll get_audio_tap for
// live levels instead.
func (s *Server) NotifyAudioTap(connected bool, name, remote string) {
	state, hint := "connected", "read live levels + waveform with get_audio_tap (pass name to pick a tap)"
	if !connected {
		state, hint = "disconnected", "no audio tap is streaming"
	}
	s.broadcast("audio-tap", map[string]any{
		"state":  state,
		"name":   name,
		"remote": remote,
		"hint":   hint,
	})
}

// NotifyHostDiagnostics broadcasts to every connected session that an
// auv3-probe extension started or stopped reporting host diagnostics, so an
// agent knows whether it has a live view of the plugin's host surface without
// polling get_host_diagnostics. Like NotifyAudioTap, clients receive it only
// after setting a logging level. Per-tick snapshots are intentionally NOT
// broadcast (they arrive ~1 Hz) — poll get_host_diagnostics for the latest.
func (s *Server) NotifyHostDiagnostics(connected bool, remote string) {
	s.broadcastConnState("host-diagnostics", connected, remote,
		"read the host surface with get_host_diagnostics",
		"no auv3-probe extension is reporting diagnostics")
}

// NotifyMidiControl broadcasts to every connected session that the
// ProbeMidiBrain control channel connected or disconnected, so an agent knows
// it has (or lost) "hands" on the rig without polling. The symmetric
// counterpart of NotifyAudioTap. Like notifyInbound, clients receive it only
// after setting a logging level.
func (s *Server) NotifyMidiControl(connected bool, remote string) {
	s.broadcastConnState("midi-control", connected, remote,
		"drive the rig with play_notes / send_midi / set_transport",
		"no brain channel; play_notes/send_midi need a hardware endpoint")
}

// Handler returns the HTTP handler to mount on a loopback listener. It muxes
// two surfaces on one listener: "/app/" serves the embedded "signalwave" SPA
// (a real in-browser MCP client), and "/" stays the MCP streamable-HTTP handler
// so existing MCP clients (e.g. Cursor) are unaffected.
func (s *Server) Handler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
	mux := http.NewServeMux()
	mux.Handle(webui.MountPath, webui.Handler())
	mux.Handle("/", mcpHandler)
	return mux
}

// addToolsForDevice generates every MCP tool a device's surfaces warrant:
// control_<logical> when it has a control surface, and the USB editor/readback
// family when it has a USB surface. A device that carries both gets both. Each
// AddTool emits tools/list_changed.
func (s *Server) addToolsForDevice(d engine.Device) {
	if d.HasControl() {
		s.AddDeviceTool(d)
	}
	if d.HasUSB() {
		s.AddUSBDeviceTool(d)
	}
}

// refreshToolsForDevice tears down and re-creates a device's tools so a re-bind
// (e.g. adding a USB surface to an existing control device) lands the current
// surface set without duplicate registrations. The device must already be
// present in the engine (RemoveUSBDeviceTool resolves it).
func (s *Server) refreshToolsForDevice(d engine.Device) {
	s.RemoveDeviceTool(d.Name)
	s.RemoveUSBDeviceTool(d.Name)
	s.addToolsForDevice(d)
}

// removeToolsForDevice removes every tool a device could have generated
// (control_<logical> and the USB family). It must be called while the device
// is still present in the engine, since RemoveUSBDeviceTool resolves the USB
// tool names from the device's definition. Removing a tool that was never
// registered is a no-op.
func (s *Server) removeToolsForDevice(logical string) {
	s.RemoveDeviceTool(logical)
	s.RemoveUSBDeviceTool(logical)
}

// usbWritesAllowed reports whether write tools may be exposed for a device's
// USB surface: both the daemon's master gate (usb_allow_writes) and the
// surface's own writable opt-in must be set. This is the two-key safety model
// from docs/usb-tools.md — writes change persistent/live device state.
func (s *Server) usbWritesAllowed(d engine.Device) bool {
	return s.usbAllowWrites && d.USBWritable()
}

// AddDeviceTool generates and registers control_<logical> for a device's
// control surface. Adding the tool also emits
// notifications/tools/list_changed to connected clients. It is a no-op for a
// device with no control surface (USB-only).
func (s *Server) AddDeviceTool(d engine.Device) {
	def, ok := s.eng.Registry().Get(d.DeviceID)
	if !ok {
		return
	}
	if !d.HasControl() {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name:        "control_" + d.Name,
		Description: fmt.Sprintf("Set one or more controls on %q (%s). Use describe_device for ranges/enums.", d.Name, def.Name),
		InputSchema: controlToolSchema(def),
	}, s.handleControl(d.Name))
}

// RemoveDeviceTool removes control_<logical> (emits list_changed).
func (s *Server) RemoveDeviceTool(logical string) {
	s.mcp.RemoveTools("control_" + logical)
}

// handleControl validates control names + values against the device definition
// in-handler and applies them via the engine. Failures are returned as
// CallToolResult{IsError:true} with an RFC-6901 JSON-pointer path (SEP-1303) so
// the model can self-correct, rather than as protocol errors.
func (s *Server) handleControl(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Settings []struct {
				Control string `json:"control"`
				Value   any    `json:"value"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		var applied int
		for i, set := range args.Settings {
			if err := s.eng.SetControl(ctx, logical, set.Control, set.Value); err != nil {
				// Validation failures carry an RFC-6901 path relative to the
				// control invocation; prefix it with the batch index so the
				// model gets e.g. /settings/2/value (SEP-1303). Transport and
				// other errors fall back to the plain batch-index pointer.
				var ve *device.ValidationError
				if errors.As(err, &ve) {
					return textResult(fmt.Sprintf("/settings/%d%s: %s", i, ve.Pointer, ve.Msg), true), nil
				}
				return textResult(fmt.Sprintf("/settings/%d: %v", i, err), true), nil
			}
			applied++
		}
		return textResult(fmt.Sprintf("applied %d setting(s) to %s", applied, logical), false), nil
	}
}

func textResult(msg string, isErr bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: isErr,
	}
}

// structuredJSONLabel prefixes the serialized-JSON text block that structResult
// inlines for text-only clients, so the model can tell the machine-readable
// payload apart from the human summary above it.
const structuredJSONLabel = "\n\nstructured (JSON):\n"

// structResult returns a successful result carrying both a human-readable text
// rendering and a machine-readable structuredContent payload (which must
// marshal to a JSON value, per the MCP spec). The rig-reasoning read tools use
// this so a web client / agent can consume JSON without re-parsing the text.
//
// It ALSO inlines a compact serialization of structured as a second text
// content block. This is the MCP spec's backwards-compatibility guidance ("a
// tool that returns structured content SHOULD also return the serialized JSON
// in a TextContent block") and, more importantly, a mitigation for clients —
// notably Cursor, and also Claude Code / Windsurf — that only surface the text
// content to the model and ignore structuredContent entirely. Without the
// inlined JSON those agents would see only the short human summary and lose the
// structured rig data they need to reason (see SEP-1624 and the Cursor bug
// report "Cursor ignores structuredContent from MCP result"). Clients that do
// read structuredContent simply prefer it and the duplicate text is harmless.
//
// Use structResultNoEmbed when the structured payload is large or binary (e.g.
// base64 PCM) and the human text already names the salient fields.
func structResult(msg string, structured any) *mcp.CallToolResult {
	res := structResultNoEmbed(msg, structured)
	if structured == nil {
		return res
	}
	raw, err := json.Marshal(structured)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return res
	}
	res.Content = append(res.Content, &mcp.TextContent{Text: structuredJSONLabel + string(raw)})
	return res
}

// structResultNoEmbed is like structResult but does NOT inline the serialized
// structuredContent into the text. Use it when the structured payload is large
// or binary (e.g. base64 PCM) and the human text already summarizes the salient
// fields, so the model's context is not bloated with data it cannot use anyway.
func structResultNoEmbed(msg string, structured any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: msg}},
		StructuredContent: structured,
	}
}
