// Package song is the song model: a multi-track arrangement an agent writes as
// one YAML document and the daemon turns into two things — an AUM session that
// hosts the instruments and effects (session.go), and the timed MIDI that plays
// it (compile.go).
//
// A song names its tracks (an instrument plus an effect chain, each a staged
// AUv3 probe), its patterns (notes on a step grid), its sections (which pattern
// each track plays, and for how many bars) and the arrangement (the order of
// sections). What a track sounds like in a section — preset, parameters, which
// effects are bypassed, level, mute — is the track's own declaration overlaid
// with the section's scenes, so every section's sound is self-contained and
// playback can start anywhere.
//
// The package does no MIDI IO and reads no files but its own store; plugins are
// handed in as probe dumps.
package song

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Song is one song document.
type Song struct {
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Tempo is the song's tempo in BPM.
	Tempo float64 `yaml:"tempo" json:"tempo"`
	// TimeSignature is "beats/unit" (default "4/4").
	TimeSignature string `yaml:"time_signature,omitempty" json:"time_signature,omitempty"`
	// Key is a note for whoever edits the song; nothing reads it.
	Key string `yaml:"key,omitempty" json:"key,omitempty"`
	// Swing delays every second grid step by this fraction of a step (0 is
	// straight, 0.33 a triplet feel). A pattern may set its own.
	Swing float64 `yaml:"swing,omitempty" json:"swing,omitempty"`

	Tracks []Track `yaml:"tracks" json:"tracks"`
	// Master is the effect chain on the master strip, after the fader.
	Master []Plugin `yaml:"master,omitempty" json:"master,omitempty"`

	Patterns map[string]Pattern `yaml:"patterns" json:"patterns"`
	Scenes   map[string]Scene   `yaml:"scenes,omitempty" json:"scenes,omitempty"`
	Sections map[string]Section `yaml:"sections" json:"sections"`
	// Arrangement is the order the sections play in; a section may repeat.
	Arrangement []string `yaml:"arrangement" json:"arrangement"`

	// Session holds how the song's AUM session is built, beyond what the
	// tracks say. nil is the plain session.
	Session *SessionSpec `yaml:"session,omitempty" json:"session,omitempty"`
}

// SessionSpec is the song's own choice of session options. They are part of
// the document so a song carries how its session is built; the tools that
// author a session take them from here unless told otherwise.
type SessionSpec struct {
	// TrackTaps puts a ProbeAudioTap after every track's fader.
	TrackTaps bool `yaml:"track_taps,omitempty" json:"track_taps,omitempty"`
	// FullControl maps every other parameter of every plugin as well.
	FullControl bool `yaml:"full_control,omitempty" json:"full_control,omitempty"`
	// Hardware is the hardware-I/O profile of the master's output: "builtin"
	// (the default) or "x32".
	Hardware string `yaml:"hardware,omitempty" json:"hardware,omitempty"`
}

// SessionOptions returns the song's session options, the zero value when it
// names none.
func (s *Song) SessionOptions() SessionSpec {
	if s.Session == nil {
		return SessionSpec{}
	}
	return *s.Session
}

// sessionHardware lists the hardware profiles a session can be authored for.
var sessionHardware = map[string]bool{"": true, "builtin": true, "x32": true}

// Track is one instrument with its effect chain on its own mixer strip and
// MIDI channel.
type Track struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Kind is "" for a melodic track or "drums" for one whose patterns are drum
	// lanes.
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Channel is the MIDI channel the track plays on. 0 takes the track's
	// position (the first track plays on channel 1).
	Channel int `yaml:"channel,omitempty" json:"channel,omitempty"`
	// Transpose shifts every note of the track by semitones.
	Transpose int `yaml:"transpose,omitempty" json:"transpose,omitempty"`

	Instrument Plugin   `yaml:"instrument" json:"instrument"`
	Effects    []Plugin `yaml:"effects,omitempty" json:"effects,omitempty"`

	// Level is the strip's fader in dB (0 is unity, the default) and Mute its
	// mute, wherever no scene says otherwise.
	Level *float64 `yaml:"level,omitempty" json:"level,omitempty"`
	Mute  bool     `yaml:"mute,omitempty" json:"mute,omitempty"`

	// DrumMap names the notes of a drum track's lanes (lane -> note name or
	// number), on top of the General MIDI names every drum track knows.
	DrumMap map[string]string `yaml:"drum_map,omitempty" json:"drum_map,omitempty"`
}

// Plugin is one hosted audio unit — a track's instrument, one of its effects or
// a master effect — and how it is set wherever no scene says otherwise.
type Plugin struct {
	// ID names an effect within its chain so scenes can address it. It
	// defaults to Plugin. An instrument needs none.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Plugin is the staged AUv3 probe id of the audio unit to host.
	Plugin string `yaml:"plugin" json:"plugin"`
	// MIDI also routes the track's notes to this effect (for an effect that
	// plays or follows notes).
	MIDI bool `yaml:"midi,omitempty" json:"midi,omitempty"`

	NodeState `yaml:",inline"`
}

// NodeState is what a scene (or a plugin's own declaration) says about one
// audio unit. Every field is optional; an unset one leaves the unit alone.
type NodeState struct {
	// Preset is the name of one of the unit's factory presets.
	Preset string `yaml:"preset,omitempty" json:"preset,omitempty"`
	// Bypass switches the unit out of the chain.
	Bypass *bool `yaml:"bypass,omitempty" json:"bypass,omitempty"`
	// Params sets parameters by name: a number in the parameter's own range,
	// "NN%" of that range, or one of its value labels.
	Params map[string]Value `yaml:"params,omitempty" json:"params,omitempty"`
}

// Scene is a named set of control settings: what some tracks sound like. A
// section lists the scenes it plays with; a control no scene names keeps the
// track's own declaration.
type Scene map[string]TrackState

// TrackState is what a scene says about one track: its strip, its instrument
// and any of its effects.
type TrackState struct {
	Level *float64 `yaml:"level,omitempty" json:"level,omitempty"`
	Mute  *bool    `yaml:"mute,omitempty" json:"mute,omitempty"`

	// The instrument's state.
	NodeState `yaml:",inline"`

	// Effects is the state of the track's effects by effect id.
	Effects map[string]NodeState `yaml:"effects,omitempty" json:"effects,omitempty"`
}

// MasterTrack is the id scenes address the master strip's effects by.
const MasterTrack = "master"

// Section is one part of the song: for a number of bars every listed track
// plays its patterns, with the sound the section's scenes give it.
type Section struct {
	Bars int `yaml:"bars" json:"bars"`
	// Scene names the scenes the section plays with, applied in order.
	Scene StringList `yaml:"scene,omitempty" json:"scene,omitempty"`
	// Play maps a track to the patterns it plays, one after another and around
	// again until the section ends. A track left out is silent.
	Play map[string]StringList `yaml:"play,omitempty" json:"play,omitempty"`
	// Automation moves controls over time within the section.
	Automation []Ramp `yaml:"automation,omitempty" json:"automation,omitempty"`
}

// Ramp moves one control from one value to another over part of a section: a
// parameter of a track's instrument or of one of its effects, or the track's
// level.
type Ramp struct {
	Track  string `yaml:"track" json:"track"`
	Effect string `yaml:"effect,omitempty" json:"effect,omitempty"`
	// Param names the parameter; "level" (and no Effect) is the strip's fader.
	Param string `yaml:"param" json:"param"`
	From  Value  `yaml:"from" json:"from"`
	To    Value  `yaml:"to" json:"to"`
	// Bar is the 1-based bar of the section the ramp starts at (default 1) and
	// Bars how long it runs (default: to the end of the section).
	Bar  int `yaml:"bar,omitempty" json:"bar,omitempty"`
	Bars int `yaml:"bars,omitempty" json:"bars,omitempty"`
}

// Pattern is a phrase one track plays: notes on a step grid, drum lanes, or
// explicitly timed events. See notation.go for the grid languages.
type Pattern struct {
	// Bars is the pattern's length. 0 takes the grid's length rounded up to a
	// whole bar.
	Bars int `yaml:"bars,omitempty" json:"bars,omitempty"`
	// Step is the grid resolution: "1/16" (the default), "1/8", "1/4", "1/2",
	// "1/32", or a triplet such as "1/8t".
	Step string `yaml:"step,omitempty" json:"step,omitempty"`
	// Gate is the part of a note's grid length that sounds (default 0.9).
	Gate float64 `yaml:"gate,omitempty" json:"gate,omitempty"`
	// Velocity is the velocity of a note without one of its own (default 96).
	Velocity int `yaml:"velocity,omitempty" json:"velocity,omitempty"`
	// Transpose shifts the pattern's notes by semitones.
	Transpose int `yaml:"transpose,omitempty" json:"transpose,omitempty"`
	// Swing overrides the song's swing for this pattern.
	Swing *float64 `yaml:"swing,omitempty" json:"swing,omitempty"`

	// Notes is one grid line per voice; the voices sound together.
	Notes StringList `yaml:"notes,omitempty" json:"notes,omitempty"`
	// Drums is one hit grid per lane of a drum track.
	Drums map[string]string `yaml:"drums,omitempty" json:"drums,omitempty"`
	// Events are notes placed freely, in beats from the pattern's start.
	Events []NoteEvent `yaml:"events,omitempty" json:"events,omitempty"`
}

// NoteEvent is one freely placed note or chord of a pattern.
type NoteEvent struct {
	// At is the start and Dur the length, in beats (quarter notes).
	At  float64 `yaml:"at" json:"at"`
	Dur float64 `yaml:"dur" json:"dur"`
	// Note is a note name ("A2"), a number, a drum lane, or several of them
	// separated by spaces for a chord.
	Note     string `yaml:"note" json:"note"`
	Velocity int    `yaml:"vel,omitempty" json:"vel,omitempty"`
}

// StringList is a list of strings that may be written as a single string.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence of scalars.
func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*l = StringList{n.Value}
		return nil
	case yaml.SequenceNode:
		out := make(StringList, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a string", c.Line)
			}
			out = append(out, c.Value)
		}
		*l = out
		return nil
	default:
		return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
	}
}

// Value is a control value as written: a number or a string ("60%", a value
// label). It is resolved against the control it sets.
type Value struct {
	Num   float64
	Str   string
	IsStr bool
}

// Number returns a numeric Value.
func Number(v float64) Value { return Value{Num: v} }

// UnmarshalYAML keeps numbers as numbers and everything else as its text.
func (v *Value) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected a number or a string", n.Line)
	}
	if n.Tag == "!!int" || n.Tag == "!!float" {
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return fmt.Errorf("line %d: %q is not a number", n.Line, n.Value)
		}
		*v = Value{Num: f}
		return nil
	}
	if n.Tag == "!!bool" {
		// on/off for a switch parameter.
		var b bool
		if err := n.Decode(&b); err != nil {
			return err
		}
		*v = Value{Str: strconv.FormatBool(b), IsStr: true}
		return nil
	}
	*v = Value{Str: n.Value, IsStr: true}
	return nil
}

// MarshalYAML writes the value back the way it was read.
func (v Value) MarshalYAML() (any, error) {
	if v.IsStr {
		return v.Str, nil
	}
	return v.Num, nil
}

// MarshalJSON writes the value as a JSON number or string.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.IsStr {
		return []byte(strconv.Quote(v.Str)), nil
	}
	return []byte(strconv.FormatFloat(v.Num, 'g', -1, 64)), nil
}

// String renders the value for a message.
func (v Value) String() string {
	if v.IsStr {
		return v.Str
	}
	return strconv.FormatFloat(v.Num, 'g', -1, 64)
}

// Parse decodes a song document (YAML, or JSON, which is YAML) and checks its
// structure. Unknown keys are errors, so a misspelled field is caught here
// rather than silently ignored.
func Parse(data []byte) (*Song, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Song
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse song: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Marshal encodes the song as YAML.
func (s *Song) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Errors is every problem found in a song, each naming where it is.
type Errors []string

func (e Errors) Error() string {
	if len(e) == 1 {
		return e[0]
	}
	return fmt.Sprintf("%d problems:\n  %s", len(e), strings.Join(e, "\n  "))
}

// errs collects problems while a song is checked.
type errs struct{ list Errors }

func (e *errs) addf(format string, args ...any) {
	e.list = append(e.list, fmt.Sprintf(format, args...))
}

func (e *errs) err() error {
	if len(e.list) == 0 {
		return nil
	}
	return e.list
}

// maxTracks is how many tracks a song may have: each takes a MIDI channel, and
// the channels above them carry the session's control messages.
const maxTracks = 12

// Bounds on a song's size. They are far beyond any song and only keep a
// mistyped number from compiling into millions of events.
const (
	maxPatternBars = 64
	maxSectionBars = 256
	maxArrangement = 256
)

// Validate checks the song's structure: that every reference resolves and
// every pattern parses. It does not look at plugins; Plan does.
func (s *Song) Validate() error {
	var e errs
	if s.Tempo < 20 || s.Tempo > 400 {
		e.addf("/tempo: %g is not a tempo (20..400 BPM)", s.Tempo)
	}
	meter, err := parseMeter(s.TimeSignature)
	if err != nil {
		e.addf("/time_signature: %v", err)
	}
	if s.Swing < 0 || s.Swing >= 1 {
		e.addf("/swing: %g is not within 0..1", s.Swing)
	}

	if s.Session != nil && !sessionHardware[strings.ToLower(strings.TrimSpace(s.Session.Hardware))] {
		e.addf("/session/hardware: %q is not builtin or x32", s.Session.Hardware)
	}

	if len(s.Tracks) == 0 {
		e.addf("/tracks: a song needs at least one track")
	}
	if len(s.Tracks) > maxTracks {
		e.addf("/tracks: %d tracks, at most %d fit", len(s.Tracks), maxTracks)
	}
	tracks := map[string]*Track{}
	channels := map[int]string{}
	for i := range s.Tracks {
		t := &s.Tracks[i]
		at := fmt.Sprintf("/tracks/%d", i)
		switch {
		case t.ID == "":
			e.addf("%s/id: a track needs an id", at)
			continue
		case t.ID == MasterTrack:
			e.addf("%s/id: %q is the master strip's name", at, MasterTrack)
		case tracks[t.ID] != nil:
			e.addf("%s/id: duplicate track %q", at, t.ID)
		}
		tracks[t.ID] = t
		if t.Kind != "" && t.Kind != "drums" {
			e.addf("%s/kind: %q is not \"drums\" (or empty for a melodic track)", at, t.Kind)
		}
		ch := s.TrackChannel(i)
		if ch < 1 || ch > maxTracks {
			e.addf("%s/channel: %d is not within 1..%d", at, ch, maxTracks)
		} else if other, taken := channels[ch]; taken {
			e.addf("%s/channel: MIDI channel %d is already track %q's", at, ch, other)
		}
		channels[ch] = t.ID
		if t.Instrument.Plugin == "" {
			e.addf("%s/instrument/plugin: a track needs an instrument", at)
		}
		checkEffects(&e, at+"/effects", t.Effects)
		for lane, note := range t.DrumMap {
			if _, err := parseNote(note); err != nil {
				e.addf("%s/drum_map/%s: %v", at, lane, err)
			}
		}
	}
	checkEffects(&e, "/master", s.Master)

	for _, name := range sortedKeys(s.Scenes) {
		for _, tid := range sortedKeys(s.Scenes[name]) {
			at := fmt.Sprintf("/scenes/%s/%s", name, tid)
			st := s.Scenes[name][tid]
			effects := s.Master
			if tid == MasterTrack {
				if st.Level != nil || st.Mute != nil || st.Preset != "" || st.Bypass != nil || len(st.Params) > 0 {
					e.addf("%s: only the master's effects can be set", at)
				}
			} else if t := tracks[tid]; t != nil {
				effects = t.Effects
			} else {
				e.addf("%s: no track %q", at, tid)
				continue
			}
			for _, fx := range sortedKeys(st.Effects) {
				if findEffect(effects, fx) < 0 {
					e.addf("%s/effects/%s: %q has no effect %q (it has %s)", at, fx, tid, fx, effectIDs(effects))
				}
			}
		}
	}

	if len(s.Patterns) == 0 {
		e.addf("/patterns: a song needs at least one pattern")
	}
	broken := map[string]bool{} // patterns already reported
	for _, name := range sortedKeys(s.Patterns) {
		p := s.Patterns[name]
		at := "/patterns/" + name
		kinds := 0
		for _, has := range []bool{len(p.Notes) > 0, len(p.Drums) > 0, len(p.Events) > 0} {
			if has {
				kinds++
			}
		}
		if len(p.Notes) > 0 && len(p.Drums) > 0 {
			e.addf("%s: a pattern has notes or drums, not both", at)
		}
		if kinds == 0 {
			e.addf("%s: a pattern needs notes, drums or events", at)
		}
		if p.Gate < 0 || p.Gate > 4 {
			e.addf("%s/gate: %g is not within 0..4", at, p.Gate)
		}
		if p.Velocity < 0 || p.Velocity > 127 {
			e.addf("%s/velocity: %d is not within 1..127", at, p.Velocity)
		}
		if p.Swing != nil && (*p.Swing < 0 || *p.Swing >= 1) {
			e.addf("%s/swing: %g is not within 0..1", at, *p.Swing)
		}
		if p.Bars < 0 || p.Bars > maxPatternBars {
			e.addf("%s/bars: %d is not a length (1..%d bars)", at, p.Bars, maxPatternBars)
		}
		if err == nil {
			// Parse with no track: lane names and note spellings are checked
			// where a track plays the pattern.
			if _, perr := compilePattern(name, p, meter, s.Swing, nil); perr != nil {
				e.addf("%s", perr)
				broken[name] = true
			}
		}
	}

	if len(s.Sections) == 0 {
		e.addf("/sections: a song needs at least one section")
	}
	for _, name := range sortedKeys(s.Sections) {
		sec := s.Sections[name]
		at := "/sections/" + name
		if sec.Bars < 1 || sec.Bars > maxSectionBars {
			e.addf("%s/bars: a section is 1..%d bars long, not %d", at, maxSectionBars, sec.Bars)
		}
		for i, sc := range sec.Scene {
			if _, ok := s.Scenes[sc]; !ok {
				e.addf("%s/scene/%d: no scene %q", at, i, sc)
			}
		}
		for _, tid := range sortedKeys(sec.Play) {
			t := tracks[tid]
			if t == nil {
				e.addf("%s/play/%s: no track %q", at, tid, tid)
				continue
			}
			if len(sec.Play[tid]) == 0 {
				e.addf("%s/play/%s: name the pattern(s) the track plays", at, tid)
			}
			for i, pid := range sec.Play[tid] {
				p, ok := s.Patterns[pid]
				if !ok {
					e.addf("%s/play/%s/%d: no pattern %q", at, tid, i, pid)
					continue
				}
				if len(p.Drums) > 0 && t.Kind != "drums" {
					e.addf("%s/play/%s/%d: pattern %q has drum lanes but %q is not a drum track (set kind: drums)", at, tid, i, pid, tid)
				}
				if err == nil && !broken[pid] {
					if _, perr := compilePattern(pid, p, meter, s.Swing, t); perr != nil {
						e.addf("%s/play/%s/%d: %v", at, tid, i, perr)
					}
				}
			}
		}
		for i, r := range sec.Automation {
			rat := fmt.Sprintf("%s/automation/%d", at, i)
			t := tracks[r.Track]
			switch {
			case t == nil && r.Track != MasterTrack:
				e.addf("%s/track: no track %q", rat, r.Track)
			case r.Param == "":
				e.addf("%s/param: name the parameter to move", rat)
			case r.Effect != "":
				effects := s.Master
				if t != nil {
					effects = t.Effects
				}
				if findEffect(effects, r.Effect) < 0 {
					e.addf("%s/effect: %q has no effect %q (it has %s)", rat, r.Track, r.Effect, effectIDs(effects))
				}
			case r.Track == MasterTrack:
				e.addf("%s/effect: name the master effect to automate", rat)
			}
			if r.Bar < 0 || r.Bar > sec.Bars {
				e.addf("%s/bar: %d is not a bar of the section (1..%d)", rat, r.Bar, sec.Bars)
			}
			if r.Bars < 0 {
				e.addf("%s/bars: %d is not a length", rat, r.Bars)
			}
		}
	}

	if len(s.Arrangement) == 0 {
		e.addf("/arrangement: list the sections in the order they play")
	}
	if len(s.Arrangement) > maxArrangement {
		e.addf("/arrangement: %d entries, at most %d", len(s.Arrangement), maxArrangement)
	}
	for i, name := range s.Arrangement {
		if _, ok := s.Sections[name]; !ok {
			e.addf("/arrangement/%d: no section %q", i, name)
		}
	}
	return e.err()
}

// checkEffects checks an effect chain's ids are present and unique.
func checkEffects(e *errs, at string, effects []Plugin) {
	seen := map[string]bool{}
	for i, fx := range effects {
		if fx.Plugin == "" {
			e.addf("%s/%d/plugin: an effect needs a plugin", at, i)
			continue
		}
		id := fx.EffectID()
		if seen[id] {
			e.addf("%s/%d/id: duplicate effect %q (give effects that share a plugin their own ids)", at, i, id)
		}
		seen[id] = true
	}
}

// EffectID is the id scenes address the effect by: its ID, or its plugin.
func (p Plugin) EffectID() string {
	if p.ID != "" {
		return p.ID
	}
	return p.Plugin
}

// findEffect returns the position of the effect with this id, or -1.
func findEffect(effects []Plugin, id string) int {
	for i, fx := range effects {
		if fx.EffectID() == id {
			return i
		}
	}
	return -1
}

// effectIDs lists a chain's effect ids for an error message.
func effectIDs(effects []Plugin) string {
	if len(effects) == 0 {
		return "none"
	}
	ids := make([]string, len(effects))
	for i, fx := range effects {
		ids[i] = fx.EffectID()
	}
	return strings.Join(ids, ", ")
}

// TrackChannel returns the MIDI channel (1-based) track i plays on.
func (s *Song) TrackChannel(i int) int {
	if c := s.Tracks[i].Channel; c != 0 {
		return c
	}
	return i + 1
}

// TrackName returns the track's display name.
func (t Track) TrackName() string {
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

// sortedKeys returns a map's keys in order, for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
