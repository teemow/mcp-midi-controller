package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/scene"
	"github.com/teemow/midi-device/device"
)

// connDeviceYAML is a device type whose control transport is the in-memory
// "fake" transport, so a flat single-connection device sends over it.
const connDeviceYAML = `id: ov
name: Conn Dev
transport: fake
settle_ms: 0
controls:
  - name: level
    type: cc
    cc: 7
    value: { type: range, min: 0, max: 127 }
  - name: preset
    type: program_change
    value: { type: range, min: 0, max: 127 }
`

func newConnTestEngine(t *testing.T) (*Engine, *fakeTransport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ov.yaml"), []byte(connDeviceYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	ft := newFakeTransport() // ID "fake"
	eng := New(reg, ft)
	// Flat single-connection device: its one connection rides the device type's
	// transport ("fake").
	d := Device{Name: "amp", DeviceID: "ov", Endpoint: "EP1", Channel: 0}
	if err := eng.Bind(d); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng, ft
}

// TestSetControlRoutesToControlConnection guards the engine.go send path: a flat
// single-connection device resolves its endpoint/transport from its device type
// and sends.
func TestSetControlRoutesToControlConnection(t *testing.T) {
	eng, ft := newConnTestEngine(t)
	if err := eng.SetControl(context.Background(), "amp", "level", 100); err != nil {
		t.Fatalf("SetControl: %v", err)
	}
	ft.mu.Lock()
	n := len(ft.sent)
	ft.mu.Unlock()
	if n != 1 {
		t.Fatalf("want 1 sent event over the control connection, got %d", n)
	}
}

// TestRecallRoutesToControlConnection guards the recall.go send path.
func TestRecallRoutesToControlConnection(t *testing.T) {
	eng, ft := newConnTestEngine(t)
	sc := &scene.Scene{
		Name:    "Verse",
		Devices: map[string]map[string]any{"amp": {"level": 100, "preset": 3}},
	}
	if _, err := eng.RecallScene(context.Background(), sc, scene.Additive); err != nil {
		t.Fatalf("RecallScene: %v", err)
	}
	ft.mu.Lock()
	n := len(ft.sent)
	ft.mu.Unlock()
	if n != 2 {
		t.Fatalf("want 2 sent events over the control connection, got %d", n)
	}
}

// TestBindRejectsUnspokenTransport proves Bind rejects a connection on a
// transport the device type does not speak.
func TestBindRejectsUnspokenTransport(t *testing.T) {
	eng, _ := newConnTestEngine(t)
	err := eng.Bind(Device{
		Name:        "bad",
		DeviceID:    "ov",
		Connections: map[string]Connection{"usbmidi": {Endpoint: "X"}},
	})
	if err == nil {
		t.Fatalf("expected bind to reject a usb connection on a control-only device type")
	}
}

// TestDevicesFileRoundTrip checks devices.yaml persistence: the flat
// single-connection shorthand and the multi-transport connections map both
// survive a save/load round trip, and a missing file loads as no devices (not an
// error).
func TestDevicesFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.yaml")

	// Missing file: no devices, no error.
	got, err := LoadDevicesFile(path)
	if err != nil {
		t.Fatalf("load (missing): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 devices when no file present, got %d", len(got))
	}

	want := []Device{
		{
			Name:     "amp",
			DeviceID: "ov",
			Endpoint: "EP1",
			Channel:  5,
		},
		{
			Name:     "sl2",
			DeviceID: "sl-2",
			Connections: map[string]Connection{
				"blemidi": {Endpoint: "10:2E:AB", Channel: 5},
				"usbmidi": {Endpoint: "SL-2", Writable: true},
			},
		},
	}
	if err := SaveDevicesFile(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = LoadDevicesFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 devices, got %d: %+v", len(got), got)
	}
	// SaveDevicesFile sorts by logical name: amp, sl2.
	amp := got[0]
	if amp.ControlEndpoint() != "EP1" || amp.ControlChannel() != 5 {
		t.Fatalf("amp flat connection did not round-trip: %+v", amp)
	}
	if amp.HasUSB() {
		t.Fatalf("amp is control-only; expected no usb connection: %+v", amp)
	}
	if amp.Name != "amp" {
		t.Fatalf("amp name = %q, want %q", amp.Name, "amp")
	}
	sl2 := got[1]
	if sl2.ControlEndpoint() != "10:2E:AB" || sl2.ControlChannel() != 5 {
		t.Fatalf("sl2 control connection did not round-trip: %+v", sl2)
	}
	usbTr, usbConn, ok := sl2.USBConnection()
	if !ok || usbTr != "usbmidi" || usbConn.Endpoint != "SL-2" || !usbConn.Writable {
		t.Fatalf("sl2 usb connection did not round-trip: %+v", sl2)
	}
}
