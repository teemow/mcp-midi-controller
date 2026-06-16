package engine

import (
	"bytes"
	"testing"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

func iptr(v int) *int { return &v }

func mustResolve(t *testing.T, c *device.Control, raw any) device.Resolved {
	t.Helper()
	r, err := device.Resolve(c, raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

func TestRenderCC(t *testing.T) {
	c := &device.Control{Type: device.ControlCC, CC: iptr(17), Value: device.ValueSpec{Type: device.ValueRange}}
	evs, err := renderControl(nil, c, 4, mustResolve(t, c, float64(64)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	want := []byte{0xB4, 17, 64} // CC on channel 4 (0-based)
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
	if evs[0].Channel != 4 {
		t.Fatalf("channel = %d, want 4", evs[0].Channel)
	}
}

func TestRenderCCChannelOverride(t *testing.T) {
	// A control pinned to channel 16 (1-based) must ride wire channel 15,
	// ignoring the binding channel passed in.
	c := &device.Control{Type: device.ControlCC, CC: iptr(17), Channel: iptr(16), Value: device.ValueSpec{Type: device.ValueRange}}
	evs, err := renderControl(nil, c, 4, mustResolve(t, c, float64(64)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xBF, 17, 64}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
	if evs[0].Channel != 15 {
		t.Fatalf("channel = %d, want 15", evs[0].Channel)
	}
}

func TestRenderProgramChangeChannelOverride(t *testing.T) {
	c := &device.Control{Type: device.ControlProgramChange, Channel: iptr(3), Value: device.ValueSpec{Type: device.ValueRange, Max: f(127)}}
	evs, err := renderControl(nil, c, 1, mustResolve(t, c, float64(5)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xC2, 5} // pinned channel 3 (1-based) -> wire 2
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
}

func TestRenderProgramChange(t *testing.T) {
	c := &device.Control{Type: device.ControlProgramChange, Value: device.ValueSpec{Type: device.ValueRange, Max: f(127)}}
	evs, err := renderControl(nil, c, 1, mustResolve(t, c, float64(5)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xC1, 5}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
}

func TestRenderBankedProgramChange(t *testing.T) {
	// A preset control spanning >128 presets (banked): index 300 -> bank 2,
	// program 44, emitted as Bank Select MSB/LSB + Program Change.
	c := &device.Control{
		Type:  device.ControlProgramChange,
		Bank:  true,
		Value: device.ValueSpec{Type: device.ValueRange, Min: f(0), Max: f(789)},
	}
	evs, err := renderControl(nil, c, 1, mustResolve(t, c, float64(300)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wants := [][]byte{
		{0xB1, 0, 0},  // Bank Select MSB (bank 2 fits the LSB)
		{0xB1, 32, 2}, // Bank Select LSB
		{0xC1, 44},    // Program Change (300 % 128)
	}
	if len(evs) != len(wants) {
		t.Fatalf("got %d events, want %d", len(evs), len(wants))
	}
	for i, w := range wants {
		if !bytes.Equal(evs[i].Data, w) {
			t.Fatalf("event %d = % X, want % X", i, evs[i].Data, w)
		}
	}
}

func TestRenderNRPN(t *testing.T) {
	c := &device.Control{Type: device.ControlNRPN, NRPN: iptr(1000), Value: device.ValueSpec{Type: device.ValueRange, Max: f(16383)}}
	evs, err := renderControl(nil, c, 0, mustResolve(t, c, float64(8192)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(evs) != 4 {
		t.Fatalf("got %d events, want 4", len(evs))
	}
	wants := [][]byte{
		{0xB0, 99, byte((1000 >> 7) & 0x7F)},
		{0xB0, 98, byte(1000 & 0x7F)},
		{0xB0, 6, byte((8192 >> 7) & 0x7F)},
		{0xB0, 38, byte(8192 & 0x7F)},
	}
	for i, w := range wants {
		if !bytes.Equal(evs[i].Data, w) {
			t.Fatalf("event %d = % X, want % X", i, evs[i].Data, w)
		}
	}
}

func TestRenderSysEx(t *testing.T) {
	c := &device.Control{Type: device.ControlSysEx, SysEx: "F0 7D 01 %v F7", Value: device.ValueSpec{Type: device.ValueRange}}
	evs, err := renderControl(nil, c, 3, mustResolve(t, c, float64(42)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xF0, 0x7D, 0x01, 42, 0xF7}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
	if evs[0].Channel != 0 {
		t.Fatalf("sysex must carry no channel, got %d", evs[0].Channel)
	}
}

func TestRenderSysExRolandChecksum(t *testing.T) {
	// SL-2 temp-patch SLICER(1) PATTERN write (DT1). Checksum covers the
	// address + data bytes inside [ ]; verified against docs/research/sl-2.md
	// (the EXP_FUNC=04 example yields checksum 0x65).
	c := &device.Control{
		Type:  device.ControlSysEx,
		SysEx: "F0 41 10 00 00 00 00 1D 12 [ 10 00 00 07 %v ] %k F7",
		Value: device.ValueSpec{Type: device.ValueRange},
	}
	evs, err := renderControl(nil, c, 5, mustResolve(t, c, float64(4)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xF0, 0x41, 0x10, 0x00, 0x00, 0x00, 0x00, 0x1D, 0x12, 0x10, 0x00, 0x00, 0x07, 0x04, 0x65, 0xF7}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
}

func TestRenderOSC(t *testing.T) {
	fc := &device.Control{Type: device.ControlOSC, Address: "/ch/01/mix/fader", Value: device.ValueSpec{Type: device.ValueFloat, Min: f(0), Max: f(1)}}
	evs, err := renderControl(nil, fc, 0, mustResolve(t, fc, 0.5))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if evs[0].Kind != transport.OSCEvent || evs[0].OSCAddr != "/ch/01/mix/fader" {
		t.Fatalf("osc event = %+v", evs[0])
	}
	if arg, ok := evs[0].OSCArgs[0].(float32); !ok || arg != 0.5 {
		t.Fatalf("arg = %#v, want float32 0.5", evs[0].OSCArgs[0])
	}

	ic := &device.Control{Type: device.ControlOSC, Address: "/-action/goscene", Value: device.ValueSpec{Type: device.ValueInt, Min: f(0), Max: f(99)}}
	evs, _ = renderControl(nil, ic, 0, mustResolve(t, ic, float64(7)))
	if arg, ok := evs[0].OSCArgs[0].(int32); !ok || arg != 7 {
		t.Fatalf("int arg = %#v, want int32 7", evs[0].OSCArgs[0])
	}
}

func TestRenderParametricCC(t *testing.T) {
	c := &device.Control{Type: device.ControlCC, Parametric: true, Value: device.ValueSpec{Type: device.ValueRange}}
	evs, err := renderControl(nil, c, 2, mustResolve(t, c, map[string]any{"number": float64(74), "value": float64(100)}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []byte{0xB2, 74, 100}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("data = % X, want % X", evs[0].Data, want)
	}
}

func TestRenderParametricCCMissingNumber(t *testing.T) {
	c := &device.Control{Type: device.ControlCC, Parametric: true, Value: device.ValueSpec{Type: device.ValueRange}}
	_, err := renderControl(nil, c, 0, mustResolve(t, c, float64(100)))
	if err == nil {
		t.Fatalf("expected error for parametric CC without a number")
	}
}

func f(v float64) *float64 { return &v }
