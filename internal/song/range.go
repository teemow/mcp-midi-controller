package song

// This file is the mapping range (ADR 0003): the part of a parameter's range
// that AUM's mapping of it spans. A song's parameter value reaches its plugin
// as a 7-bit CC, and without a range AUM spreads the CC's 128 steps over the
// parameter's whole range: a frequency of 10 Hz to 30 kHz moves in steps of
// about 235 Hz, and an 80 Hz high-pass cannot be set. With a range of 40 to
// 250 Hz the steps are about 1.7 Hz apart.
//
// The range is the session's, fixed per parameter where the plugin is
// declared, and never derived from the values the song sets: a changed value
// still plays live, and only a changed range needs the session loaded again
// (Fingerprint covers it). A song without a range compiles to the session and
// the MIDI it did before ranges existed.

import (
	"fmt"
	"math"

	"github.com/teemow/midi-device/device"
)

// checkRanges checks a plugin's mapping ranges as written, before any plugin
// is looked at: two numbers each, the lowest below the highest.
func checkRanges(e *errs, at string, ranges map[string][]float64) {
	for _, name := range sortedKeys(ranges) {
		r := ranges[name]
		where := at + "/" + name
		switch {
		case len(r) != 2:
			e.addf("%s: a mapping range is two numbers, the lowest and the highest value the parameter's CC reaches, not %d", where, len(r))
		case math.IsNaN(r[0]) || math.IsNaN(r[1]) || math.IsInf(r[0], 0) || math.IsInf(r[1], 0):
			e.addf("%s: %v is not a range of numbers", where, r)
		case r[0] >= r[1]:
			e.addf("%s: %g..%g: the lowest value comes first and lies below the highest", where, r[0], r[1])
		}
	}
}

// resolveRanges finds the parameters a plugin's mapping ranges name and keeps
// the ranges by parameter key, for the node's ParamPlans to take. A range
// names a parameter the plugin's own params set, and lies within the
// parameter's range. at is the plugin's place in the document.
func (n *NodePlan) resolveRanges(e *errs, at string, pl Plugin) {
	if len(pl.Range) == 0 {
		return
	}
	// The keys of the parameters the declaration sets.
	declared := map[string]bool{}
	for name := range pl.Params {
		if _, key, err := findParam(n.dump, name); err == nil {
			declared[key] = true
		}
	}
	named := map[string]string{} // key -> the name the range was given under
	for _, name := range sortedKeys(pl.Range) {
		r := pl.Range[name]
		where := at + "/range/" + name
		i, key, err := findParam(n.dump, name)
		if err != nil {
			e.addf("%s: %s %v", where, n.Name, err)
			continue
		}
		if len(r) != 2 || r[0] >= r[1] {
			continue // reported by checkRanges
		}
		p := n.dump.Parameters[i]
		label := device.FirstNonEmpty(p.DisplayName, p.Identifier, key)
		switch {
		case named[key] != "":
			e.addf("%s: %q and %q are both %s; give it one range", where, named[key], name, label)
			continue
		case r[0] < p.Min || r[1] > p.Max:
			e.addf("%s: %g..%g lies outside %s's range %g..%g", where, r[0], r[1], label, p.Min, p.Max)
			continue
		case !declared[key]:
			e.addf("%s: the plugin's params do not set %s; a mapping range narrows the mapping of a parameter the song sets, so set it there too", where, label)
			continue
		}
		named[key] = name
		if n.ranges == nil {
			n.ranges = map[string][]float64{}
		}
		n.ranges[key] = []float64{r[0], r[1]}
	}
}

// wire converts a value written in a song into the 0..127 a CC carries for the
// parameter through its mapping: over the parameter's whole range (paramWire),
// or over its mapping range.
func (pp *ParamPlan) wire(v Value) (int, error) {
	if len(pp.Range) != 2 {
		return paramWire(pp.param, v)
	}
	return rangeWire(pp.param, pp.Range[0], pp.Range[1], v)
}

// rangeWire converts a value into the CC that sets the parameter to it through
// a mapping that spans lo..hi of it. The value means what it means without a
// range ("NN%" is a share of the parameter's whole range, a label is its value
// there); it has to lie within the mapping range.
func rangeWire(p device.ProbeParam, lo, hi float64, v Value) (int, error) {
	pos, err := paramPos(p, v)
	if err != nil {
		return 0, err
	}
	x := v.Num
	if v.IsStr {
		x = p.Min + pos*(p.Max-p.Min)
	}
	// A value within a hair of an end, as a share of the range comes out of
	// floating point, is that end.
	const eps = 1e-9
	tol := eps * math.Max(1, hi-lo)
	switch {
	case x < lo-tol || x > hi+tol:
		name := device.FirstNonEmpty(p.DisplayName, p.Identifier)
		if v.IsStr {
			return 0, fmt.Errorf("%s at %s is %.6g, outside its mapping range %g..%g; set a value within it, or widen the range (a new session)", name, v.Str, x, lo, hi)
		}
		return 0, fmt.Errorf("%g is outside %s's mapping range %g..%g; set a value within it, or widen the range (a new session)", x, name, lo, hi)
	case x < lo:
		x = lo
	case x > hi:
		x = hi
	}
	return clamp7(int(math.Round(along(lo, hi, x) * 127))), nil
}

// mapRange is the mapping range as AUM stores it on the mapping: the two
// ends' positions in the parameter's range, 0..1 (AUM's 0 % and 100 %).
func (pp *ParamPlan) mapRange() (lo, hi float64) {
	return aumPosition(pp.param, pp.Range[0]), aumPosition(pp.param, pp.Range[1])
}

// AUM's mapping law. aumPosition is where AUM puts a value of a parameter on
// the 0..1 its mapping's range is stored on, and along is where a value lies
// between a mapping's two ends as AUM spreads a CC over them: CC 0 at lo,
// 127 at hi.
//
// Both are linear in the parameter's value, as AUM spreads a CC over a
// parameter's whole range (docs/songs.md, "Parameters": a CC of 64 puts a
// reverb's mix at 50 %). Whether a narrowed mapping does the same for a
// parameter its plugin shows on a log scale (a frequency), or follows the
// plugin's taper, is ADR 0003's measurement (docs/songs.md, "Mapping
// ranges"); should AUM follow the taper, these two are what change.

func aumPosition(p device.ProbeParam, x float64) float64 {
	span := p.Max - p.Min
	if span <= 0 {
		return 0
	}
	return (x - p.Min) / span
}

func along(lo, hi, x float64) float64 {
	return (x - lo) / (hi - lo)
}
