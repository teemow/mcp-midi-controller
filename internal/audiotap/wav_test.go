package audiotap

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteWAVStereoHeader writes a stereo float32 clip and checks the WAVE
// header advertises IEEE float, the channel count, sample rate, and a data chunk
// whose size matches the interleaved samples.
func TestWriteWAVStereoHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	clip := Clip{
		Encoding:   "f32le",
		SampleRate: 48000,
		Channels:   2,
		Samples:    []float32{0.1, -0.1, 0.2, -0.2}, // 2 stereo frames
	}
	if err := WriteWAV(path, clip); err != nil {
		t.Fatalf("WriteWAV: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[12:16]) != "fmt " {
		t.Fatalf("bad RIFF/WAVE/fmt header: %q", b[0:16])
	}
	if format := binary.LittleEndian.Uint16(b[20:22]); format != 3 {
		t.Fatalf("audio format = %d, want 3 (IEEE float)", format)
	}
	if ch := binary.LittleEndian.Uint16(b[22:24]); ch != 2 {
		t.Fatalf("channels = %d, want 2", ch)
	}
	if sr := binary.LittleEndian.Uint32(b[24:28]); sr != 48000 {
		t.Fatalf("sample rate = %d, want 48000", sr)
	}
	if bits := binary.LittleEndian.Uint16(b[34:36]); bits != 32 {
		t.Fatalf("bits per sample = %d, want 32", bits)
	}
	if string(b[36:40]) != "data" {
		t.Fatalf("expected data chunk, got %q", b[36:40])
	}
	if size := binary.LittleEndian.Uint32(b[40:44]); int(size) != len(clip.Samples)*4 {
		t.Fatalf("data size = %d, want %d", size, len(clip.Samples)*4)
	}
	// First sample round-trips.
	if got := math.Float32frombits(binary.LittleEndian.Uint32(b[44:48])); got != clip.Samples[0] {
		t.Fatalf("sample[0] = %v, want %v", got, clip.Samples[0])
	}
}

// TestPruneDirKeepsNewest writes more clips than the file budget and asserts the
// oldest are pruned down to the cap.
func TestPruneDirKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	clip := Clip{Encoding: "f32le", SampleRate: 48000, Channels: 1, Samples: []float32{0, 0}}
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "probe-"+string(rune('a'+i))+".wav")
		if err := WriteWAV(path, clip); err != nil {
			t.Fatalf("WriteWAV: %v", err)
		}
		// Stagger mtimes so the prune order is deterministic (oldest first).
		ts := time.Unix(int64(1_000_000+i), 0)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	if err := PruneDir(dir, 2, 0); err != nil {
		t.Fatalf("PruneDir: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("kept %d files, want 2 (newest)", len(entries))
	}
	// The two newest (d, e) survive; the oldest (a, b, c) are gone.
	for _, e := range entries {
		if e.Name() == "probe-a.wav" || e.Name() == "probe-b.wav" || e.Name() == "probe-c.wav" {
			t.Fatalf("expected oldest pruned, found %q", e.Name())
		}
	}
}
