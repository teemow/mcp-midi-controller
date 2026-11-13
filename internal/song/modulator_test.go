package song

import (
	"fmt"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// lfohDump is K-Devices LFOH as its probe stages it: every parameter 0..1.
func lfohDump() device.ProbeDump {
	param := func(id, name string, labels ...string) device.ProbeParam {
		return device.ProbeParam{Identifier: id, DisplayName: name, KeyPath: id, Max: 1, Writable: true, ValueStrings: labels}
	}
	return device.ProbeDump{
		Component: device.ProbeComponent{Type: "aumf", Subtype: "KLFH", Manufacturer: "KDev"},
		Name:      "LFOH",
		Parameters: []device.ProbeParam{
			param("phase", "Phase"), param("wave", "Wave"), param("smooth", "Smooth"),
			param("time_mode", "Time Mode", "free", "sync"), param("frequency", "Frequency"),
			param("time_num", "Time Numerator"), param("time_den", "Time Denominator"),
			param("midi_max", "MIDI Max"), param("midi_min", "MIDI Min"),
			param("midi_ch", "MIDI Ch"), param("midi_cc", "MIDI CC"),
		},
		FactoryPresets: []device.ProbePreset{{Number: 0, Name: "init"}, {Number: 1, Name: "gruv"}, {Number: 2, Name: "wonkie"}},
	}
}

// modCatalog is the test rig with LFOH, and a MIDI plugin that is no
// modulator.
func modCatalog() Catalog {
	cat := testCatalog()
	cat["lfoh"] = lfohDump()
	cat["arp"] = device.ProbeDump{
		Component:  device.ProbeComponent{Type: "aumf", Subtype: "Arp1", Manufacturer: "Test"},
		Name:       "Test Arp",
		Parameters: []device.ProbeParam{{Identifier: "rate", DisplayName: "Rate", Max: 1, Writable: true}},
	}
	return cat
}

// modSong has a pad whose filter one LFOH sweeps and whose reverb another
// one moves, and a bass with a third: three modulators on two strips. The
// bridge bypasses the sweep, the drop changes its preset, depth and rate.
const modSong = `
title: Mod Song
tempo: 120
tracks:
  - id: bass
    instrument: {plugin: synth, preset: Deep Bass}
    modulators:
      - {id: wob, plugin: lfoh, target: {param: Resonance}}
  - id: pad
    instrument: {plugin: synth, preset: Warm Pad, params: {Cutoff: 0.5}}
    effects:
      - {id: space, plugin: reverb}
    modulators:
      - {id: sweep, plugin: lfoh, preset: gruv, params: {Time Numerator: 0.2}, target: {param: Cutoff}, depth: [0.25, 0.75]}
      - {id: wash, plugin: LFOH, target: {effect: space, param: Mix}, depth: ["10%", "60%"]}
patterns:
  bass: {step: 1/4, notes: "A1 . A1 ."}
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
scenes:
  bridge:
    pad:
      params: {Cutoff: 0.6}
      modulators: {sweep: {bypass: true}}
  drop:
    pad:
      modulators: {sweep: {preset: wonkie, depth: [0.1, 0.9], params: {Time Numerator: 0.4}}}
sections:
  verse: {bars: 1, play: {bass: bass, pad: chords}}
  bridge:
    bars: 1
    scene: bridge
    play: {pad: chords}
    automation:
      - {track: pad, param: Cutoff, from: 0.6, to: 0.2}
      - {track: pad, modulator: wash, param: Frequency, from: 0.1, to: 0.5}
  drop: {bars: 1, scene: drop, play: {bass: bass, pad: chords}}
arrangement: [verse, bridge, drop, verse]
`

// modPlan plans modSong, and a song made from it by edit, against the
// modulator catalogue.
func modPlan(t *testing.T, edit func(string) string) (*Plan, error) {
	t.Helper()
	doc := modSong
	if edit != nil {
		doc = edit(doc)
	}
	s, err := Parse([]byte(doc))
	if err != nil {
		return nil, err
	}
	return NewPlan(s, modCatalog())
}

// lfohParams adds the depth's params to the wash in modSong, which declares
// none, so that a ramp of its Frequency has a home value.
func withWashHome(doc string) string {
	return strings.Replace(doc, `{id: wash, plugin: LFOH,`, `{id: wash, plugin: LFOH, params: {Frequency: 0.1},`, 1)
}

func TestModulatorPlan(t *testing.T) {
	p, err := modPlan(t, withWashHome)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	pad := p.Track("pad")
	if len(pad.Nodes) != 4 {
		t.Fatalf("pad's strip has %d nodes, want synth, reverb and two modulators", len(pad.Nodes))
	}
	sweep, wash := p.Node("pad", "sweep"), p.Node("pad", "wash")
	// The modulators follow the effects on the strip and hear no notes.
	if sweep.Strip != pad.Strip || sweep.Slot != firstInsertSlot+1 || wash.Slot != firstInsertSlot+2 {
		t.Errorf("sweep at %d/%d and wash at slot %d, want the strip's slots after the reverb (%d)", sweep.Strip, sweep.Slot, wash.Slot, firstInsertSlot)
	}
	if sweep.MIDI || sweep.Mod == nil || sweep.Mod.Port() != "MIDI Out" {
		t.Errorf("sweep: midi %v, modulator %+v", sweep.MIDI, sweep.Mod)
	}

	// The target is mapped, and the modulator knows its address.
	var cutoff *ParamPlan
	for i := range p.Node("pad", "").Params {
		if pp := &p.Node("pad", "").Params[i]; pp.Key == "cutoff" {
			cutoff = pp
		}
	}
	if cutoff == nil {
		t.Fatal("pad's Cutoff is not mapped")
	}
	if sweep.Mod.At != cutoff.At || sweep.Mod.Target != "pad Cutoff" {
		t.Errorf("sweep moves %s at %v, want pad Cutoff at %v", sweep.Mod.Target, sweep.Mod.At, cutoff.At)
	}
	if wash.Mod.Target != "pad/space Mix" {
		t.Errorf("wash moves %s", wash.Mod.Target)
	}
	if sweep.Mod.At.Channel < p.ControlChannels[0] || sweep.Mod.At.Channel > poolTopChannel {
		t.Errorf("Cutoff is mapped on channel %d, which is no parameter channel", sweep.Mod.At.Channel)
	}
	// 0.25 and 0.75 of Cutoff's range are CC 32 and 95; 10% and 60% of the
	// reverb's Mix 13 and 76.
	if sweep.Mod.Depth != [2]int{32, 95} || wash.Mod.Depth != [2]int{13, 76} {
		t.Errorf("depths %v and %v, want [32 95] and [13 76]", sweep.Mod.Depth, wash.Mod.Depth)
	}
	// Its routing is mapped like any parameter.
	mapped := map[string]bool{}
	for _, pp := range sweep.Params {
		mapped[pp.Key] = pp.At.Number > 0
	}
	for _, key := range []string{"midi_ch", "midi_cc", "midi_min", "midi_max", "time_num"} {
		if !mapped[key] {
			t.Errorf("sweep's %s is not mapped (%v)", key, mapped)
		}
	}
}

// TestModulatorRouteWire checks the values that set LFOH's routing land on
// the integers meant, whether the plugin rounds or truncates.
func TestModulatorRouteWire(t *testing.T) {
	k := modulatorKinds[0]
	for ch := 1; ch <= 16; ch++ {
		w := k.Channel.wire(ch)
		got := 1 + 15*float64(float32(float32(w)/127))
		if int(got+0.5) != ch || int(got) != ch {
			t.Errorf("channel %d: CC %d lands on %.3f", ch, w, got)
		}
	}
	for _, v := range []int{0, 1, 64, 74, 126, 127} {
		if w := k.CC.wire(v); w != v {
			t.Errorf("controller %d is sent as %d", v, w)
		}
	}
}

// stateValue is the value a section's state gives a controller, -1 for none,
// and the position of that message in the state.
func stateValue(events []Event, at Assignment) (value, pos int) {
	for i, ev := range events {
		if ev.Kind == Control && ev.Channel == at.Channel && ev.Data1 == at.Number {
			return ev.Data2, i
		}
	}
	return -1, -1
}

func TestModulatorCompile(t *testing.T) {
	p, err := modPlan(t, withWashHome)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	tl, err := Compile(p.song, p)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	sweep := p.Node("pad", "sweep")
	param := func(n *NodePlan, key string) Assignment {
		for _, pp := range n.Params {
			if pp.Key == key {
				return pp.At
			}
		}
		t.Fatalf("%s has no %s", nodeLabel(n), key)
		return Assignment{}
	}
	ch, cc, lo, hi := param(sweep, "midi_ch"), param(sweep, "midi_cc"), param(sweep, "midi_min"), param(sweep, "midi_max")
	cutoff := sweep.Mod.At
	k := modulatorKinds[0]

	type want struct {
		ch, cc, lo, hi, bypass, cutoff int
	}
	for i, w := range []want{
		{k.Channel.wire(cutoff.Channel), cutoff.Number, 32, 95, 0, -1},  // verse: the sweep runs; Cutoff is its own
		{k.Channel.wire(parkChannel), cutoff.Number, 32, 95, 0, 76},     // bridge: bypassed, so parked (never AUM's bypass), Cutoff at 0.6
		{k.Channel.wire(cutoff.Channel), cutoff.Number, 13, 114, 0, -1}, // drop: the scene's depth
		{k.Channel.wire(cutoff.Channel), cutoff.Number, 32, 95, 0, -1},
	} {
		sec := tl.Sections[i]
		got := want{cutoff: -1}
		got.ch, _ = stateValue(sec.State, ch)
		got.cc, _ = stateValue(sec.State, cc)
		got.lo, _ = stateValue(sec.State, lo)
		got.hi, _ = stateValue(sec.State, hi)
		got.bypass, _ = stateValue(sec.State, sweep.Bypass)
		var cpos, parkPos int
		got.cutoff, cpos = stateValue(sec.State, cutoff)
		_, parkPos = stateValue(sec.State, ch)
		if got != w {
			t.Errorf("%s's state: %+v, want %+v", sec.Name, got, w)
		}
		if got.cutoff >= 0 && cpos < parkPos {
			t.Errorf("%s: Cutoff is set before the sweep is parked", sec.Name)
		}
	}

	// Between the sections only what changes is sent; Cutoff only where the
	// sweep is bypassed, and the bridge's ramp moves it there. Its last step
	// is the bridge's end, which is sent before the drop sends the sweep back
	// to it.
	var cutoffs []Event
	routingAfterDrop := map[int]int{}
	dropTick := tl.Sections[2].Tick
	unparked := -1
	for i, ev := range tl.Events {
		if ev.Kind == Control && ev.Channel == cutoff.Channel && ev.Data1 == cutoff.Number {
			cutoffs = append(cutoffs, ev)
			if ev.Tick == dropTick && unparked >= 0 {
				t.Errorf("the bridge's last Cutoff is sent after the drop sends the sweep back to it")
			}
		}
		if ev.Tick == dropTick && ev.Kind == Control && ev.Channel == ch.Channel {
			routingAfterDrop[ev.Data1] = ev.Data2
			if ev.Data1 == ch.Number {
				unparked = i
			}
		}
	}
	bridge, drop := tl.Sections[1], tl.Sections[2]
	for _, ev := range cutoffs {
		if ev.Tick < bridge.Tick || ev.Tick > drop.Tick {
			t.Errorf("Cutoff is sent at tick %d, outside the bridge (%d..%d), where the sweep moves it", ev.Tick, bridge.Tick, drop.Tick)
		}
	}
	if len(cutoffs) < 10 {
		t.Errorf("the bridge's ramp of Cutoff sends %d values", len(cutoffs))
	}
	// The drop loads another preset into the sweep, and its routing is sent
	// again after it, whatever the preset holds.
	for at, v := range map[Assignment]int{ch: k.Channel.wire(cutoff.Channel), cc: cutoff.Number, lo: 13, hi: 114} {
		if at.Channel != ch.Channel {
			continue
		}
		if got, ok := routingAfterDrop[at.Number]; !ok || got != v {
			t.Errorf("after the drop's preset load %v is %d (sent: %v), want %d", at, got, ok, v)
		}
	}
	// AUM's bypass of a modulator stays off throughout: parking is its bypass.
	for _, ev := range tl.Events {
		if ev.Kind == Control && ev.Channel == sweep.Bypass.Channel && ev.Data1 == sweep.Bypass.Number && ev.Data2 != 0 {
			t.Errorf("tick %d bypasses the sweep in AUM (%d)", ev.Tick, ev.Data2)
		}
	}
	pr, _ := sweep.Preset("wonkie")
	loaded := false
	for _, ev := range tl.Events {
		if ev.Kind == Program && ev.Channel == pr.Channel && ev.Data1 == pr.Program && ev.Tick < dropTick {
			loaded = true
		}
	}
	if !loaded {
		t.Error("the drop does not load the sweep's preset ahead of the section")
	}
}

func TestModulatorSession(t *testing.T) {
	// The wash opens bypassed, and the reverb's Mix rests at a value of its
	// own then.
	p, err := modPlan(t, func(doc string) string {
		doc = strings.Replace(withWashHome(doc), `{id: wash, plugin: LFOH,`, `{id: wash, plugin: LFOH, bypass: true,`, 1)
		return strings.Replace(doc, `{id: space, plugin: reverb}`, `{id: space, plugin: reverb, params: {Mix: 0.3}}`, 1)
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sess, rep, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if rep.Modulators != 3 {
		t.Errorf("the report counts %d modulators", rep.Modulators)
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		t.Fatal(err)
	}
	re, err := aum.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	checkModulatorSession(t, re, p)
}

// checkModulatorSession reads a session authored from modSong back: every
// modulator hosted where the plan puts it and routed to MIDI Control through
// the control channels' filter, its target and its routing mapped.
func checkModulatorSession(t *testing.T, sess *aum.Session, p *Plan) {
	t.Helper()
	conns := matrixConnections(t, sess)
	filters := matrixFilters(t, sess)
	var control int64
	for _, ch := range p.ControlChannels {
		control |= 1 << (ch - 1)
	}
	if filters["BuiltIn:MIDI Control"] != control {
		t.Errorf("MIDI Control passes %016b, want the control channels %016b", filters["BuiltIn:MIDI Control"], control)
	}
	chans := sess.Channels()
	for _, n := range p.modulators() {
		src := fmt.Sprintf("Node:Chan%d:Slot%d:MIDI Out", n.Strip, n.Slot)
		if got := conns[src]; len(got) != 1 || got[0] != "BuiltIn:MIDI Control" {
			t.Errorf("%s routes %v, want MIDI Control alone", src, got)
		}
		var node *aum.Node
		for i := range chans[n.Strip].Nodes {
			if chans[n.Strip].Nodes[i].Slot == n.Slot {
				node = &chans[n.Strip].Nodes[i]
			}
		}
		if node == nil || node.Component == nil || !aum.ComponentMatches(*node.Component, modulatorKinds[0].Component) {
			t.Errorf("strip %d slot %d holds %+v, want LFOH", n.Strip, n.Slot, node)
		}
		if m, ok := sess.FindMapping(n.Mod.targetParamCollection(), n.Mod.targetParam().ref.Target); !ok || m.Spec.Data1 != n.Mod.At.Number || m.Spec.Channel != n.Mod.At.Channel-1 {
			t.Errorf("%s's target %s is mapped as %+v, want %v", nodeLabel(n), n.Mod.Target, m, n.Mod.At)
		}
		for _, pp := range n.Params {
			if !n.Mod.isRoute(pp.Key) {
				continue
			}
			coll := pp.ref.Collection(slotCollection(n.Strip, n.Slot))
			if m, ok := sess.FindMapping(coll, pp.ref.Target); !ok || m.Spec.Data1 != pp.At.Number || m.Spec.Channel != pp.At.Channel-1 {
				t.Errorf("%s's %s is mapped as %+v, want %v", nodeLabel(n), pp.Key, m, pp.At)
			}
		}
	}
	// The wash declares itself bypassed, and is parked by the song's state,
	// not bypassed in AUM: it opens running, as the sweep does.
	for _, id := range []string{"wash", "sweep"} {
		if n := p.Node("pad", id); nodeArchiveState(t, sess, n.Strip, n.Slot, "AUMNode.bypassed") == true {
			t.Errorf("the %s opens bypassed in AUM", id)
		}
	}
	home, err := p.HomeState()
	if err != nil {
		t.Fatal(err)
	}
	wash := p.Node("pad", "wash")
	for _, pp := range wash.Params {
		if pp.Key == "midi_ch" {
			if v, _ := stateValue(home, pp.At); v != modulatorKinds[0].Channel.wire(parkChannel) {
				t.Errorf("the wash's home state sends it to channel CC value %d, want it parked", v)
			}
		}
	}
	if v, _ := stateValue(home, wash.Bypass); v != 0 {
		t.Errorf("the wash's home state bypasses it in AUM (%d)", v)
	}
}

// nodeArchiveState reads one key of a node's archiveNodeState, nil when the
// node or the key is not there.
func nodeArchiveState(t *testing.T, sess *aum.Session, strip, slot int, key string) any {
	t.Helper()
	a := sess.Archive()
	field := func(v any, key string) any {
		m, _ := a.Deref(v).(map[string]any)
		if m == nil {
			return nil
		}
		if _, ok := m["NS.keys"]; ok {
			return nsValue(a, m, key)
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
	node := item(item(root["nodeArchives"], strip), slot)
	return a.Deref(field(field(node, "archiveNodeState"), key))
}

// targetParamCollection is the collection the session maps the target in.
func (m *ModPlan) targetParamCollection() string {
	return m.targetParam().ref.Collection(slotCollection(m.target.Strip, m.target.Slot))
}

// matrixConnections reads every source's destinations from a session's MIDI
// matrix.
func matrixConnections(t *testing.T, sess *aum.Session) map[string][]string {
	t.Helper()
	a := sess.Archive()
	root, _ := a.Root().(map[string]any)
	matrix, _ := a.Deref(root["midiMatrixState"]).(map[string]any)
	conns, _ := a.Deref(nsValue(a, matrix, "connections")).(map[string]any)
	keys, _ := conns["NS.keys"].([]any)
	out := map[string][]string{}
	for _, k := range keys {
		name, _ := a.Deref(k).(string)
		dsts, _ := a.Deref(nsValue(a, conns, name)).(map[string]any)
		objs, _ := dsts["NS.objects"].([]any)
		for _, d := range objs {
			s, _ := a.Deref(d).(string)
			out[name] = append(out[name], s)
		}
	}
	return out
}

func TestModulatorChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) string
		want string
	}{
		{"no such parameter", func(d string) string {
			return strings.Replace(d, "target: {param: Cutoff}", "target: {param: Cutof}", 1)
		}, "/tracks/1/modulators/0/target/param: Test Synth has no writable parameter \"Cutof\""},
		{"no such effect", func(d string) string {
			return strings.Replace(d, "target: {effect: space, param: Mix}", "target: {effect: hall, param: Mix}", 1)
		}, `/tracks/1/modulators/1/target/effect: "pad" has no effect "hall" (it has space)`},
		{"no target", func(d string) string {
			return strings.Replace(d, ", target: {param: Resonance}", "", 1)
		}, "/tracks/0/modulators/0/target/param: name the parameter the modulator moves"},
		{"not a modulator", func(d string) string {
			return strings.Replace(d, "{id: wob, plugin: lfoh,", "{id: wob, plugin: arp,", 1)
		}, "/tracks/0/modulators/0/plugin: Test Arp is not a modulator a song can host"},
		{"an effect is no modulator", func(d string) string {
			return strings.Replace(d, "{id: space, plugin: reverb}", "{id: space, plugin: reverb}\n      - {id: lfo, plugin: lfoh}", 1)
		}, "/tracks/1/effects/1/plugin: LFOH is a modulator; declare it under the track's modulators"},
		{"routing is the song's", func(d string) string {
			return strings.Replace(d, "params: {Time Numerator: 0.2}", "params: {Time Numerator: 0.2, MIDI CC: 0.5}", 1)
		}, "/tracks/1/modulators/0/params/MIDI CC: MIDI CC is the modulator's routing"},
		{"routing in a scene", func(d string) string {
			return strings.Replace(d, "params: {Time Numerator: 0.4}", "params: {Time Numerator: 0.4, midi_ch: 0.5}", 1)
		}, "/scenes/drop/pad/modulators/sweep/params/midi_ch: midi_ch is the modulator's routing"},
		{"one modulator a parameter", func(d string) string {
			return strings.Replace(d, "target: {effect: space, param: Mix}", "target: {param: cutoff}", 1)
		}, "/tracks/1/modulators/1/target: pad Cutoff is already moved by pad's modulator sweep"},
		{"depth of one value", func(d string) string {
			return strings.Replace(d, "depth: [0.25, 0.75]", "depth: [0.25]", 1)
		}, "/tracks/1/modulators/0/depth: two values"},
		{"depth upside down", func(d string) string {
			return strings.Replace(d, "depth: [0.25, 0.75]", "depth: [0.75, 0.25]", 1)
		}, "/tracks/1/modulators/0/depth: 0.75..0.25 is 95..32"},
		{"depth out of range", func(d string) string {
			return strings.Replace(d, "depth: [0.1, 0.9]", "depth: [0.1, 1.5]", 1)
		}, "/scenes/drop/pad/modulators/sweep/depth: 1.5 is outside Cutoff's range"},
		{"no rest value", func(d string) string {
			return strings.Replace(d, "params: {Cutoff: 0.5}", "params: {Resonance: 0.5}", 1)
		}, "/tracks/1/modulators/0/target: pad's modulator sweep moves pad Cutoff, and the scene \"bridge\" bypasses it; declare Cutoff"},
		{"a scene sets it where it runs", func(d string) string {
			return strings.Replace(d, "modulators: {sweep: {preset: wonkie,", "params: {Cutoff: 0.9}\n      modulators: {sweep: {preset: wonkie,", 1)
		}, "/sections/drop/scene: the scene \"drop\" sets pad Cutoff, which pad's modulator sweep moves in this section"},
		{"a ramp of it where it runs", func(d string) string {
			return strings.Replace(d, "drop: {bars: 1, scene: drop, play: {bass: bass, pad: chords}}",
				"drop: {bars: 1, scene: drop, play: {bass: bass, pad: chords}, automation: [{track: pad, param: Cutoff, from: 0.1, to: 0.9}]}", 1)
		}, "/sections/drop/automation/0: pad's modulator sweep moves pad Cutoff in this section"},
		{"a ramp of its routing", func(d string) string {
			return strings.Replace(d, "{track: pad, modulator: wash, param: Frequency,", "{track: pad, modulator: wash, param: MIDI Max,", 1)
		}, "/sections/bridge/automation/1/param: MIDI Max is the modulator's routing"},
		{"a modulator's id is its strip's", func(d string) string {
			return strings.Replace(d, "{id: wash, plugin: LFOH,", "{id: space, plugin: LFOH,", 1)
		}, `/tracks/1/modulators/1/id: "space" is already an effect or a modulator of "pad"`},
		{"a scene addresses it as an effect", func(d string) string {
			return strings.Replace(d, "modulators: {sweep: {bypass: true}}", "effects: {sweep: {bypass: true}}", 1)
		}, `/scenes/bridge/pad/effects/sweep: "pad" has no effect "sweep" (it has space); it is a modulator: set it under modulators`},
		{"a ramp of no modulator", func(d string) string {
			return strings.Replace(d, "{track: pad, modulator: wash,", "{track: pad, modulator: wish,", 1)
		}, `/sections/bridge/automation/1/modulator: "pad" has no modulator "wish" (it has sweep, wash)`},
		{"the master has none", func(d string) string {
			return strings.Replace(d, "{track: pad, modulator: wash,", "{track: master, modulator: wash,", 1)
		}, "/sections/bridge/automation/1/modulator: the master has no modulators"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := modPlan(t, func(d string) string { return tc.edit(withWashHome(d)) })
			if err == nil {
				t.Fatalf("accepted; want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error:\n%v\nwant it to contain:\n%s", err, tc.want)
			}
		})
	}
}

// TestModulatorFingerprint: a modulator is part of the session; what it moves
// and how far is sent as state, so moving it to a parameter the session maps
// already needs no new session.
func TestModulatorFingerprint(t *testing.T) {
	base, err := modPlan(t, withWashHome)
	if err != nil {
		t.Fatal(err)
	}
	none, err := modPlan(t, func(d string) string {
		d = withWashHome(d)
		return strings.Replace(d, "    modulators:\n      - {id: wob, plugin: lfoh, target: {param: Resonance}}\n", "", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if base.Fingerprint() == none.Fingerprint() {
		t.Error("removing a modulator keeps the fingerprint")
	}
	deeper, err := modPlan(t, func(d string) string {
		return strings.Replace(withWashHome(d), "depth: [0.25, 0.75]", "depth: [0.3, 0.9]", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if base.Fingerprint() != deeper.Fingerprint() {
		t.Error("another depth changes the fingerprint")
	}
}
