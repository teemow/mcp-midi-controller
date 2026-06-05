package audiotap

import "math"

// maxFFTSize bounds the LIVE analysis window for spectral features (the ~10 Hz
// get_audio_tap path). A power of two; it keeps the per-poll transform cheap on
// the shared rolling window — the rigorous, full-FFT path is the per-probe
// segment below.
const maxFFTSize = 1024

// segmentFFTSize bounds the per-probe SEGMENT analysis FFT. At 48 kHz, 16384
// samples ≈ 341 ms — enough to match the autocorrelation pitch window so the
// fundamental and the harmonic partials are measured over the same span of time
// (eliminating the recency asymmetry of the live path).
const segmentFFTSize = 1 << 14 // 16384

// minFFTSize is the smallest window we will analyse; below it the spectrum is
// too coarse to be meaningful and we omit spectral features.
const minFFTSize = 256

// spectralBands is the number of log-spaced energy bands reported. Octave-ish
// bands give an agent a compact "EQ curve" of the recent signal.
const spectralBands = 8

// Spectral holds frequency-domain features computed over the most recent
// analysis window: where the energy sits (centroid), how noisy-vs-tonal it is
// (flatness), and a coarse log-spaced band-energy curve (dBFS per band).
type Spectral struct {
	FFTSize     int       `json:"fft_size"`
	CentroidHz  float64   `json:"centroid_hz"`
	Flatness    float64   `json:"flatness"`
	Bands       []float64 `json:"bands_db"`
	BandEdgesHz []float64 `json:"band_edges_hz"`
}

// computeSpectral analyses the most recent power-of-two run of samples (oldest→
// newest as produced by sampleAt) and returns frequency-domain features, or nil
// when there is too little signal or no known sample rate. It is the LIVE path
// (maxFFTSize); the per-probe segment path calls computeSpectralN directly with
// segmentFFTSize.
func computeSpectral(sampleAt func(i int) float32, filled int, sampleRate float64) *Spectral {
	return computeSpectralN(sampleAt, filled, sampleRate, maxFFTSize)
}

// computeSpectralN is computeSpectral with an explicit FFT-size cap so the
// per-probe segment path can use a larger window (segmentFFTSize) than the
// cheap live path.
func computeSpectralN(sampleAt func(i int) float32, filled int, sampleRate float64, maxFFT int) *Spectral {
	if sampleRate <= 0 || filled < minFFTSize {
		return nil
	}
	n := largestPow2(filled)
	if n > maxFFT {
		n = maxFFT
	}
	if n < minFFTSize {
		return nil
	}

	// Take the most recent n samples and apply a Hann window to reduce leakage.
	// winSum (the sum of the Hann coefficients) is the window's coherent gain ×
	// n; winSum/2 is the magnitude a full-scale sine peaks at, used as the dBFS
	// reference so band energies are absolute (not ~6 dB low against n/2).
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

	// Magnitude spectrum over the positive half (bins 1..n/2-1; skip DC).
	half := n / 2
	mag := make([]float64, half)
	for k := 1; k < half; k++ {
		mag[k] = math.Hypot(re[k], im[k])
	}

	binHz := sampleRate / float64(n)

	// Spectral centroid: magnitude-weighted mean frequency.
	var num, den float64
	for k := 1; k < half; k++ {
		num += float64(k) * binHz * mag[k]
		den += mag[k]
	}
	centroid := 0.0
	if den > 0 {
		centroid = num / den
	}

	// Spectral flatness: geometric mean / arithmetic mean of the power spectrum.
	// 1.0 = white-noise-flat, →0 = tonal/peaky.
	var logSum, arithSum float64
	count := 0
	for k := 1; k < half; k++ {
		p := mag[k] * mag[k]
		if p <= 0 {
			p = 1e-20
		}
		logSum += math.Log(p)
		arithSum += p
		count++
	}
	flatness := 0.0
	if count > 0 && arithSum > 0 {
		geoMean := math.Exp(logSum / float64(count))
		flatness = geoMean / (arithSum / float64(count))
	}

	// Log-spaced band energies (dBFS). Bands span from binHz to Nyquist.
	nyquist := sampleRate / 2
	lo := binHz
	if lo < 1 {
		lo = 1
	}
	edges := make([]float64, spectralBands+1)
	ratio := math.Pow(nyquist/lo, 1.0/float64(spectralBands))
	edges[0] = lo
	for b := 1; b <= spectralBands; b++ {
		edges[b] = edges[b-1] * ratio
	}
	bands := make([]float64, spectralBands)
	for b := 0; b < spectralBands; b++ {
		var energy float64
		var bins int
		for k := 1; k < half; k++ {
			f := float64(k) * binHz
			if f >= edges[b] && f < edges[b+1] {
				energy += mag[k] * mag[k]
				bins++
			}
		}
		if bins > 0 {
			energy /= float64(bins)
		}
		// dBFS relative to the Hann coherent-gain power reference (winSum/2)²:
		// the per-bin power a full-scale sine produces. A full-scale tone in a
		// band reads ~0 dBFS instead of ~6 dB low (the old n/2 reference).
		ref := (winSum / 2) * (winSum / 2)
		bands[b] = 10 * math.Log10((energy/ref)+1e-12)
	}

	return &Spectral{
		FFTSize:     n,
		CentroidHz:  centroid,
		Flatness:    flatness,
		Bands:       bands,
		BandEdgesHz: edges,
	}
}

// hann returns the i-th coefficient of an n-point Hann window. Shared by every
// windowed transform here (spectral features, harmonic FFT, onset flux) so the
// window definition lives in one place.
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
// signal (re, im). len(re) must be a power of two and equal len(im). This is a
// tiny, dependency-free transform sized for the small analysis window above.
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
