package measure

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// The band level of a sine is its peak level when the band holds it, and
// lies far under it when the band does not.
func TestBandLevelOfASine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seconds  float64
		from, to float64
		want     float64
		tol      float64
	}{
		{"500..2000 Hz", 2, 500, 2000, -20, 0.05},
		{"990..1010 Hz", 2, 990, 1010, -20, 0.05},
		{"0 Hz to Nyquist", 2, 0, 24000, -20, 0.05},
		{"one FFT window", 4096.0 / 48000, 500, 2000, -20, 0.05},
		{"between two FFT sizes", 0.3, 500, 2000, -20, 0.05},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, channels := range []int{1, 2} {
				got, err := BandLevel(sine(48000, tc.seconds, channels, 1000, -20), tc.from, tc.to)
				if err != nil {
					t.Fatal(err)
				}
				near(t, fmt.Sprintf("BandLevel, %d channels", channels), got, tc.want, tc.tol)
			}
		})
	}

	sig := sine(48000, 2, 2, 1000, -20)
	in, err := BandLevel(sig, 500, 2000)
	if err != nil {
		t.Fatal(err)
	}
	for _, band := range [][2]float64{{20, 200}, {5000, 20000}} {
		out, err := BandLevel(sig, band[0], band[1])
		if err != nil {
			t.Fatal(err)
		}
		if in-out < 60 {
			t.Errorf("BandLevel %v = %.1f dB, only %.1f dB under the sine's band", band, out, in-out)
		}
	}
}

// The bands of white noise add up: all of it reads its mean square against a
// full-scale sine's (1/2), each half of the spectrum 3 dB less.
func TestBandLevelOfNoise(t *testing.T) {
	const amp = 0.5
	sig := noise(48000, 4, 2, amp, 1)
	ms := amp * amp / 3 // of uniform noise
	whole := 10 * math.Log10(2*ms)
	for _, tc := range []struct {
		from, to float64
		want     float64
	}{
		{0, 24000, whole},
		{0, 12000, whole - 3.01},
		{12000, 24000, whole - 3.01},
		{1000, 3400, whole - 10*math.Log10(24000.0/2400)},
	} {
		got, err := BandLevel(sig, tc.from, tc.to)
		if err != nil {
			t.Fatal(err)
		}
		near(t, fmt.Sprintf("BandLevel [%g, %g]", tc.from, tc.to), got, tc.want, 0.1)
	}
}

// A band with nothing in it reads the floor.
func TestBandLevelOfSilence(t *testing.T) {
	got, err := BandLevel(silence(48000, 1, 2), 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got != Floor {
		t.Errorf("BandLevel of silence = %g, want %g", got, Floor)
	}
}

// Brightness: a sine reads its frequency, white noise a quarter of the sample
// rate, and a sine with a strong harmonic lies between the two.
func TestBrightness(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  Signal
		want float64
		tol  float64
	}{
		{"440 Hz sine", sine(48000, 2, 2, 440, -12), 440, 1},
		{"440 Hz sine, mono, 44.1 kHz", sine(44100, 2, 1, 440, -12), 440, 1},
		{"5 kHz sine", sine(48000, 2, 2, 5000, -12), 5000, 1},
		{"white noise", noise(48000, 4, 2, 0.5, 2), 12000, 120},
		{"white noise, 96 kHz", noise(96000, 4, 1, 0.5, 3), 24000, 240},
		// 1 kHz with its third harmonic 6 dB lower: power weights 1 and 1/4.
		{"1 kHz and 3 kHz", gen(48000, 2, 2, func(i, _ int) float64 {
			x := 2 * math.Pi * float64(i) / 48000
			return 0.4*math.Sin(1000*x) + 0.2*math.Sin(3000*x)
		}), (1000 + 3000*0.25) / 1.25, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Brightness(tc.sig)
			if err != nil {
				t.Fatal(err)
			}
			near(t, "Brightness", got, tc.want, tc.tol)
		})
	}
}

// The spectrum readings refuse a stretch shorter than one FFT window, and
// the brightness of silence.
func TestSpectrumRefuses(t *testing.T) {
	short := sine(48000, 4095.0/48000, 2, 1000, -20)
	for name, read := range map[string]func() error{
		"BandLevel":  func() error { _, err := BandLevel(short, 500, 2000); return err },
		"Brightness": func() error { _, err := Brightness(short); return err },
		"SideShare":  func() error { _, err := SideShare(short, []float64{500, 2000}); return err },
	} {
		if err := read(); err == nil || !strings.Contains(err.Error(), "at least 4096 frames") {
			t.Errorf("%s of 4095 frames: error %v, want one asking for 4096 frames", name, err)
		}
	}
	if _, err := Brightness(silence(48000, 1, 2)); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Errorf("Brightness of silence: error %v, want one saying it is silent", err)
	}
	if _, err := BandLevel(sine(48000, 1, 2, 1000, -20), 1000, 1001); err == nil || !strings.Contains(err.Error(), "narrower than one bin") {
		t.Errorf("BandLevel of a 1 Hz band: error %v, want one saying it is narrower than one bin", err)
	}
}

// Welch's windows cover the stretch: a tone only in its last 100 ms counts.
func TestWelchCoversTheStretch(t *testing.T) {
	const rate = 48000
	sig := gen(rate, 1, 1, func(i, _ int) float64 {
		if i < rate*0.9 {
			return 0
		}
		return 0.5 * math.Sin(2*math.Pi*1000*float64(i)/rate)
	})
	got, err := BandLevel(sig, 500, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if got < -40 {
		t.Errorf("BandLevel = %.1f dB: the tone at the end of the stretch was missed", got)
	}
}
