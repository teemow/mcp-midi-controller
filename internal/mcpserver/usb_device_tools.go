package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// This file generates the SEMANTIC, per-binding USB tools from a device's USB
// profile (device.USBProfile) — the named, structured surface (get_param /
// set_param / list / read_block plus per-protocol convenience names like
// <logical>_read_system, <logical>_list_patterns, <logical>_list_presets) that
// sits above the generic usb_* escape hatches in usb_tools.go. Tools are
// generated per USB binding via AddUSBDeviceTool (called from
// addToolsForDevice) and torn down by RemoveUSBDeviceTool. Write tools are only
// generated when the write gate is open for that binding (see
// usbWritesAllowed). Adding/removing tools emits tools/list_changed. See
// docs/usb-tools.md.

// Roland/Boss editor command addresses (BOSS Tone Studio protocol). These drive
// the gated recall/write convenience tools for roland-address-sysex devices
// (the SL-2 family): PATCH_SELECT recalls a stored slot into the live edit
// buffer (changes the live sound), PATCH_WRITE stores the edit buffer into a
// slot (mutates stored memory). Both take "00 <slot>" as data. See
// docs/research/sl-2.md.
const (
	rolandPatchSelectAddr = 0x7F000100
	rolandPatchWriteAddr  = 0x7F000104
)

// usbDeviceTool is one generated semantic tool: its registration spec plus the
// gate flag. write tools are only registered when the binding's write gate is
// open; all tools (write or not) are removed on unbind.
type usbDeviceTool struct {
	name    string
	write   bool
	tool    *mcp.Tool
	handler mcp.ToolHandler
}

// AddUSBDeviceTool generates and registers the semantic USB tool family for a
// USB device from its device profile, emitting tools/list_changed. It is a
// no-op for a device with no USB profile.
func (s *Server) AddUSBDeviceTool(d engine.Device) {
	def, ok := s.eng.Registry().Get(d.DeviceID)
	if !ok || def.USB == nil {
		return
	}
	writes := s.usbWritesAllowed(d)
	for _, t := range s.usbDeviceTools(d.Name, def.USB) {
		if t.write && !writes {
			continue
		}
		s.mcp.AddTool(t.tool, t.handler)
	}
}

// RemoveUSBDeviceTool removes every tool AddUSBDeviceTool could have generated
// for a logical (regardless of the write gate, so a binding that was writable
// is fully torn down), emitting tools/list_changed. It resolves the names from
// the (still-registered) definition; callers must remove the binding's tools
// before forgetting the definition.
func (s *Server) RemoveUSBDeviceTool(logical string) {
	d, ok := s.eng.DeviceFor(logical)
	if !ok {
		return
	}
	def, ok := s.eng.Registry().Get(d.DeviceID)
	if !ok || def.USB == nil {
		return
	}
	specs := s.usbDeviceTools(logical, def.USB)
	names := make([]string, len(specs))
	for i, t := range specs {
		names[i] = t.name
	}
	s.mcp.RemoveTools(names...)
}

// usbDeviceTools builds the full candidate tool set for a USB binding from its
// profile (write tools included, marked write=true). The set is profile-driven:
// param tools appear only when the profile maps params; block tools appear only
// when the profile has a repeated region; convenience names are keyed off the
// protocol. Both AddUSBDeviceTool and RemoveUSBDeviceTool derive from this one
// list so the registered and removed names stay in sync.
func (s *Server) usbDeviceTools(logical string, p *device.USBProfile) []usbDeviceTool {
	var out []usbDeviceTool
	add := func(t usbDeviceTool) { out = append(out, t) }

	// <logical>_read: addressed/region read (decoded hex), the device-scoped
	// counterpart of usb_read.
	add(usbDeviceTool{
		name: logical + "_read",
		tool: &mcp.Tool{
			Name:        logical + "_read",
			Description: fmt.Sprintf("Read size bytes from %q at addr; with region, addr is an offset into that named region (index selects a repeated block).", logical),
			InputSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"},"index":{"type":"integer"},"addr":{"type":["integer","string"]},"size":{"type":"integer"}},"required":["size"]}`),
		},
		handler: s.handleUSBDeviceRead(logical),
	})

	if len(p.Params) > 0 {
		names := p.ParamNames()
		enum := paramEnumSchema(names)
		add(usbDeviceTool{
			name: logical + "_get_param",
			tool: &mcp.Tool{
				Name:        logical + "_get_param",
				Description: fmt.Sprintf("Read one named parameter of %q over USB and decode it. param is one of: %s.", logical, strings.Join(names, ", ")),
				InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"param":%s},"required":["param"]}`, enum)),
			},
			handler: s.handleUSBGetParam(logical),
		})
		add(usbDeviceTool{
			name:  logical + "_set_param",
			write: true,
			tool: &mcp.Tool{
				Name:        logical + "_set_param",
				Description: fmt.Sprintf("Write one named parameter of %q over USB (gated; mutates the device). dry_run (default true) returns the exact frame without sending. param is one of: %s.", logical, strings.Join(names, ", ")),
				InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"param":%s,"value":{},"dry_run":{"type":"boolean"}},"required":["param","value"]}`, enum)),
			},
			handler: s.handleUSBSetParam(logical),
		})

		// <logical>_read_params: decode every mapped parameter at once.
		add(usbDeviceTool{
			name: logical + "_read_params",
			tool: &mcp.Tool{
				Name:        logical + "_read_params",
				Description: fmt.Sprintf("Read and decode every mapped parameter of %q over USB. With region, only that region's params are read.", logical),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"}}}`),
			},
			handler: s.handleUSBReadParams(logical, ""),
		})
	}

	// Repeated-region tools: list the block instances and read one block.
	if region, _, ok := primaryRepeatedRegion(p); ok {
		add(usbDeviceTool{
			name: logical + "_list",
			tool: &mcp.Tool{
				Name:        logical + "_list",
				Description: fmt.Sprintf("List the repeated blocks (index + address) of %q. region defaults to %q.", logical, region),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"}}}`),
			},
			handler: s.handleUSBList(logical, region),
		})
		add(usbDeviceTool{
			name: logical + "_read_block",
			tool: &mcp.Tool{
				Name:        logical + "_read_block",
				Description: fmt.Sprintf("Read one block (by index) of a repeated region of %q. region defaults to %q; size defaults to 32 bytes.", logical, region),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"},"index":{"type":"integer"},"size":{"type":"integer"}},"required":["index"]}`),
			},
			handler: s.handleUSBReadBlock(logical, region),
		})
	}

	out = append(out, s.usbConvenienceTools(logical, p)...)
	return out
}

// usbConvenienceTools returns the per-protocol convenience aliases documented in
// docs/usb-tools.md (with the binding's logical name as the prefix, so a binding
// named "sl2" yields sl2_read_system, sl2_list_patterns, …). They are thin
// aliases over the generic profile-driven handlers, registered only when the
// profile carries what they need.
func (s *Server) usbConvenienceTools(logical string, p *device.USBProfile) []usbDeviceTool {
	var out []usbDeviceTool
	repeated, _, hasRepeated := primaryRepeatedRegion(p)
	hasParams := len(p.Params) > 0

	switch p.Protocol {
	case device.USBProtocolRoland:
		if hasParams {
			// read_system: the curated SYSTEM params (falls back to all params
			// when there is no system region).
			regionFilter := ""
			if _, ok := p.Regions["system"]; ok {
				regionFilter = "system"
			}
			out = append(out, usbDeviceTool{
				name: logical + "_read_system",
				tool: &mcp.Tool{
					Name:        logical + "_read_system",
					Description: fmt.Sprintf("Read %q SYSTEM settings (tempo, MIDI channel, EXP function, …) over USB, decoded.", logical),
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				handler: s.handleUSBReadParams(logical, regionFilter),
			})
		}
		if hasRepeated {
			out = append(out, usbDeviceTool{
				name: logical + "_list_patterns",
				tool: &mcp.Tool{
					Name:        logical + "_list_patterns",
					Description: fmt.Sprintf("List the stored pattern/patch slots (index + address) of %q.", logical),
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				handler: s.handleUSBList(logical, repeated),
			})
		}
		// recall_pattern / write_pattern (gated): editor commands.
		out = append(out, usbDeviceTool{
			name:  logical + "_recall_pattern",
			write: true,
			tool: &mcp.Tool{
				Name:        logical + "_recall_pattern",
				Description: fmt.Sprintf("Recall stored slot N into %q's live edit buffer (PATCH_SELECT; changes the LIVE sound). dry_run (default true) returns the frame without sending.", logical),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"slot":{"type":"integer"},"dry_run":{"type":"boolean"}},"required":["slot"]}`),
			},
			handler: s.handleUSBRolandSlot(logical, rolandPatchSelectAddr),
		})
		out = append(out, usbDeviceTool{
			name:  logical + "_write_pattern",
			write: true,
			tool: &mcp.Tool{
				Name:        logical + "_write_pattern",
				Description: fmt.Sprintf("Store %q's live edit buffer into stored slot N (PATCH_WRITE; mutates stored memory). dry_run (default true) returns the frame without sending.", logical),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"slot":{"type":"integer"},"dry_run":{"type":"boolean"}},"required":["slot"]}`),
			},
			handler: s.handleUSBRolandSlot(logical, rolandPatchWriteAddr),
		})

	case device.USBProtocolNeuro:
		if hasRepeated {
			out = append(out, usbDeviceTool{
				name: logical + "_list_presets",
				tool: &mcp.Tool{
					Name:        logical + "_list_presets",
					Description: fmt.Sprintf("List the stored preset slots (index + address) of %q.", logical),
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				handler: s.handleUSBList(logical, repeated),
			})
			out = append(out, usbDeviceTool{
				name: logical + "_read_preset",
				tool: &mcp.Tool{
					Name:        logical + "_read_preset",
					Description: fmt.Sprintf("Read one preset block (by index) of %q over USB. size defaults to 32 bytes.", logical),
					InputSchema: json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"},"size":{"type":"integer"}},"required":["index"]}`),
				},
				handler: s.handleUSBReadBlock(logical, repeated),
			})
		}
		out = append(out, usbDeviceTool{
			name:  logical + "_select_preset",
			write: true,
			tool: &mcp.Tool{
				Name:        logical + "_select_preset",
				Description: fmt.Sprintf("Select preset N on %q (gated; changes the LIVE sound). dry_run (default true) returns the frame without sending.", logical),
				InputSchema: json.RawMessage(`{"type":"object","properties":{"preset":{"type":"integer"},"dry_run":{"type":"boolean"}},"required":["preset"]}`),
			},
			handler: s.handleUSBSelectPreset(logical),
		})

	case device.USBProtocolMorningstar:
		if hasParams {
			out = append(out, usbDeviceTool{
				name: logical + "_read_config",
				tool: &mcp.Tool{
					Name:        logical + "_read_config",
					Description: fmt.Sprintf("Read %q's mapped configuration parameters over USB, decoded.", logical),
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				handler: s.handleUSBReadParams(logical, ""),
			})
		}
		if hasRepeated {
			out = append(out, usbDeviceTool{
				name: logical + "_get_preset",
				tool: &mcp.Tool{
					Name:        logical + "_get_preset",
					Description: fmt.Sprintf("Read one preset block (by index) of %q over USB. size defaults to 32 bytes.", logical),
					InputSchema: json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"},"size":{"type":"integer"}},"required":["index"]}`),
				},
				handler: s.handleUSBReadBlock(logical, repeated),
			})
		}
	}
	return out
}

// --- handlers -------------------------------------------------------------

func (s *Server) handleUSBDeviceRead(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Region string `json:"region"`
			Index  int    `json:"index"`
			Addr   any    `json:"addr"`
			Size   int    `json:"size"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		addr, err := parseAddrArg(args.Addr)
		if err != nil {
			return textResult("/addr: "+err.Error(), true), nil
		}
		gotAddr, data, err := s.eng.USBRead(ctx, logical, args.Region, args.Index, addr, args.Size)
		if err != nil {
			return textResult("read failed: "+err.Error(), true), nil
		}
		return textResult(fmt.Sprintf("addr=0x%X len=%d data=%s ascii=%q", gotAddr, len(data), hexBytes(data), asciiBytes(data)), false), nil
	}
}

func (s *Server) handleUSBGetParam(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Param string `json:"param"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		if args.Param == "" {
			return textResult("/param: required", true), nil
		}
		v, err := s.eng.USBGetParam(ctx, logical, args.Param)
		if err != nil {
			return textResult("get_param failed: "+err.Error(), true), nil
		}
		return textResult(fmt.Sprintf("%s = %v", args.Param, v), false), nil
	}
}

func (s *Server) handleUSBSetParam(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Param  string `json:"param"`
			Value  any    `json:"value"`
			DryRun *bool  `json:"dry_run"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		if args.Param == "" {
			return textResult("/param: required", true), nil
		}
		dryRun := true
		if args.DryRun != nil {
			dryRun = *args.DryRun
		}
		frame, err := s.eng.USBSetParam(ctx, logical, args.Param, args.Value, dryRun)
		if err != nil {
			return textResult("set_param failed: "+err.Error(), true), nil
		}
		if dryRun {
			return textResult(fmt.Sprintf("dry_run: would set %s=%v\nframe=%s", args.Param, args.Value, hexBytes(frame)), false), nil
		}
		return textResult(fmt.Sprintf("set %s=%v (frame=%s)", args.Param, args.Value, hexBytes(frame)), false), nil
	}
}

// handleUSBReadParams decodes every mapped param (optionally filtered to a
// region) and returns them as a JSON object.
func (s *Server) handleUSBReadParams(logical, region string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// An explicit region argument overrides the bound default.
		if len(req.Params.Arguments) > 0 {
			var args struct {
				Region string `json:"region"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &args); err == nil && args.Region != "" {
				region = args.Region
			}
		}
		d, ok := s.eng.DeviceFor(logical)
		if !ok {
			return textResult(fmt.Sprintf("unknown device %q", logical), true), nil
		}
		def, ok := s.eng.Registry().Get(d.DeviceID)
		if !ok || def.USB == nil {
			return textResult(fmt.Sprintf("device %q has no usb profile", logical), true), nil
		}
		out := map[string]any{}
		for _, name := range def.USB.ParamNames() {
			par, _ := def.USB.Param(name)
			if region != "" && par.Region != region {
				continue
			}
			v, err := s.eng.USBGetParam(ctx, logical, name)
			if err != nil {
				out[name] = "ERROR: " + err.Error()
				continue
			}
			out[name] = v
		}
		if len(out) == 0 {
			return textResult("no parameters read (none mapped for this region)", false), nil
		}
		j, _ := json.MarshalIndent(out, "", "  ")
		return textResult(string(j), false), nil
	}
}

func (s *Server) handleUSBList(logical, defaultRegion string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		region := defaultRegion
		if len(req.Params.Arguments) > 0 {
			var args struct {
				Region string `json:"region"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &args); err == nil && args.Region != "" {
				region = args.Region
			}
		}
		blocks, err := s.eng.USBListBlocks(ctx, logical, region)
		if err != nil {
			return textResult("list failed: "+err.Error(), true), nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "region=%s count=%d\n", region, len(blocks))
		for _, blk := range blocks {
			fmt.Fprintf(&sb, "  [%d] addr=0x%X\n", blk.Index, blk.Addr)
		}
		return textResult(sb.String(), false), nil
	}
}

func (s *Server) handleUSBReadBlock(logical, defaultRegion string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Region string `json:"region"`
			Index  int    `json:"index"`
			Size   int    `json:"size"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		region := defaultRegion
		if args.Region != "" {
			region = args.Region
		}
		size := args.Size
		if size <= 0 {
			size = 32
		}
		gotAddr, data, err := s.eng.USBRead(ctx, logical, region, args.Index, 0, size)
		if err != nil {
			return textResult("read_block failed: "+err.Error(), true), nil
		}
		return textResult(fmt.Sprintf("region=%s index=%d addr=0x%X len=%d data=%s ascii=%q", region, args.Index, gotAddr, len(data), hexBytes(data), asciiBytes(data)), false), nil
	}
}

// handleUSBRolandSlot drives a Roland editor slot command (PATCH_SELECT /
// PATCH_WRITE) at addr with "00 <slot>" as data.
func (s *Server) handleUSBRolandSlot(logical string, addr int64) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Slot   int   `json:"slot"`
			DryRun *bool `json:"dry_run"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		if args.Slot < 0 || args.Slot > 127 {
			return textResult("/slot: must be in [0, 127]", true), nil
		}
		dryRun := true
		if args.DryRun != nil {
			dryRun = *args.DryRun
		}
		frame, err := s.eng.USBWrite(ctx, logical, addr, []byte{0x00, byte(args.Slot)}, dryRun)
		if err != nil {
			return textResult("command failed: "+err.Error(), true), nil
		}
		if dryRun {
			return textResult(fmt.Sprintf("dry_run: would command slot %d at 0x%X\nframe=%s", args.Slot, addr, hexBytes(frame)), false), nil
		}
		return textResult(fmt.Sprintf("commanded slot %d at 0x%X (frame=%s)", args.Slot, addr, hexBytes(frame)), false), nil
	}
}

// handleUSBSelectPreset drives a Neuro 0x77 preset select (the preset index is
// carried as the write address; data is unused).
func (s *Server) handleUSBSelectPreset(logical string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Preset int   `json:"preset"`
			DryRun *bool `json:"dry_run"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
		if args.Preset < 0 || args.Preset > 127 {
			return textResult("/preset: must be in [0, 127]", true), nil
		}
		dryRun := true
		if args.DryRun != nil {
			dryRun = *args.DryRun
		}
		frame, err := s.eng.USBWrite(ctx, logical, int64(args.Preset), nil, dryRun)
		if err != nil {
			return textResult("select_preset failed: "+err.Error(), true), nil
		}
		if dryRun {
			return textResult(fmt.Sprintf("dry_run: would select preset %d\nframe=%s", args.Preset, hexBytes(frame)), false), nil
		}
		return textResult(fmt.Sprintf("selected preset %d (frame=%s)", args.Preset, hexBytes(frame)), false), nil
	}
}

// --- helpers --------------------------------------------------------------

// primaryRepeatedRegion returns the lexicographically first region with a
// positive count (e.g. an SL-2 patches region or an EQ2 presets region), the
// default target for the list / read_block tools.
func primaryRepeatedRegion(p *device.USBProfile) (string, device.Region, bool) {
	names := make([]string, 0, len(p.Regions))
	for name, r := range p.Regions {
		if r.Count > 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", device.Region{}, false
	}
	sort.Strings(names)
	return names[0], p.Regions[names[0]], true
}

// paramEnumSchema renders a JSON-Schema string node constrained to the param
// names so the model is steered to a valid parameter.
func paramEnumSchema(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return fmt.Sprintf(`{"type":"string","enum":[%s]}`, strings.Join(quoted, ","))
}

// usbWriteDeniedMsg explains why a USB write was refused, naming whichever half
// of the two-key gate is closed.
func usbWriteDeniedMsg(global, writable bool) string {
	switch {
	case !global && !writable:
		return "usb writes are disabled: set usb_allow_writes in config.yaml AND bind this device with writable: true (use dry_run to preview the bytes)"
	case !global:
		return "usb writes are disabled: set usb_allow_writes in config.yaml (use dry_run to preview the bytes)"
	default:
		return "usb writes are disabled for this device: re-add it with writable: true (use dry_run to preview the bytes)"
	}
}
