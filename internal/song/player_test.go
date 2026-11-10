package song

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2/smf"
)

// recorder is a Sender that keeps what it was sent and when.
type recorder struct {
	mu     sync.Mutex
	events []Event
	at     []time.Time
	fail   int // fail the n-th send (1-based); 0 never
}

func (r *recorder) send(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 && len(r.events)+1 == r.fail {
		return errors.New("brain gone")
	}
	r.events = append(r.events, e)
	r.at = append(r.at, time.Now())
	return nil
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// fastSong is the test song at a tempo that plays its six bars in 1.2 s.
func fastSong(t *testing.T) (*Timeline, *Plan) {
	t.Helper()
	return compileTest(t, strings.Replace(testSong, "tempo: 120", "tempo: 400", 1))
}

// waitDone waits for the player to finish and returns its final status.
func waitDone(t *testing.T, p *Player) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := p.Status(); !st.Playing {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("playback did not finish")
	return Status{}
}

func TestPlayerPlaysTheSong(t *testing.T) {
	tl, _ := fastSong(t)
	var rec recorder
	var p Player
	st, err := p.Start("test", tl, PlayOptions{Preroll: 20 * time.Millisecond}, rec.send)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !st.Playing || st.Song != "test" || st.DurationMS != tl.Duration().Milliseconds() {
		t.Errorf("status at start = %+v", st)
	}
	final := waitDone(t, &p)
	if final.Outcome != "finished" || final.PositionMS != final.DurationMS {
		t.Errorf("final status = %+v, want finished at the end", final)
	}

	got := rec.snapshot()
	opening := len(tl.Sections[0].State)
	if len(got) != opening+len(tl.Events) || final.Sent != len(got) {
		t.Fatalf("sent %d events (status says %d), want the opening state (%d) and the timeline (%d)", len(got), final.Sent, opening, len(tl.Events))
	}
	// The opening state leads, presets first; then the timeline in order.
	for i := 0; i < opening; i++ {
		if got[i].Kind != Program && got[i].Kind != Control {
			t.Fatalf("event %d of the opening state is a note: %s", i, describe(got[i]))
		}
		if i > 0 && got[i].Kind == Program && got[i-1].Kind == Control {
			t.Fatal("a preset load follows a control change in the opening state")
		}
	}
	for i, e := range tl.Events {
		if got[opening+i] != e {
			t.Fatalf("event %d = %s, want %s", i, describe(got[opening+i]), describe(e))
		}
	}
	// Events are sent when due, not all at once: the song takes its length.
	rec.mu.Lock()
	span := rec.at[len(rec.at)-1].Sub(rec.at[opening])
	rec.mu.Unlock()
	if want := tl.Duration(); span < want*8/10 || span > want*13/10 {
		t.Errorf("the timeline was sent over %v, want about %v", span, want)
	}
}

func TestPlayerPlaysAPart(t *testing.T) {
	tl, _ := fastSong(t)
	var rec recorder
	var p Player
	before := Event{Kind: Control, Channel: 15, Data1: 102, Data2: 127}
	after := Event{Kind: Control, Channel: 15, Data1: 103, Data2: 127}
	// Only the chorus (entry 1), only the lead.
	_, err := p.Start("test", tl, PlayOptions{
		From: 1, Count: 1, Tracks: []string{"lead"}, Preroll: 20 * time.Millisecond,
		Before: []Event{before}, After: []Event{after},
	}, rec.send)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitDone(t, &p)
	if final.Section != "chorus" || final.Outcome != "finished" {
		t.Errorf("final status = %+v, want the chorus finished", final)
	}
	got := rec.snapshot()
	// The chorus's own state opens the playback, so it sounds as it does in
	// the song although the verse before it was skipped.
	state := tl.Sections[1].State
	for i, e := range state {
		if got[i] != e {
			t.Fatalf("opening event %d = %s, want the chorus state's %s", i, describe(got[i]), describe(e))
		}
	}
	if got[len(state)] != before || got[len(got)-1] != after {
		t.Errorf("the transport events do not frame the notes: %s … %s", describe(got[len(state)]), describe(got[len(got)-1]))
	}
	notes := got[len(state)+1 : len(got)-1]
	if len(notes) != 20 {
		t.Errorf("played %d note events, want the lead's 10 notes on and off", len(notes))
	}
	for _, e := range notes {
		if e.Track != "lead" {
			t.Fatalf("played %s of track %q, want only the lead", describe(e), e.Track)
		}
	}
}

// shortSectionSong has a one-bar pickup shorter than presetLead (300 ms at this
// tempo and meter) ahead of a verse that changes the lead's preset. The verse's
// preset load is therefore due before the pickup starts.
const shortSectionSong = `
title: Short Section
tempo: 400
time_signature: 2/4
tracks:
  - id: lead
    instrument: {plugin: synth, preset: Warm Pad}
patterns:
  a:
    step: 1/4
    notes: "A3 ."
scenes:
  bright:
    lead: {preset: Bright Lead}
sections:
  pickup:
    bars: 1
    play: {lead: a}
  verse:
    bars: 2
    scene: bright
    play: {lead: a}
arrangement: [pickup, verse, pickup, verse]
`

// TestPlayerSendsLoadsDueBeforeTheStart checks a preset load that leads into a
// later section is sent although it is due at or before the playback's start:
// the opening state stands for the starting section's sound only. Without the
// load, the section after a short one would play with the wrong preset.
func TestPlayerSendsLoadsDueBeforeTheStart(t *testing.T) {
	tl, _ := compileTest(t, shortSectionSong)
	if short := tl.TickTime(tl.Sections[1].Tick); short >= presetLead {
		t.Fatalf("the pickup lasts %v, the test needs it shorter than the preset lead (%v)", short, presetLead)
	}
	for _, c := range []struct {
		name string
		from int
	}{
		{"from the top", 0},    // the verse's load is clamped to the song's start
		{"from the pickup", 2}, // the verse's load is due before the pickup starts
	} {
		t.Run(c.name, func(t *testing.T) {
			var want []Event
			for _, e := range tl.Events {
				if e.Kind == Program && e.Section == c.from+1 {
					want = append(want, e)
				}
			}
			if len(want) != 1 || want[0].Tick > tl.Sections[c.from].Tick {
				t.Fatalf("the verse's preset loads = %v, want one due at or before the pickup's start (tick %d)", want, tl.Sections[c.from].Tick)
			}
			var rec recorder
			var p Player
			if _, err := p.Start("test", tl, PlayOptions{From: c.from, Count: 2, Preroll: 20 * time.Millisecond}, rec.send); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if final := waitDone(t, &p); final.Outcome != "finished" {
				t.Fatalf("final status = %+v, want finished", final)
			}
			got := rec.snapshot()
			opening := len(tl.Sections[c.from].State)
			var loads []Event
			firstNote := -1
			for i, e := range got[opening:] {
				switch e.Kind {
				case Program:
					loads = append(loads, e)
					if firstNote >= 0 {
						t.Errorf("the verse's preset load was sent after the first note (event %d)", i)
					}
				case NoteOn:
					if firstNote < 0 {
						firstNote = i
					}
				}
			}
			if len(loads) != 1 || loads[0] != want[0] {
				t.Errorf("after the opening state the player sent the loads %v, want the verse's %s", loads, describe(want[0]))
			}
		})
	}
}

func TestPlayerStopReleasesNotes(t *testing.T) {
	tl, _ := compileTest(t, testSong) // 12 s at 120 BPM
	var rec recorder
	var p Player
	if _, err := p.Start("test", tl, PlayOptions{Preroll: 10 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Stop while the first notes sound.
	time.Sleep(120 * time.Millisecond)
	st := p.Stop()
	if st.Playing || st.Outcome != "stopped" {
		t.Errorf("status after Stop = %+v, want stopped", st)
	}
	sounding := map[[2]int]bool{}
	for _, e := range rec.snapshot() {
		switch e.Kind {
		case NoteOn:
			sounding[[2]int{e.Channel, e.Data1}] = true
		case NoteOff:
			delete(sounding, [2]int{e.Channel, e.Data1})
		}
	}
	if len(sounding) != 0 {
		t.Errorf("notes left sounding after Stop: %v", sounding)
	}
	// Stopping again reports the same end, and a new song can start.
	if again := p.Stop(); again.Outcome != "stopped" {
		t.Errorf("second Stop = %+v", again)
	}
}

func TestPlayerReportsSendFailure(t *testing.T) {
	tl, _ := fastSong(t)
	rec := recorder{fail: len(tl.Sections[0].State) + 5}
	var p Player
	if _, err := p.Start("test", tl, PlayOptions{Preroll: 10 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := waitDone(t, &p); st.Outcome != "brain gone" {
		t.Errorf("outcome = %q, want the send error", st.Outcome)
	}
}

// ends collects the statuses a playback's OnEnd was called with.
type ends struct {
	mu  sync.Mutex
	got []Status
	ch  chan Status
}

func newEnds() *ends { return &ends{ch: make(chan Status, 8)} }

func (e *ends) onEnd(st Status) {
	e.mu.Lock()
	e.got = append(e.got, st)
	e.mu.Unlock()
	e.ch <- st
}

func (e *ends) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.got)
}

// wait returns the status of the next playback to end.
func (e *ends) wait(t *testing.T) Status {
	t.Helper()
	select {
	case st := <-e.ch:
		return st
	case <-time.After(10 * time.Second):
		t.Fatal("OnEnd was not called")
		return Status{}
	}
}

// TestPlayerOnEndFinished checks a playback that runs to its end reports it
// once, after its last event, with the status the player then shows.
func TestPlayerOnEndFinished(t *testing.T) {
	tl, _ := fastSong(t)
	var rec recorder
	var p Player
	end := newEnds()
	after := Event{Kind: Control, Channel: 15, Data1: 103, Data2: 127}
	sentAtEnd := -1
	onEnd := func(st Status) {
		sentAtEnd = len(rec.snapshot())
		end.onEnd(st)
	}
	if _, err := p.Start("test", tl, PlayOptions{Preroll: 20 * time.Millisecond, After: []Event{after}, OnEnd: onEnd}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := end.count(); n != 0 {
		t.Fatalf("OnEnd was called %d time(s) while the song played", n)
	}
	st := end.wait(t)
	if st.Playing || st.Outcome != "finished" || st.Song != "test" || st.PositionMS != st.DurationMS {
		t.Errorf("OnEnd got %+v, want test finished at its end", st)
	}
	if got := p.Status(); got != st {
		t.Errorf("player status = %+v, OnEnd got %+v", got, st)
	}
	got := rec.snapshot()
	if sentAtEnd != len(got) || got[len(got)-1] != after {
		t.Errorf("OnEnd ran after %d of %d events; the last is %s", sentAtEnd, len(got), describe(got[len(got)-1]))
	}
	time.Sleep(50 * time.Millisecond)
	if n := end.count(); n != 1 {
		t.Errorf("OnEnd was called %d times, want once", n)
	}
}

// TestPlayerOnEndStopped checks Stop returns only once OnEnd has run, and that
// a playback replaced by the next Start ends as stopped before the new one
// begins.
func TestPlayerOnEndStopped(t *testing.T) {
	tl, _ := compileTest(t, testSong) // 12 s at 120 BPM
	var rec recorder
	var p Player
	first, second := newEnds(), newEnds()
	if _, err := p.Start("first", tl, PlayOptions{Preroll: 10 * time.Millisecond, OnEnd: first.onEnd}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := p.Start("second", tl, PlayOptions{Preroll: 10 * time.Millisecond, OnEnd: second.onEnd}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if first.count() != 1 || second.count() != 0 {
		t.Fatalf("after the second Start: first ended %d time(s), second %d; want 1 and 0", first.count(), second.count())
	}
	if st := first.wait(t); st.Song != "first" || st.Outcome != "stopped" {
		t.Errorf("the replaced playback ended as %+v, want first stopped", st)
	}
	time.Sleep(60 * time.Millisecond)
	st := p.Stop()
	if second.count() != 1 {
		t.Fatalf("Stop returned before OnEnd ran (%d calls)", second.count())
	}
	if got := second.wait(t); got != st || st.Song != "second" || st.Outcome != "stopped" {
		t.Errorf("OnEnd got %+v, Stop returned %+v; want second stopped", got, st)
	}
	// Stopping with nothing playing ends nothing.
	p.Stop()
	if first.count() != 1 || second.count() != 1 {
		t.Errorf("a second Stop called OnEnd again: first %d, second %d", first.count(), second.count())
	}
}

// TestPlayerOnEndFailed checks a playback ended by a send error reports that
// error, whether it struck in the opening state or among the notes.
func TestPlayerOnEndFailed(t *testing.T) {
	tl, _ := fastSong(t)
	for name, fail := range map[string]int{
		"opening state": 1,
		"notes":         len(tl.Sections[0].State) + 5,
	} {
		t.Run(name, func(t *testing.T) {
			rec := recorder{fail: fail}
			var p Player
			end := newEnds()
			if _, err := p.Start("test", tl, PlayOptions{Preroll: 10 * time.Millisecond, OnEnd: end.onEnd}, rec.send); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if st := end.wait(t); st.Playing || st.Outcome != "brain gone" || st.Sent != fail-1 {
				t.Errorf("OnEnd got %+v, want the send error after %d events", st, fail-1)
			}
			time.Sleep(50 * time.Millisecond)
			if n := end.count(); n != 1 {
				t.Errorf("OnEnd was called %d times, want once", n)
			}
		})
	}
}

func TestPlayerRejectsBadOptions(t *testing.T) {
	tl, _ := fastSong(t)
	var p Player
	var rec recorder
	end := newEnds()
	if _, err := p.Start("test", tl, PlayOptions{From: 3, OnEnd: end.onEnd}, rec.send); err == nil {
		t.Error("Start accepted a section past the arrangement")
	}
	if _, err := p.Start("test", tl, PlayOptions{Tracks: []string{"tuba"}, OnEnd: end.onEnd}, rec.send); err == nil {
		t.Error("Start accepted a track the song does not have")
	}
	if st := p.Status(); st.Playing {
		t.Errorf("a refused Start left the player playing: %+v", st)
	}
	if n := end.count(); n != 0 {
		t.Errorf("a refused Start called OnEnd %d time(s)", n)
	}
}

// TestSMF checks the exported file is a MIDI file another reader accepts, with
// the tempo, a track per song track and the notes in place.
func TestSMF(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	file, err := smf.ReadFrom(strings.NewReader(string(tl.SMF())))
	if err != nil {
		t.Fatalf("the exported file does not read back: %v", err)
	}
	if res, ok := file.TimeFormat.(smf.MetricTicks); !ok || res.Resolution() != PPQ {
		t.Errorf("time format = %v, want %d ticks per quarter", file.TimeFormat, PPQ)
	}
	// Conductor, three song tracks, control.
	if len(file.Tracks) != 5 {
		t.Fatalf("file has %d tracks, want 5", len(file.Tracks))
	}
	var bpm float64
	var markers []string
	for _, ev := range file.Tracks[0] {
		var text string
		if ev.Message.GetMetaTempo(&bpm) {
			continue
		}
		if ev.Message.GetMetaMarker(&text) {
			markers = append(markers, text)
		}
	}
	if bpm != 120 || strings.Join(markers, ",") != "verse,chorus,verse" {
		t.Errorf("conductor track: tempo %g, markers %v", bpm, markers)
	}

	// The bass track holds the bass's notes at their ticks, on channel 1.
	var name string
	var ons, abs int
	var first int64 = -1
	for _, ev := range file.Tracks[1] {
		abs += int(ev.Delta)
		var ch, key, vel uint8
		ev.Message.GetMetaTrackName(&name)
		if ev.Message.GetNoteOn(&ch, &key, &vel) {
			if ch != 0 {
				t.Fatalf("bass note on channel %d, want 1", ch+1)
			}
			if first < 0 {
				first = int64(key)
			}
			ons++
		}
	}
	if name != "bass" || ons != 21 || first != 33 || int64(abs) != tl.Ticks {
		t.Errorf("bass track: name %q, %d notes, first note %d, length %d ticks; want bass, 21, A1 (33), %d", name, ons, first, abs, tl.Ticks)
	}
}

// TestPlayerStopDuringBurst checks a stop that lands between two events of a
// burst ends the playback as stopped, and that no event is ever sent on a
// cancelled context — an abandoned send can cost the connection the note-offs
// still need.
func TestPlayerStopDuringBurst(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	var p Player
	var mu sync.Mutex
	cancelled := 0
	started := make(chan struct{})
	var once sync.Once
	slow := func(ctx context.Context, e Event) error {
		once.Do(func() { close(started) })
		time.Sleep(2 * time.Millisecond) // the opening state takes a while
		if ctx.Err() != nil {
			mu.Lock()
			cancelled++
			mu.Unlock()
		}
		return ctx.Err()
	}
	if _, err := p.Start("test", tl, PlayOptions{Preroll: time.Second}, slow); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	st := p.Stop()
	if st.Outcome != "stopped" {
		t.Errorf("outcome = %q, want stopped", st.Outcome)
	}
	if cancelled != 0 {
		t.Errorf("%d events were sent on a cancelled context", cancelled)
	}
}

// TestPlayerConcurrentStarts checks two starts at once leave exactly one
// playback, which Stop then ends: none keeps sending unseen.
func TestPlayerConcurrentStarts(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	var p Player
	var rec recorder
	if _, err := p.Start("first", tl, PlayOptions{Preroll: 10 * time.Millisecond}, rec.send); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Start(id, tl, PlayOptions{Preroll: 10 * time.Millisecond}, rec.send); err != nil {
				t.Errorf("Start %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if st := p.Stop(); st.Playing || st.Outcome != "stopped" {
		t.Errorf("status after Stop = %+v", st)
	}
	// Nothing may arrive once the one remaining playback is stopped.
	before := len(rec.snapshot())
	time.Sleep(700 * time.Millisecond) // the bass plays a note every 250 ms
	if after := len(rec.snapshot()); after != before {
		t.Errorf("%d events arrived after Stop: a playback was left running", after-before)
	}
}
