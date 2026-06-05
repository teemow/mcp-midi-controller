package audiotap

import (
	"math"
	"math/rand"
	"testing"
)

const testSampleRate = 11025.0

// sliceSampler adapts a []float32 to the sampleAt(i) signature.
func sliceSampler(s []float32) func(i int) float32 {
	return func(i int) float32 { return s[i] }
}

// sine generates n samples of a tone at freq Hz with the given amplitude.
func sine(freq, amp float64, n int) []float32 {
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/testSampleRate))
	}
	return out
}

// TestDetectF0A4 plays A4 (440 Hz) and asserts note A4 within a few cents with
// high clarity — the core live-loop ground truth.
func TestDetectF0A4(t *testing.T) {
	samples := sine(440, 0.8, 4096)
	a := computeAnalysis(sliceSampler(samples), len(samples), testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if a.Confidence < pitchClarityThreshold {
		t.Fatalf("confidence = %.3f, want >= %.2f", a.Confidence, pitchClarityThreshold)
	}
	if a.Note != "A4" {
		t.Fatalf("note = %q, want A4 (f0=%.2f midi=%d)", a.Note, a.F0Hz, a.MIDINote)
	}
	if math.Abs(a.Cents) > 15 {
		t.Fatalf("cents = %.2f, want within +/-15 of A4", a.Cents)
	}
	if math.Abs(a.F0Hz-440) > 5 {
		t.Fatalf("f0 = %.2f, want near 440", a.F0Hz)
	}
}

// TestFullScaleSineCalibration asserts the dBFS reference is correct: a
// full-scale (amplitude 1.0) sine placed exactly on an FFT bin must read ~0 dBFS
// both as a time-domain peak and as its strongest harmonic partial. Before the
// Hann coherent-gain fix the partial read ~6 dB low against the n/2 reference.
func TestFullScaleSineCalibration(t *testing.T) {
	const n = 16384
	binHz := testSampleRate / float64(n)
	freq := 654 * binHz // ~440 Hz, on an FFT bin so there is no scalloping loss
	samples := sine(freq, 1.0, n)

	a := computeAnalysisSegment(sliceSampler(samples), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if math.Abs(a.PeakDBFS) > 0.5 {
		t.Fatalf("peak dBFS = %.2f, want ~0 for a full-scale sine", a.PeakDBFS)
	}
	if len(a.Partials) == 0 {
		t.Fatal("expected at least one partial")
	}
	maxDB := -200.0
	for _, p := range a.Partials {
		if p.DB > maxDB {
			maxDB = p.DB
		}
	}
	if math.Abs(maxDB) > 1.5 {
		t.Fatalf("full-scale sine strongest partial = %.2f dBFS, want ~0 (±1.5)", maxDB)
	}
}

// TestSegmentAlignedF0Partial proves the segment path measures f0 and the
// partials over ONE aligned window: the detected fundamental must show up as a
// harmonic-1 partial at (near) f0. This is the consistency the live path lacks
// (finding #1: pitch and harmonics drawn from different time slices).
func TestSegmentAlignedF0Partial(t *testing.T) {
	const n = 16384
	samples := sine(440, 0.8, n)

	a := computeAnalysisSegment(sliceSampler(samples), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if a.Note != "A4" {
		t.Fatalf("note = %q, want A4 (f0=%.2f)", a.Note, a.F0Hz)
	}
	found := false
	for _, p := range a.Partials {
		if p.Harmonic == 1 && math.Abs(p.FreqHz-a.F0Hz) < 10 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no harmonic-1 partial near f0=%.2f; partials=%+v", a.F0Hz, a.Partials)
	}
}

// TestNoteFromHzTable spot-checks the Hz→note mapping at well-known pitches.
func TestNoteFromHzTable(t *testing.T) {
	cases := []struct {
		hz   float64
		note string
		midi int
	}{
		{440.0, "A4", 69},
		{261.63, "C4", 60},
		{329.63, "E4", 64},
		{392.0, "G4", 67},
		{82.41, "E2", 40},
	}
	for _, c := range cases {
		midi, note, cents := noteFromHz(c.hz)
		if note != c.note || midi != c.midi {
			t.Fatalf("noteFromHz(%.2f) = %q/%d, want %q/%d", c.hz, note, midi, c.note, c.midi)
		}
		if math.Abs(cents) > 5 {
			t.Fatalf("noteFromHz(%.2f) cents = %.2f, want near 0", c.hz, cents)
		}
	}
}

// TestNoiseNoF0 feeds white noise and asserts no confident pitch is reported
// and that the spectrum is flat (high flatness), matching the acceptance gate.
func TestNoiseNoF0(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	n := 4096
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32((rng.Float64()*2 - 1) * 0.5)
	}
	a := computeAnalysis(sliceSampler(samples), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if a.Note != "" || a.F0Hz != 0 {
		t.Fatalf("noise produced a pitch: note=%q f0=%.2f confidence=%.3f", a.Note, a.F0Hz, a.Confidence)
	}
	sp := computeSpectral(sliceSampler(samples), n, testSampleRate)
	if sp == nil {
		t.Fatal("expected spectral, got nil")
	}
	if sp.Flatness < 0.2 {
		t.Fatalf("flatness = %.3f, want high (>0.2) for noise", sp.Flatness)
	}
}

// TestHarmonicsCMajorChord sums three sine tones (C4/E4/G4) and asserts the
// three lowest detected partials land near those fundamentals.
func TestHarmonicsCMajorChord(t *testing.T) {
	n := 4096
	c := sine(261.63, 0.5, n)
	e := sine(329.63, 0.5, n)
	g := sine(392.0, 0.5, n)
	mix := make([]float32, n)
	for i := range mix {
		mix[i] = c[i] + e[i] + g[i]
	}
	a := computeAnalysis(sliceSampler(mix), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if len(a.Partials) < 3 {
		t.Fatalf("got %d partials, want at least 3", len(a.Partials))
	}
	wantFreqs := []float64{261.63, 329.63, 392.0}
	for _, want := range wantFreqs {
		found := false
		for _, p := range a.Partials {
			if math.Abs(p.FreqHz-want) < 15 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no partial near %.1f Hz; partials=%+v", want, a.Partials)
		}
	}
}

// TestLoudnessAndCrest checks dBFS and crest factor on a known sine: a 0.5-amp
// sine has RMS ~0.354 (-9 dBFS), peak ~0.5 (-6 dBFS), crest ~3 dB.
func TestLoudnessAndCrest(t *testing.T) {
	samples := sine(440, 0.5, 4096)
	a := computeAnalysis(sliceSampler(samples), len(samples), testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if math.Abs(a.PeakDBFS-(-6.02)) > 1.0 {
		t.Fatalf("peak dBFS = %.2f, want near -6", a.PeakDBFS)
	}
	if math.Abs(a.RMSDBFS-(-9.03)) > 1.0 {
		t.Fatalf("rms dBFS = %.2f, want near -9", a.RMSDBFS)
	}
	if math.Abs(a.CrestDB-3.0) > 1.0 {
		t.Fatalf("crest dB = %.2f, want near 3", a.CrestDB)
	}
}

// TestOnsetSingleAttack builds a silence→tone transition and asserts exactly one
// onset is detected, with ms-since-onset roughly matching the attack position.
func TestOnsetSingleAttack(t *testing.T) {
	n := 8192
	attack := 4096
	samples := make([]float32, n)
	tone := sine(440, 0.8, n)
	for i := attack; i < n; i++ {
		samples[i] = tone[i]
	}
	a := computeAnalysis(sliceSampler(samples), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if a.OnsetCount != 1 {
		t.Fatalf("onset count = %d, want exactly 1", a.OnsetCount)
	}
	if a.MSSinceOnset < 0 {
		t.Fatal("ms since onset not set")
	}
	// Attack is at sample 4096 of 8192; ~half the window from the end.
	wantMS := float64(n-attack) / testSampleRate * 1000
	if math.Abs(a.MSSinceOnset-wantMS) > 100 {
		t.Fatalf("ms since onset = %.1f, want near %.1f", a.MSSinceOnset, wantMS)
	}
}

// TestSilenceNoOnset asserts a flat silent window yields no phantom onsets and a
// floored loudness.
func TestSilenceNoOnset(t *testing.T) {
	n := 8192
	samples := make([]float32, n) // all zeros
	a := computeAnalysis(sliceSampler(samples), n, testSampleRate)
	if a == nil {
		t.Fatal("expected analysis, got nil")
	}
	if a.OnsetCount != 0 {
		t.Fatalf("onset count = %d, want 0 for silence", a.OnsetCount)
	}
	if a.MSSinceOnset != -1 {
		t.Fatalf("ms since onset = %.1f, want -1 for silence", a.MSSinceOnset)
	}
	if a.PeakDBFS != -120 {
		t.Fatalf("peak dBFS = %.2f, want -120 floor for silence", a.PeakDBFS)
	}
}

// TestAnalysisNilSafe confirms nil is returned when disconnected/idle.
func TestAnalysisNilSafe(t *testing.T) {
	z := func(int) float32 { return 0 }
	if a := computeAnalysis(z, analysisMinSamples-1, testSampleRate); a != nil {
		t.Fatal("expected nil for sub-minimum window")
	}
	if a := computeAnalysis(z, 4096, 0); a != nil {
		t.Fatal("expected nil for unknown sample rate")
	}
}
