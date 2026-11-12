package ipad

// This file reads the AUv3 Probe app's window: its "aum sessions" tab, where
// the app lists the files of AUM's folder with what its sync made of each, and
// what the "audio units" tab says of the app's other sync. The app tells the
// daemon nothing about a file it does not pull (it just never asks for it),
// so its screen is the only place that says why.
//
// The shapes follow the app's accessibility tree as WebDriverAgent serves it:
//
//	TabBar            Button "audio units" / "aum sessions", value "1" on the shown one
//	audio units tab   StaticText "syncing 2/184…" while the app sends its units
//	aum sessions tab  StaticText "aum sessions", Button "sync" (disabled while a
//	                  cycle runs), a status line ("synced · 166 files · 02:00",
//	                  "pulling 1…", "7 conflicts — pick a side below"), then the
//	                  folder browser
//	browser           a NavigationBar with a back button and the folder's name
//	                  below the root; folder rows (a button holding the name,
//	                  a file count and a chevron); file rows
//	file row          Button "open <name> in aum" holding the name, a line
//	                  "session · 156 KB · 12. Nov 2026 at 02:06" and the sync
//	                  badge; Button "actions for <name>"; for a conflict the
//	                  buttons "keep device" and "keep controller" follow as
//	                  the row's next siblings
//
// The browser lists a folder whole: a row scrolled out of sight is in the tree
// with its frame.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The probe app's labels this file goes by.
const (
	ProbeUnitsTab    = "audio units"
	ProbeSessionsTab = "aum sessions"
	// ProbeConflict is the badge of a file both sides changed since the app
	// last synced it; the app copies it neither way until a side is picked.
	ProbeConflict       = "conflict — both sides changed"
	ProbeKeepController = "keep controller"
	ProbeKeepDevice     = "keep device"
	// ProbeOffline is the sessions tab's status line while the app does not
	// reach the daemon.
	ProbeOffline = "controller offline — browsing device copy"
)

// ProbeFile is one file row of the probe app's session browser.
type ProbeFile struct {
	// Name is the file's name with its extension.
	Name string
	// Info is the row's second line, about the copy in AUM's folder: its
	// kind, size and date ("session · 156 KB · 12. Nov 2026 at 02:06").
	Info string
	// State is the row's sync badge: "" for a file in sync, else "pulling…",
	// "pushing…", "new from controller", ProbeConflict or the text of an
	// error.
	State string
	// Rect is the row's frame; it lies outside the window for a row scrolled
	// out of sight.
	Rect Rect
	// KeepController is the frame of the row's own "keep controller" button,
	// nil unless the row is a conflict.
	KeepController *Rect
}

// Conflict reports a file the app holds as a conflict.
func (f ProbeFile) Conflict() bool { return f.State == ProbeConflict }

// Size is the size the row gives for the copy in AUM's folder ("156 KB"),
// "" when the row has none.
func (f ProbeFile) Size() string {
	for _, part := range strings.Split(f.Info, " · ") {
		if probeSizeRE.MatchString(part) {
			return part
		}
	}
	return ""
}

// ProbeFolder is one folder row of the browser.
type ProbeFolder struct {
	Name string
	// Label is the row's accessibility label ("Songs, 27").
	Label string
	Rect  Rect
}

// ProbeScreen is what the probe app's window shows of its two syncs.
type ProbeScreen struct {
	// Tab is the tab shown, ProbeUnitsTab or ProbeSessionsTab ("" when the
	// window has no tab bar), and SessionsTab the frame of the tab bar's
	// button for the sessions tab.
	Tab         string
	SessionsTab *Rect
	// UnitsSyncing says the audio units tab shows a sync under way, of
	// UnitsDone of UnitsTotal units. The app sends every unit again whenever
	// it is started, and only while it is in front.
	UnitsSyncing          bool
	UnitsDone, UnitsTotal int
	// Status is the sessions tab's status line and Cycle whether a sync
	// cycle runs (the tab's sync button is disabled for as long).
	Status string
	Cycle  bool
	// Sync is the frame of the tab's sync button, nil when there is none.
	Sync *Rect
	// Browser says the tab shows the folder browser: AUM's folder is linked.
	Browser bool
	// Folder is the name of the folder the browser shows, "" at the root;
	// Back is the frame of the button that leaves it.
	Folder string
	Back   *Rect
	// Folders and Files are the rows of that folder.
	Folders []ProbeFolder
	Files   []ProbeFile
}

// File finds the row of the file name, nil when the folder shown has none.
func (p *ProbeScreen) File(name string) *ProbeFile {
	for i := range p.Files {
		if p.Files[i].Name == name {
			return &p.Files[i]
		}
	}
	return nil
}

// Offline reports that the app says it does not reach the daemon.
func (p *ProbeScreen) Offline() bool { return p.Status == ProbeOffline }

var (
	probeUnitsRE = regexp.MustCompile(`^syncing (\d+)/(\d+)…$`)
	probeOpenRE  = regexp.MustCompile(`^open (.+) in aum$`)
	probeSizeRE  = regexp.MustCompile(`^\d+(\.\d+)? (B|KB|MB)$`)
)

// ProbeByteLabel is how the probe app writes a file's size: whole bytes,
// whole KB up to 1024 of them, then MB with one decimal.
func ProbeByteLabel(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	kb := float64(bytes) / 1024
	if kb < 1024 {
		return fmt.Sprintf("%.0f KB", kb)
	}
	return fmt.Sprintf("%.1f MB", kb/1024)
}

// ParseProbeScreen reads the probe app's accessibility tree.
func ParseProbeScreen(root *Element) ProbeScreen {
	var p ProbeScreen
	if bar := root.First(func(e *Element) bool { return e.Type == "TabBar" }); bar != nil {
		for _, b := range bar.Children {
			if b.Type != "Button" {
				continue
			}
			if b.Value == "1" {
				p.Tab = strings.ToLower(b.Label)
			}
			if strings.EqualFold(b.Label, ProbeSessionsTab) {
				r := b.Rect
				p.SessionsTab = &r
			}
		}
	}
	root.Walk(func(e *Element, _ int) bool {
		switch e.Type {
		case "StaticText":
			if m := probeUnitsRE.FindStringSubmatch(e.Label); m != nil {
				p.UnitsSyncing = true
				p.UnitsDone, _ = strconv.Atoi(m[1])
				p.UnitsTotal, _ = strconv.Atoi(m[2])
			}
		case "NavigationBar":
			p.Browser = true
			for _, c := range e.Children {
				switch {
				case c.Type == "Button" && p.Back == nil:
					r := c.Rect
					p.Back = &r
				case c.Type == "StaticText" && p.Folder == "":
					p.Folder = c.Label
				}
			}
		}
		// The tab's header: its title, the buttons and the status line are
		// siblings.
		for i, c := range e.Children {
			if c.Type != "StaticText" || c.Label != ProbeSessionsTab {
				continue
			}
			for _, sib := range e.Children[i+1:] {
				switch {
				case sib.Type == "Button" && strings.EqualFold(sib.Label, "sync"):
					r := sib.Rect
					p.Sync, p.Cycle = &r, !sib.Enabled
				case sib.Type == "StaticText" && p.Status == "":
					p.Status = sib.Label
				}
			}
		}
		p.rows(e)
		return true
	})
	if len(p.Folders)+len(p.Files) > 0 {
		p.Browser = true
	}
	return p
}

// rows collects the folder and file rows among one element's children. A
// conflict's two buttons follow its row as siblings, up to the next row.
func (p *ProbeScreen) rows(parent *Element) {
	file := -1
	for _, c := range parent.Children {
		if c.Type != "Button" {
			continue
		}
		var texts []string
		for _, t := range c.Children {
			if t.Type == "StaticText" {
				texts = append(texts, t.Label)
			}
		}
		// A file row holds the name its label gives, the line about the copy,
		// and the badge when there is one. (The root's strip of arrivals has
		// buttons that end in "… tap to open in aum"; they hold another text.)
		if m := probeOpenRE.FindStringSubmatch(c.Label); m != nil && len(texts) > 0 && texts[0] == m[1] {
			f := ProbeFile{Name: m[1], Rect: c.Rect}
			if len(texts) > 1 {
				f.Info = texts[1]
			}
			if len(texts) > 2 {
				f.State = texts[2]
			}
			p.Files = append(p.Files, f)
			file = len(p.Files) - 1
			continue
		}
		if name, ok := probeFolderRow(c); ok {
			p.Folders = append(p.Folders, ProbeFolder{Name: name, Label: c.Label, Rect: c.Rect})
			file = -1
			continue
		}
		if c.Label == ProbeKeepController && file >= 0 && p.Files[file].KeepController == nil {
			r := c.Rect
			p.Files[file].KeepController = &r
		}
	}
}

// probeFolderRow tells a folder row by its parts: the folder's name, the
// number of files below it and a chevron. Its label starts with the name.
func probeFolderRow(b *Element) (string, bool) {
	var name string
	chevron, count := false, false
	for _, c := range b.Children {
		switch {
		case c.Type == "Image" && c.Label == "Forward":
			chevron = true
		case c.Type == "StaticText" && name == "":
			name = c.Label
		case c.Type == "StaticText":
			_, err := strconv.Atoi(c.Label)
			count = count || err == nil
		}
	}
	if !chevron || !count || name == "" || !strings.HasPrefix(b.Label, name+", ") {
		return "", false
	}
	return name, true
}

// ProbeScreen reads the probe app's window as it stands.
func (s *Session) ProbeScreen(ctx context.Context) (ProbeScreen, error) {
	root, err := s.Source(ctx)
	if err != nil {
		return ProbeScreen{}, err
	}
	return ParseProbeScreen(root), nil
}

// ErrProbeBrowser is returned when the probe app's session browser cannot be
// brought to a folder.
var ErrProbeBrowser = errors.New("the AUv3 Probe app's session browser")

// ProbeSettle is how long the probe app's browser is given to redraw after a
// tap. It is a variable so the tests need not sit it out.
var ProbeSettle = 600 * time.Millisecond

// ProbeOpenFolder shows the probe app's sessions tab with the browser at
// folder, a slash-separated path below AUM's folder ("" is the root), and
// returns that screen. The browser names only the last part of the folder it
// shows, so it is walked there from its root, unless the folder is one level
// deep and trust is set: then a browser that shows a folder of that name is
// taken as it is.
func (s *Session) ProbeOpenFolder(ctx context.Context, folder string, trust bool) (ProbeScreen, error) {
	// read gives the browser time to redraw after a tap, and reads it again
	// while it does not show what the tap leads to.
	read := func(arrived func(ProbeScreen) bool) (scr ProbeScreen, err error) {
		for try := 0; try < 5; try++ {
			if err = sleepCtx(ctx, ProbeSettle); err != nil {
				return scr, err
			}
			if scr, err = s.ProbeScreen(ctx); err != nil || arrived(scr) {
				return scr, err
			}
		}
		return scr, nil
	}
	scr, err := s.ProbeScreen(ctx)
	if err == nil && scr.Tab == "" {
		// An app that is only just starting has no window yet.
		scr, err = read(func(p ProbeScreen) bool { return p.Tab != "" })
	}
	if err != nil {
		return scr, err
	}
	if scr.Tab != ProbeSessionsTab {
		if scr.SessionsTab == nil {
			return scr, fmt.Errorf("%w: the app's window has no %q tab", ErrProbeBrowser, ProbeSessionsTab)
		}
		x, y := scr.SessionsTab.Center()
		if err := s.TapAt(ctx, x, y); err != nil {
			return scr, err
		}
		if scr, err = read(func(p ProbeScreen) bool { return p.Tab == ProbeSessionsTab }); err != nil {
			return scr, err
		}
		if scr.Tab != ProbeSessionsTab {
			return scr, fmt.Errorf("%w: the app stays on its %q tab", ErrProbeBrowser, scr.Tab)
		}
	}
	if !scr.Browser {
		return scr, fmt.Errorf("%w is not shown: AUM's folder is not linked in the app", ErrProbeBrowser)
	}
	var parts []string
	if folder = strings.Trim(folder, "/"); folder != "" {
		parts = strings.Split(folder, "/")
	}
	if trust && len(parts) == 1 && scr.Folder == parts[0] {
		return scr, nil
	}
	// Up to the root: the navigation bar is there only below it.
	for depth := 0; scr.Back != nil; depth++ {
		if depth > 8 {
			return scr, fmt.Errorf("%w does not return to its root", ErrProbeBrowser)
		}
		x, y := scr.Back.Center()
		if err := s.TapAt(ctx, x, y); err != nil {
			return scr, err
		}
		from := scr.Folder
		if scr, err = read(func(p ProbeScreen) bool { return p.Back == nil || p.Folder != from }); err != nil {
			return scr, err
		}
	}
	// And down. A pinned folder is listed a second time above the others,
	// under the last part of its path: the row further down is the folder
	// that lies here.
	for _, part := range parts {
		var row *ProbeFolder
		for i := range scr.Folders {
			if scr.Folders[i].Name == part {
				row = &scr.Folders[i]
			}
		}
		if row == nil {
			return scr, fmt.Errorf("%w lists no folder %q", ErrProbeBrowser, part)
		}
		if ok, err := s.ClickButtonAt(ctx, row.Label, row.Rect); err != nil {
			return scr, err
		} else if !ok {
			return scr, fmt.Errorf("%w: the row of folder %q moved before it could be tapped", ErrProbeBrowser, part)
		}
		if scr, err = read(func(p ProbeScreen) bool { return p.Folder == part }); err != nil {
			return scr, err
		}
		if scr.Folder != part {
			return scr, fmt.Errorf("%w shows %q after a tap on folder %q", ErrProbeBrowser, scr.Folder, part)
		}
	}
	return scr, nil
}

// ClickButtonAt clicks the button labelled label whose frame is rect, as an
// element: WebDriverAgent scrolls it into sight first. Several buttons may
// carry the label (every conflict's row has a "keep controller"); the frame,
// taken from the tree just read, picks the one meant. It clicks nothing and
// returns false when no button with that label has that frame any more.
func (s *Session) ClickButtonAt(ctx context.Context, label string, rect Rect) (bool, error) {
	var found []map[string]string
	pred := "type == 'XCUIElementTypeButton' AND label == " + strconv.Quote(label)
	if err := s.c.call(ctx, http.MethodPost, s.path("/elements"),
		map[string]any{"using": "predicate string", "value": pred}, &found); err != nil {
		return false, err
	}
	for _, f := range found {
		id := elementID(f)
		if id == "" {
			continue
		}
		var r Rect
		if err := s.c.call(ctx, http.MethodGet, s.path("/element/"+id+"/rect"), nil, &r); err != nil {
			return false, err
		}
		if !sameRect(r, rect) {
			continue
		}
		err := s.c.call(ctx, http.MethodPost, s.path("/element/"+id+"/click"), map[string]any{}, nil)
		return err == nil, err
	}
	return false, nil
}

// sameRect compares two frames to the point.
func sameRect(a, b Rect) bool {
	near := func(x, y float64) bool { return x-y < 1 && y-x < 1 }
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.Width, b.Width) && near(a.Height, b.Height)
}
