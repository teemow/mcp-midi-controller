package mcpserver

// This file is the MCP surface over internal/song: write a song as one
// document, author the AUM session that hosts its instruments and effects, and
// play it through the ProbeMidiBrain so it can be heard (and listened to with
// the audio tap) without a sequencer in the session.
//
// A song lives in the config dir like a scene does. The session authored from
// it is staged like every other authored session, and the daemon remembers
// which session a song was authored into so play_song can tell when a song has
// outgrown the session that is loaded.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport/midicontrol"
)

// songFormat is the song document in brief, carried by save_song so an agent
// can write one from the tool listing alone. docs/songs.md is the long form.
const songFormat = `A song is one YAML document:
title, tempo (BPM), time_signature ("4/4"), swing (0..1, optional).
tracks: [{id, name?, kind: drums?, transpose?, level (dB, 0 = unity)?, mute?, drum_map?,
  instrument: {plugin, preset?, params?},
  effects: [{id?, plugin, preset?, bypass?, params?}]}] - plugin is a staged AUv3 probe id (list_song_plugins), preset one of its factory preset names, params {name: value}: a number in the parameter's own range, "NN%" of it, or a value label (get_song_plugin lists both). Each track plays on its own MIDI channel, in order from 1.
master: [effects on the master strip].
patterns: {id: {step: "1/16" (default; 1/8, 1/4, 1/8t ...), bars?, gate? (0.9), velocity? (96),
  notes: grid line or list of lines (voices that sound together) | drums: {lane: grid} | events: [{at, dur (beats), note, vel?}]}}
  Melodic grid: steps separated by spaces - "A2" note (C4 = middle C), "[A3 C4 E4]" chord, "-" hold the previous step, "." rest; add "!" to accent or ":NN" for a velocity.
  Drum grid: one character per step - "x" hit, "X" accent, "o" soft, "." rest. Lanes: kick snare clap rim hat open_hat tom_low tom_mid tom_high crash ride cowbell tamb shaker (or the track's drum_map, or a note).
  "|" marks a bar line in both and is checked: every bar must hold exactly a bar's steps.
scenes: {id: {track: {level?, mute?, preset?, params?, effects: {effect id: {bypass?, preset?, params?}}}}} - "master" addresses the master effects.
sections: {id: {bars, scene?: id or [ids], play: {track: pattern or [patterns]}, automation?: [{track, effect?, param ("level" for the fader), from, to, bar?, bars?}]}} - patterns repeat until the section ends; a track left out is silent.
arrangement: [section ids in playing order].
A section sounds like the tracks' own declarations overlaid with its scenes, so anything a scene changes must also be declared on the track (its value everywhere else).`

func (s *Server) registerSongTools() {
	songArg := `"song": {"type": "string", "description": "Song id (see list_songs)."}`

	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_songs",
		Description: "List the stored songs: id, title, tempo, length, tracks and the AUM session each was last authored into.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListSongs)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_song",
		Description: "Get a stored song: its YAML document and what it compiles to — the arrangement with each section's bars and start time, the tracks with their MIDI channel, plugins and note count. " +
			"Edit the document and hand it back to save_song to change the song.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + songArg + `},"required":["song"]}`),
	}, s.handleGetSong)

	s.mcp.AddTool(&mcp.Tool{
		Name: "save_song",
		Description: "Create or replace a song. The document is checked before it is stored: every reference, every pattern (a miscounted bar is an error), and — against the staged AUv3 probes — every plugin, preset and parameter it names; problems come back with their place in the document and what would have matched. " +
			"Then author its session with author_song_session and play it with play_song.\n\n" + songFormat,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"song": {"type": "string", "description": "Song id to store it under (default: from the title)."},
				"yaml": {"type": "string", "description": "The song document as YAML text (kept verbatim, comments included)."},
				"document": {"type": "object", "description": "The song document as an object (alternative to yaml)."},
				"dry_run": {"type": "boolean", "description": "Check and compile the document without storing it."}
			}
		}`),
	}, s.handleSaveSong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "delete_song",
		Description: "Delete a stored song. Its authored AUM session stays staged.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + songArg + `},"required":["song"]}`),
	}, s.handleDeleteSong)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_song_plugins",
		Description: "List the plugins a song can host — the staged AUv3 probes — by role: instruments, effects and MIDI processors, each with how many factory presets and writable parameters it has. " +
			"Filter by kind and by a word in the name. Use get_song_plugin for one plugin's presets and parameters.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"kind": {"type": "string", "enum": ["instrument", "effect", "midi"], "description": "Only plugins of this role."},
				"query": {"type": "string", "description": "Only plugins whose name or maker contains this text."}
			}
		}`),
	}, s.handleListSongPlugins)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_song_plugin",
		Description: "Get what a song can set on one plugin: its factory presets by name and its writable parameters with range, current value and value labels. " +
			"query narrows both lists to the names containing it (\"bass\", \"pad\", \"cutoff\") — a plugin can have hundreds of presets.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugin": {"type": "string", "description": "Staged AUv3 probe id (see list_song_plugins)."},
				"query": {"type": "string", "description": "Only presets and parameters whose name contains this text."},
				"limit": {"type": "integer", "description": "Most presets and most parameters to list (default 60)."}
			},
			"required": ["plugin"]
		}`),
	}, s.handleGetSongPlugin)

	s.mcp.AddTool(&mcp.Tool{
		Name: "author_song_session",
		Description: "Author the AUM session a song plays in and stage it for the iPad: a ProbeMidiBrain strip, one strip per track (instrument, effect chain, fader) and a master strip with the master effects and a ProbeAudioTap. " +
			"The MIDI matrix gives every track its own channel and keeps notes out of AUM's MIDI Control; every control the song uses (levels, mutes, bypasses, parameters, preset loads) is mapped. " +
			"Load the staged session in AUM, then play_song. Author again after changing what the song hosts or controls, or its tempo — changing only notes, patterns or the arrangement needs no new session.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"out_id": {"type": "string", "description": "Staging id of the session, which may name a subfolder (default \"Songs/<title>\")."},
				"track_taps": {"type": "boolean", "description": "Also put a ProbeAudioTap after every track's fader, so each track can be listened to on its own (get_audio_tap name=…). The master always has one."},
				"full_control": {"type": "boolean", "description": "Also map every other parameter of every plugin, around the song's own mappings, so the whole session is controllable (control_* tools, the brain's surface)."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Hardware-I/O profile for the master's output (default builtin)."}
			},
			"required": ["song"]
		}`),
	}, s.handleAuthorSongSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "play_song",
		Description: "Play a song through the ProbeMidiBrain: set the session to the opening section's sound, then send every note and control change when it is due. Returns at once; the song plays on (get_song_playback, stop_song) — listen with get_audio_tap meanwhile. " +
			"Play the whole song, or from a section, for a number of sections, or only some tracks. The song's session must be the one loaded in AUM.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"from": {"type": "string", "description": "Where to start: a section id (its first occurrence) or a 1-based position in the arrangement. Default: the top."},
				"sections": {"type": "integer", "minimum": 1, "description": "How many arrangement entries to play (default: to the end)."},
				"tracks": {"type": "array", "items": {"type": "string"}, "description": "Play only these tracks' notes (default: all)."},
				"transport": {"type": "boolean", "description": "Also start AUM's transport with the first note and stop it at the end, for plugins that follow the host clock."},
				"preroll_ms": {"type": "integer", "minimum": 0, "description": "Wait between setting the opening sound and the first note (default 1200 when presets load, else 150)."}
			},
			"required": ["song"]
		}`),
	}, s.handlePlaySong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "stop_song",
		Description: "Stop the playing song and release every sounding note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleStopSong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_song_playback",
		Description: "Where the playing song stands (section, bar, position), or how the last playback ended.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleGetSongPlayback)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "export_song_midi",
		Description: "Export a song as a Standard MIDI File: the tempo and a marker per section, one track per song track, and the control messages of its session on a track of their own. For a sequencer or a DAW.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + songArg + `},"required":["song"]}`),
	}, s.handleExportSongMIDI)
}

// --- plugins ----------------------------------------------------------------

// songCatalog loads the staged AUv3 probes as the plugins a song may host,
// keyed by staged probe id (the dump's file name).
func songCatalog() song.Catalog {
	dir := config.AUv3ProbesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return song.Catalog{}
	}
	cat := song.Catalog{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if dump, derr := readProbeDump(filepath.Join(dir, e.Name())); derr == nil {
			cat[strings.TrimSuffix(e.Name(), ".json")] = dump
		}
	}
	return cat
}

// pluginKind is the role a component type plays in a song.
func pluginKind(componentType string) string {
	switch componentType {
	case "aumu":
		return "instrument"
	case "aumi":
		return "midi"
	default: // aufx, aumf
		return "effect"
	}
}

func writableCount(dump device.ProbeDump) int {
	n := 0
	for _, p := range dump.Parameters {
		if p.Writable {
			n++
		}
	}
	return n
}

type songPluginRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Maker    string `json:"maker,omitempty"`
	Kind     string `json:"kind"`
	Presets  int    `json:"presets"`
	Writable int    `json:"writable_params"`
}

func (s *Server) handleListSongPlugins(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Kind  string `json:"kind"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	cat := songCatalog()
	rows := []songPluginRow{}
	for id, dump := range cat {
		row := songPluginRow{
			ID: id, Name: dump.Name,
			Maker:    device.FirstNonEmpty(dump.Component.ManufacturerName, dump.Component.Manufacturer),
			Kind:     pluginKind(dump.Component.Type),
			Presets:  len(dump.FactoryPresets),
			Writable: writableCount(dump),
		}
		if args.Kind != "" && row.Kind != args.Kind {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(row.Name+" "+row.Maker+" "+id), query) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	if len(cat) == 0 {
		return structResult("no staged AUv3 probes: open the auv3-probe app on the iPad so it syncs the installed audio units to the daemon",
			map[string]any{"plugins": rows}), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d plugin(s)\n", len(rows))
	kind := ""
	for _, r := range rows {
		if r.Kind != kind {
			kind = r.Kind
			fmt.Fprintf(&b, "%s:\n", kind)
		}
		fmt.Fprintf(&b, "  %-6s %s [%s]: %d preset(s), %d writable param(s)\n", r.ID, r.Name, r.Maker, r.Presets, r.Writable)
	}
	return structResult(b.String(), map[string]any{"plugins": rows}), nil
}

type songParamRow struct {
	Name   string   `json:"name"`
	ID     string   `json:"id"`
	Min    float64  `json:"min"`
	Max    float64  `json:"max"`
	Value  float64  `json:"value"`
	Unit   string   `json:"unit,omitempty"`
	Group  string   `json:"group,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

func (s *Server) handleGetSongPlugin(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Plugin string `json:"plugin"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	dump, ok := songCatalog()[args.Plugin]
	if !ok {
		return textResult(fmt.Sprintf("/plugin: no staged AUv3 probe %q (see list_song_plugins)", args.Plugin), true), nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 60
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	match := func(names ...string) bool {
		return query == "" || strings.Contains(strings.ToLower(strings.Join(names, " ")), query)
	}

	presets := []string{}
	presetHits := 0
	for _, p := range dump.FactoryPresets {
		if !match(p.Name) {
			continue
		}
		if presetHits++; len(presets) < limit {
			presets = append(presets, p.Name)
		}
	}
	params := []songParamRow{}
	paramHits := 0
	for _, p := range dump.Parameters {
		if !p.Writable || !match(p.DisplayName, p.Identifier, p.Group) {
			continue
		}
		if paramHits++; len(params) < limit {
			params = append(params, songParamRow{
				Name: device.FirstNonEmpty(p.DisplayName, p.Identifier), ID: p.Identifier,
				Min: p.Min, Max: p.Max, Value: p.Value,
				Unit: device.FirstNonEmpty(p.UnitName, p.Unit), Group: p.Group, Labels: p.ValueStrings,
			})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s): %d factory preset(s), %d writable param(s)\n",
		dump.Name, args.Plugin, pluginKind(dump.Component.Type), len(dump.FactoryPresets), writableCount(dump))
	fmt.Fprintf(&b, "presets (%d of %d matching):\n", len(presets), presetHits)
	for _, p := range presets {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	fmt.Fprintf(&b, "params (%d of %d matching):\n", len(params), paramHits)
	for _, p := range params {
		fmt.Fprintf(&b, "  %s", p.Name)
		if p.ID != "" && p.ID != p.Name {
			fmt.Fprintf(&b, " (%s)", p.ID)
		}
		fmt.Fprintf(&b, ": %g..%g, now %g", p.Min, p.Max, p.Value)
		if len(p.Labels) > 0 && len(p.Labels) <= 16 {
			fmt.Fprintf(&b, " [%s]", strings.Join(p.Labels, ", "))
		}
		b.WriteByte('\n')
	}
	return structResult(b.String(), map[string]any{
		"plugin": args.Plugin, "name": dump.Name, "kind": pluginKind(dump.Component.Type),
		"presets": presets, "presets_matching": presetHits,
		"params": params, "params_matching": paramHits,
	}), nil
}

// --- songs ------------------------------------------------------------------

// compiledSong is a song with everything derived from it.
type compiledSong struct {
	id       string
	song     *song.Song
	plan     *song.Plan
	timeline *song.Timeline
}

// compileSong plans a song against the staged probes and compiles it.
func compileSong(id string, sg *song.Song) (*compiledSong, error) {
	plan, err := song.NewPlan(sg, songCatalog())
	if err != nil {
		return nil, err
	}
	tl, err := song.Compile(sg, plan)
	if err != nil {
		return nil, err
	}
	return &compiledSong{id: id, song: sg, plan: plan, timeline: tl}, nil
}

// loadSong loads and compiles a stored song, returning a tool error on failure.
func (s *Server) loadSong(id string) (*compiledSong, *mcp.CallToolResult) {
	if strings.TrimSpace(id) == "" {
		return nil, textResult("/song: name the song (see list_songs)", true)
	}
	sg, _, err := s.songs.Load(id)
	if err != nil {
		return nil, textResult("/song: "+err.Error(), true)
	}
	cs, err := compileSong(song.ID(id), sg)
	if err != nil {
		return nil, textResult(fmt.Sprintf("song %q does not compile against the staged plugins: %v", song.ID(id), err), true)
	}
	return cs, nil
}

// clock renders a duration as m:ss.
func clock(d time.Duration) string {
	sec := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// songSummary is the machine-readable summary of a compiled song.
type songSummary struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Tempo    float64          `json:"tempo"`
	Bars     int              `json:"bars"`
	Seconds  float64          `json:"seconds"`
	Sections []songSectionRow `json:"sections"`
	Tracks   []songTrackRow   `json:"tracks"`
	Session  string           `json:"session,omitempty"`
	// SessionCurrent reports whether the authored session still matches the
	// song's control layout.
	SessionCurrent *bool `json:"session_current,omitempty"`
}

type songSectionRow struct {
	Position int     `json:"position"`
	Name     string  `json:"name"`
	Bar      int     `json:"bar"`
	Bars     int     `json:"bars"`
	AtSecond float64 `json:"at_second"`
}

type songTrackRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Channel    int      `json:"channel"`
	Instrument string   `json:"instrument"`
	Effects    []string `json:"effects"`
	Notes      int      `json:"notes"`
}

// summarize renders a compiled song as text and as its structured summary.
func (s *Server) summarize(cs *compiledSong) (string, songSummary) {
	tl := cs.timeline
	sum := songSummary{
		ID: cs.id, Title: cs.song.Title, Tempo: tl.Tempo, Bars: tl.Bars,
		Seconds: tl.Duration().Seconds(), Sections: []songSectionRow{}, Tracks: []songTrackRow{},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q (%s): %g BPM, %d bars, %s\n", cs.song.Title, cs.id, tl.Tempo, tl.Bars, clock(tl.Duration()))
	b.WriteString("arrangement:\n")
	for i, sec := range tl.Sections {
		at := tl.TickTime(sec.Tick)
		fmt.Fprintf(&b, "  %2d  %-12s bars %d-%d  %s\n", i+1, sec.Name, sec.Bar, sec.Bar+sec.Bars-1, clock(at))
		sum.Sections = append(sum.Sections, songSectionRow{Position: i + 1, Name: sec.Name, Bar: sec.Bar, Bars: sec.Bars, AtSecond: at.Seconds()})
	}
	b.WriteString("tracks:\n")
	for i, tp := range cs.plan.Tracks {
		row := songTrackRow{ID: tp.ID, Name: tp.Name, Channel: tp.Channel, Effects: []string{}, Notes: tl.Tracks[i].Notes}
		for j, n := range tp.Nodes {
			if j == 0 {
				row.Instrument = n.Name
			} else {
				row.Effects = append(row.Effects, n.Name)
			}
		}
		fmt.Fprintf(&b, "  ch%-2d %-10s %s", tp.Channel, tp.ID, row.Instrument)
		if len(row.Effects) > 0 {
			fmt.Fprintf(&b, " > %s", strings.Join(row.Effects, " > "))
		}
		fmt.Fprintf(&b, "  (%d notes)\n", row.Notes)
		sum.Tracks = append(sum.Tracks, row)
	}
	if len(cs.plan.Master) > 0 {
		names := make([]string, len(cs.plan.Master))
		for i, n := range cs.plan.Master {
			names[i] = n.Name
		}
		fmt.Fprintf(&b, "  master: %s\n", strings.Join(names, " > "))
	}
	if rec, ok := loadSongSessions()[cs.id]; ok {
		current := rec.Fingerprint == tl.Fingerprint
		sum.Session, sum.SessionCurrent = rec.Session, &current
		if current {
			fmt.Fprintf(&b, "session: %s\n", rec.Session)
		} else {
			fmt.Fprintf(&b, "session: %s is out of date (the song's tempo, plugins or controls changed): run author_song_session and load it again\n", rec.Session)
		}
	} else {
		b.WriteString("session: not authored yet (author_song_session)\n")
	}
	return b.String(), sum
}

func (s *Server) handleListSongs(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ids, err := s.songs.List()
	if err != nil {
		return textResult("list songs: "+err.Error(), true), nil
	}
	type row struct {
		ID      string  `json:"id"`
		Title   string  `json:"title,omitempty"`
		Tempo   float64 `json:"tempo,omitempty"`
		Bars    int     `json:"bars,omitempty"`
		Seconds float64 `json:"seconds,omitempty"`
		Tracks  int     `json:"tracks,omitempty"`
		Session string  `json:"session,omitempty"`
		Error   string  `json:"error,omitempty"`
	}
	rows := []row{}
	sessions := loadSongSessions()
	var b strings.Builder
	for _, id := range ids {
		sg, _, err := s.songs.Load(id)
		if err != nil {
			rows = append(rows, row{ID: id, Error: err.Error()})
			fmt.Fprintf(&b, "  %s (does not load: %v)\n", id, err)
			continue
		}
		// The notes alone: listing must work without the plugins staged.
		tl, err := song.Compile(sg, nil)
		if err != nil {
			rows = append(rows, row{ID: id, Title: sg.Title, Error: err.Error()})
			fmt.Fprintf(&b, "  %s (does not compile: %v)\n", id, err)
			continue
		}
		r := row{ID: id, Title: sg.Title, Tempo: sg.Tempo, Bars: tl.Bars, Seconds: tl.Duration().Seconds(), Tracks: len(sg.Tracks), Session: sessions[id].Session}
		rows = append(rows, r)
		fmt.Fprintf(&b, "  %-24s %q  %g BPM, %d bars, %s, %d track(s)", id, sg.Title, sg.Tempo, tl.Bars, clock(tl.Duration()), len(sg.Tracks))
		if r.Session != "" {
			fmt.Fprintf(&b, ", session %s", r.Session)
		}
		b.WriteByte('\n')
	}
	if len(rows) == 0 {
		return structResult(fmt.Sprintf("no songs in %s yet (save_song)", config.SongsDir()), map[string]any{"songs": rows}), nil
	}
	return structResult(fmt.Sprintf("%d song(s):\n%s", len(rows), b.String()), map[string]any{"songs": rows}), nil
}

func (s *Server) handleGetSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song string `json:"song"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	sg, raw, err := s.songs.Load(args.Song)
	if err != nil && raw == nil {
		return textResult("/song: "+err.Error(), true), nil
	}
	id := song.ID(args.Song)
	if err != nil {
		// Stored but no longer valid: hand the text back so it can be fixed.
		return structResult(fmt.Sprintf("%v\n\n%s", err, raw), map[string]any{"id": id, "yaml": string(raw), "error": err.Error()}), nil
	}
	cs, err := compileSong(id, sg)
	if err != nil {
		return structResult(fmt.Sprintf("song %q does not compile against the staged plugins: %v\n\n%s", id, err, raw),
			map[string]any{"id": id, "yaml": string(raw), "error": err.Error()}), nil
	}
	text, sum := s.summarize(cs)
	return structResult(text+"\n"+string(raw), map[string]any{"id": id, "summary": sum, "yaml": string(raw)}), nil
}

func (s *Server) handleSaveSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song     string          `json:"song"`
		YAML     string          `json:"yaml"`
		Document json.RawMessage `json:"document"`
		DryRun   bool            `json:"dry_run"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	data := []byte(args.YAML)
	switch {
	case strings.TrimSpace(args.YAML) != "" && len(args.Document) > 0:
		return textResult("provide yaml or document, not both", true), nil
	case strings.TrimSpace(args.YAML) == "" && len(args.Document) == 0:
		return textResult("provide the song as yaml (text) or document (object)", true), nil
	case len(args.Document) > 0:
		// Store an object as YAML too, so every stored song reads the same.
		var doc any
		if err := json.Unmarshal(args.Document, &doc); err != nil {
			return textResult("/document: "+err.Error(), true), nil
		}
		out, err := yaml.Marshal(doc)
		if err != nil {
			return textResult("/document: "+err.Error(), true), nil
		}
		data = out
	}

	sg, err := song.Parse(data)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	id := song.ID(device.FirstNonEmpty(args.Song, sg.Title))
	if id == "" {
		return textResult("/song: give the song an id or a title", true), nil
	}
	cs, err := compileSong(id, sg)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	verb := "checked"
	if !args.DryRun {
		if _, err := s.songs.Save(id, data); err != nil {
			return textResult("save song: "+err.Error(), true), nil
		}
		verb = "saved"
	}
	text, sum := s.summarize(cs)
	return structResult(verb+" "+text, map[string]any{"id": id, "saved": !args.DryRun, "summary": sum}), nil
}

func (s *Server) handleDeleteSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song string `json:"song"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if err := s.songs.Delete(args.Song); err != nil {
		return textResult("/song: "+err.Error(), true), nil
	}
	return textResult(fmt.Sprintf("deleted song %q", song.ID(args.Song)), false), nil
}

// --- session ----------------------------------------------------------------

// songSessionRecord is the session a song was last authored into.
type songSessionRecord struct {
	Session     string `json:"session"`
	Fingerprint string `json:"fingerprint"`
}

// loadSongSessions reads the record of authored song sessions (empty when
// there is none yet).
func loadSongSessions() map[string]songSessionRecord {
	out := map[string]songSessionRecord{}
	data, err := os.ReadFile(config.SongSessionsPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func (s *Server) recordSongSession(id string, rec songSessionRecord) error {
	s.songSessionsMu.Lock()
	defer s.songSessionsMu.Unlock()
	all := loadSongSessions()
	all[id] = rec
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	path := config.SongSessionsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(path, data, 0o644)
}

// sessionFileName turns a song title into a file name AUM shows as the
// session's name: the title as written, minus what a path cannot hold.
func sessionFileName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < ' ':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(strings.Join(strings.Fields(b.String()), " "), ". ")
}

func (s *Server) handleAuthorSongSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song        string `json:"song"`
		OutID       string `json:"out_id"`
		TrackTaps   bool   `json:"track_taps"`
		FullControl bool   `json:"full_control"`
		Hardware    string `json:"hardware"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	hardware, err := parseHardwareProfile(args.Hardware)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	outID := strings.TrimSpace(args.OutID)
	if outID == "" {
		outID = "Songs/" + device.FirstNonEmpty(sessionFileName(cs.song.Title), cs.id)
	}
	rel, ok := aum.SafeRelPath(outID + ".aumproj")
	if !ok {
		return textResult(fmt.Sprintf("/out_id: %q is not a relative path inside the staging dir", args.OutID), true), nil
	}
	outID = aum.StripExt(rel)

	cat := songCatalog()
	opts := song.SessionOptions{Hardware: hardware, TrackTaps: args.TrackTaps, FullControl: args.FullControl}
	if d, ok := cat["pbmi"]; ok {
		opts.Brain = &d
	}
	if d, ok := cat["pbau"]; ok {
		opts.Tap = &d
	}
	defaults := loadAUv3DefaultStates()
	opts.Prepare = func(ns *aum.NodeSpec) error { return applyDefaultState(ns, defaults) }

	sess, rep, err := cs.plan.BuildSession(opts)
	if err != nil {
		return textResult("author session: "+err.Error(), true), nil
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	if _, err := aum.Open(data); err != nil {
		return textResult("authored session failed re-decode: "+err.Error(), true), nil
	}
	path, file, err := stageAUMFile(outID, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}
	if err := s.recordSongSession(cs.id, songSessionRecord{Session: outID, Fingerprint: cs.timeline.Fingerprint}); err != nil {
		return textResult("record song session: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "authored the session of %q -> %s\n", cs.song.Title, path)
	fmt.Fprintf(&b, "  %d strip(s), %d plugin node(s), %d mapping(s), %d preset load(s)", rep.Strips, rep.Nodes, rep.Mappings, rep.Presets)
	if args.FullControl {
		fmt.Fprintf(&b, ", %d further target(s) mapped (%d did not fit)", rep.Banked, rep.Overflow)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "  strip 1: Brain (ProbeMidiBrain), which plays every track and drives MIDI Control on channels %d-16\n", cs.plan.ControlChannels[0])
	for _, tp := range cs.plan.Tracks {
		names := make([]string, len(tp.Nodes))
		for i, n := range tp.Nodes {
			names[i] = n.Name
		}
		fmt.Fprintf(&b, "  strip %d: %s on MIDI channel %d: %s\n", tp.Strip+1, tp.Name, tp.Channel, strings.Join(names, " > "))
	}
	fmt.Fprintf(&b, "  strip %d: Master", cs.plan.MasterStrip+1)
	for _, n := range cs.plan.Master {
		fmt.Fprintf(&b, " > %s", n.Name)
	}
	b.WriteString(" > ProbeAudioTap\n")
	fmt.Fprintf(&b, "next: the iPad's auv3-probe app pulls it into AUM's folder (%s); open it in AUM, then play_song\n", file)

	return structResult(b.String(), map[string]any{
		"song":     cs.id,
		"id":       outID,
		"file":     file,
		"path":     path,
		"download": "/aum-session/" + file,
		"report":   rep,
		"layout":   cs.plan,
	}), nil
}

// --- playback ---------------------------------------------------------------

// songCommand renders a song event as the brain command that plays it.
func songCommand(e song.Event) midicontrol.Command {
	switch e.Kind {
	case song.NoteOn:
		return midicontrol.Command{Type: "noteOn", Channel: e.Channel, Note: e.Data1, Velocity: e.Data2}
	case song.NoteOff:
		return midicontrol.Command{Type: "noteOff", Channel: e.Channel, Note: e.Data1}
	case song.Program:
		return midicontrol.Command{Type: "pc", Channel: e.Channel, Program: e.Data1}
	default:
		return midicontrol.Command{Type: "cc", Channel: e.Channel, Controller: e.Data1, Value: e.Data2}
	}
}

// songStart resolves play_song's "from": a section id (its first occurrence)
// or a 1-based position in the arrangement.
func songStart(tl *song.Timeline, from string) (int, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return 0, nil
	}
	for i, sec := range tl.Sections {
		if sec.Name == from {
			return i, nil
		}
	}
	var pos int
	if _, err := fmt.Sscanf(from, "%d", &pos); err == nil && fmt.Sprint(pos) == from {
		if pos < 1 || pos > len(tl.Sections) {
			return 0, fmt.Errorf("the arrangement has positions 1..%d", len(tl.Sections))
		}
		return pos - 1, nil
	}
	names := make([]string, len(tl.Sections))
	for i, sec := range tl.Sections {
		names[i] = sec.Name
	}
	return 0, fmt.Errorf("no section %q in the arrangement (%s)", from, strings.Join(names, ", "))
}

func (s *Server) handlePlaySong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song      string   `json:"song"`
		From      string   `json:"from"`
		Sections  int      `json:"sections"`
		Tracks    []string `json:"tracks"`
		Transport bool     `json:"transport"`
		PrerollMS int      `json:"preroll_ms"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	if s.midi == nil || !s.midi.Connected() {
		return textResult("play_song: "+midicontrol.ErrNoBrain.Error()+" — load the song's session in AUM (author_song_session stages it) so its brain dials in", true), nil
	}
	from, err := songStart(cs.timeline, args.From)
	if err != nil {
		return textResult("/from: "+err.Error(), true), nil
	}
	opts := song.PlayOptions{
		From: from, Count: args.Sections, Tracks: args.Tracks,
		Preroll: time.Duration(args.PrerollMS) * time.Millisecond,
	}
	if args.Transport {
		start, stop := cs.plan.Transport["Start Play"], cs.plan.Transport["Stop/Rewind"]
		opts.Before = []song.Event{{Kind: song.Control, Channel: start.Channel, Data1: start.Number, Data2: 127}}
		opts.After = []song.Event{{Kind: song.Control, Channel: stop.Channel, Data1: stop.Number, Data2: 127}}
	}

	// The song must be playing into its own session. Both checks are
	// warnings: the daemon only knows what the iPad last downloaded, not what
	// AUM has open.
	warnings := []string{}
	rec, authored := loadSongSessions()[cs.id]
	switch {
	case !authored:
		warnings = append(warnings, "this song has no authored session yet (author_song_session); its control changes will not reach anything")
	case rec.Fingerprint != cs.timeline.Fingerprint:
		warnings = append(warnings, fmt.Sprintf("the song's tempo, plugins or controls changed since %q was authored: run author_song_session and load it again, or some sounds will be wrong", rec.Session))
	default:
		if cur := s.currentAUMSessionID(); cur != "" && cur != rec.Session {
			warnings = append(warnings, fmt.Sprintf("the iPad last loaded %q, not this song's session %q", cur, rec.Session))
		}
	}

	st, err := s.player.Start(cs.id, cs.timeline, opts, func(ctx context.Context, e song.Event) error {
		return s.sendBrain(ctx, songCommand(e))
	})
	if err != nil {
		return textResult("play_song: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "playing %q from %s (bar %d), %s", cs.song.Title, cs.timeline.Sections[from].Name, cs.timeline.Sections[from].Bar, clock(time.Duration(st.DurationMS)*time.Millisecond))
	if len(args.Tracks) > 0 {
		fmt.Fprintf(&b, ", tracks %s", strings.Join(args.Tracks, ", "))
	}
	b.WriteString("\nlisten with get_audio_tap, follow with get_song_playback, end with stop_song\n")
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return structResult(b.String(), map[string]any{"playback": st, "warnings": warnings}), nil
}

// playbackText renders a playback status.
func playbackText(st song.Status) string {
	switch {
	case st.Song == "":
		return "no song has been played"
	case st.Playing:
		return fmt.Sprintf("playing %q: %s, bar %d, %s of %s", st.Song, st.Section, st.Bar,
			clock(time.Duration(st.PositionMS)*time.Millisecond), clock(time.Duration(st.DurationMS)*time.Millisecond))
	default:
		return fmt.Sprintf("%q %s at %s of %s (%s, bar %d)", st.Song, st.Outcome,
			clock(time.Duration(st.PositionMS)*time.Millisecond), clock(time.Duration(st.DurationMS)*time.Millisecond), st.Section, st.Bar)
	}
}

func (s *Server) handleStopSong(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st := s.player.Stop()
	return structResult(playbackText(st), map[string]any{"playback": st}), nil
}

func (s *Server) handleGetSongPlayback(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st := s.player.Status()
	return structResult(playbackText(st), map[string]any{"playback": st}), nil
}

func (s *Server) handleExportSongMIDI(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song string `json:"song"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	dir := config.SongExportsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return textResult("export song: "+err.Error(), true), nil
	}
	path := filepath.Join(dir, cs.id+".mid")
	data := cs.timeline.SMF()
	if err := lanhttp.WriteFileAtomic(path, data, 0o644); err != nil {
		return textResult("export song: "+err.Error(), true), nil
	}
	notes := 0
	for _, t := range cs.timeline.Tracks {
		notes += t.Notes
	}
	return structResult(
		fmt.Sprintf("exported %q -> %s (%d bytes: %d track(s), %d notes, %d bars at %g BPM)", cs.song.Title, path, len(data), len(cs.timeline.Tracks), notes, cs.timeline.Bars, cs.timeline.Tempo),
		map[string]any{"song": cs.id, "path": path, "bytes": len(data), "tracks": len(cs.timeline.Tracks), "notes": notes}), nil
}
