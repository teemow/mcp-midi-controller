package audiotap

import (
	"math"
	"testing"
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
