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

func TestPlayerRejectsBadOptions(t *testing.T) {
	tl, _ := fastSong(t)
	var p Player
	var rec recorder
	if _, err := p.Start("test", tl, PlayOptions{From: 3}, rec.send); err == nil {
		t.Error("Start accepted a section past the arrangement")
	}
	if _, err := p.Start("test", tl, PlayOptions{Tracks: []string{"tuba"}}, rec.send); err == nil {
		t.Error("Start accepted a track the song does not have")
	}
	if st := p.Status(); st.Playing {
		t.Errorf("a refused Start left the player playing: %+v", st)
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
