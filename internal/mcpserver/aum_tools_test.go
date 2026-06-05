package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// stageProbe writes a minimal AUv3 probe dump into the staging dir so the
// author/import tools can source/match a node from it.
func stageProbe(t *testing.T) {
	t.Helper()
	dir := config.AUv3ProbesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir probes: %v", err)
	}
	dump := `{
		"component": {"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme", "manufacturerName": "Acme"},
		"name": "GuitarSynth",
		"parameters": [
			{"address": 0, "identifier": "cutoff", "displayName": "Cutoff", "min": 0, "max": 1, "value": 0.5, "writable": true, "readable": true}
		]
	}`
	if err := os.WriteFile(filepath.Join(dir, "gtr1.json"), []byte(dump), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
}

func newAUMServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return New(engine.New(device.NewRegistry(), fakeBLE{}))
}

func TestAUMToolsAuthorListGetDiff(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author a 2-channel session (Gitarre hosting the probe + a Master),
	// pre-wired to the convention.
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Band Test",
		"channels": []any{
			map[string]any{
				"kind": "audio", "title": "Gitarre",
				"nodes": []any{map[string]any{"probe_id": "gtr1"}},
			},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 1, "start_cc": 30},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "CC(s) assigned") {
		t.Fatalf("author output missing assignment summary:\n%s", resultText(res))
	}

	staged := filepath.Join(config.AUMSessionsDir(), "band_test.aumproj")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("authored session not staged: %v", err)
	}

	// list shows it.
	res = call(t, s.handleListAUMSessions, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), "band_test") {
		t.Fatalf("list missing band_test:\n%s", resultText(res))
	}

	// get returns the SessionMap (2 channels, the hosted node).
	res = call(t, s.handleGetAUMSession, map[string]any{"session_id": "band_test"})
	if res.IsError {
		t.Fatalf("get failed: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "Gitarre") || !strings.Contains(text, "aumu/gtr1/Acme") {
		t.Fatalf("get output missing channel/node:\n%s", text)
	}

	// diff: convention pre-wired, so the single non-master audio strip should be
	// fully wired (Volume/Mute/Solo/Rec at their convention CCs).
	res = call(t, s.handleDiffAUMSession, map[string]any{"session_id": "band_test"})
	if res.IsError {
		t.Fatalf("diff failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "fully wired to the convention") {
		t.Fatalf("diff verdict not fully wired:\n%s", resultText(res))
	}
}

func TestAUMDiffUnwiredSession(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author WITHOUT a convention (bare): every leaf stays a placeholder.
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "blank",
		"bare":   true,
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Ch1"},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}
	res = call(t, s.handleDiffAUMSession, map[string]any{"session_id": "blank"})
	if res.IsError {
		t.Fatalf("diff failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "not wired to the convention yet") {
		t.Fatalf("expected unwired verdict:\n%s", resultText(res))
	}
}

func TestAUMEditAndExport(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "edit_me",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Ch1"},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	// Edit: assign Volume to CC 7 on the first channel's controls, set a fader.
	res := call(t, s.handleEditAUMSession, map[string]any{
		"session_id": "edit_me",
		"out_id":     "edited",
		"mappings": []any{
			map[string]any{"collection": "Channels/chan0/Channel controls", "target": "Volume", "type": 0, "data1": 7, "channel": 1},
		},
		"faders": []any{map[string]any{"channel": 0, "level": 0.5}},
	})
	if res.IsError {
		t.Fatalf("edit failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUMSessionsDir(), "edited.aumproj")); err != nil {
		t.Fatalf("edited session not staged: %v", err)
	}

	// Export the collection we just assigned into.
	res = call(t, s.handleExportAUMMidiMap, map[string]any{
		"session_id": "edited",
		"collection": "Channels/chan0/Channel controls",
		"out_id":     "vol_map",
	})
	if res.IsError {
		t.Fatalf("export failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUMSessionsDir(), "vol_map.aum_midimap")); err != nil {
		t.Fatalf("exported midimap not staged: %v", err)
	}
}

func TestAUMImportProposesBindings(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "rig",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "rig"})
	if res.IsError {
		t.Fatalf("import failed: %s", resultText(res))
	}
	text := resultText(res)
	// The hosted node matched the staged probe and proposed a binding whose
	// device id derives from the probe; the channel was inferred from the
	// convention (authored send ch 3 → stored 0-based 2 → suggested send ch 3).
	if !strings.Contains(text, "probe=gtr1") {
		t.Fatalf("import did not match the probe:\n%s", text)
	}
	if !strings.Contains(text, "channel=3") {
		t.Fatalf("import did not infer the channel:\n%s", text)
	}
}

func TestAUMToolsValidate(t *testing.T) {
	s := newAUMServer(t)

	if res := call(t, s.handleGetAUMSession, map[string]any{}); !res.IsError {
		t.Fatal("expected error when neither file nor session_id given")
	}
	if res := call(t, s.handleGetAUMSession, map[string]any{"session_id": "../etc/passwd"}); !res.IsError {
		t.Fatal("expected traversal guard to reject a path-y session_id")
	}
	if res := call(t, s.handleAuthorAUMSession, map[string]any{"channels": []any{}}); !res.IsError {
		t.Fatal("expected error authoring with no channels")
	}
	if res := call(t, s.handleEditAUMSession, map[string]any{"session_id": "x"}); !res.IsError {
		t.Fatal("expected error editing with no edits")
	}
}
