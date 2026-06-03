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
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/scene"
)

// Version is reported to MCP clients.
const Version = "0.0.1-scaffold"

// Server wraps the engine and an mcp.Server.
type Server struct {
	eng    *engine.Engine
	mcp    *mcp.Server
	scenes *scene.Store

	// usbAllowWrites is the daemon's master USB write gate (config
	// usb_allow_writes). With it off, USB write tools (set_param, write_pattern,
	// recall_pattern, select_preset) are never registered and a real usb_write
	// is refused; only reads/identify/list/monitor are exposed. See
	// WithUSBAllowWrites and docs/usb-tools.md.
	usbAllowWrites bool

	// drafts holds in-progress device definitions being authored via
	// create_device_definition / add_control, keyed by draft (definition) id,
	// until save_device_definition persists them. Guarded by draftsMu.
	draftsMu sync.Mutex
	drafts   map[string]*device.Definition
}

// Option configures a Server at construction.
type Option func(*Server)

// WithUSBAllowWrites sets the daemon's master USB write gate (config
// usb_allow_writes). Default (no option) is false: read-only over USB.
func WithUSBAllowWrites(allow bool) Option {
	return func(s *Server) { s.usbAllowWrites = allow }
}

// New builds the MCP server, registers global tools, and generates a tool for
// each currently bound device.
func New(eng *engine.Engine, opts ...Option) *Server {
	s := &Server{
		eng:    eng,
		mcp:    mcp.NewServer(&mcp.Implementation{Name: "mcp-midi-controller", Version: Version}, nil),
		scenes: scene.NewStore(config.ScenesDir()),
		drafts: map[string]*device.Definition{},
	}
	for _, o := range opts {
		o(s)
	}
	s.registerGlobalTools()
	s.registerWIDITools()
	for _, b := range eng.Bindings() {
		s.addToolsForBinding(b)
	}
	// Stream inbound MIDI (reverse-mapped) to clients as log notifications so an
	// agent can watch the rig react in real time (hand-tweaks, echoes).
	eng.SetInboundHook(s.notifyInbound)
	return s
}

// notifyInbound broadcasts a decoded inbound event (and any controls it
// reverse-mapped to) to every connected session as an MCP log notification.
// Clients receive it only after setting a logging level (per the MCP spec).
func (s *Server) notifyInbound(in engine.InboundEvent, obs []engine.Observation) {
	params := &mcp.LoggingMessageParams{
		Level:  "info",
		Logger: "inbound",
		Data: map[string]any{
			"transport": in.Transport,
			"endpoint":  in.Endpoint,
			"kind":      in.Kind,
			"channel":   in.Channel,
			"number":    in.Number,
			"value":     in.Value,
			"observed":  obs,
		},
	}
	ctx := context.Background()
	for sess := range s.mcp.Sessions() {
		_ = sess.Log(ctx, params)
	}
}

// NotifyAUv3Probe broadcasts to every connected session that a fresh AUv3
// parameter-tree dump was staged by the receiver, so an agent watching the
// rig sees newly probed plugins arrive without polling list_auv3_probes. Like
// notifyInbound, clients receive it only after setting a logging level.
func (s *Server) NotifyAUv3Probe(id, name string, params, writable int) {
	p := &mcp.LoggingMessageParams{
		Level:  "info",
		Logger: "auv3-probe",
		Data: map[string]any{
			"id":       id,
			"name":     name,
			"params":   params,
			"writable": writable,
			"hint":     "inspect with get_auv3_probe, scaffold a definition with import_auv3_probe",
		},
	}
	ctx := context.Background()
	for sess := range s.mcp.Sessions() {
		_ = sess.Log(ctx, p)
	}
}

// NotifyAUMSession broadcasts to every connected session that an AUM session
// file was staged by the aum receiver (uploaded from the iPad), so an agent
// watching the rig sees newly captured sessions arrive without polling
// list_aum_sessions. Like notifyInbound, clients receive it only after setting
// a logging level.
func (s *Server) NotifyAUMSession(id, title string, version, channels, mappings int) {
	p := &mcp.LoggingMessageParams{
		Level:  "info",
		Logger: "aum-session",
		Data: map[string]any{
			"id":       id,
			"title":    title,
			"version":  version,
			"channels": channels,
			"mappings": mappings,
			"hint":     "inspect with get_aum_session, compare with diff_aum_session, propose bindings with import_aum_session",
		},
	}
	ctx := context.Background()
	for sess := range s.mcp.Sessions() {
		_ = sess.Log(ctx, p)
	}
}

// Handler returns the streamable-HTTP handler to mount on a loopback listener.
func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
}

// addToolsForBinding generates the right MCP tool(s) for a binding: the USB
// editor/readback family for a USB binding, otherwise the control_<logical>
// tool. Both paths emit tools/list_changed.
func (s *Server) addToolsForBinding(b engine.Binding) {
	if s.eng.IsUSBBinding(b.Logical) {
		s.AddUSBDeviceTool(b)
		return
	}
	s.AddDeviceTool(b)
}

// removeToolsForBinding removes whichever tool(s) a binding generated. The kind
// must be resolved before the binding is dropped from the engine.
func (s *Server) removeToolsForBinding(logical string, wasUSB bool) {
	if wasUSB {
		s.RemoveUSBDeviceTool(logical)
		return
	}
	s.RemoveDeviceTool(logical)
}

// usbWritesAllowed reports whether write tools may be exposed for a USB binding:
// both the daemon's master gate (usb_allow_writes) and the binding's own
// writable opt-in must be set. This is the two-key safety model from
// docs/usb-tools.md — writes change persistent/live device state.
func (s *Server) usbWritesAllowed(b engine.Binding) bool {
	return s.usbAllowWrites && b.Writable
}

// AddDeviceTool generates and registers control_<logical> for a binding. Adding
// the tool also emits notifications/tools/list_changed to connected clients.
func (s *Server) AddDeviceTool(b engine.Binding) {
	def, ok := s.eng.Registry().Get(b.DeviceID)
	if !ok {
		return
	}
	// USB bindings expose the device's editor/readback surface, not the
	// fire-and-forget control surface, so they do not get a control_<logical>
	// tool. The generic usb_* tools (and the per-binding semantic USB tools)
	// cover them instead.
	if s.eng.IsUSBBinding(b.Logical) {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name:        "control_" + b.Logical,
		Description: fmt.Sprintf("Set one or more controls on %q (%s). Use describe_device for ranges/enums.", b.Logical, def.Name),
		InputSchema: controlToolSchema(def),
	}, s.handleControl(b.Logical))
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

// structResult returns a successful result carrying both a human-readable text
// rendering and a machine-readable structuredContent payload (which must
// marshal to a JSON object, per the MCP spec). The rig-reasoning read tools use
// this so a web client / agent can consume JSON without re-parsing the text.
func structResult(msg string, structured any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: msg}},
		StructuredContent: structured,
	}
}
