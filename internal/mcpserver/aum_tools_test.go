package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// fakeAUv3 is a no-op transport claiming the "auv3midi" id, so the AUM import
// auto-create path (which binds devices over auv3midi) finds a registered
// transport. Binding only validates the transport is present; it never sends.
type fakeAUv3 struct{}

func (fakeAUv3) ID() string                                             { return "auv3midi" }
func (fakeAUv3) Discover(context.Context) ([]transport.Endpoint, error) { return nil, nil }
func (fakeAUv3) Pair(context.Context, string) error                     { return nil }
func (fakeAUv3) Connect(context.Context, string) error                  { return nil }
func (fakeAUv3) Disconnect(context.Context, string) error               { return nil }
func (fakeAUv3) Send(context.Context, string, transport.Event) error    { return nil }
func (fakeAUv3) Listen(context.Context, string) (<-chan transport.Event, error) {
	ch := make(chan transport.Event)
	return ch, nil
}

// newAUMServerWithBrain is newAUMServer plus a registered auv3midi transport, so
// import_aum_session can auto-create the AUM session rig (mixer + nodes).
func newAUMServerWithBrain(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return New(engine.New(device.NewRegistry(), fakeBLE{}, fakeAUv3{}))
}

// stageProbe writes a minimal AUv3 probe dump into the staging dir so the
// author/import tools can source/match a node from it.
func stageProbe(t *testing.T) {
	t.Helper()
	dir := config.AUv3ProbesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir probes: %v", err)
	}
	dump := `{
		"component": {"type": "aumu", "subtype": "gtr1", "manufacturer": "Acme", "manufacturerName": "Acme"},
		"name": "GuitarSynth",
		"parameters": [
			{"address": 0, "identifier": "cutoff", "displayName": "Cutoff", "min": 0, "max": 1, "value": 0.5, "writable": true, "readable": true}
		]
	}`
	if err := os.WriteFile(filepath.Join(dir, "gtr1.json"), []byte(dump), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
}

func newAUMServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return New(engine.New(device.NewRegistry(), fakeBLE{}))
}

func TestAUMToolsAuthorListGetDiff(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author a 2-channel session (Gitarre hosting the probe + a Master),
	// pre-wired to the convention.
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Band Test",
		"channels": []any{
			map[string]any{
				"kind": "audio", "title": "Gitarre",
				"nodes": []any{map[string]any{"probe_id": "gtr1"}},
			},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 1, "start_cc": 30},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "CC(s) assigned") {
		t.Fatalf("author output missing assignment summary:\n%s", resultText(res))
	}

	staged := filepath.Join(config.AUMSessionsDir(), "band_test.aumproj")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("authored session not staged: %v", err)
	}

	// list shows it.
	res = call(t, s.handleListAUMSessions, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), "band_test") {
		t.Fatalf("list missing band_test:\n%s", resultText(res))
	}

	// get returns the SessionMap (2 channels, the hosted node).
	res = call(t, s.handleGetAUMSession, map[string]any{"session_id": "band_test"})
	if res.IsError {
		t.Fatalf("get failed: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "Gitarre") || !strings.Contains(text, "aumu/gtr1/Acme") {
		t.Fatalf("get output missing channel/node:\n%s", text)
	}

	// diff: convention pre-wired, so the single non-master audio strip should be
	// fully wired (Volume/Mute/Solo/Rec at their convention CCs).
	res = call(t, s.handleDiffAUMSession, map[string]any{"session_id": "band_test"})
	if res.IsError {
		t.Fatalf("diff failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "fully wired to the convention") {
		t.Fatalf("diff verdict not fully wired:\n%s", resultText(res))
	}
}

func TestAUMToolsNestedSessionPaths(t *testing.T) {
	s := newAUMServer(t)

	// Stage a session in a subfolder, mirroring the iPad's AUM tree (as the
	// receiver does for ?path= uploads).
	data, err := aum.Template().Encode()
	if err != nil {
		t.Fatalf("encode template: %v", err)
	}
	sub := filepath.Join(config.AUMSessionsDir(), "Live sets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Demo.aumproj"), data, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// list walks subfolders and reports the path-style id.
	res := call(t, s.handleListAUMSessions, map[string]any{})
	if res.IsError || !strings.Contains(resultText(res), "Live sets/Demo") {
		t.Fatalf("list missing nested session:\n%s", resultText(res))
	}

	// get resolves a session_id carrying subfolder segments.
	res = call(t, s.handleGetAUMSession, map[string]any{"session_id": "Live sets/Demo"})
	if res.IsError {
		t.Fatalf("get nested failed: %s", resultText(res))
	}

	// edit without out_id stages back to the SAME nested path, so the iPad's
	// write-back lands in the session's original AUM subfolder.
	res = call(t, s.handleEditAUMSession, map[string]any{
		"session_id": "Live sets/Demo",
		"faders":     []any{map[string]any{"channel": 0, "level": 0.5}},
	})
	if res.IsError {
		t.Fatalf("edit nested failed: %s", resultText(res))
	}
	edited := filepath.Join(config.AUMSessionsDir(), "Live sets", "Demo.aumproj")
	if !strings.Contains(resultText(res), edited) {
		t.Fatalf("edit did not stage back to the nested path %s:\n%s", edited, resultText(res))
	}
	// The root must hold only the "Live sets" folder (plus the hidden .rev
	// staging counter every write maintains) — no stray session files.
	entries, err := os.ReadDir(config.AUMSessionsDir())
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "Live sets" && e.Name() != ".rev" {
			t.Fatalf("staging root polluted by edit (unexpected entry %q)", e.Name())
		}
	}

	// The import outcome's session id matches the staging-relative id the
	// download hook records, subfolders included.
	if got := stagedRelID(edited); got != "Live sets/Demo" {
		t.Fatalf("stagedRelID = %q, want Live sets/Demo", got)
	}

	// Traversal-style ids stay rejected.
	for _, id := range []string{"../escape", "Live sets/../../escape", ".hidden/x"} {
		res = call(t, s.handleGetAUMSession, map[string]any{"session_id": id})
		if !res.IsError || !strings.Contains(resultText(res), "invalid session_id") {
			t.Fatalf("session_id %q: want invalid-session_id error, got: %s", id, resultText(res))
		}
	}
}

func TestSessionCandidatesWalkNestedSessions(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author a session hosting the probe node, then move it into a subfolder
	// (as a ?path= upload would stage it).
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"title": "Nested Disco",
		"channels": []any{
			map[string]any{
				"kind": "audio", "title": "Gitarre",
				"nodes": []any{map[string]any{"probe_id": "gtr1"}},
			},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}
	dir := config.AUMSessionsDir()
	sub := filepath.Join(dir, "Live sets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "nested_disco.aumproj"), filepath.Join(sub, "nested_disco.aumproj")); err != nil {
		t.Fatalf("move: %v", err)
	}

	cands, note := s.sessionCandidates(loadStagedProbeDumps())
	if note != "" {
		t.Fatalf("sessionCandidates note: %s", note)
	}
	found := false
	for _, c := range cands {
		if c.SessionID == "Live sets/nested_disco" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no candidate carries the nested session id, got: %+v", cands)
	}
}

func TestAUMAuthorRoutedTappedSession(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author a routed + tapped session through the general tool path: an
	// instrument strip (probe -> BusDest(0)) with a post-fader tap, and a
	// master strip (BusSource(0) -> HWOutput) with master FX and a tap, plus a
	// named sub-bus. Exercises Source/Output/PostNodes/AuxSends/Tap/mix_busses.
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id":   "routed",
		"hardware": "x32",
		"channels": []any{
			map[string]any{
				"kind": "audio", "title": "Synth",
				"nodes":     []any{map[string]any{"probe_id": "gtr1"}},
				"output":    map[string]any{"kind": "bus", "bus_index": 0},
				"aux_sends": []any{map[string]any{"bus_index": 4, "amount": 0.5}},
				"tap":       true,
			},
			map[string]any{
				"kind": "audio", "title": "Master",
				"source": map[string]any{"kind": "bus", "bus_index": 0},
				"output": map[string]any{"kind": "hardware", "hw_bus_index": 0},
				"tap":    true,
			},
		},
		"mix_busses": []any{map[string]any{"index": 4, "name": "Drums Mix"}},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	if _, err := os.Stat(filepath.Join(config.AUMSessionsDir(), "routed.aumproj")); err != nil {
		t.Fatalf("routed session not staged: %v", err)
	}

	// The session re-opens and both channels carry a post-fader ProbeAudioTap.
	res = call(t, s.handleGetAUMSession, map[string]any{"session_id": "routed"})
	if res.IsError {
		t.Fatalf("get failed: %s", resultText(res))
	}
	text := resultText(res)
	if strings.Count(text, "Tmow: ProbeAudioTap [aufx/pbAu/Tmow]") != 2 {
		t.Fatalf("expected a tap in each of the 2 channels:\n%s", text)
	}
	if !strings.Contains(text, "BusDestDescription") || !strings.Contains(text, "HWOutputDescription") || !strings.Contains(text, "BusSourceDescription") {
		t.Fatalf("get output missing routing nodes:\n%s", text)
	}
	// The aux send placed a BusSendDescription on the instrument channel.
	if !strings.Contains(text, "BusSendDescription") {
		t.Fatalf("get output missing the aux send node:\n%s", text)
	}
}

func TestAUMAuthorRejectsBadSourceKind(t *testing.T) {
	s := newAUMServer(t)

	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Ch", "source": map[string]any{"kind": "bogus"}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if !res.IsError {
		t.Fatal("expected an error for an unknown source kind")
	}
	if !strings.Contains(resultText(res), "source/kind") {
		t.Fatalf("error did not name the offending field:\n%s", resultText(res))
	}
}

func TestAUMDiffUnwiredSession(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	// Author WITHOUT a convention (bare): every leaf stays a placeholder.
	res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "blank",
		"bare":   true,
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Ch1"},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	})
	if res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}
	res = call(t, s.handleDiffAUMSession, map[string]any{"session_id": "blank"})
	if res.IsError {
		t.Fatalf("diff failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "not wired to the convention yet") {
		t.Fatalf("expected unwired verdict:\n%s", resultText(res))
	}
}

func TestAUMEditAndExport(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "edit_me",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Ch1"},
			map[string]any{"kind": "audio", "title": "Master"},
		},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	// Edit: assign Volume to CC 7 on the first channel's controls, set a fader.
	res := call(t, s.handleEditAUMSession, map[string]any{
		"session_id": "edit_me",
		"out_id":     "edited",
		"mappings": []any{
			map[string]any{"collection": "Channels/chan0/Channel controls", "target": "Volume", "type": 0, "data1": 7, "channel": 1},
		},
		"faders": []any{map[string]any{"channel": 0, "level": 0.5}},
	})
	if res.IsError {
		t.Fatalf("edit failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUMSessionsDir(), "edited.aumproj")); err != nil {
		t.Fatalf("edited session not staged: %v", err)
	}

	// Export the collection we just assigned into.
	res = call(t, s.handleExportAUMMidiMap, map[string]any{
		"session_id": "edited",
		"collection": "Channels/chan0/Channel controls",
		"out_id":     "vol_map",
	})
	if res.IsError {
		t.Fatalf("export failed: %s", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(config.AUMSessionsDir(), "vol_map.aum_midimap")); err != nil {
		t.Fatalf("exported midimap not staged: %v", err)
	}
}

func TestAUMImportProposesBindings(t *testing.T) {
	s := newAUMServer(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "rig",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "rig"})
	if res.IsError {
		t.Fatalf("import failed: %s", resultText(res))
	}
	text := resultText(res)
	// The hosted node matched the staged probe and proposed a binding whose
	// device id derives from the probe; the channel was inferred from the
	// convention (authored send ch 3 → stored 0-based 2 → suggested send ch 3).
	if !strings.Contains(text, "probe=gtr1") {
		t.Fatalf("import did not match the probe:\n%s", text)
	}
	if !strings.Contains(text, "channel=3") {
		t.Fatalf("import did not infer the channel:\n%s", text)
	}
}

func TestAUMImportAutoCreatesDevices(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)

	// Author a session: Gitarre (hosting the probe) + Master, convention ch 3.
	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "rig2",
		"title":  "Rig Two",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "rig2"})
	if res.IsError {
		t.Fatalf("import failed: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "auto-created") {
		t.Fatalf("import did not auto-create:\n%s", text)
	}

	// The session device was created (bound on wire ch 2 = send 3) and tagged
	// with the session id for the replace-on-reimport lifecycle.
	sd, ok := s.eng.DeviceFor("aum")
	if !ok {
		t.Fatalf("session device not created:\n%s", text)
	}
	if sd.DeviceID != "aum_rig2" {
		t.Fatalf("session device type = %q, want aum_rig2", sd.DeviceID)
	}
	if sd.ControlChannel() != 2 {
		t.Fatalf("session device wire channel = %d, want 2", sd.ControlChannel())
	}
	if sd.ControlEndpoint() != "brain" {
		t.Fatalf("session device endpoint = %q, want brain", sd.ControlEndpoint())
	}
	if sd.Session != "rig2" {
		t.Fatalf("session device tag = %q, want rig2", sd.Session)
	}
	if _, ok := s.eng.Registry().Get("aum_rig2"); !ok {
		t.Fatal("session device type not registered")
	}

	// The hosted node became a session-scoped device on its mapped channel
	// (authored send ch 3 → wire 2).
	node, ok := s.eng.DeviceFor("gitarre")
	if !ok {
		t.Fatalf("node device not created:\n%s", text)
	}
	if node.DeviceID != "rig2_gitarre" || node.ControlChannel() != 2 {
		t.Fatalf("node device = %q ch %d, want rig2_gitarre ch 2", node.DeviceID, node.ControlChannel())
	}
	if node.Session != "rig2" {
		t.Fatalf("node device tag = %q, want rig2", node.Session)
	}
	nt, ok := s.eng.Registry().Get("rig2_gitarre")
	if !ok {
		t.Fatal("node device type (rig2_gitarre) not registered")
	}

	// Both generated device types were staged to disk, and the rig persisted.
	for _, id := range []string{"aum_rig2", "rig2_gitarre"} {
		if _, err := os.Stat(filepath.Join(config.DeviceTypesDir(), id+".yaml")); err != nil {
			t.Fatalf("device type %q not staged: %v", id, err)
		}
	}
	if _, err := os.Stat(config.DevicesPath()); err != nil {
		t.Fatalf("devices.yaml not persisted: %v", err)
	}

	// The session device type carries strip controls named from the strip
	// title (Gitarre) plus the global transport block, each pinning the
	// mapping's stored channel (send ch 3).
	st, _ := s.eng.Registry().Get("aum_rig2")
	lvl, ok := st.Control("gitarre_level")
	if !ok {
		t.Fatal("session type missing gitarre_level control")
	}
	if lvl.Channel == nil || *lvl.Channel != 3 {
		t.Fatalf("gitarre_level pinned channel = %v, want 3", lvl.Channel)
	}
	if _, ok := st.Control("transport"); !ok {
		t.Fatal("session type missing transport control")
	}

	// The node type's control mirrors the session mapping exactly: the
	// convention assigned cutoff CC 30 on send ch 3.
	cut, ok := nt.Control("cutoff")
	if !ok {
		t.Fatalf("node type missing cutoff control: %v", nt.ControlNames())
	}
	if cut.CC == nil || *cut.CC != 30 {
		t.Fatalf("cutoff CC = %v, want 30", cut.CC)
	}
	if cut.Channel == nil || *cut.Channel != 3 {
		t.Fatalf("cutoff pinned channel = %v, want 3", cut.Channel)
	}
}

func TestAUMImportReplacesPreviousSessionRig(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)

	author := func(outID, strip string) {
		t.Helper()
		if res := call(t, s.handleAuthorAUMSession, map[string]any{
			"out_id": outID,
			"channels": []any{
				map[string]any{"kind": "audio", "title": strip, "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
				map[string]any{"kind": "audio", "title": "Master"},
			},
			"convention": map[string]any{"channel": 1, "start_cc": 30},
		}); res.IsError {
			t.Fatalf("author %s failed: %s", outID, resultText(res))
		}
	}
	author("first", "Gitarre")
	author("second", "Keys")

	if res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "first"}); res.IsError {
		t.Fatalf("first import failed: %s", resultText(res))
	}
	if _, ok := s.eng.DeviceFor("gitarre"); !ok {
		t.Fatal("first import did not create gitarre")
	}

	// Importing the next session replaces the previous session's devices
	// (the daemon models the one session AUM has loaded).
	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "second"})
	if res.IsError {
		t.Fatalf("second import failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "replaced") {
		t.Fatalf("second import did not report replacement:\n%s", resultText(res))
	}
	if _, ok := s.eng.DeviceFor("gitarre"); ok {
		t.Fatal("previous session's gitarre device was not removed")
	}
	if _, ok := s.eng.DeviceFor("keys"); !ok {
		t.Fatal("second import did not create keys")
	}
	for _, d := range s.eng.Devices() {
		if d.Session != "second" {
			t.Fatalf("device %q carries session tag %q, want second", d.Name, d.Session)
		}
	}

	// The first session's generated types were retired with its devices —
	// from the registry and from the staged device-types dir — while the
	// second session's are present.
	for _, id := range []string{"aum_first", "first_gitarre"} {
		if _, ok := s.eng.Registry().Get(id); ok {
			t.Fatalf("retired type %q still registered", id)
		}
		if _, err := os.Stat(filepath.Join(config.DeviceTypesDir(), id+".yaml")); !os.IsNotExist(err) {
			t.Fatalf("retired type %q still staged (err %v)", id, err)
		}
	}
	for _, id := range []string{"aum_second", "second_keys"} {
		if _, ok := s.eng.Registry().Get(id); !ok {
			t.Fatalf("current type %q not registered", id)
		}
		if _, err := os.Stat(filepath.Join(config.DeviceTypesDir(), id+".yaml")); err != nil {
			t.Fatalf("current type %q not staged: %v", id, err)
		}
	}
}

func TestAUMImportOfUnmappedSessionKeepsPreviousRig(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "mapped",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 1, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author mapped failed: %s", resultText(res))
	}
	if res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "mapped"}); res.IsError {
		t.Fatalf("import mapped failed: %s", resultText(res))
	}
	if _, ok := s.eng.DeviceFor("gitarre"); !ok {
		t.Fatal("mapped import did not create gitarre")
	}
	if id := s.currentAUMSessionID(); id != "mapped" {
		t.Fatalf("current session = %q, want mapped", id)
	}

	// A bare (unmapped) session derives zero controls: importing it must NOT
	// wipe the working rig, and the current-session marker must not move.
	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "bare",
		"bare":   true,
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Empty"},
		},
	}); res.IsError {
		t.Fatalf("author bare failed: %s", resultText(res))
	}
	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "bare"})
	if res.IsError {
		t.Fatalf("import bare failed: %s", resultText(res))
	}
	if strings.Contains(resultText(res), "replaced 2 device(s)") {
		t.Fatalf("unmapped import replaced the previous rig:\n%s", resultText(res))
	}
	if _, ok := s.eng.DeviceFor("gitarre"); !ok {
		t.Fatal("unmapped import wiped the previous session's rig")
	}
	if id := s.currentAUMSessionID(); id != "mapped" {
		t.Fatalf("current session moved to %q on a no-op import, want mapped", id)
	}
}

func TestAUMImportProposeOnlyDoesNotCreate(t *testing.T) {
	s := newAUMServerWithBrain(t)
	stageProbe(t)

	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "rig3",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	// propose_only must not create anything even when auv3midi is available.
	res := call(t, s.handleImportAUMSession, map[string]any{"session_id": "rig3", "propose_only": true})
	if res.IsError {
		t.Fatalf("import failed: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "probe=gtr1") || !strings.Contains(text, "channel=3") {
		t.Fatalf("propose_only output missing proposal:\n%s", text)
	}
	if len(s.eng.Devices()) != 0 {
		t.Fatalf("propose_only created %d device(s), want 0", len(s.eng.Devices()))
	}
}

func TestAUMToolsValidate(t *testing.T) {
	s := newAUMServer(t)

	if res := call(t, s.handleGetAUMSession, map[string]any{}); !res.IsError {
		t.Fatal("expected error when neither file nor session_id given")
	}
	if res := call(t, s.handleGetAUMSession, map[string]any{"session_id": "../etc/passwd"}); !res.IsError {
		t.Fatal("expected traversal guard to reject a path-y session_id")
	}
	if res := call(t, s.handleAuthorAUMSession, map[string]any{"channels": []any{}}); !res.IsError {
		t.Fatal("expected error authoring with no channels")
	}
	if res := call(t, s.handleEditAUMSession, map[string]any{"session_id": "x"}); !res.IsError {
		t.Fatal("expected error editing with no edits")
	}
}
