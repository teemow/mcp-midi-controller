package measure

import (
	"errors"
	"fmt"
	"math"
)

// SideShare is the share of the side signal (S = (L-R)/2) in the energy of
// mid and side together (M = (L+R)/2), in percent: 0 for a signal that is the
// same in both channels, 100 for one channel the other inverted, about 50 for
// two channels that have nothing in common. With a band [from, to] in Hz it
// reads the energy inside the band only (the spectrum as BandLevel reads it).
// A mono stretch reads 0. A silent stretch, or a band with nothing in it, is
// an error.
func SideShare(sig Signal, band []float64) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	if len(band) == 0 {
		return sideShare(v)
	}
	from, to, err := checkBand(band, v.rate)
	if err != nil {
		return 0, err
	}
	return sideShareBand(v, from, to)
}

func sideShare(v view) (float64, error) {
	if v.chans == 1 {
		return 0, nil
	}
	var mid, side float64
	for i := range v.frames {
		l, r := v.at(i, 0), v.at(i, 1)
		mid += (l + r) * (l + r) / 4
		side += (l - r) * (l - r) / 4
	}
	if (mid+side)/float64(v.frames) <= silentPower {
		return 0, errors.New("the stretch is silent: it has no side signal to share")
	}
	return 100 * side / (mid + side), nil
}

func sideShareBand(v view, from, to float64) (float64, error) {
	if v.chans == 1 {
		// Still refuse what the stereo reading would refuse.
		sp, err := welch(v, v.mono)
		if err != nil {
			return 0, err
		}
		if _, err := sp.band(from, to); err != nil {
			return 0, err
		}
		return 0, nil
	}
	midSp, err := welch(v, func(i int) float64 { return (v.at(i, 0) + v.at(i, 1)) / 2 })
	if err != nil {
		return 0, err
	}
	sideSp, err := welch(v, func(i int) float64 { return (v.at(i, 0) - v.at(i, 1)) / 2 })
	if err != nil {
		return 0, err
	}
	mid, err := midSp.band(from, to)
	if err != nil {
		return 0, err
	}
	side, err := sideSp.band(from, to)
	if err != nil {
		return 0, err
	}
	if mid+side <= silentPower {
		return 0, fmt.Errorf("the band [%g, %g] Hz is silent: it has no side signal to share", from, to)
	}
	return 100 * side / (mid + side), nil
}

// Correlation is the correlation of the left and the right channel, from -1
// to 1: 1 for the same signal in both, -1 for one the other inverted, about 0
// for two that have nothing in common, and 0 when only one channel sounds. A
// mono stretch reads 1. A silent stretch is an error.
func Correlation(sig Signal) (float64, error) {
	v, err := newView(sig)
	if err != nil {
		return 0, err
	}
	return correlation(v)
}

func correlation(v view) (float64, error) {
	if v.chans == 1 {
		return 1, nil
	}
	var lr, ll, rr float64
	for i := range v.frames {
		l, r := v.at(i, 0), v.at(i, 1)
		lr += l * r
		ll += l * l
		rr += r * r
	}
	n := float64(v.frames)
	switch {
	case (ll+rr)/n <= silentPower:
		return 0, errors.New("the stretch is silent: it has no correlation")
	case ll/n <= silentPower || rr/n <= silentPower:
		return 0, nil
	}
	return math.Max(-1, math.Min(1, lr/math.Sqrt(ll*rr))), nil
}
