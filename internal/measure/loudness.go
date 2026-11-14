package measure

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Loudness after ITU-R BS.1770-4 and EBU Tech 3342.
const (
	// lufsOffset is BS.1770's -0.691 dB, which makes a 1 kHz sine read its
	// rms level through the K-weighting: 0 dBFS in one channel reads -3.01.
	lufsOffset = -0.691
	// absoluteGate drops the blocks under -70 LUFS: silence and the noise floor.
	absoluteGate = -70.0
	// relativeGate drops the blocks this far under the loudness of the blocks
	// above the absolute gate, for the integrated loudness.
	relativeGate = -10.0
	// rangeGate is the relative gate of the loudness range (EBU Tech 3342).
	rangeGate = -20.0
	// minLoudnessRate is the lowest sample rate the K-weighting is worked out
	// for: its shelf lies at 1.7 kHz.
	minLoudnessRate = 8000.0
)

// The gating blocks, in steps of 100 ms: a block of the integrated loudness
// is 400 ms long and starts every 100 ms (75 % overlap); a short-term block of
// the loudness range is 3 s long and starts every 100 ms.
const (
	stepsPerBlock      = 4
	stepsPerShortBlock = 30
)

// Loudness is the integrated loudness in LUFS after ITU-R BS.1770-4: the
// channels K-weighted and summed (each with weight 1; a mono stretch is one
// channel), measured in 400 ms blocks with 75 % overlap, the blocks under -70
// LUFS dropped and then those more than 10 LU under the loudness of the rest.
// A stretch shorter than one block, or one every block of which is gated
// away (silence), is an error.
func Loudness(sig Signal) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	return loudness(v)
}

func loudness(v view) (float64, error) {
	steps, step, err := kSteps(v, stepsPerBlock, "the integrated loudness")
	if err != nil {
		return 0, err
	}
	blocks := blockPowers(steps, step, stepsPerBlock)
	gated := gate(blocks, relativeGate)
	if len(gated) == 0 {
		return 0, errors.New("the stretch is silent: every block lies under the -70 LUFS gate")
	}
	return lufs(mean(gated)), nil
}

// LoudnessRange is the loudness range in LU after EBU Tech 3342: the spread
// between the 10th and the 95th percentile of the short-term loudness (3 s
// blocks, one every 100 ms), after dropping the blocks under -70 LUFS and
// those more than 20 LU under the loudness of the rest. A stretch shorter
// than one 3 s block, or a silent one, is an error.
func LoudnessRange(sig Signal) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	return loudnessRange(v)
}

func loudnessRange(v view) (float64, error) {
	steps, step, err := kSteps(v, stepsPerShortBlock, "the loudness range")
	if err != nil {
		return 0, err
	}
	blocks := blockPowers(steps, step, stepsPerShortBlock)
	gated := gate(blocks, rangeGate)
	if len(gated) == 0 {
		return 0, errors.New("the stretch is silent: every short-term block lies under the -70 LUFS gate")
	}
	levels := make([]float64, len(gated))
	for i, p := range gated {
		levels[i] = lufs(p)
	}
	slices.Sort(levels)
	return percentile(levels, 0.95) - percentile(levels, 0.10), nil
}

// PeakToLoudness is the true peak above the integrated loudness, in dB (the
// peak-to-loudness ratio, PLR). It is an error where Loudness is.
func PeakToLoudness(sig Signal) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	return peakToLoudness(v)
}

func peakToLoudness(v view) (float64, error) {
	l, err := loudness(v)
	if err != nil {
		return 0, err
	}
	return truePeak(v) - l, nil
}

// kSteps K-weights the channels and returns the sum of their squares in each
// whole 100 ms step, and the frames in a step. It refuses a stretch with fewer
// steps than need, for what.
func kSteps(v view, need int, what string) (steps []float64, step int, err error) {
	if v.rate < minLoudnessRate {
		return nil, 0, fmt.Errorf("the sample rate %g Hz is too low for %s: the K-weighting needs at least %g Hz", v.rate, what, minLoudnessRate)
	}
	step = int(math.Round(v.rate / 10))
	n := v.frames / step
	if n < need {
		return nil, 0, fmt.Errorf("the stretch is %s; %s needs at least %s", v.duration(v.frames), what, v.duration(need*step))
	}
	steps = make([]float64, n)
	for c := range v.chans {
		k := kWeighting(v.rate)
		for i := range n * step {
			y := k.process(v.at(i, c))
			steps[i/step] += y * y
		}
	}
	return steps, step, nil
}

// blockPowers sums the steps into blocks of size steps, one starting at every
// step, and returns each block's mean square (summed over the channels).
func blockPowers(steps []float64, step, size int) []float64 {
	if len(steps) < size {
		return nil
	}
	out := make([]float64, len(steps)-size+1)
	for j := range out {
		var sum float64
		for _, s := range steps[j : j+size] {
			sum += s
		}
		out[j] = sum / float64(size*step)
	}
	return out
}

// gate keeps the blocks above the absolute gate, and of those the ones above
// the relative gate under their mean loudness.
func gate(blocks []float64, relative float64) []float64 {
	var above []float64
	for _, p := range blocks {
		if lufs(p) > absoluteGate {
			above = append(above, p)
		}
	}
	if len(above) == 0 {
		return nil
	}
	threshold := lufs(mean(above)) + relative
	var kept []float64
	for _, p := range above {
		if lufs(p) > threshold {
			kept = append(kept, p)
		}
	}
	return kept
}

// lufs is the loudness of a block's mean square (summed over the channels).
func lufs(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1) // under every gate; never returned
	}
	return lufsOffset + 10*math.Log10(p)
}

func mean(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// percentile reads the sorted values at p (0 to 1), at the nearest rank, as
// EBU Tech 3342 does.
func percentile(sorted []float64, p float64) float64 {
	return sorted[int(math.Round(float64(len(sorted)-1)*p))]
}

// biquad is one second-order section, run in transposed direct form II.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	s1, s2             float64
}

func (q *biquad) process(x float64) float64 {
	y := q.b0*x + q.s1
	q.s1 = q.b1*x - q.a1*y + q.s2
	q.s2 = q.b2*x - q.a2*y
	return y
}

// kFilter is BS.1770's K-weighting: a high shelf for the head (+4 dB above
// about 1.7 kHz) followed by a high-pass (the RLB curve, about 38 Hz).
type kFilter struct{ shelf, highpass biquad }

func (k *kFilter) process(x float64) float64 { return k.highpass.process(k.shelf.process(x)) }

// kWeighting works the K-weighting's two biquads out for the sample rate from
// the analog filters BS.1770's 48 kHz coefficients describe; at 48 kHz they are
// those coefficients.
func kWeighting(rate float64) *kFilter {
	// The shelf: corner, gain and Q of the filter BS.1770 specifies at 48 kHz.
	const (
		shelfHz   = 1681.974450955533
		shelfGain = 3.999843853973347 // dB
		shelfQ    = 0.7071752369554196
		shelfBand = 0.4996667741545416 // the exponent of the band gain
	)
	k := math.Tan(math.Pi * shelfHz / rate)
	vh := math.Pow(10, shelfGain/20)
	vb := math.Pow(vh, shelfBand)
	a0 := 1 + k/shelfQ + k*k
	shelf := biquad{
		b0: (vh + vb*k/shelfQ + k*k) / a0,
		b1: 2 * (k*k - vh) / a0,
		b2: (vh - vb*k/shelfQ + k*k) / a0,
		a1: 2 * (k*k - 1) / a0,
		a2: (1 - k/shelfQ + k*k) / a0,
	}

	// The high-pass. BS.1770 gives its numerator as 1, -2, 1 at 48 kHz, and
	// its -0.691 dB is reckoned with that numerator, so it is kept at every
	// rate.
	const (
		highpassHz = 38.13547087602444
		highpassQ  = 0.5003270373238773
	)
	k = math.Tan(math.Pi * highpassHz / rate)
	a0 = 1 + k/highpassQ + k*k
	highpass := biquad{
		b0: 1,
		b1: -2,
		b2: 1,
		a1: 2 * (k*k - 1) / a0,
		a2: (1 - k/highpassQ + k*k) / a0,
	}
	return &kFilter{shelf: shelf, highpass: highpass}
}
