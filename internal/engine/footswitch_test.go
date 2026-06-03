package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/scene"
)

const settleDeviceYAML = `id: pedal
name: Test Pedal
transport: fake
settle_ms: 80
controls:
  - name: preset
    type: program_change
    value: { type: range, min: 0, max: 127 }
  - name: level
    type: cc
    cc: 17
    value: { type: range, min: 0, max: 127 }
  - name: mode
    type: cc
    cc: 28
    value: { type: enum, values: { off: 0, on: 127 } }
`

func newFootswitchTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pedal.yaml"), []byte(settleDeviceYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	eng := New(reg, newFakeTransport())
	// Bind on MIDI channel 2 (binding channel is 0-based -> wire nibble 1 ->
	// firmware-facing channel 2).
	if err := eng.Bind(Binding{Logical: "pedal", Endpoint: "EP1", Channel: 1, DeviceID: "pedal"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng
}

func TestCompileFootswitchSceneOrderingAndSettle(t *testing.T) {
	eng := newFootswitchTestEngine(t)

	sc := &scene.Scene{
		Name: "Verse",
		Devices: map[string]map[string]any{
			"pedal": {"preset": 5, "level": 100, "mode": "on"},
		},
	}
	trig := &FootswitchTrigger{Type: "program_change", Channel: 1, Number: 3}

	fs, warnings, err := eng.CompileFootswitchScene(sc, FootswitchCompileOptions{Bank: 1, Trigger: trig})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if fs.ID != "verse" || fs.Name != "Verse" || fs.Bank != 1 {
		t.Fatalf("metadata = %+v", fs)
	}
	if fs.Trigger == nil || fs.Trigger.Type != "program_change" || fs.Trigger.Number != 3 {
		t.Fatalf("trigger = %+v", fs.Trigger)
	}

	if len(fs.Events) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(fs.Events), fs.Events)
	}

	// Program change must come first and carry the per-device settle delay.
	pc := fs.Events[0]
	if pc.Type != "program_change" || pc.Channel != 2 || pc.Program == nil || *pc.Program != 5 {
		t.Fatalf("event[0] = %+v", pc)
	}
	if pc.DelayMs != 80 {
		t.Fatalf("settle not baked onto the program change: %+v", pc)
	}

	// Then the two CCs in sorted control order: level (17) then mode (28).
	if e := fs.Events[1]; e.Type != "cc" || e.Controller == nil || *e.Controller != 17 || e.Value == nil || *e.Value != 100 {
		t.Fatalf("event[1] = %+v", e)
	}
	if e := fs.Events[2]; e.Type != "cc" || e.Controller == nil || *e.Controller != 28 || e.Value == nil || *e.Value != 127 {
		t.Fatalf("event[2] = %+v", e)
	}
}

const oscDeviceYAML = `id: x32mini
name: Test X32
transport: osc
controls:
  - name: ch01_fader
    type: osc
    address: /ch/01/mix/fader
    value: { type: float, min: 0.0, max: 1.0 }
  - name: ch01_on
    type: osc
    address: /ch/01/mix/on
    value: { type: enum, values: { off: 0, on: 1 } }
  - name: scene_recall
    type: osc
    address: /-action/goscene
    value: { type: int, min: 0, max: 99 }
`

// fakeOSCTransport is a no-op transport that claims the "osc" id so OSC
// devices can be bound in tests (binding requires a transport for the device's
// transport id). The footswitch compile path never sends through it.
type fakeOSCTransport struct{ fakeTransport }

func (f *fakeOSCTransport) ID() string { return "osc" }

func newOSCFootswitchTestEngine(t *testing.T, endpoint string) *Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x32mini.yaml"), []byte(oscDeviceYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	eng := New(reg, &fakeOSCTransport{*newFakeTransport()})
	// Channel is irrelevant for OSC; the UDP target comes from the endpoint.
	if err := eng.Bind(Binding{Logical: "x32", Endpoint: endpoint, Channel: 0, DeviceID: "x32mini"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng
}

func TestCompileFootswitchSceneOSC(t *testing.T) {
	eng := newOSCFootswitchTestEngine(t, "192.168.2.50:10023")

	sc := &scene.Scene{
		Name: "Mixer Recall",
		Devices: map[string]map[string]any{
			"x32": {"ch01_fader": 0.75, "ch01_on": "on", "scene_recall": 3},
		},
	}

	fs, warnings, err := eng.CompileFootswitchScene(sc, FootswitchCompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(fs.Events) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(fs.Events), fs.Events)
	}

	// Controls are emitted in sorted name order: ch01_fader, ch01_on, scene_recall.
	fader := fs.Events[0]
	if fader.Type != "osc" || fader.OSCAddr != "/ch/01/mix/fader" || fader.OSCTypes != "f" {
		t.Fatalf("fader event = %+v", fader)
	}
	if fader.Host != "192.168.2.50" || fader.Port != 10023 {
		t.Fatalf("fader target = %s:%d", fader.Host, fader.Port)
	}
	if len(fader.OSCArgs) != 1 {
		t.Fatalf("fader args = %+v", fader.OSCArgs)
	}
	if f, ok := fader.OSCArgs[0].(float32); !ok || f != 0.75 {
		t.Fatalf("fader arg = %#v, want float32(0.75)", fader.OSCArgs[0])
	}

	on := fs.Events[1]
	if on.Type != "osc" || on.OSCAddr != "/ch/01/mix/on" || on.OSCTypes != "i" {
		t.Fatalf("on event = %+v", on)
	}
	if i, ok := on.OSCArgs[0].(int32); !ok || i != 1 {
		t.Fatalf("on arg = %#v, want int32(1)", on.OSCArgs[0])
	}

	recall := fs.Events[2]
	if recall.Type != "osc" || recall.OSCAddr != "/-action/goscene" || recall.OSCTypes != "i" {
		t.Fatalf("recall event = %+v", recall)
	}
	if i, ok := recall.OSCArgs[0].(int32); !ok || i != 3 {
		t.Fatalf("recall arg = %#v, want int32(3)", recall.OSCArgs[0])
	}
}

func TestCompileFootswitchSceneOSCBareHostDefaultsPort(t *testing.T) {
	eng := newOSCFootswitchTestEngine(t, "x32.local")
	sc := &scene.Scene{
		Name:    "Recall",
		Devices: map[string]map[string]any{"x32": {"scene_recall": 1}},
	}
	fs, warnings, err := eng.CompileFootswitchScene(sc, FootswitchCompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(fs.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(fs.Events))
	}
	if fs.Events[0].Host != "x32.local" || fs.Events[0].Port != 10023 {
		t.Fatalf("target = %s:%d, want x32.local:10023", fs.Events[0].Host, fs.Events[0].Port)
	}
}

func TestCompileFootswitchSceneOSCMissingEndpointWarns(t *testing.T) {
	eng := newOSCFootswitchTestEngine(t, "")
	sc := &scene.Scene{
		Name:    "Recall",
		Devices: map[string]map[string]any{"x32": {"scene_recall": 1}},
	}
	fs, warnings, err := eng.CompileFootswitchScene(sc, FootswitchCompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(fs.Events) != 0 {
		t.Fatalf("want 0 events (device skipped), got %d", len(fs.Events))
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning about the missing OSC endpoint, got %v", warnings)
	}
}

func TestCompileFootswitchSceneUnboundDevice(t *testing.T) {
	eng := newFootswitchTestEngine(t)
	sc := &scene.Scene{
		Name:    "Bad",
		Devices: map[string]map[string]any{"ghost": {"level": 1}},
	}
	if _, _, err := eng.CompileFootswitchScene(sc, FootswitchCompileOptions{}); err == nil {
		t.Fatal("expected an error for an unbound device")
	}
}
