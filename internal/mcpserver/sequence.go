package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teemow/midi-transport/midicontrol"
)

// The sequence feature lets one play_notes / probe_sound call execute a timed
// phrase of notes AND control changes — overlapping note-ons for legato/glide
// (which a mono synth like the Model D needs to glide at all), CCs fired
// mid-phrase, chords, retriggers — instead of one tool round trip per event.
// See docs/research/model-d.md ("Glide needs legato") for the live session that
// motivated it.
const (
	// maxSequenceSteps bounds the user-facing step list so a single call stays
	// reviewable and cannot encode unbounded work.
	maxSequenceSteps = 64
	// seqDefaultNoteMS is a note step's hold when duration_ms is omitted,
	// matching play_notes' default.
	seqDefaultNoteMS = 500
)

// sequenceSchema is the shared JSON-schema fragment for the sequence[] argument
// of play_notes and probe_sound, so both tools describe identical steps.
const sequenceSchema = `{
	"type": "array",
	"description": "Timed phrase executed in one call (alternative to notes/note). Each step is a note step {note|notes, velocity?, duration_ms?} or a control step {cc, value}. Steps run back-to-back when at_ms is omitted (a control step occupies no time); set at_ms (ms from sequence start) to overlap notes (legato/glide) or place events precisely. The whole sequence must end within 10s.",
	"items": {
		"type": "object",
		"properties": {
			"at_ms": {"type": "integer", "minimum": 0, "description": "Step start in ms from sequence start. Default: where the previous step ended."},
			"note": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Single note (note step)."},
			"notes": {"type": "array", "items": {"type": "integer", "minimum": 0, "maximum": 127}, "description": "Chord notes (note step)."},
			"velocity": {"type": "integer", "minimum": 1, "maximum": 127, "description": "Note-on velocity (default: the call-level velocity)."},
			"duration_ms": {"type": "integer", "minimum": 0, "description": "Note hold before note-off (default 500)."},
			"cc": {"type": "integer", "minimum": 0, "maximum": 127, "description": "CC controller number (control step)."},
			"value": {"type": "integer", "minimum": 0, "maximum": 127, "description": "CC value (control step)."},
			"channel": {"type": "integer", "minimum": 1, "maximum": 16, "description": "Per-step MIDI channel (default: the call-level channel)."}
		}
	}
}`

// seqStep is one user-facing step of a timed sequence: a note step
// (note/notes + velocity/duration_ms) or a control step (cc + value). at_ms
// places the step on the timeline relative to the sequence start; when omitted
// the step starts where the previous step ended (a plain sequential phrase),
// while an explicit at_ms expresses overlap (legato) or precise placement.
type seqStep struct {
	AtMS       *int  `json:"at_ms"`
	Notes      []int `json:"notes"`
	Note       *int  `json:"note"`
	Velocity   *int  `json:"velocity"`
	DurationMS *int  `json:"duration_ms"`
	CC         *int  `json:"cc"`
	Value      *int  `json:"value"`
	Channel    int   `json:"channel"`
}

// Event ordering ranks for events sharing a timestamp: note-offs first (a
// retrigger of the same note must release before it re-attacks), then CCs (a
// control change lands before the note it is meant to shape), then note-ons.
const (
	seqRankNoteOff = iota
	seqRankCC
	seqRankNoteOn
)

// seqEvent is one compiled wire event of a sequence.
type seqEvent struct {
	at   int // ms offset from sequence start
	rank int
	cmd  midicontrol.Command
}

// compileSequence expands steps into a time-sorted wire-event list, applying
// the sequential-by-default timeline (at_ms omitted -> the step starts at the
// previous step's end; control steps occupy no time). It validates each step
// with an RFC-6901-style /sequence/<i> pointer and enforces the overall
// maxNoteDurationMS span so a sequence cannot block the daemon longer than a
// held play_notes call could. Returns the events and the total span in ms.
func compileSequence(steps []seqStep, defVelocity, defChannel int) ([]seqEvent, int, error) {
	if len(steps) > maxSequenceSteps {
		return nil, 0, fmt.Errorf("/sequence: %d steps, max %d", len(steps), maxSequenceSteps)
	}
	var events []seqEvent
	cursor := 0 // where the next at_ms-less step starts
	span := 0
	for i, st := range steps {
		at := cursor
		if st.AtMS != nil {
			if *st.AtMS < 0 {
				return nil, 0, fmt.Errorf("/sequence/%d/at_ms: must be >= 0", i)
			}
			at = *st.AtMS
		}
		ch := defChannel
		if st.Channel != 0 {
			ch = clampChannel(st.Channel)
		}

		notes := st.Notes
		if st.Note != nil {
			notes = append(notes, *st.Note)
		}
		isNote := len(notes) > 0
		isCC := st.CC != nil
		switch {
		case isNote && isCC:
			return nil, 0, fmt.Errorf("/sequence/%d: provide note(s) or cc, not both", i)
		case isNote:
			for j, n := range notes {
				if n < 0 || n > 127 {
					return nil, 0, fmt.Errorf("/sequence/%d/notes/%d: note must be in [0, 127]", i, j)
				}
			}
			vel := defVelocity
			if st.Velocity != nil {
				vel = clamp7(*st.Velocity)
			}
			dur := seqDefaultNoteMS
			if st.DurationMS != nil {
				dur = *st.DurationMS
			}
			if dur < 0 {
				dur = 0
			}
			for _, n := range notes {
				events = append(events,
					seqEvent{at: at, rank: seqRankNoteOn, cmd: midicontrol.Command{Type: "noteOn", Channel: ch, Note: n, Velocity: vel}},
					seqEvent{at: at + dur, rank: seqRankNoteOff, cmd: midicontrol.Command{Type: "noteOff", Channel: ch, Note: n}},
				)
			}
			cursor = at + dur
			if at+dur > span {
				span = at + dur
			}
		case isCC:
			if st.Value == nil {
				return nil, 0, fmt.Errorf("/sequence/%d/value: cc step needs a 0-127 value", i)
			}
			events = append(events, seqEvent{at: at, rank: seqRankCC,
				cmd: midicontrol.Command{Type: "cc", Channel: ch, Controller: clamp7(*st.CC), Value: clamp7(*st.Value)}})
			cursor = at // a control step occupies no time
			if at > span {
				span = at
			}
		default:
			return nil, 0, fmt.Errorf("/sequence/%d: provide note(s) or cc", i)
		}
	}
	if len(events) == 0 {
		return nil, 0, fmt.Errorf("/sequence: no steps")
	}
	if span > maxNoteDurationMS {
		return nil, 0, fmt.Errorf("/sequence: ends at %dms, max %dms", span, maxNoteDurationMS)
	}
	sort.SliceStable(events, func(a, b int) bool {
		if events[a].at != events[b].at {
			return events[a].at < events[b].at
		}
		return events[a].rank < events[b].rank
	})
	return events, span, nil
}

// seqFlushTimeout bounds the best-effort note-off flush that releases
// still-sounding notes after a mid-sequence failure or cancellation.
const seqFlushTimeout = 2 * time.Second

// runSequence plays compiled events against the wall clock via send, blocking
// for the sequence's span. A mid-sequence failure (or context cancellation)
// does not leave a hung chord: every note still sounding gets a best-effort
// note-off through the same sender before the error returns.
func runSequence(ctx context.Context, send func(context.Context, seqEvent) error, events []seqEvent) error {
	type voice struct{ ch, note int }
	active := map[voice]bool{}
	flush := func() {
		// flush runs exactly when ctx may already be cancelled, so the
		// note-offs go out on a detached, time-boxed context.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seqFlushTimeout)
		defer cancel()
		for v := range active {
			_ = send(fctx, seqEvent{rank: seqRankNoteOff,
				cmd: midicontrol.Command{Type: "noteOff", Channel: v.ch, Note: v.note}})
		}
	}

	start := time.Now()
	for _, ev := range events {
		if wait := time.Until(start.Add(time.Duration(ev.at) * time.Millisecond)); wait > 0 {
			select {
			case <-ctx.Done():
				flush()
				return fmt.Errorf("sequence cancelled at %dms: %w", ev.at, ctx.Err())
			case <-time.After(wait):
			}
		}
		if err := send(ctx, ev); err != nil {
			flush()
			return fmt.Errorf("sequence event at %dms (%s) failed: %w", ev.at, ev.cmd.Type, err)
		}
		v := voice{ch: ev.cmd.Channel, note: ev.cmd.Note}
		switch ev.cmd.Type {
		case "noteOn":
			active[v] = true
		case "noteOff":
			delete(active, v)
		}
	}
	return nil
}

// seqNotes returns the distinct notes of the sequence's note-ons in first-played
// order — the note list for summaries and the probe WAV filename tag.
func seqNotes(events []seqEvent) []int {
	seen := map[int]bool{}
	var notes []int
	for _, ev := range events {
		if ev.cmd.Type == "noteOn" && !seen[ev.cmd.Note] {
			seen[ev.cmd.Note] = true
			notes = append(notes, ev.cmd.Note)
		}
	}
	return notes
}

// seqCounts tallies the compiled events for summaries: note-ons and CCs.
func seqCounts(events []seqEvent) (noteOns, ccs int) {
	for _, ev := range events {
		switch ev.cmd.Type {
		case "noteOn":
			noteOns++
		case "cc":
			ccs++
		}
	}
	return
}

// resolveSeqDefaults turns the call-level velocity/channel arguments into the
// defaults that sequence note steps inherit (shared by play_notes and
// probe_sound's sequence paths).
func resolveSeqDefaults(velocity *int, channel int) (defVelocity, defChannel int) {
	defVelocity = 100
	if velocity != nil {
		defVelocity = clamp7(*velocity)
	}
	return defVelocity, clampChannel(channel)
}

// seqSender returns the event sender for a sequence: the brain channel by
// default, or raw bytes over a hardware transport when the caller targeted one.
func (s *Server) seqSender(t midiTarget) func(context.Context, seqEvent) error {
	hardware := t.usesHardware()
	return func(ctx context.Context, ev seqEvent) error {
		if !hardware {
			return s.sendBrain(ctx, ev.cmd)
		}
		c := ev.cmd
		var raw []byte
		switch c.Type {
		case "noteOn":
			raw = []byte{0x90 | byte(c.Channel-1), byte(c.Note), byte(c.Velocity)}
		case "noteOff":
			raw = []byte{0x80 | byte(c.Channel-1), byte(c.Note), 0}
		case "cc":
			raw = []byte{0xB0 | byte(c.Channel-1), byte(c.Controller), byte(c.Value)}
		default:
			return fmt.Errorf("sequence: unsupported event type %q", c.Type)
		}
		return s.sendRawBytes(ctx, t, raw)
	}
}
