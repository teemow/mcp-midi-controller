package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-device/device"
)

// gtr1Component is the tuple stageProbe writes; default states are matched to it.
var gtr1Component = device.ProbeComponent{Type: "aumu", Subtype: "gtr1", Manufacturer: "Acme", ManufacturerName: "Acme"}

func TestApplyDefaultStatePrecedence(t *testing.T) {
	defs := []device.AUv3DefaultState{{
		Component: gtr1Component,
		Name:      "Guitar default",
		State: map[string]device.StateEntry{
			"shared": {Text: "from-default"},
			"only":   {Text: "default-only"},
		},
	}}

	t.Run("identity-only node gets the full default", func(t *testing.T) {
		ns := aum.NodeSpec{Component: gtr1Component}
		if err := applyDefaultState(&ns, defs); err != nil {
			t.Fatalf("err=%v", err)
		}
		if got := string(ns.StateDoc["shared"]); got != "from-default" {
			t.Fatalf("shared = %q", got)
		}
		if got := string(ns.StateDoc["only"]); got != "default-only" {
			t.Fatalf("only = %q", got)
		}
	})

	t.Run("per-call state wins per key, default fills the rest", func(t *testing.T) {
		ns := aum.NodeSpec{Component: gtr1Component, StateDoc: map[string][]byte{"shared": []byte("from-call")}}
		if err := applyDefaultState(&ns, defs); err != nil {
			t.Fatalf("err=%v", err)
		}
		if got := string(ns.StateDoc["shared"]); got != "from-call" {
			t.Fatalf("per-call key was overwritten: shared = %q", got)
		}
		if got := string(ns.StateDoc["only"]); got != "default-only" {
			t.Fatalf("default did not fill gap: only = %q", got)
		}
	})

	t.Run("no matching default is a no-op", func(t *testing.T) {
		ns := aum.NodeSpec{Component: device.ProbeComponent{Type: "aumu", Subtype: "xxxx", Manufacturer: "Acme"}}
		if err := applyDefaultState(&ns, defs); err != nil {
			t.Fatalf("err: %v", err)
		}
		if ns.StateDoc != nil {
			t.Fatalf("expected no-op, got doc=%v", ns.StateDoc)
		}
	})

	t.Run("a matching but broken default errors loudly", func(t *testing.T) {
		broken := []device.AUv3DefaultState{{
			Component: gtr1Component,
			State:     map[string]device.StateEntry{"bad": {Base64: "!!!not-base64!!!"}},
		}}
		ns := aum.NodeSpec{Component: gtr1Component}
		if err := applyDefaultState(&ns, broken); err == nil {
			t.Fatal("expected error for invalid base64 entry")
		}
	})
}

func TestSetGetDeleteAUv3DefaultState(t *testing.T) {
	s := newAUMServer(t)

	res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "gtr1",
		"component": map[string]any{"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme"},
		"name":      "Guitar default",
		"state": map[string]any{
			"myConfig": map[string]any{"text": "hello world"},
		},
	})
	if res.IsError {
		t.Fatalf("set failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUv3DefaultStatesDir(), "gtr1.yaml")); err != nil {
		t.Fatalf("default-state file not written: %v", err)
	}

	res = call(t, s.handleListAUv3DefaultStates, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), "gtr1") {
		t.Fatalf("list missing gtr1:\n%s", resultText(res))
	}

	res = call(t, s.handleGetAUv3DefaultState, map[string]any{"id": "gtr1"})
	if res.IsError {
		t.Fatalf("get failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "myConfig") || !strings.Contains(resultText(res), "hello world") {
		t.Fatalf("get missing key/text:\n%s", resultText(res))
	}

	// merge adds a key without dropping the existing one.
	res = call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "gtr1",
		"component": map[string]any{"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme"},
		"merge":     true,
		"state":     map[string]any{"second": map[string]any{"text": "two"}},
	})
	if res.IsError {
		t.Fatalf("merge set failed: %s", resultText(res))
	}
	def, err := loadAUv3DefaultState(filepath.Join(config.AUv3DefaultStatesDir(), "gtr1.yaml"))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := def.State["myConfig"]; !ok {
		t.Fatalf("merge dropped myConfig: %+v", def.State)
	}
	if _, ok := def.State["second"]; !ok {
		t.Fatalf("merge missing second: %+v", def.State)
	}

	res = call(t, s.handleDeleteAUv3DefaultState, map[string]any{"id": "gtr1"})
	if res.IsError {
		t.Fatalf("delete failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUv3DefaultStatesDir(), "gtr1.yaml")); !os.IsNotExist(err) {
		t.Fatalf("file still present after delete: %v", err)
	}
}

func TestSetAUv3DefaultStateRejectsBadID(t *testing.T) {
	s := newAUMServer(t)
	res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "../escape",
		"component": map[string]any{"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme"},
		"state":     map[string]any{"k": map[string]any{"text": "v"}},
	})
	if !res.IsError || !strings.Contains(resultText(res), "invalid id") {
		t.Fatalf("expected invalid-id error, got: %s", resultText(res))
	}
}

// TestAuthorAppliesDefaultState is the end-to-end payoff: a captured default
// state for an audio unit is applied automatically to a node of that unit when
// a session is authored.
func TestAuthorAppliesDefaultState(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "gtr1",
		"component": map[string]any{"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme"},
		"state":     map[string]any{"myConfig": map[string]any{"text": "authored-default"}},
	}); res.IsError {
		t.Fatalf("set default failed: %s", resultText(res))
	}

	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Default Test",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	doc := openAuthoredNodeStateDoc(t, "default_test")
	if got := string(doc["myConfig"]); got != "authored-default" {
		t.Fatalf("authored node missing default state: myConfig = %q (doc keys: %v)", got, keysOf(doc))
	}
}

// TestAuthorPerCallStateBeatsDefault checks the precedence end-to-end: an
// explicit per-call `state` arg overrides the audio-unit default for that key.
func TestAuthorPerCallStateBeatsDefault(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "gtr1",
		"component": map[string]any{"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme"},
		"state": map[string]any{
			"myConfig": map[string]any{"text": "from-default"},
			"extra":    map[string]any{"text": "default-extra"},
		},
	}); res.IsError {
		t.Fatalf("set default failed: %s", resultText(res))
	}

	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Override Test",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{
				map[string]any{"probe_id": "gtr1", "state": map[string]any{"myConfig": "from-call"}},
			}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	doc := openAuthoredNodeStateDoc(t, "override_test")
	if got := string(doc["myConfig"]); got != "from-call" {
		t.Fatalf("per-call state did not win: myConfig = %q", got)
	}
	if got := string(doc["extra"]); got != "default-extra" {
		t.Fatalf("default did not fill non-overridden key: extra = %q", got)
	}
}

// openAuthoredNodeStateDoc opens a staged authored session and returns the
// AuStateDoc of its single hosted AUv3 node.
func openAuthoredNodeStateDoc(t *testing.T, sessionID string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(config.AUMSessionsDir(), sessionID+".aumproj"))
	if err != nil {
		t.Fatalf("read authored session: %v", err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatalf("open authored session: %v", err)
	}
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			doc, derr := sess.NodeAuStateDoc(ch.Index, n.Slot)
			if derr != nil {
				t.Fatalf("node state doc: %v", derr)
			}
			return doc
		}
	}
	t.Fatal("no hosted AUv3 node in authored session")
	return nil
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
