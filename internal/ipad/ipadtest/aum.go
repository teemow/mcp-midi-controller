package ipadtest

import (
	"encoding/json"
	"fmt"
)

// Strip is one mixer strip of a synthetic AUM tree.
type Strip struct {
	// Title is the title button's label ("2: Synth", "MIDI 1").
	Title string
	// Level is the dB label above the fader ("-1.0 dB", "-∞ dB"); empty for
	// MIDI strips.
	Level string
}

// AUM describes a synthetic AUM screen. The shapes follow WDA's JSON source
// of AUM 1.4.8 (frames, types, the spelled-out MUTE texts, the square node
// button that repeats a channel title); the names are made up.
type AUM struct {
	Tempo    string // "120" -> "♩120"
	Position string // "001:1  00:00.0"
	// MeterLabel is the main meter's stale "SESSION: …" accessibility label
	// (AUM does not refresh it; parsers must ignore it).
	MeterLabel string
	// Session set: the main menu is open and shows it.
	Session     string
	StartScreen bool
	Strips      []Strip
}

type node map[string]any

func el(typ, label string, x, y, w, h float64, children ...node) node {
	n := node{
		"type": typ, "rect": node{"x": x, "y": y, "width": w, "height": h},
		"isEnabled": "1", "isVisible": "0", "traits": "", "value": nil,
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

// Source renders a as WDA's JSON source value.
func (a AUM) Source() json.RawMessage {
	top := []node{}
	if a.Position != "" {
		top = append(top, el("StaticText", a.Position, 89, 1, 135, 16))
	}
	if a.Tempo != "" {
		top = append(top, el("StaticText", "♩"+a.Tempo, 0, 22, 60, 22))
	}
	if a.MeterLabel != "" {
		top = append(top, el("Image", a.MeterLabel, 436, 0, 295, 60, el("StaticText", "SPKR OUT", 436, 4, 295, 12)))
	}
	top = append(top, el("Button", "", 1000, 0, 60, 60, el("StaticText", "DSP:  3 %", 1000, 15, 60, 15)))

	var strips []node
	for i, s := range a.Strips {
		x := 7 + float64(i)*242
		kids := []node{
			// The node button can repeat the title; it is square.
			el("Other", "", x, 88, 199, 98, el("Button", s.Title, x+101, 88, 98, 98)),
		}
		if s.Level != "" {
			kids = append(kids,
				el("Other", "%.1f", x+166, 228, 62, 397, el("Button", s.Level, x+166, 203, 62, 20)),
				el("Other", "MUTE", x+166, 630, 30, 30,
					el("StaticText", "M", x+176, 630, 11, 30), el("StaticText", "U", x+191, 645, 0, 0)),
				el("Other", "SOLO", x+198, 630, 30, 30, el("StaticText", "S", x+210, 630, 7, 30)),
			)
		}
		kids = append(kids, el("Button", s.Title, x+72, 764, 156, 50))
		strips = append(strips, el("Other", "", x, 65, 228, 764, kids...))
	}
	main := []node{el("Other", "", 0, 0, 1210, 60, top...), el("ScrollView", "", 0, 60, 1210, 774, strips...)}
	if a.StartScreen {
		main = append(main, el("Button", "LAST STATE", 457, 505, 115, 30), el("Button", "DEFAULT", 457, 568, 115, 30))
	}
	if a.Session != "" {
		main = append(main, el("Other", "", 950, 0, 260, 834,
			el("StaticText", "MENU", 990, 0, 180, 60),
			el("StaticText", "SESSION: "+a.Session, 965, 75, 230, 20),
			el("StaticText", "FILES", 965, 169, 64, 20)))
	}
	root := el("Application", "AUM", 0, 0, 1210, 834,
		el("Window", "", 0, 0, 1210, 834, el("Other", "", 0, 0, 1210, 834, main...)))
	b, err := json.Marshal(root)
	if err != nil {
		panic(fmt.Sprintf("ipadtest: marshal AUM source: %v", err))
	}
	return b
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
			{Title: "2: Synth One", Level: "-1.0 dB"},
			{Title: "3: Drum Machine", Level: "-∞ dB"},
			{Title: "4: Synth Two", Level: "+0.0 dB"},
			{Title: "5: Bass Synth", Level: "-7.3 dB"},
			{Title: "6: Pad Synth", Level: "-7.9 dB"},
		},
	}
}
