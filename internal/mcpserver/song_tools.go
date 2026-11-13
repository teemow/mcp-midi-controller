package mcpserver

// This file is the MCP surface over internal/song: write a song as one
// document, author the AUM session that hosts its instruments and effects, and
// play it so it can be heard (and listened to with the audio tap): from the
// sequencer the session hosts with the whole song in it, or live through the
// ProbeMidiBrain, event by event.
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
	"github.com/teemow/mcp-midi-controller/internal/sound"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport/midicontrol"
)

// songFormat is the song document in brief, carried by save_song so an agent
// can write one from the tool listing alone. docs/songs.md is the long form.
const songFormat = `A song is one YAML document:
title, tempo (BPM), time_signature ("4/4"), swing (0..1, optional), calibrate (optional, see the end).
tracks: [{id, name?, kind: drums?, channel?, control_channels?, transpose?, level (dB, 0 = unity)?, mute?, drum_map?, group?,
  instrument: {plugin, preset?, params?, sequence?} or {file},
  effects: [{id?, plugin, preset?, bypass?, params?}],
  modulators: [{id?, plugin: LFOH, preset?, bypass?, params?, target: {effect?, param}, depth?: [low, high]}]}] - plugin is a staged AUv3 probe id (list_song_plugins) or a name that fits only one plugin, preset one of its factory preset names, params {name: value}: a number in the parameter's own range, "NN%" of it, or a value label (get_song_plugin lists both). Each track plays on its own MIDI channel, in order from 1 (channel: another of 1..12). control_channels: [10, 11] lets the track's instrument also hear those channels, for control notes such as a drum machine's mutes; no other track may use them.
  sequence (UA Battalion only, no preset beside it): the drum machine's own pattern per voice, written into its saved state and played with AUM's transport - {1..8: {steps: drum grid ("x" a step, "." none), length? (1..64 steps), vel|shift|pitch|ratch|env|pan|var|prob|sends|macro2: values 0..1 as a list or a grid line ("." neutral) or {values, length?, mode?: sync|free|trig|retrig}}}. sync lanes repeat with the steps; a free lane of another length, or a trig one, comes round in another place every time. pitch is round((v-0.5)*48) semitones, ratch 1+round(6v) hits a step, prob 0 never and 0.5 always. Scenes cannot change it (a lane's length and mode are params too); docs/songs.md has the rest.
  instrument: {file: "<session>/<name>.wav"} makes the track play a recording from AUM's Recordings folder instead of a plugin (AUM's File Player; effects, level, mute and level ramps as on any track, no notes, no preset or params). The file starts at the top of AUM's transport, so such a song plays from its session's sequencer, whole and from the top; session lead_bars says the lead-in the recording was made with. mix_song_stems writes such songs.
  modulators: an LFO (K-Devices LFOH, the one a song can host) on the track's strip that moves one parameter of the track's instrument (or of effect) by itself, between the two depth values (written as values of that parameter; default its whole range). The song routes it: LFOH's MIDI Ch/CC/Min/Max are set from target and depth in every section, never by the song. Where it runs the parameter is its alone (no scene of that section sets it, no ramp moves it there); a scene may bypass it (the parameter then holds its declared value, which must exist), change its preset, params (its rate: Time Mode free|sync, Time Numerator 1+15x, Time Denominator in steps of 1/6) and depth. One modulator per parameter; use several for several parameters.
  group: the stem the track is recorded into by record_song_stems, which records a song too heavy for one pass a group of tracks at a time with AUM's own recorder; tracks that share a group are recorded together, a track without one alone.
  More than 12 tracks: one session hosts 12, so a song of more needs groups that make at most 12 stems of at most 12 tracks each. It has no session of its own (load_song, play_song, check_song and mix_song refuse it): record_song_stems records its stems, mix_song_stems writes and plays the mix, and the mix is a song like any other. Its channels are counted in each stem: a track plays on its place in its stem (the first track of every stem on channel 1), and channel and control_channels are taken within the stem alone.
master: [effects on the master strip].
patterns: {id: {step: "1/16" (default; 1/8, 1/4, 1/8t ...), bars?, gate? (0.9), velocity? (96),
  notes: grid line or list of lines (voices that sound together) | drums: {lane: grid} | events: [{at, dur (beats), note, vel?, channel?}], pedal?: [{at, dur (beats)}], channel?}}
  pedal: the sustain pedal of the track's instrument, down from at for dur beats (Control Change 64 on the track's channel). It comes up with the section at the latest, a stretch that begins under another makes the pedal change there, and stopping the song lifts it.
  channel (on a pattern or an event): send those notes on one of the playing track's control_channels, as written (no transpose, no drum lanes). A drum pattern with events on channel 10 plays pads and mutes together.
  Melodic grid: steps separated by spaces - "A2" note (C4 = middle C), "[A3 C4 E4]" chord, "-" hold the previous step, "." rest; add "!" to accent or ":NN" for a velocity.
  Drum grid: one character per step - "x" hit, "X" accent, "o" soft, "." rest. Lanes: kick snare clap rim hat open_hat tom_low tom_mid tom_high crash ride cowbell tamb shaker (or the track's drum_map, or a note).
  "|" marks a bar line in both and is checked: every bar must hold exactly a bar's steps.
scenes: {id: {track: {level?, mute?, preset?, params?, effects: {effect id: {bypass?, preset?, params?}}, modulators: {modulator id: {bypass?, preset?, params?, depth?}}}}} - "master" addresses the master effects.
sections: {id: {bars, scene?: id or [ids], play: {track: pattern or [patterns]}, automation?: [{track, effect? or modulator?, param ("level" for the fader), from, to, bar?, bars?}]}} - patterns repeat until the section ends; a track left out is silent.
arrangement: [section ids in playing order].
session: {track_taps?, full_control?, hardware? (builtin|x32), presets? (song|all), lead_bars?} - how the song's AUM session is built (see author_song_session); optional. lead_bars: the bars of lead-in the session's sequencer plays before bar 1, at the least (for a song that plays recordings). presets: all maps every factory preset, so audition_sound can play any of them in the song's session and the song can change presets without a new one.
A section sounds like the tracks' own declarations overlaid with its scenes, so anything a scene changes must also be declared on the track (its value everywhere else).
calibrate: true compiles the song against the rig's sound catalogue (audition_sound fills it, list_sounds reads it): notes are written at the pitch they should SOUND at (each track is shifted by its preset's measured transposition, per section, so no transpose is needed to undo a preset's octave), a track's level is loudness (0 = the same reference level for every preset; the fader is trimmed per preset) and a drum track's lanes are its kit's measured pads (a lane the kit lacks is an error). A preset that is not measured plays as written and is listed.

Writing a song (the review at the end of save_song's answer says where a song falls short of this; none of it is an error):
- A stored song shows the format. It is no model: the next one gets a form, sounds and an ending of its own.
- Length: 3 to 4½ minutes unless something else is asked for. In 4/4 that is minutes x tempo / 4 bars: 105 bars for 3:30 at 120 BPM.
- Form: decide where the song starts, where it peaks, and what the last chorus has that the first had not. A section that returns is a section of its own with something changed (a second verse with a track more, a last chorus with a lift): the same patterns under another scene or play list are a few lines. Give parts phrases of 8 or 16 bars beside the 1- to 4-bar loops, and let a repeat end differently with a pattern list ([a, a, a, b]).
- Tracks: up to 12 in a song that plays from one session (more only in stems, see group). Beside drums, bass, chords and a lead there is room for a second harmony part, a counter-melody, percussion, a texture, an effects track. A track that rests is heard when it comes in.
- Sounds: choose from everything installed (list_song_plugins lists the instruments and the effects), not from what the stored songs use; audition_sound measures a preset in seconds. What it reports describes the preset: a layered fifth, a slow attack or a pitch that drifts is a character to write for, not a fault.
- Movement: automation ramps any parameter over a section (a filter that opens into a chorus, a delay that grows into a break), not only a level; a modulator moves one by itself, in time with the song.
- Ending: end the song. A last chord or hit that rings out (a recording waits for the tail), a stop together on a beat, a last line left to one instrument. A fade-out is the exception.`

func (s *Server) registerSongTools() {
	songArg := `"song": {"type": "string", "description": "Song id (see list_songs)."}`

	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_songs",
		Description: "List the stored songs: id, title, tempo, length, tracks and the AUM session each was last authored into.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListSongs)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_song",
		Description: "Get a stored song: its YAML document and what it compiles to — the arrangement with each section's bars and start time, the tracks with their MIDI channel, plugins and note count, and a review of it as music (see save_song). A song of more than 12 tracks is listed stem by stem, with which of its stems have a take of the song as it is now. " +
			"Edit the document and hand it back to save_song to change the song. A stored song shows how one is written; it is no model for the next one's form, sounds or ending.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + songArg + `},"required":["song"]}`),
	}, s.handleGetSong)

	s.mcp.AddTool(&mcp.Tool{
		Name: "save_song",
		Description: "Create or replace a song. The document is checked before it is stored: every reference, every pattern (a miscounted bar is an error), and — against the staged AUv3 probes — every plugin, preset and parameter it names; problems come back with their place in the document and what would have matched. " +
			"The answer ends with a review of the song as music: its length, a fade-out at its end, sections heard unchanged several times, few tracks, and the sounds it shares with the other stored songs. That is advice and keeps nothing from being stored. " +
			"Then load_song puts its session into AUM and play_song plays it. A song of more than 12 tracks has no session of its own and is checked stem by stem: record_song_stems and mix_song_stems play it.\n\n" + songFormat,
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
		Description: "Get what a song can set on one plugin (a probe id, or a name that fits only one): its factory presets by name — with what the sound catalogue measured of each (transposition, level, envelope; see audition_sound) — and its writable parameters with range, value labels and the value of the plugin's default patch (a preset may set another, which cannot be read from outside the plugin). " +
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
			"When the sequencer Atom | Piano Roll² is installed, the session also hosts it next to the brain with the whole song in it — every note and control change — so play_song only has to start AUM's transport. " +
			"Load the staged session in AUM, then play_song (load_song does both steps and the iPad's pull in one call). Author again after changing what the song hosts or controls, or its tempo. Changed notes, patterns or arrangement play live at once; the sequencer plays them once the session is authored and loaded again.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"out_id": {"type": "string", "description": "Staging id of the session, which may name a subfolder (default \"Songs/<title>\")."},
				"track_taps": {"type": "boolean", "description": "Also put a ProbeAudioTap after every track's fader, so each track can be listened to on its own (get_audio_tap name=\"<session id>/<track id>\") and mix_song measures every track in one pass. The master always has one, \"<session id>/master\"."},
				"full_control": {"type": "boolean", "description": "Also map every other parameter of every plugin, around the song's own mappings, so the whole session is controllable (control_* tools, the brain's surface)."},
				"sequencer": {"type": "boolean", "description": "Host the sequencer with the song in it (default: yes when Atom | Piano Roll² is among the staged AUv3 probes). false leaves it out: the song then plays live only."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Hardware-I/O profile for the master's output (default builtin)."}
			},
			"required": ["song"]
		}`),
	}, s.handleAuthorSongSession)

	s.registerSongLoadTools(songArg)
	s.registerSoundTools()
	s.registerSongMixTools(songArg)
	s.registerSongStemTools(songArg)

	s.mcp.AddTool(&mcp.Tool{
		Name: "play_song",
		Description: "Play a song. Returns at once; the song plays on (get_song_playback, stop_song) — listen with get_audio_tap meanwhile. The song's session must be the one loaded in AUM (load_song; check_song tells whether every track answers). " +
			"Two modes. sequenced: the sequencer in the song's session plays it, locked to AUM's transport; the daemon only starts the transport from the top and stops it at the end. It is the whole song, as it was when the session was authored. " +
			"live: the daemon sets the session to the opening section's sound, then sends every note and control change through the ProbeMidiBrain when it is due; it plays the song as it is stored now, from any section, for a number of sections, or only some tracks. " +
			"Without mode, the whole song plays sequenced when its session holds the song as it is now, and live otherwise. A song with a track that plays a file plays sequenced, whole, from the top: the file starts at the top of AUM's transport and nowhere else.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"mode": {"type": "string", "enum": ["sequenced", "live"], "description": "How to play (default: sequenced when the session's sequencer holds the song as it is now and the whole song is asked for, else live)."},
				"from": {"type": "string", "description": "Live: where to start, a section id (its first occurrence) or a 1-based position in the arrangement. Default: the top."},
				"sections": {"type": "integer", "minimum": 1, "description": "Live: how many arrangement entries to play (default: to the end)."},
				"tracks": {"type": "array", "items": {"type": "string"}, "description": "Live: play only these tracks' notes (default: all)."},
				"transport": {"type": "boolean", "description": "Live: also start AUM's transport with the first note and stop it at the end, for plugins that follow the host clock. Sequenced playing always runs the transport."},
				"preroll_ms": {"type": "integer", "minimum": 0, "description": "Live: wait between setting the opening sound and the first note (default 1200 when presets load, else 150)."},
				"record": {"type": "boolean", "description": "Record the song at the audio tap while it plays, into a file (list_recordings). The recording ends by itself once the song is over and its tail has rung out, with the silence before the first note cut."},
				"upload": {"type": "boolean", "description": "true: record the song and upload the recording to Google Drive when it is finished (see upload_recording). false: a recording asked for with record stays on the host, also when the config's auto_upload is on. Left out: a recording asked for with record is uploaded when auto_upload is on."}
			},
			"required": ["song"]
		}`),
	}, s.handlePlaySong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "stop_song",
		Description: "Stop the playing song: release every sounding note (live), or stop and rewind AUM's transport (sequenced). Also ends a running mix measurement (mix_song) and a running stem recording (record_song_stems).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleStopSong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_song_playback",
		Description: "Where the playing song stands (mode, section, bar, position), or how the last playback ended. A sequenced song's position is the daemon's clock since it started the transport, not read from AUM.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleGetSongPlayback)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "export_song_midi",
		Description: "Export a song as a Standard MIDI File: the tempo and a marker per section, one track per song track, and the control messages of its session on a track of their own. For a sequencer or a DAW. A song of more than 12 tracks has a session for every stem: it is exported as its notes alone.",
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

// rigProbe finds the staged probe of one of the rig's own plugins, nil when
// it was never scanned. A probe is staged under its device.ProbeID, so the
// lookup goes through that id rather than spelling one out.
func rigProbe(cat song.Catalog, node aum.NodeSpec) *device.ProbeDump {
	if d, ok := cat[device.ProbeID(device.ProbeDump{Component: node.Component})]; ok {
		return &d
	}
	return nil
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
	cat := songCatalog()
	id, err := cat.Resolve(args.Plugin)
	if err != nil {
		return textResult(fmt.Sprintf("/plugin: %v (see list_song_plugins)", err), true), nil
	}
	args.Plugin = id
	dump := cat[id]
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
	// What the sound catalogue knows of the presets listed.
	measured := map[string]*sound.Sound{}
	if known, err := s.sounds.Plugin(args.Plugin); err == nil && known != nil {
		for _, p := range presets {
			for name, m := range known.Presets {
				if strings.EqualFold(name, p) {
					measured[p] = m
				}
			}
		}
		if m, ok := known.Presets[sound.DefaultPatch]; ok && query == "" {
			measured[sound.DefaultPatch] = m
		}
	}
	fmt.Fprintf(&b, "presets (%d of %d matching, %d measured):\n", len(presets), presetHits, len(measured))
	if m, ok := measured[sound.DefaultPatch]; ok {
		fmt.Fprintf(&b, "  %s  [%s]\n", sound.DefaultPatch, soundBrief(m))
	}
	for _, p := range presets {
		if m, ok := measured[p]; ok {
			fmt.Fprintf(&b, "  %s  [%s]\n", p, soundBrief(m))
		} else {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	fmt.Fprintf(&b, "params (%d of %d matching):\n", len(params), paramHits)
	for _, p := range params {
		fmt.Fprintf(&b, "  %s", p.Name)
		if p.ID != "" && p.ID != p.Name {
			fmt.Fprintf(&b, " (%s)", p.ID)
		}
		fmt.Fprintf(&b, ": %g..%g, default patch %g", p.Min, p.Max, p.Value)
		if len(p.Labels) > 0 && len(p.Labels) <= 16 {
			fmt.Fprintf(&b, " [%s]", strings.Join(p.Labels, ", "))
		}
		b.WriteByte('\n')
	}
	return structResult(b.String(), map[string]any{
		"plugin": args.Plugin, "name": dump.Name, "kind": pluginKind(dump.Component.Type),
		"presets": presets, "presets_matching": presetHits, "measured": measured,
		"params": params, "params_matching": paramHits,
	}), nil
}

// soundBrief is a measured preset in a few words, for a list.
func soundBrief(m *sound.Sound) string {
	if len(m.Pads) > 0 {
		return fmt.Sprintf("kit: %d pad(s), peak %.0f dBFS", len(m.Pads), m.PeakDB)
	}
	var parts []string
	switch {
	case m.Silent():
		return "silent"
	case m.Unpitched:
		parts = append(parts, "no pitch")
	default:
		parts = append(parts, fmt.Sprintf("transpose %+d", m.Transpose))
	}
	parts = append(parts, fmt.Sprintf("%.0f dBFS", m.LevelDB))
	if m.Sustains {
		parts = append(parts, "sustains")
	} else {
		parts = append(parts, fmt.Sprintf("decays %.0f ms", m.DecayMS))
	}
	if n := len(m.Remarks); n > 0 {
		parts = append(parts, m.Remarks[n-1])
	}
	return strings.Join(parts, ", ")
}

// --- songs ------------------------------------------------------------------

// compiledSong is a song with everything derived from it.
type compiledSong struct {
	id       string
	song     *song.Song
	plan     *song.Plan
	timeline *song.Timeline
	// catalog is the staged plugins the song was planned against.
	catalog song.Catalog
}

// compileSong plans a song against the staged probes, and against the sound
// catalogue when it asks to be calibrated, and compiles it.
func (s *Server) compileSong(id string, sg *song.Song) (*compiledSong, error) {
	cat := songCatalog()
	plan, err := song.NewPlanWith(sg, cat, song.PlanOptions{Sounds: s.voicing})
	if err != nil {
		return nil, err
	}
	tl, err := song.Compile(sg, plan)
	if err != nil {
		return nil, err
	}
	return &compiledSong{id: id, song: sg, plan: plan, timeline: tl, catalog: cat}, nil
}

// labBars is the length under which a stored song is a sound lab or a test
// and not a song another one is compared with.
const labBars = 8

// songLibrary is what a song's review compares it with: the plugins and
// presets of the other stored songs, and how many plugins are installed.
func (s *Server) songLibrary(except string, cat song.Catalog) song.Library {
	lib := song.Library{Songs: map[string][]song.Voice{}}
	for _, dump := range cat {
		switch pluginKind(dump.Component.Type) {
		case "instrument":
			lib.Instruments++
		case "effect":
			lib.Effects++
		}
	}
	ids, err := s.songs.List()
	if err != nil {
		return lib
	}
	for _, id := range ids {
		if id == except {
			continue
		}
		sg, _, err := s.songs.Load(id)
		if err != nil {
			continue
		}
		bars := 0
		for _, name := range sg.Arrangement {
			bars += sg.Sections[name].Bars
		}
		if bars < labBars {
			continue
		}
		lib.Songs[id] = sg.Palette(cat)
	}
	return lib
}

// voicing is the sound catalogue as a song's plan reads it: what a preset of
// a plugin was measured to sound like.
func (s *Server) voicing(plugin, preset string) (song.Voicing, bool) {
	m := s.sounds.Sound(plugin, preset)
	if m == nil || m.Silent() {
		return song.Voicing{}, false
	}
	pinned := false
	for _, field := range m.Pinned {
		pinned = pinned || field == "transpose"
	}
	return song.Voicing{Transpose: m.Transpose, Pinned: pinned, LevelDB: m.LevelDB, Lanes: m.Lanes}, true
}

// storedSong loads a stored song, returning a tool error on failure.
func (s *Server) storedSong(id string) (*song.Song, *mcp.CallToolResult) {
	if strings.TrimSpace(id) == "" {
		return nil, textResult("/song: name the song (see list_songs)", true)
	}
	sg, _, err := s.songs.Load(id)
	if err != nil {
		return nil, textResult("/song: "+err.Error(), true)
	}
	return sg, nil
}

// loadSong loads and compiles a stored song, returning a tool error on
// failure. It is how a tool gets a song with its session's plan, so a song of
// more tracks than one session hosts is refused here, with what there is to
// do with it instead.
func (s *Server) loadSong(id string) (*compiledSong, *mcp.CallToolResult) {
	sg, fail := s.storedSong(id)
	if fail != nil {
		return nil, fail
	}
	if sg.StemsOnly() {
		return nil, textResult(stemsOnlyText(song.ID(id), sg), true)
	}
	cs, err := s.compileSong(song.ID(id), sg)
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
	// SequenceCurrent reports, for a session that hosts the sequencer, whether
	// it holds the song as it is now (absent without a sequencer).
	SequenceCurrent *bool `json:"sequence_current,omitempty"`
	// Calibration is how each preset a calibrated song uses is corrected,
	// and Warnings what that could not do.
	Calibration []song.TrackVoicing `json:"calibration,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`
	// Review is what stands out in the song as a piece of music (its length,
	// its ending, its repeats, its sounds): advice, not errors.
	Review []song.Remark `json:"review,omitempty"`
	// Stems are the groups the song is recorded in by record_song_stems, when
	// it names any; Files counts the tracks that play a recording.
	Stems []song.Stem `json:"stems,omitempty"`
	Files int         `json:"file_tracks,omitempty"`
	// StemsOnly says the song has more tracks than one session hosts: it has
	// no session of its own and plays as its stems and their mix. A track's
	// channel is then its channel in its stem's session, and Takes says where
	// the stems' recordings stand.
	StemsOnly bool        `json:"stems_only,omitempty"`
	Takes     *stemsState `json:"takes,omitempty"`
}

type songSectionRow struct {
	Position int     `json:"position"`
	Name     string  `json:"name"`
	Bar      int     `json:"bar"`
	Bars     int     `json:"bars"`
	AtSecond float64 `json:"at_second"`
}

type songTrackRow struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Channel         int      `json:"channel"`
	ControlChannels []int    `json:"control_channels,omitempty"`
	Instrument      string   `json:"instrument"`
	Effects         []string `json:"effects"`
	Notes           int      `json:"notes"`
	ControlNotes    int      `json:"control_notes,omitempty"`
	// Pedal counts how often the track's sustain pedal goes down.
	Pedal int `json:"pedal,omitempty"`
	// Sequence says the instrument's own step sequence is written into its
	// saved state; NoBase that the rig has no state to write it over yet.
	Sequence bool `json:"sequence,omitempty"`
	NoBase   bool `json:"no_base_state,omitempty"`
	// File is the recording a track plays instead of an instrument, by its
	// path in AUM's Recordings.
	File string `json:"file,omitempty"`
	// Group is the stem the track is recorded into (record_song_stems).
	Group string `json:"group,omitempty"`
	// Modulators are the track's modulators and what each moves.
	Modulators []songModulatorRow `json:"modulators,omitempty"`
}

// songModulatorRow is one modulator of a track in a song's summary: the
// parameter it moves, the controller and channel it sends, and the lowest and
// highest value it sends where no scene gives it another depth.
type songModulatorRow struct {
	ID      string `json:"id"`
	Plugin  string `json:"plugin"`
	Target  string `json:"target"`
	Channel int    `json:"channel"`
	CC      int    `json:"cc"`
	Depth   [2]int `json:"depth"`
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
	sum.Sections = arrangementRows(&b, tl)
	b.WriteString("tracks:\n")
	for i := range cs.plan.Tracks {
		row := trackRow(&b, "", cs, i, cs.song.Tracks[i].Group)
		if row.File != "" {
			sum.Files++
		}
		sum.Tracks = append(sum.Tracks, row)
	}
	if len(cs.plan.Master) > 0 {
		names := make([]string, len(cs.plan.Master))
		for i, n := range cs.plan.Master {
			names[i] = n.Name
		}
		fmt.Fprintf(&b, "  master: %s\n", strings.Join(names, " > "))
	}
	if sum.Files > 0 {
		fmt.Fprintf(&b, "files: %d track(s) play a recording, which starts at the top of AUM's transport: the song plays from its session's sequencer and from the top only, with a lead-in of %d bar(s) before bar 1 (session lead_bars; a recording begins with the lead-in of the session it was made in)\n",
			sum.Files, tl.Sequence().LeadBars)
	}
	if stems, err := cs.song.Stems(nil); err == nil && len(stems) < len(cs.song.Tracks) {
		sum.Stems = stems
		b.WriteString("stems (record_song_stems records each alone with AUM's recorder, mix_song_stems mixes the files):\n")
		for _, st := range stems {
			fmt.Fprintf(&b, "  %-10s %s\n", st.Name, strings.Join(st.Tracks, ", "))
		}
	}
	if rows := cs.plan.Calibration(); rows != nil {
		sum.Calibration, sum.Warnings = rows, cs.plan.Warnings()
		calibrationText(&b, rows, sum.Warnings)
	}
	if rec, ok := loadSongSessions()[cs.id]; ok {
		current := rec.Fingerprint == tl.Fingerprint
		sum.Session, sum.SessionCurrent = rec.Session, &current
		if current {
			fmt.Fprintf(&b, "session: %s\n", rec.Session)
		} else {
			fmt.Fprintf(&b, "session: %s is out of date (the song's tempo, plugins, controls or session options changed): run load_song\n", rec.Session)
		}
		if rec.Sequence != "" {
			seqCurrent := rec.sequenceCurrent(tl)
			sum.SequenceCurrent = &seqCurrent
			switch {
			case seqCurrent:
				b.WriteString("playing: from the session's sequencer (play_song starts AUM's transport)\n")
			case current:
				b.WriteString("playing: live; the session's sequencer holds an older version of the song (load_song authors and loads it again to play from it)\n")
			}
		}
	} else {
		b.WriteString("session: not authored yet (load_song)\n")
	}
	if sum.Review = song.Review(cs.song, tl, cs.catalog, s.songLibrary(cs.id, cs.catalog)); len(sum.Review) > 0 {
		b.WriteString("review (the song as music; advice, none of it an error):\n")
		for _, r := range sum.Review {
			fmt.Fprintf(&b, "  %s: %s\n", r.About, r.Text)
		}
	}
	return b.String(), sum
}

// arrangementRows renders a timeline's sections as the summary's arrangement.
func arrangementRows(b *strings.Builder, tl *song.Timeline) []songSectionRow {
	rows := []songSectionRow{}
	b.WriteString("arrangement:\n")
	for i, sec := range tl.Sections {
		at := tl.TickTime(sec.Tick)
		fmt.Fprintf(b, "  %2d  %-12s bars %d-%d  %s\n", i+1, sec.Name, sec.Bar, sec.Bar+sec.Bars-1, clock(at))
		rows = append(rows, songSectionRow{Position: i + 1, Name: sec.Name, Bar: sec.Bar, Bars: sec.Bars, AtSecond: at.Seconds()})
	}
	return rows
}

// trackRow renders track i of a compiled song as a line of a summary, after
// indent, and returns it as the summary's row. group is the stem the document
// puts the track into.
func trackRow(b *strings.Builder, indent string, cs *compiledSong, i int, group string) songTrackRow {
	tp, tl := cs.plan.Tracks[i], cs.timeline
	row := songTrackRow{
		ID: tp.ID, Name: tp.Name, Channel: tp.Channel, ControlChannels: tp.ControlChannels,
		Effects: []string{}, Notes: tl.Tracks[i].Notes, ControlNotes: tl.Tracks[i].ControlNotes,
		Pedal: tl.Tracks[i].Pedal,
	}
	for j, n := range tp.Nodes {
		switch {
		case j == 0:
			row.Instrument = n.Name
		case n.Mod != nil:
			row.Modulators = append(row.Modulators, songModulatorRow{
				ID: n.Effect, Plugin: n.Name, Target: n.Mod.Target,
				Channel: n.Mod.At.Channel, CC: n.Mod.At.Number, Depth: n.Mod.Depth,
			})
		default:
			row.Effects = append(row.Effects, n.Name)
		}
	}
	row.File, row.Group = tp.File(), group
	fmt.Fprintf(b, "%s  ch%-2d %-10s %s", indent, tp.Channel, tp.ID, row.Instrument)
	if len(row.Effects) > 0 {
		fmt.Fprintf(b, " > %s", strings.Join(row.Effects, " > "))
	}
	if row.File != "" {
		// A file has no notes: it plays from its start with the transport.
		fmt.Fprintf(b, "  (plays %q from AUM's Recordings, from its start with AUM's transport; no notes)\n", row.File)
		modulatorRows(b, indent, row.Modulators)
		return row
	}
	fmt.Fprintf(b, "  (%d notes", row.Notes)
	if len(row.ControlChannels) > 0 {
		fmt.Fprintf(b, "; %d control notes on ch%s", row.ControlNotes, joinInts(row.ControlChannels, ", ch"))
	}
	if row.Pedal > 0 {
		fmt.Fprintf(b, "; the pedal goes down %d time(s)", row.Pedal)
	}
	b.WriteString(")\n")
	modulatorRows(b, indent, row.Modulators)
	if n := tp.Nodes[0]; n.HasStepSequence() {
		row.Sequence, row.NoBase = true, !hasStepSequenceBase(n)
		fmt.Fprintf(b, "%s       its own sequence, written into %s's saved state in the session\n", indent, n.Name)
		if row.NoBase {
			fmt.Fprintf(b, "%s       warning: the rig has no base state of %s to write it over, so its session cannot be authored: load a session with a %s of the default patch in AUM, save it there, and capture_auv3_default_state {session_id, channel, slot, base: true}\n", indent, n.Name, n.Name)
		}
	}
	return row
}

// modulatorRows renders a track's modulators as lines of a summary.
func modulatorRows(b *strings.Builder, indent string, rows []songModulatorRow) {
	for _, m := range rows {
		fmt.Fprintf(b, "%s       ~ %s (%s) moves %s, sending CC %d on ch%d between %d and %d\n", indent, m.ID, m.Plugin, m.Target, m.CC, m.Channel, m.Depth[0], m.Depth[1])
	}
}

// calibrationText renders how a calibrated song's presets are corrected.
func calibrationText(b *strings.Builder, rows []song.TrackVoicing, warnings []string) {
	fmt.Fprintf(b, "calibrated against the sound catalogue (level 0 = %g dBFS rms for the preset's reference note):\n", song.ReferenceLevelDB)
	for _, r := range rows {
		preset := r.Preset
		if preset == "" {
			preset = sound.DefaultPatch
		}
		switch {
		case !r.Measured:
			fmt.Fprintf(b, "  %-10s %s: not measured, plays as written\n", r.Track, preset)
		case r.Pads > 0:
			fmt.Fprintf(b, "  %-10s %s: a kit of %d pad(s), level %+.1f dB\n", r.Track, preset, r.Pads, r.TrimDB)
		default:
			fmt.Fprintf(b, "  %-10s %s: notes %+d semitone(s), level %+.1f dB\n", r.Track, preset, r.Transpose, r.TrimDB)
		}
	}
	for _, w := range warnings {
		fmt.Fprintf(b, "  warning: %s\n", w)
	}
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
		// Stems is how many stems a song of more tracks than one session
		// hosts plays as; such a song has no session of its own.
		Stems   int    `json:"stems,omitempty"`
		Session string `json:"session,omitempty"`
		Error   string `json:"error,omitempty"`
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
		fmt.Fprintf(&b, "  %-24s %q  %g BPM, %d bars, %s, %d track(s)", id, sg.Title, sg.Tempo, tl.Bars, clock(tl.Duration()), len(sg.Tracks))
		if stems, err := sg.Stems(nil); err == nil && sg.StemsOnly() {
			r.Stems = len(stems)
			fmt.Fprintf(&b, " in %d stems (record_song_stems, mix_song_stems)", r.Stems)
		}
		rows = append(rows, r)
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
	text, sum, err := s.describeSong(id, sg)
	if err != nil {
		return structResult(fmt.Sprintf("song %q does not compile against the staged plugins: %v\n\n%s", id, err, raw),
			map[string]any{"id": id, "yaml": string(raw), "error": err.Error()}), nil
	}
	return structResult(text+"\n"+string(raw), map[string]any{"id": id, "summary": sum, "yaml": string(raw)}), nil
}

// describeSong checks a song against the staged plugins and summarizes what
// it compiles to: as one session, or, for a song of more tracks than one
// session hosts, stem by stem.
func (s *Server) describeSong(id string, sg *song.Song) (string, songSummary, error) {
	if sg.StemsOnly() {
		plan, err := s.compileStems(id, sg)
		if err != nil {
			return "", songSummary{}, err
		}
		text, sum := s.summarizeStems(plan)
		return text, sum, nil
	}
	cs, err := s.compileSong(id, sg)
	if err != nil {
		return "", songSummary{}, err
	}
	text, sum := s.summarize(cs)
	return text, sum, nil
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
	text, sum, err := s.describeSong(id, sg)
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
		Mode      string   `json:"mode"`
		From      string   `json:"from"`
		Sections  int      `json:"sections"`
		Tracks    []string `json:"tracks"`
		Transport bool     `json:"transport"`
		PrerollMS int      `json:"preroll_ms"`
		Record    bool     `json:"record"`
		Upload    *bool    `json:"upload"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	if busy := s.stemsBusy("play_song"); busy != nil {
		return busy, nil
	}
	if s.midi == nil || !s.midi.Connected() {
		return textResult("play_song: "+midicontrol.ErrNoBrain.Error()+" — load_song puts the song's session into AUM so its brain dials in", true), nil
	}
	from, err := songStart(cs.timeline, args.From)
	if err != nil {
		return textResult("/from: "+err.Error(), true), nil
	}

	// The song must be playing into its own session. The daemon knows which
	// session AUM has open only when it loaded it itself (load_song) and the
	// brain has not reconnected since; what the iPad last downloaded says
	// nothing about what is open, so it is not consulted.
	warnings := []string{}
	rec, authored := loadSongSessions()[cs.id]
	switch {
	case !authored:
		warnings = append(warnings, "this song has no authored session yet (load_song authors and loads it); its control changes will not reach anything")
	case rec.Fingerprint != cs.timeline.Fingerprint:
		warnings = append(warnings, fmt.Sprintf("the song's tempo, plugins or controls changed since %q was authored: run load_song, or some sounds will be wrong", rec.Session))
	default:
		if cur := s.loadedSessionID(); cur != "" && cur != rec.Session {
			warnings = append(warnings, fmt.Sprintf("AUM has %q open, not this song's session %q (load_song loads it)", cur, rec.Session))
		}
	}

	partial := from != 0 || args.Sections > 0 || len(args.Tracks) > 0
	if cs.song.HasFiles() {
		// A File Player starts its file at the transport's top and cannot be
		// placed: only the sequencer, which runs with that transport, plays
		// the song's control changes where the files are.
		if strings.EqualFold(strings.TrimSpace(args.Mode), song.ModeLive) || partial {
			return textResult("play_song: a track of this song plays a file, which starts at the top of AUM's transport and cannot be started anywhere else: the song plays from its session's sequencer, whole, from the top (leave out mode, from, sections and tracks)", true), nil
		}
		args.Mode = song.ModeSequenced
	}
	mode, note, err := songPlayMode(args.Mode, rec, cs.timeline, partial)
	if err != nil {
		return textResult("/mode: "+err.Error(), true), nil
	}
	// A recording starts before the first message goes out and follows the
	// song to its end (recording_tools.go). Played live, the first note the
	// daemon sends marks where the music starts in it; sequenced, no note
	// passes through here and the recording goes by the playback's clock.
	// upload: true asks for a recording too. upload: false asks for none; it
	// keeps the recording that record asks for on the host, whatever the
	// config's auto_upload says.
	var take *activeRecording
	if args.Record || (args.Upload != nil && *args.Upload) {
		take, err = s.beginRecording(recordOptions{Song: cs.id, Title: cs.song.Title, Upload: args.Upload, Tap: s.songRecordingTap(rec)})
		if err != nil {
			return textResult("play_song: cannot record: "+err.Error(), true), nil
		}
	}
	send := take.sender(func(ctx context.Context, e song.Event) error {
		return s.sendBrain(ctx, songCommand(e))
	})
	cc := func(at song.Assignment, value int) song.Event {
		return song.Event{Kind: song.Control, Channel: at.Channel, Data1: at.Number, Data2: value}
	}
	// Stop/Rewind only stops a running transport, so a start rewinds first to
	// begin at the top. The player gives the rewind time before the start:
	// AUM does not rewind when the start follows at once.
	rewind, start, stop := cs.plan.Transport["Rewind"], cs.plan.Transport["Start Play"], cs.plan.Transport["Stop/Rewind"]
	bypass := cs.plan.SequencerBypass

	// The player keeps the time with the host's timers, and a host that dozes
	// fires them late: a live song stutters, a sequenced one is stopped late
	// and reports a position that stood still. Hold the host awake from
	// before the first message goes out until the playback is over, however
	// it ends.
	release := s.holdAwake()
	onEnd := func(song.Status) { release() }

	var st song.Status
	seq := cs.timeline.Sequence()
	if mode == song.ModeSequenced {
		// The sequencer runs with the transport; everything else is in it. The
		// transport is rewound again at the end, so the session's own play
		// button starts the song from the top too.
		st, err = s.player.StartSequenced(cs.id, cs.timeline, seq, song.SequencedOptions{
			Rewind: []song.Event{cc(bypass, 0), cc(rewind, 127)},
			Start:  []song.Event{cc(start, 127)},
			Stop:   []song.Event{cc(stop, 127), cc(rewind, 127)},
			OnEnd:  onEnd,
		}, send)
	} else {
		opts := song.PlayOptions{
			From: from, Count: args.Sections, Tracks: args.Tracks,
			Preroll: time.Duration(args.PrerollMS) * time.Millisecond,
			OnEnd:   onEnd,
		}
		if args.Transport {
			opts.Rewind, opts.Before, opts.After = liveTransport(cs.plan, rec.Sequence != "")
		}
		st, err = s.player.Start(cs.id, cs.timeline, opts, send)
	}
	if err != nil {
		release() // no playback began, so nothing will end
		s.abortRecording(take)
		return textResult("play_song: "+err.Error(), true), nil
	}
	if take != nil {
		go s.runRecording(take)
	}

	var b strings.Builder
	if mode == song.ModeSequenced {
		fmt.Fprintf(&b, "started AUM's transport: the session's sequencer plays %q from the top, %s after a lead-in of %.1f s", cs.song.Title,
			clock(time.Duration(st.DurationMS)*time.Millisecond), seq.Lead().Seconds())
	} else {
		fmt.Fprintf(&b, "playing %q live from %s (bar %d), %s", cs.song.Title, cs.timeline.Sections[from].Name, cs.timeline.Sections[from].Bar, clock(time.Duration(st.DurationMS)*time.Millisecond))
		if len(args.Tracks) > 0 {
			fmt.Fprintf(&b, ", tracks %s", strings.Join(args.Tracks, ", "))
		}
	}
	b.WriteString("\nlisten with get_audio_tap, follow with get_song_playback, end with stop_song\n")
	if note != "" {
		fmt.Fprintf(&b, "note: %s\n", note)
	}
	out := map[string]any{"playback": st, "mode": mode, "warnings": warnings}
	if take != nil {
		rs := take.status()
		out["recording"] = rs
		fmt.Fprintf(&b, "recording it as %q from the tap %q: the file is written once the song and its tail have ended (list_recordings)", rs.Name, rs.Tap)
		switch {
		case rs.Upload:
			b.WriteString(", and then uploaded to Google Drive")
		case args.Upload != nil:
			b.WriteString(", and not uploaded to Google Drive (upload: false)")
		}
		b.WriteByte('\n')
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return structResult(b.String(), out), nil
}

// liveTransport is what a live playback sends to run AUM's transport along
// with it: rewind (then the player's rewindSettle at the least), start with
// the first note, stop and rewind at the end, so whatever starts the
// transport next begins at the top. In a session that hosts the sequencer
// (sequencer), the sequencer would play the song from the top along with the
// transport: it is bypassed first and put back in line last.
func liveTransport(plan *song.Plan, sequencer bool) (rewind, before, after []song.Event) {
	cc := func(at song.Assignment, value int) song.Event {
		return song.Event{Kind: song.Control, Channel: at.Channel, Data1: at.Number, Data2: value}
	}
	rw, start, stop := plan.Transport["Rewind"], plan.Transport["Start Play"], plan.Transport["Stop/Rewind"]
	rewind = []song.Event{cc(rw, 127)}
	before = []song.Event{cc(start, 127)}
	after = []song.Event{cc(stop, 127), cc(rw, 127)}
	if sequencer {
		rewind = append([]song.Event{cc(plan.SequencerBypass, 127)}, rewind...)
		after = append(after, cc(plan.SequencerBypass, 0))
	}
	return rewind, before, after
}

// songPlayMode decides how a song is played. want is the mode asked for ("" to
// choose), rec the session the song was authored into, and partial whether
// only a part of the song is asked for (a start section, a number of sections
// or some tracks). The sequencer plays the whole song, as it was authored;
// anything else is played live. note says why a song that has a sequencer is
// played live anyway.
func songPlayMode(want string, rec songSessionRecord, tl *song.Timeline, partial bool) (mode, note string, err error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want != "" && want != song.ModeLive && want != song.ModeSequenced {
		return "", "", fmt.Errorf("%q is not a mode (sequenced, live)", want)
	}
	var blocked string // why the sequencer cannot play what is asked for
	switch {
	case rec.Sequence == "":
		blocked = "the song's session hosts no sequencer (load_song authors one in when Atom | Piano Roll² is installed)"
	case partial:
		blocked = "the sequencer plays the whole song from the top; from, sections and tracks are played live"
	case rec.Fingerprint != tl.Fingerprint:
		blocked = "the song's tempo, plugins or controls changed since its session was authored: run load_song"
	case !rec.sequenceCurrent(tl):
		blocked = "the session's sequencer holds an older version of the song: run load_song to play from it"
	}
	switch {
	case want == song.ModeSequenced && blocked != "":
		return "", "", fmt.Errorf("%s", blocked)
	case want == song.ModeLive:
		return song.ModeLive, "", nil
	case blocked == "":
		return song.ModeSequenced, "", nil
	case rec.Sequence == "" || partial:
		// Nothing to explain: there is no sequencer, or a part was asked for.
		return song.ModeLive, "", nil
	case rec.Fingerprint != tl.Fingerprint:
		// play_song's own warning about the outdated session says it.
		return song.ModeLive, "", nil
	default:
		return song.ModeLive, "played live: " + blocked, nil
	}
}

// playbackText renders a playback status.
func playbackText(st song.Status) string {
	switch {
	case st.Song == "":
		return "no song has been played"
	case st.Playing && st.LeadInMS > 0:
		return fmt.Sprintf("playing %q (%s): AUM's transport runs, the first bar comes in %.1f s", st.Song, st.Mode, float64(st.LeadInMS)/1000)
	case st.Playing:
		return fmt.Sprintf("playing %q (%s): %s, bar %d, %s of %s", st.Song, st.Mode, st.Section, st.Bar,
			clock(time.Duration(st.PositionMS)*time.Millisecond), clock(time.Duration(st.DurationMS)*time.Millisecond))
	default:
		return fmt.Sprintf("%q (%s) %s at %s of %s (%s, bar %d)", st.Song, st.Mode, st.Outcome,
			clock(time.Duration(st.PositionMS)*time.Millisecond), clock(time.Duration(st.DurationMS)*time.Millisecond), st.Section, st.Bar)
	}
}

func (s *Server) handleStopSong(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// A mix measurement plays the song in passes: end it, not only the pass
	// that is playing.
	s.mixMu.Lock()
	if s.mix != nil && !s.mix.finished() {
		s.mix.cancel()
	}
	s.mixMu.Unlock()
	// So does a stem recording: end it, not only the pass that is playing.
	s.stemsJobMu.Lock()
	if s.stems != nil && !s.stems.finished() {
		s.stems.cancel()
	}
	s.stemsJobMu.Unlock()
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
	sg, fail := s.storedSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	id := song.ID(args.Song)
	var tl *song.Timeline
	note := ""
	if sg.StemsOnly() {
		// The notes need no session. The control messages are a session's,
		// and this song has one for every stem: they are left out.
		var err error
		if tl, err = song.Compile(sg, nil); err != nil {
			return textResult(fmt.Sprintf("song %q does not compile: %v", id, err), true), nil
		}
		note = fmt.Sprintf("\nthe notes alone: the song has more tracks than one session hosts (%d), so each stem has a session and control messages of its own, which are left out. A track's channel is its channel in its stem", song.MaxSessionTracks)
		if sg.Calibrate {
			note += "; the notes are as written, at sounding pitch, not shifted for their presets"
		}
	} else {
		cs, err := s.compileSong(id, sg)
		if err != nil {
			return textResult(fmt.Sprintf("song %q does not compile against the staged plugins: %v", id, err), true), nil
		}
		tl = cs.timeline
	}
	dir := config.SongExportsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return textResult("export song: "+err.Error(), true), nil
	}
	path := filepath.Join(dir, id+".mid")
	data := tl.SMF()
	if err := lanhttp.WriteFileAtomic(path, data, 0o644); err != nil {
		return textResult("export song: "+err.Error(), true), nil
	}
	notes := 0
	for _, t := range tl.Tracks {
		notes += t.Notes + t.ControlNotes
	}
	return structResult(
		fmt.Sprintf("exported %q -> %s (%d bytes: %d track(s), %d notes, %d bars at %g BPM)%s", sg.Title, path, len(data), len(tl.Tracks), notes, tl.Bars, tl.Tempo, note),
		map[string]any{"song": id, "path": path, "bytes": len(data), "tracks": len(tl.Tracks), "notes": notes, "notes_only": sg.StemsOnly()}), nil
}
