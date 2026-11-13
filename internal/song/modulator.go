package song

// This file is a song's modulators: plugins that move a parameter of a track's
// instrument or effects by themselves, such as an LFO sweeping a filter.
//
// A modulator is a plugin that sends control changes. It sits on its track's
// strip after the effects (an insert that passes the audio through), and the
// session routes its MIDI output to AUM's MIDI Control, through the filter the
// brain's route there has: the control channels alone. The parameter it moves
// is mapped like every parameter the song sets, to a controller on a control
// channel (plan.go); the modulator is told to send exactly that controller on
// exactly that channel, between the two values its depth gives.
//
// The modulator's own routing — its channel, its controller and the lowest and
// highest value it sends — are parameters of the plugin. They are mapped like
// any parameter and sent in every section's state, so they hold from the first
// section on, and are sent again after a preset load replaced them, as every
// parameter of a plugin whose preset changed is (compile.go).
//
// Where a modulator runs, the parameter it moves is its alone: the state does
// not send the parameter, and no scene or ramp may set it there. Where it is
// bypassed, its output goes to channel parkChannel, which MIDI Control does
// not hear, and the parameter holds the value its node declares (or a scene
// gives it); a ramp may move it there.
//
// A bypassed modulator is parked and nothing else: AUM's own bypass of it
// stays off. Measured on the rig, AUM loses the parameter changes that reach
// a node in the same burst as its un-bypass. The routing that sends the
// modulator back to its target, and the rate a scene gives it there, are such
// changes, and the modulator stayed parked. A parked one keeps running, and
// every parameter change reaches it.

import (
	"fmt"
	"math"
	"strings"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// Modulator is one modulator of a track, and how it is set wherever no scene
// says otherwise.
type Modulator struct {
	// ID names the modulator within its track, for scenes and ramps. It
	// defaults to Plugin. A track's effects and modulators share their ids.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Plugin is the staged AUv3 probe id of the modulator, or a name that fits
	// one. It must be a plugin modulatorKinds knows.
	Plugin string `yaml:"plugin" json:"plugin"`

	// The modulator's own preset, bypass and parameters (its wave, its rate).
	// Its routing parameters are the song's to set, from Target and Depth.
	NodeState `yaml:",inline"`

	// Target is the parameter it moves.
	Target ModTarget `yaml:"target" json:"target"`
	// Depth is the lowest and the highest value the parameter moves between,
	// written as a value of the parameter is (a number in its range, or
	// "NN%"). Empty is the parameter's whole range.
	Depth []Value `yaml:"depth,omitempty" json:"depth,omitempty"`
}

// ModTarget names the parameter a modulator moves: one of its track's
// instrument (Effect "") or of one of its effects.
type ModTarget struct {
	Effect string `yaml:"effect,omitempty" json:"effect,omitempty"`
	Param  string `yaml:"param" json:"param"`
}

// ModulatorState is what a scene says about one modulator: its preset, bypass
// and parameters, as for any plugin, and its depth.
type ModulatorState struct {
	NodeState `yaml:",inline"`
	Depth     []Value `yaml:"depth,omitempty" json:"depth,omitempty"`
}

// ModulatorID is the id scenes and ramps address the modulator by: its ID,
// or its plugin.
func (m Modulator) ModulatorID() string {
	if m.ID != "" {
		return m.ID
	}
	return m.Plugin
}

// findModulator returns the position of the modulator with this id, or -1.
func findModulator(mods []Modulator, id string) int {
	for i, m := range mods {
		if m.ModulatorID() == id {
			return i
		}
	}
	return -1
}

// modulatorIDs lists a track's modulator ids for an error message.
func modulatorIDs(mods []Modulator) string {
	if len(mods) == 0 {
		return "none"
	}
	ids := make([]string, len(mods))
	for i, m := range mods {
		ids[i] = m.ModulatorID()
	}
	return strings.Join(ids, ", ")
}

// checkModulators checks a track's modulators as the document has them: an
// id of their own on the strip, a plugin, a target the track has, a depth of
// two values.
func checkModulators(e *errs, at string, t *Track) {
	ids := map[string]bool{}
	for _, fx := range t.Effects {
		ids[fx.EffectID()] = true
	}
	for i, m := range t.Modulators {
		mat := fmt.Sprintf("%s/modulators/%d", at, i)
		if m.Plugin == "" {
			e.addf("%s/plugin: a modulator needs a plugin", mat)
			continue
		}
		id := m.ModulatorID()
		if ids[id] {
			e.addf("%s/id: %q is already an effect or a modulator of %q (give modulators that share a plugin their own ids)", mat, id, t.ID)
		}
		ids[id] = true
		switch {
		case m.Target.Param == "":
			e.addf("%s/target/param: name the parameter the modulator moves", mat)
		case m.Target.Effect != "" && findEffect(t.Effects, m.Target.Effect) < 0:
			e.addf("%s/target/effect: %q has no effect %q (it has %s)", mat, t.ID, m.Target.Effect, effectIDs(t.Effects))
		case m.Target.Effect == "" && t.IsFile():
			e.addf("%s/target: %q plays a file, which has no parameter; name one of its effects", mat, t.ID)
		}
		checkDepth(e, mat+"/depth", m.Depth)
	}
}

// checkDepth checks a depth has its two ends.
func checkDepth(e *errs, at string, depth []Value) {
	if n := len(depth); n != 0 && n != 2 {
		e.addf("%s: two values, the lowest and the highest the parameter moves between, not %d", at, n)
	}
}

// parkChannel is where a bypassed modulator sends: channel 1, which is never
// a control channel (a session's tracks start there), so MIDI Control does not
// hear it.
const parkChannel = 1

// modulatorKind is a plugin the song can host as a modulator: where its MIDI
// output is and which of its parameters route it.
type modulatorKind struct {
	Name      string
	Component device.ProbeComponent
	// Port is the name the plugin gives its MIDI output, which AUM keys the
	// route from it by (aum.MIDIEndpoint.Port).
	Port string
	// The parameters that route the output: the channel it sends on, the
	// controller it sends, and the lowest and highest value it sends.
	Channel, CC, Min, Max routeParam
}

// routeParam is one routing parameter of a modulator: a 0..1 parameter the
// plugin quantizes to the integers Lo..Hi.
type routeParam struct {
	Key    string
	Lo, Hi float64
}

// wire is the CC value that sets the parameter to v. AUM maps a CC's 0..127
// onto the parameter's 0..1, and the plugin turns that into Lo..Hi; the value
// is the smallest CC that lands at or above v, less than one CC step above
// it. A step is (Hi-Lo)/127 of a unit, under a half for every routing
// parameter, so v is what the plugin makes of it whether it rounds or
// truncates.
func (rp routeParam) wire(v int) int {
	return clamp7(int(math.Ceil((float64(v)-rp.Lo)*127/(rp.Hi-rp.Lo) - 1e-9)))
}

// keys lists the routing parameters' keys.
func (k *modulatorKind) keys() []string {
	return []string{k.Channel.Key, k.CC.Key, k.Min.Key, k.Max.Key}
}

// modulatorKinds are the plugins a song can host as modulators.
//
// K-Devices LFOH is a MIDI LFO built with JUCE. Its saved state holds every
// parameter twice, as the 0..1 the host sees and as the plugin's own value,
// which gives its ranges: midi_ch 1..16 (channel 2 is 1/15), midi_min and
// midi_max 0..127 (16 is 16/127), and midi_cc the controller, 0..127. Its MIDI
// output is called "MIDI Out", as AUM keys it in a session that routes it.
var modulatorKinds = []modulatorKind{{
	Name:      "LFOH",
	Component: device.ProbeComponent{Type: "aumf", Subtype: "KLFH", Manufacturer: "KDev"},
	Port:      "MIDI Out",
	Channel:   routeParam{Key: "midi_ch", Lo: 1, Hi: 16},
	CC:        routeParam{Key: "midi_cc", Lo: 0, Hi: 127},
	Min:       routeParam{Key: "midi_min", Lo: 0, Hi: 127},
	Max:       routeParam{Key: "midi_max", Lo: 0, Hi: 127},
}}

// modulatorKindOf returns the modulator a probe dump is, nil for a plugin
// that is none.
func modulatorKindOf(dump device.ProbeDump) *modulatorKind {
	for i := range modulatorKinds {
		if aum.ComponentMatches(dump.Component, modulatorKinds[i].Component) {
			return &modulatorKinds[i]
		}
	}
	return nil
}

// modulatorNames lists the plugins a song can host as modulators.
func modulatorNames() string {
	names := make([]string, len(modulatorKinds))
	for i, k := range modulatorKinds {
		names[i] = k.Name
	}
	return strings.Join(names, ", ")
}

// ModPlan is what a modulator node moves, and how.
type ModPlan struct {
	// Target is the parameter it moves, as a song addresses it ("pad
	// Cutoff", "pad/reverb Mix"), and At the address the session maps it at:
	// the channel and controller the modulator sends on.
	Target string     `json:"target"`
	At     Assignment `json:"at"`
	// Depth is the lowest and the highest CC value it sends where no scene
	// gives it another depth.
	Depth [2]int `json:"depth"`

	kind *modulatorKind
	// target is the node of the parameter it moves and key that parameter's
	// key; routes are the keys of the modulator's own routing parameters, in
	// the order kind.keys gives them.
	target *NodePlan
	key    string
	routes []string
	// at is where the document declares the modulator.
	at string
}

// Port is the name of the modulator's MIDI output.
func (m *ModPlan) Port() string { return m.kind.Port }

// targetParam is the plan of the parameter the modulator moves.
func (m *ModPlan) targetParam() *ParamPlan {
	for i := range m.target.Params {
		if m.target.Params[i].Key == m.key {
			return &m.target.Params[i]
		}
	}
	return nil
}

// targetOf is the control the modulator moves.
func (m *ModPlan) targetOf() Target {
	return Target{Track: m.target.Track, Effect: m.target.Effect, Kind: ControlParam, Param: m.key}
}

// depth converts a depth as written into the CC values the target's mapping
// takes for its ends; an empty one is the whole range.
func (m *ModPlan) depth(d []Value) ([2]int, error) {
	if len(d) == 0 {
		return [2]int{0, 127}, nil
	}
	if len(d) != 2 {
		return [2]int{}, fmt.Errorf("two values, the lowest and the highest the parameter moves between, not %d", len(d))
	}
	pp := m.targetParam()
	var out [2]int
	for i, v := range d {
		w, err := paramWire(pp.param, v)
		if err != nil {
			return out, err
		}
		out[i] = w
	}
	if out[0] >= out[1] {
		return out, fmt.Errorf("%v..%v is %d..%d as the controller sends it; the first value is the lowest and must lie below the highest", d[0], d[1], out[0], out[1])
	}
	return out, nil
}

// routing is the value of each of the modulator's routing parameters, by key:
// its target's channel and controller, or parkChannel when bypassed, and the
// two ends of depth.
func (m *ModPlan) routing(depth [2]int, bypassed bool) map[string]int {
	at := m.targetParam().At
	ch := at.Channel
	if bypassed {
		ch = parkChannel
	}
	k := m.kind
	vals := []int{k.Channel.wire(ch), k.CC.wire(at.Number), k.Min.wire(depth[0]), k.Max.wire(depth[1])}
	out := make(map[string]int, len(vals))
	for i, key := range m.routes {
		out[key] = vals[i]
	}
	return out
}

// isRoute reports whether a parameter key is one of the modulator's routing
// parameters.
func (m *ModPlan) isRoute(key string) bool {
	for _, k := range m.routes {
		if k == key {
			return true
		}
	}
	return false
}

// modulators lists the plan's modulator nodes in plan order.
func (p *Plan) modulators() []*NodePlan {
	var out []*NodePlan
	for _, n := range p.allNodes() {
		if n.Mod != nil {
			out = append(out, n)
		}
	}
	return out
}

// modulatorBypassed reports whether a modulator is bypassed in a section: its
// own declaration overlaid with the section's scenes.
func (p *Plan) modulatorBypassed(sec Section, n *NodePlan) bool {
	off := n.home.Bypass != nil && *n.home.Bypass
	for _, sn := range sec.Scene {
		if ms, ok := p.song.Scenes[sn][n.Track].Modulators[n.Effect]; ok && ms.Bypass != nil {
			off = *ms.Bypass
		}
	}
	return off
}

// checkModulatorKinds checks every plugin against what it is hosted as: a
// modulator is a plugin modulatorKinds knows, and an effect none. s is a valid
// song whose plugins resolved.
func (p *Plan) checkModulatorKinds(e *errs) {
	s := p.song
	for _, n := range p.modulators() {
		if n.Mod.kind = modulatorKindOf(n.dump); n.Mod.kind == nil {
			e.addf("%s/plugin: %s is not a modulator a song can host: it sends no control change the session knows how to route (modulators: %s)", n.Mod.at, n.Name, modulatorNames())
		}
	}
	for i, t := range s.Tracks {
		for j, fx := range t.Effects {
			if n := p.nodes[t.ID+"/"+fx.EffectID()]; n != nil && modulatorKindOf(n.dump) != nil {
				e.addf("/tracks/%d/effects/%d/plugin: %s is a modulator; declare it under the track's modulators, with the parameter it moves as its target", i, j, n.Name)
			}
		}
	}
	for j, fx := range s.Master {
		if n := p.nodes[MasterTrack+"/"+fx.EffectID()]; n != nil && modulatorKindOf(n.dump) != nil {
			e.addf("/master/%d/plugin: %s is a modulator, which moves a parameter of a track's plugins; declare it under that track's modulators", j, n.Name)
		}
	}
}

// resolveModulators is the part of resolve that is the modulators': their
// routing parameters, their targets and depths, and that nothing else sets
// what they set. s is a valid song whose nodes resolved, each modulator to a
// plugin that is one (checkModulatorKinds).
func (p *Plan) resolveModulators(e *errs) {
	s := p.song
	targets := map[string]string{} // a target, as track/effect/key -> the modulator that moves it
	for _, n := range p.modulators() {
		m := n.Mod
		kind := m.kind
		for _, key := range kind.keys() {
			pp, err := n.param(key)
			if err != nil {
				e.addf("%s/plugin: the staged probe of %s lacks its routing parameter %s: %v", m.at, n.Name, key, err)
				continue
			}
			m.routes = append(m.routes, pp.Key)
		}
		if len(m.routes) != len(kind.keys()) {
			continue
		}

		var spec Modulator
		for _, t := range s.Tracks {
			if t.ID == n.Track {
				spec = t.Modulators[findModulator(t.Modulators, n.Effect)]
			}
		}
		m.target = p.nodes[n.Track+"/"+spec.Target.Effect]
		pp, err := m.target.param(spec.Target.Param)
		if err != nil {
			e.addf("%s/target/param: %v", m.at, err)
			continue
		}
		m.key = pp.Key
		m.Target = Target{Track: n.Track, Effect: spec.Target.Effect, Kind: ControlParam, Param: pp.Name}.String()
		where := n.Track + "/" + spec.Target.Effect + "/" + pp.Key
		if other, taken := targets[where]; taken {
			e.addf("%s/target: %s is already moved by %s; a parameter has one modulator", m.at, m.Target, other)
		}
		targets[where] = nodeLabel(n)
		if m.Depth, err = m.depth(spec.Depth); err != nil {
			e.addf("%s/depth: %v", m.at, err)
		}
	}
	if len(e.list) > 0 {
		return
	}

	// The routing is the song's to set: no declaration, scene or ramp sets it.
	route := func(n *NodePlan, name string) bool {
		pp, err := n.param(name)
		return err == nil && n.Mod != nil && n.Mod.isRoute(pp.Key)
	}
	routeErr := func(where, name string) {
		e.addf("%s: %s is the modulator's routing, which the song sets from its target and depth", where, name)
	}
	for _, n := range p.modulators() {
		for _, name := range sortedKeys(n.home.Params) {
			if route(n, name) {
				routeErr(n.Mod.at+"/params/"+name, name)
			}
		}
	}
	for _, name := range sortedKeys(s.Scenes) {
		for _, tid := range sortedKeys(s.Scenes[name]) {
			ms := s.Scenes[name][tid].Modulators
			for _, mid := range sortedKeys(ms) {
				n := p.nodes[tid+"/"+mid]
				if n == nil || n.Mod == nil {
					continue
				}
				where := fmt.Sprintf("/scenes/%s/%s/modulators/%s", name, tid, mid)
				for _, pn := range sortedKeys(ms[mid].Params) {
					if route(n, pn) {
						routeErr(where+"/params/"+pn, pn)
					}
				}
				if _, err := n.Mod.depth(ms[mid].Depth); err != nil {
					e.addf("%s/depth: %v", where, err)
				}
			}
		}
	}

	// Where a modulator runs, the parameter it moves is its alone; where it
	// is bypassed, the parameter holds the value its node declares.
	sets := func(n *NodePlan, ns NodeState, key string) bool {
		for pn := range ns.Params {
			if pp, err := n.param(pn); err == nil && pp.Key == key {
				return true
			}
		}
		return false
	}
	for _, n := range p.modulators() {
		m := n.Mod
		declared := sets(m.target, m.target.home, m.key)
		bypassed := ""
		if n.home.Bypass != nil && *n.home.Bypass {
			bypassed = "it declares itself bypassed"
		}
		for _, name := range sortedKeys(s.Scenes) {
			if ms, ok := s.Scenes[name][n.Track].Modulators[n.Effect]; ok && bypassed == "" && ms.Bypass != nil && *ms.Bypass {
				bypassed = fmt.Sprintf("the scene %q bypasses it", name)
			}
		}
		if bypassed != "" && !declared {
			e.addf("%s/target: %s moves %s, and %s; declare %s on its plugin, the value it holds where the modulator is bypassed", m.at, nodeLabel(n), m.Target, bypassed, m.targetParam().Name)
		}
		for _, name := range sortedKeys(s.Sections) {
			sec := s.Sections[name]
			if p.modulatorBypassed(sec, n) {
				continue
			}
			for _, sn := range sec.Scene {
				ts := s.Scenes[sn][n.Track]
				ns := ts.NodeState
				if m.target.Effect != "" {
					ns = ts.Effects[m.target.Effect]
				}
				if sets(m.target, ns, m.key) {
					e.addf("/sections/%s/scene: the scene %q sets %s, which %s moves in this section; bypass the modulator in this section, or leave the parameter to it", name, sn, m.Target, nodeLabel(n))
				}
			}
			for i, r := range sec.Automation {
				if r.Track != n.Track || r.Modulator != "" || r.Effect != m.target.Effect || r.fader() {
					continue
				}
				if pp, err := m.target.param(r.Param); err == nil && pp.Key == m.key {
					e.addf("/sections/%s/automation/%d: %s moves %s in this section; a ramp of it needs the modulator bypassed there (or ramp the modulator's own parameters)", name, i, nodeLabel(n), m.Target)
				}
			}
		}
	}
	for _, name := range sortedKeys(s.Sections) {
		for i, r := range s.Sections[name].Automation {
			if r.Modulator == "" {
				continue
			}
			if n := p.nodes[r.Track+"/"+r.Modulator]; n != nil && route(n, r.Param) {
				routeErr(fmt.Sprintf("/sections/%s/automation/%d/param", name, i), r.Param)
			}
		}
	}
}

// fader reports whether the ramp moves its track's fader.
func (r Ramp) fader() bool {
	return r.Effect == "" && r.Modulator == "" && strings.EqualFold(r.Param, "level")
}

// node is the id of the node whose parameter the ramp moves: an effect, a
// modulator, or "" for the instrument.
func (r Ramp) node() string {
	if r.Modulator != "" {
		return r.Modulator
	}
	return r.Effect
}
