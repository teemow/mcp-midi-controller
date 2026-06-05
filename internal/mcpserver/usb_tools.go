package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerUSBTools registers the generic USB editor/readback tools. They mirror
// send_raw: device-independent escape hatches that target a USB-bound logical
// device (usb_identify/read/dump/write) or an unbound endpoint
// (usb_probe/usb_monitor) so new devices can be authored. The semantic,
// per-binding USB tools (get_param/set_param/list) are generated separately from
// each device's profile (see docs/usb-tools.md).
func (s *Server) registerUSBTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_identify",
		Description: "Ask a USB-bound device to identify itself (SysEx identity reply). device is a USB-capable device's name.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"}},"required":["device"]}`),
	}, s.handleUSBIdentify)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_read",
		Description: "Read size bytes from a USB-bound device at addr (a number or hex string). With region, addr is an offset into that named region (index selects a repeated block).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"region":{"type":"string"},"index":{"type":"integer"},"addr":{"type":["integer","string"]},"size":{"type":"integer"}},"required":["device","size"]}`),
	}, s.handleUSBRead)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_dump",
		Description: "Read a block of size bytes from a USB-bound device, issuing one read per chunk (default 32) and returning the concatenated data.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"region":{"type":"string"},"index":{"type":"integer"},"addr":{"type":["integer","string"]},"size":{"type":"integer"},"chunk":{"type":"integer"}},"required":["device","size"]}`),
	}, s.handleUSBDump)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_write",
		Description: "Write bytes to a USB-bound device at addr. data is an array of byte values or a hex string. dry_run (default true) returns the exact frame WITHOUT sending; pass dry_run=false to actually write (mutates the device).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"addr":{"type":["integer","string"]},"data":{"type":["array","string"],"items":{"type":"integer"}},"dry_run":{"type":"boolean"}},"required":["device","addr","data"]}`),
	}, s.handleUSBWrite)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_probe",
		Description: "Probe an UNBOUND USB endpoint to author a new device: identify it (SysEx protocols) or briefly monitor it (HID). transport is usbmidi|usbhid, endpoint an ALSA port name or VID:PID, protocol one of roland-address-sysex|morningstar-sysex|neuro-hid|torpedo-hid.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"transport":{"type":"string"},"endpoint":{"type":"string"},"protocol":{"type":"string"}},"required":["transport","endpoint","protocol"]}`),
	}, s.handleUSBProbe)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "usb_monitor",
		Description: "Drain unsolicited USB frames (HID reports / hand-tweak SysEx) for timeout_ms. Target a USB-bound device, or an unbound transport+endpoint.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"transport":{"type":"string"},"endpoint":{"type":"string"},"timeout_ms":{"type":"integer"}}}`),
	}, s.handleUSBMonitor)
}

func (s *Server) handleUSBIdentify(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" {
		return textResult("/device: required", true), nil
	}
	id, err := s.eng.USBIdentify(ctx, args.Device)
	if err != nil {
		return textResult("usb_identify failed: "+err.Error(), true), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "manufacturer=0x%02X device_id=0x%02X\n", id.Manufacturer, id.DeviceID)
	if len(id.Family) > 0 {
		fmt.Fprintf(&b, "family=%s member=%s revision=%s\n", hexBytes(id.Family), hexBytes(id.Member), hexBytes(id.Revision))
	}
	fmt.Fprintf(&b, "raw=%s", hexBytes(id.Raw))
	return textResult(b.String(), false), nil
}

func (s *Server) handleUSBRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
		Region string `json:"region"`
		Index  int    `json:"index"`
		Addr   any    `json:"addr"`
		Size   int    `json:"size"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" {
		return textResult("/device: required", true), nil
	}
	addr, err := parseAddrArg(args.Addr)
	if err != nil {
		return textResult("/addr: "+err.Error(), true), nil
	}
	gotAddr, data, err := s.eng.USBRead(ctx, args.Device, args.Region, args.Index, addr, args.Size)
	if err != nil {
		return textResult("usb_read failed: "+err.Error(), true), nil
	}
	return textResult(fmt.Sprintf("addr=0x%X len=%d data=%s ascii=%q", gotAddr, len(data), hexBytes(data), asciiBytes(data)), false), nil
}

func (s *Server) handleUSBDump(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
		Region string `json:"region"`
		Index  int    `json:"index"`
		Addr   any    `json:"addr"`
		Size   int    `json:"size"`
		Chunk  int    `json:"chunk"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" {
		return textResult("/device: required", true), nil
	}
	addr, err := parseAddrArg(args.Addr)
	if err != nil {
		return textResult("/addr: "+err.Error(), true), nil
	}
	data, err := s.eng.USBDump(ctx, args.Device, args.Region, args.Index, addr, args.Size, args.Chunk)
	if err != nil {
		return textResult("usb_dump failed: "+err.Error(), true), nil
	}
	return textResult(fmt.Sprintf("len=%d data=%s ascii=%q", len(data), hexBytes(data), asciiBytes(data)), false), nil
}

func (s *Server) handleUSBWrite(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string          `json:"device"`
		Addr   any             `json:"addr"`
		Data   json.RawMessage `json:"data"`
		DryRun *bool           `json:"dry_run"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" {
		return textResult("/device: required", true), nil
	}
	addr, err := parseAddrArg(args.Addr)
	if err != nil {
		return textResult("/addr: "+err.Error(), true), nil
	}
	data, err := parseBytesArg(args.Data)
	if err != nil {
		return textResult("/data: "+err.Error(), true), nil
	}
	// Default to a dry run: writes mutate hardware, so the caller must opt in.
	dryRun := true
	if args.DryRun != nil {
		dryRun = *args.DryRun
	}
	// A real write is gated: it requires the daemon's usb_allow_writes and the
	// binding's writable opt-in. Dry runs (which only return the bytes) are
	// always allowed.
	if !dryRun {
		d, ok := s.eng.DeviceFor(args.Device)
		if !ok {
			return textResult(fmt.Sprintf("unknown device %q", args.Device), true), nil
		}
		if !s.usbWritesAllowed(d) {
			return textResult(usbWriteDeniedMsg(s.usbAllowWrites, d.USBWritable()), true), nil
		}
	}
	frame, err := s.eng.USBWrite(ctx, args.Device, addr, data, dryRun)
	if err != nil {
		return textResult("usb_write failed: "+err.Error(), true), nil
	}
	if dryRun {
		return textResult(fmt.Sprintf("dry_run: would write addr=0x%X data=%s\nframe=%s", addr, hexBytes(data), hexBytes(frame)), false), nil
	}
	return textResult(fmt.Sprintf("wrote addr=0x%X data=%s (frame=%s)", addr, hexBytes(data), hexBytes(frame)), false), nil
}

func (s *Server) handleUSBProbe(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Transport string `json:"transport"`
		Endpoint  string `json:"endpoint"`
		Protocol  string `json:"protocol"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Transport == "" || args.Endpoint == "" || args.Protocol == "" {
		return textResult("/transport, /endpoint and /protocol are required", true), nil
	}
	res, err := s.eng.USBProbe(ctx, args.Transport, args.Endpoint, args.Protocol)
	if err != nil {
		return textResult("usb_probe failed: "+err.Error(), true), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "endpoint=%s transport=%s protocol=%s\n", res.Endpoint, res.Transport, res.Protocol)
	if res.Identity != nil {
		fmt.Fprintf(&b, "identity: manufacturer=0x%02X device_id=0x%02X", res.Identity.Manufacturer, res.Identity.DeviceID)
		if len(res.Identity.Family) > 0 {
			fmt.Fprintf(&b, " family=%s member=%s revision=%s", hexBytes(res.Identity.Family), hexBytes(res.Identity.Member), hexBytes(res.Identity.Revision))
		}
		b.WriteByte('\n')
	} else {
		b.WriteString("identity: (none)\n")
	}
	if len(res.Frames) == 0 {
		b.WriteString("frames: (none observed)")
	} else {
		fmt.Fprintf(&b, "frames (%d):", len(res.Frames))
		for _, f := range res.Frames {
			fmt.Fprintf(&b, "\n  %s  %q", hexBytes(f), asciiBytes(f))
		}
	}
	return textResult(b.String(), false), nil
}

func (s *Server) handleUSBMonitor(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device    string `json:"device"`
		Transport string `json:"transport"`
		Endpoint  string `json:"endpoint"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	window := time.Duration(args.TimeoutMS) * time.Millisecond

	var (
		frames [][]byte
		err    error
	)
	switch {
	case args.Device != "":
		frames, err = s.eng.USBMonitorLogical(ctx, args.Device, window)
	case args.Transport != "" && args.Endpoint != "":
		frames, err = s.eng.USBMonitor(ctx, args.Transport, args.Endpoint, window)
	default:
		return textResult("provide either device (a USB-capable device's name) or transport+endpoint", true), nil
	}
	if err != nil {
		return textResult("usb_monitor failed: "+err.Error(), true), nil
	}
	if len(frames) == 0 {
		return textResult("no frames observed in the window", false), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "observed %d frame(s):", len(frames))
	for _, f := range frames {
		fmt.Fprintf(&b, "\n  %s  %q", hexBytes(f), asciiBytes(f))
	}
	return textResult(b.String(), false), nil
}

// parseAddrArg parses an address argument that may be a JSON number or a hex
// string ("0x20000000", "20000000", or space-separated wire bytes "20 00 00
// 00"). A nil/absent value is address 0.
func parseAddrArg(v any) (int64, error) {
	switch a := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return int64(a), nil
	case json.Number:
		return strconv.ParseInt(a.String(), 10, 64)
	case string:
		s := strings.TrimSpace(a)
		if s == "" {
			return 0, nil
		}
		if strings.ContainsAny(s, " \t") {
			// Space-separated wire bytes: assemble big-endian.
			var addr int64
			for _, f := range strings.Fields(s) {
				b, err := strconv.ParseUint(strings.TrimPrefix(f, "0x"), 16, 8)
				if err != nil {
					return 0, fmt.Errorf("bad hex byte %q", f)
				}
				addr = addr<<8 | int64(b)
			}
			return addr, nil
		}
		base := 10
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			s = s[2:]
			base = 16
		}
		return strconv.ParseInt(s, base, 64)
	default:
		return 0, fmt.Errorf("addr must be a number or hex string, got %T", v)
	}
}

// parseBytesArg parses a usb_write data argument: a JSON array of byte values or
// a hex string ("00 04 0C 04" or "00040C04").
func parseBytesArg(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("required")
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var ints []int
		if err := json.Unmarshal(raw, &ints); err != nil {
			return nil, err
		}
		out := make([]byte, len(ints))
		for i, v := range ints {
			if v < 0 || v > 255 {
				return nil, fmt.Errorf("byte %d out of range [0,255]: %d", i, v)
			}
			out[i] = byte(v)
		}
		return out, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return nil, err
	}
	return parseHexString(str)
}

// parseHexString parses "00 04 0C 04" or "00040C04" into bytes.
func parseHexString(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	if strings.ContainsAny(s, " \t,") {
		fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' })
		out := make([]byte, 0, len(fields))
		for _, f := range fields {
			b, err := strconv.ParseUint(strings.TrimPrefix(f, "0x"), 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad hex byte %q", f)
			}
			out = append(out, byte(b))
		}
		return out, nil
	}
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd-length hex string")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		b, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, fmt.Errorf("bad hex at %d: %w", i*2, err)
		}
		out[i] = byte(b)
	}
	return out, nil
}

// hexBytes renders bytes as space-separated uppercase hex.
func hexBytes(b []byte) string {
	if len(b) == 0 {
		return "(empty)"
	}
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, " ")
}

// asciiBytes renders bytes as printable ASCII, replacing non-printables with a
// dot, for human-readable name/text fields.
func asciiBytes(b []byte) string {
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, string(b))
}
