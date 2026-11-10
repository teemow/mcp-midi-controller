package audiotap

// This file is the seamless recorder under the rolling window: a Capture
// receives every batch the tap delivers, in order, and writes it to a file as
// it arrives. Nothing is joined afterwards, so a recording has no seams, and
// where the stream itself stalled the capture says so.
//
// A capture is the one place the daemon puts tap audio on disk for longer than
// a probe. The file is raw interleaved float32 (the stream's own samples); the
// recording package turns it into a file a player opens.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

// Stall is a place where the stream did not arrive on time: the audio after
// it does not follow the audio before it without a break.
type Stall struct {
	// Frame is how many frames had been captured when the late batch arrived.
	Frame int64 `json:"frame"`
	// GapMS is how long the stream had been silent on the wire.
	GapMS int64 `json:"gap_ms"`
	// Reconnect marks a stall in which the tap dropped its connection and
	// dialled in again.
	Reconnect bool `json:"reconnect,omitempty"`
}

// CaptureState is where a capture stands.
type CaptureState struct {
	Channels   int
	SampleRate float64
	// Frames counts the whole frames written so far.
	Frames int64
	// FirstSound and LastSound are the first and the last frame holding a
	// sample at or over the capture's threshold; -1 while there is none.
	FirstSound, LastSound int64
	Stalls                []Stall
	Started               time.Time
	// LastAudio is when the latest batch arrived (zero before the first).
	LastAudio time.Time
	// Ended says why the capture stopped taking audio by itself: its length
	// limit, a change of the tap's format, a write error. Empty while it runs.
	Ended string
}

// EndedFull is CaptureState.Ended of a capture that reached the length it was
// started with.
const EndedFull = "the recording reached its longest allowed length"

// Capture writes one tap's stream to a file until it is stopped.
type Capture struct {
	store *Store

	mu         sync.Mutex
	f          *os.File
	w          *bufio.Writer
	scratch    []byte
	threshold  float32
	maxSamples int64 // interleaved samples; 0 means no limit
	samples    int64 // interleaved samples written
	reconnect  bool  // the tap dialled in again since the last batch
	st         CaptureState
	closed     bool
}

// StartCapture begins writing the tap's stream to path as raw interleaved
// little-endian float32. threshold is the linear sample magnitude that counts
// as sound (for FirstSound / LastSound); maxFrames bounds the capture's
// length (0: unbounded). The tap must be streaming and have announced its
// format, because the file is only meaningful with a channel count and a rate.
func (s *Store) StartCapture(path string, threshold float32, maxFrames int64) (*Capture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return nil, errors.New("the audio tap is not streaming")
	}
	if s.format.SampleRate <= 0 || s.format.Channels < 1 {
		return nil, errors.New("the audio tap has not announced its format yet")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	c := &Capture{
		store: s, f: f, w: bufio.NewWriterSize(f, 1<<16),
		threshold:  threshold,
		maxSamples: maxFrames * int64(s.channels),
		st: CaptureState{
			Channels: s.channels, SampleRate: s.format.SampleRate,
			FirstSound: -1, LastSound: -1, Started: time.Now(),
		},
	}
	s.captures = append(s.captures, c)
	return c, nil
}

// write takes one batch from the tap. It is called with the store's lock held,
// so batches arrive in stream order.
func (c *Capture) write(samples []float32, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.st.Ended != "" {
		return
	}
	if c.maxSamples > 0 && c.samples+int64(len(samples)) > c.maxSamples {
		samples = samples[:c.maxSamples-c.samples]
		defer func() { c.st.Ended = EndedFull }()
	}
	ch := int64(c.st.Channels)
	if !c.st.LastAudio.IsZero() {
		if gap := now.Sub(c.st.LastAudio); gap > maxInterArrivalGap || c.reconnect {
			c.st.Stalls = append(c.st.Stalls, Stall{Frame: c.samples / ch, GapMS: gap.Milliseconds(), Reconnect: c.reconnect})
		}
	}
	c.reconnect = false
	c.st.LastAudio = now

	if need := len(samples) * 4; cap(c.scratch) < need {
		c.scratch = make([]byte, need)
	}
	buf := c.scratch[:len(samples)*4]
	for i, v := range samples {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
		if v >= c.threshold || v <= -c.threshold {
			frame := (c.samples + int64(i)) / ch
			if c.st.FirstSound < 0 {
				c.st.FirstSound = frame
			}
			c.st.LastSound = frame
		}
	}
	if _, err := c.w.Write(buf); err != nil {
		c.st.Ended = "writing the recording failed: " + err.Error()
		return
	}
	c.samples += int64(len(samples))
	c.st.Frames = c.samples / ch
}

// SoundFromHere forgets the sound heard so far: FirstSound and LastSound
// then describe what follows. A recorder calls it when the music starts, so
// whatever sounded before (a tail still ringing, a preset loading) is not
// taken for the first sound.
func (c *Capture) SoundFromHere() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.FirstSound, c.st.LastSound = -1, -1
}

// noteReconnect records that the tap dialled in again; the next batch is then
// marked as following a break.
func (c *Capture) noteReconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnect = true
}

// noteFormat ends the capture when the tap comes back with another layout:
// samples of two layouts in one file would be noise.
func (c *Capture) noteFormat(channels int, sampleRate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.st.Ended != "" {
		return
	}
	if channels != c.st.Channels || sampleRate != c.st.SampleRate {
		c.st.Ended = fmt.Sprintf("the tap's format changed (%d channel(s) at %g Hz, was %d at %g Hz)",
			channels, sampleRate, c.st.Channels, c.st.SampleRate)
	}
}

// State reports where the capture stands.
func (c *Capture) State() CaptureState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st
	st.Stalls = append([]Stall(nil), c.st.Stalls...)
	return st
}

// Stop detaches the capture from its tap and closes the file. The returned
// state is final. Stopping twice is harmless.
func (c *Capture) Stop() (CaptureState, error) {
	c.store.detachCapture(c)
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st
	st.Stalls = append([]Stall(nil), c.st.Stalls...)
	if c.closed {
		return st, nil
	}
	c.closed = true
	err := c.w.Flush()
	if cerr := c.f.Close(); err == nil {
		err = cerr
	}
	return st, err
}

// detachCapture removes c from the store's captures.
func (s *Store) detachCapture(c *Capture) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.captures {
		if x == c {
			s.captures = append(s.captures[:i], s.captures[i+1:]...)
			return
		}
	}
}
