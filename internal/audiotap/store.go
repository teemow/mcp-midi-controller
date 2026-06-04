// Package audiotap is the off-MCP LAN listener that terminates the ProbeAudioTap
// AUv3 audio stream (github.com/teemow/auv3-probe) and keeps the latest levels
// and a short rolling PCM window in memory, giving an agent "ears" on the rig
// via the get_audio_tap MCP tool.
//
// Like internal/auv3receiver and internal/aumreceiver it is a SEPARATE listener
// from the loopback-only MCP endpoint (the iPad cannot reach loopback). Its
// surface is intentionally tiny: a single WebSocket endpoint that only ingests
// audio + features into an in-memory store. It never touches the engine,
// transports, or hardware, and nothing is written to disk — audio is a private,
// volatile rig signal (see the public-vs-private rule), so it lives only in RAM
// and is exposed read-only through MCP.
package audiotap

import (
	"math"
	"sync"
	"time"
)

// Format mirrors the ProbeAudioTap "format" message (docs/auv3-extension.md,
// "Audio-stream protocol"): the decimated mono PCM layout fixed on connect.
type Format struct {
	Encoding   string  `json:"encoding"`
	Channels   int     `json:"channels"`
	SampleRate float64 `json:"sampleRate"`
	Source     string  `json:"source"`
}

// windowCapacity bounds the rolling PCM window. ~6 s at the default decimated
// rate (44.1 kHz / 4 ≈ 11 kHz) is plenty for "what does the rig sound like right
// now" reasoning while keeping the buffer tiny and the memory bounded regardless
// of how long a tap streams.
const windowCapacity = 1 << 16 // 65536 mono float32 samples (256 KiB)

// waveformBuckets is the number of peak-envelope points get_audio_tap returns so
// an agent can "see" the recent signal shape without shipping raw PCM.
const waveformBuckets = 48

// Store holds the latest audio-tap state. A single tap streams at a time (one
// AUM insert); a second connection simply overwrites the connection metadata.
// All access is mutex-guarded — the WebSocket reader writes, MCP tool calls
// read, on different goroutines.
type Store struct {
	mu sync.Mutex

	connected     bool
	remote        string
	connectedAt   time.Time
	lastMessageAt time.Time

	format     Format
	rms        float32
	peak       float32
	featuresAt time.Time

	// window is a ring of the most recent mono samples (most recent at head-1).
	window []float32
	head   int
	filled int

	audioMessages   int64
	audioSamples    int64
	featureMessages int64
}

// NewStore returns an empty store with a preallocated rolling window.
func NewStore() *Store {
	return &Store{window: make([]float32, windowCapacity)}
}

// Connect marks a tap as connected from remote and clears the previous window so
// stale audio from an earlier session is not mixed into the new one.
func (s *Store) Connect(remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.connected = true
	s.remote = remote
	s.connectedAt = now
	s.lastMessageAt = now
	s.head = 0
	s.filled = 0
	s.rms = 0
	s.peak = 0
	s.format = Format{}
}

// Disconnect marks the tap as gone but keeps the last levels/window so a poll
// right after a drop still reports the final state (with a growing age).
func (s *Store) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
}

// SetFormat records the on-connect format message.
func (s *Store) SetFormat(f Format) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.format = f
	s.lastMessageAt = time.Now()
}

// SetFeatures records a ~10 Hz features message.
func (s *Store) SetFeatures(rms, peak float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rms = rms
	s.peak = peak
	now := time.Now()
	s.featuresAt = now
	s.lastMessageAt = now
	s.featureMessages++
}

// AppendAudio writes decimated mono samples into the rolling window, dropping
// the oldest as needed.
func (s *Store) AppendAudio(samples []float32) {
	if len(samples) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range samples {
		s.window[s.head] = v
		s.head = (s.head + 1) % len(s.window)
		if s.filled < len(s.window) {
			s.filled++
		}
	}
	s.audioMessages++
	s.audioSamples += int64(len(samples))
	s.lastMessageAt = time.Now()
}

// Snapshot is the read-only view returned by the get_audio_tap MCP tool.
type Snapshot struct {
	Connected bool `json:"connected"`

	Source     string  `json:"source,omitempty"`
	Remote     string  `json:"remote,omitempty"`
	Encoding   string  `json:"encoding,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	SampleRate float64 `json:"sample_rate,omitempty"`

	// RMS/Peak are the last reported features; WindowRMS/WindowPeak are computed
	// over the rolling PCM window (more robust than a single 10 Hz frame).
	RMS        float32 `json:"rms"`
	Peak       float32 `json:"peak"`
	WindowRMS  float32 `json:"window_rms"`
	WindowPeak float32 `json:"window_peak"`

	// Waveform is a short peak-envelope (abs) of the window for a quick visual.
	Waveform []float32 `json:"waveform,omitempty"`

	WindowSamples int     `json:"window_samples"`
	WindowSeconds float64 `json:"window_seconds"`

	FeaturesAgeMS    int64 `json:"features_age_ms"`
	LastMessageAgeMS int64 `json:"last_message_age_ms"`
	ConnectedForMS   int64 `json:"connected_for_ms,omitempty"`

	AudioMessages   int64 `json:"audio_messages"`
	FeatureMessages int64 `json:"feature_messages"`
}

// Snapshot computes the current view: last features, window-derived peak/RMS, a
// downsampled peak envelope, and connection/age metadata.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := Snapshot{
		Connected:       s.connected,
		Source:          s.format.Source,
		Remote:          s.remote,
		Encoding:        s.format.Encoding,
		Channels:        s.format.Channels,
		SampleRate:      s.format.SampleRate,
		RMS:             s.rms,
		Peak:            s.peak,
		WindowSamples:   s.filled,
		AudioMessages:   s.audioMessages,
		FeatureMessages: s.featureMessages,
	}

	now := time.Now()
	if !s.featuresAt.IsZero() {
		snap.FeaturesAgeMS = now.Sub(s.featuresAt).Milliseconds()
	}
	if !s.lastMessageAt.IsZero() {
		snap.LastMessageAgeMS = now.Sub(s.lastMessageAt).Milliseconds()
	}
	if s.connected && !s.connectedAt.IsZero() {
		snap.ConnectedForMS = now.Sub(s.connectedAt).Milliseconds()
	}
	if s.format.SampleRate > 0 {
		snap.WindowSeconds = float64(s.filled) / s.format.SampleRate
	}

	snap.WindowPeak, snap.WindowRMS = s.windowLevelsLocked()
	snap.Waveform = s.waveformLocked()
	return snap
}

// windowLevelsLocked computes peak (max abs) and RMS over the filled window.
func (s *Store) windowLevelsLocked() (peak, rms float32) {
	if s.filled == 0 {
		return 0, 0
	}
	var sumSquares float64
	for i := 0; i < s.filled; i++ {
		v := s.window[s.index(i)]
		a := float32(math.Abs(float64(v)))
		if a > peak {
			peak = a
		}
		sumSquares += float64(v) * float64(v)
	}
	return peak, float32(math.Sqrt(sumSquares / float64(s.filled)))
}

// waveformLocked returns up to waveformBuckets peak-abs points across the window
// (oldest→newest), or nil if the window is empty.
func (s *Store) waveformLocked() []float32 {
	if s.filled == 0 {
		return nil
	}
	buckets := waveformBuckets
	if s.filled < buckets {
		buckets = s.filled
	}
	out := make([]float32, buckets)
	for b := 0; b < buckets; b++ {
		start := b * s.filled / buckets
		end := (b + 1) * s.filled / buckets
		if end <= start {
			end = start + 1
		}
		var p float32
		for i := start; i < end; i++ {
			a := float32(math.Abs(float64(s.window[s.index(i)])))
			if a > p {
				p = a
			}
		}
		out[b] = p
	}
	return out
}

// index maps a logical position (0 = oldest) to the ring index.
func (s *Store) index(logical int) int {
	start := 0
	if s.filled == len(s.window) {
		start = s.head // ring is full: oldest is at head
	}
	return (start + logical) % len(s.window)
}
