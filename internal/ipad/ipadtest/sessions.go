package ipadtest

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

// SessionsAUM is an AUM whose main menu, FILES and SAVE answer what is
// tapped: a stand-in for saving a session, reading it back from the list and
// loading one from it. It keeps the sessions of the root of Sessions and of
// the folders in it, with the time each was saved. What it does was read off
// AUM 1.4.8 on an iPad:
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
//     length. A panel that is left open stays scrolled where it was.
//   - A folder's row has a chevron and opens the folder. A tap on any other
//     row of Sessions loads that session at once: FILES closes, the main menu
//     names the session, and SAVE saves into the folder it was loaded from.
//   - FILES → Recordings is built the same way: every folder is titled
//     "RECORDINGS" and named in the header row, the back arrow goes up one
//     folder, a session's folder of takes has a chevron and a take's row has
//     none. A tap on a take opens its PREVIEW. Its root is headed "/" only
//     when the FILES root's Recordings row opened it: reached by the back
//     arrow, or when FILES reopens on it, the header names the directory the
//     recordings are kept in (RecordingsRoot), as a folder's header would.
//   - A long-press on a session's row opens iOS's share sheet for that file
//     (ShareSheet). Its row of actions ends in an entry that unfolds the rest
//     as a list, Rename first. Rename closes the sheet and opens the RENAME
//     dialog: a text field holding the session's name, Cancel and OK. OK
//     renames the file at once and the row stays where the list had it. A
//     name another session of the folder has is answered with the alert
//     "Sorry" (NameExists), which stays until its OK, and nothing is renamed.
//     A touch beside the sheet closes it; any other cell of it does
//     something with the file, which Touched reports.
type SessionsAUM struct {
	mu sync.Mutex
	w  *WDA

	folder   string                    // the folder SAVE saves into; "" is the root of Sessions
	open     string                    // the open session, which the main menu names
	lists    map[string][]savedSession // each folder's sessions, in its list's order; "" is the root
	dirs     []string                  // the root's folders, listed below its sessions
	recs     map[string][]FileCell     // each folder of Recordings' rows, in its list's order; "" is the root
	window   time.Duration
	asked    time.Time // when the button last turned to OVERWRITE?
	screen   string    // mixer, menu, files (the FILES root), sessions, recordings or save
	last     string    // the FILES view it reopens on: files, sessions or recordings
	dir      string    // the folder the sessions or recordings view shows: "" is the root
	first    int       // that list's first whole row on screen
	slash    bool      // the root of Recordings is headed "/": it was entered from the FILES root
	sheet    string    // the session whose share sheet a long-press opened
	more     bool      // that sheet's row of actions was unfolded
	slips    bool      // a long-press opens the sheet of the row below the one it meets
	bare     bool      // the sheet has no caption
	renaming string    // the session whose RENAME dialog is open
	refuse   bool      // OK in the RENAME dialog renames nothing
	sorry    string    // the text of the alert "Sorry" on screen
	held     []string
	renamed  []string
	closing  bool // the SAVE panel is about to close after a write
	runOn    int  // rows a list runs on after a drag's finger lifts
	cutRows  bool // a scrolled list rests between two rows
	writes   int
	restored bool
	loaded   []string
	clock    int
}

type savedSession struct{ name, saved string }

// RecordingsRoot heads the root of FILES → Recordings when it was not entered
// from the FILES root.
const RecordingsRoot = "AUM Recordings"

// NewSessionsAUM attaches a SessionsAUM to w: the session open is loaded,
// SAVE saves into folder ("" for the root of Sessions), and the folder holds
// sessions (name and saved time pairs) in the order the list shows them. The
// root above a folder lists a session of its own that has the folder's name,
// then the folder. The mixer is on screen.
func NewSessionsAUM(w *WDA, folder, open string, sessions ...[2]string) *SessionsAUM {
	m := &SessionsAUM{w: w, folder: folder, open: open, window: time.Second, screen: "mixer", last: "files", lists: map[string][]savedSession{}, recs: map[string][]FileCell{}}
	m.add(folder, sessions)
	if folder != "" {
		m.lists[""] = []savedSession{{folder, "1. Jan 2025 at 10:00:00"}}
		m.dirs = []string{folder}
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

// SetCutRows makes a scrolled list rest between two rows, as AUM's does after
// a drag: the list's top edge cuts a row, of which only the second line
// shows, and its bottom edge cuts another.
func (m *SessionsAUM) SetCutRows(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cutRows = on
}

// AddFolder adds a folder to the root of Sessions, below the folders that
// are there, holding sessions.
func (m *SessionsAUM) AddFolder(name string, sessions ...[2]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs = append(m.dirs, name)
	m.add(name, sessions)
}

// AddRoot adds sessions to the root of Sessions, below the ones that are
// there and above its folders.
func (m *SessionsAUM) AddRoot(sessions ...[2]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.add("", sessions)
}

func (m *SessionsAUM) add(folder string, sessions [][2]string) {
	for _, s := range sessions {
		m.lists[folder] = append(m.lists[folder], savedSession{s[0], s[1]})
	}
}

// AddRecordings adds a folder to the root of FILES → Recordings, below the
// rows that are there, holding takes (name and detail pairs); with folder ""
// the takes go into the root itself.
func (m *SessionsAUM) AddRecordings(folder string, takes ...[2]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if folder != "" {
		m.recs[""] = append(m.recs[""], FileCell{Name: folder, Detail: "13. Nov 2026 at 00:10:00", Folder: true})
	}
	for _, t := range takes {
		m.recs[folder] = append(m.recs[folder], FileCell{Name: t[0], Detail: t[1]})
	}
}

// RefuseRenames makes OK in the RENAME dialog close it without renaming, as
// a rename AUM does not take.
func (m *SessionsAUM) RefuseRenames() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuse = true
}

// HoldSlips makes a long-press open the share sheet of the row below the one
// it meets, as when the list moves under the finger; bare leaves the sheet
// without its caption.
func (m *SessionsAUM) HoldSlips(bare bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slips, m.bare = true, bare
}

// SheetOpen says whether a share sheet or the RENAME dialog is on screen.
func (m *SessionsAUM) SheetOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sheet != "" || m.renaming != "" || m.sorry != ""
}

// ShowRecordings makes folder dir of Recordings ("" for the root) the view
// FILES last showed: the one it reopens on.
func (m *SessionsAUM) ShowRecordings(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last, m.dir, m.first = "recordings", dir, 0
}

// LeaveRecordingsOpen leaves the FILES panel open on folder dir of Recordings
// ("" for the root), scrolled down to the row first.
func (m *SessionsAUM) LeaveRecordingsOpen(dir string, first int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screen, m.last, m.dir, m.first = "recordings", "recordings", dir, first
}

// ShowFolder opens FILES' last view in the folder SAVE saves into, as after
// a session was loaded from it, scrolled to the row first.
func (m *SessionsAUM) ShowFolder(first int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last, m.dir, m.first = "sessions", m.folder, first
}

// Show makes folder dir of Sessions ("" for the root) the view FILES last
// showed: the one it reopens on.
func (m *SessionsAUM) Show(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last, m.dir, m.first = "sessions", dir, 0
}

// LeaveOpen leaves the FILES panel open on folder dir of Sessions ("" for
// the root), scrolled down to the row first.
func (m *SessionsAUM) LeaveOpen(dir string, first int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screen, m.last, m.dir, m.first = "sessions", "sessions", dir, first
}

// Saved is the time the folder's session name was last saved, and whether
// the folder holds it.
func (m *SessionsAUM) Saved(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := m.index(name); i >= 0 {
		return m.lists[m.folder][i].saved, true
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

// Held lists the rows of Sessions that were long-pressed, in order, by their
// path ("/Songs/name"), which says so when the list's edge cut that row.
func (m *SessionsAUM) Held() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.held...)
}

// Renamed lists the renames the RENAME dialog made, in order
// ("/Songs/name -> new name").
func (m *SessionsAUM) Renamed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.renamed...)
}

// Sessions lists the names of folder dir of Sessions ("" for the root), in
// its list's order.
func (m *SessionsAUM) Sessions(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.lists[dir]))
	for i, s := range m.lists[dir] {
		out[i] = s.name
	}
	return out
}

// Touched lists what a tap did that a save must never do, in order: a
// session row tapped in the list, by its path ("/Songs/name"; AUM loads it
// at once), which says so when the list's edge cut that row, a take's row
// tapped in Recordings ("PREVIEW of /folder/take"), and Restore Previous
// State.
func (m *SessionsAUM) Touched() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.loaded...)
	if m.restored {
		out = append(out, "Restore Previous State")
	}
	return out
}

// index is where the folder SAVE saves into lists the session name.
func (m *SessionsAUM) index(name string) int {
	for i, s := range m.lists[m.folder] {
		if s.name == name {
			return i
		}
	}
	return -1
}

func (m *SessionsAUM) asking() bool {
	return !m.asked.IsZero() && time.Since(m.asked) <= m.window
}

// rows is the list of the folder the view on screen shows: of Sessions its
// sessions and, in the root, the folders below them.
func (m *SessionsAUM) rows() []FileCell {
	if m.screen == "recordings" {
		return m.recs[m.dir]
	}
	var out []FileCell
	for _, s := range m.lists[m.dir] {
		out = append(out, FileCell{Name: s.name, Detail: s.saved})
	}
	if m.dir == "" {
		for _, d := range m.dirs {
			out = append(out, FileCell{Name: d, Detail: "12. Nov 2026 at 20:00:00", Folder: true})
		}
	}
	return out
}

// whole is how many rows a scrolled list shows whole.
func (m *SessionsAUM) whole() int {
	if m.cutRows {
		return FilesRowsCut
	}
	return FilesRowsScrolled
}

// rowAt is the row of the list at height y of the screen (-1: above the
// rows), and whether the list's edge cuts that row.
func (m *SessionsAUM) rowAt(y float64) (int, bool) {
	switch {
	case m.first == 0:
		if y < FilesRowY(0, false) {
			return -1, false
		}
		return int((y - FilesRowY(0, false)) / 50), false
	case m.cutRows:
		i := m.first - 1 + int((y-FilesCutY)/50)
		return i, i < m.first || i >= m.first+FilesRowsCut
	}
	return m.first + int((y-FilesRowY(0, true))/50), false
}

// path is the row name of the folder on screen, as Touched and Held print
// it; cut says the list's edge cut that row.
func (m *SessionsAUM) path(name string, cut bool) string {
	path := "/" + name
	if m.dir != "" {
		path = "/" + m.dir + path
	}
	if cut {
		path += ", a row cut by the list's edge"
	}
	return path
}

// load is a tap on a session's row: AUM loads the session at once and closes
// FILES.
func (m *SessionsAUM) load(name string, cut bool) {
	m.loaded = append(m.loaded, m.path(name, cut))
	m.open, m.folder = name, m.dir
	m.show("mixer")
}

// rename is OK in the RENAME dialog: the file is renamed at once, and the
// row keeps its place in the list.
func (m *SessionsAUM) rename() {
	to := ""
	if len(m.w.Fields) > 0 {
		to = m.w.Fields[0]
	}
	if to != m.renaming && slices.ContainsFunc(m.lists[m.dir], func(s savedSession) bool { return s.name == to }) {
		m.renaming, m.w.Fields, m.sorry = "", nil, NameExists
		return
	}
	for i, s := range m.lists[m.dir] {
		if s.name == m.renaming && to != "" && !m.refuse {
			m.lists[m.dir][i].name = to
			m.renamed = append(m.renamed, m.path(s.name, false)+" -> "+to)
			if m.dir == m.folder && m.open == s.name {
				m.open = to
			}
		}
	}
	m.renaming, m.w.Fields = "", nil
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
	case "sessions", "recordings":
		rows := m.rows()
		header := "/"
		if m.dir != "" {
			header = m.dir
		}
		title := "SESSIONS"
		if m.screen == "recordings" {
			title = "RECORDINGS"
			if m.dir == "" && !m.slash {
				header = RecordingsRoot
			}
		}
		first := min(m.first, len(rows))
		a.Files = &FilesPanel{Title: title, Folder: header, Cells: rows[first:], Scrolled: first > 0}
		if m.cutRows && first > 0 {
			a.Files.Cells, a.Files.Cut = rows[first-1:], true
		}
		switch {
		case m.sheet != "":
			a.Share = &ShareSheet{Name: m.sheet, NoCaption: m.bare, More: m.more}
		case m.renaming != "":
			name := ""
			if len(m.w.Fields) > 0 {
				name = m.w.Fields[0]
			}
			a.Rename = &RenameDialog{Name: name}
		}
		a.Sorry = m.sorry
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
	case m.sorry != "":
		// The alert takes every touch; its OK closes it.
		if g.Kind == "tap" && inRect(g.X, g.Y, SorryOK) {
			m.sorry = ""
		}
		return
	case m.sheet != "":
		// The row of actions unfolds and folds; Rename opens the dialog; a
		// touch beside the sheet closes it; any other cell acts on the file.
		name := m.sheet
		cell, inside := ShareSheet{Name: name, More: m.more}.Hit(g.X, g.Y)
		switch {
		case g.Kind != "tap" || (inside && cell == ""):
		case !inside:
			m.sheet, m.more = "", false
		case cell == ShareMore || cell == ShareLess:
			m.more = cell == ShareMore
		case cell == "Rename":
			m.sheet, m.more = "", false
			m.renaming, m.w.Fields = name, []string{name}
		default:
			m.sheet, m.more = "", false
			m.loaded = append(m.loaded, cell+" of "+m.path(name, false))
		}
		return
	case m.renaming != "":
		switch {
		case g.Kind != "tap":
		case inRect(g.X, g.Y, RenameOK):
			m.rename()
		case inRect(g.X, g.Y, RenameCancel):
			m.renaming, m.w.Fields = "", nil
		}
		return
	case g.Kind == "hold" && m.screen == "sessions":
		if g.X >= filesX && g.Y >= 60 && g.Y < 774 {
			rows := m.rows()
			if i, cut := m.rowAt(g.Y); i >= 0 && i < len(rows) {
				m.held = append(m.held, m.path(rows[i].Name, cut))
				if m.slips && i+1 < len(rows) {
					i++
				}
				if !rows[i].Folder {
					m.sheet, m.more = rows[i].Name, false
				}
			}
		}
		return
	case g.Kind == "drag" && (m.screen == "sessions" || m.screen == "recordings"):
		// The list follows the finger by whole rows, a little short of the
		// drag, and stops at its ends.
		n := len(m.rows())
		last := 0
		if n > FilesRowsTop {
			last = max(1, n-m.whole())
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
			m.screen, m.first, m.slash = m.last, 0, false
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
		case x >= filesX && y >= 180 && y < 231: // Recordings: its root folder
			m.screen, m.last, m.dir, m.first, m.slash = "recordings", "recordings", "", 0, true
		case x >= filesX && y >= 265 && y < 316:
			m.restored = true
		}
	case "sessions", "recordings":
		switch {
		case corner:
			m.screen = "mixer"
		case x < filesX+60 && y <= 60: // up one folder, then to the FILES root
			if m.dir != "" {
				m.dir, m.first, m.slash = "", 0, false
			} else {
				m.screen, m.last = "files", "files"
			}
		case x >= filesX && y >= 60 && y < 774:
			i, cut := m.rowAt(y)
			rows := m.rows()
			switch {
			case i < 0 || i >= len(rows):
			case rows[i].Folder:
				m.dir, m.first = rows[i].Name, 0
			case m.screen == "recordings":
				m.loaded = append(m.loaded, "PREVIEW of "+m.path(rows[i].Name, cut))
			default:
				m.load(rows[i].Name, cut)
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
		m.lists[m.folder] = append(m.lists[m.folder][:i], m.lists[m.folder][i+1:]...)
	default:
		m.asked = time.Now()
		return
	}
	// The list is in the order of last use: what was saved is first.
	m.lists[m.folder] = append([]savedSession{{name, m.stamp()}}, m.lists[m.folder]...)
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
