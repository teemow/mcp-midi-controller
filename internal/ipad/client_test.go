package ipad

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

func newFake(t *testing.T) (*ipadtest.WDA, *Client) {
	t.Helper()
	w := ipadtest.New(ipadtest.DefaultAUM().Source())
	t.Cleanup(w.Close)
	return w, New(w.URL + "/")
}

func TestStatusAndScreen(t *testing.T) {
	w, c := newFake(t)
	st, err := c.Status(context.Background())
	if err != nil || !st.Ready || st.OSVersion != "27.2" || st.WDAVersion != "16.15.2" {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	scr, err := c.ScreenInfo(context.Background())
	if err != nil || scr.Scale != 2 || scr.Width != 1210 {
		t.Fatalf("ScreenInfo = %+v, %v", scr, err)
	}
	app, err := c.ActiveApp(context.Background())
	if err != nil || app.BundleID != AUMBundleID || app.PID != 1214 {
		t.Fatalf("ActiveApp = %+v, %v", app, err)
	}
	for _, r := range w.Requests() {
		if strings.HasPrefix(r.Path, "/session") {
			t.Fatalf("status reads must not open a session: %s %s", r.Method, r.Path)
		}
	}
}

// WithApp binds the session to the app without relaunching or killing it,
// turns off the idle wait, and always deletes the session.
func TestWithAppCapabilitiesAndCleanup(t *testing.T) {
	w, c := newFake(t)
	boom := errors.New("boom")
	err := c.WithApp(context.Background(), AUMBundleID, func(s *Session) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("WithApp error = %v, want boom", err)
	}
	if w.OpenSessions() != 0 {
		t.Fatalf("session leaked after an error")
	}
	reqs := w.Requests()
	if len(reqs) != 3 || reqs[0].Path != "/session" || reqs[1].Path != "/session/S1/appium/settings" || reqs[2].Method != http.MethodDelete {
		t.Fatalf("unexpected request sequence: %+v", reqs)
	}
	caps := reqs[0].Body["capabilities"].(map[string]any)["alwaysMatch"].(map[string]any)
	if caps["bundleId"] != AUMBundleID || caps["forceAppLaunch"] != false || caps["shouldTerminateApp"] != false || caps["shouldWaitForQuiescence"] != false {
		t.Fatalf("capabilities = %v", caps)
	}
	settings := reqs[1].Body["settings"].(map[string]any)
	if settings["waitForIdleTimeout"] != float64(0) || settings["animationCoolOffTimeout"] != float64(0) {
		t.Fatalf("settings = %v", settings)
	}
}

func TestWDAErrors(t *testing.T) {
	w, c := newFake(t)
	w.FailSession = "session not created"
	err := c.WithApp(context.Background(), AUMBundleID, func(*Session) error { return nil })
	var we *Error
	if !errors.As(err, &we) || we.Status != 500 || we.Code != "session not created" || Unreachable(err) {
		t.Fatalf("WDA error = %#v", err)
	}

	w.Close()
	_, err = c.Status(context.Background())
	if !Unreachable(err) || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("closed server error = %v", err)
	}
}

// Another client opening a session ends ours, as on a real WDA.
func TestSessionLost(t *testing.T) {
	w, c := newFake(t)
	err := c.WithApp(context.Background(), AUMBundleID, func(s *Session) error {
		resp, err := http.Post(w.URL+"/session", "application/json", strings.NewReader(`{"capabilities":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return s.TapAt(context.Background(), 1, 2)
	})
	if !SessionLost(err) || Unreachable(err) {
		t.Fatalf("err = %v, want a lost session", err)
	}
}

func TestGestures(t *testing.T) {
	w, c := newFake(t)
	w.Labels[`button:"LAST STATE"`] = 1
	w.Labels[`"Recordings"`] = 2
	ctx := context.Background()
	err := c.WithApp(ctx, AUMBundleID, func(s *Session) error {
		if n, err := s.TapLabel(ctx, "LAST STATE", 0); err != nil || n != 1 {
			t.Fatalf("TapLabel button = %d, %v", n, err)
		}
		if n, err := s.TapLabel(ctx, "Recordings", 0); err != nil || n != 2 {
			t.Fatalf("TapLabel fallback = %d, %v", n, err)
		}
		if _, err := s.TapLabel(ctx, "Nope", 0); !errors.Is(err, ErrNoSuchLabel) {
			t.Fatalf("TapLabel missing = %v", err)
		}
		if err := s.TapAt(ctx, 156, 33); err != nil {
			return err
		}
		if err := s.Hold(ctx, 300, 140, 1200*time.Millisecond); err != nil {
			return err
		}
		return s.Swipe(ctx, 1100, 560, 300, 560, 300*time.Millisecond)
	})
	if err != nil {
		t.Fatal(err)
	}
	var tap, hold, actions map[string]any
	clicks := 0
	for _, r := range w.Requests() {
		switch {
		case strings.HasSuffix(r.Path, "/wda/tap"):
			tap = r.Body
		case strings.HasSuffix(r.Path, "/wda/touchAndHold"):
			hold = r.Body
		case strings.HasSuffix(r.Path, "/actions"):
			actions = r.Body
		case strings.HasSuffix(r.Path, "/click"):
			clicks++
		}
	}
	if clicks != 2 {
		t.Fatalf("want 2 clicks, got %d", clicks)
	}
	if tap["x"] != float64(156) || tap["y"] != float64(33) {
		t.Fatalf("tap body = %v", tap)
	}
	if hold["duration"] != 1.2 {
		t.Fatalf("hold body = %v", hold)
	}
	steps := actions["actions"].([]any)[0].(map[string]any)["actions"].([]any)
	if len(steps) != 4 || steps[2].(map[string]any)["duration"] != float64(300) || steps[1].(map[string]any)["type"] != "pointerDown" {
		t.Fatalf("swipe steps = %v", steps)
	}
}

func TestReadAndWaitAUM(t *testing.T) {
	w, c := newFake(t)
	st, err := c.ReadAUM(context.Background())
	if err != nil || st.TempoBPM == nil || *st.TempoBPM != 120 || len(st.Channels) != 6 {
		t.Fatalf("ReadAUM = %+v, %v", st, err)
	}

	// The tempo moves on the third read.
	before, after := ipadtest.DefaultAUM(), ipadtest.DefaultAUM()
	after.Tempo = "128"
	w.SetSources(before.Source(), before.Source(), after.Source())
	res, err := c.WaitAUM(context.Background(), Expectation{TempoBPM: f(128)}, 5*time.Second, time.Millisecond)
	if err != nil || !res.Matched || res.Reads != 3 || len(res.Mismatches) != 0 {
		t.Fatalf("WaitAUM = %+v, %v", res, err)
	}

	// Never there: times out with the remaining mismatch.
	res, err = c.WaitAUM(context.Background(), Expectation{TempoBPM: f(90)}, 30*time.Millisecond, time.Millisecond)
	if err != nil || res.Matched || res.Reads < 1 || !strings.Contains(strings.Join(res.Mismatches, ""), "want 90") {
		t.Fatalf("WaitAUM timeout = %+v, %v", res, err)
	}

	// A stopped transport needs two equal reads.
	w.SetSources(before.Source())
	res, err = c.WaitAUM(context.Background(), Expectation{Transport: "stopped"}, time.Second, time.Millisecond)
	if err != nil || !res.Matched || res.Reads != 2 {
		t.Fatalf("WaitAUM transport = %+v, %v", res, err)
	}
	if w.OpenSessions() != 0 {
		t.Fatal("session leaked")
	}
}

// Locked asks WDA whether the iPad is on its lock screen, without a session.
// A locked iPad fails a bound session, and nothing here tries to unlock it.
func TestLocked(t *testing.T) {
	w, c := newFake(t)
	ctx := context.Background()
	if locked, err := c.Locked(ctx); err != nil || locked {
		t.Fatalf("Locked = %v, %v on an iPad that is not", locked, err)
	}
	w.SetLocked(true)
	if locked, err := c.Locked(ctx); err != nil || !locked {
		t.Fatalf("Locked = %v, %v on a locked iPad", locked, err)
	}
	err := c.BringToFront(ctx, AUMBundleID)
	if err == nil || Unreachable(err) || !strings.Contains(err.Error(), "could not be, unlocked") {
		t.Fatalf("a locked iPad brought AUM to the front: %v", err)
	}
	if app, _ := c.Front(); app != "" {
		t.Fatalf("Front = %q after a session that did not open", app)
	}
	for _, r := range w.Requests() {
		if r.Path != "/wda/locked" && r.Path != "/session" {
			t.Fatalf("a locked iPad was asked %s %s: only its state is read", r.Method, r.Path)
		}
	}
}

// Front is the app the client brought to the front last, and since when.
func TestFront(t *testing.T) {
	w, c := newFake(t)
	ctx := context.Background()
	const other = "com.example.other"
	if app, since := c.Front(); app != "" || !since.IsZero() {
		t.Fatalf("Front = %q since %v before any session", app, since)
	}
	if err := c.BringToFront(ctx, other); err != nil {
		t.Fatal(err)
	}
	app, since := c.Front()
	if app != other || since.IsZero() {
		t.Fatalf("Front = %q since %v, want %q", app, since, other)
	}
	// More sessions in the same app, and one bound to none, leave the time.
	for _, bundle := range []string{other, ""} {
		if err := c.WithApp(ctx, bundle, func(*Session) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if again, still := c.Front(); again != other || !still.Equal(since) {
			t.Fatalf("after a session bound to %q: Front = %q since %v, want %q since %v", bundle, again, still, other, since)
		}
	}
	if err := c.BringToFront(ctx, AUMBundleID); err != nil {
		t.Fatal(err)
	}
	if app, _ := c.Front(); app != AUMBundleID {
		t.Fatalf("Front = %q, want AUM", app)
	}
	want := []string{other, other, AUMBundleID}
	if got := w.Fronted(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the iPad saw %q come forward, want %q", got, want)
	}
}
