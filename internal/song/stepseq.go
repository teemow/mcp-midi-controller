package song

// This file is a drum machine's own step sequence as a song writes it: per
// voice, the steps drawn by hand and the lanes of values under them, each lane
// with a length and a mode of its own. They are not parameters. They live in
// the plugin's saved state, which the song's session carries; battalion.go
// writes them for UA Battalion. A scene cannot change a saved state, so the
// sequence is the same in every section: only the lanes' lengths and modes,
// which are parameters too, can be moved by params, scenes and ramps.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// StepSequence is an instrument's own step sequence by voice, "1" to "8".
type StepSequence map[string]VoiceSequence

// VoiceSequence is one voice's sequence: the steps drawn by hand, the length
// they run for, and the lanes under them.
type VoiceSequence struct {
	// Steps is a drum grid: "x" a step, "." none, "|" a bar line of sixteen
	// steps (in 4/4). Steps not written are empty.
	Steps string `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Length is how many steps the drawn sequence runs for (1..64). 0 takes
	// the number of steps written. Writing it switches the voice's SNAP off,
	// which would keep the sequence as long as the generator's steps.
	Length int `yaml:"length,omitempty" json:"length,omitempty"`

	Vel    *Lane `yaml:"vel,omitempty" json:"vel,omitempty"`
	Shift  *Lane `yaml:"shift,omitempty" json:"shift,omitempty"`
	Pitch  *Lane `yaml:"pitch,omitempty" json:"pitch,omitempty"`
	Ratch  *Lane `yaml:"ratch,omitempty" json:"ratch,omitempty"`
	Env    *Lane `yaml:"env,omitempty" json:"env,omitempty"`
	Pan    *Lane `yaml:"pan,omitempty" json:"pan,omitempty"`
	Var    *Lane `yaml:"var,omitempty" json:"var,omitempty"`
	Prob   *Lane `yaml:"prob,omitempty" json:"prob,omitempty"`
	Sends  *Lane `yaml:"sends,omitempty" json:"sends,omitempty"`
	Macro2 *Lane `yaml:"macro2,omitempty" json:"macro2,omitempty"`
}

// lanes lists the voice's lanes in the order of battalionLanes.
func (v *VoiceSequence) lanes() []*Lane {
	return []*Lane{v.Vel, v.Shift, v.Pitch, v.Ratch, v.Env, v.Pan, v.Var, v.Prob, v.Sends, v.Macro2}
}

// Lane is one lane of a voice: a value per step from 0 to 1, "." for the
// lane's neutral value. It is written as its values alone, or as
// {values, length, mode}.
type Lane struct {
	// Grid is the values as a grid line: separated by spaces, with "|" bar
	// lines as in a drum grid. List is the values as a list. One of them is
	// set.
	Grid string
	List []LaneValue
	// Length is how many steps the lane's values repeat after (1..64). 0
	// takes the number of values written.
	Length int
	// Mode is how the lane moves: "sync" with the steps (the default; its
	// values repeat from the sequence's start), "free" on its own, "trig"
	// only when the voice fires, "retrig" when it fires and back to its start
	// with the steps.
	Mode string
}

// LaneValue is one step of a lane: a value, or the lane's neutral value.
type LaneValue struct {
	Value   float64
	Neutral bool
}

// UnmarshalYAML reads a lane: a grid line, a list, or {values, length, mode}.
func (l *Lane) UnmarshalYAML(n *yaml.Node) error {
	*l = Lane{}
	if n.Kind != yaml.MappingNode {
		return l.readValues(n)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "values":
			if err := l.readValues(v); err != nil {
				return err
			}
		case "length":
			if err := v.Decode(&l.Length); err != nil {
				return fmt.Errorf("line %d: a lane's length is a number of steps", v.Line)
			}
		case "mode":
			if err := v.Decode(&l.Mode); err != nil {
				return fmt.Errorf("line %d: a lane's mode is sync, free, trig or retrig", v.Line)
			}
		default:
			return fmt.Errorf("line %d: a lane has values, length and mode, not %q", k.Line, k.Value)
		}
	}
	return nil
}

// readValues reads a lane's values: a grid line or a list.
func (l *Lane) readValues(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		l.Grid = n.Value
		return nil
	case yaml.SequenceNode:
		l.List = make([]LaneValue, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: a lane's values are numbers or \".\"", c.Line)
			}
			v, err := parseLaneValue(c.Value)
			if err != nil {
				return fmt.Errorf("line %d: %w", c.Line, err)
			}
			l.List = append(l.List, v)
		}
		return nil
	}
	return fmt.Errorf("line %d: a lane is its values (a grid line or a list) or {values, length, mode}", n.Line)
}

// values renders the lane's values the way they were written.
func (l Lane) values() any {
	if l.List == nil {
		return l.Grid
	}
	out := make([]any, len(l.List))
	for i, v := range l.List {
		if v.Neutral {
			out[i] = "."
		} else {
			out[i] = v.Value
		}
	}
	return out
}

// laneFull is a lane written with its length or mode.
type laneFull struct {
	Values any    `yaml:"values" json:"values"`
	Length int    `yaml:"length,omitempty" json:"length,omitempty"`
	Mode   string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// shape is the lane in the shape it is read in: its values alone, or with
// its length and mode.
func (l Lane) shape() any {
	if l.Length == 0 && l.Mode == "" {
		return l.values()
	}
	return laneFull{Values: l.values(), Length: l.Length, Mode: l.Mode}
}

// MarshalYAML writes the lane back in the shape it is read in.
func (l Lane) MarshalYAML() (any, error) { return l.shape(), nil }

// MarshalJSON writes the lane as MarshalYAML does.
func (l Lane) MarshalJSON() ([]byte, error) { return json.Marshal(l.shape()) }

// parseLaneValue reads one value of a lane: a number from 0 to 1, or "."
// for the lane's neutral value.
func parseLaneValue(s string) (LaneValue, error) {
	s = strings.TrimSpace(s)
	if s == "." {
		return LaneValue{Neutral: true}, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return LaneValue{}, fmt.Errorf("%q is not a lane value (a number from 0 to 1, or \".\" for the lane's neutral value)", s)
	}
	if v < 0 || v > 1 {
		return LaneValue{}, fmt.Errorf("%g is not within 0..1", v)
	}
	return LaneValue{Value: v}, nil
}

// maxSequenceSteps is how many steps a voice's sequence and each of its lanes
// hold.
const maxSequenceSteps = 64

// sequenceVoices is how many voices a step sequence has.
const sequenceVoices = 8

// sequenceStep is the length of a step of the plugin's sequencer, as it runs
// unless its parameters say otherwise: a sixteenth note.
const sequenceStep = PPQ / 4

// stepSequence is a checked step sequence, ready to be written into a state.
type stepSequence struct {
	voices []voiceSteps // by voice
}

// voiceSteps is one voice's checked sequence.
type voiceSteps struct {
	voice int    // 1-based
	steps []bool // nil when the song draws none
	// length is the drawn sequence's length in steps, 0 to leave it as the
	// state has it; lengthSet says the song wrote it rather than the steps.
	length    int
	lengthSet bool
	lanes     []laneSteps
}

// laneSteps is one checked lane.
type laneSteps struct {
	lane               *battalionLane
	values             []float64 // one per step of the lane, neutral filled in
	length, mode       int
	lengthSet, modeSet bool
}

// checkStepSequence checks a step sequence and resolves it. at is where it
// sits in the document; m is the song's meter, which bar lines are checked
// against. It returns nil after reporting a problem.
func checkStepSequence(e *errs, at string, seq StepSequence, m meter) *stepSequence {
	before := len(e.list)
	stepsPerBar := 0
	if m.barTicks%sequenceStep == 0 {
		stepsPerBar = int(m.barTicks / sequenceStep)
	}
	keys := make([]string, 0, len(seq))
	for k := range seq {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return voiceOrder(keys[i]) < voiceOrder(keys[j]) })
	out := &stepSequence{}
	for _, key := range keys {
		v := seq[key]
		vat := at + "/" + key
		voice, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || voice < 1 || voice > sequenceVoices {
			e.addf("%s: %q is not a voice (1..%d)", vat, key, sequenceVoices)
			continue
		}
		vs := voiceSteps{voice: voice}
		said := false
		if v.Steps != "" {
			said = true
			steps, err := parseSequenceSteps(v.Steps, stepsPerBar)
			switch {
			case err != nil:
				e.addf("%s/steps: %v", vat, err)
			case len(steps) == 0:
				e.addf("%s/steps: no steps (write \"x\" for a step and \".\" for none)", vat)
			case len(steps) > maxSequenceSteps:
				e.addf("%s/steps: %d steps, a sequence holds %d", vat, len(steps), maxSequenceSteps)
			default:
				vs.steps, vs.length = steps, len(steps)
			}
		}
		if v.Length != 0 {
			said = true
			switch {
			case v.Length < 1 || v.Length > maxSequenceSteps:
				e.addf("%s/length: %d is not a length (1..%d steps)", vat, v.Length, maxSequenceSteps)
			case v.Length < len(vs.steps):
				e.addf("%s/length: %d, but %d steps are written; the ones past the length would never play", vat, v.Length, len(vs.steps))
			default:
				vs.length, vs.lengthSet = v.Length, true
			}
		}
		for i, l := range v.lanes() {
			if l == nil {
				continue
			}
			said = true
			lane := &battalionLanes[i]
			if ls, ok := checkLane(e, vat+"/"+lane.key, lane, *l, stepsPerBar); ok {
				vs.lanes = append(vs.lanes, ls)
			}
		}
		if !said {
			e.addf("%s: the voice's sequence says nothing (give it steps, a length or a lane)", vat)
			continue
		}
		out.voices = append(out.voices, vs)
	}
	if len(e.list) > before {
		return nil
	}
	return out
}

// voiceOrder sorts voice keys by number, anything else after them.
func voiceOrder(key string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(key)); err == nil {
		return n
	}
	return 1 << 20
}

// checkLane checks one lane and resolves its values.
func checkLane(e *errs, at string, lane *battalionLane, l Lane, stepsPerBar int) (laneSteps, bool) {
	ok := true
	vals := l.List
	if vals == nil {
		var err error
		if vals, err = parseLaneGrid(l.Grid, stepsPerBar); err != nil {
			e.addf("%s: %v", at, err)
			return laneSteps{}, false
		}
	}
	switch {
	case len(vals) == 0:
		e.addf("%s: no values (a number from 0 to 1 per step, \".\" for %g)", at, lane.neutral)
		ok = false
	case len(vals) > maxSequenceSteps:
		e.addf("%s: %d values, a lane holds %d", at, len(vals), maxSequenceSteps)
		ok = false
	}
	ls := laneSteps{lane: lane, length: len(vals)}
	if l.Length != 0 {
		switch {
		case l.Length < 1 || l.Length > maxSequenceSteps:
			e.addf("%s/length: %d is not a length (1..%d steps)", at, l.Length, maxSequenceSteps)
			ok = false
		case l.Length < len(vals):
			e.addf("%s/length: %d, but %d values are written; the ones past the length would never play", at, l.Length, len(vals))
			ok = false
		default:
			ls.length, ls.lengthSet = l.Length, true
		}
	}
	if l.Mode != "" {
		mode := -1
		for i, name := range laneModes[:lane.modes] {
			if strings.EqualFold(strings.TrimSpace(l.Mode), name) {
				mode = i
			}
		}
		if mode < 0 {
			e.addf("%s/mode: %q is not one of %s", at, l.Mode, strings.Join(laneModes[:lane.modes], ", "))
			ok = false
		}
		ls.mode, ls.modeSet = mode, true
	}
	if !ok {
		return laneSteps{}, false
	}
	ls.values = make([]float64, ls.length)
	for i := range ls.values {
		ls.values[i] = lane.neutral
		if i < len(vals) && !vals[i].Neutral {
			ls.values[i] = vals[i].Value
		}
	}
	return ls, true
}

// parseSequenceSteps reads a voice's drawn steps: "x" a step, "." or "-"
// none, "|" a bar line.
func parseSequenceSteps(grid string, stepsPerBar int) ([]bool, error) {
	var steps []bool
	bars := barChecker{stepsPerBar: stepsPerBar}
	for i := 0; i < len(grid); i++ {
		switch c := grid[i]; c {
		case ' ', '\t', '\n', '\r':
		case '|':
			if err := bars.line(); err != nil {
				return nil, err
			}
		case 'x':
			steps = append(steps, true)
			bars.inBar++
		case '.', '-':
			steps = append(steps, false)
			bars.inBar++
		default:
			return nil, fmt.Errorf("step %d: %q is not a step (\"x\" a step, \".\" none; a step's level is the vel lane's)", len(steps)+1, string(c))
		}
	}
	if err := bars.end(); err != nil {
		return nil, err
	}
	return steps, nil
}

// parseLaneGrid reads a lane written as a grid line: values separated by
// spaces, "." for the neutral value, "|" a bar line.
func parseLaneGrid(grid string, stepsPerBar int) ([]LaneValue, error) {
	var vals []LaneValue
	bars := barChecker{stepsPerBar: stepsPerBar}
	for _, tok := range strings.Fields(strings.ReplaceAll(grid, "|", " | ")) {
		if tok == "|" {
			if err := bars.line(); err != nil {
				return nil, err
			}
			continue
		}
		v, err := parseLaneValue(tok)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", len(vals)+1, err)
		}
		vals = append(vals, v)
		bars.inBar++
	}
	if err := bars.end(); err != nil {
		return nil, err
	}
	return vals, nil
}

// fingerprint identifies what the sequence writes.
func (q *stepSequence) fingerprint() string {
	h := fnv.New64a()
	for _, v := range q.voices {
		_, _ = fmt.Fprintf(h, "v%d:%v:%d:%v|", v.voice, v.steps, v.length, v.lengthSet)
		for _, l := range v.lanes {
			_, _ = fmt.Fprintf(h, "%s:%v:%d:%d:%v:%v|", l.lane.key, l.values, l.length, l.mode, l.lengthSet, l.modeSet)
		}
	}
	return fmt.Sprintf("%016x", h.Sum64())
}
