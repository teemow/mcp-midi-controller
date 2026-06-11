package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
)

// TestSessionSwitchRegistryPinsPrograms exercises the registry lifecycle via
// the MCP handlers: auto-assignment, explicit pinning, idempotent
// re-registration, the never-renumber guarantee, and removal leaving a hole
// that the next registration reuses.
func TestSessionSwitchRegistryPinsPrograms(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)
	for _, id := range []string{"song_a", "song_b", "song_c"} {
		authorConventionSession(t, s, id, "Gitarre")
	}

	// First registration gets the lowest free program (0).
	res := call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_a"})
	if res.IsError {
		t.Fatalf("register song_a failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "program 0") {
		t.Fatalf("song_a not pinned to program 0:\n%s", resultText(res))
	}

	// Explicit program pin.
	res = call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_b", "program": 5})
	if res.IsError || !strings.Contains(resultText(res), "program 5") {
		t.Fatalf("register song_b on program 5 failed: %s", resultText(res))
	}

	// Re-registering without a program is idempotent (keeps the pin).
	res = call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_a"})
	if res.IsError || !strings.Contains(resultText(res), "already registered on program 0") {
		t.Fatalf("re-register song_a not idempotent: %s", resultText(res))
	}

	// Programs are never renumbered: re-pinning to a different program fails.
	res = call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_a", "program": 9})
	if !res.IsError || !strings.Contains(resultText(res), "never renumbered") {
		t.Fatalf("re-pin of song_a did not refuse: %s", resultText(res))
	}

	// A taken program is refused.
	res = call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_c", "program": 5})
	if !res.IsError || !strings.Contains(resultText(res), "already pinned") {
		t.Fatalf("program collision not refused: %s", resultText(res))
	}

	// The registry is persisted in the state dir and reloads.
	reg, err := loadSessionSwitchRegistry()
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if len(reg.Entries) != 2 || reg.byID("song_a").Program != 0 || reg.byID("song_b").Program != 5 {
		t.Fatalf("persisted registry = %+v, want song_a@0 + song_b@5", reg.Entries)
	}

	// list carries the AUM Learn cheat-sheet per entry.
	res = call(t, s.handleListAUMSessionSwitches, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), "Session Load") || !strings.Contains(resultText(res), "ch16") {
		t.Fatalf("list missing the setup cheat-sheet:\n%s", resultText(res))
	}

	// Removing leaves a hole (song_b keeps program 5)...
	res = call(t, s.handleRemoveAUMSessionSwitch, map[string]any{"session": "song_a"})
	if res.IsError {
		t.Fatalf("remove song_a failed: %s", resultText(res))
	}
	reg, _ = loadSessionSwitchRegistry()
	if reg.byID("song_a") != nil || reg.byID("song_b").Program != 5 {
		t.Fatalf("after remove, registry = %+v, want only song_b@5", reg.Entries)
	}
	// ...and the next registration reuses the hole.
	res = call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_c"})
	if res.IsError || !strings.Contains(resultText(res), "program 0") {
		t.Fatalf("song_c did not reuse the freed program 0: %s", resultText(res))
	}
}

// TestBuildControlSurfaceIncludesSessions asserts every manifest push carries
// the session-switch registry as the sessions section, ordered by program with
// the current session marked, on the reserved session-switch channel.
func TestBuildControlSurfaceIncludesSessions(t *testing.T) {
	s := newAutoImportServer(t)
	stageProbe(t)
	authorConventionSession(t, s, "song_a", "Gitarre")
	authorConventionSession(t, s, "song_b", "Keys")

	for _, id := range []string{"song_a", "song_b"} {
		if res := call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": id}); res.IsError {
			t.Fatalf("register %s failed: %s", id, resultText(res))
		}
	}
	s.setCurrentAUMSession("song_b")

	o, err := s.importSessionRig(config.AUMSessionsDir()+"/song_b.aumproj", false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	frame := s.buildControlSurface(o)

	if len(frame.Sessions) != 2 {
		t.Fatalf("frame sessions = %+v, want 2 entries", frame.Sessions)
	}
	a, b := frame.Sessions[0], frame.Sessions[1]
	if a.Program != 0 || b.Program != 1 {
		t.Fatalf("sessions not ordered by program: %+v", frame.Sessions)
	}
	if a.Channel != device.SessionSwitchChannel || b.Channel != device.SessionSwitchChannel {
		t.Fatalf("sessions not on the session-switch channel: %+v", frame.Sessions)
	}
	if a.Current || !b.Current {
		t.Fatalf("current marker wrong (want song_b current): %+v", frame.Sessions)
	}
	if a.Name != "song_a" || b.Name != "song_b" {
		t.Fatalf("session names = %q/%q, want the session titles", a.Name, b.Name)
	}
}

// TestSwitchAUMSessionUpdatesCurrentSession runs the full daemon-side switch:
// the tool sends the pinned PC on the session-switch channel through the brain
// hub, sets the target as the current session, and re-imports its rig.
func TestSwitchAUMSessionUpdatesCurrentSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	hub := midicontrol.NewHub()
	mux := http.NewServeMux()
	midicontrol.Register(mux, hub, midicontrol.Callbacks{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), WithMidiControl(hub))
	stageProbe(t)
	authorConventionSession(t, s, "song_a", "Gitarre")
	authorConventionSession(t, s, "song_b", "Keys")
	for _, id := range []string{"song_a", "song_b"} {
		if res := call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": id}); res.IsError {
			t.Fatalf("register %s failed: %s", id, resultText(res))
		}
	}

	// With no brain connected the switch fails (the PC has nowhere to go).
	res := call(t, s.handleSwitchAUMSession, map[string]any{"session": "song_b"})
	if !res.IsError || !strings.Contains(resultText(res), "no ProbeMidiBrain connected") {
		t.Fatalf("switch without a brain did not fail: %s", resultText(res))
	}

	// Connect a brain (a plain ws client standing in for ProbeMidiBrain).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/midi-control", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.CloseNow() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !hub.Connected() {
		time.Sleep(10 * time.Millisecond)
	}
	if !hub.Connected() {
		t.Fatal("hub never registered the connection")
	}

	res = call(t, s.handleSwitchAUMSession, map[string]any{"session": "song_b"})
	if res.IsError {
		t.Fatalf("switch failed: %s", resultText(res))
	}

	// The brain received the session-switch PC (program 1 = song_b) on the
	// reserved channel, before any re-pushed manifest frame.
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cmd midicontrol.Command
	if err := json.Unmarshal(data, &cmd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cmd.Type != "pc" || cmd.Program != 1 || cmd.Channel != device.SessionSwitchChannel {
		t.Fatalf("switch sent %+v, want pc program 1 on channel %d", cmd, device.SessionSwitchChannel)
	}

	// The daemon followed: current session updated, the new session's rig
	// imported (keys device exists, song_a's gitarre is gone).
	if id := s.currentAUMSessionID(); id != "song_b" {
		t.Fatalf("current session = %q, want song_b", id)
	}
	if _, ok := s.eng.DeviceFor("keys"); !ok {
		t.Fatal("switch did not import the target session's rig")
	}

	// Switching by program resolves the same way.
	res = call(t, s.handleSwitchAUMSession, map[string]any{"program": 0})
	if res.IsError {
		t.Fatalf("switch by program failed: %s", resultText(res))
	}
	if id := s.currentAUMSessionID(); id != "song_a" {
		t.Fatalf("current session = %q, want song_a", id)
	}
}

// TestOnBrainSessionSwitchSyncsDaemon covers the upstream direction: a
// brain-side switcher tap sends a sessionSwitch frame; the daemon resolves the
// program via the registry, updates the current session and re-imports.
func TestOnBrainSessionSwitchSyncsDaemon(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)
	authorConventionSession(t, s, "song_a", "Gitarre")
	if res := call(t, s.handleRegisterAUMSessionSwitch, map[string]any{"session": "song_a"}); res.IsError {
		t.Fatalf("register failed: %s", resultText(res))
	}

	// An unpinned program is ignored (logged, no state change).
	s.OnBrainSessionSwitch(42)
	time.Sleep(100 * time.Millisecond) // let the async resolver run
	if id := s.currentAUMSessionID(); id != "" {
		t.Fatalf("unpinned program set current session %q", id)
	}

	s.OnBrainSessionSwitch(0)
	waitFor(t, 2*time.Second, func() bool {
		_, ok := s.eng.DeviceFor("gitarre")
		return ok && s.currentAUMSessionID() == "song_a"
	})
}

// waitFor polls cond until it holds, failing the test when the timeout
// elapses first (the OnBrainSessionSwitch work runs async because the
// receiver callback must not block).
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held within the timeout")
}
