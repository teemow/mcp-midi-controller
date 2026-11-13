package ipad

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// AUM keeps only what reaches into its window in the accessibility tree: of a
// mixer wider than the window the tree holds the strips on screen, and the
// others are not in it until the mixer is scrolled to them. On the rig (AUM
// 1.4.8, a 1210-point window) a mix of nine strips showed five in the tree;
// the add tile's column is there only while it reaches into the window.
// What needs every strip, their titles or a strip by name, walks the mixer
// a window at a time.

// defaultPitch is how far apart two strips' left edges are in AUM 1.4.8: a
// strip is 228 points wide, the gap between two 14.
const defaultPitch = 242

// mixerSettle is how long a walk waits between reads while the mixer still
// moves after a drag: at its ends AUM bounces it back (WebDriverAgent does
// not wait for animations, see Session).
var mixerSettle = 250 * time.Millisecond

// maxWalkSteps bounds a walk in one direction (a step scrolls about half a
// window: some 140 strips in landscape).
const maxWalkSteps = 60

// mixerWalk walks the mixer sideways and keeps every strip it has seen,
// placed by its position in the mixer's content, which stays put while the
// mixer scrolls.
type mixerWalk struct {
	d *AUMDriver
	// offset is a strip's content x minus its x on screen in the last read.
	offset float64
	// seen are the strips seen, by content x.
	seen []seenStrip
	// first and last say the walk has seen the mixer's first strip and its
	// last: the scroll bar at 0 % or a step towards the start that did not
	// get there, and the add tile right of the last strip or a step towards
	// the end that did not get there.
	first, last bool
	// visit is called with each read of the walk, the one it starts from
	// included.
	visit func(AUMState)
}

// seenStrip is a strip as the walk saw it: x is its content x, ch the read
// it was seen in whole when there was one.
type seenStrip struct {
	x  float64
	ch AUMChannel
}

// walk starts a walk at the last read.
func (d *AUMDriver) walk(visit func(AUMState)) *mixerWalk {
	w := &mixerWalk{d: d, visit: visit}
	w.record()
	return w
}

// whole reports whether the walk has seen the mixer from its first strip to
// its last.
func (w *mixerWalk) whole() bool { return w.first && w.last }

// atEnd reports whether the walk has seen the mixer's end in direction dir
// (1: towards the add tile, -1: towards the first strip).
func (w *mixerWalk) atEnd(dir int) bool {
	if dir > 0 {
		return w.last
	}
	return w.first
}

// record adds the last read's strips to those seen.
func (w *mixerWalk) record() {
	st := w.d.St
	for _, ch := range st.Channels {
		x := ch.Frame.X + w.offset
		switch i := w.index(ch.Title, x); {
		case i < 0:
			w.seen = append(w.seen, seenStrip{x, ch})
		case ch.OnScreen || !w.seen[i].ch.OnScreen:
			w.seen[i] = seenStrip{x, ch}
		}
	}
	sort.SliceStable(w.seen, func(i, j int) bool { return w.seen[i].x < w.seen[j].x })
	w.first = w.first || st.atMixerStart()
	w.last = w.last || st.AddTile != nil
	if w.visit != nil {
		w.visit(st)
	}
}

// index finds the strip seen with title at content x.
func (w *mixerWalk) index(title string, x float64) int {
	for i, s := range w.seen {
		if s.ch.Title == title && math.Abs(s.x-x) < defaultPitch/2 {
			return i
		}
	}
	return -1
}

// channels are the strips seen, left to right, numbered from 1: their
// positions in the mixer once the walk has seen its first strip.
func (w *mixerWalk) channels() []AUMChannel {
	out := make([]AUMChannel, len(w.seen))
	for i, s := range w.seen {
		out[i] = s.ch
		out[i].Strip = i + 1
	}
	return out
}

// stepSize is how far a step scrolls: short enough that a strip whole on
// screen before it still is after it, so two reads always share a strip to
// measure the move by.
func (w *mixerWalk) stepSize() float64 {
	return math.Max(200, w.d.win.Width-2*w.d.St.pitch()-20)
}

// pitch is how far apart the strips in the tree are, by the middle of their
// distances.
func (st AUMState) pitch() float64 {
	var ds []float64
	for i := 1; i < len(st.Channels); i++ {
		ds = append(ds, st.Channels[i].Frame.X-st.Channels[i-1].Frame.X)
	}
	if len(ds) == 0 {
		return defaultPitch
	}
	sort.Float64s(ds)
	return ds[len(ds)/2]
}

// step scrolls the mixer a step towards its end (dir 1: the add tile comes
// into view) or its start (dir -1). A mixer that moves less than half the
// step has come to that end; full reports that it did not.
func (w *mixerWalk) step(dir int) (full bool, err error) {
	dx := -float64(dir) * w.stepSize()
	shift, err := w.drag(dx)
	if err != nil {
		return false, err
	}
	if math.Abs(shift) >= math.Abs(dx)/2 {
		return true, nil
	}
	if dir > 0 {
		w.last = true
	} else {
		w.first = true
	}
	return false, nil
}

// drag drags the mixer by dx, reads AUM until its strips hold still, and
// returns how far they moved.
func (w *mixerWalk) drag(dx float64) (float64, error) {
	d := w.d
	prev := d.St.Channels
	if len(prev) == 0 {
		return 0, fmt.Errorf("no strip in view to scroll the mixer by")
	}
	if _, err := d.dragMixer(dx); err != nil {
		return 0, err
	}
	last := math.NaN()
	for reads := 1; ; reads++ {
		if err := d.Read(); err != nil {
			return 0, err
		}
		s, ok := stripShift(prev, d.St.Channels, dx)
		// The strips followed the finger (less the few points a touch takes
		// to start a scroll), or hold still short of it: an end of the
		// mixer, which AUM bounces back to. After four reads the last one
		// stands.
		if ok && (math.Abs(s-dx) <= math.Max(2, math.Abs(dx)*0.05) || math.Abs(s-last) <= 1 || reads >= 4) {
			w.offset -= s
			w.record()
			return s, nil
		}
		if reads >= 4 {
			return 0, fmt.Errorf("lost track of the strips while scrolling the mixer by %.0f: none of %s is in view after it (in view: %s)",
				dx, titles(prev), titles(d.St.Channels))
		}
		if ok {
			last = s
		}
		if err := d.pause(mixerSettle); err != nil {
			return 0, err
		}
	}
}

// stripShift is how far the strips moved from one read to the next: the move
// most strips of one title share, of those the one nearest to want. ok is
// false when no strip is in both reads.
func stripShift(prev, cur []AUMChannel, want float64) (float64, bool) {
	type move struct {
		by float64
		n  int
	}
	var moves []move
	for _, a := range prev {
		for _, b := range cur {
			if a.Title != b.Title {
				continue
			}
			by := b.Frame.X - a.Frame.X
			found := false
			for i := range moves {
				if math.Abs(moves[i].by-by) <= 1 {
					moves[i].n++
					found = true
					break
				}
			}
			if !found {
				moves = append(moves, move{by, 1})
			}
		}
	}
	if len(moves) == 0 {
		return 0, false
	}
	best := moves[0]
	for _, m := range moves[1:] {
		if m.n > best.n || (m.n == best.n && math.Abs(m.by-want) < math.Abs(best.by-want)) {
			best = m
		}
	}
	return best.by, true
}

func titles(chs []AUMChannel) string {
	if len(chs) == 0 {
		return "none"
	}
	out := make([]string, len(chs))
	for i, ch := range chs {
		out[i] = fmt.Sprintf("%q", ch.Title)
	}
	return strings.Join(out, ", ")
}

// toEnd walks the mixer in direction dir until it has seen that end.
func (w *mixerWalk) toEnd(dir int) error {
	for i := 0; !w.atEnd(dir); i++ {
		if i == maxWalkSteps {
			return fmt.Errorf("the mixer has no end after %d steps", i)
		}
		if _, err := w.step(dir); err != nil {
			return err
		}
	}
	return nil
}

// home scrolls the mixer back to its first strip, where AUM shows a session
// it has loaded.
func (w *mixerWalk) home() error {
	for i := 0; !w.d.St.atMixerStart(); i++ {
		if i == maxWalkSteps {
			return fmt.Errorf("the mixer has no start after %d steps", i)
		}
		if full, err := w.step(-1); err != nil || !full {
			return err
		}
	}
	return nil
}

// find resolves sel among the strips seen. A position ("#3") counts from the
// mixer's first strip, so it resolves only once the walk has seen that.
func (w *mixerWalk) find(sel string) (seenStrip, error) {
	all := AUMState{Channels: w.channels()}
	if isPosition(sel) && !w.first {
		_, err := all.Channel(sel)
		if err == nil {
			err = noChannel("strip %s is counted from the mixer's first strip, which is not in view", strings.TrimSpace(sel))
		}
		return seenStrip{}, err
	}
	ch, err := all.Channel(sel)
	if err != nil {
		return seenStrip{}, err
	}
	return w.seen[ch.Strip-1], nil
}

// bring scrolls the strip seen at s onto the screen and returns it as the
// last read shows it there.
func (w *mixerWalk) bring(s seenStrip) (AUMChannel, error) {
	d := w.d
	for i := 0; ; i++ {
		x := s.x - w.offset // where it is on screen now
		ch, ok := w.current(s)
		if ok && ch.OnScreen {
			return ch, nil
		}
		if ok {
			x = ch.Frame.X
		}
		if i == maxWalkSteps {
			return ch, fmt.Errorf("strip %q is still off screen after scrolling the mixer", s.ch.Title)
		}
		step := w.stepSize()
		dx := math.Max(-step, math.Min(step, (d.win.Width-s.ch.Frame.Width)/2-x))
		shift, err := w.drag(dx)
		if err != nil {
			return AUMChannel{}, err
		}
		if math.Abs(shift) < 1 {
			return ch, fmt.Errorf("the mixer did not scroll (drag by %.0f) to bring strip %q into view", dx, s.ch.Title)
		}
	}
}

// current is the strip seen at s as the last read shows it.
func (w *mixerWalk) current(s seenStrip) (AUMChannel, bool) {
	for _, ch := range w.d.St.Channels {
		if ch.Title == s.ch.Title && math.Abs(ch.Frame.X+w.offset-s.x) < defaultPitch/2 {
			return ch, true
		}
	}
	return AUMChannel{}, false
}

// channel finds a strip and brings it on screen. A strip that is not in view
// is looked for along the mixer, towards its end first and then towards its
// start (a position towards the start first, where it is counted from).
func (d *AUMDriver) channel(sel string) (AUMChannel, error) {
	w := d.walk(nil)
	dirs := []int{1, -1}
	if isPosition(sel) {
		dirs = []int{-1, 1}
	}
	var none *noChannelError
	for _, dir := range dirs {
		for i := 0; ; i++ {
			s, err := w.find(sel)
			switch {
			case err == nil:
				return w.bring(s)
			case !errors.As(err, &none):
				return AUMChannel{}, err
			case w.atEnd(dir):
			case i == maxWalkSteps:
				return AUMChannel{}, fmt.Errorf("the mixer has no end after %d steps", i)
			default:
				if _, err := w.step(dir); err != nil {
					return AUMChannel{}, err
				}
				continue
			}
			break
		}
	}
	_, err := w.find(sel)
	if err != nil && w.whole() && len(w.seen) > len(d.St.Channels) {
		err = fmt.Errorf("%w; the mixer was scrolled from its first strip to its last", err)
	}
	return AUMChannel{}, err
}

// mixerRead is a read of every strip of the mixer.
type mixerRead struct {
	// Channels are the strips left to right, numbered from 1.
	Channels []AUMChannel
	// Failed are the titles of the strips with a node AUM draws as failed;
	// Problems what on screen reports a fault (see problems), at each stop
	// of the walk.
	Failed   []string
	Problems []string
}

// readMixer reads every strip of the mixer. When the tree does not hold the
// whole mixer, the mixer is walked to its add tile and back to its first
// strip, where it is left. With faults it also looks for failed nodes and
// fault reports at every stop.
func (d *AUMDriver) readMixer(faults bool) (mixerRead, error) {
	var out mixerRead
	var visit func(AUMState)
	if faults {
		visit = func(AUMState) {
			out.Failed = appendNew(out.Failed, d.failedStrips()...)
			out.Problems = appendNew(out.Problems, d.problems()...)
		}
	}
	w := d.walk(visit)
	if !w.whole() && len(d.St.Channels) > 0 {
		if err := w.toEnd(1); err != nil {
			return out, err
		}
		if err := w.home(); err != nil {
			return out, err
		}
	}
	out.Channels = w.channels()
	return out, nil
}

// appendNew appends the items of add that list does not hold yet.
func appendNew(list []string, add ...string) []string {
	for _, s := range add {
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

// dragMixer drags the mixer sideways by dx: slowly, and held before the
// release so it does not fling, from a gap between two strips (a drag that
// starts on a node or fader would move that instead). It returns where the
// drag started.
func (d *AUMDriver) dragMixer(dx float64) (float64, error) {
	x0 := d.scrollGrip(dx)
	y := d.win.Height * 0.55
	return x0, d.drag("the mixer", DragTiming{Move: 500 * time.Millisecond, Hold: 200 * time.Millisecond},
		Point{x0, y}, Point{x0 + dx, y})
}
