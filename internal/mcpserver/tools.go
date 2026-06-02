package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	// Stubbed surface (TODO: wire to engine).
	s.addStub("discover_endpoints", "Scan for reachable transport endpoints (BLE peripherals, OSC hosts).", objSchema)
	s.addStub("pair_endpoint", "Pair/bond with a BLE endpoint.", json.RawMessage(`{"type":"object","properties":{"endpoint":{"type":"string"}},"required":["endpoint"]}`))
	s.addStub("bind_device", "Bind an endpoint+channel to a device definition (generates a control_<logical> tool).", json.RawMessage(`{"type":"object","properties":{"logical":{"type":"string"},"endpoint":{"type":"string"},"channel":{"type":"integer"},"device":{"type":"string"}},"required":["logical","endpoint","channel","device"]}`))
	s.addStub("unbind_device", "Remove a logical device binding.", json.RawMessage(`{"type":"object","properties":{"logical":{"type":"string"}},"required":["logical"]}`))
	s.addStub("save_scene", "Save the current desired-state (or a subset) as a named scene.", sceneArgSchema)
	s.addStub("recall_scene", "Recall a scene (PC before CC, with per-device settle delay).", sceneArgSchema)
	s.addStub("list_scenes", "List saved scenes.", objSchema)
	s.addStub("send_raw", "Escape hatch: send raw MIDI bytes or an OSC address (untracked).", objSchema)
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
