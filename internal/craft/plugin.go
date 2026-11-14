package craft

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/teemow/midi-device/device"
	"gopkg.in/yaml.v3"
)

// Write paths: how a binding's value reaches a plugin.
const (
	// WriteCC is a Control Change through AUM's mapping of the parameter,
	// narrowed to the parameter's mapping range.
	WriteCC = "cc"
	// WriteState is a field of the plugin's saved state, where that is text.
	WriteState = "state"
	// WritePreset is a preset that holds the settings.
	WritePreset = "preset"
)

var writePaths = []string{WriteCC, WriteState, WritePreset}

// PluginCard says which capabilities one plugin provides and how each is set.
// The plugin is matched by its component tuple.
type PluginCard struct {
	// Plugin is the component tuple, "type/subtype/manufacturer".
	Plugin    string                `json:"plugin"`
	Component device.ProbeComponent `json:"-"`
	Name      string                `json:"name"`
	Provides  []Provision           `json:"capabilities"`
	Notes     []string              `json:"notes,omitempty"`
	Source    string                `json:"source"`
}

// Provision is how the plugin provides one capability.
type Provision struct {
	Capability string `json:"capability"`
	Write      string `json:"write"`
	// Preset is the preset that holds the settings (write: preset).
	Preset string `json:"preset,omitempty"`
	// Params map the capability's parameters, in its order, to the plugin's.
	Params []ParamMap `json:"params,omitempty"`
	// Enable are plugin parameters set to a fixed value for the capability to
	// work: a band switched on, its shape.
	Enable []Enable `json:"enable,omitempty"`
}

// Param returns how the provision sets a capability parameter.
func (p Provision) Param(name string) (ParamMap, bool) {
	for _, m := range p.Params {
		if m.Name == name {
			return m, true
		}
	}
	return ParamMap{}, false
}

// ParamMap is how one capability parameter reaches the plugin.
type ParamMap struct {
	// Name is the capability's parameter.
	Name string `json:"name"`
	// Param is the plugin's parameter, as its probe dump names it (write: cc).
	Param string `json:"param,omitempty"`
	// Field is the field of the plugin's saved state (write: state).
	Field string `json:"field,omitempty"`
	// Value converts the capability's value into the plugin's units; it may
	// use the capability's parameters by name. Nil: the same number.
	Value *Expr `json:"value,omitempty"`
	// Range is the mapping range, in the plugin's units: AUM's mapping of
	// the parameter is narrowed to it, so the 128 steps of a CC fall where
	// the capability is used.
	Range []float64 `json:"range,omitempty"`
	// Fixed is the value, in the capability's unit, at which the plugin holds
	// the parameter: it cannot be set.
	Fixed *float64 `json:"fixed,omitempty"`
}

// Enable is one plugin parameter set to a fixed value.
type Enable struct {
	Param string `json:"param"`
	Value string `json:"value"`
}

// pluginFile is a plugin card as written.
type pluginFile struct {
	Plugin       string                   `yaml:"plugin"`
	Name         string                   `yaml:"name"`
	Capabilities map[string]provisionFile `yaml:"capabilities"`
	Notes        stringList               `yaml:"notes"`
}

type provisionFile struct {
	Write  string                  `yaml:"write"`
	Preset string                  `yaml:"preset"`
	Params map[string]paramMapFile `yaml:"params"`
	Enable map[string]any          `yaml:"enable"`
}

var paramMapFields = []string{"param", "field", "value", "range", "fixed"}

type paramMapFile struct {
	Param string    `yaml:"param"`
	Field string    `yaml:"field"`
	Value any       `yaml:"value"`
	Range []float64 `yaml:"range"`
	Fixed any       `yaml:"fixed"`
}

// UnmarshalYAML also reads the short form, the plugin parameter's name alone.
func (m *paramMapFile) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		m.Param = n.Value
		return nil
	}
	// A node decodes without the card's check of unknown fields, so the
	// fields are checked here.
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i].Value; !slices.Contains(paramMapFields, k) {
				return fmt.Errorf("line %d: field %s is not one of %s", n.Content[i].Line, k, strings.Join(paramMapFields, ", "))
			}
		}
	}
	type plain paramMapFile
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*m = paramMapFile(p)
	return nil
}

// ParseComponent reads a component tuple, "type/subtype/manufacturer", each a
// four-character code.
func ParseComponent(s string) (device.ProbeComponent, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 3 {
		return device.ProbeComponent{}, fmt.Errorf("%q is not a component tuple type/subtype/manufacturer (aufx/abcd/Mnfr)", s)
	}
	for _, p := range parts {
		if len(p) != 4 {
			return device.ProbeComponent{}, fmt.Errorf("%q is not a component tuple: %q is not a four-character code", s, p)
		}
	}
	return device.ProbeComponent{Type: parts[0], Subtype: parts[1], Manufacturer: parts[2]}, nil
}

// ComponentTuple writes a component as a plugin card names it.
func ComponentTuple(c device.ProbeComponent) string {
	return c.Type + "/" + c.Subtype + "/" + c.Manufacturer
}

// ParsePluginCard reads one plugin card and checks it against the
// vocabulary. The error lists everything that is wrong with it.
func ParsePluginCard(data []byte, vocab Vocabulary) (*PluginCard, error) {
	var f pluginFile
	if err := decodeStrict(data, &f); err != nil {
		return nil, err
	}
	var p problems
	card := &PluginCard{Plugin: strings.TrimSpace(f.Plugin), Name: strings.TrimSpace(f.Name), Notes: f.Notes}
	if card.Plugin == "" {
		p.addf("plugin: missing; the component tuple type/subtype/manufacturer")
	} else if c, err := ParseComponent(card.Plugin); err != nil {
		p.addf("plugin: %v", err)
	} else {
		card.Component = c
	}
	if card.Name == "" {
		card.Name = card.Plugin
	}
	if len(f.Capabilities) == 0 {
		p.addf("capabilities: missing; say which capabilities the plugin provides")
	}
	for _, name := range sortedKeys(f.Capabilities) {
		c, ok := vocab.Capability(name)
		if !ok {
			p.addf("capabilities: %s", unknownCapability(vocab, name))
			continue
		}
		if pv, ok := compileProvision(c, f.Capabilities[name], &p); ok {
			card.Provides = append(card.Provides, pv)
		}
	}
	if err := p.err(); err != nil {
		if card.Plugin != "" {
			return nil, fmt.Errorf("%s: %w", card.Plugin, err)
		}
		return nil, err
	}
	return card, nil
}

func compileProvision(c Capability, f provisionFile, p *problems) (Provision, bool) {
	at := "capabilities." + c.Name
	before := len(*p)
	pv := Provision{Capability: c.Name, Write: strings.TrimSpace(f.Write), Preset: strings.TrimSpace(f.Preset)}
	switch {
	case pv.Write == "":
		p.addf("%s.write: missing (%s)", at, strings.Join(writePaths, ", "))
	case !slices.Contains(writePaths, pv.Write):
		p.addf("%s.write: %q is not a write path (%s)", at, pv.Write, strings.Join(writePaths, ", "))
	}
	switch {
	case pv.Write == WritePreset && pv.Preset == "":
		p.addf("%s.preset: missing; name the preset that holds the settings", at)
	case pv.Write != WritePreset && pv.Preset != "":
		p.addf("%s.preset: only write: preset names a preset", at)
	}
	if pv.Write == WritePreset && len(f.Enable) > 0 {
		p.addf("%s.enable: a preset holds its settings; write: preset enables nothing", at)
	}
	for _, name := range sortedKeys(f.Params) {
		if _, ok := c.Param(name); !ok {
			p.addf("%s.params: %s has no parameter %q (it has %s)", at, c.Name, name, strings.Join(c.ParamNames(), ", "))
		}
	}
	settable := 0
	for _, cp := range c.Params {
		mf, ok := f.Params[cp.Name]
		if !ok {
			continue
		}
		m, err := compileParamMap(c, cp, pv.Write, mf)
		if err != nil {
			p.addf("%s.params.%s: %v", at, cp.Name, err)
			continue
		}
		if m.Fixed == nil {
			settable++
		}
		pv.Params = append(pv.Params, m)
	}
	if (pv.Write == WriteCC || pv.Write == WriteState) && settable == 0 && len(*p) == before {
		p.addf("%s.params: map at least one of %s to the plugin's own", at, strings.Join(c.ParamNames(), ", "))
	}
	for _, name := range sortedKeys(f.Enable) {
		v, err := scalarString(f.Enable[name])
		if err != nil {
			p.addf("%s.enable.%s: %v", at, name, err)
			continue
		}
		pv.Enable = append(pv.Enable, Enable{Param: name, Value: v})
	}
	return pv, len(*p) == before
}

func compileParamMap(c Capability, cp Param, write string, f paramMapFile) (ParamMap, error) {
	m := ParamMap{Name: cp.Name, Param: strings.TrimSpace(f.Param), Field: strings.TrimSpace(f.Field)}
	if f.Fixed != nil {
		if m.Param != "" || m.Field != "" || f.Value != nil || f.Range != nil {
			return m, fmt.Errorf("a fixed parameter is not set; it has no param, field, value or range")
		}
		e, err := compileNumber(f.Fixed, nil)
		if err != nil {
			return m, fmt.Errorf("fixed: %v", err)
		}
		v, ok := e.Const()
		if !ok {
			return m, fmt.Errorf("fixed: %s is not a number", e)
		}
		if v < cp.Min || v > cp.Max {
			return m, fmt.Errorf("fixed: %g is outside %s's range %s", v, cp.Name, cp.Range())
		}
		m.Fixed = &v
		return m, nil
	}
	switch write {
	case WriteCC:
		if m.Param == "" {
			return m, fmt.Errorf("name the plugin's parameter (param), or say the value it is fixed at (fixed)")
		}
		if m.Field != "" {
			return m, fmt.Errorf("write: cc sets a parameter, not a field of the saved state")
		}
	case WriteState:
		if m.Field == "" {
			return m, fmt.Errorf("name the field of the saved state (field), or say the value it is fixed at (fixed)")
		}
		if m.Param != "" || f.Range != nil {
			return m, fmt.Errorf("write: state sets a field of the saved state; it has no param or mapping range")
		}
	case WritePreset:
		return m, fmt.Errorf("a preset holds its settings; say only the value it holds (fixed)")
	default:
		return m, nil // the write path's own problem is reported
	}
	if cp.Enumerated() && f.Value != nil {
		return m, fmt.Errorf("value: %s takes a label; it is passed on as it is", cp.Name)
	}
	if f.Value != nil {
		e, err := compileNumber(f.Value, c.ParamNames())
		if err != nil {
			return m, fmt.Errorf("value: %v", err)
		}
		m.Value = e
	}
	if f.Range != nil {
		if len(f.Range) != 2 || !finite(f.Range[0]) || !finite(f.Range[1]) || f.Range[0] >= f.Range[1] {
			return m, fmt.Errorf("range: write [low, high] in the plugin's units, two finite numbers, low below high")
		}
		m.Range = f.Range
	}
	return m, nil
}

// Provision returns how the card's plugin provides a capability.
func (c *PluginCard) Provision(capability string) (Provision, bool) {
	for _, p := range c.Provides {
		if p.Capability == capability {
			return p, true
		}
	}
	return Provision{}, false
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// rangeString renders a mapping range.
func rangeString(r []float64) string {
	if len(r) != 2 {
		return ""
	}
	return strconv.FormatFloat(r[0], 'g', -1, 64) + ".." + strconv.FormatFloat(r[1], 'g', -1, 64)
}
