package ipad

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AUMEndpoint is a MIDI routing matrix endpoint: a source column or a
// destination row.
type AUMEndpoint struct {
	Name string `json:"name"`
	// Kind is the category printed under the name (BUILT-IN, VIRTUAL,
	// NETWORK, AUDIO UNIT, …).
	Kind string `json:"kind,omitempty"`
	// Frame covers the name and kind texts; the matrix cell of a source and
	// a destination is (source centre x, destination centre y).
	Frame Rect `json:"frame"`
}

// AUMRouting is the open MIDI routing matrix.
type AUMRouting struct {
	Sources      []AUMEndpoint `json:"sources"`
	Destinations []AUMEndpoint `json:"destinations"`
	// Connections lists "source -> destination" for every lit cell (read
	// from a screenshot: AUM's tree does not carry them).
	Connections []string `json:"connections,omitempty"`
}

// parseRouting reads the matrix's endpoints: sources are vertical texts
// along the top (name, then kind 14 points right), destinations horizontal
// texts on the right (name, then kind 14 points below).
func parseRouting(root *Element) AUMRouting {
	var vert, horiz []*Element
	root.Walk(func(e *Element, _ int) bool {
		if stripTitle(e) != nil {
			return false // the mixer behind the matrix
		}
		r := e.Rect
		if e.Type != "StaticText" || e.Hidden || e.Label == "" || r.Empty() || r.Y < 110 {
			return true
		}
		// Endpoint texts are 18 points across and 132 long.
		switch {
		case r.Height >= 100 && r.Width >= 15 && r.Width <= 24:
			vert = append(vert, e)
		case r.Width >= 100 && r.Height >= 15 && r.Height <= 24:
			horiz = append(horiz, e)
		}
		return true
	})
	var rt AUMRouting
	sort.SliceStable(vert, func(i, j int) bool { return vert[i].Rect.X < vert[j].Rect.X })
	maxX := 0.0
	for i := 0; i < len(vert); i++ {
		e := AUMEndpoint{Name: vert[i].Label, Frame: vert[i].Rect}
		if i+1 < len(vert) && math.Abs(vert[i+1].Rect.X-vert[i].Rect.X-14) <= 4 {
			e.Kind = vert[i+1].Label
			e.Frame = union(e.Frame, vert[i+1].Rect)
			i++
		}
		maxX = math.Max(maxX, e.Frame.X+e.Frame.Width)
		rt.Sources = append(rt.Sources, e)
	}
	var right []*Element
	for _, h := range horiz {
		if len(rt.Sources) == 0 || h.Rect.X > maxX+20 {
			right = append(right, h)
		}
	}
	sort.SliceStable(right, func(i, j int) bool { return right[i].Rect.Y < right[j].Rect.Y })
	for i := 0; i < len(right); i++ {
		e := AUMEndpoint{Name: right[i].Label, Frame: right[i].Rect}
		if i+1 < len(right) && math.Abs(right[i+1].Rect.Y-right[i].Rect.Y-14) <= 4 {
			e.Kind = right[i+1].Label
			e.Frame = union(e.Frame, right[i+1].Rect)
			i++
		}
		rt.Destinations = append(rt.Destinations, e)
	}
	return rt
}

func union(a, b Rect) Rect {
	x0, y0 := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	x1, y1 := math.Max(a.X+a.Width, b.X+b.Width), math.Max(a.Y+a.Height, b.Y+b.Height)
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

func endpoint(list []AUMEndpoint, name, what string) (AUMEndpoint, error) {
	var hits []AUMEndpoint
	for _, e := range list {
		if strings.EqualFold(e.Name, name) {
			return e, nil
		}
		if strings.Contains(strings.ToLower(e.Name), strings.ToLower(name)) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	var names []string
	for _, e := range list {
		names = append(names, strconv.Quote(e.Name))
	}
	if len(hits) > 1 {
		return AUMEndpoint{}, fmt.Errorf("%q matches %d %ss (%s)", name, len(hits), what, strings.Join(names, ", "))
	}
	return AUMEndpoint{}, fmt.Errorf("no %s %q (%ss: %s)", what, name, what, strings.Join(names, ", "))
}

// cell is the matrix cell of a source and a destination; probe is the
// patch of its lower-left quarter, which a connection fills lighter while
// the L-shaped wire stays out of it.
func cell(s, d AUMEndpoint) (x, y float64, probe Rect) {
	x, _ = s.Frame.Center()
	_, y = d.Frame.Center()
	return x, y, Rect{X: x - 13, Y: y + 4, Width: 9, Height: 9}
}

// AUMRoutingActions are aum_midi_routing's actions.
var AUMRoutingActions = []string{"read", "connect", "disconnect", "destination"}

// AUMMIDIRouting reads the MIDI routing matrix, connects or disconnects a
// cell, or opens a destination's panel (MIDI sources, All Notes Off, the
// filter).
func (c *Client) AUMMIDIRouting(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		if ok, err := d.openRouting(); err != nil || !ok {
			return fmt.Errorf("the MIDI routing matrix did not open (%s)", d.St.overlaySummary())
		}
		rt := parseRouting(d.root)
		if len(rt.Sources) == 0 || len(rt.Destinations) == 0 {
			return fmt.Errorf("the routing matrix shows no endpoints")
		}
		var err error
		switch r.Action {
		case "read", "":
			if rt.Connections, err = d.connections(rt); err == nil {
				res = d.result("read", true, fmt.Sprintf("%d sources, %d destinations, connected: %s",
					len(rt.Sources), len(rt.Destinations), orNone(rt.Connections)), rt)
			}
		case "connect", "disconnect":
			res, err = d.setConnection(rt, r.Source, r.Destination, r.Action == "connect")
		case "destination":
			res, err = d.openDestination(rt, r.Destination, r.Path)
		default:
			err = fmt.Errorf("unknown aum_midi_routing action %q (%s)", r.Action, strings.Join(AUMRoutingActions, ", "))
		}
		return err
	})
	return res, err
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, "; ")
}

// lumas measures the probe patch of every cell in one screenshot.
func (d *AUMDriver) lumas(rt AUMRouting) ([][]float64, error) {
	png, err := d.s.c.screenshot(d.ctx)
	if err != nil {
		return nil, err
	}
	scr, err := d.s.c.ScreenInfo(d.ctx)
	if err != nil {
		return nil, err
	}
	out := make([][]float64, len(rt.Sources))
	for i, s := range rt.Sources {
		out[i] = make([]float64, len(rt.Destinations))
		for j, dst := range rt.Destinations {
			_, _, probe := cell(s, dst)
			if out[i][j], err = Luma(png, scr.Scale, probe); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// litThreshold is how much brighter than the matrix's median cell a
// connected cell's fill is.
const litThreshold = 8

func median(l [][]float64) float64 {
	var all []float64
	for _, row := range l {
		all = append(all, row...)
	}
	if len(all) == 0 {
		return 0
	}
	sort.Float64s(all)
	return all[len(all)/2]
}

func (d *AUMDriver) connections(rt AUMRouting) ([]string, error) {
	l, err := d.lumas(rt)
	if err != nil {
		return nil, err
	}
	m := median(l)
	var out []string
	for i, s := range rt.Sources {
		for j, dst := range rt.Destinations {
			if l[i][j] > m+litThreshold {
				out = append(out, s.Name+" -> "+dst.Name)
			}
		}
	}
	return out, nil
}

func (d *AUMDriver) setConnection(rt AUMRouting, src, dst string, want bool) (AUMResult, error) {
	action := map[bool]string{true: "connect", false: "disconnect"}[want]
	if src == "" || dst == "" {
		return AUMResult{}, fmt.Errorf("%s needs source and destination (aum_midi_routing read lists them)", action)
	}
	s, err := endpoint(rt.Sources, src, "source")
	if err != nil {
		return AUMResult{}, err
	}
	t, err := endpoint(rt.Destinations, dst, "destination")
	if err != nil {
		return AUMResult{}, err
	}
	si, ti := indexOf(rt.Sources, s), indexOf(rt.Destinations, t)
	l, err := d.lumas(rt)
	if err != nil {
		return AUMResult{}, err
	}
	m := median(l)
	lit := l[si][ti] > m+litThreshold
	pair := s.Name + " -> " + t.Name
	if lit == want {
		return d.result(action, true, fmt.Sprintf("%s already %sconnected", pair, map[bool]string{true: "", false: "dis"}[want]), nil), nil
	}
	x, y, _ := cell(s, t)
	if err := d.tap(x, y, "cell "+pair); err != nil {
		return AUMResult{}, err
	}
	if err := d.pause(400 * time.Millisecond); err != nil {
		return AUMResult{}, err
	}
	l2, err := d.lumas(rt)
	if err != nil {
		return AUMResult{}, err
	}
	now := l2[si][ti] > median(l2)+litThreshold
	if err := d.Read(); err != nil {
		return AUMResult{}, err
	}
	return d.result(action, now == want, fmt.Sprintf("%s %sconnected (cell brightness %.0f -> %.0f, matrix median %.0f)",
		pair, map[bool]string{true: "", false: "dis"}[now], l[si][ti], l2[si][ti], median(l2)), nil), nil
}

func indexOf(list []AUMEndpoint, e AUMEndpoint) int {
	for i, x := range list {
		if x.Name == e.Name && x.Frame == e.Frame {
			return i
		}
	}
	return -1
}

// openDestination taps a destination's name: its panel lists the MIDI
// sources to connect, All Notes Off and the MIDI filter. path taps further
// rows in turn (e.g. the filter's sections).
func (d *AUMDriver) openDestination(rt AUMRouting, dst string, path []string) (AUMResult, error) {
	t, err := endpoint(rt.Destinations, dst, "destination")
	if err != nil {
		return AUMResult{}, err
	}
	before := len(d.St.Overlays)
	if err := d.tapRect(t.Frame, "destination "+strconv.Quote(t.Name)); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Overlays) > before })
	if err != nil {
		return AUMResult{}, err
	}
	if !ok {
		return d.result("destination", false, "the destination panel did not open", nil), nil
	}
	if res, err := d.tapPath(path); err != nil || !res {
		return d.result("destination", false, fmt.Sprintf("could not follow path %q (%s)", path, d.St.overlaySummary()), d.St.Overlay("")), err
	}
	o := d.St.Overlay("")
	return d.result("destination", true, fmt.Sprintf("%s %q: %s", o.Kind, o.Title, describeRowsOrLabels(o)), o), nil
}

func describeRowsOrLabels(o *AUMOverlay) string {
	if s := describeRows(o.Rows); s != "no rows with values" {
		return s
	}
	return rowLabels(o)
}

// tapPath taps each label of path in the front overlay in turn, waiting for
// the screen to change after each.
func (d *AUMDriver) tapPath(path []string) (bool, error) {
	for _, label := range path {
		o := d.St.Overlay("")
		var area *Rect
		if o != nil {
			area = &o.Frame
		}
		e := d.findText(label, area)
		if e == nil {
			e = d.findPrefix(label, area)
		}
		if e == nil {
			return false, nil
		}
		title := ""
		if o != nil {
			title = o.Title
		}
		if err := d.openRow(e, title); err != nil {
			return false, err
		}
	}
	return true, nil
}

// AUMMIDIControlActions are aum_midi_control's actions.
var AUMMIDIControlActions = []string{"read", "filter", "open", "learn", "menu", "add", "main_parameter"}

// midiMenuDestructive are ⋮ menu items that change many mappings at once,
// without a question from AUM.
var midiMenuDestructive = map[string]bool{"CLEAR ALL": true, "DELETE ALL": true, "SET MIDI CHANNELS": true, "PASTE": true, "LOAD": true}

// AUMMIDIControl drives MIDI CONTROL: the filter, walking into a
// collection or control (path), LEARN, the ⋮ menus, ADD TEMPO / ADD PRESET
// and the main-parameter knob.
func (c *Client) AUMMIDIControl(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		if ok, err := d.openFromMenu("midi_control"); err != nil || !ok {
			return fmt.Errorf("MIDI CONTROL did not open (%s)", d.St.overlaySummary())
		}
		if r.Action != "filter" && len(r.Path) > 0 {
			if ok, err := d.tapPath(r.Path); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("could not follow path %q: rows are %s", r.Path, rowLabels(d.St.Overlay("panel")))
			}
		}
		var err error
		switch r.Action {
		case "read", "open", "":
			o := d.St.Overlay("")
			res = d.result(r.Action, true, describeRowsOrLabels(o), o)
		case "filter":
			res, err = d.midiFilter(r.Value)
		case "learn":
			res, err = d.midiLearn()
		case "menu":
			res, err = d.midiMenu(r)
		case "add":
			res, err = d.midiAdd(r)
		case "main_parameter":
			res, err = d.mainParameter()
		default:
			err = fmt.Errorf("unknown aum_midi_control action %q (%s)", r.Action, strings.Join(AUMMIDIControlActions, ", "))
		}
		return err
	})
	return res, err
}

// filterButton is MIDI CONTROL's ALL / ASSIGNED / UNUSED button (it
// carries the current filter as its value).
func (d *AUMDriver) filterButton() *Element {
	return d.find(func(e *Element) bool {
		v := strings.ToUpper(e.Value)
		return e.Type == "Button" && !e.Hidden && e.Label == "" && (v == "ALL" || v == "ASSIGNED" || v == "UNUSED")
	})
}

func (d *AUMDriver) midiFilter(value string) (AUMResult, error) {
	want := strings.ToUpper(strings.TrimSpace(value))
	switch want {
	case "ALL", "ASSIGNED", "UNUSED":
	default:
		return AUMResult{}, fmt.Errorf("filter needs value: ALL, ASSIGNED or UNUSED")
	}
	for i := 0; i < 4; i++ {
		b := d.filterButton()
		if b == nil {
			return AUMResult{}, fmt.Errorf("no filter button in MIDI CONTROL")
		}
		if strings.EqualFold(b.Value, want) {
			return d.result("filter", true, "MIDI CONTROL shows "+want+": "+rowLabels(d.St.Overlay("panel")), d.St.Overlay("panel")), nil
		}
		was := b.Value
		if err := d.tapRect(b.Rect, "the filter ("+was+")"); err != nil {
			return AUMResult{}, err
		}
		if _, err := d.until(2*time.Second, func(st AUMState) bool {
			f := d.filterButton()
			return f == nil || f.Value != was || d.findText(want, nil) != nil
		}); err != nil {
			return AUMResult{}, err
		}
		// The filter may open a choice instead of stepping.
		if f := d.filterButton(); f != nil && f.Value == was {
			if e := d.findText(want, nil); e != nil && e != f {
				if err := d.tapRect(e.Rect, want); err != nil {
					return AUMResult{}, err
				}
				if err := d.Read(); err != nil {
					return AUMResult{}, err
				}
			}
		}
	}
	b := d.filterButton()
	return d.result("filter", b != nil && strings.EqualFold(b.Value, want), "filter now "+b.Value, nil), nil
}

// midiLearn taps LEARN in an open mapping editor: AUM shows LEARNING until
// a MIDI message arrives (send one now, e.g. with send_midi).
func (d *AUMDriver) midiLearn() (AUMResult, error) {
	if err := d.tapText("LEARN", nil); err != nil {
		return AUMResult{}, fmt.Errorf("%w; open a control's mapping editor first (path)", err)
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return d.findText("LEARNING", nil) != nil })
	return d.result("learn", ok, verdict(ok, "LEARNING: send the MIDI message now; read again to see the mapping", "LEARNING did not appear"), nil), err
}

// midiMenu opens the ⋮ menu of a section (r.Setting: a group such as
// Channels, or a collection such as Channel controls) and taps r.Value.
func (d *AUMDriver) midiMenu(r AUMRequest) (AUMResult, error) {
	item := strings.TrimSpace(r.Value)
	if r.Setting == "" || item == "" {
		return AUMResult{}, fmt.Errorf("menu needs setting (the section whose ⋮ to open, e.g. \"Channels\") and value (the item, e.g. \"Set MIDI Channels\")")
	}
	if midiMenuDestructive[strings.ToUpper(item)] {
		if err := needConfirm(r, item+" changes every mapping below it at once"); err != nil {
			return AUMResult{}, err
		}
	}
	head := d.find(func(e *Element) bool { return !e.Hidden && strings.EqualFold(e.Identifier, r.Setting) })
	if head == nil {
		head = d.findText(r.Setting, nil)
	}
	if head == nil {
		return AUMResult{}, fmt.Errorf("no section %q in MIDI CONTROL (%s)", r.Setting, rowLabels(d.St.Overlay("panel")))
	}
	hr := head.Rect
	p := d.St.Overlay("panel")
	right := hr.X + hr.Width
	if p != nil {
		right = p.Frame.X + p.Frame.Width
	}
	dots := d.find(func(e *Element) bool {
		return e.Type == "Button" && !e.Hidden && e.Label == "" && e.Rect.X > right-60 && math.Abs(e.Rect.MidY()-hr.MidY()) <= hr.Height/2
	})
	if dots != nil {
		if err := d.tapRect(dots.Rect, "⋮ of "+r.Setting); err != nil {
			return AUMResult{}, err
		}
	} else if err := d.tap(right-25, hr.MidY(), "⋮ of "+r.Setting); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return d.findText(item, nil) != nil })
	if err != nil {
		return AUMResult{}, err
	}
	if !ok {
		return d.result("menu", false, fmt.Sprintf("the ⋮ menu of %q offers no %q (%s)", r.Setting, item, d.St.overlaySummary()), nil), nil
	}
	if err := d.tapText(item, nil); err != nil {
		return AUMResult{}, err
	}
	// Set MIDI Channels and Save ask for a value.
	if dlg, err := d.waitOverlay("", 2*time.Second); err != nil {
		return AUMResult{}, err
	} else if dlg != nil && len(dlg.Fields) > 0 {
		if r.Name == "" {
			if _, err := d.closeTop(); err != nil {
				return AUMResult{}, err
			}
			return d.result("menu", false, fmt.Sprintf("%s asks for a value (%q); pass it as name", item, dlg.Title), dlg), nil
		}
		if _, err := d.typeText(0, r.Name); err != nil {
			return AUMResult{}, err
		}
		if err := d.confirmDialog(dlg); err != nil {
			return AUMResult{}, err
		}
		if _, err := d.until(aumWait, func(st AUMState) bool { return st.Overlay("alert") == nil }); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("menu", true, fmt.Sprintf("%s → %s; now %s", r.Setting, item, describeRowsOrLabels(d.St.Overlay(""))), d.St.Overlay("")), nil
}

// midiAdd taps ADD TEMPO or ADD PRESET (r.Value) and, for a preset, the
// preset named r.Name; ADD TEMPO's EDIT TEMPO is closed with DONE.
func (d *AUMDriver) midiAdd(r AUMRequest) (AUMResult, error) {
	what := strings.ToUpper(strings.TrimSpace(r.Value))
	if what != "ADD TEMPO" && what != "ADD PRESET" {
		return AUMResult{}, fmt.Errorf("add needs value: ADD TEMPO (in Tempo Presets) or ADD PRESET (in a plugin's Preset Load)")
	}
	rowsBefore := len(d.St.Overlay("panel").Rows)
	if err := d.tapText(what, nil); err != nil {
		return AUMResult{}, err
	}
	if _, err := d.until(aumWait, func(st AUMState) bool { return st.Overlay("popover") != nil || d.findText("DONE", nil) != nil }); err != nil {
		return AUMResult{}, err
	}
	if what == "ADD PRESET" {
		if r.Name == "" {
			return d.result("add", false, "ADD PRESET lists the plugin's presets: pass name to pick one ("+rowLabels(d.St.Overlay(""))+")", d.St.Overlay("")), nil
		}
		if err := d.tapText(r.Name, nil); err != nil {
			return AUMResult{}, err
		}
	} else if done := d.findText("DONE", nil); done != nil {
		if err := d.tapRect(done.Rect, "DONE"); err != nil {
			return AUMResult{}, err
		}
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		p := st.Overlay("panel")
		return p != nil && st.Overlay("popover") == nil && len(p.Rows) > rowsBefore
	})
	return d.result("add", ok, verdict(ok, what+" added a row: "+rowLabels(d.St.Overlay("panel")), "no row was added"), nil), err
}

// mainParameter taps the knob icon at the lower right of a plugin
// parameter's mapping editor: the parameter becomes the node's main
// parameter (the icon turns green; the knob next to the node takes its
// name).
func (d *AUMDriver) mainParameter() (AUMResult, error) {
	p := d.St.Overlay("panel")
	if p == nil {
		return AUMResult{}, fmt.Errorf("MIDI CONTROL is not open")
	}
	knob := d.find(func(e *Element) bool {
		r := e.Rect
		return e.Type == "Button" && !e.Hidden && e.Label == "" && r.X > p.Frame.X+p.Frame.Width-70 &&
			r.Y > d.win.Height*0.6 && r.Width < 60
	})
	if knob == nil {
		return AUMResult{}, fmt.Errorf("no main-parameter knob icon: open a plugin parameter's mapping editor first (path)")
	}
	was := knob.Selected()
	if err := d.tapRect(knob.Rect, "the main-parameter knob icon"); err != nil {
		return AUMResult{}, err
	}
	kr := knob.Rect
	ok, err := d.until(aumWait, func(st AUMState) bool {
		k := d.find(func(e *Element) bool { return e.Type == "Button" && e.Rect == kr })
		return k != nil && k.Selected() != was
	})
	return d.result("main_parameter", ok, verdict(ok, "main parameter set (the knob next to the node now names it)", "tapped the knob icon; no change in AUM's tree (ipad_screenshot shows it green when set)"), nil), err
}
