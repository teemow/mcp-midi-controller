package song

// This file compiles a song into a timeline: every note and every control
// change as a MIDI message at a tick.
//
// Sound is compiled as state, not as a script. A section's state is each
// control's home value (what the track declares) overlaid with the section's
// scenes. Moving from one section to the next emits only what differs, and
// every section also carries its complete state, so playback can start at any
// section and sound the same as if it had played from the top.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// EventKind is the kind of a timeline event. The order is the order events
// sharing a tick are sent in: a note ends before the next begins, and a sound
// changes before the note that is meant to have it.
type EventKind int

const (
	NoteOff EventKind = iota
	Program
	Control
	NoteOn
)

// Event is one MIDI message of a compiled song.
type Event struct {
	// Tick is the position from the song's start, PPQ ticks to a quarter note.
	Tick int64     `json:"tick"`
	Kind EventKind `json:"kind"`
	// Channel is the MIDI channel, 1-based.
	Channel int `json:"channel"`
	// Data1 is the note, controller or program; Data2 the velocity or value.
	Data1 int `json:"data1"`
	Data2 int `json:"data2"`
	// Track is the id of the track a note belongs to ("" for a control).
	Track string `json:"track,omitempty"`
	// Section is the arrangement entry the event belongs to: the one a note
	// sounds in, a control change leads into, or a ramp runs in.
	Section int `json:"-"`
}

// SectionSpan is one entry of the arrangement placed on the timeline.
type SectionSpan struct {
	Name string `json:"name"`
	// Tick is where the section starts and Bar its first bar, 1-based.
	Tick int64 `json:"tick"`
	Bar  int   `json:"bar"`
	Bars int   `json:"bars"`
	// State is every control's value at the section's start: the messages
	// that put the session into the section's sound from any state.
	State []Event `json:"-"`
}

// TrackSummary is one track's part in a compiled song.
type TrackSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel int    `json:"channel"`
	Notes   int    `json:"notes"`
}

// Timeline is a compiled song.
type Timeline struct {
	Title    string  `json:"title"`
	Tempo    float64 `json:"tempo"`
	MeterNum int     `json:"-"`
	MeterDen int     `json:"-"`
	BarTicks int64   `json:"-"`
	// Ticks is the song's length.
	Ticks    int64          `json:"-"`
	Bars     int            `json:"bars"`
	Sections []SectionSpan  `json:"sections"`
	Tracks   []TrackSummary `json:"tracks"`
	// Events are sorted by tick, then by kind.
	Events []Event `json:"-"`
	// Fingerprint is the plan's (see Plan.Fingerprint), "" without a plan.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// TickTime converts a tick position into time from the song's start.
func (t *Timeline) TickTime(tick int64) time.Duration {
	return time.Duration(float64(tick) * float64(time.Minute) / (t.Tempo * PPQ))
}

// Duration is the song's length.
func (t *Timeline) Duration() time.Duration { return t.TickTime(t.Ticks) }

// leadTicks is presetLead at the song's tempo.
func (t *Timeline) leadTicks() int64 {
	return int64(float64(presetLead) * t.Tempo * PPQ / float64(time.Minute))
}

// presetLead is how far ahead of a section a preset load is sent, so the
// plugin has the patch in place when the section's first note arrives and the
// parameters sent at the section's start are not overwritten by the load. A
// plugin loads in wall-clock time, so the lead is time, whatever the tempo.
//
// A plugin is deaf while it loads: measured on a device, two of five
// instruments swallowed every note sent up to 250 ms after the Program Change
// and played the ones from 275 ms on, but still lost two in sixteen at 300 ms
// and none in thirty-two at 350 and 400 ms; the others answered within 150 ms.
// 400 ms leaves the slow ones a margin. The price is the 400 ms before the
// section: what that track plays there is cut, lost or already played with
// the new preset. A load due before the song's start is sent at the start.
const presetLead = 400 * time.Millisecond

// rampSpacingTicks is the closest two steps of a ramp are placed.
const rampSpacingTicks = PPQ / 32

// Compile turns a song into its timeline. With a plan the timeline carries the
// control messages that give every section its sound; with a nil plan it is
// the notes alone (which needs no plugins).
func Compile(s *Song, p *Plan) (*Timeline, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	m, _ := parseMeter(s.TimeSignature)
	tl := &Timeline{Title: s.Title, Tempo: s.Tempo, MeterNum: m.num, MeterDen: m.den, BarTicks: m.barTicks}
	if p != nil {
		tl.Fingerprint = p.Fingerprint()
	}

	// Each (track, pattern) pair compiles once.
	type patKey struct{ track, pattern string }
	patterns := map[patKey]*compiledPattern{}
	pattern := func(t *Track, id string) (*compiledPattern, error) {
		key := patKey{t.ID, id}
		if cp, ok := patterns[key]; ok {
			return cp, nil
		}
		cp, err := compilePattern(id, s.Patterns[id], m, s.Swing, t)
		if err != nil {
			return nil, err
		}
		patterns[key] = cp
		return cp, nil
	}

	type note struct {
		on, off   int64
		note, vel int
		section   int
	}
	trackNotes := make([][]note, len(s.Tracks))

	var current map[Target]int // each control's value as the timeline runs
	var tick int64
	for idx, name := range s.Arrangement {
		sec := s.Sections[name]
		start := tick
		end := start + int64(sec.Bars)*m.barTicks
		span := SectionSpan{Name: name, Tick: start, Bar: int(start/m.barTicks) + 1, Bars: sec.Bars}

		for ti := range s.Tracks {
			t := &s.Tracks[ti]
			ids := sec.Play[t.ID]
			if len(ids) == 0 {
				continue
			}
			pos := start
			for i := 0; pos < end; i++ {
				cp, err := pattern(t, ids[i%len(ids)])
				if err != nil {
					return nil, err
				}
				for _, n := range cp.notes {
					on := pos + n.at
					if n.at >= cp.ticks || on >= end {
						continue
					}
					off := on + n.dur
					if off > end {
						off = end
					}
					trackNotes[ti] = append(trackNotes[ti], note{on: on, off: off, note: n.note, vel: n.vel, section: idx})
				}
				pos += cp.ticks
			}
		}

		if p != nil {
			want, err := p.sectionState(name, sec)
			if err != nil {
				return nil, err
			}
			ramps, err := p.sectionRamps(name, sec, start, end, m.barTicks)
			if err != nil {
				return nil, err
			}
			// A ramp that starts with the section sets the section's opening
			// value, so the control does not jump to its home first.
			for _, r := range ramps {
				if r.start == start {
					want.set(r.target, r.at, r.from)
				}
			}
			span.State = want.events(0, 0)
			from := len(tl.Events)
			if idx > 0 {
				tl.Events = append(tl.Events, want.diff(current, start, tl.leadTicks())...)
			}
			current = want.values()
			for _, r := range ramps {
				tl.Events = append(tl.Events, r.events(current[r.target])...)
				current[r.target] = r.to
			}
			for i := from; i < len(tl.Events); i++ {
				tl.Events[i].Section = idx
			}
		}

		tl.Sections = append(tl.Sections, span)
		tick = end
	}
	tl.Ticks = tick
	tl.Bars = int(tick / m.barTicks)

	for ti := range s.Tracks {
		t := &s.Tracks[ti]
		notes := trackNotes[ti]
		sort.SliceStable(notes, func(i, j int) bool { return notes[i].on < notes[j].on })
		// A note struck again before it ended would be cut short by the first
		// note's release: end the first one where the second begins.
		last := map[int]int{}
		for i, n := range notes {
			if j, ok := last[n.note]; ok && notes[j].off > n.on {
				notes[j].off = n.on
			}
			last[n.note] = i
		}
		ch := s.TrackChannel(ti)
		for _, n := range notes {
			if n.off <= n.on {
				continue
			}
			tl.Events = append(tl.Events,
				Event{Tick: n.on, Kind: NoteOn, Channel: ch, Data1: n.note, Data2: n.vel, Track: t.ID, Section: n.section},
				Event{Tick: n.off, Kind: NoteOff, Channel: ch, Data1: n.note, Track: t.ID, Section: n.section},
			)
		}
		tl.Tracks = append(tl.Tracks, TrackSummary{ID: t.ID, Name: t.TrackName(), Channel: ch, Notes: len(notes)})
	}

	sort.SliceStable(tl.Events, func(i, j int) bool {
		a, b := tl.Events[i], tl.Events[j]
		if a.Tick != b.Tick {
			return a.Tick < b.Tick
		}
		return a.Kind < b.Kind
	})
	return tl, nil
}

// setting is one control's value in a section, with the message that sets it.
type setting struct {
	target Target
	at     Assignment
	value  int // the CC value or the program
}

// state is every control's value in a section, in the order it is sent.
type state struct {
	settings []setting
	index    map[Target]int
}

func (st *state) set(t Target, at Assignment, value int) {
	if i, ok := st.index[t]; ok {
		st.settings[i].value = value
		return
	}
	if st.index == nil {
		st.index = map[Target]int{}
	}
	st.index[t] = len(st.settings)
	st.settings = append(st.settings, setting{target: t, at: at, value: value})
}

func (st *state) values() map[Target]int {
	out := make(map[Target]int, len(st.settings))
	for _, s := range st.settings {
		out[s.target] = s.value
	}
	return out
}

// event renders a setting as its MIDI message.
func (s setting) event(tick int64) Event {
	if s.at.Kind == MsgPC {
		return Event{Tick: tick, Kind: Program, Channel: s.at.Channel, Data1: s.value}
	}
	return Event{Tick: tick, Kind: Control, Channel: s.at.Channel, Data1: s.at.Number, Data2: s.value}
}

// events renders the whole state: the preset loads at presetTick and
// everything else at tick, presets first.
func (st *state) events(presetTick, tick int64) []Event {
	var out []Event
	for _, s := range st.settings {
		if s.target.Kind == ControlPreset {
			out = append(out, s.event(presetTick))
		}
	}
	for _, s := range st.settings {
		if s.target.Kind != ControlPreset {
			out = append(out, s.event(tick))
		}
	}
	return out
}

// diff renders what must change to get from prev to this state at tick. A
// preset load is sent a little early, and since loading a preset resets the
// plugin's parameters, every parameter of a node whose preset changed is sent
// again.
func (st *state) diff(prev map[Target]int, tick, lead int64) []Event {
	presetTick := tick - lead
	if presetTick < 0 {
		presetTick = 0
	}
	type nodeKey struct{ track, effect string }
	reloaded := map[nodeKey]bool{}
	var out []Event
	for _, s := range st.settings {
		if s.target.Kind != ControlPreset {
			continue
		}
		if old, ok := prev[s.target]; !ok || old != s.value {
			out = append(out, s.event(presetTick))
			reloaded[nodeKey{s.target.Track, s.target.Effect}] = true
		}
	}
	for _, s := range st.settings {
		if s.target.Kind == ControlPreset {
			continue
		}
		old, ok := prev[s.target]
		changed := !ok || old != s.value
		if s.target.Kind == ControlParam && reloaded[nodeKey{s.target.Track, s.target.Effect}] {
			changed = true
		}
		if changed {
			out = append(out, s.event(tick))
		}
	}
	return out
}

// sectionState computes a section's state: every control's home value,
// overlaid with the section's scenes in order.
func (p *Plan) sectionState(name string, sec Section) (*state, error) {
	type nodeWant struct {
		preset string
		bypass bool
		params map[string]Value // by parameter key
	}
	nodes := map[*NodePlan]*nodeWant{}
	apply := func(n *NodePlan, ns NodeState) error {
		w := nodes[n]
		if ns.Preset != "" {
			w.preset = ns.Preset
		}
		if ns.Bypass != nil {
			w.bypass = *ns.Bypass
		}
		for pn, v := range ns.Params {
			pp, err := n.param(pn)
			if err != nil {
				return err
			}
			w.params[pp.Key] = v
		}
		return nil
	}
	for _, n := range p.allNodes() {
		nodes[n] = &nodeWant{params: map[string]Value{}}
		if err := apply(n, n.home); err != nil {
			return nil, err
		}
	}
	type stripWant struct {
		level float64
		mute  bool
	}
	strips := map[*TrackPlan]*stripWant{}
	for _, tp := range p.Tracks {
		strips[tp] = &stripWant{level: tp.homeLevel, mute: tp.homeMute}
	}

	for _, sn := range sec.Scene {
		scene := p.song.Scenes[sn]
		for _, tid := range sortedKeys(scene) {
			ts := scene[tid]
			if tp := p.Track(tid); tp != nil {
				if ts.Level != nil {
					strips[tp].level = *ts.Level
				}
				if ts.Mute != nil {
					strips[tp].mute = *ts.Mute
				}
			}
			if n := p.Node(tid, ""); n != nil {
				if err := apply(n, ts.NodeState); err != nil {
					return nil, err
				}
			}
			for _, fx := range sortedKeys(ts.Effects) {
				if n := p.Node(tid, fx); n != nil {
					if err := apply(n, ts.Effects[fx]); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	st := &state{}
	for _, n := range p.allNodes() {
		w := nodes[n]
		if w.preset != "" {
			pr, err := n.preset(w.preset)
			if err != nil {
				return nil, err
			}
			st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlPreset},
				Assignment{Kind: MsgPC, Channel: GlobalChannel}, pr.Program)
		}
		st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlBypass}, n.Bypass, boolWire(w.bypass))
		for _, pp := range n.Params {
			v, ok := w.params[pp.Key]
			if !ok {
				continue
			}
			wire, err := paramWire(pp.param, v)
			if err != nil {
				return nil, fmt.Errorf("/sections/%s: %s: %w", name, nodeLabel(n), err)
			}
			st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlParam, Param: pp.Key}, pp.At, wire)
		}
	}
	for _, tp := range p.Tracks {
		w := strips[tp]
		if w.level > MaxLevelDB {
			return nil, fmt.Errorf("/sections/%s: %s level %g dB is above the fader's +%g dB", name, tp.ID, w.level, MaxLevelDB)
		}
		st.set(Target{Track: tp.ID, Kind: ControlLevel}, tp.Level, levelWire(w.level))
		st.set(Target{Track: tp.ID, Kind: ControlMute}, tp.Mute, boolWire(w.mute))
	}
	return st, nil
}

// ramp is one compiled automation ramp.
type ramp struct {
	target     Target
	at         Assignment
	from, to   int
	start, end int64
}

// events renders the ramp as CC steps, skipping a first step that repeats the
// value the control already has.
func (r ramp) events(current int) []Event {
	steps := r.to - r.from
	if steps < 0 {
		steps = -steps
	}
	if most := int((r.end - r.start) / rampSpacingTicks); steps > most {
		steps = most
	}
	if steps < 1 {
		steps = 1
	}
	var out []Event
	last := current
	for k := 0; k <= steps; k++ {
		v := r.from + (r.to-r.from)*k/steps
		if v == last {
			continue
		}
		last = v
		out = append(out, Event{
			Tick: r.start + (r.end-r.start)*int64(k)/int64(steps), Kind: Control,
			Channel: r.at.Channel, Data1: r.at.Number, Data2: v,
		})
	}
	return out
}

// sectionRamps compiles a section's automation.
func (p *Plan) sectionRamps(name string, sec Section, start, end, barTicks int64) ([]ramp, error) {
	var out []ramp
	for i, a := range sec.Automation {
		where := fmt.Sprintf("/sections/%s/automation/%d", name, i)
		r := ramp{start: start, end: end}
		if a.Bar > 1 {
			r.start = start + int64(a.Bar-1)*barTicks
		}
		if a.Bars > 0 && r.start+int64(a.Bars)*barTicks < end {
			r.end = r.start + int64(a.Bars)*barTicks
		}
		if r.end <= r.start {
			return nil, fmt.Errorf("%s: the ramp starts after the section ends", where)
		}

		if a.Effect == "" && strings.EqualFold(a.Param, "level") {
			tp := p.Track(a.Track)
			if tp == nil {
				return nil, fmt.Errorf("%s: %q has no fader to automate", where, a.Track)
			}
			if a.From.IsStr || a.To.IsStr || a.From.Num > MaxLevelDB || a.To.Num > MaxLevelDB {
				return nil, fmt.Errorf("%s: a level ramp runs between two dB values up to +%g", where, MaxLevelDB)
			}
			r.target = Target{Track: tp.ID, Kind: ControlLevel}
			r.at, r.from, r.to = tp.Level, levelWire(a.From.Num), levelWire(a.To.Num)
		} else {
			n := p.Node(a.Track, a.Effect)
			if n == nil {
				return nil, fmt.Errorf("%s: no such plugin", where)
			}
			pp, err := n.param(a.Param)
			if err != nil {
				return nil, fmt.Errorf("%s/param: %w", where, err)
			}
			from, err := paramWire(pp.param, a.From)
			if err != nil {
				return nil, fmt.Errorf("%s/from: %w", where, err)
			}
			to, err := paramWire(pp.param, a.To)
			if err != nil {
				return nil, fmt.Errorf("%s/to: %w", where, err)
			}
			r.target = Target{Track: n.Track, Effect: n.Effect, Kind: ControlParam, Param: pp.Key}
			r.at, r.from, r.to = pp.At, from, to
		}
		out = append(out, r)
	}
	return out, nil
}
