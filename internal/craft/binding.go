package craft

import (
	"fmt"
	"math"
	"strings"

	"github.com/teemow/midi-device/device"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// Rig is the plugins a rig has: the probe dumps staged for it, which is also
// what a song's plugin names.
type Rig []device.ProbeDump

// find returns the rig's dump of a component.
func (r Rig) find(c device.ProbeComponent) (*device.ProbeDump, bool) {
	for i := range r {
		d := &r[i].Component
		if d.Type == c.Type && d.Subtype == c.Subtype && d.Manufacturer == c.Manufacturer {
			return &r[i], true
		}
	}
	return nil, false
}

// Binding is how a technique would get one capability on the rig.
type Binding struct {
	Capability string `json:"capability"`
	// Plugin is the component tuple of the plugin it would be set with, the
	// first candidate that can carry it. Empty: none can, Missing says why.
	Plugin  string `json:"plugin,omitempty"`
	Missing string `json:"missing,omitempty"`
	// Closest is, when none can carry it, the candidate on the rig that comes
	// closest to the precision the technique asks for, and Off how far off it
	// is: its worst setting's error as a multiple of the precision asked for.
	Closest string  `json:"closest,omitempty"`
	Off     float64 `json:"off,omitempty"`
	// Candidates are the plugins with a card for the capability, in the rig's
	// order of preference.
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Bound returns the candidate the binding chose.
func (b Binding) Bound() (Candidate, bool) {
	for _, c := range b.Candidates {
		if c.Plugin == b.Plugin && b.Plugin != "" {
			return c, true
		}
	}
	return Candidate{}, false
}

// Candidate is one plugin that has a card for a capability.
type Candidate struct {
	Plugin string `json:"plugin"`
	Name   string `json:"name"`
	// Probe is the staged probe id of the plugin, what a song names it by.
	// Empty: the rig has no probe dump of it.
	Probe string `json:"probe,omitempty"`
	// Preferred is its place in craft/prefer.yaml, from 1; 0: not named there.
	Preferred int          `json:"preferred,omitempty"`
	Write     string       `json:"write"`
	Preset    string       `json:"preset,omitempty"`
	Params    []BoundParam `json:"params,omitempty"`
	Enable    []Enable     `json:"enable,omitempty"`
	Notes     []string     `json:"notes,omitempty"`
	// Off is how far off the plugin comes, its worst setting's error as a
	// multiple of the precision the technique asks for: 1 or below reaches
	// it. 0: nothing could be worked out (no precision asked, or not on the
	// rig).
	Off float64 `json:"off,omitempty"`
	// Problems say why it cannot carry the technique; none: it can.
	Problems []string `json:"problems,omitempty"`
	// reach marks problems of precision or of the mapping range alone: a
	// plugin that has only those is a closest one.
	reach bool
}

// BoundParam is one parameter the technique sets, as the plugin takes it.
type BoundParam struct {
	Name  string    `json:"name"`
	Param string    `json:"param,omitempty"`
	Field string    `json:"field,omitempty"`
	Value *Expr     `json:"value,omitempty"`
	Fixed *float64  `json:"fixed,omitempty"`
	Range []float64 `json:"range,omitempty"`
	Unit  string    `json:"unit,omitempty"`
	// Want is the value the technique asks for, in the capability's unit:
	// one number with a song's values, else the span a song can give it.
	Want []float64 `json:"want,omitempty"`
	// At is Want in the plugin's units, after the card's conversion.
	At []float64 `json:"at,omitempty"`
	// Step is how far apart the values of two neighbouring CCs lie there, in
	// the plugin's units, over the mapping range or else the parameter's
	// whole range, along the parameter's travel (song.CCStep): the coarsest
	// over Want.
	Step float64 `json:"step,omitempty"`
	// Within is how far off the value may come, in the plugin's units: the
	// technique's precision carried through the conversion, the tightest
	// over Want.
	Within float64 `json:"within,omitempty"`
	// Note says what could not be worked out yet (a value that follows the
	// song in a way that has no bounds before one is known).
	Note string `json:"note,omitempty"`
}

// Bind finds, for every capability the technique needs, the plugin of the rig
// that would carry it. env holds the song's values (tempo, beat_ms, bar_ms,
// lowest_note_hz, section.energy) when the card is applied to a song; nil
// binds without one, over the values a song can give (SongSpans), so a plugin
// is chosen only if it carries every one of them. A writing card needs none.
func (l *Library) Bind(t *Technique, rig Rig, env map[string]float64) []Binding {
	out := make([]Binding, 0, len(t.Needs))
	for _, capability := range t.Needs {
		out = append(out, l.bind(t, capability, rig, env))
	}
	return out
}

func (l *Library) bind(t *Technique, capability string, rig Rig, env map[string]float64) Binding {
	b := Binding{Capability: capability}
	preferred := l.prefer[capability]
	for _, card := range l.Providers(capability) {
		cand := candidate(t, capability, card, rig, env)
		for i, tuple := range preferred {
			if tuple == card.Plugin {
				cand.Preferred = i + 1
			}
		}
		if b.Plugin == "" && len(cand.Problems) == 0 {
			b.Plugin = cand.Plugin
		}
		b.Candidates = append(b.Candidates, cand)
	}
	switch {
	case b.Plugin != "":
	case len(b.Candidates) == 0:
		b.Missing = fmt.Sprintf("no plugin card provides %s: write one in craft/plugins/ of the config dir (profile_plugin writes one)", capability)
	default:
		b.Missing = fmt.Sprintf("none of the %d plugins with a card for %s can carry it on this rig; see each one's problems", len(b.Candidates), capability)
		for _, c := range b.Candidates {
			if c.reach && c.Off > 0 && (b.Closest == "" || c.Off < b.Off) {
				b.Closest, b.Off = c.Plugin, c.Off
			}
		}
		if b.Closest != "" {
			for _, c := range b.Candidates {
				if c.Plugin == b.Closest {
					b.Missing += fmt.Sprintf("; the closest is %s, %.3g times as far off as the technique allows", c.Name, b.Off)
				}
			}
		}
	}
	return b
}

// candidate checks one plugin card against the rig and the technique.
func candidate(t *Technique, capability string, card *PluginCard, rig Rig, env map[string]float64) Candidate {
	pv, _ := card.Provision(capability)
	c := Candidate{Plugin: card.Plugin, Name: card.Name, Write: pv.Write, Preset: pv.Preset, Enable: pv.Enable, Notes: card.Notes, reach: true}
	dump, onRig := rig.find(card.Component)
	if onRig {
		c.Probe = device.ProbeID(*dump)
	} else {
		c.reach = false
		c.Problems = append(c.Problems, "not on this rig: no probe dump of it is staged (import_auv3_probe)")
	}
	setting, _ := t.Setting(capability)
	// The capability's values, for a plugin card's conversion that uses them:
	// each one's value with the song's values, else the middle of its span.
	values := map[string]float64{}
	for _, pvv := range setting.Values {
		if want, err := valueSpan(pvv.Value, env); err == nil && want != nil {
			values[pvv.Param] = (want[0] + want[1]) / 2
		}
	}
	for _, pvv := range setting.Values {
		m, ok := pv.Param(pvv.Param)
		if !ok {
			c.reach = false
			c.Problems = append(c.Problems, fmt.Sprintf("cannot set %s, which the technique sets", pvv.Param))
			continue
		}
		bp := BoundParam{Name: m.Name, Param: m.Param, Field: m.Field, Value: m.Value, Fixed: m.Fixed, Range: m.Range}
		if m.Fixed != nil {
			v, isConst := pvv.Value.Const()
			within := 1e-9
			if pvv.Precision != nil && isConst {
				within = math.Max(pvv.Precision.Within(v), within)
			}
			if !isConst || math.Abs(v-*m.Fixed) > within {
				c.reach = false
				c.Problems = append(c.Problems, fmt.Sprintf("holds %s at %g %s; the technique sets %s", m.Name, *m.Fixed, pvv.Unit, pvv.Value))
			}
		}
		if onRig && m.Param != "" {
			checkParam(&c, &bp, dump, m, pvv, values, env)
		}
		c.Params = append(c.Params, bp)
	}
	if onRig {
		for _, e := range pv.Enable {
			i, err := findParam(*dump, e.Param)
			if err != nil {
				c.reach = false
				c.Problems = append(c.Problems, fmt.Sprintf("enable %s: the probe dump %v", e.Param, err))
				continue
			}
			if _, err := song.ValuePlace(dump.Parameters[i], song.ParseValue(e.Value)); err != nil {
				c.reach = false
				c.Problems = append(c.Problems, fmt.Sprintf("enable %s: %v", e.Param, err))
			}
		}
		if pv.Write == WritePreset && !hasPreset(*dump, pv.Preset) {
			c.reach = false
			c.Problems = append(c.Problems, fmt.Sprintf("has no factory preset %q", pv.Preset))
		}
	}
	if !c.reach {
		c.Off = 0
	}
	return c
}

// valueSpan is the value a technique gives a parameter, in the capability's
// unit: one number when it is a constant or the song's values are known, else
// the span the song's values can give it. nil: a label, which has no span.
func valueSpan(v Value, env map[string]float64) ([]float64, error) {
	if v.Expr == nil {
		return nil, nil
	}
	if x, ok := v.Expr.Const(); ok {
		return []float64{x, x}, nil
	}
	if env != nil {
		x, err := v.Expr.Eval(env)
		if err != nil {
			return nil, err
		}
		return []float64{x, x}, nil
	}
	lo, hi, err := v.Expr.Bounds(SongSpans())
	if err != nil {
		return nil, err
	}
	return []float64{lo, hi}, nil
}

// convert carries a value in the capability's unit into the plugin's units
// through the card's conversion; values are the capability's other values.
func convert(m ParamMap, x float64, values map[string]float64) (float64, error) {
	if m.Value == nil {
		return x, nil
	}
	env := make(map[string]float64, len(values)+1)
	for k, v := range values {
		env[k] = v
	}
	env[m.Name] = x
	return m.Value.Eval(env)
}

// checkParam checks a mapped parameter against the plugin's probe dump: that
// it is there, that the mapping range lies within its range, that the value
// the technique asks for (one number, or the span a song can give it) reaches
// the plugin within the mapping range, and that a CC's step there, along the
// parameter's travel, reaches the technique's precision. values are the
// capability's values for a conversion that uses them.
func checkParam(c *Candidate, bp *BoundParam, dump *device.ProbeDump, m ParamMap, pvv ParamValue, values, env map[string]float64) {
	i, err := findParam(*dump, m.Param)
	if err != nil {
		c.reach = false
		c.Problems = append(c.Problems, fmt.Sprintf("%s: the probe dump %v", m.Name, err))
		return
	}
	pp := dump.Parameters[i]
	bp.Unit = pp.UnitName
	lo, hi := pp.Min, pp.Max
	if len(m.Range) == 2 {
		if m.Range[0] < pp.Min || m.Range[1] > pp.Max {
			c.reach = false
			c.Problems = append(c.Problems, fmt.Sprintf("%s: the mapping range %s lies outside %s's range %g..%g", m.Name, rangeString(m.Range), m.Param, pp.Min, pp.Max))
		}
		lo, hi = m.Range[0], m.Range[1]
	}
	if c.Write != WriteCC || hi <= lo {
		return
	}
	want, err := valueSpan(pvv.Value, env)
	switch {
	case err != nil && env != nil:
		c.reach = false
		c.Problems = append(c.Problems, fmt.Sprintf("%s: %s: %v", m.Name, pvv.Value, err))
		return
	case err != nil:
		bp.Note = fmt.Sprintf("%s follows the song (%v); it is checked when the card is applied", pvv.Value, err)
		bp.Step = song.CCStep(pp, lo, hi, (lo+hi)/2)
		return
	case want == nil:
		return // a label: passed on as it is
	}
	bp.Want = want
	at := make([]float64, 2)
	for k, x := range want {
		y, err := convert(m, x, values)
		if err != nil {
			c.reach = false
			c.Problems = append(c.Problems, fmt.Sprintf("%s: its conversion %s at %g: %v", m.Name, m.Value, x, err))
			return
		}
		at[k] = y
	}
	if at[0] > at[1] {
		at[0], at[1] = at[1], at[0]
	}
	bp.At = at
	tol := 1e-9 * math.Max(1, hi-lo)
	if at[0] < lo-tol || at[1] > hi+tol {
		what := fmt.Sprintf("%g", at[0])
		if at[1] != at[0] {
			what = fmt.Sprintf("%g..%g", at[0], at[1])
		}
		if m.Value != nil {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: %s comes to %s at the plugin, outside the mapping range %g..%g", m.Name, pvv.Value, what, lo, hi))
		} else {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: %s lies outside the mapping range %g..%g", m.Name, what, lo, hi))
		}
		if pvv.Precision != nil {
			// How far the nearest end is, against the precision asked for.
			miss := math.Max(lo-at[0], at[1]-hi)
			if w := tolerance(m, pvv, want[0], values); w > 0 {
				c.Off = math.Max(c.Off, 1+miss/w)
			}
		} else {
			c.reach = false
		}
		return
	}
	step := math.Max(song.CCStep(pp, lo, hi, at[0]), song.CCStep(pp, lo, hi, at[1]))
	bp.Step = step
	if pvv.Precision == nil {
		return
	}
	worst := 0.0
	for k, x := range want {
		w := tolerance(m, pvv, x, values)
		if k == 0 || w < bp.Within {
			bp.Within = w
		}
		if w <= 0 {
			c.reach = false
			c.Problems = append(c.Problems, fmt.Sprintf("%s: the precision %s of %g carries no tolerance through the conversion %s", m.Name, pvv.Precision, x, m.Value))
			return
		}
		worst = math.Max(worst, (song.CCStep(pp, lo, hi, at[k])/2)/w)
	}
	c.Off = math.Max(c.Off, worst)
	if worst > 1 {
		c.Problems = append(c.Problems, fmt.Sprintf("%s: a CC over %g..%g moves in steps of %.3g %s there, too coarse for %s within %s; narrow the mapping range",
			m.Name, lo, hi, step, unitOr(pp.UnitName), pvv.Value, pvv.Precision))
	}
}

// tolerance is how far off, in the plugin's units, a value x may come: the
// technique's precision carried through the card's conversion, the tighter of
// the two directions.
func tolerance(m ParamMap, pvv ParamValue, x float64, values map[string]float64) float64 {
	w := pvv.Precision.Within(x)
	if m.Value == nil {
		return w
	}
	mid, err := convert(m, x, values)
	if err != nil {
		return 0
	}
	up, errUp := convert(m, x+w, values)
	down, errDown := convert(m, x-w, values)
	switch {
	case errUp != nil && errDown != nil:
		return 0
	case errUp != nil:
		return math.Abs(mid - down)
	case errDown != nil:
		return math.Abs(up - mid)
	}
	return math.Min(math.Abs(up-mid), math.Abs(mid-down))
}

// findParam finds a writable parameter of a probe dump by its identifier,
// display name or key path, ignoring case, as a song's params find theirs.
func findParam(dump device.ProbeDump, name string) (int, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, pick := range []func(device.ProbeParam) string{
		func(p device.ProbeParam) string { return p.Identifier },
		func(p device.ProbeParam) string { return p.DisplayName },
		func(p device.ProbeParam) string { return p.KeyPath },
	} {
		found, count := -1, 0
		for i, p := range dump.Parameters {
			if p.Writable && strings.ToLower(pick(p)) == want {
				if found < 0 {
					found = i
				}
				count++
			}
		}
		switch {
		case count == 1:
			return found, nil
		case count > 1:
			return 0, fmt.Errorf("has %d parameters called %q", count, name)
		}
	}
	return 0, fmt.Errorf("has no writable parameter %q", name)
}

func hasPreset(dump device.ProbeDump, name string) bool {
	for _, p := range dump.FactoryPresets {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}
