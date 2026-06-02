package engine

import (
	"bytes"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
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
