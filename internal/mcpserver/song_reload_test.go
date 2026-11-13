package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// nightDriveEchoStrips is nightDriveStrips with the pad's echo in its first
// insert slot.
var nightDriveEchoStrips = []ipadtest.Strip{
	{Title: "Brain"}, {Title: "bass", Level: "+0.0 dB"},
	{Title: "pad", Level: "+0.0 dB", Inserts: []ipadtest.Node{{Label: "DLY"}}},
	{Title: "drums", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"},
}

// loadedMixer is the stand-in AUM's mixer once it has loaded a session of
// these strips (ipadtest.OpenSessionScreens).
func loadedMixer(strips []ipadtest.Strip) ipadtest.AUM {
	return ipadtest.AUM{Tempo: "104", Position: "001:1  00:00.0", Strips: strips}
}

// nodeFrame is where the stand-in AUM draws a node of the strip titled title:
// its source, or an insert slot.
func nodeFrame(t *testing.T, strips []ipadtest.Strip, title, role string, slot int) [4]float64 {
	t.Helper()
	root, err := ipad.ParseSource(loadedMixer(strips).Source())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := ipad.ParseAUM(root, ipad.Size{Width: 1210, Height: 834}).Channel(title)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ch.Nodes {
		if n.Role == role && n.Slot == slot && !n.Empty {
			return [4]float64{n.Frame.X, n.Frame.Y, n.Frame.Width, n.Frame.Height}
		}
	}
	t.Fatalf("strip %q has no %s node in slot %d: %+v", title, role, slot, ch.Nodes)
	return [4]float64{}
}

// failingLoad has the rig's AUM load Night Drive with the pad's echo drawn
// failed until it was reloaded reloads times (-1: never).
func failingLoad(t *testing.T, r *songRig, reloads int) (*ipadtest.FailedNodes, [4]float64) {
	t.Helper()
	if res := call(t, r.s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveEchoStrips)
	failed := ipadtest.NewFailedNodes(r.wda, loadedMixer(nightDriveEchoStrips))
	echo := nodeFrame(t, nightDriveEchoStrips, "pad", "insert", 1)
	failed.Fail(echo, reloads)
	return failed, echo
}

// TestLoadSongReloadsAFailedNode checks a plugin AUM draws as failed after the
// load is reloaded in its node, and the mixer read again: one that comes up
// leaves nothing for the check to find.
func TestLoadSongReloadsAFailedNode(t *testing.T) {
	r := newSongRig(t)
	failed, echo := failingLoad(t, r, 1)
	res := call(t, r.s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("load: %s", text)
	}
	for _, want := range []string{
		`3. load    ok    AUM has "Night Drive" open: 5 strip(s); on screen: AUM draws a node of "pad" as failed`,
		"4. reload  ok    AUM drew pad's ",
		"(insert slot 1) as failed; reloaded once, it came up",
		"5. connect ok",
		`aum     ok    AUM has "Night Drive" open with the song's 5 strip(s)`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("load report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "plugins FAIL") {
		t.Errorf("the check found the reloaded plugin failed:\n%s", text)
	}
	if n := failed.Reloads(echo); n != 1 {
		t.Errorf("the echo was reloaded %d times, want once", n)
	}
}

// TestLoadSongNodeStaysFailed checks a node that does not come up is
// reloaded songReloadRounds times, and that the load then says so and what to
// do.
func TestLoadSongNodeStaysFailed(t *testing.T) {
	r := newSongRig(t)
	failed, echo := failingLoad(t, r, -1)
	res := call(t, r.s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true})
	text := resultText(res)
	if !res.IsError {
		t.Fatalf("a load with a failed plugin is ready to play:\n%s", text)
	}
	for _, want := range []string{
		"4. reload  FAIL  AUM drew pad's ",
		"reloaded 3 times, and pad's ",
		"(insert slot 1) is still drawn failed",
		`plugins FAIL  AUM reports: AUM draws a node of "pad" as failed`,
		"next: a plugin that stays failed when it is reloaded may come up in a new load of the session: load_song again",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("load report lacks %q:\n%s", want, text)
		}
	}
	if n := failed.Reloads(echo); n != songReloadRounds {
		t.Errorf("the echo was reloaded %d times, want %d", n, songReloadRounds)
	}
}

// TestLoadSongKeepsAFailedNode checks reload: false leaves a failed plugin as
// it is, and that the check then says how to reload it.
func TestLoadSongKeepsAFailedNode(t *testing.T) {
	r := newSongRig(t)
	failed, echo := failingLoad(t, r, 1)
	res := call(t, r.s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "reload": false})
	text := resultText(res)
	if !res.IsError || strings.Contains(text, "reload  ") {
		t.Fatalf("a load with reload: false:\n%s", text)
	}
	if want := `aum_node {action: "reload", channel: "pad", node: "1"} reloads it once`; !strings.Contains(text, want) {
		t.Errorf("next lacks %q:\n%s", want, text)
	}
	if n := failed.Reloads(echo); n != 0 {
		t.Errorf("the echo was reloaded %d times, want none", n)
	}
}

// recordBand records the stem song's band stem and returns its report.
func recordBand(t *testing.T, r *songRig) (*stemsReport, string) {
	t.Helper()
	res := call(t, r.s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1, "stems": []string{"band"}})
	if res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		res = call(t, r.s.handleGetSongStems, map[string]any{"wait_ms": 2000})
		out, _ := res.StructuredContent.(map[string]any)
		if out["done"] == true {
			rep, _ := out["report"].(*stemsReport)
			if rep == nil {
				t.Fatalf("the stem recording has no report: %s", resultText(res))
			}
			return rep, resultText(res)
		}
		if res.IsError || time.Now().After(deadline) {
			t.Fatalf("the recording did not end: %s", resultText(res))
		}
	}
}

// TestRecordSongStemsReloadsAFailedPlugin checks a stem whose plugin AUM drew
// as failed is recorded once the load's reload brought it up, and the take
// says so.
func TestRecordSongStemsReloadsAFailedPlugin(t *testing.T) {
	r, _ := stemsRig(t)
	r.opens("Stem Song stem band", stemBandStrips)
	failed := ipadtest.NewFailedNodes(r.wda, loadedMixer(stemBandStrips))
	pad := nodeFrame(t, stemBandStrips, "pad", "source", 0)
	failed.Fail(pad, 1)

	rep, text := recordBand(t, r)
	band := rep.Stems[1]
	if band.Stem != "band" || band.File == "" || band.Error != "" {
		t.Fatalf("the band stem was not recorded: %+v\n%s", band, text)
	}
	noted := false
	for _, n := range band.Notes {
		noted = noted || (strings.HasPrefix(n, "the load reloaded a plugin: AUM drew pad's ") && strings.HasSuffix(n, "(source) as failed; reloaded once, it came up"))
	}
	if !noted {
		t.Errorf("the take does not say the pad was reloaded: %q", band.Notes)
	}
	if n := failed.Reloads(pad); n != 1 {
		t.Errorf("the pad was reloaded %d times, want once", n)
	}
}

// TestRecordSongStemsSkipsAFailedPlugin checks a stem whose plugin stays
// failed when it is reloaded is not recorded: no recorder, no arm, and the
// report names the plugin.
func TestRecordSongStemsSkipsAFailedPlugin(t *testing.T) {
	r, _ := stemsRig(t)
	r.opens("Stem Song stem band", stemBandStrips)
	failed := ipadtest.NewFailedNodes(r.wda, loadedMixer(stemBandStrips))
	pad := nodeFrame(t, stemBandStrips, "pad", "source", 0)
	failed.Fail(pad, -1)

	rep, text := recordBand(t, r)
	band := rep.Stems[1]
	if band.File != "" || !strings.Contains(band.Error, "the stem is not recorded over a plugin that failed: AUM drew pad's ") ||
		!strings.Contains(band.Error, "reloaded 3 times") || !strings.Contains(band.Error, "record_song_stems again loads its session anew") {
		t.Fatalf("a stem with a failed plugin: %+v\n%s", band, text)
	}
	if !strings.Contains(text, "the recording ended at the stem band") {
		t.Errorf("the report does not say where the recording ended:\n%s", text)
	}
	r.mu.Lock()
	sent := globalCCs(r.Commands, rigRecordCC, rigArmCC)
	takes := len(r.Takes)
	r.mu.Unlock()
	if len(sent) != 0 || takes != 0 {
		t.Errorf("the recorder or the master's arm was sent %v, and %d take(s) made: nothing is recorded over a failed plugin", sent, takes)
	}
	if n := failed.Reloads(pad); n != songReloadRounds {
		t.Errorf("the pad was reloaded %d times, want %d", n, songReloadRounds)
	}
}

// A successful RELOAD is not proof of recovery when the next visual check
// fails. Keep the observed failure so neither the check nor recording can
// treat the plugin as healthy.
func TestRecordSongStemsBlocksUnverifiedReload(t *testing.T) {
	r, _ := stemsRig(t)
	r.opens("Stem Song stem band", stemBandStrips)
	failed := ipadtest.NewFailedNodes(r.wda, loadedMixer(stemBandStrips))
	pad := nodeFrame(t, stemBandStrips, "pad", "source", 0)
	failed.Fail(pad, 1)
	before := r.wda.OnGesture
	r.wda.OnGesture = func(g ipadtest.Gesture) {
		before(g)
		if failed.Reloads(pad) > 0 {
			r.wda.FailScreenshot = true
		}
	}

	rep, text := recordBand(t, r)
	band := rep.Stems[1]
	if band.File != "" || !strings.Contains(band.Error, "the stem is not recorded over a plugin that failed") ||
		!strings.Contains(band.Error, "mixer could not be read after that") {
		t.Fatalf("unverified recovery allowed recording: %+v\n%s", band, text)
	}
	if strings.Contains(text, "it came up") || strings.Contains(text, "they came up") || len(band.Notes) != 0 {
		t.Errorf("unverified recovery claimed success: %+v\n%s", band, text)
	}
	r.mu.Lock()
	sent := globalCCs(r.Commands, rigRecordCC, rigArmCC)
	takes := len(r.Takes)
	r.mu.Unlock()
	if len(sent) != 0 || takes != 0 {
		t.Errorf("unverified recovery sent recorder/arm commands %v and made %d takes", sent, takes)
	}
	if n := failed.Reloads(pad); n != 1 {
		t.Errorf("reloads = %d, want one before the screenshot failure", n)
	}
}
