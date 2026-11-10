// Package sound measures what an instrument preset sounds like from the audio
// a note produced, and keeps those measurements as the rig's sound catalogue.
//
// A preset's name says little: one sounds at the written pitch, the next an
// octave below it, a third twenty decibels quieter or with a second note
// layered in. The measurements here are the facts a song is written against:
// which note sounds, how loud, how it starts and ends, and for a drum kit
// which notes are pads and what each one is.
//
// The package does no IO but its catalogue's files. Audio comes in as the
// samples a tap captured around one note.
package sound

import (
	"fmt"
	"math"
	"math/cmplx"
	"sort"
)

// Clip is the audio captured around one note: interleaved samples starting at
// the moment the note was sent.
type Clip struct {
	Samples    []float32
	Channels   int
	SampleRate float64
	// HoldMS is how long after the clip's start the note was released.
	HoldMS float64
}

// Partial is one strong component of a sound.
type Partial struct {
	Hz float64 `yaml:"hz" json:"hz"`
	// Note is the nearest note's name and DB the level below the strongest
	// partial (0 for that one).
	Note string  `yaml:"note" json:"note"`
	DB   float64 `yaml:"db" json:"db"`
}

// Sound is one preset as measured: what a single note did at the tap.
type Sound struct {
	// Measured is when (RFC 3339), Note and Velocity what was played.
	Measured string `yaml:"measured,omitempty" json:"measured,omitempty"`
	Note     int    `yaml:"note" json:"note"`
	Velocity int    `yaml:"velocity,omitempty" json:"velocity,omitempty"`

	// Transpose is the semitones to add to a written note so it sounds as
	// written: 12 for a preset that sounds an octave low.
	Transpose int `yaml:"transpose" json:"transpose"`
	// Sounds is the note that sounded, Hz its frequency and Cents how far it
	// is from that note. WobbleCents is how far the pitch of the held note
	// wandered. Unpitched is set when no pitch was found.
	Sounds      string  `yaml:"sounds,omitempty" json:"sounds,omitempty"`
	Hz          float64 `yaml:"hz,omitempty" json:"hz,omitempty"`
	Cents       float64 `yaml:"cents,omitempty" json:"cents,omitempty"`
	WobbleCents float64 `yaml:"wobble_cents,omitempty" json:"wobble_cents,omitempty"`
	Unpitched   bool    `yaml:"unpitched,omitempty" json:"unpitched,omitempty"`

	// LevelDB is the rms of the held note and PeakDB its sample peak, in
	// dBFS.
	LevelDB float64 `yaml:"level_db" json:"level_db"`
	PeakDB  float64 `yaml:"peak_db" json:"peak_db"`

	// LatencyMS is from the note being sent to the sound starting, AttackMS
	// from there to full level. Sustains tells whether the held note kept
	// its level; when it did not, DecayMS is how long it took to fall 20 dB.
	// ReleaseMS is how long the sound lasted after the release (Rings: it
	// outlasted the capture).
	LatencyMS float64 `yaml:"latency_ms,omitempty" json:"latency_ms,omitempty"`
	AttackMS  float64 `yaml:"attack_ms" json:"attack_ms"`
	Sustains  bool    `yaml:"sustains" json:"sustains"`
	DecayMS   float64 `yaml:"decay_ms,omitempty" json:"decay_ms,omitempty"`
	ReleaseMS float64 `yaml:"release_ms,omitempty" json:"release_ms,omitempty"`
	Rings     bool    `yaml:"rings,omitempty" json:"rings,omitempty"`

	// Partials are the strongest components, loudest first.
	Partials []Partial `yaml:"partials,omitempty" json:"partials,omitempty"`
	// Remarks say in words what stands out: a second note, a wandering
	// pitch, silence.
	Remarks []string `yaml:"remarks,omitempty" json:"remarks,omitempty"`

	// Pads are a kit's notes that make a sound, and Lanes its drum map: the
	// lane names a song's drum grid uses, each on its pad's note.
	Pads  []Pad          `yaml:"pads,omitempty" json:"pads,omitempty"`
	Lanes map[string]int `yaml:"lanes,omitempty" json:"lanes,omitempty"`

	// Pinned lists the fields set by hand ("transpose", "lanes"); measuring
	// the preset again keeps them.
	Pinned []string `yaml:"pinned,omitempty" json:"pinned,omitempty"`
	// Comment is a note for whoever picks sounds.
	Comment string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// Silent reports whether the measurement heard nothing.
func (s *Sound) Silent() bool { return s.PeakDB <= silentDB }

// silentDB is the peak below which a capture counts as silence.
const silentDB = -60.0

// Levels of the analysis, relative to a sound's own peak.
const (
	onsetBelowPeak   = 30.0 // the sound starts where it comes within this of its peak
	attackBelowPeak  = 3.0  // and has arrived at this
	sustainBelowPeak = 12.0 // a held note that fell further did not sustain
	decayBelowPeak   = 20.0
	releaseBelowHold = 30.0 // the sound is over this far under its level at the release
)

// envHop is the envelope's resolution.
const envHopMS = 5.0

// mono mixes a clip down and returns its sample peak over every channel.
func mono(c Clip) (m []float64, peak float64) {
	ch := max(c.Channels, 1)
	m = make([]float64, len(c.Samples)/ch)
	for i := range m {
		var sum float64
		for k := 0; k < ch; k++ {
			v := float64(c.Samples[i*ch+k])
			if a := math.Abs(v); a > peak {
				peak = a
			}
			sum += v
		}
		m[i] = sum / float64(ch)
	}
	return m, peak
}

// dbfs converts an amplitude to dBFS, bounded below.
func dbfs(a float64) float64 {
	if a <= 1e-9 {
		return -180
	}
	return 20 * math.Log10(a)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// envelope is the rms of consecutive hops, in dBFS.
func envelope(m []float64, hop int) []float64 {
	if hop < 1 {
		hop = 1
	}
	env := make([]float64, 0, len(m)/hop+1)
	for i := 0; i+hop <= len(m); i += hop {
		var sum float64
		for _, v := range m[i : i+hop] {
			sum += v * v
		}
		env = append(env, dbfs(math.Sqrt(sum/float64(hop))))
	}
	return env
}

func rmsDB(m []float64) float64 {
	if len(m) == 0 {
		return -180
	}
	var sum float64
	for _, v := range m {
		sum += v * v
	}
	return dbfs(math.Sqrt(sum / float64(len(m))))
}

// MeasureNote analyses the capture of one held note of a melodic instrument.
func MeasureNote(c Clip, note, velocity int) Sound {
	s := Sound{Note: note, Velocity: velocity, LevelDB: -180, PeakDB: -180}
	m, peak := mono(c)
	s.PeakDB = round1(dbfs(peak))
	if c.SampleRate <= 0 || len(m) == 0 || s.Silent() {
		s.Unpitched = true
		s.Remarks = append(s.Remarks, "silent: nothing sounded for this note")
		return s
	}
	sr := c.SampleRate
	hop := int(sr * envHopMS / 1000)
	env := envelope(m, hop)
	top := -180.0
	topAt := 0
	for i, v := range env {
		if v > top {
			top, topAt = v, i
		}
	}
	hold := int(c.HoldMS / envHopMS)
	if hold <= 0 || hold > len(env) {
		hold = len(env)
	}

	// Where the sound starts, and where it has arrived.
	onset := 0
	for i, v := range env {
		if v >= top-onsetBelowPeak {
			onset = i
			break
		}
	}
	arrived := onset
	for i := onset; i < len(env); i++ {
		if env[i] >= top-attackBelowPeak {
			arrived = i
			break
		}
	}
	s.LatencyMS = float64(onset) * envHopMS
	s.AttackMS = float64(arrived-onset) * envHopMS

	// The held note: its level, and whether it kept it.
	from, to := onset*hop, min(hold*hop, len(m))
	if to <= from {
		to = len(m)
	}
	s.LevelDB = round1(rmsDB(m[from:to]))
	last := env[max(onset, hold-int(100/envHopMS)):hold]
	end := -180.0
	if len(last) > 0 {
		var sum float64
		for _, v := range last {
			sum += v
		}
		end = sum / float64(len(last))
	}
	s.Sustains = end >= top-sustainBelowPeak
	if !s.Sustains {
		s.DecayMS = float64(len(env)-topAt) * envHopMS
		for i := topAt; i < len(env); i++ {
			if env[i] <= top-decayBelowPeak {
				s.DecayMS = float64(i-topAt) * envHopMS
				break
			}
		}
	}

	// After the release.
	if hold < len(env) {
		floor := math.Max(end-releaseBelowHold, silentDB)
		s.Rings = true
		s.ReleaseMS = float64(len(env)-hold) * envHopMS
		for i := hold; i < len(env); i++ {
			if env[i] <= floor {
				s.Rings = false
				s.ReleaseMS = float64(i-hold) * envHopMS
				break
			}
		}
	}

	// Pitch, over the held note once it has arrived.
	pitchFrom := min(arrived*hop+int(0.03*sr), to)
	track := pitchTrack(m[pitchFrom:to], sr)
	if len(track) == 0 {
		s.Unpitched = true
		s.Remarks = append(s.Remarks, "no steady pitch: noise, a chord or a sound that changes too fast to name a note")
	} else {
		sorted := append([]float64(nil), track...)
		sort.Float64s(sorted)
		hz := sorted[len(sorted)/2]
		midi := 69 + 12*math.Log2(hz/440)
		nearest := math.Round(midi)
		s.Hz = math.Round(hz*100) / 100
		s.Sounds = NoteName(int(nearest))
		s.Cents = math.Round((midi-nearest)*100) + 0 // + 0 turns a negative zero positive
		s.Transpose = note - int(nearest)
		lo, hi := sorted[len(sorted)/20], sorted[len(sorted)-1-len(sorted)/20]
		s.WobbleCents = math.Round(1200 * math.Log2(hi/lo))
	}

	s.Partials = partials(m[pitchFrom:to], sr)
	if !s.Unpitched {
		settlePitch(&s)
		describePitch(&s)
		describeLayers(&s)
	}
	return s
}

// settlePitch corrects a pitch that is only the common period of several
// notes sounding at once: when nothing sounds at the pitch found, the
// strongest partial is the note.
func settlePitch(s *Sound) {
	if len(s.Partials) == 0 {
		return
	}
	for _, p := range s.Partials {
		if math.Abs(p.Hz-s.Hz) <= 0.03*s.Hz {
			return
		}
	}
	hz := s.Partials[0].Hz
	midi := 69 + 12*math.Log2(hz/440)
	nearest := math.Round(midi)
	s.Hz, s.Sounds, s.Cents = hz, NoteName(int(nearest)), math.Round((midi-nearest)*100)+0
	s.Transpose = s.Note - int(nearest)
	s.Remarks = append(s.Remarks, "several notes sound at once; the pitch given is the strongest one's")
}

// describePitch puts what stands out about a pitch into words.
func describePitch(s *Sound) {
	switch t := s.Transpose; {
	case t == 0:
	case t%12 == 0 && t > 0:
		s.Remarks = append(s.Remarks, fmt.Sprintf("sounds %s below the written note", octaves(t/12)))
	case t%12 == 0:
		s.Remarks = append(s.Remarks, fmt.Sprintf("sounds %s above the written note", octaves(-t/12)))
	default:
		s.Remarks = append(s.Remarks, fmt.Sprintf("sounds %s for a written %s: %d semitone(s) off, not an octave", s.Sounds, NoteName(s.Note), -t))
	}
	if math.Abs(s.Cents) >= 20 {
		s.Remarks = append(s.Remarks, fmt.Sprintf("out of tune by %+.0f cents", s.Cents))
	}
	if s.WobbleCents >= 40 {
		s.Remarks = append(s.Remarks, fmt.Sprintf("the held note wanders over %.0f cents", s.WobbleCents))
	}
}

func octaves(n int) string {
	if n == 1 {
		return "an octave"
	}
	return fmt.Sprintf("%d octaves", n)
}

// describeLayers reports strong partials that are no harmonic of the note
// that sounded: a second note in the preset.
func describeLayers(s *Sound) {
	if s.Hz <= 0 {
		return
	}
	for _, p := range s.Partials {
		ratio := p.Hz / s.Hz
		if ratio < 0.97 {
			// Below the pitch found: a layer under it (or a pitch read an
			// octave high).
			s.Remarks = append(s.Remarks, fmt.Sprintf("also sounds %s below it (%.0f dB)", p.Note, p.DB))
			continue
		}
		if h := math.Round(ratio); math.Abs(ratio-h) > 0.03*h && p.DB >= -15 {
			s.Remarks = append(s.Remarks, fmt.Sprintf("also sounds %s (%.0f dB), not a harmonic of %s", p.Note, p.DB, s.Sounds))
		}
	}
}

// Pitch tracking. A frame's pitch is the first strong peak of its normalized
// autocorrelation, on the signal decimated to keep the search cheap.
const (
	pitchMinHz    = 25.0
	pitchMaxHz    = 2500.0
	pitchFrameMS  = 90.0
	pitchHopMS    = 30.0
	pitchClarity  = 0.5  // a frame below this has no pitch
	pitchFirstOf  = 0.85 // the first peak this close to the best one wins
	pitchRateGoal = 12000.0
)

// pitchTrack returns the pitch of every frame of m that has one, in Hz.
func pitchTrack(m []float64, sr float64) []float64 {
	d := max(int(sr/pitchRateGoal), 1)
	y := make([]float64, 0, len(m)/d)
	for i := 0; i+d <= len(m); i += d {
		var sum float64
		for _, v := range m[i : i+d] {
			sum += v
		}
		y = append(y, sum/float64(d))
	}
	rate := sr / float64(d)
	frame, hop := int(rate*pitchFrameMS/1000), int(rate*pitchHopMS/1000)
	var out []float64
	for i := 0; i+frame <= len(y); i += hop {
		if hz, clarity := framePitch(y[i:i+frame], rate); clarity >= pitchClarity {
			out = append(out, hz)
		}
	}
	if len(out) == 0 && len(y) >= frame/2 {
		// A note shorter than a frame: take what there is.
		if hz, clarity := framePitch(y, rate); clarity >= pitchClarity {
			out = append(out, hz)
		}
	}
	return out
}

// framePitch finds one frame's pitch and how clear it is (0..1).
func framePitch(y []float64, rate float64) (hz, clarity float64) {
	n := len(y)
	var mean float64
	for _, v := range y {
		mean += v
	}
	mean /= float64(n)
	x := make([]float64, n)
	var energy float64
	for i, v := range y {
		x[i] = v - mean
		energy += x[i] * x[i]
	}
	if energy < 1e-12 {
		return 0, 0
	}
	lo, hi := max(int(rate/pitchMaxHz), 2), min(int(rate/pitchMinHz), n/2)
	if hi <= lo+2 {
		return 0, 0
	}
	// The normalized square difference function: 1 at a perfect period. Its
	// norm is the energy of the two overlapping stretches, from running sums.
	squares := make([]float64, n+1)
	for i, v := range x {
		squares[i+1] = squares[i] + v*v
	}
	acf := make([]float64, hi+1)
	for lag := lo - 1; lag <= hi; lag++ {
		var prod float64
		for i, v := range x[:n-lag] {
			prod += v * x[i+lag]
		}
		if norm := squares[n-lag] + squares[n] - squares[lag]; norm > 0 {
			acf[lag] = 2 * prod / norm
		}
	}
	best := 0.0
	for lag := lo; lag < hi; lag++ {
		if acf[lag] > best && acf[lag] >= acf[lag-1] && acf[lag] >= acf[lag+1] {
			best = acf[lag]
		}
	}
	if best <= 0 {
		return 0, 0
	}
	for lag := lo; lag < hi; lag++ {
		if acf[lag] >= pitchFirstOf*best && acf[lag] >= acf[lag-1] && acf[lag] >= acf[lag+1] {
			a, b, c := acf[lag-1], acf[lag], acf[lag+1]
			off := 0.0
			if den := a - 2*b + c; den != 0 {
				off = 0.5 * (a - c) / den
			}
			return rate / (float64(lag) + off), b
		}
	}
	return 0, 0
}

// Spectrum.
const (
	partialsFFT   = 16384
	partialsMaxHz = 8000.0
	partialsFloor = -30.0 // partials further under the strongest are left out
	partialsMost  = 6
)

// partials returns the strongest spectral peaks of m, loudest first.
func partials(m []float64, sr float64) []Partial {
	mag, binHz := spectrum(m, sr, partialsFFT)
	if mag == nil {
		return nil
	}
	type peak struct{ hz, amp float64 }
	var peaks []peak
	for k := 2; k < len(mag)-1 && float64(k)*binHz < partialsMaxHz; k++ {
		if mag[k] > mag[k-1] && mag[k] >= mag[k+1] {
			a, b, c := mag[k-1], mag[k], mag[k+1]
			off := 0.0
			if den := a - 2*b + c; den != 0 {
				off = 0.5 * (a - c) / den
			}
			peaks = append(peaks, peak{(float64(k) + off) * binHz, b})
		}
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].amp > peaks[j].amp })
	var out []Partial
	for _, p := range peaks {
		if len(out) == partialsMost || (len(out) > 0 && dbfs(p.amp)-dbfs(peaks[0].amp) < partialsFloor) {
			break
		}
		near := false
		for _, o := range out {
			if math.Abs(p.hz-o.Hz) < math.Max(12, o.Hz*0.03) {
				near = true
			}
		}
		if near || p.hz < 20 {
			continue
		}
		midi := int(math.Round(69 + 12*math.Log2(p.hz/440)))
		out = append(out, Partial{Hz: math.Round(p.hz*10) / 10, Note: NoteName(midi), DB: round1(dbfs(p.amp) - dbfs(peaks[0].amp))})
	}
	return out
}

// spectrum returns the magnitudes of the first n samples of m (fewer are
// padded) under a Hann window, and the width of a bin.
func spectrum(m []float64, sr float64, n int) ([]float64, float64) {
	if len(m) < 64 || sr <= 0 {
		return nil, 0
	}
	used := min(len(m), n)
	buf := make([]complex128, n)
	for i := 0; i < used; i++ {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(used-1))
		buf[i] = complex(m[i]*w, 0)
	}
	fft(buf)
	mag := make([]float64, n/2)
	for k := range mag {
		mag[k] = cmplx.Abs(buf[k]) * 4 / float64(used)
	}
	return mag, sr / float64(n)
}

// fft is an in-place radix-2 transform; len(x) must be a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Rect(1, -2*math.Pi/float64(size))
		for i := 0; i < n; i += size {
			wn := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := x[i+k], x[i+k+size/2]*wn
				x[i+k], x[i+k+size/2] = u+v, u-v
				wn *= w
			}
		}
	}
}

var noteNames = [...]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// NoteName names a MIDI note; C4 is middle C (60).
func NoteName(n int) string {
	if n < 0 {
		return fmt.Sprintf("note %d", n)
	}
	return fmt.Sprintf("%s%d", noteNames[n%12], n/12-1)
}
