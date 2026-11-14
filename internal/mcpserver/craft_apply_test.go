package mcpserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
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
