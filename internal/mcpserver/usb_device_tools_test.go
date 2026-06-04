package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// fakeUSBMIDI is a no-op transport claiming the "usbmidi" id so USB bindings
// pass the engine's registered-transport check in these tests.
type fakeUSBMIDI struct{}

func (fakeUSBMIDI) ID() string                                             { return "usbmidi" }
func (fakeUSBMIDI) Discover(context.Context) ([]transport.Endpoint, error) { return nil, nil }
func (fakeUSBMIDI) Pair(context.Context, string) error                     { return nil }
func (fakeUSBMIDI) Connect(context.Context, string) error                  { return nil }
func (fakeUSBMIDI) Disconnect(context.Context, string) error               { return nil }
func (fakeUSBMIDI) Send(context.Context, string, transport.Event) error    { return nil }
func (fakeUSBMIDI) Listen(context.Context, string) (<-chan transport.Event, error) {
	return make(chan transport.Event), nil
}

func ip(n int) *int { return &n }

// rolandProfileDef is an SL-2-like definition carrying a USB profile with a
// system region, a repeated patches region, and a couple of params.
func rolandProfileDef() *device.Definition {
	return &device.Definition{
		ID:        "sl-2",
		Name:      "Boss SL-2",
		Transport: "blemidi",
		USB: &device.USBProfile{
			Protocol:  device.USBProtocolRoland,
			Transport: device.USBTransportMIDI,
			AddrBytes: 4,
			SizeBytes: 4,
			Identity:  &device.USBIdentity{Mfg: 0x41, Model: "00 00 00 00 1D", Device: 0x10},
			Regions: map[string]device.Region{
				"system":  {Base: 0x10000000},
				"patches": {Base: 0x20100000, Count: 88, Stride: 0x00100000},
			},
			Params: []device.USBParam{
				{Name: "tempo", Region: "system", Addr: 0x00, Enc: "int4x4", Min: ip(400), Max: ip(3000)},
				{Name: "midi_channel", Region: "system", Addr: 0x08, Enc: "int1x7", Min: ip(0), Max: ip(10)},
			},
		},
	}
}

// neuroProfileDef is an EQ2-like definition with a HID profile: a repeated
// presets region and no params.
func neuroProfileDef() *device.Definition {
	return &device.Definition{
		ID:        "eq-2",
		Name:      "Source Audio EQ2",
		Transport: "blemidi",
		USB: &device.USBProfile{
			Protocol:  device.USBProtocolNeuro,
			Transport: device.USBTransportHID,
			Regions: map[string]device.Region{
				"presets": {Base: 0x080000, Count: 128, Stride: 0x1000},
			},
		},
	}
}

// usbToolNames returns the names AddUSBDeviceTool would register for a binding,
// honouring the write gate, plus the full candidate set for assertions.
func toolNameSet(specs []usbDeviceTool, writesAllowed bool) map[string]bool {
	out := map[string]bool{}
	for _, t := range specs {
		if t.write && !writesAllowed {
			continue
		}
		out[t.name] = true
	}
	return out
}

func TestUSBDeviceToolsRoland(t *testing.T) {
	s := &Server{eng: engine.New(device.NewRegistry())}
	specs := s.usbDeviceTools("sl2", rolandProfileDef().USB)

	writeNames := map[string]bool{}
	all := map[string]bool{}
	for _, sp := range specs {
		all[sp.name] = true
		if sp.write {
			writeNames[sp.name] = true
		}
	}

	wantRead := []string{
		"sl2_read", "sl2_get_param", "sl2_read_params",
		"sl2_list", "sl2_read_block", "sl2_read_system", "sl2_list_patterns",
	}
	for _, n := range wantRead {
		if !all[n] {
			t.Fatalf("missing read tool %q (have %v)", n, all)
		}
		if writeNames[n] {
			t.Fatalf("%q should not be a write tool", n)
		}
	}
	for _, n := range []string{"sl2_set_param", "sl2_recall_pattern", "sl2_write_pattern"} {
		if !writeNames[n] {
			t.Fatalf("expected %q to be a gated write tool (have writes %v)", n, writeNames)
		}
	}

	// With writes disabled, the gated tools are not exposed.
	off := toolNameSet(specs, false)
	for _, n := range []string{"sl2_set_param", "sl2_recall_pattern", "sl2_write_pattern"} {
		if off[n] {
			t.Fatalf("%q must not be exposed when writes are off", n)
		}
	}
	if !off["sl2_get_param"] || !off["sl2_read_system"] {
		t.Fatalf("read tools must remain exposed when writes are off: %v", off)
	}
	// With writes enabled, they are.
	on := toolNameSet(specs, true)
	if !on["sl2_set_param"] || !on["sl2_recall_pattern"] {
		t.Fatalf("write tools must be exposed when writes are on: %v", on)
	}
}

func TestUSBDeviceToolsNeuro(t *testing.T) {
	s := &Server{eng: engine.New(device.NewRegistry())}
	specs := s.usbDeviceTools("eq2", neuroProfileDef().USB)
	all := map[string]bool{}
	write := map[string]bool{}
	for _, sp := range specs {
		all[sp.name] = true
		if sp.write {
			write[sp.name] = true
		}
	}
	for _, n := range []string{"eq2_read", "eq2_list", "eq2_read_block", "eq2_list_presets", "eq2_read_preset"} {
		if !all[n] {
			t.Fatalf("missing tool %q (have %v)", n, all)
		}
	}
	// No params -> no get/set/read_params.
	for _, n := range []string{"eq2_get_param", "eq2_set_param", "eq2_read_params"} {
		if all[n] {
			t.Fatalf("%q should not exist without mapped params", n)
		}
	}
	if !write["eq2_select_preset"] {
		t.Fatalf("eq2_select_preset must be a gated write tool")
	}
}

// TestBundledProfilesGenerateTools loads the SHIPPED device definitions and
// confirms their authored usb: profiles produce the expected semantic tool set
// — the end-to-end check that the YAML data layer drives tool generation.
func TestBundledProfilesGenerateTools(t *testing.T) {
	reg, err := device.LoadBundled()
	if err != nil {
		t.Fatalf("load bundled: %v", err)
	}
	s := &Server{eng: engine.New(reg)}

	cases := []struct {
		device string
		want   []string
	}{
		{"sl-2", []string{"sl2_read", "sl2_get_param", "sl2_read_system", "sl2_list_patterns", "sl2_set_param", "sl2_recall_pattern", "sl2_write_pattern"}},
		{"eq-2", []string{"eq2_read", "eq2_list_presets", "eq2_read_preset", "eq2_select_preset"}},
		{"ml10x", []string{"ml10x_read", "ml10x_list", "ml10x_get_preset"}},
	}
	for _, c := range cases {
		def, ok := reg.Get(c.device)
		if !ok || def.USB == nil {
			t.Fatalf("bundled %q has no usb profile", c.device)
		}
		logical := strings.ReplaceAll(c.device, "-", "")
		got := map[string]bool{}
		for _, sp := range s.usbDeviceTools(logical, def.USB) {
			got[sp.name] = true
		}
		for _, n := range c.want {
			if !got[n] {
				t.Errorf("%s: missing generated tool %q (have %v)", c.device, n, got)
			}
		}
	}
}

func TestUSBWritesAllowedTwoKey(t *testing.T) {
	cases := []struct {
		global, writable, want bool
	}{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{true, true, true},
	}
	for _, c := range cases {
		s := &Server{usbAllowWrites: c.global}
		b := engine.Binding{USB: &engine.USBSurface{Transport: "usbmidi", Writable: c.writable}}
		if got := s.usbWritesAllowed(b); got != c.want {
			t.Fatalf("usbWritesAllowed(global=%v, writable=%v) = %v, want %v", c.global, c.writable, got, c.want)
		}
	}
}

func TestUSBWriteGateBlocksRealWrite(t *testing.T) {
	reg := device.NewRegistry()
	if err := reg.AddDefinition(rolandProfileDef()); err != nil {
		t.Fatalf("add def: %v", err)
	}
	eng := engine.New(reg, fakeUSBMIDI{})
	if err := eng.Bind(engine.Binding{Logical: "sl2usb", DeviceID: "sl-2", USB: &engine.USBSurface{Transport: "usbmidi", Endpoint: "SL-2", Writable: true}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Writes are NOT globally enabled, so a real usb_write is refused even
	// though the binding is writable.
	s := New(eng) // default: usbAllowWrites=false

	res := call(t, s.handleUSBWrite, map[string]any{
		"device": "sl2usb", "addr": "0x10000000", "data": []int{1}, "dry_run": false,
	})
	if !res.IsError || !strings.Contains(resultText(res), "usb writes are disabled") {
		t.Fatalf("expected a write-gate refusal, got: %s", resultText(res))
	}

	// A dry run is always allowed and returns the frame.
	res = call(t, s.handleUSBWrite, map[string]any{
		"device": "sl2usb", "addr": "0x10000000", "data": []int{1}, "dry_run": true,
	})
	if res.IsError || !strings.Contains(resultText(res), "dry_run") {
		t.Fatalf("dry run should succeed, got: %s", resultText(res))
	}
}

func TestBindDeviceRoutesUSB(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := device.NewRegistry()
	if err := reg.AddDefinition(rolandProfileDef()); err != nil {
		t.Fatalf("add def: %v", err)
	}
	eng := engine.New(reg, fakeUSBMIDI{})
	s := New(eng, WithUSBAllowWrites(true))

	res := call(t, s.handleBindDevice, map[string]any{
		"logical": "sl2usb", "endpoint": "SL-2", "device": "sl-2",
		"transport": "usbmidi", "writable": true,
	})
	if res.IsError {
		t.Fatalf("bind failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "USB tool family") {
		t.Fatalf("bind message should mention the USB tool family: %s", resultText(res))
	}
	if !eng.IsUSBBinding("sl2usb") {
		t.Fatalf("sl2usb should be a USB binding")
	}
}

// TestBindDeviceMergesSurfaces is the one-logical-two-surfaces model: binding a
// control surface and then a USB surface under the SAME logical accrues both
// onto one binding, so the pedal carries control_<logical> and the USB tool
// family under one name (no separate "sl2usb" logical needed).
func TestBindDeviceMergesSurfaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := device.NewRegistry()
	if err := reg.AddDefinition(rolandProfileDef()); err != nil {
		t.Fatalf("add def: %v", err)
	}
	eng := engine.New(reg, fakeBLE{}, fakeUSBMIDI{})
	s := New(eng)

	// 1) control surface over BLE.
	res := call(t, s.handleBindDevice, map[string]any{
		"logical": "sl2", "endpoint": "BLE:AA", "channel": 5, "device": "sl-2",
	})
	if res.IsError {
		t.Fatalf("control bind failed: %s", resultText(res))
	}

	// 2) USB editor surface over usbmidi, SAME logical — must merge, not replace.
	res = call(t, s.handleBindDevice, map[string]any{
		"logical": "sl2", "endpoint": "SL-2", "device": "sl-2", "transport": "usbmidi",
	})
	if res.IsError {
		t.Fatalf("usb bind failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "alongside control_sl2") {
		t.Fatalf("usb bind should note it sits alongside the control tool: %s", resultText(res))
	}

	b, ok := eng.BindingFor("sl2")
	if !ok {
		t.Fatalf("sl2 not bound")
	}
	if !b.HasControl() || b.Endpoint != "BLE:AA" || b.Channel != 5 {
		t.Fatalf("control surface lost after USB merge: %+v", b)
	}
	if !b.HasUSB() || b.USB.Transport != "usbmidi" || b.USB.Endpoint != "SL-2" {
		t.Fatalf("usb surface missing/wrong after merge: %+v", b)
	}

	// list_bindings should show one logical carrying both surfaces.
	res = call(t, s.handleListBindings, map[string]any{})
	txt := resultText(res)
	if !strings.Contains(txt, "endpoint=\"BLE:AA\"") || !strings.Contains(txt, "usb=usbmidi") {
		t.Fatalf("list_bindings should show both surfaces for sl2: %s", txt)
	}
}
