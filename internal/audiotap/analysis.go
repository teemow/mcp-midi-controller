package audiotap

import (
	"math"
	"sort"
	"strconv"
)

// Analysis-tuning constants. These bound CPU per Snapshot() (the window is
// small) and define what counts as a confident pitch / a real onset.
const (
	// analysisMinSamples is the smallest window we will analyse; below it both
	// the FFT and the autocorrelation are too coarse to be meaningful.
	analysisMinSamples = minFFTSize // 256

	// pitchMinHz/pitchMaxHz bound the f0 search. The decimated Nyquist is
	// ~5.5 kHz; musical fundamentals of interest live well below 2 kHz, and
	// going below 50 Hz buys nothing but octave errors on a short window.
	pitchMinHz = 50.0
	pitchMaxHz = 2000.0

	// acfMaxSamples caps the autocorrelation window. ~0.37 s at 11 kHz covers
	// many periods of even a 50 Hz tone while keeping the O(W·lags) loop cheap.
	acfMaxSamples = 4096

	// pitchClarityThreshold is the minimum normalised autocorrelation peak
	// (McLeod NSDF, ~periodicity in [0,1]) for us to trust an f0. Noise sits
	// well below this; a clean tone is ~1.0.
	pitchClarityThreshold = 0.5

	// maxPartials is how many spectral peaks we report as harmonic partials.
	maxPartials = 8

	// partialFloorDB drops peaks more than this far below the strongest peak,
	// so we report real partials rather than spectral grass.
	partialFloorDB = -60.0

	// onsetMaxSamples/onsetFrame/onsetHop configure the spectral-flux onset
	// detector over the recent window. Short frames with 50% overlap localise
	// note attacks to ~23 ms at 11 kHz.
	onsetMaxSamples = 16384
	onsetFrame      = 512
	onsetHop        = 256
)

// noteNames indexes the twelve pitch classes starting at C, used to render a
// MIDI note number as a name like "A4" or "F#3".
var noteNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// Partial is a single spectral peak interpreted as a harmonic of the detected
// f0: its (interpolated) frequency, level in dBFS, and harmonic number (1 =
// fundamental, 0 when no f0 was detected).
type Partial struct {
	FreqHz   float64 `json:"freq_hz"`
	DB       float64 `json:"db"`
	Harmonic int     `json:"harmonic"`
}

// Analysis is the trusted, Go-computed interpretation of the rolling window so
// an agent gets musical numbers (pitch, harmonics, dynamics, attacks) without
// having to do DSP on base64 PCM. All fields are optional/zero-safe; pitch and
// harmonic fields are only populated when a confident f0 is found.
type Analysis struct {
	// Pitch (only meaningful when Confidence >= pitchClarityThreshold).
	F0Hz       float64 `json:"f0_hz"`
	Note       string  `json:"note,omitempty"`
	MIDINote   int     `json:"midi_note,omitempty"`
	Cents      float64 `json:"cents"`
	Confidence float64 `json:"confidence"`

	// Harmonics: the strongest spectral peaks and an overall harmonic-to-noise
	// ratio in dB (energy at f0's harmonics vs everything else).
	Partials []Partial `json:"partials,omitempty"`
	HNRDb    float64   `json:"hnr_db"`

	// Loudness / dynamics over the window.
	RMSDBFS  float64 `json:"rms_dbfs"`
	PeakDBFS float64 `json:"peak_dbfs"`
	CrestDB  float64 `json:"crest_db"`

	// Onset / transient activity within the recent window.
	OnsetCount   int     `json:"onset_count"`
	MSSinceOnset float64 `json:"ms_since_onset"` // -1 when no onset detected
}

// computeAnalysis builds the Analysis block over the most recent samples (oldest
// →newest via sampleAt). Returns nil when there is too little signal or no known
// sample rate, keeping the snapshot nil-safe when the tap is idle/disconnected.
// This is the LIVE path (cheap FFT, recent-window): the per-probe segment path
// uses computeAnalysisSegment, which aligns f0 and the partials over one window.
func computeAnalysis(sampleAt func(i int) float32, filled int, sampleRate float64) *Analysis {
	if sampleRate <= 0 || filled < analysisMinSamples {
		return nil
	}

	a := &Analysis{MSSinceOnset: -1}

	// Loudness / dynamics over the whole filled window.
	a.RMSDBFS, a.PeakDBFS, a.CrestDB = loudness(sampleAt, filled)

	// Pitch via normalised autocorrelation (McLeod NSDF).
	f0, clarity := detectF0N(sampleAt, filled, sampleRate, acfMaxSamples)
	a.Confidence = clarity
	if clarity >= pitchClarityThreshold && f0 > 0 {
		a.F0Hz = f0
		a.MIDINote, a.Note, a.Cents = noteFromHz(f0)
	}

	// Harmonic partials + HNR from the magnitude spectrum.
	a.Partials, a.HNRDb = harmonicsN(sampleAt, filled, sampleRate, a.F0Hz, maxFFTSize)

	// Onset / transient detection via spectral flux over recent frames.
	a.OnsetCount, a.MSSinceOnset = detectOnsetsN(sampleAt, filled, sampleRate, onsetMaxSamples)

	return a
}

// computeAnalysisSegment is the per-probe analysis over an isolated capture (a
// MarkEpoch/Segment slice, mono-mixed). Unlike the live path it runs the pitch
// detector AND the harmonic FFT over the SAME aligned window — the largest
// power-of-two run up to segmentFFTSize — so the fundamental and the partials
// come from the same span of time (no recency asymmetry) and the FFT covers the
// same duration the autocorrelation uses. Onset runs over the whole segment so
// the note-on attack at the start is detected, not just a sustain tail.
func computeAnalysisSegment(sampleAt func(i int) float32, filled int, sampleRate float64) *Analysis {
	if sampleRate <= 0 || filled < analysisMinSamples {
		return nil
	}

	a := &Analysis{MSSinceOnset: -1}
	a.RMSDBFS, a.PeakDBFS, a.CrestDB = loudness(sampleAt, filled)

	// One aligned window for pitch and harmonics.
	nWin := largestPow2(filled)
	if nWin > segmentFFTSize {
		nWin = segmentFFTSize
	}

	f0, clarity := detectF0N(sampleAt, filled, sampleRate, nWin)
	a.Confidence = clarity
	if clarity >= pitchClarityThreshold && f0 > 0 {
		a.F0Hz = f0
		a.MIDINote, a.Note, a.Cents = noteFromHz(f0)
	}

	a.Partials, a.HNRDb = harmonicsN(sampleAt, filled, sampleRate, a.F0Hz, nWin)
	a.OnsetCount, a.MSSinceOnset = detectOnsetsN(sampleAt, filled, sampleRate, filled)

	return a
}

// loudness returns window RMS and peak in dBFS and the crest factor (peak−RMS,
// in dB). dBFS values are clamped at a -120 dB floor so silence is finite.
func loudness(sampleAt func(i int) float32, filled int) (rmsDB, peakDB, crestDB float64) {
	var sumSquares, peak float64
	for i := 0; i < filled; i++ {
		v := float64(sampleAt(i))
		sumSquares += v * v
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	rms := math.Sqrt(sumSquares / float64(filled))
	rmsDB = AmpToDBFS(rms)
	peakDB = AmpToDBFS(peak)
	crestDB = peakDB - rmsDB
	return rmsDB, peakDB, crestDB
}

// detectF0N estimates the fundamental frequency over the most recent maxSamples
// (0 = whole window) using the McLeod-style normalised square difference
// function (NSDF), returning the frequency in Hz and a clarity score in [0,1]
// (~periodicity). f0 is 0 when no periodic peak is found within [pitchMinHz,
// pitchMaxHz]. maxSamples lets the caller align this window with the harmonic FFT.
func detectF0N(sampleAt func(i int) float32, filled int, sampleRate float64, maxSamples int) (f0, clarity float64) {
	n := filled
	if maxSamples > 0 && n > maxSamples {
		n = maxSamples
	}
	offset := filled - n

	// Copy with the mean removed so DC offset does not inflate the correlation.
	x := make([]float64, n)
	var mean float64
	for i := 0; i < n; i++ {
		x[i] = float64(sampleAt(offset + i))
		mean += x[i]
	}
	mean /= float64(n)
	for i := range x {
		x[i] -= mean
	}

	minLag := int(math.Floor(sampleRate / pitchMaxHz))
	maxLag := int(math.Ceil(sampleRate / pitchMinHz))
	if minLag < 1 {
		minLag = 1
	}
	// Need a usable correlation window (W = n - lag) at the longest lag.
	if maxLag >= n-1 {
		maxLag = n - 2
	}
	if maxLag <= minLag {
		return 0, 0
	}

	nsdf := make([]float64, maxLag+1)
	for lag := minLag; lag <= maxLag; lag++ {
		w := n - lag
		var acf, sq float64
		for i := 0; i < w; i++ {
			a := x[i]
			b := x[i+lag]
			acf += a * b
			sq += a*a + b*b
		}
		if sq > 0 {
			nsdf[lag] = 2 * acf / sq
		}
	}

	// Global max clarity over the search range; if even this is weak, the
	// signal is not periodic (e.g. noise) and we report no pitch.
	globalMax := 0.0
	for lag := minLag; lag <= maxLag; lag++ {
		if nsdf[lag] > globalMax {
			globalMax = nsdf[lag]
		}
	}
	if globalMax <= 0 {
		return 0, 0
	}

	// Pick the FIRST local maximum that reaches a fraction of the global max.
	// This favours the true (shortest) period and avoids subharmonic/octave
	// errors where a longer lag scores almost as high.
	threshold := 0.85 * globalMax
	bestLag := -1
	for lag := minLag + 1; lag < maxLag; lag++ {
		if nsdf[lag] >= threshold && nsdf[lag] > nsdf[lag-1] && nsdf[lag] >= nsdf[lag+1] {
			bestLag = lag
			break
		}
	}
	if bestLag < 0 {
		return 0, globalMax
	}

	// Parabolic interpolation around the peak for sub-sample period precision.
	period := parabolicPeak(nsdf[bestLag-1], nsdf[bestLag], nsdf[bestLag+1], float64(bestLag))
	if period <= 0 {
		return 0, globalMax
	}
	return sampleRate / period, nsdf[bestLag]
}

// harmonicsN computes the magnitude spectrum of the most recent run (capped at
// maxFFT) and returns the strongest peaks as partials plus a harmonic-to-noise
// ratio in dB. When f0 <= 0 the harmonic number is left at 0 and HNR is computed
// against the detected peaks rather than f0 multiples. maxFFT lets the per-probe
// segment path use a larger, pitch-aligned window than the live path.
func harmonicsN(sampleAt func(i int) float32, filled int, sampleRate, f0 float64, maxFFT int) ([]Partial, float64) {
	n := largestPow2(filled)
	if n > maxFFT {
		n = maxFFT
	}
	if n < minFFTSize {
		return nil, 0
	}

	re := make([]float64, n)
	im := make([]float64, n)
	offset := filled - n
	var winSum float64
	for i := 0; i < n; i++ {
		w := hann(i, n)
		winSum += w
		re[i] = float64(sampleAt(offset+i)) * w
	}
	fftRadix2(re, im)

	half := n / 2
	mag := make([]float64, half)
	var totalPower float64
	for k := 1; k < half; k++ {
		mag[k] = math.Hypot(re[k], im[k])
		totalPower += mag[k] * mag[k]
	}
	binHz := sampleRate / float64(n)

	// Find local-maximum peaks and keep the strongest within partialFloorDB of
	// the loudest one. The dBFS reference is the Hann coherent gain (winSum/2),
	// the magnitude a full-scale sine peaks at, so partials read absolute dBFS
	// (a full-scale sine ≈ 0 dBFS, not ~6 dB low against n/2).
	ref := winSum / 2
	type peak struct {
		freq float64
		db   float64
	}
	var peaks []peak
	var maxMag float64
	for k := 2; k < half-1; k++ {
		if mag[k] > mag[k-1] && mag[k] >= mag[k+1] {
			freq := parabolicPeak(mag[k-1], mag[k], mag[k+1], float64(k)) * binHz
			peaks = append(peaks, peak{freq: freq, db: 20 * math.Log10(mag[k]/ref+1e-12)})
			if mag[k] > maxMag {
				maxMag = mag[k]
			}
		}
	}
	if maxMag <= 0 {
		return nil, 0
	}
	maxDB := 20 * math.Log10(maxMag/ref+1e-12)

	// Keep peaks above the floor, sort by level, take the top maxPartials.
	kept := peaks[:0]
	for _, p := range peaks {
		if p.db >= maxDB+partialFloorDB {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].db > kept[j].db })
	if len(kept) > maxPartials {
		kept = kept[:maxPartials]
	}

	partials := make([]Partial, len(kept))
	for i, p := range kept {
		h := 0
		if f0 > 0 {
			h = int(math.Round(p.freq / f0))
			if h < 1 {
				h = 1
			}
		}
		partials[i] = Partial{FreqHz: p.freq, DB: p.db, Harmonic: h}
	}
	// Present partials in frequency order for readability.
	sort.Slice(partials, func(i, j int) bool { return partials[i].FreqHz < partials[j].FreqHz })

	hnr := harmonicToNoise(mag, binHz, f0, totalPower)
	return partials, hnr
}

// harmonicToNoise estimates the ratio (in dB) of energy sitting at the integer
// harmonics of f0 versus the rest of the spectrum. Returns 0 when no f0.
func harmonicToNoise(mag []float64, binHz, f0, totalPower float64) float64 {
	if f0 <= 0 || totalPower <= 0 {
		return 0
	}
	half := len(mag)
	var harmonicPower float64
	for h := 1; ; h++ {
		f := f0 * float64(h)
		if f >= binHz*float64(half) {
			break
		}
		k := int(math.Round(f / binHz))
		// Sum the bin and its immediate neighbours to capture leakage.
		for d := -1; d <= 1; d++ {
			kk := k + d
			if kk >= 1 && kk < half {
				harmonicPower += mag[kk] * mag[kk]
			}
		}
	}
	noisePower := totalPower - harmonicPower
	if noisePower < 1e-12 {
		noisePower = 1e-12
	}
	if harmonicPower < 1e-12 {
		harmonicPower = 1e-12
	}
	return 10 * math.Log10(harmonicPower/noisePower)
}

// detectOnsetsN counts note attacks in the most recent maxSamples (0 = whole
// window) using half-wave rectified spectral flux with an adaptive threshold,
// and reports milliseconds since the most recent onset (-1 if none). Stateless
// and window-local: a clean silence→tone transition yields exactly one onset.
// The segment path passes the whole segment so the note-on attack is in view.
func detectOnsetsN(sampleAt func(i int) float32, filled int, sampleRate float64, maxSamples int) (int, float64) {
	n := filled
	if maxSamples > 0 && n > maxSamples {
		n = maxSamples
	}
	offset := filled - n
	if n < onsetFrame+onsetHop {
		return 0, -1
	}

	numFrames := (n-onsetFrame)/onsetHop + 1
	half := onsetFrame / 2

	flux := make([]float64, numFrames)
	prevMag := make([]float64, half)
	curMag := make([]float64, half)
	re := make([]float64, onsetFrame)
	im := make([]float64, onsetFrame)

	for f := 0; f < numFrames; f++ {
		start := offset + f*onsetHop
		for i := 0; i < onsetFrame; i++ {
			w := hann(i, onsetFrame)
			re[i] = float64(sampleAt(start+i)) * w
			im[i] = 0
		}
		fftRadix2(re, im)
		var sum float64
		for k := 0; k < half; k++ {
			curMag[k] = math.Hypot(re[k], im[k])
			if f > 0 {
				if d := curMag[k] - prevMag[k]; d > 0 {
					sum += d
				}
			}
		}
		flux[f] = sum
		copy(prevMag, curMag)
	}

	// Adaptive threshold: mean + 1.5·stddev of the flux, with a small absolute
	// floor so a flat (silent) window does not produce phantom onsets.
	var mean float64
	for _, v := range flux {
		mean += v
	}
	mean /= float64(numFrames)
	var variance float64
	for _, v := range flux {
		d := v - mean
		variance += d * d
	}
	std := math.Sqrt(variance / float64(numFrames))
	threshold := mean + 1.5*std
	if threshold < 1e-6 {
		threshold = 1e-6
	}

	count := 0
	lastOnsetFrame := -1
	for f := 1; f < numFrames-1; f++ {
		if flux[f] > threshold && flux[f] > flux[f-1] && flux[f] >= flux[f+1] {
			count++
			lastOnsetFrame = f
		}
	}

	msSince := -1.0
	if lastOnsetFrame >= 0 {
		onsetSample := lastOnsetFrame * onsetHop
		samplesFromEnd := n - onsetSample
		msSince = float64(samplesFromEnd) / sampleRate * 1000
	}
	return count, msSince
}

// AmpToDBFS converts a linear amplitude to dBFS with a -120 dB floor (so
// silence is finite). Exported so callers comparing window levels read on the
// same scale as the Analysis loudness fields.
func AmpToDBFS(amp float64) float64 {
	if amp <= 1e-6 {
		return -120
	}
	return 20 * math.Log10(amp)
}

// noteFromHz maps a frequency to the nearest MIDI note, returning the note
// number, its name (e.g. "A4"), and the signed cents offset from equal
// temperament (A4 = 440 Hz).
func noteFromHz(hz float64) (midi int, name string, cents float64) {
	if hz <= 0 {
		return 0, "", 0
	}
	exact := 69 + 12*math.Log2(hz/440.0)
	midi = int(math.Round(exact))
	cents = (exact - float64(midi)) * 100
	if midi < 0 {
		return midi, "", cents
	}
	name = noteNames[((midi%12)+12)%12] + strconv.Itoa(midi/12-1)
	return midi, name, cents
}

// parabolicPeak refines the location of a peak at integer index `idx` given the
// values at idx-1, idx, idx+1, returning the sub-sample position.
func parabolicPeak(ym1, y0, yp1, idx float64) float64 {
	denom := ym1 - 2*y0 + yp1
	if denom == 0 {
		return idx
	}
	return idx + 0.5*(ym1-yp1)/denom
}
