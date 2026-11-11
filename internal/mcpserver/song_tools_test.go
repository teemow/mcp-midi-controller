package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/song"
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

// newSongServer builds a server over an empty config dir with the test song's
// plugins staged. Its host-awake hook does nothing unless opts replace it, so
// playing a song in a test never starts a process.
func newSongServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	noHold := WithHostAwake(func() func() { return func() {} })
	s := New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), append([]Option{noHold}, opts...)...)
	stageSongPlugins(t)
	return s
}

// dialBrain connects a stand-in ProbeMidiBrain to the hub served by srv and
// returns the commands it is sent. The channel closes when the connection
// ends, which hangUp forces.
func dialBrain(t *testing.T, srv *httptest.Server, hub *midicontrol.Hub) (frames <-chan midicontrol.Command, hangUp func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/midi-control", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !hub.Connected(); {
		time.Sleep(10 * time.Millisecond)
	}
	ch := make(chan midicontrol.Command, 256)
	go func() {
		defer close(ch)
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var cmd midicontrol.Command
			if json.Unmarshal(data, &cmd) == nil {
				ch <- cmd
			}
		}
	}()
	return ch, func() { _ = c.CloseNow() }
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
	if res.IsError || !strings.Contains(text, "presets (2 of 2 matching, 0 measured)") || !strings.Contains(text, "Warm Pad") || strings.Contains(text, "Deep Bass") {
		t.Fatalf("presets matching pad: %s", text)
	}
	res = call(t, s.handleGetSongPlugin, map[string]any{"plugin": "syn1", "query": "cutoff"})
	if text := resultText(res); !strings.Contains(text, "Cutoff (cutoff): 0..1, default patch 0.5") {
		t.Fatalf("params matching cutoff: %s", text)
	}
	if res := call(t, s.handleGetSongPlugin, map[string]any{"plugin": "nope"}); !res.IsError {
		t.Fatal("an unknown plugin was accepted")
	}
}

// TestRigProbe finds the brain and the tap among the staged probes the way
// the receiver stores them, under their probe id, and nothing when they were
// never scanned.
func TestRigProbe(t *testing.T) {
	newSongServer(t)
	if got := rigProbe(songCatalog(), aum.ProbeBrainNode()); got != nil {
		t.Fatalf("a brain probe was found among the song's plugins: %+v", got.Component)
	}
	for _, node := range []aum.NodeSpec{aum.ProbeBrainNode(), aum.ProbeTapNode()} {
		dump := device.ProbeDump{Component: node.Component, Name: node.ComponentName}
		data, err := json.Marshal(dump)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.AUv3ProbesDir(), device.ProbeID(dump)+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cat := songCatalog()
	for _, node := range []aum.NodeSpec{aum.ProbeBrainNode(), aum.ProbeTapNode()} {
		got := rigProbe(cat, node)
		if got == nil || got.Component != node.Component {
			t.Errorf("rigProbe(%s) = %+v, want its staged probe", node.ComponentName, got)
		}
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

	frames, _ := dialBrain(t, srv, hub)

	// Not authored yet: it plays, with a warning.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "from": "nowhere"})
	if !res.IsError || !strings.Contains(resultText(res), "no section") {
		t.Fatalf("a bad start was accepted: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20, "transport": true})
	if res.IsError || !strings.Contains(resultText(res), `playing "Night Drive" live from verse (bar 1)`) || strings.Contains(resultText(res), "warning:") || strings.Contains(resultText(res), "note:") {
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
	// The transport is rewound first of all and started right before the
	// first note; it is stopped at the very end and left at the top.
	if r, b := got[0], got[firstNote-1]; r.Type != "cc" || r.Controller != 104 || b.Type != "cc" || b.Controller != 102 || b.Value != 127 {
		t.Errorf("first frame %+v, before the first note %+v: want the transport rewind and start", r, b)
	}
	if e, r := got[len(got)-2], got[len(got)-1]; e.Type != "cc" || e.Controller != 103 || r.Controller != 104 {
		t.Errorf("last frames: %+v then %+v, want the transport stop and rewind", e, r)
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
	if !strings.Contains(resultText(res), `"night_drive" (live) finished`) {
		t.Errorf("playback status: %s", resultText(res))
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Errorf("stop with nothing playing: %s", resultText(res))
	}
}

// fakeHost stands in for the host-awake hook: it counts the holds taken and
// released and notes what the player reported at each.
type fakeHost struct {
	status func() song.Status

	mu             sync.Mutex
	held, released int
	atHold, atEnd  []song.Status
}

func (h *fakeHost) hold() func() {
	st := h.status()
	h.mu.Lock()
	h.held++
	h.atHold = append(h.atHold, st)
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			st := h.status()
			h.mu.Lock()
			h.released++
			h.atEnd = append(h.atEnd, st)
			h.mu.Unlock()
		})
	}
}

// counts reports the holds taken and released so far.
func (h *fakeHost) counts() (held, released int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held, h.released
}

// waitReleased waits until every hold taken has been released.
func (h *fakeHost) waitReleased(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if held, released := h.counts(); held == released {
			return
		}
	}
	held, released := h.counts()
	t.Fatalf("the host is still held awake: %d hold(s), %d released", held, released)
}

// lastEnd is what the player reported when the latest hold was released.
func (h *fakeHost) lastEnd() song.Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.atEnd[len(h.atEnd)-1]
}

// TestPlaySongHoldsTheHostAwake checks play_song holds its host awake for
// exactly as long as a song plays: from before the playback begins until it
// has finished, was stopped, was replaced by the next song or failed, and not
// at all when nothing was played.
func TestPlaySongHoldsTheHostAwake(t *testing.T) {
	hub := midicontrol.NewHub()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var s *Server
	host := &fakeHost{status: func() song.Status { return s.player.Status() }}
	s = newSongServer(t, WithMidiControl(hub), WithHostAwake(host.hold))
	t.Cleanup(func() { s.player.Stop() })
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	want := func(when string, held, released int) {
		t.Helper()
		if h, r := host.counts(); h != held || r != released {
			t.Fatalf("%s: %d hold(s) taken and %d released, want %d and %d", when, h, r, held, released)
		}
	}

	// Nothing played, nothing held: no brain, no such section, no such track.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive"}); !res.IsError {
		t.Fatalf("play without a brain did not fail: %s", resultText(res))
	}
	want("without a brain", 0, 0)
	frames, hangUp := dialBrain(t, srv, hub)
	go func() {
		for range frames {
		}
	}()
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "from": "nowhere"}); !res.IsError {
		t.Fatalf("a bad start was accepted: %s", resultText(res))
	}
	want("with a bad start", 0, 0)
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "tracks": []string{"tuba"}}); !res.IsError {
		t.Fatalf("an unknown track was accepted: %s", resultText(res))
	}
	if held, released := host.counts(); held != released {
		t.Fatalf("a refused play left the host held awake: %d hold(s), %d released", held, released)
	}
	base, _ := host.counts()

	// A song that plays to its end: held before the playback begins, released
	// once it has finished.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20}); res.IsError {
		t.Fatalf("play: %s", resultText(res))
	}
	want("while the song plays", base+1, base)
	host.mu.Lock()
	before := host.atHold[len(host.atHold)-1]
	host.mu.Unlock()
	if before.Playing || before.Song != "" {
		t.Errorf("the hold was taken with the player at %+v, want before any playback began", before)
	}
	host.waitReleased(t)
	want("after the song", base+1, base+1)
	if st := host.lastEnd(); st.Playing || st.Outcome != "finished" {
		t.Errorf("the hold was released at %+v, want the song finished", st)
	}

	// A song replaced by the next: the new one holds, the old one lets go.
	// Then stop_song: released by the time it returns.
	for i := 0; i < 2; i++ {
		if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive"}); res.IsError {
			t.Fatalf("play: %s", resultText(res))
		}
	}
	want("with the song played again over itself", base+3, base+2)
	if st := host.lastEnd(); st.Outcome != "stopped" {
		t.Errorf("the replaced playback's hold was released at %+v, want stopped", st)
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	want("after stop_song", base+3, base+3)
	if st := host.lastEnd(); st.Outcome != "stopped" {
		t.Errorf("the stopped playback's hold was released at %+v, want stopped", st)
	}

	// A song that fails: the brain goes away while it plays.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("play: %s", resultText(res))
	}
	want("while the last song plays", base+4, base+3)
	hangUp()
	host.waitReleased(t)
	if st := host.lastEnd(); st.Playing || st.Outcome == "" || st.Outcome == "finished" || st.Outcome == "stopped" {
		t.Errorf("the hold was released at %+v, want the playback failed", st)
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

// stageSequencer stages the probe of the sequencer a song's session can host,
// the way the receiver stores a scanned plugin.
func stageSequencer(t *testing.T) {
	t.Helper()
	dump := device.ProbeDump{Component: song.AtomComponent, Name: "Atom | Piano Roll²"}
	data, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.AUv3ProbesDir(), device.ProbeID(dump)+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// collectFrames gathers what the brain is sent until the playback has ended.
func collectFrames(t *testing.T, s *Server, frames <-chan midicontrol.Command) []midicontrol.Command {
	t.Helper()
	var got []midicontrol.Command
	deadline := time.After(15 * time.Second)
	for {
		playing := s.player.Status().Playing
		select {
		case cmd, ok := <-frames:
			if !ok {
				return got
			}
			got = append(got, cmd)
		case <-time.After(150 * time.Millisecond):
			if !playing {
				return got
			}
		case <-deadline:
			t.Fatal("playback did not finish")
		}
	}
}

// TestAuthorSongSessionSequencer: with the sequencer installed, the song's
// session hosts it with the song in it, and the song knows whether that copy
// is still the song.
func TestAuthorSongSessionSequencer(t *testing.T) {
	s := newSongServer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}

	// Not installed: asking for it is an error, the default plays live.
	res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive", "sequencer": true})
	if !res.IsError || !strings.Contains(resultText(res), "/sequencer:") || !strings.Contains(resultText(res), "aumi-apr2-_v_e_e_k") {
		t.Fatalf("asking for a sequencer that is not staged: %s", resultText(res))
	}
	res = call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"})
	if res.IsError || !strings.Contains(resultText(res), "the song plays live through the brain") {
		t.Fatalf("author without the sequencer staged: %s", resultText(res))
	}
	if rec := loadSongSessions()["night_drive"]; rec.Sequence != "" || rec.Sequencer != "" {
		t.Errorf("record = %+v, want no sequencer", rec)
	}

	stageSequencer(t)
	res = call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive", "sequencer": false})
	if res.IsError || strings.Contains(resultText(res), "holding the whole song") || loadSongSessions()["night_drive"].Sequence != "" {
		t.Fatalf("sequencer: false hosted one: %s", resultText(res))
	}
	res = call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"})
	text := resultText(res)
	for _, want := range []string{
		"+ Atom | Piano Roll² holding the whole song (23 notes,", "after a lead-in of 2 bar(s), routed like the brain",
		"play_song starts AUM's transport and the sequencer plays the song",
	} {
		if res.IsError || !strings.Contains(text, want) {
			t.Errorf("author report lacks %q:\n%s", want, text)
		}
	}
	rec := loadSongSessions()["night_drive"]
	if rec.Session != "Songs/Night Drive" || rec.Sequencer != "aumi-apr2-_v_e_e_k" || rec.Sequence == "" {
		t.Errorf("record = %+v, want the session with its sequencer and the sequence's fingerprint", rec)
	}

	sess, err := aum.OpenFile(filepath.Join(config.AUMSessionsDir(), "Songs", "Night Drive.aumproj"))
	if err != nil {
		t.Fatalf("open the staged session: %v", err)
	}
	brain := sess.Channels()[song.BrainStrip]
	if len(brain.Nodes) != 2 || brain.Nodes[song.SequencerSlot].Component == nil || *brain.Nodes[song.SequencerSlot].Component != song.AtomComponent {
		t.Fatalf("brain strip = %+v, want the brain and the sequencer", brain.Nodes)
	}
	doc, err := sess.NodeAuStateDoc(song.BrainStrip, song.SequencerSlot)
	if err != nil || !strings.Contains(string(doc["patterns"]), `"pitch":33`) || len(doc["store"]) == 0 {
		t.Errorf("the staged sequencer does not hold the song's notes (%v, %d keys)", err, len(doc))
	}

	// The song says how it plays: from the sequencer while that holds it …
	res = call(t, s.handleGetSong, map[string]any{"song": "night_drive"})
	if !strings.Contains(resultText(res), "playing: from the session's sequencer") {
		t.Fatalf("get after author: %s", resultText(res))
	}
	// … live after a change to the notes, until the session is authored again.
	edited := strings.Replace(toolSong, `"A1 . A1 . A1 . G1 ."`, `"A1 A1 A1 . E1 . G1 ."`, 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": edited})
	if res.IsError || !strings.Contains(resultText(res), "session: Songs/Night Drive\n") || !strings.Contains(resultText(res), "playing: live; the session's sequencer holds an older version of the song") {
		t.Fatalf("save with new notes: %s", resultText(res))
	}
	res = call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"})
	if res.IsError {
		t.Fatalf("author again: %s", resultText(res))
	}
	if res = call(t, s.handleGetSong, map[string]any{"song": "night_drive"}); !strings.Contains(resultText(res), "playing: from the session's sequencer") {
		t.Fatalf("get after authoring again: %s", resultText(res))
	}
}

// TestPlaySongSequenced plays the song from its session's sequencer: the brain
// is sent what starts AUM's transport and, at the end, what stops it, and not
// one note.
func TestPlaySongSequenced(t *testing.T) {
	hub := midicontrol.NewHub()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var s *Server
	host := &fakeHost{status: func() song.Status { return s.player.Status() }}
	s = newSongServer(t, WithMidiControl(hub), WithHostAwake(host.hold))
	t.Cleanup(func() { s.player.Stop() })
	stageSequencer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	frames, _ := dialBrain(t, srv, hub)

	// No session yet: nothing to play from, the song plays live.
	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "mode": "sequenced"})
	if !res.IsError || !strings.Contains(resultText(res), "hosts no sequencer") {
		t.Fatalf("sequenced without a session: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "mode": "sideways"}); !res.IsError {
		t.Fatalf("an unknown mode was accepted: %s", resultText(res))
	}
	// A part of the song is not the sequencer's to play.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "mode": "sequenced", "tracks": []string{"bass"}})
	if !res.IsError || !strings.Contains(resultText(res), "plays the whole song from the top") {
		t.Fatalf("sequenced with tracks: %s", resultText(res))
	}

	// The whole song, no mode named: the sequencer plays it.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive"})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, `started AUM's transport: the session's sequencer plays "Night Drive" from the top`) ||
		!strings.Contains(text, "after a lead-in of 1.6 s") || strings.Contains(text, "warning:") {
		t.Fatalf("play: %s", text)
	}
	if st := call(t, s.handleGetSongPlayback, map[string]any{}); !strings.Contains(resultText(st), `playing "night_drive" (sequenced): AUM's transport runs, the first bar comes in`) {
		t.Errorf("playback status in the lead-in: %s", resultText(st))
	}
	// The host is held awake for a sequenced song as for a live one; nothing
	// refused above took a hold.
	if held, released := host.counts(); held != 1 || released != 0 {
		t.Errorf("while the sequencer plays: %d hold(s) taken, %d released, want one held", held, released)
	}
	got := collectFrames(t, s, frames)
	host.waitReleased(t)
	if st := host.lastEnd(); st.Outcome != "finished" || st.Mode != song.ModeSequenced {
		t.Errorf("the hold was released at %+v, want the sequenced song finished", st)
	}
	want := []midicontrol.Command{
		{Type: "cc", Channel: 15, Controller: 106, Value: 0},   // the sequencer on
		{Type: "cc", Channel: 15, Controller: 104, Value: 127}, // rewind
		{Type: "cc", Channel: 15, Controller: 102, Value: 127}, // start
		{Type: "cc", Channel: 15, Controller: 103, Value: 127}, // stop
		{Type: "cc", Channel: 15, Controller: 104, Value: 127}, // rewind
	}
	if len(got) != len(want) {
		t.Fatalf("the brain was sent %+v, want the transport's start and stop only", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if st := call(t, s.handleGetSongPlayback, map[string]any{}); !strings.Contains(resultText(st), `"night_drive" (sequenced) finished`) {
		t.Errorf("playback status at the end: %s", resultText(st))
	}

	// stop_song stops the transport and rewinds it.
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "mode": "sequenced"}); res.IsError {
		t.Fatalf("play again: %s", resultText(res))
	}
	for deadline := time.Now().Add(5 * time.Second); s.player.Status().Sent < 3; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the transport was not started")
		}
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "(sequenced) stopped") {
		t.Fatalf("stop: %s", resultText(res))
	}
	got = collectFrames(t, s, frames)
	if n := len(got); n != 5 || got[n-2].Controller != 103 || got[n-1].Controller != 104 {
		t.Errorf("a stopped sequenced playback sent %+v, want the start, then stop and rewind", got)
	}

	// A part plays live in the same session. With the transport, the sequencer
	// is kept out for the length of the playback.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "from": "chorus", "preroll_ms": 20, "transport": true})
	if res.IsError || !strings.Contains(resultText(res), `playing "Night Drive" live from chorus (bar 2)`) || strings.Contains(resultText(res), "note:") {
		t.Fatalf("play a part: %s", resultText(res))
	}
	got = collectFrames(t, s, frames)
	var ccs []int
	notes := 0
	for _, cmd := range got {
		if cmd.Type == "noteOn" {
			notes++
		}
		if cmd.Type == "cc" && cmd.Channel == 15 && cmd.Controller >= 102 && cmd.Controller <= 106 {
			ccs = append(ccs, cmd.Controller*1000+cmd.Value)
		}
	}
	if fmt.Sprint(ccs) != fmt.Sprint([]int{106127, 104127, 102127, 103127, 104127, 106000}) || notes == 0 {
		t.Errorf("live with the transport sent %v and %d notes, want the sequencer bypassed, rewind, start, the notes, stop, rewind, the sequencer back on", ccs, notes)
	}

	// New notes: the sequencer holds the old song, so the song plays live and
	// says why; asked to play sequenced, it refuses.
	edited := strings.Replace(toolSong, `"A1 . A1 . A1 . G1 ."`, `"A1 A1 A1 . E1 . G1 ."`, 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError {
		t.Fatalf("save with new notes: %s", resultText(res))
	}
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "mode": "sequenced"})
	if !res.IsError || !strings.Contains(resultText(res), "holds an older version of the song") {
		t.Fatalf("sequenced with a stale sequence: %s", resultText(res))
	}
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 20})
	if res.IsError || !strings.Contains(resultText(res), `playing "Night Drive" live from verse`) ||
		!strings.Contains(resultText(res), "note: played live: the session's sequencer holds an older version of the song") {
		t.Fatalf("play with a stale sequence: %s", resultText(res))
	}
	got = collectFrames(t, s, frames)
	bass := 0
	for _, cmd := range got {
		if cmd.Type == "noteOn" && cmd.Channel == 1 {
			bass++
		}
		if cmd.Type == "cc" && cmd.Channel == 15 && (cmd.Controller == 102 || cmd.Controller == 106) {
			t.Errorf("a live playback without the transport sent %+v", cmd)
		}
	}
	if bass != 10 {
		t.Errorf("the live playback sent %d bass notes, want the edited song's 10", bass)
	}
}

// TestSongPlayMode covers the choice between the two ways to play.
func TestSongPlayMode(t *testing.T) {
	sg, err := song.Parse([]byte(toolSong))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := newSongServer(t).compileSong("night_drive", sg)
	if err != nil {
		t.Fatal(err)
	}
	tl := cs.timeline
	current := songSessionRecord{Session: "Songs/Night Drive", Fingerprint: tl.Fingerprint, Sequencer: "seq", Sequence: tl.Sequence().Fingerprint()}
	noSequencer := songSessionRecord{Session: current.Session, Fingerprint: tl.Fingerprint}
	oldNotes := current
	oldNotes.Sequence = "0000000000000000"
	oldLayout := current
	oldLayout.Fingerprint = "0000000000000000"

	for _, tc := range []struct {
		name    string
		want    string
		rec     songSessionRecord
		partial bool
		mode    string // "" when an error is expected
		note    string // a fragment of the note, "" for none
		err     string // a fragment of the error
	}{
		{name: "whole song, current sequence", rec: current, mode: song.ModeSequenced},
		{name: "asked for sequenced", want: "sequenced", rec: current, mode: song.ModeSequenced},
		{name: "asked for live", want: "live", rec: current, mode: song.ModeLive},
		{name: "mode in capitals", want: " Sequenced ", rec: current, mode: song.ModeSequenced},
		{name: "a part", rec: current, partial: true, mode: song.ModeLive},
		{name: "a part, asked for sequenced", want: "sequenced", rec: current, partial: true, err: "whole song from the top"},
		{name: "no sequencer", rec: noSequencer, mode: song.ModeLive},
		{name: "no sequencer, asked for sequenced", want: "sequenced", rec: noSequencer, err: "hosts no sequencer"},
		{name: "never authored", rec: songSessionRecord{}, mode: song.ModeLive},
		{name: "old notes", rec: oldNotes, mode: song.ModeLive, note: "older version of the song"},
		{name: "old notes, asked for sequenced", want: "sequenced", rec: oldNotes, err: "older version of the song"},
		{name: "old notes, asked for live", want: "live", rec: oldNotes, mode: song.ModeLive},
		{name: "old layout", rec: oldLayout, mode: song.ModeLive},
		{name: "old layout, asked for sequenced", want: "sequenced", rec: oldLayout, err: "tempo, plugins or controls changed"},
		{name: "unknown mode", want: "fast", rec: current, err: "not a mode"},
	} {
		mode, note, err := songPlayMode(tc.want, tc.rec, tl, tc.partial)
		switch {
		case tc.err != "":
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: got mode %q, error %v; want an error with %q", tc.name, mode, err, tc.err)
			}
		case err != nil || mode != tc.mode:
			t.Errorf("%s: got mode %q, error %v; want %q", tc.name, mode, err, tc.mode)
		case (tc.note == "") != (note == "") || !strings.Contains(note, tc.note):
			t.Errorf("%s: note %q, want one with %q", tc.name, note, tc.note)
		}
	}
}

// TestSongToolsReview checks save_song and get_song say what stands out in a
// song as music, and compare its sounds with the other stored songs'.
func TestSongToolsReview(t *testing.T) {
	s := newSongServer(t)
	// Eight bars: long enough to be a song the next one is compared with.
	long := strings.NewReplacer("{bars: 1,", "{bars: 4,").Replace(toolSong)

	res := call(t, s.handleSaveSong, map[string]any{"yaml": long})
	if res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{
		"review (the song as music; advice, none of it an error):",
		"  length: 0:06 long (8 bars). A full song runs 3 to 4½ minutes: 225 to 337 bars at this tempo.",
		"  tracks: 3 tracks; a song can have 12.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the review lacks %q:\n%s", want, text)
		}
	}
	// It is the only song, so there is nothing to compare its sounds with.
	if strings.Contains(text, "  sounds:") {
		t.Errorf("the only stored song got a remark on its sounds:\n%s", text)
	}

	// A second song with the first one's bass and another pad.
	second := strings.NewReplacer(`title: "Night Drive"`, `title: "Day Trip"`, "preset: Warm Pad", "preset: Init").Replace(long)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": second, "dry_run": true})
	if res.IsError {
		t.Fatalf("dry run of the second song: %s", resultText(res))
	}
	text = resultText(res)
	if want := `  sounds: the same sound as another stored song: bass has PolySynth "Deep Bass", as in night_drive (one of its 4 presets); pad has PolySynth "Bright Pad", as in night_drive (one of its 4 presets).`; !strings.Contains(text, want) {
		t.Errorf("the review lacks %q:\n%s", want, text)
	}
	var out struct {
		Summary struct {
			Review []song.Remark `json:"review"`
		} `json:"summary"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured result: %v", err)
	}
	about := map[string]int{}
	for _, r := range out.Summary.Review {
		about[r.About]++
	}
	if about[song.AboutLength] != 1 || about[song.AboutTracks] != 1 || about[song.AboutSounds] != 1 {
		t.Errorf("structured review = %+v, want one remark each on length, tracks and sounds", out.Summary.Review)
	}

	// A stored song is not compared with itself, and get_song shows its review
	// to whoever reads it as an example.
	res = call(t, s.handleGetSong, map[string]any{"song": "night_drive"})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "  length: 0:06 long") || strings.Contains(text, "  sounds:") {
		t.Errorf("get_song's review:\n%s", text)
	}

	// A sound lab has no notes and gets no review, and it is no song another
	// one's sounds are compared with.
	lab := `
title: Lab
tempo: 120
tracks: [{id: a, instrument: {plugin: syn1, preset: Init}}]
patterns: {rest: {bars: 1, notes: ["."]}}
sections: {idle: {bars: 1}}
arrangement: [idle]
`
	res = call(t, s.handleSaveSong, map[string]any{"yaml": lab})
	if res.IsError || strings.Contains(resultText(res), "review") {
		t.Fatalf("a lab's save: %s", resultText(res))
	}
	res = call(t, s.handleSaveSong, map[string]any{"yaml": second, "dry_run": true})
	if text := resultText(res); res.IsError || strings.Contains(text, "as in lab") {
		t.Errorf("the lab counts as a song with sounds to share:\n%s", text)
	}
}
