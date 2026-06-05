package audiotap

import (
	"math"
	"testing"
	"time"
)

func TestAppendAudioRingOverflowKeepsNewest(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	// Write more than the ring holds; the oldest must be dropped and the window
	// must report exactly the capacity.
	total := windowCapacity + 1000
	buf := make([]float32, total)
	for i := range buf {
		buf[i] = float32(i)
	}
	s.AppendAudio(buf)

	snap := s.Snapshot()
	if snap.WindowSamples != windowCapacity {
		t.Fatalf("WindowSamples = %d, want %d", snap.WindowSamples, windowCapacity)
	}
	// The oldest retained sample should be total-windowCapacity, and waveform's
	// last bucket must reflect the most recent (largest-magnitude) samples.
	if len(snap.Waveform) != waveformBuckets {
		t.Fatalf("Waveform len = %d, want %d", len(snap.Waveform), waveformBuckets)
	}
	last := snap.Waveform[len(snap.Waveform)-1]
	first := snap.Waveform[0]
	if last <= first {
		t.Fatalf("expected increasing envelope (newest louder): first=%v last=%v", first, last)
	}
}

func TestWindowLevels(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	// A constant ±1 square-ish signal: peak=1, rms=1.
	samples := make([]float32, 1000)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 1
		} else {
			samples[i] = -1
		}
	}
	s.AppendAudio(samples)
	snap := s.Snapshot()
	if math.Abs(float64(snap.WindowPeak)-1) > 1e-6 {
		t.Fatalf("WindowPeak = %v, want 1", snap.WindowPeak)
	}
	if math.Abs(float64(snap.WindowRMS)-1) > 1e-6 {
		t.Fatalf("WindowRMS = %v, want 1", snap.WindowRMS)
	}
}

func TestSetFormatAndFeatures(t *testing.T) {
	s := NewStore()
	s.Connect("1.2.3.4:5555")
	s.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 11025, Source: "ProbeAudioTap"})
	s.SetFeatures(0.1, 0.5)

	snap := s.Snapshot()
	if !snap.Connected {
		t.Fatal("expected connected")
	}
	if snap.Source != "ProbeAudioTap" || snap.Encoding != "f32le" || snap.Channels != 1 || snap.SampleRate != 11025 {
		t.Fatalf("format not reflected: %+v", snap)
	}
	if snap.RMS != 0.1 || snap.Peak != 0.5 {
		t.Fatalf("features not reflected: rms=%v peak=%v", snap.RMS, snap.Peak)
	}
	if snap.FeatureMessages != 1 {
		t.Fatalf("FeatureMessages = %d, want 1", snap.FeatureMessages)
	}
	if snap.Remote != "1.2.3.4:5555" {
		t.Fatalf("Remote = %q", snap.Remote)
	}
}

func TestDisconnectKeepsLastState(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	s.SetFeatures(0.2, 0.3)
	s.Disconnect()
	snap := s.Snapshot()
	if snap.Connected {
		t.Fatal("expected disconnected")
	}
	if snap.RMS != 0.2 || snap.Peak != 0.3 {
		t.Fatalf("last levels lost after disconnect: %+v", snap)
	}
}

func TestConnectClearsPreviousWindow(t *testing.T) {
	s := NewStore()
	s.Connect("a")
	s.AppendAudio([]float32{1, 2, 3})
	s.Connect("b") // new session
	snap := s.Snapshot()
	if snap.WindowSamples != 0 {
		t.Fatalf("expected window cleared on reconnect, got %d samples", snap.WindowSamples)
	}
	if snap.Remote != "b" {
		t.Fatalf("Remote = %q, want b", snap.Remote)
	}
}

// TestSnapshotWiresAnalysis drives a synthetic A4 tone through the public
// AppendAudio path and asserts Snapshot() surfaces both the spectral and the
// musical analysis blocks — i.e. computeAnalysis is wired into the store, not
// just unit-tested in isolation.
func TestSnapshotWiresAnalysis(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 12000, Source: "ProbeAudioTap"})
	const sr = 12000.0
	samples := make([]float32, 8192)
	for i := range samples {
		samples[i] = float32(0.7 * math.Sin(2*math.Pi*440.0*float64(i)/sr))
	}
	s.AppendAudio(samples)

	snap := s.Snapshot()
	if snap.Spectral == nil {
		t.Fatal("expected spectral block in snapshot")
	}
	if snap.Analysis == nil {
		t.Fatal("expected analysis block in snapshot")
	}
	if snap.Analysis.Note != "A4" {
		t.Fatalf("analysis note = %q, want A4 (f0=%.1f)", snap.Analysis.Note, snap.Analysis.F0Hz)
	}
}

// TestSegmentExtraction marks an epoch around a known ramp and asserts Segment
// returns exactly those samples, that a contiguous capture is flagged
// contiguous, and that a range which has scrolled out reports ok=false.
func TestSegmentExtraction(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000})

	buf := make([]float32, 1000)
	for i := range buf {
		buf[i] = float32(i)
	}
	start := s.MarkEpoch()
	s.AppendAudio(buf)
	end := s.MarkEpoch()

	clip, ok := s.Segment(start, end)
	if !ok {
		t.Fatal("expected an in-range segment")
	}
	if len(clip.Samples) != 1000 {
		t.Fatalf("segment len = %d, want 1000", len(clip.Samples))
	}
	for i, v := range clip.Samples {
		if v != float32(i) {
			t.Fatalf("segment[%d] = %v, want %v", i, v, float32(i))
		}
	}
	if !clip.Contiguous {
		t.Fatal("expected a stall-free segment to be contiguous")
	}
	if clip.Channels != 1 {
		t.Fatalf("clip channels = %d, want 1", clip.Channels)
	}

	if _, ok := s.Segment(start-1000, start-500); ok {
		t.Fatal("expected a scrolled-out (pre-window) range to be unavailable")
	}
}

// TestStereoMonoMix feeds interleaved stereo and asserts the window reports the
// frame count (not interleaved-sample count), the mono mix (channel average) is
// used for levels, and Clip returns interleaved stereo with the channel count.
func TestStereoMonoMix(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 2, SampleRate: 48000})

	const frames = 512
	buf := make([]float32, frames*2)
	for f := 0; f < frames; f++ {
		buf[f*2] = 0.4   // L
		buf[f*2+1] = 0.6 // R
	}
	s.AppendAudio(buf)

	snap := s.Snapshot()
	if snap.Channels != 2 {
		t.Fatalf("channels = %d, want 2", snap.Channels)
	}
	if snap.WindowSamples != frames {
		t.Fatalf("window frames = %d, want %d", snap.WindowSamples, frames)
	}
	// Mono mix of (0.4, 0.6) is 0.5, so the window RMS is 0.5.
	if math.Abs(float64(snap.WindowRMS)-0.5) > 1e-5 {
		t.Fatalf("window rms = %v, want 0.5 (mono mix)", snap.WindowRMS)
	}

	clip := s.Clip(0)
	if clip.Channels != 2 {
		t.Fatalf("clip channels = %d, want 2", clip.Channels)
	}
	if len(clip.Samples) != frames*2 {
		t.Fatalf("clip samples = %d, want %d (interleaved stereo)", len(clip.Samples), frames*2)
	}
}

// TestGapGuard verifies the inter-arrival stall guard: a segment straddling a
// stalled boundary is flagged non-contiguous, while one wholly within a single
// batch stays contiguous.
func TestGapGuard(t *testing.T) {
	s := NewStore()
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000})

	s.AppendAudio(make([]float32, 100))
	mid := s.MarkEpoch()
	time.Sleep(maxInterArrivalGap + 50*time.Millisecond)
	s.AppendAudio(make([]float32, 100))
	end := s.MarkEpoch()

	straddling, ok := s.Segment(0, end)
	if !ok {
		t.Fatal("expected a segment across the stall")
	}
	if straddling.Contiguous {
		t.Fatal("expected non-contiguous across the inter-arrival stall")
	}

	within, ok := s.Segment(mid, end)
	if !ok {
		t.Fatal("expected a segment within the second batch")
	}
	if !within.Contiguous {
		t.Fatal("expected contiguous within a single batch")
	}
}

func TestDecodeFloat32LE(t *testing.T) {
	in := []float32{0, 1, -1, 0.5}
	buf := make([]byte, len(in)*4)
	for i, v := range in {
		bits := math.Float32bits(v)
		buf[i*4+0] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	out := decodeFloat32LE(buf)
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("out[%d] = %v, want %v", i, out[i], in[i])
		}
	}
	// A trailing partial sample is ignored.
	if got := decodeFloat32LE([]byte{1, 2, 3}); got != nil {
		t.Fatalf("expected nil for sub-sample input, got %v", got)
	}
}
