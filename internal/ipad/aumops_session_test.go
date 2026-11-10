package ipad

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// sessionScreens is AUM while "Night Drive" is opened from the Songs folder.
func sessionScreens(loadingReads int, notices ...string) func(gestures, reads int) json.RawMessage {
	return ipadtest.OpenSessionScreens("Songs", "Night Drive", []ipadtest.Strip{
		{Title: "MIDI 1"}, {Title: "2: bass", Level: "-4.0 dB"}, {Title: "3: pad", Level: "+0.0 dB"}, {Title: "4: Master", Level: "+0.0 dB"},
	}, loadingReads, notices...)
}

func openFake(t *testing.T, fn func(gestures, reads int) json.RawMessage) (*ipadtest.WDA, *Client) {
	t.Helper()
	w := ipadtest.New(fn(0, 0))
	w.SourceFunc = fn
	t.Cleanup(w.Close)
	old := AUMLoadPause
	AUMLoadPause = 10 * time.Millisecond
	t.Cleanup(func() { AUMLoadPause = old })
	return w, New(w.URL)
}

// TestAUMOpenSession opens a session with a plugin window in the way: the
// window is closed first, the session is loaded from its folder, and the call
// returns only once AUM has stopped loading, with the bare mixer.
func TestAUMOpenSession(t *testing.T) {
	w, c := openFake(t, sessionScreens(3))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, ok := res.Data.(AUMLoaded)
	if !ok || !res.Verified {
		t.Fatalf("open: %+v", res)
	}
	if got.Session != "Night Drive" || len(got.Strips) != 4 || got.Strips[1] != "2: bass" || len(got.Problems) != 0 {
		t.Errorf("loaded = %+v", got)
	}
	if !strings.Contains(res.Message, `AUM has "Night Drive" open: 4 strip(s)`) {
		t.Errorf("message = %q", res.Message)
	}
	// ✕, ☰, FILES, Sessions, Songs, the row, ☰, the menu's close icon.
	if g := w.GestureLog(); len(g) != 8 {
		t.Errorf("gestures = %v, want 8", g)
	}
	if len(res.State.Overlays) != 0 || len(res.State.PluginWindows) != 0 {
		t.Errorf("the screen was not left bare: %+v", res.State.Overlays)
	}
}

// TestAUMOpenSessionReportsProblems checks a plugin AUM names as failed is
// reported with the loaded session.
func TestAUMOpenSessionReportsProblems(t *testing.T) {
	_, c := openFake(t, sessionScreens(0, "Acme: PolySynth failed to load: the plugin was invalidated"))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "PolySynth failed") {
		t.Errorf("problems = %v", got.Problems)
	}
	if !strings.Contains(res.Message, "on screen: Acme: PolySynth failed") {
		t.Errorf("message = %q", res.Message)
	}
}

// TestAUMOpenSessionStillLoading checks a session that never finishes loading
// is reported as not verified instead of being waited for forever.
func TestAUMOpenSessionStillLoading(t *testing.T) {
	old := aumLoadTimeout
	aumLoadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { aumLoadTimeout = old })
	_, c := openFake(t, sessionScreens(1<<30))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if res.Verified || !strings.Contains(res.Message, "still shows LOADING STATE") {
		t.Errorf("a session that kept loading was reported loaded: %+v", res.Message)
	}
}

// TestAUMOpenSessionUnknown checks a session that is not in the folder is an
// error that lists what is.
func TestAUMOpenSessionUnknown(t *testing.T) {
	_, c := openFake(t, sessionScreens(0))
	_, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Nowhere")
	if err == nil || !strings.Contains(err.Error(), `no session "Nowhere" in Songs (entries: Night Drive)`) {
		t.Fatalf("open of an unknown session: %v", err)
	}
}

// TestAUMSession reads the open session's name and leaves the mixer bare.
func TestAUMSession(t *testing.T) {
	menu := ipadtest.DefaultAUM()
	bare := mixer()
	w, c := opFake(t, bare, menu, bare)
	res, err := c.AUMSession(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if got.Session != "test-session (mcp test)" || len(got.Strips) != 6 {
		t.Errorf("session = %+v", got)
	}
	if g := w.GestureLog(); len(g) != 2 {
		t.Errorf("gestures = %v, want the menu opened and closed", g)
	}
}
