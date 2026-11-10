package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
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
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": gtr1ProbeID}}},
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
				map[string]any{"probe_id": gtr1ProbeID, "state": map[string]any{"myConfig": "from-call"}},
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

// stageProbeFor writes a minimal probe dump for component c into the staging
// dir under its probe id, and returns that id.
func stageProbeFor(t *testing.T, c device.ProbeComponent, name string) string {
	t.Helper()
	dump := device.ProbeDump{
		Component: c,
		Name:      name,
		Parameters: []device.ProbeParam{
			{Address: 0, Identifier: "cutoff", DisplayName: "Cutoff", Min: 0, Max: 1, Value: 0.5, Writable: true, Readable: true},
		},
	}
	b, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	dir := config.AUv3ProbesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := device.ProbeID(dump)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestCaptureAUv3DefaultStateKeepsUnitsSharingASubtypeApart captures two
// audio units that share a subtype (an instrument and an effect of one
// vendor). Each gets its own file under its component id, and authoring
// applies each one's state to its own node; under the old subtype-only key the
// second capture replaced the first.
func TestCaptureAUv3DefaultStateKeepsUnitsSharingASubtypeApart(t *testing.T) {
	s := newAUMServer(t)
	synth := device.ProbeComponent{Type: "aumu", Subtype: "Ab12", Manufacturer: "XyZw"}
	fx := device.ProbeComponent{Type: "aufx", Subtype: "Ab12", Manufacturer: "XyZw"}
	synthProbe := stageProbeFor(t, synth, "Shared Synth")
	fxProbe := stageProbeFor(t, fx, "Shared FX")

	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Capture Source",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Synth", "nodes": []any{
				map[string]any{"probe_id": synthProbe, "state": map[string]any{"cfg": "synth-state"}},
				map[string]any{"probe_id": fxProbe, "state": map[string]any{"cfg": "fx-state"}},
			}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author source failed: %s", resultText(res))
	}

	for _, n := range authoredHostedNodes(t, "capture_source") {
		res := call(t, s.handleCaptureAUv3DefaultState, map[string]any{
			"session_id": "capture_source", "channel": n.channel, "slot": n.slot,
		})
		if res.IsError {
			t.Fatalf("capture %s failed: %s", n.component.Type, resultText(res))
		}
	}

	dir := config.AUv3DefaultStatesDir()
	for _, tc := range []struct {
		c    device.ProbeComponent
		file string
		want string
	}{
		{synth, "aumu-_ab12-_xy_zw.yaml", "synth-state"},
		{fx, "aufx-_ab12-_xy_zw.yaml", "fx-state"},
	} {
		def, err := loadAUv3DefaultState(filepath.Join(dir, tc.file))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if def.Component != tc.c {
			t.Errorf("%s holds %+v, want %+v", tc.file, def.Component, tc.c)
		}
		if got := def.State["cfg"].Text; got != tc.want {
			t.Errorf("%s cfg = %q, want %q", tc.file, got, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ab12.yaml")); !os.IsNotExist(err) {
		t.Errorf("capture still wrote the subtype-only file: %v", err)
	}

	res = call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Capture Target",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Synth", "nodes": []any{
				map[string]any{"probe_id": synthProbe},
				map[string]any{"probe_id": fxProbe},
			}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author target failed: %s", resultText(res))
	}
	want := map[string]string{"aumu": "synth-state", "aufx": "fx-state"}
	nodes := authoredHostedNodes(t, "capture_target")
	if len(nodes) != len(want) {
		t.Fatalf("authored %d hosted nodes, want %d", len(nodes), len(want))
	}
	for _, n := range nodes {
		if got := string(n.doc["cfg"]); got != want[n.component.Type] {
			t.Errorf("%s node cfg = %q, want its own default %q", n.component.Type, got, want[n.component.Type])
		}
	}
}

type hostedNode struct {
	channel, slot int
	component     device.ProbeComponent
	doc           map[string][]byte
}

// authoredHostedNodes opens a staged session and returns its hosted AUv3
// nodes with their position and AuStateDoc.
func authoredHostedNodes(t *testing.T, sessionID string) []hostedNode {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(config.AUMSessionsDir(), sessionID+".aumproj"))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	var out []hostedNode
	for _, ch := range sess.Channels() {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue
			}
			doc, derr := sess.NodeAuStateDoc(ch.Index, n.Slot)
			if derr != nil {
				t.Fatalf("node state doc: %v", derr)
			}
			out = append(out, hostedNode{channel: ch.Index, slot: n.Slot, component: *n.Component, doc: doc})
		}
	}
	return out
}
