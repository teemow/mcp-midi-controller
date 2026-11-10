package mcpserver

// This file is the MCP surface over internal/sound: a lab session to hear any
// preset in, the audition that plays one and measures what the tap carried,
// and the catalogue those measurements are kept in.
//
// A preset can only be loaded through a Program Change the session maps, so
// auditioning needs a session that maps every preset (session presets: all).
// The lab is a song of nothing but instruments with that option; a song's own
// session can have it too.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/mcp-midi-controller/internal/sound"
	"github.com/teemow/midi-transport/midicontrol"
)

// soundLabID is the song the lab is kept as.
const soundLabID = "sound_lab"

// Audition defaults. The waits are variables for the tests.
var (
	// auditionLoadWait follows a preset load: synths are deaf for up to a
	// few hundred milliseconds while they load one.
	auditionLoadWait = 800 * time.Millisecond
	auditionHold     = 700 * time.Millisecond
	auditionTail     = 600 * time.Millisecond
	// A kit's pad is hit shortly and listened to for its length.
	auditionHitHold = 60 * time.Millisecond
	auditionHitTail = 500 * time.Millisecond
)

const (
	auditionNote      = 57 // A3
	auditionVelocity  = 100
	auditionMost      = 40 // presets in one call
	auditionDefault   = 12 // presets a query selects unless told otherwise
	auditionKitFrom   = 24
	auditionKitTo     = 96
	auditionKitVel    = 110
	auditionMaxHoldMS = 8000
)

func (s *Server) registerSoundTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "open_sound_lab",
		Description: "Open a session to hear any preset in: one strip per instrument, nothing else in the path, and a Program Change for every factory preset of each (session presets: all). It is stored as the song \"sound_lab\" and loaded like one (load_song: authored, pulled by the iPad, loaded in AUM, brain and tap connected). " +
			"Then audition_sound plays and measures presets in it, a second or two each, with no further reload. Loading replaces the session open in AUM, so it needs confirm: true.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugins": {"type": "array", "minItems": 1, "maxItems": 12, "items": {"type": "string"}, "description": "The instruments to host: probe ids, or names that identify one plugin each (see list_song_plugins kind=instrument)."},
				"confirm": {"type": "boolean", "description": "Required (true): loading replaces the session open in AUM."},
				"tempo": {"type": "number", "description": "The lab's tempo, for presets that follow the host clock (default 120)."}
			},
			"required": ["plugins"]
		}`),
	}, s.handleOpenSoundLab)

	s.mcp.AddTool(&mcp.Tool{
		Name: "audition_sound",
		Description: "Play a note on one or several presets of an instrument in the loaded session and measure what the tap carried: which note sounds (as the semitones to add so a written note sounds as written), how far it is out of tune and how far a held note wanders, rms and peak level, attack, whether it sustains, how long it rings, and what else sounds besides the note. " +
			"Name the presets (preset, presets) or select them by a word in their name (query); several come back as one table to compare. kit: true treats the instrument as a drum machine instead: every note from..to is hit, the ones that sound are its pads, each with level, length and brightness and a guess at its lane (kick, snare, hat …). " +
			"The session must map the preset: the lab does (open_sound_lab), and so does a song whose session: block says presets: all. In the lab — a clean path, no effects, fader at unity — the measurement is stored in the sound catalogue (list_sounds); through a song's effect chain it is reported only. Not while a song plays.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"song": {"type": "string", "description": "The song whose session is loaded (default \"sound_lab\")."},
				"track": {"type": "string", "description": "The track to play on. Or name its plugin."},
				"plugin": {"type": "string", "description": "The instrument to play: the track hosting it is used."},
				"preset": {"type": "string", "description": "One factory preset by name. Leave out (with presets and query) to measure the sound as it is."},
				"presets": {"type": "array", "items": {"type": "string"}, "description": "Several presets by name."},
				"query": {"type": "string", "description": "Every preset whose name contains this text, up to limit."},
				"limit": {"type": "integer", "minimum": 1, "maximum": 40, "description": "Most presets a query selects (default 12)."},
				"note": {"description": "The note to play: a name (\"A3\", C4 is middle C) or a number (default A3)."},
				"velocity": {"type": "integer", "minimum": 1, "maximum": 127, "description": "Default 100 (110 for a kit's pads)."},
				"hold_ms": {"type": "integer", "minimum": 20, "maximum": 8000, "description": "How long the note is held (default 700)."},
				"tail_ms": {"type": "integer", "minimum": 0, "maximum": 8000, "description": "How long it is listened to after the release (default 600)."},
				"load_ms": {"type": "integer", "minimum": 0, "maximum": 5000, "description": "How long the plugin is given to load a preset before the note (default 800)."},
				"kit": {"type": "boolean", "description": "The instrument is a drum machine: find its pads."},
				"from": {"type": "integer", "minimum": 0, "maximum": 127, "description": "First note of a kit scan (default 24)."},
				"to": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Last note of a kit scan (default 96)."},
				"save": {"type": "boolean", "description": "Store the measurement in the sound catalogue (default: yes on a clean path)."}
			}
		}`),
	}, s.handleAuditionSound)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_sounds",
		Description: "List the sound catalogue: what the presets of the rig's plugins were measured to sound like (audition_sound fills it). Per preset: the transposition that makes a written note sound as written, level, whether it sustains, and what stands out; for a kit its drum map. " +
			"Filter by plugin (id or name) and by a word in the preset's name or remarks.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugin": {"type": "string", "description": "Only this plugin (probe id, or a name that identifies it)."},
				"query": {"type": "string", "description": "Only presets whose name or remarks contain this text."}
			}
		}`),
	}, s.handleListSounds)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "set_sound",
		Description: "Correct a measured preset in the sound catalogue by hand: its transposition (when the measured pitch is not the one you hear), a kit's drum map (lanes: lane -> note, replacing the guesses), or a comment. What is set here is kept when the preset is measured again.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugin": {"type": "string", "description": "Probe id, or a name that identifies the plugin."},
				"preset": {"type": "string", "description": "The preset's name (leave out for the default patch)."},
				"transpose": {"type": "integer", "minimum": -48, "maximum": 48, "description": "Semitones to add to a written note so it sounds as written."},
				"lanes": {"type": "object", "additionalProperties": {"type": "integer", "minimum": 0, "maximum": 127}, "description": "A kit's drum map: lane name -> MIDI note."},
				"comment": {"type": "string", "description": "A note for whoever picks sounds."}
			},
			"required": ["plugin"]
		}`),
	}, s.handleSetSound)
}

// --- the lab --------------------------------------------------------------------

// labTrackID turns a plugin's name into a track id: its letters and digits,
// lower case.
func labTrackID(name string, taken map[string]bool) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	id := strings.Trim(b.String(), "_")
	if id == "" || id == song.MasterTrack {
		id = "instrument"
	}
	base := id
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	taken[id] = true
	return id
}

func (s *Server) handleOpenSoundLab(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Plugins []string `json:"plugins"`
		Confirm bool     `json:"confirm"`
		Tempo   float64  `json:"tempo"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if len(args.Plugins) == 0 {
		return textResult("/plugins: name the instruments to host (list_song_plugins kind=instrument)", true), nil
	}
	if s.ipad != nil && !args.Confirm {
		return textResult("open_sound_lab needs confirm: true: loading replaces the session open in AUM, and unsaved changes there are lost", true), nil
	}
	cat := songCatalog()
	doc := song.Song{
		Title:       "Sound Lab",
		Description: "Instruments on a clean path, every factory preset mapped: the session audition_sound plays in.",
		Tempo:       120,
		Session:     &song.SessionSpec{Presets: "all"},
		Patterns:    map[string]song.Pattern{"rest": {Bars: 1, Notes: song.StringList{"."}}},
		Sections:    map[string]song.Section{"idle": {Bars: 1}},
		Arrangement: []string{"idle"},
	}
	if args.Tempo != 0 {
		doc.Tempo = args.Tempo
	}
	taken := map[string]bool{}
	for i, name := range args.Plugins {
		id, err := cat.Resolve(name)
		if err != nil {
			return textResult(fmt.Sprintf("/plugins/%d: %v", i, err), true), nil
		}
		dump := cat[id]
		if pluginKind(dump.Component.Type) != "instrument" {
			return textResult(fmt.Sprintf("/plugins/%d: %s (%s) is not an instrument; the lab hosts instruments", i, dump.Name, id), true), nil
		}
		doc.Tracks = append(doc.Tracks, song.Track{ID: labTrackID(dump.Name, taken), Name: dump.Name, Instrument: song.Plugin{Plugin: id}})
	}
	data, err := doc.Marshal()
	if err != nil {
		return textResult("write the lab: "+err.Error(), true), nil
	}
	sg, err := song.Parse(data)
	if err != nil {
		return textResult("the lab: "+err.Error(), true), nil
	}
	cs, err := s.compileSong(soundLabID, sg)
	if err != nil {
		return textResult("the lab: "+err.Error(), true), nil
	}
	if _, err := s.songs.Save(soundLabID, data); err != nil {
		return textResult("save the lab: "+err.Error(), true), nil
	}
	// The lab plays no notes of its own, so there is nothing to check by
	// sound; an audition says at once when an instrument is silent.
	rep, err := s.runLoadSong(ctx, cs, loadSongArgs{})
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	// The lab is there once AUM has it and brain and tap are connected. An
	// instrument that failed to load is named; the others can be auditioned.
	loaded := rep.Check != nil
	failed := map[string]bool{}
	if rep.loaded != nil {
		for _, title := range rep.loaded.Failed {
			failed[strings.ToLower(title)] = true
		}
	}
	var b strings.Builder
	title := "the sound lab is loaded in AUM"
	switch {
	case !loaded:
		title = "the sound lab is NOT loaded"
	case len(failed) > 0:
		title = "the sound lab is loaded in AUM, but not every instrument came up"
	}
	b.WriteString(rep.text(title))
	rows := []map[string]any{}
	if loaded {
		b.WriteString("instruments (audition_sound track=… or plugin=…):\n")
	}
	for _, tp := range cs.plan.Tracks {
		n := tp.Nodes[0]
		down := failed[strings.ToLower(tp.Name)]
		rows = append(rows, map[string]any{"track": tp.ID, "plugin": n.Plugin, "name": n.Name, "channel": tp.Channel, "presets": len(n.Presets), "factory_presets": len(n.FactoryPresets()), "failed": down})
		switch {
		case !loaded:
		case down:
			fmt.Fprintf(&b, "  %-16s %s: FAILED to load (AUM draws its node as failed); loading the lab again may bring it up, some plugins never do when a session hosts them from scratch\n", tp.ID, n.Name)
		default:
			fmt.Fprintf(&b, "  %-16s %s: %d of %d preset(s) mapped\n", tp.ID, n.Name, len(n.Presets), len(n.FactoryPresets()))
		}
	}
	if cs.plan.PresetsUnmapped > 0 {
		fmt.Fprintf(&b, "%d preset(s) found no Program Change: a lab with fewer instruments reaches them\n", cs.plan.PresetsUnmapped)
	}
	res := structResult(b.String(), map[string]any{"song": soundLabID, "load": rep, "instruments": rows, "presets_unmapped": cs.plan.PresetsUnmapped})
	res.IsError = !loaded
	return res, nil
}

// --- audition -------------------------------------------------------------------

// auditionRow is one preset as auditioned.
type auditionRow struct {
	Preset string       `json:"preset"`
	Sound  *sound.Sound `json:"sound"`
	// Stored tells whether the measurement went into the catalogue.
	Stored bool `json:"stored"`
}

// parseNoteArg reads a note given as a name or a number.
func parseNoteArg(raw json.RawMessage, def int) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return def, nil
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		if n < 0 || n > 127 {
			return 0, fmt.Errorf("%d is not a MIDI note (0..127)", n)
		}
		return n, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return 0, errors.New("give the note as a name (\"A3\") or a number")
	}
	return song.ParseNote(name)
}

func (s *Server) handleAuditionSound(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song     string          `json:"song"`
		Track    string          `json:"track"`
		Plugin   string          `json:"plugin"`
		Preset   string          `json:"preset"`
		Presets  []string        `json:"presets"`
		Query    string          `json:"query"`
		Limit    int             `json:"limit"`
		Note     json.RawMessage `json:"note"`
		Velocity int             `json:"velocity"`
		HoldMS   int             `json:"hold_ms"`
		TailMS   *int            `json:"tail_ms"`
		LoadMS   *int            `json:"load_ms"`
		Kit      bool            `json:"kit"`
		From     *int            `json:"from"`
		To       *int            `json:"to"`
		Save     *bool           `json:"save"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	if args.Song == "" {
		args.Song = soundLabID
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		if song.ID(args.Song) == soundLabID {
			return textResult("there is no sound lab yet: open_sound_lab makes one and loads it", true), nil
		}
		return fail, nil
	}

	// The track to play on.
	var tp *song.TrackPlan
	switch {
	case args.Track != "":
		if tp = cs.plan.Track(args.Track); tp == nil {
			return textResult(fmt.Sprintf("/track: %q has no track %q (it has %s)", cs.id, args.Track, trackIDs(cs)), true), nil
		}
	case args.Plugin != "":
		id, err := songCatalog().Resolve(args.Plugin)
		if err != nil {
			return textResult("/plugin: "+err.Error(), true), nil
		}
		for _, t := range cs.plan.Tracks {
			if t.Nodes[0].Plugin == id {
				tp = t
				break
			}
		}
		if tp == nil {
			return textResult(fmt.Sprintf("/plugin: no track of %q hosts %s (tracks: %s); open_sound_lab makes a lab with it", cs.id, id, trackIDs(cs)), true), nil
		}
	case len(cs.plan.Tracks) == 1:
		tp = cs.plan.Tracks[0]
	default:
		return textResult(fmt.Sprintf("name the track or the plugin to audition (tracks of %q: %s)", cs.id, trackIDs(cs)), true), nil
	}
	node := tp.Nodes[0]

	// The presets to play.
	names := append([]string(nil), args.Presets...)
	if args.Preset != "" {
		names = append(names, args.Preset)
	}
	more := 0
	if q := strings.ToLower(strings.TrimSpace(args.Query)); q != "" {
		limit := args.Limit
		if limit <= 0 {
			limit = auditionDefault
		}
		for _, name := range node.FactoryPresets() {
			if !strings.Contains(strings.ToLower(name), q) {
				continue
			}
			if _, mapped := node.Preset(name); !mapped {
				continue
			}
			if limit == 0 {
				more++
				continue
			}
			names = append(names, name)
			limit--
		}
		if len(names) == 0 {
			return textResult(fmt.Sprintf("/query: no mapped preset of %s contains %q (get_song_plugin lists its presets)", node.Name, args.Query), true), nil
		}
	}
	if len(names) > auditionMost {
		return textResult(fmt.Sprintf("%d presets in one call; at most %d", len(names), auditionMost), true), nil
	}
	for i, name := range names {
		if _, ok := node.Preset(name); ok {
			continue
		}
		known := false
		for _, fp := range node.FactoryPresets() {
			known = known || strings.EqualFold(fp, name)
		}
		if !known {
			return textResult(fmt.Sprintf("/presets/%d: %s has no factory preset %q (get_song_plugin lists them)", i, node.Name, name), true), nil
		}
		return textResult(fmt.Sprintf("/presets/%d: the session of %q has no Program Change for %q: it maps only the presets the song names. Audition in the lab (open_sound_lab), or give the song session: {presets: all} and load_song", i, cs.id, name), true), nil
	}

	note, err := parseNoteArg(args.Note, auditionNote)
	if err != nil {
		return textResult("/note: "+err.Error(), true), nil
	}
	velocity := args.Velocity
	if velocity == 0 {
		velocity = auditionVelocity
		if args.Kit {
			velocity = auditionKitVel
		}
	}
	hold, tail, load := auditionHold, auditionTail, auditionLoadWait
	if args.Kit {
		hold, tail = auditionHitHold, auditionHitTail
	}
	if args.HoldMS > 0 {
		hold = time.Duration(min(args.HoldMS, auditionMaxHoldMS)) * time.Millisecond
	}
	if args.TailMS != nil {
		tail = time.Duration(min(max(*args.TailMS, 0), auditionMaxHoldMS)) * time.Millisecond
	}
	if args.LoadMS != nil {
		load = time.Duration(min(max(*args.LoadMS, 0), 5000)) * time.Millisecond
	}
	from, to := auditionKitFrom, auditionKitTo
	if args.From != nil {
		from = *args.From
	}
	if args.To != nil {
		to = *args.To
	}
	if to < from {
		return textResult("/to: the scan ends before it starts", true), nil
	}

	// The rig must be there, and the session the song's.
	if st := s.player.Status(); st.Playing {
		return textResult(fmt.Sprintf("audition_sound: %q is playing; stop_song first", st.Song), true), nil
	}
	if s.midi == nil || !s.midi.Connected() {
		return textResult("audition_sound: "+midicontrol.ErrNoBrain.Error()+" — load the session first (open_sound_lab, load_song)", true), nil
	}
	if s.audio == nil {
		return textResult("audition_sound: no audio tap is configured", true), nil
	}
	tap, ok := s.audio.Active()
	if !ok || !s.tapStreaming(ctx) {
		return textResult("audition_sound: no ProbeAudioTap is streaming — load the session first (open_sound_lab, load_song)", true), nil
	}
	rec, authored := loadSongSessions()[cs.id]
	var warnings []string
	switch cur := s.loadedSessionID(); {
	case !authored || rec.Fingerprint != cs.timeline.Fingerprint:
		return textResult(fmt.Sprintf("audition_sound: the session of %q is not authored for the song as it is now; load it first (open_sound_lab, load_song)", cs.id), true), nil
	case cur != "" && cur != rec.Session:
		return textResult(fmt.Sprintf("audition_sound: AUM has %q open, not %q; load it first (open_sound_lab, load_song)", cur, rec.Session), true), nil
	case cur == "":
		warnings = append(warnings, fmt.Sprintf("the daemon has not seen which session AUM has open; this assumes %q (check_song confirms)", rec.Session))
	}

	// Whether this is the preset alone: no effect after it, nothing set on it,
	// the fader at unity.
	clean, why := cleanPath(cs.song, tp.ID)
	save := clean
	if args.Save != nil {
		save = *args.Save && clean
	}

	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	// A dozing host stalls the tap's stream and the waits between the notes.
	defer s.holdAwake()()
	home, err := cs.plan.TrackHomeState(tp.ID)
	if err != nil {
		return textResult("audition_sound: "+err.Error(), true), nil
	}
	// set puts the track into its own sound with one preset in place of its
	// own ("" keeps what is loaded): the load, the wait, then everything a
	// load would overwrite, the strip unmuted.
	set := func(preset string) error {
		if preset != "" {
			ev, _ := node.PresetEvent(preset)
			if err := s.sendBrain(ctx, songCommand(ev)); err != nil {
				return err
			}
			if err := sleepContext(ctx, load); err != nil {
				return err
			}
		}
		for _, e := range home {
			if e.Kind == song.Program {
				continue
			}
			if err := s.sendBrain(ctx, songCommand(e)); err != nil {
				return err
			}
		}
		if err := s.sendBrain(ctx, songCommand(tp.MuteEvent(false))); err != nil {
			return err
		}
		return sleepContext(ctx, songSettleWait)
	}
	// restore puts the track back into the sound it declares.
	restore := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if hp := node.HomePreset(); hp != "" && len(names) > 0 {
			if ev, ok := node.PresetEvent(hp); ok {
				_ = s.sendBrain(rctx, songCommand(ev))
				_ = sleepContext(rctx, load)
			}
		}
		for _, e := range home {
			if e.Kind != song.Program {
				_ = s.sendBrain(rctx, songCommand(e))
			}
		}
	}
	defer restore()

	play := names
	if len(play) == 0 {
		play = []string{""} // the sound as it is
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var rows []auditionRow
	stored, silent := 0, 0
	for _, name := range play {
		if err := set(name); err != nil {
			return textResult("audition_sound: "+err.Error(), true), nil
		}
		var m sound.Sound
		if args.Kit {
			m = sound.Sound{Velocity: velocity, PeakDB: -180, LevelDB: -180}
			for n := from; n <= to; n++ {
				h, err := s.hearNote(ctx, tap, tp.Channel, n, velocity, hold, tail)
				if err != nil {
					return textResult("audition_sound: "+err.Error(), true), nil
				}
				pad, sounds := sound.MeasureHit(sound.Clip{Samples: h.Clip.Samples, Channels: h.Clip.Channels, SampleRate: h.Clip.SampleRate, HoldMS: float64(hold.Milliseconds())}, n)
				if !sounds || !h.Sounds() {
					continue
				}
				m.Pads = append(m.Pads, pad)
				if pad.PeakDB > m.PeakDB {
					m.PeakDB, m.LevelDB = pad.PeakDB, pad.LevelDB
				}
			}
			m.Lanes = sound.NameLanes(m.Pads)
			m.Unpitched = true
			// A kit's level is its kick's when it has one: the loudest pad is
			// often a snare far above the rest, and a kit trimmed by that
			// plays too quiet as a whole.
			for _, pad := range m.Pads {
				if pad.Lane == "kick" {
					m.LevelDB = pad.LevelDB
				}
			}
			if len(m.Pads) == 0 {
				m.PeakDB, m.LevelDB = sound.SilenceDB, sound.SilenceDB
				m.Remarks = append(m.Remarks, fmt.Sprintf("silent: no note from %d to %d made a sound", from, to))
			}
		} else {
			h, err := s.hearNote(ctx, tap, tp.Channel, note, velocity, hold, tail)
			if err != nil {
				return textResult("audition_sound: "+err.Error(), true), nil
			}
			m = sound.MeasureNote(sound.Clip{Samples: h.Clip.Samples, Channels: h.Clip.Channels, SampleRate: h.Clip.SampleRate, HoldMS: float64(hold.Milliseconds())}, note, velocity)
		}
		m.Measured = now
		row := auditionRow{Preset: name, Sound: &m}
		// Without a preset named, the sound heard is the track's own preset,
		// or the plugin's default patch when it declares none.
		key := name
		if key == "" {
			key = node.HomePreset()
		}
		// Silence says nothing of the preset: the plugin did not load, or the
		// note is none of its notes. It is reported and not kept.
		if save && !m.Silent() {
			kept, err := s.sounds.Put(node.Plugin, node.Name, key, m)
			if err != nil {
				warnings = append(warnings, "the catalogue was not written: "+err.Error())
			} else {
				row.Sound, row.Stored = kept, true
			}
		}
		if m.Silent() {
			silent++
		} else if save {
			stored++
		}
		if row.Preset == "" {
			row.Preset = key
		}
		rows = append(rows, row)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s on track %s of %q", node.Name, tp.ID, cs.id)
	if args.Kit {
		fmt.Fprintf(&b, ", notes %d-%d at velocity %d\n", from, to, velocity)
	} else {
		fmt.Fprintf(&b, ", %s at velocity %d held %d ms\n", song.NoteName(note), velocity, hold.Milliseconds())
	}
	for _, row := range rows {
		b.WriteString(describeSound(row.Preset, row.Sound, args.Kit))
	}
	switch {
	case save && stored > 0:
		fmt.Fprintf(&b, "stored in the sound catalogue (%d)\n", stored)
	case !clean:
		fmt.Fprintf(&b, "not stored: %s, so this is the chain's sound, not the preset's\n", why)
	}
	if silent > 0 {
		fmt.Fprintf(&b, "%d silent and not stored: a plugin that makes no sound for any preset did not load (AUM draws its node with a red ring; check_song says so), and loading the session again may bring it back\n", silent)
	}
	if more > 0 {
		fmt.Fprintf(&b, "%d more preset(s) match %q (raise limit, or narrow the query)\n", more, args.Query)
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return structResult(b.String(), map[string]any{
		"song": cs.id, "track": tp.ID, "plugin": node.Plugin, "name": node.Name,
		"note": note, "velocity": velocity, "kit": args.Kit, "clean_path": clean,
		"sounds": rows, "more_matching": more, "warnings": warnings,
	}), nil
}

// trackIDs lists a song's track ids for a message.
func trackIDs(cs *compiledSong) string {
	ids := make([]string, len(cs.plan.Tracks))
	for i, tp := range cs.plan.Tracks {
		ids[i] = tp.ID
	}
	return strings.Join(ids, ", ")
}

// cleanPath reports whether a track's sound is its instrument's preset alone:
// no effects after it or on the master, no parameter set on it, the fader at
// unity. Otherwise it says what is in the way.
func cleanPath(sg *song.Song, track string) (bool, string) {
	if len(sg.Master) > 0 {
		return false, "the master has effects"
	}
	for _, t := range sg.Tracks {
		if t.ID != track {
			continue
		}
		switch {
		case len(t.Effects) > 0:
			return false, "the track has effects"
		case len(t.Instrument.Params) > 0:
			return false, "the song sets parameters on the instrument"
		case t.Level != nil && *t.Level != 0:
			return false, "the track's fader is not at unity"
		}
	}
	return true, ""
}

// describeSound renders one measured preset as a block of text.
func describeSound(preset string, m *sound.Sound, kit bool) string {
	var b strings.Builder
	name := preset
	if name == "" {
		name = sound.DefaultPatch
	}
	if kit || len(m.Pads) > 0 {
		fmt.Fprintf(&b, "  %s: %d pad(s)\n", name, len(m.Pads))
		for _, p := range m.Pads {
			lane := p.Lane
			if lane == "" {
				lane = "?"
			}
			fmt.Fprintf(&b, "    %3d %-4s %-10s peak %6.1f dBFS  %4.0f ms%s  centre %5.0f Hz", p.Note, song.NoteName(p.Note), lane, p.PeakDB, p.LengthMS, ringMark(p.Rings), p.CentroidHz)
			if p.PitchHz > 0 {
				fmt.Fprintf(&b, "  pitch %.0f Hz", p.PitchHz)
			}
			b.WriteByte('\n')
		}
		if len(m.Lanes) > 0 {
			lanes := make([]string, 0, len(m.Lanes))
			for lane, note := range m.Lanes {
				lanes = append(lanes, fmt.Sprintf("%s: %d", lane, note))
			}
			sort.Strings(lanes)
			fmt.Fprintf(&b, "    drum map (guessed from the sound; set_sound corrects it): {%s}\n", strings.Join(lanes, ", "))
		}
	} else if m.Silent() {
		fmt.Fprintf(&b, "  %s: silent\n", name)
		return b.String()
	} else {
		pitch := "no pitch"
		if !m.Unpitched {
			pitch = fmt.Sprintf("sounds %s %+.0fc, transpose %+d", m.Sounds, m.Cents, m.Transpose)
		}
		env := "sustains"
		if !m.Sustains {
			env = fmt.Sprintf("decays in %.0f ms", m.DecayMS)
		}
		fmt.Fprintf(&b, "  %s: %s | rms %.1f peak %.1f dBFS | attack %.0f ms, %s, release %.0f ms%s\n",
			name, pitch, m.LevelDB, m.PeakDB, m.AttackMS, env, m.ReleaseMS, ringMark(m.Rings))
	}
	for _, r := range m.Remarks {
		fmt.Fprintf(&b, "    - %s\n", r)
	}
	if m.Comment != "" {
		fmt.Fprintf(&b, "    comment: %s\n", m.Comment)
	}
	return b.String()
}

func ringMark(rings bool) string {
	if rings {
		return "+"
	}
	return ""
}

// --- the catalogue --------------------------------------------------------------

func (s *Server) handleListSounds(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Plugin string `json:"plugin"`
		Query  string `json:"query"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	plugins, err := s.sounds.Plugins()
	if err != nil {
		return textResult("read the sound catalogue: "+err.Error(), true), nil
	}
	only := ""
	if args.Plugin != "" {
		// A plugin that is no longer staged can still be in the catalogue.
		if id, rerr := songCatalog().Resolve(args.Plugin); rerr == nil {
			only = id
		} else {
			only = args.Plugin
		}
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	var b strings.Builder
	out := []map[string]any{}
	count := 0
	for _, p := range plugins {
		if only != "" && p.Plugin != only {
			continue
		}
		names := make([]string, 0, len(p.Presets))
		for name, m := range p.Presets {
			if query != "" && !strings.Contains(strings.ToLower(name+" "+strings.Join(m.Remarks, " ")+" "+m.Comment), query) {
				continue
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "%s (%s)\n", p.Name, p.Plugin)
		presets := map[string]*sound.Sound{}
		for _, name := range names {
			m := p.Presets[name]
			presets[name] = m
			b.WriteString(describeSound(name, m, len(m.Pads) > 0))
			count++
		}
		out = append(out, map[string]any{"plugin": p.Plugin, "name": p.Name, "presets": presets})
	}
	if count == 0 {
		return structResult("the sound catalogue has nothing for that yet: audition_sound measures presets (in the lab, open_sound_lab)", map[string]any{"plugins": out}), nil
	}
	return structResult(fmt.Sprintf("%d measured preset(s):\n%s", count, b.String()), map[string]any{"plugins": out}), nil
}

func (s *Server) handleSetSound(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Plugin    string         `json:"plugin"`
		Preset    string         `json:"preset"`
		Transpose *int           `json:"transpose"`
		Lanes     map[string]int `json:"lanes"`
		Comment   *string        `json:"comment"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Transpose == nil && args.Lanes == nil && args.Comment == nil {
		return textResult("give transpose, lanes or comment", true), nil
	}
	id := args.Plugin
	if resolved, err := songCatalog().Resolve(args.Plugin); err == nil {
		id = resolved
	}
	for lane, note := range args.Lanes {
		if strings.TrimSpace(lane) == "" || note < 0 || note > 127 {
			return textResult(fmt.Sprintf("/lanes/%s: a lane is a name on a MIDI note (0..127)", lane), true), nil
		}
	}
	m, err := s.sounds.Correct(id, args.Preset, func(m *sound.Sound) []string {
		var set []string
		if args.Transpose != nil {
			m.Transpose = *args.Transpose
			set = append(set, "transpose")
		}
		if args.Lanes != nil {
			m.Lanes = args.Lanes
			byNote := map[int]string{}
			for lane, note := range args.Lanes {
				byNote[note] = lane
			}
			for i := range m.Pads {
				m.Pads[i].Lane = byNote[m.Pads[i].Note]
			}
			set = append(set, "lanes")
		}
		if args.Comment != nil {
			m.Comment = *args.Comment
		}
		return set
	})
	if err != nil {
		return textResult("set_sound: "+err.Error()+" (audition_sound measures it first)", true), nil
	}
	return structResult("corrected:\n"+describeSound(args.Preset, m, len(m.Pads) > 0), map[string]any{"plugin": id, "preset": args.Preset, "sound": m}), nil
}
