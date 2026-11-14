package craft

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
)

// highpassCard is the design's example of a settings card.
const highpassCard = `
id: mixing.highpass_non_bass
craft: mixing
title: High-pass every track that is not bass
intent: Leave the low end to the kick and the bass.
when: A track whose role is not bass has energy below its lowest note.
when_not:
  - A sub pad or an 808 that is meant to fill the low end.
  - Low strings in an orchestral piece.
applies_to: track
needs: [eq.highpass]
set:
  eq.highpass:
    freq: "clamp(0.7 * lowest_note_hz, 40, 250)"
    slope: 12
precision:
  eq.highpass: {freq: "10%"}
place: first
verify:
  measure: band_level
  band: [20, 80]
  falls: 6
`

func TestParseSettingsCard(t *testing.T) {
	tc, err := ParseTechnique([]byte(highpassCard), Default())
	if err != nil {
		t.Fatal(err)
	}
	if tc.Kind != KindSettings || tc.Craft != "mixing" || !slices.Equal(tc.AppliesTo, []string{"track"}) {
		t.Fatalf("card = %+v", tc)
	}
	if len(tc.WhenNot) != 2 || !slices.Equal(tc.Needs, []string{"eq.highpass"}) {
		t.Fatalf("when_not %v, needs %v", tc.WhenNot, tc.Needs)
	}
	s, ok := tc.Setting("eq.highpass")
	if !ok || len(s.Values) != 2 {
		t.Fatalf("setting = %+v", s)
	}
	freq, slope := s.Values[0], s.Values[1]
	if freq.Param != "freq" || freq.Unit != UnitHz || freq.Precision == nil || freq.Precision.Percent != 10 {
		t.Errorf("freq = %+v", freq)
	}
	if slope.Param != "slope" || slope.Precision != nil {
		t.Errorf("slope = %+v", slope)
	}
	if v, ok := slope.Value.Const(); !ok || v != 12 {
		t.Errorf("slope = %v", slope.Value)
	}
	cp, _ := Default().Capabilities["eq.highpass"].Param("freq")
	got, err := freq.Value.Eval(cp, map[string]float64{VarLowestNoteHz: 110})
	if err != nil || math.Abs(got-77) > 1e-9 {
		t.Errorf("freq for a lowest note of 110 Hz = %g, %v", got, err)
	}
	if tc.Place == nil || tc.Place.String() != "first" {
		t.Errorf("place = %v", tc.Place)
	}
	if tc.Verify == nil || tc.Verify.Measure != MeasureBandLevel || tc.Verify.Expect != "falls" || tc.Verify.Unit != UnitDB {
		t.Fatalf("verify = %+v", tc.Verify)
	}
	if got := tc.Verify.String(); got != "band_level 20..80 Hz falls by 6 dB or more" {
		t.Errorf("verify reads %q", got)
	}
	if got := tc.Vars(); !slices.Equal(got, []string{VarLowestNoteHz}) {
		t.Errorf("vars = %v", got)
	}

	b, err := json.Marshal(tc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"value":"clamp(0.7 * lowest_note_hz, 40, 250)"`, `"value":12`, `"precision":"10%"`, `"kind":"settings"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json lacks %s:\n%s", want, b)
		}
	}
}

func TestParseSettingsCardDefaults(t *testing.T) {
	tc, err := ParseTechnique([]byte(`
id: mastering.level
craft: mastering
title: A level
applies_to: [master, track]
needs: [level.gain, stereo.mono_bass]
set:
  level.gain: {gain: -3}
precision:
  level.gain: {gain: 0.5}
`), Default())
	if err != nil {
		t.Fatal(err)
	}
	if tc.Place.String() != "last" {
		t.Errorf("place defaults to %v", tc.Place)
	}
	if len(tc.Settings) != 1 || tc.Settings[0].Values[0].Precision.Amount != 0.5 {
		t.Errorf("settings = %+v", tc.Settings)
	}
	if !slices.Equal(tc.AppliesTo, []string{"master", "track"}) || !slices.Equal(tc.Needs, []string{"level.gain", "stereo.mono_bass"}) {
		t.Errorf("applies_to %v, needs %v", tc.AppliesTo, tc.Needs)
	}
	if tc.Verify != nil {
		t.Errorf("verify = %+v", tc.Verify)
	}
}

func TestParseTechniqueErrors(t *testing.T) {
	base := func(extra string) string {
		return "id: mastering.x\ncraft: mastering\ntitle: X\napplies_to: master\n" + extra
	}
	for _, tc := range []struct {
		name, card string
		want       []string
	}{
		{"unknown capability", base("needs: [eq.tilt]\n"), []string{`"eq.tilt" is not a capability`}},
		{"unknown parameter", base("needs: [eq.highpass]\nset:\n  eq.highpass: {frq: 30}\n"), []string{`eq.highpass has no parameter "frq" (it has freq, slope)`}},
		{"out of range", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: 5}\n"), []string{"5 is outside freq's range 10..2000 Hz"}},
		{"constant that does not compute", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: \"100 / (2 - 2)\"}\n"), []string{"division by zero"}},
		{"unknown song value", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: \"0.7 * lowest_note\"}\n"), []string{"lowest_note is not known here"}},
		{"set not in needs", base("needs: [eq.highpass]\nset:\n  eq.lowpass: {freq: 5000}\n"), []string{"eq.lowpass is not in needs"}},
		{"unknown capability in set", base("needs: [eq.highpass]\nset:\n  eq.tilt: {gain: 1}\n"), []string{`set: "eq.tilt" is not a capability`}},
		{"precision of what is not set", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: 30}\nprecision:\n  eq.highpass: {slope: 1}\n"), []string{"precision: eq.highpass.slope is not set"}},
		{"bad precision", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: 30}\nprecision:\n  eq.highpass: {freq: \"150%\"}\n"), []string{"not a share between"}},
		{"negative precision", base("needs: [eq.highpass]\nset:\n  eq.highpass: {freq: 30}\nprecision:\n  eq.highpass: {freq: -1}\n"), []string{"is not above 0"}},
		{"bad place", base("needs: [eq.highpass]\nplace: middle\n"), []string{`"middle" is not first, last`}},
		{"place by an unknown capability", base("needs: [eq.highpass]\nplace: after eq.tilt\n"), []string{`place: "eq.tilt" is not a capability`}},
		{"place by its own capability", base("needs: [eq.highpass]\nplace: after eq.highpass\n"), []string{"one of the card's own"}},
		{"unknown measurement", base("needs: [eq.highpass]\nverify: {measure: rumble, falls: 3}\n"), []string{`"rumble" is not a measurement`}},
		{"band missing", base("needs: [eq.highpass]\nverify: {measure: band_level, falls: 3}\n"), []string{"band_level needs a band"}},
		{"band where none is read", base("needs: [eq.highpass]\nverify: {measure: true_peak, band: [1, 2], at_most: -1}\n"), []string{"true_peak reads no band"}},
		{"band upside down", base("needs: [eq.highpass]\nverify: {measure: band_level, band: [80, 20], falls: 3}\n"), []string{"is not a band"}},
		{"two expectations", base("needs: [eq.highpass]\nverify: {measure: true_peak, at_most: -1, at_least: -3}\n"), []string{"exactly one of"}},
		{"no expectation", base("needs: [eq.highpass]\nverify: {measure: true_peak}\n"), []string{"exactly one of"}},
		{"falls by nothing", base("needs: [eq.highpass]\nverify: {measure: crest_factor, falls: 0}\n"), []string{"falls: 0 is not above 0"}},
		{"unknown field", base("needs: [eq.highpass]\nroles: [eq]\n"), []string{"field roles not found"}},
		{"unknown craft", "id: mix.x\ncraft: mix\ntitle: X\napplies_to: track\nneeds: [eq.highpass]\n", []string{`"mix" is not a craft`}},
		{"id not of its craft", "id: mixing.x\ncraft: mastering\ntitle: X\napplies_to: track\nneeds: [eq.highpass]\n", []string{"does not start with its craft"}},
		{"bad id", "id: X\ncraft: mastering\ntitle: X\napplies_to: track\nneeds: [eq.highpass]\n", []string{"is not <craft>.<name>"}},
		{"missing id, title, applies_to", "craft: mastering\nneeds: [eq.highpass]\n", []string{"id: missing", "title: missing", "applies_to: missing"}},
		{"both kinds", base("needs: [eq.highpass]\ntransform: lock_bass\n"), []string{"not both"}},
		{"neither kind", base(""), []string{"needs capabilities (needs, set) or a transform"}},
		{"unknown transform", base("transform: lock_bass\n"), []string{`"lock_bass" is not a transform (there are none yet)`}},
		{"writing with a place", base("transform: lock_bass\nplace: first\n"), []string{"belong to a settings card"}},
		{"settings with options", base("needs: [eq.highpass]\noptions: {x: 1}\n"), []string{"only a writing card"}},
		{"two cards", base("needs: [eq.highpass]\n---\nid: mastering.y\n"), []string{"one card per file"}},
		{"empty", "", []string{"is empty"}},
	} {
		_, err := ParseTechnique([]byte(tc.card), Default())
		if err == nil {
			t.Errorf("%s: loaded", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", tc.name, err, w)
			}
		}
	}

	_, err := ParseTechnique([]byte("id: mastering.x\ncraft: mastering\ntitle: X\napplies_to: [master, desk]\nneeds: [eq.highpass]\n"), Default())
	if err == nil || !strings.Contains(err.Error(), `applies_to: "desk" is not one of track, section, master, song`) {
		t.Errorf("unknown applies_to: %v", err)
	}

	_, err = ParseTechnique([]byte("id: mastering.x\ncraft: mastering\ntitle: X\ntargets: master\nneeds: [eq.highpass]\n"), Default())
	if err == nil || !strings.Contains(err.Error(), "targets: renamed to applies_to") {
		t.Errorf("the old field name: %v", err)
	}
}

// writingVocab is the daemon's vocabulary with one transform, so a writing
// card can be read before the first real transform lands.
func writingVocab() Vocabulary {
	v := Default()
	v.Transforms["lock_bass"] = Transform{
		Name:  "lock_bass",
		Title: "Move the bass onto the kick",
		Options: []Param{
			{Name: "amount", Unit: UnitPercent, Min: 0, Max: 100, Doc: "how many notes move"},
			{Name: "feel", Values: []string{"straight", "pushed", "laid_back"}, Doc: "where the bass sits"},
		},
	}
	return v
}

func TestParseWritingCard(t *testing.T) {
	card := `
id: composition.lock_bass_to_kick
craft: composition
title: Lock the bass to the kick
applies_to: [section, song]
transform: lock_bass
options:
  amount: "clamp(section.energy * 10, 20, 90)"
  feel: Pushed
`
	tc, err := ParseTechnique([]byte(card), writingVocab())
	if err != nil {
		t.Fatal(err)
	}
	if tc.Kind != KindWriting || tc.Transform != "lock_bass" || len(tc.Options) != 2 {
		t.Fatalf("card = %+v", tc)
	}
	if tc.Options[0].Option != "amount" || tc.Options[1].Value.Label != "pushed" {
		t.Errorf("options = %+v", tc.Options)
	}
	if got := tc.Vars(); !slices.Equal(got, []string{VarSectionEnergy}) {
		t.Errorf("vars = %v", got)
	}
	if tc.Place != nil || len(tc.Needs) != 0 {
		t.Errorf("a writing card has no place or needs: %+v", tc)
	}

	for _, bad := range []struct{ options, want string }{
		{"{amount: 150}", "outside amount's range"},
		{"{feel: sideways}", `"sideways" is not a value of feel`},
		{"{feel: 3}", "feel takes one of"},
		{"{swing: 3}", `lock_bass has no option "swing"`},
	} {
		_, err := ParseTechnique([]byte("id: composition.x\ncraft: composition\ntitle: X\napplies_to: song\ntransform: lock_bass\noptions: "+bad.options+"\n"), writingVocab())
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("options %s: %v, want %q", bad.options, err, bad.want)
		}
	}
	_, err = ParseTechnique([]byte("id: composition.x\ncraft: composition\ntitle: X\napplies_to: song\ntransform: voice\n"), writingVocab())
	if err == nil || !strings.Contains(err.Error(), "known: lock_bass") {
		t.Errorf("unknown transform: %v", err)
	}
}

func TestParseTechniqueReportsEverything(t *testing.T) {
	_, err := ParseTechnique([]byte(`
id: mastering.x
craft: mastering
title: X
applies_to: master
needs: [eq.highpass, dynamics.limiter]
set:
  eq.highpass: {freq: 1, sloap: 12}
  dynamics.limiter: {ceiling: "-1 + tempoo"}
place: sideways
`), Default())
	if err == nil {
		t.Fatal("loaded")
	}
	for _, want := range []string{"mastering.x:", "sloap", "1 is outside freq's range", "tempoo is not known here", "sideways"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
