package ipad

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// saveFake is an AUM whose SAVE SESSION button asks OVERWRITE? as AUM's does,
// with the session open loaded and sessions in folder, where SAVE saves ("" is
// the root of Sessions).
func saveFake(t *testing.T, folder, open string, sessions ...[2]string) (*ipadtest.WDA, *ipadtest.SessionsAUM, *Client) {
	t.Helper()
	old := AUMSaveSettle
	AUMSaveSettle = 50 * time.Millisecond
	t.Cleanup(func() { AUMSaveSettle = old })
	w := ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(w.Close)
	aum := ipadtest.NewSessionsAUM(w, folder, open, sessions...)
	t.Cleanup(func() {
		// A save reads the list: it loads no session and restores no state.
		if touched := aum.Touched(); len(touched) > 0 {
			t.Errorf("the save tapped %v", touched)
		}
	})
	return w, aum, New(w.URL)
}

func saveReport(t *testing.T, res AUMResult) AUMSave {
	t.Helper()
	rep, ok := res.Data.(AUMSave)
	if !ok {
		t.Fatalf("data = %#v", res.Data)
	}
	return rep
}

func saveNow(t *testing.T, c *Client, name string) AUMResult {
	t.Helper()
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: name, Confirm: true})
	if err != nil {
		t.Fatalf("save %q: %v", name, err)
	}
	return res
}

const (
	savedWas  = "12. Nov 2026 at 21:38:05"
	savePress = "tap 1080,320.5" // SAVE SESSION, at its centre
)

// A name the folder does not hold is saved by one press, and the proof is
// its row in the list.
func TestAUMFilesSaveNewName(t *testing.T) {
	w, aum, c := saveFake(t, "Songs", "Untitled", [2]string{"other", "11. Nov 2026 at 09:00:00"})
	res := saveNow(t, c, "scratch save")
	saved, ok := aum.Saved("scratch save")
	if !res.Verified || !ok || aum.Writes() != 1 {
		t.Fatalf("save: %+v; the stand-in holds %q (%v) after %d writes", res, saved, ok, aum.Writes())
	}
	rep := saveReport(t, res)
	if rep.Existed || rep.SavedBefore != "" || rep.SavedAfter != saved || rep.Folder != "/Songs" || !rep.PanelClosed {
		t.Fatalf("report = %+v, saved %q", rep, saved)
	}
	if !strings.Contains(res.Message, "as a new session") || !strings.Contains(res.Message, saved) {
		t.Fatalf("message = %q", res.Message)
	}
	// One press of the button: a second would ask to overwrite what the
	// first wrote.
	if n := countGestures(w, savePress); n != 1 {
		t.Fatalf("%d presses of SAVE SESSION in %v", n, w.GestureLog())
	}
}

// Over a session that exists SAVE SESSION asks OVERWRITE? for a second: both
// presses go out together, and the row's saved time moves.
func TestAUMFilesSaveOverExisting(t *testing.T) {
	w, aum, c := saveFake(t, "Songs", "scratch save", [2]string{"other", "11. Nov 2026 at 09:00:00"}, [2]string{"scratch save", savedWas})
	res := saveNow(t, c, "scratch save")
	saved, _ := aum.Saved("scratch save")
	if !res.Verified || saved == savedWas || aum.Writes() != 1 {
		t.Fatalf("save: %+v; the stand-in holds %q after %d writes", res, saved, aum.Writes())
	}
	rep := saveReport(t, res)
	if !rep.Existed || rep.SavedBefore != savedWas || rep.SavedAfter != saved || !rep.PanelClosed {
		t.Fatalf("report = %+v, saved %q", rep, saved)
	}
	if !strings.Contains(res.Message, "saved over") || !strings.Contains(res.Message, savedWas) || !strings.Contains(res.Message, saved) {
		t.Fatalf("message = %q", res.Message)
	}
	if n := countGestures(w, savePress); n != 2 {
		t.Fatalf("%d presses of SAVE SESSION in %v", n, w.GestureLog())
	}
	// The name was in the field already: nothing is typed, no Return.
	if n := countGestures(w, "click") + countGestures(w, "keys"); n != 0 {
		t.Fatalf("typed into the name field: %v", w.GestureLog())
	}
}

// Saving over another session than the open one types its name first.
func TestAUMFilesSaveOverAnotherName(t *testing.T) {
	_, aum, c := saveFake(t, "Songs", "Untitled", [2]string{"scratch save", savedWas})
	res := saveNow(t, c, "scratch save")
	if saved, _ := aum.Saved("scratch save"); !res.Verified || saved == savedWas || aum.Open() != "scratch save" {
		t.Fatalf("save: %+v; the stand-in holds %q, open %q", res, saved, aum.Open())
	}
}

// A confirm that comes after AUM's second is a first press again: nothing is
// written, the session's name in the menu is what it was before, and the
// tool says so instead of taking that name for proof.
func TestAUMFilesSaveThatDidNotLand(t *testing.T) {
	_, aum, c := saveFake(t, "Songs", "scratch save", [2]string{"scratch save", savedWas})
	aum.SetWindow(50 * time.Millisecond) // shorter than the gap between the two presses
	res := saveNow(t, c, "scratch save")
	if res.Verified {
		t.Fatalf("verified a save that wrote nothing: %+v", res)
	}
	if saved, _ := aum.Saved("scratch save"); saved != savedWas || aum.Writes() != 0 {
		t.Fatalf("the stand-in holds %q after %d writes", saved, aum.Writes())
	}
	rep := saveReport(t, res)
	if !rep.Existed || rep.SavedBefore != savedWas || rep.SavedAfter != savedWas || rep.PanelClosed {
		t.Fatalf("report = %+v", rep)
	}
	for _, want := range []string{"NOT saved", "still", savedWas, "stayed open"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q lacks %q", res.Message, want)
		}
	}
}

// FILES reopens in the folder it last showed, and every folder is titled
// SESSIONS: the list is taken up to the root and down the folder SAVE names,
// and the session's row is found beyond its first page, before the save and
// after.
func TestAUMFilesSaveFindsARowDownTheList(t *testing.T) {
	var sessions [][2]string
	for i := 0; i < 40; i++ {
		sessions = append(sessions, [2]string{fmt.Sprintf("song %02d", i), "10. Nov 2026 at 10:00:00"})
	}
	sessions = append(sessions, [2]string{"scratch save", savedWas})
	w, aum, c := saveFake(t, "Songs", "scratch save", sessions...)
	aum.ShowFolder(0)
	res := saveNow(t, c, "scratch save")
	if rep := saveReport(t, res); !res.Verified || rep.SavedBefore != savedWas || rep.SavedAfter == savedWas || rep.Folder != "/Songs" {
		t.Fatalf("save: %+v", res)
	}
	if aum.Writes() != 1 || countGestures(w, "actions") < 4 {
		t.Fatalf("%d writes, gestures %v", aum.Writes(), w.GestureLog())
	}
	// The back arrow, out of the folder FILES reopened in.
	if countGestures(w, "tap 830,30") == 0 {
		t.Fatalf("did not go up to the root: %v", w.GestureLog())
	}
}

// A session in the root of Sessions (SAVE's folder reads "/").
func TestAUMFilesSaveInTheRoot(t *testing.T) {
	_, aum, c := saveFake(t, "", "scratch save", [2]string{"other", "11. Nov 2026 at 09:00:00"}, [2]string{"scratch save", savedWas})
	res := saveNow(t, c, "scratch save")
	rep := saveReport(t, res)
	if !res.Verified || rep.Folder != "/" || rep.SavedBefore != savedWas || aum.Writes() != 1 {
		t.Fatalf("save: %+v", res)
	}
	res = saveNow(t, c, "another name")
	if rep := saveReport(t, res); !res.Verified || rep.Existed || aum.Writes() != 2 {
		t.Fatalf("save as new: %+v", res)
	}
}

// A list that runs on after a drag can carry rows past unread. That is seen
// (a page shares no row with the one before) and stops the save before
// anything is pressed: "not listed" would decide for one press, which over a
// session that exists writes nothing.
func TestAUMFilesSaveRefusesAListItCannotRead(t *testing.T) {
	var sessions [][2]string
	for i := 0; i < 80; i++ {
		sessions = append(sessions, [2]string{fmt.Sprintf("song %02d", i), "10. Nov 2026 at 10:00:00"})
	}
	w, aum, c := saveFake(t, "Songs", "Untitled", sessions...)
	aum.SetRunOn(8)
	_, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "not in the list", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), "scrolled past rows") || !strings.Contains(err.Error(), "not saving") {
		t.Fatalf("err = %v", err)
	}
	if aum.Writes() != 0 || countGestures(w, savePress) != 0 {
		t.Fatalf("%d writes, gestures %v", aum.Writes(), w.GestureLog())
	}
}

// What the report says for each outcome. A name AUM asks to overwrite
// although the list did not show it is not confirmed and not called saved:
// what it would overwrite is not known.
func TestAUMSaveMessages(t *testing.T) {
	for _, tc := range []struct {
		rep  AUMSave
		ok   bool
		want string
	}{
		{AUMSave{Folder: "/Songs", SavedAfter: "t1", PanelClosed: true}, true, "as a new session"},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0", SavedAfter: "t1", PanelClosed: true}, true, `from "t0" to "t1"`},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0", SavedAfter: "t0", Button: "SAVE SESSION"}, false, `still "t0"`},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0", PanelClosed: true}, false, "does not list it now"},
		{AUMSave{Folder: "/Songs", Button: "OVERWRITE?"}, false, "was not answered"},
		// The list missed the row before; the button told, or the panel
		// that stayed open.
		{AUMSave{Folder: "/Songs", SavedAfter: "t0", Button: "OVERWRITE?"}, false, "was not answered"},
		{AUMSave{Folder: "/Songs", SavedAfter: "t0", Button: "SAVE SESSION"}, false, "not taken for a save"},
		{AUMSave{Folder: "/Songs", PanelClosed: true}, false, `does not list "x" after`},
	} {
		if got := tc.rep.saved(); got != tc.ok {
			t.Errorf("%+v: saved = %v", tc.rep, got)
		}
		if msg := tc.rep.message("x"); !strings.Contains(msg, tc.want) || tc.ok == strings.Contains(msg, "NOT saved") {
			t.Errorf("%+v: message %q", tc.rep, msg)
		}
	}
}

// The folder and the button are read from the SAVE panel as AUM 1.4.8 draws
// it (captured on the iPad, a plugin window open under it).
func TestSavePanelCaptured(t *testing.T) {
	st := parseCaptured(t, "aum-plugin-window-save.json")
	p := st.Overlay("panel")
	shown, path, ok := saveFolder(p)
	if !ok || shown != "/Songs" || len(path) != 1 || path[0] != "Songs" {
		t.Fatalf("folder = %q %v %v", shown, path, ok)
	}
	for in, want := range map[string]int{"/": 0, "/Songs/Lab": 2} {
		a := ipadtest.MixerAUM()
		a.Save = &ipadtest.SavePanel{Name: "x", Folder: in, Button: "SAVE SESSION"}
		root, err := ParseSource(a.Source())
		if err != nil {
			t.Fatal(err)
		}
		st := ParseAUM(root, Size{Width: 1210, Height: 834})
		if shown, path, ok := saveFolder(st.Overlay("panel")); !ok || shown != in || len(path) != want {
			t.Errorf("folder %q = %q %v %v", in, shown, path, ok)
		}
	}
}

// With the SAVE panel open, FILES is opened from the menu: SAVE is no FILES
// view to back out of.
func TestAUMFilesListFromTheSavePanel(t *testing.T) {
	w, aum, c := saveFake(t, "", "scratch save", [2]string{"scratch save", savedWas})
	if res, err := c.AUMPanel(context.Background(), AUMRequest{Action: "open", Panel: "save"}); err != nil || !res.Verified {
		t.Fatalf("open save: %+v, %v", res, err)
	}
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "list"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "1 entries: scratch save") {
		t.Fatalf("list: %+v, %v (gestures %v)", res, err, w.GestureLog())
	}
	if aum.Writes() != 0 {
		t.Fatalf("%d writes", aum.Writes())
	}
}

func countGestures(w *ipadtest.WDA, g string) int {
	n := 0
	for _, l := range w.GestureLog() {
		if l == g {
			n++
		}
	}
	return n
}
