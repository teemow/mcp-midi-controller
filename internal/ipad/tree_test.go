package ipad

import (
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

func TestParseSourceWDAShapes(t *testing.T) {
	raw := `{"type":"XCUIElementTypeButton","label":null,"value":7,"isEnabled":"0","rect":{"x":1,"y":2,"width":3,"height":4},
		"children":[{"type":"StaticText","label":"hi","value":"hi","isEnabled":"1","rect":{"x":0,"y":0,"width":5,"height":5}}]}`
	root, err := ParseSource([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "Button" || root.Label != "" || root.Value != "7" || root.Enabled || root.Rect.Height != 4 {
		t.Fatalf("root = %+v", root)
	}
	if len(root.Children) != 1 || !root.Children[0].Enabled || root.Children[0].Label != "hi" {
		t.Fatalf("child = %+v", root.Children)
	}
	if _, err := ParseSource([]byte(`[`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestFlatten(t *testing.T) {
	root, err := ParseSource(ipadtest.DefaultAUM().Source())
	if err != nil {
		t.Fatal(err)
	}
	nodes, cut := root.Flatten(FlattenOptions{})
	text := FormatNodes(nodes, false)
	if cut {
		t.Fatal("unexpected truncation")
	}
	for _, want := range []string{`Button "MIDI 1" (79,764 156x50)`, `Other "MUTE"`, `StaticText "♩120"`, `Button "-1.0 dB"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("flattened tree lacks %s:\n%s", want, text)
		}
	}
	// The spelled-out M of MUTE and zero-size texts are dropped.
	if strings.Contains(text, `StaticText "M" `) || strings.Contains(text, `"U"`) {
		t.Fatalf("noise kept:\n%s", text)
	}
	// StaticText values equal to the label are not repeated.
	if strings.Contains(text, "value=") {
		t.Fatalf("duplicate value shown:\n%s", text)
	}

	all, _ := root.Flatten(FlattenOptions{All: true})
	if len(all) <= len(nodes) {
		t.Fatalf("all=%d should list more than labelled=%d", len(all), len(nodes))
	}
	if indented := FormatNodes(all[:3], true); !strings.HasPrefix(strings.Split(indented, "\n")[1], "  ") {
		t.Fatalf("all=true should indent by depth:\n%s", indented)
	}

	hits, _ := root.Flatten(FlattenOptions{Contains: "synth"})
	if len(hits) != 8 { // four titles, each repeated by a node button
		t.Fatalf("contains=synth: %d hits\n%s", len(hits), FormatNodes(hits, false))
	}
	limited, cut := root.Flatten(FlattenOptions{Limit: 3})
	if len(limited) != 3 || !cut {
		t.Fatalf("limit: %d nodes, cut=%v", len(limited), cut)
	}
}
