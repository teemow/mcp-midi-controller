package song

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// atomDump is the probe of the sequencer as a rig stages it.
var atomDump = device.ProbeDump{Component: AtomComponent, Name: "Atom | Piano Roll²"}

// TestSequenceLayout checks the lead-in and that the timeline follows it
// unchanged: the opening presets as the transport starts, the rest of the
// opening sound half a preroll later, the first note on the bar line after.
func TestSequenceLayout(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	q := tl.Sequence()

	// 120 BPM in 4/4: a bar lasts 2 s, more than the 1.2 s the presets need.
	if q.LeadBars != 1 || q.LeadTicks() != tl.BarTicks || q.Ticks != tl.Ticks+tl.BarTicks {
		t.Fatalf("lead-in = %d bar(s), %d ticks of %d; want one bar ahead of the song's %d", q.LeadBars, q.LeadTicks(), q.Ticks, tl.Ticks)
	}
	if q.Lead() != 2*time.Second || q.Duration() != 14*time.Second {
		t.Errorf("lead-in lasts %s of %s, want 2 s of 14 s", q.Lead(), q.Duration())
	}

	// The opening state: every preset at 0, everything else 600 ms later, and
	// all of it before the song's first bar.
	opening := tl.Sections[0].State
	rest := tl.ticksOf(defaultPreroll / 2)
	if rest <= 0 || rest >= q.LeadTicks() {
		t.Fatalf("the opening controls sit at tick %d, outside the lead-in", rest)
	}
	wantPresets := 0
	for _, e := range opening {
		if e.Kind == Program {
			wantPresets++
		}
	}
	var presets, others int
	for _, e := range q.Controls[:len(opening)] {
		switch {
		case e.Kind == Program && e.Tick == 0:
			presets++
		case e.Kind == Control && e.Tick == rest:
			others++
		default:
			t.Errorf("opening event %s at tick %d", describe(e), e.Tick)
		}
	}
	if wantPresets != 3 || presets != wantPresets || presets+others != len(opening) {
		t.Errorf("the lead-in holds %d presets and %d other controls, want the opening state's %d with its %d presets (three: bass, lead and the lead's reverb)", presets, others, len(opening), wantPresets)
	}

	// The timeline's own changes follow, shifted by the lead-in: the chorus's
	// presets go out 400 ms ahead of bar 3, which is the transport's bar 4.
	later := q.Controls[len(opening):]
	if len(later) == 0 || later[0].Kind != Program {
		t.Fatalf("after the opening state comes %v, want the chorus's preset loads", later)
	}
	chorus := q.LeadTicks() + tl.Sections[1].Tick
	if got, want := later[0].Tick, chorus-tl.leadTicks(); got != want {
		t.Errorf("the chorus's first preset load is at tick %d, want %d (400 ms ahead of tick %d)", got, want, chorus)
	}
	timelineControls := 0
	for _, e := range tl.Events {
		if e.Kind == Program || e.Kind == Control {
			timelineControls++
		}
	}
	if len(later) != timelineControls {
		t.Errorf("the sequence carries %d of the timeline's %d control changes", len(later), timelineControls)
	}

	// Every note, with its length, on its track's channel.
	notes := 0
	for _, ts := range tl.Tracks {
		notes += ts.Notes
	}
	if len(q.Notes) != notes {
		t.Fatalf("the sequence has %d notes, the timeline %d", len(q.Notes), notes)
	}
	first := q.Notes[0]
	if first.Tick != q.LeadTicks() {
		t.Errorf("the first note is at tick %d, want the end of the lead-in (%d)", first.Tick, q.LeadTicks())
	}
	for i, n := range q.Notes {
		if n.Ticks <= 0 || n.Tick+n.Ticks > q.Ticks {
			t.Errorf("note %d = %+v does not end inside the sequence", i, n)
		}
		if i > 0 && n.Tick < q.Notes[i-1].Tick {
			t.Errorf("note %d starts before note %d", i, i-1)
		}
	}
	// The bass's first A1 is an eighth at gate 0.9: 216 ticks on channel 1.
	var bass *SeqNote
	for i := range q.Notes {
		if q.Notes[i].Channel == 1 {
			bass = &q.Notes[i]
			break
		}
	}
	if bass == nil || bass.Note != 33 || bass.Ticks != 216 || bass.Velocity != 96 {
		t.Errorf("the bass's first note = %+v, want A1 for 216 ticks at velocity 96", bass)
	}
}

// TestSequenceLeadIn sizes the lead-in in whole bars: enough for the presets
// to load, and never less than one.
func TestSequenceLeadIn(t *testing.T) {
	// shortBars is a song in 2/4 with a preset to load.
	const shortBars = `
title: Short Bars
tempo: 240
time_signature: 2/4
tracks:
  - id: a
    instrument: {plugin: synth, preset: Init}
patterns:
  p: {step: 1/4, notes: "C3 ."}
sections:
  s: {bars: 4, play: {a: p}}
arrangement: [s]
`
	for _, tc := range []struct {
		name, doc string
		bars      int
	}{
		{"a bar that holds the preroll", testSong, 1},
		{"a bar of exactly the preroll", strings.Replace(testSong, "tempo: 120", "tempo: 200", 1), 1},
		{"bars shorter than the preroll", strings.Replace(testSong, "tempo: 120", "tempo: 240", 1), 2},
		{"half-second bars", shortBars, 3},
	} {
		tl, _ := compileTest(t, tc.doc)
		q := tl.Sequence()
		if q.LeadBars != tc.bars {
			t.Errorf("%s: lead-in of %d bar(s) (%s), want %d", tc.name, q.LeadBars, q.Lead(), tc.bars)
		}
		if q.Lead() < defaultPreroll {
			t.Errorf("%s: the lead-in lasts %s, less than the %s the presets get", tc.name, q.Lead(), defaultPreroll)
		}
		// The song still starts on a bar line of the transport.
		if q.Notes[0].Tick != q.LeadTicks() || q.LeadTicks()%tl.BarTicks != 0 {
			t.Errorf("%s: the first note is at tick %d with bars of %d ticks", tc.name, q.Notes[0].Tick, tl.BarTicks)
		}
	}

	// Without a preset to load the opening controls go out with the transport.
	noPresets := `
title: Plain
tempo: 120
tracks:
  - id: a
    instrument: {plugin: synth}
patterns:
  p: {step: 1/4, notes: "C3 . . ."}
sections:
  s: {bars: 1, play: {a: p}}
arrangement: [s]
`
	tl, _ := compileTest(t, noPresets)
	q := tl.Sequence()
	if q.LeadBars != 1 {
		t.Errorf("a song without presets has a lead-in of %d bars, want 1", q.LeadBars)
	}
	for _, e := range q.Controls {
		if e.Tick != 0 {
			t.Errorf("%s at tick %d, want the whole opening state at 0", describe(e), e.Tick)
		}
	}
}

// TestSequenceFingerprint: the fingerprint follows what is played, and nothing
// else.
func TestSequenceFingerprint(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	base := tl.Sequence().Fingerprint()
	if again, _ := compileTest(t, testSong); again.Sequence().Fingerprint() != base {
		t.Error("the same song has two sequence fingerprints")
	}
	for name, doc := range map[string]string{
		"a note":      strings.Replace(testSong, `"A1 . A1 . A1 . A1 . |`, `"A1 . A1 . A1 . E1 . |`, 1),
		"a velocity":  strings.Replace(testSong, "A3:50", "A3:60", 1),
		"the tempo":   strings.Replace(testSong, "tempo: 120", "tempo: 121", 1),
		"a level":     strings.Replace(testSong, "level: -6", "level: -9", 1),
		"the arrange": strings.Replace(testSong, "arrangement: [verse, chorus, verse]", "arrangement: [verse, chorus]", 1),
	} {
		changed, _ := compileTest(t, doc)
		if changed.Sequence().Fingerprint() == base {
			t.Errorf("changing %s left the sequence fingerprint as it was", name)
		}
	}
	if renamed, _ := compileTest(t, strings.Replace(testSong, "title: Test Song", "title: Other", 1)); renamed.Sequence().Fingerprint() != base {
		t.Error("a new title changed the sequence fingerprint")
	}
}

// atomStateOf decodes the state Atom is given.
func atomStateOf(t *testing.T, q *Sequence) (map[string]json.RawMessage, atomPattern) {
	t.Helper()
	state, err := q.AtomState("Test Song", BrainStrip, SequencerSlot)
	if err != nil {
		t.Fatalf("AtomState: %v", err)
	}
	out := map[string]json.RawMessage{}
	for k, v := range state {
		if !json.Valid(v) {
			t.Errorf("state key %s is not JSON: %.40s", k, v)
		}
		out[k] = v
	}
	var patterns []atomPattern
	if err := json.Unmarshal(state["patterns"], &patterns); err != nil || len(patterns) != 1 {
		t.Fatalf("patterns = %d (%v), want the one clip", len(patterns), err)
	}
	return out, patterns[0]
}

// TestAtomPattern checks the clip the sequencer's engine plays: positions in
// sixteenths, channels from 0, and control changes as raw MIDI.
func TestAtomPattern(t *testing.T) {
	tl, plan := compileTest(t, testSong)
	q := tl.Sequence()
	state, p := atomStateOf(t, q)

	// Seven bars of 4/4 (the lead-in and the song's six) are 112 sixteenths.
	if p.ID != "initial" || p.Interval.Begin != 0 || p.Interval.End != 112 {
		t.Errorf("clip %q runs %g..%g, want initial 0..112", p.ID, p.Interval.Begin, p.Interval.End)
	}
	if len(p.Notes) != len(q.Notes) || len(p.Events) != len(q.Controls) {
		t.Fatalf("clip has %d notes and %d events, the sequence %d and %d", len(p.Notes), len(p.Events), len(q.Notes), len(q.Controls))
	}

	// The first bar of the song is the clip's second: the bass's A1 on the
	// downbeat (sixteenth 16) and again an eighth later, each 0.45 of a
	// quarter long (1.8 sixteenths).
	var bass []atomNote
	for _, n := range p.Notes {
		if n.Channel == 0 {
			bass = append(bass, n)
		}
	}
	if len(bass) < 2 || bass[0] != (atomNote{Time: 16, Duration: 1.8, Channel: 0, Pitch: 33, Velocity: 96}) || bass[1].Time != 20 {
		t.Errorf("the bass begins %+v, want A1 at 16 and at 20, 1.8 sixteenths long on channel 0", bass[:min(2, len(bass))])
	}
	// The drums play on the third track's channel; a kick on the downbeat.
	kick := false
	for _, n := range p.Notes {
		kick = kick || (n.Channel == 2 && n.Pitch == 36 && n.Time == 16)
		if n.Channel < 0 || n.Channel > 2 {
			t.Fatalf("note %+v is on a channel no track plays on", n)
		}
	}
	if !kick {
		t.Error("no kick on the song's first downbeat (channel 2, sixteenth 16)")
	}

	// Control changes are status bytes with the channel in them: a Program
	// Change on the global channel 15 is 0xCE with the program and a zero, a
	// controller 0xB0 plus the channel with its number and value.
	lead := plan.Node("lead", "")
	brightLead := -1
	for _, pr := range lead.Presets {
		if pr.Name == "Bright Lead" {
			brightLead = pr.Program
		}
	}
	chorusPreset := atomEvent{Time: atomTime(q.LeadTicks() + tl.Sections[1].Tick - tl.leadTicks()), Byte1: 0xCE, Byte2: brightLead}
	level := plan.Track("lead").Level
	chorusLevel := atomEvent{Time: 48, Byte1: 0xB0 | (level.Channel - 1), Byte2: level.Number, Byte3: levelWire(-6)}
	found := map[atomEvent]bool{}
	for _, e := range p.Events {
		found[e] = true
		if e.Byte1&0xF0 != 0xB0 && e.Byte1&0xF0 != 0xC0 {
			t.Fatalf("event %+v is neither a controller nor a program change", e)
		}
		if ch := e.Byte1&0x0F + 1; ch <= 3 {
			t.Errorf("event %+v is on MIDI channel %d, a track's channel", e, ch)
		}
	}
	if !found[chorusPreset] {
		t.Errorf("no %+v: the lead's chorus preset 400 ms ahead of the chorus", chorusPreset)
	}
	if !found[chorusLevel] {
		t.Errorf("no %+v: the lead's chorus level on the chorus's bar line", chorusLevel)
	}

	// The clip is armed, plays once and is not written to.
	for key, want := range map[string]string{
		"isLaunched": "true", "isLooped": "false", "isRecording": "false", "isMuted": "false",
		"isQuantizing": "false", "isSnapbackAutomation": "false", "outputTempo": "1",
		"name": `"Test Song"`, "trackIndex": "0", "slotIndex": "1", "activePattern": "0",
	} {
		if got := string(state[key]); got != want {
			t.Errorf("state %s = %s, want %s", key, got, want)
		}
	}
	var channels []bool
	if err := json.Unmarshal(state["outputChannels"], &channels); err != nil || len(channels) != 16 {
		t.Fatalf("outputChannels = %s", state["outputChannels"])
	}
	for ch, on := range channels {
		if !on {
			t.Errorf("the clip's output channel %d is off", ch+1)
		}
	}
}

// TestAtomStore checks the editor's copy of the clip: it is what the plugin's
// window draws, so it must hold the same notes and events as the engine's.
func TestAtomStore(t *testing.T) {
	tl, _ := compileTest(t, testSong)
	q := tl.Sequence()
	state, p := atomStateOf(t, q)
	var store string
	if err := json.Unmarshal(state["store"], &store); err != nil {
		t.Fatalf("store is not a JSON string: %v", err)
	}

	// The editor's serialization is a JavaScript literal: balanced, with its
	// maps and sets spelled out.
	depth := 0
	inString := false
	for i := 0; i < len(store); i++ {
		switch c := store[i]; {
		case inString:
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{' || c == '[' || c == '(':
			depth++
		case c == '}' || c == ']' || c == ')':
			depth--
		}
		if depth < 0 {
			t.Fatalf("the store closes a bracket it never opened at %d", i)
		}
	}
	if depth != 0 || inString {
		t.Fatalf("the store is not balanced (depth %d, in a string: %v)", depth, inString)
	}

	for _, want := range []string{
		`"phrases":{"active":"initial","playing":"initial","map":new Map([["initial",{"bars":7,`,
		`"timeSignature":{"numerator":4,"denominator":4},"interval":{"begin":0,"end":112}`,
		`"meta":{"generation":"`, `"name":"Test Song","track":0,"slot":1`,
		`"playback":{"launched":true,"looped":false}`,
		`"quantize":{"enabled":false,`,
	} {
		if !strings.Contains(store, want) {
			t.Errorf("the store lacks %s", want)
		}
	}
	// Every note is there once, in each of the forms the editor keeps, and in
	// the drawing order; every event with its bytes.
	if got := strings.Count(store, `"quantized":{`); got != len(p.Notes) {
		t.Errorf("the store has %d notes, the clip %d", got, len(p.Notes))
	}
	first := p.Notes[0]
	note := fmt.Sprintf(`["n00000",{"channel":%d,"quantized":{"time":%g,"duration":%g,"pitch":%d,"velocity":%d},"unquantized":{"time":%g,"duration":%g,"pitch":%d,"velocity":%d},"swinged":{"time":%g,"duration":%g},"jittered":{"velocity":%d},`,
		first.Channel, first.Time, first.Duration, first.Pitch, first.Velocity, first.Time, first.Duration, first.Pitch, first.Velocity, first.Time, first.Duration, first.Velocity)
	if !strings.Contains(store, note) {
		t.Errorf("the store does not hold the first note as %s", note)
	}
	last := fmt.Sprintf(`"n%05d"`, len(p.Notes)-1)
	if strings.Count(store, last) != 2 || !strings.Contains(store, last+`]),"selections"`) {
		t.Errorf("the last note %s is not in the notes and at the end of the orderings", last)
	}
	if got := strings.Count(store, `,"data":[`); got != len(p.Events) {
		t.Errorf("the store has %d events, the clip %d", got, len(p.Events))
	}
	e := p.Events[len(p.Events)-1]
	if want := fmt.Sprintf(`["e%05d",{"time":%g,"data":[%d,%d,%d]}]`, len(p.Events)-1, e.Time, e.Byte1, e.Byte2, e.Byte3); !strings.Contains(store, want) {
		t.Errorf("the store does not hold the last event as %s", want)
	}

	// The same song gives the same state, byte for byte.
	again, err := q.AtomState("Test Song", BrainStrip, SequencerSlot)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range again {
		if string(state[k]) != string(v) {
			t.Errorf("state key %s differs between two encodings of the same sequence", k)
		}
	}
	var id string
	if err := json.Unmarshal(state["id"], &id); err != nil || len(id) != 36 || strings.ToUpper(id) != id {
		t.Errorf("clip id = %s, want an upper-case UUID", state["id"])
	}
}

// TestBuildSessionWithSequencer authors a session that hosts the sequencer and
// reads the encoded file back: the node next to the brain, its state with the
// version the plugin expects, its routes and its bypass mapping.
func TestBuildSessionWithSequencer(t *testing.T) {
	tl, p := compileTest(t, testSong)
	q := tl.Sequence()

	if _, _, err := p.BuildSession(SessionOptions{Sequence: q}); err == nil {
		t.Error("a sequence without the sequencer's probe was accepted")
	}
	other := testCatalog()["delay"]
	if _, _, err := p.BuildSession(SessionOptions{Sequencer: &other, Sequence: q}); err == nil {
		t.Error("a sequence was authored into a plugin that is not the sequencer")
	}
	plain, plainRep, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	sess, rep, err := p.BuildSession(SessionOptions{Sequencer: &atomDump, Sequence: q})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if !rep.Sequenced || rep.SequenceNotes != len(q.Notes) || rep.SequenceControls != len(q.Controls) || rep.LeadBars != 1 {
		t.Errorf("report = %+v, want the sequence's notes, controls and lead-in", rep)
	}
	if rep.Strips != plainRep.Strips || rep.Nodes != plainRep.Nodes+1 || rep.Mappings != plainRep.Mappings+1 {
		t.Errorf("with the sequencer: %d strips, %d nodes, %d mappings; without: %d, %d, %d; want one node and one mapping more",
			rep.Strips, rep.Nodes, rep.Mappings, plainRep.Strips, plainRep.Nodes, plainRep.Mappings)
	}
	if plainRep.Sequenced || len(plain.Channels()[BrainStrip].Nodes) != 1 {
		t.Error("a session authored without a sequence hosts one")
	}

	data, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := aum.Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	brain := re.Channels()[BrainStrip]
	if brain.Kind != aum.KindMIDI || len(brain.Nodes) != 2 {
		t.Fatalf("brain strip = %v with %d nodes, want the brain and the sequencer on a MIDI strip", brain.Kind, len(brain.Nodes))
	}
	if c := brain.Nodes[SequencerSlot].Component; c == nil || *c != AtomComponent {
		t.Errorf("slot %d hosts %+v, want the sequencer", SequencerSlot, c)
	}

	// The state: every key as data, the clip among them, and the version as
	// the number the plugin checks.
	doc, err := re.NodeAuStateDoc(BrainStrip, SequencerSlot)
	if err != nil {
		t.Fatalf("NodeAuStateDoc: %v", err)
	}
	var patterns []atomPattern
	if err := json.Unmarshal(doc["patterns"], &patterns); err != nil || len(patterns) != 1 || len(patterns[0].Notes) != len(q.Notes) {
		t.Errorf("the staged clip does not decode to the sequence's %d notes: %v", len(q.Notes), err)
	}
	if _, ok := doc["store"]; !ok {
		t.Error("the staged state has no editor store")
	}
	if got := stateVersion(t, re, BrainStrip, SequencerSlot); got != uint64(atomStateVersion) {
		t.Errorf("the sequencer's state version is %v, want %d", got, atomStateVersion)
	}
	// The brain's own state keeps the version the session library gives it.
	if got := stateVersion(t, re, BrainStrip, 0); got != uint64(1) {
		t.Errorf("the brain's state version is %v, want it untouched", got)
	}

	// Routes: the sequencer reaches what the brain reaches. Both are sources
	// in the session's MIDI matrix, keyed by their output port.
	sources := map[string]bool{}
	for _, o := range re.Archive().Objects {
		if s, ok := o.(string); ok && strings.HasPrefix(s, "Node:Chan0:Slot") {
			sources[s] = true
		}
	}
	for _, want := range []string{"Node:Chan0:Slot0:ProbeMidiBrain", "Node:Chan0:Slot1:MIDI OUT"} {
		if !sources[want] {
			t.Errorf("the MIDI matrix has no source %s (has %v)", want, sources)
		}
	}

	// The bypass that keeps the sequencer out of a live playback.
	if p.SequencerBypass != (Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: 106}) {
		t.Errorf("sequencer bypass = %s, want CC 106 on the global channel", p.SequencerBypass)
	}
	mapped := false
	for _, m := range re.Mappings(false) {
		if m.Collection == "Channels/chan0/slot1" && m.Target == "_AUMNode:Bypass" {
			mapped = m.Spec.Type == aum.SpecStateTypeCC && m.Spec.Data1 == 106 && m.Spec.Channel == GlobalChannel-1
		}
		if m.Spec.Type == aum.SpecStateTypeCC && m.Spec.Data1 == 106 && m.Spec.Channel == GlobalChannel-1 && m.Target != "_AUMNode:Bypass" {
			t.Errorf("%s/%s shares the sequencer's bypass controller", m.Collection, m.Target)
		}
	}
	if !mapped {
		t.Error("the sequencer's bypass is not mapped to CC 106 on the global channel")
	}
}

// stateVersion reads the version entry of a node's saved state from a decoded
// session.
func stateVersion(t *testing.T, sess *aum.Session, strip, slot int) any {
	t.Helper()
	a := sess.Archive()
	root, _ := a.Root().(map[string]any)
	nodes := nsObjects(a, nsObjects(a, root["nodeArchives"])[strip])
	node, _ := a.Deref(nodes[slot]).(map[string]any)
	state, _ := a.Deref(node["archiveNodeState"]).(map[string]any)
	doc, _ := a.Deref(nsValue(a, state, "AuStateDoc")).(map[string]any)
	if doc == nil {
		t.Fatalf("no saved state at strip %d slot %d", strip, slot)
	}
	return a.Deref(nsValue(a, doc, "version"))
}

// TestStampAtomVersionErrors: a node that is not there is reported, not
// skipped.
func TestStampAtomVersionErrors(t *testing.T) {
	_, p := compileTest(t, testSong)
	sess, _, err := p.BuildSession(SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range [][2]int{{BrainStrip, SequencerSlot}, {99, 0}, {-1, 0}} {
		if err := stampAtomVersion(sess, at[0], at[1]); err == nil {
			t.Errorf("stamping a version at strip %d slot %d, where no node is, succeeded", at[0], at[1])
		}
	}
}
