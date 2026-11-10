package mcpserver

import (
	"context"
	"encoding/json"
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
	if r, b := got[firstNote-2], got[firstNote-1]; r.Controller != 104 || b.Type != "cc" || b.Controller != 102 || b.Value != 127 {
		t.Errorf("before the first note: %+v then %+v, want the transport rewind and start", r, b)
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
