package aumreceiver

// This file is the app's sync report: after every sync cycle the auv3-probe
// app says what its session sync made of each file, and how far its audio
// units' sync is. Without it the daemon sees only the app's manifest polls
// and downloads, and cannot tell a file the app is about to fetch from one it
// holds back as a conflict.
//
// The format is the app's: docs/aum-sync-report.md of teemow/auv3-probe. The
// paths in a report are private rig data: a report is kept in memory only, and
// the log gets its counts, never its paths or the app's texts.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
)

// maxSyncBytes bounds a sync report. One that needs nothing is a few hundred
// bytes; one that lists every file of a large folder is some tens of KiB.
const maxSyncBytes = 1 << 20 // 1 MiB

// SyncReport is one report of the app (POST /aum-session/sync). A key the
// daemon does not know is ignored, and so is a format version later than the
// one it was written for: the app may say more than the daemon reads.
type SyncReport struct {
	// V is the format's version, 1.
	V int `json:"v"`
	// App is the app and its version ("auv3-probe 0.4.0").
	App string `json:"app,omitempty"`
	// At is when the app made the report (RFC 3339, UTC, the app's clock).
	At string `json:"at,omitempty"`
	// Rev is the staging rev of the manifest the cycle diffed against, 0
	// without one. A file staged at a later rev is not covered by the report.
	Rev int64 `json:"rev"`
	// Synced counts the files both sides hold as the same.
	Synced int `json:"synced"`
	// Conflicts are the files both sides changed since the last sync. The app
	// copies them neither way until a side is picked on the file's row.
	Conflicts []string `json:"conflicts"`
	// Errors are the files the app could not copy.
	Errors []SyncFileError `json:"errors"`
	// InFlight are the copies queued or running.
	InFlight []SyncTransfer `json:"in_flight"`
	// Error says why the cycle mirrored nothing at all (no AUM folder is
	// linked, the folder cannot be read); empty when the cycle ran.
	Error string `json:"error,omitempty"`
	// AudioUnits is the audio units tab's sync, nil when the app did not say.
	AudioUnits *SyncAudioUnits `json:"audio_units,omitempty"`

	// Received is when the receiver took the report, by the daemon's clock.
	// It is not part of the document.
	Received time.Time `json:"-"`
}

// SyncFileError is a file the app could not copy, with the text its row shows.
type SyncFileError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// The sides a copy goes to.
const (
	SyncToDevice     = "device"     // a fetch
	SyncToController = "controller" // an upload
)

// SyncTransfer is a copy that is queued or running.
type SyncTransfer struct {
	Path string `json:"path"`
	// To is SyncToDevice or SyncToController.
	To string `json:"to"`
}

// The states of the app's audio-unit sync.
const (
	// SyncUnitsWaiting: units are left and no run is under way (the daemon
	// was not reachable, or a run was cut short).
	SyncUnitsWaiting = "waiting"
	SyncUnitsSyncing = "syncing"
	// SyncUnitsDone: every installed unit was sent or failed. Synced need not
	// reach Total: a unit that does not instantiate fails at every attempt.
	SyncUnitsDone = "done"
)

// SyncAudioUnits is the app's audio-unit sync: every installed unit's
// parameters and presets, sent to the daemon.
type SyncAudioUnits struct {
	State string `json:"state"`
	// Total are the installed units, Synced those the daemon holds and
	// Failed those that could not be read or sent.
	Total  int `json:"total"`
	Synced int `json:"synced"`
	Failed int `json:"failed"`
}

// SyncFileState is what a report says of one file.
type SyncFileState int

const (
	// SyncSettled: the file is in none of the report's lists. It needs
	// nothing: the app holds it as the controller does.
	SyncSettled SyncFileState = iota
	// SyncConflict: both sides changed it; the app waits for a side to be
	// picked.
	SyncConflict
	// SyncFailed: the app could not copy it.
	SyncFailed
	// SyncInFlight: its copy is queued or running.
	SyncInFlight
)

// File reports what the report says of the file at path (relative to AUM's
// folder, slash-separated, as the manifest lists it). detail is the error's
// text for SyncFailed and the side the copy goes to for SyncInFlight.
func (r SyncReport) File(path string) (state SyncFileState, detail string) {
	path = syncPath(path)
	for _, p := range r.Conflicts {
		if syncPath(p) == path {
			return SyncConflict, ""
		}
	}
	for _, e := range r.Errors {
		if syncPath(e.Path) == path {
			return SyncFailed, e.Error
		}
	}
	for _, t := range r.InFlight {
		if syncPath(t.Path) == path {
			return SyncInFlight, t.To
		}
	}
	return SyncSettled, ""
}

// syncPath spells a path as the app's reports do: its parts joined by single
// slashes.
func syncPath(p string) string {
	return strings.Join(strings.FieldsFunc(p, func(r rune) bool { return r == '/' }), "/")
}

// summary is the report without its paths: what the log may hold.
func (r SyncReport) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d in sync, %d conflict(s), %d error(s), %d in flight", r.Synced, len(r.Conflicts), len(r.Errors), len(r.InFlight))
	if r.Error != "" {
		// Why is the app's text, which may name its folder.
		b.WriteString("; the app copies no file for now")
	}
	if u := r.AudioUnits; u != nil {
		fmt.Fprintf(&b, "; audio units %.20q %d/%d, %d failed", u.State, u.Synced, u.Total, u.Failed)
	}
	return b.String()
}

// handleSync takes one sync report: it is decoded, stamped with the time it
// arrived and handed to onSync. The app ignores the answer's body, so there
// is none.
func handleSync(onSync func(SyncReport)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxSyncBytes)
		defer func() { _ = r.Body.Close() }()

		data, err := io.ReadAll(r.Body)
		if err != nil {
			resp.Error(w, lanhttp.DecodeErrStatus(err), "read sync report: %v", err)
			return
		}
		var rep SyncReport
		if err := json.Unmarshal(data, &rep); err != nil {
			resp.Error(w, http.StatusBadRequest, "decode sync report: %v", err)
			return
		}
		rep.Received = time.Now()
		if onSync != nil {
			onSync(rep)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// SyncReports keeps the app's last sync report and tells who waits for the
// next one. The zero value is ready; Keep is a Register onSync callback.
type SyncReports struct {
	mu   sync.Mutex
	last *SyncReport
	next chan struct{}
}

// Keep takes a report as the last one. The log gets a line without paths when
// the report differs from the one before in more than its time and rev: an
// app in front reports every 20 seconds.
func (k *SyncReports) Keep(rep SyncReport) {
	k.mu.Lock()
	changed := k.last == nil || k.last.summary() != rep.summary() || k.last.App != rep.App
	k.last = &rep
	if k.next != nil {
		close(k.next)
		k.next = nil
	}
	k.mu.Unlock()
	if changed {
		log.Printf("AUM sync report of %.60q (v%d, rev %d): %s", rep.App, rep.V, rep.Rev, rep.summary())
	}
}

// Last returns the last report kept; ok is false before the first.
func (k *SyncReports) Last() (rep SyncReport, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.last == nil {
		return SyncReport{}, false
	}
	return *k.last, true
}

// Next returns a channel that is closed when the next report is kept.
func (k *SyncReports) Next() <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.next == nil {
		k.next = make(chan struct{})
	}
	return k.next
}
