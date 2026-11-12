package ipadtest

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SessionsAUM is an AUM whose main menu, FILES and SAVE answer what is
// tapped: a stand-in for saving a session and reading it back from the list.
// It keeps one folder of sessions with the time each was saved.
//
// Its SAVE SESSION button behaves as AUM 1.4.8's does. A name the folder does
// not hold is written at the first press. For a name it holds the button
// reads "OVERWRITE?" for Window and writes only when it is pressed again in
// that time; after it the button reads "SAVE SESSION" again, nothing was
// written, and the next press is a first one. Return in the name field is a
// press of the button.
type SessionsAUM struct {
	mu sync.Mutex
	w  *WDA

	folder   string
	open     string // the open session, which the main menu names
	sessions []savedSession
	window   time.Duration
	asked    time.Time // when the button last turned to OVERWRITE?
	screen   string    // mixer, menu, files, sessions, folder or save
	last     string    // the FILES view it reopens on
	first    int       // the folder list's first row on screen
	writes   int
	clock    int
}

type savedSession struct{ name, saved string }

// filesFrame is the FILES side panel, wider than the menu.
var filesFrame = [4]float64{810, 0, 400, 834}

// listRows is how many rows of a FILES list are on screen at once; the rest
// is not in the tree, as with any table view.
const listRows = 15

// NewSessionsAUM attaches a SessionsAUM to w: the session open is loaded,
// SAVE saves into folder, and the folder holds sessions (name and saved time
// pairs) in the order the list shows them. The mixer is on screen.
func NewSessionsAUM(w *WDA, folder, open string, sessions ...[2]string) *SessionsAUM {
	m := &SessionsAUM{w: w, folder: folder, open: open, window: time.Second, screen: "mixer", last: "files"}
	for _, s := range sessions {
		m.sessions = append(m.sessions, savedSession{s[0], s[1]})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.SourceFunc = func(_, _ int) json.RawMessage { return m.source() }
	w.OnGesture = m.gesture
	return m
}

// SetWindow sets how long the button reads OVERWRITE? (a second by default).
func (m *SessionsAUM) SetWindow(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window = d
}

// Saved is the time the folder's session name was last saved, and whether
// the folder holds it.
func (m *SessionsAUM) Saved(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := m.index(name); i >= 0 {
		return m.sessions[i].saved, true
	}
	return "", false
}

// Writes is how often a session file was written.
func (m *SessionsAUM) Writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes
}

// Open is the session the main menu names.
func (m *SessionsAUM) Open() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open
}

func (m *SessionsAUM) index(name string) int {
	for i, s := range m.sessions {
		if s.name == name {
			return i
		}
	}
	return -1
}

func (m *SessionsAUM) asking() bool {
	return !m.asked.IsZero() && time.Since(m.asked) <= m.window
}

// source is the screen. The fake's lock is held.
func (m *SessionsAUM) source() json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := MixerAUM()
	panel := func(title string, o Overlay) {
		o.Kind, o.Title, o.Frame = "panel", title, &filesFrame
		a.Overlays = []Overlay{o}
	}
	switch m.screen {
	case "menu":
		a.Session = m.open
	case "files":
		panel("FILES", Overlay{Rows: [][2]string{{"Sessions", ""}, {"Recordings", ""}}})
	case "sessions":
		panel("Sessions", Overlay{Cells: [][2]string{{m.folder, ""}}})
	case "folder":
		var cells [][2]string
		for i := m.first; i < len(m.sessions) && i < m.first+listRows; i++ {
			cells = append(cells, [2]string{m.sessions[i].name, m.sessions[i].saved})
		}
		panel(m.folder, Overlay{Cells: cells})
	case "save":
		name := ""
		if len(m.w.Fields) > 0 {
			name = m.w.Fields[0]
		}
		button := "SAVE SESSION"
		if m.asking() {
			button = "OVERWRITE?"
		}
		a.Save = &SavePanel{Name: name, Folder: "/" + m.folder, Button: button}
	}
	return a.Source()
}

// gesture answers a gesture. The fake's lock is held.
func (m *SessionsAUM) gesture(g Gesture) {
	m.mu.Lock()
	defer m.mu.Unlock()
	const W = 1210.0
	switch {
	case g.Kind == "keys" && m.screen == "save":
		m.press()
		return
	case g.Kind == "drag" && m.screen == "folder":
		// The list follows the finger by whole rows and stops at its ends.
		m.first -= int(g.DY / 50)
		m.first = max(0, min(m.first, len(m.sessions)-listRows))
		return
	case g.Kind != "tap":
		return
	}
	x, y := g.X, g.Y
	corner := x >= W-60 && y <= 60 // ☰, and a panel's close icon on its spot
	switch m.screen {
	case "mixer":
		if corner {
			m.screen = "menu"
		}
	case "menu":
		switch {
		case corner:
			m.screen = "mixer"
		case inRect(x, y, [4]float64{965, 105, 64, 64}): // FILES
			m.screen = m.last
		case inRect(x, y, [4]float64{965 + 83, 105, 64, 64}): // SAVE
			m.show("save")
		}
	case "save":
		switch {
		case corner:
			m.show("mixer")
		case x < 1010 && y <= 60:
			m.show("menu")
		case inRect(x, y, SaveButton):
			m.press()
		}
	default: // a FILES view
		row := -1
		if y >= 70 {
			row = int((y - 70) / 50)
		}
		up := map[string]string{"files": "menu", "sessions": "files", "folder": "sessions"}
		switch {
		case corner:
			m.screen = "mixer"
		case x < filesFrame[0]+60 && y <= 60:
			m.screen = up[m.screen]
		case m.screen == "files" && row == 0:
			m.screen = "sessions"
		case m.screen == "sessions" && row == 0:
			m.screen, m.first = "folder", 0
		}
		if m.screen != "mixer" && m.screen != "menu" {
			m.last = m.screen
		}
	}
}

// show switches between the SAVE panel and another screen: the panel's name
// field holds the open session's name when it opens and is gone with it.
func (m *SessionsAUM) show(screen string) {
	m.screen, m.asked = screen, time.Time{}
	if screen == "save" {
		m.w.Fields = []string{m.open}
	} else {
		m.w.Fields = nil
	}
}

// press is a press of SAVE SESSION (or Return in the name field).
func (m *SessionsAUM) press() {
	if len(m.w.Fields) == 0 {
		return
	}
	name := m.w.Fields[0]
	i := m.index(name)
	switch {
	case i < 0:
		m.sessions = append([]savedSession{{name, m.stamp()}}, m.sessions...)
	case m.asking():
		m.sessions[i].saved = m.stamp()
	default:
		m.asked = time.Now()
		return
	}
	m.asked = time.Time{}
	m.open = name
	m.writes++
}

// stamp is the saved time of the next write, as AUM prints it in a list.
func (m *SessionsAUM) stamp() string {
	m.clock++
	return fmt.Sprintf("12. Nov 2026 at 22:%02d:%02d", m.clock/60, m.clock%60)
}

func inRect(x, y float64, r [4]float64) bool {
	return x >= r[0] && x < r[0]+r[2] && y >= r[1] && y < r[1]+r[3]
}
