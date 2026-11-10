package ipadtest

import (
	"encoding/json"
	"fmt"
)

// Strip is one mixer strip of a synthetic AUM tree.
type Strip struct {
	// Title is the title button's label ("2: Synth", "MIDI 1", "CHAN 3",
	// or a custom name).
	Title string
	// Level is the dB label above the fader ("-1.0 dB", "-∞ dB"); empty for
	// MIDI strips.
	Level string
	// Muted, Soloed and Armed light M, S and R (the Selected trait). A
	// muted strip's MUTE button widens over SOLO, which leaves the tree.
	Muted, Soloed, Armed bool
	// Source is the source node's label; by default it repeats the title,
	// as AUM's square node button does. EmptySource leaves the slot empty.
	Source      string
	EmptySource bool
	// Inserts are the insert slots top to bottom.
	Inserts []Node
	// AddSlot shows the orange "+1" below the inserts.
	AddSlot bool
	// PlayEnable adds a File Player's PLAY ENABLE toggle (on when true).
	PlayEnable *bool
}

// Node is an insert slot.
type Node struct {
	// Label is the node's short name ("LP"); "" with Empty for a "+" slot.
	Label    string
	Empty    bool
	Bypassed bool
}

// Overlay is a popover, side panel or alert over the mixer.
type Overlay struct {
	// Kind is "popover", "panel" or "alert".
	Kind  string
	Title string
	// Rows are text → value pairs (value "" = a text alone).
	Rows [][2]string
	// Buttons are labelled buttons stacked below the rows.
	Buttons []string
	// Fields are text fields (their values).
	Fields []string
	// Cells are list cells (FILES): a name and the detail line below it.
	Cells [][2]string
	// NavBar titles a side panel with a navigation bar (the Navigator's:
	// an eye and a bookmark filter, no close button) instead of a 60-point
	// header text.
	NavBar bool
	// Frame overrides the default frame (x, y, w, h).
	Frame *[4]float64
}

// AUM describes a synthetic AUM screen. The shapes follow WDA's JSON source
// of AUM 1.4.8 (frames, types, traits, the spelled-out MUTE texts, the
// square node buttons with their inner button, the add tile and its
// AUDIO/MIDI/IMPORT menu, popovers and side panels); the names are made up.
type AUM struct {
	Tempo    string // "120" -> "♩120"
	Position string // "001:1  00:00.0"
	// MeterLabel is the main meter's stale "SESSION: …" accessibility label
	// (AUM does not refresh it; parsers must ignore it).
	MeterLabel string
	// Session set: the main menu is open and shows it.
	Session     string
	StartScreen bool
	TempoBar    bool
	// AddMenu opens the add tile's AUDIO / MIDI / IMPORT menu.
	AddMenu bool
	Strips  []Strip
	// Overlays are drawn over the mixer, front-most last.
	Overlays []Overlay
	// PluginWindow opens a plugin window with this title ("Synth @A1").
	PluginWindow string
	// Routing opens the MIDI routing matrix with these endpoints (name and
	// kind pairs).
	Routing *Routing
	// MeterSource is printed above the main meter (default "SPKR OUT").
	MeterSource string
	// Keyboard opens the built-in keyboard with its HIDE↓ toolbar button.
	Keyboard bool
}

// Routing is the MIDI routing matrix of a synthetic AUM screen.
type Routing struct {
	Sources      [][2]string
	Destinations [][2]string
}

// Cell is the matrix cell of source i and destination j, as x, y, w, h in
// points (light it with WDA.Bright to mark a connection).
func (r Routing) Cell(i, j int) [4]float64 {
	return [4]float64{430 + float64(i)*37 - 2, 434 + float64(j)*37 - 2, 36, 36}
}

type node map[string]any

// hide marks n and everything in it invisible (WDA isVisible "0").
func hide(n node) {
	n["isVisible"] = "0"
	if kids, ok := n["children"].([]node); ok {
		for _, k := range kids {
			hide(k)
		}
	}
}

func el(typ, label string, x, y, w, h float64, children ...node) node {
	n := node{
		"type": typ, "rect": node{"x": x, "y": y, "width": w, "height": h},
		"isEnabled": "1", "isVisible": "1", "traits": "", "value": nil, "rawIdentifier": nil,
	}
	if label != "" {
		n["label"] = label
		n["name"] = label
		if typ == "StaticText" {
			n["value"] = label
		}
	} else {
		n["label"] = nil
	}
	if len(children) > 0 {
		n["children"] = children
	}
	return n
}

func (n node) with(k string, v any) node { n[k] = v; return n }

// Source renders a as WDA's JSON source value.
func (a AUM) Source() json.RawMessage {
	const W, H = 1210.0, 834.0
	top := []node{}
	if a.Position != "" {
		x := 89.0
		if a.TempoBar {
			x = 340
		}
		top = append(top, el("StaticText", a.Position, x, 1, 135, 16))
	}
	if a.Tempo != "" {
		if a.TempoBar {
			// ✕, TAP, ‹ ♩ ›, metronome, Clock Options •••; the tempo is a
			// button here.
			top = append(top,
				el("Button", "", 0, 0, 60, 60),
				el("Button", "TAP", 60, 8, 50, 50),
				el("Button", "", 110, 8, 30, 50),
				el("Button", "♩"+a.Tempo, 138, 8, 60, 50).with("traits", "StaticText, Button"),
				el("Button", "", 200, 8, 30, 50),
				el("Button", "", 230, 8, 50, 50).with("traits", "Button"),
				el("Button", "", 280, 8, 50, 50))
		} else {
			top = append(top, el("StaticText", "♩"+a.Tempo, 0, 22, 60, 22))
		}
	}
	meter := a.MeterSource
	if meter == "" {
		meter = "SPKR OUT"
	}
	if a.MeterLabel != "" {
		top = append(top, el("Image", a.MeterLabel, 436, 0, 295, 60, el("StaticText", meter, 436, 4, 295, 12)))
	} else {
		top = append(top, el("StaticText", meter, 436, 4, 295, 12))
	}
	top = append(top, el("Button", "", 1000, 0, 60, 60, el("StaticText", "DSP:  3 %", 1000, 15, 60, 15)))

	var mixer []node
	lastX := -235.0
	for i, s := range a.Strips {
		x := 7 + float64(i)*242
		lastX = x
		mixer = append(mixer, strip(x, s))
	}
	// The add tile is an unlabelled button in a column of its own; its
	// menu is in the tree only while it is open.
	tile := lastX + 242
	col := []node{el("Button", "", tile+46, 372, 150, 150)}
	if a.AddMenu {
		col = append(col,
			el("Button", "AUDIO", tile+46, 372, 150, 45, el("StaticText", "AUDIO", tile+95, 383, 52, 23)),
			el("Button", "MIDI", tile+46, 425, 150, 45, el("StaticText", "MIDI", tile+103, 436, 36, 23)),
			el("Button", "IMPORT", tile+46, 477, 150, 45, el("StaticText", "IMPORT", tile+91, 488, 60, 23)))
	}
	mixer = append(mixer, el("Other", "", tile, 60, 242, 774, col...))
	mixerView := el("ScrollView", "", 0, 60, W, 774, mixer...)
	// Under a side panel or the routing matrix AUM marks the mixer invisible.
	covered := a.Session != "" || a.Routing != nil
	for _, o := range a.Overlays {
		covered = covered || o.Kind == "panel"
	}
	if covered {
		hide(mixerView)
	}
	main := []node{el("Other", "", 0, 0, W, 60, top...), mixerView}
	if a.StartScreen {
		main = append(main, el("Button", "LAST STATE", 457, 505, 115, 30), el("Button", "DEFAULT", 457, 568, 115, 30))
	}
	if a.Session != "" {
		main = append(main, menuPanel(a.Session))
	}
	if a.PluginWindow != "" {
		main = append(main, el("Other", "", 93, 60, 1024, 383,
			el("Button", "-∞", 161, 60, 36, 32), el("Button", "", 197, 60, 32, 32),
			el("Button", a.PluginWindow, 539, 64, 100, 24), el("Button", "", 640, 60, 28, 32),
			el("Button", "", 1049, 60, 32, 32), el("Button", "", 1081, 60, 32, 32)))
	}
	if a.Routing != nil {
		main = append(main, routing(*a.Routing))
	}
	if a.Keyboard {
		main = append(main, el("Other", "", 0, 666, W, 168,
			el("Button", "HIDE↓", 10, 666, 48, 24, el("StaticText", "HIDE↓", 13, 669, 42, 18)),
			el("Button", "DSP:  3 %", 1152, 666, 48, 24)))
	}
	for _, o := range a.Overlays {
		main = append(main, overlay(o)...)
	}
	root := el("Application", "AUM", 0, 0, W, H,
		el("Window", "", 0, 0, W, H, el("Other", "", 0, 0, W, H, main...)))
	b, err := json.Marshal(root)
	if err != nil {
		panic(fmt.Sprintf("ipadtest: marshal AUM source: %v", err))
	}
	return b
}

// nodeButton is a square node button and, when loaded, the inner button
// over it (value "1", Selected).
func nodeButton(label string, x, y, size float64, empty bool) []node {
	out := []node{el("Button", label, x, y, size, size, el("StaticText", label, x+15, y+size-28, size-30, 14))}
	if !empty {
		in := size - 30
		if size < 80 {
			in = size - 22
		}
		off := (size - in) / 2
		out = append(out, el("Button", "", x+off, y+off, in, in).with("value", "1").with("traits", "Selected, Button"))
	}
	return out
}

func strip(x float64, s Strip) node {
	midi := s.Level == ""
	var kids []node
	if midi {
		// AUM glues texts around a MIDI node into its label.
		glued := "-∞, DSP:  2 %, -∞, " + s.Title + ", " + s.Title
		kids = append(kids, el("Other", "", x, 127, 228, 88, nodeButton(glued, x+111, 127, 88, false)...))
	} else {
		src := s.Source
		if src == "" {
			src = s.Title
		}
		kids = append(kids, el("Other", "", x, 88, 199, 98, nodeButton(src, x+101, 88, 98, s.EmptySource)...))
		kids = append(kids, el("Button", "", x+193, 87, 41, 99))
		if s.PlayEnable != nil {
			t := el("Button", "", x+113, 125, 24, 24)
			if *s.PlayEnable {
				t = t.with("traits", "Selected, Button")
			}
			kids = append(kids, el("StaticText", "PLAY ENABLE", x+93, 88, 65, 37), t)
		}
		var ins []node
		for k, n := range s.Inserts {
			nx := x + 72
			if n.Bypassed {
				nx -= 55
			}
			y := 221 + float64(k)*84
			ins = append(ins, el("Other", "", x, y, 146, 74, nodeButton(n.Label, nx, y, 74, n.Empty)...))
		}
		if s.AddSlot {
			ins = append(ins, el("Button", "+1", x+57, 221+float64(len(s.Inserts))*84, 42, 42))
		} else {
			ins = append(ins, el("Button", "+1", x+57, 678, 42, 42).with("isVisible", "0"))
		}
		kids = append(kids, el("ScrollView", "", x, 216, 166, 420, ins...))
		kids = append(kids, el("Other", "%.1f", x+166, 228, 62, 397, el("Button", s.Level, x+166, 203, 62, 20).with("traits", "StaticText, Button")))
		if s.Muted {
			kids = append(kids, el("Other", "MUTE", x+166, 630, 62, 30,
				el("StaticText", "M", x+180, 630, 11, 30), el("StaticText", "U", x+191, 630, 9, 30),
				el("StaticText", "T", x+200, 630, 7, 30), el("StaticText", "E", x+207, 630, 8, 30)).with("traits", "Selected"))
		} else {
			kids = append(kids,
				el("Other", "MUTE", x+166, 630, 30, 30,
					el("StaticText", "M", x+176, 630, 11, 30), el("StaticText", "U", x+191, 645, 0, 0)),
				el("Other", "SOLO", x+198, 630, 30, 30, el("StaticText", "S", x+210, 630, 7, 30)).with("traits", sel(s.Soloed)))
		}
		rec := el("Other", "REC", x+61, 700, 30, 30, el("StaticText", "R", x+72, 700, 8, 30), el("StaticText", "E", x+83, 714, 2, 2))
		kids = append(kids, el("Other", "", x, 666, 228, 98, append([]node{rec.with("traits", sel(s.Armed))}, nodeButton("SPKR", x+101, 666, 98, false)...)...))
	}
	kids = append(kids, el("Button", s.Title, x+72, 764, 156, 50))
	return el("Other", "", x, 65, 228, 764, kids...)
}

func sel(on bool) string {
	if on {
		return "Selected"
	}
	return ""
}

func menuPanel(session string) node {
	kids := []node{el("StaticText", "SESSION: "+session, 965, 75, 230, 20)}
	rows := [][]string{{"FILES", "SAVE", "CLEAR"}, {"NOTES", "ABOUT", "HELP"}, {"SETTINGS", "MIDI ROUTE", "MIDI CTRL"}}
	for r, row := range rows {
		y := 105 + float64(r)*114
		for c, caption := range row {
			x := 965 + float64(c)*83
			kids = append(kids, el("Button", "", x, y, 64, 64), el("StaticText", caption, x, y+64, 64, 20))
		}
	}
	return el("Other", "", 950, 0, 260, 834,
		el("Other", "", 950, 0, 260, 60, el("StaticText", "MENU", 990, 0, 180, 60)),
		el("ScrollView", "", 950, 60, 260, 774, kids...))
}

func overlay(o Overlay) []node {
	switch o.Kind {
	case "panel":
		f := [4]float64{950, 0, 260, 834}
		if o.Frame != nil {
			f = *o.Frame
		}
		// The back button's label is glued from texts around it, as AUM's is.
		kids := []node{el("Other", "", f[0], 0, f[2], 60,
			el("Button", "SESSION: Old, Copyright (c) Someone, DSP:  0 %, -∞", f[0], 0, 60, 60),
			el("StaticText", o.Title, f[0]+40, 0, f[2]-80, 60))}
		if o.NavBar {
			kids = []node{el("NavigationBar", "", f[0], 0, f[2], 50,
				el("StaticText", o.Title, f[0]+95, 17, 82, 20).with("traits", "Header"),
				el("Button", "", f[0]+16, 11, 26, 27).with("traits", "NotEnabled, Button"),
				el("Button", "", f[0]+f[2]-56, 5, 40, 40)).with("rawIdentifier", o.Title)}
		}
		kids = append(kids, body(o, f[0], 70, f[2])...)
		return []node{el("Other", "", f[0], f[1], f[2], f[3], kids...)}
	case "alert":
		f := [4]float64{455, 300, 300, 200}
		if o.Frame != nil {
			f = *o.Frame
		}
		kids := []node{el("StaticText", o.Title, f[0]+20, f[1]+15, f[2]-40, 22)}
		kids = append(kids, body(o, f[0], f[1]+50, f[2])...)
		return []node{el("Alert", o.Title, f[0], f[1], f[2], f[3], kids...)}
	}
	f := [4]float64{633, 186, 500, 400}
	if o.Frame != nil {
		f = *o.Frame
	}
	kids := []node{el("NavigationBar", "", f[0], f[1]+13, f[2], 50, el("StaticText", o.Title, f[0]+180, f[1]+30, 140, 20)).with("rawIdentifier", o.Title)}
	kids = append(kids, body(o, f[0], f[1]+63, f[2])...)
	return []node{
		el("Other", "dismiss popup", 0, 0, 1210, 834).with("rawIdentifier", "PopoverDismissRegion"),
		el("Popover", "", f[0], f[1], f[2], f[3], kids...),
	}
}

// body lays out rows (text left, value button right), then text fields,
// then buttons, 50 points apart.
func body(o Overlay, x, y, w float64) []node {
	var out []node
	for _, r := range o.Rows {
		out = append(out, el("StaticText", r[0], x+16, y+15, 120, 20))
		if r[1] != "" {
			out = append(out, el("Button", r[1], x+w-115, y+7, 100, 36, el("StaticText", r[1], x+w-90, y+16, 50, 17)))
		}
		y += 50
	}
	for _, f := range o.Fields {
		// iOS lists the field's clear button as a button of the dialog.
		out = append(out, el("TextField", "", x+16, y+5, w-32, 36,
			el("Button", "Clear text", x+w-40, y+15, 16, 16)).with("value", f))
		y += 50
	}
	for _, b := range o.Buttons {
		out = append(out, el("Button", b, x+16, y+5, w-32, 40, el("StaticText", b, x+w/2-40, y+15, 80, 20)))
		y += 50
	}
	for _, c := range o.Cells {
		out = append(out, el("Cell", "", x, y, w, 51,
			el("StaticText", c[0], x+65, y+7, 120, 20),
			el("StaticText", c[1], x+65, y+29, 110, 14)))
		y += 50
	}
	return out
}

func routing(r Routing) node {
	kids := []node{el("NavigationBar", "", 0, 60, 1210, 50, el("StaticText", "MIDI ROUTING", 555, 77, 100, 20)).with("rawIdentifier", "MIDI ROUTING")}
	for i, s := range r.Sources {
		x := 430 + float64(i)*37
		kids = append(kids, el("StaticText", s[0], x, 255, 18, 132), el("StaticText", s[1], x+14, 255, 18, 132))
	}
	for j, d := range r.Destinations {
		y := 434 + float64(j)*37
		kids = append(kids, el("StaticText", d[0], 653, y, 132, 18), el("StaticText", d[1], 653, y+14, 132, 18))
	}
	return el("Other", "", 0, 60, 1210, 774, kids...)
}

// DefaultAUM is a small session: a MIDI strip and five audio strips (one at
// -∞, the last scrolled off screen), the main menu open, and a stale meter
// label naming another session.
func DefaultAUM() AUM {
	return AUM{
		Tempo: "120", Position: "001:1  00:00.0",
		MeterLabel: "SESSION: Old Session, MIDI 1, 0.0, 0, -24, -48, SPKR OUT, -∞",
		Session:    "test-session (mcp test)",
		Strips: []Strip{
			{Title: "MIDI 1"},
			{Title: "2: Synth One", Level: "-1.0 dB", Inserts: []Node{{Label: "LP"}, {Empty: true}}},
			{Title: "3: Drum Machine", Level: "-∞ dB", Muted: true},
			{Title: "4: Synth Two", Level: "+0.0 dB"},
			{Title: "5: Bass Synth", Level: "-7.3 dB"},
			{Title: "6: Pad Synth", Level: "-7.9 dB"},
		},
	}
}

// MixerAUM is DefaultAUM with the main menu closed.
func MixerAUM() AUM {
	a := DefaultAUM()
	a.Session = ""
	return a
}
