package device

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func intp(n int) *int { return &n }

func TestUSBProfileValidate(t *testing.T) {
	good := func() *USBProfile {
		return &USBProfile{
			Protocol:  USBProtocolRoland,
			Transport: USBTransportMIDI,
			Identity:  &USBIdentity{Mfg: 0x41, Model: "00 00 00 00 1D", Device: 0x10},
			AddrBytes: 4,
			SizeBytes: 4,
			Regions: map[string]Region{
				"system":  {Base: 0x10000000},
				"temp":    {Base: 0x20000000},
				"patches": {Base: 0x20100000, Count: 88, Stride: 0x00100000},
			},
			Params: []USBParam{
				{Name: "midi_channel", Region: "system", Addr: 0x08, Enc: "int1x7", Min: intp(0), Max: intp(10)},
				{Name: "tempo", Region: "system", Addr: 0x00, Enc: "int4x4", Min: intp(400), Max: intp(3000)},
				{Name: "patch_name", Region: "temp", Addr: 0x00, Enc: "ascii16"},
			},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*USBProfile)
		wantErr bool
	}{
		{name: "good", mutate: func(*USBProfile) {}},
		{name: "unknown protocol", mutate: func(p *USBProfile) { p.Protocol = "nope" }, wantErr: true},
		{name: "unknown transport", mutate: func(p *USBProfile) { p.Transport = "serial" }, wantErr: true},
		{name: "sysex without identity", mutate: func(p *USBProfile) { p.Identity = nil }, wantErr: true},
		{name: "hid needs no identity", mutate: func(p *USBProfile) {
			p.Protocol = USBProtocolNeuro
			p.Transport = USBTransportHID
			p.Identity = nil
		}},
		{name: "unknown encoding", mutate: func(p *USBProfile) { p.Params[0].Enc = "int9x9" }, wantErr: true},
		{name: "param references unknown region", mutate: func(p *USBProfile) { p.Params[0].Region = "ghost" }, wantErr: true},
		{name: "duplicate param", mutate: func(p *USBProfile) { p.Params[1].Name = "midi_channel" }, wantErr: true},
		{name: "empty param name", mutate: func(p *USBProfile) { p.Params[0].Name = "" }, wantErr: true},
		{name: "missing enc", mutate: func(p *USBProfile) { p.Params[0].Enc = "" }, wantErr: true},
		{name: "min greater than max", mutate: func(p *USBProfile) { p.Params[0].Min, p.Params[0].Max = intp(20), intp(10) }, wantErr: true},
		{name: "count without stride", mutate: func(p *USBProfile) {
			r := p.Regions["patches"]
			r.Stride = 0
			p.Regions["patches"] = r
		}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := good()
			tc.mutate(p)
			err := p.validate("sl-2")
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDefinitionValidateWithUSB(t *testing.T) {
	d := &DeviceType{
		ID:        "sl-2",
		Transport: "blemidi",
		USB: &USBProfile{
			Protocol:  USBProtocolRoland,
			Transport: USBTransportMIDI,
			Identity:  &USBIdentity{Mfg: 0x41, Model: "00 00 00 00 1D"},
			Params:    []USBParam{{Name: "tempo", Enc: "int4x4"}},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid definition with usb rejected: %v", err)
	}

	// A bad USB profile fails the whole definition.
	d.USB.Params[0].Enc = "bogus"
	if err := d.Validate(); err == nil {
		t.Fatal("expected definition validation to fail on a bad usb encoding")
	}
}

// TestBundledUSBProfiles asserts the shipped device definitions carry the USB
// profiles the semantic tools are generated from (the data layer that turns the
// USB subsystem on), with the protocol/transport/region/param shape from the
// research docs — and that the H90 deliberately has none.
func TestBundledUSBProfiles(t *testing.T) {
	reg, err := LoadBundled()
	if err != nil {
		t.Fatalf("load bundled: %v", err)
	}

	cases := []struct{ id, protocol, transport string }{
		{"sl-2", USBProtocolRoland, USBTransportMIDI},
		{"eq-2", USBProtocolNeuro, USBTransportHID},
		{"ml10x", USBProtocolMorningstar, USBTransportMIDI},
		{"opus", USBProtocolTorpedo, USBTransportHID},
	}
	for _, c := range cases {
		d, ok := reg.Get(c.id)
		if !ok {
			t.Fatalf("no bundled definition %q", c.id)
		}
		if d.USB == nil {
			t.Fatalf("%q has no usb profile", c.id)
		}
		if d.USB.Protocol != c.protocol {
			t.Errorf("%q usb protocol = %q, want %q", c.id, d.USB.Protocol, c.protocol)
		}
		if d.USB.Transport != c.transport {
			t.Errorf("%q usb transport = %q, want %q", c.id, d.USB.Transport, c.transport)
		}
	}

	// The H90 is the resolved negative result: no USB readback surface.
	if d, ok := reg.Get("h90"); !ok || d.USB != nil {
		t.Errorf("h90 should have no usb profile, got %+v", d.USB)
	}

	// SL-2: the 88-pattern region + a curated nibble-packed param.
	sl2, _ := reg.Get("sl-2")
	if r, ok := sl2.USB.Regions["patches"]; !ok || r.Count != 88 || r.Base != 0x20100000 || r.Stride != 0x00100000 {
		t.Errorf("sl-2 patches region = %+v", r)
	}
	if p, ok := sl2.USB.Param("tempo"); !ok || p.Enc != "int4x4" || p.Region != "system" {
		t.Errorf("sl-2 tempo param = %+v ok=%v", p, ok)
	}
	if _, ok := sl2.USB.Param("slicer1_pattern"); !ok {
		t.Errorf("sl-2 missing slicer1_pattern param")
	}

	// EQ2: the 128-preset region (HID has no params yet — offsets undecoded).
	eq2, _ := reg.Get("eq-2")
	if r, ok := eq2.USB.Regions["presets"]; !ok || r.Count != 128 || r.Stride != 0x1000 || r.Base != 0x080000 {
		t.Errorf("eq-2 presets region = %+v", r)
	}

	// ML10X: 4 banks modeled so op2 0x16 + bank is reachable as a repeated read.
	ml10x, _ := reg.Get("ml10x")
	if r, ok := ml10x.USB.Regions["banks"]; !ok || r.Count != 4 {
		t.Errorf("ml10x banks region = %+v", r)
	}
}

// TestUSBProfileYAMLHexAddresses confirms yaml.v3 resolves 0x.. hex ints so the
// research addresses can be written verbatim in the definitions.
func TestUSBProfileYAMLHexAddresses(t *testing.T) {
	src := `
protocol: roland-address-sysex
transport: usbmidi
identity: { mfg: 0x41, model: "00 00 00 00 1D", device: 0x10 }
addr_bytes: 4
size_bytes: 4
regions:
  system:  { base: 0x10000000 }
  patches: { base: 0x20100000, count: 88, stride: 0x00100000 }
params:
  - { name: midi_channel, region: system, addr: 0x08, enc: int1x7, min: 0, max: 10 }
  - { name: tempo,        region: system, addr: 0x00, enc: int4x4, min: 400, max: 3000 }
`
	var p USBProfile
	if err := yaml.Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := p.validate("sl-2"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if p.Identity.Mfg != 0x41 || p.Identity.Device != 0x10 {
		t.Fatalf("identity hex not resolved: %+v", p.Identity)
	}
	if got := p.Regions["patches"]; got.Base != 0x20100000 || got.Stride != 0x00100000 || got.Count != 88 {
		t.Fatalf("patches region hex not resolved: %+v", got)
	}
	mc, ok := p.Param("midi_channel")
	if !ok || mc.Addr != 0x08 || mc.Region != "system" {
		t.Fatalf("midi_channel param wrong: %+v ok=%v", mc, ok)
	}
	if names := p.ParamNames(); len(names) != 2 || names[0] != "midi_channel" || names[1] != "tempo" {
		t.Fatalf("ParamNames = %v", names)
	}
}
