package ipadtest

import (
	"encoding/json"
	"math"
	"sync"
)

// WideMixer is an AUM whose mixer holds more strips than fit in its window,
// as AUM 1.4.8 drew a mix of nine strips on the rig's iPad:
//
//   - The tree holds only the strips that reach into the window, and the add
//     tile's column only while that does (AUM.OnScreenOnly). The mixer's
//     horizontal scroll bar says how far it is scrolled, in percent.
//   - A sideways drag across the mixer scrolls it by the drag's length,
//     within its ends.
//   - The menu icon top right opens the main menu, which names the session;
//     its close icon on the same spot closes it.
//   - A tap on a strip's source opens its FILE PLAYER when the strip has
//     one; the next tap, outside it, closes it.
//   - A strip's source node can be drawn as failed (FailNode): its red ring
//     is in the screenshot wherever the mixer is scrolled.
type WideMixer struct {
	mu      sync.Mutex
	w       *WDA
	a       AUM
	session string
	menu    bool
	players map[string][][2]string
	player  string // the strip whose FILE PLAYER is open
	drags   []Gesture
	opened  []string
	failed  map[string]bool
}

// NewWideMixer attaches a WideMixer to w: the session named session is open,
// its mixer of strips scrolled to its first strip, and each strip players
// names has a File Player whose panel shows those rows.
func NewWideMixer(w *WDA, session string, strips []Strip, players map[string][][2]string) *WideMixer {
	m := &WideMixer{
		w:       w,
		a:       AUM{Tempo: "120", Position: "001:1  00:00.0", Strips: strips, OnScreenOnly: true},
		session: session,
		players: players,
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.SourceFunc = func(_, _ int) json.RawMessage { return m.source() }
	w.OnGesture = m.gesture
	return m
}

// Scroll is how far the mixer is scrolled, in points.
func (m *WideMixer) Scroll() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.a.Scroll
}

// SetScroll scrolls the mixer to x points (within its ends).
func (m *WideMixer) SetScroll(x float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.a.Scroll = math.Max(0, math.Min(MaxScroll(len(m.a.Strips)), x))
}

// Drags are the drags AUM received, in order.
func (m *WideMixer) Drags() []Gesture {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Gesture(nil), m.drags...)
}

// FailNode draws the source node of the strip titled title the way AUM
// draws a node whose plugin failed: a ring of red in the screenshot, where
// the strip is scrolled to.
func (m *WideMixer) FailNode(title string) {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failed == nil {
		m.failed = map[string]bool{}
	}
	m.failed[title] = true
	m.paint()
}

// paint puts the failed source nodes' rings where their strips are now.
// m.mu and the WDA's lock are held.
func (m *WideMixer) paint() {
	if len(m.failed) == 0 {
		return
	}
	var rings [][4]float64
	for i, s := range m.a.Strips {
		if m.failed[s.Title] {
			// A source node is 98 points square, 101 points into its strip.
			rings = append(rings, [4]float64{StripX(i) - m.a.Scroll + 101, 88, 98, 98})
		}
	}
	m.w.RedRings = rings
}

// Opened are the strips whose FILE PLAYER was opened, in order.
func (m *WideMixer) Opened() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.opened...)
}

func (m *WideMixer) source() json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paint()
	a := m.a
	if m.menu {
		a.Session = m.session
	}
	if m.player != "" {
		a.Overlays = []Overlay{{Kind: "popover", Title: "FILE PLAYER", Rows: m.players[m.player]}}
	}
	return a.Source()
}

func (m *WideMixer) gesture(g Gesture) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case g.Kind == "drag":
		m.drags = append(m.drags, g)
		if !m.menu && m.player == "" && g.Y > 60 && math.Abs(g.DX) > math.Abs(g.DY) {
			m.a.Scroll = math.Max(0, math.Min(MaxScroll(len(m.a.Strips)), m.a.Scroll-g.DX))
			m.paint()
		}
	case g.Kind != "tap":
	case m.player != "":
		m.player = ""
	case g.X >= ScreenWidth-60 && g.Y <= 60:
		m.menu = !m.menu
	case m.menu:
	default:
		// A source node is 98 points square, 101 points into its strip.
		for i, s := range m.a.Strips {
			x := StripX(i) - m.a.Scroll + 101
			if g.X >= x && g.X <= x+98 && g.Y >= 88 && g.Y <= 186 {
				if _, ok := m.players[s.Title]; ok {
					m.player = s.Title
					m.opened = append(m.opened, s.Title)
				}
				return
			}
		}
	}
}
