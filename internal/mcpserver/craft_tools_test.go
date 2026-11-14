package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
)

var craftToolNames = []string{"list_techniques", "get_technique"}

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
	writeFile(t, filepath.Join(config.CraftDir(), craft.TechniquesDir, "broken.yaml"), []byte("id: mastering.broken\ncraft: mastering\ntitle: X\ntargets: master\nneeds: [eq.tilt]\n"))
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
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_techniques", Arguments: map[string]any{"craft": "mastering", "target": "master"}})
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
}
