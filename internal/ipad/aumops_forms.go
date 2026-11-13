package ipad

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Settings, File Player and Clock Options are lists of rows: a text and,
// right of it, a value button that steps through its values on each tap
// (ON/OFF, PRE/POST, INT 16/INT 24/FLOAT 32, …) or opens a list (Sample
// Rate, Buffer Size). setRow drives both.

// AUMSettingsActions are aum_settings' actions.
var AUMSettingsActions = []string{"read", "set"}

// AUMSettings reads AUM's Settings or sets one of them. Settings are
// global (not part of a session).
func (c *Client) AUMSettings(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		if ok, err := d.openFromMenu("settings"); err != nil || !ok {
			return fmt.Errorf("SETTINGS did not open (%s)", d.St.overlaySummary())
		}
		var err error
		switch r.Action {
		case "read", "":
			var rows []AUMRow
			if rows, err = d.collectRows(); err == nil {
				res = d.result("read", true, describeRows(rows), rows)
			}
		case "set":
			if r.Setting == "" || r.Value == "" {
				return fmt.Errorf("set needs setting (a row label, e.g. \"Confirm Eject\") and value (e.g. \"ON\")")
			}
			res, err = d.setRow(r.Setting, r.Value, true)
		default:
			err = fmt.Errorf("unknown aum_settings action %q (read, set)", r.Action)
		}
		return err
	})
	return res, err
}

func describeRows(rows []AUMRow) string {
	var b strings.Builder
	for _, r := range rows {
		if r.Value == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(r.Label + " " + r.Value)
	}
	if b.Len() == 0 {
		return "no rows with values"
	}
	return b.String()
}

// scrollPanel drags the front side panel's list by dy (negative = towards
// the bottom), on its left edge where the row texts sit.
func (d *AUMDriver) scrollPanel(dy float64) error {
	p := d.St.Overlay("panel")
	if p == nil {
		return fmt.Errorf("no side panel open")
	}
	x := p.Frame.X + 30
	y0 := d.win.Height/2 - dy/2
	return d.drag("the panel", DragTiming{Move: 500 * time.Millisecond, Hold: 200 * time.Millisecond}, Point{x, y0}, Point{x, y0 + dy})
}

// visibleRow reports whether a row is drawn inside the panel's list (AUM
// keeps rows scrolled away in the tree with made-up frames).
func (d *AUMDriver) visibleRow(r AUMRow) bool {
	return r.Frame.Y >= 60 && r.Frame.Y+r.Frame.Height <= d.win.Height-2
}

// collectRows reads the front panel's rows, scrolling down until nothing
// new appears, then back to the top.
func (d *AUMDriver) collectRows() ([]AUMRow, error) {
	var out []AUMRow
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		p := d.St.Overlay("panel")
		if p == nil {
			break
		}
		added := 0
		for _, r := range p.Rows {
			if !d.visibleRow(r) || seen[r.Label] {
				continue
			}
			seen[r.Label] = true
			out = append(out, r)
			added++
		}
		if added == 0 && page > 0 {
			break
		}
		if err := d.scrollPanel(-(d.win.Height - 200)); err != nil {
			return nil, err
		}
		if err := d.Read(); err != nil {
			return nil, err
		}
	}
	for i := 0; i < 3; i++ {
		if err := d.scrollPanel(d.win.Height - 150); err != nil {
			return nil, err
		}
	}
	return out, d.Read()
}

// findRow finds a row of the front overlay by label, scrolling a side
// panel when the row is not drawn.
func (d *AUMDriver) findRow(label string, scroll bool) (AUMRow, error) {
	for try := 0; try < 8; try++ {
		o := d.St.Overlay("")
		if o == nil {
			return AUMRow{}, fmt.Errorf("nothing open to set %q in", label)
		}
		if r, ok := o.Row(label); ok && d.visibleRow(r) {
			return r, nil
		}
		if !scroll || o.Kind != "panel" {
			break
		}
		dy := -(d.win.Height - 250)
		if try >= 4 {
			dy = d.win.Height - 250
		}
		if try == 4 {
			for i := 0; i < 3; i++ {
				if err := d.scrollPanel(d.win.Height - 150); err != nil {
					return AUMRow{}, err
				}
			}
		}
		if err := d.scrollPanel(dy); err != nil {
			return AUMRow{}, err
		}
		if err := d.Read(); err != nil {
			return AUMRow{}, err
		}
	}
	return AUMRow{}, fmt.Errorf("no row %q (rows: %s)", label, rowLabels(d.St.Overlay("")))
}

// setRow taps a row's value until it reads value. A value that opens a list
// (Sample Rate, Buffer Size) is picked from it.
func (d *AUMDriver) setRow(label, value string, scroll bool) (AUMResult, error) {
	r, err := d.findRow(label, scroll)
	if err != nil {
		return AUMResult{}, err
	}
	was := r.Value
	if strings.EqualFold(was, value) {
		return d.result("set", true, fmt.Sprintf("%s already %s", r.Label, was), r), nil
	}
	if r.Prev != nil || r.Next != nil {
		return d.stepRow(r, label, value, scroll)
	}
	if r.ValueFrame == nil {
		return AUMResult{}, fmt.Errorf("row %q shows no value to tap", r.Label)
	}
	seen := map[string]bool{strings.ToUpper(was): true}
	for i := 0; i < 8; i++ {
		before := len(d.St.Overlays)
		if err := d.tapRect(*r.ValueFrame, r.Label+" ("+r.Value+")"); err != nil {
			return AUMResult{}, err
		}
		if err := d.pause(400 * time.Millisecond); err != nil {
			return AUMResult{}, err
		}
		if err := d.Read(); err != nil {
			return AUMResult{}, err
		}
		if len(d.St.Overlays) > before {
			// A list: pick the value, then come back.
			list := d.St.Overlay("")
			item := d.findText(value, &list.Frame)
			if item == nil {
				item = d.findPrefix(value, &list.Frame)
			}
			if item == nil {
				labels := rowLabels(list)
				if _, err := d.closeTop(); err != nil {
					return AUMResult{}, err
				}
				return d.result("set", false, fmt.Sprintf("%s offers no %q (choices: %s)", r.Label, value, labels), nil), nil
			}
			if err := d.tapRect(item.Rect, value); err != nil {
				return AUMResult{}, err
			}
			if _, err := d.until(aumWait, func(st AUMState) bool { return len(st.Overlays) <= before }); err != nil {
				return AUMResult{}, err
			}
			if len(d.St.Overlays) > before {
				if _, err := d.closeTop(); err != nil {
					return AUMResult{}, err
				}
			}
		}
		if r, err = d.findRow(label, scroll); err != nil {
			return AUMResult{}, err
		}
		if strings.EqualFold(r.Value, value) {
			return d.result("set", true, fmt.Sprintf("%s %s -> %s", r.Label, was, r.Value), r), nil
		}
		if a := d.St.Overlay("alert"); a != nil {
			return d.result("set", false, fmt.Sprintf("%s: AUM says %q", r.Label, a.Title), a), nil
		}
		if seen[strings.ToUpper(r.Value)] {
			break
		}
		seen[strings.ToUpper(r.Value)] = true
		if r.ValueFrame == nil {
			break
		}
	}
	return d.result("set", false, fmt.Sprintf("%s cycles through %s without %q", r.Label, strings.Join(keys(seen), ", "), value), r), nil
}

// stepRow sets a stepper row (‹ value ›): it steps › until value shows,
// then from the top end ‹, and steps back to where it was when the row does
// not offer value.
func (d *AUMDriver) stepRow(r AUMRow, label, value string, scroll bool) (AUMResult, error) {
	const limit = 40
	was := r.Value
	seen := []string{was}
	step := func(next bool) (bool, error) { // reports whether the value moved
		arrow, name := r.Prev, "‹"
		if next {
			arrow, name = r.Next, "›"
		}
		if arrow == nil {
			return false, nil
		}
		before := r.Value
		if err := d.tapRect(*arrow, r.Label+" "+name); err != nil {
			return false, err
		}
		if err := d.pause(300 * time.Millisecond); err != nil {
			return false, err
		}
		if err := d.Read(); err != nil {
			return false, err
		}
		nr, err := d.findRow(label, scroll)
		if err != nil {
			return false, err
		}
		r = nr
		if !slices.ContainsFunc(seen, func(v string) bool { return strings.EqualFold(v, r.Value) }) {
			seen = append(seen, r.Value)
		}
		return r.Value != before, nil
	}
	done := func() AUMResult {
		return d.result("set", true, fmt.Sprintf("%s %s -> %s", r.Label, was, r.Value), r)
	}
	// Up: stop at the top end, or when it wraps round to where it was.
	wrapped := false
	for i := 0; i < limit; i++ {
		moved, err := step(true)
		if err != nil {
			return AUMResult{}, err
		}
		if strings.EqualFold(r.Value, value) {
			return done(), nil
		}
		if !moved {
			break
		}
		if strings.EqualFold(r.Value, was) {
			wrapped = true
			break
		}
	}
	if !wrapped {
		// Down from the top end, past where it was, to the bottom end.
		for i := 0; i < 2*limit; i++ {
			moved, err := step(false)
			if err != nil {
				return AUMResult{}, err
			}
			if strings.EqualFold(r.Value, value) {
				return done(), nil
			}
			if !moved {
				break
			}
		}
		// Not offered: back up to where it was.
		for i := 0; i < 2*limit && !strings.EqualFold(r.Value, was); i++ {
			if moved, err := step(true); err != nil || !moved {
				if err != nil {
					return AUMResult{}, err
				}
				break
			}
		}
	}
	return d.result("set", false, fmt.Sprintf("%s offers no %q (values: %s); left at %s", r.Label, value, strings.Join(seen, ", "), r.Value), r), nil
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AUMFilePlayerActions are aum_file_player's actions.
var AUMFilePlayerActions = []string{"open", "read", "load", "set", "play_enable"}

// AUMFilePlayer drives a File Player node.
func (c *Client) AUMFilePlayer(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		// What the request lacks is answered before anything is touched.
		if err := checkFilePlayer(r); err != nil {
			return err
		}
		// The node and its PLAY ENABLE toggle are on the strip (uncoverMixer).
		if err := d.uncoverMixer(); err != nil {
			return err
		}
		ch, err := d.channel(r.Channel)
		if err != nil {
			return err
		}
		if r.Action == "play_enable" {
			res, err = d.playEnable(ch, r.On)
			return err
		}
		o, err := d.openFilePlayer(ch)
		if err != nil {
			return err
		}
		switch r.Action {
		case "open", "read", "":
			res = d.result("read", true, describeRows(o.Rows), o.Rows)
		case "set":
			res, err = d.setRow(r.Setting, r.Value, false)
		case "load":
			res, err = d.loadSoundFile(r.Path, r.Name)
		}
		return err
	})
	return res, err
}

// checkFilePlayer reports what a request lacks for its action ("" reads).
func checkFilePlayer(r AUMRequest) error {
	switch {
	case r.Action != "" && !slices.Contains(AUMFilePlayerActions, r.Action):
		return fmt.Errorf("unknown aum_file_player action %q (%s)", r.Action, strings.Join(AUMFilePlayerActions, ", "))
	case r.Action == "set" && (r.Setting == "" || r.Value == ""):
		return fmt.Errorf("set needs setting (Loop, Sync, Normalize) and value (ON, OFF)")
	case r.Action == "load" && r.Name == "":
		return fmt.Errorf("load needs name: the sound file, with path for its folders")
	}
	return nil
}

// openFilePlayer taps a strip's source node and waits for FILE PLAYER.
// Nothing is open over the mixer by then (uncoverMixer), a FILE PLAYER that
// open left there included: its title names no strip, so it can be another
// strip's, and the one that opens here is the named strip's own.
func (d *AUMDriver) openFilePlayer(ch AUMChannel) (*AUMOverlay, error) {
	src, err := ch.Node("source")
	if err != nil {
		return nil, err
	}
	if err := d.tapRect(src.Frame, "the File Player node"); err != nil {
		return nil, err
	}
	o, err := d.waitOverlay("FILE PLAYER", aumWait)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("%q's source did not open FILE PLAYER (%s)", ch.Title, d.St.overlaySummary())
	}
	return o, nil
}

// loadSoundFile taps the Selected file row (SOUNDFILES opens), walks the
// folders of path and taps the file: it loads at once.
func (d *AUMDriver) loadSoundFile(path []string, name string) (AUMResult, error) {
	o := d.St.Overlay("popover")
	head, ok := o.Row("Selected file")
	if !ok {
		return AUMResult{}, fmt.Errorf("FILE PLAYER shows no Selected file row")
	}
	var fileRow *AUMRow
	for i := range o.Rows {
		r := o.Rows[i]
		if r.Frame.Y > head.Frame.Y+head.Frame.Height-1 && (fileRow == nil || r.Frame.Y < fileRow.Frame.Y) {
			fileRow = &o.Rows[i]
		}
	}
	if fileRow == nil {
		return AUMResult{}, fmt.Errorf("no file row under Selected file")
	}
	if err := d.tapRect(fileRow.Frame, "the selected-file row"); err != nil {
		return AUMResult{}, err
	}
	if _, err := d.until(aumWait, func(st AUMState) bool {
		p := st.Overlay("popover")
		return p != nil && p.Title != "FILE PLAYER"
	}); err != nil {
		return AUMResult{}, err
	}
	for _, step := range append(append([]string{}, path...), name) {
		p := d.St.Overlay("popover")
		if p == nil {
			return AUMResult{}, fmt.Errorf("the file browser closed before %q", step)
		}
		row := d.findText(step, &p.Frame)
		if row == nil {
			return d.result("load", false, fmt.Sprintf("no %q in %s (rows: %s)", step, p.Title, rowLabels(p)), p.Rows), nil
		}
		if err := d.openRow(row, p.Title); err != nil {
			return AUMResult{}, err
		}
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		p := st.Overlay("popover")
		return p != nil && p.Title == "FILE PLAYER" && d.findPrefix(strings.TrimSuffix(name, ".wav"), &p.Frame) != nil
	})
	return d.result("load", ok, verdict(ok, fmt.Sprintf("FILE PLAYER loaded %q", name), "the File Player does not show "+name), nil), err
}

// playEnable taps the PLAY ENABLE toggle left of a File Player node.
func (d *AUMDriver) playEnable(ch AUMChannel, want *bool) (AUMResult, error) {
	label := d.findText("PLAY ENABLE", &ch.Frame)
	if label == nil {
		return AUMResult{}, fmt.Errorf("strip %q has no PLAY ENABLE toggle (not a File Player channel?)", ch.Title)
	}
	x, _ := label.Rect.Center()
	y := label.Rect.Y + label.Rect.Height + 12
	toggle := d.find(func(e *Element) bool {
		return e.Type == "Button" && !e.Hidden && e.Rect.Contains(x, y) && e.Rect.Width < 80
	})
	var was *bool
	if toggle != nil {
		s := toggle.Selected()
		was = &s
		x, y = toggle.Rect.Center()
	}
	if want != nil && was != nil && *was == *want {
		return d.result("play_enable", true, "PLAY ENABLE already "+onOff(*want), nil), nil
	}
	if err := d.tap(x, y, "PLAY ENABLE"); err != nil {
		return AUMResult{}, err
	}
	if was == nil {
		return d.result("play_enable", false, "tapped PLAY ENABLE; its state is not in AUM's tree (ipad_screenshot)", nil), nil
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		t := d.find(func(e *Element) bool {
			return e.Type == "Button" && !e.Hidden && e.Rect.Contains(x, y) && e.Rect.Width < 80
		})
		return t != nil && t.Selected() != *was
	})
	return d.result("play_enable", ok, verdict(ok, "PLAY ENABLE "+onOff(!*was), "PLAY ENABLE did not change"), nil), err
}
