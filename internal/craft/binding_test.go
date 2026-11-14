package craft

import (
	"math"
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

	bindings := lib.Bind(ceiling, rig, nil)
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
	if c.Name != "ceiling" || c.Param != "Ceiling" || c.Unit != "dB" || math.Abs(c.Step-3.0/127) > 1e-12 {
		t.Errorf("ceiling = %+v", c)
	}
	if g := beta.Params[1]; g.Name != "gain" || math.Abs(g.Step-24.0/127) > 1e-12 {
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
	b := lib.Bind(ceiling, rig, nil)[0]
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
	b := lib.Bind(glue, nil, nil)
	if len(b) != 1 || b[0].Plugin != "" || !strings.Contains(b[0].Missing, "no plugin card provides dynamics.compressor") {
		t.Errorf("bindings = %+v", b)
	}
}

// hpDump is a probe dump of a high-pass: a frequency of 10 Hz to 30 kHz,
// shown on a log scale when log is set, and a shape with labels.
func hpDump(subtype string, log bool) device.ProbeDump {
	return device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: subtype, Manufacturer: "Test"},
		Name:      "HP " + subtype,
		Parameters: []device.ProbeParam{
			{Address: 0, Identifier: "freq", DisplayName: "Frequency", Min: 10, Max: 30000, UnitName: "Hz", Writable: true, DisplayLogarithmic: log},
			{Address: 1, Identifier: "slope", DisplayName: "Slope", Min: 6, Max: 48, UnitName: "dB/oct", Writable: true},
			{Address: 2, Identifier: "shape", DisplayName: "Shape", Min: 0, Max: 2, Writable: true, ValueStrings: []string{"Bell", "Low Cut", "High Cut"}},
			// Octaves above 1 Hz, as Pro-Q 3 takes a band's frequency.
			{Address: 3, Identifier: "oct", DisplayName: "Octaves", Min: 3.321928, Max: 14.872674, Writable: true},
		},
	}
}

func hpCard(subtype, freq string) []byte {
	return []byte("plugin: aufx/" + subtype + "/Test\nname: HP " + subtype + `
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: ` + freq + `
      slope: Slope
    enable: {Shape: Low Cut}
`)
}

// TestBindFollowsTheSong checks a value that follows the song is checked over
// the span a song can give it, or at the song's own value once one is known:
// the subsonic high-pass's clamp(0.5 * lowest_note_hz, 20, 35) is 20..35 Hz.
func TestBindFollowsTheSong(t *testing.T) {
	lib := bindingLibrary(t, fstest.MapFS{
		"plugins/whole.yaml":  {Data: hpCard("Whol", "Frequency")},
		"plugins/narrow.yaml": {Data: hpCard("Narr", "{param: Frequency, range: [40, 250]}")},
		"plugins/fits.yaml":   {Data: hpCard("Fits", "{param: Frequency, range: [15, 60]}")},
		"plugins/log.yaml":    {Data: hpCard("Logs", "{param: Frequency, range: [15, 60]}")},
		"plugins/oct.yaml":    {Data: hpCard("Octs", `{param: Octaves, value: "log2(freq)", range: [3.9, 6]}`)},
		"prefer.yaml":         {Data: []byte("eq.highpass: [aufx/Whol/Test, aufx/Narr/Test, aufx/Fits/Test, aufx/Logs/Test, aufx/Octs/Test]\n")},
	})
	hp, _ := lib.Technique("mastering.subsonic_highpass")
	rig := Rig{hpDump("Whol", false), hpDump("Narr", false), hpDump("Fits", false), hpDump("Logs", true), hpDump("Octs", false)}
	b := lib.Bind(hp, rig, nil)[0]
	if b.Plugin != "aufx/Fits/Test" {
		t.Fatalf("bound %q, want the one whose mapping range holds 20..35 Hz finely enough: %+v", b.Plugin, b.Candidates)
	}
	byName := map[string]Candidate{}
	for _, c := range b.Candidates {
		byName[c.Name] = c
	}
	for name, want := range map[string]string{
		"HP Whol": "too coarse for clamp(0.5 * lowest_note_hz, 20, 35) within 20%",
		"HP Narr": "freq: 20..35 lies outside the mapping range 40..250",
	} {
		c := byName[name]
		if len(c.Problems) == 0 || !strings.Contains(strings.Join(c.Problems, "; "), want) {
			t.Errorf("%s: problems %q, want %q", name, c.Problems, want)
		}
	}
	if f := byName["HP Fits"].Params[0]; len(f.Want) != 2 || f.Want[0] != 20 || f.Want[1] != 35 {
		t.Errorf("Fits wants %v, want the span 20..35", f.Want)
	}
	// On a log travel the step grows with the value; the coarsest over
	// 20..35 Hz is at 35 Hz: 35·(60/15)^(1/127) - 35, coarser than a linear
	// travel's 45/127 Hz there.
	if l := byName["HP Logs"].Params[0]; len(byName["HP Logs"].Problems) > 0 || math.Abs(l.Step-35*(math.Pow(4, 1.0/127)-1)) > 1e-9 {
		t.Errorf("Logs freq = %+v", l)
	}
	// Octaves: 20..35 Hz is 4.32..5.13 octaves, and 20 % carried through
	// log2 is log2(1.2) = 0.26 octaves up and log2(1.25) = 0.32 down: the
	// tighter, up, is what the step is held to.
	o := byName["HP Octs"]
	if len(o.Problems) != 0 || o.Params[0].At[0] < 4.32 || o.Params[0].At[0] > 4.33 || math.Abs(o.Params[0].Within-math.Log2(1.2)) > 1e-9 {
		t.Errorf("Octs = %+v", o)
	}

	// With the song's lowest note known: 0.5 * 30 Hz is 15, clamped to 20.
	b = lib.Bind(hp, rig, map[string]float64{VarLowestNoteHz: 30})[0]
	if f := b.Candidates[2].Params[0]; b.Plugin != "aufx/Fits/Test" || f.Want[0] != 20 || f.Want[1] != 20 {
		t.Errorf("with a song: bound %q, freq %+v", b.Plugin, f)
	}
}

// TestBindConversions checks a conversion carries the precision with it and
// that a conversion that fails is a problem, not a plugin that binds.
func TestBindConversions(t *testing.T) {
	lib := bindingLibrary(t, fstest.MapFS{
		"plugins/idnt.yaml": {Data: []byte(`plugin: aufx/Idnt/Test
name: Identity
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, value: "ceiling", range: [-30, 0]}
      gain: Gain
      release: Release
`)},
		"plugins/divz.yaml": {Data: []byte(`plugin: aufx/Divz/Test
name: Divides
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, value: "ceiling / gain", range: [-3, 0]}
      gain: Gain
      release: Release
`)},
	})
	ceiling, _ := lib.Technique("mastering.ceiling")
	b := lib.Bind(ceiling, Rig{limiterDump("Idnt", -30), limiterDump("Divz", -30)}, nil)[0]
	if b.Plugin != "" {
		t.Fatalf("bound %q; neither can carry -1 dBFS within 0.1", b.Plugin)
	}
	for _, c := range b.Candidates {
		joined := strings.Join(c.Problems, "; ")
		switch c.Name {
		case "Identity":
			if !strings.Contains(joined, "too coarse for -1 within 0.1") {
				t.Errorf("Identity: %q", c.Problems)
			}
		case "Divides":
			if !strings.Contains(joined, "its conversion ceiling / gain at -1: division by zero") {
				t.Errorf("Divides: %q", c.Problems)
			}
		}
	}
	// The closest is the one whose only trouble is its step: 0.236 dB steps
	// against 0.1 dB, 1.18 times too coarse.
	if b.Closest != "aufx/Idnt/Test" || b.Off < 1.1 || b.Off > 1.25 || !strings.Contains(b.Missing, "the closest is Identity") {
		t.Errorf("closest %q off %g: %s", b.Closest, b.Off, b.Missing)
	}
}

// TestBindChecksEnable checks an enable value against the plugin's
// parameter.
func TestBindChecksEnable(t *testing.T) {
	lib := bindingLibrary(t, fstest.MapFS{
		"plugins/typo.yaml": {Data: []byte(strings.Replace(string(hpCard("Typo", "{param: Frequency, range: [15, 60]}")), "Low Cut", "Low Cutt", 1))},
	})
	hp, _ := lib.Technique("mastering.subsonic_highpass")
	b := lib.Bind(hp, Rig{hpDump("Typo", false)}, nil)[0]
	if b.Plugin != "" || len(b.Candidates) != 1 || !strings.Contains(strings.Join(b.Candidates[0].Problems, "; "), `enable Shape: "Low Cutt" is not a value of Shape`) {
		t.Errorf("binding = %+v", b)
	}
}

// TestCardsRefuseWhatCannotBeChecked checks the load refuses non-finite
// numbers and a share of a value of 0.
func TestCardsRefuseWhatCannotBeChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		dir, card, want string
	}{
		"nan range":  {"plugins", "plugin: aufx/Nann/Test\ncapabilities:\n  dynamics.limiter:\n    write: cc\n    params:\n      ceiling: {param: Ceiling, range: [.nan, 0]}\n", "two finite numbers"},
		"inf fixed":  {"plugins", "plugin: aufx/Infd/Test\ncapabilities:\n  dynamics.limiter:\n    write: cc\n    params:\n      ceiling: Ceiling\n      gain: {fixed: .inf}\n", "not a finite number"},
		"nan value":  {"techniques", "id: mastering.nan\ncraft: mastering\ntitle: x\napplies_to: master\nneeds: [dynamics.limiter]\nset:\n  dynamics.limiter: {ceiling: .nan}\n", "not a finite number"},
		"share of 0": {"techniques", "id: mastering.zero\ncraft: mastering\ntitle: x\napplies_to: master\nneeds: [level.gain]\nset:\n  level.gain: {gain: 0}\nprecision:\n  level.gain: {gain: \"10%\"}\n", "a share of 0 is 0"},
	} {
		_, err := LoadFrom(Shipped(), fstest.MapFS{tc.dir + "/x.yaml": {Data: []byte(tc.card)}}, Default())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// cutoffDump is a high-pass whose Cutoff Frequency runs from 10 Hz to
// 22050 Hz, shown on a log scale (as Apple's AUHighPassFilter shows it) or
// linearly.
func cutoffDump(subtype string, log bool) device.ProbeDump {
	return device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: subtype, Manufacturer: "Test"},
		Name:      "High-pass " + subtype,
		Parameters: []device.ProbeParam{
			{Address: 0, Identifier: "cutoff", DisplayName: "Cutoff Frequency", Min: 10, Max: 22050, UnitName: "Hz", Writable: true, DisplayLogarithmic: log},
		},
	}
}

// cutoffCard returns a plugin card that sets a high-pass's frequency over a
// mapping range, or over the parameter's whole range without one.
func cutoffCard(subtype, rng string) []byte {
	freq := `"Cutoff Frequency"`
	if rng != "" {
		freq = `{param: "Cutoff Frequency", range: ` + rng + `}`
	}
	return []byte("plugin: aufx/" + subtype + "/Test\nname: " + subtype + `
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: ` + freq + "\n")
}

// TestBindStepAlongALogScale checks a binding's CC step follows the scale the
// plugin shows the parameter on, as AUM moves it (song.Travel): on a log
// scale it is fine low in the range and coarse high in it, so 80 Hz within
// 5 % is in reach over the whole range, where a linear step of 174 Hz is not,
// and 1500 Hz within 1 % is out of reach over 100..2000 Hz, where a linear
// step of 15 Hz is fine.
func TestBindStepAlongALogScale(t *testing.T) {
	technique := func(id, freq, precision string) []byte {
		return []byte("id: mixing." + id + "\ncraft: mixing\ntitle: A high-pass at " + freq + ` Hz
applies_to: track
needs: [eq.highpass]
set:
  eq.highpass: {freq: ` + freq + `}
precision:
  eq.highpass: {freq: "` + precision + `"}
place: first
`)
	}
	lib := bindingLibrary(t, fstest.MapFS{
		"techniques/low.yaml":  {Data: technique("highpass_low", "80", "5%")},
		"techniques/high.yaml": {Data: technique("highpass_high", "1500", "1%")},
		"plugins/logw.yaml":    {Data: cutoffCard("LogW", "")},
		"plugins/linw.yaml":    {Data: cutoffCard("LinW", "")},
		"plugins/logn.yaml":    {Data: cutoffCard("LogN", "[100, 2000]")},
		"plugins/linn.yaml":    {Data: cutoffCard("LinN", "[100, 2000]")},
	})
	rig := Rig{cutoffDump("LogW", true), cutoffDump("LinW", false), cutoffDump("LogN", true), cutoffDump("LinN", false)}

	type want struct {
		step    [2]float64 // the step's bounds, Hz
		problem string     // "": it can carry it
	}
	for _, tc := range []struct {
		technique string
		plugin    string
		want      map[string]want
	}{
		{"mixing.highpass_low", "aufx/LogW/Test", map[string]want{
			// 80·(2205^(1/127) - 1)
			"LogW": {[2]float64{4.95, 5.05}, ""},
			"LinW": {[2]float64{22040.0 / 127, 22040.0 / 127}, "too coarse for 80 within 5%"},
			"LogN": {[2]float64{0, 1e9}, "80 lies outside the mapping range 100..2000"},
			"LinN": {[2]float64{0, 1e9}, "80 lies outside the mapping range 100..2000"},
		}},
		{"mixing.highpass_high", "aufx/LinN/Test", map[string]want{
			"LogW": {[2]float64{93.5, 94}, "too coarse for 1500 within 1%"},
			"LinW": {[2]float64{22040.0 / 127, 22040.0 / 127}, "too coarse for 1500 within 1%"},
			// 1500·(20^(1/127) - 1)
			"LogN": {[2]float64{35.7, 35.9}, "too coarse for 1500 within 1%"},
			"LinN": {[2]float64{1900.0 / 127, 1900.0 / 127}, ""},
		}},
	} {
		tech, ok := lib.Technique(tc.technique)
		if !ok {
			t.Fatalf("no technique %s", tc.technique)
		}
		b := lib.Bind(tech, rig, nil)[0]
		if b.Plugin != tc.plugin {
			t.Errorf("%s: bound to %q, want %q (%+v)", tc.technique, b.Plugin, tc.plugin, b.Candidates)
		}
		for _, c := range b.Candidates {
			w := tc.want[c.Name]
			if len(c.Params) != 1 {
				t.Errorf("%s on %s: params %+v", tc.technique, c.Name, c.Params)
				continue
			}
			if step := c.Params[0].Step; step < w.step[0] || step > w.step[1] || math.IsNaN(step) {
				t.Errorf("%s on %s: a CC step of %g Hz, want %g..%g", tc.technique, c.Name, step, w.step[0], w.step[1])
			}
			switch {
			case w.problem == "" && len(c.Problems) != 0:
				t.Errorf("%s on %s: problems %q, want none", tc.technique, c.Name, c.Problems)
			case w.problem != "" && (len(c.Problems) != 1 || !strings.Contains(c.Problems[0], w.problem)):
				t.Errorf("%s on %s: problems %q, want %q", tc.technique, c.Name, c.Problems, w.problem)
			}
		}
	}
}
