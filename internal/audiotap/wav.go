package audiotap

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteWAV writes an interleaved float32 Clip to path as a canonical WAVE file
// using WAVE_FORMAT_IEEE_FLOAT (format tag 3, 32-bit). The channel count and
// sample rate come from the Clip so a stereo, full-rate per-probe segment is
// written losslessly — exactly the bytes the daemon analysed.
func WriteWAV(path string, clip Clip) error {
	ch := clip.Channels
	if ch < 1 {
		ch = 1
	}
	sr := uint32(clip.SampleRate)
	if sr == 0 {
		sr = 48000
	}
	dataBytes := len(clip.Samples) * 4

	var buf bytes.Buffer
	buf.Grow(44 + dataBytes)

	// RIFF / WAVE container.
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataBytes))
	buf.WriteString("WAVE")

	// fmt chunk (16 bytes, PCM-style layout with IEEE-float tag).
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3)) // WAVE_FORMAT_IEEE_FLOAT
	_ = binary.Write(&buf, binary.LittleEndian, uint16(ch))
	_ = binary.Write(&buf, binary.LittleEndian, sr)
	_ = binary.Write(&buf, binary.LittleEndian, sr*uint32(ch)*4) // byte rate
	_ = binary.Write(&buf, binary.LittleEndian, uint16(ch*4))    // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(32))      // bits per sample

	// data chunk.
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataBytes))
	_ = binary.Write(&buf, binary.LittleEndian, clip.Samples)

	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// PruneDir keeps the .wav files under dir within a retention budget by deleting
// the oldest (by mtime) until at most maxFiles remain AND the total size is at
// most maxBytes. A zero/negative cap disables that dimension. Per-probe WAVs are
// volatile rig audio (private, never committed) so the budget bounds disk use.
func PruneDir(dir string, maxFiles int, maxBytes int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type wavFile struct {
		path string
		size int64
		mod  int64
	}
	var files []wavFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, wavFile{filepath.Join(dir, e.Name()), info.Size(), info.ModTime().UnixNano()})
		total += info.Size()
	}
	// Oldest first so we evict in age order.
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })

	for len(files) > 0 {
		overCount := maxFiles > 0 && len(files) > maxFiles
		overBytes := maxBytes > 0 && total > maxBytes
		if !overCount && !overBytes {
			break
		}
		f := files[0]
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
		files = files[1:]
	}
	return nil
}
