package song

// This file lays a compiled song out for a sequencer hosted in the song's own
// session. Played that way the session carries the whole song: the notes, and
// every control change at its place. Starting the session's transport is all
// it takes, and the timing is the host's, not the network's.
//
// A sequence is the timeline with a lead-in in front of it. The lead-in holds
// what the live player sends before its first note: the opening section's
// preset loads, a pause for them to land, then every other control. It is a
// whole number of bars, so the song's bars stay on the transport's bar lines
// and tempo-synced effects fall where they do when the song is played live.

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// SeqNote is one note of a sequence.
type SeqNote struct {
	// Tick is where the note starts, from the transport's start, and Ticks how
	// long it sounds.
	Tick  int64
	Ticks int64
	// Channel is the MIDI channel, 1-based.
	Channel  int
	Note     int
	Velocity int
}

// Sequence is a compiled song as a sequencer plays it from the top of the
// transport.
type Sequence struct {
	Tempo    float64
	MeterNum int
	MeterDen int
	BarTicks int64
	// LeadBars is the lead-in ahead of the song's first bar.
	LeadBars int
	// Ticks is the whole length, lead-in included.
	Ticks int64
	// Notes are sorted by start.
	Notes []SeqNote
	// Controls are the Program and Control events, sorted by tick: the opening
	// state inside the lead-in, then the timeline's own.
	Controls []Event
}

// LeadTicks is the length of the lead-in.
func (q *Sequence) LeadTicks() int64 { return int64(q.LeadBars) * q.BarTicks }

// TickTime converts a tick position into time from the transport's start.
func (q *Sequence) TickTime(tick int64) time.Duration {
	return time.Duration(float64(tick) * float64(time.Minute) / (q.Tempo * PPQ))
}

// Lead is how long the lead-in lasts, and Duration the whole sequence.
func (q *Sequence) Lead() time.Duration     { return q.TickTime(q.LeadTicks()) }
func (q *Sequence) Duration() time.Duration { return q.TickTime(q.Ticks) }

// RecorderOffBeats is how far ahead of its last bar line a stem's sequence
// switches AUM's recorder off, in beats (quarter notes). AUM ends a take on
// the next bar line after that, so the take ends where the sequence does, as
// long as the lead-in, the song and the tail. The sequencer sends it, with
// the transport's own timing: a take ends by itself, also when the daemon
// that started it is gone, and a recorder that nobody switches off records on
// through a stopped transport.
const RecorderOffBeats = 0.5

// Sequence lays the whole song out for a sequencer.
func (t *Timeline) Sequence() *Sequence {
	q := &Sequence{Tempo: t.Tempo, MeterNum: t.MeterNum, MeterDen: t.MeterDen, BarTicks: t.BarTicks}

	var opening []Event
	if len(t.Sections) > 0 {
		opening = t.Sections[0].State
	}
	presets := 0
	for _, e := range opening {
		if e.Kind == Program {
			presets++
		}
	}
	// The same pauses the live player takes: presets, half the preroll, the
	// rest, half the preroll, the first note.
	preroll := defaultPreroll
	if presets == 0 {
		preroll = ccOnlyPreroll
	}
	bar := t.TickTime(t.BarTicks)
	q.LeadBars = 1
	if bar > 0 {
		q.LeadBars = max(1, int(math.Ceil(float64(preroll)/float64(bar))))
	}
	// A song can ask for a longer lead-in than its opening sound needs: the
	// one its recordings were made with (SessionSpec.LeadBars).
	q.LeadBars = max(q.LeadBars, t.LeadBars)
	lead := q.LeadTicks()
	rest := int64(0)
	if presets > 0 {
		rest = t.ticksOf(preroll / 2)
	}
	for _, e := range opening {
		e.Tick = rest
		if e.Kind == Program {
			e.Tick = 0
		}
		q.Controls = append(q.Controls, e)
	}

	type voice struct{ channel, note int }
	open := map[voice]int{} // voice -> index of its sounding note
	for _, e := range t.Events {
		switch e.Kind {
		case NoteOn:
			open[voice{e.Channel, e.Data1}] = len(q.Notes)
			q.Notes = append(q.Notes, SeqNote{Tick: lead + e.Tick, Channel: e.Channel, Note: e.Data1, Velocity: e.Data2})
		case NoteOff:
			v := voice{e.Channel, e.Data1}
			if i, ok := open[v]; ok {
				q.Notes[i].Ticks = lead + e.Tick - q.Notes[i].Tick
				delete(open, v)
			}
		default:
			// The opening state stands for the first section's own changes up
			// to its start. A later section's preset load that is due that
			// early (the sections before it are shorter than the lead) stays,
			// and so does a pedal that goes down with the first note.
			if e.Section == 0 && e.Tick <= 0 && !e.pedal() {
				continue
			}
			e.Tick += lead
			q.Controls = append(q.Controls, e)
		}
	}
	q.Ticks = lead + t.Ticks
	// A stem's sequence ends the take it is recorded into: see
	// RecorderOffBeats.
	if t.recorderOff != nil {
		at := max(q.Ticks-int64(RecorderOffBeats*PPQ), lead)
		q.Controls = append(q.Controls, Event{Tick: at, Kind: Control, Channel: t.recorderOff.Channel, Data1: t.recorderOff.Number})
	}
	// A note the timeline never ends runs to the end of the song.
	for _, i := range open {
		q.Notes[i].Ticks = q.Ticks - q.Notes[i].Tick
	}
	sort.SliceStable(q.Notes, func(i, j int) bool { return q.Notes[i].Tick < q.Notes[j].Tick })
	sort.SliceStable(q.Controls, func(i, j int) bool {
		a, b := q.Controls[i], q.Controls[j]
		if a.Tick != b.Tick {
			return a.Tick < b.Tick
		}
		return a.Kind < b.Kind
	})
	return q
}

// pedalChannels lists the channels the sequence presses the sustain pedal on.
func (q *Sequence) pedalChannels() []int {
	var out []int
	for _, e := range q.Controls {
		if e.pedal() && !slices.Contains(out, e.Channel) {
			out = append(out, e.Channel)
		}
	}
	sort.Ints(out)
	return out
}

// ticksOf converts a duration into ticks at the song's tempo.
func (t *Timeline) ticksOf(d time.Duration) int64 {
	return int64(float64(d) * t.Tempo * PPQ / float64(time.Minute))
}

// Fingerprint identifies what the sequence plays: two sequences with the same
// fingerprint hold the same notes and control changes at the same places. A
// session's sequencer holds the song as it was when the session was authored,
// so a song whose sequence fingerprint has changed since plays its old self
// from the sequencer until the session is authored and loaded again.
func (q *Sequence) Fingerprint() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%g|%d/%d|%d|%d|%d|", q.Tempo, q.MeterNum, q.MeterDen, q.BarTicks, q.LeadBars, q.Ticks)
	for _, n := range q.Notes {
		fmt.Fprintf(&b, "n%d:%d:%d:%d:%d|", n.Tick, n.Ticks, n.Channel, n.Note, n.Velocity)
	}
	for _, e := range q.Controls {
		fmt.Fprintf(&b, "c%d:%d:%d:%d:%d|", e.Tick, e.Kind, e.Channel, e.Data1, e.Data2)
	}
	return fmt.Sprintf("%016x", fnv64(b.String()))
}
