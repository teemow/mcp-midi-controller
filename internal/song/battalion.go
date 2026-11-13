package song

// This file writes a song's step sequence (stepseq.go) into the saved state
// of Unfiltered Audio Battalion. The state is JUCE's binary XML (internal/juce)
// under the key jucePluginState. Its <sequences> element holds per voice N
// (0-based) a comma-separated list of 64 values per lane: fillsN for the
// drawn steps (0 or 1), velocitiesN, shiftN, pitchesN, ratchetsN, lengthsN,
// pansN, variationsN, probabilitiesN, sendsN and modN. The lanes' lengths and
// modes, and the drawn sequence's length, are parameters, which the state
// keeps as <PARAM id=… value=…> in the units the plugin shows: a length as
// the index of its label (0 is one step), a mode as 0 SYNC, 1 FREE, 2 TRIG,
// 3 RETRIG.
//
// Measured on a device (Battalion 1.2.0): a lane in SYNC runs with the steps
// and its own length changes nothing, so the values are written repeating
// over all 64 positions; FREE, TRIG and RETRIG lanes run for their length.
// The drawn sequence's length counts only with SNAP (lockLength) off: on, as
// in the default patch, the sequence is as long as the generator's steps.
//
// The state is written over a base, the state of the default patch as AUM
// saved it, and only what the song says changes. Battalion also keeps a
// binary twin of its state (the key data). Given both it is not known which
// it reads; given jucePluginState alone it plays what the XML says, so the
// authored state is that key alone.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/teemow/midi-device/device"

	"github.com/teemow/mcp-midi-controller/internal/juce"
)

// BattalionComponent identifies UA Battalion (Unfiltered Audio).
var BattalionComponent = device.ProbeComponent{Type: "aumu", Subtype: "uaBL", Manufacturer: "UnAu"}

// battalionStateKey is the key of Battalion's saved state that holds its XML.
const battalionStateKey = "jucePluginState"

// battalionLane is one lane of a Battalion voice's sequencer.
type battalionLane struct {
	key     string  // what a song calls it
	attr    string  // the attribute of <sequences> with its values, before the voice index
	param   string  // the parameters of its length and mode: seqChan<N><param>SeqLength, …SeqMode
	neutral float64 // the value that changes nothing
	modes   int     // how many of laneModes it has
}

// battalionLanes are the lanes under a voice's steps, in the window's order.
var battalionLanes = []battalionLane{
	{key: "vel", attr: "velocities", param: "vel", neutral: 0.5, modes: 4},
	{key: "shift", attr: "shift", param: "shift", neutral: 0.5, modes: 2},
	{key: "pitch", attr: "pitches", param: "pitch", neutral: 0.5, modes: 4},
	{key: "ratch", attr: "ratchets", param: "ratch", neutral: 0, modes: 4},
	{key: "env", attr: "lengths", param: "env", neutral: 0.5, modes: 4},
	{key: "pan", attr: "pans", param: "pan", neutral: 0.5, modes: 4},
	{key: "var", attr: "variations", param: "var", neutral: 0.5, modes: 4},
	{key: "prob", attr: "probabilities", param: "prob", neutral: 0.5, modes: 4},
	{key: "sends", attr: "sends", param: "send", neutral: 0.5, modes: 4},
	{key: "macro2", attr: "mod", param: "mod", neutral: 0.5, modes: 4},
}

// laneModes are the ways a lane moves, in the order of the plugin's labels.
var laneModes = []string{"sync", "free", "trig", "retrig"}

// stepsAttr is the attribute of <sequences> with a voice's drawn steps.
const stepsAttr = "fills"

// seqLengthParam, snapParam, laneLengthParam and laneModeParam name the
// parameters a voice's sequence sets.
func seqLengthParam(voice int) string { return fmt.Sprintf("seqChan%dseqLength", voice) }
func snapParam(voice int) string      { return fmt.Sprintf("seqChan%dlockLength", voice) }
func laneLengthParam(voice int, l *battalionLane) string {
	return fmt.Sprintf("seqChan%d%sSeqLength", voice, l.param)
}
func laneModeParam(voice int, l *battalionLane) string {
	return fmt.Sprintf("seqChan%d%sSeqMode", voice, l.param)
}

// sequenceParam is one parameter a step sequence writes into the state.
type sequenceParam struct {
	id    string
	value float64 // in the state's units
	// set says the song wrote it; otherwise it follows from the values.
	set   bool
	where string // where the song says it, for a message
}

// params lists the parameters the sequence writes, voice by voice.
func (q *stepSequence) params(at string) []sequenceParam {
	var out []sequenceParam
	for _, v := range q.voices {
		vat := fmt.Sprintf("%s/%d", at, v.voice)
		if v.length > 0 {
			// SNAP keeps the drawn sequence as long as the generator's steps;
			// off, it runs for its own length.
			out = append(out,
				sequenceParam{id: seqLengthParam(v.voice), value: float64(v.length - 1), set: v.lengthSet, where: vat + "/length"},
				sequenceParam{id: snapParam(v.voice), value: 0, where: vat + "/length"})
		}
		for _, l := range v.lanes {
			lat := vat + "/" + l.lane.key
			out = append(out,
				sequenceParam{id: laneLengthParam(v.voice, l.lane), value: float64(l.length - 1), set: l.lengthSet, where: lat + "/length"},
				sequenceParam{id: laneModeParam(v.voice, l.lane), value: float64(l.mode), set: l.modeSet, where: lat + "/mode"})
		}
	}
	return out
}

// ErrNoBaseState is returned when the state a step sequence, samples or locks
// are written over has no XML to write them into.
var ErrNoBaseState = errors.New("no saved state to write the sequence, samples and locks over")

// writeBattalionState writes k into a Battalion state (a binary XML block)
// and returns the new block. A parameter in skip is left as the state has it:
// the song sets it by MIDI instead.
func writeBattalionState(state []byte, k *kitState, skip map[string]bool) ([]byte, error) {
	text, err := juce.DecodeBinaryXML(state)
	if err != nil {
		return nil, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromString(text); err != nil {
		return nil, fmt.Errorf("the state's XML: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("the state's XML has no root element")
	}
	seqs := root.SelectElement("sequences")
	if seqs == nil {
		return nil, fmt.Errorf("the state has no <sequences>: it is not UA Battalion's")
	}
	params := map[string]*etree.Element{}
	for _, p := range root.SelectElements("PARAM") {
		params[p.SelectAttrValue("id", "")] = p
	}

	setValues := func(attr string, vals []string) error {
		if seqs.SelectAttr(attr) == nil {
			return fmt.Errorf("the state's <sequences> has no %s", attr)
		}
		seqs.CreateAttr(attr, strings.Join(vals, ","))
		return nil
	}
	if q := k.seq; q != nil {
		for _, v := range q.voices {
			i := v.voice - 1
			if v.steps != nil {
				vals := make([]string, maxSequenceSteps)
				for s := range vals {
					vals[s] = "0"
					if s < len(v.steps) && v.steps[s] {
						vals[s] = "1"
					}
				}
				if err := setValues(fmt.Sprintf("%s%d", stepsAttr, i), vals); err != nil {
					return nil, err
				}
			}
			for _, l := range v.lanes {
				// The values repeat over all of the lane's positions. A lane in
				// SYNC follows the steps whatever its length, so this is what
				// makes a short one repeat; in the other modes the positions past
				// its length are not played.
				vals := make([]string, maxSequenceSteps)
				for s := range vals {
					vals[s] = strconv.FormatFloat(l.values[s%len(l.values)], 'f', -1, 64)
				}
				if err := setValues(fmt.Sprintf("%s%d", l.lane.attr, i), vals); err != nil {
					return nil, err
				}
			}
		}
		for _, sp := range q.params("") {
			if skip[sp.id] {
				continue
			}
			p := params[sp.id]
			if p == nil {
				return nil, fmt.Errorf("the state has no parameter %s", sp.id)
			}
			p.CreateAttr("value", stateNumber(sp.value))
		}
	}

	// A voice's sample is an attribute of the root, url_0 to url_7: a file
	// URL, "" for none.
	for _, s := range k.samples {
		u := s.path
		if u != "" && !s.isURL() {
			u = libraryURL(s.path)
		}
		root.CreateAttr(sampleAttr(s.voice), u)
	}

	// The locks are attributes of <locks>, a parameter's id set to "1".
	if len(k.locks) > 0 {
		locks := root.SelectElement("locks")
		if locks == nil {
			locks = etree.NewElement("locks")
			// Where Battalion writes it: before <sequences>.
			root.InsertChildAt(seqs.Index(), locks)
		}
		for _, id := range k.locks {
			locks.CreateAttr(id, "1")
		}
	}

	out, err := doc.WriteToString()
	if err != nil {
		return nil, fmt.Errorf("write the state's XML: %w", err)
	}
	return juce.EncodeBinaryXML(out), nil
}

// stateNumber writes a whole number the way JUCE writes a parameter's value
// ("15.0").
func stateNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// sampleAttr is the attribute of the state's root with a voice's sample.
func sampleAttr(voice int) string { return fmt.Sprintf("url_%d", voice-1) }

// WritesState reports whether the song writes into the node's saved state:
// its instrument's step sequence, samples or locks (sequence:, samples:,
// locks:).
func (n *NodePlan) WritesState() bool { return n.kit != nil }

// StateWrites says what the song writes into the node's saved state, for a
// message: "its own sequence, 2 samples and 3 locks".
func (n *NodePlan) StateWrites() string {
	if n.kit == nil {
		return ""
	}
	return n.kit.says()
}

// AuthoredState returns the saved state the node is authored with: base (the
// plugin's state the song's sequence, samples and locks are written over, as
// key -> bytes) with them in it. It holds the plugin's XML alone; see the top
// of this file. A base without that XML is ErrNoBaseState.
func (n *NodePlan) AuthoredState(base map[string][]byte) (map[string][]byte, error) {
	if n.kit == nil {
		return base, nil
	}
	xml, ok := base[battalionStateKey]
	if !ok || len(xml) == 0 {
		return nil, ErrNoBaseState
	}
	skip := map[string]bool{}
	for _, pa := range n.Params {
		skip[pa.Key] = true
	}
	state, err := writeBattalionState(xml, n.kit, skip)
	if err != nil {
		return nil, fmt.Errorf("writing into %s's saved state: %w", n.Name, err)
	}
	return map[string][]byte{battalionStateKey: state}, nil
}
