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
