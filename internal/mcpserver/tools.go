package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// registerGlobalTools registers the device-independent tools. Handlers that are
// not yet implemented return an informative IsError result so the surface is
// discoverable while the engine is filled in.
func (s *Server) registerGlobalTools() {
	objSchema := json.RawMessage(`{"type":"object"}`)
	deviceArgSchema := json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"}},"required":["device"]}`)
	sceneArgSchema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"mode":{"type":"string","enum":["additive","exact"]}},"required":["name"]}`)

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
		Description: "Scan for reachable transport endpoints (BLE peripherals, OSC hosts).",
		InputSchema: objSchema,
	}, s.handleDiscoverEndpoints)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "pair_endpoint",
		Description: "Pair/bond with a BLE endpoint and open its data path. Pass transport to target a non-default backend.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"endpoint":{"type":"string"},"transport":{"type":"string"}},"required":["endpoint"]}`),
	}, s.handlePairEndpoint)
	s.mcp.AddTool(&mcp.Tool{
		Name:        "bind_device",
		Description: "Bind an endpoint+channel to a device definition (generates a control_<logical> tool).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"logical":{"type":"string"},"endpoint":{"type":"string"},"channel":{"type":"integer"},"device":{"type":"string"}},"required":["logical","endpoint","channel","device"]}`),
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

	// Stubbed surface (TODO: wire to engine).
	s.addStub("save_scene", "Save the current desired-state (or a subset) as a named scene.", sceneArgSchema)
	s.addStub("recall_scene", "Recall a scene (PC before CC, with per-device settle delay).", sceneArgSchema)
	s.addStub("list_scenes", "List saved scenes.", objSchema)
	s.addStub("create_device_definition", "Begin authoring a new device definition.", objSchema)
	s.addStub("add_control", "Add a control to a device definition (optionally via MIDI-learn capture).", objSchema)
	s.addStub("save_device_definition", "Persist an authored definition to the user devices dir (hot-reloads).", objSchema)
	s.addStub("learn_start", "Start MIDI-learn: listen on an endpoint's inbound channel.", objSchema)
	s.addStub("learn_capture", "Return the most recently captured inbound CC/NRPN from learn mode.", objSchema)
}

func (s *Server) addStub(name, desc string, schema json.RawMessage) {
	s.mcp.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: schema}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult(name+": not implemented yet", true), nil
	})
}

func (s *Server) handleListDevices(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bindings := s.eng.Bindings()
	if len(bindings) == 0 {
		return textResult("no devices bound yet; use discover_endpoints + bind_device", false), nil
	}
	var b strings.Builder
	for _, bind := range bindings {
		name := bind.DeviceID
		if def, ok := s.eng.Registry().Get(bind.DeviceID); ok {
			name = def.Name
		}
		fmt.Fprintf(&b, "%s\t(device=%s, endpoint=%q, channel=%d)\n", bind.Logical, name, bind.Endpoint, bind.Channel)
	}
	return textResult(b.String(), false), nil
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
		if c.Description != "" {
			fmt.Fprintf(&b, " — %s", c.Description)
		}
		b.WriteByte('\n')
	}
	return textResult(b.String(), false), nil
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
		Logical  string `json:"logical"`
		Endpoint string `json:"endpoint"`
		Channel  int    `json:"channel"`
		Device   string `json:"device"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	b := engine.Binding{Logical: args.Logical, Endpoint: args.Endpoint, Channel: args.Channel, DeviceID: args.Device}
	if err := s.eng.Bind(b); err != nil {
		return textResult(err.Error(), true), nil
	}
	s.AddDeviceTool(b)
	if err := s.persistBindings(); err != nil {
		return textResult(fmt.Sprintf("bound %s (warning: could not persist bindings: %v)", args.Logical, err), false), nil
	}
	return textResult(fmt.Sprintf("bound %s -> %s on %q channel %d (tool control_%s)", args.Logical, args.Device, args.Endpoint, args.Channel, args.Logical), false), nil
}

func (s *Server) handleUnbindDevice(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Logical string `json:"logical"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	s.eng.Unbind(args.Logical)
	s.RemoveDeviceTool(args.Logical)
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

// persistBindings writes the current bindings to the rig-as-code bindings file.
func (s *Server) persistBindings() error {
	return engine.SaveBindingsFile(config.BindingsPath(), s.eng.Bindings())
}

// controlToolSchema builds the input schema for a control_<logical> tool: a
// batch of {control, value} where control is constrained to the device's
// control-name enum. Value is left open here and validated in-handler against
// the YAML value spec.
func controlToolSchema(controlNames []string) json.RawMessage {
	enum := make([]any, len(controlNames))
	for i, n := range controlNames {
		enum[i] = n
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"settings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"control": map[string]any{"type": "string", "enum": enum},
						"value":   map[string]any{"description": "Value for the control; validated against its spec."},
					},
					"required": []any{"control", "value"},
				},
			},
		},
		"required": []any{"settings"},
	}
	b, _ := json.Marshal(schema)
	return b
}
