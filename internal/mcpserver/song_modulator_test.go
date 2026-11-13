package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

// stageLFOH stages K-Devices LFOH as its probe dumps it: every parameter 0..1.
func stageLFOH(t *testing.T) {
	t.Helper()
	var params []string
	for i, id := range []string{"phase", "wave", "time_mode", "time_num", "time_den", "midi_max", "midi_min", "midi_ch", "midi_cc"} {
		params = append(params, fmt.Sprintf(`{"address": %d, "identifier": %q, "displayName": %q, "min": 0, "max": 1, "value": 0, "writable": true}`, i, id, id))
	}
	dump := `{"component": {"type": "aumf", "subtype": "KLFH", "manufacturer": "KDev", "manufacturerName": "K-Devices"}, "name": "LFOH",
		"parameters": [` + strings.Join(params, ", ") + `],
		"factoryPresets": [{"number": 0, "name": "init"}, {"number": 10, "name": "gruv"}]}`
	if err := os.WriteFile(filepath.Join(config.AUv3ProbesDir(), "aumf-_k_l_f_h-_k_dev.json"), []byte(dump), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
}

// modSong is a pad whose filter one LFOH sweeps and whose delay another one
// moves; the bridge bypasses the sweep, and the filter rests at 0.3 there.
const modToolSong = `title: Sweep
tempo: 300
tracks:
  - id: bass
    instrument: {plugin: syn1, preset: Deep Bass}
  - id: pad
    instrument: {plugin: syn1, preset: Warm Pad, params: {Cutoff: 0.3}}
    effects:
      - {id: echo, plugin: dly1}
    modulators:
      - {id: sweep, plugin: LFOH, preset: gruv, target: {param: Cutoff}, depth: [0.25, 0.75]}
      - {id: drift, plugin: LFOH, bypass: true, target: {effect: echo, param: Mix}}
patterns:
  bass: {step: 1/8, notes: "A1 . A1 . A1 . G1 ."}
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
scenes:
  bridge:
    pad: {modulators: {sweep: {bypass: true}, drift: {bypass: false}}}
sections:
  verse: {bars: 1, play: {bass: bass, pad: chords}}
  bridge: {bars: 1, scene: bridge, play: {pad: chords}}
arrangement: [verse, bridge]
`

func TestSongModulators(t *testing.T) {
	s := newSongServer(t)
	stageLFOH(t)

	// The drift is bypassed where it does not run, and the echo's Mix needs a
	// value to rest at there.
	res := call(t, s.handleSaveSong, map[string]any{"yaml": modToolSong})
	if !res.IsError || !strings.Contains(resultText(res), "/tracks/1/modulators/1/target: pad's modulator drift moves pad/echo Mix, and it declares itself bypassed; declare Mix on its plugin") {
		t.Fatalf("save without the echo's Mix: %s", resultText(res))
	}
	doc := strings.Replace(modToolSong, "{id: echo, plugin: dly1}", "{id: echo, plugin: dly1, params: {Mix: 0.2}}", 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": doc})
	if res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	text := resultText(res)
	// Two tracks on channels 1 and 2; the pool fills channel 14 from CC 1:
	// the bypasses of five nodes, then the parameters in plan order.
	for _, want := range []string{
		"ch2  pad        PolySynth > TapeDelay  (",
		"~ sweep (LFOH) moves pad Cutoff, sending CC ",
		"between 32 and 95",
		"~ drift (LFOH) moves pad/echo Mix, sending CC ",
		"between 0 and 127",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the summary lacks %q:\n%s", want, text)
		}
	}

	// A plugin that sends no control change is no modulator.
	bad := strings.Replace(doc, "{id: sweep, plugin: LFOH,", "{id: sweep, plugin: dly1,", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": bad, "dry_run": true}); !res.IsError || !strings.Contains(resultText(res), "TapeDelay is not a modulator a song can host") {
		t.Errorf("a delay as a modulator: %s", resultText(res))
	}
	// A target the track does not have.
	bad = strings.Replace(doc, "target: {param: Cutoff}", "target: {param: Drive}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": bad, "dry_run": true}); !res.IsError || !strings.Contains(resultText(res), `/tracks/1/modulators/0/target/param: PolySynth has no writable parameter "Drive"`) {
		t.Errorf("a target that does not exist: %s", resultText(res))
	}

	res = call(t, s.handleAuthorSongSession, map[string]any{"song": "sweep"})
	if res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	text = resultText(res)
	for _, want := range []string{
		"2 modulator(s) routed to MIDI Control",
		"strip 3: pad on MIDI channel 2: PolySynth > TapeDelay > LFOH > LFOH",
		"sweep (LFOH) sends CC ",
		"from its MIDI Out to MIDI Control, which moves pad Cutoff",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the author report lacks %q:\n%s", want, text)
		}
	}

	// The session as the iPad gets it.
	sess, err := aum.OpenFile(filepath.Join(config.AUMSessionsDir(), "Songs", "Sweep.aumproj"))
	if err != nil {
		t.Fatalf("open the staged session: %v", err)
	}
	sg, err := song.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.compileSong("sweep", sg)
	if err != nil {
		t.Fatal(err)
	}
	pad := cs.plan.Track("pad")
	sweep, drift := cs.plan.Node("pad", "sweep"), cs.plan.Node("pad", "drift")
	// The nodes: LFOH twice, after the delay on the pad's strip.
	chans := sess.Channels()
	slots := map[int]string{}
	for _, n := range chans[pad.Strip].Nodes {
		if n.Component != nil {
			slots[n.Slot] = n.Component.Type + "/" + n.Component.Subtype
		}
	}
	if slots[sweep.Slot] != "aumf/KLFH" || slots[drift.Slot] != "aumf/KLFH" || sweep.Slot != 3 || drift.Slot != 4 {
		t.Errorf("the pad's strip holds %v; want LFOH at slots 3 and 4", slots)
	}
	// Their state: neither opens bypassed in AUM. The drift declares itself
	// bypassed, which the song's state does by parking it.
	for _, n := range []int{sweep.Slot, drift.Slot} {
		if got := sessionNodeState(t, sess, pad.Strip, n, "AUMNode.bypassed"); got == true {
			t.Errorf("the LFOH at slot %d opens bypassed in AUM", n)
		}
	}
	// The routes: each LFOH's MIDI Out to MIDI Control alone, which passes the
	// control channels alone.
	conns, filters := sessionMatrix(t, sess)
	for _, n := range []int{sweep.Slot, drift.Slot} {
		src := fmt.Sprintf("Node:Chan%d:Slot%d:MIDI Out", pad.Strip, n)
		if got := conns[src]; len(got) != 1 || got[0] != "BuiltIn:MIDI Control" {
			t.Errorf("%s is routed to %v", src, got)
		}
	}
	var control int64
	for _, ch := range cs.plan.ControlChannels {
		control |= 1 << (ch - 1)
	}
	if filters["BuiltIn:MIDI Control"] != control || control&1 != 0 {
		t.Errorf("MIDI Control passes %016b, want %016b, never channel 1", filters["BuiltIn:MIDI Control"], control)
	}
	// The target's mapping is the controller and channel the sweep sends.
	coll := fmt.Sprintf("Channels/chan%d/slot0", pad.Strip)
	m, ok := sess.FindMapping(coll, "cutoff")
	if !ok || m.Spec.Data1 != sweep.Mod.At.Number || m.Spec.Channel != sweep.Mod.At.Channel-1 || m.Spec.Type != aum.SpecStateTypeCC {
		t.Errorf("Cutoff is mapped as %+v (found %v), want CC %d on channel %d", m.Spec, ok, sweep.Mod.At.Number, sweep.Mod.At.Channel)
	}
	// And the sweep's routing is mapped, to be set by the song's state.
	for _, key := range []string{"midi_ch", "midi_cc", "midi_min", "midi_max"} {
		if m, ok := sess.FindMapping(fmt.Sprintf("Channels/chan%d/slot%d", pad.Strip, sweep.Slot), key); !ok || m.Spec.Channel+1 < cs.plan.ControlChannels[0] {
			t.Errorf("the sweep's %s is mapped as %+v (found %v)", key, m, ok)
		}
	}
}

// sessionNodeState reads one key of a node's archiveNodeState in a session.
func sessionNodeState(t *testing.T, sess *aum.Session, strip, slot int, key string) any {
	t.Helper()
	a := sess.Archive()
	field := func(v any, key string) any {
		m, _ := a.Deref(v).(map[string]any)
		if keys, ok := m["NS.keys"].([]any); ok {
			objs, _ := m["NS.objects"].([]any)
			for i, k := range keys {
				if name, _ := a.Deref(k).(string); name == key && i < len(objs) {
					return objs[i]
				}
			}
			return nil
		}
		return m[key]
	}
	item := func(v any, i int) any {
		m, _ := a.Deref(v).(map[string]any)
		objs, _ := m["NS.objects"].([]any)
		if i >= len(objs) {
			return nil
		}
		return objs[i]
	}
	root, _ := a.Root().(map[string]any)
	return a.Deref(field(field(item(item(root["nodeArchives"], strip), slot), "archiveNodeState"), key))
}

// sessionMatrix reads a session's MIDI matrix: every source's destinations,
// and every destination's channel mask.
func sessionMatrix(t *testing.T, sess *aum.Session) (map[string][]string, map[string]int64) {
	t.Helper()
	a := sess.Archive()
	dict := func(v any) map[string]any {
		m, _ := a.Deref(v).(map[string]any)
		return m
	}
	entries := func(m map[string]any) map[string]any {
		keys, _ := m["NS.keys"].([]any)
		objs, _ := m["NS.objects"].([]any)
		out := map[string]any{}
		for i, k := range keys {
			name, _ := a.Deref(k).(string)
			if i < len(objs) {
				out[name] = objs[i]
			}
		}
		return out
	}
	root, _ := a.Root().(map[string]any)
	matrix := entries(dict(root["midiMatrixState"]))
	if matrix == nil {
		t.Fatal("the session has no MIDI matrix")
	}
	conns := map[string][]string{}
	for src, v := range entries(dict(matrix["connections"])) {
		objs, _ := dict(v)["NS.objects"].([]any)
		for _, d := range objs {
			name, _ := a.Deref(d).(string)
			conns[src] = append(conns[src], name)
		}
	}
	filters := map[string]int64{}
	for dst, v := range entries(dict(matrix["filters"])) {
		switch mask := a.Deref(entries(dict(v))["channelFilter"]).(type) {
		case int64:
			filters[dst] = mask
		case uint64:
			filters[dst] = int64(mask)
		case int:
			filters[dst] = int64(mask)
		}
	}
	return conns, filters
}
