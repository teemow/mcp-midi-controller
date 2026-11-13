package ipad

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AUMState is what AUM's accessibility tree tells about the running session.
// It is read from AUM's own UI, independent of the MIDI path, so it is the
// oracle for "did the MIDI I sent actually change AUM?".
//
// Plugin parameters are not in it: they live in out-of-process plugin views.
// Check those with the audio tap.
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
	// advances while the transport runs. A pre-roll counts in from "-01:1".
	Position string `json:"position,omitempty"`
	// DSP is the DSP load text ("3 %").
	DSP string `json:"dsp,omitempty"`
	// MeterSource is what the main meter shows, as printed above it
	// ("SPKR OUT", "Ch 2 IN").
	MeterSource string `json:"meter_source,omitempty"`
	// TempoBar is true while the tempo bar (close, TAP, ‹ ♩ ›, metronome,
	// Clock Options) replaces the top left.
	TempoBar bool `json:"tempo_bar,omitempty"`
	// Keyboard is true while AUM's built-in keyboard covers the bottom of
	// the mixer (its toolbar starts with "HIDE↓").
	Keyboard bool `json:"keyboard,omitempty"`
	// Overlays are the popovers, side panels, alerts and the MIDI routing
	// matrix over the mixer, front-most last.
	Overlays []AUMOverlay `json:"overlays,omitempty"`
	// Channels are the mixer strips in the tree, in mixer order. AUM keeps
	// only the strips that reach into the window in it: of a mixer wider
	// than the window the others are missing until it is scrolled to them
	// (see mixerWalk).
	Channels []AUMChannel `json:"channels"`
	// AddTile is the add-channel tile right of the last strip. Like a strip
	// it is in the tree only while its column reaches into the window.
	AddTile *Rect `json:"add_tile,omitempty"`
	// MixerScroll is how far the mixer is scrolled sideways, in percent of
	// its scroll range (0 at its first strip), as its horizontal scroll bar
	// says; nil when that is not in the tree.
	MixerScroll *float64 `json:"mixer_scroll,omitempty"`
	// PluginWindows are the open plugin windows (title "<name> @<node>").
	PluginWindows []AUMPluginWindow `json:"plugin_windows,omitempty"`

	positionFrame Rect
	tempoFrame    Rect
	addMenu       map[string]Rect
	mixer         Rect
}

// AUMChannel is one mixer strip.
type AUMChannel struct {
	// Strip is the 1-based position in the mixer (left to right).
	Strip int `json:"strip"`
	// Number is the number AUM prints in the title ("2: …", "MIDI 1",
	// "CHAN 3"); 0 for a custom title, which replaces the number.
	Number int `json:"number,omitempty"`
	// Kind is "audio" (the strip has a fader) or "midi".
	Kind string `json:"kind"`
	// Title is the full title label; Name is the part after "n: ".
	Title string `json:"title"`
	Name  string `json:"name,omitempty"`
	// LevelDB is the fader level from the dB label above the fader; nil for
	// -∞ (LevelLabel then reads "-∞ dB") and for MIDI strips.
	LevelDB    *float64 `json:"level_db,omitempty"`
	LevelLabel string   `json:"level_label,omitempty"`
	// Muted, Soloed and Armed are the MUTE, SOLO and record-arm states; nil
	// when the button is not in the tree (MIDI strips; a muted strip's MUTE
	// button covers SOLO).
	Muted  *bool `json:"muted,omitempty"`
	Soloed *bool `json:"soloed,omitempty"`
	Armed  *bool `json:"armed,omitempty"`
	// Nodes are the strip's nodes top to bottom: source, inserts (empty
	// slots included), output; MIDI strips hold MIDI nodes.
	Nodes []AUMNode `json:"nodes,omitempty"`
	// Frame is the whole strip; TitleFrame the title button (tap it for
	// Edit Channel).
	Frame      Rect `json:"frame"`
	TitleFrame Rect `json:"title_frame"`
	// LevelFrame is the dB label (tap it for Set level), FaderFrame the
	// fader track, MuteFrame/SoloFrame/RecFrame the M, S and R buttons.
	LevelFrame *Rect `json:"level_frame,omitempty"`
	FaderFrame *Rect `json:"fader_frame,omitempty"`
	MuteFrame  *Rect `json:"mute_frame,omitempty"`
	SoloFrame  *Rect `json:"solo_frame,omitempty"`
	RecFrame   *Rect `json:"rec_frame,omitempty"`
	// AddSlot is the orange "+1" below the last insert when it is visible.
	AddSlot *Rect `json:"add_slot,omitempty"`
	// OnScreen is false for strips scrolled out of the window: scroll the
	// mixer before tapping them.
	OnScreen bool `json:"on_screen"`

	inserts Rect // the insert column's visible area
}

// AUMNode is one node or empty slot of a strip.
type AUMNode struct {
	// Index is the 1-based position in the strip, top to bottom.
	Index int `json:"index"`
	// Role is "source", "insert", "output" or "midi".
	Role string `json:"role"`
	// Slot is the 1-based insert slot (role insert only).
	Slot int `json:"slot,omitempty"`
	// Label is the node's short name as AUM labels it (PAR, LP, SPKR, a
	// plugin's short name); many plugin nodes carry none.
	Label string `json:"label,omitempty"`
	// Empty is true for a slot without a node (it shows "+").
	Empty bool `json:"empty,omitempty"`
	// Bypassed is true for a bypassed node: AUM slides it left off the
	// strip's wire and shows an eject ⏏ where it was.
	Bypassed bool `json:"bypassed,omitempty"`
	Frame    Rect `json:"frame"`
	// OnScreen is false for an insert scrolled out of the insert column.
	OnScreen bool `json:"on_screen"`
}

// AUMPluginWindow is an open plugin window.
type AUMPluginWindow struct {
	// Title is the title bar's label: the plugin name and its node ("@A1").
	Title string `json:"title"`
	// TitleFrame is the title button; Bar lists the title bar's buttons left
	// to right (✕ is the last, the window mode the one before it).
	TitleFrame Rect   `json:"title_frame"`
	Bar        []Rect `json:"bar"`
}

// AUMOverlay is a popover, side panel, alert or the MIDI routing matrix.
type AUMOverlay struct {
	// Kind is "popover", "panel", "alert" or "routing".
	Kind string `json:"kind"`
	// Title is the popover's or panel's title ("FILE PLAYER", "EDIT
	// CHANNEL 2", "SETTINGS", "MENU", "MIDI ROUTING") or the alert's.
	Title string `json:"title,omitempty"`
	Frame Rect   `json:"frame"`
	// Rows pair each text with the value shown right of it on the same line
	// (Settings, File Player options, mapping editors).
	Rows []AUMRow `json:"rows,omitempty"`
	// Buttons are the labelled buttons, in tree order.
	Buttons []string `json:"buttons,omitempty"`
	// Fields are the values of its text fields.
	Fields []string `json:"fields,omitempty"`

	el *Element
}

// AUMRow is one text of an overlay and the value right of it.
type AUMRow struct {
	Label      string `json:"label"`
	Value      string `json:"value,omitempty"`
	Frame      Rect   `json:"frame"`
	ValueFrame *Rect  `json:"value_frame,omitempty"`
	// Prev and Next are a stepper's ‹ and › around the value (Clock
	// Options' Pre-roll, Time Signature, Sync Quantum): tapping the value
	// itself does nothing there.
	Prev *Rect `json:"prev,omitempty"`
	Next *Rect `json:"next,omitempty"`
}

var (
	reAudioTitle = regexp.MustCompile(`^(\d+): (.*)$`)
	reMIDITitle  = regexp.MustCompile(`^MIDI (\d+)$`)
	reChanTitle  = regexp.MustCompile(`^CHAN (\d+)$`)
	reLevel      = regexp.MustCompile(`^([+-]?\d+(?:\.\d+)?|-∞|-inf) dB$`)
	reTempo      = regexp.MustCompile(`♩\s*(\d+(?:\.\d+)?)`)
	rePosition   = regexp.MustCompile(`^-?\d+:\d+\s+-?\d+:\d+(?:\.\d+)?$`)
	// rePluginWin matches a plugin window's title: "<plugin> @<node>", and
	// " - <strip title>" after it when the strip has a custom title ("UA
	// Battalion @A2 - Battalion"). A MIDI node reads "@M1:1".
	rePluginWin = regexp.MustCompile(`^.+ @[A-Z]+\d+(?::\d+)?(?: - .+)?$`)
	// reGlued matches the texts iOS glues into the accessibility label of
	// an unlabelled AUM control: meter readings, the DSP load, the main
	// meter's stale "SESSION: …" label and the about line.
	reGlued = regexp.MustCompile(`^(-∞|DSP:.*|SESSION:.*|Copyright.*|[-+]?\d+(\.\d+)?\s*(dB|%)?)$`)
)

// cleanLabel drops what iOS glued into an unlabelled control's label from
// the texts around it: "-∞, DSP:  2 %, -∞, Name, Name" is "Name", and a
// label made only of such texts is "". Other labels are returned as they are.
func cleanLabel(s string) string {
	if !strings.Contains(s, ", ") {
		return s
	}
	var keep []string
	dropped := false
	for _, p := range strings.Split(s, ", ") {
		if reGlued.MatchString(strings.TrimSpace(p)) {
			dropped = true
			continue
		}
		if len(keep) > 0 && keep[len(keep)-1] == p {
			continue
		}
		keep = append(keep, p)
	}
	if !dropped {
		return s
	}
	return strings.Join(keep, ", ")
}

const sessionPrefix = "SESSION: "

// ParseAUM reads AUMState out of AUM's accessibility tree. window is AUM's
// window size (for OnScreen and Orientation).
func ParseAUM(root *Element, window Size) AUMState {
	st := AUMState{Window: window, Orientation: window.Orientation(), Channels: []AUMChannel{}, addMenu: map[string]Rect{}}

	var strips []*Element
	seen := map[Rect]bool{}
	root.Walk(func(el *Element, _ int) bool {
		if t := stripTitle(el); t != nil && !seen[t.Rect] {
			seen[t.Rect] = true
			strips = append(strips, el)
		}
		switch el.Type {
		case "StaticText":
			switch {
			case strings.HasPrefix(el.Label, "DSP:"):
				st.DSP = strings.Join(strings.Fields(strings.TrimPrefix(el.Label, "DSP:")), " ")
			case st.TempoBPM == nil && el.Rect.Y < 60 && reTempo.MatchString(el.Label):
				if v, err := strconv.ParseFloat(reTempo.FindStringSubmatch(el.Label)[1], 64); err == nil {
					st.TempoBPM = &v
					st.tempoFrame = el.Rect
				}
			case st.Position == "" && el.Rect.Y < 30 && rePosition.MatchString(el.Label):
				st.Position = el.Label
				st.positionFrame = el.Rect
			case st.MeterSource == "" && el.Rect.Y < 10 && el.Rect.Height <= 14 && el.Rect.Width >= 150:
				st.MeterSource = el.Label
			}
		case "Button":
			switch {
			case strings.EqualFold(el.Label, "LAST STATE"):
				st.StartScreen = true
			case el.Label == "TAP" && el.Rect.Y < 60:
				st.TempoBar = true
			case st.TempoBPM == nil && el.Rect.Y < 60 && reTempo.MatchString(el.Label):
				// The tempo bar shows the tempo as a button between ‹ and ›.
				if v, err := strconv.ParseFloat(reTempo.FindStringSubmatch(el.Label)[1], 64); err == nil {
					st.TempoBPM = &v
					st.tempoFrame = el.Rect
				}
			case strings.HasPrefix(el.Label, "HIDE") && !el.Hidden:
				st.Keyboard = true
			}
		case "ScrollView":
			if st.mixer.Empty() && el.Rect.Width >= window.Width*0.9 && el.Rect.Height >= window.Height*0.8 {
				st.mixer = el.Rect
				st.findAddTile(el)
				st.readMixerScroll(el)
			}
		}
		return true
	})

	sort.SliceStable(strips, func(i, j int) bool { return strips[i].Rect.X < strips[j].Rect.X })
	for i, s := range strips {
		st.Channels = append(st.Channels, parseStrip(i+1, s, window))
	}
	st.Overlays = parseOverlays(root, window)
	st.PluginWindows = parsePluginWindows(root)
	st.readMenu()
	return st
}

// readMenu sets MenuOpen and Session from the main menu when it is the open
// side panel. Texts alone do not tell: the menu's page stays in the tree for
// a moment after it was closed or another page was pushed over it, and its
// "MENU" header and "SESSION: …" line with it.
func (st *AUMState) readMenu() {
	menu := st.Overlay("panel")
	if menu == nil || menu.Title != "MENU" {
		return
	}
	st.MenuOpen = true
	if t := menu.el.First(func(e *Element) bool {
		return e.Type == "StaticText" && strings.HasPrefix(e.Label, sessionPrefix)
	}); t != nil {
		st.Session = strings.TrimSpace(strings.TrimPrefix(t.Label, sessionPrefix))
	}
}

// parsePluginWindows finds plugin title bars: a button labelled
// "<name> @<node>" (or "<name> @<node> - <strip>") among a row of buttons.
// A side panel opened over the window marks the bar hidden: it is not
// listed then, as its ✕ lies under the panel.
func parsePluginWindows(root *Element) []AUMPluginWindow {
	var out []AUMPluginWindow
	root.Walk(func(el *Element, _ int) bool {
		for _, c := range el.Children {
			if c.Type != "Button" || c.Hidden || !rePluginWin.MatchString(c.Label) || c.Rect.Height > 40 {
				continue
			}
			w := AUMPluginWindow{Title: c.Label, TitleFrame: c.Rect}
			for _, b := range el.Children {
				if b.Type == "Button" && !b.Hidden && math.Abs(b.Rect.MidY()-c.Rect.MidY()) <= 8 && b.Rect.Height <= 40 {
					w.Bar = append(w.Bar, b.Rect)
				}
			}
			sort.Slice(w.Bar, func(i, j int) bool { return w.Bar[i].X < w.Bar[j].X })
			out = append(out, w)
		}
		return true
	})
	return out
}

// stripTitle returns el's title button when el is a mixer strip: a
// full-height container (228×764 in AUM 1.4.8) whose lowest child is a
// wide button (156×50). The title can be "2: <source>", "MIDI 1", "CHAN 3"
// or any custom name, so it is found by shape, not text.
func stripTitle(el *Element) *Element {
	r := el.Rect
	if el.Type != "Other" || r.Height < 300 || r.Width < 150 || r.Width > 320 {
		return nil
	}
	for _, c := range el.Children {
		cr := c.Rect
		if c.Type == "Button" && c.Label != "" && cr.Width >= 120 && cr.Width > cr.Height*1.5 &&
			cr.Height >= 30 && cr.Height <= 70 && cr.Y >= r.Y+r.Height*0.75 {
			return c
		}
	}
	return nil
}

// findAddTile finds the add-channel tile: a square (150×150) in a
// full-height column right of the last strip. AUM 1.4.8 lists it as an
// unlabelled Button; its AUDIO, MIDI and IMPORT menu shows in the tree only
// while it is open.
func (st *AUMState) findAddTile(mixer *Element) {
	for _, c := range mixer.Children {
		if stripTitle(c) != nil || c.Type != "Other" || c.Rect.Height < 300 {
			continue
		}
		for _, g := range c.Children {
			if (g.Type == "Other" || g.Type == "Button") && g.Rect.Width >= 100 && math.Abs(g.Rect.Width-g.Rect.Height) <= 2 {
				r := g.Rect
				st.AddTile = &r
				c.Walk(func(b *Element, _ int) bool {
					if l := strings.ToUpper(b.Label); b.Type == "Button" && (l == "AUDIO" || l == "MIDI" || l == "IMPORT") {
						st.addMenu[l] = b.Rect
					}
					return true
				})
				return
			}
		}
	}
}

// readMixerScroll reads the mixer's horizontal scroll bar: a child of the
// mixer labelled "Horizontal scroll bar, 2 pages" whose value is how far the
// mixer is scrolled ("0 %" at its first strip, with a no-break space). It is
// in the tree hidden as well, under a panel or a plugin window.
func (st *AUMState) readMixerScroll(mixer *Element) {
	for _, c := range mixer.Children {
		if !strings.HasPrefix(strings.ToLower(c.Label), "horizontal scroll bar") {
			continue
		}
		v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c.Value), "%"))
		if f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64); err == nil {
			st.MixerScroll = &f
			return
		}
	}
}

// atMixerStart reports whether the mixer is scrolled to its first strip: its
// scroll bar says so, and no strip reaches past its left edge.
func (st AUMState) atMixerStart() bool {
	if st.MixerScroll == nil || *st.MixerScroll >= 0.5 {
		return false
	}
	return len(st.Channels) == 0 || st.Channels[0].Frame.X >= st.mixer.X-1
}

func parseStrip(n int, s *Element, window Size) AUMChannel {
	title := stripTitle(s)
	ch := AUMChannel{Strip: n, Title: title.Label, TitleFrame: title.Rect, Frame: s.Rect, Kind: "midi"}
	r := s.Rect
	ch.OnScreen = window.Width <= 0 || (r.X >= -1 && r.X+r.Width <= window.Width+1)
	switch {
	case reMIDITitle.MatchString(title.Label):
		ch.Number, _ = strconv.Atoi(reMIDITitle.FindStringSubmatch(title.Label)[1])
	case reChanTitle.MatchString(title.Label):
		ch.Number, _ = strconv.Atoi(reChanTitle.FindStringSubmatch(title.Label)[1])
	case reAudioTitle.MatchString(title.Label):
		m := reAudioTitle.FindStringSubmatch(title.Label)
		ch.Number, _ = strconv.Atoi(m[1])
		ch.Name = m[2]
	}
	// Under a side panel or the routing matrix AUM marks the whole mixer
	// invisible; its M/S/R states still hold.
	under := title.Hidden
	s.Walk(func(el *Element, _ int) bool {
		switch {
		case el.Type == "Button" && ch.LevelFrame == nil && reLevel.MatchString(el.Label):
			ch.Kind = "audio"
			ch.LevelLabel = el.Label
			lf := el.Rect
			ch.LevelFrame = &lf
			if v, err := strconv.ParseFloat(reLevel.FindStringSubmatch(el.Label)[1], 64); err == nil {
				ch.LevelDB = &v
			}
		case el.Label == "%.1f" && ch.FaderFrame == nil:
			ff := el.Rect
			ch.FaderFrame = &ff
		case el.Type == "Other" && (el.Label == "MUTE" || el.Label == "SOLO" || el.Label == "REC") && (!el.Hidden || under) && !el.Rect.Empty():
			on, fr := el.Selected(), el.Rect
			switch el.Label {
			case "MUTE":
				ch.Muted, ch.MuteFrame = &on, &fr
			case "SOLO":
				ch.Soloed, ch.SoloFrame = &on, &fr
			case "REC":
				ch.Armed, ch.RecFrame = &on, &fr
			}
		case el.Type == "Button" && el.Label == "+1" && (!el.Hidden || under):
			ar := el.Rect
			ch.AddSlot = &ar
		case el.Type == "ScrollView" && ch.inserts.Empty():
			ch.inserts = el.Rect
		}
		return true
	})
	ch.Nodes = parseNodes(s, ch)
	return ch
}

// parseNodes finds the strip's node buttons by shape: square, 98 points for
// source and output, 74 for inserts, 88 for MIDI nodes. A loaded node has an
// inner button over it (68 or 52 points, value "1"), selected unless the
// node is bypassed; an empty slot ("+") has none.
func parseNodes(s *Element, ch AUMChannel) []AUMNode {
	var nodes []AUMNode
	var inner []*Element
	mid := s.Rect.Y + s.Rect.Height/2
	s.Walk(func(el *Element, _ int) bool {
		r := el.Rect
		if el.Type != "Button" || math.Abs(r.Width-r.Height) > 2 {
			return true
		}
		if r.Width >= 40 && r.Width < 70 && el.Label == "" {
			inner = append(inner, el)
			return true
		}
		if r.Width < 70 || r.Width > 110 {
			return true
		}
		n := AUMNode{Label: cleanLabel(el.Label), Frame: r, OnScreen: !el.Hidden}
		if n.Label == "-∞" { // a silent meter's reading, not a name
			n.Label = ""
		}
		switch {
		case ch.Kind == "midi":
			n.Role = "midi"
		case r.Width >= 95 && r.MidY() < mid:
			n.Role = "source"
		case r.Width >= 95:
			n.Role = "output"
		default:
			n.Role = "insert"
			if !ch.inserts.Empty() {
				cx, cy := r.Center()
				n.OnScreen = n.OnScreen && ch.inserts.Contains(cx, cy)
			}
		}
		nodes = append(nodes, n)
		return true
	})
	// Inserts sit on the strip's wire 41 points left of the output node's
	// centre (source and output share one axis); a bypassed node slides
	// about 55 points further left and leaves an eject ⏏ in its place.
	axis := math.NaN()
	for _, n := range nodes {
		if n.Role == "output" {
			axis, _ = n.Frame.Center()
		}
	}
	for i := range nodes {
		var in *Element
		for _, b := range inner {
			if cx, cy := b.Rect.Center(); nodes[i].Frame.Contains(cx, cy) {
				in = b
				break
			}
		}
		n := &nodes[i]
		n.Empty = in == nil
		cx, _ := n.Frame.Center()
		switch n.Role {
		case "insert":
			n.Bypassed = !n.Empty && !math.IsNaN(axis) && cx < axis-41-25
		case "source":
			n.Bypassed = !n.Empty && !math.IsNaN(axis) && cx < axis-25
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Frame.Y < nodes[j].Frame.Y })
	slot := 0
	for i := range nodes {
		nodes[i].Index = i + 1
		if nodes[i].Role == "insert" {
			slot++
			nodes[i].Slot = slot
		}
	}
	return nodes
}

// parseOverlays finds what covers the mixer: popovers (an element of type
// Popover, titled by its navigation bar or first text), alerts, the side
// panels (a full-height column at the right edge, titled in its header or,
// for the Navigator, its navigation bar) and the MIDI routing matrix
// (navigation bar "MIDI ROUTING").
func parseOverlays(root *Element, window Size) []AUMOverlay {
	var out []AUMOverlay
	root.Walk(func(el *Element, _ int) bool {
		switch {
		case el.Type == "Popover":
			out = append(out, newOverlay("popover", popoverTitle(el), el))
			return false
		case el.Type == "Alert":
			out = append(out, newOverlay("alert", el.Label, el))
			return false
		case el.Type == "NavigationBar" && el.Identifier == "MIDI ROUTING":
			out = append(out, AUMOverlay{Kind: "routing", Title: "MIDI ROUTING", Frame: el.Rect, el: el})
			return false
		case isSidePanel(el, window):
			out = append(out, newOverlay("panel", panelTitle(el), el))
			return false
		}
		return true
	})
	return out
}

func isSidePanel(el *Element, window Size) bool {
	r := el.Rect
	return el.Type == "Other" && window.Width > 0 && r.Y <= 1 && r.Height >= window.Height-2 &&
		r.Width >= 200 && r.Width <= 500 && math.Abs(r.X+r.Width-window.Width) <= 2 && panelTitle(el) != ""
}

// panelTitle is the text in a side panel's 60-point header, or the
// identifier of a navigation bar at its top (the Navigator).
//
// While AUM pushes a page (MENU → FILES, SAVE, MIDI CONTROL) the outgoing
// page's header stays in the tree for a moment, hidden and slid up above
// the screen (y -60), ahead of the new one. So a header must sit at the top
// edge, and a visible one wins over a hidden one.
func panelTitle(el *Element) string {
	header := func(e *Element) bool {
		r := e.Rect
		return e.Type == "StaticText" && e.Label != "" && r.Y >= -1 && r.Y <= 1 && r.Height >= 40 && r.Height <= 70
	}
	if t := el.First(func(e *Element) bool { return header(e) && !e.Hidden }); t != nil {
		return t.Label
	}
	if t := el.First(header); t != nil {
		return t.Label
	}
	if nb := el.First(func(e *Element) bool { return e.Type == "NavigationBar" && e.Identifier != "" && e.Rect.Y <= 1 }); nb != nil {
		return nb.Identifier
	}
	return ""
}

// popoverTitle is the identifier of the popover's navigation bar, else its
// first text that is not a button's caption; a bare menu has none.
func popoverTitle(el *Element) string {
	if nb := el.First(func(e *Element) bool { return e.Type == "NavigationBar" && e.Identifier != "" }); nb != nil {
		return nb.Identifier
	}
	var find func(e *Element, inButton bool) string
	find = func(e *Element, inButton bool) string {
		if e.Type == "StaticText" && e.Label != "" && !e.Rect.Empty() && !inButton {
			return e.Label
		}
		for _, c := range e.Children {
			if t := find(c, inButton || e.Type == "Button"); t != "" {
				return t
			}
		}
		return ""
	}
	return find(el, false)
}

func newOverlay(kind, title string, el *Element) AUMOverlay {
	o := AUMOverlay{Kind: kind, Title: title, Frame: el.Rect, el: el}
	seen := map[string]bool{}
	el.Walk(func(e *Element, _ int) bool {
		if e.Hidden || e.Rect.Empty() {
			return true
		}
		switch e.Type {
		case "Button":
			if l := cleanLabel(e.Label); l != "" && l != "chevron" && l != "Clear text" && !seen[l] {
				seen[l] = true
				o.Buttons = append(o.Buttons, l)
			}
		case "TextField", "SearchField", "TextView", "SecureTextField":
			o.Fields = append(o.Fields, e.Value)
		}
		return true
	})
	o.Rows = formRows(el, title)
	return o
}

// formRows pairs every text of el with the nearest value right of it on the
// same line: a button's label (or its text), or another text.
// stepper finds the unlabelled ‹ and › buttons right beside value frame v.
func stepper(v Rect, blanks []*Element) (prev, next *Rect) {
	for _, b := range blanks {
		r := b.Rect
		if math.Abs(r.MidY()-v.MidY()) > 4 {
			continue
		}
		switch {
		case math.Abs(r.X+r.Width-v.X) <= 4:
			prev = &r
		case math.Abs(r.X-(v.X+v.Width)) <= 4:
			next = &r
		}
	}
	return prev, next
}

func formRows(el *Element, title string) []AUMRow {
	type item struct {
		e      *Element
		text   string
		button bool
	}
	var texts, values []item
	var blanks []*Element
	var walk func(e, parent *Element)
	walk = func(e, parent *Element) {
		if !e.Hidden && !e.Rect.Empty() {
			switch e.Type {
			case "StaticText":
				if e.Label != "" && (parent == nil || parent.Type != "Button") {
					texts = append(texts, item{e: e, text: e.Label})
				}
			case "Button":
				t := cleanLabel(e.Text())
				if t == "" {
					for _, c := range e.Children {
						if c.Type == "StaticText" && c.Label != "" {
							t = c.Label
							break
						}
					}
				}
				switch {
				case t == "" && e.Rect.Width <= 44:
					blanks = append(blanks, e)
				case t != "" && t != "chevron":
					values = append(values, item{e: e, text: t, button: true})
				}
			}
		}
		for _, c := range e.Children {
			walk(c, e)
		}
	}
	walk(el, nil)
	values = append(values, texts...)
	var rows []AUMRow
	for _, t := range texts {
		if t.text == title {
			continue
		}
		row := AUMRow{Label: t.text, Frame: t.e.Rect}
		best := math.MaxFloat64
		for _, v := range values {
			vr, tr := v.e.Rect, t.e.Rect
			if v.e == t.e || vr.X < tr.X+tr.Width-1 {
				continue
			}
			if math.Abs(vr.MidY()-tr.MidY()) > math.Max(vr.Height, tr.Height)/2+2 {
				continue
			}
			if d := vr.X - tr.X; d < best {
				best = d
				row.Value = v.text
				vf := vr
				row.ValueFrame = &vf
			}
		}
		if row.ValueFrame != nil {
			row.Prev, row.Next = stepper(*row.ValueFrame, blanks)
		}
		rows = append(rows, row)
	}
	return rows
}

// Overlay returns the front-most overlay of kind ("" = any).
func (st AUMState) Overlay(kind string) *AUMOverlay {
	for i := len(st.Overlays) - 1; i >= 0; i-- {
		if kind == "" || st.Overlays[i].Kind == kind {
			return &st.Overlays[i]
		}
	}
	return nil
}

// Panel is the title of the open side panel ("MENU", "SETTINGS", …), or "".
func (st AUMState) Panel() string {
	if o := st.Overlay("panel"); o != nil {
		return o.Title
	}
	return ""
}

// Row finds an overlay row by its text (case-insensitive; a prefix matches
// when nothing matches in full).
func (o *AUMOverlay) Row(label string) (AUMRow, bool) {
	for _, r := range o.Rows {
		if strings.EqualFold(r.Label, label) {
			return r, true
		}
	}
	for _, r := range o.Rows {
		if strings.HasPrefix(strings.ToLower(r.Label), strings.ToLower(label)) {
			return r, true
		}
	}
	return AUMRow{}, false
}

// noChannelError is Channel's error for a selector that no strip of those
// read answers: one of a mixer wider than the window may be off screen.
type noChannelError struct{ msg string }

func (e *noChannelError) Error() string { return e.msg }

func noChannel(format string, a ...any) error { return &noChannelError{fmt.Sprintf(format, a...)} }

// isPosition reports whether sel selects a strip by its position ("#3").
func isPosition(sel string) bool { return strings.HasPrefix(strings.TrimSpace(sel), "#") }

// Channel finds a strip by selector: "#3" (the third strip from the left),
// a number ("2" = the strip titled "2: …" or "CHAN 2"), a MIDI title
// ("MIDI 1"), or a case-insensitive part of the title.
func (st AUMState) Channel(sel string) (AUMChannel, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return AUMChannel{}, fmt.Errorf("empty channel selector")
	}
	if pos, ok := strings.CutPrefix(sel, "#"); ok {
		n, err := strconv.Atoi(pos)
		switch {
		case err != nil || n < 1:
			return AUMChannel{}, fmt.Errorf("no strip %s: the mixer has %d (channels: %s)", sel, len(st.Channels), st.channelList())
		case n > len(st.Channels):
			return AUMChannel{}, noChannel("no strip %s: the mixer has %d (channels: %s)", sel, len(st.Channels), st.channelList())
		}
		return st.Channels[n-1], nil
	}
	if n, err := strconv.Atoi(sel); err == nil {
		for _, ch := range st.Channels {
			if ch.Kind == "audio" && ch.Number == n && !reMIDITitle.MatchString(ch.Title) {
				return ch, nil
			}
		}
		return AUMChannel{}, noChannel("no audio channel %d (channels: %s; select a custom-named strip by title or #position)", n, st.channelList())
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
		return AUMChannel{}, noChannel("no channel matches %q (channels: %s)", sel, st.channelList())
	case 1:
		return hits[0], nil
	}
	return AUMChannel{}, fmt.Errorf("%q matches %d channels; use the number or #position (channels: %s)", sel, len(hits), st.channelList())
}

func (st AUMState) channelList() string {
	if len(st.Channels) == 0 {
		return "none"
	}
	names := make([]string, len(st.Channels))
	for i, ch := range st.Channels {
		names[i] = fmt.Sprintf("#%d %q", ch.Strip, ch.Title)
	}
	return strings.Join(names, ", ")
}

// Node finds a strip's node by selector: "source", "output", an insert slot
// number ("1" = the first insert slot), or a node label ("LP",
// case-insensitive).
func (ch AUMChannel) Node(sel string) (AUMNode, error) {
	sel = strings.TrimSpace(sel)
	switch strings.ToLower(sel) {
	case "":
		return AUMNode{}, fmt.Errorf("empty node selector")
	case "source", "output":
		for _, n := range ch.Nodes {
			if n.Role == strings.ToLower(sel) {
				return n, nil
			}
		}
		return AUMNode{}, fmt.Errorf("strip %q has no %s node (nodes: %s)", ch.Title, sel, ch.nodeList())
	}
	if k, err := strconv.Atoi(sel); err == nil {
		for _, n := range ch.Nodes {
			if (n.Role == "insert" && n.Slot == k) || (n.Role == "midi" && n.Index == k) {
				return n, nil
			}
		}
		return AUMNode{}, fmt.Errorf("strip %q has no slot %d (nodes: %s)", ch.Title, k, ch.nodeList())
	}
	var hits []AUMNode
	for _, n := range ch.Nodes {
		if strings.EqualFold(n.Label, sel) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		return AUMNode{}, fmt.Errorf("strip %q has no node labelled %q (nodes: %s)", ch.Title, sel, ch.nodeList())
	case 1:
		return hits[0], nil
	}
	return AUMNode{}, fmt.Errorf("%d nodes on %q are labelled %q; use the slot number (nodes: %s)", len(hits), ch.Title, sel, ch.nodeList())
}

func (ch AUMChannel) nodeList() string {
	if len(ch.Nodes) == 0 {
		return "none"
	}
	var parts []string
	for _, n := range ch.Nodes {
		p := n.Role
		if n.Role == "insert" {
			p = fmt.Sprintf("slot %d", n.Slot)
		}
		switch {
		case n.Empty:
			p += " (empty)"
		case n.Label != "":
			p += " " + strconv.Quote(n.Label)
		}
		if n.Bypassed {
			p += " bypassed"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}
