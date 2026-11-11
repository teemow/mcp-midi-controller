package ipad

import (
	"context"
	"fmt"
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
			res, err = d.listFiles("Sessions", r.Path)
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

// filesPanel is the open FILES panel (the front side panel), or nil.
func (d *AUMDriver) filesPanel() *AUMOverlay {
	o := d.St.Overlay("panel")
	if o == nil || o.Title == "MENU" || o.Title == "SETTINGS" || o.Title == "MIDI CONTROL" || o.Title == "NAVIGATOR" {
		return nil
	}
	return o
}

// openFiles opens FILES and walks to view (Sessions or Recordings), then
// into the folders of path. FILES reopens on its last view, so it backs out
// to its root first when the view's row is not on screen.
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
			return p, fmt.Errorf("no folder %q in %s (entries: %s)", folder, p.Title, entryNames(p))
		}
		if err := d.openRow(row, p.Title); err != nil {
			return nil, err
		}
	}
	return d.filesPanel(), nil
}

// openRow taps a list row and waits for the panel to change.
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
	Frame  Rect   `json:"frame"`
}

// fileEntries reads the cells of a FILES list: each holds the entry's name
// and, below it, a smaller detail line.
func fileEntries(p *AUMOverlay) []AUMFileEntry {
	var out []AUMFileEntry
	if p == nil || p.el == nil {
		return out
	}
	p.el.Walk(func(e *Element, _ int) bool {
		if e.Type != "Cell" || e.Hidden {
			return true
		}
		var texts []*Element
		e.Walk(func(t *Element, _ int) bool {
			if t.Type == "StaticText" && t.Label != "" && !t.Hidden && !t.Rect.Empty() {
				texts = append(texts, t)
			}
			return true
		})
		if len(texts) == 0 {
			return false
		}
		sort.SliceStable(texts, func(i, j int) bool { return texts[i].Rect.Y < texts[j].Rect.Y })
		fe := AUMFileEntry{Name: texts[0].Label, Frame: e.Rect}
		if len(texts) > 1 {
			fe.Detail = texts[1].Label
		}
		out = append(out, fe)
		return false
	})
	return out
}

// entryNames lists a FILES panel's entries, else its rows.
func entryNames(p *AUMOverlay) string {
	es := fileEntries(p)
	if len(es) == 0 {
		return rowLabels(p)
	}
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

func (d *AUMDriver) listFiles(view string, path []string) (AUMResult, error) {
	p, err := d.openFiles(view, path)
	if err != nil {
		return AUMResult{}, err
	}
	es := fileEntries(p)
	return d.result(strings.ToLower(view), true, fmt.Sprintf("%s: %d entries: %s", p.Title, len(es), entryNames(p)), es), nil
}

// AUMLoadPause is how long AUM is given to close FILES after a session row
// was tapped, before its menu is asked for the session's name. It is a
// variable so tests against a stand-in need not sit it out.
var AUMLoadPause = 2 * time.Second

// loadSession taps a session row in FILES → Sessions: AUM loads it at once.
func (d *AUMDriver) loadSession(path []string, name string) (AUMResult, error) {
	p, err := d.openFiles("Sessions", path)
	if err != nil {
		return AUMResult{}, err
	}
	row := d.findText(name, listArea(p))
	if row == nil {
		row = d.findText(name+".aumproj", listArea(p))
	}
	if row == nil {
		return AUMResult{}, fmt.Errorf("no session %q in %s (entries: %s)", name, p.Title, entryNames(p))
	}
	if err := d.tapRect(row.Rect, "session "+name); err != nil {
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

// saveSession types the name into SAVE's name field and presses Return,
// which saves at once (over a session of the same name, too).
func (d *AUMDriver) saveSession(name string) (AUMResult, error) {
	if ok, err := d.openFromMenu("save"); err != nil || !ok {
		return d.result("save", false, "SAVE did not open: "+d.St.overlaySummary(), nil), err
	}
	got, err := d.typeText(0, name)
	if err != nil {
		return AUMResult{}, err
	}
	if got != name {
		return AUMResult{}, fmt.Errorf("the name field reads %q after typing %q; not saving", got, name)
	}
	if err := d.pressReturn(); err != nil {
		return AUMResult{}, err
	}
	if err := d.pause(time.Second); err != nil {
		return AUMResult{}, err
	}
	if err := d.Read(); err != nil {
		return AUMResult{}, err
	}
	if b := d.findPrefix("OVERWRITE", nil); b != nil {
		if err := d.tapRect(b.Rect, "OVERWRITE?"); err != nil {
			return AUMResult{}, err
		}
	}
	if _, err := d.ensureMenu(); err != nil {
		return AUMResult{}, err
	}
	ok, err := d.until(aumWait, func(st AUMState) bool { return strings.EqualFold(st.Session, name) })
	return d.result("save", ok, fmt.Sprintf("menu shows session %q (saved as %q)", d.St.Session, name), nil), err
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
