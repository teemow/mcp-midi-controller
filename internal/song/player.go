package song

// This file plays a compiled song against the wall clock. The player is what
// lets an agent hear a song it wrote: it sets the session to the opening
// section's sound, waits for the plugins to settle, then sends every event when
// it is due. It knows nothing of the transport it sends through; the caller
// hands it a Sender.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sender delivers one event. It is called from the player's own goroutine, in
// order.
type Sender func(ctx context.Context, e Event) error

// PlayOptions selects what of a song is played.
type PlayOptions struct {
	// From is the arrangement entry to start at (0-based) and Count how many
	// entries to play (0 plays to the end).
	From, Count int
	// Tracks limits the notes to these tracks; empty plays them all. The
	// sound of every track is set either way.
	Tracks []string
	// Preroll is how long the player waits between setting the opening sound
	// and the first note, so loaded presets are in place. 0 takes the default.
	Preroll time.Duration
	// Before and After are sent as the first note is due and once the last has
	// ended — the session's transport start and stop, when wanted.
	Before, After []Event
	// OnEnd, when set, is called once with the playback's final status when it
	// is over, however it ended: finished, stopped or failed. It runs on the
	// playback's goroutine after the last event went out, and Stop (and so the
	// next Start) waits for it: it must not block or start another playback.
	// A Start that returns an error began no playback and does not call it.
	OnEnd func(Status)
}

// Status is where a playback stands.
type Status struct {
	Playing bool   `json:"playing"`
	Song    string `json:"song,omitempty"`
	// Section is the arrangement entry being played and Bar the song's bar.
	Section      string `json:"section,omitempty"`
	SectionIndex int    `json:"section_index"`
	Bar          int    `json:"bar,omitempty"`
	PositionMS   int64  `json:"position_ms"`
	DurationMS   int64  `json:"duration_ms"`
	// Sent counts the events delivered so far.
	Sent int `json:"sent"`
	// Outcome is how the last playback ended: "finished", "stopped" or the
	// error that ended it. Empty while playing.
	Outcome string `json:"outcome,omitempty"`
}

const (
	defaultPreroll = 1200 * time.Millisecond
	// ccOnlyPreroll is enough when no preset has to load.
	ccOnlyPreroll = 150 * time.Millisecond
	// flushTimeout bounds the note-offs sent after a playback is cut short.
	flushTimeout = 2 * time.Second
	// sendTimeout bounds one event's delivery.
	sendTimeout = 2 * time.Second
)

// Player plays one song at a time.
type Player struct {
	// starting serializes Start, so two starts cannot both stop the old
	// playback and then both install their own.
	starting sync.Mutex

	mu  sync.Mutex
	cur *playback
	// last is the status of the most recent playback once it has ended.
	last Status
}

// playback is one run of the player.
type playback struct {
	id     string
	tl     *Timeline
	cancel context.CancelFunc
	done   chan struct{}

	startTick, endTick int64

	mu      sync.Mutex
	started time.Time // when the first note was due; zero during the preroll
	sent    int
	outcome string
}

// Start plays tl (the compiled song id) from the top, or as opts selects,
// stopping whatever was playing. It returns once playback has begun; the song
// plays on in the background until it ends or Stop is called.
func (p *Player) Start(id string, tl *Timeline, opts PlayOptions, send Sender) (Status, error) {
	if len(tl.Sections) == 0 {
		return Status{}, fmt.Errorf("the song has no sections")
	}
	if opts.From < 0 || opts.From >= len(tl.Sections) {
		return Status{}, fmt.Errorf("from %d is not an arrangement entry (0..%d)", opts.From, len(tl.Sections)-1)
	}
	last := len(tl.Sections) - 1
	if opts.Count > 0 && opts.From+opts.Count-1 < last {
		last = opts.From + opts.Count - 1
	}
	only := map[string]bool{}
	for _, t := range opts.Tracks {
		found := false
		for _, ts := range tl.Tracks {
			found = found || ts.ID == t
		}
		if !found {
			return Status{}, fmt.Errorf("the song has no track %q", t)
		}
		only[t] = true
	}

	p.starting.Lock()
	defer p.starting.Unlock()
	p.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	pb := &playback{
		id: id, tl: tl, cancel: cancel, done: make(chan struct{}),
		startTick: tl.Sections[opts.From].Tick,
		endTick:   tl.Sections[last].Tick + int64(tl.Sections[last].Bars)*tl.BarTicks,
	}

	// The opening state replaces the starting section's own control changes
	// up to the start; after it, the timeline's own changes apply. A preset
	// that leads into a later section is kept even when it is due at or
	// before the start (the sections between are shorter than the lead): it
	// goes out as the playback starts, or that section would play with the
	// wrong preset. A change that leads into the section after the last one
	// played (a preset sent early) is left out.
	var events []Event
	for _, e := range tl.Events {
		if e.Section < opts.From || e.Section > last {
			continue
		}
		switch e.Kind {
		case NoteOn, NoteOff:
			if len(only) > 0 && !only[e.Track] {
				continue
			}
		default:
			if e.Section == opts.From && e.Tick <= pb.startTick {
				continue
			}
		}
		events = append(events, e)
	}
	opening := tl.Sections[opts.From].State

	p.mu.Lock()
	p.cur = pb
	p.mu.Unlock()

	go func() {
		defer close(pb.done)
		outcome := pb.run(ctx, opening, events, opts, send)
		pb.mu.Lock()
		pb.outcome = outcome
		pb.mu.Unlock()
		final := pb.status()
		p.mu.Lock()
		if p.cur == pb {
			p.last = final
			p.cur = nil
		}
		p.mu.Unlock()
		if opts.OnEnd != nil {
			opts.OnEnd(final)
		}
	}()
	return pb.status(), nil
}

// run sends the opening state and then every event when it is due. It returns
// how the playback ended.
func (pb *playback) run(ctx context.Context, opening, events []Event, opts PlayOptions, send Sender) string {
	// errStopped marks a delivery skipped because the playback was stopped.
	errStopped := errors.New("stopped")
	// deliver sends one event. Stopping is noticed between events, never
	// within one: an event goes out on a context of its own, because a send
	// abandoned halfway can cost the connection the note-offs still need.
	deliver := func(e Event) error {
		if ctx.Err() != nil {
			return errStopped
		}
		sctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		err := send(sctx, e)
		cancel()
		if err != nil {
			return err
		}
		pb.mu.Lock()
		pb.sent++
		pb.mu.Unlock()
		return nil
	}
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}

	// Presets first, a pause for them to load, then everything they would
	// otherwise overwrite, and a pause again before the first note.
	preroll := opts.Preroll
	presets := 0
	for _, e := range opening {
		if e.Kind == Program {
			presets++
			if err := deliver(e); err != nil {
				return err.Error()
			}
		}
	}
	if preroll <= 0 {
		preroll = defaultPreroll
		if presets == 0 {
			preroll = ccOnlyPreroll
		}
	}
	if presets > 0 && !wait(preroll/2) {
		return "stopped"
	}
	for _, e := range opening {
		if e.Kind != Program {
			if err := deliver(e); err != nil {
				return err.Error()
			}
		}
	}
	if !wait(preroll / 2) {
		return "stopped"
	}

	type voice struct{ channel, note int }
	sounding := map[voice]bool{}
	// release ends every note still sounding. It runs when the playback is
	// cut short, so it must not depend on the cancelled context.
	release := func() {
		fctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		defer cancel()
		for v := range sounding {
			_ = send(fctx, Event{Kind: NoteOff, Channel: v.channel, Data1: v.note})
		}
		for _, e := range opts.After {
			_ = send(fctx, e)
		}
	}

	start := time.Now()
	pb.mu.Lock()
	pb.started = start
	pb.mu.Unlock()
	for _, e := range opts.Before {
		if err := deliver(e); err != nil {
			release()
			return err.Error()
		}
	}
	for _, e := range events {
		due := start.Add(pb.tl.TickTime(e.Tick - pb.startTick))
		if d := time.Until(due); d > 0 && !wait(d) {
			release()
			return "stopped"
		}
		if err := deliver(e); err != nil {
			release()
			return err.Error()
		}
		v := voice{e.Channel, e.Data1}
		switch e.Kind {
		case NoteOn:
			sounding[v] = true
		case NoteOff:
			delete(sounding, v)
		}
	}
	// Let the last bar run out before reporting the song finished.
	if d := time.Until(start.Add(pb.tl.TickTime(pb.endTick - pb.startTick))); d > 0 && !wait(d) {
		release()
		return "stopped"
	}
	release()
	return "finished"
}

// status reports where the playback stands.
func (pb *playback) status() Status {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	tl := pb.tl
	st := Status{
		Song:       pb.id,
		Playing:    pb.outcome == "",
		DurationMS: tl.TickTime(pb.endTick - pb.startTick).Milliseconds(),
		Sent:       pb.sent,
		Outcome:    pb.outcome,
	}
	tick := pb.startTick
	if !pb.started.IsZero() {
		elapsed := time.Since(pb.started)
		if total := tl.TickTime(pb.endTick - pb.startTick); elapsed > total || pb.outcome == "finished" {
			elapsed = total
		}
		st.PositionMS = elapsed.Milliseconds()
		tick += int64(float64(elapsed) * tl.Tempo * PPQ / float64(time.Minute))
	}
	if tick >= pb.endTick {
		tick = pb.endTick - 1 // the end belongs to the last section played
	}
	for i, sec := range tl.Sections {
		if sec.Tick <= tick {
			st.Section, st.SectionIndex = sec.Name, i
		}
	}
	st.Bar = int(tick/tl.BarTicks) + 1
	if st.Bar > tl.Bars {
		st.Bar = tl.Bars
	}
	return st
}

// Stop ends the current playback, releasing every sounding note, and reports
// where it stood. With nothing playing it reports the last playback's end.
func (p *Player) Stop() Status {
	p.mu.Lock()
	pb := p.cur
	p.mu.Unlock()
	if pb == nil {
		return p.Status()
	}
	pb.cancel()
	<-pb.done
	return p.Status()
}

// Status reports the current playback, or how the last one ended.
func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil {
		return p.cur.status()
	}
	return p.last
}
