package mcpserver

import (
	"math"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
)

// probeTestServer builds a server whose audio store is pre-filled with a known
// tone, plus a bound testpedal so the device-control settings path is testable.
// There is no brain hub, so the note-playing and raw-cc paths (which need a
// connected ProbeMidiBrain) are deliberately not exercised here — those are
// covered by the live loop (scripts/sound-loop.sh).
func probeTestServer(t *testing.T, toneHz float64) (*Server, *audiotap.Store) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	reg := device.NewRegistry()
	def := &device.DeviceType{
		ID:        "testsynth",
		Name:      "Test Synth",
		Transport: "blemidi",
		Controls: []device.Control{
			{Name: "brightness", Type: device.ControlCC, CC: ccPtr(74), Value: device.ValueSpec{Type: device.ValueRange}},
		},
	}
	if err := reg.AddDefinition(def); err != nil {
		t.Fatalf("add definition: %v", err)
	}
	eng := engine.New(reg, fakeBLE{})

	store := audiotap.NewStore()
	store.Connect("test-tap")
	store.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 1, SampleRate: 12000, Source: "ProbeAudioTap"})
	store.SetFeatures(0.5, 0.7)
	const sr = 12000.0
	samples := make([]float32, 8192)
	for i := range samples {
		samples[i] = float32(0.6 * math.Sin(2*math.Pi*toneHz*float64(i)/sr))
	}
	store.AppendAudio(samples)

	audioReg := audiotap.NewRegistry()
	audioReg.Adopt("test-tap", store)
	s := New(eng, WithAudioTap(audioReg))

	d, _ := eng.DeviceFor("synth")
	d.Name = "synth"
	d.DeviceID = "testsynth"
	d.Endpoint = "ep1"
	if err := eng.Bind(d); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return s, store
}

// TestProbeSoundReadsAnalysis verifies the compound tool surfaces the trusted
// analysis (the same numbers as get_audio_tap) in one call — here with no notes,
// so it is a pure read of the pre-filled A4 tone.
func TestProbeSoundReadsAnalysis(t *testing.T) {
	s, _ := probeTestServer(t, 440)

	res := call(t, s.handleProbeSound, map[string]any{})
	if res.IsError {
		t.Fatalf("probe_sound failed: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "A4") {
		t.Fatalf("text missing detected pitch A4:\n%s", text)
	}

	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want map", res.StructuredContent)
	}
	snap, ok := sc["snapshot"].(audiotap.Snapshot)
	if !ok {
		t.Fatalf("snapshot is %T, want audiotap.Snapshot", sc["snapshot"])
	}
	if snap.Analysis == nil {
		t.Fatal("expected analysis block in snapshot")
	}
	if snap.Analysis.Note != "A4" {
		t.Fatalf("analysis note = %q, want A4 (f0=%.1f)", snap.Analysis.Note, snap.Analysis.F0Hz)
	}
}

// TestProbeSoundAppliesDeviceControl verifies the settings[] device-control path
// applies the change (and reports it) before reading the analysis.
func TestProbeSoundAppliesDeviceControl(t *testing.T) {
	s, _ := probeTestServer(t, 440)

	res := call(t, s.handleProbeSound, map[string]any{
		"settings": []map[string]any{
			{"device": "synth", "control": "brightness", "value": 100},
		},
	})
	if res.IsError {
		t.Fatalf("probe_sound failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "applied 1 setting") {
		t.Fatalf("text missing applied-setting summary:\n%s", resultText(res))
	}
	sc := res.StructuredContent.(map[string]any)
	applied, ok := sc["settings_applied"].([]map[string]any)
	if !ok || len(applied) != 1 {
		t.Fatalf("settings_applied = %#v, want one entry", sc["settings_applied"])
	}
	if applied[0]["control"] != "brightness" {
		t.Fatalf("applied entry = %#v, want brightness", applied[0])
	}
}

// TestProbeSoundRejectsEmptySetting verifies a setting that is neither a device
// control nor a raw cc is rejected with a JSON-pointer the model can act on.
func TestProbeSoundRejectsEmptySetting(t *testing.T) {
	s, _ := probeTestServer(t, 440)

	res := call(t, s.handleProbeSound, map[string]any{
		"settings": []map[string]any{{"value": 1}},
	})
	if !res.IsError {
		t.Fatalf("expected error for empty setting, got: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "/settings/0") {
		t.Fatalf("error missing /settings/0 pointer:\n%s", resultText(res))
	}
}
