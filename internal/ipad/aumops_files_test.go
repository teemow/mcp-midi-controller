package ipad

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// filesFake is an AUM whose FILES answers what is tapped: SAVE saves into
// folder ("" is the root of Sessions), which holds sessions, and the session
// open is loaded.
func filesFake(t *testing.T, folder, open string, sessions ...[2]string) (*ipadtest.WDA, *ipadtest.SessionsAUM, *Client) {
	t.Helper()
	old := AUMLoadPause
	AUMLoadPause = 10 * time.Millisecond
	t.Cleanup(func() { AUMLoadPause = old })
	w := ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(w.Close)
	return w, ipadtest.NewSessionsAUM(w, folder, open, sessions...), New(w.URL)
}

// songs are n sessions "song 00" to "song n-1", in a list's order.
func songs(n int) [][2]string {
	out := make([][2]string, n)
	for i := range out {
		out[i] = [2]string{fmt.Sprintf("song %02d", i), fmt.Sprintf("10. Nov 2026 at 10:%02d:00", i)}
	}
	return out
}

func listNow(t *testing.T, c *Client, path ...string) (AUMResult, []AUMFileEntry) {
	t.Helper()
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "list", Path: path})
	if err != nil {
		t.Fatalf("list %q: %v", path, err)
	}
	es, ok := res.Data.([]AUMFileEntry)
	if !ok {
		t.Fatalf("list %q: data = %#v", path, res.Data)
	}
	return res, es
}

func loadNow(t *testing.T, c *Client, name string, path ...string) AUMResult {
	t.Helper()
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "load", Path: path, Name: name, Confirm: true})
	if err != nil {
		t.Fatalf("load %q from %q: %v", name, path, err)
	}
	return res
}

// wantTouched checks what the taps loaded or restored: a load taps one
// session's row, a list none.
func wantTouched(t *testing.T, w *ipadtest.WDA, aum *ipadtest.SessionsAUM, want ...string) {
	t.Helper()
	if got := aum.Touched(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tapped %q, want %q (gestures %v)", got, want, w.GestureLog())
	}
}

const (
	filesIcon = "tap 997,137" // FILES in the main menu
	backArrow = "tap 830,30"  // up one folder
)

// The root of Sessions, from FILES' own root: a session and a folder of one
// name are two entries, told apart by the folder's chevron.
func TestAUMFilesList(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", [2]string{"scratch (mcp test)", "12. Nov 2026 at 21:00:00"})
	res, es := listNow(t, c)
	if !res.Verified || !strings.Contains(res.Message, "FILES → Sessions → /, the whole list") || !strings.Contains(res.Message, "2 entries: Songs, Songs") {
		t.Fatalf("list: %+v", res.Message)
	}
	if len(es) != 2 || es[0].Folder || es[0].Detail != "1. Jan 2025 at 10:00:00" || !es[1].Folder || es[1].Name != "Songs" {
		t.Fatalf("entries = %+v", es)
	}
	// Both rows are still on screen, where they can be tapped.
	if es[0].Frame.Empty() || es[1].Frame.Empty() || es[1].Frame.Y <= es[0].Frame.Y {
		t.Fatalf("frames = %v, %v", es[0].Frame, es[1].Frame)
	}
	wantTouched(t, w, aum)
}

// A folder is listed whole: every page of it, each row once, in the list's
// order, although the list's edges cut a row at either end of a page and the
// one at the top shows only its saved time.
func TestAUMFilesListsTheWholeFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	aum.SetCutRows(true)
	res, es := listNow(t, c, "Songs")
	if !res.Verified || !strings.Contains(res.Message, "FILES → Sessions → /Songs, the whole list") || !strings.Contains(res.Message, "60 entries: song 00, song 01") || !strings.Contains(res.Message, "(data lists all)") {
		t.Fatalf("list: %+v", res.Message)
	}
	if len(es) != 60 {
		t.Fatalf("%d entries: %+v", len(es), es)
	}
	framed := 0
	for i, e := range es {
		if want := songs(60)[i]; e.Name != want[0] || e.Detail != want[1] || e.Folder {
			t.Fatalf("entry %d = %+v, want %v", i, e, want)
		}
		if !e.Frame.Empty() {
			framed++
		}
	}
	// Only the last page's rows are where their frame says; the first row
	// is long gone.
	if framed == 0 || framed > ipadtest.FilesRowsScrolled || !es[0].Frame.Empty() || es[59].Frame.Empty() {
		t.Fatalf("%d entries carry a frame; the first %v, the last %v", framed, es[0].Frame, es[59].Frame)
	}
	if n := countGestures(w, "actions"); n < 6 {
		t.Fatalf("%d drags for 60 rows: %v", n, w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// FILES left open and scrolled down holds neither the header row nor the
// folder's first rows: it is opened from the menu anew, which shows the list
// from its top.
func TestAUMFilesListFromAScrolledPanel(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	aum.LeaveOpen("Songs", 30)
	res, es := listNow(t, c, "Songs")
	if !res.Verified || len(es) != 60 || es[0].Name != "song 00" || es[59].Name != "song 59" {
		t.Fatalf("list: %+v (%d entries)", res.Message, len(es))
	}
	if countGestures(w, filesIcon) != 1 {
		t.Fatalf("FILES was not opened from the menu anew: %v", w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// A list that runs on after a drag carries rows past unread: that is an
// error, not a shorter list.
func TestAUMFilesListRefusesAListItCannotRead(t *testing.T) {
	_, aum, c := filesFake(t, "Songs", "Untitled", songs(80)...)
	aum.SetRunOn(8)
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "list", Path: []string{"Songs"}})
	if err == nil || !strings.Contains(err.Error(), "scrolled past rows") {
		t.Fatalf("err = %v", err)
	}
}

// A folder Sessions does not hold is the error the stems tools tell from
// others, with what the folder above it lists.
func TestAUMFilesListNoSuchFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(3)...)
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "list", Path: []string{"Nowhere"}})
	var none *NoFolderError
	if !errors.As(err, &none) || none.Folder != "Nowhere" || none.In != "FILES → Sessions → /" || none.Entries != "Songs, Songs" {
		t.Fatalf("err = %v (%+v)", err, none)
	}
	wantTouched(t, w, aum)
}

// A session far down its folder is found on a later page and loaded by one
// tap on its row.
func TestAUMFilesLoadBeyondTheFirstPage(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	res := loadNow(t, c, "song 45", "Songs")
	if !res.Verified || aum.Open() != "song 45" || res.Message != `session now "song 45" (wanted "song 45")` {
		t.Fatalf("load: %+v, open %q", res.Message, aum.Open())
	}
	wantTouched(t, w, aum, "/Songs/song 45")
	if n := countGestures(w, "actions"); n < 4 {
		t.Fatalf("%d drags to row 45: %v", n, w.GestureLog())
	}
}

// With FILES left open and scrolled, the folder that is asked for is not
// among the rows on screen and a session above them is not either: the list
// is read from its top.
func TestAUMFilesLoadFromAScrolledPanel(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	aum.LeaveOpen("Songs", 30)
	res := loadNow(t, c, "song 03", "Songs")
	if !res.Verified || aum.Open() != "song 03" {
		t.Fatalf("load: %+v, open %q", res.Message, aum.Open())
	}
	wantTouched(t, w, aum, "/Songs/song 03")
}

// FILES reopens in the folder it showed last, and every folder is titled
// SESSIONS: from another folder the list goes up to the root and down the
// folder asked for, which the root lists below a page of sessions.
func TestAUMFilesLoadFromAnotherFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(20)...)
	aum.AddRoot(songs(30)...)
	aum.AddFolder("Other", [2]string{"song 07", "9. Nov 2026 at 08:00:00"}, [2]string{"elsewhere", "9. Nov 2026 at 08:30:00"})
	aum.Show("Other")
	res := loadNow(t, c, "song 07", "Songs")
	if !res.Verified || aum.Open() != "song 07" {
		t.Fatalf("load: %+v, open %q", res.Message, aum.Open())
	}
	// Song 07 of Songs, not Other's, and not the root's of that name.
	wantTouched(t, w, aum, "/Songs/song 07")
	if countGestures(w, backArrow) != 1 {
		t.Fatalf("did not go up to the root once: %v", w.GestureLog())
	}

	// And the other way, FILES now reopening in Songs.
	res = loadNow(t, c, "elsewhere", "Other")
	if !res.Verified || aum.Open() != "elsewhere" {
		t.Fatalf("load: %+v, open %q", res.Message, aum.Open())
	}
	wantTouched(t, w, aum, "/Songs/song 07", "/Other/elsewhere")
}

// A session and a folder of one name: the folder's row is the one opened on
// the way down, the session's the one loaded.
func TestAUMFilesLoadSessionNamedLikeAFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(3)...)
	res := loadNow(t, c, "song 01", "Songs")
	if !res.Verified {
		t.Fatalf("load from the folder: %+v", res.Message)
	}
	wantTouched(t, w, aum, "/Songs/song 01")

	// The root's session "Songs", with FILES reopening inside the folder
	// "Songs", whose header reads the same.
	res = loadNow(t, c, "Songs")
	if !res.Verified || aum.Open() != "Songs" {
		t.Fatalf("load of the session: %+v, open %q", res.Message, aum.Open())
	}
	wantTouched(t, w, aum, "/Songs/song 01", "/Songs")
}

// A row the list's edge cuts is not tapped: the list goes on until the row
// is on screen whole.
func TestAUMFilesLoadWaitsForAWholeRow(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	aum.SetCutRows(true)
	// A page is eight rows: after the first drag rows 8 to 20 are whole and
	// row 21 reaches below the list's edge, both of its lines readable.
	res := loadNow(t, c, "song 21", "Songs")
	if !res.Verified || aum.Open() != "song 21" {
		t.Fatalf("load: %+v, open %q", res.Message, aum.Open())
	}
	wantTouched(t, w, aum, "/Songs/song 21")
	if n := countGestures(w, "actions"); n != 2 {
		t.Fatalf("%d drags, want the one that brought the row up and the one that showed it whole: %v", n, w.GestureLog())
	}
}

// A session the folder does not hold is an error that lists the folder, all
// of it, and nothing is tapped; a folder of the name is pointed out.
func TestAUMFilesLoadUnknown(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(20)...)
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "load", Path: []string{"Songs"}, Name: "Nowhere", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), `no session "Nowhere" in FILES → Sessions → /Songs (entries: song 00, song 01`) || !strings.Contains(err.Error(), "song 19)") {
		t.Fatalf("err = %v", err)
	}
	aum.AddFolder("Lab")
	_, err = c.AUMFiles(context.Background(), AUMRequest{Action: "load", Name: "Lab", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), `no session "Lab" in FILES → Sessions → / (entries: Songs, Songs, Lab); there is a folder of that name`) {
		t.Fatalf("err = %v", err)
	}
	wantTouched(t, w, aum)
}

// The rows of a list as AUM draws it scrolled between two rows: the row its
// top edge cuts shows its saved time alone, which is no entry's name, and
// neither it nor the row its bottom edge cuts counts as on screen.
func TestFileEntriesMarkCutRows(t *testing.T) {
	a := ipadtest.MixerAUM()
	var cells []ipadtest.FileCell
	for _, s := range songs(20) {
		cells = append(cells, ipadtest.FileCell{Name: s[0], Detail: s[1]})
	}
	a.Files = &ipadtest.FilesPanel{Title: "SESSIONS", Cells: cells, Scrolled: true, Cut: true}
	st := ParseAUM(mustParse(t, a.Source()), Size{Width: 1210, Height: 834})
	es := fileEntries(st.Overlay("panel"))
	if len(es) != ipadtest.FilesRowsCut+2 {
		t.Fatalf("%d rows on screen: %+v", len(es), es)
	}
	for i, e := range es {
		edge := i == 0 || i == len(es)-1
		if e.cut != edge {
			t.Errorf("row %d (%q at %v): cut = %v", i, e.Name, e.Frame, e.cut)
		}
	}
	if es[0].Name != songs(20)[0][1] || es[0].Detail != "" || es[1].Name != "song 01" || es[len(es)-1].Name != "song 14" || es[len(es)-1].Detail == "" {
		t.Fatalf("rows = %+v", es)
	}
}
