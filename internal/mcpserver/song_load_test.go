package mcpserver

import (
	"strings"
	"testing"
	"time"

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
