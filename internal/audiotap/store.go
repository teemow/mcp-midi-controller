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
//
// The stream is full-fidelity: interleaved native float32 at the host sample
// rate, every channel. The window therefore stores interleaved samples and most
// indexing is in terms of frames (one sample per channel) so analysis and clip
// extraction stay channel-correct.
package audiotap

import (
	"math"
	"sync"
	"time"
)

// Format mirrors the ProbeAudioTap "format" message (docs/auv3-extension.md,
// "Audio-stream protocol"): the interleaved float32 PCM layout fixed on connect.
type Format struct {
	Encoding   string  `json:"encoding"`
	Channels   int     `json:"channels"`
	SampleRate float64 `json:"sampleRate"`
	Source     string  `json:"source"`
}

// windowCapacity bounds the rolling PCM window in interleaved float32 samples.
// 1<<20 (~4 MiB) holds ~10.9 s of stereo at 48 kHz (2 ch × 48000 × ~10.9 s),
// which is ample for "what does the rig sound like right now" reasoning and for
// extracting a clean per-probe segment while keeping memory bounded regardless
// of how long a tap streams.
const windowCapacity = 1 << 20 // 1048576 interleaved float32 samples (4 MiB)

// waveformBuckets is the number of peak-envelope points get_audio_tap returns so
// an agent can "see" the recent signal shape without shipping raw PCM.
const waveformBuckets = 48

// maxInterArrivalGap is how long the stream may stall between audio batches
// before the boundary is recorded as a discontinuity. Batches normally arrive
// ~10 ms apart; a gap beyond this means the producer stalled (network hiccup,
// app suspended) and any segment spanning the boundary is non-contiguous.
const maxInterArrivalGap = 200 * time.Millisecond

// Store holds the latest state of ONE audio tap (one ProbeAudioTap AUM insert):
// its rolling PCM window, levels and connection metadata. Several taps stream
// concurrently, each in its own Store; the Registry owns the named set and the
// receiver routes each connection's frames here. Within a Store a re-connection
// (same name) simply starts a fresh session (the window is cleared).
//
// All access is mutex-guarded — the WebSocket reader writes, MCP tool calls
// read, on different goroutines.
type Store struct {
	mu sync.Mutex

	// name is the tap's stable registry identity (the key it is addressed by),
	// distinct from the format's Source (a human label the producer may send).
	name string

	connected     bool
	remote        string
	connectedAt   time.Time
	lastMessageAt time.Time

	format     Format
	channels   int // interleave stride (>=1); derived from the format message
	rms        float32
	peak       float32
	featuresAt time.Time

	// window is a ring of the most recent interleaved samples (most recent at
	// head-1). head/filled count interleaved samples; frames = filled/channels.
	window []float32
	head   int
	filled int

	// audioSamples is the monotonic count of interleaved samples ever appended;
	// it doubles as the absolute epoch used by MarkEpoch/Segment.
	audioMessages   int64
	audioSamples    int64
	featureMessages int64

	// Gap/jitter guard: gaps holds absolute interleaved-sample positions where a
	// stall longer than maxInterArrivalGap was observed (the boundary sits just
	// before the batch that arrived late). lastAppendAt is the wall-clock time of
	// the previous batch. Positions that scroll out of the window are pruned.
	gaps         []int64
	lastAppendAt time.Time
}

// NewStore returns an empty store with a preallocated rolling window.
func NewStore() *Store {
	return &Store{window: make([]float32, windowCapacity), channels: 1}
}

// setName records the tap's registry identity. Called once when the Registry
// creates or adopts the Store, before it is shared with concurrent readers.
func (s *Store) setName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

// Name returns the tap's registry identity ("" for a bare standalone store).
func (s *Store) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// Connected reports whether the tap is currently streaming. Cheap accessor for
// the registry's default-tap selection (no analysis, unlike Snapshot).
func (s *Store) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Source returns the latest format "source" label (the producer's human name
// for the tap), or "" before a format message arrives. Cheap accessor.
func (s *Store) Source() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.format.Source
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
	s.channels = 1
	s.gaps = nil
	s.lastAppendAt = time.Time{}
}

// Disconnect marks the tap as gone but keeps the last levels/window so a poll
// right after a drop still reports the final state (with a growing age).
func (s *Store) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
}

// SetFormat records the on-connect format message and the interleave stride.
func (s *Store) SetFormat(f Format) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.format = f
	s.channels = f.Channels
	if s.channels < 1 {
		s.channels = 1
	}
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

// Level returns the latest reported (~10 Hz) RMS without computing any
// frequency/musical analysis. It is the cheap accessor for hot polling paths
// (e.g. settle loops) that only need the recent level, not a full Snapshot.
func (s *Store) Level() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rms
}

// SampleRate returns the current format sample rate (0 when unknown). It is the
// cheap accessor for callers that only need the rate to translate durations,
// without paying for a full Snapshot's analysis.
func (s *Store) SampleRate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.format.SampleRate
}

// AppendAudio writes interleaved float32 samples into the rolling window,
// dropping the oldest as needed, and records a discontinuity boundary when the
// stream stalled since the previous batch.
func (s *Store) AppendAudio(samples []float32) {
	if len(samples) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if !s.lastAppendAt.IsZero() && now.Sub(s.lastAppendAt) > maxInterArrivalGap {
		// The boundary is the absolute position of the first sample of this
		// (late) batch: everything strictly after it is on the far side of the
		// stall, so a segment straddling it is non-contiguous.
		s.gaps = append(s.gaps, s.audioSamples)
	}
	s.lastAppendAt = now

	for _, v := range samples {
		s.window[s.head] = v
		s.head = (s.head + 1) % len(s.window)
		if s.filled < len(s.window) {
			s.filled++
		}
	}
	s.audioMessages++
	s.audioSamples += int64(len(samples))
	s.lastMessageAt = now
	s.pruneGapsLocked()
}

// pruneGapsLocked drops discontinuity boundaries that have scrolled out of the
// resident window so the slice stays small. Must hold s.mu.
func (s *Store) pruneGapsLocked() {
	if len(s.gaps) == 0 {
		return
	}
	oldest := s.audioSamples - int64(s.filled)
	i := 0
	for i < len(s.gaps) && s.gaps[i] < oldest {
		i++
	}
	if i > 0 {
		s.gaps = append(s.gaps[:0], s.gaps[i:]...)
	}
}

// Snapshot is the read-only view returned by the get_audio_tap MCP tool.
type Snapshot struct {
	Connected bool `json:"connected"`

	// Name is the tap's registry identity (how MCP tools address it); Source is
	// the producer-supplied human label from the format message.
	Name       string  `json:"name,omitempty"`
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

	// Spectral holds frequency-domain features (centroid, flatness, log band
	// energies) computed over the most recent analysis window. Omitted when
	// there is too little signal or no known sample rate.
	Spectral *Spectral `json:"spectral,omitempty"`

	// Analysis holds the trusted musical interpretation of the window: detected
	// pitch (f0/note/cents), harmonic partials + HNR, loudness/crest, and onset
	// activity. Omitted when there is too little signal or no known sample rate.
	Analysis *Analysis `json:"analysis,omitempty"`

	// WindowSamples is the number of frames (samples per channel) resident in
	// the window; WindowSeconds is that duration in seconds.
	WindowSamples int     `json:"window_samples"`
	WindowSeconds float64 `json:"window_seconds"`

	FeaturesAgeMS    int64 `json:"features_age_ms"`
	LastMessageAgeMS int64 `json:"last_message_age_ms"`
	ConnectedForMS   int64 `json:"connected_for_ms,omitempty"`

	AudioMessages   int64 `json:"audio_messages"`
	FeatureMessages int64 `json:"feature_messages"`
}

// Snapshot computes the current view: last features, window-derived peak/RMS, a
// downsampled peak envelope, and connection/age metadata. Frequency/musical
// analysis runs over the mono mix of the resident frames.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	frames := s.framesLocked()
	snap := Snapshot{
		Connected:       s.connected,
		Name:            s.name,
		Source:          s.format.Source,
		Remote:          s.remote,
		Encoding:        s.format.Encoding,
		Channels:        s.format.Channels,
		SampleRate:      s.format.SampleRate,
		RMS:             s.rms,
		Peak:            s.peak,
		WindowSamples:   frames,
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
		snap.WindowSeconds = float64(frames) / s.format.SampleRate
	}

	snap.WindowPeak, snap.WindowRMS = s.windowLevelsLocked()
	snap.Waveform = s.waveformLocked()
	monoAt := func(i int) float32 { return s.monoAtLocked(i) }
	snap.Spectral = computeSpectral(monoAt, frames, s.format.SampleRate)
	snap.Analysis = computeAnalysis(monoAt, frames, s.format.SampleRate)
	return snap
}

// Clip is the read-only view returned by the get_audio_clip MCP tool and the
// per-probe segment extractor: interleaved float32 PCM (oldest→newest) plus the
// format needed to interpret it. Contiguous reports whether the returned span is
// free of stream stalls (always true for the live rolling Clip).
type Clip struct {
	Connected  bool      `json:"connected"`
	Encoding   string    `json:"encoding"`
	SampleRate float64   `json:"sample_rate"`
	Channels   int       `json:"channels"`
	Contiguous bool      `json:"contiguous"`
	Samples    []float32 `json:"-"`
}

// Clip returns up to maxFrames of the most recent frames (oldest→newest) from
// the rolling window as interleaved samples, along with the current format.
// maxFrames <= 0 means the whole filled window.
func (s *Store) Clip(maxFrames int) Clip {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.framesLocked()
	frames := total
	if maxFrames > 0 && maxFrames < frames {
		frames = maxFrames
	}
	ch := s.channels
	out := make([]float32, frames*ch)
	offset := (total - frames) * ch
	for i := range out {
		out[i] = s.window[s.index(offset+i)]
	}
	return Clip{
		Connected:  s.connected,
		Encoding:   "f32le",
		SampleRate: s.format.SampleRate,
		Channels:   ch,
		Contiguous: true,
		Samples:    out,
	}
}

// SegmentSnapshot extracts the absolute range [start, end) (positions from
// MarkEpoch) and returns a Snapshot whose Spectral/Analysis are computed over
// that isolated, aligned segment (full-FFT, mono-mixed), the underlying Clip for
// WAV writing, and ok=false when the range has scrolled out of the window. This
// is the per-probe path: it never reads the shared rolling window mid-analysis,
// so back-to-back probes do not contaminate each other.
func (s *Store) SegmentSnapshot(start, end int64) (Snapshot, Clip, bool) {
	// Extract the clip and the connection metadata under one lock so the
	// reported source/remote/age cannot disagree with the captured segment;
	// the expensive analysis runs after the lock is released.
	s.mu.Lock()
	clip, ok := s.segmentLocked(start, end)
	var source, remote string
	var connectedForMS int64
	if ok {
		source = s.format.Source
		remote = s.remote
		if s.connected && !s.connectedAt.IsZero() {
			connectedForMS = time.Since(s.connectedAt).Milliseconds()
		}
	}
	s.mu.Unlock()
	if !ok {
		return Snapshot{}, Clip{}, false
	}

	snap := analyzeSegmentClip(clip)
	snap.Source = source
	snap.Remote = remote
	snap.ConnectedForMS = connectedForMS
	return snap, clip, true
}

// analyzeSegmentClip computes a Snapshot (levels, waveform, spectral, musical
// analysis) over an interleaved Clip, mono-mixing across its channels. It is the
// segment counterpart of Store.Snapshot and uses the aligned, full-FFT segment
// analysis path.
func analyzeSegmentClip(clip Clip) Snapshot {
	ch := clip.Channels
	if ch < 1 {
		ch = 1
	}
	frames := len(clip.Samples) / ch
	sampleAt := func(i int) float32 { return clip.Samples[i] }
	at := func(frame int) float32 { return monoMixAt(sampleAt, frame, ch) }

	snap := Snapshot{
		Connected:     clip.Connected,
		Encoding:      clip.Encoding,
		Channels:      ch,
		SampleRate:    clip.SampleRate,
		WindowSamples: frames,
	}
	if clip.SampleRate > 0 {
		snap.WindowSeconds = float64(frames) / clip.SampleRate
	}

	var peak, sumSquares float64
	for i := 0; i < frames; i++ {
		v := float64(at(i))
		if a := math.Abs(v); a > peak {
			peak = a
		}
		sumSquares += v * v
	}
	snap.WindowPeak = float32(peak)
	if frames > 0 {
		snap.WindowRMS = float32(math.Sqrt(sumSquares / float64(frames)))
	}

	snap.Waveform = waveformOf(at, frames)
	snap.Spectral = computeSpectralN(at, frames, clip.SampleRate, segmentFFTSize)
	snap.Analysis = computeAnalysisSegment(at, frames, clip.SampleRate)
	return snap
}

// MarkEpoch returns the current absolute interleaved-sample position. Pair two
// marks (before/after a probe) and pass them to Segment to extract exactly the
// audio captured in that window, isolated from prior or subsequent probes.
func (s *Store) MarkEpoch() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audioSamples
}

// Segment extracts the interleaved samples in the absolute range [start, end)
// (positions as returned by MarkEpoch). ok is false when the range is invalid or
// has already scrolled out of the resident window. The returned Clip's
// Contiguous flag is false when a stream stall was recorded inside the range.
func (s *Store) Segment(start, end int64) (Clip, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.segmentLocked(start, end)
}

// segmentLocked is Segment's body; callers must hold s.mu. Splitting it out lets
// SegmentSnapshot extract the clip and the connection metadata under a single
// lock (the expensive analysis then runs unlocked).
func (s *Store) segmentLocked(start, end int64) (Clip, bool) {
	if end <= start {
		return Clip{}, false
	}
	oldest := s.audioSamples - int64(s.filled)
	if start < oldest || end > s.audioSamples {
		return Clip{}, false
	}
	// Align the range to channel (frame) boundaries so we never return a partial
	// frame of interleaved data.
	ch := int64(s.channels)
	if ch < 1 {
		ch = 1
	}
	if rem := start % ch; rem != 0 {
		start += ch - rem
	}
	end -= end % ch
	if end <= start {
		return Clip{}, false
	}

	n := int(end - start)
	out := make([]float32, n)
	base := int(start - oldest)
	for i := 0; i < n; i++ {
		out[i] = s.window[s.index(base+i)]
	}
	return Clip{
		Connected:  s.connected,
		Encoding:   "f32le",
		SampleRate: s.format.SampleRate,
		Channels:   s.channels,
		Contiguous: !s.hasGapLocked(start, end),
		Samples:    out,
	}, true
}

// hasGapLocked reports whether a discontinuity boundary lies strictly inside the
// absolute range (start, end). Must hold s.mu.
func (s *Store) hasGapLocked(start, end int64) bool {
	for _, g := range s.gaps {
		if g > start && g < end {
			return true
		}
	}
	return false
}

// framesLocked is the number of whole frames resident in the window.
func (s *Store) framesLocked() int {
	if s.channels < 1 {
		return s.filled
	}
	return s.filled / s.channels
}

// monoMixAt averages the `channels` interleaved samples of one frame, fetching
// interleaved sample i via at (0 = oldest). channels < 1 is treated as mono.
// Shared by the ring-buffer (monoAtLocked) and flat-clip (analyzeSegmentClip)
// mono mixes so the channel-averaging rule lives in one place.
func monoMixAt(at func(i int) float32, frame, channels int) float32 {
	if channels < 1 {
		channels = 1
	}
	base := frame * channels
	var sum float32
	for c := 0; c < channels; c++ {
		sum += at(base + c)
	}
	return sum / float32(channels)
}

// monoAtLocked returns the mono mix (channel average) of frame i (0 = oldest).
func (s *Store) monoAtLocked(frame int) float32 {
	return monoMixAt(func(i int) float32 { return s.window[s.index(i)] }, frame, s.channels)
}

// windowLevelsLocked computes peak (max abs) and RMS over the filled window's
// mono mix (per-frame), so the levels are channel-correct for stereo.
func (s *Store) windowLevelsLocked() (peak, rms float32) {
	frames := s.framesLocked()
	if frames == 0 {
		return 0, 0
	}
	var sumSquares float64
	for i := 0; i < frames; i++ {
		v := s.monoAtLocked(i)
		a := float32(math.Abs(float64(v)))
		if a > peak {
			peak = a
		}
		sumSquares += float64(v) * float64(v)
	}
	return peak, float32(math.Sqrt(sumSquares / float64(frames)))
}

// waveformLocked returns up to waveformBuckets peak-abs points (mono mix) across
// the window (oldest→newest), or nil if the window is empty.
func (s *Store) waveformLocked() []float32 {
	return waveformOf(s.monoAtLocked, s.framesLocked())
}

// waveformOf reduces a frame-indexed mono signal (0 = oldest) to up to
// waveformBuckets peak-abs envelope points, or nil when there are no frames.
// Shared by the live window (monoAtLocked) and segment-clip paths.
func waveformOf(at func(frame int) float32, frames int) []float32 {
	if frames == 0 {
		return nil
	}
	buckets := waveformBuckets
	if frames < buckets {
		buckets = frames
	}
	out := make([]float32, buckets)
	for b := 0; b < buckets; b++ {
		start := b * frames / buckets
		end := (b + 1) * frames / buckets
		if end <= start {
			end = start + 1
		}
		var p float32
		for i := start; i < end; i++ {
			a := float32(math.Abs(float64(at(i))))
			if a > p {
				p = a
			}
		}
		out[b] = p
	}
	return out
}

// index maps a logical interleaved position (0 = oldest) to the ring index.
func (s *Store) index(logical int) int {
	start := 0
	if s.filled == len(s.window) {
		start = s.head // ring is full: oldest is at head
	}
	return (start + logical) % len(s.window)
}
