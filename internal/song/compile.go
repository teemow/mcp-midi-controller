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
	"math"
	"sort"
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
	// Track is the id of the track a note or a pedal change belongs to ("" for
	// a control of the session's sound).
	Track string `json:"track,omitempty"`
	// Section is the arrangement entry the event belongs to: the one a note
	// sounds in, a control change leads into, or a ramp runs in.
	Section int `json:"-"`
	// again marks a parameter sent once more after its node's un-bypass (see
	// unbypassSettle). A playback that opens with the section has it in the
	// section's SectionSpan.Again.
	again bool
}

// SustainPedal is the controller a pattern's pedal is sent as, on the channel
// of the track that plays the pattern: 127 down, 0 up.
const SustainPedal = 64

// pedal reports whether the event is a pedal change. It is a Control, but one
// that is part of a track's playing, as its notes are, and not of a section's
// sound: a section's opening state does not stand for it, and it is played
// only when its track is.
func (e Event) pedal() bool { return e.Kind == Control && e.Track != "" }

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
	// Again is the parameters of every node that is on in State but off
	// somewhere in the song, to be sent once more unbypassSettle after State:
	// the node may be bypassed in AUM when State reaches it, and AUM loses the
	// parameter changes that come with an un-bypass.
	Again []Event `json:"-"`
}

// TrackSummary is one track's part in a compiled song.
type TrackSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel int    `json:"channel"`
	// Notes counts the notes on the track's channel and ControlNotes those on
	// its control channels.
	Notes        int `json:"notes"`
	ControlNotes int `json:"control_notes,omitempty"`
	// Pedal counts how often the sustain pedal goes down.
	Pedal int `json:"pedal,omitempty"`
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
	// LeadBars is the lead-in the song asks its session's sequencer for, at
	// the least (SessionSpec.LeadBars); 0 leaves it to the opening sound.
	LeadBars int `json:"-"`
	// recorderOff is the control that switches AUM's recorder off, in the
	// timeline of a stem's song: its sequence sends it RecorderOffBeats
	// before its end. nil for any other song.
	recorderOff *Assignment
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

// settleTicks is unbypassSettle at the song's tempo, rounded up.
func (t *Timeline) settleTicks() int64 {
	return max(1, int64(math.Ceil(float64(unbypassSettle)*t.Tempo*PPQ/float64(time.Minute))))
}

// unbypassSettle is how long after a node's un-bypass its parameters are sent
// again. AUM loses the parameter changes that reach a plugin in the same burst
// of messages as its un-bypass, and one sent while the plugin is bypassed
// (see docs/songs.md, Status). So a node a section switches back on gets every
// parameter the song sets on it once more, this long after the un-bypass, the
// way a preset load is followed by them; and so does every node that is on in
// a section's opening state but off somewhere in the song.
const unbypassSettle = 50 * time.Millisecond

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
	tl := &Timeline{Title: s.Title, Tempo: s.Tempo, MeterNum: m.num, MeterDen: m.den, BarTicks: m.barTicks, LeadBars: s.SessionOptions().LeadBars}
	if p != nil {
		tl.Fingerprint = p.Fingerprint()
		if at, ok := p.Transport["Toggle Record"]; ok && s.stem {
			tl.recorderOff = &at
		}
	}

	// Each (track, pattern) pair compiles once; a calibrated drum track once
	// for every kit it plays.
	type patKey struct{ track, pattern, kit string }
	patterns := map[patKey]*compiledPattern{}
	pattern := func(t *Track, id string) (*compiledPattern, error) {
		key := patKey{t.ID, id, t.kitName}
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
		channel   int // a control note's channel, 0 for the track's own
		section   int
	}
	trackNotes := make([][]note, len(s.Tracks))
	type pedal struct {
		down, up int64
		section  int
	}
	trackPedals := make([][]pedal, len(s.Tracks))

	var current map[Target]int // each control's value as the timeline runs
	var states []*state        // each section's, in the arrangement's order
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
			// A calibrated song is written in sounding pitch and in its kit's
			// lanes: both follow the preset the track has in this section.
			shift := 0
			switch {
			case p == nil:
				t = s.laneTrack(t)
			case p.calibrating():
				tp := p.Tracks[ti]
				preset := p.sectionPreset(sec, tp.Nodes[0])
				if !tp.Drums {
					shift = p.pitchShift(tp, preset)
				} else if kit := p.kit(tp, preset); kit != nil {
					with := *t
					with.kit, with.kitName = kit, preset
					t = &with
				}
			}
			pos := start
			for i := 0; pos < end; i++ {
				cp, err := pattern(t, ids[i%len(ids)])
				if err != nil {
					if t.kit != nil {
						return nil, fmt.Errorf("/sections/%s/play/%s: %w", name, t.ID, err)
					}
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
					// A control note is sent as written: a preset's pitch
					// does not move a mute.
					pitch := n.note
					if n.channel == 0 {
						pitch += shift
					}
					if pitch < 0 || pitch > 127 {
						return nil, fmt.Errorf("/sections/%s/play/%s: %s shifted by %+d for its preset leaves the MIDI range", name, t.ID, NoteName(n.note), shift)
					}
					trackNotes[ti] = append(trackNotes[ti], note{on: on, off: off, note: pitch, vel: n.vel, channel: n.channel, section: idx})
				}
				// The pedal comes up with the section at the latest, as a note
				// ends with it: nothing carries over into the next section.
				for _, sp := range cp.pedals {
					down := pos + sp.at
					if sp.at >= cp.ticks || down >= end {
						continue
					}
					trackPedals[ti] = append(trackPedals[ti], pedal{down: down, up: min(down+sp.dur, end), section: idx})
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
			for _, r := range ramps {
				if mod, ok := want.held[r.target]; ok {
					return nil, fmt.Errorf("/sections/%s: %s moves %s here; a ramp of it needs the modulator bypassed", name, mod, r.target)
				}
			}
			// A ramp that starts with the section sets the section's opening
			// value, so the control does not jump to its home first.
			for _, r := range ramps {
				if r.start == start {
					want.set(r.target, r.at, r.from)
				}
			}
			span.State = want.events(0, 0)
			states = append(states, want)
			from := len(tl.Events)
			var woken map[nodeKey]bool
			if idx > 0 {
				woken = want.switchedOn(current)
				tl.Events = append(tl.Events, want.diff(current, woken, start, tl.leadTicks())...)
			}
			current = want.values()
			// again is when a node the section switches back on gets its
			// parameters, and stepped the last step each ramp has sent by
			// then.
			again := start + tl.settleTicks()
			stepped := map[Target]Event{}
			for _, r := range ramps {
				steps := r.events(current[r.target])
				for _, e := range steps {
					if e.Tick <= again {
						stepped[r.target] = e
					}
				}
				tl.Events = append(tl.Events, steps...)
				current[r.target] = r.to
			}
			// A ramp that has moved the parameter by then is where it
			// continues from, and one that steps right then sends it itself.
			for _, e := range want.params(woken, again) {
				if step, ok := stepped[e.target]; ok {
					if step.Tick == again {
						continue
					}
					e.Data2 = step.Data2
				}
				e.again = true
				tl.Events = append(tl.Events, e.Event)
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

	// A section's opening state may find any node the song bypasses, at home
	// (the session opens it bypassed) or in a section, still bypassed in AUM:
	// where the state has it on, its parameters follow once more.
	if p != nil {
		home, err := p.sectionState("", Section{})
		if err != nil {
			return nil, err
		}
		off := home.off()
		for _, st := range states {
			for n := range st.off() {
				off[n] = true
			}
		}
		for i, st := range states {
			here := st.off()
			on := map[nodeKey]bool{}
			for n := range off {
				if !here[n] {
					on[n] = true
				}
			}
			for _, e := range st.params(on, 0) {
				tl.Sections[i].Again = append(tl.Sections[i].Again, e.Event)
			}
		}
	}

	for ti := range s.Tracks {
		t := &s.Tracks[ti]
		notes := trackNotes[ti]
		sort.SliceStable(notes, func(i, j int) bool { return notes[i].on < notes[j].on })
		ch := s.TrackChannel(ti)
		for i := range notes {
			if notes[i].channel == 0 {
				notes[i].channel = ch
			}
		}
		// A note struck again before it ended would be cut short by the first
		// note's release: end the first one where the second begins.
		type voice struct{ channel, note int }
		last := map[voice]int{}
		for i, n := range notes {
			v := voice{n.channel, n.note}
			if j, ok := last[v]; ok && notes[j].off > n.on {
				notes[j].off = n.on
			}
			last[v] = i
		}
		sum := TrackSummary{ID: t.ID, Name: t.TrackName(), Channel: ch}
		for _, n := range notes {
			if n.channel == ch {
				sum.Notes++
			} else {
				sum.ControlNotes++
			}
			if n.off <= n.on {
				continue
			}
			tl.Events = append(tl.Events,
				Event{Tick: n.on, Kind: NoteOn, Channel: n.channel, Data1: n.note, Data2: n.vel, Track: t.ID, Section: n.section},
				Event{Tick: n.off, Kind: NoteOff, Channel: n.channel, Data1: n.note, Track: t.ID, Section: n.section},
			)
		}
		// There is one pedal: a stretch that begins while another holds it
		// down takes over, and the pedal comes up and goes down again there.
		pedals := trackPedals[ti]
		sort.SliceStable(pedals, func(i, j int) bool { return pedals[i].down < pedals[j].down })
		for i, pd := range pedals {
			if i+1 < len(pedals) && pedals[i+1].down < pd.up {
				pd.up = pedals[i+1].down
			}
			if pd.up <= pd.down {
				continue
			}
			sum.Pedal++
			tl.Events = append(tl.Events,
				Event{Tick: pd.down, Kind: Control, Channel: ch, Data1: SustainPedal, Data2: 127, Track: t.ID, Section: pd.section},
				Event{Tick: pd.up, Kind: Control, Channel: ch, Data1: SustainPedal, Track: t.ID, Section: pd.section},
			)
		}
		tl.Tracks = append(tl.Tracks, sum)
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
	// held is the parameters a running modulator moves in the section, with
	// the modulator's label: the state leaves them to it.
	held map[Target]string
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
// again. The parameters of the woken nodes, which the state switches back on,
// are left out: they are sent after the un-bypass (see unbypassSettle).
func (st *state) diff(prev map[Target]int, woken map[nodeKey]bool, tick, lead int64) []Event {
	presetTick := tick - lead
	if presetTick < 0 {
		presetTick = 0
	}
	reloaded := map[nodeKey]bool{}
	var out []Event
	for _, s := range st.settings {
		if s.target.Kind != ControlPreset {
			continue
		}
		if old, ok := prev[s.target]; !ok || old != s.value {
			out = append(out, s.event(presetTick))
			reloaded[s.target.node()] = true
		}
	}
	for _, s := range st.settings {
		if s.target.Kind == ControlPreset {
			continue
		}
		if s.target.Kind == ControlParam && woken[s.target.node()] {
			continue
		}
		old, ok := prev[s.target]
		changed := !ok || old != s.value
		if s.target.Kind == ControlParam && reloaded[s.target.node()] {
			changed = true
		}
		if changed {
			out = append(out, s.event(tick))
		}
	}
	return out
}

// nodeKey names a node: a track's instrument (effect "") or one of its
// effects or modulators.
type nodeKey struct{ track, effect string }

func (t Target) node() nodeKey { return nodeKey{t.Track, t.Effect} }

// bypassed reads a bypass's wire value as AUM does: 64 and above bypasses.
func bypassed(wire int) bool { return wire >= 64 }

// off lists the nodes the state bypasses.
func (st *state) off() map[nodeKey]bool {
	out := map[nodeKey]bool{}
	for _, s := range st.settings {
		if s.target.Kind == ControlBypass && bypassed(s.value) {
			out[s.target.node()] = true
		}
	}
	return out
}

// switchedOn lists the nodes the state has on and prev has bypassed.
func (st *state) switchedOn(prev map[Target]int) map[nodeKey]bool {
	out := map[nodeKey]bool{}
	for _, s := range st.settings {
		if s.target.Kind != ControlBypass || bypassed(s.value) {
			continue
		}
		if old, ok := prev[s.target]; ok && bypassed(old) {
			out[s.target.node()] = true
		}
	}
	return out
}

// paramEvent is a parameter's message, with the parameter it sets.
type paramEvent struct {
	Event
	target Target
}

// params renders, at tick, every parameter the state sets on the nodes.
func (st *state) params(nodes map[nodeKey]bool, tick int64) []paramEvent {
	var out []paramEvent
	for _, s := range st.settings {
		if s.target.Kind == ControlParam && nodes[s.target.node()] {
			out = append(out, paramEvent{Event: s.event(tick), target: s.target})
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
		depth  []Value          // a modulator's depth as a scene gives it
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
			for _, mid := range sortedKeys(ts.Modulators) {
				if n := p.Node(tid, mid); n != nil {
					ms := ts.Modulators[mid]
					if err := apply(n, ms.NodeState); err != nil {
						return nil, err
					}
					if len(ms.Depth) > 0 {
						nodes[n].depth = ms.Depth
					}
				}
			}
		}
	}

	st := &state{held: map[Target]string{}}
	// The modulators come first: a bypassed one is sent away from the
	// parameter it moves before the parameter is set to its own value.
	for _, n := range p.modulators() {
		w := nodes[n]
		if w.preset != "" {
			pr, err := n.preset(w.preset)
			if err != nil {
				return nil, err
			}
			st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlPreset},
				Assignment{Kind: MsgPC, Channel: pr.Channel}, pr.Program)
		}
		// A bypassed modulator is parked, never bypassed in AUM: AUM loses
		// the parameter changes that reach a node together with its
		// un-bypass, and the routing that brings it back is such a change
		// (modulator.go).
		st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlBypass}, n.Bypass, boolWire(false))
		depth := n.Mod.Depth
		if w.depth != nil {
			d, err := n.Mod.depth(w.depth)
			if err != nil {
				return nil, fmt.Errorf("/sections/%s: %s's depth: %w", name, nodeLabel(n), err)
			}
			depth = d
		}
		routing := n.Mod.routing(depth, w.bypass)
		for _, pp := range n.Params {
			wire, ok := routing[pp.Key]
			if !ok {
				v, set := w.params[pp.Key]
				if !set {
					continue
				}
				var err error
				if wire, err = paramWire(pp.param, v); err != nil {
					return nil, fmt.Errorf("/sections/%s: %s: %w", name, nodeLabel(n), err)
				}
			}
			st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlParam, Param: pp.Key}, pp.At, wire)
		}
		if !w.bypass {
			st.held[n.Mod.targetOf()] = nodeLabel(n)
		}
	}
	for _, n := range p.allNodes() {
		if n.Mod != nil {
			continue
		}
		w := nodes[n]
		if w.preset != "" {
			pr, err := n.preset(w.preset)
			if err != nil {
				return nil, err
			}
			st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlPreset},
				Assignment{Kind: MsgPC, Channel: pr.Channel}, pr.Program)
		}
		if n.IsFile() {
			continue // a File Player has nothing a song sets but its strip
		}
		st.set(Target{Track: n.Track, Effect: n.Effect, Kind: ControlBypass}, n.Bypass, boolWire(w.bypass))
		for _, pp := range n.Params {
			v, ok := w.params[pp.Key]
			if !ok {
				continue
			}
			t := Target{Track: n.Track, Effect: n.Effect, Kind: ControlParam, Param: pp.Key}
			if _, held := st.held[t]; held {
				continue // the modulator moves it here
			}
			wire, err := paramWire(pp.param, v)
			if err != nil {
				return nil, fmt.Errorf("/sections/%s: %s: %w", name, nodeLabel(n), err)
			}
			st.set(t, pp.At, wire)
		}
	}
	for _, tp := range p.Tracks {
		w := strips[tp]
		if w.level > MaxLevelDB && !p.calibrating() {
			return nil, fmt.Errorf("/sections/%s: %s level %g dB is above the fader's +%g dB", name, tp.ID, w.level, MaxLevelDB)
		}
		level := p.fader(tp, nodes[tp.Nodes[0]].preset, w.level)
		st.set(Target{Track: tp.ID, Kind: ControlLevel}, tp.Level, levelWire(level))
		st.set(Target{Track: tp.ID, Kind: ControlMute}, tp.Mute, boolWire(w.mute))
	}
	return st, nil
}

// HomeState is the messages that give every track the sound it declares for
// itself, with no scene applied: the presets first, then everything else.
// It is the sound a track is checked and auditioned with.
func (p *Plan) HomeState() ([]Event, error) {
	st, err := p.sectionState("", Section{})
	if err != nil {
		return nil, err
	}
	return st.events(0, 0), nil
}

// TrackHomeState is HomeState for one track alone: its strip and its plugins.
func (p *Plan) TrackHomeState(track string) ([]Event, error) {
	st, err := p.sectionState("", Section{})
	if err != nil {
		return nil, err
	}
	own := &state{}
	for _, s := range st.settings {
		if s.target.Track == track {
			own.set(s.target, s.at, s.value)
		}
	}
	return own.events(0, 0), nil
}

// MuteEvent is the message that mutes or unmutes a track's strip.
func (tp *TrackPlan) MuteEvent(muted bool) Event {
	return Event{Kind: Control, Channel: tp.Mute.Channel, Data1: tp.Mute.Number, Data2: boolWire(muted)}
}

// HomeMute reports whether the track declares itself muted.
func (tp *TrackPlan) HomeMute() bool { return tp.homeMute }

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

		if a.fader() {
			tp := p.Track(a.Track)
			if tp == nil {
				return nil, fmt.Errorf("%s: %q has no fader to automate", where, a.Track)
			}
			if a.From.IsStr || a.To.IsStr || ((a.From.Num > MaxLevelDB || a.To.Num > MaxLevelDB) && !p.calibrating()) {
				return nil, fmt.Errorf("%s: a level ramp runs between two dB values up to +%g", where, MaxLevelDB)
			}
			// A calibrated track's level is trimmed for the preset it has in
			// the section; a ramp moves between two such levels.
			preset := p.sectionPreset(sec, tp.Nodes[0])
			r.target = Target{Track: tp.ID, Kind: ControlLevel}
			r.at, r.from, r.to = tp.Level, levelWire(p.fader(tp, preset, a.From.Num)), levelWire(p.fader(tp, preset, a.To.Num))
		} else {
			n := p.Node(a.Track, a.node())
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
