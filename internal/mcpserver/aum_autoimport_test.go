package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
)

// newAutoImportServer is newAUMServerWithBrain with the auto-import flag on,
// standing in for a daemon running with aum_auto_import: true.
func newAutoImportServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}), WithAUMAutoImport(true))
}

// authorConventionSession stages a 2-strip session (Gitarre hosting the probe
// + Master) wired to the convention on send channel 3.
func authorConventionSession(t *testing.T, s *Server, outID, strip string) {
	t.Helper()
	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": outID,
		"title":  outID,
		"channels": []any{
			map[string]any{"kind": "audio", "title": strip, "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
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

	// Brain connect with no current session is a no-op.
	s.reimportCurrentAUMSession()
	if n := len(s.eng.Devices()); n != 0 {
		t.Fatalf("connect with no current session created %d device(s)", n)
	}

	// Record the current session (as a download would), then "connect": the
	// import re-runs even though no download happens now — the daemon may have
	// restarted in between.
	s.setCurrentAUMSession("rig_conn")
	s.reimportCurrentAUMSession()
	if _, ok := s.eng.DeviceFor("gitarre"); !ok {
		t.Fatal("brain connect did not re-import the current session")
	}

	// A second connect replaces (not duplicates) the session rig.
	s.reimportCurrentAUMSession()
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
