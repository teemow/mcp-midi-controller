package song

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// describe renders an event the way the tests read it: "tick kind chN d1 d2".
func describe(e Event) string {
	kind := map[EventKind]string{NoteOff: "off", Program: "pc", Control: "cc", NoteOn: "on"}[e.Kind]
	return fmt.Sprintf("%d %s ch%d %d %d", e.Tick, kind, e.Channel, e.Data1, e.Data2)
}

// controls returns the timeline's control and program events as text.
func controls(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == Program || e.Kind == Control {
			out = append(out, describe(e))
		}
	}
	return out
}

func compileTest(t *testing.T, doc string) (*Timeline, *Plan) {
	t.Helper()
	s := mustParse(t, doc)
	p := mustPlan(t, s)
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return tl, p
}

func TestCompileNotes(t *testing.T) {
	s := mustParse(t, testSong)
	tl, err := Compile(s, nil) // notes need no plugins
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	bar := int64(4 * PPQ)
	if tl.Ticks != 6*bar || tl.Bars != 6 || len(tl.Sections) != 3 {
		t.Fatalf("timeline = %d ticks, %d bars, %d sections; want 6 bars in 3 sections", tl.Ticks, tl.Bars, len(tl.Sections))
	}
	if got := tl.Sections[1]; got.Name != "chorus" || got.Tick != 2*bar || got.Bar != 3 {
		t.Errorf("second section = %+v, want the chorus from bar 3", got)
	}
	if d := tl.Duration().Seconds(); d != 12 {
		t.Errorf("duration = %gs, want 12s (6 bars at 120 BPM)", d)
	}

	notes := map[string]int{}
	for _, ts := range tl.Tracks {
		notes[ts.ID] = ts.Notes
	}
	// bass: 7 notes per 2-bar pattern, in all three sections. lead: 5 notes
	// per bar, looped over the 2-bar chorus. drums: 15 hits a bar, six bars.
	if notes["bass"] != 21 || notes["lead"] != 10 || notes["drums"] != 90 {
		t.Errorf("note counts = %v, want bass 21, lead 10, drums 90", notes)
	}

	// The lead plays on its own channel, an octave up, only in the chorus.
	var lead []Event
	for _, e := range tl.Events {
		if e.Track == "lead" && e.Kind == NoteOn {
			lead = append(lead, e)
		}
	}
	if first := lead[0]; first.Tick != 2*bar || first.Channel != 2 || first.Data1 != 69 {
		t.Errorf("first lead note = %s, want A4 on channel 2 at the chorus", describe(first))
	}
	if last := lead[len(lead)-1]; last.Tick >= 4*bar {
		t.Errorf("a lead note at %d sounds after the chorus", last.Tick)
	}

	// Events are ordered, and at one tick a note ends before the next begins.
	for i := 1; i < len(tl.Events); i++ {
		a, b := tl.Events[i-1], tl.Events[i]
		if a.Tick > b.Tick || (a.Tick == b.Tick && a.Kind > b.Kind) {
			t.Fatalf("events out of order: %s before %s", describe(a), describe(b))
		}
	}
}

// TestCompileClipsAndRetriggers checks a note is cut at its section's end and
// a note struck again while it sounds is released first.
func TestCompileClipsAndRetriggers(t *testing.T) {
	doc := `
title: Clip
tempo: 120
tracks:
  - {id: a, instrument: {plugin: synth}}
patterns:
  long: {step: 1/4, gate: 3, notes: "C3 . . . | . . C3 ."}
sections:
  one: {bars: 2, play: {a: long}}
  two: {bars: 1, play: {a: long}}
arrangement: [one, two]
`
	tl, err := Compile(mustParse(t, doc), nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var got []string
	for _, e := range tl.Events {
		got = append(got, describe(e))
	}
	// gate 3 makes every C3 three beats long: the second (beat 7) is cut at
	// the section's end (beat 8), and in section two the pattern is cut after
	// one bar, so only its first note plays.
	want := []string{
		"0 on ch1 48 96", "1440 off ch1 48 0",
		"2880 on ch1 48 96", "3840 off ch1 48 0",
		"3840 on ch1 48 96", "5280 off ch1 48 0",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("events:\n got  %v\n want %v", got, want)
	}

	// Struck again while sounding: the first note ends where the second
	// begins.
	doc = strings.Replace(doc, `"C3 . . . | . . C3 ."`, `"C3 . C3 . | . . . ."`, 1)
	tl, err = Compile(mustParse(t, doc), nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := describe(tl.Events[1]); got != "960 off ch1 48 0" {
		t.Errorf("second event = %s, want the first C3 released as the second is struck", got)
	}
}

// TestCompileSceneChanges checks the control messages between sections: only
// what differs is sent, a preset load is sent early, and it is followed by the
// node's parameters.
func TestCompileSceneChanges(t *testing.T) {
	tl, p := compileTest(t, testSong)
	bar := int64(4 * PPQ)

	bass, lead := p.Track("bass"), p.Track("lead")
	bassSynth, echo := p.Node("bass", ""), p.Node("bass", "echo")
	leadSynth, space := p.Node("lead", ""), p.Node("lead", "space")
	glue := p.Node(MasterTrack, "glue")
	cc := func(tick int64, at Assignment, v int) string {
		return fmt.Sprintf("%d cc ch%d %d %d", tick, at.Channel, at.Number, v)
	}
	pc := func(tick int64, program int) string {
		return fmt.Sprintf("%d pc ch%d %d 0", tick, GlobalChannel, program)
	}
	preset := func(n *NodePlan, name string) int {
		for _, pr := range n.Presets {
			if pr.Name == name {
				return pr.Program
			}
		}
		t.Fatalf("%s has no planned preset %q", nodeLabel(n), name)
		return -1
	}

	// Into the chorus (bar 3): the lead's and the reverb's presets load
	// presetLead early; at the bar the bass opens up and un-bypasses its echo,
	// the master effect comes in, and the lead is unmuted at -6 dB.
	early := 2*bar - tl.leadTicks()
	wantChorus := []string{
		pc(early, preset(leadSynth, "Bright Lead")),
		pc(early, preset(space, "Large Hall")),
		cc(2*bar, bassSynth.Params[0].At, 102), // Cutoff 80%
		cc(2*bar, echo.Bypass, 0),
		cc(2*bar, glue.Bypass, 0),
		cc(2*bar, lead.Level, levelWire(-6)),
		cc(2*bar, lead.Mute, 0),
	}
	// Back to the verse (bar 5): everything returns home.
	early = 4*bar - tl.leadTicks()
	wantVerse := []string{
		pc(early, preset(leadSynth, "Warm Pad")),
		pc(early, preset(space, "Small Room")),
		cc(4*bar, bassSynth.Params[0].At, 32), // Cutoff 0.25
		cc(4*bar, echo.Bypass, 127),
		cc(4*bar, glue.Bypass, 127),
		cc(4*bar, lead.Level, levelWire(0)),
		cc(4*bar, lead.Mute, 127),
	}
	want := append(wantChorus, wantVerse...)
	got := controls(tl.Events)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("control events:\n got:\n  %s\n want:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// Every section carries its whole sound, presets first.
	state := controls(tl.Sections[0].State)
	wantOpen := []string{
		pc(0, preset(bassSynth, "Deep Bass")),
		pc(0, preset(leadSynth, "Warm Pad")),
		pc(0, preset(space, "Small Room")),
	}
	for i, w := range wantOpen {
		if state[i] != w {
			t.Errorf("opening state[%d] = %s, want %s", i, state[i], w)
		}
	}
	for _, w := range []string{
		cc(0, bass.Level, levelWire(-3)), cc(0, bass.Mute, 0),
		cc(0, lead.Mute, 127), cc(0, echo.Bypass, 127), cc(0, bassSynth.Bypass, 0),
		cc(0, echo.Params[0].At, 25), // the echo's own Mix 0.2
	} {
		if !contains(state, w) {
			t.Errorf("opening state lacks %s:\n  %s", w, strings.Join(state, "\n  "))
		}
	}
	// The verse sounds the same wherever it stands in the arrangement.
	if a, b := controls(tl.Sections[0].State), controls(tl.Sections[2].State); strings.Join(a, ";") != strings.Join(b, ";") {
		t.Errorf("the verse's state differs between its two occurrences:\n %v\n %v", a, b)
	}
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// TestPresetReloadResendsParams checks a node's parameters are sent again
// after its preset changes, even when their values did not.
func TestPresetReloadResendsParams(t *testing.T) {
	doc := `
title: Reload
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: synth, preset: Warm Pad, params: {Cutoff: 0.5, Waveform: Square}}
patterns:
  p: {step: 1/4, notes: "C3 . . ."}
scenes:
  bright: {pad: {preset: Bright Lead}}
sections:
  a: {bars: 1, play: {pad: p}}
  b: {bars: 1, scene: bright, play: {pad: p}}
arrangement: [a, b]
`
	tl, p := compileTest(t, doc)
	pad := p.Node("pad", "")
	got := controls(tl.Events)
	bar := int64(4 * PPQ)
	want := []string{
		fmt.Sprintf("%d pc ch%d 1 0", bar-tl.leadTicks(), GlobalChannel),
		fmt.Sprintf("%d cc ch%d %d 64", bar, pad.Params[0].At.Channel, pad.Params[0].At.Number), // Cutoff 0.5
		fmt.Sprintf("%d cc ch%d %d 64", bar, pad.Params[1].At.Channel, pad.Params[1].At.Number), // Waveform: Square of Saw/Square/Sine
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("control events:\n got  %v\n want %v", got, want)
	}
}

// TestAutomation checks a ramp's steps, that a ramp opening a section sets the
// section's opening value, and that the next section returns the control home.
func TestAutomation(t *testing.T) {
	doc := `
title: Ramp
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: synth, params: {Cutoff: 0.5}}
patterns:
  p: {step: 1/4, notes: "C3 . . ."}
sections:
  build:
    bars: 2
    play: {pad: p}
    automation:
      - {track: pad, param: Cutoff, from: 0, to: 3.2%, bars: 1}
      - {track: pad, param: level, from: -90, to: 0, bar: 2}
  drop: {bars: 1, play: {pad: p}}
arrangement: [build, drop]
`
	tl, p := compileTest(t, doc) // 3.2% of the cutoff's range is CC value 4
	pad, strip := p.Node("pad", ""), p.Track("pad")
	cutoff := pad.Params[0].At
	bar := int64(4 * PPQ)

	// The opening state carries the ramp's start, not the parameter's home.
	want := fmt.Sprintf("0 cc ch%d %d 0", cutoff.Channel, cutoff.Number)
	if state := controls(tl.Sections[0].State); !contains(state, want) {
		t.Errorf("opening state lacks the ramp's first value %q: %v", want, state)
	}

	var cut, level []Event
	for _, e := range tl.Events {
		if e.Kind != Control {
			continue
		}
		switch {
		case e.Channel == cutoff.Channel && e.Data1 == cutoff.Number:
			cut = append(cut, e)
		case e.Channel == strip.Level.Channel && e.Data1 == strip.Level.Number:
			level = append(level, e)
		}
	}
	// Cutoff: 1, 2, 3, 4 spread over the first bar, then home (64) at the drop.
	var got []string
	for _, e := range cut {
		got = append(got, fmt.Sprintf("%d=%d", e.Tick, e.Data2))
	}
	wantCut := []string{"480=1", "960=2", "1440=3", "1920=4", fmt.Sprintf("%d=64", 2*bar)}
	if strings.Join(got, " ") != strings.Join(wantCut, " ") {
		t.Errorf("cutoff ramp = %v, want %v", got, wantCut)
	}
	// Level: a fade-in over bar 2, never closer than the ramp spacing, ending
	// at unity — which is also the strip's home, so the drop sends nothing.
	if len(level) < 30 || level[0].Tick != bar || level[len(level)-1].Data2 != levelWire(0) || level[len(level)-1].Tick != 2*bar {
		t.Errorf("level ramp = %d steps from %d to %d (value %d), want a fade over bar 2 up to unity",
			len(level), level[0].Tick, level[len(level)-1].Tick, level[len(level)-1].Data2)
	}
	for i := 1; i < len(level); i++ {
		if d := level[i].Tick - level[i-1].Tick; d < rampSpacingTicks {
			t.Fatalf("level steps %d ticks apart, closer than %d", d, rampSpacingTicks)
		}
		if level[i].Data2 <= level[i-1].Data2 {
			t.Fatalf("level ramp is not rising: %d then %d", level[i-1].Data2, level[i].Data2)
		}
	}
}

func TestLevelLaw(t *testing.T) {
	// The values measured on AUM: 90 is unity, 127 is +6 dB, and every
	// halving is 12 dB down.
	for db, want := range map[float64]int{0: 90, 6: 127, -2: 80, -5.9: 64, -10.9: 48, -17.9: 32, -30: 16, -90: 0, -200: 0, 30: 127} {
		if got := levelWire(db); got != want {
			t.Errorf("levelWire(%g) = %d, want %d", db, got, want)
		}
	}
	if g := LevelGain(-6); g < 0.5 || g > 0.502 {
		t.Errorf("LevelGain(-6) = %g, want about 0.501", g)
	}
	if g := LevelGain(-90); g != 0 {
		t.Errorf("LevelGain(-90) = %g, want silence", g)
	}
}

// TestPresetLeadIsTime checks a preset load leads its section by the same
// wall-clock time at any tempo: a plugin does not load faster in a fast song.
func TestPresetLeadIsTime(t *testing.T) {
	for _, tempo := range []string{"60", "120", "240"} {
		tl, _ := compileTest(t, strings.Replace(testSong, "tempo: 120", "tempo: "+tempo, 1))
		var first Event
		for _, e := range tl.Events {
			if e.Kind == Program {
				first = e
				break
			}
		}
		lead := tl.TickTime(tl.Sections[1].Tick) - tl.TickTime(first.Tick)
		if lead < presetLead-5*time.Millisecond || lead > presetLead+5*time.Millisecond {
			t.Errorf("at %s BPM the preset leads by %v, want %v", tempo, lead, presetLead)
		}
	}
}

// TestFingerprintCoversTempo checks a tempo change marks the session as out of
// date: the session's clock is the song's tempo.
func TestFingerprintCoversTempo(t *testing.T) {
	a := mustPlan(t, mustParse(t, testSong)).Fingerprint()
	b := mustPlan(t, mustParse(t, strings.Replace(testSong, "tempo: 120", "tempo: 90", 1))).Fingerprint()
	if a == b {
		t.Error("the fingerprint is the same at 120 and at 90 BPM")
	}
	// Notes are not part of it.
	c := mustPlan(t, mustParse(t, strings.Replace(testSong, `"A1 . A1 . A1 . A1 . |`, `"A1 A1 A1 . A1 . A1 . |`, 1))).Fingerprint()
	if a != c {
		t.Error("the fingerprint changed with a note")
	}
}
