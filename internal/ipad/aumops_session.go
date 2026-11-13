package ipad

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AUMLoaded is what AUM shows once a session has been opened.
type AUMLoaded struct {
	// Session is the name the main menu shows.
	Session string `json:"session"`
	// Strips are the mixer's strip titles, left to right.
	Strips []string `json:"strips"`
	// Problems are what on screen reports a fault: an alert's title, a label
	// that says a plugin failed or was invalidated, a node drawn as failed.
	Problems []string `json:"problems,omitempty"`
	// Failed are the titles of the strips with a node AUM draws as failed:
	// its plugin did not load, or crashed. FailedNodes are those nodes.
	Failed      []string        `json:"failed,omitempty"`
	FailedNodes []AUMFailedNode `json:"failed_nodes,omitempty"`
	// LoadMS is how long AUM showed that it was loading.
	LoadMS int64 `json:"load_ms,omitempty"`
	// Restarted is set when AUM was not running at the load and was started
	// again for it: what WebDriverAgent said of AUM before that.
	Restarted string `json:"restarted,omitempty"`
}

// AUMFailedNode is a node AUM draws as failed: a ring of red stripes, for a
// plugin that did not load or crashed. RELOAD in the node's menu (aum_node
// reload) starts the plugin anew, which brings back one that failed as AUM
// initialised it.
type AUMFailedNode struct {
	// Strip is its strip's title, Node its selector on that strip as
	// aum_node takes it: "source", an insert slot's number, a MIDI node's
	// index.
	Strip string `json:"strip"`
	Node  string `json:"node"`
	// Role, Slot and Label are the node's (see AUMNode).
	Role  string `json:"role"`
	Slot  int    `json:"slot,omitempty"`
	Label string `json:"label,omitempty"`
}

// failedNode is the AUMFailedNode of node n on strip ch.
func failedNode(ch AUMChannel, n AUMNode) AUMFailedNode {
	sel := n.Role
	switch n.Role {
	case "insert":
		sel = strconv.Itoa(n.Slot)
	case "midi":
		sel = strconv.Itoa(n.Index)
	}
	return AUMFailedNode{Strip: ch.Title, Node: sel, Role: n.Role, Slot: n.Slot, Label: n.Label}
}

// String names the node for a message: its strip and its place there.
func (f AUMFailedNode) String() string {
	where := f.Role
	switch f.Role {
	case "insert":
		where = fmt.Sprintf("insert slot %d", f.Slot)
	case "midi":
		where = "MIDI node " + f.Node
	}
	if f.Label != "" {
		where += fmt.Sprintf(" %q", f.Label)
	}
	return fmt.Sprintf("%s of %q", where, f.Strip)
}

// setFailed takes the failed nodes of a read of the mixer: Failed names
// their strips, and Problems says each strip once.
func (l *AUMLoaded) setFailed(nodes []AUMFailedNode) {
	l.FailedNodes = nodes
	l.Failed = nil
	for _, n := range nodes {
		if !slices.Contains(l.Failed, n.Strip) {
			l.Failed = append(l.Failed, n.Strip)
			l.Problems = append(l.Problems, fmt.Sprintf("AUM draws a node of %q as failed: its plugin did not load", n.Strip))
		}
	}
}

// loadingLabel is the overlay AUM shows while it restores a session's
// plugins.
const loadingLabel = "LOADING STATE"

// aumLoadTimeout bounds the wait for AUM to finish loading a session. A
// session of a dozen plugins takes 5 to 20 seconds.
var aumLoadTimeout = 120 * time.Second

// aumStartWait is how long a freshly started AUM is given to show its mixer
// before it is driven.
var aumStartWait = 15 * time.Second

// AUMOpenSession loads a session from FILES and waits until AUM has finished
// loading it. Unlike the bare load action it clears the screen first (FILES
// does not open over a plugin window or another panel) and it does not return
// while AUM still restores the plugins' state, so what follows can rely on the
// session being there. The result's Data is an AUMLoaded.
//
// Loading replaces the open session; the caller is responsible for having
// asked.
//
// AUM can go away under a load: on the rig it crashed while it replaced the
// open session, in the system's own code. WebDriverAgent then fails the
// session's next call (AppNotRunning) and the iPad shows its Home Screen,
// where nothing keeps it awake. Of the session that was open nothing is left
// that the load would not have replaced, and a new WebDriverAgent session
// starts an app that is not running. So the load is made once more in a new
// session: AUM starts and loads the session, and the result says so
// (AUMLoaded.Restarted). An error that is still AppNotRunning means AUM went
// away again, or did not start.
func (c *Client) AUMOpenSession(ctx context.Context, path []string, name string) (AUMResult, error) {
	res, err := c.aumOpenSession(ctx, path, name, false)
	if !AppNotRunning(err) || ctx.Err() != nil {
		return res, err
	}
	gone := err
	res, err = c.aumOpenSession(ctx, path, name, true)
	if err != nil {
		return res, fmt.Errorf("%w (AUM had been started again after: %v)", err, gone)
	}
	if loaded, ok := res.Data.(AUMLoaded); ok {
		loaded.Restarted = gone.Error()
		res.Data = loaded
	}
	res.Message = fmt.Sprintf("AUM was not running (%v): the daemon started it again and loaded the session once more. %s", gone, res.Message)
	return res, nil
}

// aumOpenSession is one go at AUMOpenSession. started says that the session
// is expected to have started AUM, which then is given time to show its
// mixer.
func (c *Client) aumOpenSession(ctx context.Context, path []string, name string, started bool) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		if started && !d.St.ready() {
			// WebDriverAgent returns from the launch before AUM has drawn
			// its screen. One that never shows is the load's to report: the
			// menu's gestures go by coordinates and would land on whatever
			// is there instead.
			ready, err := d.until(aumStartWait, AUMState.ready)
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("AUM was started again but did not show its screen within %s", aumStartWait)
			}
		}
		if err := d.clearScreen(); err != nil {
			return err
		}
		start := time.Now()
		loaded, err := d.loadSession(path, name)
		if err != nil {
			return err
		}
		// loadSession leaves the main menu open with the session's name. It
		// may have waited out most of the load already; the rest of
		// aumLoadTimeout is what is left for AUM to finish.
		out := AUMLoaded{Session: d.St.Session}
		done, err := d.until(aumLoadTimeout-time.Since(start), func(AUMState) bool { return !d.loading() })
		if err != nil {
			return err
		}
		out.LoadMS = time.Since(start).Milliseconds()
		if err := d.clearScreen(); err != nil {
			return err
		}
		mix, err := d.readMixer(true)
		if err != nil {
			return err
		}
		out.setMixer(mix)
		ok := loaded.Verified && done
		msg := fmt.Sprintf("AUM has %q open: %d strip(s)", out.Session, len(out.Strips))
		switch {
		case !loaded.Verified:
			msg = loaded.Message
		case !done:
			msg = fmt.Sprintf("AUM still shows %s after %s", loadingLabel, aumLoadTimeout)
		}
		if len(out.Problems) > 0 {
			msg += "; on screen: " + strings.Join(out.Problems, "; ")
		}
		res = d.result("open_session", ok, msg, out)
		return nil
	})
	return res, err
}

// ready reports whether the tree read is AUM's own screen: its top bar with
// the tempo is there once AUM has come up.
func (st AUMState) ready() bool {
	return st.TempoBPM != nil || st.TempoBar || st.StartScreen || st.MenuOpen
}

// AUMSession reads which session AUM has open without changing it: it opens
// the main menu for the name, closes it again and lists the strips and
// whatever on screen reports a fault. The result's Data is an AUMLoaded.
func (c *Client) AUMSession(ctx context.Context) (AUMResult, error) {
	return c.readAUM(ctx, true)
}

// AUMMixer reads the mixer of the session AUM has open, without its name:
// the strips and whatever on screen reports a fault. It is AUMSession for a
// caller that knows the session otherwise, and leaves AUM's menu shut. The
// result's Data is an AUMLoaded with an empty Session.
func (c *Client) AUMMixer(ctx context.Context) (AUMResult, error) {
	return c.readAUM(ctx, false)
}

// readAUM reads the open session's strips and faults and, with menu, its
// name, which only AUM's main menu shows.
func (c *Client) readAUM(ctx context.Context, menu bool) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		// Faults are read first: an alert would be dismissed on the way to
		// the menu.
		out := AUMLoaded{Problems: d.problems()}
		if err := d.clearScreen(); err != nil {
			return err
		}
		if menu {
			if ok, err := d.ensureMenu(); err != nil || !ok {
				if err != nil {
					return err
				}
				return fmt.Errorf("AUM's main menu did not open (%s)", d.St.overlaySummary())
			}
			out.Session = d.St.Session
			if err := d.clearScreen(); err != nil {
				return err
			}
		}
		mix, err := d.readMixer(true)
		if err != nil {
			return err
		}
		out.setMixer(mix)
		if !menu {
			res = d.result("mixer", true, fmt.Sprintf("AUM's mixer has %d strip(s)", len(out.Strips)), out)
			return nil
		}
		res = d.result("session", out.Session != "", fmt.Sprintf("AUM has %q open: %d strip(s)", out.Session, len(out.Strips)), out)
		return nil
	})
	return res, err
}

// setMixer takes the strips and faults of a read of the whole mixer.
func (l *AUMLoaded) setMixer(mix mixerRead) {
	l.Strips = make([]string, len(mix.Channels))
	for i, ch := range mix.Channels {
		l.Strips[i] = ch.Title
	}
	l.Problems = appendNew(l.Problems, mix.Problems...)
	l.setFailed(mix.Failed)
}

// clearScreen closes plugin windows and whatever lies over the mixer, so the
// next operation starts from the bare mixer.
func (d *AUMDriver) clearScreen() error {
	for i := 0; i < 8; i++ {
		switch {
		case len(d.St.PluginWindows) > 0:
			if _, err := d.pluginWindow(AUMRequest{Action: "window_close"}); err != nil {
				return err
			}
		case d.St.Overlay("") != nil || d.St.TempoBar || d.St.Keyboard:
			if _, err := d.closeTop(); err != nil {
				return err
			}
		default:
			return nil
		}
		if err := d.Read(); err != nil {
			return err
		}
	}
	return fmt.Errorf("AUM's screen did not clear (%s)", d.St.overlaySummary())
}

// loading reports whether AUM shows that it is still loading a session.
func (d *AUMDriver) loading() bool {
	return d.find(func(e *Element) bool {
		return !e.Hidden && strings.EqualFold(strings.TrimSpace(e.Label), loadingLabel)
	}) != nil
}

// A node counts as failed when most of its ring is red and what lies around
// the node is not (a screen that is red all over says nothing about a node).
const (
	failedRingFrom, failedRingTo = 0.80, 0.90
	aroundRingFrom, aroundRingTo = 1.15, 1.30
	failedRingShare              = 0.6
)

// failedNodes returns the nodes on screen that AUM draws as failed. AUM says
// so only in the node's colour, a ring of red stripes, so this takes a
// screenshot; it reports nothing when that cannot be had. readMixer looks at
// every strip.
func (d *AUMDriver) failedNodes() []AUMFailedNode {
	raw, err := d.s.c.screenshot(d.ctx)
	if err != nil {
		return nil
	}
	scr, err := d.s.c.ScreenInfo(d.ctx)
	if err != nil {
		return nil
	}
	img, err := DecodePNG(raw)
	if err != nil {
		return nil
	}
	var out []AUMFailedNode
	for _, ch := range d.St.Channels {
		for _, n := range ch.Nodes {
			if n.Empty || !n.OnScreen || !ch.OnScreen || n.Role == "output" {
				continue
			}
			if RingRedness(img, scr.Scale, n.Frame, failedRingFrom, failedRingTo) >= failedRingShare &&
				RingRedness(img, scr.Scale, n.Frame, aroundRingFrom, aroundRingTo) < failedRingShare/2 {
				out = append(out, failedNode(ch, n))
			}
		}
	}
	return out
}

// problems collects what on screen reports a fault: every alert, and every
// label that says something failed or was invalidated (AUM names the plugin
// in it).
func (d *AUMDriver) problems() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.Join(strings.Fields(s), " "); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, o := range d.St.Overlays {
		if o.Kind == "alert" {
			add("alert: " + o.Title)
		}
	}
	if d.root != nil {
		d.root.Walk(func(e *Element, _ int) bool {
			if l := strings.ToLower(e.Label); !e.Hidden && (strings.Contains(l, "failed") || strings.Contains(l, "invalidated")) {
				add(e.Label)
			}
			return true
		})
	}
	return out
}
