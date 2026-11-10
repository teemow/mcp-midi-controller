package audiotap

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readRaw reads a capture file back as float32 samples.
func readRaw(t *testing.T, path string) []float32 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	out := make([]float32, len(data)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return out
}

func stereoTap(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 2, SampleRate: 48000})
	return s
}

// TestCaptureIsSeamless feeds a capture far more audio than the rolling window
// holds, in batches of uneven size, and reads back every sample in order.
func TestCaptureIsSeamless(t *testing.T) {
	s := stereoTap(t)
	path := filepath.Join(t.TempDir(), "take.f32")
	// Audio from before the capture started is not part of it.
	s.AppendAudio([]float32{9, 9, 9, 9})
	c, err := s.StartCapture(path, 0.5, 0)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}

	const total = windowCapacity*2 + 346 // stereo: an even count of samples
	next := 0
	for size := 2; next < total; size = size%1022 + 2 {
		n := min(size, total-next)
		batch := make([]float32, n)
		for i := range batch {
			// A ramp well under the threshold, so no frame counts as sound.
			batch[i] = float32(next+i) / float32(total) * 0.25
		}
		s.AppendAudio(batch)
		next += n
	}
	if st := c.State(); st.Frames != total/2 || st.FirstSound != -1 || st.Ended != "" {
		t.Fatalf("state while running = %+v, want %d frames, no sound", st, total/2)
	}
	st, err := c.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Audio after the stop is not part of it either.
	s.AppendAudio([]float32{9, 9})

	if st.Frames != total/2 || st.Channels != 2 || st.SampleRate != 48000 || len(st.Stalls) != 0 {
		t.Fatalf("final state = %+v", st)
	}
	got := readRaw(t, path)
	if len(got) != total {
		t.Fatalf("captured %d samples, want %d", len(got), total)
	}
	for i, v := range got {
		if want := float32(i) / float32(total) * 0.25; v != want {
			t.Fatalf("sample %d = %v, want %v: the capture has a seam", i, v, want)
		}
	}
	if _, err := c.Stop(); err != nil {
		t.Errorf("a second Stop failed: %v", err)
	}
}

// TestCaptureFindsSound checks the first and last frame at or over the
// threshold, on either channel and of either sign.
func TestCaptureFindsSound(t *testing.T) {
	s := stereoTap(t)
	c, err := s.StartCapture(filepath.Join(t.TempDir(), "take.f32"), 0.1, 0)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	frames := make([]float32, 2*100)
	frames[2*10+1] = -0.5 // frame 10, right channel, negative
	frames[2*40] = 0.1    // frame 40, left channel, exactly the threshold
	frames[2*60] = 0.09   // under it
	s.AppendAudio(frames[:2*30])
	s.AppendAudio(frames[2*30:])
	if st := c.State(); st.FirstSound != 10 || st.LastSound != 40 {
		t.Fatalf("sound from frame %d to %d, want 10 to 40", st.FirstSound, st.LastSound)
	}
	// From a mark on, only what follows counts.
	c.SoundFromHere()
	if st := c.State(); st.FirstSound != -1 || st.LastSound != -1 {
		t.Fatalf("after the mark: sound from frame %d to %d, want none", st.FirstSound, st.LastSound)
	}
	late := make([]float32, 2*20)
	late[2*5], late[2*7] = 0.3, -0.3
	s.AppendAudio(late)
	st, _ := c.Stop()
	if st.FirstSound != 105 || st.LastSound != 107 || st.Frames != 120 {
		t.Fatalf("after the mark: sound from frame %d to %d of %d, want 105 to 107 of 120", st.FirstSound, st.LastSound, st.Frames)
	}
}

// TestCaptureRecordsStalls checks that a late batch and a reconnect are both
// recorded at the frame where the audio breaks.
func TestCaptureRecordsStalls(t *testing.T) {
	s := stereoTap(t)
	c, err := s.StartCapture(filepath.Join(t.TempDir(), "take.f32"), 0.5, 0)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	s.AppendAudio(make([]float32, 2*100))
	time.Sleep(maxInterArrivalGap + 50*time.Millisecond)
	s.AppendAudio(make([]float32, 2*50))
	// The tap drops and dials in again with the same format.
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 2, SampleRate: 48000})
	s.AppendAudio(make([]float32, 2*10))

	st, _ := c.Stop()
	if st.Frames != 160 || st.Ended != "" {
		t.Fatalf("state = %+v, want 160 frames and still running", st)
	}
	if len(st.Stalls) != 2 {
		t.Fatalf("stalls = %+v, want the late batch and the reconnect", st.Stalls)
	}
	if a := st.Stalls[0]; a.Frame != 100 || a.GapMS < maxInterArrivalGap.Milliseconds() || a.Reconnect {
		t.Errorf("first stall = %+v, want a gap at frame 100", a)
	}
	if b := st.Stalls[1]; b.Frame != 150 || !b.Reconnect {
		t.Errorf("second stall = %+v, want a reconnect at frame 150", b)
	}
}

// TestCaptureEnds checks the two ways a capture ends by itself: its length
// limit, exactly, and a tap that comes back with another layout.
func TestCaptureEnds(t *testing.T) {
	s := stereoTap(t)
	path := filepath.Join(t.TempDir(), "take.f32")
	c, err := s.StartCapture(path, 0.5, 150)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	s.AppendAudio(make([]float32, 2*100))
	s.AppendAudio(make([]float32, 2*100))
	s.AppendAudio(make([]float32, 2*100))
	st, _ := c.Stop()
	if st.Frames != 150 || st.Ended != EndedFull {
		t.Fatalf("state = %+v, want exactly the 150 frames allowed", st)
	}
	if got := readRaw(t, path); len(got) != 300 {
		t.Fatalf("file holds %d samples, want 300", len(got))
	}

	c, err = s.StartCapture(path, 0.5, 0)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	s.AppendAudio(make([]float32, 2*100))
	s.Connect("test")
	s.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 44100})
	s.AppendAudio(make([]float32, 100))
	st, _ = c.Stop()
	if st.Frames != 100 || !strings.Contains(st.Ended, "format changed") {
		t.Fatalf("state = %+v, want the capture ended at the format change", st)
	}
}

// TestStartCaptureNeedsAStreamingTap checks the refusals: a capture is only
// meaningful on a tap that is streaming in a known layout.
func TestStartCaptureNeedsAStreamingTap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "take.f32")
	s := NewStore()
	if _, err := s.StartCapture(path, 0.5, 0); err == nil || !strings.Contains(err.Error(), "not streaming") {
		t.Fatalf("capture of a tap that never connected: %v", err)
	}
	s.Connect("test")
	if _, err := s.StartCapture(path, 0.5, 0); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("capture before the format message: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a refused capture left a file behind")
	}
}
