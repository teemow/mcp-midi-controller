package measure

import (
	"math"
	"strings"
	"testing"
)

// stereo builds a stereo signal from a left and a right channel.
func stereo(rate, seconds float64, left, right func(i int) float64) Signal {
	return gen(rate, seconds, 2, func(i, c int) float64 {
		if c == 0 {
			return left(i)
		}
		return right(i)
	})
}

func tone(rate, hz, amp float64) func(i int) float64 {
	return func(i int) float64 { return amp * math.Sin(2*math.Pi*hz*float64(i)/rate) }
}

func TestSideShareAndCorrelation(t *testing.T) {
	const rate = 48000
	a := tone(rate, 440, 0.5)
	inverted := func(i int) float64 { return -a(i) }
	n := noise(rate, 2, 2, 0.5, 4)
	left := func(i int) float64 { return float64(n.Samples[2*i]) }
	right := func(i int) float64 { return float64(n.Samples[2*i+1]) }
	quiet := func(int) float64 { return 0 }
	for _, tc := range []struct {
		name        string
		sig         Signal
		share       float64
		shareTol    float64
		correlation float64
		corrTol     float64
	}{
		{"identical", stereo(rate, 2, a, a), 0, 1e-9, 1, 1e-9},
		{"inverted", stereo(rate, 2, a, inverted), 100, 1e-9, -1, 1e-9},
		{"uncorrelated noise", stereo(rate, 2, left, right), 50, 1, 0, 0.02},
		{"left only", stereo(rate, 2, a, quiet), 50, 1e-9, 0, 0},
		{"mono", sine(rate, 2, 1, 440, -6), 0, 0, 1, 0},
		{"half as loud on the right", stereo(rate, 2, a, func(i int) float64 { return a(i) / 2 }), 10, 1e-6, 1, 1e-9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			share, err := SideShare(tc.sig, nil)
			if err != nil {
				t.Fatal(err)
			}
			near(t, "SideShare", share, tc.share, tc.shareTol)
			corr, err := Correlation(tc.sig)
			if err != nil {
				t.Fatal(err)
			}
			near(t, "Correlation", corr, tc.correlation, tc.corrTol)
		})
	}
}

// A stereo signal whose low end is mono and whose top is wide: its side share
// reads 0 in the low band, 100 in the top band and half over the whole.
func TestSideShareInABand(t *testing.T) {
	const rate = 48000
	bass := tone(rate, 60, 0.4)
	top := tone(rate, 3000, 0.4)
	sig := stereo(rate, 2,
		func(i int) float64 { return bass(i) + top(i) },
		func(i int) float64 { return bass(i) - top(i) })
	for _, tc := range []struct {
		band []float64
		want float64
		tol  float64
	}{
		{[]float64{20, 150}, 0, 0.01},
		{[]float64{1000, 5000}, 100, 0.01},
		{nil, 50, 0.1},
		{[]float64{0, 24000}, 50, 0.1},
	} {
		got, err := SideShare(sig, tc.band)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "SideShare "+bandName(tc.band), got, tc.want, tc.tol)
	}

	// Mono reads 0 in a band too, but the band is still checked.
	mono := sine(rate, 1, 1, 60, -6)
	if got, err := SideShare(mono, []float64{20, 150}); err != nil || got != 0 {
		t.Errorf("SideShare of mono in [20, 150] = %g, %v; want 0", got, err)
	}
	if _, err := SideShare(mono, []float64{150, 20}); err == nil {
		t.Error("SideShare of mono in [150, 20]: no error")
	}
}

func bandName(band []float64) string {
	if band == nil {
		return "without a band"
	}
	return "in a band"
}

func TestStereoRefusesSilence(t *testing.T) {
	sig := silence(48000, 1, 2)
	if _, err := SideShare(sig, nil); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Errorf("SideShare of silence: error %v, want one saying it is silent", err)
	}
	if _, err := SideShare(sig, []float64{20, 150}); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Errorf("SideShare of silence in a band: error %v, want one saying it is silent", err)
	}
	if _, err := Correlation(sig); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Errorf("Correlation of silence: error %v, want one saying it is silent", err)
	}
}
