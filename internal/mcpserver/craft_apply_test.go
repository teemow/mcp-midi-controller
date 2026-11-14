package mcpserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

// stageHighPass stages, under its probe id as import_auv3_probe does, a high-pass whose frequency runs from 20 Hz to 2 kHz,
// with a plugin card that maps eq.highpass onto it over 20..200 Hz and holds
// its slope at 24 dB/oct.
func stageHighPass(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), "aufx-_eq01-_acme.json"), []byte(`{
		"component": {"type": "aufx", "subtype": "Eq01", "manufacturer": "Acme"},
		"name": "CleanEQ",
		"parameters": [{"address": 0, "identifier": "freq", "displayName": "Freq", "min": 20, "max": 2000, "value": 80, "unitName": "Hz", "writable": true}]
	}`))
	writeFile(t, filepath.Join(config.CraftDir(), craft.PluginsDir, "clean-eq.yaml"), []byte(`
plugin: aufx/Eq01/Acme
name: CleanEQ
capabilities:
  eq.highpass:
    write: cc
    params:
      freq: {param: Freq, range: [20, 200]}
      slope: {fixed: 24}
`))
}

func TestApplyTechnique(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	stageHighPass(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}

	// Checked only: the patch and why, nothing stored. The song's lowest
	// pitched note is G1, 49 Hz (the drums are not pitched), so the
	// subsonic high-pass sits at 24.5 Hz.
	res := call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.subsonic_highpass"})
	if res.IsError {
		t.Fatalf("apply_technique: %s", resultText(res))
	}
	var ans craft.Answer
	decodeStructured(t, res, &ans)
	if ans.Craft != "mastering" || ans.Applied || len(ans.Techniques) != 1 || ans.Revision == "" {
		t.Fatalf("answer = %+v", ans)
	}
	hp, _ := ans.Patch["master"].(map[string]any)["subsonic_highpass"].(map[string]any)
	if hp["plugin"] != "aufx-_eq01-_acme" || hp["place"] != "first" || hp["params"].(map[string]any)["Freq"] != 24.4997 {
		t.Errorf("patch = %v", ans.Patch)
	}
	text := resultText(res)
	for _, want := range []string{"adds master subsonic_highpass: eq.highpass with CleanEQ (aufx-_eq01-_acme), placed first", `"Freq" = 24.4997 Hz`, "precision: eq.highpass freq within 20%", "verify: band_level 10..20 Hz falls by 6 dB or more (not run"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
	if res := call(t, s.handleGetSong, map[string]any{"song": "night_drive"}); strings.Contains(resultText(res), "subsonic_highpass") {
		t.Error("a check stored the patch")
	}

	// Applied: stored through save_song.
	res = call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.subsonic_highpass", "apply": true})
	if res.IsError {
		t.Fatalf("apply_technique apply: %s", resultText(res))
	}
	decodeStructured(t, res, &ans)
	if !ans.Applied {
		t.Errorf("answer = %+v", ans)
	}
	if got := resultText(call(t, s.handleGetSong, map[string]any{"song": "night_drive"})); !strings.Contains(got, "id: subsonic_highpass") || !strings.Contains(got, "Freq: 24.4997") {
		t.Errorf("the stored song lacks the high-pass:\n%s", got)
	}

	// A card the rig has no plugin for changes nothing and says why.
	res = call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.glue", "apply": true})
	if !res.IsError || !strings.Contains(resultText(res), "no plugin card provides dynamics.compressor") {
		t.Errorf("glue: %s", resultText(res))
	}
	// A master card on a track, and a track that is not there.
	res = call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.subsonic_highpass", "track": "pad"})
	if !res.IsError || !strings.Contains(resultText(res), "applies to master, not to a track") {
		t.Errorf("a master card on a track: %s", resultText(res))
	}
	res = call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.subsonic_highpass", "track": "lead"})
	if !res.IsError || !strings.Contains(resultText(res), `the song has no track "lead"`) {
		t.Errorf("a missing track: %s", resultText(res))
	}
}

// stageLabRig stages a noise source and Apple's high-pass under their probe
// ids, as the lab plays one through the other.
func stageLabRig(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), "aumu-_nois-_test.json"), []byte(`{
		"component": {"type": "aumu", "subtype": "Nois", "manufacturer": "Test"},
		"name": "Noise",
		"parameters": [{"address": 0, "identifier": "lvl", "displayName": "Level", "min": 0, "max": 1, "value": 0.5, "writable": true}]
	}`))
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), "aufx-hpas-appl.json"), []byte(`{
		"component": {"type": "aufx", "subtype": "hpas", "manufacturer": "appl"},
		"name": "AUHighPassFilter",
		"parameters": [
			{"address": 0, "identifier": "0", "displayName": "Cutoff Frequency", "min": 10, "max": 22050, "value": 6900, "unitName": "Hz", "writable": true, "displayLogarithmic": true},
			{"address": 1, "identifier": "1", "displayName": "Resonance", "min": -20, "max": 40, "value": 0, "unitName": "dB", "writable": true}
		]
	}`))
}

func TestProfilePluginProposesAndBuildsItsLab(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	stageLabRig(t)
	res := call(t, s.handleProfilePlugin, map[string]any{"plugin": "AUHighPassFilter", "dry_run": true})
	if res.IsError || !strings.Contains(resultText(res), "eq.highpass") || !strings.Contains(resultText(res), "Cutoff Frequency") {
		t.Fatalf("dry run: %s", resultText(res))
	}
	// Not a capability it was matched to.
	res = call(t, s.handleProfilePlugin, map[string]any{"plugin": "AUHighPassFilter", "capabilities": []string{"dynamics.limiter"}, "dry_run": true})
	if !res.IsError || !strings.Contains(resultText(res), "dynamics.limiter is not among the capabilities matched") {
		t.Errorf("a capability not matched: %s", resultText(res))
	}

	// The lab: a section per value of the sweep, and a song that plans.
	cat := songCatalog()
	lib, _ := loadCraft()
	card, err := profileCard(cat["aufx-hpas-appl"], profileArgs{}, lib.Vocab)
	if err != nil {
		t.Fatal(err)
	}
	lab, sweeps, err := labSong(cat["aufx-hpas-appl"], "aufx-hpas-appl", card, lib.Vocab, "aumu-_nois-_test", map[string]any{"Level": 0.8})
	if err != nil {
		t.Fatal(err)
	}
	if len(sweeps) != 1 || len(sweeps[0].Values) != 3 {
		t.Fatalf("sweeps = %+v", sweeps)
	}
	sg, err := song.Parse(lab)
	if err != nil {
		t.Fatalf("parse the lab:\n%s\n%v", lab, err)
	}
	if _, err := song.NewPlan(sg, cat); err != nil {
		t.Fatalf("plan the lab:\n%s\n%v", lab, err)
	}
	if len(sg.Arrangement) != 3 || sg.Scenes["eq_highpass_2"]["source"].Effects["dut"].Params["Cutoff Frequency"].Num != 160 {
		t.Errorf("the lab:\n%s", lab)
	}
	// Sweeping needs consent.
	res = call(t, s.handleProfilePlugin, map[string]any{"plugin": "AUHighPassFilter", "source": map[string]any{"plugin": "aumu-_nois-_test"}})
	if !res.IsError {
		t.Errorf("a sweep without a brain or consent started: %s", resultText(res))
	}
}

func TestGetCraftJobWithoutAJob(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	if res := call(t, s.handleGetCraftJob, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "no craft job has run") {
		t.Errorf("get_craft_job: %s", resultText(res))
	}
}

func TestVerifyNeedsApplyAndAMeasurement(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	stageHighPass(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	res := call(t, s.handleApplyTechnique, map[string]any{"song": "night_drive", "technique": "mastering.subsonic_highpass", "verify": true})
	if !res.IsError || !strings.Contains(resultText(res), "verify: needs apply: true") {
		t.Errorf("verify without apply: %s", resultText(res))
	}
}

// stageLimiter stages a limiter under its probe id, with a plugin card that
// maps dynamics.limiter onto it.
func stageLimiter(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), "aufx-_lim1-_test.json"), []byte(`{
		"component": {"type": "aufx", "subtype": "Lim1", "manufacturer": "Test"},
		"name": "Test Limiter",
		"parameters": [
			{"address": 0, "identifier": "out", "displayName": "Output Level", "min": -30, "max": 0, "value": 0, "unitName": "dB", "writable": true},
			{"address": 1, "identifier": "in", "displayName": "Input Gain", "min": 0, "max": 24, "value": 0, "unitName": "dB", "writable": true},
			{"address": 2, "identifier": "rel", "displayName": "Release", "min": 1, "max": 1000, "value": 100, "unitName": "ms", "writable": true}
		]
	}`))
	writeFile(t, filepath.Join(config.CraftDir(), craft.PluginsDir, "test-limiter.yaml"), []byte(`
plugin: aufx/Lim1/Test
name: Test Limiter
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Output Level, range: [-6, 0]}
      gain: Input Gain
      release: Release
`))
}

// TestApplyTechniqueVerify runs a verify on the stand-in rig: a pass before,
// the change stored and the session loaded again, a pass after, and the
// card's measurement judged. The rig's voices are sines well under full
// scale (the chorus's three tracks sum to about -8 dBTP), so the ceiling's
// true peak at most -1 dBTP is met before and after.
func TestApplyTechniqueVerify(t *testing.T) {
	r := mixRig(t)
	s := r.s
	stageLimiter(t)
	r.connect()
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	if res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true}); res.IsError {
		t.Fatalf("load: %s", resultText(res))
	}
	args := map[string]any{"song": "night_drive", "technique": "mastering.ceiling", "apply": true, "verify": true, "bars": 1, "section": "chorus"}
	if res := call(t, s.handleApplyTechnique, args); !res.IsError || !strings.Contains(resultText(res), "needs confirm: true") {
		t.Fatalf("verify without confirm: %s", resultText(res))
	}
	args["confirm"] = true
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	res := call(t, s.handleApplyTechnique, args)
	if res.IsError || !strings.Contains(resultText(res), "verifying mastering.ceiling") {
		t.Fatalf("verify: %s", resultText(res))
	}
	res = call(t, s.handleGetCraftJob, map[string]any{"wait_ms": 50000})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("job: %s", text)
	}
	for _, want := range []string{"verify mastering.ceiling on the master", "true_peak met", "the card asks at most -1 dBTP", "mastering: applied, made against revision"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	s.craftMu.Lock()
	rep, _ := s.craftJob.report.(*verifyReport)
	s.craftMu.Unlock()
	if rep == nil || !rep.Met || rep.Before > -3 || rep.Before < -20 || !rep.Answer.Applied || strings.Contains(strings.Join(rep.Answer.Checks, "\n"), "not run") {
		t.Errorf("report = %+v", rep)
	}
	if got := resultText(call(t, s.handleGetSong, map[string]any{"song": "night_drive"})); !strings.Contains(got, "id: ceiling") {
		t.Errorf("the stored song lacks the limiter:\n%s", got)
	}
}

// TestACraftJobHoldsTheRig: while a craft job plays, nothing else loads a
// session or plays, as while a song's stems are recorded.
func TestACraftJobHoldsTheRig(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	job := &craftJob{tool: "profile_plugin", song: pluginLab, phase: "eq.highpass: freq at 160 (2 of 3)", done: make(chan struct{}), cancel: func() {}}
	s.craftMu.Lock()
	s.craftJob = job
	s.craftMu.Unlock()
	for name, h := range map[string]func() string{
		"play_song": func() string { return resultText(call(t, s.handlePlaySong, map[string]any{"song": "night_drive"})) },
		"load_song": func() string {
			return resultText(call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true}))
		},
		"check_song": func() string { return resultText(call(t, s.handleCheckSong, map[string]any{"song": "night_drive"})) },
		"mix_song":   func() string { return resultText(call(t, s.handleMixSong, map[string]any{"song": "night_drive"})) },
		"mix_song_stems": func() string {
			return resultText(call(t, s.handleMixSongStems, map[string]any{"song": "night_drive", "confirm": true}))
		},
		"open_sound_lab": func() string {
			return resultText(call(t, s.handleOpenSoundLab, map[string]any{"plugins": []string{"syn1"}, "confirm": true}))
		},
	} {
		if text := h(); !strings.Contains(text, name+": profile_plugin is running") {
			t.Errorf("%s while a craft job runs: %s", name, text)
		}
	}
	// Once it has ended, they run again.
	close(job.done)
	if text := resultText(call(t, s.handlePlaySong, map[string]any{"song": "night_drive"})); strings.Contains(text, "profile_plugin is running") {
		t.Errorf("play_song after the craft job: %s", text)
	}
}
