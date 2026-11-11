package ipad

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// The testdata/aum-plugin-window*.json trees were captured from AUM 1.4.8 on
// an iPad in landscape (1210×834) with a plugin window open on a strip with
// a custom title, trimmed to the keys ParseSource reads, the plugin's own view
// emptied and the names of private sessions replaced.

func capturedTree(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parseCaptured(t *testing.T, name string) AUMState {
	t.Helper()
	root, err := ParseSource(capturedTree(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return ParseAUM(root, Size{Width: 1210, Height: 834})
}

func TestPluginWindowTitles(t *testing.T) {
	for title, want := range map[string]bool{
		"Synth @A1":                    true,
		"UA Battalion @A2 - Battalion": true,
		"Viking Synth @A3 - Bass":      true,
		"Arp @M1:1":                    true,
		"Arp @M1:1 - Brain":            true,
		"◀︎ MidiBrain @M1:1 + 1 more":  false, // MIDI CONTROL's source selector
		"Battalion":                    false,
		"UA Battalion @":               false,
	} {
		if got := rePluginWin.MatchString(title); got != want {
			t.Errorf("%q: plugin window title %v, want %v", title, got, want)
		}
	}
}

func TestParseAUMCapturedPluginWindow(t *testing.T) {
	for file, want := range map[string]struct {
		title  string
		closeX float64
	}{
		"aum-plugin-window.json":              {"UA Battalion @A2 - Battalion", 1180}, // MAXIMIZED
		"aum-plugin-window-default-mode.json": {"UA Battalion @A2 - Battalion", 1097}, // DEFAULT
		"aum-plugin-window-pad.json":          {"Phosphor3 @A4 - Pad", 1180},
	} {
		st := parseCaptured(t, file)
		if len(st.PluginWindows) != 1 || st.PluginWindows[0].Title != want.title {
			t.Fatalf("%s: plugin windows = %+v, want %q", file, st.PluginWindows, want.title)
		}
		w := st.PluginWindows[0]
		if len(w.Bar) != 8 {
			t.Fatalf("%s: title bar = %v", file, w.Bar)
		}
		// ✕ is the last button of the bar, at the window's top right.
		if x, y := w.Bar[len(w.Bar)-1].Center(); x != want.closeX || y != 76 {
			t.Fatalf("%s: ✕ at %v,%v", file, x, y)
		}
		if len(st.Overlays) != 0 || len(st.Channels) != 5 {
			t.Fatalf("%s: overlays %+v, %d strips", file, st.Overlays, len(st.Channels))
		}
	}
}

// A side panel opened over a plugin window: the window's bar is hidden under
// it, and the panel is read by its own title. SESSIONS and SAVE were
// captured while AUM pushed them over the main menu, whose header was still
// in the tree, hidden and slid above the screen, ahead of the new one.
func TestParseAUMCapturedPanelOverPluginWindow(t *testing.T) {
	for file, title := range map[string]string{
		"aum-plugin-window-menu.json":         "MENU",
		"aum-plugin-window-sessions.json":     "SESSIONS",
		"aum-plugin-window-save.json":         "SAVE",
		"aum-plugin-window-midi-control.json": "MIDI CONTROL",
	} {
		st := parseCaptured(t, file)
		if got := st.Panel(); got != title {
			t.Errorf("%s: panel %q, want %q", file, got, title)
		}
		if len(st.Overlays) != 1 || len(st.PluginWindows) != 0 {
			t.Errorf("%s: overlays %+v, plugin windows %+v", file, st.Overlays, st.PluginWindows)
		}
	}
	if st := parseCaptured(t, "aum-plugin-window-save.json"); st.Overlays[0].Fields[0] != "Eight Voices" {
		t.Errorf("SAVE fields = %v", st.Overlays[0].Fields)
	}
}

// The main menu's page stays in the tree after AUM has pushed another page
// over it (SESSIONS, SAVE): its header and its "SESSION: …" line are then no
// sign of an open menu, and the name is not on screen.
func TestParseAUMCapturedMenuLeftover(t *testing.T) {
	if st := parseCaptured(t, "aum-plugin-window-menu.json"); !st.MenuOpen || st.Session != "Eight Voices" {
		t.Errorf("menu open: menu %v, session %q", st.MenuOpen, st.Session)
	}
	for _, file := range []string{
		"aum-plugin-window-sessions.json",
		"aum-plugin-window-save.json",
		"aum-plugin-window-midi-control.json",
		"aum-plugin-window.json",
	} {
		if st := parseCaptured(t, file); st.MenuOpen || st.Session != "" {
			t.Errorf("%s: menu %v, session %q, want no open menu and no name", file, st.MenuOpen, st.Session)
		}
	}
}

func TestAUMWindowCloseCapturedPluginWindow(t *testing.T) {
	w := ipadtest.New(capturedTree(t, "aum-plugin-window.json"))
	w.SetScreens(capturedTree(t, "aum-plugin-window.json"), ipadtest.MixerAUM().Source())
	t.Cleanup(w.Close)
	c := New(w.URL)
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "window_close"})
	if err != nil || !res.Verified {
		t.Fatalf("window_close: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 1180,76" {
		t.Fatalf("gestures = %v", g)
	}
}

// The window-mode menu marks the current mode ✓ and the default one ‧.
func TestAUMWindowModeCaptured(t *testing.T) {
	for mode, item := range map[string]string{"MAXIMIZED": "tap 1116.5,212", "DEFAULT": "tap 1116.5,126", "CUSTOM": "tap 1116.5,169"} {
		window := capturedTree(t, "aum-plugin-window.json")
		w := ipadtest.New(window)
		w.SetScreens(window, capturedTree(t, "aum-plugin-window-mode-menu.json"), window)
		res, err := New(w.URL).AUMNode(context.Background(), AUMRequest{Action: "window_mode", Value: mode})
		w.Close()
		if err != nil || !res.Verified {
			t.Fatalf("%s: %+v, %v", mode, res.Message, err)
		}
		if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 1148,76" || g[1] != item {
			t.Fatalf("%s: gestures = %v", mode, g)
		}
	}
}

// With a plugin window open a tree read can outlast aumWait; the first read
// after the tap still shows the main menu. The driver reads again.
func TestAUMOpenPanelSlowReads(t *testing.T) {
	window, menu, panel := capturedTree(t, "aum-plugin-window.json"), capturedTree(t, "aum-plugin-window-menu.json"), capturedTree(t, "aum-plugin-window-midi-control.json")
	w := ipadtest.New(window)
	w.SourceFunc = func(gestures, reads int) json.RawMessage {
		switch {
		case gestures == 0:
			return window
		case gestures == 1:
			return menu
		case reads == 0:
			time.Sleep(aumWait + 100*time.Millisecond)
			return menu
		default:
			return panel
		}
	}
	t.Cleanup(w.Close)
	res, err := New(w.URL).AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "midi_control"})
	if err != nil || !res.Verified {
		t.Fatalf("open midi_control: %+v, %v", res.Message, err)
	}
}

func TestAUMOpenPanelOverCapturedPluginWindow(t *testing.T) {
	for panel, screen := range map[string]string{
		"files":        "aum-plugin-window-sessions.json",
		"save":         "aum-plugin-window-save.json",
		"midi_control": "aum-plugin-window-midi-control.json",
	} {
		w := ipadtest.New(capturedTree(t, "aum-plugin-window.json"))
		w.SetScreens(capturedTree(t, "aum-plugin-window.json"), capturedTree(t, "aum-plugin-window-menu.json"), capturedTree(t, screen))
		c := New(w.URL)
		res, err := c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: panel})
		w.Close()
		if err != nil || !res.Verified {
			t.Fatalf("open %s: %+v, %v", panel, res.Message, err)
		}
		if g := w.GestureLog(); len(g) != 2 {
			t.Fatalf("open %s: gestures = %v", panel, g)
		}
	}
}
