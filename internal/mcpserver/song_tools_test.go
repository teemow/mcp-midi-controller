package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport/midicontrol"
)

// stageSongPlugins stages the probes the test song hosts: a synth with presets
// and parameters, a drum machine and a delay.
func stageSongPlugins(t *testing.T) {
	t.Helper()
	dir := config.AUv3ProbesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir probes: %v", err)
	}
	for id, dump := range map[string]string{
		"syn1": `{"component": {"type": "aumu", "subtype": "Syn1", "manufacturer": "Acme", "manufacturerName": "Acme"}, "name": "PolySynth",
			"parameters": [
				{"address": 0, "identifier": "cutoff", "displayName": "Cutoff", "min": 0, "max": 1, "value": 0.5, "writable": true},
				{"address": 1, "identifier": "res", "displayName": "Resonance", "min": 0, "max": 1, "value": 0.1, "writable": true}],
			"factoryPresets": [{"number": 0, "name": "Init"}, {"number": 1, "name": "Deep Bass"}, {"number": 2, "name": "Warm Pad"}, {"number": 3, "name": "Bright Pad"}]}`,
		"drm1": `{"component": {"type": "aumu", "subtype": "Drm1", "manufacturer": "Acme"}, "name": "DrumBox", "parameters": []}`,
		"dly1": `{"component": {"type": "aufx", "subtype": "Dly1", "manufacturer": "Acme"}, "name": "TapeDelay",
			"parameters": [{"address": 0, "identifier": "mix", "displayName": "Mix", "min": 0, "max": 1, "value": 0.3, "writable": true}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(dump), 0o644); err != nil {
			t.Fatalf("write probe %s: %v", id, err)
		}
	}
}

// toolSong is a short song at a tempo that plays its two bars in under a
// second.
const toolSong = `# a comment that must survive being stored
title: "Night Drive"
tempo: 300
tracks:
  - id: bass
    instrument: {plugin: syn1, preset: Deep Bass}
  - id: pad
    instrument: {plugin: syn1, preset: Warm Pad, params: {Cutoff: 0.3}}
    effects:
      - {id: echo, plugin: dly1, bypass: true}
  - id: drums
    kind: drums
    instrument: {plugin: drm1}
patterns:
  bass: {step: 1/8, notes: "A1 . A1 . A1 . G1 ."}
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
scenes:
  open:
    pad: {preset: Bright Pad, effects: {echo: {bypass: false}}}
sections:
  verse: {bars: 1, play: {bass: bass, drums: beat}}
  chorus: {bars: 1, scene: open, play: {bass: bass, pad: chords, drums: beat}}
arrangement: [verse, chorus]
`

func newSongServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), opts...)
	stageSongPlugins(t)
	return s
}

func TestSongToolsSaveGetList(t *testing.T) {
	s := newSongServer(t)

	// A dry run checks and compiles but stores nothing.
	res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong, "dry_run": true})
	if res.IsError || !strings.Contains(resultText(res), `checked "Night Drive" (night_drive): 300 BPM, 2 bars`) {
		t.Fatalf("dry run: %s", resultText(res))
	}
	if res := call(t, s.handleListSongs, map[string]any{}); !strings.Contains(resultText(res), "no songs") {
		t.Fatalf("the dry run stored the song:\n%s", resultText(res))
	}

	res = call(t, s.handleSaveSong, map[string]any{"yaml": toolSong})
	if res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{
		"saved", "1  verse", "2  chorus", "bars 2-2",
		"ch1  bass       PolySynth  (8 notes)",
		"ch2  pad        PolySynth > TapeDelay  (3 notes)",
		"session: not authored yet",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("save summary lacks %q:\n%s", want, text)
		}
	}

	res = call(t, s.handleListSongs, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), `night_drive`) || !strings.Contains(resultText(res), "3 track(s)") {
		t.Fatalf("list: %s", resultText(res))
	}
	// The stored text is the author's, comment included.
	res = call(t, s.handleGetSong, map[string]any{"song": "night_drive"})
	if res.IsError || !strings.Contains(resultText(res), "# a comment that must survive being stored") {
		t.Fatalf("get: %s", resultText(res))
	}

	// A document may also arrive as an object.
	res = call(t, s.handleSaveSong, map[string]any{"song": "From Object", "document": map[string]any{
		"title": "Object", "tempo": 120,
		"tracks":      []any{map[string]any{"id": "a", "instrument": map[string]any{"plugin": "syn1"}}},
		"patterns":    map[string]any{"p": map[string]any{"step": "1/4", "notes": "C3 . . ."}},
		"sections":    map[string]any{"s": map[string]any{"bars": 1, "play": map[string]any{"a": "p"}}},
		"arrangement": []any{"s"},
	}})
	if res.IsError || !strings.Contains(resultText(res), "(from_object)") {
		t.Fatalf("save from an object: %s", resultText(res))
	}

	res = call(t, s.handleDeleteSong, map[string]any{"song": "from_object"})
	if res.IsError {
		t.Fatalf("delete: %s", resultText(res))
	}
	if res := call(t, s.handleGetSong, map[string]any{"song": "from_object"}); !res.IsError {
		t.Fatal("get of a deleted song succeeded")
	}
}

// TestSongToolsRejectBrokenSongs checks a song that does not hold together is
// refused with every problem, in the document's and in the plugins' terms.
func TestSongToolsRejectBrokenSongs(t *testing.T) {
	s := newSongServer(t)
	doc := strings.NewReplacer(
		"preset: Deep Bass", "preset: Deep Base",
		`"A1 . A1 . A1 . G1 ."`, `"A1 . A1 . | A1 . G1"`,
		"plugin: drm1", "plugin: drums",
	).Replace(toolSong)
	res := call(t, s.handleSaveSong, map[string]any{"yaml": doc})
	if !res.IsError || !strings.Contains(resultText(res), "bar 1 has 4 steps, a bar holds 8") {
		t.Fatalf("a miscounted bar was not refused: %s", resultText(res))
	}

	doc = strings.NewReplacer("preset: Deep Bass", "preset: Deep Base", "plugin: drm1", "plugin: drums").Replace(toolSong)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": doc})
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, `no staged AUv3 probe "drums"`) {
		t.Fatalf("an unknown plugin was not refused: %s", text)
	}
	doc = strings.Replace(toolSong, "preset: Deep Bass", "preset: Deep Base", 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": doc})
	if !res.IsError || !strings.Contains(resultText(res), `PolySynth has no factory preset "Deep Base" (did you mean: Deep Bass?)`) {
		t.Fatalf("an unknown preset was not refused with a suggestion: %s", resultText(res))
	}
	if res := call(t, s.handleSaveSong, map[string]any{}); !res.IsError {
		t.Fatal("save without a document succeeded")
	}
	if res := call(t, s.handleListSongs, map[string]any{}); !strings.Contains(resultText(res), "no songs") {
		t.Fatalf("a refused song was stored:\n%s", resultText(res))
	}
}

func TestSongPluginTools(t *testing.T) {
	s := newSongServer(t)
	res := call(t, s.handleListSongPlugins, map[string]any{"kind": "instrument"})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "PolySynth") || !strings.Contains(text, "DrumBox") || strings.Contains(text, "TapeDelay") {
		t.Fatalf("instruments: %s", text)
	}
	res = call(t, s.handleListSongPlugins, map[string]any{"query": "delay"})
	if text := resultText(res); !strings.Contains(text, "1 plugin(s)") || !strings.Contains(text, "dly1") {
		t.Fatalf("query delay: %s", text)
	}

	res = call(t, s.handleGetSongPlugin, map[string]any{"plugin": "syn1", "query": "pad"})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "presets (2 of 2 matching)") || !strings.Contains(text, "Warm Pad") || strings.Contains(text, "Deep Bass") {
		t.Fatalf("presets matching pad: %s", text)
	}
	res = call(t, s.handleGetSongPlugin, map[string]any{"plugin": "syn1", "query": "cutoff"})
	if text := resultText(res); !strings.Contains(text, "Cutoff (cutoff): 0..1, now 0.5") {
		t.Fatalf("params matching cutoff: %s", text)
	}
	if res := call(t, s.handleGetSongPlugin, map[string]any{"plugin": "nope"}); !res.IsError {
		t.Fatal("an unknown plugin was accepted")
	}
}

// TestAuthorSongSession authors the song's session and reads the staged file
// back; then changes what the song controls and checks the session is
// reported as out of date.
func TestAuthorSongSession(t *testing.T) {
	s := newSongServer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"})
	if res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{
		"5 strip(s)", "3 preset load(s)",
		"strip 2: bass on MIDI channel 1: PolySynth",
		"strip 3: pad on MIDI channel 2: PolySynth > TapeDelay",
		"strip 5: Master > ProbeAudioTap",
		"Songs/Night Drive.aumproj",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("author report lacks %q:\n%s", want, text)
		}
	}

	sess, err := aum.OpenFile(filepath.Join(config.AUMSessionsDir(), "Songs", "Night Drive.aumproj"))
	if err != nil {
		t.Fatalf("open the staged session: %v", err)
	}
	if sess.Title() != "Night Drive" || sess.Tempo() != 300 || len(sess.Channels()) != 5 {
		t.Errorf("staged session = %q at %g BPM with %d strips", sess.Title(), sess.Tempo(), len(sess.Channels()))
	}
	if loads := sess.PresetLoads(2, 0); len(loads) != 2 || loads[1].Name != "Bright Pad" {
		t.Errorf("pad preset loads = %+v, want Warm Pad and Bright Pad", loads)
	}

	// The song knows its session, and that it is current.
	res = call(t, s.handleGetSong, map[string]any{"song": "night_drive"})
	if !strings.Contains(resultText(res), "session: Songs/Night Drive\n") {
		t.Fatalf("get after author: %s", resultText(res))
	}
	// New notes need no new session …
	edited := strings.Replace(toolSong, `"A1 . A1 . A1 . G1 ."`, `"A1 A1 A1 . E1 . G1 ."`, 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": edited})
	if res.IsError || !strings.Contains(resultText(res), "session: Songs/Night Drive\n") {
		t.Fatalf("save with new notes: %s", resultText(res))
	}
	// … a new control does.
	edited = strings.Replace(edited, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": edited})
	if res.IsError || !strings.Contains(resultText(res), "is out of date") {
		t.Fatalf("save with a new control: %s", resultText(res))
	}

	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive", "out_id": "../escape"}); !res.IsError {
		t.Fatal("an out_id outside the staging dir was accepted")
	}
}

// TestPlaySong plays the song into a stand-in brain and checks what arrives:
// the opening sound first, then the notes on their tracks' channels.
func TestPlaySong(t *testing.T) {
	hub := midicontrol.NewHub()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newSongServer(t, WithMidiControl(hub))
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}

	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive"})
	if !res.IsError || !strings.Contains(resultText(res), "no ProbeMidiBrain connected") {
		t.Fatalf("play without a brain did not fail: %s", resultText(res))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/midi-control", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.CloseNow() }()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !hub.Connected(); {
		time.Sleep(10 * time.Millisecond)
	}
	frames := make(chan midicontrol.Command, 256)
	go func() {
		defer close(frames)
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var cmd midicontrol.Command
			if json.Unmarshal(data, &cmd) == nil {
				frames <- cmd
			}
		}
	}()

	// Not authored yet: it plays, with a warning.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "from": "nowhere"})
	if !res.IsError || !strings.Contains(resultText(res), "no section") {
		t.Fatalf("a bad start was accepted: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20, "transport": true})
	if res.IsError || !strings.Contains(resultText(res), `playing "Night Drive" from verse (bar 1)`) || strings.Contains(resultText(res), "warning:") {
		t.Fatalf("play: %s", resultText(res))
	}

	var got []midicontrol.Command
	deadline := time.After(8 * time.Second)
collect:
	for {
		st := s.player.Status()
		select {
		case cmd, ok := <-frames:
			if !ok {
				break collect
			}
			got = append(got, cmd)
		case <-time.After(100 * time.Millisecond):
			if !st.Playing {
				break collect
			}
		case <-deadline:
			t.Fatal("playback did not finish")
		}
	}

	// The opening sound: three presets on the global channel, before any note.
	firstNote := -1
	programs := 0
	for i, cmd := range got {
		if cmd.Type == "noteOn" && firstNote < 0 {
			firstNote = i
		}
		if cmd.Type == "pc" && firstNote < 0 {
			programs++
			if cmd.Channel != 15 {
				t.Errorf("preset load on channel %d, want the global channel 15", cmd.Channel)
			}
		}
	}
	if firstNote < 0 || programs != 2 {
		t.Fatalf("got %d preset loads before the first note (at %d), want the two tracks' presets: %+v", programs, firstNote, got)
	}
	// Transport start right before the first note, stop at the very end.
	if b := got[firstNote-1]; b.Type != "cc" || b.Controller != 102 || b.Value != 127 {
		t.Errorf("before the first note: %+v, want the transport start", b)
	}
	if e := got[len(got)-1]; e.Type != "cc" || e.Controller != 103 {
		t.Errorf("last frame: %+v, want the transport stop", e)
	}
	// Notes ride their tracks' channels; the pad (channel 2) plays its chord
	// only in the chorus, after its preset changed.
	notes := map[int]int{}
	padPreset := false
	for _, cmd := range got[firstNote:] {
		switch cmd.Type {
		case "noteOn":
			notes[cmd.Channel]++
			if cmd.Channel == 2 && !padPreset {
				t.Error("the pad played before its chorus preset was loaded")
			}
		case "pc":
			padPreset = true
		}
	}
	if notes[1] != 8 || notes[2] != 3 || notes[3] != 12 {
		t.Errorf("notes per channel = %v, want bass 8, pad 3, drums 12", notes)
	}

	res = call(t, s.handleGetSongPlayback, map[string]any{})
	if !strings.Contains(resultText(res), `"night_drive" finished`) {
		t.Errorf("playback status: %s", resultText(res))
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Errorf("stop with nothing playing: %s", resultText(res))
	}
}

func TestExportSongMIDI(t *testing.T) {
	s := newSongServer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := call(t, s.handleExportSongMIDI, map[string]any{"song": "night_drive"})
	if res.IsError || !strings.Contains(resultText(res), "3 track(s), 23 notes, 2 bars at 300 BPM") {
		t.Fatalf("export: %s", resultText(res))
	}
	data, err := os.ReadFile(filepath.Join(config.SongExportsDir(), "night_drive.mid"))
	if err != nil || !strings.HasPrefix(string(data), "MThd") {
		t.Fatalf("exported file: %v, %d bytes", err, len(data))
	}
}
