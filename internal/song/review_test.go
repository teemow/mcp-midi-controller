package song

import (
	"strings"
	"testing"
)

// loopSong is the kind of song the first ones were: forty seconds, three
// tracks, two-bar loops, a verse three times over and a fade at the end.
const loopSong = `
title: Loop
tempo: 120
tracks:
  - id: bass
    instrument: {plugin: synth, preset: Deep Bass}
  - id: pad
    instrument: {plugin: Test Synth, preset: Warm Pad}
    effects: [{id: space, plugin: reverb, preset: Small Room}]
  - id: drums
    kind: drums
    instrument: {plugin: drums, preset: Kit 808}
master:
  - {id: echo, plugin: delay}
patterns:
  bass_a: {step: 1/4, notes: "A1 . A1 . | G1 . G1 ."}
  pad_a: {step: 1/2, gate: 1, notes: "[A3 C4 E4] - | [G3 B3 D4] -"}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
scenes:
  bright:
    pad: {preset: Bright Lead}
sections:
  intro: {bars: 4, play: {pad: pad_a}}
  verse: {bars: 4, play: {bass: bass_a, pad: pad_a, drums: beat}}
  outro:
    bars: 4
    scene: bright
    play: {bass: bass_a, pad: pad_a}
    automation:
      - {track: bass, param: level, from: 0, to: -40, bar: 2}
      - {track: pad, param: level, from: 0, to: -40}
arrangement: [intro, verse, verse, verse, outro]
`

// fullSong runs 2:44 on six tracks, with phrases of eight bars, a last chorus
// that differs from the first two and an ending on a held chord.
const fullSong = `
title: Full
tempo: 120
tracks:
  - {id: bass, instrument: {plugin: synth, preset: Deep Bass}}
  - {id: keys, instrument: {plugin: synth, preset: Warm Pad}}
  - {id: lead, instrument: {plugin: synth, preset: Bright Lead}}
  - {id: counter, instrument: {plugin: synth, preset: Init}}
  - {id: air, instrument: {plugin: synth}}
  - {id: drums, kind: drums, instrument: {plugin: drums, preset: Kit 909}}
patterns:
  line: {bars: 8, step: 1/4, notes: "A1 . C2 . | E2 . D2 ."}
  chords: {bars: 8, step: 1/2, gate: 1, notes: "[A3 C4 E4] - | [F3 A3 C4] -"}
  tune: {bars: 8, step: 1/4, notes: "A4 C5 E5 D5"}
  answer: {bars: 8, step: 1/4, notes: ". . A5 ."}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
  last: {bars: 2, step: 1/2, gate: 1, notes: "[A3 C4 E4] -"}
  hit: {bars: 2, drums: {kick: "x", crash: "x"}}
sections:
  intro: {bars: 8, play: {keys: chords}}
  verse: {bars: 16, play: {bass: line, keys: chords, drums: beat}}
  chorus: {bars: 8, play: {bass: line, keys: chords, lead: tune, drums: beat}}
  verse2: {bars: 16, play: {bass: line, keys: chords, counter: answer, drums: beat}}
  bridge: {bars: 8, play: {keys: chords, air: tune}}
  chorus_last: {bars: 16, play: {bass: line, keys: chords, lead: tune, counter: answer, drums: beat}}
  ending: {bars: 2, play: {keys: last, drums: hit}}
arrangement: [intro, verse, chorus, verse2, chorus, bridge, chorus_last, ending]
`

func reviewOf(t *testing.T, doc string, lib Library) []Remark {
	t.Helper()
	s := mustParse(t, doc)
	tl, err := Compile(s, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return Review(s, tl, testCatalog(), lib)
}

// about returns the texts of a review's remarks on one subject.
func about(rs []Remark, subject string) []string {
	var out []string
	for _, r := range rs {
		if r.About == subject {
			out = append(out, r.Text)
		}
	}
	return out
}

// wantRemark fails unless exactly one remark on the subject holds every part.
func wantRemark(t *testing.T, rs []Remark, subject string, parts ...string) {
	t.Helper()
	hits := 0
	for _, text := range about(rs, subject) {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(text, p)
		}
		if all {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("remarks on %s = %q, want one with %q", subject, about(rs, subject), parts)
	}
}

func TestReviewOfAShortFadingLoop(t *testing.T) {
	rs := reviewOf(t, loopSong, Library{})
	// 20 bars at 120 BPM are 40 s; three minutes are 90 bars, four and a half 135.
	wantRemark(t, rs, AboutLength, "0:40 long (20 bars)", "90 to 135 bars")
	wantRemark(t, rs, AboutEnding, "fade-out", `"outro"`, "every track playing in it")
	wantRemark(t, rs, AboutForm, `"verse" is heard 3 times unchanged`)
	wantRemark(t, rs, AboutForm, "no part has a phrase longer than 2 bars")
	wantRemark(t, rs, AboutTracks, "3 tracks", "can have 12")
	if got := about(rs, AboutSounds); got != nil {
		t.Errorf("remarks on sounds without other songs = %q, want none", got)
	}
	if len(rs) != 5 {
		t.Errorf("%d remarks, want 5: %+v", len(rs), rs)
	}
}

func TestReviewLeavesAFullSongAlone(t *testing.T) {
	if rs := reviewOf(t, fullSong, Library{}); len(rs) != 0 {
		t.Errorf("a full song got remarks: %+v", rs)
	}
}

func TestReviewSkipsASongWithoutNotes(t *testing.T) {
	lab := `
title: Lab
tempo: 120
tracks: [{id: a, instrument: {plugin: synth}}]
patterns: {rest: {bars: 1, notes: ["."]}}
sections: {idle: {bars: 1}}
arrangement: [idle]
`
	if rs := reviewOf(t, lab, Library{}); rs != nil {
		t.Errorf("a song without a note got a review: %+v", rs)
	}
}

func TestReviewEnding(t *testing.T) {
	const head = `
title: End
tempo: 120
tracks:
  - {id: a, instrument: {plugin: synth}}
  - {id: b, instrument: {plugin: synth}}
  - {id: c, instrument: {plugin: synth}}
patterns:
  p: {step: 1/4, notes: "A2 . . ."}
sections:
  main: {bars: 4, play: {a: p, b: p, c: p}}
  end:
    bars: 4
    play: {a: p, b: p, c: p}
    automation:
`
	const tail = "\narrangement: [main, end]\n"
	cases := []struct {
		name, ramps, want string
	}{
		{"every track", `
      - {track: a, param: level, from: 0, to: -40}
      - {track: b, param: Level, from: -6, to: -30, bar: 3}
      - {track: c, param: level, from: 0, to: -40, bar: 2, bars: 3}`, "every track playing in it"},
		{"most tracks", `
      - {track: a, param: level, from: 0, to: -40}
      - {track: b, param: level, from: 0, to: -40}`, "2 of the 3 tracks playing in it"},
		{"one track of three thins out", `
      - {track: a, param: level, from: 0, to: -40}`, ""},
		{"a swell", `
      - {track: a, param: level, from: -40, to: 0}
      - {track: b, param: level, from: -40, to: 0}
      - {track: c, param: level, from: -40, to: 0}`, ""},
		{"a dip that is over before the end", `
      - {track: a, param: level, from: 0, to: -40, bars: 2}
      - {track: b, param: level, from: 0, to: -40, bars: 2}
      - {track: c, param: level, from: 0, to: -40, bars: 2}`, ""},
		{"a few dB down", `
      - {track: a, param: level, from: 0, to: -6}
      - {track: b, param: level, from: 0, to: -6}
      - {track: c, param: level, from: 0, to: -6}`, ""},
		{"a filter closing", `
      - {track: a, param: Cutoff, from: 1, to: 0}
      - {track: b, param: Cutoff, from: 1, to: 0}
      - {track: c, param: Cutoff, from: 1, to: 0}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := about(reviewOf(t, head+strings.TrimPrefix(c.ramps, "\n")+tail, Library{}), AboutEnding)
			switch {
			case c.want == "" && got != nil:
				t.Errorf("remark on the ending = %q, want none", got)
			case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
				t.Errorf("remark on the ending = %q, want one with %q", got, c.want)
			}
		})
	}
}

func TestReviewKnowsASectionUnderTwoNames(t *testing.T) {
	doc := `
title: Twice
tempo: 120
tracks: [{id: a, instrument: {plugin: synth}}]
patterns:
  p: {bars: 8, step: 1/4, notes: "A2 . . ."}
  q: {bars: 8, step: 1/4, notes: "C3 . . ."}
sections:
  verse: {bars: 8, play: {a: p}}
  verse2: {bars: 8, play: {a: p}}
  chorus: {bars: 8, play: {a: q}}
  chorus2: {bars: 8, play: {a: [q, p]}}
arrangement: [verse, chorus, verse2, chorus2, verse, chorus]
`
	rs := reviewOf(t, doc, Library{})
	wantRemark(t, rs, AboutForm, `"verse" (the same as "verse2") is heard 3 times unchanged`)
	// The chorus is heard twice as it is and once with another pattern list.
	for _, text := range about(rs, AboutForm) {
		if strings.Contains(text, `"chorus"`) {
			t.Errorf("the chorus counts as heard three times unchanged: %q", text)
		}
	}
}

func TestPalette(t *testing.T) {
	s := mustParse(t, loopSong)
	s.Tracks = append(s.Tracks, Track{ID: "ghost", Instrument: Plugin{Plugin: "not installed"}})
	got := s.Palette(testCatalog())
	want := []Voice{
		{Track: "bass", Plugin: "synth", Name: "Test Synth", Preset: "Deep Bass", Instrument: true},
		// By its display name in the document, and once more for its scene's preset.
		{Track: "pad", Plugin: "synth", Name: "Test Synth", Preset: "Warm Pad", Instrument: true},
		{Track: "pad", Plugin: "synth", Name: "Test Synth", Preset: "Bright Lead", Instrument: true},
		{Track: "pad", Plugin: "reverb", Name: "Test Reverb", Preset: "Small Room"},
		{Track: "drums", Plugin: "drums", Name: "Test Drums", Preset: "Kit 808", Instrument: true},
		{Track: "master", Plugin: "delay", Name: "Test Delay"},
	}
	if len(got) != len(want) {
		t.Fatalf("palette = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("palette[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReviewSounds(t *testing.T) {
	synth := func(preset string) Voice {
		return Voice{Track: "x", Plugin: "synth", Name: "Test Synth", Preset: preset, Instrument: true}
	}
	kit := Voice{Track: "d", Plugin: "drums", Name: "Test Drums", Preset: "Kit 909", Instrument: true}
	hall := Voice{Track: "x", Plugin: "reverb", Name: "Test Reverb", Preset: "Large Hall"}
	echo := Voice{Track: "master", Plugin: "delay", Name: "Test Delay"}

	t.Run("the same presets and nothing new", func(t *testing.T) {
		rs := reviewOf(t, loopSong, Library{
			Songs: map[string][]Voice{
				"first":  {synth("deep bass"), synth("Init"), kit, hall, echo},
				"second": {synth("Deep Bass"), synth("Bright Lead"), hall},
			},
			Instruments: 57, Effects: 83,
		})
		// The bass has its preset in both songs, the pad's scene preset in one;
		// the kit is the same plugin with another preset.
		wantRemark(t, rs, AboutSounds, "the same sound as another stored song",
			`bass has Test Synth "Deep Bass", as in first, second (one of its 4 presets)`,
			`pad has Test Synth "Bright Lead", as in second`)
		for _, text := range about(rs, AboutSounds) {
			if strings.Contains(text, "Kit 808") || strings.Contains(text, "Warm Pad") {
				t.Errorf("a preset no other song has is called shared: %q", text)
			}
		}
		wantRemark(t, rs, AboutSounds, "every instrument is one the 2 other stored songs already use",
			"Test Synth (in 2)", "Test Drums (in 1)", "57 instruments are installed and those songs use 2 of them")
		wantRemark(t, rs, AboutSounds, "every effect is one the 2 other stored songs already use",
			"Test Reverb (in 2)", "Test Delay (in 1)", "83 effects are installed and those songs use 2 of them")
	})

	t.Run("a plugin of its own", func(t *testing.T) {
		rs := reviewOf(t, loopSong, Library{Songs: map[string][]Voice{
			"first":  {synth("Init"), hall},
			"second": {synth("Init"), hall},
		}})
		// The drum machine and the master's delay are new to the rig's songs.
		if got := about(rs, AboutSounds); got != nil {
			t.Errorf("remarks on sounds = %q, want none", got)
		}
	})

	t.Run("one other song says little about a palette", func(t *testing.T) {
		rs := reviewOf(t, loopSong, Library{Songs: map[string][]Voice{
			"first": {synth("Deep Bass"), kit, hall, echo},
		}})
		got := about(rs, AboutSounds)
		if len(got) != 1 || !strings.Contains(got[0], `bass has Test Synth "Deep Bass", as in first`) {
			t.Errorf("remarks on sounds = %q, want only the shared preset", got)
		}
	})

	t.Run("a default patch is no shared sound", func(t *testing.T) {
		rs := reviewOf(t, fullSong, Library{Songs: map[string][]Voice{
			"first": {synth("")},
		}})
		if got := about(rs, AboutSounds); got != nil {
			t.Errorf("remarks on sounds = %q, want none", got)
		}
	})
}
