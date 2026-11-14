// Package measure computes, from a stretch of PCM recorded at an audio tap,
// the measurements the craft tools read: the level of a band of frequencies,
// the rms level, the sample and the true peak, the integrated loudness and the
// loudness range (ITU-R BS.1770-4, EBU Tech 3342), the true peak above the
// loudness, the crest factor, the brightness, the share of the side signal and
// the stereo correlation.
//
// A craft tool measures before and after a change, or while it sweeps a
// parameter, and compares the readings; Measure takes the measurement by its
// name so the tool can pass on what a card says.
//
// Every reading is a finite number. A level of silence reads the floor, -120
// dB; what cannot be read at all (a stretch too short, a loudness every block
// of which is gated away, the brightness of silence) is an error.
package measure

import (
	"errors"
	"fmt"
	"math"
)

// Floor is the lowest level a reading in dB goes to: what silence reads.
const Floor = -120.0

// silentPower is the mean square at the floor: an rms of 1e-6, -120 dBFS.
const silentPower = 1e-12

// Signal is a stretch of interleaved PCM as a tap delivers it.
type Signal struct {
	Samples    []float32 // interleaved
	Channels   int       // 1 or 2 (more: use the first two)
	SampleRate float64
}

// The measurements, spelled as craft.Measurement spells them.
const (
	nameBandLevel      = "band_level"
	nameLevel          = "level"
	nameTruePeak       = "true_peak"
	nameLoudness       = "loudness"
	nameLoudnessRange  = "loudness_range"
	namePeakToLoudness = "peak_to_loudness"
	nameCrestFactor    = "crest_factor"
	nameBrightness     = "brightness"
	nameSideShare      = "side_share"
	nameCorrelation    = "correlation"
)

type bandUse int

const (
	bandNone bandUse = iota
	bandOptional
	bandRequired
)

// bandUses says, for every measurement, whether it reads a band.
var bandUses = map[string]bandUse{
	nameBandLevel:      bandRequired,
	nameLevel:          bandNone,
	nameTruePeak:       bandNone,
	nameLoudness:       bandNone,
	nameLoudnessRange:  bandNone,
	namePeakToLoudness: bandNone,
	nameCrestFactor:    bandNone,
	nameBrightness:     bandNone,
	nameSideShare:      bandOptional,
	nameCorrelation:    bandNone,
}

// Measure computes one measurement by its name (as craft.Measurement spells
// it). band is nil or [from, to] in Hz; band_level requires one, side_share
// takes one optionally, the others refuse one. The error says what is wrong
// (unknown name, a band where none is read, a stretch too short or silent to
// measure).
func Measure(sig Signal, name string, band []float64) (float64, error) {
	use, ok := bandUses[name]
	if !ok {
		return 0, fmt.Errorf("unknown measurement %q", name)
	}
	switch {
	case len(band) > 0 && use == bandNone:
		return 0, fmt.Errorf("%s reads no band; it was given %v", name, band)
	case len(band) == 0 && use == bandRequired:
		return 0, fmt.Errorf("%s needs a band [from, to] in Hz", name)
	}
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	var from, to float64
	if len(band) > 0 {
		if from, to, err = checkBand(band, v.rate); err != nil {
			return 0, err
		}
	}
	switch name {
	case nameBandLevel:
		return bandLevel(v, from, to)
	case nameLevel:
		return level(v), nil
	case nameTruePeak:
		return truePeak(v), nil
	case nameLoudness:
		return loudness(v)
	case nameLoudnessRange:
		return loudnessRange(v)
	case namePeakToLoudness:
		return peakToLoudness(v)
	case nameCrestFactor:
		return crestFactor(v), nil
	case nameBrightness:
		return brightness(v)
	case nameSideShare:
		if len(band) > 0 {
			return sideShareBand(v, from, to)
		}
		return sideShare(v)
	case nameCorrelation:
		return correlation(v)
	}
	return 0, fmt.Errorf("unknown measurement %q", name)
}

// view is a checked Signal: the first one or two channels of whole frames.
type view struct {
	s      []float32
	stride int // the channels in s
	chans  int // the channels read: 1 or 2
	frames int
	rate   float64
}

// at returns frame i of channel c.
func (v view) at(i, c int) float64 { return float64(v.s[i*v.stride+c]) }

// mono returns frame i of the mono mix: the mean of the channels read.
func (v view) mono(i int) float64 {
	if v.chans == 1 {
		return v.at(i, 0)
	}
	return (v.at(i, 0) + v.at(i, 1)) / 2
}

// duration says how long n frames last, for an error.
func (v view) duration(n int) string {
	return fmt.Sprintf("%.0f ms", float64(n)/v.rate*1000)
}

// newView checks a signal: at least one channel, a sample rate, at least one
// whole frame and nothing but finite samples. A trailing partial frame is
// left out.
func newView(sig Signal) (view, error) {
	switch {
	case sig.Channels < 1:
		return view{}, fmt.Errorf("the stretch has %d channels; it needs at least one", sig.Channels)
	case !(sig.SampleRate > 0) || math.IsInf(sig.SampleRate, 0):
		return view{}, fmt.Errorf("the sample rate %g Hz is not a sample rate", sig.SampleRate)
	}
	frames := len(sig.Samples) / sig.Channels
	if frames == 0 {
		return view{}, errors.New("the stretch is empty")
	}
	s := sig.Samples[:frames*sig.Channels]
	for _, x := range s {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return view{}, errors.New("the stretch holds a sample that is not a finite number")
		}
	}
	return view{s: s, stride: sig.Channels, chans: min(sig.Channels, 2), frames: frames, rate: sig.SampleRate}, nil
}

// checkBand checks a band [from, to] in Hz: two finite numbers, from at or
// above 0 Hz and below to, to at most the Nyquist frequency.
func checkBand(band []float64, rate float64) (from, to float64, err error) {
	if len(band) != 2 {
		return 0, 0, fmt.Errorf("a band is [from, to] in Hz, not %d numbers", len(band))
	}
	from, to = band[0], band[1]
	nyquist := rate / 2
	switch {
	case math.IsNaN(from) || math.IsNaN(to) || math.IsInf(from, 0) || math.IsInf(to, 0):
		return 0, 0, fmt.Errorf("the band %v holds a number that is not finite", band)
	case from < 0:
		return 0, 0, fmt.Errorf("the band [%g, %g] Hz starts below 0 Hz", from, to)
	case from > to:
		return 0, 0, fmt.Errorf("the band [%g, %g] Hz is upside down: from has to lie below to", from, to)
	case from == to:
		return 0, 0, fmt.Errorf("the band [%g, %g] Hz is empty", from, to)
	case to > nyquist:
		return 0, 0, fmt.Errorf("the band [%g, %g] Hz reaches above %g Hz, the Nyquist frequency at %g Hz", from, to, nyquist, rate)
	}
	return from, to, nil
}

// ampDB converts a linear amplitude to dB with the floor.
func ampDB(amp float64) float64 {
	if amp <= 1e-6 {
		return Floor
	}
	return 20 * math.Log10(amp)
}

// powerDB converts a mean square to dB with the floor.
func powerDB(ms float64) float64 {
	if ms <= silentPower {
		return Floor
	}
	return 10 * math.Log10(ms)
}
