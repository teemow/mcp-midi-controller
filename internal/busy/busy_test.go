package busy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// clock is a stand-in time source the tests move by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTracker() (*Tracker, *clock) {
	c := &clock{t: time.Date(2026, 11, 13, 4, 17, 3, 0, time.Local)}
	t := New()
	t.now = c.now
	return t, c
}

func TestIdleUntilSomethingBegins(t *testing.T) {
	tr, _ := newTracker()
	if got := tr.Report(); got != "idle\n" {
		t.Fatalf("a fresh tracker reports %q, want idle", got)
	}
	end := tr.Begin(`play_song "battalion"`)
	if got := len(tr.Active()); got != 1 {
		t.Fatalf("%d activities under way, want 1", got)
	}
	end()
	if got := tr.Report(); got != "idle\n" {
		t.Fatalf("after the end the tracker reports %q, want idle", got)
	}
}

func TestReportListsTheOldestFirstWithHowLongEachRan(t *testing.T) {
	tr, c := newTracker()
	endStems := tr.Begin(`record_song_stems "battalion"`)
	defer endStems()
	c.t = c.t.Add(90 * time.Second)
	endPlay := tr.Begin(`play_song "battalion"`)
	defer endPlay()
	c.t = c.t.Add(30 * time.Second)

	want := "busy\n" +
		`record_song_stems "battalion", for 2m0s (since 04:17:03)` + "\n" +
		`play_song "battalion", for 30s (since 04:18:33)` + "\n"
	if got := tr.Report(); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
}

// TestEndingTwiceEndsOnce checks a second call of an end does not take away
// another activity of the same name.
func TestEndingTwiceEndsOnce(t *testing.T) {
	tr, _ := newTracker()
	first := tr.Begin("tool call load_song")
	second := tr.Begin("tool call load_song")
	defer second()
	first()
	first()
	if got := len(tr.Active()); got != 1 {
		t.Fatalf("%d activities under way after one of two ended twice, want 1", got)
	}
}

func TestHandlerServesTheReport(t *testing.T) {
	tr, _ := newTracker()
	end := tr.Begin("recording \"take\"")
	defer end()
	srv := httptest.NewServer(tr.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/busy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type %q", got)
	}
	if got, want := string(body), tr.Report(); got != want {
		t.Fatalf("body %q, want %q", got, want)
	}
}
