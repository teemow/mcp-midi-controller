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
// with the session open loaded and sessions in the folder "Songs".
func saveFake(t *testing.T, open string, sessions ...[2]string) (*ipadtest.WDA, *ipadtest.SessionsAUM, *Client) {
	t.Helper()
	old := AUMSaveSettle
	AUMSaveSettle = 10 * time.Millisecond
	t.Cleanup(func() { AUMSaveSettle = old })
	w := ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(w.Close)
	return w, ipadtest.NewSessionsAUM(w, "Songs", open, sessions...), New(w.URL)
}

func saveReport(t *testing.T, res AUMResult) AUMSave {
	t.Helper()
	rep, ok := res.Data.(AUMSave)
	if !ok {
		t.Fatalf("data = %#v", res.Data)
	}
	return rep
}

// A name the folder does not hold is saved by one press, and the proof is
// its row in the list.
func TestAUMFilesSaveNewName(t *testing.T) {
	w, aum, c := saveFake(t, "Untitled", [2]string{"other", "11. Nov 2026 at 09:00:00"})
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "scratch save", Confirm: true})
	if err != nil || !res.Verified {
		t.Fatalf("save: %+v, %v", res, err)
	}
	saved, ok := aum.Saved("scratch save")
	if !ok || aum.Writes() != 1 {
		t.Fatalf("the stand-in holds %q (%v) after %d writes", saved, ok, aum.Writes())
	}
	rep := saveReport(t, res)
	if rep.Existed || rep.SavedBefore != "" || rep.SavedAfter != saved || rep.Folder != "/Songs" {
		t.Fatalf("report = %+v, saved %q", rep, saved)
	}
	if !strings.Contains(res.Message, "as a new session") || !strings.Contains(res.Message, saved) {
		t.Fatalf("message = %q", res.Message)
	}
	// One press of the button, at its centre: a second would ask to
	// overwrite what the first wrote.
	if n := countGestures(w, "tap 1080,320.5"); n != 1 {
		t.Fatalf("%d presses of SAVE SESSION in %v", n, w.GestureLog())
	}
}

// Over a session that exists SAVE SESSION asks OVERWRITE? for a second: both
// presses go out together, and the row's saved time moves.
func TestAUMFilesSaveOverExisting(t *testing.T) {
	const was = "12. Nov 2026 at 21:38:05"
	w, aum, c := saveFake(t, "scratch save", [2]string{"other", "11. Nov 2026 at 09:00:00"}, [2]string{"scratch save", was})
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "scratch save", Confirm: true})
	if err != nil || !res.Verified {
		t.Fatalf("save: %+v, %v", res, err)
	}
	saved, _ := aum.Saved("scratch save")
	if saved == was || aum.Writes() != 1 {
		t.Fatalf("the stand-in holds %q after %d writes", saved, aum.Writes())
	}
	rep := saveReport(t, res)
	if !rep.Existed || rep.SavedBefore != was || rep.SavedAfter != saved {
		t.Fatalf("report = %+v, saved %q", rep, saved)
	}
	if !strings.Contains(res.Message, "saved over") || !strings.Contains(res.Message, was) || !strings.Contains(res.Message, saved) {
		t.Fatalf("message = %q", res.Message)
	}
	if n := countGestures(w, "tap 1080,320.5"); n != 2 {
		t.Fatalf("%d presses of SAVE SESSION in %v", n, w.GestureLog())
	}
	// The name was in the field already: nothing is typed, no Return.
	if n := countGestures(w, "click") + countGestures(w, "keys"); n != 0 {
		t.Fatalf("typed into the name field: %v", w.GestureLog())
	}
}

// Saving over another session than the open one types its name first.
func TestAUMFilesSaveOverAnotherName(t *testing.T) {
	const was = "12. Nov 2026 at 21:38:05"
	_, aum, c := saveFake(t, "Untitled", [2]string{"scratch save", was})
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "scratch save", Confirm: true})
	if err != nil || !res.Verified {
		t.Fatalf("save: %+v, %v", res, err)
	}
	if saved, _ := aum.Saved("scratch save"); saved == was || aum.Open() != "scratch save" {
		t.Fatalf("the stand-in holds %q, open %q", saved, aum.Open())
	}
}

// A confirm that comes after AUM's second is a first press again: nothing is
// written, the session's name in the menu is what it was before, and the
// tool says so instead of taking that name for proof.
func TestAUMFilesSaveThatDidNotLand(t *testing.T) {
	const was = "12. Nov 2026 at 21:38:05"
	_, aum, c := saveFake(t, "scratch save", [2]string{"scratch save", was})
	aum.SetWindow(50 * time.Millisecond) // shorter than the gap between the two presses
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "scratch save", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified {
		t.Fatalf("verified a save that wrote nothing: %+v", res)
	}
	if saved, _ := aum.Saved("scratch save"); saved != was || aum.Writes() != 0 {
		t.Fatalf("the stand-in holds %q after %d writes", saved, aum.Writes())
	}
	rep := saveReport(t, res)
	if !rep.Existed || rep.SavedBefore != was || rep.SavedAfter != was {
		t.Fatalf("report = %+v", rep)
	}
	for _, want := range []string{"NOT saved", "still", was} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q lacks %q", res.Message, want)
		}
	}
}

// The session's row is found beyond the list's first page, before and after.
func TestAUMFilesSaveFindsARowDownTheList(t *testing.T) {
	const was = "12. Nov 2026 at 21:38:05"
	var sessions [][2]string
	for i := 0; i < 40; i++ {
		sessions = append(sessions, [2]string{fmt.Sprintf("song %02d", i), "10. Nov 2026 at 10:00:00"})
	}
	sessions = append(sessions, [2]string{"scratch save", was})
	w, aum, c := saveFake(t, "scratch save", sessions...)
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "save", Name: "scratch save", Confirm: true})
	if err != nil || !res.Verified {
		t.Fatalf("save: %+v, %v", res, err)
	}
	if rep := saveReport(t, res); rep.SavedBefore != was || rep.SavedAfter == was {
		t.Fatalf("report = %+v", rep)
	}
	if aum.Writes() != 1 || countGestures(w, "actions") == 0 {
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
		{AUMSave{Folder: "/Songs", SavedAfter: "t1"}, true, "as a new session"},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0", SavedAfter: "t1"}, true, `from "t0" to "t1"`},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0", SavedAfter: "t0", Button: "SAVE SESSION"}, false, `still "t0"`},
		{AUMSave{Folder: "/Songs", Existed: true, SavedBefore: "t0"}, false, "does not list it now"},
		{AUMSave{Folder: "/Songs", Button: "OVERWRITE?"}, false, "was not answered"},
		// The list missed the row before; the button told.
		{AUMSave{Folder: "/Songs", SavedAfter: "t0", Button: "OVERWRITE?"}, false, "was not answered"},
		{AUMSave{Folder: "/Songs", Button: "SAVE SESSION"}, false, `does not list "x" after`},
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

func countGestures(w *ipadtest.WDA, g string) int {
	n := 0
	for _, l := range w.GestureLog() {
		if l == g {
			n++
		}
	}
	return n
}
