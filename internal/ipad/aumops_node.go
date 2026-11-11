package ipad

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AUMNodeActions are aum_node's actions.
var AUMNodeActions = []string{
	"open", "menu", "select", "replace", "reload", "pre_fader", "post_fader", "swap",
	"copy_here", "move_here", "add_slot", "remove_slot", "bypass", "unbypass", "eject",
	"move", "meter", "name", "window_close", "window_mode",
}

// nodeMenuItems maps actions to the long-press menu items they tap.
var nodeMenuItems = map[string]string{
	"select": "SELECT", "replace": "REPLACE", "reload": "RELOAD", "pre_fader": "PRE FADER",
	"post_fader": "POST FADER", "swap": "SWAP", "copy_here": "COPY HERE", "move_here": "MOVE HERE",
	"remove_slot": "REMOVE SLOT",
}

// AUMNode runs a node or slot operation.
func (c *Client) AUMNode(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		var err error
		switch r.Action {
		case "window_close", "window_mode":
			res, err = d.pluginWindow(r)
			return err
		case "eject":
			if err := needConfirm(r, "eject removes the node and its state"); err != nil {
				return err
			}
		}
		ch, n, err := d.node(r.Channel, r.Node, r.Action == "add_slot")
		if err != nil {
			return err
		}
		switch r.Action {
		case "open":
			res, err = d.openNode(ch, n)
		case "menu":
			var o *AUMOverlay
			if o, err = d.nodeMenu(ch, n); err == nil {
				res = d.result("menu", o != nil, verdict(o != nil, "menu: "+strings.Join(o.menuItems(), ", "), "no menu appeared"), o)
			}
		case "select", "replace", "reload", "pre_fader", "post_fader", "swap", "copy_here", "move_here", "remove_slot":
			res, err = d.nodeMenuAction(r.Action, ch, n)
		case "add_slot":
			res, err = d.addSlot(ch)
		case "bypass", "unbypass":
			res, err = d.bypassNode(r.Action, ch, n)
		case "eject":
			res, err = d.ejectNode(ch, n)
		case "move":
			res, err = d.moveNode(ch, n, r.Steps)
		case "meter":
			res, err = d.meterNode(ch, n)
		case "name":
			res, err = d.nameNode(ch, n, r.Name)
		default:
			err = fmt.Errorf("unknown aum_node action %q (%s)", r.Action, strings.Join(AUMNodeActions, ", "))
		}
		return err
	})
	return res, err
}

// node finds a strip (bringing it on screen) and one of its nodes.
func (d *AUMDriver) node(chSel, nodeSel string, stripOnly bool) (AUMChannel, AUMNode, error) {
	ch, err := d.channel(chSel)
	if err != nil {
		return ch, AUMNode{}, err
	}
	if stripOnly {
		return ch, AUMNode{}, nil
	}
	n, err := ch.Node(nodeSel)
	if err != nil {
		return ch, n, err
	}
	if !n.OnScreen {
		return ch, n, fmt.Errorf("%s of %q is scrolled out of the insert column; scroll it with ipad_swipe on the column's empty part first", nodeName(n), ch.Title)
	}
	return ch, n, nil
}

func nodeName(n AUMNode) string {
	name := n.Role
	if n.Role == "insert" {
		name = fmt.Sprintf("slot %d", n.Slot)
	}
	if n.Label != "" {
		name += " " + strconv.Quote(n.Label)
	}
	if n.Empty {
		name += " (empty)"
	}
	return name
}

// sameNode finds n again in a fresh read of strip.
func sameNode(st AUMState, strip int, n AUMNode) (AUMNode, bool) {
	if strip > len(st.Channels) {
		return AUMNode{}, false
	}
	for _, m := range st.Channels[strip-1].Nodes {
		if m.Role == n.Role && m.Slot == n.Slot && (n.Role != "midi" || m.Index == n.Index) {
			return m, true
		}
	}
	return AUMNode{}, false
}

// openNode taps a node: a plugin opens its window, a built-in node its
// popover, an output node Destinations, an empty slot the node picker.
func (d *AUMDriver) openNode(ch AUMChannel, n AUMNode) (AUMResult, error) {
	before := d.St
	if err := d.tapRect(n.Frame, nodeName(n)+" of "+strconv.Quote(ch.Title)); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		return len(st.Overlays) > len(before.Overlays) || len(st.PluginWindows) > len(before.PluginWindows)
	})
	if err != nil {
		return AUMResult{}, err
	}
	switch {
	case len(d.St.PluginWindows) > len(before.PluginWindows):
		w := d.St.PluginWindows[len(d.St.PluginWindows)-1]
		return d.result("open", true, fmt.Sprintf("plugin window %q open (avoid reading the tree while a plugin window is open if WebDriverAgent stalls)", w.Title), w), nil
	case ok:
		o := d.St.Overlay("")
		return d.result("open", true, fmt.Sprintf("%s %q open", o.Kind, o.Title), o), nil
	}
	return d.result("open", false, "nothing new opened (a plugin window may be hidden: Navigator long-press or a mixer double-tap shows hidden windows)", nil), nil
}

// nodeMenu long-presses a node or slot and waits for its menu (POST FADER,
// PRE FADER, SELECT, REPLACE, RELOAD; SWAP while another node is
// selected; REMOVE SLOT, COPY HERE, MOVE HERE on empty slots).
func (d *AUMDriver) nodeMenu(ch AUMChannel, n AUMNode) (*AUMOverlay, error) {
	if err := d.hold(n.Frame, nodeName(n)+" of "+strconv.Quote(ch.Title)); err != nil {
		return nil, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		return d.findText("PRE FADER", nil) != nil || d.findText("POST FADER", nil) != nil || d.findText("SELECT", nil) != nil
	})
	if err != nil || !ok {
		return nil, err
	}
	o := d.St.Overlay("popover")
	if o == nil {
		o = &AUMOverlay{Kind: "menu", Frame: Rect{Width: d.win.Width, Height: d.win.Height}}
		for _, label := range []string{"POST FADER", "PRE FADER", "SELECT", "SWAP", "REPLACE", "RELOAD", "REMOVE SLOT", "COPY HERE", "MOVE HERE"} {
			if d.findText(label, nil) != nil {
				o.Buttons = append(o.Buttons, label)
			}
		}
	}
	return o, nil
}

// menuItems lists the long-press menu items an overlay offers.
func (o *AUMOverlay) menuItems() []string {
	known := map[string]bool{}
	for _, v := range nodeMenuItems {
		known[v] = true
	}
	var out []string
	add := func(s string) {
		if known[strings.ToUpper(s)] {
			out = append(out, strings.ToUpper(s))
			known[strings.ToUpper(s)] = false
		}
	}
	for _, b := range o.Buttons {
		add(b)
	}
	for _, r := range o.Rows {
		add(r.Label)
	}
	return out
}

func (d *AUMDriver) nodeMenuAction(action string, ch AUMChannel, n AUMNode) (AUMResult, error) {
	item := nodeMenuItems[action]
	switch action {
	case "copy_here", "move_here", "remove_slot":
		if !n.Empty {
			return AUMResult{}, fmt.Errorf("%s works on an empty slot; %s is not empty", item, nodeName(n))
		}
	default:
		if n.Empty {
			return AUMResult{}, fmt.Errorf("%s works on a node; %s is empty", item, nodeName(n))
		}
	}
	o, err := d.nodeMenu(ch, n)
	if err != nil {
		return AUMResult{}, err
	}
	if o == nil {
		return d.result(action, false, "no menu appeared on "+nodeName(n), nil), nil
	}
	if d.findText(item, nil) == nil {
		hint := ""
		switch action {
		case "copy_here", "move_here", "swap":
			hint = " (SELECT a node first)"
		case "remove_slot":
			hint = " (a strip keeps at least one slot)"
		}
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
		return d.result(action, false, fmt.Sprintf("the menu offers no %s%s: %s", item, hint, strings.Join(o.menuItems(), ", ")), nil), nil
	}
	before := d.St
	if err := d.tapText(item, nil); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	var ok bool
	var msg string
	switch action {
	case "copy_here", "move_here":
		ok, err = d.until(aumWait, func(st AUMState) bool {
			m, found := sameNode(st, strip, n)
			return found && !m.Empty
		})
		msg = verdict(ok, fmt.Sprintf("%s: %s of %q now holds a node", item, nodeName(n), ch.Title), "the slot is still empty")
	case "remove_slot":
		ok, err = d.until(aumWait, func(st AUMState) bool { return len(st.Channels[strip-1].Nodes) < len(before.Channels[strip-1].Nodes) })
		msg = verdict(ok, fmt.Sprintf("removed %s of %q", nodeName(n), ch.Title), "the slot is still there")
	case "replace":
		var pick *AUMOverlay
		pick, err = d.waitOverlay("", aumWait)
		ok = pick != nil
		msg = "the node picker did not open"
		if ok {
			msg = fmt.Sprintf("node picker %q open: tap the replacement by label (ipad_tap), or close it with aum_panel close", pick.Title)
		}
	case "swap":
		ok, err = d.until(aumWait, func(st AUMState) bool {
			m, found := sameNode(st, strip, n)
			return found && m.Label != n.Label
		})
		msg = verdict(ok, fmt.Sprintf("swapped into %s of %q", nodeName(n), ch.Title), "the node did not change (labels may match; check with ipad_screenshot)")
	default:
		// SELECT, RELOAD, PRE/POST FADER leave no trace in the tree: the menu
		// closing is all there is to see.
		ok, err = d.until(aumWait, func(st AUMState) bool { return d.findText(item, nil) == nil })
		msg = verdict(ok, fmt.Sprintf("%s on %s of %q (no further state in AUM's tree; the fader wire and selection glow show in ipad_screenshot)", item, nodeName(n), ch.Title), "the menu did not close")
	}
	return d.result(action, ok, msg, nil), err
}

// addSlot taps the visible orange +1 of a strip, or double-taps the empty
// area right below its last insert.
func (d *AUMDriver) addSlot(ch AUMChannel) (AUMResult, error) {
	if ch.Kind != "audio" {
		return AUMResult{}, fmt.Errorf("add_slot works on audio strips; %q is a MIDI strip (tap its last node's slot)", ch.Title)
	}
	inserts := 0
	var last *AUMNode
	for i, n := range ch.Nodes {
		if n.Role == "insert" {
			inserts++
			last = &ch.Nodes[i]
		}
	}
	switch {
	case ch.AddSlot != nil:
		if err := d.tapRect(*ch.AddSlot, "+1"); err != nil {
			return AUMResult{}, err
		}
	case last != nil && last.OnScreen:
		x, _ := last.Frame.Center()
		if err := d.doubleTap(x, last.Frame.Y+last.Frame.Height+42, "below the last insert"); err != nil {
			return AUMResult{}, err
		}
	default:
		return AUMResult{}, fmt.Errorf("no +1 and no visible last insert on %q; scroll the insert column first", ch.Title)
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		k := 0
		for _, n := range st.Channels[strip-1].Nodes {
			if n.Role == "insert" {
				k++
			}
		}
		return k > inserts
	})
	return d.result("add_slot", ok, verdict(ok, fmt.Sprintf("%q now has %d insert slots", ch.Title, inserts+1), "no slot was added"), nil), err
}

// bypassNode swipes a node 90 points left (bypass: it slides off the wire
// and shows ⏏) or right (un-bypass).
func (d *AUMDriver) bypassNode(action string, ch AUMChannel, n AUMNode) (AUMResult, error) {
	if n.Empty {
		return AUMResult{}, fmt.Errorf("%s is empty", nodeName(n))
	}
	want := action == "bypass"
	if n.Bypassed == want {
		return d.result(action, true, fmt.Sprintf("%s of %q is already %sbypassed", nodeName(n), ch.Title, map[bool]string{true: "", false: "not "}[want]), nil), nil
	}
	x, y := n.Frame.Center()
	dx := -90.0
	if !want {
		dx = 90
	}
	if err := d.swipe(x, y, x+dx, y, nodeName(n)); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		m, found := sameNode(st, strip, n)
		return found && m.Bypassed == want
	})
	return d.result(action, ok, verdict(ok, fmt.Sprintf("%s of %q %s", nodeName(n), ch.Title, map[bool]string{true: "bypassed", false: "back on"}[want]), "the node's bypass state did not change"), nil), err
}

// ejectNode bypasses the node (if needed) and taps the ⏏ that appears where
// it sat; with Settings → Confirm Eject on it answers OK.
func (d *AUMDriver) ejectNode(ch AUMChannel, n AUMNode) (AUMResult, error) {
	if n.Empty {
		return AUMResult{}, fmt.Errorf("%s is already empty", nodeName(n))
	}
	if n.Role == "output" {
		return AUMResult{}, fmt.Errorf("the output node cannot be ejected; tap it for Destinations")
	}
	axisX, cy := n.Frame.Center()
	if n.Bypassed {
		axisX += 55
	} else {
		x, y := n.Frame.Center()
		if err := d.swipe(x, y, x-90, y, nodeName(n)); err != nil {
			return AUMResult{}, err
		}
		if _, err := d.until(aumWait, func(st AUMState) bool {
			m, found := sameNode(st, ch.Strip, n)
			return found && m.Bypassed
		}); err != nil {
			return AUMResult{}, err
		}
	}
	if err := d.tap(axisX, cy, "eject ⏏"); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		if st.Overlay("alert") != nil {
			return true
		}
		m, found := sameNode(st, strip, n)
		return !found || m.Empty
	})
	if err != nil {
		return AUMResult{}, err
	}
	if a := d.St.Overlay("alert"); a != nil {
		if err := d.tapText("OK", &a.Frame); err != nil {
			return AUMResult{}, err
		}
		ok, err = d.until(aumWait, func(st AUMState) bool {
			m, found := sameNode(st, strip, n)
			return st.Overlay("alert") == nil && (!found || m.Empty)
		})
		if err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("eject", ok, verdict(ok, fmt.Sprintf("ejected %s of %q", nodeName(n), ch.Title), "the node is still there"), nil), nil
}

// moveNode presses a node, moves 90 points right and holds (AUM shows its
// DSP % and latency), then moves 84 points per slot up (steps < 0) or down
// and releases: the node moves to that slot.
func (d *AUMDriver) moveNode(ch AUMChannel, n AUMNode, steps int) (AUMResult, error) {
	if steps == 0 {
		return AUMResult{}, fmt.Errorf("move needs steps: slots to move, negative = up")
	}
	if n.Empty || n.Role != "insert" {
		return AUMResult{}, fmt.Errorf("move reorders inserts; %s is not one", nodeName(n))
	}
	x, y := n.Frame.Center()
	dy := float64(steps) * 84
	if err := d.drag(nodeName(n), DragTiming{Hold: 600 * time.Millisecond},
		Point{x, y}, Point{x + 90, y}, Point{x + 90, y + dy}); err != nil {
		return AUMResult{}, err
	}
	target := n.Slot + steps
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		for _, m := range st.Channels[strip-1].Nodes {
			if m.Role == "insert" && m.Slot == target {
				return m.Label == n.Label && !m.Empty
			}
		}
		return false
	})
	msg := verdict(ok, fmt.Sprintf("%q moved to slot %d of %q", n.Label, target, ch.Title), fmt.Sprintf("slot %d does not hold %q", target, n.Label))
	if n.Label == "" {
		msg = fmt.Sprintf("dragged slot %d by %d; the node has no label to check (ipad_screenshot)", n.Slot, steps)
		ok = false
	}
	return d.result("move", ok, msg, nil), err
}

// meterNode taps the three dots right of a node: the main meter then shows
// that point (e.g. "Ch 2 IN").
func (d *AUMDriver) meterNode(ch AUMChannel, n AUMNode) (AUMResult, error) {
	dots := d.find(func(e *Element) bool {
		r := e.Rect
		return e.Type == "Button" && e.Label == "" && !e.Hidden && r.Width < 50 && r.Height > r.Width*1.8 &&
			math.Abs(r.X-(n.Frame.X+n.Frame.Width)) <= 12 && math.Abs(r.MidY()-n.Frame.MidY()) <= 15
	})
	x, y := n.Frame.X+n.Frame.Width+12, n.Frame.MidY()
	if dots != nil {
		x, y = dots.Rect.Center()
	}
	was := d.St.MeterSource
	if err := d.tap(x, y, "the meter dots of "+nodeName(n)); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return st.MeterSource != was })
	return d.result("meter", ok, fmt.Sprintf("main meter %q -> %q", was, d.St.MeterSource), nil), err
}

// pluginWindow closes the front plugin window (its ✕, the last title-bar
// button) or opens its window-mode menu (the button before ✕), then taps
// the mode named in r.Value (DEFAULT, CUSTOM, MAXIMIZED, FULLSCREEN).
func (d *AUMDriver) pluginWindow(r AUMRequest) (AUMResult, error) {
	if len(d.St.PluginWindows) == 0 {
		return AUMResult{}, fmt.Errorf("no plugin window open")
	}
	w := d.St.PluginWindows[len(d.St.PluginWindows)-1]
	if len(w.Bar) < 2 {
		return AUMResult{}, fmt.Errorf("plugin window %q: title bar buttons not found", w.Title)
	}
	switch r.Action {
	case "window_close":
		if err := d.tapRect(w.Bar[len(w.Bar)-1], "✕ of "+strconv.Quote(w.Title)); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool {
			for _, x := range st.PluginWindows {
				if x.Title == w.Title {
					return false
				}
			}
			return true
		})
		return d.result("window_close", ok, verdict(ok, "closed "+w.Title, w.Title+" is still open"), nil), err
	}
	mode := strings.ToUpper(strings.TrimSpace(r.Value))
	switch mode {
	case "DEFAULT", "CUSTOM", "MAXIMIZED", "FULLSCREEN":
	default:
		return AUMResult{}, fmt.Errorf("window_mode needs value: DEFAULT, CUSTOM, MAXIMIZED or FULLSCREEN")
	}
	if err := d.tapRect(w.Bar[len(w.Bar)-2], "window mode of "+strconv.Quote(w.Title)); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return d.windowModeItem(mode) != nil })
	if err != nil {
		return AUMResult{}, err
	}
	if !ok {
		return d.result("window_mode", false, "the window-mode menu did not open", nil), nil
	}
	if err := d.tapRect(d.windowModeItem(mode).Rect, mode); err != nil {
		return AUMResult{}, err
	}
	ok, err = d.until(aumWait, func(st AUMState) bool { return d.windowModeItem(mode) == nil })
	return d.result("window_mode", ok, verdict(ok, fmt.Sprintf("%q set to %s", w.Title, mode), "the menu did not close"), nil), err
}

// windowModeItem finds mode in the open window-mode menu. AUM marks the
// window's current mode with ✓ and the default one with ‧ ("MAXIMIZED ✓",
// "DEFAULT ‧"), so an item is matched by its first word.
func (d *AUMDriver) windowModeItem(mode string) *Element {
	pop := d.St.Overlay("popover")
	if pop == nil {
		return nil
	}
	return d.find(func(e *Element) bool {
		f := strings.Fields(e.Text())
		if e.Type != "Button" || e.Hidden || e.Rect.Empty() || len(f) == 0 || !strings.EqualFold(f[0], mode) {
			return false
		}
		cx, cy := e.Rect.Center()
		return pop.Frame.Contains(cx, cy)
	})
}

// nameNode sets the custom short name of a bus or MIDI-bus node: its
// popover (MIX BUS A NAME & COLOR, MIDI BUS NAME & COLOR) has a name field
// and SAVE. The name applies to every node of that bus.
func (d *AUMDriver) nameNode(ch AUMChannel, n AUMNode, name string) (AUMResult, error) {
	if name == "" {
		return AUMResult{}, fmt.Errorf("name needs name: the bus's short name")
	}
	if err := d.tapRect(n.Frame, nodeName(n)+" of "+strconv.Quote(ch.Title)); err != nil {
		return AUMResult{}, err
	}
	pop, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if pop == nil || len(pop.Fields) == 0 || !strings.Contains(strings.ToUpper(pop.Title), "NAME") {
		if pop != nil {
			if _, err := d.closeTop(); err != nil {
				return AUMResult{}, err
			}
		}
		return d.result("name", false, fmt.Sprintf("%s opened no NAME & COLOR popover (only bus and MIDI-bus nodes have one)", nodeName(n)), pop), nil
	}
	if _, err := d.typeText(0, name); err != nil {
		return AUMResult{}, err
	}
	if b := d.findText("SAVE", &pop.Frame); b != nil {
		if err := d.tapRect(b.Rect, "SAVE"); err != nil {
			return AUMResult{}, err
		}
	} else if err := d.pressReturn(); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		m, found := sameNode(st, strip, n)
		return found && strings.EqualFold(m.Label, name)
	})
	if err != nil {
		return AUMResult{}, err
	}
	if d.St.Overlay("popover") != nil {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("name", ok, verdict(ok, fmt.Sprintf("%s of %q now %q", n.Role, ch.Title, name), "the node's label did not change to "+name), nil), nil
}
