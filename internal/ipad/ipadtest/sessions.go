package ipadtest

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"
)

// SessionsAUM is an AUM whose main menu, FILES and SAVE answer what is
// tapped: a stand-in for saving a session and reading it back from the list.
// It keeps the sessions of the folder SAVE saves into, with the time each was
// saved. What it does was read off AUM 1.4.8 on an iPad:
//
//   - SAVE SESSION writes a name the folder does not hold at the first press.
//     For a name it holds the button reads "OVERWRITE?" for Window and writes
//     only when it is pressed again in that time; after it the button reads
//     "SAVE SESSION" again, nothing was written, and the next press is a first
//     one. Return in the name field is a press of the button.
//   - After a write the SAVE panel closes by itself, a moment later: the next
//     read still has it. A press that wrote nothing leaves it open.
//   - FILES reopens in the view it last showed, at the top of its list. Every
//     folder of Sessions is titled "SESSIONS" and named in a header row ("/"
//     for the root); the back arrow goes up one folder, and the FILES root's
//     Sessions row opens the root folder. (From the root folder the back
//     arrow takes the stand-in to the FILES root; that step was not tried on
//     the iPad and nothing here relies on it.)
//   - A list has only the rows on screen, and follows a drag by nearly its
//     length.
type SessionsAUM struct {
	mu sync.Mutex
	w  *WDA

	folder   string // the folder SAVE saves into; "" is the root of Sessions
	open     string // the open session, which the main menu names
	sessions []savedSession
	window   time.Duration
	asked    time.Time // when the button last turned to OVERWRITE?
	screen   string    // mixer, menu, files (the FILES root), sessions or save
	last     string    // the FILES view it reopens on: files or sessions
	dir      string    // the folder the sessions view shows: "" or folder
	first    int       // that list's first row on screen
	closing  bool      // the SAVE panel is about to close after a write
	runOn    int       // rows a list runs on after a drag's finger lifts
	writes   int
	restored bool
	loaded   []string
	clock    int
}

type savedSession struct{ name, saved string }

// NewSessionsAUM attaches a SessionsAUM to w: the session open is loaded,
// SAVE saves into folder ("" for the root of Sessions), and the folder holds
// sessions (name and saved time pairs) in the order the list shows them. The
// mixer is on screen.
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

// SetRunOn makes a list run on by rows after every drag, as AUM's does after
// a fast one: with enough of them a page shares no row with the one before.
func (m *SessionsAUM) SetRunOn(rows int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runOn = rows
}

// ShowFolder opens FILES' last view in the folder SAVE saves into, as after
// a session was loaded from it, scrolled to the row first.
func (m *SessionsAUM) ShowFolder(first int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last, m.dir, m.first = "sessions", m.folder, first
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

// Touched lists what a tap did that a save must never do: a session row
// tapped in the list (AUM loads it at once) and Restore Previous State.
func (m *SessionsAUM) Touched() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.loaded...)
	if m.restored {
		out = append(out, "Restore Previous State")
	}
	return out
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

// rows is the list of the folder the sessions view shows.
func (m *SessionsAUM) rows() []FileCell {
	var out []FileCell
	if m.dir != m.folder {
		// The root, above the folder SAVE saves into: a session of the
		// root's own that has the folder's name, then the folder.
		return []FileCell{{Name: m.folder, Detail: "1. Jan 2025 at 10:00:00"}, {Name: m.folder, Detail: "12. Nov 2026 at 20:00:00", Folder: true}}
	}
	for _, s := range m.sessions {
		out = append(out, FileCell{Name: s.name, Detail: s.saved})
	}
	return out
}

// source is the screen. The fake's lock is held.
func (m *SessionsAUM) source() json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := MixerAUM()
	switch m.screen {
	case "menu":
		a.Session = m.open
	case "files":
		a.Files = &FilesPanel{Title: "FILES", Cells: []FileCell{
			{Name: "Sessions", Folder: true}, {Name: "Recordings", Folder: true},
			{Name: "Restore Previous State", Detail: "12. Nov 2026 at 20:30:00"}}}
	case "sessions":
		rows := m.rows()
		header := "/"
		if m.dir != "" {
			header = m.dir
		}
		a.Files = &FilesPanel{Title: "SESSIONS", Folder: header, Cells: rows[min(m.first, len(rows)):], Scrolled: m.first > 0}
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
		if m.closing {
			// This read still had the panel; it is gone for the next.
			m.show("mixer")
		}
	}
	return a.Source()
}

// gesture answers a gesture. The fake's lock is held.
func (m *SessionsAUM) gesture(g Gesture) {
	m.mu.Lock()
	defer m.mu.Unlock()
	const W, filesX = 1210.0, 810.0
	if m.closing {
		// The SAVE panel is on its way out: the touch meets the mixer.
		m.show("mixer")
	}
	switch {
	case g.Kind == "keys" && m.screen == "save":
		m.press()
		return
	case g.Kind == "drag" && m.screen == "sessions":
		// The list follows the finger by whole rows, a little short of the
		// drag, and stops at its ends.
		n := len(m.rows())
		last := 0
		if n > FilesRowsTop {
			last = max(1, n-FilesRowsScrolled)
		}
		by := -int(math.Round(g.DY * 0.965 / 50))
		if by > 0 {
			by += m.runOn
		}
		m.first = max(0, min(m.first+by, last))
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
			m.screen, m.first = m.last, 0
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
	case "files":
		switch {
		case corner:
			m.screen = "mixer"
		case x < filesX+60 && y <= 60:
			m.screen = "menu"
		case x >= filesX && y >= 95 && y < 146: // Sessions: its root folder
			m.screen, m.last, m.dir, m.first = "sessions", "sessions", "", 0
		case x >= filesX && y >= 265 && y < 316:
			m.restored = true
		}
	case "sessions":
		switch {
		case corner:
			m.screen = "mixer"
		case x < filesX+60 && y <= 60: // up one folder, then to the FILES root
			if m.dir != "" {
				m.dir, m.first = "", 0
			} else {
				m.screen, m.last = "files", "files"
			}
		case x >= filesX && y >= FilesRowY(0, m.first > 0) && y < 774:
			i := m.first + int((y-FilesRowY(0, m.first > 0))/50)
			rows := m.rows()
			switch {
			case i >= len(rows):
			case rows[i].Folder:
				m.dir, m.first = rows[i].Name, 0
			default:
				m.loaded = append(m.loaded, rows[i].Name)
			}
		}
	}
}

// show switches between the SAVE panel and another screen: the panel's name
// field holds the open session's name when it opens and is gone with it.
func (m *SessionsAUM) show(screen string) {
	m.screen, m.asked, m.closing = screen, time.Time{}, false
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
	case m.asking():
		m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
	default:
		m.asked = time.Now()
		return
	}
	// The list is in the order of last use: what was saved is first.
	m.sessions = append([]savedSession{{name, m.stamp()}}, m.sessions...)
	m.asked = time.Time{}
	m.open = name
	m.writes++
	m.closing = true
}

// stamp is the saved time of the next write, as AUM prints it in a list.
func (m *SessionsAUM) stamp() string {
	m.clock++
	return fmt.Sprintf("12. Nov 2026 at 22:%02d:%02d", m.clock/60, m.clock%60)
}

func inRect(x, y float64, r [4]float64) bool {
	return x >= r[0] && x < r[0]+r[2] && y >= r[1] && y < r[1]+r[3]
}
