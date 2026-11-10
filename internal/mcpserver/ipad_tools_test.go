package mcpserver

import (
	"context"
	"encoding/json"
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
	"ipad_swipe", "ipad_launch", "get_aum_ui", "wait_for_aum_ui",
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
	for _, want := range []string{"main menu open", `session: "test-session (mcp test)"`, "tempo: 120 BPM", `strip 2 "2: Synth One" -1.0 dB`, `"6: Pad Synth" -7.9 dB (off screen)`, "mute/solo"} {
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
