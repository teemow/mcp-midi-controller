package recording

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
)

const testRate = 48000

// writeRaw writes interleaved samples as a capture file and returns the take.
func writeRaw(t *testing.T, path string, channels int, samples []float32) Take {
	t.Helper()
	buf := make([]byte, len(samples)*4)
	for i, v := range samples {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write take: %v", err)
	}
	take := Take{RawPath: path, Channels: channels, SampleRate: testRate, Frames: int64(len(samples) / channels), FirstSound: -1, LastSound: -1}
	for i, v := range samples {
		if math.Abs(float64(v)) >= 0.001 {
			if take.FirstSound < 0 {
				take.FirstSound = int64(i / channels)
			}
			take.LastSound = int64(i / channels)
		}
	}
	return take
}

// music is a stereo test signal: lead silence, a tone that differs between
// the channels, then tail silence. Lengths are in frames.
func music(lead, tone, tail int) []float32 {
	out := make([]float32, 0, 2*(lead+tone+tail))
	out = append(out, make([]float32, 2*lead)...)
	for i := range tone {
		ph := 2 * math.Pi * float64(i) / testRate
		out = append(out, float32(0.5*math.Sin(440*ph)), float32(0.25*math.Sin(660*ph)+0.1*math.Sin(3000*ph)))
	}
	return append(out, make([]float32, 2*tail)...)
}

// decodeFLAC reads a FLAC file back: its stream info, its tags and its
// samples, interleaved.
func decodeFLAC(t *testing.T, path string) (*meta.StreamInfo, map[string]string, []int32) {
	t.Helper()
	stream, err := flac.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	defer func() { _ = stream.Close() }()
	tags := map[string]string{}
	for _, b := range stream.Blocks {
		if vc, ok := b.Body.(*meta.VorbisComment); ok {
			for _, tag := range vc.Tags {
				tags[tag[0]] = tag[1]
			}
		}
	}
	var out []int32
	for {
		f, err := stream.ParseNext()
		if err != nil {
			break
		}
		for i := range f.Subframes[0].NSamples {
			for _, sub := range f.Subframes {
				out = append(out, sub.Samples[i])
			}
		}
	}
	return stream.Info, tags, out
}

func TestPlanCut(t *testing.T) {
	ms := func(n int64) int64 { return n * testRate / 1000 }
	take := Take{SampleRate: testRate, Frames: ms(10000), FirstSound: ms(2350), LastSound: ms(8000)}

	// The silence around the music goes: 20 ms are kept ahead of it, and a
	// second after it, faded.
	cut := PlanCut(take, true)
	if want := (Cut{Start: ms(2350 - 20), End: ms(8000+1000) + 1, FadeOut: ms(1000)}); cut != want {
		t.Errorf("cut = %+v, want %+v", cut, want)
	}
	// Less than a second of silence after the music: all of it, faded.
	near := Take{SampleRate: testRate, Frames: ms(8300), FirstSound: ms(2350), LastSound: ms(8000)}
	if cut := PlanCut(near, true); cut.End != ms(8300) || cut.FadeOut != ms(300)-1 {
		t.Errorf("cut of a take with a short tail = %+v", cut)
	}
	// Sound right at the edges: nothing before the start, and a take that was
	// still sounding when it ended is faded out.
	edge := Take{SampleRate: testRate, Frames: ms(10000), FirstSound: 5, LastSound: ms(10000) - 10}
	if cut := PlanCut(edge, true); cut != (Cut{Start: 0, End: ms(10000), FadeOut: ms(400)}) { // the hot fade
		t.Errorf("cut of a take that ends sounding = %+v", cut)
	}
	// A fade is never longer than what is kept.
	short := Take{SampleRate: testRate, Frames: ms(100), FirstSound: 0, LastSound: ms(100) - 1}
	if cut := PlanCut(short, true); cut.FadeOut != ms(100) {
		t.Errorf("fade of a 100 ms take = %d frames, want all %d", cut.FadeOut, ms(100))
	}
	// Untrimmed, and silent throughout: everything, as it is.
	if cut := PlanCut(take, false); cut != (Cut{End: take.Frames}) {
		t.Errorf("untrimmed cut = %+v", cut)
	}
	silent := Take{SampleRate: testRate, Frames: 1000, FirstSound: -1, LastSound: -1}
	if cut := PlanCut(silent, true); cut != (Cut{End: 1000}) {
		t.Errorf("cut of a silent take = %+v", cut)
	}
}

// TestEncodeFLACIsLossless encodes a cut of a take and decodes it again: every
// sample must come back as the 24-bit value of the float that went in.
func TestEncodeFLACIsLossless(t *testing.T) {
	dir := t.TempDir()
	// 9000 frames of tone: two whole blocks and a short one.
	samples := music(1000, 9000, 3000)
	take := writeRaw(t, filepath.Join(dir, "take.f32"), 2, samples)
	cut := Cut{Start: 500, End: 11000}
	dst := filepath.Join(dir, "take.flac")
	levels, err := Encode(dst, take, cut, FLAC, Tags{Title: "Night Drive", Date: "2026-11-10", Comment: "a test"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	info, tags, got := decodeFLAC(t, dst)
	if info.SampleRate != testRate || info.NChannels != 2 || info.BitsPerSample != 24 || info.NSamples != 10500 {
		t.Fatalf("stream info = %+v, want 10500 stereo frames at 24 bit, 48 kHz", info)
	}
	// Fixed-size blocks: the short last one is not the stream's minimum.
	if info.BlockSizeMin != blockFrames || info.BlockSizeMax != blockFrames {
		t.Errorf("block sizes %d..%d, want %d throughout", info.BlockSizeMin, info.BlockSizeMax, blockFrames)
	}
	if info.MD5sum == [16]byte{} {
		t.Error("the stream carries no checksum of its audio")
	}
	if tags["TITLE"] != "Night Drive" || tags["DATE"] != "2026-11-10" || tags["COMMENT"] != "a test" {
		t.Errorf("tags = %v", tags)
	}
	want := samples[2*500 : 2*11000]
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}
	for i, v := range want {
		if q := int32(math.Round(float64(v) * (1 << 23))); got[i] != q {
			t.Fatalf("sample %d decodes to %d, want %d", i, got[i], q)
		}
	}

	// The tone is 0.5 at its loudest: -6.02 dBFS. Its mean level over both
	// channels and the silence around it, from the samples themselves:
	var sumQ float64
	for _, v := range want {
		sumQ += float64(v) * float64(v)
	}
	wantRMS := 20 * math.Log10(math.Sqrt(sumQ/float64(len(want))))
	if math.Abs(levels.PeakDBFS+6.02) > 0.01 || math.Abs(levels.RMSDBFS-wantRMS) > 0.001 || levels.Clipped != 0 {
		t.Errorf("levels = %+v, want peak -6.02, rms %.3f, nothing clipped", levels, wantRMS)
	}
	if info, err := os.Stat(dst); err != nil || info.Size() >= int64(len(want))*3 {
		t.Errorf("the FLAC is no smaller than its samples as 24-bit PCM (%v bytes, %v)", info.Size(), err)
	}
}

// TestFLACPassesTheReferenceDecoder runs the reference decoder over a file,
// where the host has it: it checks the frame checksums and the audio's MD5.
func TestFLACPassesTheReferenceDecoder(t *testing.T) {
	tool, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no flac tool on this host")
	}
	dir := t.TempDir()
	for name, channels := range map[string]int{"mono": 1, "stereo": 2} {
		samples := music(100, 20000, 100)
		if channels == 1 {
			samples = samples[:len(samples)/2]
		}
		take := writeRaw(t, filepath.Join(dir, name+".f32"), channels, samples)
		dst := filepath.Join(dir, name+".flac")
		if _, err := Encode(dst, take, Cut{End: take.Frames}, FLAC, Tags{Title: name}); err != nil {
			t.Fatalf("%s: Encode: %v", name, err)
		}
		if out, err := exec.Command(tool, "--test", "--silent", "--warnings-as-errors", dst).CombinedOutput(); err != nil {
			t.Errorf("%s: flac --test: %v\n%s", name, err, out)
		}
	}
}

// TestEncodeWAVKeepsTheSamples checks the float WAV: the header a player
// reads, and the stream's samples bit for bit, over full scale included.
func TestEncodeWAVKeepsTheSamples(t *testing.T) {
	dir := t.TempDir()
	samples := music(10, 5000, 10)
	samples[2*100] = 1.5 // over full scale
	take := writeRaw(t, filepath.Join(dir, "take.f32"), 2, samples)
	dst := filepath.Join(dir, "take.wav")
	levels, err := Encode(dst, take, Cut{Start: 5, End: 5015}, WAV, Tags{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if string(data[0:4]) != "RIFF" || string(data[8:16]) != "WAVEfmt " || string(data[36:40]) != "data" {
		t.Fatalf("not a WAVE header: %q", data[:44])
	}
	if le.Uint16(data[20:]) != 3 || le.Uint16(data[22:]) != 2 || le.Uint32(data[24:]) != testRate || le.Uint16(data[34:]) != 32 {
		t.Errorf("fmt chunk: tag %d, %d channel(s), %d Hz, %d bit", le.Uint16(data[20:]), le.Uint16(data[22:]), le.Uint32(data[24:]), le.Uint16(data[34:]))
	}
	want := samples[2*5 : 2*5015]
	if int(le.Uint32(data[40:])) != len(want)*4 || int(le.Uint32(data[4:])) != len(data)-8 || len(data) != 44+len(want)*4 {
		t.Fatalf("sizes: data %d, riff %d, file %d; want %d samples", le.Uint32(data[40:]), le.Uint32(data[4:]), len(data), len(want))
	}
	for i, v := range want {
		if got := le.Uint32(data[44+i*4:]); got != math.Float32bits(v) {
			t.Fatalf("sample %d = %08x, want %08x", i, got, math.Float32bits(v))
		}
	}
	if levels.Clipped != 1 || math.Abs(levels.PeakDBFS-3.52) > 0.01 {
		t.Errorf("levels = %+v, want one sample over full scale, peaking at +3.52 dBFS", levels)
	}

	// The same take as FLAC stores that sample at full scale.
	flacPath := filepath.Join(dir, "take.flac")
	if _, err := Encode(flacPath, take, Cut{Start: 5, End: 5015}, FLAC, Tags{}); err != nil {
		t.Fatalf("Encode flac: %v", err)
	}
	if _, _, got := decodeFLAC(t, flacPath); got[2*95] != 1<<23-1 {
		t.Errorf("the sample over full scale decodes to %d, want %d", got[2*95], 1<<23-1)
	}
}

// TestEncodeFadesOut checks the fade: untouched before it starts, silent
// where it ends, and never louder than the take.
func TestEncodeFadesOut(t *testing.T) {
	dir := t.TempDir()
	samples := make([]float32, 2*2000)
	for i := range samples {
		samples[i] = 0.5
	}
	take := writeRaw(t, filepath.Join(dir, "take.f32"), 2, samples)
	dst := filepath.Join(dir, "take.flac")
	if _, err := Encode(dst, take, Cut{End: 2000, FadeOut: 1000}, FLAC, Tags{}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	_, _, got := decodeFLAC(t, dst)
	const full = 1 << 22 // 0.5
	if got[2*999] != full || got[2*1000] != full {
		t.Errorf("the fade starts early: frames 999 and 1000 = %d, %d, want %d", got[2*999], got[2*1000], full)
	}
	if mid := got[2*1500]; mid < full/2-2 || mid > full/2+2 {
		t.Errorf("halfway through the fade = %d, want about %d", mid, full/2)
	}
	if last := got[2*1999]; last < 0 || last > full/1000 {
		t.Errorf("the last frame = %d, want next to silence", last)
	}
	for i := 2 * 1000; i < len(got)-2; i += 2 {
		if got[i+2] > got[i] {
			t.Fatalf("the fade rises at frame %d", i/2)
		}
	}
}

func TestEncodeRefusals(t *testing.T) {
	dir := t.TempDir()
	take := writeRaw(t, filepath.Join(dir, "take.f32"), 2, music(0, 100, 0))
	dst := filepath.Join(dir, "out.flac")
	for name, tc := range map[string]struct {
		take Take
		cut  Cut
		want string
	}{
		"an empty cut":       {take, Cut{Start: 50, End: 50}, "nothing to write"},
		"a cut past the end": {take, Cut{End: 101}, "nothing to write"},
		"a short take file":  {Take{RawPath: take.RawPath, Channels: 2, SampleRate: testRate, Frames: 200}, Cut{End: 200}, "ends early"},
		"nine channels":      {Take{RawPath: take.RawPath, Channels: 9, SampleRate: testRate, Frames: 10}, Cut{End: 10}, "at most 8 channels"},
		"no layout":          {Take{RawPath: take.RawPath, Frames: 10}, Cut{End: 10}, "no layout"},
	} {
		if _, err := Encode(dst, tc.take, tc.cut, FLAC, Tags{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		if _, err := os.Stat(dst); err == nil {
			t.Errorf("%s: a failed encode left a file behind", name)
			_ = os.Remove(dst)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": FLAC, "flac": FLAC, " FLAC ": FLAC, "wav": WAV, "wave": WAV} {
		if got, err := ParseFormat(in); err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseFormat("mp3"); err == nil {
		t.Error("mp3 was accepted")
	}
}

// capture stands in for a stopped capture of the given samples.
func capture(t *testing.T, s *Store, name string, samples []float32) audiotap.CaptureState {
	t.Helper()
	take := writeRaw(t, s.PartPath(name), 2, samples)
	started := time.Date(2026, 11, 10, 13, 47, 0, 0, time.UTC)
	return audiotap.CaptureState{
		Channels: 2, SampleRate: testRate, Frames: take.Frames,
		FirstSound: take.FirstSound, LastSound: take.LastSound,
		Started: started, LastAudio: started.Add(time.Duration(take.Frames) * time.Second / testRate),
	}
}

// TestStoreFinish runs a capture through the store: the recording, its record
// and nothing else is left, and the record says what was done to the take.
func TestStoreFinish(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "recordings"))
	at := time.Date(2026, 11, 10, 13, 47, 0, 0, time.UTC)
	name, err := s.NewName("Night: Drive/2", at)
	if err != nil {
		t.Fatalf("NewName: %v", err)
	}
	if name != "Night Drive 2 2026-11-10 1347" {
		t.Fatalf("name = %q", name)
	}
	// 0.5 s of silence, 1 s of music, 2 s of silence.
	st := capture(t, s, name, music(testRate/2, testRate, 2*testRate))
	st.Stalls = []audiotap.Stall{{Frame: 4800, GapMS: 300}}
	m, err := s.Finish(Meta{Name: name, Song: "night_drive", Title: "Night Drive", Tap: "master", RecordedAt: at}, st, FLAC, true)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if m.File != name+".flac" || m.Format != FLAC || m.BitDepth != 24 || m.Channels != 2 || m.SampleRate != testRate {
		t.Errorf("record = %+v", m)
	}
	// The music's first frame is silent (a sine starts at zero), so the first
	// sound is a frame or two in; the cut keeps 20 ms before it and a second
	// after the last, faded.
	if math.Abs(m.Seconds-2.02) > 0.001 || m.TrimmedLeadMS != 480 || m.TrimmedTailMS < 999 || m.TrimmedTailMS > 1000 || m.FadeOutMS != 1000 {
		t.Errorf("cut: %.4f s, lead %d ms, tail %d ms, fade %d ms; want 2.02 s, 480, 1000, 1000", m.Seconds, m.TrimmedLeadMS, m.TrimmedTailMS, m.FadeOutMS)
	}
	if math.Abs(m.CapturedSeconds-3.5) > 1e-9 || math.Abs(m.WallSeconds-3.5) > 1e-9 || len(m.Stalls) != 1 || m.Silent {
		t.Errorf("capture: %.3f s captured in %.3f s, stalls %+v, silent %v", m.CapturedSeconds, m.WallSeconds, m.Stalls, m.Silent)
	}
	if math.Abs(m.PeakDBFS+6.02) > 0.01 || m.RMSDBFS > -9 || m.RMSDBFS < -14 {
		t.Errorf("levels: peak %.2f, rms %.2f", m.PeakDBFS, m.RMSDBFS)
	}
	info, tags, _ := decodeFLAC(t, s.AudioPath(m))
	if tags["TITLE"] != "Night Drive" || tags["DATE"] != "2026-11-10" || float64(info.NSamples)/testRate != m.Seconds {
		t.Errorf("file: tags %v, %d frames; record says %.4f s", tags, info.NSamples, m.Seconds)
	}
	if fi, _ := os.Stat(s.AudioPath(m)); fi == nil || fi.Size() != m.Bytes {
		t.Errorf("record says %d bytes", m.Bytes)
	}
	entries, _ := os.ReadDir(s.Dir())
	if len(entries) != 2 {
		t.Errorf("the store holds %d files, want the audio and its record", len(entries))
	}

	// The record reads back, by name and by file name.
	for _, ref := range []string{name, name + ".flac"} {
		if got, err := s.Get(ref); err != nil || got.Name != name || got.Song != "night_drive" || !got.RecordedAt.Equal(at) {
			t.Errorf("Get(%q) = %+v, %v", ref, got, err)
		}
	}
	for _, ref := range []string{"", "nothing", "../" + name, name + "/x"} {
		if _, err := s.Get(ref); err != ErrNotFound {
			t.Errorf("Get(%q) err = %v, want ErrNotFound", ref, err)
		}
	}
	// A second take in the same minute gets a name of its own.
	if again, _ := s.NewName("Night: Drive/2", at); again != name+" (2)" {
		t.Errorf("second name = %q", again)
	}
}

// TestStoreFinishSilentAndEmpty: a silent take is kept whole and marked; a
// capture without audio is an error. Neither leaves its capture file behind.
func TestStoreFinishSilentAndEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	at := time.Now()
	st := capture(t, s, "silent", make([]float32, 2*4800))
	m, err := s.Finish(Meta{Name: "silent", RecordedAt: at}, st, WAV, true)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if !m.Silent || m.Seconds != 0.1 || m.TrimmedLeadMS != 0 || m.PeakDBFS != silenceDBFS || m.File != "silent.wav" {
		t.Errorf("silent take: %+v", m)
	}

	st = capture(t, s, "empty", nil)
	if _, err := s.Finish(Meta{Name: "empty", RecordedAt: at}, st, FLAC, true); err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Errorf("empty take: %v", err)
	}
	for _, name := range []string{"silent", "empty"} {
		if _, err := os.Stat(s.PartPath(name)); err == nil {
			t.Errorf("the capture of %q is still on the disk", name)
		}
	}
}

// TestStoreListAndPrune checks the order (newest first), that pruning keeps
// the newest, and that unfinished captures are cleared away.
func TestStoreListAndPrune(t *testing.T) {
	s := NewStore(t.TempDir())
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"first", "second", "third"} {
		st := capture(t, s, name, music(0, 480, 0))
		if _, err := s.Finish(Meta{Name: name, RecordedAt: base.Add(time.Duration(i) * time.Hour)}, st, FLAC, true); err != nil {
			t.Fatalf("Finish %s: %v", name, err)
		}
	}
	// A record whose audio is gone is not a recording.
	if err := os.WriteFile(filepath.Join(s.Dir(), "ghost.json"), []byte(`{"name": "ghost", "file": "ghost.flac"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		all, err := s.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var out []string
		for _, m := range all {
			out = append(out, m.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(); got != "third second first" {
		t.Fatalf("list = %q, want newest first", got)
	}
	if gone, err := s.Prune(0); err != nil || len(gone) != 0 {
		t.Errorf("Prune(0) deleted %v (%v), want nothing", gone, err)
	}
	gone, err := s.Prune(2)
	if err != nil || len(gone) != 1 || gone[0] != "first" {
		t.Fatalf("Prune(2) deleted %v (%v), want the oldest", gone, err)
	}
	if got := names(); got != "third second" {
		t.Errorf("after pruning: %q", got)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "first.flac")); err == nil {
		t.Error("the pruned recording's audio is still there")
	}

	for _, name := range []string{"running", "abandoned"} {
		if err := os.WriteFile(s.PartPath(name), []byte{0, 0, 0, 0}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.RemoveStaleParts("running")
	if _, err := os.Stat(s.PartPath("running")); err != nil {
		t.Error("the running capture was deleted")
	}
	if _, err := os.Stat(s.PartPath("abandoned")); err == nil {
		t.Error("the abandoned capture is still there")
	}
	// An empty store lists nothing and is not an error.
	if all, err := NewStore(filepath.Join(t.TempDir(), "none")).List(); err != nil || len(all) != 0 {
		t.Errorf("empty store: %v, %v", all, err)
	}
}
