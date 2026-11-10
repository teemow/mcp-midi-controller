package ipad

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Rect is an element frame in points.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Center is the rect's centre point.
func (r Rect) Center() (float64, float64) { return r.X + r.Width/2, r.Y + r.Height/2 }

// Contains reports whether the point lies inside the rect.
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && x <= r.X+r.Width && y >= r.Y && y <= r.Y+r.Height
}

// MidY is the rect's vertical centre.
func (r Rect) MidY() float64 { return r.Y + r.Height/2 }

// Empty reports a zero-area rect (AUM keeps many zero-size text nodes).
func (r Rect) Empty() bool { return r.Width <= 0 || r.Height <= 0 }

func (r Rect) String() string {
	return fmt.Sprintf("(%g,%g %gx%g)", r.X, r.Y, r.Width, r.Height)
}

// Element is one node of an app's accessibility tree.
type Element struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	// Identifier is the accessibility identifier (WDA's rawIdentifier):
	// AUM sets it on popover parts ("PopoverDismissRegion") and navigation
	// bars (the popover's title).
	Identifier string `json:"identifier,omitempty"`
	Rect       Rect   `json:"rect"`
	Enabled    bool   `json:"enabled"`
	// Hidden is WDA's isVisible "0": AUM keeps hidden controls in the tree
	// (the add-channel menu, a scrolled-away "+1").
	Hidden   bool       `json:"hidden,omitempty"`
	Traits   string     `json:"traits,omitempty"`
	Children []*Element `json:"children,omitempty"`
}

// Selected reports the accessibility "Selected" trait: AUM sets it on a lit
// MUTE, SOLO or REC button and on toggles that are on.
func (e *Element) Selected() bool { return hasTrait(e.Traits, "Selected") }

func hasTrait(traits, t string) bool {
	for _, f := range strings.Split(traits, ",") {
		if strings.TrimSpace(f) == t {
			return true
		}
	}
	return false
}

// Text is the element's label, or its value when it has no label (AUM's
// filter buttons carry only a value).
func (e *Element) Text() string {
	if e.Label != "" {
		return e.Label
	}
	return e.Value
}

// rawElement is WDA's JSON source node. Booleans arrive as "0"/"1" strings and
// value can be any JSON scalar.
type rawElement struct {
	Type       string          `json:"type"`
	Label      *string         `json:"label"`
	Identifier *string         `json:"rawIdentifier"`
	Value      json.RawMessage `json:"value"`
	Rect       Rect            `json:"rect"`
	IsEnabled  json.RawMessage `json:"isEnabled"`
	IsVisible  json.RawMessage `json:"isVisible"`
	Traits     *string         `json:"traits"`
	Children   []rawElement    `json:"children"`
}

// ParseSource decodes WDA's GET /source?format=json value.
func ParseSource(raw []byte) (*Element, error) {
	var r rawElement
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode accessibility tree: %w", err)
	}
	return r.convert(), nil
}

func (r *rawElement) convert() *Element {
	e := &Element{
		Type:    strings.TrimPrefix(r.Type, "XCUIElementType"),
		Rect:    r.Rect,
		Enabled: truthy(r.IsEnabled),
		Hidden:  len(r.IsVisible) > 0 && !truthy(r.IsVisible),
	}
	if r.Label != nil {
		e.Label = *r.Label
	}
	if r.Traits != nil {
		e.Traits = *r.Traits
	}
	if r.Identifier != nil {
		e.Identifier = *r.Identifier
	}
	e.Value = scalar(r.Value)
	for i := range r.Children {
		e.Children = append(e.Children, r.Children[i].convert())
	}
	return e
}

func truthy(raw json.RawMessage) bool {
	switch strings.Trim(string(raw), `"`) {
	case "1", "true":
		return true
	}
	return false
}

func scalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// Walk visits e and its descendants depth-first; fn returning false skips the
// node's children.
func (e *Element) Walk(fn func(el *Element, depth int) bool) {
	var walk func(el *Element, depth int)
	walk = func(el *Element, depth int) {
		if !fn(el, depth) {
			return
		}
		for _, c := range el.Children {
			walk(c, depth+1)
		}
	}
	walk(e, 0)
}

// Find returns every element (in tree order) for which match is true.
func (e *Element) Find(match func(*Element) bool) []*Element {
	var out []*Element
	e.Walk(func(el *Element, _ int) bool {
		if match(el) {
			out = append(out, el)
		}
		return true
	})
	return out
}

// First returns the first element (in tree order) for which match is true,
// or nil.
func (e *Element) First(match func(*Element) bool) *Element {
	var hit *Element
	e.Walk(func(el *Element, _ int) bool {
		if hit != nil {
			return false
		}
		if match(el) {
			hit = el
			return false
		}
		return true
	})
	return hit
}

// Node is one flattened, labelled element: what an agent needs to read a
// screen and tap by label or position.
type Node struct {
	Type    string  `json:"type"`
	Label   string  `json:"label,omitempty"`
	Value   string  `json:"value,omitempty"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	Enabled bool    `json:"enabled"`
	Depth   int     `json:"depth"`
}

// FlattenOptions selects which elements Flatten keeps.
type FlattenOptions struct {
	// All keeps unlabelled elements too (containers, icon-only buttons).
	All bool
	// Contains keeps only elements whose label or value contains this text
	// (case-insensitive).
	Contains string
	// Limit caps the number of nodes returned (0 = no cap).
	Limit int
}

// Flatten lists the tree's elements in order. By default it keeps only
// elements that carry a label or value and have a non-zero frame, and drops a
// single-character text child that only spells out its parent's label (AUM
// renders MUTE as the texts M, U, T, E). It reports whether Limit cut the list.
func (e *Element) Flatten(o FlattenOptions) ([]Node, bool) {
	needle := strings.ToLower(o.Contains)
	var out []Node
	truncated := false
	var walk func(el, parent *Element, depth int)
	walk = func(el, parent *Element, depth int) {
		if truncated {
			return
		}
		if keep(el, parent, o.All, needle) {
			if o.Limit > 0 && len(out) >= o.Limit {
				truncated = true
				return
			}
			out = append(out, Node{
				Type: el.Type, Label: el.Label, Value: valueIfDistinct(el),
				X: el.Rect.X, Y: el.Rect.Y, Width: el.Rect.Width, Height: el.Rect.Height,
				Enabled: el.Enabled, Depth: depth,
			})
		}
		for _, c := range el.Children {
			walk(c, el, depth+1)
		}
	}
	walk(e, nil, 0)
	return out, truncated
}

func keep(el, parent *Element, all bool, needle string) bool {
	if needle != "" {
		hay := strings.ToLower(el.Label + "\x00" + el.Value)
		return strings.Contains(hay, needle)
	}
	if all {
		return true
	}
	if el.Rect.Empty() || (el.Label == "" && el.Value == "") {
		return false
	}
	// A spelled-out character of the parent's label (MUTE -> M) adds nothing.
	if parent != nil && el.Type == "StaticText" && len([]rune(el.Label)) == 1 &&
		strings.Contains(parent.Label, el.Label) {
		return false
	}
	return true
}

func valueIfDistinct(el *Element) string {
	if el.Value == el.Label {
		return ""
	}
	return el.Value
}

// FormatNodes renders nodes one per line, indented by depth when indent is
// set: `Button "MIDI 1" (79,764 156x50)`.
func FormatNodes(nodes []Node, indent bool) string {
	var b strings.Builder
	for _, n := range nodes {
		if indent {
			b.WriteString(strings.Repeat("  ", n.Depth))
		}
		b.WriteString(n.Type)
		if n.Label != "" {
			b.WriteString(" " + strconv.Quote(n.Label))
		}
		if n.Value != "" {
			b.WriteString(" value=" + strconv.Quote(n.Value))
		}
		fmt.Fprintf(&b, " (%g,%g %gx%g)", n.X, n.Y, n.Width, n.Height)
		if !n.Enabled {
			b.WriteString(" disabled")
		}
		b.WriteByte('\n')
	}
	return b.String()
}
