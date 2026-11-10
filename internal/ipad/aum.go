package ipad

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Expectation is what AUM should show after a MIDI action. Every set field
// must hold at once.
type Expectation struct {
	// TempoBPM expects this tempo (± TempoTolerance, default 0.05).
	TempoBPM *float64 `json:"tempo_bpm,omitempty"`
	// NotTempoBPM expects the tempo to differ from this (pass the value read
	// before sending, to prove a tempo CC moved it).
	NotTempoBPM    *float64 `json:"not_tempo_bpm,omitempty"`
	TempoTolerance float64  `json:"tempo_tolerance,omitempty"`
	// Session expects this session name, as AUM's main menu shows it
	// (case-insensitive; a trailing .aumproj is ignored). The menu must be
	// open: AUM shows the name nowhere else.
	Session string `json:"session,omitempty"`
	// Transport expects "playing" (the position advances between two reads)
	// or "stopped" (it does not).
	Transport string `json:"transport,omitempty"`
	// Channels expects per-strip state: fader level, mute, solo, arm, node
	// bypass.
	Channels []ChannelExpectation `json:"channels,omitempty"`
	// StripCount expects this many mixer strips (channels added, imported
	// or removed).
	StripCount *int `json:"strip_count,omitempty"`
	// Overlay expects an open popover, side panel or alert whose title
	// contains this text (case-insensitive), e.g. "SETTINGS".
	Overlay string `json:"overlay,omitempty"`
	// NoOverlay expects no popover, side panel or alert.
	NoOverlay bool `json:"no_overlay,omitempty"`
	// PluginWindow expects an open plugin window whose title contains this
	// text (a "Show & Front Plugin" MIDI mapping opens one).
	PluginWindow string `json:"plugin_window,omitempty"`
}

// ChannelExpectation is a per-strip fader expectation. A level at or below
// -100 dB stands for -∞.
type ChannelExpectation struct {
	// Channel selects the strip (see AUMState.Channel).
	Channel string `json:"channel"`
	// LevelDB expects this fader level (± ToleranceDB, default 0.5).
	LevelDB *float64 `json:"level_db,omitempty"`
	// NotLevelDB expects the level to differ from this by more than
	// ToleranceDB (pass the value read before sending).
	NotLevelDB  *float64 `json:"not_level_db,omitempty"`
	ToleranceDB float64  `json:"tolerance_db,omitempty"`
	// Muted, Soloed and Armed expect the MUTE, SOLO and record-arm state.
	Muted  *bool `json:"muted,omitempty"`
	Soloed *bool `json:"soloed,omitempty"`
	Armed  *bool `json:"armed,omitempty"`
	// Nodes expects nodes of the strip to be bypassed or not.
	Nodes []NodeExpectation `json:"nodes,omitempty"`
}

// NodeExpectation is a per-node bypass expectation.
type NodeExpectation struct {
	// Node selects the node (see AUMChannel.Node).
	Node     string `json:"node"`
	Bypassed bool   `json:"bypassed"`
}

func (c ChannelExpectation) empty() bool {
	return c.LevelDB == nil && c.NotLevelDB == nil && c.Muted == nil && c.Soloed == nil && c.Armed == nil && len(c.Nodes) == 0
}

// Validate rejects an expectation that can never be checked.
func (e Expectation) Validate() error {
	if e.TempoBPM == nil && e.NotTempoBPM == nil && e.Session == "" && e.Transport == "" && len(e.Channels) == 0 &&
		e.StripCount == nil && e.Overlay == "" && !e.NoOverlay && e.PluginWindow == "" {
		return fmt.Errorf("nothing to wait for: set tempo_bpm, not_tempo_bpm, session, transport, channels, strip_count, overlay, no_overlay or plugin_window")
	}
	if e.Overlay != "" && e.NoOverlay {
		return fmt.Errorf("overlay and no_overlay contradict each other")
	}
	switch e.Transport {
	case "", "playing", "stopped":
	default:
		return fmt.Errorf("transport must be playing or stopped, got %q", e.Transport)
	}
	for i, c := range e.Channels {
		if strings.TrimSpace(c.Channel) == "" {
			return fmt.Errorf("channels[%d]: channel is required", i)
		}
		if c.empty() {
			return fmt.Errorf("channels[%d]: set level_db, not_level_db, muted, soloed, armed or nodes", i)
		}
		for j, n := range c.Nodes {
			if strings.TrimSpace(n.Node) == "" {
				return fmt.Errorf("channels[%d].nodes[%d]: node is required", i, j)
			}
		}
	}
	return nil
}

const silentDB = -100

// Mismatches lists every way cur misses e; empty means it matches. prev is
// the previous read (nil on the first), needed for the transport check.
func (e Expectation) Mismatches(prev *AUMState, cur AUMState) []string {
	var out []string
	tol := e.TempoTolerance
	if tol <= 0 {
		tol = 0.05
	}
	if e.TempoBPM != nil || e.NotTempoBPM != nil {
		switch {
		case cur.TempoBPM == nil:
			out = append(out, "tempo: not visible")
		case e.TempoBPM != nil && math.Abs(*cur.TempoBPM-*e.TempoBPM) > tol:
			out = append(out, fmt.Sprintf("tempo: want %g BPM, got %g", *e.TempoBPM, *cur.TempoBPM))
		case e.NotTempoBPM != nil && math.Abs(*cur.TempoBPM-*e.NotTempoBPM) <= tol:
			out = append(out, fmt.Sprintf("tempo: still %g BPM", *cur.TempoBPM))
		}
	}
	if e.Session != "" {
		want := strings.TrimSuffix(strings.TrimSpace(e.Session), ".aumproj")
		switch {
		case cur.Session == "":
			out = append(out, "session: name not visible (AUM shows it only in the main menu: ipad_tap target menu)")
		case !strings.EqualFold(want, cur.Session):
			out = append(out, fmt.Sprintf("session: want %q, got %q", want, cur.Session))
		}
	}
	switch e.Transport {
	case "playing", "stopped":
		switch {
		case cur.Position == "":
			out = append(out, "transport: position not visible")
		case prev == nil:
			out = append(out, "transport: needs a second read")
		case e.Transport == "playing" && cur.Position == prev.Position:
			out = append(out, fmt.Sprintf("transport: position stays at %q", cur.Position))
		case e.Transport == "stopped" && cur.Position != prev.Position:
			out = append(out, fmt.Sprintf("transport: position moves (%q -> %q)", prev.Position, cur.Position))
		}
	}
	for _, ce := range e.Channels {
		ch, err := cur.Channel(ce.Channel)
		if err != nil {
			out = append(out, "channel "+ce.Channel+": "+err.Error())
			continue
		}
		out = append(out, ce.mismatches(ch)...)
	}
	if e.StripCount != nil && len(cur.Channels) != *e.StripCount {
		out = append(out, fmt.Sprintf("strips: want %d, got %d", *e.StripCount, len(cur.Channels)))
	}
	if e.Overlay != "" {
		found := false
		for _, o := range cur.Overlays {
			found = found || strings.Contains(strings.ToLower(o.Title), strings.ToLower(e.Overlay))
		}
		if !found {
			out = append(out, fmt.Sprintf("overlay: nothing titled %q open (%s)", e.Overlay, cur.overlaySummary()))
		}
	}
	if e.NoOverlay && len(cur.Overlays) > 0 {
		out = append(out, "overlay: "+cur.overlaySummary())
	}
	if e.PluginWindow != "" {
		found := false
		for _, w := range cur.PluginWindows {
			found = found || strings.Contains(strings.ToLower(w.Title), strings.ToLower(e.PluginWindow))
		}
		if !found {
			out = append(out, fmt.Sprintf("plugin window: none titled %q open", e.PluginWindow))
		}
	}
	return out
}

func (ce ChannelExpectation) mismatches(ch AUMChannel) []string {
	var out []string
	if ce.LevelDB != nil || ce.NotLevelDB != nil {
		ctol := ce.ToleranceDB
		if ctol <= 0 {
			ctol = 0.5
		}
		if ch.LevelLabel == "" {
			out = append(out, fmt.Sprintf("channel %q: no fader level visible", ch.Title))
		} else {
			if ce.LevelDB != nil && !levelNear(ch.LevelDB, *ce.LevelDB, ctol) {
				out = append(out, fmt.Sprintf("channel %q: want %s, got %s", ch.Title, dbText(*ce.LevelDB), ch.LevelLabel))
			}
			if ce.NotLevelDB != nil && levelNear(ch.LevelDB, *ce.NotLevelDB, ctol) {
				out = append(out, fmt.Sprintf("channel %q: level still %s", ch.Title, ch.LevelLabel))
			}
		}
	}
	for _, b := range []struct {
		name      string
		want, got *bool
	}{{"mute", ce.Muted, ch.Muted}, {"solo", ce.Soloed, ch.Soloed}, {"arm", ce.Armed, ch.Armed}} {
		switch {
		case b.want == nil:
		case b.got == nil:
			out = append(out, fmt.Sprintf("channel %q: %s state not visible", ch.Title, b.name))
		case *b.got != *b.want:
			out = append(out, fmt.Sprintf("channel %q: %s %s, want %s", ch.Title, b.name, onOff(*b.got), onOff(*b.want)))
		}
	}
	for _, ne := range ce.Nodes {
		n, err := ch.Node(ne.Node)
		switch {
		case err != nil:
			out = append(out, "channel "+strconv.Quote(ch.Title)+": "+err.Error())
		case n.Bypassed != ne.Bypassed:
			out = append(out, fmt.Sprintf("channel %q: %s bypassed=%v, want %v", ch.Title, nodeName(n), n.Bypassed, ne.Bypassed))
		}
	}
	return out
}

// levelNear compares a read level (nil = -∞) with a wanted one (≤ -100 = -∞).
func levelNear(got *float64, want, tol float64) bool {
	if got == nil || want <= silentDB {
		return got == nil && want <= silentDB
	}
	return math.Abs(*got-want) <= tol
}

func dbText(v float64) string {
	if v <= silentDB {
		return "-∞ dB"
	}
	return fmt.Sprintf("%.1f dB", v)
}

// ReadAUM opens a session bound to AUM (bringing it to the front, launching it
// when it is not running) and reads its state.
func (c *Client) ReadAUM(ctx context.Context) (AUMState, error) {
	var st AUMState
	err := c.WithApp(ctx, AUMBundleID, func(s *Session) error {
		var err error
		st, err = readAUM(ctx, s)
		return err
	})
	return st, err
}

func readAUM(ctx context.Context, s *Session) (AUMState, error) {
	win, err := s.WindowSize(ctx)
	if err != nil {
		return AUMState{}, err
	}
	root, err := s.Source(ctx)
	if err != nil {
		return AUMState{}, err
	}
	return ParseAUM(root, win), nil
}

// WaitResult is the outcome of WaitAUM.
type WaitResult struct {
	Matched bool `json:"matched"`
	// Reads is how many times AUM's tree was read.
	Reads     int   `json:"reads"`
	ElapsedMS int64 `json:"elapsed_ms"`
	// Mismatches are what the last read still missed (empty when matched).
	Mismatches []string `json:"mismatches,omitempty"`
	// State is the last read.
	State AUMState `json:"state"`
}

// WaitAUM reads AUM's state until it meets e or timeout passes, at most every
// interval (one read of AUM's tree takes 0.5–2 s, growing with the session). The transport check
// compares consecutive reads, so it needs at least two.
func (c *Client) WaitAUM(ctx context.Context, e Expectation, timeout, interval time.Duration) (WaitResult, error) {
	if err := e.Validate(); err != nil {
		return WaitResult{}, err
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	var res WaitResult
	start := time.Now()
	err := c.WithApp(ctx, AUMBundleID, func(s *Session) error {
		var prev *AUMState
		for {
			readAt := time.Now()
			st, err := readAUM(ctx, s)
			if err != nil {
				return err
			}
			res.Reads++
			res.State = st
			res.Mismatches = e.Mismatches(prev, st)
			if len(res.Mismatches) == 0 {
				res.Matched = true
				return nil
			}
			if time.Since(start) >= timeout {
				return nil
			}
			prev = &st
			if wait := interval - time.Since(readAt); wait > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}
		}
	})
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, err
}

// TopBarTarget is a named AUM top-bar control, placed from the window size
// (the right-hand buttons keep their offset from the right edge in both
// orientations). Positions come from auv3-probe's docs/aum-ui-map.md.
type TopBarTarget struct {
	Name, Description string
	x, y              func(Size) float64
}

func fixed(v float64) func(Size) float64 { return func(Size) float64 { return v } }
func fromRight(d float64) func(Size) float64 {
	return func(z Size) float64 { return z.Width - d }
}
func fromBottom(d float64) func(Size) float64 {
	return func(z Size) float64 { return z.Height - d }
}

// AUMTargets are the named AUM controls ipad_tap accepts. Record is left out
// on purpose: it starts recording every armed channel and writes files.
var AUMTargets = []TopBarTarget{
	{"tempo", "the tempo bar (TAP, ±0.1 BPM, metronome, Clock Options)", fixed(30), fixed(30)},
	{"rewind", "transport rewind", fixed(111), fixed(33)},
	{"play", "transport play/stop (starts every synced plugin)", fixed(156), fixed(33)},
	{"engine_info", "Engine Info (the DSP % button)", fromRight(180), fixed(30)},
	{"navigator", "the Navigator side panel", fromRight(125), fixed(33)},
	{"midi_routing", "the MIDI routing matrix (tap again to close)", fromRight(75), fixed(33)},
	{"menu", "the main menu side panel (its close icon sits on the same spot)", fromRight(25), fixed(33)},
	{"keyboard", "the built-in keyboard", fixed(52), fromBottom(17)},
}

// AUMTarget resolves a named AUM control to a point for window z.
func AUMTarget(name string, z Size) (float64, float64, error) {
	for _, t := range AUMTargets {
		if t.Name == name {
			return t.x(z), t.y(z), nil
		}
	}
	names := make([]string, len(AUMTargets))
	for i, t := range AUMTargets {
		names[i] = t.Name
	}
	return 0, 0, fmt.Errorf("unknown AUM target %q (known: %s)", name, strings.Join(names, ", "))
}
