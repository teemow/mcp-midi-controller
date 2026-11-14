package craft

// The span a card's value can take. A value that follows the song (the
// subsonic high-pass's clamp(0.5 * lowest_note_hz, 20, 35)) is one number only
// once a song is known; before that, get_technique still has to tell whether a
// plugin can carry it. So a value is also worked out as a span: every name
// over the span a song can give it, and the arithmetic over spans. clamp, min
// and max with constant ends give a tight span (20..35 Hz), which is what
// makes it useful.

import (
	"errors"
	"fmt"
	"math"
)

// SongSpans are the spans a song can give the names a technique card's values
// use: a tempo of 20 to 300 BPM and the beat and bar that follow from it (a
// bar of one to sixteen beats), a lowest note from MIDI 0 (8.2 Hz) to C8
// (4186 Hz), and an energy of 1 to 10.
func SongSpans() map[string][2]float64 {
	return map[string][2]float64{
		VarTempo:         {20, 300},
		VarBeatMS:        {200, 3000},
		VarBarMS:         {200, 48000},
		VarLowestNoteHz:  {8.18, 4186},
		VarSectionEnergy: {1, 10},
	}
}

// Bounds is the span of values the expression can take when each name lies
// within its span in spans. A name without a span, or a division by a span
// that holds 0, has no bounds and is an error.
func (e *Expr) Bounds(spans map[string][2]float64) (lo, hi float64, err error) {
	lo, hi, err = e.root.bounds(spans)
	if err != nil {
		return 0, 0, err
	}
	if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
		return 0, 0, fmt.Errorf("%s has no finite bounds", e.src)
	}
	return lo, hi, nil
}

func (n numNode) bounds(map[string][2]float64) (float64, float64, error) {
	return float64(n), float64(n), nil
}

func (n varNode) bounds(spans map[string][2]float64) (float64, float64, error) {
	s, ok := spans[string(n)]
	if !ok {
		return 0, 0, fmt.Errorf("needs %s, which is not known here", string(n))
	}
	return s[0], s[1], nil
}

func (n negNode) bounds(spans map[string][2]float64) (float64, float64, error) {
	lo, hi, err := n.x.bounds(spans)
	return -hi, -lo, err
}

func (n binNode) bounds(spans map[string][2]float64) (float64, float64, error) {
	a, b, err := n.l.bounds(spans)
	if err != nil {
		return 0, 0, err
	}
	c, d, err := n.r.bounds(spans)
	if err != nil {
		return 0, 0, err
	}
	switch n.op {
	case '+':
		return a + c, b + d, nil
	case '-':
		return a - d, b - c, nil
	case '*':
		return spanOf(a*c, a*d, b*c, b*d)
	default:
		if c <= 0 && d >= 0 {
			return 0, 0, errors.New("divides by a value that can be 0")
		}
		return spanOf(a/c, a/d, b/c, b/d)
	}
}

func (n callNode) bounds(spans map[string][2]float64) (float64, float64, error) {
	los := make([]float64, len(n.args))
	his := make([]float64, len(n.args))
	for i, a := range n.args {
		lo, hi, err := a.bounds(spans)
		if err != nil {
			return 0, 0, err
		}
		los[i], his[i] = lo, hi
	}
	switch n.fn {
	case "min":
		lo, hi := los[0], his[0]
		for i := 1; i < len(los); i++ {
			lo, hi = math.Min(lo, los[i]), math.Min(hi, his[i])
		}
		return lo, hi, nil
	case "max":
		lo, hi := los[0], his[0]
		for i := 1; i < len(los); i++ {
			lo, hi = math.Max(lo, los[i]), math.Max(hi, his[i])
		}
		return lo, hi, nil
	case "log2":
		if los[0] <= 0 {
			return 0, 0, errors.New("takes log2 of a value that can be 0 or below")
		}
		return math.Log2(los[0]), math.Log2(his[0]), nil
	default: // clamp(x, low, high)
		if los[1] > his[2] {
			return 0, 0, fmt.Errorf("clamp's low end can lie above its high end")
		}
		return math.Min(math.Max(los[0], los[1]), los[2]), math.Min(math.Max(his[0], his[1]), his[2]), nil
	}
}

func spanOf(xs ...float64) (float64, float64, error) {
	lo, hi := xs[0], xs[0]
	for _, x := range xs[1:] {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi, nil
}
