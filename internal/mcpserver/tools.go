package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// registerGlobalTools registers the device-independent tools. Handlers that are
// not yet implemented return an informative IsError result so the surface is
// discoverable while the engine is filled in.
func (s *Server) registerGlobalTools() {
	objSchema := json.RawMessage(`{"type":"object"}`)
	deviceArgSchema := json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"}},"required":["device"]}`)

	// Implemented: a real read-only view of the bound rig.
	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_devices",
		Description: "List the bound logical devices and their definitions.",
		InputSchema: objSchema,
	}, s.handleListDevices)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "describe_device",
		Description: "Describe a device's controls, types and value ranges/enums.",
		InputSchema: deviceArgSchema,
	}, s.handleDescribeDevice)

	// Endpoint discovery, pairing and bindings (wired to the engine).
	s.mcp.AddTool(&mcp.Tool{
		Name:        "discover_endpoints",
		Description: "Scan for reachable transport endpoints (BLE peripherals, OSC hosts, USB-MIDI ports, USB-HID VID:PIDs).",
		InputSchema: objSchema,
	}, s.handleDiscoverEndpoints)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "pair_endpoint",
		Description: "Pair/bond with a BLE endpoint and open its data path. Pass transport to target a non-default backend.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"endpoint":{"type":"string"},"transport":{"type":"string"}},"required":["endpoint"]}`),
	}, s.handlePairEndpoint)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "bind_device",
		Description: "Bind an endpoint to a device definition. Default (blemidi/osc) generates a control_<logical> tool. Set transport to usbmidi|usbhid for a device with a usb profile to bind its editor/readback surface instead (generates the USB tool family; channel is ignored, optional writable opts the binding in to gated write tools).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"logical":{"type":"string"},"endpoint":{"type":"string"},"channel":{"type":"integer"},"device":{"type":"string"},"transport":{"type":"string"},"writable":{"type":"boolean"}},"required":["logical","endpoint","device"]}`),
	}, s.handleBindDevice)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "unbind_device",
		Description: "Remove a logical device binding (removes its control_<logical> tool).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"logical":{"type":"string"}},"required":["logical"]}`),
	}, s.handleUnbindDevice)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "send_raw",
		Description: "Escape hatch: send raw MIDI bytes (e.g. [176,17,64]) or an OSC address+args to an endpoint (untracked).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"transport":{"type":"string"},"endpoint":{"type":"string"},"midi":{"type":"array","items":{"type":"integer"}},"address":{"type":"string"},"args":{"type":"array"}},"required":["endpoint"]}`),
	}, s.handleSendRaw)

	// Feedback / inbound (Phase D): observed-state, verification and MIDI-learn.
	s.mcp.AddTool(&mcp.Tool{
		Name:        "read_state",
		Description: "Read desired-state (last values sent) and observed-state (reverse-mapped inbound MIDI) for a device or the whole rig.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"}}}`),
	}, s.handleReadState)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "verify_control",
		Description: "Set a control then wait for an inbound echo, classifying the result confirmed | no_feedback | mismatch.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"control":{"type":"string"},"value":{},"timeout_ms":{"type":"integer"}},"required":["device","control","value"]}`),
	}, s.handleVerifyControl)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "probe_feedback",
		Description: "Sweep a device's controls (or the whole rig) and record which transport sources echo each control — the empirical feedback capability matrix.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"timeout_ms":{"type":"integer"}}}`),
	}, s.handleProbeFeedback)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "learn_start",
		Description: "Start MIDI-learn: listen on an endpoint's inbound channel (or all bound endpoints) and mark now as the capture cut-off.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"endpoint":{"type":"string"},"transport":{"type":"string"}}}`),
	}, s.handleLearnStart)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "learn_capture",
		Description: "Return the most recently captured inbound CC/program-change/note since learn_start.",
		InputSchema: objSchema,
	}, s.handleLearnCapture)

	// Rig-reasoning read tools (list_bindings / list_definitions /
	// get_definition) — the machine-readable companions to list_devices /
	// describe_device — are wired in registerRigTools.
	s.registerRigTools()
	// Generic USB editor/readback tools (usb_identify/read/dump/write +
	// usb_probe/usb_monitor) are wired in registerUSBTools.
	s.registerUSBTools()
	// Scene tools: list + compile/push to the footswitch, and the live
	// save/recall path, are all wired to the engine in registerSceneTools.
	s.registerSceneTools()
	// Device authoring tools (create/add_control/save) are wired in
	// registerAuthoringTools.
	s.registerAuthoringTools()
	// AUM session tools (list/get/diff/import/author/edit + export_aum_midimap)
	// over the internal/aum library are wired in registerAUMTools.
	s.registerAUMTools()
}

func (s *Server) handleListDevices(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bindings := s.eng.Bindings()
	if len(bindings) == 0 {
		return structResult("no devices bound yet; use discover_endpoints + bind_device", map[string]any{"devices": []bindingView{}}), nil
	}
	views := make([]bindingView, 0, len(bindings))
	var b strings.Builder
	for _, bind := range bindings {
		name := bind.DeviceID
		v := bindingView{
			Logical:   bind.Logical,
			Device:    bind.DeviceID,
			Endpoint:  bind.Endpoint,
			Channel:   bind.Channel,
			Transport: bind.Transport,
			USB:       s.eng.IsUSBBinding(bind.Logical),
			Writable:  bind.Writable,
		}
		if def, ok := s.eng.Registry().Get(bind.DeviceID); ok {
			name = def.Name
			v.DeviceName = def.Name
		}
		views = append(views, v)
		fmt.Fprintf(&b, "%s\t(device=%s, endpoint=%q, channel=%d)\n", bind.Logical, name, bind.Endpoint, bind.Channel)
	}
	return structResult(b.String(), map[string]any{"devices": views}), nil
}

func (s *Server) handleDescribeDevice(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	// Accept either a logical device name or a definition id.
	defID := args.Device
	for _, bind := range s.eng.Bindings() {
		if bind.Logical == args.Device {
			defID = bind.DeviceID
			break
		}
	}
	def, ok := s.eng.Registry().Get(defID)
	if !ok {
		return textResult(fmt.Sprintf("unknown device %q", args.Device), true), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, transport=%s)\n", def.Name, def.ID, def.Transport)
	names := def.ControlNames()
	sort.Strings(names)
	for _, n := range names {
		c, _ := def.Control(n)
		fmt.Fprintf(&b, "  - %s [%s]", c.Name, c.Type)
		if vs := describeValueSpec(c); vs != "" {
			fmt.Fprintf(&b, " %s", vs)
		}
		if c.Description != "" {
			fmt.Fprintf(&b, " — %s", c.Description)
		}
		b.WriteByte('\n')
	}
	return structResult(b.String(), newDefinitionView(def)), nil
}

// describeValueSpec renders a control's accepted-value domain (range/enum/unit)
// for describe_device, e.g. "0..127 (dB)" or "enum {off=0, on=127}".
func describeValueSpec(c *device.Control) string {
	spec := &c.Value
	var s string
	switch spec.Type {
	case device.ValueEnum:
		s = "enum " + enumLabelWire(spec.Values)
	case device.ValueFloat:
		s = "float " + boundsText(spec, false)
	case device.ValueInt:
		s = "int " + boundsText(spec, true)
	case device.ValueString:
		s = "string"
	case device.ValueRange, "":
		s = boundsText(spec, true)
	default:
		s = string(spec.Type)
	}
	s = strings.TrimSpace(s)
	if spec.Unit != "" {
		s += " (" + spec.Unit + ")"
	}
	if c.Parametric {
		s = "parametric {number, value:" + strings.TrimSpace(s) + "}"
	}
	return s
}

// boundsText formats the [min, max] window, defaulting to the 0..127 CC domain
// for range/int controls that omit bounds.
func boundsText(spec *device.ValueSpec, defaultCC bool) string {
	lo, hi := "", ""
	if spec.Min != nil {
		lo = formatBound(*spec.Min)
	} else if defaultCC {
		lo = "0"
	}
	if spec.Max != nil {
		hi = formatBound(*spec.Max)
	} else if defaultCC {
		hi = "127"
	}
	switch {
	case lo != "" && hi != "":
		return lo + ".." + hi
	case lo != "":
		return ">=" + lo
	case hi != "":
		return "<=" + hi
	default:
		return ""
	}
}

func formatBound(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

func (s *Server) handleDiscoverEndpoints(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	eps, err := s.eng.DiscoverEndpoints(ctx)
	if err != nil {
		return textResult("discover failed: "+err.Error(), true), nil
	}
	if len(eps) == 0 {
		return textResult("no endpoints found", false), nil
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].ID < eps[j].ID })
	var b strings.Builder
	for _, ep := range eps {
		fmt.Fprintf(&b, "%s\t%q\t(transport=%s, paired=%t, connected=%t)\n", ep.ID, ep.Name, ep.Transport, ep.Paired, ep.Connected)
	}
	return textResult(b.String(), false), nil
}

func (s *Server) handlePairEndpoint(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Endpoint  string `json:"endpoint"`
		Transport string `json:"transport"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Endpoint == "" {
		return textResult("/endpoint: required", true), nil
	}
	if err := s.eng.PairEndpoint(ctx, args.Transport, args.Endpoint); err != nil {
		return textResult("pair failed: "+err.Error(), true), nil
	}
	return textResult(fmt.Sprintf("paired and connected %s", args.Endpoint), false), nil
}

func (s *Server) handleBindDevice(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Logical   string `json:"logical"`
		Endpoint  string `json:"endpoint"`
		Channel   int    `json:"channel"`
		Device    string `json:"device"`
		Transport string `json:"transport"`
		Writable  bool   `json:"writable"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	// A MIDI channel is 0..15 on the wire. Reject out-of-range values so a
	// typo (e.g. the human 1..16 form) does not get masked into a wrong channel
	// when rendered (status |= channel & 0x0F).
	if args.Channel < 0 || args.Channel > 15 {
		return textResult(fmt.Sprintf("/channel: %d out of range (must be 0..15; note the wire form is 0-based)", args.Channel), true), nil
	}
	b := engine.Binding{
		Logical:   args.Logical,
		Endpoint:  args.Endpoint,
		Channel:   args.Channel,
		DeviceID:  args.Device,
		Transport: args.Transport,
		Writable:  args.Writable,
	}
	if err := s.eng.Bind(b); err != nil {
		return textResult(err.Error(), true), nil
	}
	s.addToolsForBinding(b)
	persistNote := ""
	if err := s.persistBindings(); err != nil {
		persistNote = fmt.Sprintf(" (warning: could not persist bindings: %v)", err)
	}
	if s.eng.IsUSBBinding(args.Logical) {
		write := "read-only"
		if s.usbWritesAllowed(b) {
			write = "writable"
		}
		return textResult(fmt.Sprintf("bound %s -> %s over %s on %q (%s; USB tool family generated)%s", args.Logical, args.Device, args.Transport, args.Endpoint, write, persistNote), false), nil
	}
	return textResult(fmt.Sprintf("bound %s -> %s on %q channel %d (tool control_%s)%s", args.Logical, args.Device, args.Endpoint, args.Channel, args.Logical, persistNote), false), nil
}

func (s *Server) handleUnbindDevice(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Logical string `json:"logical"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	// Resolve the binding kind before dropping it, so we remove the matching
	// tool family.
	wasUSB := s.eng.IsUSBBinding(args.Logical)
	s.eng.Unbind(args.Logical)
	s.removeToolsForBinding(args.Logical, wasUSB)
	if err := s.persistBindings(); err != nil {
		return textResult(fmt.Sprintf("unbound %s (warning: could not persist bindings: %v)", args.Logical, err), false), nil
	}
	return textResult(fmt.Sprintf("unbound %s", args.Logical), false), nil
}

func (s *Server) handleSendRaw(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Transport string `json:"transport"`
		Endpoint  string `json:"endpoint"`
		MIDI      []int  `json:"midi"`
		Address   string `json:"address"`
		Args      []any  `json:"args"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Endpoint == "" {
		return textResult("/endpoint: required", true), nil
	}

	var ev transport.Event
	switch {
	case len(args.MIDI) > 0:
		data := make([]byte, len(args.MIDI))
		for i, v := range args.MIDI {
			if v < 0 || v > 255 {
				return textResult(fmt.Sprintf("/midi/%d: byte must be in [0, 255]", i), true), nil
			}
			data[i] = byte(v)
		}
		ev = transport.Event{Kind: transport.MIDIEvent, Data: data}
	case args.Address != "":
		ev = transport.Event{Kind: transport.OSCEvent, OSCAddr: args.Address, OSCArgs: args.Args}
	default:
		return textResult("provide either midi (raw bytes) or address (OSC)", true), nil
	}

	if err := s.eng.SendRaw(ctx, args.Transport, args.Endpoint, ev); err != nil {
		return textResult("send_raw failed: "+err.Error(), true), nil
	}
	return textResult("sent", false), nil
}

func (s *Server) handleReadState(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}

	logicals := s.stateLogicals(args.Device)
	if args.Device != "" && len(logicals) == 0 {
		return textResult(fmt.Sprintf("unknown logical device %q", args.Device), true), nil
	}
	out := map[string]any{}
	for _, l := range logicals {
		out[l] = map[string]any{
			"desired":  s.eng.State().Device(l),
			"observed": s.eng.Observed().Device(l),
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return textResult("encode state: "+err.Error(), true), nil
	}
	return structResult(string(b), out), nil
}

// stateLogicals returns the logical device names to report on: the requested
// one (resolved from a logical name) or every bound device.
func (s *Server) stateLogicals(device string) []string {
	if device != "" {
		for _, b := range s.eng.Bindings() {
			if b.Logical == device {
				return []string{device}
			}
		}
		return nil
	}
	var out []string
	for _, b := range s.eng.Bindings() {
		out = append(out, b.Logical)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleVerifyControl(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device    string `json:"device"`
		Control   string `json:"control"`
		Value     any    `json:"value"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" || args.Control == "" {
		return textResult("/device and /control are required", true), nil
	}
	res, err := s.eng.VerifyControl(ctx, args.Device, args.Control, args.Value, time.Duration(args.TimeoutMS)*time.Millisecond)
	if err != nil {
		var ve *device.ValidationError
		if errors.As(err, &ve) {
			return textResult(ve.Pointer+": "+ve.Msg, true), nil
		}
		return textResult("verify_control failed: "+err.Error(), true), nil
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return textResult(string(b), false), nil
}

func (s *Server) handleProbeFeedback(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device    string `json:"device"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	results, err := s.eng.ProbeFeedback(ctx, args.Device, time.Duration(args.TimeoutMS)*time.Millisecond)
	if err != nil {
		return textResult("probe_feedback failed: "+err.Error(), true), nil
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	return textResult(string(b), false), nil
}

func (s *Server) handleLearnStart(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Endpoint  string `json:"endpoint"`
		Transport string `json:"transport"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	if err := s.eng.LearnStart(ctx, args.Transport, args.Endpoint); err != nil {
		return textResult("learn_start failed: "+err.Error(), true), nil
	}
	target := args.Endpoint
	if target == "" {
		target = "all bound endpoints"
	}
	return textResult(fmt.Sprintf("learning on %s; move a control then call learn_capture", target), false), nil
}

func (s *Server) handleLearnCapture(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	captured, ok := s.eng.LearnCapture()
	if !ok {
		return textResult("nothing captured yet; move a control on the hardware then retry", false), nil
	}
	b, _ := json.MarshalIndent(captured, "", "  ")
	return textResult(string(b), false), nil
}

// persistBindings writes the current bindings to the rig-as-code bindings file.
func (s *Server) persistBindings() error {
	return engine.SaveBindingsFile(config.BindingsPath(), s.eng.Bindings())
}

// controlToolSchema builds the input schema for a control_<logical> tool: a
// batch of {control, value} settings. Each setting's items schema is a oneOf of
// per-control objects so the value field is bound to that control's own value
// schema (ranges/enums/parametric shape) derived from its ValueSpec. This is
// guidance for the model only — the engine's in-handler device.Resolve
// validation remains the authoritative safety net (a client that ignores the
// schema is still validated server-side).
func controlToolSchema(def *device.Definition) json.RawMessage {
	oneOf := make([]any, 0, len(def.Controls))
	for i := range def.Controls {
		c := &def.Controls[i]
		props := map[string]any{
			"control": map[string]any{"const": c.Name},
			"value":   valueSchemaNode(c),
		}
		item := map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []any{"control", "value"},
		}
		if c.Description != "" {
			item["description"] = c.Description
		}
		oneOf = append(oneOf, item)
	}

	// items binds each control to its own value schema via oneOf; fall back to
	// an open object if the definition has no controls (so the schema stays
	// valid).
	var items map[string]any
	if len(oneOf) > 0 {
		items = map[string]any{"oneOf": oneOf}
	} else {
		items = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"control": map[string]any{"type": "string"},
				"value":   map[string]any{"description": "Value for the control; validated against its spec."},
			},
			"required": []any{"control", "value"},
		}
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"settings": map[string]any{
				"type":  "array",
				"items": items,
			},
		},
		"required": []any{"settings"},
	}
	b, _ := json.Marshal(schema)
	return b
}

// valueSchemaNode builds the JSON Schema node for a control's value field from
// its ValueSpec. Parametric controls accept an object {number, value}; all
// others accept the value scalar directly.
func valueSchemaNode(c *device.Control) map[string]any {
	base := specSchemaNode(&c.Value)
	if c.Parametric {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"number": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "address number (cc#/nrpn#/note#) supplied at call time",
				},
				"value": base,
			},
			"required": []any{"number", "value"},
		}
	}
	return base
}

// specSchemaNode maps a ValueSpec to a JSON Schema node mirroring resolveValue's
// accepted domain (range defaults to 0..127; enum accepts its labels and also
// the raw wire ints resolveEnum allows).
func specSchemaNode(spec *device.ValueSpec) map[string]any {
	switch spec.Type {
	case device.ValueEnum:
		labels := make([]string, 0, len(spec.Values))
		for k := range spec.Values {
			labels = append(labels, k)
		}
		sort.Strings(labels)
		enum := make([]any, 0, len(spec.Values)*2)
		for _, l := range labels {
			enum = append(enum, l)
		}
		// Also accept the raw wire values (resolveEnum allows them).
		seen := map[int]bool{}
		wires := make([]int, 0, len(spec.Values))
		for _, w := range spec.Values {
			if !seen[w] {
				seen[w] = true
				wires = append(wires, w)
			}
		}
		sort.Ints(wires)
		for _, w := range wires {
			enum = append(enum, w)
		}
		return map[string]any{
			"enum":        enum,
			"description": "one of " + enumLabelWire(spec.Values),
		}
	case device.ValueInt:
		node := map[string]any{"type": "integer"}
		applyBounds(node, spec)
		return node
	case device.ValueFloat:
		node := map[string]any{"type": "number"}
		applyBounds(node, spec)
		return node
	case device.ValueString:
		return map[string]any{"type": "string"}
	case device.ValueRange, "":
		node := map[string]any{"type": "integer", "minimum": 0, "maximum": 127}
		applyBounds(node, spec)
		return node
	default:
		return map[string]any{"description": "value (validated against its spec)"}
	}
}

// applyBounds copies the spec's min/max (and unit hint) onto a numeric schema
// node. Min/Max are *float64; integer schemas get whole-number bounds.
func applyBounds(node map[string]any, spec *device.ValueSpec) {
	isInt := node["type"] == "integer"
	if spec.Min != nil {
		if isInt {
			node["minimum"] = int(*spec.Min)
		} else {
			node["minimum"] = *spec.Min
		}
	}
	if spec.Max != nil {
		if isInt {
			node["maximum"] = int(*spec.Max)
		} else {
			node["maximum"] = *spec.Max
		}
	}
	if spec.Unit != "" {
		node["description"] = "unit: " + spec.Unit
	}
}

// enumLabelWire renders an enum's label->wire mapping as a stable string for a
// schema/description, e.g. "{off=0, on=127}".
func enumLabelWire(values map[string]int) string {
	labels := make([]string, 0, len(values))
	for k := range values {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	parts := make([]string, 0, len(labels))
	for _, l := range labels {
		parts = append(parts, fmt.Sprintf("%s=%d", l, values[l]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
