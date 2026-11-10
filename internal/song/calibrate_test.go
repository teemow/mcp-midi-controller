package song

import (
	"strings"
	"testing"
)

// calSong is a song written against the catalogue: no transposition anywhere,
// although its presets sound in three different octaves.
const calSong = `
title: Calibrated
tempo: 120
calibrate: true
tracks:
  - id: bass
    level: -6
    instrument: {plugin: synth, preset: Deep Bass}
  - id: lead
    instrument: {plugin: synth, preset: Warm Pad}
  - id: drums
    kind: drums
    instrument: {plugin: drums, preset: Kit 808}
patterns:
  bass_a: {step: 1/4, notes: "A1 . A1 ."}
  lead_a: {step: 1/4, notes: "A3 . C4 ."}
  beat:
    drums:
      kick:  "x...x...x...x..."
      snare: "....x.......x..."
      hat:   "x.x.x.x.x.x.x.x."
scenes:
  chorus:
    lead: {preset: Bright Lead, level: -3}
sections:
  verse: {bars: 1, play: {bass: bass_a, lead: lead_a, drums: beat}}
  chorus: {bars: 1, scene: chorus, play: {bass: bass_a, lead: lead_a, drums: beat}}
arrangement: [verse, chorus]
`

// calSounds is a catalogue for calSong: Deep Bass two octaves low and loud,
// Warm Pad an octave low and quiet, Bright Lead an octave high, and a kit
// with three pads.
func calSounds(plugin, preset string) (Voicing, bool) {
	switch plugin + "/" + preset {
	case "synth/Deep Bass":
		return Voicing{Transpose: 24, LevelDB: -12}, true
	case "synth/Warm Pad":
		return Voicing{Transpose: 12, LevelDB: -23.5}, true
	case "synth/Bright Lead":
		return Voicing{Transpose: -12, LevelDB: -17}, true
	case "drums/Kit 808":
		return Voicing{LevelDB: -9, Lanes: map[string]int{"kick": 49, "snare": 51, "hat": 54}}, true
	}
	return Voicing{}, false
}

func calibrated(t *testing.T, doc string) (*Timeline, *Plan) {
	t.Helper()
	s := mustParse(t, doc)
	p, err := NewPlanWith(s, testCatalog(), PlanOptions{Sounds: calSounds})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return tl, p
}

// notesOf lists a track's note-ons as "section:note".
func notesOf(tl *Timeline, track string) []string {
	var out []string
	for _, e := range tl.Events {
		if e.Kind == NoteOn && e.Track == track {
			out = append(out, tl.Sections[e.Section].Name+":"+NoteName(e.Data1))
		}
	}
	return out
}

// levelOf is a track's fader value in a section's state.
func levelOf(tl *Timeline, p *Plan, section int, track string) int {
	at := p.Track(track).Level
	for _, e := range tl.Sections[section].State {
		if e.Kind == Control && e.Channel == at.Channel && e.Data1 == at.Number {
			return e.Data2
		}
	}
	return -1
}

func TestCalibratePitch(t *testing.T) {
	tl, p := calibrated(t, calSong)
	// Written A1: Deep Bass sounds two octaves low, so A3 is sent.
	if got := strings.Join(notesOf(tl, "bass"), " "); got != "verse:A3 verse:A3 chorus:A3 chorus:A3" {
		t.Errorf("bass notes = %s", got)
	}
	// The lead changes preset with the chorus, and its shift with it: an
	// octave up for Warm Pad, an octave down for Bright Lead.
	if got := strings.Join(notesOf(tl, "lead"), " "); got != "verse:A4 verse:C5 chorus:A2 chorus:C3" {
		t.Errorf("lead notes = %s", got)
	}
	if w := p.Warnings(); len(w) != 0 {
		t.Errorf("warnings for a fully measured song: %v", w)
	}

	// A track's own transpose still applies on top.
	tl, _ = calibrated(t, strings.Replace(calSong, "  - id: bass\n", "  - id: bass\n    transpose: 12\n", 1))
	if got := notesOf(tl, "bass")[0]; got != "verse:A4" {
		t.Errorf("bass with its own transpose = %s, want A4", got)
	}

	// A shift out of the MIDI range is an error that names the place.
	s := mustParse(t, strings.Replace(calSong, `notes: "A3 . C4 ."`, `notes: "A8 . C4 ."`, 1))
	p, err := NewPlanWith(s, testCatalog(), PlanOptions{Sounds: calSounds})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := Compile(s, p); err == nil || !strings.Contains(err.Error(), "/sections/verse/play/lead: A8 shifted by +12 for its preset leaves the MIDI range") {
		t.Errorf("a shift out of range: %v", err)
	}
}

func TestCalibrateLevel(t *testing.T) {
	tl, p := calibrated(t, calSong)
	// bass: level -6, Deep Bass 8 dB over the reference: fader -14.
	// lead: level 0, Warm Pad 3.5 dB under it: fader +3.5; in the chorus
	// level -3 with Bright Lead 3 dB over it: fader -6.
	// drums: level 0, the kit 11 dB over it: fader -11.
	for _, tc := range []struct {
		section int
		track   string
		db      float64
	}{{0, "bass", -14}, {0, "lead", 3.5}, {1, "lead", -6}, {1, "bass", -14}, {0, "drums", -11}} {
		if got, want := levelOf(tl, p, tc.section, tc.track), levelWire(tc.db); got != want {
			t.Errorf("%s in section %d: fader CC %d, want %d (%+.1f dB)", tc.track, tc.section, got, want, tc.db)
		}
	}
	// The session opens with the same faders.
	sess, _, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := sess.Channels()[1].FaderLevel; got == nil || *got < LevelGain(-14)-0.001 || *got > LevelGain(-14)+0.001 {
		t.Errorf("the bass strip is authored at gain %v, want %g", got, LevelGain(-14))
	}

	rows := p.Calibration()
	if len(rows) != 4 || rows[0] != (TrackVoicing{Track: "bass", Plugin: "Test Synth", Preset: "Deep Bass", Measured: true, Transpose: 24, TrimDB: -8}) {
		t.Errorf("calibration = %+v", rows)
	}
	if rows[2].Preset != "Bright Lead" || rows[2].Transpose != -12 || rows[3].Pads != 3 {
		t.Errorf("calibration = %+v", rows)
	}

	// A preset too quiet for the fader is played as loud as the fader goes,
	// and the song says how much is missing and where to turn.
	quiet := func(plugin, preset string) (Voicing, bool) {
		if preset == "Warm Pad" {
			return Voicing{Transpose: 12, LevelDB: -47}, true
		}
		return calSounds(plugin, preset)
	}
	s := mustParse(t, calSong)
	p, err = NewPlanWith(s, testCatalog(), PlanOptions{Sounds: quiet})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err = Compile(s, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := levelOf(tl, p, 0, "lead"); got != 127 {
		t.Errorf("a preset 27 dB under the reference: fader CC %d, want the top (127)", got)
	}
	w := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(w, `lead: "Warm Pad" of Test Synth needs the fader at +27.0 dB for level 0, and it ends at +6: it plays 21.0 dB too quiet; raise the plugin's volume`) {
		t.Errorf("warnings = %s", w)
	}

	// A level ramp runs between trimmed levels.
	ramped := strings.Replace(calSong, "verse: {bars: 1, play:", "verse: {bars: 1, automation: [{track: bass, param: level, from: -30, to: -6}], play:", 1)
	tl, p = calibrated(t, ramped)
	if got, want := levelOf(tl, p, 0, "bass"), levelWire(-38); got != want {
		t.Errorf("a fade-in from -30 starts at fader CC %d, want %d (-38 dB)", got, want)
	}
}

func TestCalibrateKit(t *testing.T) {
	tl, _ := calibrated(t, calSong)
	got := map[string]int{}
	for _, n := range notesOf(tl, "drums") {
		got[strings.TrimPrefix(n, "verse:")]++
	}
	// The kit's pads, not General MIDI's notes: 4 kicks on C#3 (49), 2 snares
	// on D#3 (51), 8 hats on F#3 (54), in the verse.
	if got["C#3"] != 4 || got["D#3"] != 2 || got["F#3"] != 8 {
		t.Errorf("drum notes = %v", got)
	}

	// A lane the kit has no pad for is an error that lists its pads.
	s := mustParse(t, strings.Replace(calSong, `snare: "....x.......x..."`, `clap:  "....x.......x..."`, 1))
	_, err := NewPlanWith(s, testCatalog(), PlanOptions{Sounds: calSounds})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	p, _ := NewPlanWith(s, testCatalog(), PlanOptions{Sounds: calSounds})
	_, err = Compile(s, p)
	if err == nil || !strings.Contains(err.Error(), `/sections/verse/play/drums: /patterns/beat/drums/clap: the kit "Kit 808" has no pad for lane "clap" (it has: hat, kick, snare)`) {
		t.Errorf("a lane the kit lacks: %v", err)
	}
	// The track's own drum_map says where it goes.
	withMap := strings.Replace(calSong, "    instrument: {plugin: drums, preset: Kit 808}", "    instrument: {plugin: drums, preset: Kit 808}\n    drum_map: {clap: \"51\"}", 1)
	withMap = strings.Replace(withMap, `snare: "....x.......x..."`, `clap:  "....x.......x..."`, 1)
	tl, _ = calibrated(t, withMap)
	if n := notesOf(tl, "drums"); !contains(n, "verse:D#3") {
		t.Errorf("clap through the drum_map = %v", n)
	}
	// A lane the kit names that General MIDI does not know is fine, and the
	// song still lists without the catalogue.
	kit2 := func(plugin, preset string) (Voicing, bool) {
		if plugin == "drums" {
			return Voicing{Lanes: map[string]int{"kick": 49, "snare": 51, "hat": 54, "hat_2": 56}}, true
		}
		return calSounds(plugin, preset)
	}
	s = mustParse(t, strings.Replace(calSong, `hat:   "x.x.x.x.x.x.x.x."`, `hat_2: "x.x.x.x.x.x.x.x."`, 1))
	p, err = NewPlanWith(s, testCatalog(), PlanOptions{Sounds: kit2})
	if err != nil {
		t.Fatalf("plan with a kit's own lane: %v", err)
	}
	if tl, err = Compile(s, p); err != nil || !contains(notesOf(tl, "drums"), "verse:G#3") {
		t.Errorf("a kit's own lane: %v", err)
	}
	if _, err := Compile(s, nil); err != nil {
		t.Errorf("the notes alone do not compile: %v", err)
	}
}

func TestCalibrateUnmeasured(t *testing.T) {
	// Without a catalogue everything plays as written, and the song says so.
	s := mustParse(t, calSong)
	p, err := NewPlan(s, testCatalog())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := notesOf(tl, "bass")[0]; got != "verse:A1" {
		t.Errorf("an unmeasured bass plays %s, want the written A1", got)
	}
	w := p.Warnings()
	if len(w) != 4 || !strings.Contains(w[0], `bass: "Deep Bass" of Test Synth is not in the sound catalogue, so it plays as written (audition_sound measures it)`) {
		t.Errorf("warnings = %q", w)
	}
	for _, row := range p.Calibration() {
		if row.Measured || row.Transpose != 0 || row.TrimDB != 0 {
			t.Errorf("an unmeasured preset is calibrated: %+v", row)
		}
	}

	// A measured preset that is no kit leaves a drum track on General MIDI.
	noKit := func(plugin, preset string) (Voicing, bool) {
		if plugin == "drums" {
			return Voicing{LevelDB: -9}, true
		}
		return calSounds(plugin, preset)
	}
	p, err = NewPlanWith(s, testCatalog(), PlanOptions{Sounds: noKit})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if tl, err = Compile(s, p); err != nil || !contains(notesOf(tl, "drums"), "verse:C2") {
		t.Errorf("a drum track without a measured kit: %v, %v", err, notesOf(tl, "drums"))
	}
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, "but not as a kit") {
		t.Errorf("warnings = %s", w)
	}

	// The same song without calibrate is not touched by the catalogue.
	plain := mustParse(t, strings.Replace(calSong, "calibrate: true\n", "", 1))
	p, err = NewPlanWith(plain, testCatalog(), PlanOptions{Sounds: calSounds})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err = Compile(plain, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := notesOf(tl, "bass")[0]; got != "verse:A1" || len(p.Warnings()) != 0 || p.Calibration() != nil {
		t.Errorf("without calibrate: bass plays %s, warnings %v", got, p.Warnings())
	}
	if got, want := levelOf(tl, p, 0, "bass"), levelWire(-6); got != want {
		t.Errorf("without calibrate: bass fader CC %d, want %d", got, want)
	}
}

// TestCalibrateOnlyOctaves checks a measured transposition that is no whole
// number of octaves is not applied by itself: on a device a bell preset
// measured 16 semitones off and a texture 38, and neither is a tuning.
func TestCalibrateOnlyOctaves(t *testing.T) {
	odd := func(pinned bool) Sounds {
		return func(plugin, preset string) (Voicing, bool) {
			if preset == "Warm Pad" {
				return Voicing{Transpose: -16, Pinned: pinned, LevelDB: -20}, true
			}
			return calSounds(plugin, preset)
		}
	}
	s := mustParse(t, calSong)
	p, err := NewPlanWith(s, testCatalog(), PlanOptions{Sounds: odd(false)})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := notesOf(tl, "lead")[0]; got != "verse:A3" {
		t.Errorf("the lead plays %s, want the written A3", got)
	}
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, `lead: "Warm Pad" of Test Synth measures +16 semitone(s) from the written note, which is not an octave, so the notes are not shifted`) {
		t.Errorf("warnings = %s", w)
	}
	for _, row := range p.Calibration() {
		if row.Preset == "Warm Pad" && (row.Transpose != 0 || !row.Measured) {
			t.Errorf("calibration row = %+v, want measured and not shifted", row)
		}
	}

	// Set by hand, it is the patch's tuning and applies.
	p, err = NewPlanWith(s, testCatalog(), PlanOptions{Sounds: odd(true)})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if tl, err = Compile(s, p); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := notesOf(tl, "lead")[0]; got != "verse:F2" || len(p.Warnings()) != 0 {
		t.Errorf("with the transposition pinned the lead plays %s (warnings %v), want F2", got, p.Warnings())
	}
}
