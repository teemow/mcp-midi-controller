package audiotap

import (
	"math"
	"testing"
)

// TestComputeSpectralSinusoid feeds a pure tone and asserts the centroid lands
// near the tone frequency and the flatness is low (tonal, not noise-flat).
func TestComputeSpectralSinusoid(t *testing.T) {
	const (
		sampleRate = 11025.0
		freq       = 1000.0
		n          = 4096
	)
	samples := make([]float32, n)
	for i := 0; i < n; i++ {
		samples[i] = float32(0.8 * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}

	sp := computeSpectral(func(i int) float32 { return samples[i] }, n, sampleRate)
	if sp == nil {
		t.Fatal("expected spectral features, got nil")
	}
	if sp.FFTSize != maxFFTSize {
		t.Fatalf("FFTSize = %d, want %d", sp.FFTSize, maxFFTSize)
	}
	// Centroid should be within a few bins of the tone (bin ~= 11025/1024 ≈ 11 Hz).
	if math.Abs(sp.CentroidHz-freq) > 150 {
		t.Fatalf("CentroidHz = %.1f, want near %.0f", sp.CentroidHz, freq)
	}
	// A pure tone is highly tonal: flatness must be well below 1.0.
	if sp.Flatness > 0.1 {
		t.Fatalf("Flatness = %.3f, want < 0.1 for a pure tone", sp.Flatness)
	}
	if len(sp.Bands) != spectralBands || len(sp.BandEdgesHz) != spectralBands+1 {
		t.Fatalf("bands=%d edges=%d, want %d/%d", len(sp.Bands), len(sp.BandEdgesHz), spectralBands, spectralBands+1)
	}
}

// TestComputeSpectralTooSmall returns nil below the minimum window / no rate.
func TestComputeSpectralTooSmall(t *testing.T) {
	z := func(int) float32 { return 0 }
	if sp := computeSpectral(z, minFFTSize-1, 11025); sp != nil {
		t.Fatal("expected nil for sub-minimum window")
	}
	if sp := computeSpectral(z, 4096, 0); sp != nil {
		t.Fatal("expected nil for unknown sample rate")
	}
}

// TestLargestPow2 sanity-checks the helper.
func TestLargestPow2(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 2: 2, 3: 2, 5: 4, 1023: 512, 1024: 1024, 2000: 1024}
	for in, want := range cases {
		if got := largestPow2(in); got != want {
			t.Fatalf("largestPow2(%d) = %d, want %d", in, got, want)
		}
	}
}
