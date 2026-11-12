package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/mcp-midi-controller/internal/sound"
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

// nightDriveSize is how the probe app writes the size of the staged session
// of toolSong.
func nightDriveSize(t *testing.T) string {
	t.Helper()
	info, err := os.Stat(stagedSessionPath("Songs/Night Drive"))
	if err != nil {
		t.Fatal(err)
	}
	return ipad.ProbeByteLabel(info.Size())
}

// TestLoadSongResolvesItsOwnConflict checks a session the probe app holds as
// a conflict is fetched all the same when the copy in AUM's folder is a file
// the daemon staged: the daemon taps "keep controller" on that row, on no
// other, and says so.
func TestLoadSongResolvesItsOwnConflict(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	app := r.probeApp(map[string][]ipadtest.ProbeFile{
		"Songs": {{Name: "Aardvark.aumproj", State: ipadtest.ProbeConflictState}, {Name: "Night Drive.aumproj"}, {Name: "Zebra.aumproj", State: ipadtest.ProbeConflictState}},
		"Sets":  {{Name: "Night Drive.aumproj", State: ipadtest.ProbeConflictState}},
	})
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}

	// The first load: the app is brought forward and fetches the file.
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
	if text := resultText(res); res.IsError || !strings.Contains(text, "2. pull    ok    the iPad pulled the session (the probe app was brought to the front)") {
		t.Fatalf("first load: %s", text)
	}
	if got, want := app.Actions(), []string{"tab aum sessions", "folder Songs", "sync"}; !slices.Equal(got, want) {
		t.Fatalf("for the first pull the app was asked %q, want %q", got, want)
	}
	served := nightDriveSize(t)
	if rec := loadSongSessions()["night_drive"]; len(rec.Served) != 1 || ipad.ProbeByteLabel(rec.Served[0]) != served {
		t.Fatalf("the record keeps %v as fetched, want the staged file's size (%s)", rec.Served, served)
	}

	// The song changes; the app holds its session as a conflict, with the
	// file it fetched before in AUM's folder.
	edited := strings.Replace(toolSong, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError {
		t.Fatalf("save with a new control: %s", resultText(res))
	}
	app.Set(func(p *ipadtest.ProbeApp) {
		p.Log = nil
		p.Files["Songs"][1].State = ipadtest.ProbeConflictState
		p.Files["Songs"][1].Info = "session · " + served + " · 12. Nov 2026 at 02:06"
	})
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res = call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("load with a conflict of the daemon's own: %s", text)
	}
	for _, want := range []string{
		"2. pull    ok    the iPad pulled the session. The AUv3 Probe app held Songs/Night Drive.aumproj as a conflict",
		"was the size of a file the daemon had staged (" + served + ")",
		`tapped "keep controller" on that row, and the staged session replaced it`,
		`3. load    ok    AUM has "Night Drive" open`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("load report lacks %q:\n%s", want, text)
		}
	}
	// The browser was walked from its root before the tap, and one row was
	// tapped: not the conflicts beside it, not its namesake in another folder.
	var keeps []string
	for _, a := range app.Actions() {
		if strings.HasPrefix(a, "keep ") {
			keeps = append(keeps, a)
		}
	}
	if want := []string{"keep controller Songs/Night Drive.aumproj"}; !slices.Equal(keeps, want) {
		t.Errorf("sides picked = %q, want %q (the app was asked %q)", keeps, want, app.Actions())
	}
	if got := app.Actions(); !slices.Contains(got, "back") || got[len(got)-1] != keeps[0] {
		t.Errorf("the app was asked %q, want the browser walked from its root and the tap last", got)
	}
	if rec := loadSongSessions()["night_drive"]; rec.Pulled != rec.SHA256 {
		t.Errorf("after the load the iPad holds %s, the session is %s", rec.Pulled, rec.SHA256)
	}
}

// TestLoadSongNamesAConflict checks the conflicts the daemon leaves alone end
// the pull step at once, by the session's name and with what to do: a copy in
// AUM's folder that was saved there, one the iPad never fetched from the
// daemon, and a session outside Songs/.
func TestLoadSongNamesAConflict(t *testing.T) {
	const savedInAUM = "session · 9.9 MB · 12. Nov 2026 at 02:06"
	for name, c := range map[string]struct {
		outID string
		// loaded: the iPad fetched the session once before the conflict.
		loaded bool
		info   string
		want   []string
	}{
		"saved in AUM": {
			outID: "Songs/Night Drive", loaded: true, info: savedInAUM,
			want: []string{
				`2. pull    FAIL  the iPad did not pull the session: the AUv3 Probe app holds Songs/Night Drive.aumproj as a conflict ("conflict — both sides changed") and copies it neither way until a side is picked.`,
				`The copy in AUM's folder ("` + savedInAUM + `") is not the size of a file the daemon staged for it (`,
				"it was saved in AUM",
				`next: what AUM's folder holds under this name is lost when the staged session replaces it: save it under another name in AUM first if it is to be kept.`,
				`in the AUv3 Probe app (the "aum sessions" tab, folder "Songs"), tap "keep controller" on the row "Night Drive.aumproj" and on no other, and load_song again`,
			},
		},
		"never fetched": {
			outID: "Songs/Night Drive", info: savedInAUM,
			want: []string{
				"the AUv3 Probe app holds Songs/Night Drive.aumproj as a conflict",
				"The daemon has no record of a file the iPad fetched under this name",
			},
		},
		"outside Songs": {
			outID: "Sets/Night Drive", loaded: true,
			want: []string{
				"the AUv3 Probe app holds Sets/Night Drive.aumproj as a conflict",
				"The daemon picks a side by itself only for a song's session under Songs/.",
				`folder "Sets"`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSongRig(t)
			s := r.s
			folder, title := path.Split(c.outID)
			folder = strings.Trim(folder, "/")
			app := r.probeApp(map[string][]ipadtest.ProbeFile{
				folder:  {{Name: "Aardvark.aumproj", State: ipadtest.ProbeConflictState}, {Name: title + ".aumproj"}},
				"Other": {{Name: title + ".aumproj", State: ipadtest.ProbeConflictState}},
			})
			if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
				t.Fatalf("save: %s", resultText(res))
			}
			args := map[string]any{"song": "night_drive", "confirm": true, "check": false, "out_id": c.outID}
			if c.loaded {
				r.aumLoads(folder, title, nightDriveStrips)
				if res := call(t, s.handleLoadSong, args); res.IsError {
					t.Fatalf("first load: %s", resultText(res))
				}
				edited := strings.Replace(toolSong, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1)
				if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError {
					t.Fatalf("save with a new control: %s", resultText(res))
				}
			}
			app.Set(func(p *ipadtest.ProbeApp) {
				p.Log = nil
				p.Files[folder][1].State = ipadtest.ProbeConflictState
				p.Files[folder][1].Info = c.info
			})
			r.wda.SetScreens() // AUM's gestures count from here

			start := time.Now()
			res := call(t, s.handleLoadSong, args)
			text := resultText(res)
			if !res.IsError {
				t.Fatalf("a load with a conflict the daemon leaves alone succeeded: %s", text)
			}
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("report lacks %q:\n%s", want, text)
				}
			}
			// The wait ended with the conflict, not with its timeout.
			if took := time.Since(start); took > songPullTimeout/2 {
				t.Errorf("the pull step took %s of its %s although the conflict was on screen", took, songPullTimeout)
			}
			for _, a := range app.Actions() {
				if strings.HasPrefix(a, "keep ") {
					t.Errorf("the daemon picked a side: %q", a)
				}
			}
			if r.wda.Gestures() != 0 {
				t.Errorf("AUM was touched (%d gestures) although the session never arrived", r.wda.Gestures())
			}
		})
	}
}

// TestLoadSongPullSaysWhatTheAppShows checks a pull that times out without a
// conflict reports what the probe app's screen said meanwhile.
func TestLoadSongPullSaysWhatTheAppShows(t *testing.T) {
	for name, c := range map[string]struct {
		app  func(p *ipadtest.ProbeApp)
		want []string
	}{
		"offline, just started": {
			app: func(p *ipadtest.ProbeApp) { p.Units, p.Status, p.Cycle = "syncing 2/184…", ipad.ProbeOffline, true },
			want: []string{
				`2. pull    FAIL  the iPad did not pull the session within 400ms (the probe app was brought to the front; its sessions tab says "controller offline — browsing device copy": it does not reach the daemon`,
				"it had just been started and was sending its audio units to the daemon again (2/184 when it came forward), which it does only while it is in front",
				"next: check which daemon the AUv3 Probe app has found",
			},
		},
		"an error on the row": {
			app: func(p *ipadtest.ProbeApp) {
				p.Tab, p.Path, p.Cycle = ipad.ProbeSessionsTab, []string{"Songs"}, true
				p.Files["Songs"][0].State = "the request timed out"
			},
			want: []string{
				`(the probe app was brought to the front; it lists Night Drive.aumproj as "the request timed out")`,
				"next: open the AUv3 Probe app on the iPad",
			},
		},
		"no folder linked": {
			app: func(p *ipadtest.ProbeApp) { p.Unlinked = true },
			want: []string{
				"its list of sessions could not be read: the AUv3 Probe app's session browser is not shown: AUM's folder is not linked in the app",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSongRig(t)
			s := r.s
			songPullTimeout = 400 * time.Millisecond // restored by the rig's cleanup
			app := r.probeApp(map[string][]ipadtest.ProbeFile{"Songs": {{Name: "Night Drive.aumproj"}}})
			app.Set(c.app)
			if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
				t.Fatalf("save: %s", resultText(res))
			}
			res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
			text := resultText(res)
			if !res.IsError {
				t.Fatalf("a load without a pull succeeded: %s", text)
			}
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("report lacks %q:\n%s", want, text)
				}
			}
		})
	}
}

// TestFetchedSizes checks the record of a song's session keeps the sizes of
// the files the iPad fetched under its name: every download's, and for a
// record from before sizes were kept the one the iPad holds, taken from the
// staged file the next authoring replaces.
func TestFetchedSizes(t *testing.T) {
	s := newSongServer(t)
	stagedSize := func() int64 {
		t.Helper()
		info, err := os.Stat(stagedSessionPath("Songs/Night Drive"))
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	save := func(doc string) {
		t.Helper()
		if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
			t.Fatalf("save: %s", resultText(res))
		}
		if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
			t.Fatalf("author: %s", resultText(res))
		}
	}
	save(toolSong)
	if rec := loadSongSessions()["night_drive"]; len(rec.Served) != 0 {
		t.Fatalf("a session nobody fetched has %v as fetched", rec.Served)
	}
	s.OnAUMSessionDownloaded("Songs/Night Drive.aumproj")
	first := stagedSize()
	if rec := loadSongSessions()["night_drive"]; !slices.Equal(rec.Served, []int64{first}) {
		t.Fatalf("after a download the record keeps %v, want %d", rec.Served, first)
	}

	// A record from before sizes were kept: the next authoring finds the file
	// the iPad holds still staged.
	if err := s.updateSongSessions(func(all map[string]songSessionRecord) bool {
		rec := all["night_drive"]
		rec.Served = nil
		all["night_drive"] = rec
		return true
	}); err != nil {
		t.Fatal(err)
	}
	save(strings.Replace(toolSong, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1))
	second := stagedSize()
	if second == first {
		t.Fatal("the edited song's session is as large as the first: the test tells nothing")
	}
	rec := loadSongSessions()["night_drive"]
	if !slices.Equal(rec.Served, []int64{first}) || rec.Pulled == rec.SHA256 {
		t.Fatalf("after the next authoring the record keeps %v (pulled %v), want the size the iPad holds (%d) and a pull owed", rec.Served, rec.Pulled == rec.SHA256, first)
	}
	s.OnAUMSessionDownloaded("Songs/Night Drive.aumproj")
	if rec := loadSongSessions()["night_drive"]; !slices.Equal(rec.Served, []int64{first, second}) {
		t.Fatalf("after the second download the record keeps %v, want %v", rec.Served, []int64{first, second})
	}
	// No more than a few are kept, the latest last.
	sizes := []int64{}
	for n := int64(1); n <= servedKept+3; n++ {
		sizes = addServed(sizes, n)
	}
	if len(sizes) != servedKept || sizes[len(sizes)-1] != servedKept+3 {
		t.Errorf("kept %v, want the last %d", sizes, servedKept)
	}
}

// TestOwnCopy checks whose a copy in AUM's folder is taken to be.
func TestOwnCopy(t *testing.T) {
	rec := songSessionRecord{Session: "Songs/Night Drive", Served: []int64{98000, 107067}}
	row := func(info string) ipad.ProbeFile {
		return ipad.ProbeFile{Name: "Night Drive.aumproj", Info: info, State: ipad.ProbeConflict}
	}
	for _, c := range []struct {
		outID string
		rec   songSessionRecord
		info  string
		own   bool
		why   string
	}{
		{"Songs/Night Drive", rec, "session · 105 KB · 12. Nov 2026 at 02:06", true, ""},
		// The file fetched before the last one: a pull the app did not finish.
		{"Songs/Night Drive", rec, "session · 96 KB", true, ""},
		{"Songs/Night Drive", rec, "session · 156 KB · 12. Nov 2026 at 02:06", false, "is not the size of a file the daemon staged for it (96 KB, 105 KB): it was saved in AUM"},
		{"Songs/Night Drive", rec, "session", false, "gives no size"},
		{"Songs/Night Drive", songSessionRecord{Session: "Songs/Night Drive"}, "session · 105 KB", false, "no record of a file the iPad fetched"},
		{"Live/Night Drive", rec, "session · 105 KB", false, "only for a song's session under Songs/"},
	} {
		own, why := ownCopy(c.outID, c.rec, row(c.info))
		if own != c.own || !strings.Contains(why, c.why) {
			t.Errorf("ownCopy(%q, %v, %q) = %v, %q; want %v, %q", c.outID, c.rec.Served, c.info, own, why, c.own, c.why)
		}
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

// mustParseSource parses the source of one of the stand-in's screens.
func mustParseSource(t *testing.T, src json.RawMessage) *ipad.Element {
	t.Helper()
	root, err := ipad.ParseSource(src)
	if err != nil {
		t.Fatalf("parse the screen's source: %v", err)
	}
	return root
}

// TestLoadSongNamedTaps is a rig whose taps dial in under the names the
// session gives them, a tap on every track: the load knows its session from
// the master's tap, the notes are heard there and not at a track's tap, the
// check needs no look at AUM, and another session's tap is told from the
// song's.
func TestLoadSongNamedTaps(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	r.Session = "Songs/Night Drive"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong + "session: {track_taps: true}\n"}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("load: %s", text)
	}
	for _, want := range []string{
		`4. connect ok    brain and tap connected; the tap dialled in as "Songs/Night Drive/master"`,
		`tap     ok    ProbeAudioTap "Songs/Night Drive/master" streaming, and the taps of its 3 track(s)`,
		// The track taps connected after the master's: each carries one
		// track, and a check that listened there would call the others silent.
		"sound   ok    every track answers its first note",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("load report lacks %q:\n%s", want, text)
		}
	}
	if names := r.reg.Names(); len(names) != 4 {
		t.Errorf("taps = %q, want the master's and one per track", names)
	}

	// The check knows the session from the tap: it does not touch AUM.
	r.wda.SourceFunc = nil
	bare := ipadtest.AUM{Tempo: "300", Position: "001:1  00:00.0", Strips: nightDriveStrips}
	menu := bare
	menu.Session = "Night Drive"
	r.wda.SetScreens(bare.Source(), menu.Source(), bare.Source())
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive"})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, `aum     ok    AUM has "Songs/Night Drive" (its ProbeAudioTap dialled in under that name) open with the song's 5 strip(s)`) {
		t.Fatalf("check: %s", text)
	}
	if n := r.wda.Gestures(); n != 0 {
		t.Errorf("the check made %d gesture(s) in AUM although the tap names the session", n)
	}
	// The mixer is still looked at: a node AUM draws as failed is seen
	// without the menu.
	st := ipad.ParseAUM(mustParseSource(t, bare.Source()), ipad.Size{Width: 1210, Height: 834})
	var source ipad.Rect
	for _, n := range st.Channels[2].Nodes {
		if n.Role == "source" {
			source = n.Frame
		}
	}
	if source.Empty() {
		t.Fatalf("no source node on the pad's strip: %+v", st.Channels[2].Nodes)
	}
	r.wda.RedRings = [][4]float64{{source.X, source.Y, source.Width, source.Height}}
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "sound": false})
	if text := resultText(res); !res.IsError || r.wda.Gestures() != 0 || !strings.Contains(text, "plugins FAIL  AUM reports: AUM draws a node of") {
		t.Fatalf("check with a failed node (%d gestures): %s", r.wda.Gestures(), text)
	}
	r.wda.RedRings = nil
	// ui: false leaves AUM's screen alone; the tap still names the session.
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false, "sound": false})
	if text := resultText(res); res.IsError || !strings.Contains(text, `aum     ok    AUM has "Songs/Night Drive" open: its ProbeAudioTap dialled in under that name`) {
		t.Fatalf("check with ui false: %s", text)
	}
	// Asked for, the menu is read all the same.
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": true, "sound": false})
	if text := resultText(res); res.IsError || r.wda.Gestures() == 0 || !strings.Contains(text, `aum     ok    AUM has "Night Drive" open with the song's 5 strip(s)`) {
		t.Fatalf("check with ui true (%d gestures): %s", r.wda.Gestures(), text)
	}

	// A track's tap that is gone is named; the song still sounds.
	r.mu.Lock()
	r.trackTaps[2].Disconnect()
	r.mu.Unlock()
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "sound": false})
	if text := resultText(res); res.IsError || !strings.Contains(text, `ProbeAudioTap "Songs/Night Drive/master" streaming; no tap is connected for: pad`) {
		t.Fatalf("check with a track's tap gone: %s", text)
	}

	// Somebody loads another session the daemon authored: its master tap
	// dials in, the song's taps go away. Nothing asks AUM, nothing is played.
	r.mu.Lock()
	r.tap.Disconnect()
	for _, st := range r.trackTaps {
		st.Disconnect()
	}
	sent := len(r.Commands)
	r.mu.Unlock()
	other := r.reg.Connect("Songs/Other/master", "ipad:1")
	r.wda.SetScreens(bare.Source(), menu.Source(), bare.Source())
	res = call(t, s.handleCheckSong, map[string]any{"song": "night_drive"})
	if text := resultText(res); !res.IsError || !strings.Contains(text, `AUM has "Songs/Other" open (its ProbeAudioTap dialled in under that name), not the song's session "Songs/Night Drive"`) {
		t.Fatalf("check against another session's tap: %s", text)
	}
	r.mu.Lock()
	played := len(r.Commands) - sent
	r.mu.Unlock()
	if played != 0 || r.wda.Gestures() != 0 {
		t.Errorf("the check played %d message(s) and made %d gesture(s) for a session that is not the song's", played, r.wda.Gestures())
	}
	if got := s.loadedSessionID(); got != "Songs/Other" {
		t.Errorf("loaded session = %q, want the one the connected master tap names", got)
	}
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), `AUM has "Songs/Other" open`) {
		t.Fatalf("mix into another session: %s", resultText(res))
	}
	// With that tap gone the daemon is back to what it loaded itself.
	other.Disconnect()
	if got := s.loadedSessionID(); got != "Songs/Night Drive" {
		t.Errorf("loaded session without a tap that names one = %q", got)
	}
}

// TestPlaySongRecordsAtTheMasterTap checks a recorded playback takes the
// session's master tap in a session with a tap on every track, where the tap
// that connected last is a track's.
func TestPlaySongRecordsAtTheMasterTap(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	s.rec.quietFor, s.rec.maxTail, s.rec.poll = 150*time.Millisecond, 2*time.Second, 5*time.Millisecond
	r.Session = "Songs/Night Drive"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong + "session: {track_taps: true}\n"}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	if st, ok := r.reg.Active(); !ok || st.Name() == "Songs/Night Drive/master" {
		t.Fatalf("the most recent tap is %q, want a track's for this test", st.Name())
	}
	res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 50, "sections": 1, "record": true})
	if text := resultText(res); res.IsError || !strings.Contains(text, `from the tap "Songs/Night Drive/master"`) {
		t.Fatalf("play: %s", text)
	}
	last := lastRecording(t, s)
	if last.Error != "" || last.Meta.Tap != "Songs/Night Drive/master" {
		t.Fatalf("the recording = %+v (error %q), want it taken at the master tap", last.Meta, last.Error)
	}
}

// TestLoadSongAuthorsTapNamesIntoAnOlderSession checks a session staged
// before taps had names is authored again by the next load, although the
// song did not change.
func TestLoadSongAuthorsTapNamesIntoAnOlderSession(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError ||
		!strings.Contains(resultText(res), `taps, as get_audio_tap names them: "Songs/Night Drive/master"`) {
		t.Fatalf("author: %s", resultText(res))
	}
	if names := stagedTapNames(t, "Songs/Night Drive"); len(names) != 1 || names[4] != "Songs/Night Drive/master" {
		t.Fatalf("the staged session's taps = %v, want the master's on strip 4 under its name", names)
	}
	// The record as an earlier daemon wrote it.
	if err := s.updateSongSessions(func(all map[string]songSessionRecord) bool {
		rec := all["night_drive"]
		rec.TapNames = false
		all["night_drive"] = rec
		return true
	}); err != nil {
		t.Fatal(err)
	}
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
	text := resultText(res)
	if !strings.Contains(text, "1. author  ok    Songs/Night Drive.aumproj") {
		t.Fatalf("load of a session from before taps had names: %s", text)
	}
	// The rig's tap gives no name, like the plugin of that time: the load
	// goes through and says so.
	if !strings.Contains(text, "4. connect ok    brain and tap connected; the tap gave no name") {
		t.Errorf("load with a tap that gives no name: %s", text)
	}
	if rec := loadSongSessions()["night_drive"]; !rec.TapNames {
		t.Errorf("the record after the load = %+v, want the tap names noted", rec)
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

// padAttack is how long slowPadRig's pad takes to rise to its level.
const padAttack = 500 * time.Millisecond

// slowPadRig saves and authors the test song with its pad playing, on a rig
// whose pad (channel 2) is a slow string pad: it rises to its level (0.2,
// -14 dBFS) over padAttack and rings 300 ms after its release. While its
// strip is open it carries a floor of 0.08 (-22 dBFS) that does not die: on a
// device the pad's check note stood over something still ringing. With dead
// the pad's instrument makes no sound and the floor is all its strip carries.
func slowPadRig(t *testing.T, dead bool) (*songRig, *compiledSong) {
	t.Helper()
	r := newSongRig(t)
	s := r.s
	r.Voice = func(ch, note int) (float64, float64) {
		if ch == 2 && dead {
			return 0, 0
		}
		return 220, 0.2
	}
	r.Envelope = func(ch int) (time.Duration, time.Duration) {
		if ch == 2 {
			return padAttack, 300 * time.Millisecond
		}
		return 0, 0
	}
	r.Floor = func(ch int) float64 {
		if ch == 2 {
			return 0.08
		}
		return 0
	}
	doc := strings.Replace(toolSong, "verse: {bars: 1, play: {bass: bass, drums: beat}}", "verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}", 1)
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
	return r, cs
}

// measurePad puts the pad's preset into the sound catalogue with the attack
// it has on the rig.
func measurePad(t *testing.T, s *Server, cs *compiledSong) {
	t.Helper()
	n := songTrack(t, cs, "pad").Nodes[0]
	m := sound.Sound{Note: 57, PeakDB: -14, LevelDB: -17, LatencyMS: 20, AttackMS: float64((padAttack - 20*time.Millisecond).Milliseconds()), Sustains: true}
	if _, err := s.sounds.Put(n.Plugin, n.Name, n.HomePreset(), m); err != nil {
		t.Fatal(err)
	}
}

// songTrack is a compiled song's track by id.
func songTrack(t *testing.T, cs *compiledSong, id string) *song.TrackPlan {
	t.Helper()
	for _, tp := range cs.plan.Tracks {
		if tp.ID == id {
			return tp
		}
	}
	t.Fatalf("the song has no track %q", id)
	return nil
}

// checkRow is a track's row of a check_song result.
func checkRow(t *testing.T, res *mcp.CallToolResult, id string) trackCheck {
	t.Helper()
	rep, ok := res.StructuredContent.(checkReport)
	if !ok {
		t.Fatalf("check_song's structured content is a %T", res.StructuredContent)
	}
	for _, row := range rep.Tracks {
		if row.Track == id {
			return row
		}
	}
	t.Fatalf("check_song has no row for %q:\n%s", id, resultText(res))
	return trackCheck{}
}

// TestSoundCheckHoldsASlowAttack checks a pad whose attack the sound
// catalogue knows is held until it has risen to its level, and so heard over
// what was ringing before it. On a device a string pad with a 1.2 s attack
// was held 250 ms, peaked at -33 dBFS and was called silent twice although it
// played.
func TestSoundCheckHoldsASlowAttack(t *testing.T) {
	r, cs := slowPadRig(t, false)
	s := r.s
	measurePad(t, s, cs)
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if res.IsError || strings.Contains(text, "SILENT") || strings.Contains(text, "played again") {
		t.Fatalf("the slow pad was not heard at its first note:\n%s", text)
	}
	if !strings.Contains(text, fmt.Sprintf("dBFS, held %d ms\n", padAttack.Milliseconds())) {
		t.Errorf("the report does not say the note was held for the attack:\n%s", text)
	}
	pad := checkRow(t, res, "pad")
	if pad.Tries != 1 || pad.HoldMS != padAttack.Milliseconds() || pad.PeakDB < -14.5 {
		t.Errorf("pad = %+v, want it heard at its level (-14 dBFS or more) at the first note, held %v", pad, padAttack)
	}
	// The tracks the catalogue knows nothing of keep the short note.
	if bass := checkRow(t, res, "bass"); bass.HoldMS != songNoteHold.Milliseconds() {
		t.Errorf("the bass was held %d ms, want %v", bass.HoldMS, songNoteHold)
	}
}

// TestSoundCheckHoldsTheSecondNoteLonger checks a slow pad the catalogue does
// not know: its short first note does not stand out of the floor, and the
// second one, held longer, does.
func TestSoundCheckHoldsTheSecondNoteLonger(t *testing.T) {
	r, _ := slowPadRig(t, false)
	s := r.s
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if res.IsError || strings.Contains(text, "SILENT") {
		t.Fatalf("the slow pad was not heard:\n%s", text)
	}
	want := fmt.Sprintf("dBFS, held %d ms (silent at first, answered when played again)", songNoteHoldMax.Milliseconds())
	if !strings.Contains(text, want) {
		t.Errorf("report lacks %q:\n%s", want, text)
	}
	if pad := checkRow(t, res, "pad"); pad.Tries != 2 || pad.HoldMS != songNoteHoldMax.Milliseconds() {
		t.Errorf("pad = %+v, want it heard at its second note, held %v", pad, songNoteHoldMax)
	}
}

// TestSoundCheckCallsADeadPadSilentOverItsFloor checks the longer notes do
// not take what was ringing for an answer: a pad whose instrument is dead is
// silent over its floor, and the report gives the floor.
func TestSoundCheckCallsADeadPadSilentOverItsFloor(t *testing.T) {
	r, cs := slowPadRig(t, true)
	s := r.s
	measurePad(t, s, cs)
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if !res.IsError {
		t.Fatalf("a dead pad was heard:\n%s", text)
	}
	for _, want := range []string{
		"sound   FAIL  silent: pad (PolySynth)",
		"SILENT (peak -21.9 dBFS over a floor of -21.9 dBFS, played 2 times)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "silent: bass") || strings.Contains(text, "drums (") {
		t.Errorf("a track that plays was called silent:\n%s", text)
	}
}

// TestCheckHold checks how long a track's check note is held for what the
// sound catalogue knows of its preset.
func TestCheckHold(t *testing.T) {
	s := newSongServer(t)
	fastSongWaits(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	cs, fail := s.loadSong("night_drive")
	if fail != nil {
		t.Fatal(resultText(fail))
	}
	pad := songTrack(t, cs, "pad")
	n := pad.Nodes[0]
	for _, tc := range []struct {
		name string
		m    *sound.Sound
		want time.Duration
	}{
		{"not measured", nil, songNoteHold},
		{"a fast attack", &sound.Sound{PeakDB: -10, LatencyMS: 80, AttackMS: 5}, songNoteHold},
		{"a slow attack", &sound.Sound{PeakDB: -15, LatencyMS: 80, AttackMS: 420}, 500 * time.Millisecond},
		{"slower than the longest hold", &sound.Sound{PeakDB: -15, LatencyMS: 80, AttackMS: 3000}, songNoteHoldMax},
		{"measured silent", &sound.Sound{PeakDB: sound.SilenceDB, AttackMS: 420}, songNoteHold},
	} {
		if tc.m != nil {
			if _, err := s.sounds.Put(n.Plugin, n.Name, n.HomePreset(), *tc.m); err != nil {
				t.Fatal(err)
			}
		}
		if got := s.checkHold(pad); got != tc.want {
			t.Errorf("%s: held %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSoundCheckLetsATailDie checks a quiet pad is measured once what still
// rang in its strip has died, not against it: its strip opens onto a long
// reverb tail louder than the pad's note. On a device the tail of a string
// pad's first try was the floor of its second, and the pad was called silent.
func TestSoundCheckLetsATailDie(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	songQuietWait = 2500 * time.Millisecond // restored by the rig's cleanup
	// The pad plays at -26 dBFS and its reverb rings on, falling 60 dB in
	// 8 s. Note 40 stands for what the song left ringing in that reverb.
	r.Voice = func(ch, note int) (float64, float64) {
		switch {
		case ch == 2 && note == 40:
			return 330, 0.2
		case ch == 2:
			return 220, 0.05
		}
		return 220, 0.2
	}
	r.Envelope = func(ch int) (time.Duration, time.Duration) {
		if ch == 2 {
			return 0, 8 * time.Second
		}
		return 0, 0
	}
	doc := strings.Replace(toolSong, "verse: {bars: 1, play: {bass: bass, drums: beat}}", "verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}", 1)
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
	r.mu.Lock()
	r.notes[[2]int{2, 40}] = &rigNote{on: r.frame - rigSampleRate, off: r.frame}
	r.mu.Unlock()

	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if res.IsError || strings.Contains(text, "SILENT") || strings.Contains(text, "played again") {
		t.Fatalf("the quiet pad was not heard at its first note:\n%s", text)
	}
	if pad := checkRow(t, res, "pad"); pad.Tries != 1 || pad.FloorDB != 0 && pad.FloorDB > pad.PeakDB-6 {
		t.Errorf("pad = %+v, want it heard at its first note, 6 dB over the floor", pad)
	}
}

// TestSoundCheckSkipsControlNotes: a track whose first note in time is a
// control note (a mute on channel 10) is checked with the first note on its
// own channel, and a track that plays control notes alone is skipped, not
// called silent. On a device a Battalion whose first note was a mute was
// reported SILENT.
func TestSoundCheckSkipsControlNotes(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	r.Voice = func(ch, note int) (float64, float64) {
		if ch >= 10 { // control notes make no sound of their own
			return 0, 0
		}
		return 220, 0.2
	}
	doc := strings.NewReplacer(
		"    kind: drums\n", "    kind: drums\n    control_channels: [10]\n",
		"  - id: pad\n", "  - id: pad\n    control_channels: [11]\n",
		`beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}`,
		`beat: {drums: {kick: "....x...x...x...", snare: "........x......."}, events: [{at: 0, dur: 1, note: D2, channel: 10}]}
  reset: {channel: 11, step: 1/4, notes: "C#0 . . ."}`,
		"chorus: {bars: 1, scene: open, play: {bass: bass, pad: chords, drums: beat}}",
		"chorus: {bars: 1, scene: open, play: {bass: bass, pad: reset, drums: beat}}",
	).Replace(toolSong)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	res := call(t, s.handleCheckSong, map[string]any{"song": "night_drive", "ui": false})
	text := resultText(res)
	if res.IsError || strings.Contains(text, "SILENT") {
		t.Fatalf("check: %s", text)
	}
	for _, want := range []string{
		"drums      DrumBox                C2 ", // the kick on beat 2, not the D2 on 10
		"it plays no note on its own channel",   // the pad
	} {
		if !strings.Contains(text, want) {
			t.Errorf("check lacks %q:\n%s", want, text)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.Commands {
		if c.Type == "noteOn" && c.Channel >= 10 {
			t.Errorf("the check sent a control note: %+v", c)
		}
	}
}

// TestCheckNotes picks each track's check note from its own channel only.
func TestCheckNotes(t *testing.T) {
	plan := &song.Plan{Tracks: []*song.TrackPlan{{ID: "drums", Channel: 1, ControlChannels: []int{10}}, {ID: "keys", Channel: 2}}}
	tl := &song.Timeline{Events: []song.Event{
		{Tick: 0, Kind: song.NoteOn, Channel: 10, Data1: 36, Track: "drums"}, // a mute, earliest and lowest
		{Tick: 0, Kind: song.Control, Channel: 15, Data1: 7},
		{Tick: 10, Kind: song.NoteOn, Channel: 1, Data1: 42, Track: "drums"},
		{Tick: 10, Kind: song.NoteOn, Channel: 1, Data1: 38, Track: "drums"},
		{Tick: 20, Kind: song.NoteOn, Channel: 1, Data1: 30, Track: "drums"},
		{Tick: 5, Kind: song.NoteOn, Channel: 2, Data1: 60, Track: "keys"},
	}}
	got := checkNotes(plan, tl)
	if e := got["drums"]; e.Channel != 1 || e.Data1 != 38 || e.Tick != 10 {
		t.Errorf("drums check note = %+v, want the lowest of the first notes on channel 1 (38 at 10)", e)
	}
	if e := got["keys"]; e.Channel != 2 || e.Data1 != 60 {
		t.Errorf("keys check note = %+v, want 60 on channel 2", e)
	}
}
