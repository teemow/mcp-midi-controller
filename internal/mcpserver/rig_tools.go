package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

// registerRigTools wires the read-only "rig reasoning" surface: machine-readable
// views of the bound rig (list_bindings), the loaded device-definition registry
// (list_definitions / get_definition) so an agent (or the web client) can reason
// about what is bound and what each device can do. Like describe_device these
// never mutate anything; unlike it they emit structuredContent (JSON) alongside
// the human text.
func (s *Server) registerRigTools() {
	objSchema := json.RawMessage(`{"type":"object"}`)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_bindings",
		Description: "List the rig's logical-device bindings (logical name, device definition id, endpoint, MIDI channel, transport, USB/write flags). " +
			"The machine-readable companion to list_devices: emits structuredContent {bindings:[...]} for programmatic rig reasoning.",
		InputSchema: objSchema,
	}, s.handleListBindings)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_definitions",
		Description: "List every loaded device definition (bundled + user dir), bound or not: id, name, manufacturer, transport, control count, USB-surface flag. " +
			"Use get_definition for a definition's full control detail. Emits structuredContent {definitions:[...]}.",
		InputSchema: objSchema,
	}, s.handleListDefinitions)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_definition",
		Description: "Get one device definition's full detail: every control with its type, addressing (cc/nrpn/program/sysex/osc), value spec (range/enum/unit) and description, plus USB-surface presence. " +
			"Select by definition id (see list_definitions) or by a bound logical device name. Emits the definition as structuredContent.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Definition id (see list_definitions) or a bound logical device name."}
			},
			"required": ["id"]
		}`),
	}, s.handleGetDefinition)
}

// bindingView is the machine-readable shape of one binding for list_bindings.
type bindingView struct {
	Logical    string `json:"logical"`
	Device     string `json:"device"`
	DeviceName string `json:"device_name,omitempty"`
	Endpoint   string `json:"endpoint"`
	Channel    int    `json:"channel"`
	Transport  string `json:"transport,omitempty"`
	USB        bool   `json:"usb"`
	Writable   bool   `json:"writable,omitempty"`
}

func (s *Server) handleListBindings(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bindings := s.eng.Bindings()
	views := make([]bindingView, 0, len(bindings))
	for _, b := range bindings {
		v := bindingView{
			Logical:   b.Logical,
			Device:    b.DeviceID,
			Endpoint:  b.Endpoint,
			Channel:   b.Channel,
			Transport: b.Transport,
			USB:       s.eng.IsUSBBinding(b.Logical),
			Writable:  b.Writable,
		}
		if def, ok := s.eng.Registry().Get(b.DeviceID); ok {
			v.DeviceName = def.Name
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Logical < views[j].Logical })

	var b strings.Builder
	if len(views) == 0 {
		b.WriteString("no devices bound yet; use discover_endpoints + bind_device")
	}
	for _, v := range views {
		name := v.DeviceName
		if name == "" {
			name = v.Device
		}
		fmt.Fprintf(&b, "%s\t(device=%s, endpoint=%q, channel=%d", v.Logical, name, v.Endpoint, v.Channel)
		if v.Transport != "" {
			fmt.Fprintf(&b, ", transport=%s", v.Transport)
		}
		if v.USB {
			b.WriteString(", usb")
			if v.Writable {
				b.WriteString("/writable")
			}
		}
		b.WriteString(")\n")
	}
	return structResult(b.String(), map[string]any{"bindings": views}), nil
}

// definitionSummary is the per-definition row for list_definitions.
type definitionSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Transport    string `json:"transport"`
	Controls     int    `json:"controls"`
	USB          bool   `json:"usb"`
}

func (s *Server) handleListDefinitions(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	defs := s.eng.Registry().All()
	rows := make([]definitionSummary, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, definitionSummary{
			ID:           d.ID,
			Name:         d.Name,
			Manufacturer: d.Manufacturer,
			Transport:    d.Transport,
			Controls:     len(d.Controls),
			USB:          d.USB != nil,
		})
	}

	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("no device definitions loaded")
	}
	for _, r := range rows {
		mfr := r.Manufacturer
		if mfr == "" {
			mfr = "?"
		}
		fmt.Fprintf(&b, "%-20s %s [%s, transport=%s]: %d control(s)", r.ID, r.Name, mfr, r.Transport, r.Controls)
		if r.USB {
			b.WriteString(" +usb")
		}
		b.WriteByte('\n')
	}
	return structResult(b.String(), map[string]any{"definitions": rows}), nil
}

// valueSpecView / controlView / definitionView are the machine-readable shape of
// a definition for get_definition. They carry json tags (device.* uses yaml
// tags), so they decode predictably for the web client / agents.
type valueSpecView struct {
	Type   string         `json:"type,omitempty"`
	Min    *float64       `json:"min,omitempty"`
	Max    *float64       `json:"max,omitempty"`
	Step   *float64       `json:"step,omitempty"`
	Unit   string         `json:"unit,omitempty"`
	Values map[string]int `json:"values,omitempty"`
}

type controlView struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Type        string        `json:"type"`
	CC          *int          `json:"cc,omitempty"`
	NRPN        *int          `json:"nrpn,omitempty"`
	Program     *int          `json:"program,omitempty"`
	SysEx       string        `json:"sysex,omitempty"`
	Address     string        `json:"address,omitempty"`
	Parametric  bool          `json:"parametric,omitempty"`
	Value       valueSpecView `json:"value"`
}

type definitionView struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Manufacturer string        `json:"manufacturer,omitempty"`
	Description  string        `json:"description,omitempty"`
	Transport    string        `json:"transport"`
	SettleMS     int           `json:"settle_ms,omitempty"`
	USB          bool          `json:"usb"`
	Controls     []controlView `json:"controls"`
}

func newDefinitionView(d *device.Definition) definitionView {
	v := definitionView{
		ID:           d.ID,
		Name:         d.Name,
		Manufacturer: d.Manufacturer,
		Description:  d.Description,
		Transport:    d.Transport,
		SettleMS:     d.SettleMS,
		USB:          d.USB != nil,
		Controls:     make([]controlView, 0, len(d.Controls)),
	}
	for i := range d.Controls {
		c := &d.Controls[i]
		v.Controls = append(v.Controls, controlView{
			Name:        c.Name,
			Description: c.Description,
			Type:        string(c.Type),
			CC:          c.CC,
			NRPN:        c.NRPN,
			Program:     c.Program,
			SysEx:       c.SysEx,
			Address:     c.Address,
			Parametric:  c.Parametric,
			Value: valueSpecView{
				Type:   string(c.Value.Type),
				Min:    c.Value.Min,
				Max:    c.Value.Max,
				Step:   c.Value.Step,
				Unit:   c.Value.Unit,
				Values: c.Value.Values,
			},
		})
	}
	return v
}

func (s *Server) handleGetDefinition(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.ID == "" {
		return textResult("/id: required (definition id or bound logical device name)", true), nil
	}
	// Accept either a definition id or a logical device name (resolved via its
	// binding), mirroring describe_device.
	defID := args.ID
	for _, bind := range s.eng.Bindings() {
		if bind.Logical == args.ID {
			defID = bind.DeviceID
			break
		}
	}
	def, ok := s.eng.Registry().Get(defID)
	if !ok {
		return textResult(fmt.Sprintf("unknown definition %q (see list_definitions)", args.ID), true), nil
	}

	view := newDefinitionView(def)
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, transport=%s)", def.Name, def.ID, def.Transport)
	if def.Manufacturer != "" {
		fmt.Fprintf(&b, " by %s", def.Manufacturer)
	}
	if def.SettleMS > 0 {
		fmt.Fprintf(&b, " settle_ms=%d", def.SettleMS)
	}
	if def.USB != nil {
		b.WriteString(" +usb")
	}
	b.WriteByte('\n')
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
	return structResult(b.String(), view), nil
}
