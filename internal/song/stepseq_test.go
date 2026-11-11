package song

import (
	"fmt"
	"strings"
	"testing"

	"github.com/beevik/etree"
	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"

	"github.com/teemow/mcp-midi-controller/internal/juce"
)

// battalionCatalog is the test catalogue with a stand-in for UA Battalion:
// its component, and of its parameters the ones the tests set.
func battalionCatalog() Catalog {
	cat := testCatalog()
	var params []device.ProbeParam
	for _, id := range []string{"seqChan1density", "seqChan3probSeqLength", "seqChan3probSeqMode", "seqChan3velSeqLength", "seqChan1seqLength"} {
		params = append(params, device.ProbeParam{Identifier: id, DisplayName: id, KeyPath: id, Min: 0, Max: 1, Writable: true})
	}
	cat["battalion"] = device.ProbeDump{
		Component:      BattalionComponent,
		Name:           "UA Battalion",
		Parameters:     params,
		FactoryPresets: []device.ProbePreset{{Number: 0, Name: "Rebound"}},
	}
	return cat
}

// battalionBase is a stand-in for Battalion's saved state: the default patch's
// <sequences> (neutral lanes, no steps) and the parameters a sequence sets,
// wrapped as JUCE writes it.
func battalionBase() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n\n" + `<state presetName="Default" PluginVersion="1.2.0" url_0="">`)
	b.WriteString(`<PARAM id="drum1env1decay" value="0.2105185091495514"/>`)
	for v := 1; v <= 8; v++ {
		fmt.Fprintf(&b, `<PARAM id="%s" value="15.0"/><PARAM id="%s" value="1.0"/>`, seqLengthParam(v), snapParam(v))
		for i := range battalionLanes {
			l := &battalionLanes[i]
			fmt.Fprintf(&b, `<PARAM id="%s" value="15.0"/><PARAM id="%s" value="0.0"/>`, laneLengthParam(v, l), laneModeParam(v, l))
		}
	}
	b.WriteString(`<locks/><sequences`)
	for v := 0; v < 8; v++ {
		fmt.Fprintf(&b, ` %s%d="%s"`, stepsAttr, v, strings.TrimSuffix(strings.Repeat("0,", 64), ","))
		for _, l := range battalionLanes {
			n := strings.TrimSuffix(strings.Repeat(stateNumber(l.neutral)[:len(stateNumber(l.neutral))-2]+",", 64), ",")
			if l.neutral != 0 {
				n = strings.TrimSuffix(strings.Repeat("0.5,", 64), ",")
			}
			fmt.Fprintf(&b, ` %s%d="%s"`, l.attr, v, n)
		}
	}
	b.WriteString(`/></state>`)
	return juce.EncodeBinaryXML(b.String())
}

// seqSong is a song whose drums are a Battalion with a sequence.
const seqSong = `title: Lanes
tempo: 120
tracks:
  - id: drums
    kind: drums
    instrument:
      plugin: battalion
      params: {seqChan1density: 0.496}
      sequence:
        1:
          steps: "x... x... x... x..."
        3:
          steps: "x.x. x.x. x.x. x.x. | x.x. x.x. x.x. x.x."
          prob: {values: [0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0, 0, 0, 0, 0], mode: free}
          vel: ". . 0.9 . . . 0.9 ."
          ratch: {values: "0 0 0 1", length: 6, mode: TRIG}
patterns:
  beat: {bars: 1, drums: {kick: "x..."}}
sections:
  a: {bars: 2, play: {drums: beat}}
arrangement: [a]
`

func planSeq(t *testing.T, doc string) *Plan {
	t.Helper()
	p, err := NewPlan(mustParse(t, doc), battalionCatalog())
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

// readState decodes an authored state and returns its <state> element.
func readState(t *testing.T, doc map[string][]byte) *etree.Element {
	t.Helper()
	if len(doc) != 1 || doc[battalionStateKey] == nil {
		t.Fatalf("authored state has keys %v, want %s alone", mapKeys(doc), battalionStateKey)
	}
	raw := doc[battalionStateKey]
	text, err := juce.DecodeBinaryXML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(raw) - juce.HeaderLen - 1; got != len(text) || raw[len(raw)-1] != 0 {
		t.Errorf("the block holds %d bytes of text and ends in %#x; its header says %d", got, raw[len(raw)-1], len(text))
	}
	x := etree.NewDocument()
	if err := x.ReadFromString(text); err != nil {
		t.Fatalf("the authored XML does not parse: %v", err)
	}
	return x.Root()
}

func mapKeys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func attrValues(t *testing.T, root *etree.Element, attr string) []string {
	t.Helper()
	v := root.SelectElement("sequences").SelectAttrValue(attr, "")
	vals := strings.Split(v, ",")
	if len(vals) != 64 {
		t.Fatalf("%s has %d values, want 64", attr, len(vals))
	}
	return vals
}

func paramValue(root *etree.Element, id string) string {
	for _, p := range root.SelectElements("PARAM") {
		if p.SelectAttrValue("id", "") == id {
			return p.SelectAttrValue("value", "")
		}
	}
	return "(none)"
}

func TestStepSequenceState(t *testing.T) {
	p := planSeq(t, seqSong)
	n := p.Node("drums", "")
	if !n.HasStepSequence() {
		t.Fatal("the drums have no sequence")
	}
	// The binary twin goes: Battalion plays the XML when it is given alone.
	doc, err := n.StepSequenceState(map[string][]byte{battalionStateKey: battalionBase(), "data": []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	root := readState(t, doc)

	join := func(vals []string, n int) string { return strings.Join(vals[:n], " ") }
	if got := join(attrValues(t, root, "fills0"), 17); got != "1 0 0 0 1 0 0 0 1 0 0 0 1 0 0 0 0" {
		t.Errorf("voice 1's steps = %s", got)
	}
	if got := join(attrValues(t, root, "fills2"), 33); got != "1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 1 0 0" {
		t.Errorf("voice 3's steps = %s", got)
	}
	if got := join(attrValues(t, root, "probabilities2"), 13); got != "0.5 0.5 0.5 0.5 0.5 0.5 0 0 0 0 0 0 0.5" {
		t.Errorf("voice 3's prob lane = %s", got)
	}
	if got := join(attrValues(t, root, "velocities2"), 9); got != "0.5 0.5 0.9 0.5 0.5 0.5 0.9 0.5 0.5" {
		t.Errorf("voice 3's vel lane = %s", got)
	}
	// A lane longer than its values runs on at its neutral value, and its
	// values repeat after its length: a lane in SYNC follows the steps
	// whatever its length.
	if got := join(attrValues(t, root, "ratchets2"), 13); got != "0 0 0 1 0 0 0 0 0 1 0 0 0" {
		t.Errorf("voice 3's ratch lane = %s", got)
	}
	if got := attrValues(t, root, "velocities2")[63]; got != "0.5" {
		t.Errorf("voice 3's vel lane ends in %s, want its 8th value", got)
	}
	if got := attrValues(t, root, "velocities2")[58]; got != "0.9" {
		t.Errorf("voice 3's vel lane has %s at step 59, want its 3rd value again", got)
	}
	// What the song does not say stays as the base has it.
	if got := join(attrValues(t, root, "fills1"), 4); got != "0 0 0 0" {
		t.Errorf("voice 2's steps = %s, want none", got)
	}
	if got := join(attrValues(t, root, "pitches2"), 4); got != "0.5 0.5 0.5 0.5" {
		t.Errorf("voice 3's pitch lane = %s, want neutral", got)
	}
	if got := paramValue(root, "drum1env1decay"); got != "0.2105185091495514" {
		t.Errorf("an unrelated parameter became %s", got)
	}

	for id, want := range map[string]string{
		"seqChan1seqLength":      "15.0", // 16 steps
		"seqChan3seqLength":      "31.0", // 32 steps
		"seqChan3probSeqLength":  "11.0", // 12 values
		"seqChan3probSeqMode":    "1.0",  // FREE
		"seqChan3velSeqLength":   "7.0",
		"seqChan3velSeqMode":     "0.0",
		"seqChan3ratchSeqLength": "5.0",
		"seqChan3ratchSeqMode":   "2.0", // TRIG
		"seqChan2seqLength":      "15.0",
		// SNAP off where the song draws steps, so they run for their length.
		"seqChan1lockLength": "0.0",
		"seqChan3lockLength": "0.0",
		"seqChan2lockLength": "1.0",
	} {
		if got := paramValue(root, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}

// TestStepSequenceParamsWin checks a lane length the song also sets as a
// parameter is left to the parameter when the sequence only implies it, and
// is an error when the sequence says it too.
func TestStepSequenceParamsWin(t *testing.T) {
	doc := strings.Replace(seqSong, "params: {seqChan1density: 0.496}", "params: {seqChan1density: 0.496, seqChan3velSeqLength: 0.5}", 1)
	p := planSeq(t, doc)
	state, err := p.Node("drums", "").StepSequenceState(map[string][]byte{battalionStateKey: battalionBase()})
	if err != nil {
		t.Fatal(err)
	}
	if got := paramValue(readState(t, state), "seqChan3velSeqLength"); got != "15.0" {
		t.Errorf("seqChan3velSeqLength = %s, want the base's 15.0: the song sends it", got)
	}

	doc = strings.Replace(seqSong, "params: {seqChan1density: 0.496}", "params: {seqChan1density: 0.496, seqChan3probSeqMode: 0.5}", 1)
	_, err = NewPlan(mustParse(t, doc), battalionCatalog())
	if err == nil || !strings.Contains(err.Error(), "/tracks/0/instrument/sequence/3/prob/mode: the song also sets seqChan3probSeqMode") {
		t.Errorf("a mode set twice: %v", err)
	}
}

func TestStepSequenceNeedsABase(t *testing.T) {
	n := planSeq(t, seqSong).Node("drums", "")
	if _, err := n.StepSequenceState(map[string][]byte{"data": {1}}); err != ErrNoBaseState {
		t.Errorf("no XML to write into: %v, want ErrNoBaseState", err)
	}
	if _, err := n.StepSequenceState(map[string][]byte{battalionStateKey: juce.EncodeBinaryXML(`<state/>`)}); err == nil || !strings.Contains(err.Error(), "no <sequences>") {
		t.Errorf("a state that is not Battalion's: %v", err)
	}
}

func TestStepSequenceChecks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"voice":       {"        1:\n", "        9:\n", `/tracks/0/instrument/sequence/9: "9" is not a voice (1..8)`},
		"step":        {`"x... x... x... x..."`, `"x... X... x... x..."`, `/sequence/1/steps: step 5: "X" is not a step`},
		"bar":         {`"x.x. x.x. x.x. x.x. | `, `"x.x. x.x. x.x. x.x. x | `, "/sequence/3/steps: bar 1 has 17 steps, a bar holds 16"},
		"lane bar":    {`". . 0.9 . . . 0.9 ."`, `". . 0.9 . | . . 0.9 ."`, "/sequence/3/vel: bar 1 has 4 steps, a bar holds 16"},
		"value":       {`". . 0.9 . . . 0.9 ."`, `". . 1.9 . . . 0.9 ."`, "/sequence/3/vel: step 3: 1.9 is not within 0..1"},
		"list value":  {"[0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0", "[0.5, loud, 0.5, 0.5, 0.5, 0.5, 0, 0", `"loud" is not a lane value`},
		"lane length": {"length: 6, mode: TRIG", "length: 3, mode: TRIG", "/sequence/3/ratch/length: 3, but 4 values are written"},
		"too long":    {"length: 6, mode: TRIG", "length: 65, mode: TRIG", "/sequence/3/ratch/length: 65 is not a length (1..64 steps)"},
		"mode":        {"mode: TRIG", "mode: once", `/sequence/3/ratch/mode: "once" is not one of sync, free, trig, retrig`},
		"shift mode":  {"ratch: {values", "shift: {values", `/sequence/3/shift/mode: "TRIG" is not one of sync, free`},
		"lane key":    {"ratch: {values", "ratchet: {values", "field ratchet not found"},
		"lane field":  {"length: 6, mode: TRIG", "steps: 6, mode: TRIG", `a lane has values, length and mode, not "steps"`},
		"seq length":  {`steps: "x... x... x... x..."`, "steps: \"x... x... x... x...\"\n          length: 8", "/sequence/1/length: 8, but 16 steps are written"},
		"empty":       {`steps: "x... x... x... x..."`, "{}", "/sequence/1: the voice's sequence says nothing"},
		"in a scene":  {"sections:", "scenes:\n  s: {drums: {sequence: {1: {steps: x}}}}\nsections:", "field sequence not found"},
	} {
		doc := strings.Replace(seqSong, tc.from, tc.to, 1)
		if doc == seqSong {
			t.Fatalf("%s: the song no longer contains %q", name, tc.from)
		}
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestStepSequencePlanChecks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"plugin":       {"plugin: battalion\n      params: {seqChan1density: 0.496}", "plugin: drums", "/tracks/0/instrument/sequence: Test Drums has no step sequence a song can write; only UA Battalion does"},
		"preset":       {"plugin: battalion", "plugin: battalion\n      preset: Rebound", "a preset replaces the whole kit with its own sequence"},
		"scene preset": {"sections:", "scenes:\n  s: {drums: {preset: Rebound}}\nsections:", "/scenes/s/drums/preset: a preset replaces the whole kit"},
		"length twice": {"steps: \"x... x... x... x...\"", "steps: \"x... x... x... x...\"\n          length: 16", ""},
	} {
		doc := strings.Replace(seqSong, tc.from, tc.to, 1)
		if tc.want == "" {
			// The drawn sequence's length set in the sequence alone is fine.
			if _, err := NewPlan(mustParse(t, doc), battalionCatalog()); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		_, err := NewPlan(mustParse(t, doc), battalionCatalog())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// An effect has no sequence.
	doc := strings.Replace(seqSong, "    kind: drums\n", "    kind: drums\n    effects: [{plugin: delay, sequence: {1: {steps: x}}}]\n", 1)
	if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "/tracks/0/effects/0/sequence: only an instrument has a step sequence") {
		t.Errorf("a sequence on an effect: %v", err)
	}
}

// TestStepSequenceFingerprint checks another sequence is another session, and
// a song without one keeps its fingerprint.
func TestStepSequenceFingerprint(t *testing.T) {
	a := planSeq(t, seqSong).Fingerprint()
	b := planSeq(t, strings.Replace(seqSong, `". . 0.9 . . . 0.9 ."`, `". . 0.8 . . . 0.9 ."`, 1)).Fingerprint()
	if a == b {
		t.Error("a changed lane kept the session's fingerprint")
	}
	if c := planSeq(t, seqSong).Fingerprint(); c != a {
		t.Error("the same song has two fingerprints")
	}
	if got, want := mustPlan(t, mustParse(t, testSong)).Fingerprint(), testSongFingerprint(t); got != want {
		t.Errorf("a song without a sequence changed its fingerprint: %s, want %s", got, want)
	}
}

// testSongFingerprint is the test song's fingerprint as it is computed
// without the sequence term.
func testSongFingerprint(t *testing.T) string {
	t.Helper()
	p := mustPlan(t, mustParse(t, testSong))
	for _, n := range p.allNodes() {
		if n.seq != nil {
			t.Fatal("the test song has a sequence")
		}
	}
	return p.Fingerprint()
}

// TestStepSequenceRoundTrip checks a song with a sequence is written back as
// it is read.
func TestStepSequenceRoundTrip(t *testing.T) {
	s := mustParse(t, seqSong)
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(data)
	if err != nil {
		t.Fatalf("the written song does not parse: %v\n%s", err, data)
	}
	p1, _ := NewPlan(s, battalionCatalog())
	p2, err := NewPlan(again, battalionCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if p1.Fingerprint() != p2.Fingerprint() {
		t.Errorf("the sequence changed in the round trip:\n%s", data)
	}
	for _, want := range []string{"steps: x... x... x... x...", "mode: free", "- 0.5", "ratch:\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the written song lacks %q:\n%s", want, data)
		}
	}
}

// TestBuildSessionStepSequence authors a session whose Prepare writes the
// sequence, and finds it in the node's saved state.
func TestBuildSessionStepSequence(t *testing.T) {
	p := planSeq(t, seqSong)
	sess, _, err := p.BuildSession(SessionOptions{Prepare: func(n *NodePlan, ns *aum.NodeSpec) error {
		if !n.HasStepSequence() {
			return nil
		}
		state, err := n.StepSequenceState(map[string][]byte{battalionStateKey: battalionBase()})
		ns.StateDoc = state
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := sess.NodeAuStateDoc(p.Track("drums").Strip, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(attrValues(t, readState(t, doc), "fills0")[:5], ""); got != "10001" {
		t.Errorf("the session's voice 1 steps begin %s", got)
	}
}
