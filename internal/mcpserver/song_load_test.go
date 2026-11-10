package mcpserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/midi-transport/midicontrol"
)

// TestLoadSong takes a saved song to a loaded session in one call: authored,
// pulled by the iPad, loaded in AUM, brain and tap connected, every track
// heard. A second call finds nothing to author or pull.
func TestLoadSong(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), "needs confirm: true") {
		t.Fatalf("a load without confirm was accepted: %s", resultText(res))
	}

	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("load: %s", text)
	}
	for _, want := range []string{
		`"Night Drive" is loaded in AUM as Songs/Night Drive and ready to play`,
		"1. author  ok    Songs/Night Drive.aumproj: 5 strip(s)",
		"2. pull ",
		`3. load    ok    AUM has "Night Drive" open: 5 strip(s)`,
		"4. connect ok    brain and tap connected",
		"sound   ok    every track answers its first note",
		"bass       PolySynth",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("load report lacks %q:\n%s", want, text)
		}
	}
	if got := s.loadedSessionID(); got != "Songs/Night Drive" {
		t.Errorf("loaded session = %q, want the song's", got)
	}
	// The check set the song's own sound before it played: two preset loads.
	countPrograms := func(cmds []midicontrol.Command) (n int) {
		for _, cmd := range cmds {
			if cmd.Type == "pc" {
				n++
			}
		}
		return n
	}
	programs := countPrograms(r.waitCommands(func(cmds []midicontrol.Command) bool { return countPrograms(cmds) >= 2 }))
	if programs != 2 {
		t.Errorf("the check sent %d preset loads, want the two tracks' presets", programs)
	}

	// Nothing changed: the second load authors and pulls nothing.
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res = call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "1. author  skip  the staged Songs/Night Drive is the song's session") ||
		!strings.Contains(text, "2. pull    skip  the iPad already has this file") {
		t.Fatalf("second load: %s", text)
	}

	// A new control makes the session out of date: it is authored and pulled
	// again.
	edited := strings.Replace(toolSong, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError || !strings.Contains(resultText(res), "run load_song") {
		t.Fatalf("save with a new control: %s", resultText(res))
	}
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res = call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text = resultText(res)
	// The stand-in iPad may have fetched the new file before the load asked.
	if res.IsError || !strings.Contains(text, "1. author  ok") || !strings.Contains(text, "2. pull ") {
		t.Fatalf("load after a new control: %s", text)
	}
	if rec := loadSongSessions()["night_drive"]; rec.Pulled != rec.SHA256 {
		t.Errorf("after the load the iPad holds %s, the session is %s", rec.Pulled, rec.SHA256)
	}
}

// TestLoadSongSessionOptions checks the song's own session block is what the
// session is authored with, that a change to it makes the session out of
// date, and that the tool's arguments override it.
func TestLoadSongSessionOptions(t *testing.T) {
	s := newSongServer(t)
	doc := toolSong + "session: {track_taps: true}\n"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"})
	if res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	rec := loadSongSessions()["night_drive"]
	if !rec.Options.TrackTaps || rec.SHA256 == "" {
		t.Fatalf("recorded session = %+v, want track taps and the file's hash", rec)
	}
	if res := call(t, s.handleGetSong, map[string]any{"song": "night_drive"}); !strings.Contains(resultText(res), "session: Songs/Night Drive\n") {
		t.Fatalf("get after author: %s", resultText(res))
	}

	// Without the block the same song needs another session.
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); !strings.Contains(resultText(res), "is out of date") {
		t.Fatalf("save without the session block: %s", resultText(res))
	}
	// An argument overrides the document.
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive", "track_taps": true}); res.IsError {
		t.Fatalf("author with an override: %s", resultText(res))
	}
	if rec := loadSongSessions()["night_drive"]; !rec.Options.TrackTaps {
		t.Fatalf("recorded session after the override = %+v", rec)
	}
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong + "session: {hardware: mixer}\n"}); !res.IsError || !strings.Contains(resultText(res), `/session/hardware: "mixer" is not builtin or x32`) {
		t.Fatalf("an unknown hardware profile was accepted: %s", resultText(res))
	}
}

// TestLoadSongSilentTrack checks a track whose plugin makes no sound fails
// the load and is named.
func TestLoadSongSilentTrack(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	r.Voice = func(ch, note int) (float64, float64) {
		if ch == 2 { // the pad's instrument is dead
			return 0, 0
		}
		return 220, 0.2
	}
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips, "Acme: PolySynth failed: the plugin was invalidated")
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if !res.IsError {
		t.Fatalf("a load with a dead instrument succeeded: %s", text)
	}
	for _, want := range []string{
		`"Night Drive" is NOT ready to play`,
		"plugins FAIL  AUM reports: Acme: PolySynth failed",
		"sound   FAIL  silent: pad (PolySynth)",
		"SILENT",
		"next: a silent track is a plugin that failed to load or crashed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

// TestLoadSongNotPulled checks a session the iPad does not fetch ends the
// load at that step, before AUM is touched.
func TestLoadSongNotPulled(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	songPullTimeout = 300 * time.Millisecond // restored by the rig's cleanup
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "2. pull    FAIL  the iPad did not pull the session (the probe app could not be brought to the front") ||
		!strings.Contains(text, "next: open the AUv3 Probe app on the iPad") {
		t.Fatalf("load without a pull: %s", text)
	}
	if r.wda.Gestures() != 0 {
		t.Errorf("AUM was touched (%d gestures) although the session never arrived", r.wda.Gestures())
	}
}

// TestCheckSong checks the standalone check: it reads which session AUM has
// open, refuses another one, and listens to every track of the right one.
func TestCheckSong(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), "the song has no authored session") {
		t.Fatalf("check before authoring: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()

	// AUM shows its menu after the first gesture and the mixer after the
	// second.
	screen := func(session string, strips []ipadtest.Strip) {
		bare := ipadtest.AUM{Tempo: "300", Position: "001:1  00:00.0", Strips: strips}
		menu := bare
		menu.Session = session
		r.wda.SetScreens(bare.Source(), menu.Source(), bare.Source())
	}
	screen("Somebody Else's", ipadtest.DefaultAUM().Strips)
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive"})
	if !res.IsError || !strings.Contains(resultText(res), `AUM has "Somebody Else's" open, not the song's session "Night Drive"`) {
		t.Fatalf("check against another session: %s", resultText(res))
	}
	r.mu.Lock()
	sent := len(r.Commands)
	r.mu.Unlock()
	if sent != 0 {
		t.Errorf("the check played %d message(s) into a session that is not the song's", sent)
	}

	screen("Night Drive", nightDriveStrips)
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive"})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, `"Night Drive" is ready to play`) || !strings.Contains(text, `aum     ok    AUM has "Night Drive" open with the song's 5 strip(s)`) {
		t.Fatalf("check: %s", text)
	}
	if got := s.loadedSessionID(); got != "Songs/Night Drive" {
		t.Errorf("after a check that saw the session, loaded session = %q", got)
	}

	// A strip that is not there is reported.
	screen("Night Drive", nightDriveStrips[:3])
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "sound": false})
	if !res.IsError || !strings.Contains(resultText(res), "no strip for: drums, Master") {
		t.Fatalf("check with strips missing: %s", resultText(res))
	}

	// Without the look at AUM only the rig is checked.
	gestures := r.wda.Gestures()
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	if res.IsError || r.wda.Gestures() != gestures {
		t.Fatalf("check with ui false (gestures %d -> %d): %s", gestures, r.wda.Gestures(), resultText(res))
	}
}

// TestLoadedSession checks what the daemon claims to know about the open
// session: what it loaded, until a new brain connects.
func TestLoadedSession(t *testing.T) {
	var l loadedSession
	if l.get() != "" {
		t.Fatal("a session is known before any load")
	}
	l.brainConnected()
	l.set("Songs/A")
	if l.get() != "Songs/A" {
		t.Fatalf("loaded = %q", l.get())
	}
	l.brainConnected()
	if l.get() != "" {
		t.Fatalf("a session is still claimed after a new brain connected: %q", l.get())
	}
}

// TestPlaySongWarnsAboutAnotherSession checks play_song's warning comes from
// what the daemon loaded, not from what the iPad last downloaded.
func TestPlaySongWarnsAboutAnotherSession(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	// The iPad syncing some other session says nothing about what AUM has open.
	s.OnAUMSessionDownloaded("Live sets/Other.aumproj")
	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "sections": 1, "preroll_ms": 10})
	if res.IsError || strings.Contains(resultText(res), "warning:") {
		t.Fatalf("play after an unrelated download: %s", resultText(res))
	}
	call(t, s.handleStopSong, map[string]any{})

	s.loaded.set("Songs/Other")
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "sections": 1, "preroll_ms": 10})
	if res.IsError || !strings.Contains(resultText(res), `warning: AUM has "Songs/Other" open, not this song's session "Songs/Night Drive"`) {
		t.Fatalf("play into another session: %s", resultText(res))
	}
	call(t, s.handleStopSong, map[string]any{})
}

// TestLoadSongSequencer checks load_song keeps the session's sequencer on the
// song: a change to the notes alone leaves the layout as it was, but the
// sequencer would go on playing the old notes, so the session is authored and
// pulled again; after that the song plays from the sequencer.
func TestLoadSongSequencer(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	stageSequencer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	load := func(when string, args map[string]any) string {
		t.Helper()
		r.aumLoads("Songs", "Night Drive", nightDriveStrips)
		args["song"], args["confirm"] = "night_drive", true
		res := call(t, s.handleLoadSong, args)
		if res.IsError {
			t.Fatalf("load %s: %s", when, resultText(res))
		}
		return resultText(res)
	}

	r.pullStaged("Songs/Night Drive.aumproj")
	if text := load("the first time", map[string]any{}); !strings.Contains(text, "1. author  ok") {
		t.Fatalf("first load: %s", text)
	}
	rec := loadSongSessions()["night_drive"]
	if rec.Sequence == "" || rec.Sequencer == "" {
		t.Fatalf("record after the load = %+v, want the sequencer and its song", rec)
	}
	if text := load("again", map[string]any{}); !strings.Contains(text, "1. author  skip") {
		t.Fatalf("second load authored again: %s", text)
	}

	// New notes: the layout stands, the sequencer is behind.
	edited := strings.Replace(toolSong, `"A1 . A1 . A1 . G1 ."`, `"A1 A1 A1 . E1 . G1 ."`, 1)
	res := call(t, s.handleSaveSong, map[string]any{"yaml": edited})
	if res.IsError || !strings.Contains(resultText(res), "session: Songs/Night Drive\n") || !strings.Contains(resultText(res), "holds an older version of the song") {
		t.Fatalf("save with new notes: %s", resultText(res))
	}
	if text := load("after new notes", map[string]any{}); !strings.Contains(text, "1. author  ok") {
		t.Fatalf("load after new notes did not author the session again: %s", text)
	}
	if now := loadSongSessions()["night_drive"]; now.Sequence == rec.Sequence || now.Pulled != now.SHA256 {
		t.Errorf("record after new notes = %+v, want the new sequence, pulled", now)
	}
	if res := call(t, s.handleGetSong, map[string]any{"song": "night_drive"}); !strings.Contains(resultText(res), "playing: from the session's sequencer") {
		t.Fatalf("get after the load: %s", resultText(res))
	}

	// Asked to leave the sequencer out, the session is authored without it and
	// the next plain load puts it back.
	if text := load("without the sequencer", map[string]any{"sequencer": false}); !strings.Contains(text, "1. author  ok") {
		t.Fatalf("load with sequencer: false: %s", text)
	}
	if now := loadSongSessions()["night_drive"]; now.Sequence != "" {
		t.Errorf("record after sequencer: false = %+v, want no sequence", now)
	}
	if text := load("with it again", map[string]any{}); !strings.Contains(text, "1. author  ok") {
		t.Fatalf("load after sequencer: false did not author the sequencer back in: %s", text)
	}

	// And it plays from there.
	res = call(t, s.handlePlaySong, map[string]any{"song": "night_drive"})
	if res.IsError || !strings.Contains(resultText(res), "the session's sequencer plays") {
		t.Fatalf("play: %s", resultText(res))
	}
	s.player.Stop()
}

// TestSoundCheckIsolatesEachTrack checks the check mutes every strip but the
// one it plays, puts the song's own mutes back, and plays the lowest of the
// notes a track starts with: on a device a bell's delay was still ringing
// when the drums were checked with their hat, and the drums were called
// silent.
func TestSoundCheckIsolatesEachTrack(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	// The drums start with a kick and a hat together, and the pad is muted
	// by the song itself.
	doc := strings.Replace(toolSong, `kick: "x...x...x...x...", snare:`, `hat: "x.x.x.x.x.x.x.x.", kick: "x...x...x...x...", snare:`, 1)
	doc = strings.Replace(doc, "  - id: pad\n", "  - id: pad\n    mute: true\n", 1)
	doc = strings.Replace(doc, "verse: {bars: 1, play: {bass: bass, drums: beat}}", "verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "drums      DrumBox                C2 ") {
		t.Fatalf("check: %s", text)
	}

	cs, fail := s.loadSong("night_drive")
	if fail != nil {
		t.Fatal(resultText(fail))
	}
	muteCC := map[int]string{}
	for _, tp := range cs.plan.Tracks {
		muteCC[tp.MuteEvent(true).Data1] = tp.ID
	}
	cmds := r.waitCommands(func(cmds []midicontrol.Command) bool {
		n := 0
		for _, c := range cmds {
			if c.Type == "noteOff" {
				n++
			}
		}
		return n >= 3
	})
	// While a note sounds, its own strip is the only one not muted.
	muted := map[string]bool{}
	notes := 0
	for _, c := range cmds {
		if id, ok := muteCC[c.Controller]; ok && c.Type == "cc" && c.Channel == 15 {
			muted[id] = c.Value >= 64
		}
		if c.Type != "noteOn" {
			continue
		}
		notes++
		playing := cs.plan.Tracks[c.Channel-1].ID
		for _, tp := range cs.plan.Tracks {
			if muted[tp.ID] != (tp.ID != playing) {
				t.Errorf("while %s is checked, %s muted = %v", playing, tp.ID, muted[tp.ID])
			}
		}
	}
	if notes != 3 {
		t.Fatalf("the check played %d notes, want one per track", notes)
	}
	// Afterwards the song's own mutes are back: only the pad.
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	for _, c := range r.Commands {
		if id, ok := muteCC[c.Controller]; ok && c.Type == "cc" && c.Channel == 15 {
			muted[id] = c.Value >= 64
		}
	}
	r.mu.Unlock()
	if muted["bass"] || !muted["pad"] || muted["drums"] {
		t.Errorf("mutes after the check = %v, want only the pad's", muted)
	}
}

// TestSoundCheckAgainstAUMsMutes runs the check against a mixer that, like
// AUM, can keep the first of two opposite mutes sent to a strip in the same
// moment. On a device the first track was called silent (its strip was muted
// and opened again at once) and the last one was left muted (muted after its
// note and restored at once).
func TestSoundCheckAgainstAUMsMutes(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	// The pad is muted by the song itself and plays in the first section.
	doc := strings.Replace(toolSong, "  - id: pad\n", "  - id: pad\n    mute: true\n", 1)
	doc = strings.Replace(doc, "verse: {bars: 1, play: {bass: bass, drums: beat}}", "verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	cs, fail := s.loadSong("night_drive")
	if fail != nil {
		t.Fatal(resultText(fail))
	}
	r.strips(cs.plan)
	r.connect()

	for run := 1; run <= 2; run++ {
		res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
		text := resultText(res)
		if res.IsError || strings.Contains(text, "SILENT") || strings.Contains(text, "played again") {
			t.Fatalf("check %d: %s", run, text)
		}
		// The song's own mutes are back: only the pad's strip, on channel 2.
		r.wantMuted(fmt.Sprintf("after check %d", run), 2)
	}
}

// TestSoundCheckPlaysASilentTrackAgain checks a track that does not answer
// its first note is played once more before it is called silent, and that
// the report says so either way.
func TestSoundCheckPlaysASilentTrackAgain(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	// The bass swallows its first note (a plugin still busy with its
	// preset); the pad's instrument is dead.
	doc := strings.Replace(toolSong, "verse: {bars: 1, play: {bass: bass, drums: beat}}", "verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}", 1)
	awake := false // read by the rig under its lock
	r.Voice = func(ch, note int) (float64, float64) {
		if ch == 2 || (ch == 1 && !awake) {
			return 0, 0
		}
		return 220, 0.2
	}
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	done := make(chan *mcp.CallToolResult, 1)
	go func() { done <- call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false}) }()
	// The bass answers from its second note on.
	r.waitCommands(func(cmds []midicontrol.Command) bool {
		for _, c := range cmds {
			if c.Type == "noteOff" && c.Channel == 1 {
				return true
			}
		}
		return false
	})
	r.mu.Lock()
	awake = true
	r.mu.Unlock()
	res := <-done
	text := resultText(res)
	for _, want := range []string{
		"sound   FAIL  silent: pad (PolySynth)",
		"bass       PolySynth              A1   peak",
		"(silent at first, answered when played again)",
		"SILENT (peak -120.0 dBFS, played 2 times)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "silent: bass") {
		t.Errorf("the bass was called silent although it answered the second note:\n%s", text)
	}
}
