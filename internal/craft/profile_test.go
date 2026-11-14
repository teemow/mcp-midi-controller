package craft

import (
	"slices"
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

// TestOverKeepsWhatWasNotSwept: a profile replaces the rig's earlier card of
// the plugin, and what that card says of the capabilities it did not sweep
// stays, with its notes.
func TestOverKeepsWhatWasNotSwept(t *testing.T) {
	vocab := Default()
	prev := []byte(`plugin: aufx/hpas/appl
name: AUHighPassFilter
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: {param: Cutoff Frequency, range: [10, 400]}
  eq.lowpass:
    write: cc
    params:
      freq: Cutoff Frequency
notes:
  - Its resonance peaks at the cutoff.
measured:
  at: "2026-11-01T10:00:00Z"
  by: profile_plugin
  checks:
    - "eq.highpass swept freq: band_level falls 2.00 dB"
    - "eq.lowpass swept freq: brightness rises 900.00 Hz"
`)
	if _, err := ParsePluginCard(prev, vocab); err != nil {
		t.Fatalf("the earlier card: %v", err)
	}
	now := Propose(appleHighPass, vocab)
	now.Notes = []string{"Profiled at the tap."}
	now.Measured = &Measured{At: "2026-11-14T12:00:00Z", By: "profile_plugin", Checks: []string{"eq.highpass swept freq: band_level falls 20.00 dB"}}
	data, kept, err := now.Over(prev, []string{"eq.highpass"}, vocab)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0] != "eq.lowpass" {
		t.Errorf("kept = %v, want eq.lowpass", kept)
	}
	card, err := ParsePluginCard(data, vocab)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if _, ok := card.Provision("eq.lowpass"); !ok {
		t.Errorf("the capability that was not swept is gone:\n%s", data)
	}
	hp, _ := card.Provision("eq.highpass")
	if m, _ := hp.Param("freq"); len(m.Range) != 2 || m.Range[1] != 2000 {
		t.Errorf("the swept capability is not the one measured now: %+v", m)
	}
	if !slices.Contains(card.Notes, "Its resonance peaks at the cutoff.") || !slices.Contains(card.Notes, "Profiled at the tap.") {
		t.Errorf("notes = %q", card.Notes)
	}
	want := []string{"eq.highpass swept freq: band_level falls 20.00 dB", "(2026-11-01T10:00:00Z) eq.lowpass swept freq: brightness rises 900.00 Hz"}
	if card.Measured == nil || card.Measured.At != "2026-11-14T12:00:00Z" || !slices.Equal(card.Measured.Checks, want) {
		t.Errorf("measured = %+v, want the checks %q", card.Measured, want)
	}

	// Without an earlier card it is the card as it renders.
	data, kept, err = now.Over(nil, []string{"eq.highpass"}, vocab)
	if err != nil || kept != nil {
		t.Fatalf("without an earlier card: %v, %v", kept, err)
	}
	if rendered, _ := now.Render(vocab); string(data) != string(rendered) {
		t.Errorf("without an earlier card:\n%s\nwant\n%s", data, rendered)
	}
}

// TestSteps: the step size of every parameter a card sets over CC, over its
// mapping range and along its travel.
func TestSteps(t *testing.T) {
	vocab := Default()
	for _, tc := range []struct {
		dump device.ProbeDump
		want string
	}{
		// 60 dB over a linear travel: 128 values 0.472 dB apart.
		{aCompressor, "dynamics.compressor threshold: a CC moves Threshold in steps of 0.472 dB at -60 to 0.472 dB at 0, along its linear travel over -60..0"},
		// A log travel: the step grows with the cutoff.
		{appleHighPass, "eq.highpass freq: a CC moves Cutoff Frequency in steps of "},
	} {
		data, err := Propose(tc.dump, vocab).Render(vocab)
		if err != nil {
			t.Fatal(err)
		}
		card, err := ParsePluginCard(data, vocab)
		if err != nil {
			t.Fatal(err)
		}
		steps := Steps(card, tc.dump)
		if !slices.ContainsFunc(steps, func(s string) bool { return strings.HasPrefix(s, tc.want) }) {
			t.Errorf("%s: steps = %q, want %q", tc.dump.Name, steps, tc.want)
		}
	}
	data, _ := Propose(appleHighPass, vocab).Render(vocab)
	card, _ := ParsePluginCard(data, vocab)
	if steps := Steps(card, appleHighPass); len(steps) != 1 || !strings.HasSuffix(steps[0], "along its log travel over 10..2000") {
		t.Errorf("AUHighPassFilter: steps = %q", steps)
	}
}
