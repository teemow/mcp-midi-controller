package mcpserver

import (
	"math"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
)

// fillTone (re)fills the store with a fresh window of a single tone at freq/amp.
// Connect clears the previous window (and format), so the format is re-set so
// the snapshot has a sample rate and thus an Analysis/Spectral block.
func fillTone(store *audiotap.Store, freq, amp float64, n int) {
	store.Connect("test-tap")
	store.SetFormat(audiotap.Format{Encoding: "f32le", Channels: 1, SampleRate: 12000, Source: "ProbeAudioTap"})
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/12000.0))
	}
	store.AppendAudio(samples)
}

// deltaMap pulls the structured delta map out of a compare_audio result.
func deltaMap(t *testing.T, res any) map[string]any {
	t.Helper()
	sc, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want map", res)
	}
	d, ok := sc["delta"].(map[string]any)
	if !ok {
		t.Fatalf("delta is %T, want map", sc["delta"])
	}
	return d
}

// TestCaptureAndCompareLouder captures a quiet then a loud tone and asserts the
// compare reports a positive loudness delta — the live-loop "louder CC" gate.
func TestCaptureAndCompareLouder(t *testing.T) {
	s, store := probeTestServer(t, 440)

	fillTone(store, 440, 0.2, 8192)
	if res := call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "a"}); res.IsError {
		t.Fatalf("capture a failed: %s", resultText(res))
	}
	fillTone(store, 440, 0.8, 8192)
	if res := call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "b"}); res.IsError {
		t.Fatalf("capture b failed: %s", resultText(res))
	}

	res := call(t, s.handleCompareAudio, map[string]any{"a": "a", "b": "b"})
	if res.IsError {
		t.Fatalf("compare failed: %s", resultText(res))
	}
	d := deltaMap(t, res.StructuredContent)
	rms, ok := d["rms_dbfs_delta"].(float64)
	if !ok {
		t.Fatalf("rms_dbfs_delta is %T, want float64", d["rms_dbfs_delta"])
	}
	if rms <= 0 {
		t.Fatalf("rms_dbfs_delta = %.2f, want > 0 for a louder b", rms)
	}
	// Roughly 0.2->0.8 is +12 dB; allow generous slack.
	if math.Abs(rms-12) > 4 {
		t.Fatalf("rms_dbfs_delta = %.2f, want near +12 dB", rms)
	}
}

// TestCompareBrighter captures a low tone then a high tone and asserts the
// spectral centroid delta is positive — the live-loop "brighter CC" gate.
func TestCompareBrighter(t *testing.T) {
	s, store := probeTestServer(t, 440)

	fillTone(store, 300, 0.5, 8192)
	call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "dark"})
	fillTone(store, 2000, 0.5, 8192)
	call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "bright"})

	res := call(t, s.handleCompareAudio, map[string]any{"a": "dark", "b": "bright"})
	if res.IsError {
		t.Fatalf("compare failed: %s", resultText(res))
	}
	d := deltaMap(t, res.StructuredContent)
	cent, ok := d["centroid_hz_delta"].(float64)
	if !ok {
		t.Fatalf("centroid_hz_delta is %T, want float64", d["centroid_hz_delta"])
	}
	if cent <= 0 {
		t.Fatalf("centroid_hz_delta = %.1f, want > 0 for a brighter b", cent)
	}
}

// TestComparePitchSign captures A4 then a sharper tone and asserts the cents
// delta is positive (b is sharper).
func TestComparePitchSign(t *testing.T) {
	s, store := probeTestServer(t, 440)

	fillTone(store, 440, 0.6, 8192)
	call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "a"})
	fillTone(store, 466.16, 0.6, 8192) // A#4, +100 cents
	call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "b"})

	res := call(t, s.handleCompareAudio, map[string]any{"a": "a", "b": "b"})
	if res.IsError {
		t.Fatalf("compare failed: %s", resultText(res))
	}
	d := deltaMap(t, res.StructuredContent)
	cents, ok := d["f0_cents_delta"].(float64)
	if !ok {
		t.Fatalf("f0_cents_delta is %T, want float64", d["f0_cents_delta"])
	}
	if math.Abs(cents-100) > 25 {
		t.Fatalf("f0_cents_delta = %.1f, want near +100", cents)
	}
}

// TestCompareMissingLabel returns an actionable error with a JSON pointer when a
// referenced snapshot was never captured.
func TestCompareMissingLabel(t *testing.T) {
	s, store := probeTestServer(t, 440)
	fillTone(store, 440, 0.5, 8192)
	call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": "a"})

	res := call(t, s.handleCompareAudio, map[string]any{"a": "a", "b": "missing"})
	if !res.IsError {
		t.Fatalf("expected error for missing label, got: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "/b") {
		t.Fatalf("error missing /b pointer:\n%s", resultText(res))
	}
}

// TestCaptureRequiresLabel rejects an empty label.
func TestCaptureRequiresLabel(t *testing.T) {
	s, _ := probeTestServer(t, 440)
	res := call(t, s.handleCaptureAudioSnapshot, map[string]any{"label": ""})
	if !res.IsError {
		t.Fatalf("expected error for empty label, got: %s", resultText(res))
	}
}

// TestProbeSoundAutoDelta confirms a second probe_sound (no notes, pure read)
// reports a delta vs the previous probe in both text and structuredContent.
func TestProbeSoundAutoDelta(t *testing.T) {
	s, store := probeTestServer(t, 440)

	fillTone(store, 440, 0.2, 8192)
	if res := call(t, s.handleProbeSound, map[string]any{}); res.IsError {
		t.Fatalf("probe 1 failed: %s", resultText(res))
	}
	fillTone(store, 440, 0.8, 8192)
	res := call(t, s.handleProbeSound, map[string]any{})
	if res.IsError {
		t.Fatalf("probe 2 failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "delta vs previous probe") {
		t.Fatalf("text missing auto-delta section:\n%s", resultText(res))
	}
	sc := res.StructuredContent.(map[string]any)
	d, ok := sc["delta"].(map[string]any)
	if !ok {
		t.Fatalf("delta is %T, want map", sc["delta"])
	}
	if rms, _ := d["rms_dbfs_delta"].(float64); rms <= 0 {
		t.Fatalf("auto-delta rms_dbfs_delta = %.2f, want > 0 (louder second probe)", rms)
	}
}
