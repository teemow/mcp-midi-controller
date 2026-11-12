package mcpserver

// This file is load_song's pull step once the iPad has not fetched the staged
// session by itself: the AUv3 Probe app is brought to the front, and the file
// is waited for along with the app's word on it. An app with the sync report
// says after every sync cycle what it made of each file (song_pull_report.go);
// the screen of an app that sends none is read for why the file does not come.
//
// What keeps a file from coming is most often a conflict. The app lists the
// staged files, and a file that changed on both sides since its last sync it
// neither fetches nor sends: it shows the file as a conflict and waits for a
// side to be picked. From the daemon that is a file nobody asks for.
//
// The app does not keep the iPad awake as AUM does. After a pull the load
// step brings AUM forward; a pull that fails ends the load, so the step
// itself brings AUM back then (aumBack). The probe app left in front let the
// rig's iPad lock itself, and a locked iPad needs its passcode by hand.

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/aumreceiver"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

// pullOutcome is how the pull step ended.
type pullOutcome struct {
	ok           bool
	detail, next string
	// app is the probe app's bundle id once the step set out to bring it
	// forward.
	app string
}

// pullOpenApp is what to do when the session did not arrive and nothing says
// why.
const pullOpenApp = "open the AUv3 Probe app on the iPad (it pulls staged sessions while it is in front and reaches the daemon), then load_song again"

// pullWithProbeApp brings the probe app forward and waits for it to fetch the
// staged session of a song. When that fails AUM is brought back to the front.
func (s *Server) pullWithProbeApp(ctx context.Context, songID, outID string, rec songSessionRecord) pullOutcome {
	out := s.pullInFront(ctx, songID, outID, rec)
	if !out.ok {
		s.aumBack(ctx, &out)
	}
	return out
}

// pullAUMBackTimeout bounds bringing AUM back after a failed pull.
const pullAUMBackTimeout = 20 * time.Second

// pullAUMBack is added to what to do after a failed pull once AUM is in front
// again; the %q is the probe app's bundle id.
const pullAUMBack = " AUM is in front again, so the AUv3 Probe app has to be opened again for this: ipad_launch with app %q (ipad_ui_tree and ipad_tap take the same app). " +
	"It syncs only while it is in front. Bring AUM back when done (ipad_launch without app): left in front, the app lets the iPad lock itself."

// aumBack brings AUM back to the front after a pull that failed with the probe
// app in front, and says so in the outcome. Nothing else would: the load ends
// with the failed pull, and the probe app does not keep the iPad awake, so
// left there the iPad locks itself after its auto-lock time and needs its
// passcode. The app's sync stops with it; what the reader is told to look at
// or do in the app therefore starts with opening it again.
func (s *Server) aumBack(ctx context.Context, out *pullOutcome) {
	if s.ipad == nil || out.app == "" {
		return
	}
	// The call may be over; AUM comes back all the same.
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pullAUMBackTimeout)
	defer cancel()
	// The app is in front, or the Home Screen an app left there that went
	// away (ipad.Client.Front): neither keeps the iPad awake.
	front, _ := s.ipad.Front()
	home := front == ipad.HomeScreenBundleID
	if front != out.app && !home {
		// The app did not come forward, as far as WebDriverAgent told. A call
		// that ended while it was asked to may have brought it there unheard.
		if ctx.Err() != nil {
			_ = s.ipad.BringToFront(bctx, ipad.AUMBundleID)
		}
		return
	}
	detail := strings.TrimRight(out.detail, ".")
	if err := s.ipad.BringToFront(bctx, ipad.AUMBundleID); err != nil {
		why, locked := s.ipadFailure(bctx, err)
		if !locked {
			why = err.Error() // what an AUM that is not running means is said below
		}
		still, first := "The probe app is still in front", "bring AUM to the front (ipad_launch without app): the probe app does not keep the iPad awake, and left in front the iPad locks itself"
		if home {
			still, first = "The iPad is still on its Home Screen", "bring AUM to the front (ipad_launch without app): on its Home Screen nothing keeps the iPad awake, and it locks itself"
		}
		if locked {
			first = ipadUnlock
		}
		out.detail = fmt.Sprintf("%s. %s: AUM could not be brought back (%s)", detail, still, why)
		out.next = first + ". Then: " + out.next
		return
	}
	if home {
		out.detail = detail + ". AUM is in front again: an app that went away had left the Home Screen there, where nothing keeps the iPad awake"
	} else {
		out.detail = detail + ". AUM is in front again: the probe app does not keep the iPad awake, so the daemon does not leave it in front"
	}
	out.next = strings.TrimRight(out.next, ".") + "." + fmt.Sprintf(pullAUMBack, out.app)
}

// pullLocked is the pull step's end at a locked iPad.
func pullLocked() pullOutcome {
	return pullOutcome{
		detail: ipadLockedText + ": it pulls no session, and AUM cannot be brought to the front to load one",
		next:   ipadUnlock + ", then load_song again",
	}
}

// pullInFront is the step itself: the probe app is brought forward and the
// session waited for. A failure leaves the app in front.
func (s *Server) pullInFront(ctx context.Context, songID, outID string, rec songSessionRecord) pullOutcome {
	// blind waits without a look at the app: for the file, and for what an
	// app that somebody else has brought forward reports of it.
	blind := func(note string) pullOutcome {
		w := newPullWatch(s, songID, outID, rec)
		w.blind = note
		_ = w.run(ctx, nil)
		return *w.out
	}
	if s.ipad == nil {
		return blind("the iPad UI tools are not configured, so the probe app was not brought forward")
	}
	app, err := s.probeApp(ctx)
	if err != nil {
		return blind("the probe app could not be brought to the front: " + err.Error())
	}
	w := newPullWatch(s, songID, outID, rec)
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error { return w.run(ctx, sess) })
	var out pullOutcome
	switch {
	case w.out != nil:
		out = *w.out
	case s.ipadLocked(ctx):
		return pullLocked() // no app comes forward on a locked iPad
	default:
		out = blind(fmt.Sprintf("the probe app could not be brought to the front: %v", err))
	}
	out.app = app
	return out
}

// pullWatch waits for one session's pull, with the probe app in front where
// the daemon could bring it there.
type pullWatch struct {
	s             *Server
	songID, outID string
	rec           songSessionRecord
	// folder and file are where the app's browser lists the session.
	folder, file string
	// blind is why the daemon has no look at the app ("" when it has): the
	// app's screen is then neither read nor tapped.
	blind string

	// began is when the step set out to bring the app forward: a sync report
	// counts when it arrived since. rev is the staging rev from which a
	// report says what the app made of the session (stagedRev).
	began time.Time
	rev   int64
	// report is the app's last sync report since began, nil while none came:
	// the app's screen is read for why the file does not come only until one
	// did. looked is when the report arrived whose conflict was taken to the
	// app's browser (zero for none): each report is acted on once.
	report *aumreceiver.SyncReport
	looked time.Time
	// watching is when the wait began, the app in front or not.
	watching time.Time

	// units is the app's audio-unit sync as its first tab had it when the app
	// came forward ("2/184"), "" when none ran or the app's report tells.
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

// newPullWatch sets out to wait for the pull of a song's session from now.
func newPullWatch(s *Server, songID, outID string, rec songSessionRecord) *pullWatch {
	w := &pullWatch{s: s, songID: songID, outID: outID, rec: rec, began: time.Now(), rev: stagedRev(rec)}
	w.folder, w.file = path.Split(outID)
	w.folder, w.file = strings.Trim(w.folder, "/"), w.file+".aumproj"
	return w
}

func (w *pullWatch) end(ok bool, next, format string, a ...any) error {
	w.out = &pullOutcome{ok: ok, detail: fmt.Sprintf(format, a...), next: next}
	return nil
}

// run is the wait: in a WebDriverAgent session bound to the probe app, or
// without one (sess nil) when the daemon has no look at the app.
func (w *pullWatch) run(ctx context.Context, sess *ipad.Session) error {
	deadline := time.Now().Add(songPullTimeout)
	if sess != nil {
		w.forward(ctx, sess)
	}
	// The screen of an app that sends no report is read from the second
	// look's time on: a report, where one comes, is there by then.
	w.watching = time.Now()

	for {
		// A download, a sync report or the time of the next look, whichever
		// comes first, ends the wait at the foot of the loop. The two signals
		// are taken before what they tell of is looked at, so none is missed.
		pulled, reported := w.s.pulls.signal(), w.s.syncReports.Next()
		if loadSongSessions()[w.songID].Pulled == w.rec.SHA256 {
			return w.pulled()
		}
		if ctx.Err() != nil {
			if w.blind != "" {
				return w.end(false, pullOpenApp, "the iPad did not pull the session (%s)", w.blind)
			}
			return w.end(false, pullOpenApp, "the iPad did not pull the session (the call ended while the probe app was in front)")
		}
		picked := !w.resolved.IsZero()
		if done, err := w.step(ctx, sess); err != nil {
			w.unread = err
		} else if done {
			return nil
		}
		// The app fetches the file it was just told to keep: that gets its
		// own time.
		if !picked && !w.resolved.IsZero() {
			if d := w.resolved.Add(songPullResolved); d.After(deadline) {
				deadline = d
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return w.timedOut()
		}
		wait := time.NewTimer(min(left, songPullPoll))
		select {
		case <-pulled:
		case <-reported:
		case <-wait.C:
		case <-ctx.Done():
		}
		wait.Stop()
	}
}

// forward is what the daemon does in the app once it is in front: it shows
// the session's folder and starts the app's sync.
func (w *pullWatch) forward(ctx context.Context, sess *ipad.Session) {
	// After a start the app shows its audio units tab, and one from before
	// the sync report sends every unit to the daemon again and says so there
	// only. An app with the report says in it how far that sync is.
	if !w.s.reportsUnits() {
		if first, err := sess.ProbeScreen(ctx); err == nil && first.UnitsSyncing {
			w.units = fmt.Sprintf("%d/%d", first.UnitsDone, first.UnitsTotal)
		}
	}
	scr, err := sess.ProbeOpenFolder(ctx, w.folder, true)
	switch {
	case err != nil:
		w.unread = err
	case scr.Sync != nil && !scr.Cycle:
		x, y := scr.Sync.Center()
		_ = sess.TapAt(ctx, x, y)
	}
}

// step looks at what is known of the session's pull and acts on it: the app's
// sync report where it sends one, its screen where it does not. It reports
// done when that ended the pull step.
func (w *pullWatch) step(ctx context.Context, sess *ipad.Session) (bool, error) {
	if rep, covers := w.reported(); covers {
		return w.byReport(ctx, sess, rep)
	}
	// An app that reports says what it made of the session in the report of
	// its next cycle; a browser that could not be read is not read again.
	if w.report != nil || w.unread != nil || sess == nil || time.Since(w.watching) < songPullPoll {
		return false, nil
	}
	return w.look(ctx, sess)
}

// shows reports whether the browser, as read, shows the session's folder. It
// may have been moved by a hand on the iPad since the daemon brought it there.
// A browser that names its folder's whole path is taken at that; one that
// does not, at its title, the path's last part.
func (w *pullWatch) shows(scr ipad.ProbeScreen) bool {
	if scr.Tab != ipad.ProbeSessionsTab {
		return false
	}
	if scr.FolderNamed {
		return scr.FolderPath == w.folder
	}
	if w.folder == "" {
		return scr.Back == nil
	}
	return scr.Back != nil && scr.Folder == path.Base(w.folder)
}

// look reads the app's browser and acts on the session's row: what is left to
// go by with an app that sends no sync report. It reports done when that
// ended the step.
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
	return w.conflict(ctx, sess, &scr)
}

// conflict acts on the session the app holds as a conflict, as its sync
// report or its browser said: the wait ends here, one way or the other,
// unless the conflict is gone when the row is looked at. seen is the browser
// as just read, nil when it was not.
//
// The copy in AUM's folder is described on the session's row and nowhere
// else, so the row is read, and it is acted on only when it is the session's
// beyond doubt. A row whose buttons name its file's path is. In an app
// without those identifiers the browser names no more than the last part of
// the folder it shows, and is walked to the session's folder from its root.
func (w *pullWatch) conflict(ctx context.Context, sess *ipad.Session, seen *ipad.ProbeScreen) (bool, error) {
	file := w.outID + ".aumproj"
	if sess == nil {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. The daemon has no look at the session's row to see whose copy AUM's folder holds (%s)`,
			file, ipad.ProbeConflict, w.blind)
	}
	var row *ipad.ProbeFile
	if seen != nil {
		if r := seen.File(w.file); r != nil && r.Conflict() && r.Path == file {
			row = r
		}
	}
	if row == nil {
		scr, err := sess.ProbeOpenFolder(ctx, w.folder, false)
		if err != nil {
			return true, w.end(false, w.pickASide(),
				`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. The daemon could not walk the app's browser to that row to see whose copy AUM's folder holds: %v`,
				file, ipad.ProbeConflict, err)
		}
		if row = scr.File(w.file); row == nil || !row.Conflict() {
			return false, nil // picked by someone else meanwhile
		}
		w.last = scr
	}
	w.row = row
	if row.Path != "" && row.Path != file {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. The row the daemon found for it names another file (%q), so it picked no side`,
			file, ipad.ProbeConflict, row.Path)
	}
	own, why := ownCopy(w.outID, w.rec, *row)
	if !own {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q) and copies it neither way until a side is picked. %s`,
			file, ipad.ProbeConflict, why)
	}
	// The button that names the file, where the app gives it; else the one
	// labelled "keep controller" at the frame that follows the row.
	clicked, err := false, error(nil)
	if row.KeepControllerID != "" {
		clicked, err = sess.ClickButtonID(ctx, row.KeepControllerID)
	}
	if !clicked && err == nil && row.KeepController != nil {
		clicked, err = sess.ClickButtonAt(ctx, ipad.ProbeKeepController, *row.KeepController)
	}
	if err != nil {
		return false, err
	}
	if !clicked {
		return true, w.end(false, w.pickASide(),
			`the iPad did not pull the session: the AUv3 Probe app holds %s as a conflict (%q), and its %q could not be tapped (the row moved, or has no such button)`,
			file, ipad.ProbeConflict, ipad.ProbeKeepController)
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
	return w.end(true, "", "the iPad pulled the session (%s)", w.how())
}

// how says in what way the daemon waited.
func (w *pullWatch) how() string {
	if w.blind != "" {
		return w.blind
	}
	return "the probe app was brought to the front"
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
	case w.report != nil:
		var said string
		said, next = w.reportSaid()
		seen = append(seen, said)
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
	if units := w.unitsNote(); units != "" {
		seen = append(seen, units)
	}
	note := w.how()
	if len(seen) > 0 {
		note += "; " + strings.Join(seen, "; ")
	}
	if w.blind != "" {
		return w.end(false, next, "the iPad did not pull the session (%s)", note)
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
