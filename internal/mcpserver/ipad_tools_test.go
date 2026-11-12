package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/midi-device/device"
)

var ipadToolNames = []string{
	"ipad_status", "ipad_screenshot", "ipad_ui_tree", "ipad_tap", "ipad_hold",
	"ipad_swipe", "ipad_drag", "ipad_text", "ipad_launch", "get_aum_ui", "wait_for_aum_ui",
	"aum_panel", "aum_channel", "aum_node", "aum_transport", "aum_files",
	"aum_midi_routing", "aum_midi_control", "aum_settings", "aum_file_player",
}

func newIPadServer(t *testing.T) (*ipadtest.WDA, *Server) {
	t.Helper()
	w := ipadtest.New(ipadtest.DefaultAUM().Source())
	t.Cleanup(w.Close)
	return w, New(engine.New(device.NewRegistry(), fakeBLE{}), WithIPad(ipad.New(w.URL)))
}

// connectClient opens an in-memory MCP client session on s, so a test goes
// through tools/list and tools/call (schema validation, content encoding).
func connectClient(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.mcp.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listedTools(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestIPadToolsRegistration(t *testing.T) {
	plain := New(engine.New(device.NewRegistry(), fakeBLE{}))
	got := listedTools(t, connectClient(t, plain))
	for _, n := range ipadToolNames {
		if got[n] {
			t.Errorf("%s registered without WithIPad", n)
		}
	}
	_, s := newIPadServer(t)
	got = listedTools(t, connectClient(t, s))
	for _, n := range ipadToolNames {
		if !got[n] {
			t.Errorf("%s not registered with WithIPad", n)
		}
	}
}

func TestIPadStatus(t *testing.T) {
	w, s := newIPadServer(t)
	res := call(t, s.handleIPadStatus, map[string]any{})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "WebDriverAgent ready") || !strings.Contains(text, "iPadOS 27.2") ||
		!strings.Contains(text, "1210x834 points, 2 pixels per point (landscape)") || !strings.Contains(text, "com.kymatica.AUM") {
		t.Fatalf("status:\n%s", text)
	}

	// Unreachable: still a result (not a tool error), with the stack hint.
	w.Close()
	res = call(t, s.handleIPadStatus, map[string]any{})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "unavailable") || !strings.Contains(text, "scripts/ipad") {
		t.Fatalf("unreachable status:\n%s", text)
	}
	if st, ok := res.StructuredContent.(ipadStatus); !ok || st.Reachable || st.Hint == "" {
		t.Fatalf("structured = %#v", res.StructuredContent)
	}
	// Other tools fail with the same hint.
	res = call(t, s.handleGetAUMUI, map[string]any{})
	if !res.IsError || !strings.Contains(resultText(res), "not reachable") || !strings.Contains(resultText(res), "scripts/ipad wda") {
		t.Fatalf("unreachable get_aum_ui:\n%s", resultText(res))
	}
}

func TestIPadScreenshotOverMCP(t *testing.T) {
	w, s := newIPadServer(t)
	w.Window = [2]float64{120, 80}
	cs := connectClient(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "ipad_screenshot",
		Arguments: map[string]any{"region": map[string]any{"x": 10, "y": 20, "width": 30, "height": 40}, "format": "png"},
	})
	if err != nil || res.IsError {
		t.Fatalf("ipad_screenshot: %v %s", err, resultText(res))
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !strings.HasPrefix(string(img.Data), "\x89PNG") {
		t.Fatalf("first content = %#v", res.Content[0])
	}
	if text := resultText(res); !strings.Contains(text, "30x40 px") || !strings.Contains(text, "point = (10 + px/1, 20 + py/1)") {
		t.Fatalf("caption: %s", text)
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ipad_screenshot", Arguments: map[string]any{"format": "gif"}})
	if err == nil && !res.IsError {
		t.Fatal("gif accepted")
	}
}

func TestIPadUITree(t *testing.T) {
	_, s := newIPadServer(t)
	res := call(t, s.handleIPadUITree, map[string]any{})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, `Button "MIDI 1" (79,764 156x50)`) || !strings.HasPrefix(text, "com.kymatica.AUM: ") {
		t.Fatalf("tree:\n%s", text)
	}
	res = call(t, s.handleIPadUITree, map[string]any{"contains": "dB", "limit": 2})
	tree := res.StructuredContent.(ipadTree)
	if len(tree.Nodes) != 2 || !tree.Truncated || !strings.Contains(resultText(res), "cut at 2") {
		t.Fatalf("filtered tree: %+v\n%s", tree, resultText(res))
	}
}

// probeOnIPad puts the stand-in probe app on the fake iPad under a bundle id
// with a signing-team prefix, as a sideloaded build carries it, and gives the
// test a state directory of its own. newIPadServer's daemon has no go-ios, so
// it cannot look that id up: the alias resolves from what it remembers.
func probeOnIPad(t *testing.T, w *ipadtest.WDA) *ipadtest.ProbeApp {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	app := &ipadtest.ProbeApp{}
	w.Apps = map[string]*ipadtest.ProbeApp{rigProbeBundle: app}
	return app
}

// sessionApps lists the app of every session the fake was asked to open.
func sessionApps(w *ipadtest.WDA) []string {
	ids := []string{}
	for _, r := range w.Requests() {
		if r.Method != http.MethodPost || r.Path != "/session" {
			continue
		}
		caps, _ := r.Body["capabilities"].(map[string]any)
		match, _ := caps["alwaysMatch"].(map[string]any)
		id, _ := match["bundleId"].(string)
		ids = append(ids, id)
	}
	return ids
}

// ipadAppCalls is one call of every tool that takes an app argument.
var ipadAppCalls = []struct {
	tool string
	args map[string]any
}{
	{"ipad_ui_tree", map[string]any{}},
	{"ipad_tap", map[string]any{"x": 600, "y": 400}},
	{"ipad_hold", map[string]any{"x": 600, "y": 400}},
	{"ipad_swipe", map[string]any{"x1": 600, "y1": 400, "x2": 300, "y2": 400}},
	{"ipad_drag", map[string]any{"points": []map[string]any{{"x": 600, "y": 400}, {"x": 300, "y": 400}}}},
	{"ipad_text", map[string]any{"action": "fields"}},
	{"ipad_launch", map[string]any{}},
}

func TestIPadUITreeOfProbeAlias(t *testing.T) {
	w, s := newIPadServer(t)
	probeOnIPad(t, w)

	// Never seen: the alias cannot be resolved, and the error says how the
	// daemon learns the id.
	res := call(t, s.handleIPadUITree, map[string]any{"app": "probe"})
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, `resolve "probe": go-ios`) ||
		!strings.Contains(text, "call ipad_launch once with the app's full bundle id") {
		t.Fatalf("unknown probe alias:\n%s", text)
	}
	if got := sessionApps(w); len(got) != 0 {
		t.Fatalf("sessions opened for an unresolved app: %v", got)
	}

	// ipad_launch with the full id remembers it; from then on the alias
	// reads the probe app's tree, not AUM's.
	if res := call(t, s.handleIPadLaunch, map[string]any{"app": rigProbeBundle}); res.IsError {
		t.Fatalf("launch by bundle id: %s", resultText(res))
	}
	for _, alias := range []string{"probe", " Probe "} {
		res = call(t, s.handleIPadUITree, map[string]any{"app": alias})
		text = resultText(res)
		if res.IsError || !strings.HasPrefix(text, rigProbeBundle+": ") || !strings.Contains(text, `Button "aum sessions"`) ||
			strings.Contains(text, `Button "MIDI 1"`) {
			t.Fatalf("tree of %q:\n%s", alias, text)
		}
		if tree := res.StructuredContent.(ipadTree); tree.App != rigProbeBundle {
			t.Fatalf("tree of %q is of app %q", alias, tree.App)
		}
	}
}

// Every tool that takes an app resolves the probe alias the same way: none
// without the remembered id, all of them with it.
func TestIPadToolsResolveProbeAlias(t *testing.T) {
	w, s := newIPadServer(t)
	probeOnIPad(t, w)
	cs := connectClient(t, s)
	callProbe := func(tool string, args map[string]any) (*mcp.CallToolResult, string) {
		t.Helper()
		with := map[string]any{"app": "probe"}
		for k, v := range args {
			with[k] = v
		}
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: with})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return res, resultText(res)
	}

	for _, c := range ipadAppCalls {
		res, text := callProbe(c.tool, c.args)
		if !res.IsError || !strings.Contains(text, "call ipad_launch once with the app's full bundle id") {
			t.Errorf("%s with an unknown probe alias:\n%s", c.tool, text)
		}
	}
	if got := sessionApps(w); len(got) != 0 {
		t.Fatalf("sessions opened for an unresolved app: %v", got)
	}

	rememberIPadApp("probe", rigProbeBundle)
	for _, c := range ipadAppCalls {
		before := len(sessionApps(w))
		res, text := callProbe(c.tool, c.args)
		if res.IsError {
			t.Errorf("%s: %s", c.tool, text)
			continue
		}
		if got := sessionApps(w)[before:]; len(got) != 1 || got[0] != rigProbeBundle {
			t.Errorf("%s opened %v, want one session in %s", c.tool, got, rigProbeBundle)
		}
	}
}

// A host with go-ios asks the device: its answer wins over an id remembered
// from an earlier build of the app, and replaces it.
func TestIPadProbeAliasPrefersTheDevice(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const installed = "NEWTEAM.com.teemow.auv3probe"
	goios := filepath.Join(t.TempDir(), "ios")
	script := "#!/bin/sh\necho '[{\"CFBundleIdentifier\":\"com.kymatica.AUM\"},{\"CFBundleIdentifier\":\"" + installed + "\"}]'\n"
	if err := os.WriteFile(goios, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w := ipadtest.New(ipadtest.DefaultAUM().Source())
	t.Cleanup(w.Close)
	w.Apps = map[string]*ipadtest.ProbeApp{installed: {}}
	s := New(engine.New(device.NewRegistry(), fakeBLE{}), WithIPad(ipad.New(w.URL, ipad.WithGoIOS(ipad.NewGoIOS(goios)))))

	rememberIPadApp("probe", rigProbeBundle)
	res := call(t, s.handleIPadUITree, map[string]any{"app": "probe"})
	if text := resultText(res); res.IsError || !strings.HasPrefix(text, installed+": ") {
		t.Fatalf("tree:\n%s", text)
	}
	if got := knownIPadApps()["probe"]; got != installed {
		t.Fatalf("remembered %q, want the installed app %q", got, installed)
	}
}

func TestIPadTap(t *testing.T) {
	w, s := newIPadServer(t)
	w.Labels[`button:"LAST STATE"`] = 1

	for _, bad := range []map[string]any{
		{},
		{"label": "x", "x": 1, "y": 2},
		{"x": 1},
		{"target": "play", "app": "com.example.other"},
	} {
		if res := call(t, s.handleIPadTap, bad); !res.IsError {
			t.Errorf("args %v accepted: %s", bad, resultText(res))
		}
	}
	if res := call(t, s.handleIPadTap, map[string]any{"label": "last state"}); res.IsError {
		t.Fatalf("label tap: %s", resultText(res))
	}
	res := call(t, s.handleIPadTap, map[string]any{"label": "Nope"})
	if !res.IsError || !strings.Contains(resultText(res), "ipad_ui_tree") {
		t.Fatalf("missing label: %s", resultText(res))
	}
	res = call(t, s.handleIPadTap, map[string]any{"target": "menu"})
	if res.IsError || !strings.Contains(resultText(res), "(1185,33)") {
		t.Fatalf("target tap: %s", resultText(res))
	}
	res = call(t, s.handleIPadTap, map[string]any{"x": 10.5, "y": 20})
	if res.IsError || !strings.Contains(resultText(res), "tapped (10.5,20)") {
		t.Fatalf("point tap: %s", resultText(res))
	}
	if n := w.OpenSessions(); n != 0 {
		t.Fatalf("%d sessions left open", n)
	}
}

func TestIPadErrorHints(t *testing.T) {
	_, s := newIPadServer(t)
	lost := &ipad.Error{Status: 404, Code: "invalid session id", Message: "Session does not exist"}
	if res := s.ipadError(context.Background(), lost); !res.IsError || !strings.Contains(resultText(res), "one at a time") {
		t.Fatalf("lost-session hint: %s", resultText(res))
	}
	other := &ipad.Error{Status: 500, Code: "unknown error", Message: "x"}
	if res := s.ipadError(context.Background(), other); strings.Contains(resultText(res), "\n") {
		t.Fatalf("plain errors carry no hint: %s", resultText(res))
	}
}

func TestIPadHoldSwipeLaunch(t *testing.T) {
	_, s := newIPadServer(t)
	if res := call(t, s.handleIPadHold, map[string]any{"x": 1}); !res.IsError {
		t.Fatal("hold without y accepted")
	}
	if res := call(t, s.handleIPadHold, map[string]any{"x": 300, "y": 140}); res.IsError || !strings.Contains(resultText(res), "1.2s") {
		t.Fatalf("hold: %s", resultText(res))
	}
	if res := call(t, s.handleIPadSwipe, map[string]any{"x1": 1, "y1": 2, "x2": 3}); !res.IsError {
		t.Fatal("swipe without y2 accepted")
	}
	if res := call(t, s.handleIPadSwipe, map[string]any{"x1": 1100, "y1": 560, "x2": 300, "y2": 560}); res.IsError || !strings.Contains(resultText(res), "over 300ms") {
		t.Fatalf("swipe: %s", resultText(res))
	}
	if res := call(t, s.handleIPadLaunch, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "com.kymatica.AUM is in front") {
		t.Fatalf("launch: %s", resultText(res))
	}
}

func TestGetAUMUI(t *testing.T) {
	_, s := newIPadServer(t)
	res := call(t, s.handleGetAUMUI, map[string]any{})
	text := resultText(res)
	for _, want := range []string{"main menu open", `session: "test-session (mcp test)"`, "tempo: 120 BPM", `strip 2 "2: Synth One" -1.0 dB nodes: source 2: Synth One, 1 LP, 2 +, output SPKR`,
		`"3: Drum Machine" -∞ dB MUTED`, `"6: Pad Synth" -7.9 dB (off screen)`, `open panel "MENU"`, "plugin parameters"} {
		if !strings.Contains(text, want) {
			t.Fatalf("get_aum_ui lacks %q:\n%s", want, text)
		}
	}
	if st, ok := res.StructuredContent.(ipad.AUMState); !ok || len(st.Channels) != 6 {
		t.Fatalf("structured = %#v", res.StructuredContent)
	}
}

func TestWaitForAUMUI(t *testing.T) {
	w, s := newIPadServer(t)
	if res := call(t, s.handleWaitForAUMUI, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "nothing to wait for") {
		t.Fatalf("empty expectation: %s", resultText(res))
	}

	before, after := ipadtest.DefaultAUM(), ipadtest.DefaultAUM()
	after.Strips[1].Level = "-6.2 dB"
	w.SetSources(before.Source(), after.Source())
	res := call(t, s.handleWaitForAUMUI, map[string]any{
		"channels":   []map[string]any{{"channel": "2", "not_level_db": -1.0}},
		"timeout_ms": 3000,
	})
	if res.IsError || !strings.HasPrefix(resultText(res), "matched after") {
		t.Fatalf("wait: %s", resultText(res))
	}
	if r, ok := res.StructuredContent.(ipad.WaitResult); !ok || !r.Matched || r.Reads != 2 {
		t.Fatalf("structured = %#v", res.StructuredContent)
	}

	res = call(t, s.handleWaitForAUMUI, map[string]any{"session": "Other Session", "timeout_ms": 0})
	text := resultText(res)
	if res.IsError || !strings.HasPrefix(text, "NOT matched") || !strings.Contains(text, `want "Other Session"`) {
		t.Fatalf("mismatch: %s", text)
	}

	// The schema-level enum rejects a bad transport before the handler runs.
	cs := connectClient(t, s)
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_for_aum_ui", Arguments: json.RawMessage(`{"transport":"running"}`)})
	if err == nil && !r.IsError {
		t.Fatal("transport=running accepted")
	}
}

func TestIPadTapGestures(t *testing.T) {
	w, s := newIPadServer(t)
	w.Labels[`button:"CLEAR"`] = 1
	res := call(t, s.handleIPadTap, map[string]any{"x": 100, "y": 200, "gesture": "double"})
	if res.IsError || !strings.Contains(resultText(res), "double-tapped (100,200)") {
		t.Fatalf("double: %s", resultText(res))
	}
	res = call(t, s.handleIPadTap, map[string]any{"label": "clear", "gesture": "twice"})
	if res.IsError || !strings.Contains(resultText(res), `tapped twice "clear" at (60,40)`) {
		t.Fatalf("twice by label: %s", resultText(res))
	}
	if res := call(t, s.handleIPadTap, map[string]any{"x": 1, "y": 2, "gesture": "triple"}); !res.IsError {
		t.Fatal("unknown gesture accepted")
	}
	var taps, doubles int
	for _, r := range w.Requests() {
		switch {
		case strings.HasSuffix(r.Path, "/wda/tap"):
			taps++
		case strings.HasSuffix(r.Path, "/wda/doubleTap"):
			doubles++
		}
	}
	if taps != 2 || doubles != 1 {
		t.Fatalf("taps=%d doubles=%d", taps, doubles)
	}
}

func TestIPadDragAndText(t *testing.T) {
	w, s := newIPadServer(t)
	if res := call(t, s.handleIPadDrag, map[string]any{"points": []map[string]any{{"x": 1, "y": 2}}}); !res.IsError {
		t.Fatal("one-point drag accepted")
	}
	res := call(t, s.handleIPadDrag, map[string]any{"points": []map[string]any{{"x": 842, "y": 426}, {"x": 930, "y": 426}, {"x": 930, "y": 330}}, "hold_ms": 500})
	if res.IsError || !strings.Contains(resultText(res), "(842,426) -> (930,426) -> (930,330)") {
		t.Fatalf("drag: %s", resultText(res))
	}
	var acts []any
	for _, r := range w.Requests() {
		if strings.HasSuffix(r.Path, "/actions") {
			acts = r.Body["actions"].([]any)[0].(map[string]any)["actions"].([]any)
		}
	}
	// move, down, (move, rest)×2, hold, up
	if len(acts) != 8 || acts[6].(map[string]any)["duration"].(float64) != 500 {
		t.Fatalf("drag actions = %v", acts)
	}

	w.Fields = []string{"old name"}
	res = call(t, s.handleIPadText, map[string]any{"action": "fields"})
	if res.IsError || !strings.Contains(resultText(res), `0 text_field at (100,200 300x36): "old name"`) {
		t.Fatalf("fields: %s", resultText(res))
	}
	res = call(t, s.handleIPadText, map[string]any{"action": "type", "text": "new name", "enter": true})
	if res.IsError || !strings.Contains(resultText(res), `field 0 now reads "new name"; pressed Return`) {
		t.Fatalf("type: %s", resultText(res))
	}
	if res := call(t, s.handleIPadText, map[string]any{"action": "type", "text": "x", "field": 3}); !res.IsError || !strings.Contains(resultText(res), "no such text field") {
		t.Fatalf("bad field: %s", resultText(res))
	}
}

func TestAUMToolsOverMCP(t *testing.T) {
	w, s := newIPadServer(t)
	muted := ipadtest.MixerAUM()
	muted.Strips[1].Muted = true
	w.SetScreens(ipadtest.MixerAUM().Source(), muted.Source())
	cs := connectClient(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "aum_channel", Arguments: map[string]any{"action": "mute", "channel": "2", "on": true}})
	if err != nil || res.IsError {
		t.Fatalf("aum_channel: %v %s", err, resultText(res))
	}
	text := resultText(res)
	if !strings.HasPrefix(text, "mute: verified — strip 2 \"2: Synth One\": mute on") || !strings.Contains(text, "gestures: tap MUTE") || !strings.Contains(text, `"2: Synth One" -1.0 dB MUTED`) {
		t.Fatalf("aum_channel text:\n%s", text)
	}
	// Guarded actions fail without confirm, before any gesture.
	before := w.Gestures()
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "aum_files", Arguments: map[string]any{"action": "clear"}})
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "needs confirm: true") || w.Gestures() != before {
		t.Fatalf("aum_files clear: %v %s", err, resultText(res))
	}
	// The action enum is enforced by the schema.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "aum_node", Arguments: map[string]any{"action": "explode"}})
	if err == nil && !res.IsError {
		t.Fatal("unknown action accepted")
	}
	// wait_for_aum_ui takes the new expectations.
	w.SetScreens(muted.Source())
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_for_aum_ui", Arguments: map[string]any{
		"channels":    []map[string]any{{"channel": "2", "muted": true}, {"channel": "2", "nodes": []map[string]any{{"node": "LP", "bypassed": false}}}},
		"strip_count": 6, "no_overlay": true, "timeout_ms": 0,
	}})
	if err != nil || res.IsError || !strings.HasPrefix(resultText(res), "matched") {
		t.Fatalf("wait_for_aum_ui: %v %s", err, resultText(res))
	}
}
