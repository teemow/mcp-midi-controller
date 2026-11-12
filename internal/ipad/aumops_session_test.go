package ipad

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// sessionScreens is AUM while "Night Drive" is opened from the Songs folder.
func sessionScreens(loadingReads int, notices ...string) func(gestures, reads int) json.RawMessage {
	return ipadtest.OpenSessionScreens("Songs", "Night Drive", []ipadtest.Strip{
		{Title: "MIDI 1"}, {Title: "2: bass", Level: "-4.0 dB"}, {Title: "3: pad", Level: "+0.0 dB"}, {Title: "4: Master", Level: "+0.0 dB"},
	}, loadingReads, notices...)
}

func openFake(t *testing.T, fn func(gestures, reads int) json.RawMessage) (*ipadtest.WDA, *Client) {
	t.Helper()
	w := ipadtest.New(fn(0, 0))
	w.SourceFunc = fn
	t.Cleanup(w.Close)
	old := AUMLoadPause
	AUMLoadPause = 10 * time.Millisecond
	t.Cleanup(func() { AUMLoadPause = old })
	return w, New(w.URL)
}

// TestAUMOpenSession opens a session with a plugin window in the way: the
// window is closed first, the session is loaded from its folder, and the call
// returns only once AUM has stopped loading, with the bare mixer.
func TestAUMOpenSession(t *testing.T) {
	w, c := openFake(t, sessionScreens(3))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, ok := res.Data.(AUMLoaded)
	if !ok || !res.Verified {
		t.Fatalf("open: %+v", res)
	}
	if got.Session != "Night Drive" || len(got.Strips) != 4 || got.Strips[1] != "2: bass" || len(got.Problems) != 0 {
		t.Errorf("loaded = %+v", got)
	}
	if !strings.Contains(res.Message, `AUM has "Night Drive" open: 4 strip(s)`) {
		t.Errorf("message = %q", res.Message)
	}
	// ✕, ☰, FILES, Sessions, Songs, the row, ☰, the menu's close icon.
	if g := w.GestureLog(); len(g) != 8 {
		t.Errorf("gestures = %v, want 8", g)
	}
	if len(res.State.Overlays) != 0 || len(res.State.PluginWindows) != 0 {
		t.Errorf("the screen was not left bare: %+v", res.State.Overlays)
	}
}

// TestAUMOpenSessionReportsProblems checks a plugin AUM names as failed is
// reported with the loaded session.
func TestAUMOpenSessionReportsProblems(t *testing.T) {
	_, c := openFake(t, sessionScreens(0, "Acme: PolySynth failed to load: the plugin was invalidated"))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "PolySynth failed") {
		t.Errorf("problems = %v", got.Problems)
	}
	if !strings.Contains(res.Message, "on screen: Acme: PolySynth failed") {
		t.Errorf("message = %q", res.Message)
	}
}

// TestAUMOpenSessionStillLoading checks a session that never finishes loading
// is reported as not verified instead of being waited for forever.
func TestAUMOpenSessionStillLoading(t *testing.T) {
	old := aumLoadTimeout
	aumLoadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { aumLoadTimeout = old })
	_, c := openFake(t, sessionScreens(1<<30))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if res.Verified || !strings.Contains(res.Message, "still shows LOADING STATE") {
		t.Errorf("a session that kept loading was reported loaded: %+v", res.Message)
	}
}

// slowSessionScreens is AUM loading a heavy session from the Songs folder:
// for slow after the row's tap it shows LOADING STATE and no menu, whatever
// is tapped (its UI does not answer), and the first read after ☰ blocks for
// block. When the load is over the menu it opened is gone; ☰ then shows
// endName (none: the menu does not open), and the next gesture closes it.
func slowSessionScreens(slow, block time.Duration, endName string) func(gestures, reads int) json.RawMessage {
	strips := []ipadtest.Strip{{Title: "1: Battalion"}, {Title: "2: Master", Level: "+0.0 dB"}}
	before := ipadtest.OpenSessionScreens("Songs", "Heavy", strips, 0)
	fresh := ipadtest.AUM{Tempo: "104", Position: "001:1  00:00.0", Strips: strips}
	loading := fresh
	loading.Notices = []string{ipadtest.LoadingNotice}
	menu := fresh
	menu.Session = endName
	var tapped time.Time
	blocked := false
	over := -1 // the gestures so far when the load was first seen over
	return func(gestures, reads int) json.RawMessage {
		if gestures < 6 {
			return before(gestures, reads)
		}
		if tapped.IsZero() {
			tapped = time.Now()
		}
		if time.Since(tapped) < slow {
			if gestures > 6 && !blocked {
				blocked = true
				time.Sleep(block)
			}
			return loading.Source()
		}
		if over < 0 {
			over = gestures
		}
		switch {
		case gestures == over || endName == "":
			return fresh.Source()
		case gestures == over+1:
			return menu.Source()
		default:
			return fresh.Source()
		}
	}
}

// shortLoadWaits scales the load's waits down so the slow-load tests take
// a second, with the same proportions as on a device: AUM's UI stays busy
// for longer than the name is waited for once the load is over.
func shortLoadWaits(t *testing.T) {
	t.Helper()
	oldLoad, oldName, oldWait := aumLoadTimeout, aumNameWait, aumWait
	aumLoadTimeout, aumNameWait, aumWait = 30*time.Second, 100*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { aumLoadTimeout, aumNameWait, aumWait = oldLoad, oldName, oldWait })
}

// TestAUMOpenSessionSlowLoad is a heavy session on a device: AUM's UI blocks
// a read and shows no session name for longer than the name is waited for,
// the load closes the menu, and only then does the menu show the name. The
// load is verified because AUM ends up with the session open within
// aumLoadTimeout.
func TestAUMOpenSessionSlowLoad(t *testing.T) {
	shortLoadWaits(t)
	_, c := openFake(t, slowSessionScreens(600*time.Millisecond, 400*time.Millisecond, "Heavy"))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Heavy")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !res.Verified || !strings.Contains(res.Message, `AUM has "Heavy" open: 2 strip(s)`) {
		t.Fatalf("a session AUM was slow to load was not verified: %q", res.Message)
	}
	if got := res.Data.(AUMLoaded); got.Session != "Heavy" || got.LoadMS < 600 {
		t.Errorf("loaded = %+v", got)
	}
}

// TestAUMFilesLoadSlow is the same load through aum_files: the bare load
// action waits for the name as long.
func TestAUMFilesLoadSlow(t *testing.T) {
	shortLoadWaits(t)
	w, c := openFake(t, slowSessionScreens(600*time.Millisecond, 400*time.Millisecond, "Heavy"))
	// aum_files load does not clear the screen first: start on the bare mixer.
	before := w.SourceFunc
	w.SourceFunc = func(g, r int) json.RawMessage { return before(g+1, r) }
	res, err := c.AUMFiles(context.Background(), AUMRequest{Action: "load", Path: []string{"Songs"}, Name: "Heavy", Confirm: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !res.Verified {
		t.Fatalf("load not verified: %q", res.Message)
	}
}

// TestAUMOpenSessionSlowLoadWrongSession checks a slow load that ends with
// another session, or with no name to be read, still fails and says what
// was seen.
func TestAUMOpenSessionSlowLoadWrongSession(t *testing.T) {
	for _, tc := range []struct{ name, end, want string }{
		{"another", "Other Session", `session now "Other Session" (wanted "Heavy")`},
		{"none", "", `session now "" (wanted "Heavy")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortLoadWaits(t)
			// It gives up once the name has had its time after the load,
			// not at aumLoadTimeout (long here, so a busy machine does not
			// blur the two).
			aumLoadTimeout = time.Minute
			_, c := openFake(t, slowSessionScreens(600*time.Millisecond, 400*time.Millisecond, tc.end))
			start := time.Now()
			res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Heavy")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if res.Verified || res.Message != tc.want {
				t.Errorf("verified = %v, message = %q, want %q", res.Verified, res.Message, tc.want)
			}
			if took := time.Since(start); took > 30*time.Second {
				t.Errorf("took %s: %q", took, res.Steps)
			}
		})
	}
}

// TestAUMOpenSessionLoadsForever checks a load that never ends and never
// shows the name fails at aumLoadTimeout, saying AUM is still loading.
func TestAUMOpenSessionLoadsForever(t *testing.T) {
	shortLoadWaits(t)
	aumLoadTimeout = 500 * time.Millisecond
	_, c := openFake(t, slowSessionScreens(time.Hour, 0, "Heavy"))
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Heavy")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if res.Verified || !strings.Contains(res.Message, `session now "" (wanted "Heavy"); AUM still shows LOADING STATE after 500ms`) {
		t.Errorf("verified = %v, message = %q", res.Verified, res.Message)
	}
}

// TestAUMOpenSessionUnknown checks a session that is not in the folder is an
// error that lists what is.
func TestAUMOpenSessionUnknown(t *testing.T) {
	_, c := openFake(t, sessionScreens(0))
	_, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Nowhere")
	if err == nil || !strings.Contains(err.Error(), `no session "Nowhere" in Songs (entries: Night Drive)`) {
		t.Fatalf("open of an unknown session: %v", err)
	}
}

// freshScreens is AUM, just started, while "Night Drive" is opened from the
// Songs folder: its screen is drawn after two reads.
func freshScreens() func(gestures, reads int) json.RawMessage {
	return ipadtest.FreshSessionScreens("Songs", "Night Drive", []ipadtest.Strip{
		{Title: "MIDI 1"}, {Title: "2: bass", Level: "-4.0 dB"}, {Title: "3: pad", Level: "+0.0 dB"}, {Title: "4: Master", Level: "+0.0 dB"},
	}, 2, 1)
}

// TestAUMOpenSessionStartsAUMAgain is AUM going away under the load, as it
// crashed on a device while it replaced the open session: the tap on the
// session's row takes it down, the session's next call says it is not
// running, and the Home Screen is in front. The load is made once more in a
// new session, which starts AUM, and says so.
func TestAUMOpenSessionStartsAUMAgain(t *testing.T) {
	w, c := openFake(t, sessionScreens(1))
	// The started AUM has not drawn its screen at the first two reads: it is
	// not tapped before it has.
	fresh, drawn, early := freshScreens(), false, false
	w.AfterLaunch = func(g, r int) json.RawMessage {
		drawn = drawn || (g == 0 && r >= 2) // the fake's lock is held
		early = early || (g > 0 && !drawn)
		return fresh(g, r)
	}
	w.SetAUMCrashes(6, 1) // ✕, ☰, FILES, Sessions, Songs, the row
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, ok := res.Data.(AUMLoaded)
	if !ok || !res.Verified {
		t.Fatalf("open: %+v", res)
	}
	if early || !drawn {
		t.Errorf("the started AUM was tapped before its screen was drawn (drawn %v)", drawn)
	}
	if got.Session != "Night Drive" || len(got.Strips) != 4 {
		t.Errorf("loaded = %+v", got)
	}
	const gone = "WebDriverAgent: invalid element state: The application under test with bundle id 'com.kymatica.AUM' is not running, possibly crashed"
	if got.Restarted != gone {
		t.Errorf("restarted = %q, want what WebDriverAgent said of AUM", got.Restarted)
	}
	if want := "AUM was not running (" + gone + `): the daemon started it again and loaded the session once more. AUM has "Night Drive" open: 4 strip(s)`; res.Message != want {
		t.Errorf("message = %q, want %q", res.Message, want)
	}
	if n := w.Launches(); n != 1 {
		t.Errorf("AUM was started %d time(s), want once", n)
	}
	if w.AUMDown() {
		t.Error("AUM is not running after the load")
	}
	if app, _ := c.Front(); app != AUMBundleID {
		t.Errorf("Front = %q, want AUM after it was started again", app)
	}
	if n := w.OpenSessions(); n != 0 {
		t.Errorf("%d WebDriverAgent sessions left open", n)
	}
	// In the AUM that was started: ☰, FILES, Sessions, Songs, the row, ☰, the
	// menu's close icon. Its start screen is not tapped: nothing of a last
	// state is loaded on the way.
	if g := w.Gestures(); g != 7 {
		t.Errorf("gestures in the started AUM = %d, want 7", g)
	}
}

// TestAUMOpenSessionStartedAUMNeverShows is an AUM started again that does
// not draw its screen: the load ends on that before a gesture, which goes by
// coordinates and would land on whatever is there instead.
func TestAUMOpenSessionStartedAUMNeverShows(t *testing.T) {
	old := aumStartWait
	aumStartWait = 100 * time.Millisecond
	t.Cleanup(func() { aumStartWait = old })
	w, c := openFake(t, sessionScreens(1))
	w.AfterLaunch = ipadtest.FreshSessionScreens("Songs", "Night Drive", []ipadtest.Strip{{Title: "4: Master"}}, 1<<30, 1)
	w.SetAUMCrashes(6, 1) // ✕, ☰, FILES, Sessions, Songs, the row
	res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
	if err == nil {
		t.Fatalf("a load in an AUM that never showed its screen succeeded: %+v", res)
	}
	if !strings.Contains(err.Error(), "did not show its screen within") {
		t.Errorf("err = %q, want it to say AUM did not show its screen", err)
	}
	if n := w.Launches(); n != 1 {
		t.Errorf("AUM was started %d time(s), want once", n)
	}
	if g := w.Gestures(); g != 0 {
		t.Errorf("gestures in the started AUM = %d, want none", g)
	}
	if n := w.OpenSessions(); n != 0 {
		t.Errorf("%d WebDriverAgent sessions left open", n)
	}
}

// TestAUMOpenSessionAUMGoesAwayAgain checks the load is made once more and
// no more often: an AUM that goes away at every load, or does not start, ends
// the load with what WebDriverAgent said both times, and the Home Screen is
// known to be in front.
func TestAUMOpenSessionAUMGoesAwayAgain(t *testing.T) {
	const first = "(AUM had been started again after: WebDriverAgent: invalid element state: The application under test with bundle id 'com.kymatica.AUM' is not running, possibly crashed)"
	for _, tc := range []struct {
		name string
		// set makes the AUM that went away at the row's tap go away again.
		set      func(w *ipadtest.WDA)
		want     string
		launches int
	}{
		{"at every load", func(w *ipadtest.WDA) {
			fresh := freshScreens()
			w.AfterLaunch = func(g, r int) json.RawMessage {
				// In the started AUM the row is the fifth gesture. The
				// fake's lock is held here.
				w.AUMCrashAt, w.AUMCrashes = 5, 1
				return fresh(g, r)
			}
		}, "WebDriverAgent: invalid element state: The application under test with bundle id 'com.kymatica.AUM' is not running, possibly crashed " + first, 1},
		{"does not start", func(w *ipadtest.WDA) { w.AUMStaysDown = true },
			"WebDriverAgent: session not created: Cannot launch com.kymatica.AUM application. Make sure the correct bundle identifier has been provided in capabilities and check the device log for possible crash report occurrences " + first, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, c := openFake(t, sessionScreens(1))
			w.SetAUMCrashes(6, 1) // ✕, ☰, FILES, Sessions, Songs, the row
			tc.set(w)
			res, err := c.AUMOpenSession(context.Background(), []string{"Songs"}, "Night Drive")
			if err == nil {
				t.Fatalf("a load in an AUM that went away succeeded: %+v", res)
			}
			if !AppNotRunning(err) {
				t.Errorf("AppNotRunning(%v) = false", err)
			}
			if err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err, tc.want)
			}
			if n := w.Launches(); n != tc.launches {
				t.Errorf("AUM was started %d time(s), want %d", n, tc.launches)
			}
			if !w.AUMDown() {
				t.Error("AUM runs: the test's AUM should have gone away")
			}
			if app, _ := c.Front(); app != HomeScreenBundleID {
				t.Errorf("Front = %q, want the Home Screen once AUM went away", app)
			}
			if n := w.OpenSessions(); n != 0 {
				t.Errorf("%d WebDriverAgent sessions left open", n)
			}
		})
	}
}

// TestAppNotRunning checks which of WebDriverAgent's errors say that the app
// is not there.
func TestAppNotRunning(t *testing.T) {
	went := &Error{Status: 500, Code: "invalid element state", Message: "The application under test with bundle id 'com.kymatica.AUM' is not running, possibly crashed"}
	for _, tc := range []struct {
		err error
		// away: the app went away under its session; not: it is not running.
		away, not bool
	}{
		{went, true, true},
		{fmt.Errorf("%w (AUM had been started again after: x)", went), true, true},
		// An app that was not up after its start left nothing behind: what
		// was in front before still is.
		{&Error{Status: 500, Code: "session not created", Message: "Cannot launch com.kymatica.AUM application. Make sure the correct bundle identifier has been provided in capabilities and check the device log for possible crash report occurrences"}, false, true},
		// A locked iPad starts no app, and is not an app that went away.
		{&Error{Status: 500, Code: "unknown error", Message: "Unable to launch com.kymatica.AUM because the device was not, or could not be, unlocked."}, false, false},
		{&Error{Status: 404, Code: "invalid session id", Message: "session S1 does not exist"}, false, false},
		{&Error{Message: "dial tcp 127.0.0.1:8100: connect: connection refused"}, false, false},
		{context.DeadlineExceeded, false, false},
		{nil, false, false},
	} {
		if got := AppWentAway(tc.err); got != tc.away {
			t.Errorf("AppWentAway(%v) = %v, want %v", tc.err, got, tc.away)
		}
		if got := AppNotRunning(tc.err); got != tc.not {
			t.Errorf("AppNotRunning(%v) = %v, want %v", tc.err, got, tc.not)
		}
	}
}

// TestAUMSession reads the open session's name and leaves the mixer bare.
func TestAUMSession(t *testing.T) {
	menu := ipadtest.DefaultAUM()
	bare := mixer()
	w, c := opFake(t, bare, menu, bare)
	res, err := c.AUMSession(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if got.Session != "test-session (mcp test)" || len(got.Strips) != 6 {
		t.Errorf("session = %+v", got)
	}
	if g := w.GestureLog(); len(g) != 2 {
		t.Errorf("gestures = %v, want the menu opened and closed", g)
	}
}

// TestAUMSessionSeesAFailedNode checks a node AUM draws as failed is found:
// AUM says it only in the node's colour, a ring of red stripes.
// TestAUMMixer reads the strips and a failed node without the session's name:
// AUM's menu stays shut.
func TestAUMMixer(t *testing.T) {
	bare := mixer()
	w, c := opFake(t, bare, ipadtest.DefaultAUM(), bare)
	st := ParseAUM(mustParse(t, bare.Source()), Size{Width: 1210, Height: 834})
	var source Rect
	for _, n := range st.Channels[3].Nodes {
		if n.Role == "source" {
			source = n.Frame
		}
	}
	w.RedRings = [][4]float64{{source.X, source.Y, source.Width, source.Height}}
	res, err := c.AUMMixer(context.Background())
	if err != nil {
		t.Fatalf("mixer: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if got.Session != "" || len(got.Strips) != 6 {
		t.Errorf("mixer = %+v, want the six strips and no session name", got)
	}
	if len(got.Failed) != 1 || got.Failed[0] != "4: Synth Two" || len(got.Problems) != 1 {
		t.Errorf("failed = %v, problems = %v, want the fourth strip's node", got.Failed, got.Problems)
	}
	if g := w.GestureLog(); len(g) != 0 {
		t.Errorf("gestures = %v, want none: the menu is not opened", g)
	}
}

func TestAUMSessionSeesAFailedNode(t *testing.T) {
	menu := ipadtest.DefaultAUM()
	bare := mixer()
	w, c := opFake(t, bare, menu, bare)
	st := ParseAUM(mustParse(t, bare.Source()), Size{Width: 1210, Height: 834})
	// The fourth strip's source node failed.
	var source Rect
	for _, n := range st.Channels[3].Nodes {
		if n.Role == "source" {
			source = n.Frame
		}
	}
	if source.Empty() {
		t.Fatalf("no source node on strip 4: %+v", st.Channels[3].Nodes)
	}
	w.RedRings = [][4]float64{{source.X, source.Y, source.Width, source.Height}}
	res, err := c.AUMSession(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	got := res.Data.(AUMLoaded)
	if len(got.Failed) != 1 || got.Failed[0] != "4: Synth Two" {
		t.Fatalf("failed strips = %v, want the fourth", got.Failed)
	}
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], `AUM draws a node of "4: Synth Two" as failed`) {
		t.Errorf("problems = %v", got.Problems)
	}

	// The stand-in's screenshot is red in its top-left quarter, where the
	// first strips' nodes are: a screen red all over is no failed node.
	w.RedRings = nil
	w.SetScreens(bare.Source(), menu.Source(), bare.Source())
	res, err = c.AUMSession(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got := res.Data.(AUMLoaded); len(got.Failed) != 0 || len(got.Problems) != 0 {
		t.Errorf("without a failed node: %+v", got)
	}
}

func mustParse(t *testing.T, src json.RawMessage) *Element {
	t.Helper()
	root, err := ParseSource(src)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
