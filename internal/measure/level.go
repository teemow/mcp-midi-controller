package measure

import "math"

// Level is the rms level in dBFS: the power averaged over the channels, so a
// sine of peak amplitude A reads 20·log10(A) - 3.01. Silence, and a signal
// Measure would refuse, read the floor.
func Level(sig Signal) float64 {
	v, err := newView(sig)
	if err != nil {
		return Floor
	}
	return level(v)
}

func level(v view) float64 {
	var sum float64
	for i := range v.frames {
		for c := range v.chans {
			x := v.at(i, c)
			sum += x * x
		}
	}
	return powerDB(sum / float64(v.frames*v.chans))
}

// SamplePeak is the largest sample of any channel, in dBFS.
func SamplePeak(sig Signal) float64 {
	v, err := newView(sig)
	if err != nil {
		return Floor
	}
	return samplePeak(v)
}

func samplePeak(v view) float64 {
	var peak float64
	for i := range v.frames {
		for c := range v.chans {
			peak = max(peak, math.Abs(v.at(i, c)))
		}
	}
	return ampDB(peak)
}

// CrestFactor is the sample peak above the rms level, in dB: about 3 dB for a
// sine, more the more the signal's peaks stand out. Silence reads 0.
func CrestFactor(sig Signal) float64 {
	v, err := newView(sig)
	if err != nil {
		return 0
	}
	return crestFactor(v)
}

func crestFactor(v view) float64 { return samplePeak(v) - level(v) }

// TruePeak is the largest peak of any channel in dBTP, including the peaks
// between the samples: the signal is oversampled four times with a polyphase
// FIR, after ITU-R BS.1770-4 Annex 2. A sine whose peaks fall between the
// samples reads its peak, up to 3 dB above its sample peak.
func TruePeak(sig Signal) float64 {
	v, err := newView(sig)
	if err != nil {
		return Floor
	}
	return truePeak(v)
}

// The true-peak interpolator: four phases of twelve taps, 48 coefficients.
const (
	tpFactor = 4
	tpTaps   = 12
	tpBeta   = 5.0 // the Kaiser window's shape: the flattest reading up to 20 kHz at 48 kHz
)

// tpPhases are the interpolator's coefficients: phase p computes the signal p/4
// of a sample past frame n from frames n-5 to n+6. They are a windowed sinc
// (cut off at the Nyquist frequency, Kaiser window), each phase scaled to unit
// gain at 0 Hz, so phase 0 is the samples themselves and the true peak never
// reads under the sample peak.
var tpPhases = designInterpolator()

func designInterpolator() [tpFactor][tpTaps]float64 {
	var h [tpFactor][tpTaps]float64
	const half = tpTaps / 2
	for p := range tpFactor {
		frac := float64(p) / tpFactor
		var sum float64
		for t := range tpTaps {
			u := frac - float64(t-(half-1)) // from tap to the point computed, in samples
			h[p][t] = sinc(u) * kaiser(u/half, tpBeta)
			sum += h[p][t]
		}
		for t := range h[p] {
			h[p][t] /= sum
		}
	}
	return h
}

// truePeak oversamples each channel. The points between samples are computed
// only where all twelve taps lie inside the stretch: past its edges the signal
// is unknown, and a step to silence there would ring into a peak the signal
// does not have.
func truePeak(v view) float64 {
	const before = tpTaps/2 - 1 // taps before the frame
	var peak float64
	for c := range v.chans {
		for i := range v.frames {
			peak = max(peak, math.Abs(v.at(i, c)))
		}
		for n := before; n+tpTaps-before <= v.frames; n++ {
			for p := 1; p < tpFactor; p++ {
				var y float64
				for t, h := range tpPhases[p] {
					y += h * v.at(n-before+t, c)
				}
				peak = max(peak, math.Abs(y))
			}
		}
	}
	return ampDB(peak)
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// kaiser is the Kaiser window at r, from -1 to 1 across the window.
func kaiser(r, beta float64) float64 {
	if r < -1 || r > 1 {
		return 0
	}
	return besselI0(beta*math.Sqrt(1-r*r)) / besselI0(beta)
}

// besselI0 is the modified Bessel function of the first kind, order 0.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 100; k++ {
		f := x / (2 * float64(k))
		term *= f * f
		sum += term
		if term < 1e-15*sum {
			break
		}
	}
	return sum
}
