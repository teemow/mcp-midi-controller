package song

import (
	"slices"
	"strings"
	"testing"
)

func TestRevision(t *testing.T) {
	r1 := Revision([]byte(testSong))
	if len(r1) != 16 {
		t.Fatalf("Revision length = %d (%q), want 16 hex chars", len(r1), r1)
	}
	for _, ch := range r1 {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			t.Fatalf("Revision %q contains non-lowercase-hex char %q", r1, ch)
		}
	}
	if got := Revision([]byte(testSong)); got != r1 {
		t.Fatalf("Revision not deterministic: %q != %q", got, r1)
	}
	withComment := "# header comment\n" + testSong
	if r2 := Revision([]byte(withComment)); r2 == r1 {
		t.Fatal("Revision did not change when a comment was added to the stored text")
	}

	st := NewStore(t.TempDir())
	if _, err := st.Save("rev_check", []byte(withComment)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, loaded, err := st.Load("rev_check")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := Revision(loaded), Revision([]byte(withComment)); got != want {
		t.Fatalf("Revision after Load = %q, want %q", got, want)
	}
}

func TestPatchEmptyKeepsExactBytes(t *testing.T) {
	orig := "# top comment\n" + testSong
	out, err := Patch([]byte(orig), []byte(`{}`))
	if err != nil {
		t.Fatalf("Patch empty: %v", err)
	}
	if string(out) != orig || Revision(out) != Revision([]byte(orig)) {
		t.Fatalf("empty patch altered song bytes")
	}
}

// TestPatchADR0002Example exercises the ADR 0002 shape: insert an effect at the
// start of a chain, merge parameters on an existing effect, remove an effect
// with null, and replace lists of values (play list, arrangement) whole while
// keeping author comments intact.
func TestPatchADR0002Example(t *testing.T) {
	src := `# author comment that must survive patching
title: Night Drive
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: synth, preset: Warm Pad}
    effects:
      - {id: verb, plugin: reverb, params: {Mix: 10%, Size: 0.5}}
      - {id: chorus, plugin: delay, params: {Mix: 0.2}}
  - id: drums
    kind: drums
    instrument: {plugin: drums}
patterns:
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
  fill: {drums: {kick: "x.......x.......", snare: "....x.x.xxxxXXXX"}}
sections:
  verse1: {bars: 4, play: {pad: chords, drums: beat}}
  verse2: {bars: 4, play: {pad: chords, drums: [beat, beat]}}
arrangement: [verse1, verse2, verse1]
`
	patch := `
tracks:
  pad:
    effects:
      hp: {place: first, plugin: eq, params: {Freq: 80}}
      verb: {params: {Mix: 25%}}
      chorus: null
sections:
  verse2:
    play:
      drums: [beat, beat, beat, fill]
arrangement: [verse1, verse2]
`
	out, err := Patch([]byte(src), []byte(patch))
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !strings.Contains(string(out), "# author comment that must survive patching") {
		t.Errorf("comment lost after patch:\n%s", out)
	}
	sg := mustParse(t, string(out))
	var pad *Track
	for i := range sg.Tracks {
		if sg.Tracks[i].ID == "pad" {
			pad = &sg.Tracks[i]
		}
	}
	if pad == nil {
		t.Fatal("pad track missing")
	}
	gotFX := make([]string, len(pad.Effects))
	for i, fx := range pad.Effects {
		gotFX[i] = fx.EffectID()
	}
	if want := []string{"hp", "verb"}; !slices.Equal(gotFX, want) {
		t.Fatalf("pad effects = %v, want %v\n%s", gotFX, want, out)
	}
	if pad.Effects[0].ID != "hp" || pad.Effects[0].Plugin != "eq" || pad.Effects[0].Params["Freq"].Num != 80 {
		t.Errorf("hp effect = %+v", pad.Effects[0])
	}
	if pad.Effects[1].Params["Mix"].Str != "25%" || pad.Effects[1].Params["Size"].Num != 0.5 {
		t.Errorf("verb params = %+v, want Mix=25%% and untouched Size=0.5", pad.Effects[1].Params)
	}
	if got, want := []string(sg.Sections["verse2"].Play["drums"]), []string{"beat", "beat", "beat", "fill"}; !slices.Equal(got, want) {
		t.Errorf("verse2 drums play = %v, want %v", got, want)
	}
	if got, want := []string(sg.Sections["verse2"].Play["pad"]), []string{"chords"}; !slices.Equal(got, want) {
		t.Errorf("verse2 pad play = %v, want untouched %v", got, want)
	}
	if got, want := sg.Arrangement, []string{"verse1", "verse2"}; !slices.Equal(got, want) {
		t.Errorf("arrangement = %v, want %v", got, want)
	}
}

// TestPatchPlacementsAndChaining checks JSON patches where alphabetical keys
// list dependent entries ("comp" after "hp") before their anchors ("hp" first),
// plus before, map-form place ({after: ...}) and moving an existing entry.
func TestPatchPlacementsAndChaining(t *testing.T) {
	jsonPatch := `{
		"master": {
			"air": {"place": {"before": "comp"}, "plugin": "eq_air"},
			"comp": {"place": "after: hp", "plugin": "bus_comp"},
			"glue": {"place": "last", "bypass": false},
			"hp": {"place": "first", "plugin": "sub_hp"},
			"lim": {"after": "glue", "plugin": "limiter"}
		}
	}`
	out, err := Patch([]byte(testSong), []byte(jsonPatch))
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	sg := mustParse(t, string(out))
	got := make([]string, len(sg.Master))
	for i, fx := range sg.Master {
		got[i] = fx.EffectID()
	}
	want := []string{"hp", "air", "comp", "glue", "lim"}
	if !slices.Equal(got, want) {
		t.Fatalf("master chain = %v, want %v\n%s", got, want, out)
	}
	if sg.Master[3].Bypass == nil || *sg.Master[3].Bypass {
		t.Errorf("moved glue bypass = %v, want false", sg.Master[3].Bypass)
	}
}

// TestPatchSeveralFirstAndLastKeepPatchOrder checks that several new entries
// placed "first" (or "last") in one patch keep the patch's key order, that an
// entry placed before one of them in the same patch stays before it, and that
// an entry chained after one of them stays right after it even when a later
// key places another entry "first" (or "last").
func TestPatchSeveralFirstAndLastKeepPatchOrder(t *testing.T) {
	jsonPatch := `{
		"master": {
			"hp": {"place": "first", "plugin": "sub_hp"},
			"comp": {"place": "after: hp", "plugin": "bus_comp"},
			"trim": {"place": "before: hp", "plugin": "gain"},
			"air": {"place": "first", "plugin": "eq_air"},
			"lim": {"place": "last", "plugin": "limiter"},
			"tail": {"place": "after: lim", "plugin": "dither"},
			"clip": {"place": "last", "plugin": "clipper"}
		}
	}`
	out, err := Patch([]byte(testSong), []byte(jsonPatch))
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	sg := mustParse(t, string(out))
	got := make([]string, len(sg.Master))
	for i, fx := range sg.Master {
		got[i] = fx.EffectID()
	}
	want := []string{"trim", "hp", "comp", "air", "glue", "lim", "tail", "clip"}
	if !slices.Equal(got, want) {
		t.Fatalf("master chain = %v, want %v\n%s", got, want, out)
	}
}

// TestPatchTracksModulatorsMasterAndStems checks every id-keyed list in a song:
// tracks (including adding a new track whose own effects and modulators are
// written as id-keyed maps), effects addressed by default plugin id, modulators
// and stem strips.
func TestPatchTracksModulatorsMasterAndStems(t *testing.T) {
	patch := `
tracks:
  pad:
    place: "after: bass"
    group: synths
    instrument: {plugin: synth, preset: Warm Pad, params: {Cutoff: 0.4}}
    effects:
      hp: {place: first, plugin: eq}
      reverb: {plugin: reverb, params: {Mix: 0.3}}
    modulators:
      lfo1:
        plugin: LFOH
        target: {param: Cutoff}
        depth: [20%, 80%]
  lead:
    group: synths
    effects:
      delay: {params: {Time: 375}}
  bass:
    level: null
master:
  glue: null
scenes:
  chorus:
    master: null
stems:
  synths:
    level: -2
    effects:
      bus_eq: {place: first, plugin: eq}
      bus_comp: {place: "after: bus_eq", plugin: comp}
`
	out, err := Patch([]byte(testSong), []byte(patch))
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	sg := mustParse(t, string(out))

	trackIDs := make([]string, len(sg.Tracks))
	for i, tr := range sg.Tracks {
		trackIDs[i] = tr.ID
	}
	if want := []string{"bass", "pad", "lead", "drums"}; !slices.Equal(trackIDs, want) {
		t.Fatalf("tracks = %v, want %v\n%s", trackIDs, want, out)
	}
	if sg.Tracks[0].Level != nil {
		t.Errorf("bass level = %v, want nil after null patch", *sg.Tracks[0].Level)
	}

	pad := sg.Tracks[1]
	if len(pad.Effects) != 2 || pad.Effects[0].EffectID() != "hp" || pad.Effects[1].EffectID() != "reverb" {
		t.Fatalf("new track pad effects = %+v", pad.Effects)
	}
	if pad.Effects[1].ID != "" {
		t.Errorf("effect whose key matches plugin got redundant id %q", pad.Effects[1].ID)
	}
	if len(pad.Modulators) != 1 || pad.Modulators[0].ModulatorID() != "lfo1" || len(pad.Modulators[0].Depth) != 2 {
		t.Fatalf("new track pad modulators = %+v", pad.Modulators)
	}

	// Update the modulator's depth (whole-list replacement) and add a second one
	// before it, then remove the first.
	patch2 := `
tracks:
  pad:
    modulators:
      lfo1: {depth: [10%, 90%]}
      lfo2: {place: "before: lfo1", plugin: LFOH, target: {effect: reverb, param: Mix}}
stems:
  synths:
    effects:
      bus_eq: null
`
	out2, err := Patch(out, []byte(patch2))
	if err != nil {
		t.Fatalf("Patch 2: %v", err)
	}
	sg2 := mustParse(t, string(out2))
	mods := sg2.Tracks[1].Modulators
	if len(mods) != 2 || mods[0].ModulatorID() != "lfo2" || mods[1].ModulatorID() != "lfo1" {
		t.Fatalf("pad modulators = %+v, want [lfo2, lfo1]", mods)
	}
	if mods[1].Depth[0].Str != "10%" || mods[1].Depth[1].Str != "90%" {
		t.Errorf("lfo1 depth = %+v, want [10%%, 90%%]", mods[1].Depth)
	}
	if fx := sg2.StemStrips["synths"].Effects; len(fx) != 1 || fx[0].EffectID() != "bus_comp" {
		t.Errorf("synths stem effects = %+v, want [bus_comp]", fx)
	}
	if len(sg2.Master) != 0 {
		t.Errorf("master = %+v, want empty after removing glue", sg2.Master)
	}

	// Lead's first effect had no explicit id (defaulted to plugin "delay"):
	// swapping its plugin under key "delay" preserves id "delay".
	lead := sg2.Tracks[2]
	if lead.Effects[0].EffectID() != "delay" || lead.Effects[0].Params["Time"].Num != 375 {
		t.Fatalf("lead delay effect = %+v", lead.Effects[0])
	}
	out3, err := Patch(out2, []byte(`tracks: {lead: {effects: {delay: {plugin: tape_delay}}}}`))
	if err != nil {
		t.Fatalf("Patch 3: %v", err)
	}
	sg3 := mustParse(t, string(out3))
	if fx := sg3.Tracks[2].Effects[0]; fx.ID != "delay" || fx.Plugin != "tape_delay" {
		t.Errorf("swapped plugin on unnamed effect = %+v, want id=delay plugin=tape_delay", fx)
	}
}

func TestPatchErrors(t *testing.T) {
	cases := map[string]struct {
		patch string
		want  string
	}{
		"non-mapping root": {
			patch: `- a`,
			want:  `parse patch: expected a mapping object at the root`,
		},
		"tracks given as list": {
			patch: `tracks: [{id: bass}]`,
			want:  `/tracks: in a patch, tracks are keyed by id`,
		},
		"effects given as list": {
			patch: `tracks: {bass: {effects: [{plugin: delay}]}}`,
			want:  `/tracks/bass/effects: in a patch, effects are keyed by id`,
		},
		"modulators given as list": {
			patch: `tracks: {bass: {modulators: [{plugin: LFOH}]}}`,
			want:  `/tracks/bass/modulators: in a patch, modulators are keyed by id`,
		},
		"master given as list": {
			patch: `master: [{plugin: limiter}]`,
			want:  `/master: in a patch, master effects are keyed by id`,
		},
		"unknown anchor": {
			patch: `tracks: {bass: {effects: {hp: {place: "after: ghost", plugin: eq}}}}`,
			want:  `/tracks/bass/effects/hp/place: no entry "ghost" (have echo)`,
		},
		"anchor removed in same patch": {
			patch: `tracks: {bass: {effects: {echo: null, hp: {place: "after: echo", plugin: eq}}}}`,
			want:  `/tracks/bass/effects/hp/place: no entry "echo" (have none)`,
		},
		"self anchor": {
			patch: `tracks: {bass: {effects: {hp: {place: "after: hp", plugin: eq}}}}`,
			want:  `/tracks/bass/effects/hp/place: "hp" cannot be placed relative to itself`,
		},
		"circular placement": {
			patch: `tracks: {bass: {effects: {a: {place: "after: b", plugin: eq}, b: {place: "after: a", plugin: eq}}}}`,
			want:  `circular placement involving "a" and "b"`,
		},
		"invalid place word": {
			patch: `tracks: {bass: {effects: {hp: {place: middle, plugin: eq}}}}`,
			want:  `is not "first", "last", "after: <id>" or "before: <id>"`,
		},
		"mismatched new id": {
			patch: `tracks: {bass: {effects: {hp: {id: lp, plugin: eq}}}}`,
			want:  `/tracks/bass/effects/hp/id: "lp" does not match patch key "hp"`,
		},
		"rename existing track id": {
			patch: `tracks: {bass: {id: sub}}`,
			want:  `/tracks/bass/id: cannot change id "bass" to "sub"`,
		},
		"dangling scene reference caught by Parse": {
			patch: `tracks: {bass: {effects: {echo: null}}}`,
			want:  `/scenes/chorus/bass/effects/echo: "bass" has no effect "echo"`,
		},
		"unknown field caught by Parse": {
			patch: `tracks: {bass: {bogus: 1}}`,
			want:  `field bogus not found`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Patch([]byte(testSong), []byte(tc.patch))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Patch(%s) error = %v, want substring %q", name, err, tc.want)
			}
		})
	}
}

// aliasSong shares values through YAML anchors, aliases and a merge key: the
// chorus scene takes the bass's params, the lead's instrument merges the
// bass's, and the lead's first effect is the bass's echo.
const aliasSong = `title: Alias Song
tempo: 120
tracks:
  - id: bass
    instrument: &synth {plugin: synth, preset: Deep Bass, params: &bass_params {Cutoff: 0.25, Res: 0.1}}
    effects:
      - &echo {id: echo, plugin: delay, params: {Mix: 0.2}}
  - id: lead
    instrument: {<<: *synth, preset: Warm Pad}
    effects:
      - *echo # the bass's echo
      - {id: space, plugin: reverb}
patterns:
  bass_a: {step: 1/8, notes: "A1 . A1 . A1 . A1 ."}
scenes:
  chorus:
    bass:
      params: *bass_params
sections:
  verse: {bars: 1, play: {bass: bass_a}}
  chorus: {bars: 1, scene: chorus, play: {bass: bass_a}}
arrangement: [verse, chorus]
`

// TestPatchThroughAliases checks that a patch changes only the place it names
// when the song shares values through anchors, aliases and merge keys.
func TestPatchThroughAliases(t *testing.T) {
	mustParse(t, aliasSong)

	params := func(m map[string]Value) [2]float64 { return [2]float64{m["Cutoff"].Num, m["Res"].Num} }
	track := func(t *testing.T, sg *Song, id string) Track {
		t.Helper()
		for _, tr := range sg.Tracks {
			if tr.ID == id {
				return tr
			}
		}
		t.Fatalf("no track %q", id)
		return Track{}
	}
	effect := func(t *testing.T, tr Track, id string) Plugin {
		t.Helper()
		for _, fx := range tr.Effects {
			if fx.EffectID() == id {
				return fx
			}
		}
		t.Fatalf("track %q has no effect %q (%+v)", tr.ID, id, tr.Effects)
		return Plugin{}
	}
	// unchanged checks the values the patch does not name.
	type want struct {
		bass, lead, chorus [2]float64
		bassMix, leadMix   float64
	}
	orig := want{bass: [2]float64{0.25, 0.1}, lead: [2]float64{0.25, 0.1}, chorus: [2]float64{0.25, 0.1}, bassMix: 0.2, leadMix: 0.2}
	check := func(t *testing.T, sg *Song, w want) {
		t.Helper()
		bass, lead := track(t, sg, "bass"), track(t, sg, "lead")
		if got := params(bass.Instrument.Params); got != w.bass {
			t.Errorf("bass params = %v, want %v", got, w.bass)
		}
		if got := params(lead.Instrument.Params); got != w.lead {
			t.Errorf("lead params = %v, want %v", got, w.lead)
		}
		if got := params(sg.Scenes["chorus"]["bass"].Params); got != w.chorus {
			t.Errorf("chorus bass params = %v, want %v", got, w.chorus)
		}
		if lead.Instrument.Plugin != "synth" || lead.Instrument.Preset != "Warm Pad" {
			t.Errorf("lead instrument = %+v, want the merged synth with its own preset", lead.Instrument)
		}
		if w.bassMix != 0 {
			if got := effect(t, bass, "echo").Params["Mix"].Num; got != w.bassMix {
				t.Errorf("bass echo Mix = %v, want %v", got, w.bassMix)
			}
		}
		if fx := effect(t, lead, "echo"); fx.Plugin != "delay" || fx.Params["Mix"].Num != w.leadMix {
			t.Errorf("lead echo = %+v, want delay with Mix %v", fx, w.leadMix)
		}
	}

	cases := []struct {
		name  string
		patch string
		want  func(w want) want
		then  func(t *testing.T, sg *Song, out string)
	}{
		{
			name:  "a scene's aliased params change there only",
			patch: `scenes: {chorus: {bass: {params: {Cutoff: 0.9}}}}`,
			want:  func(w want) want { w.chorus = [2]float64{0.9, 0.1}; return w },
			then: func(t *testing.T, _ *Song, out string) {
				if !strings.Contains(out, "*echo") || !strings.Contains(out, "&bass_params") {
					t.Errorf("anchors and aliases the patch does not reach were not kept:\n%s", out)
				}
			},
		},
		{
			name:  "an anchored value changes, its aliases keep theirs",
			patch: `tracks: {bass: {instrument: {params: {Cutoff: 0.5}}}}`,
			want:  func(w want) want { w.bass = [2]float64{0.5, 0.1}; return w },
		},
		{
			name:  "an aliased effect entry changes there only",
			patch: `tracks: {lead: {effects: {echo: {params: {Mix: 0.5}}}}}`,
			want:  func(w want) want { w.leadMix = 0.5; return w },
		},
		{
			name:  "an anchored effect entry changes, its alias keeps its value",
			patch: `tracks: {bass: {effects: {echo: {params: {Mix: 0.3}}}}}`,
			want:  func(w want) want { w.bassMix = 0.3; return w },
		},
		{
			name:  "an anchored effect entry removed leaves its alias a copy",
			patch: `tracks: {bass: {effects: {echo: null}}}`,
			want:  func(w want) want { w.bassMix = 0; return w },
			then: func(t *testing.T, sg *Song, _ string) {
				if fx := track(t, sg, "bass").Effects; len(fx) != 0 {
					t.Errorf("bass effects = %+v, want none", fx)
				}
			},
		},
		{
			name:  "a track moved before the anchor its alias names",
			patch: `tracks: {lead: {place: first}}`,
			want:  func(w want) want { return w },
			then: func(t *testing.T, sg *Song, _ string) {
				if sg.Tracks[0].ID != "lead" {
					t.Errorf("tracks start with %q, want lead", sg.Tracks[0].ID)
				}
			},
		},
		{
			name:  "an entry moved next to an alias leaves the alias",
			patch: `tracks: {lead: {effects: {space: {place: first}}}}`,
			want:  func(w want) want { return w },
			then: func(t *testing.T, sg *Song, out string) {
				if fx := track(t, sg, "lead").Effects; len(fx) != 2 || fx[0].EffectID() != "space" || fx[1].EffectID() != "echo" {
					t.Errorf("lead effects = %+v, want [space, echo]", fx)
				}
				if !strings.Contains(out, "*echo") {
					t.Errorf("an alias the patch only moved past was written out:\n%s", out)
				}
			},
		},
		{
			name:  "a value a merge key brings changes there only",
			patch: `tracks: {lead: {instrument: {params: {Cutoff: 0.6}}}}`,
			want:  func(w want) want { w.lead = [2]float64{0.6, 0.1}; return w },
		},
		{
			name:  "a value a merge key brings is removed with null",
			patch: `tracks: {lead: {instrument: {params: null}}}`,
			want:  func(w want) want { w.lead = [2]float64{}; return w },
			then: func(t *testing.T, sg *Song, _ string) {
				if p := track(t, sg, "lead").Instrument.Params; len(p) != 0 {
					t.Errorf("lead params = %v, want none", p)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Patch([]byte(aliasSong), []byte(tc.patch))
			if err != nil {
				t.Fatalf("Patch: %v", err)
			}
			sg := mustParse(t, string(out))
			check(t, sg, tc.want(orig))
			if tc.then != nil {
				tc.then(t, sg, string(out))
			}
		})
	}
}

// TestPatchRefusesRemovingAnIDTheListLacks checks that null on an id that is
// not in an id-keyed list is refused with the list's ids, not passed over.
func TestPatchRefusesRemovingAnIDTheListLacks(t *testing.T) {
	cases := map[string]struct {
		patch string
		want  string
	}{
		"an index": {
			patch: `tracks: {bass: {effects: {"0": null}}}`,
			want:  `/tracks/bass/effects/0: no entry "0" to remove (have echo)`,
		},
		"a misspelt track": {
			patch: `tracks: {bas: null}`,
			want:  `/tracks/bas: no entry "bas" to remove (have bass, lead, drums)`,
		},
		"an effect named by its plugin's id": {
			patch: `tracks: {lead: {effects: {reverb: null}}}`,
			want:  `/tracks/lead/effects/reverb: no entry "reverb" to remove (have delay, space)`,
		},
		"a list the track does not have": {
			patch: `tracks: {drums: {effects: {echo: null}}}`,
			want:  `/tracks/drums/effects/echo: no entry "echo" to remove (have none)`,
		},
		"the master chain": {
			patch: `master: {limiter: null}`,
			want:  `/master/limiter: no entry "limiter" to remove (have glue)`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Patch([]byte(testSong), []byte(tc.patch))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Patch(%s) error = %v, want %q", tc.patch, err, tc.want)
			}
		})
	}
}

// TestPatchRefusesAnchorsAliasesAndMergeKeys checks that a patch names every
// value it sets in its place.
func TestPatchRefusesAnchorsAliasesAndMergeKeys(t *testing.T) {
	cases := map[string]struct {
		patch string
		want  string
	}{
		"an anchor": {
			patch: `tempo: &t 130`,
			want:  `/tempo: a patch cannot set an anchor (&t)`,
		},
		"an anchor an alias names": {
			patch: "x: &p {Cutoff: 0.5}\nscenes: {chorus: {bass: {params: *p}}}",
			want:  `/x: a patch cannot set an anchor (&p)`,
		},
		"an anchored key": {
			patch: `&k tempo: 130`,
			want:  `/tempo: a patch cannot set an anchor (&k)`,
		},
		"a merge key": {
			patch: `tracks: {bass: {<<: {level: -1}}}`,
			want:  `/tracks/bass: a patch cannot use a merge key (<<)`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Patch([]byte(testSong), []byte(tc.patch))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Patch(%s) error = %v, want %q", tc.patch, err, tc.want)
			}
		})
	}
}
