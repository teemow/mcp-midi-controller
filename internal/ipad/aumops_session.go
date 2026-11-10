package ipad

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AUMLoaded is what AUM shows once a session has been opened.
type AUMLoaded struct {
	// Session is the name the main menu shows.
	Session string `json:"session"`
	// Strips are the mixer's strip titles, left to right.
	Strips []string `json:"strips"`
	// Problems are texts on screen that report a fault: an alert's title, a
	// label that says a plugin failed or was invalidated.
	Problems []string `json:"problems,omitempty"`
	// LoadMS is how long AUM showed that it was loading.
	LoadMS int64 `json:"load_ms,omitempty"`
}

// loadingLabel is the overlay AUM shows while it restores a session's
// plugins.
const loadingLabel = "LOADING STATE"

// aumLoadTimeout bounds the wait for AUM to finish loading a session. A
// session of a dozen plugins takes 5 to 20 seconds.
var aumLoadTimeout = 120 * time.Second

// AUMOpenSession loads a session from FILES and waits until AUM has finished
// loading it. Unlike the bare load action it clears the screen first (FILES
// does not open over a plugin window or another panel) and it does not return
// while AUM still restores the plugins' state, so what follows can rely on the
// session being there. The result's Data is an AUMLoaded.
//
// Loading replaces the open session; the caller is responsible for having
// asked.
func (c *Client) AUMOpenSession(ctx context.Context, path []string, name string) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		if err := d.clearScreen(); err != nil {
			return err
		}
		start := time.Now()
		loaded, err := d.loadSession(path, name)
		if err != nil {
			return err
		}
		// loadSession leaves the main menu open with the session's name.
		out := AUMLoaded{Session: d.St.Session}
		done, err := d.until(aumLoadTimeout, func(AUMState) bool { return !d.loading() })
		if err != nil {
			return err
		}
		out.LoadMS = time.Since(start).Milliseconds()
		if err := d.clearScreen(); err != nil {
			return err
		}
		out.Strips, out.Problems = d.stripTitles(), d.problems()
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

// AUMSession reads which session AUM has open without changing it: it opens
// the main menu for the name, closes it again and lists the strips and
// whatever on screen reports a fault. The result's Data is an AUMLoaded.
func (c *Client) AUMSession(ctx context.Context) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		// Faults are read first: an alert would be dismissed on the way to
		// the menu.
		problems := d.problems()
		if err := d.clearScreen(); err != nil {
			return err
		}
		if ok, err := d.ensureMenu(); err != nil || !ok {
			if err != nil {
				return err
			}
			return fmt.Errorf("AUM's main menu did not open (%s)", d.St.overlaySummary())
		}
		out := AUMLoaded{Session: d.St.Session, Problems: problems}
		if err := d.clearScreen(); err != nil {
			return err
		}
		out.Strips = d.stripTitles()
		res = d.result("session", out.Session != "", fmt.Sprintf("AUM has %q open: %d strip(s)", out.Session, len(out.Strips)), out)
		return nil
	})
	return res, err
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

// stripTitles lists the mixer's strip titles.
func (d *AUMDriver) stripTitles() []string {
	out := make([]string, len(d.St.Channels))
	for i, ch := range d.St.Channels {
		out[i] = ch.Title
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
