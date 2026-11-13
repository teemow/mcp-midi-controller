package ipad

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// The aum-fader-wire*.json trees and aum-fader-wire-<n>.png crops come from
// a scratch session on AUM 1.4.8: one strip, CHAN 1, with AUM's built-in
// Stereo Balance (BAL), Hard Clip (CLIP) and Gain (GAIN) as inserts. The
// trees are the bare mixer and the node menu that PRE FADER on BAL left
// open. Each crop is the strip's wire, x 472 to 508 and y 205 to 480
// points of a screenshot (2 pixels per point), with the fader's wire below
// n inserts; the one with 2 was taken with GAIN's menu still open.

// capturedWire puts crop aum-fader-wire-<pre>.png back where it was taken,
// on a screen of AUM's dark mixer.
func capturedWire(t *testing.T, pre string) *image.RGBA {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "aum-fader-wire-"+pre+".png"))
	if err != nil {
		t.Fatal(err)
	}
	crop, err := DecodePNG(raw)
	if err != nil {
		t.Fatal(err)
	}
	screen := image.NewRGBA(image.Rect(0, 0, 2420, 1668))
	draw.Draw(screen, screen.Bounds(), &image.Uniform{C: color.RGBA{32, 32, 32, 255}}, image.Point{}, draw.Src)
	at := image.Pt(944, 410)
	draw.Draw(screen, crop.Bounds().Add(at), crop, crop.Bounds().Min, draw.Src)
	return screen
}

// AUM draws where the fader sits among the inserts only as the fader's
// wire: FaderWire reads it from AUM's own pixels, for every place it can
// take on a strip with three inserts.
func TestFaderWireCaptured(t *testing.T) {
	st := parseCaptured(t, "aum-fader-wire.json")
	if len(st.Channels) != 1 || st.Channels[0].Title != "CHAN 1" {
		t.Fatalf("channels = %+v", st.Channels)
	}
	ch := st.Channels[0]
	for pre, name := range []string{"0", "1", "2", "3"} {
		if got, ok := FaderWire(capturedWire(t, name), 2, ch); !ok || got != pre {
			t.Errorf("crop %s: fader's wire below %d inserts (%v), want %d", name, got, ok, pre)
		}
	}

	// A screen without the wire, one with two light rows, and one whose
	// line is orange (the add-slot +1, a selection) tell nothing.
	bare := capturedWire(t, "1")
	draw.Draw(bare, image.Rect(944, 590, 1016, 610), &image.Uniform{C: color.RGBA{32, 32, 32, 255}}, image.Point{}, draw.Src)
	if got, ok := FaderWire(bare, 2, ch); ok {
		t.Errorf("no wire: read below %d inserts", got)
	}
	two := capturedWire(t, "1")
	draw.Draw(two, image.Rect(956, 768, 1000, 770), &image.Uniform{C: color.RGBA{190, 190, 190, 255}}, image.Point{}, draw.Src)
	if got, ok := FaderWire(two, 2, ch); ok {
		t.Errorf("two wires: read below %d inserts", got)
	}
	orange := capturedWire(t, "0")
	draw.Draw(orange, image.Rect(944, 420, 1016, 446), &image.Uniform{C: color.RGBA{32, 32, 32, 255}}, image.Point{}, draw.Src)
	draw.Draw(orange, image.Rect(956, 432, 1000, 434), &image.Uniform{C: color.RGBA{255, 140, 30, 255}}, image.Point{}, draw.Src)
	if got, ok := FaderWire(orange, 2, ch); ok {
		t.Errorf("orange line: read below %d inserts", got)
	}

	// An insert scrolled out of the column hides where the wire may leave.
	hidden := ch
	hidden.Nodes = append([]AUMNode{}, ch.Nodes...)
	hidden.Nodes[3].OnScreen = false
	if _, ok := FaderWire(capturedWire(t, "3"), 2, hidden); ok {
		t.Error("read the wire of a strip with an insert off screen")
	}
}

// AUM leaves a node's menu open after PRE FADER (and POST FADER), whether
// the fader moved or not: on its own trees, pre_fader closes the menu and
// answers by the fader's wire.
func TestAUMNodePreFaderCapturedTrees(t *testing.T) {
	bare := capturedTree(t, "aum-fader-wire.json")
	menu := capturedTree(t, "aum-fader-wire-menu.json")
	w := ipadtest.New(bare)
	t.Cleanup(w.Close)
	// The long-press opens the menu, PRE FADER leaves it, the tap outside
	// closes it.
	w.SetScreens(bare, menu, menu, bare)
	// The fader's wire right below BAL (5 points below its frame, 221+74).
	w.Wires = [][4]float64{{472, 299.5, 34, 1}}
	c := New(w.URL)
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "pre_fader", Channel: "CHAN 1", Node: "BAL"})
	if err != nil || !res.Verified {
		t.Fatalf("pre_fader: %+v, %v", res, err)
	}
	if !strings.Contains(res.Message, "was pre fader already") || !strings.Contains(res.Message, "between slot 1 and slot 2") {
		t.Fatalf("pre_fader: %s", res.Message)
	}
	// The long-press on BAL, PRE FADER at its button's centre, and the tap
	// outside the menu.
	if g := w.GestureLog(); len(g) != 3 || g[0] != "hold 472,258" || g[1] != "tap 592.5,215" || !strings.HasPrefix(g[2], "tap ") {
		t.Fatalf("gestures = %v", g)
	}
	if len(res.State.Overlays) != 0 {
		t.Fatalf("menu left open: %+v", res.State.Overlays)
	}
}

// faderMixer is the stand-in's mixer with BAL, CLIP and GAIN as strip 2's
// inserts, centred at x 358, y 258, 342 and 426.
func faderMixer() ipadtest.AUM {
	a := mixer()
	a.Strips[1].Inserts = []ipadtest.Node{{Label: "BAL"}, {Label: "CLIP"}, {Label: "GAIN"}}
	return a
}

func TestAUMNodePreAndPostFader(t *testing.T) {
	w := ipadtest.New(faderMixer().Source())
	t.Cleanup(w.Close)
	f := ipadtest.NewFaderMenu(w, faderMixer())
	c := New(w.URL)
	for _, tc := range []struct {
		action, node string
		pre          int
		says         string
	}{
		// AUM changes nothing for a node that is where the item puts it.
		{"pre_fader", "GAIN", 3, "was pre fader already: the fader's wire leaves right below it (below slot 3, the last)"},
		{"post_fader", "GAIN", 2, "is post fader: the fader's wire leaves right above it (between slot 2 and slot 3, was below slot 3, the last)"},
		// PRE FADER puts the fader right below the node, also for a node
		// that is pre fader already: the ones below it go post fader.
		{"pre_fader", "BAL", 1, "is pre fader: the fader's wire leaves right below it (between slot 1 and slot 2, was between slot 2 and slot 3)"},
		{"post_fader", "BAL", 0, "is post fader: the fader's wire leaves right above it (above slot 1, was between slot 1 and slot 2)"},
		// POST FADER puts it right above the node, also for one that is post
		// fader already.
		{"post_fader", "CLIP", 1, "is post fader: the fader's wire leaves right above it (between slot 1 and slot 2, was above slot 1)"},
		{"post_fader", "CLIP", 1, "was post fader already"},
	} {
		w.SetScreens()
		res, err := c.AUMNode(context.Background(), AUMRequest{Action: tc.action, Channel: "2", Node: tc.node})
		if err != nil || !res.Verified || !strings.Contains(res.Message, tc.says) {
			t.Fatalf("%s %s: %+v, %v", tc.action, tc.node, res.Message, err)
		}
		if got := f.PreFader(1); got != tc.pre {
			t.Fatalf("%s %s: %d inserts pre fader, want %d", tc.action, tc.node, got, tc.pre)
		}
		// The long-press, the item, and the tap outside the menu AUM left
		// open.
		if g := w.GestureLog(); len(g) != 3 || !strings.HasPrefix(g[0], "hold 358,") || !strings.HasPrefix(g[2], "tap ") || f.Open() || len(res.State.Overlays) != 0 {
			t.Fatalf("%s %s: gestures %v, menu open %v", tc.action, tc.node, g, f.Open())
		}
	}
}

// The answer is the fader's wire: one that does not leave right at the node
// or cannot be read is no proof, whatever the menu did.
func TestAUMNodePreFaderNotVerified(t *testing.T) {
	menu := faderMixer()
	menu.Overlays = []ipadtest.Overlay{ipadtest.NodeMenu}
	w, c := opFake(t, faderMixer(), menu, menu, faderMixer())
	w.Wires = [][4]float64{ipadtest.FaderWire(1, 3)}
	res, err := c.AUMNode(context.Background(), AUMRequest{Action: "post_fader", Channel: "2", Node: "GAIN"})
	if err != nil || res.Verified || !strings.Contains(res.Message, "leaves below slot 3, the last, not right above it") {
		t.Fatalf("wire unmoved: %+v, %v", res.Message, err)
	}
	if len(res.State.Overlays) != 0 {
		t.Fatalf("wire unmoved: menu left open: %+v", res.State.Overlays)
	}
	w.SetScreens(faderMixer().Source(), menu.Source(), menu.Source(), faderMixer().Source())
	w.Wires = nil
	res, err = c.AUMNode(context.Background(), AUMRequest{Action: "pre_fader", Channel: "2", Node: "BAL"})
	if err != nil || res.Verified || !strings.Contains(res.Message, "could not be read") {
		t.Fatalf("no wire: %+v, %v", res.Message, err)
	}

	// What has no fader, or is no insert, is refused before anything is
	// touched.
	for _, r := range []AUMRequest{
		{Action: "pre_fader", Channel: "1", Node: "source"},
		{Action: "post_fader", Channel: "2", Node: "source"},
		{Action: "pre_fader", Channel: "2", Node: "output"},
	} {
		w.SetScreens(faderMixer().Source())
		if _, err := c.AUMNode(context.Background(), r); err == nil || w.Gestures() != 0 {
			t.Errorf("%s on %s of %s: %v (%d gestures)", r.Action, r.Node, r.Channel, err, w.Gestures())
		}
	}
	w.SetScreens(mixer().Source())
	if _, err := c.AUMNode(context.Background(), AUMRequest{Action: "pre_fader", Channel: "2", Node: "2"}); err == nil || !strings.Contains(err.Error(), "empty") || w.Gestures() != 0 {
		t.Errorf("pre_fader on an empty slot: %v", err)
	}
}
