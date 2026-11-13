package song

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// unbypassSong switches a reverb on and off: on with a new Mix at once (wet),
// off with another Mix (held), and on again with the Mix it already has
// (back). The delay is never bypassed.
const unbypassSong = `
title: Unbypass
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: synth, params: {Cutoff: 0.5}}
    effects:
      - {id: verb, plugin: reverb, bypass: true, params: {Mix: 0, Size: 0.5}}
      - {id: dly, plugin: delay, params: {Mix: 0.2}}
patterns:
  p: {step: 1/4, notes: "C3 . . ."}
scenes:
  wet:  {pad: {effects: {verb: {bypass: false, params: {Mix: 1}}}}}
  held: {pad: {effects: {verb: {bypass: true, params: {Mix: 0.5}}}}}
  back: {pad: {effects: {verb: {bypass: false, params: {Mix: 0.5}}}}}
sections:
  dry:  {bars: 1, play: {pad: p}}
  wet:  {bars: 1, scene: wet, play: {pad: p}}
  held: {bars: 1, scene: held, play: {pad: p}}
  back: {bars: 1, scene: back, play: {pad: p}}
arrangement: [dry, wet, held, back]
`

// nodeCC renders a control change of one of the node's controls the way
// controls() does.
func nodeCC(tick int64, at Assignment, v int) string {
	return fmt.Sprintf("%d cc ch%d %d %d", tick, at.Channel, at.Number, v)
}

func paramAt(t *testing.T, n *NodePlan, name string) Assignment {
	t.Helper()
	pp, err := n.param(name)
	if err != nil {
		t.Fatal(err)
	}
	return pp.At
}

// TestUnbypassResendsParams checks a node a section switches back on gets
// every parameter the song sets on it once the un-bypass has taken effect,
// whether the section changes it (wet) or it was changed while the node was
// bypassed (held, then back); going into the bypass the change goes with it.
func TestUnbypassResendsParams(t *testing.T) {
	tl, p := compileTest(t, unbypassSong)
	verb := p.Node("pad", "verb")
	mix, size := paramAt(t, verb, "Mix"), paramAt(t, verb, "Size")
	bar, settle := int64(4*PPQ), tl.settleTicks()

	want := []string{
		// wet: on at the bar, the new Mix and the Size after it, and not with it.
		nodeCC(bar, verb.Bypass, 0),
		nodeCC(bar+settle, mix, 127),
		nodeCC(bar+settle, size, 64),
		// held: off, with its Mix.
		nodeCC(2*bar, verb.Bypass, 127),
		nodeCC(2*bar, mix, 64),
		// back: on, and Mix and Size again although neither changed.
		nodeCC(3*bar, verb.Bypass, 0),
		nodeCC(3*bar+settle, mix, 64),
		nodeCC(3*bar+settle, size, 64),
	}
	if got := controls(tl.Events); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("control events:\n got:\n  %s\n want:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// Each section's opening state may find the reverb bypassed in AUM: where
	// it has it on, Mix and Size follow. Nothing of the delay or the synth,
	// which the song never bypasses.
	for i, w := range [][]string{
		nil,
		{nodeCC(0, mix, 127), nodeCC(0, size, 64)},
		nil,
		{nodeCC(0, mix, 64), nodeCC(0, size, 64)},
	} {
		if got := controls(tl.Sections[i].Again); strings.Join(got, ";") != strings.Join(w, ";") {
			t.Errorf("%s's Again = %v, want %v", tl.Sections[i].Name, got, w)
		}
	}
}

// TestUnbypassSettleIsTime checks the parameters follow the un-bypass by the
// same wall-clock time at any tempo: AUM does not switch faster in a fast
// song.
func TestUnbypassSettleIsTime(t *testing.T) {
	for _, tempo := range []string{"60", "120", "240", "333"} {
		tl, p := compileTest(t, strings.Replace(unbypassSong, "tempo: 120", "tempo: "+tempo, 1))
		mix := paramAt(t, p.Node("pad", "verb"), "Mix")
		wet := tl.Sections[1].Tick
		var at int64 = -1
		for _, e := range tl.Events {
			if e.Kind == Control && e.Channel == mix.Channel && e.Data1 == mix.Number && e.Tick >= wet {
				at = e.Tick
				break
			}
		}
		d := tl.TickTime(at) - tl.TickTime(wet)
		if d < unbypassSettle || d > unbypassSettle+tl.TickTime(1) {
			t.Errorf("at %s BPM the Mix follows the un-bypass by %v, want %v", tempo, d, unbypassSettle)
		}
	}
}

// TestUnbypassInAShortSection checks the parameters of a node switched back
// on stay in their section when it is shorter than unbypassSettle: sent after
// the next section's own changes, they would undo them.
func TestUnbypassInAShortSection(t *testing.T) {
	// A bar of 1/8 at 300 BPM lasts 100 ms.
	doc := strings.Replace(unbypassSong, "tempo: 120", "tempo: 300\ntime_signature: \"1/8\"", 1)
	doc = strings.Replace(doc, `notes: "C3 . . ."`, `notes: "C3 ."`, 1)
	tl, p := compileTest(t, doc)
	if bar := tl.TickTime(tl.BarTicks); bar >= unbypassSettle {
		t.Fatalf("a bar lasts %v, not less than %v", bar, unbypassSettle)
	}
	mix := paramAt(t, p.Node("pad", "verb"), "Mix")
	wet, held := tl.Sections[1], tl.Sections[2]
	var got []string
	for _, e := range tl.Events {
		if e.Kind == Control && e.Channel == mix.Channel && e.Data1 == mix.Number {
			got = append(got, describe(e))
			if e.Section == 1 && (e.Tick < wet.Tick || e.Tick >= held.Tick) {
				t.Errorf("the wet section's Mix is sent at tick %d, outside it (%d..%d)", e.Tick, wet.Tick, held.Tick)
			}
		}
	}
	if len(got) == 0 || !strings.HasSuffix(got[0], " 127") {
		t.Errorf("Mix messages %v, want the wet section's 127 first", got)
	}
}

// TestUnbypassWithPresetLoad checks a node that loads a preset and comes back
// on in one section gets its parameters after the un-bypass, not with it.
func TestUnbypassWithPresetLoad(t *testing.T) {
	doc := strings.Replace(unbypassSong, "{verb: {bypass: false, params: {Mix: 1}}}", "{verb: {bypass: false, preset: Large Hall, params: {Mix: 1}}}", 1)
	doc = strings.Replace(doc, "{id: verb, plugin: reverb, bypass: true,", "{id: verb, plugin: reverb, preset: Small Room, bypass: true,", 1)
	tl, p := compileTest(t, doc)
	verb := p.Node("pad", "verb")
	mix, size := paramAt(t, verb, "Mix"), paramAt(t, verb, "Size")
	bar, settle := int64(4*PPQ), tl.settleTicks()
	hall, _ := verb.Preset("Large Hall")
	var upTo []Event
	for _, e := range tl.Events {
		if e.Tick <= bar+settle {
			upTo = append(upTo, e)
		}
	}
	wet := controls(upTo)
	want := []string{
		fmt.Sprintf("%d pc ch%d %d 0", bar-tl.leadTicks(), hall.Channel, hall.Program),
		nodeCC(bar, verb.Bypass, 0),
		nodeCC(bar+settle, mix, 127),
		nodeCC(bar+settle, size, 64),
	}
	if strings.Join(wet, "\n") != strings.Join(want, "\n") {
		t.Errorf("into wet:\n got:\n  %s\n want:\n  %s", strings.Join(wet, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestUnbypassUnderARamp checks a ramp that starts as its node comes back on
// is not set back to its start when the parameters follow the un-bypass: the
// parameter is sent with the value the ramp has reached, or not at all where
// the ramp steps at that very tick.
func TestUnbypassUnderARamp(t *testing.T) {
	settle := (&Timeline{Tempo: 120}).settleTicks()
	bar := int64(4 * PPQ)
	for _, tc := range []struct {
		name string
		to   string
	}{
		// 127 steps, about 15 ticks apart: some before the parameters follow.
		{"steps before", "1"},
		// One step every settle ticks: the first lands where they follow.
		{"a step right then", fmt.Sprintf("%.6f", float64(bar/settle)/127)},
	} {
		if tc.name == "a step right then" && (bar%settle != 0 || bar/settle > 127) {
			t.Logf("%s: a bar of %d ticks does not divide into steps of %d", tc.name, bar, settle)
			continue
		}
		doc := strings.Replace(unbypassSong, "  wet:  {bars: 1, scene: wet, play: {pad: p}}",
			"  wet:  {bars: 1, scene: wet, play: {pad: p}, automation: [{track: pad, effect: verb, param: Mix, from: 0, to: "+tc.to+"}]}", 1)
		tl, p := compileTest(t, doc)
		mix := paramAt(t, p.Node("pad", "verb"), "Mix")
		again := bar + settle
		var steps []Event // the Mix in the wet section
		for _, e := range tl.Events {
			if e.Kind == Control && e.Channel == mix.Channel && e.Data1 == mix.Number && e.Tick >= bar && e.Tick < 2*bar {
				steps = append(steps, e)
			}
		}
		atAgain := 0
		for i, e := range steps {
			if e.Tick == again {
				atAgain++
			}
			if i > 0 && e.Data2 < steps[i-1].Data2 {
				t.Errorf("%s: the Mix goes back from %d to %d at tick %d", tc.name, steps[i-1].Data2, e.Data2, e.Tick)
			}
		}
		if atAgain != 1 {
			t.Errorf("%s: %d Mix messages where the parameters follow the un-bypass (tick %d), want one: %v", tc.name, atAgain, again, controls(steps))
		}
		if len(steps) == 0 || steps[0].Tick == bar && steps[0].Data2 == 0 {
			t.Errorf("%s: the ramp's start is sent with the un-bypass: %v", tc.name, controls(steps))
		}
	}
}

// TestPlayerSendsAgainAfterTheSwitch checks a playback that opens with a
// section that switches a node back on sends the node's parameters once more,
// unbypassSettle after the opening state and before the first note.
func TestPlayerSendsAgainAfterTheSwitch(t *testing.T) {
	tl, _ := compileTest(t, strings.Replace(unbypassSong, "tempo: 120", "tempo: 400", 1))
	var rec recorder
	var p Player
	if _, err := p.Start("test", tl, PlayOptions{From: 1, Count: 1, Preroll: 20 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if final := waitDone(t, &p); final.Outcome != "finished" {
		t.Fatalf("final status = %+v", final)
	}
	got := rec.snapshot()
	state, again := tl.Sections[1].State, tl.Sections[1].Again
	if len(again) == 0 {
		t.Fatal("the wet section has no Again")
	}
	for i, e := range append(append([]Event(nil), state...), again...) {
		if got[i] != e {
			t.Fatalf("opening event %d = %s, want %s", i, describe(got[i]), describe(e))
		}
	}
	rec.mu.Lock()
	gap := rec.at[len(state)].Sub(rec.at[len(state)-1])
	rec.mu.Unlock()
	if gap < unbypassSettle {
		t.Errorf("Again follows the opening state after %v, want %v at the least", gap, unbypassSettle)
	}
	// The section's own sending of them after the un-bypass is the Again.
	for _, e := range got[len(state)+len(again):] {
		if e.Kind == Control {
			t.Errorf("after the opening the playback sends %s", describe(e))
		}
	}
}

// TestSequenceSendsAgainAfterTheSwitch checks the lead-in carries the
// opening section's Again unbypassSettle after the rest of its state, ahead
// of the first note.
func TestSequenceSendsAgainAfterTheSwitch(t *testing.T) {
	tl, _ := compileTest(t, strings.Replace(unbypassSong, "arrangement: [dry, wet, held, back]", "arrangement: [wet, dry]", 1))
	again := tl.Sections[0].Again
	if len(again) == 0 {
		t.Fatal("the opening section has no Again")
	}
	q := tl.Sequence()
	want := tl.settleTicks() // no preset to load: the state goes out at 0
	var found []string
	for _, e := range q.Controls {
		if e.Tick == want {
			found = append(found, fmt.Sprintf("cc ch%d %d %d", e.Channel, e.Data1, e.Data2))
		}
	}
	var wantFound []string
	for _, e := range again {
		wantFound = append(wantFound, fmt.Sprintf("cc ch%d %d %d", e.Channel, e.Data1, e.Data2))
	}
	if strings.Join(found, ";") != strings.Join(wantFound, ";") {
		t.Errorf("the lead-in at tick %d holds %v, want the Again %v", want, found, wantFound)
	}
	if want >= q.LeadTicks() {
		t.Errorf("Again at tick %d, after the lead-in of %d", want, q.LeadTicks())
	}
}
