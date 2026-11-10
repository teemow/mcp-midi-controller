package ipad

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

func init() { aumWait = 600 * time.Millisecond }

// opFake serves screens in turn, one per gesture.
func opFake(t *testing.T, screens ...ipadtest.AUM) (*ipadtest.WDA, *Client) {
	t.Helper()
	srcs := make([]json.RawMessage, len(screens))
	for i, a := range screens {
		srcs[i] = a.Source()
	}
	w := ipadtest.New(srcs[0])
	w.SetScreens(srcs...)
	t.Cleanup(w.Close)
	return w, New(w.URL)
}

func mixer() ipadtest.AUM { return ipadtest.MixerAUM() }

func bptr(b bool) *bool { return &b }

func TestParseAUMStripsNodesAndOverlays(t *testing.T) {
	a := mixer()
	a.Strips = append(a.Strips[:2:2], ipadtest.Strip{Title: "my custom", Level: "+0.0 dB", Armed: true, Soloed: true,
		Inserts: []ipadtest.Node{{Label: "PAR", Bypassed: true}, {Label: "LIM"}}, AddSlot: true})
	a.PluginWindow = "Synth @A1"
	a.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "FILE PLAYER", Rows: [][2]string{{"Loop", "OFF"}, {"Sync", "ON"}}, Buttons: []string{"chevron"}}}
	root, err := ParseSource(a.Source())
	if err != nil {
		t.Fatal(err)
	}
	st := ParseAUM(root, Size{Width: 1210, Height: 834})
	if len(st.Channels) != 3 {
		t.Fatalf("channels = %+v", st.Channels)
	}
	c := st.Channels[2]
	if c.Title != "my custom" || c.Number != 0 || c.Kind != "audio" || *c.Armed != true || *c.Soloed != true || *c.Muted != false {
		t.Fatalf("custom strip = %+v", c)
	}
	if c.AddSlot == nil || len(c.Nodes) != 4 {
		t.Fatalf("nodes = %+v (add slot %v)", c.Nodes, c.AddSlot)
	}
	par, err := c.Node("PAR")
	if err != nil || !par.Bypassed || par.Slot != 1 || par.Empty {
		t.Fatalf("PAR = %+v, %v", par, err)
	}
	if lim, _ := c.Node("2"); lim.Label != "LIM" || lim.Bypassed {
		t.Fatalf("slot 2 = %+v", lim)
	}
	if out, _ := c.Node("output"); out.Label != "SPKR" || out.Empty {
		t.Fatalf("output = %+v", out)
	}
	if st.Channels[1].Nodes[2].Empty != true {
		t.Fatalf("empty slot not seen: %+v", st.Channels[1].Nodes)
	}
	if ch, err := st.Channel("#3"); err != nil || ch.Title != "my custom" {
		t.Fatalf("#3 = %+v, %v", ch, err)
	}
	if _, err := st.Channel("#9"); err == nil {
		t.Fatal("#9 accepted")
	}
	if st.AddTile == nil || st.AddTile.X != 7+3*242+46 || len(st.addMenu) != 0 {
		t.Fatalf("add tile = %+v %+v", st.AddTile, st.addMenu)
	}
	if len(st.PluginWindows) != 1 || st.PluginWindows[0].Title != "Synth @A1" || len(st.PluginWindows[0].Bar) != 6 {
		t.Fatalf("plugin windows = %+v", st.PluginWindows)
	}
	o := st.Overlay("popover")
	if o == nil || o.Title != "FILE PLAYER" {
		t.Fatalf("overlays = %+v", st.Overlays)
	}
	if r, ok := o.Row("sync"); !ok || r.Value != "ON" || r.ValueFrame == nil {
		t.Fatalf("Sync row = %+v %v (rows %+v)", r, ok, o.Rows)
	}
	if st.MeterSource != "SPKR OUT" {
		t.Fatalf("meter = %q", st.MeterSource)
	}
}

func TestAUMChannelMute(t *testing.T) {
	muted := mixer()
	muted.Strips[1].Muted = true
	w, c := opFake(t, mixer(), muted)
	res, err := c.AUMChannel(context.Background(), AUMRequest{Action: "mute", Channel: "2", On: bptr(true)})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "mute on") {
		t.Fatalf("mute: %+v, %v", res, err)
	}
	// MUTE of strip 2 sits at (249+166, 630) 30×30.
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 430,645" {
		t.Fatalf("gestures = %v", g)
	}
	// Already muted: nothing is tapped.
	w.SetScreens(muted.Source())
	res, err = c.AUMChannel(context.Background(), AUMRequest{Action: "mute", Channel: "Synth One", On: bptr(true)})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "already on") || w.Gestures() != 0 {
		t.Fatalf("idempotent mute: %+v, %v (%d gestures)", res, err, w.Gestures())
	}
	// A muted strip's MUTE covers SOLO.
	if _, err := c.AUMChannel(context.Background(), AUMRequest{Action: "solo", Channel: "2"}); err == nil || !strings.Contains(err.Error(), "unmute it first") {
		t.Fatalf("solo on muted strip: %v", err)
	}
}

func TestAUMChannelAddAndRemove(t *testing.T) {
	added := mixer()
	added.Strips = append(added.Strips[:4:4], ipadtest.Strip{Title: "CHAN 5", Level: "+0.0 dB"})
	base := mixer()
	base.Strips = base.Strips[:4]
	menu := base
	menu.AddMenu = true
	w, c := opFake(t, base, menu, added)
	res, err := c.AUMChannel(context.Background(), AUMRequest{Action: "add_audio"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, `"CHAN 5"`) {
		t.Fatalf("add_audio: %+v, %v", res, err)
	}
	// The tile (7+4·242+46, 372) 150×150, then AUDIO (its top 45 points).
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 1096,447" || g[1] != "tap 1096,394.5" {
		t.Fatalf("gestures = %v", g)
	}
	// When the open menu is not in the tree, MIDI is the tile's middle row,
	// right under the tap that opened it.
	w.SetScreens(base.Source(), base.Source(), added.Source())
	if res, err = c.AUMChannel(context.Background(), AUMRequest{Action: "add_midi"}); err != nil || !res.Verified {
		t.Fatalf("add_midi: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 2 || g[1] != "tap 1096,447" {
		t.Fatalf("add_midi gestures = %v", g)
	}

	// REMOVE CHANNEL needs confirm and taps twice.
	if _, err := c.AUMChannel(context.Background(), AUMRequest{Action: "remove", Channel: "CHAN 5"}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("remove without confirm: %v", err)
	}
	edit := added
	edit.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "EDIT CHANNEL 5", Fields: []string{"CHAN 5"}, Buttons: []string{"DUPLICATE", "REMOVE CHANNEL"}}}
	w.SetScreens(added.Source(), edit.Source(), edit.Source(), base.Source())
	res, err = c.AUMChannel(context.Background(), AUMRequest{Action: "remove", Channel: "CHAN 5", Confirm: true})
	if err != nil || !res.Verified || len(res.State.Channels) != 4 {
		t.Fatalf("remove: %+v, %v", res, err)
	}
	g := w.GestureLog()
	if len(g) != 3 || g[1] != g[2] || g[0] != "tap 1125,789" || g[1] != "tap 883,374" {
		t.Fatalf("remove gestures = %v", g)
	}
}

func TestAUMChannelRenameAndLevel(t *testing.T) {
	edit := mixer()
	edit.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "EDIT CHANNEL 2", Fields: []string{"2: Synth One"}, Buttons: []string{"DUPLICATE", "REMOVE CHANNEL"}}}
	renamed := mixer()
	renamed.Strips[1].Title = "Lead"
	w, c := opFake(t, mixer(), edit, edit, renamed)
	w.Fields = []string{"2: Synth One"}
	res, err := c.AUMChannel(context.Background(), AUMRequest{Action: "rename", Channel: "2", Name: "Lead"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, `-> "Lead"`) || w.Fields[0] != "Lead" {
		t.Fatalf("rename: %+v, %v (field %q)", res, err, w.Fields[0])
	}

	dlg := mixer()
	dlg.Overlays = []ipadtest.Overlay{{Kind: "alert", Title: "Set level", Fields: []string{"-1.0"}, Buttons: []string{"Cancel", "Set"}}}
	set := mixer()
	set.Strips[1].Level = "-6.5 dB"
	// The field is filled without a tap, and Set pressed as an element.
	w.SetScreens(mixer().Source(), dlg.Source(), set.Source())
	w.Fields = []string{"-1.0"}
	w.Labels = map[string]int{`button:"Set"`: 1}
	level := -6.5
	res, err = c.AUMChannel(context.Background(), AUMRequest{Action: "set_level", Channel: "2", LevelDB: &level})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "-1.0 dB -> -6.5 dB") || w.Fields[0] != "-6.5" {
		t.Fatalf("set_level: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 446,213" || g[1] != "click" {
		t.Fatalf("set_level gestures = %v", g)
	}
}

func TestAUMNodeBypassAndEject(t *testing.T) {
	byp := mixer()
	byp.Strips[1].Inserts[0].Bypassed = true
	w, c := opFake(t, mixer(), byp)
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "bypass", Channel: "2", Node: "LP"})
	if err != nil || !res.Verified || w.GestureLog()[0] != "actions" {
		t.Fatalf("bypass: %+v, %v", res, err)
	}
	if _, err := c.AUMNode(context.Background(), AUMRequest{Action: "eject", Channel: "2", Node: "LP"}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("eject without confirm: %v", err)
	}
	gone := mixer()
	gone.Strips[1].Inserts[0] = ipadtest.Node{Empty: true}
	w.SetScreens(byp.Source(), gone.Source())
	res, err = c.AUMNode(context.Background(), AUMRequest{Action: "eject", Channel: "2", Node: "1", Confirm: true})
	if err != nil || !res.Verified {
		t.Fatalf("eject: %+v, %v", res, err)
	}
	// Already bypassed: the ⏏ is tapped where the node sat on the wire.
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 358,258" {
		t.Fatalf("eject gestures = %v", g)
	}
}

func TestAUMNodeMenuAction(t *testing.T) {
	menu := mixer()
	menu.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "POST FADER", Buttons: []string{"POST FADER", "PRE FADER", "SELECT", "REPLACE", "RELOAD"}}}
	w, c := opFake(t, mixer(), menu, mixer())
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "pre_fader", Channel: "2", Node: "LP"})
	if err != nil || !res.Verified {
		t.Fatalf("pre_fader: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 2 || !strings.HasPrefix(g[0], "hold ") {
		t.Fatalf("gestures = %v", g)
	}
	if _, err := c.AUMNode(context.Background(), AUMRequest{Action: "copy_here", Channel: "2", Node: "LP"}); err == nil {
		t.Fatal("COPY HERE on a node accepted")
	}
}

func TestAUMTransportSetTempo(t *testing.T) {
	bar := mixer()
	bar.TempoBar = true
	dlg := bar
	dlg.Overlays = []ipadtest.Overlay{{Kind: "alert", Title: "Edit Value", Fields: []string{"120"}, Buttons: []string{"Cancel", "Set"}}}
	done := bar
	done.Tempo = "128"
	closed := mixer()
	closed.Tempo = "128"
	w, c := opFake(t, mixer(), bar, dlg, done, closed)
	w.Fields = []string{"120"}
	bpm := 128.0
	res, err := c.AUMTransport(context.Background(), AUMRequest{Action: "set_tempo", BPM: &bpm})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "120 BPM -> 128 BPM") {
		t.Fatalf("set_tempo: %+v, %v", res, err)
	}
	// No Set button among the fake's labels: Set is tapped at its place.
	g := w.GestureLog()
	if len(g) != 4 || g[0] != "tap 30,30" || g[1] != "doubleTap 168,33" || g[2] != "tap 605,475" || g[3] != "tap 30,32" {
		t.Fatalf("gestures = %v", g)
	}
	if _, err := c.AUMTransport(context.Background(), AUMRequest{Action: "record"}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("record without confirm: %v", err)
	}
}

func TestAUMPanelOpenAndClose(t *testing.T) {
	settings := mixer()
	settings.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "SETTINGS", Rows: [][2]string{{"Confirm Eject", "OFF"}}}}
	w, c := opFake(t, ipadtest.DefaultAUM(), settings)
	res, err := c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "settings"})
	if err != nil || !res.Verified {
		t.Fatalf("open settings: %+v, %v", res, err)
	}
	// SETTINGS' caption is at (965,397) 64×20: its icon is 32 points above.
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 997,365" {
		t.Fatalf("gestures = %v", g)
	}
	pop := mixer()
	pop.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "FILE PLAYER"}}
	w.SetScreens(pop.Source(), mixer().Source())
	res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "close"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "FILE PLAYER") {
		t.Fatalf("close: %+v, %v", res, err)
	}
	// Outside the popover (633,186 500×400): left of it.
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 316.5,386" {
		t.Fatalf("close gestures = %v", g)
	}
	if _, err := c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "nope"}); err == nil {
		t.Fatal("unknown panel accepted")
	}
}

func TestAUMSettingsSet(t *testing.T) {
	menu := ipadtest.DefaultAUM()
	off := mixer()
	off.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "SETTINGS", Rows: [][2]string{{"Grid Snap", "OFF"}, {"Confirm Eject", "OFF"}}}}
	on := off
	on.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "SETTINGS", Rows: [][2]string{{"Grid Snap", "OFF"}, {"Confirm Eject", "ON"}}}}
	w, c := opFake(t, menu, off, on)
	res, err := c.AUMSettings(context.Background(), AUMRequest{Action: "set", Setting: "confirm eject", Value: "ON"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Confirm Eject OFF -> ON") {
		t.Fatalf("set: %+v, %v", res, err)
	}
	// The value button of the second row: (1095, 70+50+7) 100×36.
	if g := w.GestureLog(); len(g) != 2 || g[1] != "tap 1145,145" {
		t.Fatalf("gestures = %v", g)
	}
}

func TestAUMMIDIRoutingConnect(t *testing.T) {
	rt := ipadtest.Routing{
		Sources:      [][2]string{{"Keyboard", "BUILT-IN"}, {"Net Session", "NETWORK"}},
		Destinations: [][2]string{{"MIDI Control", "BUILT-IN"}, {"Synth @A1", "AUDIO UNIT"}, {"Net Session", "NETWORK"}},
	}
	a := mixer()
	a.Routing = &rt
	w, c := opFake(t, a)
	w.Window, w.Scale = [2]float64{800, 600}, 1 // a small screenshot keeps -race fast
	w.BrightScreens = [][][4]float64{nil, {rt.Cell(0, 1)}}
	res, err := c.AUMMIDIRouting(context.Background(), AUMRequest{Action: "connect", Source: "keyboard", Destination: "synth"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Keyboard -> Synth @A1 connected") {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	// The cell: source column centre x 446, destination row centre y 487.
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 446,487" {
		t.Fatalf("gestures = %v", g)
	}
	res, err = c.AUMMIDIRouting(context.Background(), AUMRequest{Action: "read"})
	routing, _ := res.Data.(AUMRouting)
	if err != nil || len(routing.Sources) != 2 || len(routing.Destinations) != 3 || len(routing.Connections) != 1 {
		t.Fatalf("read: %+v, %v", res, err)
	}
	if _, err := c.AUMMIDIRouting(context.Background(), AUMRequest{Action: "connect", Source: "net", Destination: "nowhere"}); err == nil {
		t.Fatal("unknown destination accepted")
	}
}

func TestAUMMIDIControlMenuNeedsConfirm(t *testing.T) {
	mc := mixer()
	mc.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "MIDI CONTROL", Frame: &[4]float64{890, 0, 320, 834}, Rows: [][2]string{{"Channels", ""}}}}
	_, c := opFake(t, mc)
	_, err := c.AUMMIDIControl(context.Background(), AUMRequest{Action: "menu", Setting: "Channels", Value: "Clear All"})
	if !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("Clear All without confirm: %v", err)
	}
}

func TestAUMFilesGuards(t *testing.T) {
	_, c := opFake(t, ipadtest.DefaultAUM())
	for _, r := range []AUMRequest{
		{Action: "load", Name: "x"}, {Action: "save", Name: "x"}, {Action: "clear"}, {Action: "rename", Name: "a", NewName: "b"},
	} {
		if _, err := c.AUMFiles(context.Background(), r); !errors.Is(err, ErrNeedsConfirm) {
			t.Errorf("%s without confirm: %v", r.Action, err)
		}
	}
}

func TestExpectationNewFields(t *testing.T) {
	a := mixer()
	a.Strips[1].Inserts[0].Bypassed = true
	root, _ := ParseSource(a.Source())
	st := ParseAUM(root, Size{Width: 1210, Height: 834})
	n := 6
	ok := Expectation{
		StripCount: &n, NoOverlay: true,
		Channels: []ChannelExpectation{{Channel: "3", Muted: bptr(true)}, {Channel: "2", Nodes: []NodeExpectation{{Node: "LP", Bypassed: true}}}},
	}
	if m := ok.Mismatches(nil, st); len(m) != 0 {
		t.Fatalf("mismatches: %v", m)
	}
	bad := Expectation{Overlay: "SETTINGS", PluginWindow: "Synth", Channels: []ChannelExpectation{{Channel: "2", Soloed: bptr(true)}}}
	if m := bad.Mismatches(nil, st); len(m) != 3 {
		t.Fatalf("mismatches: %v", m)
	}
	if err := (Expectation{Overlay: "x", NoOverlay: true}).Validate(); err == nil {
		t.Fatal("contradiction accepted")
	}
	if err := (Expectation{Channels: []ChannelExpectation{{Channel: "2", Armed: bptr(false)}}}).Validate(); err != nil {
		t.Fatalf("armed-only expectation: %v", err)
	}
}

func TestAUMTransportPlay(t *testing.T) {
	at := func(pos string) ipadtest.AUM { a := mixer(); a.Position = pos; return a }
	// Stopped (two equal reads), then the position runs after the tap: a
	// running transport changes per read, not per gesture.
	w := ipadtest.New(at("001:1  00:00.0").Source())
	t.Cleanup(w.Close)
	w.SetSources(at("001:1  00:00.0").Source(), at("001:1  00:00.0").Source(), at("001:2  00:00.5").Source(), at("001:3  00:01.0").Source())
	c := New(w.URL)
	res, err := c.AUMTransport(context.Background(), AUMRequest{Action: "play"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "playing") {
		t.Fatalf("play: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 156,33" { // play: 67 points right of the position text
		t.Fatalf("gestures = %v", g)
	}
}

func TestAUMNodeAddSlot(t *testing.T) {
	more := mixer()
	more.Strips[1].Inserts = append(more.Strips[1].Inserts, ipadtest.Node{Empty: true})
	w, c := opFake(t, mixer(), more)
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "add_slot", Channel: "2"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "3 insert slots") {
		t.Fatalf("add_slot: %+v, %v", res, err)
	}
	// Double-tap 42 points below the last insert (slot 2 at y 305, 74 high).
	if g := w.GestureLog(); len(g) != 1 || g[0] != "doubleTap 358,421" {
		t.Fatalf("gestures = %v", g)
	}
}

func TestAUMFilesList(t *testing.T) {
	files := mixer()
	files.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "FILES", Rows: [][2]string{{"Sessions", ""}, {"Recordings", ""}}}}
	sessions := mixer()
	sessions.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "Sessions", Cells: [][2]string{{"scratch (mcp test)", "today"}, {"other", "yesterday"}}}}
	w, c := opFake(t, ipadtest.DefaultAUM(), files, sessions)
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "list"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "2 entries: scratch (mcp test), other") {
		t.Fatalf("list: %+v, %v", res, err)
	}
	if es, ok := res.Data.([]AUMFileEntry); !ok || len(es) != 2 || es[0].Detail != "today" {
		t.Fatalf("entries = %#v", res.Data)
	}
	// The FILES icon above its caption (965,169), then the Sessions row.
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 997,137" || g[1] != "tap 1026,95" {
		t.Fatalf("gestures = %v", g)
	}
}

func TestAUMFilePlayerSet(t *testing.T) {
	fp := ipadtest.Strip{Title: "4: File Player", Level: "+0.0 dB", PlayEnable: bptr(true)}
	base := mixer()
	base.Strips[3] = fp
	open := base
	open.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "FILE PLAYER", Rows: [][2]string{{"Selected file", ""}, {"Loop", "OFF"}}}}
	on := base
	on.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "FILE PLAYER", Rows: [][2]string{{"Selected file", ""}, {"Loop", "ON"}}}}
	_, c := opFake(t, base, open, on)
	res, err := c.AUMFilePlayer(context.Background(), AUMRequest{Action: "set", Channel: "4", Setting: "Loop", Value: "ON"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Loop OFF -> ON") {
		t.Fatalf("set loop: %+v, %v", res, err)
	}
}

func TestAUMPanelSetAndNodeName(t *testing.T) {
	clock := mixer()
	clock.TempoBar = true
	clock.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "CLOCK OPTIONS", Rows: [][2]string{{"Time Signature", "4 / 4"}, {"Pre-roll", "OFF"}}}}
	one := clock
	one.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "CLOCK OPTIONS", Rows: [][2]string{{"Time Signature", "4 / 4"}, {"Pre-roll", "1 bar"}}}}
	_, c := opFake(t, clock, one)
	res, err := c.AUMPanel(context.Background(), AUMRequest{Action: "set", Setting: "Pre-roll", Value: "1 bar"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Pre-roll OFF -> 1 bar") {
		t.Fatalf("set pre-roll: %+v, %v", res, err)
	}

	// Live, Pre-roll is a stepper (‹ OFF ›): › steps up, and a value the
	// row does not offer leaves it where it was.
	steps := func(vals ...string) []json.RawMessage {
		var out []json.RawMessage
		for _, v := range vals {
			a := mixer()
			a.TempoBar = true
			a.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "CLOCK OPTIONS", Steppers: true, Rows: [][2]string{{"Time Signature", "4 / 4"}, {"Pre-roll", v}}}}
			out = append(out, a.Source())
		}
		return out
	}
	w, c := opFake(t, clock)
	w.SetScreens(steps("OFF", "1 bar")...)
	res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "set", Setting: "Pre-roll", Value: "1 bar"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Pre-roll OFF -> 1 bar") {
		t.Fatalf("step pre-roll: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 1 || !strings.HasPrefix(g[0], "tap 1133,") {
		t.Fatalf("step gestures = %v", g)
	}
	w.SetScreens(steps("OFF", "1 bar", "2 bars", "2 bars", "1 bar", "OFF", "OFF")...)
	res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "set", Setting: "Pre-roll", Value: "4 bars"})
	if err != nil || res.Verified || !strings.Contains(res.Message, "values: OFF, 1 bar, 2 bars); left at OFF") {
		t.Fatalf("pre-roll 4 bars: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 6 || g[0] != g[2] || g[3] != g[5] || g[0] == g[3] {
		t.Fatalf("up and down gestures = %v", g)
	}
	_, bare := opFake(t, mixer())
	if _, err := bare.AUMPanel(context.Background(), AUMRequest{Action: "set", Setting: "x", Value: "y"}); err == nil {
		t.Fatal("set with nothing open accepted")
	}

	bus := mixer()
	bus.Strips[3].Inserts = []ipadtest.Node{{Label: "BUS A"}}
	pop := bus
	pop.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "MIX BUS A NAME & COLOR", Fields: []string{""}, Buttons: []string{"CUSTOMIZE NODE COLOR", "SAVE"}}}
	named := bus
	named.Strips[3].Inserts = []ipadtest.Node{{Label: "FX"}}
	w, c = opFake(t, bus, pop, pop, named)
	w.Fields = []string{""}
	res, err = c.AUMNode(context.Background(), AUMRequest{Action: "name", Channel: "4", Node: "1", Name: "FX"})
	if err != nil || !res.Verified || w.Fields[0] != "FX" {
		t.Fatalf("name: %+v, %v", res, err)
	}
}

// TestParseAUMLiveShapes covers shapes first seen on a live AUM: glued
// accessibility labels, the mixer marked invisible under a side panel, the
// Navigator's navigation bar, the keyboard and a bare long-press menu.
func TestParseAUMLiveShapes(t *testing.T) {
	parse := func(a ipadtest.AUM) AUMState {
		t.Helper()
		root, err := ParseSource(a.Source())
		if err != nil {
			t.Fatal(err)
		}
		return ParseAUM(root, Size{Width: 1210, Height: 834})
	}
	menu := ipadtest.DefaultAUM()
	st := parse(menu)
	if st.Panel() != "MENU" {
		t.Fatalf("panel = %q", st.Panel())
	}
	if m := st.Channels[2].Muted; m == nil || !*m {
		t.Fatalf("strip 3 under the menu: muted = %v", m)
	}
	if n := st.Channels[0].Nodes; len(n) != 1 || n[0].Label != "MIDI 1" {
		t.Fatalf("MIDI node = %+v", n)
	}

	settings := mixer()
	settings.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "SETTINGS", Rows: [][2]string{{"Sample Rate", "48 kHz"}}}}
	st = parse(settings)
	if o := st.Overlay("panel"); o == nil || strings.Join(o.Buttons, "|") != "48 kHz" {
		t.Fatalf("settings panel = %+v", o)
	}

	nav := mixer()
	nav.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "NAVIGATOR", NavBar: true, Frame: &[4]float64{938, 0, 272, 834}, Buttons: []string{"CHANNELS", "PLUGINS"}}}
	nav.Keyboard = true
	nav.Overlays = append(nav.Overlays, ipadtest.Overlay{Kind: "popover", Buttons: []string{"POST FADER", "PRE FADER"}})
	st = parse(nav)
	if st.Panel() != "NAVIGATOR" || !st.Keyboard {
		t.Fatalf("navigator %q, keyboard %v", st.Panel(), st.Keyboard)
	}
	if o := st.Overlay("popover"); o == nil || o.Title != "" || len(o.Buttons) != 2 {
		t.Fatalf("bare menu = %+v", o)
	}

	// A silent source node is labelled "-∞"; a dialog's field brings a
	// "Clear text" button.
	silent := mixer()
	silent.Strips[1].Source = "-∞"
	silent.Overlays = []ipadtest.Overlay{{Kind: "alert", Title: "Set level", Fields: []string{"-6"}, Buttons: []string{"Cancel", "Set"}}}
	st = parse(silent)
	if l := st.Channels[1].Nodes[0].Label; l != "" {
		t.Fatalf("silent source label = %q", l)
	}
	if o := st.Overlay("alert"); o == nil || strings.Join(o.Buttons, "|") != "Cancel|Set" {
		t.Fatalf("Set level = %+v", o)
	}
}

func TestCleanLabel(t *testing.T) {
	for in, want := range map[string]string{
		"-∞, DSP:  2 %, -∞, Arp, Arp":                        "Arp",
		"SESSION: Old, Copyright (c) Someone, DSP:  0 %, -∞": "",
		"-∞, EFFECT (AU)":                                    "EFFECT (AU)",
		"Bass, Lead":                                         "Bass, Lead",
		"+0.0 dB":                                            "+0.0 dB",
		"Synth Two @A1":                                      "Synth Two @A1",
		"Delay, 1/4, 50 %":                                   "Delay, 1/4",
	} {
		if got := cleanLabel(in); got != want {
			t.Errorf("cleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAUMPanelNavigatorAndKeyboard(t *testing.T) {
	menu := ipadtest.DefaultAUM()
	bare := mixer()
	nav := mixer()
	nav.Overlays = []ipadtest.Overlay{{Kind: "panel", Title: "NAVIGATOR", NavBar: true, Frame: &[4]float64{938, 0, 272, 834}}}
	w, c := opFake(t, menu, bare, nav, bare)
	res, err := c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "navigator"})
	if err != nil || !res.Verified {
		t.Fatalf("open navigator: %+v, %v", res, err)
	}
	// The menu closes first (its close icon), then the Navigator button.
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 1190,30" || g[1] != "tap 1085,33" {
		t.Fatalf("gestures = %v", g)
	}
	// It has no close icon: a tap on the mixer, in the gap between the
	// first two strips (7+228 .. 249), closes it.
	w.SetScreens(nav.Source(), bare.Source())
	res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "close"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "NAVIGATOR") {
		t.Fatalf("close navigator: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 1 || g[0] != "tap 242,447" {
		t.Fatalf("close gestures = %v", g)
	}

	keys := mixer()
	keys.Keyboard = true
	w, c = opFake(t, mixer(), keys, mixer())
	if res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "keyboard"}); err != nil || !res.Verified {
		t.Fatalf("open keyboard: %+v, %v", res, err)
	}
	if res, err = c.AUMPanel(context.Background(), AUMRequest{Action: "close"}); err != nil || !res.Verified || !strings.Contains(res.Message, "keyboard") {
		t.Fatalf("close keyboard: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 2 || g[0] != "tap 52,817" || g[1] != "tap 34,678" {
		t.Fatalf("HIDE gestures = %v", g)
	}
}
