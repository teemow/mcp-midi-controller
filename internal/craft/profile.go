package craft

// profile_plugin's knowledge: how a plugin's parameters are matched to the
// capabilities by name, unit and range, how each capability is swept at the
// tap to check that the plugin provides it, and how what was found is written
// as a plugin card. Matching proposes; the sweep decides. A capability whose
// sweep does not move the way it says is left out of the card.

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/teemow/midi-device/device"
	"gopkg.in/yaml.v3"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// CardSpec is a plugin card as it is written: what profile_plugin proposes
// and writes, and what an agent may pass it instead of the proposal.
type CardSpec struct {
	Plugin       string                   `yaml:"plugin" json:"plugin"`
	Name         string                   `yaml:"name" json:"name"`
	Capabilities map[string]ProvisionSpec `yaml:"capabilities" json:"capabilities"`
	Notes        []string                 `yaml:"notes,omitempty" json:"notes,omitempty"`
	Measured     *Measured                `yaml:"measured,omitempty" json:"measured,omitempty"`
}

// ProvisionSpec is how the plugin provides one capability, as written.
type ProvisionSpec struct {
	Write  string               `yaml:"write" json:"write"`
	Params map[string]ParamSpec `yaml:"params,omitempty" json:"params,omitempty"`
	Enable map[string]any       `yaml:"enable,omitempty" json:"enable,omitempty"`
}

// ParamSpec maps one capability parameter onto the plugin's.
type ParamSpec struct {
	Param string    `yaml:"param,omitempty" json:"param,omitempty"`
	Value string    `yaml:"value,omitempty" json:"value,omitempty"`
	Range []float64 `yaml:"range,flow,omitempty" json:"range,omitempty"`
	Fixed *float64  `yaml:"fixed,omitempty" json:"fixed,omitempty"`
}

// Render writes the card as YAML, checked by loading it back.
func (c CardSpec) Render(vocab Vocabulary) ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	if _, err := ParsePluginCard(data, vocab); err != nil {
		return nil, fmt.Errorf("the card does not load: %v", err)
	}
	return data, nil
}

// Over renders the card to replace prev, the rig's earlier card of the same
// plugin as it is written, after a profile that swept the capabilities in
// swept. The capabilities swept now are decided by the sweep; what prev says
// besides stays: its other capabilities (kept, sorted), what was measured of
// those, marked with when, and its notes, which are what is known of the
// plugin. Without prev it is Render.
func (c CardSpec) Over(prev []byte, swept []string, vocab Vocabulary) (data []byte, kept []string, err error) {
	if len(prev) == 0 {
		data, err = c.Render(vocab)
		return data, nil, err
	}
	var old struct {
		Capabilities map[string]any `yaml:"capabilities"`
		Notes        stringList     `yaml:"notes"`
		Measured     *Measured      `yaml:"measured"`
	}
	if err := yaml.Unmarshal(prev, &old); err != nil {
		return nil, nil, fmt.Errorf("the rig's earlier card: %v", err)
	}
	caps := map[string]any{}
	for name, pv := range old.Capabilities {
		if !slices.Contains(swept, name) {
			caps[name] = pv
			kept = append(kept, name)
		}
	}
	sort.Strings(kept)
	for name, pv := range c.Capabilities {
		caps[name] = pv
	}
	notes := append([]string{}, old.Notes...)
	for _, n := range c.Notes {
		if !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}
	measured := c.Measured
	if old.Measured != nil {
		var earlier []string
		for _, check := range old.Measured.Checks {
			if !slices.Contains(kept, checkedCapability(check)) {
				continue
			}
			if !strings.HasPrefix(check, "(") {
				check = "(" + old.Measured.At + ") " + check
			}
			earlier = append(earlier, check)
		}
		if len(earlier) > 0 {
			m := Measured{At: old.Measured.At, By: old.Measured.By}
			if measured != nil {
				m = *measured
			}
			m.Checks = append(append([]string{}, m.Checks...), earlier...)
			measured = &m
		}
	}
	out := struct {
		Plugin       string         `yaml:"plugin"`
		Name         string         `yaml:"name"`
		Capabilities map[string]any `yaml:"capabilities"`
		Notes        []string       `yaml:"notes,omitempty"`
		Measured     *Measured      `yaml:"measured,omitempty"`
	}{c.Plugin, c.Name, caps, notes, measured}
	if data, err = yaml.Marshal(out); err != nil {
		return nil, nil, err
	}
	if _, err := ParsePluginCard(data, vocab); err != nil {
		return nil, nil, fmt.Errorf("the card does not load: %v", err)
	}
	return data, kept, nil
}

// displayOther are the AU flags of a display scale other than linear and log
// (square root, squared, cubed, cube root, exponential): kAudioUnitParameter
// Flag_DisplayMask without DisplayLogarithmic.
const displayOther = 7 << 16

// Steps is the step size of every parameter a card sets over CC, one line per
// parameter for the card's measured checks: how far apart the values of two
// neighbouring CCs lie at the low and the high end of its mapping range (or
// its whole range). It follows the parameter's travel, the mapping law AUM was
// measured to move a parameter by (linear, or the log scale its plugin shows
// it on: songs.md, "Mapping ranges"); a parameter shown on another scale,
// which is not measured, says so.
func Steps(card *PluginCard, dump device.ProbeDump) []string {
	var out []string
	for _, pv := range card.Provides {
		if pv.Write != WriteCC {
			continue
		}
		for _, m := range pv.Params {
			if m.Param == "" || m.Fixed != nil {
				continue
			}
			i, err := findParam(dump, m.Param)
			if err != nil {
				continue
			}
			pp := dump.Parameters[i]
			if len(pp.ValueStrings) > 0 || pp.Max <= pp.Min {
				continue
			}
			lo, hi := pp.Min, pp.Max
			if len(m.Range) == 2 {
				lo, hi = m.Range[0], m.Range[1]
			}
			travel := "its linear travel"
			switch {
			case song.LogTravel(pp):
				travel = "its log travel"
			case pp.Flags&displayOther != 0:
				travel = "a linear travel, though its plugin shows it on another scale, which is not measured"
			}
			unit := pp.UnitName
			if unit != "" {
				unit = " " + unit
			}
			out = append(out, fmt.Sprintf("%s %s: a CC moves %s in steps of %.3g%s at %g to %.3g%s at %g, along %s over %g..%g",
				pv.Capability, m.Name, m.Param, song.CCStep(pp, lo, hi, lo), unit, lo, song.CCStep(pp, lo, hi, hi), unit, hi, travel, lo, hi))
		}
	}
	sort.Strings(out)
	return out
}

// checkedCapability is the capability a measured check is of: its first
// word, after the time it was measured when one is marked.
func checkedCapability(check string) string {
	if strings.HasPrefix(check, "(") {
		if _, rest, ok := strings.Cut(check, ") "); ok {
			check = rest
		}
	}
	name, _, _ := strings.Cut(check, " ")
	return name
}

// synonyms are the words a plugin's parameter is called by, per capability
// parameter, most telling first.
var synonyms = map[string][]string{
	"freq":      {"frequency", "freq", "cutoff", "corner"},
	"slope":     {"slope", "steepness"},
	"gain":      {"gain", "boost"},
	"q":         {"q", "resonance", "bandwidth"},
	"threshold": {"threshold", "thresh"},
	"ratio":     {"ratio"},
	"attack":    {"attack"},
	"release":   {"release"},
	"makeup":    {"makeup", "make-up", "make up", "output gain"},
	"ceiling":   {"ceiling", "output level", "out level", "output"},
	"drive":     {"drive", "saturation", "amount"},
	"mix":       {"mix", "dry/wet", "wet", "blend"},
	"width":     {"width", "spread"},
}

// hints are the words that say a plugin provides a capability, in its name or
// its parameters' names.
var hints = map[string][]string{
	"eq.highpass":         {"high pass", "highpass", "hipass", "high-pass", "low cut", "lowcut", "hpf"},
	"eq.lowpass":          {"low pass", "lowpass", "low-pass", "high cut", "highcut", "lpf"},
	"eq.low_shelf":        {"low shelf", "lowshelf", "low-shelf"},
	"eq.high_shelf":       {"high shelf", "highshelf", "high-shelf"},
	"eq.bell":             {"parametric", "bell", "peak", "peaking"},
	"dynamics.compressor": {"compressor", "compression", "ratio"},
	"dynamics.limiter":    {"limiter", "limiting", "ceiling"},
	"color.saturation":    {"saturat", "drive", "distortion", "tape"},
	"stereo.width":        {"stereo width", "width", "imager"},
	"stereo.mono_bass":    {"mono bass", "bass mono", "mono below"},
	"level.gain":          {"gain", "trim", "volume"},
}

// Propose matches a plugin's writable parameters to the capabilities: a
// capability is proposed when the plugin's name or parameters hint at it and
// its swept parameter (the one profile_plugin checks it by) matches one of the
// plugin's by name, unit and range. Each match carries the conversion its
// units need (seconds for milliseconds, a share of 1 for a percentage) and,
// where the capability uses only part of the plugin's range, that part as
// the mapping range.
func Propose(dump device.ProbeDump, vocab Vocabulary) CardSpec {
	card := CardSpec{Plugin: ComponentTuple(dump.Component), Name: device.FirstNonEmpty(dump.Name, device.ProbeID(dump)), Capabilities: map[string]ProvisionSpec{}}
	words := strings.ToLower(dump.Name)
	for _, p := range dump.Parameters {
		words += " | " + strings.ToLower(p.DisplayName+" "+p.Identifier)
	}
	for _, name := range vocab.CapabilityNames() {
		c, _ := vocab.Capability(name)
		hinted := false
		for _, h := range hints[name] {
			hinted = hinted || strings.Contains(words, h)
		}
		if !hinted {
			continue
		}
		pv := ProvisionSpec{Write: WriteCC, Params: map[string]ParamSpec{}}
		used := map[int]bool{}
		for _, cp := range c.Params {
			if i, spec, ok := matchParam(dump, cp, used); ok {
				used[i] = true
				pv.Params[cp.Name] = spec
			}
		}
		if _, ok := pv.Params[c.Check.Sweep]; !ok {
			continue
		}
		card.Capabilities[name] = pv
	}
	return card
}

// matchParam finds the plugin parameter a capability parameter is, unused so
// far: the best named one whose units fit and whose range overlaps.
func matchParam(dump device.ProbeDump, cp Param, used map[int]bool) (int, ParamSpec, bool) {
	best, bestScore := -1, 0
	var bestSpec ParamSpec
	for i, pp := range dump.Parameters {
		if !pp.Writable || used[i] || len(pp.ValueStrings) > 0 {
			continue
		}
		name := strings.ToLower(device.FirstNonEmpty(pp.DisplayName, pp.Identifier))
		if excluded(name) {
			continue
		}
		score := 0
		for k, w := range synonyms[cp.Name] {
			if name == w {
				score = 100 - k
				break
			}
			if wordIn(name, w) && score < 50-k {
				score = 50 - k
			}
		}
		if score == 0 {
			continue
		}
		spec, ok := fitUnits(pp, cp)
		if !ok {
			continue
		}
		if score > bestScore {
			best, bestScore, bestSpec = i, score, spec
		}
	}
	return best, bestSpec, best >= 0
}

// notMatched are words that mark a plugin parameter as something else than
// it seems: an expander's ratio and threshold are not a compressor's, and a
// side chain's frequency or level is not the signal's.
var notMatched = []string{"expansion", "expander", "side chain", "sidechain", "lookahead"}

func excluded(name string) bool {
	for _, w := range notMatched {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// wordIn reports whether w is in name as a word (or the start of one).
func wordIn(name, w string) bool {
	for i := strings.Index(name, w); i >= 0; {
		before := i == 0 || !isLetter(name[i-1])
		if before {
			return true
		}
		j := strings.Index(name[i+1:], w)
		if j < 0 {
			return false
		}
		i += 1 + j
	}
	return false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// fitUnits says how a plugin parameter takes a capability parameter's value:
// the same number, seconds for milliseconds, or a share of 1 for a
// percentage; and the mapping range when the capability uses only part of the
// plugin's range.
func fitUnits(pp device.ProbeParam, cp Param) (ParamSpec, bool) {
	unit := strings.ToLower(strings.TrimSpace(device.FirstNonEmpty(pp.UnitName, pp.Unit)))
	spec := ParamSpec{Param: device.FirstNonEmpty(pp.DisplayName, pp.Identifier)}
	scale := 1.0
	switch cp.Unit {
	case UnitHz:
		if unit != "hz" && unit != "hertz" {
			return spec, false
		}
	case UnitDB, UnitDBFS:
		// Some plugins (FabFilter's) give their parameters no unit: a
		// threshold of -60..0 is in dB all the same, and the sweep checks it.
		if unit != "db" && unit != "decibels" && unit != "dbfs" && unit != "" && unit != "generic" {
			return spec, false
		}
	case UnitMS:
		switch unit {
		case "ms", "milliseconds":
		case "s", "secs", "seconds":
			scale = 0.001
			spec.Value = cp.Name + " / 1000"
		default:
			return spec, false
		}
	case UnitPercent:
		switch {
		case unit == "%" || unit == "percent":
		case pp.Min == 0 && pp.Max == 1:
			scale = 0.01
			spec.Value = cp.Name + " / 100"
		default:
			return spec, false
		}
	case UnitRatio:
		if unit != "" && unit != "ratio" && unit != "generic" && unit != "indexed" {
			return spec, false
		}
	}
	lo, hi := cp.Min*scale, cp.Max*scale
	if pp.Max <= lo || pp.Min >= hi {
		return spec, false // no more than a point in common: another scale
	}
	// The part of the plugin's range the capability uses, when that is less.
	rlo, rhi := math.Max(lo, pp.Min), math.Min(hi, pp.Max)
	if rhi > rlo && (rlo > pp.Min || rhi < pp.Max) {
		spec.Range = []float64{rlo, rhi}
	}
	return spec, true
}

// Sweep is how profile_plugin checks a capability: the values its swept
// parameter takes in turn, the values the other parameters are held at, and
// the band the measurement reads (none: the whole signal).
type Sweep struct {
	Capability string             `json:"capability"`
	Param      string             `json:"param"`
	Values     []float64          `json:"values"`
	Hold       map[string]float64 `json:"hold,omitempty"`
	Measure    Measurement        `json:"measure"`
	Band       []float64          `json:"band,omitempty"`
	Moves      Direction          `json:"moves"`
	// By is how far the reading has to move from the first value to the last
	// for the check to hold, in the measurement's unit.
	By float64 `json:"by"`
}

// sweeps are the closed list of sweeps, one per capability: values chosen to
// move a noise source's reading clearly.
var sweeps = map[string]Sweep{
	"eq.highpass":         {Values: []float64{40, 160, 640}, Hold: map[string]float64{"slope": 24}, Band: []float64{20, 40}, By: 6},
	"eq.lowpass":          {Values: []float64{1000, 4000, 16000}, Hold: map[string]float64{"slope": 24}, By: 0},
	"eq.low_shelf":        {Values: []float64{-12, 0, 12}, Hold: map[string]float64{"freq": 200}, Band: []float64{20, 150}, By: 6},
	"eq.high_shelf":       {Values: []float64{-12, 0, 12}, Hold: map[string]float64{"freq": 4000}, Band: []float64{6000, 20000}, By: 6},
	"eq.bell":             {Values: []float64{-12, 0, 12}, Hold: map[string]float64{"freq": 1000, "q": 1}, Band: []float64{800, 1250}, By: 6},
	"dynamics.compressor": {Values: []float64{-40, -20, 0}, Hold: map[string]float64{"ratio": 8, "attack": 1, "release": 100, "makeup": 0}, By: 2},
	"dynamics.limiter":    {Values: []float64{-12, -6, 0}, Hold: map[string]float64{"gain": 0, "release": 100}, By: 4},
	"color.saturation":    {Values: []float64{0, 18, 36}, Hold: map[string]float64{"mix": 100}, By: 0},
	"stereo.width":        {Values: []float64{0, 100, 200}, By: 10},
	"stereo.mono_bass":    {Values: []float64{20, 150, 500}, By: 10},
	"level.gain":          {Values: []float64{-12, 0, 12}, By: 12},
}

// SweepFor is how a capability is checked. Values and held values outside
// the capability's own range are brought into it.
func SweepFor(c Capability) Sweep {
	sw := sweeps[c.Name]
	sw.Capability, sw.Param, sw.Measure, sw.Moves = c.Name, c.Check.Sweep, c.Check.Measure, c.Check.Moves
	if cp, ok := c.Param(sw.Param); ok {
		vals := make([]float64, len(sw.Values))
		for i, v := range sw.Values {
			vals[i] = math.Min(math.Max(v, cp.Min), cp.Max)
		}
		sw.Values = vals
	}
	if c.Check.Band != nil && c.Check.Band.FromHz > 0 {
		sw.Band = []float64{c.Check.Band.FromHz, c.Check.Band.ToHz}
	}
	if sw.By == 0 {
		// A centroid moves in Hz; it has to move by a fifth at least.
		sw.By = -20
	}
	return sw
}

// Judge says whether readings taken at a sweep's values moved the way the
// capability says: from the first to the last by sw.By or more (a negative
// By is a share in percent of the first reading), and not back the other way
// on the way.
func (sw Sweep) Judge(readings []float64) (bool, string) {
	if len(readings) != len(sw.Values) || len(readings) < 2 {
		return false, "not every value of the sweep was measured"
	}
	first, last := readings[0], readings[len(readings)-1]
	moved := last - first
	if sw.Moves == Falls {
		moved = first - last
	}
	need := sw.By
	if need < 0 {
		need = math.Abs(first) * -sw.By / 100
	}
	back := false
	for i := 1; i < len(readings); i++ {
		d := readings[i] - readings[i-1]
		if sw.Moves == Falls {
			d = -d
		}
		back = back || d < -need/2
	}
	vals := make([]string, len(readings))
	for i, r := range readings {
		vals[i] = fmt.Sprintf("%g→%.2f", sw.Values[i], r)
	}
	what := fmt.Sprintf("%s swept %s: %s %s %.2f %s", sw.Capability, sw.Param, sw.Measure, sw.Moves, moved, sw.Measure.Unit())
	if len(sw.Band) == 2 {
		what = fmt.Sprintf("%s swept %s: %s %g..%g Hz %s %.2f %s", sw.Capability, sw.Param, sw.Measure, sw.Band[0], sw.Band[1], sw.Moves, moved, sw.Measure.Unit())
	}
	what += " (" + strings.Join(vals, ", ") + ")"
	switch {
	case back:
		return false, what + "; it moved back on the way"
	case moved < need:
		return false, fmt.Sprintf("%s; it has to move %.2f or more", what, need)
	}
	return true, what
}

// sortedCaps lists a card's capabilities, sorted.
func (c CardSpec) sortedCaps() []string {
	out := make([]string, 0, len(c.Capabilities))
	for k := range c.Capabilities {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Capabilities lists the card's capabilities, sorted.
func (c CardSpec) CapabilityNames() []string { return c.sortedCaps() }

// PluginValues converts a capability's values into the plugin's own
// parameters through the provision's conversions: plugin parameter -> value.
// A parameter that is fixed, or that values do not give, is left out.
func (p Provision) PluginValues(values map[string]float64) (map[string]float64, error) {
	out := map[string]float64{}
	for _, m := range p.Params {
		x, ok := values[m.Name]
		if !ok || m.Param == "" || m.Fixed != nil {
			continue
		}
		y, err := convert(m, x, values)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", m.Name, err)
		}
		out[m.Param] = y
	}
	return out, nil
}
