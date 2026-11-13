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
	"path"
	"slices"
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
	// Calibrate compiles the song against the rig's sound catalogue: notes
	// are sounding pitch, levels are loudness, and a drum track's lanes are
	// its kit's pads. See calibrate.go.
	Calibrate bool `yaml:"calibrate,omitempty" json:"calibrate,omitempty"`

	Tracks []Track `yaml:"tracks" json:"tracks"`
	// Master is the effect chain on the master strip, after the fader.
	Master []Plugin `yaml:"master,omitempty" json:"master,omitempty"`
	// StemStrips are the stems' own strips in their mix (MixSong), by stem
	// name: the level and the effect chain a stem's recording plays through
	// there, before the master. A stem left out plays at unity and dry. No
	// stem is recorded through its strip, so a change here is a new mix of
	// the same takes.
	StemStrips map[string]StemStrip `yaml:"stems,omitempty" json:"stems,omitempty"`

	Patterns map[string]Pattern `yaml:"patterns" json:"patterns"`
	Scenes   map[string]Scene   `yaml:"scenes,omitempty" json:"scenes,omitempty"`
	Sections map[string]Section `yaml:"sections" json:"sections"`
	// Arrangement is the order the sections play in; a section may repeat.
	Arrangement []string `yaml:"arrangement" json:"arrangement"`

	// Session holds how the song's AUM session is built, beyond what the
	// tracks say. nil is the plain session.
	Session *SessionSpec `yaml:"session,omitempty" json:"session,omitempty"`

	// stem marks a song derived to record a stem (StemSong): its sequence
	// ends AUM's recorder itself. It is not part of a document.
	stem bool
}

// StemStrip is a stem's strip in the mix of the stems: the file track that
// plays the stem's recording there.
type StemStrip struct {
	// Level is the strip's fader in dB, 0 (unity) when it is nil.
	Level *float64 `yaml:"level,omitempty" json:"level,omitempty"`
	// Effects is the strip's effect chain, after the recording.
	Effects []Plugin `yaml:"effects,omitempty" json:"effects,omitempty"`
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
	// Presets says which factory presets the session can load: "song" (the
	// default) maps the ones the song names, "all" every preset of every
	// plugin, so any of them can be auditioned in the loaded session and a
	// song can change to another without a new session.
	Presets string `yaml:"presets,omitempty" json:"presets,omitempty"`
	// LeadBars is how many bars of lead-in the session's sequencer plays
	// before bar 1, at the least; 0 takes what the opening sound needs
	// (Timeline.Sequence). A song that plays recordings of other sessions
	// says theirs: a recording begins with its session's lead-in, and plays
	// from the transport's top.
	LeadBars int `yaml:"lead_bars,omitempty" json:"lead_bars,omitempty"`
}

// AllPresets reports whether the session maps every factory preset.
func (o SessionSpec) AllPresets() bool {
	return strings.EqualFold(strings.TrimSpace(o.Presets), "all")
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
	// ControlChannels are further MIDI channels the track's instrument hears,
	// for one that takes control notes on a channel of its own (a drum
	// machine's mutes on channel 10) while it plays the track's notes on the
	// track's channel. A pattern or event with a channel sends its notes on
	// one of them. No other track may play or listen on them.
	ControlChannels []int `yaml:"control_channels,omitempty" json:"control_channels,omitempty"`
	// Transpose shifts every note of the track by semitones.
	Transpose int `yaml:"transpose,omitempty" json:"transpose,omitempty"`
	// Group names the stem the track is recorded into when the song is
	// recorded a group at a time (stems.go). Tracks of one group are recorded
	// together; a track without one is a stem of its own, named by its id.
	Group string `yaml:"group,omitempty" json:"group,omitempty"`

	Instrument Plugin   `yaml:"instrument" json:"instrument"`
	Effects    []Plugin `yaml:"effects,omitempty" json:"effects,omitempty"`

	// Level is the strip's fader in dB (0 is unity, the default) and Mute its
	// mute, wherever no scene says otherwise.
	Level *float64 `yaml:"level,omitempty" json:"level,omitempty"`
	Mute  bool     `yaml:"mute,omitempty" json:"mute,omitempty"`

	// DrumMap names the notes of a drum track's lanes (lane -> note name or
	// number), on top of the General MIDI names every drum track knows.
	DrumMap map[string]string `yaml:"drum_map,omitempty" json:"drum_map,omitempty"`

	// kit is the measured drum map of the kit the track plays in the section
	// being compiled (lane -> note) and kitName its preset; set only while a
	// calibrated song compiles. With a kit, a lane is one of its pads, not a
	// General MIDI note.
	kit     map[string]int
	kitName string
	// anyLane lets a lane nothing knows compile to a silent placeholder: a
	// calibrated song's lanes are checked against its kit, which needs the
	// catalogue, so a compile without one must not reject them.
	anyLane bool
}

// Plugin is one hosted audio unit — a track's instrument, one of its effects or
// a master effect — and how it is set wherever no scene says otherwise.
type Plugin struct {
	// ID names an effect within its chain so scenes can address it. It
	// defaults to Plugin. An instrument needs none.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Plugin is the staged AUv3 probe id of the audio unit to host.
	Plugin string `yaml:"plugin,omitempty" json:"plugin,omitempty"`
	// File makes a track's instrument a recording instead of a plugin: an
	// audio file in AUM's Recordings folder, by its path there
	// ("<session>/<name>.wav"), which the strip's File Player plays from its
	// start whenever AUM's transport runs from the top. An instrument has a
	// plugin or a file, not both; an effect has no file.
	File string `yaml:"file,omitempty" json:"file,omitempty"`
	// MIDI also routes the track's notes to this effect (for an effect that
	// plays or follows notes).
	MIDI bool `yaml:"midi,omitempty" json:"midi,omitempty"`

	NodeState `yaml:",inline"`

	// Sequence is an instrument's own step sequence (a drum machine's steps
	// and lanes per voice), written into its saved state. See stepseq.go.
	Sequence StepSequence `yaml:"sequence,omitempty" json:"sequence,omitempty"`
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
	// Channel sends the pattern's notes on one of the control channels of the
	// track that plays it instead of the track's own: control notes, sent as
	// written, with no transposition and no drum lanes. 0 is the track's
	// channel.
	Channel int `yaml:"channel,omitempty" json:"channel,omitempty"`

	// Notes is one grid line per voice; the voices sound together.
	Notes StringList `yaml:"notes,omitempty" json:"notes,omitempty"`
	// Drums is one hit grid per lane of a drum track.
	Drums map[string]string `yaml:"drums,omitempty" json:"drums,omitempty"`
	// Events are notes placed freely, in beats from the pattern's start.
	Events []NoteEvent `yaml:"events,omitempty" json:"events,omitempty"`
	// Pedal is where the sustain pedal of the track's instrument is down.
	Pedal []PedalSpan `yaml:"pedal,omitempty" json:"pedal,omitempty"`
}

// PedalSpan is one stretch of a pattern with the sustain pedal down.
type PedalSpan struct {
	// At is where the pedal goes down and Dur how long it stays down, in
	// beats (quarter notes) from the pattern's start.
	At  float64 `yaml:"at" json:"at"`
	Dur float64 `yaml:"dur" json:"dur"`
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
	// Channel sends the event on one of the track's control channels, as the
	// pattern's channel does, so a pattern can hold the track's notes and
	// control notes together. 0 takes the pattern's channel.
	Channel int `yaml:"channel,omitempty" json:"channel,omitempty"`
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

// maxTracks is how many tracks one session hosts: each takes a MIDI channel,
// and the channels above them carry the session's control messages. A song of
// more tracks has no session of its own (StemsOnly): it is recorded a stem at
// a time, each stem of at most maxTracks tracks from a session of its own, and
// played as the mix of the recordings, which has a track per stem and so at
// most maxTracks stems.
const maxTracks = 12

// MaxSessionTracks is how many tracks one session hosts, for a message.
const MaxSessionTracks = maxTracks

// Bounds on a song's size. They are far beyond any song and only keep a
// mistyped number from compiling into millions of events.
const (
	maxPatternBars = 64
	maxSectionBars = 256
	maxArrangement = 256
	maxLeadBars    = 16
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
	if s.Session != nil {
		switch strings.ToLower(strings.TrimSpace(s.Session.Presets)) {
		case "", "song", "all":
		default:
			e.addf("/session/presets: %q is not song or all", s.Session.Presets)
		}
		if s.Session.LeadBars < 0 || s.Session.LeadBars > maxLeadBars {
			e.addf("/session/lead_bars: %d is not a lead-in (0..%d bars)", s.Session.LeadBars, maxLeadBars)
		}
	}

	if len(s.Tracks) == 0 {
		e.addf("/tracks: a song needs at least one track")
	}
	// MIDI channels are counted among the tracks that share a session: the
	// whole song's, or, in a song of more tracks than a session hosts, each
	// stem's. Whether such a song's stems fit is Stems' to say, below.
	type slot struct {
		stem    string // "" in a song that is one session
		channel int
	}
	scope := func(i int) string {
		if s.StemsOnly() {
			return s.stemOf(i)
		}
		return ""
	}
	// in says which session a channel is taken in, where there are several.
	in := func(i int) string {
		if s.StemsOnly() {
			return fmt.Sprintf(" in the stem %q", s.stemOf(i))
		}
		return ""
	}
	tracks := map[string]*Track{}
	channels := map[slot]string{} // the track that plays on a channel
	scopes := map[string]string{} // track id -> the stem its channels are counted in
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
		scopes[t.ID] = scope(i)
		if t.Kind != "" && t.Kind != "drums" {
			e.addf("%s/kind: %q is not \"drums\" (or empty for a melodic track)", at, t.Kind)
		}
		ch := s.TrackChannel(i)
		here := slot{scope(i), ch}
		if ch < 1 || ch > maxTracks {
			// A position past the last channel is a stem of too many tracks,
			// which Stems reports.
			if t.Channel != 0 || !s.StemsOnly() {
				e.addf("%s/channel: %d is not within 1..%d", at, ch, maxTracks)
			}
		} else if other, taken := channels[here]; taken {
			e.addf("%s/channel: MIDI channel %d is already track %q's%s", at, ch, other, in(i))
		}
		channels[here] = t.ID
		switch {
		case t.Instrument.File != "":
			checkFileTrack(&e, at, t)
		case t.Instrument.Plugin == "":
			e.addf("%s/instrument/plugin: a track needs an instrument: a plugin, or a file (a recording in AUM's Recordings)", at)
		}
		// The stem is the group as Stems trims it.
		if strings.TrimSpace(t.Group) == MasterTrack {
			e.addf("%s/group: %q is the master strip's name", at, MasterTrack)
		}
		if len(t.Instrument.Sequence) > 0 && err == nil && t.Instrument.File == "" {
			checkStepSequence(&e, at+"/instrument/sequence", t.Instrument.Sequence, meter)
		}
		checkEffects(&e, at+"/effects", t.Effects)
		for lane, note := range t.DrumMap {
			if _, err := parseNote(note); err != nil {
				e.addf("%s/drum_map/%s: %v", at, lane, err)
			}
		}
	}
	// A control channel is its track's alone: what is sent on it reaches that
	// track's instrument and nothing else.
	listens := map[slot]string{}
	for i := range s.Tracks {
		t := &s.Tracks[i]
		if t.ID == "" {
			continue
		}
		for j, ch := range t.ControlChannels {
			at := fmt.Sprintf("/tracks/%d/control_channels/%d", i, j)
			here := slot{scope(i), ch}
			other, taken := listens[here]
			switch {
			case ch < 1 || ch > maxTracks:
				e.addf("%s: %d is not within 1..%d", at, ch, maxTracks)
			case channels[here] == t.ID:
				e.addf("%s: MIDI channel %d is the track's own channel", at, ch)
			case channels[here] != "":
				e.addf("%s: MIDI channel %d is track %q's%s (give that track another channel)", at, ch, channels[here], in(i))
			case taken && other == t.ID:
				e.addf("%s: MIDI channel %d is listed twice", at, ch)
			case taken:
				e.addf("%s: MIDI channel %d is already a control channel of track %q%s", at, ch, other, in(i))
			}
			listens[here] = t.ID
		}
	}
	checkEffects(&e, "/master", s.Master)
	// The grouping holds together: a song that cannot be taken apart into
	// its stems says so when it is saved, not when its stems are recorded.
	// For a song of more tracks than a session hosts this is also the check
	// of its size: its stems are all there is to play of it.
	stems, serr := s.Stems(nil)
	if serr != nil {
		if list, ok := serr.(Errors); ok {
			e.list = append(e.list, list...)
		} else {
			e.addf("/tracks: %v", serr)
		}
	}
	// A stem's strip in the mix belongs to one of the document's stems.
	if serr == nil && len(s.StemStrips) > 0 {
		names := make([]string, len(stems))
		known := map[string]bool{}
		for i, st := range stems {
			names[i], known[st.Name] = st.Name, true
		}
		for _, name := range sortedKeys(s.StemStrips) {
			at := "/stems/" + name
			if !known[name] {
				e.addf("%s: the song has no stem %q (its stems are %s)", at, name, strings.Join(names, ", "))
			}
			ss := s.StemStrips[name]
			if ss.Level != nil && *ss.Level > MaxLevelDB {
				e.addf("%s/level: %g dB is above the fader's +%g dB", at, *ss.Level, MaxLevelDB)
			}
			checkEffects(&e, at+"/effects", ss.Effects)
		}
	}

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
				if t.IsFile() && (st.Preset != "" || st.Bypass != nil || len(st.Params) > 0) {
					e.addf("%s: %q plays a file: a scene can set its level, its mute and its effects, and nothing of a plugin", at, tid)
				}
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

	if len(s.Patterns) == 0 && !s.filesOnly() {
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
		if p.Channel != 0 {
			if p.Channel < 1 || p.Channel > maxTracks {
				e.addf("%s/channel: %d is not within 1..%d", at, p.Channel, maxTracks)
			}
			if len(p.Drums) > 0 {
				e.addf("%s/channel: control notes are written as notes or events, not drum lanes", at)
			}
			if p.Transpose != 0 {
				e.addf("%s/transpose: control notes are sent as written; a pattern with a channel is not transposed", at)
			}
			if len(p.Pedal) > 0 {
				e.addf("%s/pedal: a pattern with a channel is control notes; the pedal goes with the notes on the track's own channel", at)
			}
		}
		for i, ev := range p.Events {
			if ev.Channel != 0 && (ev.Channel < 1 || ev.Channel > maxTracks) {
				e.addf("%s/events/%d/channel: %d is not within 1..%d", at, i, ev.Channel, maxTracks)
			}
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
			if t.IsFile() {
				e.addf("%s/play/%s: %q plays a file, from its start with AUM's transport, and no patterns; leave it out of play", at, tid, tid)
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
				for _, ch := range p.channels() {
					switch {
					case slices.Contains(t.ControlChannels, ch):
					case channels[slot{scopes[tid], ch}] == tid:
						e.addf("%s/play/%s/%d: pattern %q sends on channel %d, which is %q's own channel; leave the channel out", at, tid, i, pid, ch, tid)
					default:
						e.addf("%s/play/%s/%d: pattern %q sends on channel %d, which is not one of %q's control_channels (%s)", at, tid, i, pid, ch, tid, channelList(slices.Sorted(slices.Values(t.ControlChannels))))
					}
				}
				if err == nil && !broken[pid] {
					if _, perr := compilePattern(pid, p, meter, s.Swing, s.laneTrack(t)); perr != nil {
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
			case t.IsFile() && !strings.EqualFold(r.Param, "level"):
				e.addf("%s/param: %q plays a file: its level and its effects can be automated, and no parameter of a plugin", rat, r.Track)
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
		if fx.File != "" {
			e.addf("%s/%d/file: only a track's instrument can be a file", at, i)
		}
		if fx.Plugin == "" {
			e.addf("%s/%d/plugin: an effect needs a plugin", at, i)
			continue
		}
		if len(fx.Sequence) > 0 {
			e.addf("%s/%d/sequence: only an instrument has a step sequence", at, i)
		}
		id := fx.EffectID()
		if seen[id] {
			e.addf("%s/%d/id: duplicate effect %q (give effects that share a plugin their own ids)", at, i, id)
		}
		seen[id] = true
	}
}

// IsFile reports whether the track's instrument is a recording, played by a
// File Player, and no plugin.
func (t *Track) IsFile() bool { return t != nil && t.Instrument.File != "" }

// filesOnly reports whether every track of the song plays a file: such a song
// has no notes, and needs no pattern.
func (s *Song) filesOnly() bool {
	for i := range s.Tracks {
		if !s.Tracks[i].IsFile() {
			return false
		}
	}
	return len(s.Tracks) > 0
}

// HasFiles reports whether any track of the song plays a file.
func (s *Song) HasFiles() bool {
	for i := range s.Tracks {
		if s.Tracks[i].IsFile() {
			return true
		}
	}
	return false
}

// checkFileTrack checks a track whose instrument is a file: the path, and
// that the track says nothing only a plugin or its notes could take.
func checkFileTrack(e *errs, at string, t *Track) {
	in := t.Instrument
	if err := checkRecordingPath(in.File); err != nil {
		e.addf("%s/instrument/file: %v", at, err)
	}
	no := func(set bool, field, why string) {
		if set {
			e.addf("%s/%s: %s", at, field, why)
		}
	}
	no(in.Plugin != "", "instrument/plugin", "an instrument is a plugin or a file, not both")
	no(in.Preset != "", "instrument/preset", "a file has no preset")
	no(len(in.Params) > 0, "instrument/params", "a file has no parameters; its level is the track's")
	no(in.Bypass != nil, "instrument/bypass", "a file is not bypassed; mute the track")
	no(in.MIDI, "instrument/midi", "a file hears no notes")
	no(len(in.Sequence) > 0, "instrument/sequence", "a file has no step sequence")
	no(in.ID != "", "instrument/id", "an instrument needs no id")
	no(t.Kind != "", "kind", "a track that plays a file has no notes, so no kind")
	no(t.Transpose != 0, "transpose", "a file is not transposed")
	no(len(t.ControlChannels) > 0, "control_channels", "a file hears no control notes")
	no(len(t.DrumMap) > 0, "drum_map", "a file has no drum lanes")
}

// checkRecordingPath checks a file's path in AUM's Recordings folder: relative,
// inside the folder, with a file name.
func checkRecordingPath(file string) error {
	switch {
	case strings.TrimSpace(file) != file:
		return fmt.Errorf("%q begins or ends with a blank", file)
	case strings.HasPrefix(file, "/"):
		return fmt.Errorf("%q is not a path relative to AUM's Recordings (\"<session>/<name>.wav\")", file)
	case strings.Contains(file, "\\"):
		return fmt.Errorf("%q: folders are separated by /", file)
	}
	for _, part := range strings.Split(file, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%q is not a path inside AUM's Recordings (\"<session>/<name>.wav\")", file)
		}
	}
	if !strings.Contains(path.Base(file), ".") {
		return fmt.Errorf("%q has no file extension: name the file as AUM's Recordings lists it, .wav included", file)
	}
	return nil
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

// laneTrack returns the track a pattern is checked against where no kit is
// known: a calibrated song's drum lanes are its kit's pads, which only the
// plan can tell, so they pass here and are checked when the song is planned.
func (s *Song) laneTrack(t *Track) *Track {
	if !s.Calibrate || t == nil || t.Kind != "drums" {
		return t
	}
	open := *t
	open.anyLane = true
	return &open
}

// StemsOnly reports whether the song has more tracks than one session hosts.
// Such a song has no session of its own and no plan: it is recorded a stem at
// a time, each stem from a session that hosts its tracks alone (StemSong), and
// played as the mix of the recordings (MixSong). Its MIDI channels are counted
// in each stem, not in the song.
func (s *Song) StemsOnly() bool { return len(s.Tracks) > maxTracks }

// stemOf names the stem the document's grouping puts track i into: its group,
// or itself (Stems).
func (s *Song) stemOf(i int) string {
	if g := strings.TrimSpace(s.Tracks[i].Group); g != "" {
		return g
	}
	return s.Tracks[i].ID
}

// TrackChannel returns the MIDI channel (1-based) track i plays on: the one it
// names, or its position among the tracks it shares a session with. That is
// its position in the song, and in a song of more tracks than a session hosts
// (StemsOnly) its position in its stem, by the document's grouping.
func (s *Song) TrackChannel(i int) int {
	if c := s.Tracks[i].Channel; c != 0 {
		return c
	}
	if !s.StemsOnly() {
		return i + 1
	}
	stem, pos := s.stemOf(i), 1
	for j := range i {
		if s.stemOf(j) == stem {
			pos++
		}
	}
	return pos
}

// channels lists the control channels the pattern sends notes on, its own and
// its events', each once and in order; none for a pattern that plays on its
// track's channel.
func (p Pattern) channels() []int {
	var out []int
	add := func(ch int) {
		if ch != 0 && !slices.Contains(out, ch) {
			out = append(out, ch)
		}
	}
	add(p.Channel)
	for _, ev := range p.Events {
		add(ev.Channel)
	}
	sort.Ints(out)
	return out
}

// channelList renders channels for a message.
func channelList(chs []int) string {
	if len(chs) == 0 {
		return "none"
	}
	s := make([]string, len(chs))
	for i, ch := range chs {
		s[i] = strconv.Itoa(ch)
	}
	return strings.Join(s, ", ")
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
