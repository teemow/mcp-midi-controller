package ipadtest

import (
	"encoding/json"
	"sync"
)

// NodeMenu is the menu a long-press on a loaded node opens, in AUM 1.4.8's
// order, as a popover.
var NodeMenu = Overlay{Kind: "popover", Buttons: []string{"POST FADER", "PRE FADER", "SELECT", "REPLACE", "RELOAD"}}

// FailedNodes stands in for nodes AUM draws as failed (a ring of red stripes
// in the screenshot) and for RELOAD in their long-press menu, which loads a
// node again: a long-press on a node opens NodeMenu over the mixer, a tap on
// its RELOAD closes the menu and reloads that node, and any other tap closes
// it. A failed node comes up after the reloads it was given (Fail).
type FailedNodes struct {
	mu sync.Mutex
	// need is how many reloads each failed node still takes, by its frame
	// (-1: it never comes up).
	need map[[4]float64]int
	// reloads counts the reloads of each node.
	reloads map[[4]float64]int
	menu    *[4]float64 // the node whose menu is open
	w       *WDA
}

// NewFailedNodes attaches failed nodes to w. The menu is drawn over mixer;
// while it is closed w serves its SourceFunc as before, so set that first.
// Fail names the failed nodes.
func NewFailedNodes(w *WDA, mixer AUM) *FailedNodes {
	f := &FailedNodes{need: map[[4]float64]int{}, reloads: map[[4]float64]int{}, w: w}
	open := mixer
	open.Overlays = append(append([]Overlay{}, mixer.Overlays...), NodeMenu)
	menuSrc := open.Source()
	reload := popoverButton(NodeMenu, len(NodeMenu.Buttons)-1)

	w.mu.Lock()
	defer w.mu.Unlock()
	under, before := w.SourceFunc, w.OnGesture
	w.SourceFunc = func(gestures, reads int) json.RawMessage {
		f.mu.Lock()
		menu := f.menu != nil
		f.mu.Unlock()
		if menu {
			return menuSrc
		}
		return under(gestures, reads)
	}
	w.OnGesture = func(g Gesture) {
		if before != nil {
			before(g)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case g.Kind == "hold" && f.menu == nil:
			// Every node that was given to Fail has the menu, failed or
			// come up again.
			for frame := range f.need {
				if inside(frame, g.X, g.Y) {
					fr := frame
					f.menu = &fr
				}
			}
		case g.Kind == "tap" && f.menu != nil:
			node := *f.menu
			f.menu = nil
			if !inside(reload, g.X, g.Y) {
				return
			}
			f.reloads[node]++
			if n, failed := f.need[node]; failed && n > 0 {
				f.need[node] = n - 1
			}
			f.rings()
		}
	}
	return f
}

// Fail draws the node at frame (x, y, w, h in points) as failed until it has
// been reloaded reloads times; -1 keeps it failed whatever is done, 0 draws
// it up.
func (f *FailedNodes) Fail(frame [4]float64, reloads int) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.need[frame] = reloads
	f.rings()
}

// Reloads is how often the node at frame was reloaded.
func (f *FailedNodes) Reloads(frame [4]float64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads[frame]
}

// rings draws the nodes that are still failed. f.mu and f.w.mu must be held.
func (f *FailedNodes) rings() {
	f.w.RedRings = nil
	for frame, n := range f.need {
		if n != 0 {
			f.w.RedRings = append(f.w.RedRings, frame)
		}
	}
}

func inside(r [4]float64, x, y float64) bool {
	return x >= r[0] && x <= r[0]+r[2] && y >= r[1] && y <= r[1]+r[3]
}

// popoverButton is the frame of button i of a popover o, as overlay and body
// lay it out: below the navigation bar, the rows and the fields, 50 points
// apart.
func popoverButton(o Overlay, i int) [4]float64 {
	f := [4]float64{633, 186, 500, 400}
	if o.Frame != nil {
		f = *o.Frame
	}
	y := f[1] + 63 + float64(len(o.Rows)+len(o.Fields)+i)*50
	return [4]float64{f[0] + 16, y + 5, f[2] - 32, 40}
}
