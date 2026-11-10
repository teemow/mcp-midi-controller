package ipad

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// The gestures below mirror auv3-probe's scripts/ipad (dtap, drag, confirm,
// type, enter, fields): the same WebDriverAgent requests, the same timings.

// Point is a screen position in points.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// DoubleTap double-taps a point (AUM: reset a fader, edit the tempo value,
// add an insert slot below the last one).
func (s *Session) DoubleTap(ctx context.Context, x, y float64) error {
	return s.c.call(ctx, http.MethodPost, s.path("/wda/doubleTap"), map[string]any{"x": x, "y": y}, nil)
}

// ConfirmGap is the pause between the two taps of TapTwice. AUM's two-step
// buttons (CLEAR, OVERWRITE?, REMOVE CHANNEL -> TAP TO CONFIRM) take the
// second tap only for about a second, and each WDA action takes about 0.8 s,
// so both taps go out in one session with a short gap.
const ConfirmGap = 500 * time.Millisecond

// TapTwice taps a point twice, gap apart (≤0 = ConfirmGap): the second tap
// confirms a two-step button.
func (s *Session) TapTwice(ctx context.Context, x, y float64, gap time.Duration) error {
	if gap <= 0 {
		gap = ConfirmGap
	}
	if err := s.TapAt(ctx, x, y); err != nil {
		return err
	}
	if err := sleepCtx(ctx, gap); err != nil {
		return err
	}
	return s.TapAt(ctx, x, y)
}

// DragTiming shapes a Drag. Zero fields take the scripts/ipad defaults.
type DragTiming struct {
	// Move is how long each leg between two points takes (default 400 ms).
	Move time.Duration
	// Rest is the pause at each point after a leg (default 150 ms).
	Rest time.Duration
	// Hold is the pause before the finger lifts (default 1 s).
	Hold time.Duration
	// Press is a pause right after the finger goes down (default 0); a
	// long press before moving turns AUM's node drag into a reorder.
	Press time.Duration
}

func (t DragTiming) withDefaults() DragTiming {
	if t.Move <= 0 {
		t.Move = 400 * time.Millisecond
	}
	if t.Rest <= 0 {
		t.Rest = 150 * time.Millisecond
	}
	if t.Hold <= 0 {
		t.Hold = time.Second
	}
	return t
}

// Drag presses at pts[0], moves through the other points (resting at each),
// holds and releases, as one W3C touch action. AUM shows some drag states (a
// node's DSP % and latency, the reorder target) only while the finger is
// down and drops a quick release, which is why the hold matters: use it to
// reorder nodes and channels and to move plugin windows. Swipe is enough for
// faders, knobs and scrolling.
func (s *Session) Drag(ctx context.Context, pts []Point, t DragTiming) error {
	if len(pts) < 2 {
		return fmt.Errorf("drag needs at least two points, got %d", len(pts))
	}
	t = t.withDefaults()
	steps := []any{
		map[string]any{"type": "pointerMove", "duration": 0, "x": pts[0].X, "y": pts[0].Y},
		map[string]any{"type": "pointerDown", "button": 0},
	}
	if t.Press > 0 {
		steps = append(steps, map[string]any{"type": "pause", "duration": t.Press.Milliseconds()})
	}
	for _, p := range pts[1:] {
		steps = append(steps,
			map[string]any{"type": "pointerMove", "duration": t.Move.Milliseconds(), "x": p.X, "y": p.Y},
			map[string]any{"type": "pause", "duration": t.Rest.Milliseconds()})
	}
	steps = append(steps,
		map[string]any{"type": "pause", "duration": t.Hold.Milliseconds()},
		map[string]any{"type": "pointerUp", "button": 0})
	return s.touch(ctx, steps)
}

func (s *Session) touch(ctx context.Context, steps []any) error {
	return s.c.call(ctx, http.MethodPost, s.path("/actions"), map[string]any{"actions": []any{map[string]any{
		"type": "pointer", "id": "finger", "parameters": map[string]any{"pointerType": "touch"},
		"actions": steps,
	}}}, nil)
}

// TextField is one editable text element on screen.
type TextField struct {
	// Index is the position in TextFields' list (what TypeText takes).
	Index int `json:"index"`
	// Kind is "text_field", "search_field" or "text_view".
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Rect  Rect   `json:"rect"`
	id    string
}

var textFieldClasses = []struct{ class, kind string }{
	{"XCUIElementTypeTextField", "text_field"},
	{"XCUIElementTypeSearchField", "search_field"},
	{"XCUIElementTypeTextView", "text_view"},
}

// TextFields lists the text fields, then search fields, then text views on
// screen, with their values and frames. AUM's name fields (Save, Edit
// Channel, bus names) and number dialogs are text fields; its notes editor
// is a text view.
func (s *Session) TextFields(ctx context.Context) ([]TextField, error) {
	var out []TextField
	for _, c := range textFieldClasses {
		var found []map[string]string
		if err := s.c.call(ctx, http.MethodPost, s.path("/elements"),
			map[string]any{"using": "class name", "value": c.class}, &found); err != nil {
			return nil, err
		}
		for _, f := range found {
			id := elementID(f)
			if id == "" {
				continue
			}
			tf := TextField{Index: len(out), Kind: c.kind, id: id}
			if err := s.c.call(ctx, http.MethodGet, s.path("/element/"+id+"/rect"), nil, &tf.Rect); err != nil {
				return nil, err
			}
			v, err := s.attribute(ctx, id, "value")
			if err != nil {
				return nil, err
			}
			tf.Value = v
			out = append(out, tf)
		}
	}
	return out, nil
}

func (s *Session) attribute(ctx context.Context, id, name string) (string, error) {
	var raw any
	if err := s.c.call(ctx, http.MethodGet, s.path("/element/"+id+"/attribute/"+name), nil, &raw); err != nil {
		return "", err
	}
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return fmt.Sprint(v), nil
	}
}

// ErrNoTextField is returned by TypeText when the field index is not on
// screen.
var ErrNoTextField = fmt.Errorf("no such text field on screen")

// TypeText focuses text field index (in TextFields' order), clears it, types
// text and reads the field's value back. It does not press Return.
func (s *Session) TypeText(ctx context.Context, index int, text string) (string, error) {
	return s.typeInto(ctx, index, text, true)
}

// FillText clears text field index and types text like TypeText, without
// tapping the field first. AUM's number alerts (Set level, Edit Value) are
// filled this way: after a tap on their field they ignored their Set button.
func (s *Session) FillText(ctx context.Context, index int, text string) (string, error) {
	return s.typeInto(ctx, index, text, false)
}

func (s *Session) typeInto(ctx context.Context, index int, text string, focus bool) (string, error) {
	fields, err := s.TextFields(ctx)
	if err != nil {
		return "", err
	}
	if index < 0 || index >= len(fields) {
		return "", fmt.Errorf("%w: field %d of %d", ErrNoTextField, index, len(fields))
	}
	id := fields[index].id
	if focus {
		if err := s.c.call(ctx, http.MethodPost, s.path("/element/"+id+"/click"), map[string]any{}, nil); err != nil {
			return "", err
		}
		if err := sleepCtx(ctx, 400*time.Millisecond); err != nil {
			return "", err
		}
	}
	if err := s.c.call(ctx, http.MethodPost, s.path("/element/"+id+"/clear"), map[string]any{}, nil); err != nil {
		return "", err
	}
	chars := make([]string, 0, len(text))
	for _, r := range text {
		chars = append(chars, string(r))
	}
	if err := s.c.call(ctx, http.MethodPost, s.path("/element/"+id+"/value"), map[string]any{"value": chars}, nil); err != nil {
		return "", err
	}
	return s.attribute(ctx, id, "value")
}

// PressReturn presses Return on the on-screen keyboard (AUM's Save panel
// and channel name field apply on Return).
func (s *Session) PressReturn(ctx context.Context) error {
	return s.c.call(ctx, http.MethodPost, s.path("/wda/keys"), map[string]any{"value": []string{"\n"}}, nil)
}

// LabelRect finds the element labelled label (case-insensitive, buttons
// first) like TapLabel, without tapping it, and returns its frame and the
// number of matches.
func (s *Session) LabelRect(ctx context.Context, label string, wait time.Duration) (Rect, int, error) {
	id, n, err := s.findLabel(ctx, label, wait)
	if err != nil {
		return Rect{}, 0, err
	}
	var r Rect
	err = s.c.call(ctx, http.MethodGet, s.path("/element/"+id+"/rect"), nil, &r)
	return r, n, err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
