// Package recording turns a tap capture (internal/audiotap) into a recording a
// player opens: it cuts the silence around the music, measures the levels and
// writes the audio in a lossless format, with a small record next to it that
// says what the file is.
//
// Two formats, both written by the daemon itself, so a recording needs no
// program on the host:
//
//   - FLAC, 24 bit: the default. Lossless, about two thirds the size of the
//     same audio as PCM, tagged with its title, and what a music library or a
//     DAW takes as a master.
//   - WAV, 32-bit float: the stream's own samples, bit for bit, for when the
//     capture itself is what is wanted (levels over full scale survive).
//
// There is no lossy format. An MP3 or AAC encoder is either a program the host
// may not have or a library under a copyleft licence.
package recording

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// Format is the file format of a recording.
type Format string

const (
	FLAC Format = "flac"
	WAV  Format = "wav"
)

// ParseFormat reads a format name; empty is FLAC.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "flac":
		return FLAC, nil
	case "wav", "wave":
		return WAV, nil
	}
	return "", fmt.Errorf("unknown format %q (flac, wav)", s)
}

// MIMEType is the format's media type.
func (f Format) MIMEType() string {
	if f == WAV {
		return "audio/wav"
	}
	return "audio/flac"
}

// BitDepth is the format's sample size.
func (f Format) BitDepth() int {
	if f == WAV {
		return 32
	}
	return 24
}

// Take is a finished capture: the raw file, its layout and where the capture
// heard sound.
type Take struct {
	// RawPath holds interleaved little-endian float32 samples.
	RawPath    string
	Channels   int
	SampleRate int
	Frames     int64
	// FirstSound and LastSound are the first and last frame at or over the
	// silence threshold; -1 when the take is silent throughout.
	FirstSound, LastSound int64
}

// Cut is the part of a take that becomes the recording: the frames
// [Start, End), the last FadeOut of them faded to silence.
type Cut struct {
	Start, End int64
	FadeOut    int64
}

const (
	// leadKeepMS is kept ahead of the first sound, so an attack is whole.
	leadKeepMS = 20
	// tailKeepMS is kept after the last sound and faded out: a reverb tail
	// crosses the threshold long before it is gone, and cut there it would
	// stop short. The fade only touches what is already under the threshold.
	tailKeepMS = 1000
	// hotEndMS: a take whose last sound is this close to its end was cut off
	// while it sounded, and is faded out instead of ending in a click.
	hotEndMS = 50
	// hotFadeMS is the length of that fade.
	hotFadeMS = 400
)

// PlanCut decides what of a take to keep. With trim, the silence before the
// first sound goes, and the take ends a second after its last sound, faded
// over that second; a take that was still sounding when it ended gets a short
// fade instead. Without trim, or when the take is silent throughout,
// everything is kept as it is.
func PlanCut(take Take, trim bool) Cut {
	all := Cut{Start: 0, End: take.Frames}
	if !trim || take.FirstSound < 0 || take.LastSound < take.FirstSound {
		return all
	}
	ms := func(n int64) int64 { return n * int64(take.SampleRate) / 1000 }
	cut := Cut{
		Start: max(0, take.FirstSound-ms(leadKeepMS)),
		End:   min(take.Frames, take.LastSound+1+ms(tailKeepMS)),
	}
	if take.Frames-1-take.LastSound < ms(hotEndMS) {
		cut.FadeOut = min(ms(hotFadeMS), cut.End-cut.Start)
	} else {
		cut.FadeOut = cut.End - (take.LastSound + 1)
	}
	return cut
}

// Levels are measured over the recording as written, all channels together.
type Levels struct {
	// PeakDBFS is the largest sample, RMSDBFS the mean level. A silent
	// recording reads silenceDBFS.
	PeakDBFS float64
	RMSDBFS  float64
	// Clipped counts the samples over full scale. FLAC stores them at full
	// scale; WAV keeps them.
	Clipped int64
}

// silenceDBFS stands for "no signal": JSON has no minus infinity.
const silenceDBFS = -150.0

func dbfs(linear float64) float64 {
	if linear <= 0 {
		return silenceDBFS
	}
	return max(silenceDBFS, 20*math.Log10(linear))
}

// Tags name a recording inside its file (FLAC only; WAV carries none).
type Tags struct {
	Title string
	// Date is the day of the recording, as YYYY-MM-DD.
	Date string
	// Comment says where the recording comes from.
	Comment string
}

// blockFrames is how many frames are read, measured and encoded at a time. It
// is also the FLAC block size.
const blockFrames = 4096

// Encode writes the cut of a take to dst in the given format and returns the
// levels of what it wrote. It reads the take block by block, so a long
// recording costs no more memory than a short one.
func Encode(dst string, take Take, cut Cut, format Format, tags Tags) (Levels, error) {
	if take.Channels < 1 || take.SampleRate < 1 {
		return Levels{}, fmt.Errorf("the take has no layout (%d channel(s) at %d Hz)", take.Channels, take.SampleRate)
	}
	if cut.Start < 0 || cut.End > take.Frames || cut.End <= cut.Start {
		return Levels{}, fmt.Errorf("nothing to write: frames %d to %d of %d", cut.Start, cut.End, take.Frames)
	}
	if format == FLAC && take.Channels > 8 {
		return Levels{}, fmt.Errorf("FLAC holds at most 8 channels and the take has %d: record it as wav", take.Channels)
	}
	src, err := os.Open(take.RawPath)
	if err != nil {
		return Levels{}, err
	}
	defer func() { _ = src.Close() }()
	frameBytes := int64(take.Channels) * 4
	if _, err := src.Seek(cut.Start*frameBytes, io.SeekStart); err != nil {
		return Levels{}, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Levels{}, err
	}
	file := &bufferedFile{f: out, w: bufio.NewWriterSize(out, 1<<20)}

	frames := cut.End - cut.Start
	var sink blockWriter
	switch format {
	case WAV:
		sink, err = newWAVWriter(file, take.Channels, take.SampleRate, frames)
	default:
		sink, err = newFLACWriter(file, take.Channels, take.SampleRate, tags)
	}
	if err != nil {
		_ = file.finish()
		_ = os.Remove(dst)
		return Levels{}, err
	}

	lv, err := pump(bufio.NewReaderSize(src, 1<<20), sink, take.Channels, frames, cut.FadeOut)
	// The sink owns the file: closing it finishes the format and the file.
	if cerr := sink.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return Levels{}, err
	}
	return lv, nil
}

// blockWriter takes the recording block by block: interleaved samples of
// whole frames.
type blockWriter interface {
	WriteBlock(samples []float32) error
	Close() error
}

// pump reads frames of the take, fades the end, measures and hands every
// block to the sink.
func pump(src io.Reader, sink blockWriter, channels int, frames, fadeOut int64) (Levels, error) {
	raw := make([]byte, blockFrames*channels*4)
	block := make([]float32, blockFrames*channels)
	var (
		lv         Levels
		peak, sumQ float64
		done       int64
	)
	fadeFrom := frames - fadeOut
	for done < frames {
		n := min(int64(blockFrames), frames-done)
		buf := raw[:n*int64(channels)*4]
		if _, err := io.ReadFull(src, buf); err != nil {
			return Levels{}, fmt.Errorf("the take ends early, at frame %d of %d: %w", done, frames, err)
		}
		samples := block[:n*int64(channels)]
		for i := range samples {
			v := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			if at := done + int64(i/channels); at >= fadeFrom {
				// A raised-cosine fade: no corner where it starts or ends.
				x := float64(at-fadeFrom) / float64(fadeOut)
				v *= float32(0.5 * (1 + math.Cos(math.Pi*x)))
			}
			samples[i] = v
			a := math.Abs(float64(v))
			if a > peak {
				peak = a
			}
			if a > 1 {
				lv.Clipped++
			}
			sumQ += a * a
		}
		if err := sink.WriteBlock(samples); err != nil {
			return Levels{}, err
		}
		done += n
	}
	lv.PeakDBFS = dbfs(peak)
	lv.RMSDBFS = dbfs(math.Sqrt(sumQ / float64(frames*int64(channels))))
	return lv, nil
}

// bufferedFile is a file written through a buffer that can still be sought:
// the FLAC encoder goes back to the head of the file to fill in the stream's
// length and checksum once it knows them. It is deliberately not an
// io.Closer: the format's writer decides when the file is done.
type bufferedFile struct {
	f *os.File
	w *bufio.Writer
}

func (b *bufferedFile) Write(p []byte) (int, error) { return b.w.Write(p) }

func (b *bufferedFile) Seek(offset int64, whence int) (int64, error) {
	if err := b.w.Flush(); err != nil {
		return 0, err
	}
	return b.f.Seek(offset, whence)
}

// patch overwrites bytes that were already written.
func (b *bufferedFile) patch(data []byte, offset int64) error {
	if err := b.w.Flush(); err != nil {
		return err
	}
	_, err := b.f.WriteAt(data, offset)
	return err
}

// finish writes what is still buffered and closes the file.
func (b *bufferedFile) finish() error {
	err := b.w.Flush()
	if cerr := b.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// --- WAV ----------------------------------------------------------------------

// wavWriter writes 32-bit float WAVE (format tag 3), the layout the daemon's
// per-probe clips use too.
type wavWriter struct {
	file *bufferedFile
	buf  []byte
}

func newWAVWriter(file *bufferedFile, channels, sampleRate int, frames int64) (*wavWriter, error) {
	dataBytes := frames * int64(channels) * 4
	if dataBytes > math.MaxUint32-36 {
		return nil, errors.New("the recording is too long for a WAV file: record it as flac")
	}
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 3) // WAVE_FORMAT_IEEE_FLOAT
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(sampleRate*channels*4)) // byte rate
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*4))            // block align
	binary.LittleEndian.PutUint16(h[34:], 32)                            // bits per sample
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	if _, err := file.Write(h[:]); err != nil {
		return nil, err
	}
	return &wavWriter{file: file}, nil
}

func (w *wavWriter) WriteBlock(samples []float32) error {
	if need := len(samples) * 4; cap(w.buf) < need {
		w.buf = make([]byte, need)
	}
	buf := w.buf[:len(samples)*4]
	for i, v := range samples {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	_, err := w.file.Write(buf)
	return err
}

func (w *wavWriter) Close() error { return w.file.finish() }

// --- FLAC ---------------------------------------------------------------------

// flacScale turns a float sample into a 24-bit one.
const flacScale = 1 << 23

// flacChannels is the channel assignment of each channel count. Stereo is
// stored as mid and side, which is smaller for anything that is not two
// unrelated signals; the decoder gives left and right back exactly.
var flacChannels = [...]frame.Channels{
	1: frame.ChannelsMono,
	2: frame.ChannelsMidSide,
	3: frame.ChannelsLRC,
	4: frame.ChannelsLRLsRs,
	5: frame.ChannelsLRCLsRs,
	6: frame.ChannelsLRCLfeLsRs,
	7: frame.ChannelsLRCLfeCsSlSr,
	8: frame.ChannelsLRCLfeLsRsSlSr,
}

type flacWriter struct {
	file       *bufferedFile
	enc        *flac.Encoder
	channels   int
	sampleRate int
	blocks     int
	// planes holds one block per channel, reused from block to block.
	planes [][]int32
}

func newFLACWriter(file *bufferedFile, channels, sampleRate int, tags Tags) (*flacWriter, error) {
	info := &meta.StreamInfo{
		BlockSizeMin: blockFrames, BlockSizeMax: blockFrames,
		SampleRate: uint32(sampleRate), NChannels: uint8(channels), BitsPerSample: 24,
	}
	comment := &meta.VorbisComment{Vendor: "mcp-midi-controller"}
	for _, t := range [][2]string{{"TITLE", tags.Title}, {"DATE", tags.Date}, {"COMMENT", tags.Comment}} {
		if t[1] != "" {
			comment.Tags = append(comment.Tags, t)
		}
	}
	// The encoder writes a block without a length as an empty one, so the
	// header carries the comment's size.
	length := int64(4 + len(comment.Vendor) + 4)
	for _, t := range comment.Tags {
		length += int64(4 + len(t[0]) + 1 + len(t[1]))
	}
	enc, err := flac.NewEncoder(file, info, &meta.Block{
		Header: meta.Header{Type: meta.TypeVorbisComment, Length: length},
		Body:   comment,
	})
	if err != nil {
		return nil, err
	}
	planes := make([][]int32, channels)
	for c := range planes {
		planes[c] = make([]int32, blockFrames)
	}
	return &flacWriter{file: file, enc: enc, channels: channels, sampleRate: sampleRate, planes: planes}, nil
}

func (w *flacWriter) WriteBlock(samples []float32) error {
	n := len(samples) / w.channels
	w.blocks++
	subframes := make([]*frame.Subframe, w.channels)
	for c := range subframes {
		plane := w.planes[c][:n]
		for i := range plane {
			q := math.Round(float64(samples[i*w.channels+c]) * flacScale)
			plane[i] = int32(max(-flacScale, min(flacScale-1, q)))
		}
		// Verbatim is the request to analyse: the encoder picks the smallest
		// of a constant, a fixed predictor and the plain samples.
		subframes[c] = &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: plane, NSamples: n}
	}
	return w.enc.WriteFrame(&frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: uint16(n),
			SampleRate: uint32(w.sampleRate), Channels: flacChannels[w.channels], BitsPerSample: 24,
		},
		Subframes: subframes,
	})
}

// streamInfoMinBlock is where STREAMINFO's minimum block size sits in the
// file: after the "fLaC" signature and the block's 4-byte header.
const streamInfoMinBlock = 8

// Close fills in the stream's length and checksum and closes the file.
func (w *flacWriter) Close() error {
	err := w.enc.Close()
	if err == nil && w.blocks > 1 {
		// The encoder records the smallest block it wrote as the stream's
		// minimum. In a stream of fixed-size blocks the last, shorter one does
		// not count: with a minimum unlike the maximum a decoder can no longer
		// tell where a frame sits from its number.
		err = w.file.patch([]byte{blockFrames >> 8, blockFrames & 0xff}, streamInfoMinBlock)
	}
	if ferr := w.file.finish(); err == nil {
		err = ferr
	}
	return err
}
