package song

// This file is the mapping range (ADR 0003): the part of a parameter's range
// that AUM's mapping of it spans. A song's parameter value reaches its plugin
// as a 7-bit CC, and without a range AUM spreads the CC's 128 steps over the
// parameter's whole range: a frequency of 10 Hz to 30 kHz moves in steps of
// about 236 Hz, and an 80 Hz high-pass cannot be set. With a range of 40 to
// 250 Hz the steps are about 1.7 Hz apart.
//
// AUM spreads the steps along the parameter's travel, which follows the scale
// its plugin shows it on: linear for most, a log scale for a frequency an
// Apple filter shows on one (see Travel).
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
// range ("NN%" is a share of the parameter's whole travel, a label is its value
// there); it has to lie within the mapping range.
func rangeWire(p device.ProbeParam, lo, hi float64, v Value) (int, error) {
	pos, err := paramPos(p, v)
	if err != nil {
		return 0, err
	}
	a, b := Travel(p, lo), Travel(p, hi)
	// A value within a hair of an end, as a share of the travel comes out of
	// floating point, is that end.
	const tol = 1e-9
	switch {
	case pos < a-tol || pos > b+tol:
		name := device.FirstNonEmpty(p.DisplayName, p.Identifier)
		if v.IsStr {
			return 0, fmt.Errorf("%s at %s is %.6g, outside its mapping range %g..%g; set a value within it, or widen the range (a new session)", name, v.Str, ValueAt(p, pos), lo, hi)
		}
		return 0, fmt.Errorf("%g is outside %s's mapping range %g..%g; set a value within it, or widen the range (a new session)", v.Num, name, lo, hi)
	case pos < a:
		pos = a
	case pos > b:
		pos = b
	}
	return clamp7(int(math.Round((pos - a) / (b - a) * 127))), nil
}

// mapRange is the mapping range as AUM stores it on the mapping: the two
// ends' places on the parameter's travel, 0..1 (AUM's 0 % and 100 %).
func (pp *ParamPlan) mapRange() (lo, hi float64) {
	return Travel(pp.param, pp.Range[0]), Travel(pp.param, pp.Range[1])
}

// AUM's mapping law. A mapping moves a parameter along its travel, 0 at its
// lowest value and 1 at its highest, and spreads a CC's 128 steps evenly over
// the travel between the mapping's two ends: CC 0 at one, 127 at the other.
// AUM stores those ends as places on the travel (0 % and 100 % in its
// mapping's range).
//
// The travel follows the scale the plugin shows the parameter on. Most
// parameters are shown linearly, and their travel is linear in their value (a
// CC of 64 puts a reverb's mix at 50 %). A parameter its plugin shows on a log
// scale (the AU flag DisplayLogarithmic, as Apple's filters show a frequency)
// travels along that scale: measured on AUHighPassFilter, whose Cutoff
// Frequency runs from 10 Hz to 22050 Hz, a CC of 76 over the whole range set
// about 1 kHz (10·2205^(76/127)) and not the 13.2 kHz a linear travel gives,
// and a mapping range of 40..250 Hz stored as linear places came out at about
// 10 Hz (docs/songs.md, Status, "Mapping ranges").
//
// Other display scales (square root, squared, cubed, cube root, exponential)
// are not measured; such a parameter is taken to travel linearly.

// flagDisplayLogarithmic is kAudioUnitParameterFlag_DisplayLogarithmic.
const flagDisplayLogarithmic = 1 << 22

// LogTravel reports whether AUM moves a parameter along a log scale: its
// plugin shows it on one, and its range lies above 0, as a log scale needs.
func LogTravel(p device.ProbeParam) bool {
	return (p.DisplayLogarithmic || p.Flags&flagDisplayLogarithmic != 0) && p.Min > 0 && p.Max > p.Min
}

// Travel is where a value of a parameter lies on its travel, 0 at its lowest
// value and 1 at its highest, as AUM's mapping moves it.
func Travel(p device.ProbeParam, x float64) float64 {
	if p.Max <= p.Min {
		return 0
	}
	if LogTravel(p) {
		if x <= p.Min {
			return 0
		}
		return math.Log(x/p.Min) / math.Log(p.Max/p.Min)
	}
	return (x - p.Min) / (p.Max - p.Min)
}

// ValueAt is the parameter's value at a place on its travel, Travel's inverse.
func ValueAt(p device.ProbeParam, t float64) float64 {
	if LogTravel(p) {
		return p.Min * math.Pow(p.Max/p.Min, t)
	}
	return p.Min + t*(p.Max-p.Min)
}

// CCStep is how far apart, in the parameter's units, the values of two
// neighbouring CCs lie near x, over a mapping that spans lo..hi of the
// parameter (its whole range when lo >= hi): the finest a CC can set it there.
// Linear travel has one step everywhere; a log travel's step grows with x.
func CCStep(p device.ProbeParam, lo, hi, x float64) float64 {
	if lo >= hi {
		lo, hi = p.Min, p.Max
	}
	if hi <= lo || p.Max <= p.Min {
		return 0
	}
	if !LogTravel(p) {
		return (hi - lo) / 127
	}
	a, b := Travel(p, lo), Travel(p, hi)
	if b <= a {
		return 0
	}
	t := Travel(p, x)
	d := (b - a) / 127
	up, down := ValueAt(p, math.Min(t+d, b)), ValueAt(p, math.Max(t-d, a))
	return math.Max(up-ValueAt(p, t), ValueAt(p, t)-down)
}
