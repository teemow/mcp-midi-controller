package ipad

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Named AUM operations: each locates its targets in AUM's accessibility tree
// (by label where AUM labels them, by the positions auv3-probe's
// docs/aum-ui-map.md measured otherwise), sends the gestures, and re-reads
// the tree to check that AUM shows the result. All of it runs in one
// WebDriverAgent session, so two-step buttons and short-lived menus get
// their second tap in time.

// AUMRequest carries the arguments of every named AUM operation; each
// operation reads the fields it needs.
type AUMRequest struct {
	Action string `json:"action"`
	// Panel names a screen for aum_panel.
	Panel string `json:"panel,omitempty"`
	// Channel selects a strip (see AUMState.Channel).
	Channel string `json:"channel,omitempty"`
	// Node selects a node of the strip (see AUMChannel.Node).
	Node string `json:"node,omitempty"`
	// Target is the destination of a move: a strip selector for channels,
	// a slot selector for nodes.
	Target string `json:"target,omitempty"`
	// Name is a channel, session, file or row name; NewName renames.
	Name    string `json:"name,omitempty"`
	NewName string `json:"new_name,omitempty"`
	// LevelDB is a fader level; BPM a tempo; Steps a count of arrow taps or
	// slots (negative = down/left); Bars a transport distance.
	LevelDB *float64 `json:"level_db,omitempty"`
	BPM     *float64 `json:"bpm,omitempty"`
	Steps   int      `json:"steps,omitempty"`
	Bars    int      `json:"bars,omitempty"`
	// On is the wanted state of a toggle (nil = flip it).
	On *bool `json:"on,omitempty"`
	// Source and Destination are MIDI routing endpoints.
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	// Path is a list of row labels to tap in turn (MIDI Control, FILES,
	// SOUNDFILES).
	Path []string `json:"path,omitempty"`
	// Setting and Value set a row (Settings, File Player options, Clock
	// Options); Value also selects the MIDI Control filter.
	Setting string `json:"setting,omitempty"`
	Value   string `json:"value,omitempty"`
	// Confirm must be true for operations that change or lose state.
	Confirm bool `json:"confirm,omitempty"`
}

// AUMResult is what a named AUM operation did and saw.
type AUMResult struct {
	Action string `json:"action"`
	// Verified is true when AUM's UI shows the expected result. A false
	// value is a normal outcome (the gesture went out but AUM shows
	// something else), not a tool error.
	Verified bool `json:"verified"`
	// Message says what was done and seen.
	Message string `json:"message"`
	// Steps are the gestures sent, in order.
	Steps []string `json:"steps,omitempty"`
	// Data is the operation's readout (a list, rows, a value).
	Data any `json:"data,omitempty"`
	// State is AUM's state after the operation.
	State AUMState `json:"state"`
}

// ErrNeedsConfirm is returned when an operation that changes or loses state
// was called without confirm.
var ErrNeedsConfirm = errors.New("needs confirm: true")

func needConfirm(r AUMRequest, what string) error {
	if r.Confirm {
		return nil
	}
	return fmt.Errorf("%w: %s. Take an ipad_screenshot first, and per AGENTS.md act only on a scratch session or a ' (mcp test)' copy, never an original", ErrNeedsConfirm, what)
}

// AUMDriver runs gestures against AUM inside one WDA session and keeps the
// last tree read.
type AUMDriver struct {
	ctx   context.Context
	s     *Session
	win   Size
	root  *Element
	St    AUMState
	steps []string
}

// Wait is how long an operation re-reads AUM for its result (one read takes
// 0.5–2 s).
var aumWait = 5 * time.Second

// DriveAUM opens a session bound to AUM, reads its state and runs fn.
func (c *Client) DriveAUM(ctx context.Context, fn func(d *AUMDriver) error) error {
	return c.WithApp(ctx, AUMBundleID, func(s *Session) error {
		d := &AUMDriver{ctx: ctx, s: s}
		var err error
		if d.win, err = s.WindowSize(ctx); err != nil {
			return err
		}
		if err := d.Read(); err != nil {
			return err
		}
		return fn(d)
	})
}

// Read re-reads AUM's tree.
func (d *AUMDriver) Read() error {
	root, err := d.s.Source(d.ctx)
	if err != nil {
		return err
	}
	d.root = root
	d.St = ParseAUM(root, d.win)
	return nil
}

func (d *AUMDriver) step(format string, a ...any) {
	d.steps = append(d.steps, fmt.Sprintf(format, a...))
}

func (d *AUMDriver) tap(x, y float64, what string) error {
	d.step("tap %s at (%.0f,%.0f)", what, x, y)
	return d.s.TapAt(d.ctx, x, y)
}

func (d *AUMDriver) tapRect(r Rect, what string) error {
	x, y := r.Center()
	return d.tap(x, y, what)
}

func (d *AUMDriver) doubleTap(x, y float64, what string) error {
	d.step("double-tap %s at (%.0f,%.0f)", what, x, y)
	return d.s.DoubleTap(d.ctx, x, y)
}

func (d *AUMDriver) tapTwice(r Rect, what string) error {
	x, y := r.Center()
	d.step("tap %s twice at (%.0f,%.0f)", what, x, y)
	return d.s.TapTwice(d.ctx, x, y, 0)
}

func (d *AUMDriver) hold(r Rect, what string) error {
	x, y := r.Center()
	d.step("long-press %s at (%.0f,%.0f)", what, x, y)
	return d.s.Hold(d.ctx, x, y, 1200*time.Millisecond)
}

func (d *AUMDriver) swipe(x1, y1, x2, y2 float64, what string) error {
	d.step("swipe %s (%.0f,%.0f)->(%.0f,%.0f)", what, x1, y1, x2, y2)
	return d.s.Swipe(d.ctx, x1, y1, x2, y2, 300*time.Millisecond)
}

func (d *AUMDriver) drag(what string, t DragTiming, pts ...Point) error {
	var b strings.Builder
	for i, p := range pts {
		if i > 0 {
			b.WriteString("->")
		}
		fmt.Fprintf(&b, "(%.0f,%.0f)", p.X, p.Y)
	}
	d.step("drag %s %s", what, b.String())
	return d.s.Drag(d.ctx, pts, t)
}

func (d *AUMDriver) typeText(index int, text string) (string, error) {
	d.step("type %q into text field %d", text, index)
	return d.s.TypeText(d.ctx, index, text)
}

func (d *AUMDriver) pressReturn() error {
	d.step("press Return")
	return d.s.PressReturn(d.ctx)
}

func (d *AUMDriver) pause(t time.Duration) error { return sleepCtx(d.ctx, t) }

// until re-reads AUM until cond holds or timeout passes.
func (d *AUMDriver) until(timeout time.Duration, cond func(st AUMState) bool) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if err := d.Read(); err != nil {
			return false, err
		}
		if cond(d.St) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := d.pause(150 * time.Millisecond); err != nil {
			return false, err
		}
	}
}

// result builds an AUMResult from the driver's steps and last state.
func (d *AUMDriver) result(action string, verified bool, msg string, data any) AUMResult {
	return AUMResult{Action: action, Verified: verified, Message: msg, Steps: d.steps, Data: data, State: d.St}
}

// find returns the first element of the last tree read matching fn.
func (d *AUMDriver) find(fn func(*Element) bool) *Element {
	if d.root == nil {
		return nil
	}
	return d.root.First(fn)
}

// findText finds a visible element whose label (or value) equals text
// (case-insensitive), preferring a button, within area (nil = anywhere).
func (d *AUMDriver) findText(text string, area *Rect) *Element {
	match := func(button bool) func(*Element) bool {
		return func(e *Element) bool {
			if e.Hidden || e.Rect.Empty() || !strings.EqualFold(strings.TrimSpace(e.Text()), text) {
				return false
			}
			if button != (e.Type == "Button") {
				return false
			}
			if area != nil {
				cx, cy := e.Rect.Center()
				return area.Contains(cx, cy)
			}
			return true
		}
	}
	if e := d.find(match(true)); e != nil {
		return e
	}
	return d.find(match(false))
}

// findPrefix finds a visible element whose label starts with prefix
// (case-insensitive) within area (nil = anywhere).
func (d *AUMDriver) findPrefix(prefix string, area *Rect) *Element {
	p := strings.ToLower(prefix)
	return d.find(func(e *Element) bool {
		if e.Hidden || e.Rect.Empty() || !strings.HasPrefix(strings.ToLower(e.Text()), p) {
			return false
		}
		if area != nil {
			cx, cy := e.Rect.Center()
			return area.Contains(cx, cy)
		}
		return true
	})
}

// tapText taps the element labelled text (see findText).
func (d *AUMDriver) tapText(text string, area *Rect) error {
	e := d.findText(text, area)
	if e == nil {
		return fmt.Errorf("no %q on screen (%s)", text, d.St.overlaySummary())
	}
	return d.tapRect(e.Rect, strings.ToUpper(text))
}

func (st AUMState) overlaySummary() string {
	if len(st.Overlays) == 0 {
		return "no popover or panel open"
	}
	var parts []string
	for _, o := range st.Overlays {
		p := o.Kind
		if o.Title != "" {
			p += " " + fmt.Sprintf("%q", o.Title)
		}
		if len(o.Buttons) > 0 {
			p += " with buttons " + strings.Join(limit(o.Buttons, 12), ", ")
		}
		parts = append(parts, p)
	}
	return "open: " + strings.Join(parts, "; ")
}

func limit(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), "…")
}

// channel finds a strip and brings it on screen.
func (d *AUMDriver) channel(sel string) (AUMChannel, error) {
	ch, err := d.St.Channel(sel)
	if err != nil || ch.OnScreen {
		return ch, err
	}
	strip := ch.Strip
	if err := d.scrollTo(func(st AUMState) (Rect, bool) {
		if strip > len(st.Channels) {
			return Rect{}, false
		}
		return st.Channels[strip-1].Frame, true
	}); err != nil {
		return ch, err
	}
	return d.St.Channels[strip-1], nil
}

// scrollTo scrolls the mixer sideways until the rect locate returns (a strip
// or the add tile) is on screen. It drags slowly and holds before release so
// the mixer does not fling, starting in a gap between two strips (a drag
// that starts on a node or fader would move that instead).
func (d *AUMDriver) scrollTo(locate func(AUMState) (Rect, bool)) error {
	for i := 0; i < 6; i++ {
		r, ok := locate(d.St)
		if !ok {
			return fmt.Errorf("lost track of the target while scrolling the mixer")
		}
		if r.X >= -1 && r.X+r.Width <= d.win.Width+1 {
			return nil
		}
		want := (d.win.Width - r.Width) / 2
		dx := math.Max(-(d.win.Width - 260), math.Min(d.win.Width-260, want-r.X))
		x0 := d.scrollGrip(dx)
		y := d.win.Height * 0.55
		if err := d.drag("the mixer", DragTiming{Move: 500 * time.Millisecond, Hold: 200 * time.Millisecond},
			Point{x0, y}, Point{x0 + dx, y}); err != nil {
			return err
		}
		if err := d.Read(); err != nil {
			return err
		}
		if r2, ok := locate(d.St); ok && math.Abs(r2.X-r.X) < 1 {
			return fmt.Errorf("the mixer did not scroll (drag from x %.0f by %.0f)", x0, dx)
		}
	}
	r, _ := locate(d.St)
	if r.X < -1 || r.X+r.Width > d.win.Width+1 {
		return fmt.Errorf("still off screen after scrolling the mixer")
	}
	return nil
}

// scrollGrip picks the x where a mixer drag of dx starts: a gap between two
// on-screen strips with room for the move.
func (d *AUMDriver) scrollGrip(dx float64) float64 {
	var gaps []float64
	for i := 0; i+1 < len(d.St.Channels); i++ {
		a, b := d.St.Channels[i].Frame, d.St.Channels[i+1].Frame
		g := (a.X + a.Width + b.X) / 2
		if g > 20 && g < d.win.Width-20 {
			gaps = append(gaps, g)
		}
	}
	if len(d.St.Channels) > 0 {
		last := d.St.Channels[len(d.St.Channels)-1].Frame
		if g := last.X + last.Width + 5; g > 20 && g < d.win.Width-20 {
			gaps = append(gaps, g)
		}
	}
	best := d.win.Width / 2
	bestRoom := -1.0
	for _, g := range gaps {
		end := g + dx
		room := math.Min(end-10, d.win.Width-10-end)
		if room > bestRoom {
			best, bestRoom = g, room
		}
	}
	return best
}
