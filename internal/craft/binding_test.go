package craft

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/teemow/midi-device/device"
)

// limiterDump is a probe dump of a limiter whose parameters a card names.
func limiterDump(subtype string, ceilingMin float64) device.ProbeDump {
	return device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: subtype, Manufacturer: "Test"},
		Name:      "Limiter " + subtype,
		Parameters: []device.ProbeParam{
			{Address: 0, Identifier: "ceiling", DisplayName: "Ceiling", Min: ceilingMin, Max: 0, UnitName: "dB", Writable: true},
			{Address: 1, Identifier: "gain", DisplayName: "Gain", Min: 0, Max: 24, UnitName: "dB", Writable: true},
			{Address: 2, Identifier: "release", DisplayName: "Release", Min: 1, Max: 1000, UnitName: "ms", Writable: true},
		},
		FactoryPresets: []device.ProbePreset{{Number: 0, Name: "Transparent"}},
	}
}

func bindingLibrary(t *testing.T, rig fstest.MapFS) *Library {
	t.Helper()
	lib, err := LoadFrom(Shipped(), rig, Default())
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestBindPicksTheFirstPluginThatCanCarryIt(t *testing.T) {
	lib := bindingLibrary(t, fstest.MapFS{
		"plugins/a.yaml": {Data: limiterCard("aufx/LimA/Test", "Alpha Limiter")},
		"plugins/b.yaml": {Data: limiterCard("aufx/LimB/Test", "Beta Limiter")},
		"prefer.yaml":    {Data: []byte("dynamics.limiter: [aufx/LimA/Test, aufx/LimB/Test]\n")},
	})
	ceiling, _ := lib.Technique("mastering.ceiling")
	rig := Rig{limiterDump("LimB", -30)} // the preferred one is not on the rig

	bindings := lib.Bind(ceiling, rig)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %+v", bindings)
	}
	b := bindings[0]
	if b.Capability != "dynamics.limiter" || b.Plugin != "aufx/LimB/Test" || b.Missing != "" || len(b.Candidates) != 2 {
		t.Fatalf("binding = %+v", b)
	}
	alpha, beta := b.Candidates[0], b.Candidates[1]
	if alpha.Preferred != 1 || alpha.Probe != "" || len(alpha.Problems) != 1 || !strings.Contains(alpha.Problems[0], "not on this rig") {
		t.Errorf("alpha = %+v", alpha)
	}
	if beta.Preferred != 2 || beta.Probe != device.ProbeID(rig[0]) || len(beta.Problems) != 0 {
		t.Errorf("beta = %+v", beta)
	}
	bound, ok := b.Bound()
	if !ok || bound.Name != "Beta Limiter" {
		t.Errorf("bound = %+v", bound)
	}
	if len(beta.Params) != 3 {
		t.Fatalf("params = %+v", beta.Params)
	}
	c := beta.Params[0]
	if c.Name != "ceiling" || c.Param != "Ceiling" || c.Unit != "dB" || c.Step != 3.0/127 {
		t.Errorf("ceiling = %+v", c)
	}
	if g := beta.Params[1]; g.Name != "gain" || g.Step != 24.0/127 {
		t.Errorf("gain over the parameter's whole range = %+v", g)
	}
}

func TestBindReportsWhyAPluginCannot(t *testing.T) {
	lib := bindingLibrary(t, fstest.MapFS{
		// A CC over 30 dB moves the ceiling in steps of 0.24 dB: too coarse
		// for the card's 0.1 dB.
		"plugins/wide.yaml": {Data: []byte(`
plugin: aufx/Wide/Test
name: Wide
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, range: [-30, 0]}
      gain: Gain
      release: Release
`)},
		// It names parameters its plugin does not have, and a range beyond
		// the parameter's own.
		"plugins/typo.yaml": {Data: []byte(`
plugin: aufx/Typo/Test
name: Typo
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, range: [-60, 0]}
      gain: Input Gain
    enable: {Mode: Modern}
`)},
		// It holds the release where the card wants another.
		"plugins/fixed.yaml": {Data: []byte(`
plugin: aufx/Fixd/Test
name: Fixed
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, range: [-3, 0]}
      gain: Gain
      release: {fixed: 50}
`)},
		// It takes its release in seconds, and its parameter starts at 1.
		"plugins/seconds.yaml": {Data: []byte(`
plugin: aufx/Secs/Test
name: Seconds
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, range: [-3, 0]}
      gain: Gain
      release: {param: Release, value: "release / 1000"}
`)},
		"plugins/preset.yaml": {Data: []byte(`
plugin: aufx/Prst/Test
name: Preset
capabilities:
  dynamics.limiter:
    write: preset
    preset: Loud
    params:
      ceiling: {fixed: -1}
`)},
	})
	ceiling, _ := lib.Technique("mastering.ceiling")
	rig := Rig{limiterDump("Wide", -30), limiterDump("Typo", -30), limiterDump("Fixd", -30), limiterDump("Secs", -30), limiterDump("Prst", -30)}
	b := lib.Bind(ceiling, rig)[0]
	if b.Plugin != "" || !strings.Contains(b.Missing, "none of the 5 plugins") || len(b.Candidates) != 5 {
		t.Fatalf("binding = %+v", b)
	}
	want := map[string][]string{
		"Wide":    {"too coarse for -1 within 0.1; narrow the mapping range"},
		"Typo":    {"the mapping range -60..0 lies outside Ceiling's range -30..0", `gain: the probe dump has no writable parameter "Input Gain"`, "cannot set release", `enable Mode: the probe dump has no writable parameter "Mode"`},
		"Fixed":   {"holds release at 50 ms; the technique sets 100"},
		"Seconds": {"release: 100 comes to 0.1 at the plugin, outside the mapping range 1..1000"},
		"Preset":  {"cannot set gain", `has no factory preset "Loud"`},
	}
	for _, c := range b.Candidates {
		for _, w := range want[c.Name] {
			found := false
			for _, p := range c.Problems {
				found = found || strings.Contains(p, w)
			}
			if !found {
				t.Errorf("%s: problems %q lack %q", c.Name, c.Problems, w)
			}
		}
	}
}

func TestBindWithoutCards(t *testing.T) {
	lib := bindingLibrary(t, nil)
	glue, _ := lib.Technique("mastering.glue")
	b := lib.Bind(glue, nil)
	if len(b) != 1 || b[0].Plugin != "" || !strings.Contains(b[0].Missing, "no plugin card provides dynamics.compressor") {
		t.Errorf("bindings = %+v", b)
	}
}
