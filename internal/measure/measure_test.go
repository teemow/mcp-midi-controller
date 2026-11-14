package measure

import (
	"math"
	"strings"
	"testing"
)

// Measure reads by name what the functions read.
func TestMeasureDispatches(t *testing.T) {
	sig := gen(48000, 4, 2, func(i, c int) float64 {
		x := 2 * math.Pi * float64(i) / 48000
		return 0.3*math.Sin(220*x) + 0.1*math.Sin(3000*x+float64(c))
	})
	direct := func(v float64, err error) float64 {
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name string
		band []float64
		want float64
	}{
		{nameBandLevel, []float64{100, 400}, direct(BandLevel(sig, 100, 400))},
		{nameLevel, nil, Level(sig)},
		{nameTruePeak, nil, TruePeak(sig)},
		{nameLoudness, nil, direct(Loudness(sig))},
		{nameLoudnessRange, nil, direct(LoudnessRange(sig))},
		{namePeakToLoudness, nil, direct(PeakToLoudness(sig))},
		{nameCrestFactor, nil, CrestFactor(sig)},
		{nameBrightness, nil, direct(Brightness(sig))},
		{nameSideShare, nil, direct(SideShare(sig, nil))},
		{nameSideShare, []float64{1000, 5000}, direct(SideShare(sig, []float64{1000, 5000}))},
		{nameCorrelation, nil, direct(Correlation(sig))},
	} {
		got, err := Measure(sig, tc.name, tc.band)
		if err != nil {
			t.Errorf("%s %v: %v", tc.name, tc.band, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %v = %g, want %g", tc.name, tc.band, got, tc.want)
		}
	}
	if len(bandUses) != 10 {
		t.Errorf("Measure knows %d measurements, the test reads 10", len(bandUses))
	}
}

func TestMeasureRefuses(t *testing.T) {
	sig := sine(48000, 1, 2, 1000, -20)
	for _, tc := range []struct {
		why  string
		sig  Signal
		name string
		band []float64
		want string
	}{
		{"unknown name", sig, "loudness_max", nil, `unknown measurement "loudness_max"`},
		{"no name", sig, "", nil, "unknown measurement"},
		{"a band on level", sig, nameLevel, []float64{20, 200}, "level reads no band"},
		{"a band on loudness", sig, nameLoudness, []float64{20, 200}, "loudness reads no band"},
		{"a band on correlation", sig, nameCorrelation, []float64{20, 200}, "correlation reads no band"},
		{"no band on band_level", sig, nameBandLevel, nil, "band_level needs a band"},
		{"an empty band on band_level", sig, nameBandLevel, []float64{}, "band_level needs a band"},
		{"an upside-down band", sig, nameBandLevel, []float64{2000, 500}, "upside down"},
		{"an empty band", sig, nameBandLevel, []float64{500, 500}, "is empty"},
		{"a band above Nyquist", sig, nameBandLevel, []float64{20000, 30000}, "above 24000 Hz, the Nyquist frequency"},
		{"a band wholly above Nyquist", sig, nameSideShare, []float64{25000, 30000}, "the Nyquist frequency"},
		{"a band below 0 Hz", sig, nameBandLevel, []float64{-10, 200}, "below 0 Hz"},
		{"a band of three", sig, nameBandLevel, []float64{20, 200, 2000}, "not 3 numbers"},
		{"a band of one", sig, nameSideShare, []float64{20}, "not 1 numbers"},
		{"a band of NaN", sig, nameBandLevel, []float64{math.NaN(), 200}, "not finite"},
		{"an infinite band", sig, nameBandLevel, []float64{20, math.Inf(1)}, "not finite"},
		{"no channels", Signal{Samples: sig.Samples, SampleRate: 48000}, nameLevel, nil, "0 channels"},
		{"no sample rate", Signal{Samples: sig.Samples, Channels: 2}, nameLevel, nil, "not a sample rate"},
		{"a NaN sample rate", Signal{Samples: sig.Samples, Channels: 2, SampleRate: math.NaN()}, nameLevel, nil, "not a sample rate"},
		{"an empty stretch", Signal{Channels: 2, SampleRate: 48000}, nameLevel, nil, "empty"},
		{"half a frame", Signal{Samples: []float32{0.5}, Channels: 2, SampleRate: 48000}, nameTruePeak, nil, "empty"},
		{"an infinite sample", Signal{Samples: []float32{0.5, float32(math.Inf(1))}, Channels: 2, SampleRate: 48000}, nameLevel, nil, "not a finite number"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			got, err := Measure(tc.sig, tc.name, tc.band)
			if err == nil {
				t.Fatalf("Measure = %g, want an error saying %q", got, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want one saying %q", err, tc.want)
			}
		})
	}
}

// A stretch too short for a reading is an error; the readings that need no
// window read a stretch of any length.
func TestMeasureTooShort(t *testing.T) {
	short := sine(48000, 0.05, 2, 1000, -20)
	for _, tc := range []struct {
		name string
		band []float64
		ok   bool
	}{
		{nameBandLevel, []float64{500, 2000}, false},
		{nameLevel, nil, true},
		{nameTruePeak, nil, true},
		{nameLoudness, nil, false},
		{nameLoudnessRange, nil, false},
		{namePeakToLoudness, nil, false},
		{nameCrestFactor, nil, true},
		{nameBrightness, nil, false},
		{nameSideShare, nil, true},
		{nameSideShare, []float64{20, 150}, false},
		{nameCorrelation, nil, true},
	} {
		got, err := Measure(short, tc.name, tc.band)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s %v of 50 ms: %v", tc.name, tc.band, err)
		case !tc.ok && err == nil:
			t.Errorf("%s %v of 50 ms = %g, want an error", tc.name, tc.band, got)
		case !tc.ok && !strings.Contains(err.Error(), "the stretch is 50 ms"):
			t.Errorf("%s %v of 50 ms: error %q does not say how long the stretch is", tc.name, tc.band, err)
		}
	}
}

// Every reading of every stretch is a finite number or an error: silence,
// one frame, a constant, full-scale square waves, the quietest float32
// numbers, the loudest.
func TestMeasureIsFinite(t *testing.T) {
	const rate = 48000
	sigs := map[string]Signal{
		"silence":     silence(rate, 4, 2),
		"one frame":   {Samples: []float32{0.5, -0.5}, Channels: 2, SampleRate: rate},
		"one sample":  {Samples: []float32{1}, Channels: 1, SampleRate: rate},
		"dc":          gen(rate, 4, 2, func(int, int) float64 { return 0.5 }),
		"square":      gen(rate, 4, 2, func(i, _ int) float64 { return float64(1 - 2*(i/24%2)) }),
		"denormal":    gen(rate, 4, 2, func(i, _ int) float64 { return 1e-40 * float64(i%2) }),
		"huge":        gen(rate, 4, 2, func(i, c int) float64 { return 1e30 * math.Sin(float64(i+c)) }),
		"three chans": gen(rate, 4, 3, func(i, c int) float64 { return 0.1 * float64(c) * math.Sin(float64(i)) }),
	}
	for sigName, sig := range sigs {
		for name, use := range bandUses {
			var band []float64
			if use == bandRequired {
				band = []float64{20, 2000}
			}
			got, err := Measure(sig, name, band)
			if err == nil && (math.IsNaN(got) || math.IsInf(got, 0)) {
				t.Errorf("%s of %s = %g", name, sigName, got)
			}
		}
	}
}

// Of more than two channels the first two are read.
func TestMeasureReadsTheFirstTwoChannels(t *testing.T) {
	two := sine(48000, 1, 2, 1000, -20)
	three := gen(48000, 1, 3, func(i, c int) float64 {
		if c == 2 {
			return 1 // a third channel at full scale, not read
		}
		return float64(two.Samples[2*i+c])
	})
	for name, use := range bandUses {
		if name == nameLoudnessRange {
			continue // needs 3 s
		}
		var band []float64
		if use == bandRequired {
			band = []float64{500, 2000}
		}
		want, err := Measure(two, name, band)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Measure(three, name, band)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s of three channels = %g, of the first two %g", name, got, want)
		}
	}
}
