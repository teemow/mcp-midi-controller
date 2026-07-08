package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/scene"
	"github.com/teemow/midi-device/device"
)

// newRecallTestEngine builds an engine with the settle pedal definition bound on
// channel 1 and returns both so a test can inspect the sent events.
func newRecallTestEngine(t *testing.T) (*Engine, *fakeTransport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pedal.yaml"), []byte(settleDeviceYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	ft := newFakeTransport()
	eng := New(reg, ft)
	if err := eng.Bind(Device{Name: "pedal", DeviceID: "pedal", Endpoint: "EP1", Channel: 1}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng, ft
}

func TestRecallSceneProgramChangeFirstAndSettle(t *testing.T) {
	eng, ft := newRecallTestEngine(t)
	sc := &scene.Scene{
		Name:    "Verse",
		Devices: map[string]map[string]any{"pedal": {"preset": 5, "level": 100, "mode": "on"}},
	}

	start := time.Now()
	warnings, err := eng.RecallScene(context.Background(), sc, scene.Additive)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	ft.mu.Lock()
	got := make([][]byte, len(ft.sent))
	for i, ev := range ft.sent {
		got[i] = ev.Data
	}
	ft.mu.Unlock()

	if len(got) != 3 {
		t.Fatalf("want 3 sent events, got %d: % X", len(got), got)
	}
	// Program change first (status 0xC1 on channel 1), program 5.
	if got[0][0] != 0xC1 || got[0][1] != 5 {
		t.Fatalf("event[0] = % X, want program change C1 05", got[0])
	}
	// Then the two CCs in sorted control order: level (cc17), mode (cc28).
	if got[1][0] != 0xB1 || got[1][1] != 17 || got[1][2] != 100 {
		t.Fatalf("event[1] = % X, want CC 17 = 100", got[1])
	}
	if got[2][0] != 0xB1 || got[2][1] != 28 || got[2][2] != 127 {
		t.Fatalf("event[2] = % X, want CC 28 = 127", got[2])
	}

	// settle_ms is 80 in the definition; recall must pause at least that long
	// between the program change and the CCs.
	if elapsed < 80*time.Millisecond {
		t.Fatalf("expected at least the 80ms settle delay, took %v", elapsed)
	}

	// Desired-state recorded.
	st := eng.State().Device("pedal")
	if st["preset"] == nil || st["level"] == nil || st["mode"] == nil {
		t.Fatalf("desired-state not recorded: %+v", st)
	}
}

func TestRecallSceneAdditiveVsExact(t *testing.T) {
	eng, _ := newRecallTestEngine(t)

	// Pre-existing desired-state the scene does not mention.
	eng.State().Set("pedal", "level", 42)

	sc := &scene.Scene{
		Name:    "Lead",
		Devices: map[string]map[string]any{"pedal": {"preset": 3}},
	}

	// Additive keeps the untouched "level".
	if _, err := eng.RecallScene(context.Background(), sc, scene.Additive); err != nil {
		t.Fatalf("additive recall: %v", err)
	}
	st := eng.State().Device("pedal")
	if st["level"] == nil {
		t.Fatalf("additive recall dropped the untouched control: %+v", st)
	}
	if st["preset"] == nil {
		t.Fatalf("additive recall did not apply preset: %+v", st)
	}

	// Exact resets the device to exactly the scene's values.
	if _, err := eng.RecallScene(context.Background(), sc, scene.Exact); err != nil {
		t.Fatalf("exact recall: %v", err)
	}
	st = eng.State().Device("pedal")
	if _, ok := st["level"]; ok {
		t.Fatalf("exact recall should have pruned the untouched control: %+v", st)
	}
	if st["preset"] == nil {
		t.Fatalf("exact recall did not apply preset: %+v", st)
	}
}

func TestRecallSceneUnboundDevice(t *testing.T) {
	eng, _ := newRecallTestEngine(t)
	sc := &scene.Scene{
		Name:    "Bad",
		Devices: map[string]map[string]any{"ghost": {"level": 1}},
	}
	if _, err := eng.RecallScene(context.Background(), sc, scene.Additive); err == nil {
		t.Fatal("expected an error recalling a scene that references an unbound device")
	}
}

func TestSaveSceneSnapshotAndFilter(t *testing.T) {
	eng, _ := newRecallTestEngine(t)
	eng.State().Set("pedal", "preset", 7)
	eng.State().Set("pedal", "level", 90)

	sc, err := eng.SaveScene("Snap", "desc", nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if sc.Description != "desc" {
		t.Fatalf("description = %q", sc.Description)
	}
	if len(sc.Devices["pedal"]) != 2 {
		t.Fatalf("want 2 controls snapshotted, got %+v", sc.Devices)
	}

	// Filtering to an unknown device is an error (likely a typo).
	if _, err := eng.SaveScene("Snap", "", []string{"nope"}); err == nil {
		t.Fatal("expected error filtering to a device with no state")
	}

	// Filtering to the real device keeps only it.
	sc, err = eng.SaveScene("Snap", "", []string{"pedal"})
	if err != nil {
		t.Fatalf("filtered save: %v", err)
	}
	if len(sc.Devices) != 1 || sc.Devices["pedal"] == nil {
		t.Fatalf("filtered snapshot = %+v", sc.Devices)
	}
}

func TestDesiredStateSaveLoadRoundTrip(t *testing.T) {
	s := NewDesiredState()
	s.Set("amp", "gain", 64)
	s.Set("amp", "mode", "on")

	path := filepath.Join(t.TempDir(), "state.json")
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := NewDesiredState()
	if err := s2.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	dev := s2.Device("amp")
	if dev["mode"] != "on" {
		t.Fatalf("mode = %#v, want \"on\"", dev["mode"])
	}
	// JSON numbers reload as float64.
	if g, ok := dev["gain"].(float64); !ok || g != 64 {
		t.Fatalf("gain = %#v, want float64(64)", dev["gain"])
	}

	// Loading a missing file is a no-op (not an error).
	if err := NewDesiredState().Load(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("load missing: %v", err)
	}
}
