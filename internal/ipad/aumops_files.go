package ipad

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// AUMFilesActions are aum_files' actions.
var AUMFilesActions = []string{"list", "recordings", "load", "save", "rename", "clear", "notes"}

// AUMFiles runs a FILES, SAVE or CLEAR operation.
func (c *Client) AUMFiles(ctx context.Context, r AUMRequest) (AUMResult, error) {
	var res AUMResult
	err := c.DriveAUM(ctx, func(d *AUMDriver) error {
		var err error
		switch r.Action {
		case "list", "":
			res, err = d.listSessions(r.Path)
		case "recordings":
			res, err = d.listFiles("Recordings", r.Path)
		case "load":
			if r.Name == "" {
				return fmt.Errorf("load needs name: the session to load")
			}
			if err := needConfirm(r, "loading a session replaces the open one (unsaved changes are lost)"); err != nil {
				return err
			}
			res, err = d.loadSession(r.Path, r.Name)
		case "save":
			if r.Name == "" {
				return fmt.Errorf("save needs name: the session name to save under")
			}
			if err := needConfirm(r, "saving writes a session file and overwrites one of the same name"); err != nil {
				return err
			}
			res, err = d.saveSession(r.Name)
		case "rename":
			if r.Name == "" || r.NewName == "" {
				return fmt.Errorf("rename needs name and new_name")
			}
			if err := needConfirm(r, "rename renames the session file at once"); err != nil {
				return err
			}
			res, err = d.renameSession(r.Path, r.Name, r.NewName)
		case "clear":
			if err := needConfirm(r, "CLEAR empties the open session"); err != nil {
				return err
			}
			res, err = d.clearSession()
		case "notes":
			var ok bool
			if ok, err = d.openFromMenu("notes"); err == nil {
				var notes []string
				for _, o := range d.St.Overlays {
					notes = append(notes, o.Fields...)
				}
				res = d.result("notes", ok, verdict(ok, "session notes open", "NOTES did not open"), notes)
			}
		default:
			err = fmt.Errorf("unknown aum_files action %q (%s)", r.Action, strings.Join(AUMFilesActions, ", "))
		}
		return err
	})
	return res, err
}

// filesPanel is the open FILES panel (the front side panel), or nil. A FILES
// view is titled by its folder, so it is told by what it is not: with SAVE
// open, FILES is opened from the menu like from any other panel.
func (d *AUMDriver) filesPanel() *AUMOverlay {
	o := d.St.Overlay("panel")
	if o == nil || o.Title == "MENU" || o.Title == "SETTINGS" || o.Title == "MIDI CONTROL" || o.Title == "NAVIGATOR" || o.Title == "SAVE" {
		return nil
	}
	return o
}

// openFiles opens FILES and walks to view (Sessions or Recordings), then
// into the folders of path. FILES reopens on its last view, so it backs out
// to its root first when the view's row is not on screen.
//
// The folders of path are looked for among the rows on screen and told by the
// panel's title, which is wrong for both views: every folder of Sessions is
// titled SESSIONS and every folder of Recordings RECORDINGS (AUM 1.4.8). A
// folder of Recordings is therefore taken for its root and each folder step
// waits aumWait for nothing
// (https://github.com/teemow/mcp-midi-controller/issues/199). A folder of
// Sessions is opened with openSessions.
func (d *AUMDriver) openFiles(view string, path []string) (*AUMOverlay, error) {
	if d.filesPanel() == nil {
		if ok, err := d.openFromMenu("files"); err != nil || !ok {
			return nil, fmt.Errorf("FILES did not open (%s)", d.St.overlaySummary())
		}
	}
	for i := 0; i < 4; i++ {
		p := d.filesPanel()
		if p == nil {
			return nil, fmt.Errorf("FILES closed (%s)", d.St.overlaySummary())
		}
		if strings.EqualFold(p.Title, view) {
			break
		}
		if row := d.findText(view, listArea(p)); row != nil {
			if err := d.openRow(row, p.Title); err != nil {
				return nil, err
			}
			continue
		}
		// Back arrow at the panel header's left.
		if err := d.tap(p.Frame.X+20, 30, "back"); err != nil {
			return nil, err
		}
		title := p.Title
		if _, err := d.until(2*time.Second, func(st AUMState) bool { return d.filesPanel() == nil || d.filesPanel().Title != title }); err != nil {
			return nil, err
		}
	}
	for _, folder := range path {
		p := d.filesPanel()
		if p == nil {
			return nil, fmt.Errorf("FILES closed")
		}
		row := d.findText(folder, listArea(p))
		if row == nil {
			return p, &NoFolderError{Folder: folder, In: p.Title, Entries: entryNames(p)}
		}
		if err := d.openRow(row, p.Title); err != nil {
			return nil, err
		}
	}
	return d.filesPanel(), nil
}

// NoFolderError says a FILES list has no folder of a name: a session's folder
// of recordings that no take was made into yet, for one.
type NoFolderError struct {
	// Folder is the folder looked for, In the list's title and Entries what
	// the list holds.
	Folder, In, Entries string
}

func (e *NoFolderError) Error() string {
	return fmt.Sprintf("no folder %q in %s (entries: %s)", e.Folder, e.In, e.Entries)
}

// openRow taps a row of the FILES panel titled title and waits for the
// panel's title to change (the list slides in before its header does).
func (d *AUMDriver) openRow(row *Element, title string) error {
	if err := d.tapRect(row.Rect, row.Text()); err != nil {
		return err
	}
	_, err := d.until(aumWait, func(st AUMState) bool {
		o := st.Overlay("")
		return o == nil || o.Title != title
	})
	return err
}

// listArea is a side panel below its 60-point header, so a row search does
// not hit the header's title.
func listArea(p *AUMOverlay) *Rect {
	return &Rect{X: p.Frame.X, Y: p.Frame.Y + 60, Width: p.Frame.Width, Height: p.Frame.Height - 60}
}

// AUMFileEntry is one entry of a FILES list: a session, recording or
// folder, with the line below its name (date, size).
type AUMFileEntry struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	// Folder is true for a row that opens a folder or a view (AUM draws a
	// chevron at its right); a tap on any other row of Sessions loads that
	// session at once.
	Folder bool `json:"folder,omitempty"`
	// Frame is the row on screen. A list that was read through page by page
	// has it only for the rows still on screen when the reading ended.
	Frame Rect `json:"frame,omitzero"`

	// text is the name's own frame in the row, where the row is tapped.
	text Rect
	// cut says the row is not on screen whole: the list's edge cuts it. Such
	// a row can show one of its two lines, the second one at the list's top,
	// which then reads as its name.
	cut bool
}

// fileEntries reads the cells of a FILES list: each holds the entry's name
// and, below it, a smaller detail line.
func fileEntries(p *AUMOverlay) []AUMFileEntry {
	var out []AUMFileEntry
	if p == nil || p.el == nil {
		return out
	}
	list := listFrame(p)
	p.el.Walk(func(e *Element, _ int) bool {
		if e.Type != "Cell" || e.Hidden {
			return true
		}
		var texts []*Element
		folder := false
		e.Walk(func(t *Element, _ int) bool {
			if t.Type == "StaticText" && t.Label != "" && !t.Hidden && !t.Rect.Empty() {
				texts = append(texts, t)
			}
			folder = folder || (t.Type == "Button" && t.Label == "chevron")
			return true
		})
		if len(texts) == 0 {
			return false
		}
		sort.SliceStable(texts, func(i, j int) bool { return texts[i].Rect.Y < texts[j].Rect.Y })
		fe := AUMFileEntry{Name: texts[0].Label, Folder: folder, Frame: e.Rect, text: texts[0].Rect}
		if len(texts) > 1 {
			fe.Detail = texts[1].Label
		}
		fe.cut = e.Rect.Y < list.Y-rowSlack || e.Rect.Y+e.Rect.Height > list.Y+list.Height+rowSlack
		out = append(out, fe)
		return false
	})
	return out
}

// rowSlack is by how much a row may reach over the edge of its list and
// still be on screen whole: at the top of a list its twelfth row ends two
// points below the table's edge (AUM 1.4.8).
const rowSlack = 3.0

// listFrame is where a FILES panel shows its list: its table, else the panel
// below its header.
func listFrame(p *AUMOverlay) Rect {
	if t := p.el.First(func(e *Element) bool { return e.Type == "Table" && !e.Hidden && !e.Rect.Empty() }); t != nil {
		return t.Rect
	}
	return *listArea(p)
}

// entryNames lists a FILES panel's entries, else its rows.
func entryNames(p *AUMOverlay) string {
	es := fileEntries(p)
	if len(es) == 0 {
		return rowLabels(p)
	}
	return fileNames(es)
}

// fileNames lists the names of entries, the first forty of a long list.
func fileNames(es []AUMFileEntry) string {
	l := make([]string, len(es))
	for i, e := range es {
		l[i] = e.Name
	}
	return strings.Join(limit(l, 40), ", ")
}

func rowLabels(o *AUMOverlay) string {
	if o == nil {
		return ""
	}
	var l []string
	for _, r := range o.Rows {
		l = append(l, r.Label)
	}
	return strings.Join(limit(l, 40), ", ")
}

// listFiles lists the page that shows of a view of FILES, in the folder path.
// It is FILES → Recordings' list; Sessions is read by listSessions.
func (d *AUMDriver) listFiles(view string, path []string) (AUMResult, error) {
	p, err := d.openFiles(view, path)
	if err != nil {
		return AUMResult{}, err
	}
	if p == nil {
		return AUMResult{}, fmt.Errorf("FILES closed on the way to %s (%s)", view, d.St.overlaySummary())
	}
	es := fileEntries(p)
	return d.result(strings.ToLower(view), true, fmt.Sprintf("%s: %d entries: %s", p.Title, len(es), entryNames(p)), es), nil
}

// listSessions lists a folder of FILES → Sessions: all of it, read a page at
// a time from the list's top, in the order AUM shows it. The entries are the
// result's data; only those on the last page carry a frame.
func (d *AUMDriver) listSessions(path []string) (AUMResult, error) {
	if err := d.openSessions(path); err != nil {
		return AUMResult{}, err
	}
	scan, err := d.readFiles(nil)
	if err != nil {
		return AUMResult{}, err
	}
	where, names := sessionsFolder(path), fileNames(scan.Rows)
	if len(scan.Rows) > 40 {
		names += " (data lists all)"
	}
	if !scan.End {
		return d.result("sessions", false, fmt.Sprintf("%s, NOT the whole list: it went on after %d pages: the first %d entries: %s", where, filePages, len(scan.Rows), names), scan.Rows), nil
	}
	return d.result("sessions", true, fmt.Sprintf("%s, the whole list (%d pages): %d entries: %s", where, scan.Pages, len(scan.Rows), names), scan.Rows), nil
}

// sessionsFolder names a folder of Sessions as SAVE prints it, "/" being the
// root, for a message.
func sessionsFolder(path []string) string {
	return "FILES → Sessions → /" + strings.Join(path, "/")
}

// AUMLoadPause is how long AUM is given to close FILES after a session row
// was tapped, before its menu is asked for the session's name. It is a
// variable so tests against a stand-in need not sit it out.
var AUMLoadPause = 2 * time.Second

// loadSession taps a session's row in FILES → Sessions → path: AUM loads it
// at once. The folder is reached as save reaches it (openSessions) and the
// row is looked for through the whole list. What is tapped is a session's
// row, never a folder's of that name, and only once the row is on screen
// whole: a tap on any other session's row would load that one.
func (d *AUMDriver) loadSession(path []string, name string) (AUMResult, error) {
	if err := d.openSessions(path); err != nil {
		return AUMResult{}, err
	}
	row, scan, err := d.findFile(false, name, name+".aumproj")
	if err != nil {
		return AUMResult{}, err
	}
	if row == nil {
		also := ""
		for _, e := range scan.Rows {
			if e.Folder && e.Name == name {
				also = "; there is a folder of that name: its sessions are loaded with it in path"
			}
		}
		return AUMResult{}, fmt.Errorf("no session %q in %s (entries: %s)%s", name, sessionsFolder(path), fileNames(scan.Rows), also)
	}
	if err := d.tapRect(row.text, "session "+row.Name); err != nil {
		return AUMResult{}, err
	}
	// Loading takes a moment and closes FILES; the menu then shows the name.
	if err := d.pause(AUMLoadPause); err != nil {
		return AUMResult{}, err
	}
	want := strings.TrimSuffix(name, ".aumproj")
	ok, loading, err := d.awaitSession(want)
	if err != nil {
		return AUMResult{}, err
	}
	msg := fmt.Sprintf("session now %q (wanted %q)", d.St.Session, want)
	if !ok && loading {
		msg += fmt.Sprintf("; AUM still shows %s after %s", loadingLabel, aumLoadTimeout)
	}
	return d.result("load", ok, msg, nil), nil
}

// aumNameWait is how long the main menu is given to show the loaded
// session's name once AUM has stopped loading. A variable for tests.
var aumNameWait = 10 * time.Second

// awaitSession waits for AUM's main menu to show the session want after its
// row was tapped. While AUM instantiates heavy plugins its UI does not answer
// and a single read can block for most of a minute, so the wait is not a
// fixed budget of reads: it lasts as long as AUM shows that it is loading
// (bounded by aumLoadTimeout from now), and only once AUM has stopped loading
// is the name given aumNameWait, counted from the first read that saw the
// load over. The load can close the menu; it is opened again. loading reports
// whether AUM still showed that it was loading when the wait gave up.
func (d *AUMDriver) awaitSession(want string) (ok, loading bool, err error) {
	deadline := time.Now().Add(aumLoadTimeout)
	var settled time.Time // when a read first saw AUM no longer loading
	for {
		if err := d.Read(); err != nil {
			return false, false, err
		}
		if strings.EqualFold(d.St.Session, want) {
			return true, false, nil
		}
		now := time.Now()
		loading = d.loading()
		switch {
		case loading:
			settled = time.Time{}
		case settled.IsZero():
			settled = now
		case now.Sub(settled) > aumNameWait:
			return false, false, nil
		}
		if now.After(deadline) {
			return false, loading, nil
		}
		if d.St.Panel() != "MENU" {
			// ensureMenu re-reads until the menu shows (or aumWait passes);
			// the loop then reads the name again.
			if _, err := d.ensureMenu(); err != nil {
				return false, false, err
			}
			continue
		}
		if err := d.pause(150 * time.Millisecond); err != nil {
			return false, false, err
		}
	}
}

// AUMSave is what aum_files save saw of the session's row in FILES →
// Sessions: the proof that AUM wrote the file, or that it did not.
type AUMSave struct {
	// Folder is the folder SAVE names ("/Songs"), the one the row is read in.
	Folder string `json:"folder"`
	// Existed says the folder listed the session before the save, so SAVE
	// SESSION asked OVERWRITE? and was pressed a second time.
	Existed bool `json:"existed"`
	// SavedBefore and SavedAfter are the saved time in the session's row
	// before and after the save, as AUM prints it ("" = not listed).
	SavedBefore string `json:"saved_before,omitempty"`
	SavedAfter  string `json:"saved_after,omitempty"`
	// PanelClosed says the SAVE panel closed by itself after the press, as
	// it does once AUM has written the file. After a press AUM did not take
	// it stays open.
	PanelClosed bool `json:"panel_closed"`
	// Button is what the SAVE panel's button read after the press while the
	// panel was still open: "OVERWRITE?" when it was seen to ask, else its
	// last label.
	Button string `json:"button,omitempty"`
}

// saveButton is the SAVE panel's button by its two labels.
const (
	saveButton      = "SAVE SESSION"
	overwriteButton = "OVERWRITE"
)

// saveSession saves the open session under name and proves the save by the
// saved time of the session's row in FILES → Sessions, read before and after.
//
// AUM writes a new name at the first press of SAVE SESSION. For a name that
// exists the button reads "OVERWRITE?" for a second or two and writes only
// when it is pressed again in that time; afterwards it reads SAVE SESSION
// again and nothing was written. One read of AUM's tree can take longer than
// that, so the confirming press cannot wait for a read of the label: the
// list says beforehand whether the name exists, and then both presses go out
// as one gesture (tapTwice), like CLEAR's and REMOVE CHANNEL's.
//
// The session's name in the main menu proves nothing: it is that name before
// a save over the open session, too. (Up to v0.20.0 this pressed Return, read
// the tree and tapped OVERWRITE? after it had gone, then took the menu's name
// for proof: "verified" for saves that wrote nothing.)
func (d *AUMDriver) saveSession(name string) (AUMResult, error) {
	if ok, err := d.openFromMenu("save"); err != nil || !ok {
		return d.result("save", false, "SAVE did not open: "+d.St.overlaySummary(), nil), err
	}
	shown, path, ok := saveFolder(d.St.Overlay("panel"))
	if !ok {
		return AUMResult{}, fmt.Errorf("the SAVE panel names no folder (%s); not saving", d.St.overlaySummary())
	}
	rep := AUMSave{Folder: shown}
	before, err := d.sessionEntry(path, name)
	if err != nil {
		return AUMResult{}, fmt.Errorf("reading %q in FILES → Sessions before saving: %w; not saving", name, err)
	}
	if before != nil {
		rep.Existed, rep.SavedBefore = true, before.Detail
	}

	if ok, err := d.openFromMenu("save"); err != nil || !ok {
		return d.result("save", false, "SAVE did not open again after FILES; not saved: "+d.St.overlaySummary(), rep), err
	}
	if p := d.St.Overlay("panel"); p == nil || len(p.Fields) == 0 || p.Fields[0] != name {
		got, err := d.typeText(0, name)
		if err != nil {
			return AUMResult{}, err
		}
		if got != name {
			return AUMResult{}, fmt.Errorf("the name field reads %q after typing %q; not saving", got, name)
		}
		// What was typed into must be the SAVE panel's own field: a save
		// under the name it still held would overwrite another session.
		if err := d.Read(); err != nil {
			return AUMResult{}, err
		}
	}
	p := d.St.Overlay("panel")
	if p == nil || len(p.Fields) == 0 || p.Fields[0] != name {
		return AUMResult{}, fmt.Errorf("the SAVE panel's name field does not read %q (%s); not saving", name, d.St.overlaySummary())
	}
	btn := d.findText(saveButton, &p.Frame)
	if btn == nil {
		return AUMResult{}, fmt.Errorf("no %s button in the SAVE panel (%s); not saving", saveButton, d.St.overlaySummary())
	}
	if rep.Existed {
		err = d.tapTwice(btn.Rect, saveButton+" / OVERWRITE?")
	} else {
		err = d.tapRect(btn.Rect, saveButton)
	}
	if err != nil {
		return AUMResult{}, err
	}
	// AUM closes the SAVE panel by itself a moment after it has written the
	// file, and leaves it open after a press it did not take. The list is
	// opened only once that is settled: a tap on the panel's close icon after
	// the panel has gone hits the menu button on the same spot.
	if rep.PanelClosed, err = d.until(4*AUMSaveSettle, func(st AUMState) bool {
		p := st.Overlay("panel")
		if p == nil || p.Title != "SAVE" {
			return true
		}
		if b := d.findPrefix(overwriteButton, &p.Frame); b != nil {
			rep.Button = b.Text()
		} else if b := d.findText(saveButton, &p.Frame); b != nil && !rep.asked() {
			rep.Button = b.Text()
		}
		return false
	}); err != nil {
		return AUMResult{}, err
	}

	// The list is read again; once more after a moment when AUM took the
	// press and the list does not show it yet.
	for try := 0; ; try++ {
		after, err := d.sessionEntry(path, name)
		if err != nil {
			return d.result("save", false, fmt.Sprintf("pressed %s, but FILES → Sessions could not be read afterwards (%v): not known whether %q was saved", saveButton, err, name), rep), nil
		}
		rep.SavedAfter = ""
		if after != nil {
			rep.SavedAfter = after.Detail
		}
		if rep.saved() || !rep.PanelClosed || try > 0 {
			break
		}
		if err := d.pause(AUMSaveSettle); err != nil {
			return AUMResult{}, err
		}
	}
	return d.result("save", rep.saved(), rep.message(name), rep), nil
}

// AUMSaveSettle is the pause before the list is read a second time after a
// save that does not show in it, and a quarter of the time the SAVE panel is
// given to close by itself after the press. It is a variable so tests against
// a stand-in need not sit it out.
var AUMSaveSettle = time.Second

// saved reports whether the row shows a save: one whose saved time moved, or
// one that was not there. For a row that was not there the SAVE panel must
// have closed as well: a panel that stays open after the one press of a new
// name, or a button that asks OVERWRITE?, says the name was there after all,
// whatever the list showed before, and that press wrote nothing.
func (r AUMSave) saved() bool {
	switch {
	case r.SavedAfter == "":
		return false
	case r.Existed:
		return r.SavedAfter != r.SavedBefore
	default:
		return r.PanelClosed && !r.asked()
	}
}

// asked reports whether the button read OVERWRITE? after the press.
func (r AUMSave) asked() bool {
	return strings.HasPrefix(strings.ToUpper(r.Button), overwriteButton)
}

func (r AUMSave) message(name string) string {
	where := "FILES → Sessions → " + r.Folder
	button := "; the SAVE panel closed by itself after the press"
	if !r.PanelClosed {
		button = fmt.Sprintf("; the SAVE panel stayed open after the press, its button reading %q", r.Button)
	}
	switch {
	case r.saved() && r.Existed:
		return fmt.Sprintf("saved over %q: its saved time in %s went from %q to %q", name, where, r.SavedBefore, r.SavedAfter)
	case r.saved():
		return fmt.Sprintf("saved %q as a new session: %s lists it now, saved %q", name, where, r.SavedAfter)
	case r.Existed && r.SavedAfter == "":
		return fmt.Sprintf("NOT saved: %s listed %q (saved %q) before %s and OVERWRITE? were pressed, and does not list it now%s", where, name, r.SavedBefore, saveButton, button)
	case r.Existed:
		return fmt.Sprintf("NOT saved: the saved time of %q in %s is still %q after %s and OVERWRITE? were pressed, so AUM wrote nothing%s", name, where, r.SavedBefore, saveButton, button)
	case r.asked():
		return fmt.Sprintf("NOT saved: AUM asks %q for %q, a name %s did not list before the press, and was not answered: what it would overwrite is not known", r.Button, name, where)
	case r.SavedAfter != "":
		return fmt.Sprintf("NOT saved: %s lists %q (saved %q) now and did not before, but the SAVE panel stayed open after the one press of %s, as it does when AUM asks to overwrite: not taken for a save", where, name, r.SavedAfter, saveButton)
	default:
		return fmt.Sprintf("NOT saved: %s does not list %q after %s was pressed%s", where, name, saveButton, button)
	}
}

// saveFolder reads the folder the SAVE panel saves into: the one text of the
// panel that is a path ("/Songs", "/" for the root of Sessions). shown is
// that text, path its folders.
func saveFolder(p *AUMOverlay) (shown string, path []string, ok bool) {
	if p == nil || p.el == nil {
		return "", nil, false
	}
	t := p.el.First(func(e *Element) bool {
		return e.Type == "StaticText" && !e.Hidden && !e.Rect.Empty() && strings.HasPrefix(strings.TrimSpace(e.Label), "/")
	})
	if t == nil {
		return "", nil, false
	}
	shown = strings.TrimSpace(t.Label)
	for _, f := range strings.Split(shown, "/") {
		if f != "" {
			path = append(path, f)
		}
	}
	return shown, path, true
}

// sessionEntry reads the row of session name in FILES → Sessions → path, or
// nil when the folder does not list it. The folder is reached with
// openSessions, and the session is looked for through the whole list, not
// only its first page.
func (d *AUMDriver) sessionEntry(path []string, name string) (*AUMFileEntry, error) {
	if err := d.openSessions(path); err != nil {
		return nil, err
	}
	e, _, err := d.findFile(false, name, name+".aumproj")
	return e, err
}

// openSessions opens FILES → Sessions in the folder path, its list at the
// top. It is how save, list and load reach a folder.
//
// AUM titles every folder of Sessions "SESSIONS" and names the folder in a
// header row above the list: "/" at the root, the folder's name inside one.
// FILES reopens in the folder it last showed, a panel left open stays
// scrolled where it was, and its back arrow goes up one folder. So FILES is
// opened from the menu anew, which shows the list from its top, the list is
// taken to the root and walked down path from there, checking the header at
// each step: the list that is read is in the folder that was asked for, and
// AUM has just made it. Each folder is looked for through the whole list, not
// only its first page, and what is tapped is a folder's row, on screen whole.
func (d *AUMDriver) openSessions(path []string) error {
	if ok, err := d.openFromMenu("files"); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("FILES did not open (%s)", d.St.overlaySummary())
	}
	for up := 0; ; up++ {
		p, err := d.openFiles("Sessions", nil)
		if err != nil {
			return err
		}
		at, ok := filesFolder(p)
		if !ok {
			return fmt.Errorf("FILES → Sessions names no folder above its list (entries: %s)", entryNames(p))
		}
		if at == "/" {
			break
		}
		if up == 8 {
			return fmt.Errorf("FILES → Sessions is still in %q after going up %d folders", at, up)
		}
		if err := d.tap(p.Frame.X+20, 30, "back, out of "+at); err != nil {
			return err
		}
		if _, err := d.until(aumWait, func(AUMState) bool {
			now, ok := filesFolder(d.filesPanel())
			return !ok || now != at
		}); err != nil {
			return err
		}
	}
	for i, folder := range path {
		at, _ := filesFolder(d.filesPanel())
		// A session of the folder's name would be loaded by the tap: the row
		// that is tapped is a folder's, by the name inside that row (the
		// header can read the same).
		e, scan, err := d.findFile(true, folder)
		if err != nil {
			return err
		}
		if e == nil {
			return &NoFolderError{Folder: folder, In: sessionsFolder(path[:i]), Entries: fileNames(scan.Rows)}
		}
		if err := d.tapRect(e.text, "folder "+folder); err != nil {
			return err
		}
		in, err := d.until(aumWait, func(AUMState) bool {
			now, ok := filesFolder(d.filesPanel())
			return ok && now != at && lastFolder(now) == folder
		})
		if err != nil {
			return err
		}
		if !in {
			now, _ := filesFolder(d.filesPanel())
			return fmt.Errorf("FILES → Sessions shows folder %q after a tap on %q in %q", now, folder, at)
		}
	}
	return nil
}

// filesFolder is the folder a FILES list shows, from the header row above
// its cells ("/" for the root of Sessions).
func filesFolder(p *AUMOverlay) (string, bool) {
	if p == nil || p.el == nil {
		return "", false
	}
	h := p.el.First(func(e *Element) bool {
		return e.Type == "StaticText" && !e.Hidden && !e.Rect.Empty() && hasTrait(e.Traits, "Header") && strings.TrimSpace(e.Label) != ""
	})
	if h == nil {
		return "", false
	}
	return strings.TrimSpace(h.Label), true
}

// lastFolder is the last folder of a path as a FILES header prints it.
func lastFolder(path string) string {
	path = strings.TrimRight(path, "/")
	return path[strings.LastIndex(path, "/")+1:]
}

// filePages bounds how often readFiles reads a list (eight new rows a page).
const filePages = 60

// fileStep is how far readFiles drags a FILES list for its next page, and
// fileStepTime how long that drag takes. Measured on the iPad (AUM 1.4.8): a
// drag of 420 points in 0.9 s moves the list by 404 to 406 points, eight of
// its 50-point rows, so six of the twelve to fifteen rows on screen are read
// twice. A drag of 584 points in 0.5 s ran on after the finger lifted and
// moved the list by 674: the row in between was on neither page.
const (
	fileStep     = 420.0
	fileStepTime = 900 * time.Millisecond
)

// fileScan is what readFiles read of a FILES list.
type fileScan struct {
	// Rows are the rows that were on screen whole, in the list's order. Only
	// those of the last page read keep their frame: the others have moved.
	Rows []AUMFileEntry
	// Pages is how many pages brought rows: the reads at the list's end,
	// which show its last page again, are not counted.
	Pages int
	// End says the list was read to its end. It is false when a visit ended
	// the reading, or when the list went on after filePages reads.
	End bool
}

// fileKey tells the rows of a list apart: a folder and a session can share a
// name.
type fileKey struct {
	name   string
	folder bool
}

// readFiles reads the open FILES list from where it stands to its end. Only
// the rows on screen can be read (the tree's other cells are hidden, some with
// another row's contents), so the list is scrolled a page at a time
// until two pages in a row bring no row that was not seen (one such page can
// be a drag the list did not follow). Pages overlap; a page that shares no
// row with the one before means rows went by unread, which is an error and
// not a shorter list. Only rows on screen whole count: a row the list's edge
// cuts is read on the page before or after, where it is whole.
//
// visit, when set, is shown each page as it is read, the cut rows too, top
// to bottom; it ends the reading by returning true, with the list left on
// that page.
func (d *AUMDriver) readFiles(visit func(page []AUMFileEntry) bool) (fileScan, error) {
	out := fileScan{Rows: []AUMFileEntry{}}
	at := map[fileKey]int{}
	var prev, cur map[fileKey]bool
	for reads, idle := 0, 0; reads < filePages; reads++ {
		p := d.filesPanel()
		if p == nil {
			return out, fmt.Errorf("FILES closed (%s)", d.St.overlaySummary())
		}
		page := fileEntries(p)
		sort.SliceStable(page, func(i, j int) bool { return page[i].Frame.Y < page[j].Frame.Y })
		cur = map[fileKey]bool{}
		fresh, overlap := 0, len(prev) == 0
		for _, e := range page {
			if e.cut {
				continue
			}
			k := fileKey{e.Name, e.Folder}
			cur[k] = true
			overlap = overlap || prev[k]
			if i, ok := at[k]; ok {
				out.Rows[i] = e
			} else {
				at[k] = len(out.Rows)
				out.Rows = append(out.Rows, e)
				fresh++
			}
		}
		if !overlap && len(cur) > 0 {
			return out, fmt.Errorf("the list %s scrolled past rows that were not read: no row of one page is on the next", p.Title)
		}
		if fresh > 0 || reads == 0 {
			out.Pages++
		}
		if visit != nil && visit(page) {
			break
		}
		if len(cur) > 0 {
			prev = cur
		}
		if fresh > 0 {
			idle = 0
		} else if idle++; idle == 2 {
			out.End = true
			break
		}
		x, y := p.Frame.X+30, d.win.Height/2+fileStep/2
		if err := d.drag("the list", DragTiming{Move: fileStepTime, Hold: 200 * time.Millisecond}, Point{x, y}, Point{x, y - fileStep}); err != nil {
			return out, err
		}
		if err := d.Read(); err != nil {
			return out, err
		}
		cur = nil // the rows have moved: the next read says where they are
	}
	for i, e := range out.Rows {
		if !cur[fileKey{e.Name, e.Folder}] {
			out.Rows[i].Frame, out.Rows[i].text = Rect{}, Rect{}
		}
	}
	return out, nil
}

// findFile finds the entry called one of names in the open FILES list, or
// nil, reading the list page by page (readFiles) until the entry shows. The
// list is left on the page that shows it. scan holds the rows read on the
// way: all of the list's when the entry is not in it.
//
// With folder set it finds a folder's row, else a session's: a folder and a
// session can share a name, and only one of them may be tapped. A row counts
// only once it is on screen whole, and a session's only once its second
// line, the saved time, can be read: a row cut by the list's edge shows one
// of its lines, and a tap aimed at it can land beside the list.
func (d *AUMDriver) findFile(folder bool, names ...string) (found *AUMFileEntry, scan fileScan, err error) {
	cut := ""
	scan, err = d.readFiles(func(page []AUMFileEntry) bool {
		for i, e := range page {
			if e.Folder != folder || !slices.Contains(names, e.Name) {
				continue
			}
			if e.cut || (!folder && e.Detail == "") {
				cut = e.Name
				continue
			}
			found = &page[i]
			return true
		}
		return false
	})
	if err != nil || found != nil {
		return found, scan, err
	}
	if cut != "" {
		what := "shows no saved time"
		if folder {
			what = "was never on screen whole"
		}
		return nil, scan, fmt.Errorf("the row of %q in %s %s", cut, d.St.Panel(), what)
	}
	return nil, scan, nil
}

// renameSession long-presses a session row, opens Rename from the share
// sheet's actions and enters the new name.
func (d *AUMDriver) renameSession(path []string, name, newName string) (AUMResult, error) {
	p, err := d.openFiles("Sessions", path)
	if err != nil {
		return AUMResult{}, err
	}
	row := d.findText(name, listArea(p))
	if row == nil {
		return AUMResult{}, fmt.Errorf("no session %q in %s (entries: %s)", name, p.Title, entryNames(p))
	}
	if err := d.hold(row.Rect, "session "+name); err != nil {
		return AUMResult{}, err
	}
	found, err := d.until(aumWait, func(st AUMState) bool { return d.findText("Rename", nil) != nil || d.findText("Copy URL", nil) != nil })
	if err != nil {
		return AUMResult{}, err
	}
	if !found {
		return d.result("rename", false, "the session's share sheet did not appear", nil), nil
	}
	if d.findText("Rename", nil) == nil {
		// The share sheet lists AUM's actions after its "more" entry.
		for _, more := range []string{"More", "Mehr", "Edit Actions…", "Aktionen bearbeiten …"} {
			if e := d.findText(more, nil); e != nil {
				if err := d.tapRect(e.Rect, more); err != nil {
					return AUMResult{}, err
				}
				break
			}
		}
		if _, err := d.until(aumWait, func(st AUMState) bool { return d.findText("Rename", nil) != nil }); err != nil {
			return AUMResult{}, err
		}
	}
	if err := d.tapText("Rename", nil); err != nil {
		return AUMResult{}, err
	}
	dlg, err := d.waitOverlay("", aumWait)
	if err != nil {
		return AUMResult{}, err
	}
	if dlg == nil {
		return d.result("rename", false, "the RENAME dialog did not open", nil), nil
	}
	if _, err := d.fillText(0, newName); err != nil {
		return AUMResult{}, err
	}
	if err := d.confirmDialog(dlg); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool {
		p := d.filesPanel()
		return p != nil && d.findText(newName, &p.Frame) != nil
	})
	return d.result("rename", ok, verdict(ok, fmt.Sprintf("renamed %q to %q", name, newName), fmt.Sprintf("%q is not listed", newName)), nil), err
}

// clearSession taps the CLEAR icon twice within its one-second window.
func (d *AUMDriver) clearSession() (AUMResult, error) {
	if ok, err := d.ensureMenu(); err != nil || !ok {
		return d.result("clear", false, "the main menu did not open", nil), err
	}
	menu := d.St.Overlay("panel")
	cap := d.findText("CLEAR", &menu.Frame)
	if cap == nil {
		return AUMResult{}, fmt.Errorf("no CLEAR in the main menu")
	}
	before := len(d.St.Channels)
	if err := d.tapTwice(Rect{X: cap.Rect.X, Y: cap.Rect.Y - 64, Width: cap.Rect.Width, Height: 64}, "CLEAR / Sure?"); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return len(st.Channels) < before || before == 0 })
	return d.result("clear", ok, fmt.Sprintf("%d strips -> %d", before, len(d.St.Channels)), nil), err
}
