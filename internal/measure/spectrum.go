package measure

import (
	"errors"
	"fmt"
	"math"
)

// The FFT of the band and brightness readings: 16384 points (2.9 Hz a bin at
// 48 kHz, so a 20 Hz band holds several bins), fewer on a shorter stretch,
// never fewer than 4096.
const (
	maxFFT = 1 << 14
	minFFT = 1 << 12
)

// BandLevel is the level of the band [fromHz, toHz] in dB: the power of the
// bins inside it, referenced to a full-scale sine as the audio tap's spectral
// bands are, so that a sine of peak amplitude A inside the band reads
// 20·log10(A) (a full-scale sine 0, 3.01 dB above its Level) and a band with
// nothing in it reads the floor. The power is averaged
// over the channels and, on a stretch longer than one FFT, over Hann windows
// overlapping by half (Welch). A stretch shorter than 4096 frames, and a band
// that is upside down, below 0 Hz, above the Nyquist frequency or narrower
// than one bin, are errors.
func BandLevel(sig Signal, fromHz, toHz float64) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	from, to, err := checkBand([]float64{fromHz, toHz}, v.rate)
	if err != nil {
		return 0, err
	}
	return bandLevel(v, from, to)
}

func bandLevel(v view, from, to float64) (float64, error) {
	var power float64
	for c := range v.chans {
		sp, err := welch(v, func(i int) float64 { return v.at(i, c) })
		if err != nil {
			return 0, err
		}
		p, err := sp.band(from, to)
		if err != nil {
			return 0, err
		}
		power += p
	}
	// A full-scale sine has a mean square of 1/2.
	return powerDB(2 * power / float64(v.chans)), nil
}

// Brightness is the spectral centroid of the mono mix in Hz: the mean
// frequency of the power spectrum, each frequency weighted by its power,
// summed over Hann windows overlapping by half, so the louder windows weigh
// more. A sine reads its frequency and white noise a quarter of the sample
// rate. A stretch shorter than 4096 frames, or a silent one, is an error.
func Brightness(sig Signal) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	return brightness(v)
}

func brightness(v view) (float64, error) {
	sp, err := welch(v, v.mono)
	if err != nil {
		return 0, err
	}
	var num, den float64
	for k := 1; k < len(sp.p); k++ { // without the 0 Hz bin
		num += float64(k) * sp.binHz * sp.p[k]
		den += sp.p[k]
	}
	if den <= silentPower {
		return 0, errors.New("the stretch is silent: it has no brightness")
	}
	return num / den, nil
}

// spectrum is a Welch estimate of a signal's power by frequency: p[k] is the
// mean square in bin k (one-sided, so the bins sum to the signal's mean
// square), binHz apart, from 0 Hz to the Nyquist frequency.
type spectrum struct {
	p     []float64
	binHz float64
}

// band sums the power of the bins in [from, to].
func (sp spectrum) band(from, to float64) (float64, error) {
	lo := int(math.Ceil(from / sp.binHz))
	hi := min(int(math.Floor(to/sp.binHz)), len(sp.p)-1)
	if lo > hi {
		return 0, fmt.Errorf("the band [%g, %g] Hz is narrower than one bin of the analysis (%.1f Hz)", from, to, sp.binHz)
	}
	var sum float64
	for k := lo; k <= hi; k++ {
		sum += sp.p[k]
	}
	return sum, nil
}

// welch estimates the power spectrum of the signal get returns frame by frame
// (Welch's method): Hann windows of the largest power of two up to maxFFT,
// overlapping by half or more, spread so the last one ends with the stretch.
func welch(v view, get func(i int) float64) (spectrum, error) {
	if v.frames < minFFT {
		return spectrum{}, fmt.Errorf("the stretch is %s (%d frames); its spectrum needs at least %d frames (%s)", v.duration(v.frames), v.frames, minFFT, v.duration(minFFT))
	}
	n := min(largestPow2(v.frames), maxFFT)
	windows := 1
	if v.frames > n {
		windows = (v.frames-n+n/2-1)/(n/2) + 1
	}
	w := make([]float64, n)
	var wPower float64
	for i := range w {
		w[i] = hann(i, n)
		wPower += w[i] * w[i]
	}
	re := make([]float64, n)
	im := make([]float64, n)
	acc := make([]float64, n/2+1)
	for j := range windows {
		start := 0
		if windows > 1 {
			start = j * (v.frames - n) / (windows - 1)
		}
		for i := range n {
			re[i] = get(start+i) * w[i]
			im[i] = 0
		}
		fftRadix2(re, im)
		for k := range acc {
			acc[k] += re[k]*re[k] + im[k]*im[k]
		}
	}
	// Parseval: the bins of one window sum to n·Σ(x·w)², and Σ(x·w)² is the
	// mean square times Σw². The bins between 0 Hz and the Nyquist frequency
	// stand for their mirror images too.
	scale := 1 / (float64(windows) * float64(n) * wPower)
	for k := range acc {
		acc[k] *= scale
		if k > 0 && k < n/2 {
			acc[k] *= 2
		}
	}
	return spectrum{p: acc, binHz: v.rate / float64(n)}, nil
}

// hann returns the i-th coefficient of an n-point Hann window, as the audio
// tap's spectral analysis has it.
func hann(i, n int) float64 {
	return 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
}

// largestPow2 returns the largest power of two <= n (0 for n < 1).
func largestPow2(n int) int {
	if n < 1 {
		return 0
	}
	p := 1
	for p<<1 <= n {
		p <<= 1
	}
	return p
}

// fftRadix2 computes the in-place radix-2 decimation-in-time FFT of the complex
// signal (re, im). len(re) must be a power of two and equal len(im). A copy of
// the audio tap's transform.
func fftRadix2(re, im []float64) {
	n := len(re)
	if n <= 1 {
		return
	}
	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j &^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	// Danielson–Lanczos butterflies.
	for length := 2; length <= n; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		wRe, wIm := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += length {
			curRe, curIm := 1.0, 0.0
			for j := 0; j < length/2; j++ {
				a := i + j
				b := i + j + length/2
				tRe := curRe*re[b] - curIm*im[b]
				tIm := curRe*im[b] + curIm*re[b]
				re[b] = re[a] - tRe
				im[b] = im[a] - tIm
				re[a] += tRe
				im[a] += tIm
				curRe, curIm = curRe*wRe-curIm*wIm, curRe*wIm+curIm*wRe
			}
		}
	}
}
