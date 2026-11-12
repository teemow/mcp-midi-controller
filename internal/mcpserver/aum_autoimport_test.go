package mcpserver

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport/midicontrol"
)

// newAutoImportServer is newAUMServerWithBrain with the auto-import flag on,
// standing in for a daemon running with aum_auto_import: true.
func newAutoImportServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), WithAUMAutoImport(true))
	waitBackgroundAtCleanup(t, s)
	return s
}

// authorConventionSession stages a 2-strip session (Gitarre hosting the probe
// + Master) wired to the convention on send channel 3.
func authorConventionSession(t *testing.T, s *Server, outID, strip string) {
	t.Helper()
	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": outID,
		"title":  outID,
		"channels": []any{
			map[string]any{"kind": "audio", "title": strip, "nodes": []any{map[string]any{"probe_id": gtr1ProbeID}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author %s failed: %s", outID, resultText(res))
	}
}

func TestAUMAutoImportOnSessionDownload(t *testing.T) {
	s := newAutoImportServer(t)
	stageProbe(t)
	authorConventionSession(t, s, "rig_auto", "Gitarre")

	// The iPad downloading the staged session triggers the import.
	s.OnAUMSessionDownloaded("rig_auto.aumproj")

	if _, ok := s.eng.DeviceFor("aum"); !ok {
		t.Fatal("download did not auto-create the session device")
	}
	if d, ok := s.eng.DeviceFor("gitarre"); !ok || d.Session != "rig_auto" {
		t.Fatalf("download did not auto-create the node device (ok=%t, %+v)", ok, d)
	}

	// The session became the persisted current session.
	data, err := os.ReadFile(config.CurrentAUMSessionPath())
	if err != nil {
		t.Fatalf("current-session marker not persisted: %v", err)
	}
	var cur currentAUMSession
	if err := json.Unmarshal(data, &cur); err != nil || cur.ID != "rig_auto" {
		t.Fatalf("current session = %+v (err %v), want id rig_auto", cur, err)
	}

	// A later download of another session replaces the first session's rig.
	authorConventionSession(t, s, "rig_next", "Keys")
	s.OnAUMSessionDownloaded("rig_next.aumproj")
	if _, ok := s.eng.DeviceFor("gitarre"); ok {
		t.Fatal("previous session's device survived the next download import")
	}
	if _, ok := s.eng.DeviceFor("keys"); !ok {
		t.Fatal("next download did not create the new session's device")
	}
	if id := s.currentAUMSessionID(); id != "rig_next" {
		t.Fatalf("current session = %q, want rig_next", id)
	}
}

func TestAUMAutoImportOnBrainConnect(t *testing.T) {
	s := newAutoImportServer(t)
	stageProbe(t)
	authorConventionSession(t, s, "rig_conn", "Gitarre")

	// The hook hands its work to a goroutine (the receiver callback must not
	// block); connect waits until that work is over.
	connect := func() {
		s.OnMidiControlConnected()
		s.waitBackground()
	}

	// Brain connect with no current session is a no-op.
	connect()
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("connect with no current session created %d device(s)", n)
	}

	// Record the current session (as a download would), then "connect": the
	// import re-runs even though no download happens now — the daemon may have
	// restarted in between.
	s.setCurrentAUMSession("rig_conn")
	connect()
	if _, ok := s.eng.DeviceFor("gitarre"); !ok {
		t.Fatal("brain connect did not re-import the current session")
	}

	// A second connect replaces (not duplicates) the session rig.
	connect()
	count := 0
	for _, d := range s.eng.Devices() {
		if d.Session == "rig_conn" {
			count++
		}
	}
	if count != 2 { // session device + gitarre node
		t.Fatalf("after reconnect the session rig has %d device(s), want 2", count)
	}
}

// TestAUMTapConnectNamesTheOpenSession checks the connect of a session's
// master tap makes that session the current one, whatever was downloaded
// last, and brings its rig in: the tap dials in under the session's id, so it
// is the session AUM has open. Every other tap says nothing.
func TestAUMTapConnectNamesTheOpenSession(t *testing.T) {
	s := newAutoImportServer(t)
	stageProbe(t)
	authorConventionSession(t, s, "rig_a", "Gitarre")
	authorConventionSession(t, s, "rig_b", "Keys")
	tap := func(name string) {
		s.OnAudioTapConnected(name)
		s.waitBackground()
	}

	// The iPad downloads one session after the other: the second is current.
	s.OnAUMSessionDownloaded("rig_a.aumproj")
	s.OnAUMSessionDownloaded("rig_b.aumproj")
	if id := s.currentAUMSessionID(); id != "rig_b" {
		t.Fatalf("current session = %q after two downloads, want the second", id)
	}

	// A track's tap, a tap without a name, one named by hand and one of a
	// session that is not staged leave it alone.
	for _, name := range []string{"rig_a/bass", "192.0.2.7:51234", "my tap", "gone/master", "../rig_a/master"} {
		tap(name)
		if id := s.currentAUMSessionID(); id != "rig_b" {
			t.Fatalf("the tap %q made %q the current session", name, id)
		}
	}
	if _, ok := s.eng.DeviceFor("keys"); !ok {
		t.Fatal("the second session's rig is gone although no session's master tap connected")
	}

	// The first session's master tap connects: AUM has that one open.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	tap("rig_a/master")
	if id := s.currentAUMSessionID(); id != "rig_a" {
		t.Fatalf("current session = %q, want the one whose master tap connected", id)
	}
	if d, ok := s.eng.DeviceFor("gitarre"); !ok || d.Session != "rig_a" {
		t.Fatalf("the rig of the open session was not imported (ok=%t, %+v)", ok, d)
	}
	if _, ok := s.eng.DeviceFor("keys"); ok {
		t.Fatal("the other session's device survived")
	}
	if n := strings.Count(buf.String(), `aum auto-import "rig_a"`); n != 1 {
		t.Fatalf("%d import(s) at the tap's connect, want one:\n%s", n, buf.String())
	}

	// The same word again, from the tap or from load_song, imports nothing
	// anew: the rig is that session's already.
	buf.Reset()
	tap("rig_a/master")
	s.sessionOpen("rig_a", "load_song loaded the session")
	if n := strings.Count(buf.String(), "aum auto-import"); n != 0 {
		t.Fatalf("%d import(s) for the session whose rig is in place:\n%s", n, buf.String())
	}
	// A brain that connects since has not seen the surface: it is pushed.
	s.NotifyMidiControl(true, "ipad:1")
	s.sessionOpen("rig_a", "load_song loaded the session")
	if n := strings.Count(buf.String(), `aum auto-import "rig_a"`); n != 1 {
		t.Fatalf("%d import(s) for a brain that connected since, want one:\n%s", n, buf.String())
	}
}

// TestAUMTapConnectThatIsNoLongerNewest checks a tap's connect that waits
// behind an import changes nothing when, by its turn, its tap has dropped or
// another session's master has dialled in since: the session AUM has open is
// the one of the newest connected master, and the older word does not replace
// its marker, rig and surface. currentMu is held while the registry changes,
// so the connect's turn comes after it whatever the scheduler does.
func TestAUMTapConnectThatIsNoLongerNewest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(reg *audiotap.Registry, s *Server)
	}{
		{"its tap dropped", func(reg *audiotap.Registry, _ *Server) {
			reg.Disconnect("rig_a/master")
		}},
		{"a newer master connected", func(reg *audiotap.Registry, s *Server) {
			reg.Connect("rig_b/master", "ipad:1")
			s.OnAudioTapConnected("rig_b/master")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			reg := audiotap.NewRegistry()
			s := New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), WithAUMAutoImport(true), WithAudioTap(reg))
			waitBackgroundAtCleanup(t, s)
			stageProbe(t)
			authorConventionSession(t, s, "rig_a", "Gitarre")
			authorConventionSession(t, s, "rig_b", "Keys")
			s.OnAUMSessionDownloaded("rig_b.aumproj")

			reg.Connect("rig_a/master", "ipad:1")
			s.currentMu.Lock()
			s.OnAudioTapConnected("rig_a/master")
			tc.change(reg, s)
			s.currentMu.Unlock()
			s.waitBackground()

			if id := s.currentAUMSessionID(); id != "rig_b" {
				t.Fatalf("current session = %q, want rig_b: an older tap's word replaced it", id)
			}
			if _, ok := s.eng.DeviceFor("gitarre"); ok {
				t.Fatal("the rig of the session whose tap is no longer the newest was imported")
			}
			if d, ok := s.eng.DeviceFor("keys"); !ok || d.Session != "rig_b" {
				t.Fatalf("the open session's rig is gone (ok=%t, %+v)", ok, d)
			}
		})
	}
}

// TestAUMTapConnectWithoutAutoImport checks the marker follows the open
// session with the automatic import off, and nothing is imported.
func TestAUMTapConnectWithoutAutoImport(t *testing.T) {
	s := newAUMServerWithBrain(t) // auto-import NOT enabled
	stageProbe(t)
	authorConventionSession(t, s, "rig_a", "Gitarre")
	authorConventionSession(t, s, "rig_b", "Keys")
	s.OnAUMSessionDownloaded("rig_b.aumproj")
	s.OnAudioTapConnected("rig_a/master")
	s.waitBackground()
	if id := s.currentAUMSessionID(); id != "rig_a" {
		t.Fatalf("current session = %q, want the one whose master tap connected", id)
	}
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("the tap's connect created %d device(s) with the import off", n)
	}
}

func TestAUMAutoImportDisabledOnlyTracksCurrentSession(t *testing.T) {
	s := newAUMServerWithBrain(t) // auto-import NOT enabled
	stageProbe(t)
	authorConventionSession(t, s, "rig_off", "Gitarre")

	s.OnAUMSessionDownloaded("rig_off.aumproj")
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("disabled auto-import still created %d device(s)", n)
	}
	// Current-session tracking stays on (cheap, and a later manual import or
	// re-enabled flag uses it).
	if id := s.currentAUMSessionID(); id != "rig_off" {
		t.Fatalf("current session = %q, want rig_off", id)
	}

	// OnMidiControlConnected is gated too (synchronously checkable: the flag
	// short-circuits before the goroutine spawns).
	s.OnMidiControlConnected()
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("disabled brain-connect import created %d device(s)", n)
	}
}

func TestAUMAutoImportIgnoresMidimapDownloads(t *testing.T) {
	s := newAutoImportServer(t)

	s.OnAUMSessionDownloaded("vol_map.aum_midimap")
	if id := s.currentAUMSessionID(); id != "" {
		t.Fatalf("midimap download set current session %q", id)
	}
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("midimap download created %d device(s)", n)
	}
}

func TestBuildControlSurfaceFromImport(t *testing.T) {
	s := newAutoImportServer(t)
	stageProbe(t)
	authorConventionSession(t, s, "rig_surf", "Gitarre")

	o, err := s.importSessionRig(filepath.Join(config.AUMSessionsDir(), "rig_surf.aumproj"), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	frame := s.buildControlSurface(o)

	if frame.Type != midicontrol.ControlSurfaceType {
		t.Fatalf("frame type = %q, want %q", frame.Type, midicontrol.ControlSurfaceType)
	}
	if frame.Session != "rig_surf" {
		t.Fatalf("frame session = %q, want rig_surf", frame.Session)
	}
	byName := map[string]midicontrol.SurfaceDevice{}
	for _, d := range frame.Devices {
		byName[d.Name] = d
	}
	if len(byName) != 2 {
		t.Fatalf("frame devices = %v, want aum + gitarre", frame.Devices)
	}

	controls := func(dev string) map[string]midicontrol.SurfaceControl {
		out := map[string]midicontrol.SurfaceControl{}
		for _, c := range byName[dev].Controls {
			out[c.Name] = c
		}
		return out
	}

	// Session device: the strip level is a fader on the mapping's pinned
	// channel (send ch 3), the mute a toggle with both named states.
	sc := controls("aum")
	lvl, ok := sc["gitarre_level"]
	if !ok {
		t.Fatalf("session surface missing gitarre_level: %v", byName["aum"].Controls)
	}
	if lvl.Widget != "fader" || lvl.Msg.Type != "cc" || lvl.Msg.Channel != 3 {
		t.Fatalf("gitarre_level surface control = %+v, want cc fader on ch 3", lvl)
	}
	mute, ok := sc["gitarre_mute"]
	if !ok || mute.Widget != "toggle" || len(mute.Values) != 2 {
		t.Fatalf("gitarre_mute surface control = %+v (ok=%t), want 2-value toggle", mute, ok)
	}
	if mute.Values[0].Value > mute.Values[1].Value {
		t.Fatalf("toggle values not ordered by wire value: %+v", mute.Values)
	}

	// Node device: the probe param mirrors its session mapping (CC 30, ch 3).
	nc := controls("gitarre")
	cut, ok := nc["cutoff"]
	if !ok {
		t.Fatalf("node surface missing cutoff: %v", byName["gitarre"].Controls)
	}
	if cut.Widget != "fader" || cut.Msg.Type != "cc" || cut.Msg.Number != 30 || cut.Msg.Channel != 3 {
		t.Fatalf("cutoff surface control = %+v, want cc 30 fader on ch 3", cut)
	}
}
