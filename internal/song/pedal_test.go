package song

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2/smf"
)

// pedalSong is a bass and a pedalled piano on channel 2. The piano's pattern
// is two bars: the pedal goes down with the first chord, changes after the
// second and is then held past the pattern's end.
const pedalSong = `
title: Pedal Song
tempo: 120
tracks:
  - id: bass
    instrument: {plugin: synth}
  - id: piano
    instrument: {plugin: synth}
patterns:
  riff:
    step: 1/4
    notes: "A1 . A1 . | G1 . G1 ."
  chords:
    bars: 2
    events:
      - {at: 0, dur: 1, note: "A3 C4 E4"}
      - {at: 4, dur: 1, note: "G3 B3 D4"}
    pedal:
      - {at: 4.25, dur: 5}
      - {at: 0, dur: 3.75}
sections:
  one: {bars: 2, play: {bass: riff, piano: chords}}
  two: {bars: 3, play: {bass: riff, piano: chords}}
arrangement: [one, two]
`

// pedalling returns the pedal changes among evs as "beat down|up".
func pedalling(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if !e.pedal() {
			continue
		}
		move := "up"
		if e.Data2 > 0 {
			move = "down"
		}
		out = append(out, fmt.Sprintf("%g %s", float64(e.Tick)/PPQ, move))
	}
	return out
}

func pedalTimeline(t *testing.T, doc string) *Timeline {
	t.Helper()
	tl, err := Compile(mustParse(t, doc), nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return tl
}

func TestCompilePedal(t *testing.T) {
	tl := pedalTimeline(t, pedalSong)

	// Section one ends at beat 8 and cuts the second stretch there. In section
	// two the pattern comes round at beat 16 while the stretch from 12.25 still
	// holds the pedal: it changes there. The stretch due at beat 20.25 is past
	// the section's end.
	want := "0 down, 3.75 up, 4.25 down, 8 up, 8 down, 11.75 up, 12.25 down, 16 up, 16 down, 19.75 up"
	if got := strings.Join(pedalling(tl.Events), ", "); got != want {
		t.Errorf("pedalling = %s\nwant        %s", got, want)
	}
	for _, e := range tl.Events {
		if !e.pedal() {
			continue
		}
		section := 0
		if e.Data2 > 0 && e.Tick >= 8*PPQ || e.Data2 == 0 && e.Tick > 8*PPQ {
			section = 1
		}
		if e.Channel != 2 || e.Data1 != SustainPedal || (e.Data2 != 0 && e.Data2 != 127) || e.Track != "piano" || e.Section != section {
			t.Errorf("pedal change %s (track %q, section %d): want controller 64 at 0 or 127 on the piano's channel 2, in section %d",
				describe(e), e.Track, e.Section, section)
		}
	}
	if got := tl.Tracks[1].Pedal; got != 5 {
		t.Errorf("the piano's pedal goes down %d times, want 5", got)
	}
	if got := tl.Tracks[0].Pedal; got != 0 {
		t.Errorf("the bass has a pedal count of %d, want none", got)
	}

	// Where the pedal comes up and a chord is struck on one tick, the old
	// notes end first, then the pedal moves, then the new ones begin.
	var at8 []EventKind
	for _, e := range tl.Events {
		if e.Tick == 8*PPQ && (e.Track == "piano") {
			at8 = append(at8, e.Kind)
		}
	}
	if fmt.Sprint(at8) != fmt.Sprint([]EventKind{Control, Control, NoteOn, NoteOn, NoteOn}) {
		t.Errorf("the piano's events at beat 8 = %v, want the pedal up and down, then the chord", at8)
	}
}

func TestPedalChecks(t *testing.T) {
	for _, tc := range []struct{ name, old, new, want string }{
		{"no length", "{at: 0, dur: 3.75}", "{at: 0, dur: 0}", "/patterns/chords/pedal/1: the pedal needs at >= 0 and dur > 0"},
		{"before the start", "{at: 0, dur: 3.75}", "{at: -1, dur: 2}", "/patterns/chords/pedal/1: the pedal needs at >= 0 and dur > 0"},
		{"control notes", "    bars: 2\n", "    bars: 2\n    channel: 10\n", "/patterns/chords/pedal: a pattern with a channel is control notes"},
	} {
		s, err := Parse([]byte(strings.Replace(pedalSong, tc.old, tc.new, 1)))
		if err == nil {
			err = s.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error with %q", tc.name, err, tc.want)
		}
	}
	// A misspelled key is reported, as everywhere in a song.
	if _, err := Parse([]byte(strings.Replace(pedalSong, "{at: 0, dur: 3.75}", "{at: 0, len: 3.75}", 1))); err == nil || !strings.Contains(err.Error(), "len") {
		t.Errorf("a pedal with an unknown key: %v, want an error naming it", err)
	}
}

// TestPedalExtendsThePattern: without bars, a pattern is as long as its last
// event or pedal stretch begins.
func TestPedalExtendsThePattern(t *testing.T) {
	doc := strings.Replace(pedalSong, "    bars: 2\n", "", 1)
	doc = strings.Replace(doc, "{at: 4.25, dur: 5}", "{at: 8.5, dur: 1}", 1)
	tl := pedalTimeline(t, doc)
	// Three bars now: in section one (two bars) the stretch at 8.5 is never
	// reached; section two (three bars, from beat 8) plays it at 16.5.
	want := "0 down, 3.75 up, 8 down, 11.75 up, 16.5 down, 17.5 up"
	if got := strings.Join(pedalling(tl.Events), ", "); got != want {
		t.Errorf("pedalling = %s\nwant        %s", got, want)
	}
}

func TestSequenceKeepsThePedal(t *testing.T) {
	tl := pedalTimeline(t, pedalSong)
	q := tl.Sequence()
	lead := q.LeadTicks()
	if lead == 0 {
		t.Fatal("the sequence has no lead-in")
	}
	// The pedal that goes down with the first note is no part of the opening
	// state: it stays, behind the lead-in like every note.
	var got []string
	for _, e := range q.Controls {
		e.Tick -= lead
		got = append(got, pedalling([]Event{e})...)
	}
	if want := pedalling(tl.Events); strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the sequence's pedalling = %v, want the timeline's %v", got, want)
	}
	if chs := q.pedalChannels(); len(chs) != 1 || chs[0] != 2 {
		t.Errorf("pedal channels = %v, want [2]", chs)
	}

	// The sequencer's clip carries it as raw Control Changes on channel 2.
	first := atomPatternOf(q).Events[0]
	if first.Time != atomTime(lead) || first.Byte1 != 0xB1 || first.Byte2 != SustainPedal || first.Byte3 != 127 {
		t.Errorf("the clip's first event = %+v, want the pedal down (B1 40 7F) at the first bar", first)
	}

	// A song without a pedal has none to lift.
	if chs := pedalTimeline(t, testSong).Sequence().pedalChannels(); len(chs) != 0 {
		t.Errorf("pedal channels of a song without a pedal = %v", chs)
	}
}

// lifted reports whether the last message on the piano's pedal is a pedal up.
func lifted(evs []Event) (ups, downs int, last Event) {
	for _, e := range evs {
		if e.Kind == Control && e.Channel == 2 && e.Data1 == SustainPedal {
			last = e
			if e.Data2 > 0 {
				downs++
			} else {
				ups++
			}
		}
	}
	return ups, downs, last
}

func TestPlayerStopLiftsThePedal(t *testing.T) {
	tl := pedalTimeline(t, pedalSong) // the pedal is down from the first note for 1.9 s
	var rec recorder
	var p Player
	if _, err := p.Start("test", tl, PlayOptions{Preroll: 10 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if st := p.Stop(); st.Outcome != "stopped" {
		t.Fatalf("status after Stop = %+v", st)
	}
	if ups, downs, last := lifted(rec.snapshot()); downs != 1 || ups != 1 || last.Data2 != 0 {
		t.Errorf("stopped under the pedal: %d down, %d up, last %s; want it down once and lifted by the stop", downs, ups, describe(last))
	}
}

func TestPlayerPlaysThePedalWithItsTrack(t *testing.T) {
	tl := pedalTimeline(t, strings.Replace(pedalSong, "tempo: 120", "tempo: 400", 1))

	// From the second section: the pedal that goes down on the section's first
	// tick is played, since the opening state does not stand for it. Every
	// stretch ends within the section, so the end has nothing left to lift.
	var rec recorder
	var p Player
	if _, err := p.Start("test", tl, PlayOptions{From: 1, Preroll: 5 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := waitDone(t, &p); st.Outcome != "finished" {
		t.Fatalf("outcome = %q", st.Outcome)
	}
	if ups, downs, _ := lifted(rec.snapshot()); downs != 3 || ups != 3 {
		t.Errorf("section two sent %d pedal downs and %d ups, want 3 and 3", downs, ups)
	}

	// The bass alone: the piano's pedal stays out of it.
	rec = recorder{}
	if _, err := p.Start("test", tl, PlayOptions{Count: 1, Tracks: []string{"bass"}, Preroll: 5 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, &p)
	if ups, downs, _ := lifted(rec.snapshot()); downs != 0 || ups != 0 {
		t.Errorf("the bass alone sent %d pedal downs and %d ups, want none", downs, ups)
	}
	if len(rec.snapshot()) == 0 {
		t.Error("the bass alone sent nothing")
	}
}

func TestPlayerSequencedLiftsThePedal(t *testing.T) {
	tl := pedalTimeline(t, pedalSong)
	q := tl.Sequence()
	var rec recorder
	var p Player
	if _, err := p.StartSequenced("test", tl, q, SequencedOptions{Rewind: seqRewind, Start: seqStart, Stop: seqStop}, rec.send); err != nil {
		t.Fatalf("StartSequenced: %v", err)
	}
	time.Sleep(rewindSettle + 100*time.Millisecond)
	p.Stop()
	// The sequencer plays the pedal itself, so the player sends none of it: only
	// the lift, after the transport's stop.
	got := rec.snapshot()
	want := Event{Kind: Control, Channel: 2, Data1: SustainPedal}
	if n := len(seqRewind) + len(seqStart) + len(seqStop) + 1; len(got) != n || got[len(got)-1] != want || got[len(got)-2] != seqStop[1] {
		t.Errorf("a sequenced song stopped under the pedal sent %v, want the rewind, the start, the stop and then the pedal up on channel 2", got)
	}
}

func TestSMFCarriesThePedal(t *testing.T) {
	tl := pedalTimeline(t, pedalSong)
	file, err := smf.ReadFrom(strings.NewReader(string(tl.SMF())))
	if err != nil {
		t.Fatalf("the exported file does not read back: %v", err)
	}
	// Conductor, bass, piano. Without a plan there is no control track, and
	// the pedal is no part of one: it is in the piano's own track.
	if len(file.Tracks) != 3 {
		t.Fatalf("file has %d tracks, want 3", len(file.Tracks))
	}
	count := func(track smf.Track) (n int) {
		for _, ev := range track {
			var ch, cc, val uint8
			if ev.Message.GetControlChange(&ch, &cc, &val) && ch == 1 && cc == SustainPedal {
				n++
			}
		}
		return n
	}
	if bass, piano := count(file.Tracks[1]), count(file.Tracks[2]); bass != 0 || piano != 10 {
		t.Errorf("pedal changes: %d in the bass track, %d in the piano's; want 0 and 10", bass, piano)
	}

	// With a plan the control track holds the session's controls and not the
	// pedal a second time.
	withPlan, _ := compileTest(t, pedalSong)
	file, err = smf.ReadFrom(strings.NewReader(string(withPlan.SMF())))
	if err != nil {
		t.Fatalf("the exported file does not read back: %v", err)
	}
	if len(file.Tracks) != 4 {
		t.Fatalf("file has %d tracks, want 4", len(file.Tracks))
	}
	if n := count(file.Tracks[3]); n != 0 {
		t.Errorf("the control track holds %d pedal changes, want none", n)
	}
}
