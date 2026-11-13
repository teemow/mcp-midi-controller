package ipadtest

import (
	"encoding/json"
	"sync"
)

// FaderWire is the fader's wire of strip i of an unscrolled mixer, as AUM
// 1.4.8 draws it: a light line from the strip's wire towards the fader,
// 5 points below the last insert above the fader, or 5 points above the
// first insert when pre is 0 (every insert post fader). pre is how many
// inserts lie above the fader.
func FaderWire(i, pre int) [4]float64 {
	y := 221.0 - 5
	if pre > 0 {
		y = 221 + float64(pre-1)*84 + 74 + 5
	}
	// The strip's wire runs through the inserts' centres (StripX+72+37).
	return [4]float64{StripX(i) + 109, y - 0.5, 34, 1}
}

// FaderMenu stands in for PRE FADER and POST FADER in a node's long-press
// menu as AUM 1.4.8 has them: a long-press on an insert of an audio strip
// opens NodeMenu over the mixer, PRE FADER puts the strip's fader right
// below that insert and POST FADER right above it, and the menu stays open
// after either, moved or not; any other tap closes it. AUM shows where the
// fader sits only in the screenshot, so the stand-in draws each audio
// strip's FaderWire there and leaves the tree alone.
type FaderMenu struct {
	mu   sync.Mutex
	a    AUM
	pre  map[int]int // inserts above the fader, by strip index
	menu *[2]int     // strip index and insert of the open menu
	w    *WDA
}

// NewFaderMenu attaches the menu to w over mixer, every strip's inserts pre
// fader, as AUM adds them. It replaces w's SourceFunc.
func NewFaderMenu(w *WDA, mixer AUM) *FaderMenu {
	f := &FaderMenu{a: mixer, pre: map[int]int{}, w: w}
	for i, s := range mixer.Strips {
		f.pre[i] = len(s.Inserts)
	}
	open := mixer
	open.Overlays = append(append([]Overlay{}, mixer.Overlays...), NodeMenu)
	closedSrc, openSrc := mixer.Source(), open.Source()
	post, pre := popoverButton(NodeMenu, 0), popoverButton(NodeMenu, 1)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.SourceFunc = func(int, int) json.RawMessage {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.menu != nil {
			return openSrc
		}
		return closedSrc
	}
	w.OnGesture = func(g Gesture) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case g.Kind == "hold" && f.menu == nil:
			f.menu = f.insertAt(g.X, g.Y)
		case g.Kind == "tap" && f.menu != nil:
			at := *f.menu
			switch {
			case inside(post, g.X, g.Y):
				f.pre[at[0]] = at[1]
			case inside(pre, g.X, g.Y):
				f.pre[at[0]] = at[1] + 1
			default:
				f.menu = nil
			}
			f.wires()
		}
	}
	f.wires()
	return f
}

// insertAt is the strip and insert a point lies on, or nil.
func (f *FaderMenu) insertAt(x, y float64) *[2]int {
	for i, s := range f.a.Strips {
		if s.Level == "" {
			continue
		}
		for k, n := range s.Inserts {
			nx := StripX(i) + 72
			if n.Bypassed {
				nx -= 55
			}
			if inside([4]float64{nx, 221 + float64(k)*84, 74, 74}, x, y) {
				return &[2]int{i, k}
			}
		}
	}
	return nil
}

// SetPreFader puts strip i's fader below its first pre inserts.
func (f *FaderMenu) SetPreFader(i, pre int) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pre[i] = pre
	f.wires()
}

// PreFader is how many of strip i's inserts lie above its fader.
func (f *FaderMenu) PreFader(i int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pre[i]
}

// Open reports whether the menu is open.
func (f *FaderMenu) Open() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.menu != nil
}

// wires draws every audio strip's fader's wire. f.mu and f.w.mu must be
// held.
func (f *FaderMenu) wires() {
	f.w.Wires = nil
	for i, s := range f.a.Strips {
		if s.Level != "" {
			f.w.Wires = append(f.w.Wires, FaderWire(i, f.pre[i]))
		}
	}
}
