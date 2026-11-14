package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
)

var craftToolNames = []string{"list_techniques", "get_technique", "patch_song"}

func TestCraftToolsAreSwitchedOnByTheConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	off := listedTools(t, connectClient(t, New(engine.New(device.NewRegistry(), fakeBLE{}))))
	on := listedTools(t, connectClient(t, New(engine.New(device.NewRegistry(), fakeBLE{}), WithCraftTools(true))))
	for _, n := range craftToolNames {
		if off[n] {
			t.Errorf("%s is registered without craft_tools", n)
		}
		if !on[n] {
			t.Errorf("%s is not registered with craft_tools", n)
		}
	}
}

// craftRig builds a server with the craft tools over a rig that has one
// limiter: its probe dump staged, its plugin card and the order of preference
// in the config dir.
func craftRig(t *testing.T) (*Server, device.ProbeDump) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dump := device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: "Lim1", Manufacturer: "Test"},
		Name:      "Test Limiter",
		Parameters: []device.ProbeParam{
			{Address: 0, Identifier: "out", DisplayName: "Output Level", Min: -30, Max: 0, UnitName: "dB", Writable: true},
			{Address: 1, Identifier: "in", DisplayName: "Input Gain", Min: 0, Max: 24, UnitName: "dB", Writable: true},
			{Address: 2, Identifier: "rel", DisplayName: "Release", Min: 1, Max: 1000, UnitName: "ms", Writable: true},
		},
	}
	b, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), device.ProbeID(dump)+".json"), b)
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
notes:
  - Its release is in ms.
`))
	writeFile(t, filepath.Join(config.CraftDir(), craft.PreferFile), []byte("dynamics.limiter: [aufx/Lim1/Test]\n"))
	return New(engine.New(device.NewRegistry(), fakeBLE{}), WithCraftTools(true)), dump
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// decodeStructured reads a result's structured content into out.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
}

func TestGetTechniqueBindsOnTheRig(t *testing.T) {
	s, dump := craftRig(t)
	res := call(t, s.handleGetTechnique, map[string]any{"id": "mastering.ceiling"})
	text := resultText(res)
	if res.IsError {
		t.Fatal(text)
	}
	for _, want := range []string{
		"mastering.ceiling (mastering, settings card for master; shipped)",
		"set dynamics.limiter: ceiling -1 dBFS (within 0.1), gain 0 dB (within 0.5), release 100 ms",
		"verify: true_peak at most -1 dBTP",
		"-> dynamics.limiter: Test Limiter (aufx/Lim1/Test, probe " + device.ProbeID(dump) + ", preferred #1), write cc",
		`ceiling -> "Output Level", mapped -6..0, a CC step 0.0472 dB`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	var out struct {
		Technique struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"technique"`
		Capabilities []struct {
			Name   string `json:"name"`
			Params []struct {
				Name string `json:"name"`
				Unit string `json:"unit"`
			} `json:"params"`
		} `json:"capabilities"`
		Bindings []struct {
			Capability string `json:"capability"`
			Plugin     string `json:"plugin"`
			Candidates []struct {
				Probe  string `json:"probe"`
				Params []struct {
					Name  string    `json:"name"`
					Param string    `json:"param"`
					Range []float64 `json:"range"`
				} `json:"params"`
			} `json:"candidates"`
		} `json:"bindings"`
	}
	decodeStructured(t, res, &out)
	if out.Technique.ID != "mastering.ceiling" || out.Technique.Kind != "settings" {
		t.Errorf("technique = %+v", out.Technique)
	}
	if len(out.Capabilities) != 1 || out.Capabilities[0].Name != "dynamics.limiter" || out.Capabilities[0].Params[0].Unit != "dBFS" {
		t.Errorf("capabilities = %+v", out.Capabilities)
	}
	if len(out.Bindings) != 1 || out.Bindings[0].Plugin != "aufx/Lim1/Test" || out.Bindings[0].Candidates[0].Probe != device.ProbeID(dump) {
		t.Fatalf("bindings = %+v", out.Bindings)
	}
	if p := out.Bindings[0].Candidates[0].Params[0]; p.Name != "ceiling" || p.Param != "Output Level" || len(p.Range) != 2 {
		t.Errorf("ceiling = %+v", p)
	}

	// A card that follows the song says what it needs to know of it.
	res = call(t, s.handleGetTechnique, map[string]any{"id": "mastering.glue"})
	text = resultText(res)
	if !strings.Contains(text, "uses from the song: beat_ms") || !strings.Contains(text, "dynamics.compressor: no plugin. no plugin card provides dynamics.compressor") {
		t.Errorf("glue:\n%s", text)
	}

	res = call(t, s.handleGetTechnique, map[string]any{"id": "mastering.ceil"})
	if !res.IsError || !strings.Contains(resultText(res), "did you mean: mastering.ceiling?") {
		t.Errorf("an unknown id: %s", resultText(res))
	}
}

func TestListTechniques(t *testing.T) {
	s, dump := craftRig(t)
	type listed struct {
		Techniques []struct {
			ID        string `json:"id"`
			CarriedBy []struct {
				Capability string `json:"capability"`
				Name       string `json:"name"`
				Probe      string `json:"probe"`
				Missing    string `json:"missing"`
			} `json:"carried_by"`
		} `json:"techniques"`
		Problems []string `json:"problems"`
	}

	var all listed
	res := call(t, s.handleListTechniques, map[string]any{"craft": "mastering"})
	decodeStructured(t, res, &all)
	if len(all.Techniques) != 6 {
		t.Errorf("%d mastering cards:\n%s", len(all.Techniques), resultText(res))
	}

	var lim listed
	res = call(t, s.handleListTechniques, map[string]any{"capability": "dynamics.limiter"})
	decodeStructured(t, res, &lim)
	if len(lim.Techniques) != 1 || lim.Techniques[0].ID != "mastering.ceiling" ||
		lim.Techniques[0].CarriedBy[0].Name != "Test Limiter" || lim.Techniques[0].CarriedBy[0].Probe != device.ProbeID(dump) {
		t.Errorf("limiter cards = %+v", lim)
	}
	if text := resultText(res); !strings.Contains(text, "mastering.ceiling [settings, master] Hold the true peak under -1 dBTP with a limiter — dynamics.limiter: Test Limiter") {
		t.Errorf("text:\n%s", text)
	}

	var glue listed
	decodeStructured(t, call(t, s.handleListTechniques, map[string]any{"query": "GLUE"}), &glue)
	if len(glue.Techniques) != 1 || glue.Techniques[0].CarriedBy[0].Missing == "" {
		t.Errorf("glue = %+v", glue)
	}

	if res := call(t, s.handleListTechniques, map[string]any{"capability": "eq.tilt"}); !res.IsError {
		t.Errorf("an unknown capability: %s", resultText(res))
	}
	if res := call(t, s.handleListTechniques, map[string]any{"kind": "writing"}); !strings.Contains(resultText(res), "no technique card matches") {
		t.Errorf("no writing cards yet: %s", resultText(res))
	}

	// A broken card of the rig is named; the others stay usable.
	writeFile(t, filepath.Join(config.CraftDir(), craft.TechniquesDir, "broken.yaml"), []byte("id: mastering.broken\ncraft: mastering\ntitle: X\napplies_to: master\nneeds: [eq.tilt]\n"))
	var broken listed
	res = call(t, s.handleListTechniques, nil)
	decodeStructured(t, res, &broken)
	if len(broken.Techniques) != 6 || len(broken.Problems) != 1 || !strings.Contains(resultText(res), "1 card file(s) did not load") {
		t.Errorf("with a broken card: %+v\n%s", broken, resultText(res))
	}
}

// TestCraftToolsThroughAClient calls the tools as a client does, through
// tools/call and its schema check.
func TestCraftToolsThroughAClient(t *testing.T) {
	s, _ := craftRig(t)
	cs := connectClient(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_techniques", Arguments: map[string]any{"craft": "mastering", "applies_to": "master"}})
	if err != nil {
		t.Fatalf("list_techniques: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_techniques: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "6 technique card(s)") {
		t.Errorf("list:\n%s", resultText(res))
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_technique", Arguments: map[string]any{"id": "mastering.subsonic_highpass"}})
	if err != nil {
		t.Fatalf("get_technique: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_technique: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "uses from the song: lowest_note_hz") {
		t.Errorf("get:\n%s", resultText(res))
	}

	stageSongPlugins(t)
	saved, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "save_song",
		Arguments: map[string]any{"yaml": toolSong},
	})
	if err != nil || saved.IsError {
		t.Fatalf("save_song via client: %v %s", err, resultText(saved))
	}
	var savedOut struct {
		Revision string `json:"revision"`
	}
	decodeStructured(t, saved, &savedOut)
	patched, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "patch_song",
		Arguments: map[string]any{
			"song":     "night_drive",
			"revision": savedOut.Revision,
			"patch": map[string]any{
				"tracks": map[string]any{
					"pad": map[string]any{
						"effects": map[string]any{
							"echo": map[string]any{"params": map[string]any{"Mix": 0.45}},
						},
					},
				},
			},
		},
	})
	if err != nil || patched.IsError {
		t.Fatalf("patch_song via client: %v %s", err, resultText(patched))
	}
}

func TestPatchSongAndStaleRevisionRefusal(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	writeFile(t, filepath.Join(config.AUv3ProbesDir(), "eq1.json"), []byte(`{
		"component": {"type": "aufx", "subtype": "Eq01", "manufacturer": "Acme"},
		"name": "CleanEQ",
		"parameters": [{"address": 0, "identifier": "freq", "displayName": "Freq", "min": 20, "max": 2000, "value": 80, "writable": true}]
	}`))

	saveRes := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong})
	if saveRes.IsError {
		t.Fatalf("save: %s", resultText(saveRes))
	}
	var saved struct {
		Revision string `json:"revision"`
	}
	decodeStructured(t, saveRes, &saved)
	rev0 := saved.Revision

	// Dry run checks the patch through save_song without changing the stored song.
	dryRes := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": rev0,
		"dry_run":  true,
		"patch": map[string]any{
			"tracks": map[string]any{
				"pad": map[string]any{
					"effects": map[string]any{
						"hp": map[string]any{"place": "first", "plugin": "eq1", "params": map[string]any{"Freq": 90}},
					},
				},
			},
		},
	})
	if dryRes.IsError {
		t.Fatalf("dry_run patch_song: %s", resultText(dryRes))
	}
	var dryOut struct {
		Saved            bool   `json:"saved"`
		PreviousRevision string `json:"previous_revision"`
		Revision         string `json:"revision"`
	}
	decodeStructured(t, dryRes, &dryOut)
	if dryOut.Saved || dryOut.PreviousRevision != rev0 || dryOut.Revision != "" {
		t.Fatalf("dry_run patch_song = %+v", dryOut)
	}

	// Apply patch: insert hp first on pad, update echo params, add fill pattern and
	// replace verse's drum play list whole.
	patchRes := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": rev0,
		"patch": map[string]any{
			"tracks": map[string]any{
				"pad": map[string]any{
					"effects": map[string]any{
						"hp":   map[string]any{"place": "first", "plugin": "eq1", "params": map[string]any{"Freq": 90}},
						"echo": map[string]any{"params": map[string]any{"Mix": 0.4}},
					},
				},
			},
			"patterns": map[string]any{
				"fill": map[string]any{"drums": map[string]any{"kick": "x.......x.......", "snare": "....x.x.xxxxXXXX"}},
			},
			"sections": map[string]any{
				"verse": map[string]any{"bars": 2, "play": map[string]any{"drums": []any{"beat", "fill"}}},
			},
		},
	})
	if patchRes.IsError {
		t.Fatalf("patch_song: %s", resultText(patchRes))
	}
	var patched struct {
		Saved            bool   `json:"saved"`
		PreviousRevision string `json:"previous_revision"`
		Revision         string `json:"revision"`
	}
	decodeStructured(t, patchRes, &patched)
	if !patched.Saved || patched.PreviousRevision != rev0 || patched.Revision == "" || patched.Revision == rev0 {
		t.Fatalf("patched = %+v", patched)
	}
	if !strings.Contains(resultText(patchRes), "PolySynth > CleanEQ > TapeDelay") {
		t.Errorf("patch_song summary lacks updated effect chain:\n%s", resultText(patchRes))
	}
	rev1 := patched.Revision

	// get_song returns the new revision and preserves author comments.
	getRes := call(t, s.handleGetSong, map[string]any{"song": "night_drive"})
	if getRes.IsError || !strings.Contains(resultText(getRes), "# a comment that must survive being stored") {
		t.Fatalf("get after patch: %s", resultText(getRes))
	}
	var got struct {
		Revision string `json:"revision"`
	}
	decodeStructured(t, getRes, &got)
	if got.Revision != rev1 {
		t.Fatalf("get_song revision = %q, want %q", got.Revision, rev1)
	}

	// A patch made against the older revision rev0 is refused and names rev1.
	stale := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": rev0,
		"patch":    map[string]any{"tempo": 140},
	})
	if !stale.IsError || !strings.Contains(resultText(stale), "at revision "+rev1+", not "+rev0) {
		t.Fatalf("stale patch was not refused with revisions: %s", resultText(stale))
	}

	// save_song's plugin/parameter validation applies to patch_song too: an
	// out-of-range parameter is refused without changing the stored song.
	badParam := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": rev1,
		"patch": map[string]any{
			"tracks": map[string]any{
				"pad": map[string]any{
					"effects": map[string]any{
						"hp": map[string]any{"params": map[string]any{"Freq": 99999}},
					},
				},
			},
		},
	})
	if !badParam.IsError || !strings.Contains(resultText(badParam), "20..2000") {
		t.Fatalf("out-of-range patched param was not refused by save_song check: %s", resultText(badParam))
	}
	_, rawAfter, err := s.songs.Load("night_drive")
	if err != nil || song.Revision(rawAfter) != rev1 {
		t.Fatalf("failed patch altered stored song: err=%v rev=%s want=%s", err, song.Revision(rawAfter), rev1)
	}
}

func TestRunCraftToolAnswerShapeAndOneLockApply(t *testing.T) {
	s := newSongServer(t, WithCraftTools(true))
	saveRes := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong})
	if saveRes.IsError {
		t.Fatalf("save: %s", resultText(saveRes))
	}
	var saved struct {
		Revision string `json:"revision"`
	}
	decodeStructured(t, saveRes, &saved)
	rev0 := saved.Revision

	// apply: false returns the 7-field craft.Answer against rev0 without storing,
	// and its patch applies cleanly through patch_song.
	res, err := s.RunCraftTool("mixing", "night_drive", false, func(_ string, _ *song.Song, _ []byte) (CraftPlan, error) {
		return CraftPlan{
			Techniques: []string{"mixing.echo_trim"},
			Patch: map[string]any{
				"tracks": map[string]any{
					"pad": map[string]any{
						"effects": map[string]any{
							"echo": map[string]any{"params": map[string]any{"Mix": 0.25}},
						},
					},
				},
			},
			Why:    []string{"pull pad delay back so chords stay clear"},
			Checks: []string{"pad wet level checked"},
		}, nil
	})
	if err != nil || res.IsError {
		t.Fatalf("RunCraftTool apply=false: %v %s", err, resultText(res))
	}
	var rawMap map[string]any
	decodeStructured(t, res, &rawMap)
	keys := make([]string, 0, len(rawMap))
	for k := range rawMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := []string{"applied", "checks", "craft", "patch", "revision", "techniques", "why"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("craft answer keys = %v, want %v", keys, wantKeys)
	}
	var ans craft.Answer
	decodeStructured(t, res, &ans)
	if ans.Applied || ans.Craft != "mixing" || ans.Revision != rev0 {
		t.Fatalf("apply=false answer = %+v, want applied=false revision=%s", ans, rev0)
	}
	if _, diskRaw, _ := s.songs.Load("night_drive"); song.Revision(diskRaw) != rev0 {
		t.Fatal("apply=false modified the stored song")
	}

	// Handing the returned patch + revision to patch_song stores it.
	appliedViaTool := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": ans.Revision,
		"patch":    ans.Patch,
	})
	if appliedViaTool.IsError {
		t.Fatalf("patch_song from craft answer: %s", resultText(appliedViaTool))
	}
	var patchedOut struct {
		Revision string `json:"revision"`
	}
	decodeStructured(t, appliedViaTool, &patchedOut)
	rev1 := patchedOut.Revision

	// apply: true holds one lock across read, patch and store: a concurrent
	// patch_song against rev1 launched while build is running must wait for the
	// lock and then fail as stale against the newly stored rev2.
	inBuild := make(chan struct{})
	releaseBuild := make(chan struct{})
	patchDone := make(chan *mcp.CallToolResult, 1)

	var applyRes *mcp.CallToolResult
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var rerr error
		applyRes, rerr = s.RunCraftTool("drums", "night_drive", true, func(_ string, _ *song.Song, _ []byte) (CraftPlan, error) {
			close(inBuild)
			<-releaseBuild
			return CraftPlan{
				Techniques: []string{"drums.fill_into_section"},
				Patch: map[string]any{
					"patterns": map[string]any{
						"fill": map[string]any{"drums": map[string]any{"kick": "x.......x.......", "snare": "....x.x.xxxxXXXX"}},
					},
					"sections": map[string]any{
						"verse": map[string]any{"play": map[string]any{"drums": []string{"beat"}}},
					},
				},
				Why: []string{"add fill vocabulary"},
			}, nil
		})
		if rerr != nil {
			t.Errorf("RunCraftTool apply=true: %v", rerr)
		}
	}()

	<-inBuild
	go func() {
		patchDone <- call(t, s.handlePatchSong, map[string]any{
			"song":     "night_drive",
			"revision": rev1,
			"patch":    map[string]any{"tempo": 240},
		})
	}()

	// Give the concurrent patch_song goroutine a moment to contend on s.songsMu;
	// it must not complete while RunCraftTool is still inside build.
	select {
	case early := <-patchDone:
		t.Fatalf("concurrent patch_song completed while apply=true held lock: %s", resultText(early))
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseBuild)
	wg.Wait()

	if applyRes == nil || applyRes.IsError {
		t.Fatalf("RunCraftTool apply=true failed: %v", resultText(applyRes))
	}
	var appliedAns craft.Answer
	decodeStructured(t, applyRes, &appliedAns)
	// The applied answer names the revision its patch was made against (ADR
	// 0002), rev1; its text names the stored revision too.
	if !appliedAns.Applied || appliedAns.Revision != rev1 {
		t.Fatalf("apply=true answer = %+v, want applied=true revision=%s (the patch's base)", appliedAns, rev1)
	}
	_, storedRaw, err := s.songs.Load("night_drive")
	if err != nil {
		t.Fatalf("load after apply: %v", err)
	}
	rev2 := song.Revision(storedRaw)
	if rev2 == rev1 {
		t.Fatalf("apply=true did not store a new revision (still %s)", rev1)
	}
	applyText := resultText(applyRes)
	for _, want := range []string{
		"drums: applied, made against revision " + rev1,
		"revision: " + rev2 + " (from " + rev1 + ")",
	} {
		if !strings.Contains(applyText, want) {
			t.Errorf("apply=true text lacks %q:\n%s", want, applyText)
		}
	}

	concurrentRes := <-patchDone
	if !concurrentRes.IsError || !strings.Contains(resultText(concurrentRes), "at revision "+rev2+", not "+rev1) {
		t.Fatalf("concurrent patch_song against rev1 = %s, want stale-revision refusal naming %s",
			resultText(concurrentRes), rev2)
	}

	// Handed to patch_song, the applied answer's patch and revision are refused
	// as stale: the patch is not applied a second time.
	again := call(t, s.handlePatchSong, map[string]any{
		"song":     "night_drive",
		"revision": appliedAns.Revision,
		"patch":    appliedAns.Patch,
	})
	if !again.IsError || !strings.Contains(resultText(again), "at revision "+rev2+", not "+rev1) {
		t.Fatalf("patch_song with the applied answer = %s, want stale-revision refusal naming %s", resultText(again), rev2)
	}
	if _, afterRaw, _ := s.songs.Load("night_drive"); song.Revision(afterRaw) != rev2 {
		t.Fatal("refused patch_song altered the stored song")
	}
}
