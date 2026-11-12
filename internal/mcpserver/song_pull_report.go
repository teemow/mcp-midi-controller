package mcpserver

// This file is what load_song's pull step makes of the AUv3 Probe app's sync
// report. After every sync cycle the app posts what it made of each file
// (POST /aum-session/sync, internal/aumreceiver): the files it holds as
// conflicts, those it could not copy, the copies still queued, and the staging
// rev of the manifest the cycle went by. A file in none of the lists is one
// the app holds as the daemon staged it.
//
// A report says what became of a session only when its cycle saw the session
// staged: when its rev is the rev the session was staged at, or a later one.
// The app's own uploads bump the staging rev too, so a report's rev may lag
// behind the dir's current rev and cover the session all the same; it is the
// session's rev that counts.

import (
	"context"
	"fmt"
	"time"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/aumreceiver"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

// OnAUMSyncReport is the aum receiver's sync callback: the probe app's report
// of what its session sync made of each file. The last one is kept, for
// load_song's pull step to end on.
func (s *Server) OnAUMSyncReport(rep aumreceiver.SyncReport) { s.syncReports.Keep(rep) }

// reportsUnits says the app is one whose sync report tells of its audio
// units' sync.
func (s *Server) reportsUnits() bool {
	rep, ok := s.syncReports.Last()
	return ok && rep.AudioUnits != nil
}

// stagedRev is the staging rev from which a sync report covers a song's
// session: the rev it was staged at. A record from before that rev was kept
// has none, and a staging dir whose counter began anew is behind the record;
// the dir's rev as it stands is then the first that is sure to cover it.
func stagedRev(rec songSessionRecord) int64 {
	now := aum.StagingRev(config.AUMSessionsDir())
	if rec.Rev <= 0 || rec.Rev > now {
		return now
	}
	return rec.Rev
}

// appText quotes a text of the app's for a report to the caller, cut to a
// length a row's text has.
func appText(text string) string {
	const most = 300
	if r := []rune(text); len(r) > most {
		text = string(r[:most]) + "…"
	}
	return fmt.Sprintf("%q", text)
}

// reported returns the app's last sync report since the step began, and
// whether it covers the session: whether its cycle saw the session staged. A
// report that says the app copies no file at all covers it whatever its rev:
// it tells of the app, not of a file. A report from before the step is not
// gone by: the app may have been out of sight since, and its next cycle,
// which starts when it comes forward, tells how things are now.
func (w *pullWatch) reported() (rep aumreceiver.SyncReport, covers bool) {
	rep, ok := w.s.syncReports.Last()
	if !ok || rep.Received.Before(w.began) {
		return rep, false
	}
	w.report = &rep
	return rep, rep.Rev >= w.rev || rep.Error != ""
}

// pullAppFolder is what to do when the app says it copies no file at all.
const pullAppFolder = `look at the AUv3 Probe app's "aum sessions" tab: AUM's folder has to be linked there and readable. Then load_song again`

// pullAppRow is what to do when the app could not copy the session.
const pullAppRow = `the AUv3 Probe app tries the copy again at its next sync, every 20 s while it is in front: load_song again. When it fails again, the session's row in the app's "aum sessions" tab says why`

// byReport acts on a sync report that covers the session. It reports done
// when that ended the pull step.
func (w *pullWatch) byReport(ctx context.Context, sess *ipad.Session, rep aumreceiver.SyncReport) (bool, error) {
	file := w.outID + ".aumproj"
	if rep.Error != "" {
		return true, w.end(false, pullAppFolder,
			`the iPad did not pull the session: the AUv3 Probe app reports that its sync copies no file for now: %s`, appText(rep.Error))
	}
	state, detail := rep.File(file)
	switch state {
	case aumreceiver.SyncConflict:
		// After the daemon picked a side this may be the report of the cycle
		// that was running then; the app's next one, or the file, tells.
		if !w.resolved.IsZero() || rep.Received.Equal(w.looked) {
			return false, nil
		}
		w.looked = rep.Received
		// Whose copy AUM's folder holds is on the session's row.
		var seen *ipad.ProbeScreen
		if sess != nil {
			if scr, err := sess.ProbeScreen(ctx); err == nil && w.shows(scr) {
				w.last, seen = scr, &scr
			}
		}
		return w.conflict(ctx, sess, seen)
	case aumreceiver.SyncFailed:
		return true, w.end(false, pullAppRow,
			`the iPad did not pull the session: the AUv3 Probe app could not copy %s: %s`, file, appText(detail))
	case aumreceiver.SyncInFlight:
		return false, nil // the app is on it; its next report follows
	}
	return w.holds(ctx, rep)
}

// holds ends the step on a report that has the session in none of its lists:
// the app holds the staged bytes.
func (w *pullWatch) holds(ctx context.Context, rep aumreceiver.SyncReport) (bool, error) {
	// The download that brought them there is in the daemon's own record,
	// which the report may be ahead of by a moment.
	if w.s.waitPulled(ctx, w.songID, w.rec.SHA256, songPullSettle) {
		return true, w.pulled()
	}
	if ctx.Err() != nil {
		return false, nil
	}
	file := w.outID + ".aumproj"
	// In sync with what is staged now: that is the song's session only while
	// the staged file is the one authored. The app sends the copy in AUM's
	// folder to the daemon when it takes that one for the newer.
	if fileSHA256(stagedSessionPath(w.outID)) != w.rec.SHA256 {
		return true, w.end(false,
			"what AUM's folder holds under this name may be a session saved in AUM: save it under another name there first if it is to be kept. Then load_song again, which authors the song's session anew",
			`the iPad did not pull the session: the staged %s is no longer the file authored for the song. The AUv3 Probe app's sync report (staging rev %d) lists it as in sync, so the app has sent the copy in AUM's folder to the daemon over it, or the file was staged again by other means`,
			file, rep.Rev)
	}
	// The iPad holds this file: the next load need not ask.
	w.s.noteSessionPulled(w.outID)
	if !w.resolved.IsZero() {
		return true, w.pulled()
	}
	return true, w.end(true, "",
		`the iPad holds the session: the AUv3 Probe app's sync report (staging rev %d, the session was staged at %d) lists %s as in sync with the staged file. The daemon saw no download of it`,
		rep.Rev, w.rev, file)
}

// reportSaid is what the app's last sync report said of a session that did
// not arrive in time, and what to do then.
func (w *pullWatch) reportSaid() (said, next string) {
	rep := *w.report
	ago := time.Since(rep.Received).Round(time.Second)
	if rep.Rev < w.rev {
		return fmt.Sprintf("its last sync report, %s ago, is of staging rev %d and the session was staged at rev %d: no sync cycle of the app has listed the daemon's files since", ago, rep.Rev, w.rev),
			pullOpenApp
	}
	file := w.outID + ".aumproj"
	switch state, detail := rep.File(file); {
	case state == aumreceiver.SyncInFlight && detail == aumreceiver.SyncToController:
		return fmt.Sprintf("its last sync report, %s ago, lists %s as queued to be sent to the daemon: the app takes the copy in AUM's folder for the newer one", ago, file),
			pullLookAtApp
	case state == aumreceiver.SyncInFlight:
		return fmt.Sprintf("its last sync report, %s ago, lists %s as queued to be fetched", ago, file),
			"the AUv3 Probe app was on it, behind other copies or a slow one: load_song again"
	case state == aumreceiver.SyncConflict && w.unread != nil:
		return fmt.Sprintf("its last sync report, %s ago, lists %s as a conflict, and the daemon could not act on the session's row: %v", ago, file, w.unread),
			w.pickASide()
	case state == aumreceiver.SyncConflict:
		return fmt.Sprintf("its last sync report, %s ago, lists %s as a conflict, and the app's browser showed no such conflict when the daemon looked", ago, file),
			pullLookAtApp
	}
	return fmt.Sprintf("its last sync report, %s ago, lists %s as in sync", ago, file), pullLookAtApp
}

// unitsNote says what the app's audio-unit sync was doing while the session
// did not arrive, "" when that explains nothing. The app's report tells where
// it sends one; else what its first tab showed when the app came forward.
func (w *pullWatch) unitsNote() string {
	if w.report != nil && w.report.AudioUnits != nil {
		u := w.report.AudioUnits
		failed := ""
		if u.Failed > 0 {
			failed = fmt.Sprintf(", %d failed", u.Failed)
		}
		switch u.State {
		case aumreceiver.SyncUnitsSyncing:
			return fmt.Sprintf("it was sending its audio units to the daemon (%d of %d sent%s), which it does only while it is in front", u.Synced, u.Total, failed)
		case aumreceiver.SyncUnitsWaiting:
			return fmt.Sprintf("its audio units' sync is waiting: %d of %d sent%s and no run under way", u.Synced, u.Total, failed)
		}
		// Done, however many of them were sent: a unit that does not
		// instantiate fails at every attempt.
		return ""
	}
	if w.units != "" {
		return fmt.Sprintf("it had just been started and was sending its audio units to the daemon again (%s when it came forward), which it does only while it is in front", w.units)
	}
	return ""
}
