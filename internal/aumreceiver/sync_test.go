package aumreceiver

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fullReport is the example of the app's docs/aum-sync-report.md (made-up
// paths), with a key and a version the daemon does not know.
const fullReport = `{
  "v": 2,
  "app": "auv3-probe 0.4.0",
  "at": "2026-01-02T03:04:05Z",
  "rev": 42,
  "synced": 131,
  "conflicts": ["Songs/Held.aumproj"],
  "errors": [
    {"path": "Songs/Broken.aumproj", "error": "mcp-midi-controller returned HTTP 400: decode session", "tries": 3}
  ],
  "in_flight": [
    {"path": "Songs/Down.aumproj", "to": "device"},
    {"path": "Songs/Up.aumproj", "to": "controller"}
  ],
  "audio_units": {"state": "syncing", "total": 96, "synced": 60, "failed": 1},
  "since_start": {"cycles": 7}
}`

// postSync posts a body to the sync route of a receiver that keeps its
// reports in k.
func postSync(t *testing.T, k *SyncReports, body []byte) int {
	t.Helper()
	ts := httptest.NewServer(Handler(t.TempDir(), nil, nil, k.Keep))
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/aum-session/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestSyncReportFull checks a whole report is decoded, stamped with the time
// it arrived, kept and answered with 204, and that keys and a version the
// daemon does not know do not fail it.
func TestSyncReportFull(t *testing.T) {
	var k SyncReports
	if _, ok := k.Last(); ok {
		t.Fatal("a report is kept before one arrived")
	}
	next := k.Next()
	before := time.Now()
	if code := postSync(t, &k, []byte(fullReport)); code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	select {
	case <-next:
	default:
		t.Error("the report did not wake who waited for it")
	}
	rep, ok := k.Last()
	if !ok {
		t.Fatal("the report was not kept")
	}
	if rep.V != 2 || rep.App != "auv3-probe 0.4.0" || rep.At != "2026-01-02T03:04:05Z" || rep.Rev != 42 || rep.Synced != 131 || rep.Error != "" {
		t.Errorf("report = %+v", rep)
	}
	if rep.Received.Before(before) || rep.Received.After(time.Now()) {
		t.Errorf("received at %s, want the time it arrived (after %s)", rep.Received, before)
	}
	if u := rep.AudioUnits; u == nil || *u != (SyncAudioUnits{State: SyncUnitsSyncing, Total: 96, Synced: 60, Failed: 1}) {
		t.Errorf("audio units = %+v", u)
	}
	for path, want := range map[string]struct {
		state  SyncFileState
		detail string
	}{
		"Songs/Held.aumproj":    {SyncConflict, ""},
		"Songs/Broken.aumproj":  {SyncFailed, "mcp-midi-controller returned HTTP 400: decode session"},
		"Songs/Down.aumproj":    {SyncInFlight, SyncToDevice},
		"Songs/Up.aumproj":      {SyncInFlight, SyncToController},
		"Songs/Fine.aumproj":    {SyncSettled, ""},
		"/Songs//Held.aumproj/": {SyncConflict, ""}, // spelled as the app spells it
		"songs/held.aumproj":    {SyncSettled, ""},  // another file to the app
	} {
		if state, detail := rep.File(path); state != want.state || detail != want.detail {
			t.Errorf("File(%q) = %v, %q; want %v, %q", path, state, detail, want.state, want.detail)
		}
	}
}

// TestSyncReportAsTheAppWritesIt decodes the two documents the app's own
// tests pin as its encoder's output (AUMSyncReportTests in teemow/auv3-probe;
// the paths are made up there): keys sorted, slashes as they are, the lists
// always there and what does not apply left out.
func TestSyncReportAsTheAppWritesIt(t *testing.T) {
	const cycle = `{"app":"auv3-probe 1.2.3","at":"2026-01-02T03:04:05Z",` +
		`"audio_units":{"failed":1,"state":"syncing","synced":2,"total":4},` +
		`"conflicts":["Songs/Held.aumproj"],` +
		`"errors":[{"error":"refused","path":"Songs/Broken.aumproj"}],` +
		`"in_flight":[{"path":"Songs/Down.aumproj","to":"device"}],` +
		`"rev":42,"synced":1,"v":1}`
	const quiet = `{"at":"2026-01-02T03:04:05Z","conflicts":[],"errors":[],"in_flight":[],"rev":0,"synced":0,"v":1}`

	var k SyncReports
	if code := postSync(t, &k, []byte(cycle)); code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	rep, _ := k.Last()
	rep.Received = time.Time{}
	want := SyncReport{
		V: 1, App: "auv3-probe 1.2.3", At: "2026-01-02T03:04:05Z", Rev: 42, Synced: 1,
		Conflicts:  []string{"Songs/Held.aumproj"},
		Errors:     []SyncFileError{{Path: "Songs/Broken.aumproj", Error: "refused"}},
		InFlight:   []SyncTransfer{{Path: "Songs/Down.aumproj", To: SyncToDevice}},
		AudioUnits: &SyncAudioUnits{State: SyncUnitsSyncing, Total: 4, Synced: 2, Failed: 1},
	}
	if !reflect.DeepEqual(rep, want) {
		t.Errorf("the app's document decoded as\n%+v\nwant\n%+v", rep, want)
	}

	if code := postSync(t, &k, []byte(quiet)); code != http.StatusNoContent {
		t.Fatalf("status of a quiet cycle's report = %d, want 204", code)
	}
	rep, _ = k.Last()
	if rep.V != 1 || rep.Rev != 0 || rep.App != "" || rep.Error != "" || rep.AudioUnits != nil ||
		rep.Conflicts == nil || len(rep.Conflicts)+len(rep.Errors)+len(rep.InFlight) != 0 {
		t.Errorf("a quiet cycle's report decoded as %+v", rep)
	}
}

// TestSyncReportEmpty checks a report that lists nothing, and a document
// without any key, are reports like any other.
func TestSyncReportEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"nothing needs attention": `{"v":1,"at":"2026-01-02T03:04:05Z","rev":7,"synced":3,"conflicts":[],"errors":[],"in_flight":[]}`,
		"mirrors nothing":         `{"v":1,"at":"2026-01-02T03:04:05Z","rev":7,"synced":0,"conflicts":[],"errors":[],"in_flight":[],"error":"no aum folder linked yet"}`,
		"no keys":                 `{}`,
		"null lists":              `{"conflicts":null,"errors":null,"in_flight":null,"audio_units":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var k SyncReports
			if code := postSync(t, &k, []byte(body)); code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", code)
			}
			rep, ok := k.Last()
			if !ok || rep.Received.IsZero() {
				t.Fatalf("kept = %+v, %v", rep, ok)
			}
			if len(rep.Conflicts)+len(rep.Errors)+len(rep.InFlight) != 0 || rep.AudioUnits != nil {
				t.Errorf("report = %+v, want nothing listed", rep)
			}
			if state, _ := rep.File("Songs/Any.aumproj"); state != SyncSettled {
				t.Errorf("a file of an empty report is %v, want settled", state)
			}
			if wantErr := strings.Contains(body, `"error"`); (rep.Error != "") != wantErr {
				t.Errorf("error = %q", rep.Error)
			}
		})
	}
}

// TestSyncReportMalformed checks a body that is no report is refused and
// replaces nothing.
func TestSyncReportMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"no body":          ``,
		"not JSON":         `rev=42`,
		"cut short":        `{"v":1,"rev":42,"conflicts":["Songs/Held.aum`,
		"an array":         `[{"v":1}]`,
		"a list as a text": `{"v":1,"rev":42,"conflicts":"Songs/Held.aumproj"}`,
		"a rev as a text":  `{"v":1,"rev":"42"}`,
		"two documents":    `{"v":1,"rev":42}{"v":1,"rev":43}`,
	} {
		t.Run(name, func(t *testing.T) {
			var k SyncReports
			k.Keep(SyncReport{Rev: 1})
			if code := postSync(t, &k, []byte(body)); code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if rep, _ := k.Last(); rep.Rev != 1 {
				t.Errorf("a malformed report replaced the last one: %+v", rep)
			}
		})
	}
}

// TestSyncReportOversized checks the route reads no more than its bound.
func TestSyncReportOversized(t *testing.T) {
	var k SyncReports
	// A report that is well-formed and too long: one path of 1 MiB.
	body := `{"v":1,"rev":42,"conflicts":["` + strings.Repeat("a", maxSyncBytes) + `"]}`
	if code := postSync(t, &k, []byte(body)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", code)
	}
	if _, ok := k.Last(); ok {
		t.Error("an oversized report was kept")
	}
	// One just under the bound is taken.
	body = `{"v":1,"rev":42,"conflicts":["` + strings.Repeat("a", maxSyncBytes-64) + `"]}`
	if code := postSync(t, &k, []byte(body)); code != http.StatusNoContent {
		t.Fatalf("status of a report under the bound = %d, want 204", code)
	}
}

// TestSyncRouteBesideDownloads checks the route takes nothing from the file
// routes beside it, and answers without anybody listening.
func TestSyncRouteBesideDownloads(t *testing.T) {
	ts := httptest.NewServer(Handler(t.TempDir(), nil, nil, nil))
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/aum-session/sync", "application/json", strings.NewReader(fullReport))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status without a callback = %d, want 204", resp.StatusCode)
	}
	// GET on the same path is the download route's, which knows no staged
	// file of that name (it is not a session's or a mapping's).
	resp, err = http.Get(ts.URL + "/aum-session/sync")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET status = %d, want the download route's 400", resp.StatusCode)
	}
}

// TestSyncReportsNext checks every report wakes those waiting, once.
func TestSyncReportsNext(t *testing.T) {
	var k SyncReports
	a, b := k.Next(), k.Next()
	if a != b {
		t.Error("two waits for the same report got different channels")
	}
	k.Keep(SyncReport{Rev: 1})
	select {
	case <-a:
	default:
		t.Fatal("the report woke nobody")
	}
	c := k.Next()
	select {
	case <-c:
		t.Fatal("a wait that began after the report was woken by it")
	default:
	}
	k.Keep(SyncReport{Rev: 2})
	<-c
	if rep, _ := k.Last(); rep.Rev != 2 {
		t.Errorf("last = rev %d, want 2", rep.Rev)
	}
}

// TestSyncReportsLogNoPaths checks what the log gets of a report: its counts,
// once for as long as they stay the same, and none of its paths or texts.
func TestSyncReportsLogNoPaths(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	var k SyncReports
	rep := SyncReport{
		V: 1, App: "auv3-probe 0.4.0", Rev: 42, Synced: 131,
		Conflicts:  []string{"Songs/Held.aumproj"},
		Errors:     []SyncFileError{{Path: "Songs/Broken.aumproj", Error: "refused by the controller"}},
		InFlight:   []SyncTransfer{{Path: "Songs/Down.aumproj", To: SyncToDevice}},
		Error:      "could not open the linked aum folder: /private/var/Folder",
		AudioUnits: &SyncAudioUnits{State: SyncUnitsSyncing, Total: 96, Synced: 60, Failed: 1},
	}
	k.Keep(rep)
	line := buf.String()
	for _, want := range []string{`"auv3-probe 0.4.0"`, "rev 42", "131 in sync, 1 conflict(s), 1 error(s), 1 in flight", "the app copies no file for now", `audio units "syncing" 60/96, 1 failed`} {
		if !strings.Contains(line, want) {
			t.Errorf("the log lacks %q: %s", want, line)
		}
	}
	for _, not := range []string{"Songs", "aumproj", "refused", "Folder"} {
		if strings.Contains(line, not) {
			t.Errorf("the log holds %q of the report: %s", not, line)
		}
	}

	// The next cycle's report, 20 s on, says the same of another rev: no line.
	buf.Reset()
	rep.Rev, rep.At = 43, "2026-01-02T03:04:25Z"
	k.Keep(rep)
	if buf.Len() != 0 {
		t.Errorf("a report that says the same was logged again: %s", buf.String())
	}
	rep.Conflicts = nil
	k.Keep(rep)
	if !strings.Contains(buf.String(), "0 conflict(s)") {
		t.Errorf("a report that says something new was not logged: %s", buf.String())
	}
}
