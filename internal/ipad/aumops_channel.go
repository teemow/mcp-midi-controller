package ipad

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AUMChannelActions are aum_channel's actions.
var AUMChannelActions = []string{
	"add_audio", "add_midi", "restore", "import", "edit", "rename", "bookmark", "duplicate", "remove",
	"move", "set_level", "reset_level", "mute", "solo", "arm", "scroll_to",
}

// AUMChannel runs a channel operation.
func (c *Client) AUMChannel(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		// What the request lacks is answered before anything is touched.
		if err := checkChannel(r); err != nil {
			return err
		}
		if err := d.uncoverMixer(); err != nil {
			return err
		}
		var err error
		switch r.Action {
		case "add_audio", "add_midi":
			res, err = d.addChannel(strings.ToUpper(strings.TrimPrefix(r.Action, "add_")))
		case "restore":
			res, err = d.importChannels(r.Action, "")
		case "import":
			res, err = d.importChannels(r.Action, r.Name)
		case "edit":
			res, err = d.editChannel(r.Channel)
		case "rename":
			res, err = d.renameChannel(r)
		case "bookmark":
			res, err = d.bookmarkChannel(r.Channel)
		case "duplicate":
			res, err = d.duplicateChannel(r.Channel)
		case "remove":
			res, err = d.removeChannel(r.Channel)
		case "move":
			res, err = d.moveChannel(r.Channel, r.Target)
		case "set_level":
			res, err = d.setLevel(r.Channel, *r.LevelDB)
		case "reset_level":
			res, err = d.resetLevel(r.Channel)
		case "mute", "solo", "arm":
			res, err = d.toggleStrip(r.Action, r.Channel, r.On)
		case "scroll_to":
			ch, err2 := d.channel(r.Channel)
			if err2 != nil {
				return err2
			}
			msg := fmt.Sprintf("strip %d %q is on screen at x %.0f", ch.Strip, ch.Title, ch.Frame.X)
			if s := d.St.MixerScroll; s != nil && *s >= 0.5 {
				// Scrolled from its first strip, the tree counts the strips
				// from the window's left edge, not as "#n" does.
				msg = fmt.Sprintf("%q is on screen at x %.0f", ch.Title, ch.Frame.X)
			}
			res = d.result(r.Action, ch.OnScreen, msg, nil)
		}
		return err
	})
	return res, err
}

// checkChannel reports what a request lacks for its action.
func checkChannel(r AUMRequest) error {
	switch {
	case !slices.Contains(AUMChannelActions, r.Action):
		return fmt.Errorf("unknown aum_channel action %q (%s)", r.Action, strings.Join(AUMChannelActions, ", "))
	case r.Action == "import" && r.Name == "":
		return fmt.Errorf("import needs name: the session to import channels from")
	case r.Action == "remove":
		return needConfirm(r, "REMOVE CHANNEL deletes the channel (restore brings back only the last one)")
	case r.Action == "move" && r.Target == "":
		return fmt.Errorf("move needs target: the strip to move onto (e.g. \"#1\")")
	case r.Action == "set_level" && r.LevelDB == nil:
		return fmt.Errorf("set_level needs level_db")
	}
	return nil
}

// uncoverMixer closes what lies over the mixer before a strip, one of its
// nodes or the add tile is touched (aum_channel, aum_node, aum_file_player).
// While a side panel is open (the main menu, which a session load leaves
// open, or FILES after a save) AUM takes a tap on the mixer to close the
// panel and passes nothing on, and a swipe does nothing at all: the dB
// label's tap opened no dialog, a tap on MUTE did not mute, a swipe on the
// fader left the level where it was. A popover takes the tap that closes it
// the same way; an alert and the routing matrix cover the strips. The tempo
// bar and the built-in keyboard (the mixer shrinks above it) take nothing.
//
// An EDIT CHANNEL left open is closed as well: it can belong to another strip
// than the one the request names (edit leaves it open), and edit, rename,
// bookmark, duplicate and remove act in it, so they open the named strip's
// own (openEdit). The same holds for a FILE PLAYER left open
// (openFilePlayer) and a node's long-press menu.
//
// A plugin window is none of these and stays. Under a side panel its bar is
// not in the tree (parsePluginWindows), so the window is found again once the
// panel is gone.
func (d *AUMDriver) uncoverMixer() error {
	return d.clearOverlays("")
}

// addChannel taps AUDIO or MIDI in the add-channel tile's menu (tapAddMenu).
// On the start screen the tile is the one in the middle of the mixer. The
// strip that appears is read back: only one of the kind asked for is the
// channel that was added.
func (d *AUMDriver) addChannel(kind string) (AUMResult, error) {
	action := "add_" + strings.ToLower(kind)
	before := len(d.St.Channels)
	if err := d.tapAddMenu(kind); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Channels) > before })
	if err != nil {
		return AUMResult{}, err
	}
	if !ok {
		return d.result(action, false, fmt.Sprintf("still %d strips: the %s channel was not added", before, kind), nil), nil
	}
	added := d.St.Channels[len(d.St.Channels)-1]
	if !strings.EqualFold(added.Kind, kind) {
		return d.result(action, false, fmt.Sprintf("strip %d %q was added, and it is %s, not %s: another channel than the one asked for", added.Strip, added.Title, added.Kind, strings.ToLower(kind)), added), nil
	}
	return d.result(action, true, fmt.Sprintf("added strip %d %q", added.Strip, added.Title), added), nil
}

// tapAddMenu taps the add-channel tile, then AUDIO, MIDI or IMPORT at once:
// the tile's menu closes by itself after a few seconds.
//
// A menu that is open already (the tile was tapped a moment ago) has turned
// the tile into a container of its three buttons, and the tap meant to open
// it would land on MIDI, the button at the tile's centre. That menu is waited
// out, so the tile is the tile again; one that stays is tapped where the tree
// has the item.
func (d *AUMDriver) tapAddMenu(kind string) error {
	if err := d.showAddTile(); err != nil {
		return err
	}
	if len(d.St.addMenu) > 0 {
		closed, err := d.until(aumWait, func(st AUMState) bool { return len(st.addMenu) == 0 })
		if err != nil {
			return err
		}
		if item, ok := d.St.addMenu[kind]; ok && !closed {
			return d.tapRect(item, kind)
		}
	}
	if d.St.AddTile == nil {
		return fmt.Errorf("the add-channel tile is not on screen (%s)", d.St.overlaySummary())
	}
	tile := *d.St.AddTile
	if err := d.tapRect(tile, "the add-channel tile"); err != nil {
		return err
	}
	item, err := d.addMenuItem(tile, kind)
	if err != nil {
		return err
	}
	return d.tapRect(item, kind)
}

// addMenuItem finds AUDIO, MIDI or IMPORT once the tile's menu is open: in
// the tree when AUM lists it, else at its place in the tile.
func (d *AUMDriver) addMenuItem(tile Rect, kind string) (Rect, error) {
	if err := d.Read(); err != nil {
		return Rect{}, err
	}
	if r, ok := d.St.addMenu[kind]; ok {
		return r, nil
	}
	return addMenuFallback(tile, kind), nil
}

// addMenuFallback places AUDIO, MIDI and IMPORT in the tile (150×150; the
// three 45-point buttons stack from its top) when the tree lacks them.
func addMenuFallback(tile Rect, kind string) Rect {
	i := map[string]float64{"AUDIO": 0, "MIDI": 1, "IMPORT": 2}[kind]
	return Rect{X: tile.X, Y: tile.Y + i*52.5, Width: tile.Width, Height: 45}
}

func (d *AUMDriver) showAddTile() error {
	if d.St.AddTile == nil && len(d.St.Channels) == 0 {
		return fmt.Errorf("no add-channel tile on screen (%s)", d.St.overlaySummary())
	}
	return d.scrollTo(func(st AUMState) (Rect, bool) {
		if st.AddTile != nil {
			return *st.AddTile, true
		}
		// Off to the right: aim past the last strip.
		if n := len(st.Channels); n > 0 {
			last := st.Channels[n-1].Frame
			return Rect{X: last.X + last.Width + 78, Y: last.Y, Width: 150, Height: 150}, true
		}
		return Rect{}, false
	})
}

// importChannels opens IMPORT on the add tile (tapAddMenu), then either
// restores the last removed channel (session "") or imports every channel of
// session.
func (d *AUMDriver) importChannels(action, session string) (AUMResult, error) {
	before := len(d.St.Channels)
	if err := d.tapAddMenu("IMPORT"); err != nil {
		return AUMResult{}, err
	}
	pop, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if pop == nil {
		return d.result(action, false, "the IMPORT popover did not open (the tile's menu may have closed first)", nil), nil
	}
	if session == "" {
		row := d.findPrefix("Restore Removed Channel", &pop.Frame)
		if row == nil {
			return d.result(action, false, "no Restore Removed Channel row: nothing was removed in this AUM run", pop), nil
		}
		if err := d.tapRect(row.Rect, "Restore Removed Channel"); err != nil {
			return AUMResult{}, err
		}
	} else {
		row := d.findText(session, &pop.Frame)
		if row == nil {
			row = d.findPrefix(session, &pop.Frame)
		}
		if row == nil {
			return d.result(action, false, fmt.Sprintf("no session %q in the IMPORT list", session), pop), nil
		}
		if err := d.tapRect(row.Rect, session); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool { return d.findPrefix("IMPORT ", nil) != nil })
		if err != nil {
			return AUMResult{}, err
		}
		btn := d.findPrefix("IMPORT ", nil)
		if !ok || btn == nil {
			return d.result(action, false, "no IMPORT n CHANNELS button after picking the session", d.St.Overlays), nil
		}
		if err := d.tapRect(btn.Rect, btn.Text()); err != nil {
			return AUMResult{}, err
		}
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Channels) > before })
	if err != nil {
		return AUMResult{}, err
	}
	if ok && d.St.Overlay("popover") != nil {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result(action, ok, verdict(ok, fmt.Sprintf("%d strips now (was %d)", len(d.St.Channels), before), "no channel was added"), nil), nil
}

// openEdit taps a strip's title and waits for EDIT CHANNEL. Nothing is open
// over the mixer by then (uncoverMixer), so the one that opens is the strip's.
func (d *AUMDriver) openEdit(sel string) (AUMChannel, *AUMOverlay, error) {
	ch, err := d.channel(sel)
	if err != nil {
		return ch, nil, err
	}
	if err := d.tapRect(ch.TitleFrame, fmt.Sprintf("title %q", ch.Title)); err != nil {
		return ch, nil, err
	}
	o, err := d.waitOverlay("EDIT CHANNEL", aumWait)
	if err != nil {
		return ch, nil, err
	}
	if o == nil {
		return ch, nil, fmt.Errorf("EDIT CHANNEL did not open for %q (%s)", ch.Title, d.St.overlaySummary())
	}
	return ch, o, nil
}

func (d *AUMDriver) editChannel(sel string) (AUMResult, error) {
	ch, o, err := d.openEdit(sel)
	if err != nil {
		return AUMResult{}, err
	}
	return d.result("edit", true, fmt.Sprintf("%s open for strip %d %q", o.Title, ch.Strip, ch.Title), o), nil
}

// renameChannel types the new name into Edit Channel's name field and
// presses Return. A custom name replaces the whole title, number included;
// an empty name restores AUM's own title.
func (d *AUMDriver) renameChannel(r AUMRequest) (AUMResult, error) {
	ch, _, err := d.openEdit(r.Channel)
	if err != nil {
		return AUMResult{}, err
	}
	if _, err := d.typeText(0, r.Name); err != nil {
		return AUMResult{}, err
	}
	if err := d.pressReturn(); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		if strip > len(st.Channels) {
			return false
		}
		t := st.Channels[strip-1].Title
		if r.Name == "" {
			return t != ch.Title
		}
		return t == r.Name
	})
	if err != nil {
		return AUMResult{}, err
	}
	if o := d.St.Overlay("popover"); o != nil && strings.HasPrefix(o.Title, "EDIT CHANNEL") {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	now := ""
	if strip <= len(d.St.Channels) {
		now = d.St.Channels[strip-1].Title
	}
	return d.result("rename", ok, fmt.Sprintf("strip %d title %q -> %q", strip, ch.Title, now), nil), nil
}

// bookmarkChannel taps the bookmark at Edit Channel's top right (yellow
// when set); AUM's tree marks it with the Selected trait.
func (d *AUMDriver) bookmarkChannel(sel string) (AUMResult, error) {
	ch, o, err := d.openEdit(sel)
	if err != nil {
		return AUMResult{}, err
	}
	mark := d.bookmarkButton(o.Frame)
	var was *bool
	if mark != nil {
		s := mark.Selected()
		was = &s
		if err := d.tapRect(mark.Rect, "the bookmark"); err != nil {
			return AUMResult{}, err
		}
	} else if err := d.tap(o.Frame.X+o.Frame.Width-35, o.Frame.Y+23, "the bookmark"); err != nil {
		return AUMResult{}, err
	}
	if was == nil {
		return d.result("bookmark", false, fmt.Sprintf("tapped the bookmark of %q; its state is not in AUM's tree (check with ipad_screenshot)", ch.Title), nil), nil
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		m := d.bookmarkButton(o.Frame)
		return m != nil && m.Selected() != *was
	})
	state := "set"
	if *was {
		state = "cleared"
	}
	return d.result("bookmark", ok, verdict(ok, fmt.Sprintf("bookmark of %q %s", ch.Title, state), "the bookmark did not change"), nil), err
}

// bookmarkButton is the button in Edit Channel's header right corner.
func (d *AUMDriver) bookmarkButton(pop Rect) *Element {
	return d.find(func(e *Element) bool {
		r := e.Rect
		return e.Type == "Button" && !e.Hidden && r.Width < 60 && r.Height < 60 &&
			r.X > pop.X+pop.Width-80 && r.Y < pop.Y+60 && pop.Contains(r.Center())
	})
}

func (d *AUMDriver) duplicateChannel(sel string) (AUMResult, error) {
	ch, o, err := d.openEdit(sel)
	if err != nil {
		return AUMResult{}, err
	}
	before := len(d.St.Channels)
	if err := d.tapText("DUPLICATE", &o.Frame); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Channels) > before })
	if err != nil {
		return AUMResult{}, err
	}
	msg := "no copy appeared"
	if ok && ch.Strip < len(d.St.Channels) {
		msg = fmt.Sprintf("copy of %q inserted as strip %d %q", ch.Title, ch.Strip+1, d.St.Channels[ch.Strip].Title)
	}
	if d.St.Overlay("popover") != nil {
		if _, err := d.closeTop(); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("duplicate", ok, msg, nil), nil
}

// removeChannel taps REMOVE CHANNEL and, within its one-second window, TAP
// TO CONFIRM on the same spot.
func (d *AUMDriver) removeChannel(sel string) (AUMResult, error) {
	ch, o, err := d.openEdit(sel)
	if err != nil {
		return AUMResult{}, err
	}
	btn := d.findText("REMOVE CHANNEL", &o.Frame)
	if btn == nil {
		return AUMResult{}, fmt.Errorf("no REMOVE CHANNEL in %s", o.Title)
	}
	before := len(d.St.Channels)
	if err := d.tapTwice(btn.Rect, "REMOVE CHANNEL / TAP TO CONFIRM"); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Channels) < before })
	return d.result("remove", ok, verdict(ok, fmt.Sprintf("removed strip %d %q (aum_channel restore brings it back)", ch.Strip, ch.Title), "the channel is still there"), nil), err
}

// moveChannel drags a strip's title up about 100 points, sideways onto the
// target strip, holds and releases.
func (d *AUMDriver) moveChannel(sel, target string) (AUMResult, error) {
	ch, err := d.channel(sel)
	if err != nil {
		return AUMResult{}, err
	}
	to, err := d.St.Channel(target)
	if err != nil {
		return AUMResult{}, err
	}
	if !to.OnScreen {
		return AUMResult{}, fmt.Errorf("target strip %q is off screen; strips must be on screen together to drag", to.Title)
	}
	x, y := ch.TitleFrame.Center()
	tx, _ := to.TitleFrame.Center()
	if err := d.drag("title "+strconv.Quote(ch.Title), DragTiming{Press: 300 * time.Millisecond},
		Point{x, y}, Point{x, y - 100}, Point{tx, y - 100}); err != nil {
		return AUMResult{}, err
	}
	title := ch.Title
	ok, err := d.until(aumWait, func(st AUMState) bool {
		return to.Strip <= len(st.Channels) && st.Channels[to.Strip-1].Title == title
	})
	return d.result("move", ok, verdict(ok, fmt.Sprintf("%q is now strip %d", title, to.Strip), fmt.Sprintf("%q did not move to strip %d", title, to.Strip)), nil), err
}

// setLevel taps the dB label above the fader, types the level into the Set
// level dialog and confirms it. One tap opens the dialog, on a channel that
// was just added as on any other, as long as nothing lies over the mixer
// (uncoverMixer).
func (d *AUMDriver) setLevel(sel string, db float64) (AUMResult, error) {
	ch, err := d.channel(sel)
	if err != nil {
		return AUMResult{}, err
	}
	if ch.LevelFrame == nil {
		return AUMResult{}, fmt.Errorf("strip %q has no fader", ch.Title)
	}
	if err := d.tapRect(*ch.LevelFrame, "the dB label "+ch.LevelLabel); err != nil {
		return AUMResult{}, err
	}
	dlg, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if dlg == nil {
		return d.result("set_level", false, fmt.Sprintf("the Set level dialog did not open (%s)", d.St.overlaySummary()), nil), nil
	}
	text := strconv.FormatFloat(db, 'f', -1, 64)
	if _, err := d.fillText(0, text); err != nil {
		return AUMResult{}, err
	}
	if err := d.confirmDialog(dlg); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		c := st.Channels[strip-1]
		return st.Overlay("alert") == nil && c.LevelDB != nil && math.Abs(*c.LevelDB-db) <= 0.05
	})
	now := d.St.Channels[strip-1].LevelLabel
	return d.result("set_level", ok, fmt.Sprintf("strip %d %q fader %s -> %s", strip, ch.Title, ch.LevelLabel, now), nil), err
}

// confirmDialog presses the confirming button of a dialog (Set, OK, Done)
// as an element: AUM's alerts ignored a tap at its position. It falls back
// to Return.
func (d *AUMDriver) confirmDialog(dlg *AUMOverlay) error {
	area := dlg.Frame
	for _, label := range []string{"Set", "OK", "Done", "Save", "Sichern", "Fertig"} {
		if b := d.findText(label, &area); b != nil {
			return d.press(b.Rect, label)
		}
	}
	// The dialog moves up while the keyboard shows: re-read before giving up.
	if err := d.Read(); err != nil {
		return err
	}
	for _, label := range []string{"Set", "OK", "Done"} {
		if b := d.findText(label, nil); b != nil {
			return d.press(b.Rect, label)
		}
	}
	return d.pressReturn()
}

// resetLevel double-taps the fader: it returns to Settings → Fader Level.
func (d *AUMDriver) resetLevel(sel string) (AUMResult, error) {
	ch, err := d.channel(sel)
	if err != nil {
		return AUMResult{}, err
	}
	if ch.FaderFrame == nil {
		return AUMResult{}, fmt.Errorf("strip %q has no fader", ch.Title)
	}
	x, y := ch.FaderFrame.Center()
	if err := d.doubleTap(x, y, "the fader of "+strconv.Quote(ch.Title)); err != nil {
		return AUMResult{}, err
	}
	strip, was := ch.Strip, ch.LevelLabel
	ok, err := d.until(2*time.Second, func(st AUMState) bool { return st.Channels[strip-1].LevelLabel != was })
	if err != nil {
		return AUMResult{}, err
	}
	now := d.St.Channels[strip-1].LevelLabel
	if !ok {
		return d.result("reset_level", now != "", fmt.Sprintf("strip %d %q fader stays at %s (already at the default level?)", strip, ch.Title, now), nil), nil
	}
	return d.result("reset_level", true, fmt.Sprintf("strip %d %q fader %s -> %s", strip, ch.Title, was, now), nil), nil
}

// toggleStrip taps M, S or R and checks the button's Selected trait.
func (d *AUMDriver) toggleStrip(what, sel string, want *bool) (AUMResult, error) {
	ch, err := d.channel(sel)
	if err != nil {
		return AUMResult{}, err
	}
	state, frame := stripButton(ch, what)
	if frame == nil {
		if what == "solo" && ch.Muted != nil && *ch.Muted {
			return AUMResult{}, fmt.Errorf("strip %q is muted: its MUTE button covers SOLO; unmute it first", ch.Title)
		}
		return AUMResult{}, fmt.Errorf("strip %q has no %s button", ch.Title, what)
	}
	if want != nil && state != nil && *state == *want {
		return d.result(what, true, fmt.Sprintf("strip %d %q: %s already %s", ch.Strip, ch.Title, what, onOff(*want)), nil), nil
	}
	if err := d.tapRect(*frame, strings.ToUpper(what)+" of "+strconv.Quote(ch.Title)); err != nil {
		return AUMResult{}, err
	}
	strip := ch.Strip
	ok, err := d.until(aumWait, func(st AUMState) bool {
		s, _ := stripButton(st.Channels[strip-1], what)
		return s != nil && state != nil && *s != *state
	})
	if err != nil {
		return AUMResult{}, err
	}
	now, _ := stripButton(d.St.Channels[strip-1], what)
	if now == nil {
		return d.result(what, false, fmt.Sprintf("tapped %s of %q; its state is no longer in the tree", what, ch.Title), nil), nil
	}
	if want != nil {
		ok = ok && *now == *want
	}
	return d.result(what, ok, fmt.Sprintf("strip %d %q: %s %s", strip, ch.Title, what, onOff(*now)), nil), nil
}

func stripButton(ch AUMChannel, what string) (*bool, *Rect) {
	switch what {
	case "mute":
		return ch.Muted, ch.MuteFrame
	case "solo":
		return ch.Soloed, ch.SoloFrame
	default:
		return ch.Armed, ch.RecFrame
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
