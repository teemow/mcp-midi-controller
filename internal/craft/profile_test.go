package craft

import (
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

// appleHighPass is AUHighPassFilter's probe dump as the rig has it.
var appleHighPass = device.ProbeDump{
	Component: device.ProbeComponent{Type: "aufx", Subtype: "hpas", Manufacturer: "appl"},
	Name:      "AUHighPassFilter",
	Parameters: []device.ProbeParam{
		{Address: 0, Identifier: "0", DisplayName: "Cutoff Frequency", Min: 10, Max: 22050, UnitName: "Hz", Writable: true, DisplayLogarithmic: true},
		{Address: 1, Identifier: "1", DisplayName: "Resonance", Min: -20, Max: 40, UnitName: "dB", Writable: true},
	},
}

// aCompressor has its times in seconds and its mix as a share of 1.
var aCompressor = device.ProbeDump{
	Component: device.ProbeComponent{Type: "aufx", Subtype: "Cmp1", Manufacturer: "Test"},
	Name:      "Test Compressor",
	Parameters: []device.ProbeParam{
		{Address: 0, Identifier: "thr", DisplayName: "Threshold", Min: -60, Max: 0, UnitName: "dB", Writable: true},
		{Address: 1, Identifier: "rat", DisplayName: "Ratio", Min: 1, Max: 30, Writable: true},
		{Address: 2, Identifier: "att", DisplayName: "Attack Time", Min: 0.0001, Max: 1, UnitName: "Seconds", Writable: true},
		{Address: 3, Identifier: "rel", DisplayName: "Release Time", Min: 0.01, Max: 3, UnitName: "Seconds", Writable: true},
		{Address: 4, Identifier: "mku", DisplayName: "Makeup Gain", Min: 0, Max: 40, UnitName: "dB", Writable: true},
		{Address: 5, Identifier: "mode", DisplayName: "Mode", Max: 2, Writable: true, ValueStrings: []string{"A", "B", "C"}},
	},
}

func TestPropose(t *testing.T) {
	vocab := Default()
	hp := Propose(appleHighPass, vocab)
	if got := hp.CapabilityNames(); len(got) != 1 || got[0] != "eq.highpass" {
		t.Fatalf("AUHighPassFilter proposes %v, want eq.highpass alone", got)
	}
	// The capability's 10..2000 Hz is the mapping range; the resonance is
	// not a slope.
	if f := hp.Capabilities["eq.highpass"].Params["freq"]; f.Param != "Cutoff Frequency" || len(f.Range) != 2 || f.Range[0] != 10 || f.Range[1] != 2000 {
		t.Errorf("freq = %+v", f)
	}
	if _, ok := hp.Capabilities["eq.highpass"].Params["slope"]; ok {
		t.Error("the resonance was taken for a slope")
	}

	cmp := Propose(aCompressor, vocab)
	pv, ok := cmp.Capabilities["dynamics.compressor"]
	if !ok {
		t.Fatalf("Test Compressor proposes %v", cmp.CapabilityNames())
	}
	for name, want := range map[string]ParamSpec{
		"threshold": {Param: "Threshold"},
		"ratio":     {Param: "Ratio", Range: []float64{1, 20}},
		"attack":    {Param: "Attack Time", Value: "attack / 1000", Range: []float64{0.0001, 0.5}},
		"release":   {Param: "Release Time", Value: "release / 1000"},
		"makeup":    {Param: "Makeup Gain", Range: []float64{0, 30}},
	} {
		got := pv.Params[name]
		if got.Param != want.Param || got.Value != want.Value || len(got.Range) != len(want.Range) || (len(want.Range) == 2 && (got.Range[0] != want.Range[0] || got.Range[1] != want.Range[1])) {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	// What is proposed loads as a card.
	cmp.Measured = &Measured{At: "2026-11-14T12:00:00Z", By: "profile_plugin", Checks: []string{"dynamics.compressor swept threshold"}}
	data, err := cmp.Render(vocab)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	card, err := ParsePluginCard(data, vocab)
	if err != nil || card.Measured == nil || card.Measured.At == "" {
		t.Fatalf("the rendered card: %v, %+v\n%s", err, card, data)
	}
	prov, _ := card.Provision("dynamics.compressor")
	vals, err := prov.PluginValues(map[string]float64{"threshold": -20, "ratio": 4, "attack": 10, "release": 100, "makeup": 0})
	if err != nil || vals["Attack Time"] != 0.01 || vals["Release Time"] != 0.1 || vals["Threshold"] != -20 {
		t.Errorf("plugin values = %v, %v", vals, err)
	}
}

func TestSweepJudge(t *testing.T) {
	vocab := Default()
	hp, _ := vocab.Capability("eq.highpass")
	sw := SweepFor(hp)
	if sw.Param != "freq" || sw.Measure != MeasureBandLevel || sw.Moves != Falls || len(sw.Band) != 2 {
		t.Fatalf("sweep = %+v", sw)
	}
	if ok, why := sw.Judge([]float64{-30, -40, -52}); !ok {
		t.Errorf("a band that falls 22 dB: %s", why)
	}
	if ok, why := sw.Judge([]float64{-30, -31, -32}); ok || !strings.Contains(why, "has to move 6.00 or more") {
		t.Errorf("a band that hardly falls: %v %s", ok, why)
	}
	if ok, why := sw.Judge([]float64{-30, -20, -40}); ok || !strings.Contains(why, "moved back") {
		t.Errorf("a band that rises on the way: %v %s", ok, why)
	}
	lp, _ := vocab.Capability("eq.lowpass")
	if ok, why := SweepFor(lp).Judge([]float64{2000, 3500, 6000}); !ok {
		t.Errorf("a centroid that rises: %s", why)
	}
	for _, c := range Capabilities() {
		if sw := SweepFor(c); len(sw.Values) < 2 {
			t.Errorf("%s has no sweep", c.Name)
		}
	}
}
