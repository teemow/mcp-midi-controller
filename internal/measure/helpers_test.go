package measure

import (
	"math"
	"math/rand/v2"
	"testing"
)

// gen builds a signal of the given length from f, which returns the sample of
// channel c at frame i.
func gen(rate, seconds float64, channels int, f func(i, c int) float64) Signal {
	frames := int(math.Round(rate * seconds))
	s := make([]float32, frames*channels)
	for i := range frames {
		for c := range channels {
			s[i*channels+c] = float32(f(i, c))
		}
	}
	return Signal{Samples: s, Channels: channels, SampleRate: rate}
}

// sine is a sine of peak level dbfs at hz, the same in every channel.
func sine(rate, seconds float64, channels int, hz, dbfs float64) Signal {
	amp := math.Pow(10, dbfs/20)
	return gen(rate, seconds, channels, func(i, _ int) float64 {
		return amp * math.Sin(2*math.Pi*hz*float64(i)/rate)
	})
}

// part is a stretch of a 1 kHz sine at a peak level, in a test signal of
// EBU Tech 3341 and 3342.
type part struct {
	dbfs    float64
	seconds float64
}

// parts joins 1 kHz sines at their levels, the same in both channels of a
// stereo signal, the phase running on across the joins.
func parts(rate float64, ps ...part) Signal {
	var s []float32
	frame := 0
	for _, p := range ps {
		amp := math.Pow(10, p.dbfs/20)
		n := int(math.Round(rate * p.seconds))
		for range n {
			x := float32(amp * math.Sin(2*math.Pi*1000*float64(frame)/rate))
			s = append(s, x, x)
			frame++
		}
	}
	return Signal{Samples: s, Channels: 2, SampleRate: rate}
}

// noise is white noise, uniform in [-amp, amp], independent in each channel.
func noise(rate, seconds float64, channels int, amp float64, seed uint64) Signal {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return gen(rate, seconds, channels, func(int, int) float64 {
		return amp * (2*rng.Float64() - 1)
	})
}

// silence is digital silence.
func silence(rate, seconds float64, channels int) Signal {
	return gen(rate, seconds, channels, func(int, int) float64 { return 0 })
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f ± %g", what, got, want, tol)
	}
}
