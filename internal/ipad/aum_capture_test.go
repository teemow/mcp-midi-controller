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

// The testdata/aum-*.json trees were captured from AUM 1.4.8 on an iPad in
// landscape (1210×834), trimmed to the keys ParseSource reads, with the names
// of private sessions replaced. The aum-plugin-window*.json ones have a
// plugin window open on a strip with a custom title, the plugin's own view
// emptied. The aum-start-screen*.json and aum-one-channel*.json ones are a
// scratch session: the start screen CLEAR leaves, its add tile's menu, the
// first read after AUDIO was tapped, and that one empty channel with the main
// menu open, bare, with its Set level dialog (the iOS keyboard's keys
// removed) and after Set.

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

// aum-menu-closing.json is the first read after the main menu's close icon
// was tapped: the menu's page is still in the tree, hidden and slid out past
// the right edge. The close reports the menu as closed, and so does the state
// it returns.
func TestAUMCloseMenuCaptured(t *testing.T) {
	if st := parseCaptured(t, "aum-menu-closing.json"); st.MenuOpen || st.Session != "" || st.Panel() != "" || len(st.Channels) != 5 {
		t.Fatalf("closing menu: menu %v, session %q, panel %q, %d strips", st.MenuOpen, st.Session, st.Panel(), len(st.Channels))
	}
	w := ipadtest.New(capturedTree(t, "aum-plugin-window-menu.json"))
	w.SetScreens(capturedTree(t, "aum-plugin-window-menu.json"), capturedTree(t, "aum-menu-closing.json"))
	t.Cleanup(w.Close)
	res, err := New(w.URL).AUMPanel(context.Background(), AUMRequest{Action: "close"})
	if err != nil || !res.Verified {
		t.Fatalf("close: %+v, %v", res.Message, err)
	}
	if res.State.MenuOpen || res.State.Session != "" {
		t.Fatalf("state after the close: menu %v, session %q", res.State.MenuOpen, res.State.Session)
	}
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 1190,30" {
		t.Fatalf("gestures = %v", g)
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

// The start screen (what CLEAR leaves) has no strip: its add tile sits in the
// middle of the mixer, in a column that also holds LAST STATE and DEFAULT.
// DEFAULT is a wide button low in a full-height container, like a strip's
// title, and is none.
func TestParseAUMCapturedStartScreen(t *testing.T) {
	for _, file := range []string{"aum-start-screen.json", "aum-start-screen-add-menu.json"} {
		st := parseCaptured(t, file)
		if !st.StartScreen || len(st.Channels) != 0 {
			t.Fatalf("%s: start screen %v, strips %s", file, st.StartScreen, st.channelList())
		}
		if st.AddTile == nil || *st.AddTile != (Rect{X: 530, Y: 372, Width: 150, Height: 150}) {
			t.Fatalf("%s: add tile = %v", file, st.AddTile)
		}
	}
	// The open menu is the tile itself, turned into a container of its items.
	menu := parseCaptured(t, "aum-start-screen-add-menu.json")
	if got := menu.addMenu["MIDI"]; got != (Rect{X: 530, Y: 425, Width: 150, Height: 45}) || len(menu.addMenu) != 3 {
		t.Fatalf("add menu = %v", menu.addMenu)
	}
	// One channel: the strip and the tile's column share the middle.
	one := parseCaptured(t, "aum-start-screen-channel-added.json")
	if one.StartScreen || len(one.Channels) != 1 || one.Channels[0].Title != "CHAN 1" || one.Channels[0].Frame.X != 363 {
		t.Fatalf("one channel: start screen %v, strips %+v", one.StartScreen, one.Channels)
	}
	if one.AddTile == nil || one.AddTile.X != 651 {
		t.Fatalf("one channel: add tile = %v", one.AddTile)
	}
}

func TestAUMChannelAddFromCapturedStartScreen(t *testing.T) {
	start, menu, added := capturedTree(t, "aum-start-screen.json"), capturedTree(t, "aum-start-screen-add-menu.json"), capturedTree(t, "aum-start-screen-channel-added.json")
	w := ipadtest.New(start)
	w.SetScreens(start, menu, added)
	t.Cleanup(w.Close)
	res, err := New(w.URL).AUMChannel(context.Background(), AUMRequest{Action: "add_audio"})
	if err != nil || !res.Verified || res.Message != `added strip 1 "CHAN 1"` {
		t.Fatalf("add_audio: %+v, %v", res.Message, err)
	}
	if len(res.State.Channels) != 1 || res.State.Channels[0].Kind != "audio" || res.State.StartScreen {
		t.Fatalf("state after add_audio: %+v", res.State.Channels)
	}
	// The tile in the middle, then AUDIO, the top of the menu over it.
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 605,447" || g[1] != "tap 605,394.5" {
		t.Fatalf("gestures = %v", g)
	}
}

// CLEAR on a session of one channel leaves the start screen: no strip.
func TestAUMFilesClearCapturedOneChannel(t *testing.T) {
	menu, start := capturedTree(t, "aum-one-channel-menu.json"), capturedTree(t, "aum-start-screen.json")
	w := ipadtest.New(menu)
	w.SetScreens(menu, menu, start)
	t.Cleanup(w.Close)
	res, err := New(w.URL).AUMFiles(context.Background(), AUMRequest{Action: "clear", Confirm: true})
	if err != nil || !res.Verified || res.Message != "1 strips -> 0" {
		t.Fatalf("clear: %+v, %v", res.Message, err)
	}
}

// While a side panel is open AUM takes a tap on the mixer to close the panel
// and passes nothing on: the dB label's tap under the main menu (which a
// session load leaves open) opened no dialog, the next one did. The screens
// follow that: whatever the first gesture is, it only closes the menu.
func TestAUMChannelSetLevelCapturedUnderMenu(t *testing.T) {
	w := ipadtest.New(capturedTree(t, "aum-one-channel-menu.json"))
	w.SetScreens(capturedTree(t, "aum-one-channel-menu.json"), capturedTree(t, "aum-one-channel.json"),
		capturedTree(t, "aum-one-channel-set-level.json"), capturedTree(t, "aum-one-channel-level-set.json"))
	w.Fields = []string{"-9.00"}
	w.Labels = map[string]int{`button:"Set"`: 1}
	t.Cleanup(w.Close)
	level := -6.0
	res, err := New(w.URL).AUMChannel(context.Background(), AUMRequest{Action: "set_level", Channel: "1", LevelDB: &level})
	if err != nil || !res.Verified || res.Message != `strip 1 "CHAN 1" fader -9.0 dB -> -6.0 dB` || w.Fields[0] != "-6" {
		t.Fatalf("set_level: %+v, %v (field %q)", res.Message, err, w.Fields[0])
	}
	// The menu's close icon, the dB label, Set.
	if g := w.GestureLog(); len(g) != 3 || g[0] != "tap 1190,30" || g[1] != "tap 560,213" || g[2] != "click" {
		t.Fatalf("gestures = %v", g)
	}
	if res.State.MenuOpen || len(res.State.Overlays) != 0 {
		t.Fatalf("state after set_level: menu %v, overlays %+v", res.State.MenuOpen, res.State.Overlays)
	}
}

// The aum-node-*.json trees: a scratch strip with AUM's built-in Gain in
// insert slot 1, on the wire and then bypassed by aum_node bypass, and a
// session that holds an Audio Unit effect bypassed in insert slot 1 of
// "kick" (no plugin window open). AUM slides a bypassed node's button 55
// points left of its slot and leaves its inner button (value "1", Selected)
// at the slot's place, beside the node in the slot's container, where it
// draws the eject ⏏. A bypassed node is loaded, never an empty slot
// (https://github.com/teemow/mcp-midi-controller/issues/214).
func TestParseAUMCapturedBypassedNode(t *testing.T) {
	for _, tc := range []struct {
		file, strip, label string
		bypassed           bool
		x                  float64
	}{
		{"aum-node-gain.json", "CHAN 1", "GAIN", false, 435},
		{"aum-node-gain-bypassed.json", "CHAN 1", "GAIN", true, 380},
		{"aum-node-plugin-bypassed.json", "kick", "", true, 380},
	} {
		st := parseCaptured(t, tc.file)
		ch, err := st.Channel(tc.strip)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		n, err := ch.Node("1")
		if err != nil || n.Empty || n.Bypassed != tc.bypassed || n.Label != tc.label || n.Frame.X != tc.x || !n.OnScreen {
			t.Errorf("%s: slot 1 = %+v, %v; want %q loaded, bypassed %v, at x %v", tc.file, n, err, tc.label, tc.bypassed, tc.x)
		}
		// The strip's other nodes read as they did: nothing else is bypassed,
		// and an empty source slot stays empty.
		for _, m := range ch.Nodes {
			if m.Slot == 1 {
				continue
			}
			if want := tc.strip == "CHAN 1" && m.Role == "source"; m.Empty != want || m.Bypassed {
				t.Errorf("%s: %s = %+v, want empty %v and not bypassed", tc.file, nodeName(m), m, want)
			}
		}
	}
	// The probe session's other strips: the brain's MIDI nodes and the
	// master's insert are loaded and on the wire.
	st := parseCaptured(t, "aum-node-plugin-bypassed.json")
	for _, ch := range st.Channels {
		for _, m := range ch.Nodes {
			if ch.Title != "kick" && (m.Empty || m.Bypassed) {
				t.Errorf("%q: %s = %+v", ch.Title, nodeName(m), m)
			}
		}
	}
	if got := st.Channels[1].nodeList(); got != "source, slot 1 bypassed, output \"A\"" {
		t.Errorf("kick's nodes = %s", got)
	}
	// A MIDI node has the same shape: in aum-menu-closing.json the brain's
	// second node sits 66 points left of its slot (x 118 in the
	// aum-plugin-window*.json trees of that session) and its inner button is
	// still at the slot. It read as an empty slot before.
	if got := parseCaptured(t, "aum-menu-closing.json").Channels[0].nodeList(); got != `midi, midi "▷︎ EIG" bypassed` {
		t.Errorf("the brain's nodes = %s", got)
	}
	if got := parseCaptured(t, "aum-plugin-window.json").Channels[0].nodeList(); got != `midi, midi "▷︎ EIG"` {
		t.Errorf("the brain's nodes on the wire = %s", got)
	}
}

// aum_node bypass and unbypass on AUM's captured trees: each swipe is read
// back from the node's place, so both verify (on AUM 1.4.8 the bypass
// answered "the node's bypass state did not change" before).
func TestAUMNodeBypassCaptured(t *testing.T) {
	onWire, bypassed := capturedTree(t, "aum-node-gain.json"), capturedTree(t, "aum-node-gain-bypassed.json")
	for _, tc := range []struct {
		action      string
		from, after json.RawMessage
		swipe, msg  string
	}{
		{"bypass", onWire, bypassed, `swipe slot 1 "GAIN" (472,258)->(382,258)`, `slot 1 "GAIN" of "CHAN 1" bypassed`},
		{"unbypass", bypassed, onWire, `swipe slot 1 "GAIN" (417,258)->(507,258)`, `slot 1 "GAIN" of "CHAN 1" back on`},
	} {
		w := ipadtest.New(tc.from)
		w.SetScreens(tc.from, tc.after)
		res, err := New(w.URL).AUMNode(context.Background(), AUMRequest{Action: tc.action, Channel: "1", Node: "GAIN"})
		w.Close()
		if err != nil || !res.Verified || res.Message != tc.msg || len(res.Steps) != 1 || res.Steps[0] != tc.swipe {
			t.Errorf("%s: %+v, %v", tc.action, res, err)
		}
	}
}
