package ipad

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

const probeBundle = "TEAM.com.teemow.auv3probe"

// probeFake starts a fake WebDriverAgent with the probe app's window on it.
func probeFake(t *testing.T, app *ipadtest.ProbeApp) *Client {
	t.Helper()
	settle := ProbeSettle
	ProbeSettle = time.Millisecond
	t.Cleanup(func() { ProbeSettle = settle })
	w := ipadtest.New(ipadtest.MixerAUM().Source())
	w.Apps = map[string]*ipadtest.ProbeApp{probeBundle: app}
	t.Cleanup(w.Close)
	return New(w.URL)
}

// inProbe runs fn in a session bound to the probe app.
func inProbe(t *testing.T, c *Client, fn func(s *Session)) {
	t.Helper()
	err := c.WithApp(context.Background(), probeBundle, func(s *Session) error {
		fn(s)
		return nil
	})
	if err != nil {
		t.Fatalf("session with the probe app: %v", err)
	}
}

func TestParseProbeScreen(t *testing.T) {
	app := &ipadtest.ProbeApp{
		Units: "syncing 2/184…",
		Files: map[string][]ipadtest.ProbeFile{
			"":      {{Name: "open mic.aumproj"}},
			"Songs": {{Name: "Aardvark.aumproj", State: ipadtest.ProbeConflictState}, {Name: "Night Drive.aumproj", Info: "session · 156 KB · 12. Nov 2026 at 02:06", State: ipadtest.ProbeConflictState}, {Name: "Zebra.aumproj", State: "pulling…"}},
			"Sets":  {{Name: "Live.aumproj"}},
		},
		// An arrival's button ends like a file row's label and is none.
		Arrivals: []string{"open mic.aumproj"},
	}
	c := probeFake(t, app)

	inProbe(t, c, func(s *Session) {
		// As after a start: the audio units tab, sending its units.
		scr, err := s.ProbeScreen(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if scr.Tab != ProbeUnitsTab || !scr.UnitsSyncing || scr.UnitsDone != 2 || scr.UnitsTotal != 184 {
			t.Errorf("audio units tab read as %+v", scr)
		}
		if scr.Browser || len(scr.Files)+len(scr.Folders) != 0 {
			t.Errorf("the audio units tab has no browser, read %d file(s), %d folder(s)", len(scr.Files), len(scr.Folders))
		}
		if scr.SessionsTab == nil {
			t.Fatal("no sessions tab button found")
		}
	})

	// The root of the sessions tab.
	app.Set(func(p *ipadtest.ProbeApp) { p.Tab = ProbeSessionsTab })
	inProbe(t, c, func(s *Session) {
		scr, _ := s.ProbeScreen(context.Background())
		if scr.Tab != ProbeSessionsTab || scr.Status != "2 conflicts — pick a side below" || scr.Cycle || scr.Sync == nil {
			t.Errorf("sessions tab header read as tab %q, status %q, cycle %v, sync %v", scr.Tab, scr.Status, scr.Cycle, scr.Sync)
		}
		if !scr.Browser || scr.Folder != "" || scr.Back != nil {
			t.Errorf("root read as folder %q, back %v", scr.Folder, scr.Back)
		}
		var folders []string
		for _, f := range scr.Folders {
			folders = append(folders, f.Label)
		}
		if want := []string{"Sets, 1", "Songs, has changes that need attention, 3"}; !slices.Equal(folders, want) {
			t.Errorf("folder rows = %q, want %q", folders, want)
		}
		if len(scr.Files) != 1 || scr.Files[0].Name != "open mic.aumproj" {
			t.Errorf("root files = %+v, want the one file and not its arrival", scr.Files)
		}
	})

	// A folder: every row with its badge, a conflict with its own button.
	app.Set(func(p *ipadtest.ProbeApp) { p.Path, p.Cycle = []string{"Songs"}, true })
	inProbe(t, c, func(s *Session) {
		scr, _ := s.ProbeScreen(context.Background())
		if scr.Folder != "Songs" || scr.Back == nil || !scr.Cycle {
			t.Errorf("folder read as %q, back %v, cycle %v", scr.Folder, scr.Back, scr.Cycle)
		}
		if len(scr.Files) != 3 {
			t.Fatalf("read %d file rows, want 3", len(scr.Files))
		}
		a, nd, z := scr.File("Aardvark.aumproj"), scr.File("Night Drive.aumproj"), scr.File("Zebra.aumproj")
		if !a.Conflict() || !nd.Conflict() || z.Conflict() || z.State != "pulling…" {
			t.Errorf("states = %q, %q, %q", a.State, nd.State, z.State)
		}
		if nd.Size() != "156 KB" || a.Size() != "105 KB" {
			t.Errorf("sizes = %q, %q", nd.Size(), a.Size())
		}
		if a.KeepController == nil || nd.KeepController == nil || z.KeepController != nil {
			t.Fatalf("keep controller buttons = %v, %v, %v", a.KeepController, nd.KeepController, z.KeepController)
		}
		// Each row's button lies below its own row and above the next.
		if !(a.Rect.Y < a.KeepController.Y && a.KeepController.Y < nd.Rect.Y && nd.Rect.Y < nd.KeepController.Y && nd.KeepController.Y < z.Rect.Y) {
			t.Errorf("buttons do not belong to their rows: %v %v %v %v %v", a.Rect, a.KeepController, nd.Rect, nd.KeepController, z.Rect)
		}
		if scr.File("Live.aumproj") != nil {
			t.Error("a file of another folder is listed")
		}
	})
}

func TestProbeByteLabel(t *testing.T) {
	for bytes, want := range map[int64]string{
		512: "512 B", 107067: "105 KB", 159744: "156 KB", 852201: "832 KB",
		1048575: "1024 KB", 1569631: "1.5 MB", 5213478: "5.0 MB",
	} {
		if got := ProbeByteLabel(bytes); got != want {
			t.Errorf("ProbeByteLabel(%d) = %q, want %q", bytes, got, want)
		}
	}
}

// TestProbeOpenFolder walks the app's browser to a folder: from the audio
// units tab, from another folder, and not at all when it is there already.
func TestProbeOpenFolder(t *testing.T) {
	files := map[string][]ipadtest.ProbeFile{
		"Songs":         {{Name: "Night Drive.aumproj"}},
		"Sets/Live":     {{Name: "Tour.aumproj"}},
		"Sets/Old/Live": {{Name: "Then.aumproj"}},
	}
	ctx := context.Background()

	t.Run("from a start", func(t *testing.T) {
		app := &ipadtest.ProbeApp{Files: files}
		inProbe(t, probeFake(t, app), func(s *Session) {
			scr, err := s.ProbeOpenFolder(ctx, "Songs", true)
			if err != nil || scr.Folder != "Songs" || scr.File("Night Drive.aumproj") == nil {
				t.Fatalf("open Songs: %v, folder %q", err, scr.Folder)
			}
		})
		if got, want := app.Actions(), []string{"tab aum sessions", "folder Songs"}; !slices.Equal(got, want) {
			t.Errorf("the app was asked %q, want %q", got, want)
		}
	})

	t.Run("there already", func(t *testing.T) {
		app := &ipadtest.ProbeApp{Files: files, Tab: ProbeSessionsTab, Path: []string{"Songs"}}
		inProbe(t, probeFake(t, app), func(s *Session) {
			if scr, err := s.ProbeOpenFolder(ctx, "Songs", true); err != nil || scr.Folder != "Songs" {
				t.Fatalf("open Songs: %v, folder %q", err, scr.Folder)
			}
		})
		// A tap on the tab that is shown would send the browser to its root.
		if got := app.Actions(); len(got) != 0 {
			t.Errorf("the app was asked %q although it showed the folder", got)
		}
	})

	t.Run("from the root when the name is not enough", func(t *testing.T) {
		// The browser shows "Live" of Sets/Old; Sets/Live is another folder.
		app := &ipadtest.ProbeApp{Files: files, Tab: ProbeSessionsTab, Path: []string{"Sets", "Old", "Live"}}
		inProbe(t, probeFake(t, app), func(s *Session) {
			scr, err := s.ProbeOpenFolder(ctx, "Sets/Live", true)
			if err != nil || scr.File("Tour.aumproj") == nil {
				t.Fatalf("open Sets/Live: %v, files %+v", err, scr.Files)
			}
		})
		if got, want := app.Actions(), []string{"back", "back", "back", "folder Sets", "folder Sets/Live"}; !slices.Equal(got, want) {
			t.Errorf("the app was asked %q, want %q", got, want)
		}
	})

	t.Run("without trust", func(t *testing.T) {
		app := &ipadtest.ProbeApp{Files: files, Tab: ProbeSessionsTab, Path: []string{"Songs"}}
		inProbe(t, probeFake(t, app), func(s *Session) {
			if scr, err := s.ProbeOpenFolder(ctx, "Songs", false); err != nil || scr.Folder != "Songs" {
				t.Fatalf("open Songs: %v, folder %q", err, scr.Folder)
			}
		})
		if got, want := app.Actions(), []string{"back", "folder Songs"}; !slices.Equal(got, want) {
			t.Errorf("the app was asked %q, want %q", got, want)
		}
	})

	t.Run("a pinned folder of the same name", func(t *testing.T) {
		// The root lists a pinned folder twice, under one label.
		app := &ipadtest.ProbeApp{Files: files, Tab: ProbeSessionsTab, Pinned: "Songs"}
		inProbe(t, probeFake(t, app), func(s *Session) {
			if scr, err := s.ProbeOpenFolder(ctx, "Songs", true); err != nil || scr.Folder != "Songs" {
				t.Fatalf("open Songs: %v, folder %q", err, scr.Folder)
			}
		})
		if got, want := app.Actions(), []string{"folder Songs"}; !slices.Equal(got, want) {
			t.Errorf("the app was asked %q, want %q", got, want)
		}
	})

	t.Run("no such folder", func(t *testing.T) {
		app := &ipadtest.ProbeApp{Files: files, Tab: ProbeSessionsTab}
		inProbe(t, probeFake(t, app), func(s *Session) {
			_, err := s.ProbeOpenFolder(ctx, "Elsewhere", true)
			if !errors.Is(err, ErrProbeBrowser) || !strings.Contains(err.Error(), `lists no folder "Elsewhere"`) {
				t.Fatalf("open a missing folder: %v", err)
			}
		})
	})

	t.Run("no folder linked", func(t *testing.T) {
		app := &ipadtest.ProbeApp{Unlinked: true}
		inProbe(t, probeFake(t, app), func(s *Session) {
			_, err := s.ProbeOpenFolder(ctx, "Songs", true)
			if !errors.Is(err, ErrProbeBrowser) || !strings.Contains(err.Error(), "AUM's folder is not linked") {
				t.Fatalf("open a folder without a browser: %v", err)
			}
		})
	})
}

// TestClickButtonAt checks that of several buttons with one label only the
// one at the frame given is clicked, wherever it lies, and none when the row
// has moved since the tree was read.
func TestClickButtonAt(t *testing.T) {
	// Twelve rows: the last ones lie below the window's edge.
	var songs []ipadtest.ProbeFile
	for _, name := range strings.Fields("A B C D E F G H I J K L") {
		songs = append(songs, ipadtest.ProbeFile{Name: name + ".aumproj", State: ipadtest.ProbeConflictState})
	}
	app := &ipadtest.ProbeApp{Tab: ProbeSessionsTab, Path: []string{"Songs"}, Files: map[string][]ipadtest.ProbeFile{"Songs": songs}}
	ctx := context.Background()
	inProbe(t, probeFake(t, app), func(s *Session) {
		scr, err := s.ProbeScreen(ctx)
		if err != nil {
			t.Fatal(err)
		}
		k := scr.File("K.aumproj")
		if k == nil || k.KeepController == nil || k.KeepController.Y < 834 {
			t.Fatalf("row K = %+v, want it below the window's edge with its button", k)
		}
		if ok, err := s.ClickButtonAt(ctx, ProbeKeepController, *k.KeepController); err != nil || !ok {
			t.Fatalf("click K's keep controller: %v, %v", ok, err)
		}
		// K's row is in sync now and every row below it has moved up: the
		// frame read for L before is no button's any more.
		l := scr.File("L.aumproj")
		if ok, err := s.ClickButtonAt(ctx, ProbeKeepController, *l.KeepController); err != nil || ok {
			t.Fatalf("click at L's old frame: %v, %v, want nothing clicked", ok, err)
		}
	})
	if got, want := app.Actions(), []string{"keep controller Songs/K.aumproj"}; !slices.Equal(got, want) {
		t.Errorf("the app was asked %q, want %q", got, want)
	}
}
