package measure

import (
	"fmt"
	"math"
	"testing"
)

// A sine at -20 dBFS: its rms lies 3.01 dB under its peak.
func TestSineLevels(t *testing.T) {
	for _, rate := range []float64{44100, 48000, 96000} {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%g Hz, %d channels", rate, channels), func(t *testing.T) {
				sig := sine(rate, 1, channels, 1000, -20)
				near(t, "Level", Level(sig), -23.01, 0.01)
				near(t, "SamplePeak", SamplePeak(sig), -20, 0.01)
				near(t, "CrestFactor", CrestFactor(sig), 3.01, 0.02)
				near(t, "TruePeak", TruePeak(sig), -20, 0.01)
			})
		}
	}
}

// Level averages the power over the channels: one silent channel halves it.
func TestLevelAveragesChannels(t *testing.T) {
	sig := gen(48000, 1, 2, func(i, c int) float64 {
		if c == 1 {
			return 0
		}
		return 0.1 * math.Sin(2*math.Pi*1000*float64(i)/48000)
	})
	near(t, "Level", Level(sig), -26.02, 0.01)
	near(t, "SamplePeak", SamplePeak(sig), -20, 0.01)
}

// A sine at a quarter of the sample rate, shifted by 45°, has its samples at
// ±0.707 of its peak: the sample peak reads -3.01 dBFS, the true peak finds
// the peak between the samples.
func TestTruePeakBetweenSamples(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amp        float64
		samplePeak float64
		truePeak   float64
	}{
		{"full scale", 1, -3.01, 0},
		{"samples at full scale", math.Sqrt2, 0, 3.01},
		{"-6 dBFS", 0.5, -9.03, -6.02},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig := gen(48000, 1, 2, func(i, _ int) float64 {
				return tc.amp * math.Sin(math.Pi/2*float64(i)+math.Pi/4)
			})
			near(t, "SamplePeak", SamplePeak(sig), tc.samplePeak, 0.01)
			near(t, "TruePeak", TruePeak(sig), tc.truePeak, 0.05)
		})
	}
}

// The interpolator reads the peak of a sine within EBU Tech 3341's tolerance
// for a true-peak meter (+0.2 / -0.4 dB) up to 20 kHz at 48 kHz, whichever
// phase the samples take.
func TestTruePeakAcrossFrequencies(t *testing.T) {
	const rate = 48000
	for _, hz := range []float64{100, 1000, rate / 8, rate / 6, rate / 4, 15000, 18000, 20000} {
		for _, phase := range []float64{0, 0.2, 0.45, 0.7, 1.1} {
			sig := gen(rate, 0.05, 1, func(i, _ int) float64 {
				return 0.5 * math.Sin(2*math.Pi*hz*float64(i)/rate+phase)
			})
			got := TruePeak(sig) - ampDB(0.5)
			if got > 0.2 || got < -0.4 {
				t.Errorf("%g Hz, phase %g: true peak off by %+.3f dB", hz, phase, got)
			}
		}
	}
}

// Phase 0 of the interpolator is the samples themselves, and every phase has
// unit gain at 0 Hz.
func TestInterpolatorPhases(t *testing.T) {
	for p, h := range tpPhases {
		var sum float64
		for _, c := range h {
			sum += c
		}
		near(t, fmt.Sprintf("phase %d gain", p), sum, 1, 1e-12)
	}
	for i, h := range tpPhases[0] {
		want := 0.0
		if i == tpTaps/2-1 {
			want = 1
		}
		near(t, fmt.Sprintf("phase 0 tap %d", i), h, want, 1e-12)
	}
}

// Silence reads the floor, and so does a signal Measure would refuse.
func TestLevelsOfSilenceAndNothing(t *testing.T) {
	for name, sig := range map[string]Signal{
		"silence":     silence(48000, 1, 2),
		"empty":       {Channels: 2, SampleRate: 48000},
		"no channels": {Samples: []float32{0.5}, SampleRate: 48000},
		"no rate":     {Samples: []float32{0.5}, Channels: 1},
		"NaN":         {Samples: []float32{float32(math.NaN())}, Channels: 1, SampleRate: 48000},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Level(sig); got != Floor {
				t.Errorf("Level = %g, want %g", got, Floor)
			}
			if got := SamplePeak(sig); got != Floor {
				t.Errorf("SamplePeak = %g, want %g", got, Floor)
			}
			if got := TruePeak(sig); got != Floor {
				t.Errorf("TruePeak = %g, want %g", got, Floor)
			}
			if got := CrestFactor(sig); got != 0 {
				t.Errorf("CrestFactor = %g, want 0", got)
			}
		})
	}
}
