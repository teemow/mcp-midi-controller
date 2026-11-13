package ipad

import (
	"fmt"
	"image"
	"math"
	"time"
)

// AUM keeps where a strip's fader sits among its inserts out of its tree:
// it draws it only, as the fader's wire, a light line from the strip's wire
// to the fader. The line leaves 5 points below the last insert above the
// fader, or 5 points above the first insert when every insert is post
// fader, and runs right for about 34 points before it bends to the fader.
// PRE FADER in a node's menu puts the fader right below that node, POST
// FADER right above it (seen on AUM 1.4.8).
const (
	faderWireGap = 5 // points between the wire and the insert's frame
	// faderWireFrom and faderWireTo are the part of the line that is
	// measured, in points right of the strip's wire.
	faderWireFrom, faderWireTo = 6, 28
	// faderWireLit is the share of that part a row of the line has lit.
	faderWireLit = 0.6
)

// FaderWire reads where the fader's wire leaves strip ch in a decoded
// screenshot with pngScale pixels per point: the number of inserts above it
// (0 = every insert is post fader, len(inserts) = every insert is pre
// fader). It reports false when the inserts are not all on screen or the
// screenshot shows no single such line.
func FaderWire(src image.Image, pngScale float64, ch AUMChannel) (int, bool) {
	if pngScale <= 0 {
		pngScale = 1
	}
	var ins []AUMNode
	axis := math.NaN()
	for _, n := range ch.Nodes {
		switch n.Role {
		case "insert":
			if !n.OnScreen {
				return 0, false
			}
			ins = append(ins, n)
		case "output":
			// Inserts sit on the strip's wire 41 points left of the
			// output node's centre (see parseNodes).
			cx, _ := n.Frame.Center()
			axis = cx - 41
		}
	}
	if len(ins) == 0 || math.IsNaN(axis) || !ch.OnScreen {
		return 0, false
	}
	found, at := 0, 0
	for k := 0; k <= len(ins); k++ {
		y := ins[0].Frame.Y - faderWireGap
		if k > 0 {
			f := ins[k-1].Frame
			y = f.Y + f.Height + faderWireGap
		}
		if litRow(src, pngScale, axis+faderWireFrom, axis+faderWireTo, y-2, y+2) >= faderWireLit {
			found++
			at = k
		}
	}
	return at, found == 1
}

// litRow is the largest share of light grey pixels in a pixel row from x0
// to x1 between y0 and y1 (all in points). The fader's wire is light grey;
// the mixer behind it is dark, and orange marks (the add-slot +1, a
// selection) are no wire.
func litRow(src image.Image, scale, x0, x1, y0, y1 float64) float64 {
	b := src.Bounds()
	px0, px1 := b.Min.X+int(math.Round(x0*scale)), b.Min.X+int(math.Round(x1*scale))
	best := 0.0
	for y := b.Min.Y + int(math.Floor(y0*scale)); y <= b.Min.Y+int(math.Ceil(y1*scale)); y++ {
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		lit, all := 0, 0
		for x := max(px0, b.Min.X); x < min(px1, b.Max.X); x++ {
			all++
			r, g, bl, _ := src.At(x, y).RGBA()
			r, g, bl = r>>8, g>>8, bl>>8
			luma := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
			if luma > 110 && max(r, g, bl)-min(r, g, bl) < 40 {
				lit++
			}
		}
		if all > 0 {
			best = max(best, float64(lit)/float64(all))
		}
	}
	return best
}

// faderWire takes a screenshot and reads the fader's wire of ch in it
// (FaderWire).
func (d *AUMDriver) faderWire(ch AUMChannel) (int, bool, error) {
	raw, err := d.s.c.screenshot(d.ctx)
	if err != nil {
		return 0, false, fmt.Errorf("read the fader's wire: screenshot: %w", err)
	}
	scr, err := d.s.c.ScreenInfo(d.ctx)
	if err != nil {
		return 0, false, fmt.Errorf("read the fader's wire: screen info: %w", err)
	}
	img, err := DecodePNG(raw)
	if err != nil {
		return 0, false, fmt.Errorf("read the fader's wire: %w", err)
	}
	k, ok := FaderWire(img, scr.Scale, ch)
	return k, ok, nil
}

// checkFaderNode reports why PRE FADER or POST FADER cannot be asked of n.
func checkFaderNode(item string, ch AUMChannel, n AUMNode) error {
	switch {
	case ch.Kind != "audio":
		return fmt.Errorf("%s: %q is a MIDI strip, it has no fader", item, ch.Title)
	case n.Role != "insert":
		return fmt.Errorf("%s places the fader among the inserts; %s is the strip's %s node", item, nodeName(n), n.Role)
	case n.Empty:
		return fmt.Errorf("%s works on a node; %s is empty", item, nodeName(n))
	}
	return nil
}

// wireAt says where the fader's wire leaves a strip with inserts slots
// when pre inserts lie above it.
func wireAt(pre, inserts int) string {
	switch {
	case pre <= 0:
		return "above slot 1"
	case pre >= inserts:
		return fmt.Sprintf("below slot %d, the last", inserts)
	}
	return fmt.Sprintf("between slot %d and slot %d", pre, pre+1)
}

// faderMoved checks PRE FADER or POST FADER, tapped in n's menu: the
// fader's wire must leave right below n (pre) or right above it (post).
// AUM leaves the menu open after either item, whether the wire moved or
// not, so it is closed here first, and the message says so when it stays
// open. was is where the wire left before the menu opened (wasOK false:
// not read).
func (d *AUMDriver) faderMoved(action string, ch AUMChannel, n AUMNode, was int, wasOK bool) (bool, string, error) {
	item := nodeMenuItems[action]
	want, side, state := n.Slot, "below", "pre fader"
	if action == "post_fader" {
		want, side, state = n.Slot-1, "above", "post fader"
	}
	inserts := 0
	for _, m := range ch.Nodes {
		if m.Role == "insert" {
			inserts++
		}
	}
	if err := d.Read(); err != nil {
		return false, "", err
	}
	open := ""
	if d.findText(item, nil) != nil {
		closed, err := d.closeTop()
		if err != nil {
			return false, "", err
		}
		if !closed.Verified {
			open = "; the menu is still open (aum_panel close closes it)"
		}
	}
	// The wire moves as the item is tapped; a few screenshots give a slow
	// redraw its time.
	var got int
	var ok bool
	deadline := time.Now().Add(aumWait)
	for {
		var err error
		if got, ok, err = d.faderWire(ch); err != nil {
			return false, "", err
		}
		if (ok && got == want) || time.Now().After(deadline) {
			break
		}
		if err := d.pause(300 * time.Millisecond); err != nil {
			return false, "", err
		}
	}
	name := fmt.Sprintf("%s of %q", nodeName(n), ch.Title)
	switch {
	case !ok:
		return false, fmt.Sprintf("%s tapped on %s, but the fader's wire could not be read in the screenshot (check with ipad_screenshot)%s", item, name, open), nil
	case got != want:
		return false, fmt.Sprintf("%s tapped on %s, but the fader's wire leaves %s, not right %s it%s", item, name, wireAt(got, inserts), side, open), nil
	case wasOK && was == want:
		return true, fmt.Sprintf("%s: %s was %s already: the fader's wire leaves right %s it (%s)%s", item, name, state, side, wireAt(got, inserts), open), nil
	}
	from := ""
	if wasOK {
		from = ", was " + wireAt(was, inserts)
	}
	return true, fmt.Sprintf("%s: %s is %s: the fader's wire leaves right %s it (%s%s)%s", item, name, state, side, wireAt(got, inserts), from, open), nil
}
