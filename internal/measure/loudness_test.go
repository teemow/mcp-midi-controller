package measure

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// At 48 kHz the K-weighting worked out from the analog filters is the filter
// ITU-R BS.1770-4 gives in its tables 1 and 2.
func TestKWeightingAt48kHz(t *testing.T) {
	k := kWeighting(48000)
	for _, tc := range []struct {
		name string
		got  biquad
		want [5]float64 // b0, b1, b2, a1, a2
	}{
		{"shelf", k.shelf, [5]float64{1.53512485958697, -2.69169618940638, 1.19839281085285, -1.69065929318241, 0.73248077421585}},
		{"high-pass", k.highpass, [5]float64{1, -2, 1, -1.99004745483398, 0.99007225036621}},
	} {
		got := [5]float64{tc.got.b0, tc.got.b1, tc.got.b2, tc.got.a1, tc.got.a2}
		for i := range got {
			if math.Abs(got[i]-tc.want[i]) > 1e-8 {
				t.Errorf("%s coefficient %d = %.14f, want %.14f", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// The references, 1 kHz sines throughout:
//   - ITU-R BS.1770-4, Annex 1: "If a 0 dB FS, 1 kHz sine wave is applied to
//     the left, centre or right channel input, the indicated loudness will
//     equal -3.01 LKFS." The standard's own calibration, so it comes first.
//   - EBU Tech 3341 test signals 1 and 2: a stereo sine at -23 and -33 dBFS
//     in each channel reads -23.0 and -33.0 LUFS (±0.1). Two equal channels
//     add 3.01 dB to the -3.01 of one, so a stereo sine reads its peak level;
//     -18 dBFS reads -18.0 the same way.
//   - EBU Tech 3341 test signals 3 to 5, which check the gates: the parts at
//     -36 dBFS fall under the relative gate, those at -72 under the absolute
//     one, and -26/-20/-26 averages in power, not in dB, to -23.0.
//
// The K-weighting is worked out for the sample rate, so the steady sines read
// the same at 44.1, 48 and 96 kHz.
func TestLoudnessReferences(t *testing.T) {
	leftOnly := func(rate float64) Signal {
		return gen(rate, 20, 2, func(i, c int) float64 {
			if c == 1 {
				return 0
			}
			return math.Sin(2 * math.Pi * 1000 * float64(i) / rate)
		})
	}
	for _, tc := range []struct {
		name  string
		rates []float64
		sig   func(rate float64) Signal
		want  float64
	}{
		{"BS.1770-4: 0 dBFS in one channel, mono", []float64{44100, 48000, 96000},
			func(rate float64) Signal { return sine(rate, 20, 1, 1000, 0) }, -3.01},
		{"BS.1770-4: 0 dBFS in the left channel", []float64{48000}, leftOnly, -3.01},
		{"Tech 3341 1: stereo -23 dBFS", []float64{44100, 48000, 96000},
			func(rate float64) Signal { return sine(rate, 20, 2, 1000, -23) }, -23},
		{"Tech 3341 2: stereo -33 dBFS", []float64{48000},
			func(rate float64) Signal { return sine(rate, 20, 2, 1000, -33) }, -33},
		{"stereo -18 dBFS", []float64{48000},
			func(rate float64) Signal { return sine(rate, 20, 2, 1000, -18) }, -18},
		{"Tech 3341 3: -36, -23, -36", []float64{48000},
			func(rate float64) Signal { return parts(rate, part{-36, 10}, part{-23, 60}, part{-36, 10}) }, -23},
		{"Tech 3341 4: -72, -36, -23, -36, -72", []float64{48000},
			func(rate float64) Signal {
				return parts(rate, part{-72, 10}, part{-36, 10}, part{-23, 60}, part{-36, 10}, part{-72, 10})
			}, -23},
		{"Tech 3341 5: -26, -20, -26", []float64{48000},
			func(rate float64) Signal { return parts(rate, part{-26, 20}, part{-20, 20.1}, part{-26, 20}) }, -23},
	} {
		for _, rate := range tc.rates {
			t.Run(fmt.Sprintf("%s at %g Hz", tc.name, rate), func(t *testing.T) {
				got, err := Loudness(tc.sig(rate))
				if err != nil {
					t.Fatal(err)
				}
				near(t, "Loudness", got, tc.want, 0.1)
			})
		}
	}
}

// EBU Tech 3342's test signals 1 to 4 (stereo 1 kHz sines; the expected
// range ±1 LU), and the shorter 10 s / 10 s step the craft tools see.
func TestLoudnessRangeReferences(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []part
		want  float64
	}{
		{"Tech 3342 1: -20, -30", []part{{-20, 20}, {-30, 20}}, 10},
		{"Tech 3342 2: -20, -15", []part{{-20, 20}, {-15, 20}}, 5},
		{"Tech 3342 3: -40, -20", []part{{-40, 20}, {-20, 20}}, 20},
		{"Tech 3342 4: -50, -35, -20, -35, -50", []part{{-50, 20}, {-35, 20}, {-20, 20}, {-35, 20}, {-50, 20}}, 15},
		{"10 s, then 10 s 10 dB lower", []part{{-18, 10}, {-28, 10}}, 10},
		{"steady", []part{{-18, 10}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoudnessRange(parts(48000, tc.parts...))
			if err != nil {
				t.Fatal(err)
			}
			near(t, "LoudnessRange", got, tc.want, 1)
		})
	}
}

// A sine's true peak lies at its peak level and its loudness, in stereo, at
// the same level: the peak-to-loudness ratio is 0. In one channel the
// loudness is 3.01 dB lower.
func TestPeakToLoudness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
		want     float64
	}{
		{"stereo", 2, 0},
		{"mono", 1, 3.01},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PeakToLoudness(sine(48000, 5, tc.channels, 1000, -10))
			if err != nil {
				t.Fatal(err)
			}
			near(t, "PeakToLoudness", got, tc.want, 0.1)
		})
	}
}

// What the loudness readings refuse: silence (every block gated away), a
// stretch shorter than one block, a sample rate too low to K-weight.
func TestLoudnessRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  Signal
		want string // in the error of each
		read []string
	}{
		{"silence", silence(48000, 5, 2), "silent", []string{"loudness", "range", "plr"}},
		{"under -70 LUFS", sine(48000, 5, 2, 1000, -75), "silent", []string{"loudness", "range", "plr"}},
		{"50 ms", sine(48000, 0.05, 2, 1000, -20), "at least 400 ms", []string{"loudness", "plr"}},
		{"399 ms", sine(48000, 0.399, 2, 1000, -20), "at least 400 ms", []string{"loudness", "plr"}},
		{"2.9 s", sine(48000, 2.9, 2, 1000, -20), "at least 3000 ms", []string{"range"}},
		{"4 kHz", sine(4000, 5, 2, 100, -20), "too low", []string{"loudness", "range", "plr"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range tc.read {
				var err error
				switch r {
				case "loudness":
					_, err = Loudness(tc.sig)
				case "range":
					_, err = LoudnessRange(tc.sig)
				case "plr":
					_, err = PeakToLoudness(tc.sig)
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s: error %v, want one saying %q", r, err, tc.want)
				}
			}
		})
	}
}

// One 400 ms block is enough for the integrated loudness, one 3 s block for
// the range.
func TestLoudnessShortestStretch(t *testing.T) {
	if got, err := Loudness(sine(48000, 0.4, 2, 1000, -23)); err != nil {
		t.Error(err)
	} else {
		near(t, "Loudness of 400 ms", got, -23, 0.1)
	}
	if got, err := LoudnessRange(sine(48000, 3, 2, 1000, -23)); err != nil {
		t.Error(err)
	} else {
		near(t, "LoudnessRange of 3 s", got, 0, 0.01)
	}
}
