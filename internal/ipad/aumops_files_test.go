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
	menuButton = "tap 1185,33" // ☰
	filesIcon  = "tap 997,137" // FILES in the main menu
	backArrow  = "tap 830,30"  // up one folder
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

// AUM takes a tap outside a popover only to close it. With a FILE PLAYER
// left open (as aum_node open on a File Player's node leaves it), the tap on
// ☰ closed the popover and opened no menu, and load answered "FILES did not
// open (no popover or panel open)". The popover is closed first, by a tap
// outside it, and the load goes on from the bare mixer.
func TestAUMFilesLoadUnderAPopover(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(3)...)
	aum.Cover(ipadtest.Overlay{Kind: "popover", Title: "FILE PLAYER", Rows: [][2]string{{"Loop", "ON"}, {"Sync", "OFF"}}})
	res := loadNow(t, c, "song 01", "Songs")
	if !res.Verified || aum.Open() != "song 01" || aum.Covered() {
		t.Fatalf("load: %+v, open %q, popover still open %v", res.Message, aum.Open(), aum.Covered())
	}
	wantTouched(t, w, aum, "/Songs/song 01")
	// Left of the popover (633,186 500×400), then ☰.
	if g := w.GestureLog(); len(g) < 3 || g[0] != "tap 316.5,386" || g[1] != menuButton || g[2] != filesIcon {
		t.Fatalf("gestures = %v", g)
	}
	if !strings.HasPrefix(res.Steps[0], "tap outside the popover") {
		t.Fatalf("steps = %v", res.Steps)
	}
}

// An alert takes every touch, a tap on ☰ too: list answered "FILES did not
// open". The alert is answered with its Cancel first.
func TestAUMFilesListUnderAnAlert(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(3)...)
	aum.Cover(ipadtest.Overlay{Kind: "alert", Title: "Set level", Fields: []string{"-1.0"}, Buttons: []string{"Cancel", "Set"}})
	res, es := listNow(t, c, "Songs")
	if !res.Verified || len(es) != 3 || aum.Covered() {
		t.Fatalf("list: %+v, %d entries, alert still open %v", res.Message, len(es), aum.Covered())
	}
	wantTouched(t, w, aum)
	// Cancel (471,405 268×40), then ☰.
	if g := w.GestureLog(); len(g) < 3 || g[0] != "tap 605,425" || g[1] != menuButton || g[2] != filesIcon {
		t.Fatalf("gestures = %v", g)
	}
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

// takes are n takes of a session's folder of recordings, in a list's order.
func takes(n int) [][2]string {
	out := make([][2]string, n)
	for i := range out {
		out[i] = [2]string{fmt.Sprintf("Master - 120 bpm - %03d", i+1), fmt.Sprintf("13. Nov 2026 at 00:%02d:57 • 4.0 MiB • wav", i)}
	}
	return out
}

func recordingsNow(t *testing.T, c *Client, path ...string) (AUMResult, []AUMFileEntry) {
	t.Helper()
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "recordings", Path: path})
	if err != nil {
		t.Fatalf("recordings %q: %v", path, err)
	}
	es, ok := res.Data.([]AUMFileEntry)
	if !ok {
		t.Fatalf("recordings %q: data = %#v", path, res.Data)
	}
	return res, es
}

func entryNamesOf(es []AUMFileEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

// Every folder of Recordings is titled RECORDINGS. With FILES reopening
// inside one, the root is still the root and another folder is found from
// there: neither is the folder FILES happened to show.
func TestAUMFilesRecordingsFromInsideAFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(3)...)
	aum.AddRecordings("Night Drive", takes(2)...)
	aum.AddRecordings("Other Song", [2]string{"Master - 90 bpm - 001", "12. Nov 2026 at 21:23:42 • 4.4 MiB • wav"})
	aum.ShowRecordings("Other Song")

	res, es := recordingsNow(t, c)
	if !res.Verified || res.Action != "recordings" || !strings.Contains(res.Message, "FILES → Recordings → /, the whole list") || !strings.Contains(res.Message, "2 entries: Night Drive, Other Song") {
		t.Fatalf("the root: %+v", res.Message)
	}
	if len(es) != 2 || !es[0].Folder || !es[1].Folder {
		t.Fatalf("the root's entries = %+v", es)
	}

	// FILES now reopens in the root; then inside the folder just listed.
	for range 2 {
		res, es = recordingsNow(t, c, "Night Drive")
		if !res.Verified || !strings.Contains(res.Message, "FILES → Recordings → /Night Drive, the whole list") || len(es) != 2 ||
			es[0].Name != "Master - 120 bpm - 001" || es[0].Detail != "13. Nov 2026 at 00:00:57 • 4.0 MiB • wav" || es[0].Folder {
			t.Fatalf("the folder: %+v, entries %+v", res.Message, es)
		}
	}
	aum.ShowRecordings("Night Drive")
	if res, es = recordingsNow(t, c, "Other Song"); len(es) != 1 || es[0].Name != "Master - 90 bpm - 001" {
		t.Fatalf("the other folder: %+v, entries %+v", res.Message, es)
	}
	// No take was tapped on the way: a tap opens its PREVIEW.
	wantTouched(t, w, aum)
}

// A folder of recordings is listed whole, page by page, each take once and
// in the list's order.
func TestAUMFilesRecordingsListsTheWholeFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	aum.AddRecordings("Night Drive", takes(40)...)
	aum.SetCutRows(true)
	res, es := recordingsNow(t, c, "Night Drive")
	if !res.Verified || !strings.Contains(res.Message, "the whole list") || !strings.Contains(res.Message, "40 entries: Master - 120 bpm - 001, ") {
		t.Fatalf("recordings: %+v", res.Message)
	}
	if len(es) != 40 {
		t.Fatalf("%d entries: %q", len(es), entryNamesOf(es))
	}
	for i, e := range es {
		if want := takes(40)[i]; e.Name != want[0] || e.Detail != want[1] || e.Folder {
			t.Fatalf("entry %d = %+v, want %v", i, e, want)
		}
	}
	if n := countGestures(w, "actions"); n < 4 {
		t.Fatalf("%d drags for 40 rows: %v", n, w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// A session's folder the root lists beyond its first page is found there,
// with FILES left open and scrolled inside another folder.
func TestAUMFilesRecordingsFolderBeyondTheFirstPage(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	for i := range 30 {
		aum.AddRecordings(fmt.Sprintf("Song %02d", i), [2]string{fmt.Sprintf("Master - 120 bpm - %03d", i), "13. Nov 2026 at 00:12:57 • 4.0 MiB • wav"})
	}
	aum.AddRecordings("Long One", takes(40)...)
	aum.LeaveRecordingsOpen("Long One", 20)
	res, es := recordingsNow(t, c, "Song 25")
	if !res.Verified || len(es) != 1 || es[0].Name != "Master - 120 bpm - 025" {
		t.Fatalf("recordings: %+v, entries %q", res.Message, entryNamesOf(es))
	}
	// Up to the root, which the back arrow leaves headed by its directory's
	// name, and on to FILES' own root, whose Recordings row heads it "/".
	if countGestures(w, filesIcon) != 1 || countGestures(w, backArrow) != 2 {
		t.Fatalf("FILES was not opened from the menu anew and left through its own root: %v", w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// The root of Recordings is headed "/" only when FILES' own Recordings row
// opened it. Reached by the back arrow, or with FILES reopening on it, its
// header names the directory the recordings are kept in, as the header of a
// session's folder of that name would: only "/" is taken for the root.
func TestAUMFilesRecordingsRootNamedLikeAFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	aum.AddRecordings("Night Drive", takes(2)...)
	aum.AddRecordings(ipadtest.RecordingsRoot, takes(3)...)
	for _, last := range []string{"", ipadtest.RecordingsRoot, "Night Drive"} {
		aum.ShowRecordings(last)
		res, es := recordingsNow(t, c)
		if !res.Verified || len(es) != 2 || !es[0].Folder || !es[1].Folder || !strings.Contains(res.Message, "2 entries: Night Drive, "+ipadtest.RecordingsRoot) {
			t.Fatalf("FILES last in %q, the root: %+v, entries %q", last, res.Message, entryNamesOf(es))
		}
		if res, es = recordingsNow(t, c, ipadtest.RecordingsRoot); !res.Verified || len(es) != 3 {
			t.Fatalf("FILES last in %q, the folder: %+v, entries %q", last, res.Message, entryNamesOf(es))
		}
	}
	wantTouched(t, w, aum)
}

// A folder the root of Recordings does not hold is the error the stems tools
// read as "no takes yet": it must come from the root, whatever folder FILES
// showed last, and say what the root lists.
func TestAUMFilesRecordingsNoSuchFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	aum.AddRecordings("Night Drive", takes(2)...)
	aum.AddRecordings("Other Song", takes(1)...)
	for _, last := range []string{"", "Other Song"} {
		aum.ShowRecordings(last)
		_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "recordings", Path: []string{"New Song"}})
		var none *NoFolderError
		if !errors.As(err, &none) || none.Folder != "New Song" || none.In != "FILES → Recordings → /" || none.Entries != "Night Drive, Other Song" {
			t.Fatalf("FILES last in %q: err = %v (%+v)", last, err, none)
		}
	}
	// A take of the folder's name is no folder.
	aum.AddRecordings("", [2]string{"Loose Take", "13. Nov 2026 at 00:12:57 • 4.0 MiB • wav"})
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "recordings", Path: []string{"Loose Take"}})
	var none *NoFolderError
	if !errors.As(err, &none) || none.Entries != "Night Drive, Other Song, Loose Take" {
		t.Fatalf("err = %v (%+v)", err, none)
	}
	wantTouched(t, w, aum)
}

// From a folder of Sessions the way to Recordings is up to FILES' own root
// and its Recordings row: a session called Recordings in that folder is not
// that row, and a tap on it would load it.
func TestAUMFilesRecordingsFromSessions(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", [2]string{"Recordings", "12. Nov 2026 at 21:00:00"}, [2]string{"song 01", "12. Nov 2026 at 20:00:00"})
	aum.AddRecordings("Night Drive", takes(2)...)
	aum.Show("Songs")
	res, es := recordingsNow(t, c, "Night Drive")
	if !res.Verified || len(es) != 2 {
		t.Fatalf("recordings: %+v, entries %q", res.Message, entryNamesOf(es))
	}
	wantTouched(t, w, aum)
	if aum.Open() != "Untitled" {
		t.Fatalf("the open session is now %q", aum.Open())
	}

	// And back: list finds Sessions again from a folder of Recordings.
	if res, es := listNow(t, c, "Songs"); !res.Verified || len(es) != 2 {
		t.Fatalf("list after recordings: %+v, entries %q", res.Message, entryNamesOf(es))
	}
	wantTouched(t, w, aum)
}

// The stems tools ask a session's folder of Recordings only whether it holds a
// take that an earlier reading did not see. Sorted LAST OPENED, as on the
// iPad, the newest take is first: the first page tells it, and the list is
// not scrolled to its end (two reads more, about 8 s on the iPad, and a page
// more for every eight takes).
func TestAUMTakesReadsTheFirstPageOfANewestFirstFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	aum.AddRecordings("Night Drive", takes(40)...)
	for i := range 20 {
		aum.AddRecordings(fmt.Sprintf("Song %02d", i), takes(1)...)
	}
	aum.ShowRecordings("Song 07")
	got, err := c.AUMTakes(context.Background(), "Night Drive")
	if err != nil || !got.Top || got.Sort != ipadtest.SortLastOpened || got.Missing || len(got.Root) != 0 {
		t.Fatalf("takes = %+v, %v", got, err)
	}
	if len(got.Takes) != ipadtest.FilesRowsTop {
		t.Fatalf("%d takes, want the first page's %d: %q", len(got.Takes), ipadtest.FilesRowsTop, entryNamesOf(got.Takes))
	}
	for i, e := range got.Takes {
		if want := takes(40)[i]; e.Name != want[0] || e.Detail != want[1] || e.Folder {
			t.Fatalf("take %d = %+v, want %v", i, e, want)
		}
	}
	// Reached from the root as aum_files recordings reaches it, and nothing
	// dragged: the folder is on the root's first page and read at its own.
	if countGestures(w, filesIcon) != 1 || countGestures(w, backArrow) != 2 || countGestures(w, "actions") != 0 {
		t.Fatalf("FILES was not walked from the root, or a list was scrolled: %v", w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// Sorted by anything else the newest take need not be on the first page: the
// folder is read whole, to its end.
func TestAUMTakesReadsAnotherSortWhole(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	aum.AddRecordings("Night Drive", takes(40)...)
	aum.SetSort("NAME")
	aum.SetCutRows(true)
	got, err := c.AUMTakes(context.Background(), "Night Drive")
	if err != nil || got.Top || got.Sort != "NAME" || got.Missing || len(got.Takes) != 40 {
		t.Fatalf("takes = %+v (%d), %v", got.Sort, len(got.Takes), err)
	}
	if n := countGestures(w, "actions"); n < 4 {
		t.Fatalf("%d drags for 40 takes: %v", n, w.GestureLog())
	}
	wantTouched(t, w, aum)
}

// A folder the root does not hold is Missing once the root was read through,
// whatever FILES showed last, and every row of the root comes with it: the
// stems tools keep it for the stems after this one.
func TestAUMTakesMissingFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled")
	for i := range 30 {
		aum.AddRecordings(fmt.Sprintf("Song %02d", i), takes(1)...)
	}
	aum.AddRecordings("", [2]string{"New Song", "13. Nov 2026 at 00:12:57 • 4.0 MiB • wav"})
	aum.LeaveRecordingsOpen("Song 12", 0)
	got, err := c.AUMTakes(context.Background(), "New Song")
	if err != nil || !got.Missing || got.Top || len(got.Takes) != 0 || len(got.Root) != 31 {
		t.Fatalf("takes = %+v, %v", got, err)
	}
	// A take of the folder's name is a row of the root, and no folder.
	if got.Root[0].Name != "Song 00" || !got.Root[0].Folder || got.Root[30].Name != "New Song" || got.Root[30].Folder {
		t.Fatalf("the root's rows = %q", entryNamesOf(got.Root))
	}
	wantTouched(t, w, aum)
}

// "No such folder" is said only of a list read to its end: a root that goes
// on after the pages a reading is given can hold the folder further down.
func TestAUMFilesNoFolderOnlyForAWholeList(t *testing.T) {
	_, aum, c := filesFake(t, "Songs", "Untitled")
	for i := range filePages*8 + 40 {
		aum.AddRecordings(fmt.Sprintf("Song %03d", i), takes(1)...)
	}
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "recordings", Path: []string{"New Song"}})
	var none *NoFolderError
	if err == nil || errors.As(err, &none) || !strings.Contains(err.Error(), `went on after 60 pages without a folder "New Song"`) {
		t.Fatalf("recordings of a folder beyond what was read: %v", err)
	}
	if got, err := c.AUMTakes(context.Background(), "New Song"); err == nil || got.Missing {
		t.Fatalf("takes of a folder beyond what was read = %+v, %v", got, err)
	}
}

func renameNow(t *testing.T, c *Client, name, to string, path ...string) AUMResult {
	t.Helper()
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "rename", Path: path, Name: name, NewName: to, Confirm: true})
	if err != nil {
		t.Fatalf("rename %q to %q in %q: %v", name, to, path, err)
	}
	return res
}

func wantStrings(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// A session far down its folder is found on a later page; the row that is
// long-pressed is its own, on screen whole, and the rename is proved by the
// folder's list, which is read again.
func TestAUMFilesRenameBeyondTheFirstPage(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(60)...)
	aum.SetCutRows(true)
	res := renameNow(t, c, "song 45", "song 45 b", "Songs")
	if !res.Verified || !strings.Contains(res.Message, `renamed "song 45" to "song 45 b"`) || !strings.Contains(res.Message, "FILES → Sessions → /Songs") {
		t.Fatalf("rename: %+v", res.Message)
	}
	wantStrings(t, "held", aum.Held(), "/Songs/song 45")
	wantStrings(t, "renamed", aum.Renamed(), "/Songs/song 45 -> song 45 b")
	if got := aum.Sessions("Songs"); got[45] != "song 45 b" || got[44] != "song 44" || len(got) != 60 {
		t.Fatalf("the folder now: %q", got)
	}
	wantTouched(t, w, aum)
}

// FILES reopens in the folder it showed last, and every folder is titled
// SESSIONS: the session renamed is the one in the folder that was asked for,
// not one of its name in the folder FILES showed.
func TestAUMFilesRenameFromAnotherFolder(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(20)...)
	aum.AddRoot(songs(3)...)
	aum.AddFolder("Other", songs(10)...)
	aum.Show("Other")
	res := renameNow(t, c, "song 07", "song 07 b", "Songs")
	if !res.Verified {
		t.Fatalf("rename in the folder: %+v", res.Message)
	}
	wantStrings(t, "held", aum.Held(), "/Songs/song 07")

	// The root's session of that name, with FILES left open and scrolled in
	// a folder that holds one too.
	aum.LeaveOpen("Songs", 5)
	res = renameNow(t, c, "song 01", "song 01 b")
	if !res.Verified {
		t.Fatalf("rename in the root: %+v", res.Message)
	}
	wantStrings(t, "held", aum.Held(), "/Songs/song 07", "/song 01")
	wantStrings(t, "renamed", aum.Renamed(), "/Songs/song 07 -> song 07 b", "/song 01 -> song 01 b")
	if got := aum.Sessions("Other"); got[1] != "song 01" || got[7] != "song 07" {
		t.Fatalf("the other folder's sessions were touched: %q", got)
	}
	wantTouched(t, w, aum)
}

// A session the folder does not hold is an error that lists the folder, and
// no row is pressed: not a folder of that name either.
func TestAUMFilesRenameUnknown(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(20)...)
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "rename", Path: []string{"Songs"}, Name: "Nowhere", NewName: "Somewhere", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), `no session "Nowhere" in FILES → Sessions → /Songs (entries: song 00, song 01`) || !strings.Contains(err.Error(), "song 19)") {
		t.Fatalf("err = %v", err)
	}
	aum.AddFolder("Lab")
	_, err = c.AUMFiles(context.Background(), AUMRequest{Action: "rename", Name: "Lab", NewName: "Bench", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), `no session "Lab" in FILES → Sessions → / (entries: Songs, Songs, Lab)`) {
		t.Fatalf("err = %v", err)
	}
	_, err = c.AUMFiles(context.Background(), AUMRequest{Action: "rename", Path: []string{"Nowhere"}, Name: "song 01", NewName: "x", Confirm: true})
	var none *NoFolderError
	if !errors.As(err, &none) || none.Folder != "Nowhere" {
		t.Fatalf("err = %v", err)
	}
	wantStrings(t, "held", aum.Held())
	wantStrings(t, "renamed", aum.Renamed())
	wantTouched(t, w, aum)
}

// A rename AUM does not take is not a rename: the folder still lists the old
// name and not the new one, and the result says so.
func TestAUMFilesRenameThatDidNotHappen(t *testing.T) {
	_, aum, c := filesFake(t, "Songs", "Untitled", songs(30)...)
	aum.RefuseRenames()
	res := renameNow(t, c, "song 20", "song 20 b", "Songs")
	if res.Verified || !strings.Contains(res.Message, "NOT renamed") || !strings.Contains(res.Message, `still lists "song 20"`) {
		t.Fatalf("rename: verified %v, %+v", res.Verified, res.Message)
	}
	wantStrings(t, "held", aum.Held(), "/Songs/song 20")
	wantStrings(t, "renamed", aum.Renamed())
}

// The share sheet is drawn by another process and reports its cells from its
// own corner. The tool taps the entry that unfolds its actions and then
// Rename where they are on screen: a tap at a frame as the tree prints it
// lands beside the sheet, which closes it, and a tap on another of its cells
// sends the session somewhere.
func TestAUMFilesRenameThroughTheShareSheet(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(8)...)
	res := renameNow(t, c, "song 03", "song 03 b", "Songs")
	if !res.Verified {
		t.Fatalf("rename: %+v", res.Message)
	}
	steps := strings.Join(res.Steps, "\n")
	for _, want := range []string{
		"long-press session song 03",
		"tap " + ipadtest.ShareMore + ", which unfolds the sheet's actions at (1132,606)",
		"tap Rename in the share sheet of song 03 at (1004,456)",
		`fill text field 0 with "song 03 b"`,
	} {
		if !strings.Contains(steps, want) {
			t.Fatalf("no step %q in:\n%s", want, steps)
		}
	}
	if aum.SheetOpen() {
		t.Fatal("a sheet or dialog is left open")
	}
	wantTouched(t, w, aum)
}

// AUM answers a name another session of the folder has with an alert and
// renames nothing. The tool reads the alert, presses its OK, and says what
// AUM said: left open, the alert keeps FILES from opening.
func TestAUMFilesRenameOntoAnExistingName(t *testing.T) {
	w, aum, c := filesFake(t, "Songs", "Untitled", songs(30)...)
	res := renameNow(t, c, "song 20", "song 05", "Songs")
	if res.Verified || !strings.Contains(res.Message, `NOT renamed: FILES → Sessions → /Songs still lists "song 20", and lists "song 05" as well`) ||
		!strings.Contains(res.Message, `AUM answered "Sorry: `+ipadtest.NameExists+`"`) {
		t.Fatalf("rename: verified %v, %+v", res.Verified, res.Message)
	}
	wantStrings(t, "renamed", aum.Renamed())
	if got := aum.Sessions("Songs"); len(got) != 30 || got[20] != "song 20" || got[5] != "song 05" {
		t.Fatalf("the folder now: %q", got)
	}
	if aum.SheetOpen() {
		t.Fatal("the alert is left open")
	}
	wantTouched(t, w, aum)
}

// The share sheet names the file it is for, and the RENAME dialog opens
// holding that name. A sheet or a dialog for another session than the one
// that was held is closed, and nothing is typed or renamed.
func TestAUMFilesRenameSheetOfAnotherSession(t *testing.T) {
	for _, bare := range []bool{false, true} {
		w, aum, c := filesFake(t, "Songs", "Untitled", songs(8)...)
		aum.HoldSlips(bare)
		_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "rename", Path: []string{"Songs"}, Name: "song 03", NewName: "song 03 b", Confirm: true})
		want := `the share sheet that opened is for "song 04", not for "song 03": closed the share sheet, nothing renamed`
		if bare {
			// No caption to go by: the dialog's field is what tells.
			want = `the RENAME dialog holds "song 04", not "song 03": cancelled the dialog, nothing renamed`
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("sheet without caption %v: err = %v", bare, err)
		}
		wantStrings(t, "held", aum.Held(), "/Songs/song 03")
		wantStrings(t, "renamed", aum.Renamed())
		if aum.SheetOpen() {
			t.Fatalf("sheet without caption %v: a sheet or dialog is left open", bare)
		}
		wantTouched(t, w, aum)
	}
}
