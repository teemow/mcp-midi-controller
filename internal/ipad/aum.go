package ipad

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AUMState is what AUM's accessibility tree tells about the running session.
// It is read from AUM's own UI, independent of the MIDI path, so it is the
// oracle for "did the MIDI I sent actually change AUM?".
//
// Not everything is in the tree: mute, solo and record-arm buttons carry a
// label but no on/off state, and plugin parameters live in out-of-process
// plugin views. Check those with a screenshot (mute/solo) or the audio tap.
type AUMState struct {
	// Window is AUM's window in points; it rotates with the iPad.
	Window      Size   `json:"window"`
	Orientation string `json:"orientation"`
	// StartScreen is true while AUM shows LAST STATE / DEFAULT (no session).
	StartScreen bool `json:"start_screen,omitempty"`
	// MenuOpen is true while the main-menu side panel is open.
	MenuOpen bool `json:"menu_open,omitempty"`
	// Session is the session name the main menu shows ("SESSION: …"); it is
	// only on screen while the menu is open. (The main meter's accessibility
	// label also starts with "SESSION: …", but AUM does not refresh it: it
	// can name a session and channels that are long gone, so it is ignored.)
	Session string `json:"session,omitempty"`
	// TempoBPM is the tempo shown top left (♩120).
	TempoBPM *float64 `json:"tempo_bpm,omitempty"`
	// Position is the transport position text ("001:1  00:00.0"); it
	// advances while the transport runs.
	Position string `json:"position,omitempty"`
	// DSP is the DSP load text ("3 %").
	DSP string `json:"dsp,omitempty"`
	// Channels are the mixer strips in mixer order (on screen or scrolled
	// away).
	Channels []AUMChannel `json:"channels"`
}

// AUMChannel is one mixer strip.
type AUMChannel struct {
	// Strip is the 1-based position in the mixer (left to right).
	Strip int `json:"strip"`
	// Number is the number AUM prints in the title ("2: …", "MIDI 1").
	Number int `json:"number"`
	// Kind is "audio" or "midi".
	Kind string `json:"kind"`
	// Title is the full title label; Name is the part after "n: ".
	Title string `json:"title"`
	Name  string `json:"name,omitempty"`
	// LevelDB is the fader level from the dB label above the fader; nil for
	// -∞ (LevelLabel then reads "-∞ dB") and for MIDI strips.
	LevelDB    *float64 `json:"level_db,omitempty"`
	LevelLabel string   `json:"level_label,omitempty"`
	// TitleFrame is where the title button sits (tap it for Edit Channel).
	TitleFrame Rect `json:"title_frame"`
	// OnScreen is false for strips scrolled out of the window: swipe the mixer
	// before tapping them.
	OnScreen bool `json:"on_screen"`
}

var (
	reAudioTitle = regexp.MustCompile(`^(\d+): (.*)$`)
	reMIDITitle  = regexp.MustCompile(`^MIDI (\d+)$`)
	reLevel      = regexp.MustCompile(`^([+-]?\d+(?:\.\d+)?|-∞|-inf) dB$`)
	reTempo      = regexp.MustCompile(`♩\s*(\d+(?:\.\d+)?)`)
	rePosition   = regexp.MustCompile(`^\d+:\d+\s+\d+:\d+(?:\.\d+)?$`)
)

const sessionPrefix = "SESSION: "

// ParseAUM reads AUMState out of AUM's accessibility tree. window is AUM's
// window size (for OnScreen and Orientation).
func ParseAUM(root *Element, window Size) AUMState {
	st := AUMState{Window: window, Orientation: window.Orientation(), Channels: []AUMChannel{}}

	type strip struct {
		container *Element
		title     *Element
	}
	var strips []strip
	seen := map[string]bool{}
	var walk func(el *Element)
	walk = func(el *Element) {
		for _, c := range el.Children {
			// A strip's title button sits directly in the full-height strip
			// container. The first occurrence wins, so a channel listed again
			// elsewhere (e.g. the Navigator) is not counted twice.
			if isChannelTitle(c) && el.Rect.Height >= 300 && !seen[c.Label] {
				seen[c.Label] = true
				strips = append(strips, strip{container: el, title: c})
			}
		}
		switch el.Type {
		case "StaticText":
			switch {
			case el.Label == "MENU":
				st.MenuOpen = true
			case strings.HasPrefix(el.Label, sessionPrefix):
				st.Session = strings.TrimSpace(strings.TrimPrefix(el.Label, sessionPrefix))
			case strings.HasPrefix(el.Label, "DSP:"):
				st.DSP = strings.Join(strings.Fields(strings.TrimPrefix(el.Label, "DSP:")), " ")
			case st.TempoBPM == nil && reTempo.MatchString(el.Label):
				if v, err := strconv.ParseFloat(reTempo.FindStringSubmatch(el.Label)[1], 64); err == nil {
					st.TempoBPM = &v
				}
			case st.Position == "" && rePosition.MatchString(el.Label):
				st.Position = el.Label
			}
		case "Button":
			if strings.EqualFold(el.Label, "LAST STATE") {
				st.StartScreen = true
			}
		}
		for _, c := range el.Children {
			walk(c)
		}
	}
	walk(root)

	sort.SliceStable(strips, func(i, j int) bool { return strips[i].container.Rect.X < strips[j].container.Rect.X })
	for i, s := range strips {
		ch := AUMChannel{Strip: i + 1, Title: s.title.Label, TitleFrame: s.title.Rect}
		if m := reMIDITitle.FindStringSubmatch(s.title.Label); m != nil {
			ch.Kind = "midi"
			ch.Number, _ = strconv.Atoi(m[1])
		} else if m := reAudioTitle.FindStringSubmatch(s.title.Label); m != nil {
			ch.Kind = "audio"
			ch.Number, _ = strconv.Atoi(m[1])
			ch.Name = m[2]
		}
		for _, lv := range s.container.Find(func(e *Element) bool { return e.Type == "Button" && reLevel.MatchString(e.Label) }) {
			ch.LevelLabel = lv.Label
			if v, err := strconv.ParseFloat(reLevel.FindStringSubmatch(lv.Label)[1], 64); err == nil {
				ch.LevelDB = &v
			}
			break
		}
		r := s.container.Rect
		ch.OnScreen = window.Width <= 0 || (r.X >= -1 && r.X+r.Width <= window.Width+1)
		st.Channels = append(st.Channels, ch)
	}
	return st
}

// isChannelTitle matches a strip's title button: "2: <name>" or "MIDI 1",
// wider than tall (156×50). A plugin node button can carry the same text but is
// square, which keeps it out.
func isChannelTitle(e *Element) bool {
	if e.Type != "Button" || e.Rect.Width <= e.Rect.Height*1.5 {
		return false
	}
	return reMIDITitle.MatchString(e.Label) || reAudioTitle.MatchString(e.Label)
}

// Channel finds a strip by selector: a number ("2" = the audio strip titled
// "2: …"), a MIDI title ("MIDI 1"), or a case-insensitive part of the title.
func (st AUMState) Channel(sel string) (AUMChannel, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return AUMChannel{}, fmt.Errorf("empty channel selector")
	}
	if n, err := strconv.Atoi(sel); err == nil {
		for _, ch := range st.Channels {
			if ch.Kind == "audio" && ch.Number == n {
				return ch, nil
			}
		}
		return AUMChannel{}, fmt.Errorf("no audio channel %d (channels: %s)", n, st.channelList())
	}
	var hits []AUMChannel
	for _, ch := range st.Channels {
		if strings.EqualFold(ch.Title, sel) {
			return ch, nil
		}
		if strings.Contains(strings.ToLower(ch.Title), strings.ToLower(sel)) {
			hits = append(hits, ch)
		}
	}
	switch len(hits) {
	case 0:
		return AUMChannel{}, fmt.Errorf("no channel matches %q (channels: %s)", sel, st.channelList())
	case 1:
		return hits[0], nil
	}
	return AUMChannel{}, fmt.Errorf("%q matches %d channels; use the number (channels: %s)", sel, len(hits), st.channelList())
}

func (st AUMState) channelList() string {
	if len(st.Channels) == 0 {
		return "none"
	}
	names := make([]string, len(st.Channels))
	for i, ch := range st.Channels {
		names[i] = strconv.Quote(ch.Title)
	}
	return strings.Join(names, ", ")
}

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
	// Channels expects per-strip fader levels.
	Channels []ChannelExpectation `json:"channels,omitempty"`
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
}

// Validate rejects an expectation that can never be checked.
func (e Expectation) Validate() error {
	if e.TempoBPM == nil && e.NotTempoBPM == nil && e.Session == "" && e.Transport == "" && len(e.Channels) == 0 {
		return fmt.Errorf("nothing to wait for: set tempo_bpm, not_tempo_bpm, session, transport or channels")
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
		if c.LevelDB == nil && c.NotLevelDB == nil {
			return fmt.Errorf("channels[%d]: set level_db or not_level_db", i)
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
		if ch.LevelLabel == "" {
			out = append(out, fmt.Sprintf("channel %q: no fader level visible", ch.Title))
			continue
		}
		ctol := ce.ToleranceDB
		if ctol <= 0 {
			ctol = 0.5
		}
		if ce.LevelDB != nil && !levelNear(ch.LevelDB, *ce.LevelDB, ctol) {
			out = append(out, fmt.Sprintf("channel %q: want %s, got %s", ch.Title, dbText(*ce.LevelDB), ch.LevelLabel))
		}
		if ce.NotLevelDB != nil && levelNear(ch.LevelDB, *ce.NotLevelDB, ctol) {
			out = append(out, fmt.Sprintf("channel %q: level still %s", ch.Title, ch.LevelLabel))
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
