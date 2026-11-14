package craft

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

// eqCard is a plugin card for an EQ (made-up codes and parameter names).
const eqCard = `
plugin: aufx/Eq1x/Test
name: Test EQ
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: {param: "Band 1 Frequency", range: [10, 400]}
      slope: {fixed: 12}
    enable:
      "Band 1 Used": on
      "Band 1 Shape": Low Cut
  eq.high_shelf:
    write: cc
    params:
      freq: "Band 4 Frequency"
      gain: {param: "Band 4 Gain", value: "gain / 2", range: [-12, 12]}
notes:
  - Its saved state is not text; exact values come from a CC or a preset.
`

func TestParsePluginCard(t *testing.T) {
	c, err := ParsePluginCard([]byte(eqCard), Default())
	if err != nil {
		t.Fatal(err)
	}
	if c.Plugin != "aufx/Eq1x/Test" || c.Name != "Test EQ" || len(c.Notes) != 1 {
		t.Fatalf("card = %+v", c)
	}
	if c.Component != (device.ProbeComponent{Type: "aufx", Subtype: "Eq1x", Manufacturer: "Test"}) {
		t.Errorf("component = %+v", c.Component)
	}
	if len(c.Provides) != 2 || c.Provides[0].Capability != "eq.high_shelf" || c.Provides[1].Capability != "eq.highpass" {
		t.Fatalf("provides = %+v", c.Provides)
	}
	hp, _ := c.Provision("eq.highpass")
	freq, _ := hp.Param("freq")
	slope, _ := hp.Param("slope")
	if freq.Param != "Band 1 Frequency" || len(freq.Range) != 2 || freq.Range[1] != 400 || freq.Value != nil {
		t.Errorf("freq = %+v", freq)
	}
	if slope.Fixed == nil || *slope.Fixed != 12 || slope.Param != "" {
		t.Errorf("slope = %+v", slope)
	}
	if len(hp.Enable) != 2 || hp.Enable[0] != (Enable{Param: "Band 1 Shape", Value: "Low Cut"}) || hp.Enable[1] != (Enable{Param: "Band 1 Used", Value: "on"}) {
		t.Errorf("enable = %+v", hp.Enable)
	}
	shelf, _ := c.Provision("eq.high_shelf")
	f, _ := shelf.Param("freq")
	g, _ := shelf.Param("gain")
	if f.Param != "Band 4 Frequency" {
		t.Errorf("the short form: %+v", f)
	}
	if g.Value == nil || g.Value.String() != "gain / 2" {
		t.Fatalf("gain = %+v", g)
	}
	if v, err := g.Value.Eval(map[string]float64{"gain": 3}); err != nil || v != 1.5 {
		t.Errorf("gain 3 reaches the plugin as %g, %v", v, err)
	}
	if b, err := json.Marshal(c); err != nil || strings.Contains(string(b), "roles") || !strings.Contains(string(b), `"plugin":"aufx/Eq1x/Test"`) {
		t.Errorf("json = %s, %v", b, err)
	}
}

func TestParsePluginCardWritePaths(t *testing.T) {
	c, err := ParsePluginCard([]byte(`
plugin: aufx/Lim1/Test
capabilities:
  dynamics.limiter:
    write: state
    params:
      ceiling: {field: "params.ceiling", value: "ceiling * 10"}
      release: {field: "params.release"}
  color.saturation:
    write: preset
    preset: Warm Tape
    params:
      drive: {fixed: 6}
      mix: {fixed: 100}
`), Default())
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "aufx/Lim1/Test" {
		t.Errorf("a card without a name is named by its tuple: %q", c.Name)
	}
	lim, _ := c.Provision("dynamics.limiter")
	if p, _ := lim.Param("ceiling"); p.Field != "params.ceiling" || p.Value.String() != "ceiling * 10" {
		t.Errorf("ceiling = %+v", p)
	}
	sat, _ := c.Provision("color.saturation")
	if sat.Write != WritePreset || sat.Preset != "Warm Tape" || len(sat.Params) != 2 {
		t.Errorf("saturation = %+v", sat)
	}
}

func TestParsePluginCardErrors(t *testing.T) {
	head := "plugin: aufx/Eq1x/Test\ncapabilities:\n"
	for _, tc := range []struct{ name, card, want string }{
		{"roles", "plugin: aufx/Eq1x/Test\nroles: [eq]\ncapabilities:\n  eq.highpass: {write: cc, params: {freq: F}}\n", "field roles not found"},
		{"no plugin", "capabilities:\n  eq.highpass: {write: cc, params: {freq: F}}\n", "plugin: missing"},
		{"bad tuple", "plugin: Pro-Q 3\ncapabilities:\n  eq.highpass: {write: cc, params: {freq: F}}\n", "is not a component tuple"},
		{"long code", "plugin: aufx/Eq1xx/Test\ncapabilities:\n  eq.highpass: {write: cc, params: {freq: F}}\n", `"Eq1xx" is not a four-character code`},
		{"no capabilities", "plugin: aufx/Eq1x/Test\n", "capabilities: missing"},
		{"unknown capability", head + "  eq.tilt: {write: cc}\n", `"eq.tilt" is not a capability`},
		{"unknown parameter", head + "  eq.highpass: {write: cc, params: {frequency: F}}\n", `eq.highpass has no parameter "frequency"`},
		{"no write path", head + "  eq.highpass: {params: {freq: F}}\n", "write: missing"},
		{"bad write path", head + "  eq.highpass: {write: osc, params: {freq: F}}\n", `"osc" is not a write path`},
		{"cc without a parameter", head + "  eq.highpass: {write: cc, params: {freq: {range: [10, 400]}}}\n", "name the plugin's parameter"},
		{"cc with a field", head + "  eq.highpass: {write: cc, params: {freq: {param: F, field: f}}}\n", "not a field of the saved state"},
		{"state without a field", head + "  eq.highpass: {write: state, params: {freq: F}}\n", "name the field of the saved state"},
		{"state with a range", head + "  eq.highpass: {write: state, params: {freq: {field: f, range: [1, 2]}}}\n", "no param or mapping range"},
		{"preset without a name", head + "  eq.highpass: {write: preset, params: {freq: {fixed: 30}}}\n", "name the preset"},
		{"preset that sets", head + "  eq.highpass: {write: preset, preset: P, params: {freq: F}}\n", "say only the value it holds"},
		{"preset that enables", head + "  eq.highpass: {write: preset, preset: P, enable: {On: on}}\n", "enables nothing"},
		{"preset named elsewhere", head + "  eq.highpass: {write: cc, preset: P, params: {freq: F}}\n", "only write: preset names a preset"},
		{"nothing mapped", head + "  eq.highpass: {write: cc, params: {slope: {fixed: 12}}}\n", "map at least one of freq, slope"},
		{"bad range", head + "  eq.highpass: {write: cc, params: {freq: {param: F, range: [400, 10]}}}\n", "low below high"},
		{"fixed out of range", head + "  eq.highpass: {write: cc, params: {freq: F, slope: {fixed: 200}}}\n", "200 is outside slope's range"},
		{"fixed with a param", head + "  eq.highpass: {write: cc, params: {freq: F, slope: {fixed: 12, param: S}}}\n", "a fixed parameter is not set"},
		{"value by another capability's parameter", head + "  eq.highpass: {write: cc, params: {freq: {param: F, value: \"gain * 2\"}}}\n", "gain is not known here (known: freq, slope)"},
		{"value by a song value", head + "  eq.highpass: {write: cc, params: {freq: {param: F, value: \"tempo\"}}}\n", "tempo is not known here"},
		{"unknown field in a mapping", head + "  eq.highpass: {write: cc, params: {freq: {param: F, scale: 2}}}\n", "field scale is not one of"},
		{"empty enable", head + "  eq.highpass: {write: cc, params: {freq: F}, enable: {On: \"\"}}\n", "enable.On: empty value"},
	} {
		_, err := ParsePluginCard([]byte(tc.card), Default())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

func TestParseComponent(t *testing.T) {
	c, err := ParseComponent(" aumf/FQ3p/FabF ")
	if err != nil || c != (device.ProbeComponent{Type: "aumf", Subtype: "FQ3p", Manufacturer: "FabF"}) {
		t.Fatalf("component = %+v, %v", c, err)
	}
	if got := ComponentTuple(c); got != "aumf/FQ3p/FabF" {
		t.Errorf("tuple = %q", got)
	}
	for _, bad := range []string{"", "aufx/abcd", "aufx/abcd/Mnfr/x", "aufx//Mnfr", "aufx-abcd-Mnfr"} {
		if _, err := ParseComponent(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
