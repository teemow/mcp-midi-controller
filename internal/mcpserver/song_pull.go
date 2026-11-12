package mcpserver

// This file is load_song's pull step once the iPad has not fetched the staged
// session by itself: the AUv3 Probe app is brought to the front, and while the
// file is waited for the app's screen is read for why it does not come.
//
// The app tells the daemon nothing about a file it leaves alone. It lists the
// staged files, and a file that changed on both sides since its last sync it
// neither fetches nor sends: it shows the file as a conflict and waits for a
// side to be picked. From the daemon that is a file nobody asks for.

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

// pullOutcome is how the pull step ended.
type pullOutcome struct {
	ok           bool
	detail, next string
}

// pullOpenApp is what to do when the session did not arrive and nothing says
// why.
const pullOpenApp = "open the AUv3 Probe app on the iPad (it pulls staged sessions while it is in front and reaches the daemon), then load_song again"

// pullWithProbeApp brings the probe app forward and waits for it to fetch the
// staged session of a song.
func (s *Server) pullWithProbeApp(ctx context.Context, songID, outID string, rec songSessionRecord) pullOutcome {
	// blind waits without a look at the app.
	blind := func(note string) pullOutcome {
		if !s.waitPulled(ctx, songID, rec.SHA256, songPullTimeout) {
			return pullOutcome{detail: fmt.Sprintf("the iPad did not pull the session (%s)", note), next: pullOpenApp}
		}
		return pullOutcome{ok: true, detail: fmt.Sprintf("the iPad pulled the session (%s)", note)}
	}
	if s.ipad == nil {
		return blind("the iPad UI tools are not configured, so the probe app was not brought forward")
	}
	app, err := s.probeApp(ctx)
	if err != nil {
		return blind("the probe app could not be brought to the front: " + err.Error())
	}
	w := &pullWatch{s: s, songID: songID, outID: outID, rec: rec}
	w.folder, w.file = path.Split(outID)
	w.folder, w.file = strings.Trim(w.folder, "/"), w.file+".aumproj"
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error { return w.run(ctx, sess) })
	if w.out == nil {
		return blind(fmt.Sprintf("the probe app could not be brought to the front: %v", err))
	}
	return *w.out
}

// pullWatch waits for one session's pull with the probe app in front.
type pullWatch struct {
	s             *Server
	songID, outID string
	rec           songSessionRecord
	// folder and file are where the app's browser lists the session.
	folder, file string

	// units is the app's audio-unit sync as it stood when the app came
	// forward ("2/184"), "" when none ran.
	units string
	// unread is why the app's session browser is not read.
	unread error
	// last is the browser as it was read last, row the session's row in it.
	last ipad.ProbeScreen
	row  *ipad.ProbeFile
	// resolved is when the daemon picked the controller's side of a conflict
	// (zero when it did not), held the size the app gave for the copy that
	// was replaced.
	resolved time.Time
	held     string

	out *pullOutcome
}

func (w *pullWatch) end(ok bool, next, format string, a ...any) error {
	w.out = &pullOutcome{ok: ok, detail: fmt.Sprintf(format, a...), next: next}
	return nil
}

// run is the wait, in a WebDriverAgent session bound to the probe app.
func (w *pullWatch) run(ctx context.Context, sess *ipad.Session) error {
	deadline := time.Now().Add(songPullTimeout)
	// After a start the app shows its audio units tab and sends every unit
	// to the daemon again.
	if first, err := sess.ProbeScreen(ctx); err == nil && first.UnitsSyncing {
		w.units = fmt.Sprintf("%d/%d", first.UnitsDone, first.UnitsTotal)
	}
	scr, err := sess.ProbeOpenFolder(ctx, w.folder, true)
	switch {
	case err != nil:
		w.unread = err
	case scr.Sync != nil && !scr.Cycle:
		x, y := scr.Sync.Center()
		_ = sess.TapAt(ctx, x, y)
	}

	for {
		if w.s.waitPulled(ctx, w.songID, w.rec.SHA256, songPullPoll) {
			return w.pulled()
		}
		if ctx.Err() != nil {
			return w.end(false, pullOpenApp, "the iPad did not pull the session (the call ended while the probe app was in front)")
		}
		if w.unread == nil {
			picked := !w.resolved.IsZero()
			if done, err := w.look(ctx, sess); err != nil {
				w.unread = err
			} else if done {
				return nil
			}
			// The app fetches the file it was just told to keep: that gets
			// its own time.
			if !picked && !w.resolved.IsZero() {
				if d := w.resolved.Add(songPullResolved); d.After(deadline) {
					deadline = d
				}
			}
		}
		if time.Now().After(deadline) {
			return w.timedOut()
		}
	}
}

// shows reports whether the browser, as read, shows the session's folder. It
// may have been moved by a hand on the iPad since the daemon brought it there.
func (w *pullWatch) shows(scr ipad.ProbeScreen) bool {
	if scr.Tab != ipad.ProbeSessionsTab {
		return false
	}
	if w.folder == "" {
		return scr.Back == nil
	}
	return scr.Back != nil && scr.Folder == path.Base(w.folder)
}

// look reads the app's browser and acts on the session's row. It reports
// done when that ended the step.
func (w *pullWatch) look(ctx context.Context, sess *ipad.Session) (bool, error) {
	scr, err := sess.ProbeScreen(ctx)
	if err != nil {
		return false, err
	}
	w.last, w.row = scr, nil
	if !w.shows(scr) {
		return false, nil
	}
	if w.row = scr.File(w.file); w.row == nil || !w.row.Conflict() {
		return false, nil
	}
	if !w.resolved.IsZero() {
		return true, w.end(false, pullLookAtApp,
			`the iPad did not pull the session: the AUv3 Probe app held %s as a conflict, the daemon tapped %q on that row, and the app lists it as a conflict again`,
			w.outID+".aumproj", ipad.ProbeKeepController)
	}

	// The conflict is known: the wait ends here, one way or the other. The
	// row is acted on only in a browser walked to its folder from the root,
	// which names no more than the last part of where it is.
	scr, err = sess.ProbeOpenFolder(ctx, w.folder, false)
	if err != nil {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. The daemon could not walk the app's browser to that row to see whose copy AUM's folder holds: %v`,
			w.outID+".aumproj", ipad.ProbeConflict, err)
	}
	row := scr.File(w.file)
	if row == nil || !row.Conflict() {
		return false, nil // picked by someone else meanwhile
	}
	w.last, w.row = scr, row
	own, why := ownCopy(w.outID, w.rec, *row)
	if !own {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. %s`,
			w.outID+".aumproj", ipad.ProbeConflict, why)
	}
	clicked := false
	if row.KeepController != nil {
		if clicked, err = sess.ClickButtonAt(ctx, ipad.ProbeKeepController, *row.KeepController); err != nil {
			return false, err
		}
	}
	if !clicked {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q), and its %q could not be tapped (the row moved, or has no such button)`,
			w.outID+".aumproj", ipad.ProbeConflict, ipad.ProbeKeepController)
	}
	w.resolved, w.held = time.Now(), row.Size()
	return false, nil
}

// pullLookAtApp is what to do when the app did not do what its screen said.
const pullLookAtApp = `look at the AUv3 Probe app's "aum sessions" tab (ipad_ui_tree with the app), then load_song again`

// pickASide tells how to resolve the session's conflict by hand.
func (w *pullWatch) pickASide() string {
	where := `the "aum sessions" tab`
	if w.folder != "" {
		where += fmt.Sprintf(", folder %q", w.folder)
	}
	return fmt.Sprintf(`what AUM's folder holds under this name is lost when the staged session replaces it: save it under another name in AUM first if it is to be kept. `+
		`Then, in the AUv3 Probe app (%s), tap %q on the row %q and on no other, and load_song again`, where, ipad.ProbeKeepController, w.file)
}

func (w *pullWatch) pulled() error {
	if !w.resolved.IsZero() {
		return w.end(true, "",
			`the iPad pulled the session. The AUv3 Probe app held %s as a conflict; its copy in AUM's folder was the size of a file the daemon had staged (%s), so the daemon took it for its own, tapped %q on that row, and the staged session replaced it`,
			w.outID+".aumproj", w.held, ipad.ProbeKeepController)
	}
	return w.end(true, "", "the iPad pulled the session (the probe app was brought to the front)")
}

// timedOut ends the step with what the app's screen said, if anything.
func (w *pullWatch) timedOut() error {
	if !w.resolved.IsZero() {
		return w.end(false, pullLookAtApp,
			`the iPad did not pull the session: the AUv3 Probe app held %s as a conflict, the daemon tapped %q on that row, and the file was not fetched in the %s since`,
			w.outID+".aumproj", ipad.ProbeKeepController, time.Since(w.resolved).Round(time.Second))
	}
	next := pullOpenApp
	var seen []string
	switch {
	case w.unread != nil:
		seen = append(seen, fmt.Sprintf("its list of sessions could not be read: %v", w.unread))
	case w.last.Offline():
		seen = append(seen, fmt.Sprintf("its sessions tab says %q: it does not reach the daemon", w.last.Status))
		next = "check which daemon the AUv3 Probe app has found (the line under \"mcp-midi-controller\" at the top of its window), then load_song again"
	case w.row != nil && w.row.State != "":
		seen = append(seen, fmt.Sprintf("it lists %s as %q", w.file, w.row.State))
	case w.last.Status != "":
		seen = append(seen, fmt.Sprintf("its sessions tab says %q", w.last.Status))
	}
	if w.units != "" {
		seen = append(seen, fmt.Sprintf("it had just been started and was sending its audio units to the daemon again (%s when it came forward), which it does only while it is in front", w.units))
	}
	note := "the probe app was brought to the front"
	if len(seen) > 0 {
		note += "; " + strings.Join(seen, "; ")
	}
	return w.end(false, next, "the iPad did not pull the session within %s (%s)", songPullTimeout, note)
}

// ownCopy reports whether the copy of a song's session in AUM's folder, as the
// probe app's row describes it, is a file the daemon staged: only then does
// the daemon pick the controller's side of a conflict by itself, because the
// staged session then replaces nothing but an older one of its own. A session
// saved in AUM is the same file with another size (AUM writes every plugin's
// state out again: 105 KB as authored came back as 156 KB), and that one is
// somebody's to keep or drop.
//
// The size is all the daemon can see of the copy, and the app gives it rounded
// (ipad.ProbeByteLabel): a copy that rounds like a file the iPad fetched
// under this name counts as that file.
func ownCopy(outID string, rec songSessionRecord, row ipad.ProbeFile) (bool, string) {
	if !strings.HasPrefix(outID, "Songs/") {
		return false, "The daemon picks a side by itself only for a song's session under Songs/."
	}
	if len(rec.Served) == 0 {
		return false, "The daemon has no record of a file the iPad fetched under this name, so the copy in AUM's folder is not one it staged."
	}
	size := row.Size()
	if size == "" {
		return false, fmt.Sprintf("The app's row (%q) gives no size for the copy in AUM's folder, so the daemon cannot tell whether it staged it.", row.Info)
	}
	staged := make([]string, 0, len(rec.Served))
	for _, n := range rec.Served {
		label := ipad.ProbeByteLabel(n)
		if label == size {
			return true, ""
		}
		staged = append(staged, label)
	}
	return false, fmt.Sprintf("The copy in AUM's folder (%q) is not the size of a file the daemon staged for it (%s): it was saved in AUM, or put there by other means.",
		row.Info, strings.Join(staged, ", "))
}
