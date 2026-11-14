package craft

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/teemow/midi-device/device"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// applySong is a song with a pad, whose chain has a limiter, and a master
// chain with a limiter.
const applySong = `title: Apply Song
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: aumu-_synt-_test}
    effects:
      - {id: lim, plugin: aufx-_lim_a-_test, params: {Ceiling: -1}, range: {Ceiling: [-3, 0]}}
master:
  - {id: limiter, plugin: aufx-_lim_a-_test, params: {Ceiling: -1}, range: {Ceiling: [-3, 0]}}
patterns:
  a: {step: 1/4, notes: "A1 - - -"}
sections:
  verse: {bars: 1, play: {pad: a}}
  chorus: {bars: 1, scene: wide, play: {pad: a}}
scenes:
  wide: {pad: {level: -2}}
arrangement: [verse, chorus]
`

func applyRig() (Rig, song.Catalog) {
	synth := device.ProbeDump{
		Component:  device.ProbeComponent{Type: "aumu", Subtype: "Synt", Manufacturer: "Test"},
		Name:       "Synth",
		Parameters: []device.ProbeParam{{Address: 0, Identifier: "cut", DisplayName: "Cutoff", Max: 1, Writable: true}},
	}
	rig := Rig{hpDump("Fits", false), limiterDump("LimA", -30), synth}
	cat := song.Catalog{}
	for _, d := range rig {
		cat[device.ProbeID(d)] = d
	}
	return rig, cat
}

func applyLibrary(t *testing.T) *Library {
	t.Helper()
	return bindingLibrary(t, fstest.MapFS{
		"plugins/fits.yaml": {Data: hpCard("Fits", "{param: Frequency, range: [15, 60]}")},
		"plugins/lim.yaml":  {Data: limiterCard("aufx/LimA/Test", "Alpha Limiter")},
		"techniques/mixing.cut.yaml": {Data: []byte(`id: mixing.cut
craft: mixing
title: Cut the rumble in a section
applies_to: [track, section]
needs: [eq.highpass]
set:
  eq.highpass: {freq: 40, slope: 12}
place: before dynamics.limiter
`)},
	})
}

// patched applies a's patch to the song and plans the result.
func patched(t *testing.T, doc string, a *Applied, cat song.Catalog) (string, *song.Plan) {
	t.Helper()
	pj, err := json.Marshal(a.Patch)
	if err != nil {
		t.Fatal(err)
	}
	out, err := song.Patch([]byte(doc), pj)
	if err != nil {
		t.Fatalf("patch %s: %v", pj, err)
	}
	sg, err := song.Parse(out)
	if err != nil {
		t.Fatalf("parse:\n%s\n%v", out, err)
	}
	p, err := song.NewPlan(sg, cat)
	if err != nil {
		t.Fatalf("plan:\n%s\n%v", out, err)
	}
	return string(out), p
}

func masterChain() []ChainEntry {
	return []ChainEntry{{ID: "limiter", Plugin: "aufx/LimA/Test"}}
}

func TestApplyToTheMaster(t *testing.T) {
	lib := applyLibrary(t)
	rig, cat := applyRig()
	hp, _ := lib.Technique("mastering.subsonic_highpass")
	env := map[string]float64{VarLowestNoteHz: 55}
	a, err := lib.Apply(hp, Target{Track: song.MasterTrack, Chain: masterChain()}, rig, env)
	if err != nil {
		t.Fatal(err)
	}
	pj, _ := json.Marshal(a.Patch)
	// 0.5 * 55 Hz is 27.5, within 20..35.
	want := `{"master":{"subsonic_highpass":{"params":{"Frequency":27.5,"Shape":"Low Cut","Slope":24},"place":"first","plugin":"aufx-_fits-_test","range":{"Frequency":[15,60]}}}}`
	if string(pj) != want {
		t.Errorf("patch\n got %s\nwant %s", pj, want)
	}
	doc, p := patched(t, applySong, a, cat)
	if !strings.Contains(doc, "master:\n  - id: subsonic_highpass") {
		t.Errorf("the high-pass is not first on the master:\n%s", doc)
	}
	if n := p.Node(song.MasterTrack, "subsonic_highpass"); n == nil {
		t.Error("the plan has no node for the high-pass")
	}
	if !strings.Contains(strings.Join(a.Why, "\n"), `freq 27.5 Hz: "Frequency" = 27.5 Hz (clamp(0.5 * lowest_note_hz, 20, 35)), mapped 15..60`) {
		t.Errorf("why: %q", a.Why)
	}
	joined := strings.Join(a.Checks, "\n")
	if !strings.Contains(joined, "precision: eq.highpass freq within 20%") || !strings.Contains(joined, "verify: band_level 10..20 Hz falls by 6 dB or more (not run") {
		t.Errorf("checks: %q", a.Checks)
	}

	// Applied again, it is set where it is.
	chain := append([]ChainEntry{{ID: "subsonic_highpass", Plugin: "aufx/Fits/Test"}}, masterChain()...)
	again, err := lib.Apply(hp, Target{Track: song.MasterTrack, Chain: chain}, rig, map[string]float64{VarLowestNoteHz: 60})
	if err != nil {
		t.Fatal(err)
	}
	if e := again.Patch["master"].(map[string]any)["subsonic_highpass"].(map[string]any); e["place"] != nil {
		t.Errorf("an effect that is there is placed again: %v", e)
	}
	if !strings.HasPrefix(again.Why[0], "sets master subsonic_highpass") {
		t.Errorf("why: %q", again.Why)
	}
}

func TestApplyPlacesByCapability(t *testing.T) {
	lib := applyLibrary(t)
	rig, cat := applyRig()
	cut, _ := lib.Technique("mixing.cut")
	chain := []ChainEntry{{ID: "lim", Plugin: "aufx/LimA/Test"}}
	a, err := lib.Apply(cut, Target{Track: "pad", Chain: chain}, rig, map[string]float64{})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := patched(t, applySong, a, cat)
	if i, j := strings.Index(doc, "id: cut"), strings.Index(doc, "id: lim,"); i < 0 || j < 0 || i > j {
		t.Errorf("cut is not before the limiter:\n%s", doc)
	}
	// Without a limiter in the chain, before it is last.
	b, _ := lib.Apply(cut, Target{Track: "pad"}, rig, map[string]float64{})
	if pl := b.Patch["tracks"].(map[string]any)["pad"].(map[string]any)["effects"].(map[string]any)["cut"].(map[string]any)["place"]; pl != "last" {
		t.Errorf("place = %v, want last", pl)
	}
}

func TestApplyToASection(t *testing.T) {
	lib := applyLibrary(t)
	rig, cat := applyRig()
	cut, _ := lib.Technique("mixing.cut")
	a, err := lib.Apply(cut, Target{Track: "pad", Section: "chorus", Scenes: []string{"wide"}}, rig, map[string]float64{})
	if err != nil {
		t.Fatal(err)
	}
	doc, p := patched(t, applySong, a, cat)
	for _, want := range []string{"bypass: true", "mixing_cut:", `scene: ["wide", "mixing_cut"]`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the song lacks %q:\n%s", want, doc)
		}
	}
	if p.Node("pad", "cut") == nil {
		t.Error("the plan has no node for the cut")
	}

	// Applied to the whole track afterwards, it plays everywhere: the
	// section's bypass is dropped.
	chain := []ChainEntry{{ID: "lim", Plugin: "aufx/LimA/Test"}, {ID: "cut", Plugin: "aufx/Fits/Test"}}
	all, err := lib.Apply(cut, Target{Track: "pad", Chain: chain}, rig, map[string]float64{})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := patched(t, doc, all, cat); strings.Contains(again, "bypass: true") {
		t.Errorf("applied to the whole track, the cut stays bypassed:\n%s", again)
	}
}

func TestApplyRefuses(t *testing.T) {
	lib := applyLibrary(t)
	rig, _ := applyRig()
	hp, _ := lib.Technique("mastering.subsonic_highpass")
	glue, _ := lib.Technique("mastering.glue")
	cut, _ := lib.Technique("mixing.cut")
	for name, tc := range map[string]struct {
		t      *Technique
		target Target
		env    map[string]float64
		want   string
	}{
		"a master card on a track":   {hp, Target{Track: "pad"}, map[string]float64{VarLowestNoteHz: 55}, "applies to master, not to a track"},
		"no lowest note":             {hp, Target{Track: song.MasterTrack}, map[string]float64{}, "follows the song's lowest_note_hz, which the master does not tell"},
		"no plugin card":             {glue, Target{Track: song.MasterTrack}, map[string]float64{VarBeatMS: 500}, "dynamics.compressor: no plugin card provides dynamics.compressor"},
		"a track card on the master": {cut, Target{Track: song.MasterTrack}, nil, "applies to track, section, not to the master"},
	} {
		if _, err := lib.Apply(tc.t, tc.target, rig, tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
