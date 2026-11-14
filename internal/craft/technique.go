package craft

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Crafts are the ids of the crafts, one per craft. A card belongs to one, and
// its id starts with it.
var Crafts = []string{"production", "composition", "drums", "sound_design", "orchestration", "mixing", "mastering"}

// AppliesTo are what a technique can be applied to.
var AppliesTo = []string{"track", "section", "master", "song"}

// Kind is the kind of a technique card.
type Kind string

// The two kinds: a settings card sets capabilities in a chain, a writing card
// gives a transform its options.
const (
	KindSettings Kind = "settings"
	KindWriting  Kind = "writing"
)

// Technique is one technique of one craft, checked against the vocabulary.
type Technique struct {
	ID        string   `json:"id"`
	Craft     string   `json:"craft"`
	Kind      Kind     `json:"kind"`
	Title     string   `json:"title"`
	Intent    string   `json:"intent,omitempty"`
	When      string   `json:"when,omitempty"`
	WhenNot   []string `json:"when_not,omitempty"`
	AppliesTo []string `json:"applies_to"`

	// A settings card: the capabilities it needs in the order they go into
	// the chain, what it sets, where in the chain, and how its result is
	// checked.
	Needs    []string  `json:"needs,omitempty"`
	Settings []Setting `json:"set,omitempty"`
	Place    *Place    `json:"place,omitempty"`
	Verify   *Verify   `json:"verify,omitempty"`

	// A writing card: the transform and its options.
	Transform string        `json:"transform,omitempty"`
	Options   []OptionValue `json:"options,omitempty"`

	// Source is where the card came from: "shipped", or its file in the
	// config dir.
	Source string `json:"source"`
}

// Setting is what a settings card sets on one capability.
type Setting struct {
	Capability string       `json:"capability"`
	Values     []ParamValue `json:"values,omitempty"`
}

// ParamValue is one parameter's value, in the order the capability lists its
// parameters, and the precision a binding has to reach for it.
type ParamValue struct {
	Param     string     `json:"param"`
	Value     Value      `json:"value"`
	Unit      string     `json:"unit,omitempty"`
	Precision *Precision `json:"precision,omitempty"`
}

// OptionValue is one option a writing card gives its transform.
type OptionValue struct {
	Option string `json:"option"`
	Value  Value  `json:"value"`
}

// Precision is how close a binding has to come to a value: a share of the
// value ("10%") or an amount in the parameter's unit.
type Precision struct {
	Percent float64 // > 0 for a share of the value
	Amount  float64 // > 0 for an amount in the unit
}

// Within returns how far off a value of v may be.
func (p Precision) Within(v float64) float64 {
	if p.Percent > 0 {
		if v < 0 {
			v = -v
		}
		return v * p.Percent / 100
	}
	return p.Amount
}

func (p Precision) String() string {
	if p.Percent > 0 {
		return strconv.FormatFloat(p.Percent, 'g', -1, 64) + "%"
	}
	return strconv.FormatFloat(p.Amount, 'g', -1, 64)
}

// MarshalJSON writes a share as "10%" and an amount as its number.
func (p Precision) MarshalJSON() ([]byte, error) {
	if p.Percent > 0 {
		return json.Marshal(p.String())
	}
	return json.Marshal(p.Amount)
}

// Place is where a settings card's capabilities go in the chain of what it is
// applied to: first, last, or before or after the plugin of another capability.
type Place struct {
	Where      string `json:"where"` // first | last | before | after
	Capability string `json:"capability,omitempty"`
}

func (p Place) String() string {
	if p.Capability != "" {
		return p.Where + " " + p.Capability
	}
	return p.Where
}

// Verify is how a settings card's result is checked: a measurement of what
// it is applied to before and after, and what it has to show.
type Verify struct {
	Measure Measurement `json:"measure"`
	Band    []*Expr     `json:"band,omitempty"` // from and to, in Hz
	// Expect is falls or rises (by at least Value, after against before),
	// at_most or at_least (after, against Value).
	Expect string `json:"expect"`
	Value  *Expr  `json:"value"`
	Unit   string `json:"unit,omitempty"`
}

func (v Verify) String() string {
	var b strings.Builder
	b.WriteString(string(v.Measure))
	if len(v.Band) == 2 {
		fmt.Fprintf(&b, " %s..%s Hz", v.Band[0], v.Band[1])
	}
	switch v.Expect {
	case expectFalls, expectRises:
		fmt.Fprintf(&b, " %s by %s %s or more", v.Expect, v.Value, v.Unit)
	default:
		fmt.Fprintf(&b, " %s %s %s", strings.ReplaceAll(v.Expect, "_", " "), v.Value, v.Unit)
	}
	return strings.TrimSpace(b.String())
}

const (
	expectFalls   = "falls"
	expectRises   = "rises"
	expectAtMost  = "at_most"
	expectAtLeast = "at_least"
)

// stringList reads a single string or a list of them.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = stringList{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*l = list
	return nil
}

// techniqueFile is a technique card as written.
type techniqueFile struct {
	ID        string                    `yaml:"id"`
	Craft     string                    `yaml:"craft"`
	Title     string                    `yaml:"title"`
	Intent    string                    `yaml:"intent"`
	When      string                    `yaml:"when"`
	WhenNot   stringList                `yaml:"when_not"`
	AppliesTo stringList                `yaml:"applies_to"`
	Needs     []string                  `yaml:"needs"`
	Set       map[string]map[string]any `yaml:"set"`
	Precision map[string]map[string]any `yaml:"precision"`
	Place     string                    `yaml:"place"`
	Verify    *verifyFile               `yaml:"verify"`
	Transform string                    `yaml:"transform"`
	Options   map[string]any            `yaml:"options"`

	// Targets is applies_to's name until 2026-11-14, read only to say so.
	Targets stringList `yaml:"targets"`
}

type verifyFile struct {
	Measure string `yaml:"measure"`
	Band    []any  `yaml:"band"`
	Falls   any    `yaml:"falls"`
	Rises   any    `yaml:"rises"`
	AtMost  any    `yaml:"at_most"`
	AtLeast any    `yaml:"at_least"`
}

// idPattern is the form of a card's id: its craft, a dot, and a name in lower
// case.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z0-9_]+(\.[a-z0-9_]+)*$`)

// ParseTechnique reads one technique card and checks it against the
// vocabulary. The error lists everything that is wrong with it.
func ParseTechnique(data []byte, vocab Vocabulary) (*Technique, error) {
	var f techniqueFile
	if err := decodeStrict(data, &f); err != nil {
		return nil, err
	}
	var p problems
	t := &Technique{
		ID:      strings.TrimSpace(f.ID),
		Craft:   strings.TrimSpace(f.Craft),
		Title:   strings.TrimSpace(f.Title),
		Intent:  strings.TrimSpace(f.Intent),
		When:    strings.TrimSpace(f.When),
		WhenNot: f.WhenNot,
	}
	switch {
	case t.ID == "":
		p.addf("id: missing")
	case !idPattern.MatchString(t.ID):
		p.addf("id: %q is not <craft>.<name> in lower case", t.ID)
	}
	switch {
	case t.Craft == "":
		p.addf("craft: missing (one of %s)", strings.Join(Crafts, ", "))
	case !slices.Contains(Crafts, t.Craft):
		p.addf("craft: %q is not a craft (one of %s)", t.Craft, strings.Join(Crafts, ", "))
	case t.ID != "" && !strings.HasPrefix(t.ID, t.Craft+"."):
		p.addf("id: %q does not start with its craft, %s.", t.ID, t.Craft)
	}
	if t.Title == "" {
		p.addf("title: missing")
	}
	switch {
	case len(f.Targets) > 0:
		p.addf("targets: renamed to applies_to")
	case len(f.AppliesTo) == 0:
		p.addf("applies_to: missing (%s)", strings.Join(AppliesTo, ", "))
	}
	for _, at := range f.AppliesTo {
		at = strings.TrimSpace(at)
		switch {
		case !slices.Contains(AppliesTo, at):
			p.addf("applies_to: %q is not one of %s", at, strings.Join(AppliesTo, ", "))
		case slices.Contains(t.AppliesTo, at):
			p.addf("applies_to: %s twice", at)
		default:
			t.AppliesTo = append(t.AppliesTo, at)
		}
	}

	settings := len(f.Set) > 0 || len(f.Needs) > 0
	writing := strings.TrimSpace(f.Transform) != ""
	switch {
	case settings && writing:
		p.addf("a card either sets capabilities (needs, set) or gives a transform its options (transform), not both")
	case settings:
		t.Kind = KindSettings
		compileSettings(t, &f, vocab, &p)
	case writing:
		t.Kind = KindWriting
		compileWriting(t, &f, vocab, &p)
	default:
		p.addf("a card needs capabilities (needs, set) or a transform")
	}
	if err := p.err(); err != nil {
		if t.ID != "" {
			return nil, fmt.Errorf("%s: %w", t.ID, err)
		}
		return nil, err
	}
	return t, nil
}

func compileSettings(t *Technique, f *techniqueFile, vocab Vocabulary, p *problems) {
	if f.Options != nil {
		p.addf("options: only a writing card (transform) has options")
	}
	if len(f.Needs) == 0 {
		p.addf("needs: missing; list the capabilities in the order they go into the chain")
	}
	for _, name := range f.Needs {
		name = strings.TrimSpace(name)
		switch _, ok := vocab.Capability(name); {
		case !ok:
			p.addf("needs: %s", unknownCapability(vocab, name))
		case slices.Contains(t.Needs, name):
			p.addf("needs: %s twice", name)
		default:
			t.Needs = append(t.Needs, name)
		}
	}
	for _, name := range sortedKeys(f.Set) {
		_, known := vocab.Capability(name)
		switch {
		case !known && !slices.Contains(f.Needs, name):
			p.addf("set: %s", unknownCapability(vocab, name))
		case known && !slices.Contains(t.Needs, name):
			p.addf("set: %s is not in needs", name)
		}
	}
	for _, name := range sortedKeys(f.Precision) {
		if _, ok := f.Set[name]; !ok {
			p.addf("precision: %s is not set", name)
			continue
		}
		for _, param := range sortedKeys(f.Precision[name]) {
			if _, ok := f.Set[name][param]; !ok {
				p.addf("precision: %s.%s is not set", name, param)
			}
		}
	}
	for _, name := range t.Needs {
		values, ok := f.Set[name]
		if !ok {
			continue
		}
		c, _ := vocab.Capability(name)
		s := Setting{Capability: name}
		for _, param := range sortedKeys(values) {
			if _, ok := c.Param(param); !ok {
				p.addf("set: %s has no parameter %q (it has %s)", name, param, strings.Join(c.ParamNames(), ", "))
			}
		}
		for _, cp := range c.Params {
			raw, ok := values[cp.Name]
			if !ok {
				continue
			}
			v, err := compileValue(raw, cp, SongVars())
			if err != nil {
				p.addf("set: %s.%s: %v", name, cp.Name, err)
				continue
			}
			pv := ParamValue{Param: cp.Name, Value: v, Unit: cp.Unit}
			if rawP, ok := f.Precision[name][cp.Name]; ok {
				prec, err := parsePrecision(rawP, cp)
				switch c, isConst := v.Const(); {
				case err != nil:
					p.addf("precision: %s.%s: %v", name, cp.Name, err)
				case prec.Percent > 0 && isConst && c == 0:
					p.addf("precision: %s.%s: a share of 0 is 0; give an amount in %s", name, cp.Name, unitOr(cp.Unit))
				default:
					pv.Precision = &prec
				}
			}
			s.Values = append(s.Values, pv)
		}
		t.Settings = append(t.Settings, s)
	}
	place, err := parsePlace(f.Place, t.Needs, vocab)
	if err != nil {
		p.addf("place: %v", err)
	} else {
		t.Place = &place
	}
	if f.Verify != nil {
		v, err := compileVerify(f.Verify)
		if err != nil {
			p.addf("verify: %v", err)
		} else {
			t.Verify = v
		}
	}
}

func compileWriting(t *Technique, f *techniqueFile, vocab Vocabulary, p *problems) {
	if f.Precision != nil || f.Place != "" || f.Verify != nil {
		p.addf("precision, place and verify belong to a settings card; a writing card has a transform and its options")
	}
	t.Transform = strings.TrimSpace(f.Transform)
	tr, ok := vocab.Transforms[t.Transform]
	if !ok {
		known := "there are none yet"
		if names := vocab.TransformNames(); len(names) > 0 {
			known = "known: " + strings.Join(names, ", ")
		}
		p.addf("transform: %q is not a transform (%s)", t.Transform, known)
		return
	}
	for _, name := range sortedKeys(f.Options) {
		if _, ok := tr.Option(name); !ok {
			var names []string
			for _, o := range tr.Options {
				names = append(names, o.Name)
			}
			p.addf("options: %s has no option %q (it has %s)", tr.Name, name, strings.Join(names, ", "))
		}
	}
	for _, o := range tr.Options {
		raw, ok := f.Options[o.Name]
		if !ok {
			continue
		}
		v, err := compileValue(raw, o, SongVars())
		if err != nil {
			p.addf("options: %s: %v", o.Name, err)
			continue
		}
		t.Options = append(t.Options, OptionValue{Option: o.Name, Value: v})
	}
}

func unknownCapability(vocab Vocabulary, name string) string {
	return fmt.Sprintf("%q is not a capability (known: %s)", name, strings.Join(vocab.CapabilityNames(), ", "))
}

// parsePrecision reads "10%" (a share of the value) or an amount in the
// parameter's unit.
func parsePrecision(raw any, cp Param) (Precision, error) {
	if cp.Enumerated() {
		return Precision{}, fmt.Errorf("%s takes a label; a label is exact", cp.Name)
	}
	if s, ok := raw.(string); ok && strings.HasSuffix(strings.TrimSpace(s), "%") {
		pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%")), 64)
		if err != nil || pct <= 0 || pct >= 100 {
			return Precision{}, fmt.Errorf("%q is not a share between 0%% and 100%%", s)
		}
		return Precision{Percent: pct}, nil
	}
	e, err := compileNumber(raw, nil)
	if err != nil {
		return Precision{}, fmt.Errorf("write a share (\"10%%\") or an amount in %s: %v", unitOr(cp.Unit), err)
	}
	v, _ := e.Const()
	if v <= 0 {
		return Precision{}, fmt.Errorf("%s is not above 0", e)
	}
	return Precision{Amount: v}, nil
}

func unitOr(unit string) string {
	if unit == "" {
		return "its unit"
	}
	return unit
}

// parsePlace reads first, last (the default), "before <capability>" or "after
// <capability>".
func parsePlace(s string, needs []string, vocab Vocabulary) (Place, error) {
	fields := strings.Fields(s)
	switch {
	case len(fields) == 0:
		return Place{Where: "last"}, nil
	case len(fields) == 1 && (fields[0] == "first" || fields[0] == "last"):
		return Place{Where: fields[0]}, nil
	case len(fields) == 2 && (fields[0] == "before" || fields[0] == "after"):
		if _, ok := vocab.Capability(fields[1]); !ok {
			return Place{}, errors.New(unknownCapability(vocab, fields[1]))
		}
		if slices.Contains(needs, fields[1]) {
			return Place{}, fmt.Errorf("%s is one of the card's own capabilities", fields[1])
		}
		return Place{Where: fields[0], Capability: fields[1]}, nil
	default:
		return Place{}, fmt.Errorf("%q is not first, last, \"before <capability>\" or \"after <capability>\"", s)
	}
}

func compileVerify(f *verifyFile) (*Verify, error) {
	m := Measurement(strings.TrimSpace(f.Measure))
	if !m.Known() {
		names := make([]string, 0, len(measurements))
		for _, k := range Measurements() {
			names = append(names, string(k))
		}
		return nil, fmt.Errorf("measure: %q is not a measurement (%s)", f.Measure, strings.Join(names, ", "))
	}
	v := &Verify{Measure: m, Unit: m.Unit()}
	switch use := measurements[m].band; {
	case len(f.Band) == 0 && use == bandRequired:
		return nil, fmt.Errorf("band: %s needs a band, [from, to] in Hz", m)
	case len(f.Band) > 0 && use == bandNone:
		return nil, fmt.Errorf("band: %s reads no band", m)
	case len(f.Band) > 0 && len(f.Band) != 2:
		return nil, fmt.Errorf("band: write [from, to] in Hz")
	case len(f.Band) == 2:
		for _, raw := range f.Band {
			e, err := compileNumber(raw, SongVars())
			if err != nil {
				return nil, fmt.Errorf("band: %v", err)
			}
			v.Band = append(v.Band, e)
		}
		lo, okLo := v.Band[0].Const()
		hi, okHi := v.Band[1].Const()
		if (okLo && lo <= 0) || (okLo && okHi && lo >= hi) {
			return nil, fmt.Errorf("band: %s..%s Hz is not a band", v.Band[0], v.Band[1])
		}
	}
	var set []string
	for _, e := range []struct {
		name string
		raw  any
	}{{expectFalls, f.Falls}, {expectRises, f.Rises}, {expectAtMost, f.AtMost}, {expectAtLeast, f.AtLeast}} {
		if e.raw == nil {
			continue
		}
		set = append(set, e.name)
		x, err := compileNumber(e.raw, SongVars())
		if err != nil {
			return nil, fmt.Errorf("%s: %v", e.name, err)
		}
		if c, ok := x.Const(); ok && c <= 0 && (e.name == expectFalls || e.name == expectRises) {
			return nil, fmt.Errorf("%s: %s is not above 0", e.name, x)
		}
		v.Expect, v.Value = e.name, x
	}
	if len(set) != 1 {
		return nil, fmt.Errorf("say what the measurement has to show: exactly one of falls, rises, at_most, at_least")
	}
	return v, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Vars lists the song values the card's values use, sorted: what has to be
// known of the song before the card can be applied.
func (t *Technique) Vars() []string {
	seen := map[string]bool{}
	add := func(e *Expr) {
		if e == nil {
			return
		}
		for _, v := range e.Vars() {
			seen[v] = true
		}
	}
	for _, s := range t.Settings {
		for _, pv := range s.Values {
			add(pv.Value.Expr)
		}
	}
	for _, o := range t.Options {
		add(o.Value.Expr)
	}
	if t.Verify != nil {
		for _, b := range t.Verify.Band {
			add(b)
		}
		add(t.Verify.Value)
	}
	return sortedKeys(seen)
}

// Setting returns what the card sets on a capability.
func (t *Technique) Setting(capability string) (Setting, bool) {
	for _, s := range t.Settings {
		if s.Capability == capability {
			return s, true
		}
	}
	return Setting{}, false
}
