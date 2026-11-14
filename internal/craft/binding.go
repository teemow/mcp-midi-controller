package craft

import (
	"fmt"
	"strings"

	"github.com/teemow/midi-device/device"
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
	// Problems say why it cannot carry the technique; none: it can.
	Problems []string `json:"problems,omitempty"`
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
	// Step is how far apart the 128 values of a CC lie, in the plugin's
	// units, over the mapping range or else the parameter's whole range.
	Step float64 `json:"step,omitempty"`
}

// Bind finds, for every capability the technique needs, the plugin of the rig
// that would carry it. A writing card needs none.
func (l *Library) Bind(t *Technique, rig Rig) []Binding {
	out := make([]Binding, 0, len(t.Needs))
	for _, capability := range t.Needs {
		out = append(out, l.bind(t, capability, rig))
	}
	return out
}

func (l *Library) bind(t *Technique, capability string, rig Rig) Binding {
	b := Binding{Capability: capability}
	preferred := l.prefer[capability]
	for _, card := range l.Providers(capability) {
		cand := candidate(t, capability, card, rig)
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
		b.Missing = fmt.Sprintf("no plugin card provides %s: write one in craft/plugins/ of the config dir", capability)
	default:
		b.Missing = fmt.Sprintf("none of the %d plugins with a card for %s can carry it on this rig; see each one's problems", len(b.Candidates), capability)
	}
	return b
}

// candidate checks one plugin card against the rig and the technique.
func candidate(t *Technique, capability string, card *PluginCard, rig Rig) Candidate {
	pv, _ := card.Provision(capability)
	c := Candidate{Plugin: card.Plugin, Name: card.Name, Write: pv.Write, Preset: pv.Preset, Enable: pv.Enable, Notes: card.Notes}
	dump, onRig := rig.find(card.Component)
	if onRig {
		c.Probe = device.ProbeID(*dump)
	} else {
		c.Problems = append(c.Problems, "not on this rig: no probe dump of it is staged (import_auv3_probe)")
	}
	setting, _ := t.Setting(capability)
	// The card's constant values by parameter, for a plugin card's value
	// that converts them.
	consts := map[string]float64{}
	for _, pvv := range setting.Values {
		if v, ok := pvv.Value.Const(); ok {
			consts[pvv.Param] = v
		}
	}
	for _, pvv := range setting.Values {
		m, ok := pv.Param(pvv.Param)
		if !ok {
			c.Problems = append(c.Problems, fmt.Sprintf("cannot set %s, which the technique sets", pvv.Param))
			continue
		}
		bp := BoundParam{Name: m.Name, Param: m.Param, Field: m.Field, Value: m.Value, Fixed: m.Fixed, Range: m.Range}
		if m.Fixed != nil {
			if v, ok := pvv.Value.Const(); !ok || v != *m.Fixed {
				c.Problems = append(c.Problems, fmt.Sprintf("holds %s at %g %s; the technique sets %s", m.Name, *m.Fixed, pvv.Unit, pvv.Value))
			}
		}
		if onRig && m.Param != "" {
			checkParam(&c, &bp, dump, m, pvv, consts)
		}
		c.Params = append(c.Params, bp)
	}
	if onRig {
		for _, e := range pv.Enable {
			if _, err := findParam(*dump, e.Param); err != nil {
				c.Problems = append(c.Problems, fmt.Sprintf("enable %s: the probe dump %v", e.Param, err))
			}
		}
		if pv.Write == WritePreset && !hasPreset(*dump, pv.Preset) {
			c.Problems = append(c.Problems, fmt.Sprintf("has no factory preset %q", pv.Preset))
		}
	}
	return c
}

// checkParam checks a mapped parameter against the plugin's probe dump: that
// it is there, that the mapping range lies within its range, that a constant
// value reaches the plugin within the mapping range, and, where the value is
// a number in the plugin's units, that a CC's step reaches the technique's
// precision. consts are the card's constant values by parameter.
func checkParam(c *Candidate, bp *BoundParam, dump *device.ProbeDump, m ParamMap, pvv ParamValue, consts map[string]float64) {
	i, err := findParam(*dump, m.Param)
	if err != nil {
		c.Problems = append(c.Problems, fmt.Sprintf("%s: the probe dump %v", m.Name, err))
		return
	}
	pp := dump.Parameters[i]
	bp.Unit = pp.UnitName
	lo, hi := pp.Min, pp.Max
	if len(m.Range) == 2 {
		if m.Range[0] < pp.Min || m.Range[1] > pp.Max {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: the mapping range %s lies outside %s's range %g..%g", m.Name, rangeString(m.Range), m.Param, pp.Min, pp.Max))
		}
		lo, hi = m.Range[0], m.Range[1]
	}
	if c.Write != WriteCC || hi <= lo {
		return
	}
	bp.Step = (hi - lo) / 127
	v, isConst := pvv.Value.Const()
	if !isConst {
		return // it follows the song; it is worked out when the card is applied
	}
	at := v
	if m.Value != nil {
		x, err := m.Value.Eval(consts)
		if err != nil {
			return
		}
		at = x
	}
	if at < lo || at > hi {
		if m.Value != nil {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: %g comes to %g at the plugin, outside the mapping range %g..%g", m.Name, v, at, lo, hi))
		} else {
			c.Problems = append(c.Problems, fmt.Sprintf("%s: %g lies outside the mapping range %g..%g", m.Name, v, lo, hi))
		}
		return
	}
	if pvv.Precision == nil || m.Value != nil {
		return
	}
	if within := pvv.Precision.Within(v); bp.Step/2 > within {
		c.Problems = append(c.Problems, fmt.Sprintf("%s: a CC over %g..%g moves in steps of %.3g %s, too coarse for %g within %s; narrow the mapping range",
			m.Name, lo, hi, bp.Step, pvv.Unit, v, pvv.Precision))
	}
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
