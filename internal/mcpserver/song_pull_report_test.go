package mcpserver

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/aumreceiver"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// The session of toolSong as the probe app's sync report names it.
const nightDriveFile = "Songs/Night Drive.aumproj"

// report posts a sync report to the daemon's LAN receiver, as the probe app
// does after a sync cycle: the document goes over the wire, through the
// receiver's route and into the server's callback. doc overrides the keys of
// a report that lists nothing and is of the staging rev as it stands.
func (r *songRig) report(doc map[string]any) {
	url := r.receiver()
	full := map[string]any{
		"v": 1, "app": "auv3-probe 0.4.0", "at": time.Now().UTC().Format(time.RFC3339),
		"rev": aum.StagingRev(config.AUMSessionsDir()), "synced": 3,
		"conflicts": []string{}, "errors": []any{}, "in_flight": []any{},
	}
	maps.Copy(full, doc)
	body, err := json.Marshal(full)
	if err != nil {
		r.t.Errorf("encode the sync report: %v", err)
		return
	}
	resp, err := http.Post(url+"/aum-session/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		r.t.Errorf("post the sync report: %v", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		r.t.Errorf("the sync report was answered %d, want 204", resp.StatusCode)
	}
}

// receiver starts the daemon's LAN receiver, wired to the server as the daemon
// wires it, and returns its address. It is closed with the test, after every
// cleanup registered later: a test whose app runs beside it calls this first.
func (r *songRig) receiver() string {
	r.lanOnce.Do(func() {
		r.lan = httptest.NewServer(aumreceiver.Handler(config.AUMSessionsDir(), nil, r.s.OnAUMSessionDownloaded, r.s.OnAUMSyncReport))
		r.t.Cleanup(r.lan.Close)
	})
	return r.lan.URL
}

// fetch plays the app's download of a staged file.
func (r *songRig) fetch(file string) { r.s.OnAUMSessionDownloaded(file) }

// reportingApp puts a probe app with the sync report on the rig's iPad: it
// names its conflicts and its folder in accessibility identifiers, and cycle
// is what its sync does when the daemon starts it. Unlike the app of
// songRig.probeApp it fetches nothing by itself.
func (r *songRig) reportingApp(files map[string][]ipadtest.ProbeFile, cycle func()) *ipadtest.ProbeApp {
	r.t.Helper()
	app := r.probeApp(files)
	app.Set(func(p *ipadtest.ProbeApp) {
		p.IDs = true
		p.OnSync = cycle
		p.OnKeepController = nil
	})
	return app
}

// keeps lists the sides picked in the app.
func keeps(app *ipadtest.ProbeApp) (out []string) {
	for _, a := range app.Actions() {
		if strings.HasPrefix(a, "keep ") {
			out = append(out, a)
		}
	}
	return out
}

// TestLoadSongPullEndsOnTheSyncReport checks what the pull step makes of the
// app's report of its sync cycle: a conflict and an error end it at once and
// are named, a copy still queued keeps it waiting, a session in none of the
// report's lists is held by the app, and a report says nothing of a session
// staged after the rev it is of.
func TestLoadSongPullEndsOnTheSyncReport(t *testing.T) {
	const savedInAUM = "session · 9.9 MB · 12. Nov 2026 at 02:06"
	type rig struct {
		*songRig
		app *ipadtest.ProbeApp
	}
	// staged is the rev the song's session was staged at.
	staged := func() int64 { return loadSongSessions()["night_drive"].Rev }
	for name, c := range map[string]struct {
		// state is the badge of the session's row in the app.
		state string
		// cycle is the app's sync cycle, started by the daemon's tap.
		cycle func(r rig)
		// timeout shortens the step's time for a pull that is not to arrive.
		timeout time.Duration
		ok      bool
		want    []string
		not     []string
		// quick: the step ended on the report, well before its time ran out.
		quick bool
		check func(t *testing.T, r rig)
	}{
		"a conflict": {
			state: ipadtest.ProbeConflictState,
			cycle: func(r rig) { r.report(map[string]any{"conflicts": []string{"Songs/Aardvark.aumproj", nightDriveFile}}) },
			want: []string{
				`2. pull    FAIL  the iPad did not pull the session: the AUv3 Probe app holds Songs/Night Drive.aumproj as a conflict ("conflict — both sides changed") and copies it neither way until a side is picked.`,
				"The daemon has no record of a file the iPad fetched under this name",
				`in the AUv3 Probe app (the "aum sessions" tab, folder "Songs"), tap "keep controller" on the row "Night Drive.aumproj" and on no other, and load_song again`,
			},
			quick: true,
			check: func(t *testing.T, r rig) {
				if got := keeps(r.app); len(got) != 0 {
					t.Errorf("the daemon picked a side: %q", got)
				}
				// The row named its file: the browser was not walked again.
				if got := r.app.Actions(); slices.Contains(got, "back") {
					t.Errorf("the app was asked %q, want no walk from the browser's root", got)
				}
			},
		},
		"an error": {
			cycle: func(r rig) {
				r.report(map[string]any{"errors": []map[string]string{
					{"path": "Songs/Aardvark.aumproj", "error": "the request timed out"},
					{"path": nightDriveFile, "error": "mcp-midi-controller returned HTTP 500: internal error"},
				}})
			},
			want: []string{
				`2. pull    FAIL  the iPad did not pull the session: the AUv3 Probe app could not copy Songs/Night Drive.aumproj: "mcp-midi-controller returned HTTP 500: internal error"`,
				"next: the AUv3 Probe app tries the copy again at its next sync",
			},
			quick: true,
		},
		"a pending copy that arrives": {
			cycle: func(r rig) {
				// Before the first copy the app says what is queued; the file
				// follows, and the report that ends the cycle.
				r.report(map[string]any{"in_flight": []map[string]string{{"path": nightDriveFile, "to": "device"}}})
				go func() {
					time.Sleep(150 * time.Millisecond)
					r.fetch(nightDriveFile)
					r.report(nil)
				}()
			},
			ok:   true,
			want: []string{"2. pull    ok    the iPad pulled the session (the probe app was brought to the front)"},
		},
		"a pending copy that does not arrive": {
			timeout: 400 * time.Millisecond,
			cycle: func(r rig) {
				r.report(map[string]any{
					"in_flight":   []map[string]string{{"path": nightDriveFile, "to": "device"}},
					"audio_units": map[string]any{"state": "syncing", "total": 96, "synced": 60, "failed": 1},
				})
			},
			want: []string{
				"2. pull    FAIL  the iPad did not pull the session within 400ms (the probe app was brought to the front; its last sync report, 0s ago, lists Songs/Night Drive.aumproj as queued to be fetched",
				"it was sending its audio units to the daemon (60 of 96 sent, 1 failed), which it does only while it is in front",
				"next: the AUv3 Probe app was on it",
			},
		},
		"queued to be sent, units done": {
			timeout: 400 * time.Millisecond,
			cycle: func(r rig) {
				r.report(map[string]any{
					"in_flight": []map[string]string{{"path": nightDriveFile, "to": "controller"}},
					// Done although one unit never made it.
					"audio_units": map[string]any{"state": "done", "total": 96, "synced": 95, "failed": 1},
				})
			},
			want: []string{"lists Songs/Night Drive.aumproj as queued to be sent to the daemon: the app takes the copy in AUM's folder for the newer one"},
			not:  []string{"audio units"},
		},
		"units waiting": {
			timeout: 400 * time.Millisecond,
			cycle: func(r rig) {
				r.report(map[string]any{
					"in_flight":   []map[string]string{{"path": nightDriveFile, "to": "device"}},
					"audio_units": map[string]any{"state": "waiting", "total": 96, "synced": 60, "failed": 0},
				})
			},
			want: []string{"its audio units' sync is waiting: 60 of 96 sent and no run under way"},
		},
		"a clean report": {
			// The app holds the bytes and the daemon served no download of
			// them: the report is all that says so.
			cycle: func(r rig) { r.report(nil) },
			ok:    true,
			quick: true,
			want: []string{
				"2. pull    ok    the iPad holds the session: the AUv3 Probe app's sync report (staging rev ",
				"lists Songs/Night Drive.aumproj as in sync with the staged file. The daemon saw no download of it",
			},
			check: func(t *testing.T, r rig) {
				if rec := loadSongSessions()["night_drive"]; rec.Pulled != rec.SHA256 || len(rec.Served) != 1 {
					t.Errorf("the record has the iPad hold %q (served %v), the session is %q", rec.Pulled, rec.Served, rec.SHA256)
				}
			},
		},
		"a clean report after the app's own uploads": {
			// The app uploaded two files in its cycle: the dir's rev moved on
			// and the report is of the manifest the cycle went by, which
			// listed the session.
			cycle: func(r rig) {
				at := staged()
				aum.BumpStagingRev(config.AUMSessionsDir())
				aum.BumpStagingRev(config.AUMSessionsDir())
				r.report(map[string]any{"rev": at})
			},
			ok:    true,
			quick: true,
			want:  []string{"2. pull    ok    the iPad holds the session"},
		},
		"a report of an earlier rev": {
			// A cycle that went by a manifest from before the session was
			// staged says nothing of it, whatever it lists.
			timeout: 400 * time.Millisecond,
			state:   ipadtest.ProbeConflictState,
			cycle: func(r rig) {
				r.report(map[string]any{"rev": staged() - 1, "conflicts": []string{nightDriveFile}})
			},
			want: []string{
				"2. pull    FAIL  the iPad did not pull the session within 400ms (the probe app was brought to the front; its last sync report, 0s ago, is of staging rev ",
				"no sync cycle of the app has listed the daemon's files since",
				"next: open the AUv3 Probe app on the iPad",
			},
			check: func(t *testing.T, r rig) {
				if got := keeps(r.app); len(got) != 0 {
					t.Errorf("the daemon picked a side on a report that did not cover the session: %q", got)
				}
			},
		},
		"an earlier rev, then the cycle that saw it": {
			cycle: func(r rig) {
				r.report(map[string]any{"rev": staged() - 1, "errors": []map[string]string{{"path": nightDriveFile, "error": "of an earlier cycle"}}})
				go func() {
					time.Sleep(100 * time.Millisecond)
					r.report(map[string]any{"in_flight": []map[string]string{{"path": nightDriveFile, "to": "device"}}})
					r.fetch(nightDriveFile)
					r.report(nil)
				}()
			},
			ok:   true,
			want: []string{"2. pull    ok    the iPad pulled the session"},
			not:  []string{"of an earlier cycle"},
		},
		"the app mirrors nothing": {
			// Of whatever rev: the app says of itself that it copies nothing.
			cycle: func(r rig) { r.report(map[string]any{"rev": 0, "synced": 0, "error": "no aum folder linked yet"}) },
			quick: true,
			want: []string{
				`2. pull    FAIL  the iPad did not pull the session: the AUv3 Probe app reports that its sync copies no file for now: "no aum folder linked yet"`,
				"next: look at the AUv3 Probe app's \"aum sessions\" tab: AUM's folder has to be linked there and readable",
			},
		},
		"the staged file was replaced": {
			// In sync, and no longer the session authored: the app sent its
			// own copy over it.
			cycle: func(r rig) {
				data, err := aum.Template().Encode()
				if err != nil {
					r.t.Errorf("encode a session: %v", err)
					return
				}
				if _, _, err := stageAUMFile("Songs/Night Drive", ".aumproj", data); err != nil {
					r.t.Errorf("stage the app's copy: %v", err)
				}
				r.report(nil)
			},
			quick: true,
			want: []string{
				"2. pull    FAIL  the iPad did not pull the session: the staged Songs/Night Drive.aumproj is no longer the file authored for the song.",
				"next: what AUM's folder holds under this name may be a session saved in AUM",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sr := newSongRig(t)
			r := rig{songRig: sr}
			if c.timeout > 0 {
				songPullTimeout = c.timeout // restored by the rig's cleanup
			}
			r.app = sr.reportingApp(map[string][]ipadtest.ProbeFile{
				"Songs": {{Name: "Aardvark.aumproj", State: ipadtest.ProbeConflictState}, {Name: "Night Drive.aumproj", State: c.state, Info: savedInAUM}},
				"Sets":  {{Name: "Night Drive.aumproj", State: ipadtest.ProbeConflictState}},
			}, func() { c.cycle(r) })
			if res := call(t, sr.s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
				t.Fatalf("save: %s", resultText(res))
			}
			if c.ok {
				sr.aumLoads("Songs", "Night Drive", nightDriveStrips)
			}
			start := time.Now()
			res := call(t, sr.s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
			took := time.Since(start)
			text := resultText(res)
			if res.IsError == c.ok {
				t.Fatalf("load ok = %v, want %v:\n%s", !res.IsError, c.ok, text)
			}
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("report lacks %q:\n%s", want, text)
				}
			}
			for _, not := range c.not {
				if strings.Contains(text, not) {
					t.Errorf("report has %q:\n%s", not, text)
				}
			}
			if c.quick && !c.ok && took > songPullTimeout/2 {
				t.Errorf("the load took %s of the pull step's %s although the app's report said why", took, songPullTimeout)
			}
			if !c.ok && sr.wda.Gestures() != 0 {
				t.Errorf("AUM was touched (%d gestures) although the session never arrived", sr.wda.Gestures())
			}
			if c.check != nil {
				c.check(t, r)
			}
		})
	}
}

// TestLoadSongResolvesAReportedConflict checks a conflict the app reports is
// resolved when the copy in AUM's folder is a file the daemon staged: the
// daemon presses the button that names the session's file, in an app that
// names it, and the button after the session's row in a browser walked from
// its root in one that does not. A bar that names the wrong folder leads to
// no other row.
func TestLoadSongResolvesAReportedConflict(t *testing.T) {
	for name, c := range map[string]struct {
		ids bool
		// stale has the browser show Sets/Songs under a bar that names Songs.
		stale bool
		// walked: the browser was walked from its root before the tap.
		walked bool
	}{
		"the button names the file":   {ids: true},
		"an app without identifiers":  {walked: true},
		"a bar that names a namesake": {ids: true, stale: true, walked: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSongRig(t)
			s := r.s
			var app *ipadtest.ProbeApp
			conflict := false
			app = r.reportingApp(map[string][]ipadtest.ProbeFile{
				"Songs":      {{Name: "Aardvark.aumproj", State: ipadtest.ProbeConflictState}, {Name: "Night Drive.aumproj"}, {Name: "Zebra.aumproj", State: ipadtest.ProbeConflictState}},
				"Sets/Songs": {{Name: "Night Drive.aumproj", State: ipadtest.ProbeConflictState}},
			}, func() {
				if conflict {
					r.report(map[string]any{"conflicts": []string{"Sets/Songs/Night Drive.aumproj", "Songs/Aardvark.aumproj", nightDriveFile, "Songs/Zebra.aumproj"}})
					return
				}
				r.fetch(nightDriveFile)
				r.report(map[string]any{"conflicts": []string{"Sets/Songs/Night Drive.aumproj", "Songs/Aardvark.aumproj", "Songs/Zebra.aumproj"}})
			})
			app.Set(func(p *ipadtest.ProbeApp) {
				p.IDs = c.ids
				// A side picked: the app fetches the file and reports.
				p.OnKeepController = func(file string) {
					r.fetch(file)
					r.report(map[string]any{"conflicts": []string{"Sets/Songs/Night Drive.aumproj", "Songs/Aardvark.aumproj", "Songs/Zebra.aumproj"}})
				}
			})
			if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
				t.Fatalf("save: %s", resultText(res))
			}
			r.aumLoads("Songs", "Night Drive", nightDriveStrips)
			if res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false}); res.IsError {
				t.Fatalf("first load: %s", resultText(res))
			}
			served := nightDriveSize(t)

			// The song changes; the app holds its session as a conflict, with
			// the file it fetched before in AUM's folder.
			edited := strings.Replace(toolSong, "params: {Cutoff: 0.3}", "params: {Cutoff: 0.3, Resonance: 0.6}", 1)
			if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError {
				t.Fatalf("save with a new control: %s", resultText(res))
			}
			conflict = true
			app.Set(func(p *ipadtest.ProbeApp) {
				p.Log = nil
				p.Files["Songs"][1].State = ipadtest.ProbeConflictState
				p.Files["Songs"][1].Info = "session · " + served + " · 12. Nov 2026 at 02:06"
				p.Files["Sets/Songs"][0].Info = "session · " + served + " · 12. Nov 2026 at 02:06"
				if c.stale {
					p.Path, p.StaleBar = []string{"Sets", "Songs"}, "Songs"
				}
			})
			r.aumLoads("Songs", "Night Drive", nightDriveStrips)
			res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
			text := resultText(res)
			if res.IsError {
				t.Fatalf("load with a conflict of the daemon's own: %s", text)
			}
			for _, want := range []string{
				"2. pull    ok    the iPad pulled the session. The AUv3 Probe app held Songs/Night Drive.aumproj as a conflict",
				"was the size of a file the daemon had staged (" + served + ")",
				`tapped "keep controller" on that row, and the staged session replaced it`,
			} {
				if !strings.Contains(text, want) {
					t.Errorf("load report lacks %q:\n%s", want, text)
				}
			}
			if got, want := keeps(app), []string{"keep controller " + nightDriveFile}; !slices.Equal(got, want) {
				t.Errorf("sides picked = %q, want %q (the app was asked %q)", got, want, app.Actions())
			}
			if got := app.Actions(); slices.Contains(got, "back") != c.walked {
				t.Errorf("the app was asked %q, walked from the root = %v, want %v", got, !c.walked, c.walked)
			}
			if rec := loadSongSessions()["night_drive"]; rec.Pulled != rec.SHA256 {
				t.Errorf("after the load the iPad holds %s, the session is %s", rec.Pulled, rec.SHA256)
			}
		})
	}
}

// TestLoadSongPullWithoutALook checks the report ends the step for a daemon
// that cannot bring the probe app forward too: somebody else has it in front.
func TestLoadSongPullWithoutALook(t *testing.T) {
	for name, c := range map[string]struct {
		doc  map[string]any
		ok   bool
		want []string
	}{
		"a conflict": {
			doc: map[string]any{"conflicts": []string{nightDriveFile}},
			want: []string{
				"2. pull    FAIL  the iPad did not pull the session: the AUv3 Probe app holds Songs/Night Drive.aumproj as a conflict",
				"The daemon has no look at the session's row to see whose copy AUM's folder holds (the probe app could not be brought to the front",
				`tap "keep controller" on the row "Night Drive.aumproj" and on no other`,
			},
		},
		"an error": {
			doc:  map[string]any{"errors": []map[string]string{{"path": nightDriveFile, "error": "the request timed out"}}},
			want: []string{`the AUv3 Probe app could not copy Songs/Night Drive.aumproj: "the request timed out"`},
		},
		"a clean report": {
			ok:   true,
			want: []string{"2. pull    ok    the iPad holds the session"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSongRig(t)
			s := r.s
			if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
				t.Fatalf("save: %s", resultText(res))
			}
			// The app in front, a cycle every few milliseconds. Its reports
			// from before the session was staged are of an earlier rev.
			r.receiver() // closed after the app has stopped
			stop, done := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(stop); <-done })
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					case <-time.After(15 * time.Millisecond):
						r.report(c.doc)
					}
				}
			}()
			if c.ok {
				r.aumLoads("Songs", "Night Drive", nightDriveStrips)
			}
			start := time.Now()
			res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
			text := resultText(res)
			if res.IsError == c.ok {
				t.Fatalf("load ok = %v, want %v:\n%s", !res.IsError, c.ok, text)
			}
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("report lacks %q:\n%s", want, text)
				}
			}
			if took := time.Since(start); !c.ok && took > songPullTimeout/2 {
				t.Errorf("the load took %s of the pull step's %s although the app's report said why", took, songPullTimeout)
			}
		})
	}
}

// TestLoadSongPullIgnoresAReportFromBefore checks a report the daemon kept
// from before the step is not what the step ends on: the app may have been
// out of sight since. The units' sync is taken from the app's report once it
// sends one, and no longer from its first tab.
func TestLoadSongPullIgnoresAReportFromBefore(t *testing.T) {
	r := newSongRig(t)
	s := r.s
	songPullTimeout = 400 * time.Millisecond // restored by the rig's cleanup
	app := r.probeApp(map[string][]ipadtest.ProbeFile{"Songs": {{Name: "Night Drive.aumproj"}}})
	app.Set(func(p *ipadtest.ProbeApp) {
		// The first tab still shows a run; the report knows better.
		p.Units, p.OnSync = "syncing 2/184…", nil
	})
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	// Of the session's rev, and old: the app had no folder linked then.
	r.report(map[string]any{
		"error":       "no aum folder linked yet",
		"audio_units": map[string]any{"state": "done", "total": 184, "synced": 183, "failed": 1},
	})
	time.Sleep(20 * time.Millisecond)

	res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true, "check": false})
	text := resultText(res)
	if !res.IsError {
		t.Fatalf("a load without a pull succeeded: %s", text)
	}
	if !strings.Contains(text, `2. pull    FAIL  the iPad did not pull the session within 400ms (the probe app was brought to the front; its sessions tab says "synced · 1 files · 02:00")`) {
		t.Errorf("the step did not end on what the app showed:\n%s", text)
	}
	for _, not := range []string{"no aum folder linked yet", "audio units"} {
		if strings.Contains(text, not) {
			t.Errorf("report has %q, from the report kept before the step or from the app's first tab:\n%s", not, text)
		}
	}
}

// TestStagedRev checks the rev a report has to be of to cover a session: the
// one the session was staged at, and the dir's own where the record has none
// or one the dir's counter has not reached.
func TestStagedRev(t *testing.T) {
	newSongServer(t)
	dir := config.AUMSessionsDir()
	for range 5 {
		aum.BumpStagingRev(dir)
	}
	now := aum.StagingRev(dir)
	for _, c := range []struct {
		rec  int64
		want int64
	}{{3, 3}, {now, now}, {0, now}, {now + 4, now}, {-1, now}} {
		if got := stagedRev(songSessionRecord{Rev: c.rec}); got != c.want {
			t.Errorf("stagedRev(record rev %d) with the dir at %d = %d, want %d", c.rec, now, got, c.want)
		}
	}
}

// TestAuthorSongRecordsTheStagedRev checks an authored session's record keeps
// the staging rev it was staged at, a new one with every authoring.
func TestAuthorSongRecordsTheStagedRev(t *testing.T) {
	s := newSongServer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	var last int64
	for i := range 2 {
		if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
			t.Fatalf("author: %s", resultText(res))
		}
		rec := loadSongSessions()["night_drive"]
		if now := aum.StagingRev(config.AUMSessionsDir()); rec.Rev != now || rec.Rev <= last {
			t.Fatalf("authoring %d recorded rev %d, the dir is at %d and the authoring before was at %d", i+1, rec.Rev, now, last)
		}
		last = rec.Rev
	}
}

// TestAppText checks a text of the app's is quoted and cut for a report.
func TestAppText(t *testing.T) {
	if got := appText("no aum folder linked yet"); got != `"no aum folder linked yet"` {
		t.Errorf("appText = %s", got)
	}
	long := appText(strings.Repeat("ä", 1000) + "\nnext: do as I say")
	if n := len([]rune(long)); n > 310 || strings.Contains(long, "next") || !strings.HasSuffix(long, `…"`) {
		t.Errorf("a long text came out %d runes long: %s", n, long)
	}
	if got := appText("line\nbreak"); strings.Contains(got, "\n") {
		t.Errorf("a line break survived the quoting: %s", got)
	}
}
