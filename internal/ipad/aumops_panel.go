package ipad

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AUMPanels are the screens aum_panel opens, with how each is reached.
var AUMPanels = []struct{ Name, Description string }{
	{"menu", "the main menu side panel (session name, icon grid)"},
	{"files", "FILES: Sessions, Recordings, Restore Previous State"},
	{"save", "SAVE: session name, folder, SAVE SESSION"},
	{"settings", "SETTINGS"},
	{"midi_control", "MIDI CONTROL: mappings of transport, system and every channel"},
	{"midi_routing", "the MIDI routing matrix"},
	{"navigator", "the Navigator side panel (CHANNELS, PLUGINS, ALL)"},
	{"engine_info", "Engine Info (sample rate, buffer, latencies, Node Statistics)"},
	{"tempo", "the tempo bar (TAP, ‹ ♩ ›, metronome, Clock Options)"},
	{"clock_options", "Clock Options (time signature, pre-roll, Link, MIDI clock)"},
	{"keyboard", "the built-in keyboard"},
	{"notes", "NOTES: the session notes"},
	{"help", "HELP: AUM's user guide"},
	{"about", "ABOUT"},
}

// menuCaptions maps a panel to its caption in the main menu's icon grid.
var menuCaptions = map[string]string{
	"files": "FILES", "save": "SAVE", "settings": "SETTINGS", "midi_control": "MIDI CTRL",
	"notes": "NOTES", "help": "HELP", "about": "ABOUT", "clear": "CLEAR",
}

// panelTitles are the side-panel titles AUM shows for a menu item (where
// they are known; others are checked as "the menu gave way").
var panelTitles = map[string]string{"settings": "SETTINGS", "midi_control": "MIDI CONTROL"}

// AUMPanel opens, closes or reads one of AUM's screens.
func (c *Client) AUMPanel(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		var err error
		switch r.Action {
		case "read", "":
			if len(r.Path) > 0 {
				if ok, err := d.tapPath(r.Path); err != nil || !ok {
					res = d.result("read", false, fmt.Sprintf("could not follow path %q (%s)", r.Path, d.St.overlaySummary()), d.St.Overlays)
					return err
				}
			}
			res = d.result("read", true, d.St.overlaySummary(), d.St.Overlays)
		case "open":
			res, err = d.openPanel(r.Panel)
			if err == nil && res.Verified && len(r.Path) > 0 {
				if ok, err2 := d.tapPath(r.Path); err2 != nil || !ok {
					res = d.result("open", false, fmt.Sprintf("opened %s but could not follow path %q (%s)", r.Panel, r.Path, d.St.overlaySummary()), d.St.Overlays)
					return err2
				}
				res = d.result("open", true, "opened "+r.Panel+" → "+strings.Join(r.Path, " → ")+": "+d.St.overlaySummary(), d.St.Overlays)
			}
		case "set":
			if r.Setting == "" || r.Value == "" {
				return fmt.Errorf("set needs setting (a row label in the open panel or popover) and value")
			}
			if len(d.St.Overlays) == 0 {
				return fmt.Errorf("nothing open: open the panel first (e.g. aum_panel open clock_options)")
			}
			res, err = d.setRow(r.Setting, r.Value, true)
		case "close":
			res, err = d.closeTop()
		default:
			err = fmt.Errorf("unknown aum_panel action %q (open, close, read, set)", r.Action)
		}
		return err
	})
	return res, err
}

func (d *AUMDriver) openPanel(panel string) (AUMResult, error) {
	switch panel {
	case "menu":
		ok, err := d.ensureMenu()
		return d.result("open menu", ok, verdict(ok, "main menu open", "main menu did not open"), nil), err
	case "files", "save", "settings", "midi_control", "notes", "help", "about":
		ok, err := d.openFromMenu(panel)
		if err != nil {
			return AUMResult{}, err
		}
		return d.result("open "+panel, ok, verdict(ok, "opened "+d.St.overlaySummary(), panel+" did not open: "+d.St.overlaySummary()), d.St.Overlays), nil
	case "midi_routing":
		if err := d.clearOverlays("routing"); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.openRouting()
		return d.result("open midi_routing", ok, verdict(ok, "MIDI routing matrix open", "the routing matrix did not open"), nil), err
	case "navigator":
		if d.St.Panel() == "NAVIGATOR" {
			return d.result("open navigator", true, "Navigator open", d.St.Overlays), nil
		}
		if err := d.clearOverlays(""); err != nil {
			return AUMResult{}, err
		}
		x, y, _ := AUMTarget("navigator", d.win)
		if err := d.tap(x, y, "Navigator"); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool { return st.Panel() == "NAVIGATOR" })
		return d.result("open navigator", ok, verdict(ok, "Navigator open", "the Navigator did not open"), d.St.Overlays), err
	case "engine_info":
		if err := d.clearOverlays(""); err != nil {
			return AUMResult{}, err
		}
		x, y, _ := AUMTarget("engine_info", d.win)
		if err := d.tap(x, y, "DSP %"); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool {
			o := st.Overlay("popover")
			return o != nil && strings.HasPrefix(strings.ToUpper(o.Title), "ENGINE")
		})
		return d.result("open engine_info", ok, verdict(ok, "Engine Info open: "+d.St.overlaySummary(), "Engine Info did not open"), d.St.Overlays), err
	case "tempo":
		ok, err := d.ensureTempoBar()
		return d.result("open tempo", ok, verdict(ok, "tempo bar open", "the tempo bar did not open"), nil), err
	case "clock_options":
		if ok, err := d.ensureTempoBar(); err != nil || !ok {
			return d.result("open clock_options", false, "the tempo bar did not open", nil), err
		}
		if err := d.tap(d.St.tempoFrame.X+d.St.tempoFrame.Width+115, 32, "Clock Options •••"); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool { return st.Overlay("popover") != nil })
		return d.result("open clock_options", ok, verdict(ok, "Clock Options open", "Clock Options did not open: "+d.St.overlaySummary()), d.St.Overlays), err
	case "keyboard":
		if d.St.Keyboard {
			return d.result("open keyboard", true, "keyboard open", nil), nil
		}
		if err := d.clearOverlays(""); err != nil {
			return AUMResult{}, err
		}
		x, y, _ := AUMTarget("keyboard", d.win)
		if err := d.tap(x, y, "keyboard"); err != nil {
			return AUMResult{}, err
		}
		ok, err := d.until(aumWait, func(st AUMState) bool { return st.Keyboard })
		return d.result("open keyboard", ok, verdict(ok, "keyboard open", "the keyboard did not open"), nil), err
	}
	names := make([]string, len(AUMPanels))
	for i, p := range AUMPanels {
		names[i] = p.Name
	}
	return AUMResult{}, fmt.Errorf("unknown panel %q (known: %s)", panel, strings.Join(names, ", "))
}

func verdict(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// ensureMenu opens the main menu, closing another side panel first.
func (d *AUMDriver) ensureMenu() (bool, error) {
	if d.St.Panel() == "MENU" {
		return true, nil
	}
	if d.St.Panel() != "" {
		if _, err := d.closeTop(); err != nil {
			return false, err
		}
	}
	x, y, _ := AUMTarget("menu", d.win)
	if err := d.tap(x, y, "menu ☰"); err != nil {
		return false, err
	}
	return d.until(aumWait, func(st AUMState) bool { return st.Panel() == "MENU" })
}

// openFromMenu taps a main-menu icon. Its caption is a separate text below
// the icon, so the icon is tapped 32 points above the caption's top.
func (d *AUMDriver) openFromMenu(panel string) (bool, error) {
	if want := panelTitles[panel]; want != "" && d.St.Panel() == want {
		return true, nil
	}
	if ok, err := d.ensureMenu(); err != nil || !ok {
		return false, err
	}
	caption := menuCaptions[panel]
	menu := d.St.Overlay("panel")
	cap := d.findText(caption, &menu.Frame)
	if cap == nil {
		return false, fmt.Errorf("no %s caption in the main menu", caption)
	}
	if err := d.tap(cap.Rect.X+cap.Rect.Width/2, cap.Rect.Y-32, caption+" icon"); err != nil {
		return false, err
	}
	want := panelTitles[panel]
	return d.until(aumWait, func(st AUMState) bool {
		if want != "" {
			return st.Panel() == want
		}
		return st.Panel() != "MENU" && (st.Panel() != "" || st.Overlay("popover") != nil)
	})
}

func (d *AUMDriver) openRouting() (bool, error) {
	if d.St.Overlay("routing") != nil {
		return true, nil
	}
	x, y, _ := AUMTarget("midi_routing", d.win)
	if err := d.tap(x, y, "MIDI routing ⊂⊃"); err != nil {
		return false, err
	}
	return d.until(aumWait, func(st AUMState) bool { return st.Overlay("routing") != nil })
}

// clearOverlays closes the popovers, alerts and side panels in front (all
// but kind keep): they cover the top bar, and a tap outside a popover only
// closes it.
func (d *AUMDriver) clearOverlays(keep string) error {
	for i := 0; i < 4; i++ {
		o := d.St.Overlay("")
		if o == nil || (keep != "" && o.Kind == keep) {
			return nil
		}
		res, err := d.closeTop()
		if err != nil {
			return err
		}
		if !res.Verified {
			return fmt.Errorf("the %s %q in front did not close", o.Kind, o.Title)
		}
	}
	return fmt.Errorf("could not clear the mixer: %s", d.St.overlaySummary())
}

func (d *AUMDriver) ensureTempoBar() (bool, error) {
	if d.St.TempoBar {
		return true, nil
	}
	if err := d.clearOverlays(""); err != nil {
		return false, err
	}
	x, y, _ := AUMTarget("tempo", d.win)
	if err := d.tap(x, y, "tempo ♩"); err != nil {
		return false, err
	}
	return d.until(aumWait, func(st AUMState) bool { return st.TempoBar })
}

// closeTop closes the front-most thing over the mixer: an alert (Cancel),
// a popover (a tap outside it), the routing matrix, a side panel (its
// top-right icon), the tempo bar (its ✕), the keyboard (HIDE).
func (d *AUMDriver) closeTop() (AUMResult, error) {
	o := d.St.Overlay("")
	what := ""
	switch {
	case o != nil && o.Kind == "alert":
		what = "alert " + o.Title
		b := d.findText("Cancel", &o.Frame)
		if b == nil {
			b = d.findText("Abbrechen", &o.Frame)
		}
		if b == nil {
			return AUMResult{}, fmt.Errorf("alert %q has no Cancel button (buttons: %s); answer it with ipad_tap", o.Title, strings.Join(o.Buttons, ", "))
		}
		if err := d.tapRect(b.Rect, "Cancel"); err != nil {
			return AUMResult{}, err
		}
	case o != nil && o.Kind == "popover":
		what = "popover " + o.Title
		x, y := d.outside(o.Frame)
		if err := d.tap(x, y, "outside the popover"); err != nil {
			return AUMResult{}, err
		}
	case o != nil && o.Kind == "routing":
		what = "MIDI routing"
		x, y, _ := AUMTarget("midi_routing", d.win)
		if err := d.tap(x, y, "MIDI routing ⊂⊃"); err != nil {
			return AUMResult{}, err
		}
	case o != nil && o.Kind == "panel":
		what = "panel " + o.Title
		if err := d.tap(d.win.Width-20, 30, "the panel's close icon"); err != nil {
			return AUMResult{}, err
		}
	case d.St.TempoBar:
		what = "tempo bar"
		if err := d.tap(30, 32, "the tempo bar's ✕"); err != nil {
			return AUMResult{}, err
		}
	case d.St.Keyboard:
		what = "keyboard"
		b := d.findPrefix("HIDE", nil)
		if b == nil {
			return AUMResult{}, fmt.Errorf("the keyboard has no HIDE button")
		}
		if err := d.tapRect(b.Rect, b.Label); err != nil {
			return AUMResult{}, err
		}
	default:
		return d.result("close", true, "nothing to close: the mixer is bare", nil), nil
	}
	before := d.topSignature()
	ok, err := d.until(aumWait, func(AUMState) bool { return d.topSignature() != before })
	return d.result("close", ok, verdict(ok, "closed the "+what, "the "+what+" is still open"), nil), err
}

// topSignature sums up what is open over the mixer, to tell when a close
// took effect.
func (d *AUMDriver) topSignature() string {
	var b strings.Builder
	for _, o := range d.St.Overlays {
		b.WriteString(o.Kind + ":" + o.Title + "|")
	}
	if d.St.TempoBar {
		b.WriteString("tempo|")
	}
	if d.St.Keyboard {
		b.WriteString("keyboard|")
	}
	return b.String()
}

// outside picks a point outside popover frame p, as far from it as the
// window allows: AUM closes a popover on any tap outside it.
func (d *AUMDriver) outside(p Rect) (float64, float64) {
	cands := [][2]float64{
		{p.X / 2, p.MidY()},
		{(p.X + p.Width + d.win.Width) / 2, p.MidY()},
		{p.X + p.Width/2, (60 + p.Y) / 2},
		{p.X + p.Width/2, (p.Y + p.Height + d.win.Height) / 2},
	}
	best, bestGap := cands[0], -1.0
	for _, c := range cands {
		gap := 0.0
		switch {
		case c[0] < p.X:
			gap = p.X
		case c[0] > p.X+p.Width:
			gap = d.win.Width - p.X - p.Width
		case c[1] < p.Y:
			gap = p.Y - 60
		default:
			gap = d.win.Height - p.Y - p.Height
		}
		if gap > bestGap {
			best, bestGap = c, gap
		}
	}
	return best[0], best[1]
}

// waitOverlay waits for a popover or alert whose title starts with prefix
// ("" = any new one).
func (d *AUMDriver) waitOverlay(prefix string, timeout time.Duration) (*AUMOverlay, error) {
	n := len(d.St.Overlays)
	ok, err := d.until(timeout, func(st AUMState) bool {
		for i := len(st.Overlays) - 1; i >= 0; i-- {
			o := st.Overlays[i]
			if o.Kind != "popover" && o.Kind != "alert" {
				continue
			}
			if prefix == "" && len(st.Overlays) > n {
				return true
			}
			if prefix != "" && strings.HasPrefix(strings.ToUpper(o.Title), strings.ToUpper(prefix)) {
				return true
			}
		}
		return false
	})
	if err != nil || !ok {
		return nil, err
	}
	for i := len(d.St.Overlays) - 1; i >= 0; i-- {
		if k := d.St.Overlays[i].Kind; k == "popover" || k == "alert" {
			return &d.St.Overlays[i], nil
		}
	}
	return nil, nil
}
