package song

// This file plans a song's AUM session: which strip and slot every plugin
// lands on, which MIDI channel every track plays on, and which MIDI message
// sets every control the song uses. The plan is the one place those numbers
// are decided, so the session authored from it (session.go) and the MIDI
// compiled against it (compile.go) agree by construction.
//
// Channel layout. Tracks play on the low MIDI channels, one each. Everything
// above them is control, which the session's MIDI matrix keeps apart: an
// instrument only hears its own channel and AUM's MIDI Control only hears the
// control channels, so a note never fires a mapping and a control message never
// reaches a synth.
//
//	1..N    one per track (notes), and the control channels a track's
//	        instrument also hears (control notes)
//	N+1..14 node bypasses and parameters, filled from 14 downwards
//	15      mixer strips and transport (the convention's CCs), preset loads (PC)
//	16      left to the rig: tap toggles and session switching

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// Catalog is the plugins a song may host: staged AUv3 probe dumps by probe id.
type Catalog map[string]device.ProbeDump

// Resolve finds the plugin a name stands for: a probe id as it is, else the
// one plugin whose display name is the name, else the one whose name or id
// contains it (ignoring case). A name that fits several plugins is an error
// that lists them.
func (c Catalog) Resolve(name string) (string, error) {
	if _, ok := c[name]; ok {
		return name, nil
	}
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return "", fmt.Errorf("name the plugin")
	}
	var exact, partial []string
	for id, d := range c {
		switch dn := strings.ToLower(d.Name); {
		case dn == want || strings.ToLower(id) == want:
			exact = append(exact, id)
		case strings.Contains(dn, want) || strings.Contains(strings.ToLower(id), want):
			partial = append(partial, id)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = partial
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no staged AUv3 probe %q%s", name, suggestPlugins(c, name))
	}
	sort.Strings(hits)
	for i, id := range hits {
		hits[i] = id + " (" + c[id].Name + ")"
	}
	return "", fmt.Errorf("%q fits %d plugins: %s; use the id", name, len(hits), strings.Join(hits, ", "))
}

const (
	// GlobalChannel carries the mixer strips, the transport and the preset
	// loads. It is not 16: the rig's session-switch Program Changes live there
	// and AUM fires them whatever session is loaded.
	GlobalChannel = 15
	// poolTopChannel is where bypass and parameter mappings start; they fill
	// downwards towards the tracks.
	poolTopChannel = 14
)

// MsgKind is the kind of MIDI message a control is set with.
type MsgKind int

const (
	MsgCC MsgKind = iota
	MsgPC
)

// Assignment is the MIDI address of one control: a CC or a Program Change on a
// channel (1-based).
type Assignment struct {
	Kind    MsgKind `json:"-"`
	Channel int     `json:"channel"`
	Number  int     `json:"number"`
}

// String renders the address for reports ("CC 22 ch15").
func (a Assignment) String() string {
	kind := "CC"
	if a.Kind == MsgPC {
		kind = "PC"
	}
	return fmt.Sprintf("%s %d ch%d", kind, a.Number, a.Channel)
}

// ControlKind is what a control sets.
type ControlKind int

const (
	ControlPreset ControlKind = iota // a node's preset
	ControlBypass                    // a node's bypass
	ControlParam                     // a node parameter
	ControlLevel                     // a strip's fader
	ControlMute                      // a strip's mute
)

// Target names one control of the song: a strip control of a track, or a
// control of a node (Effect "" is the track's instrument).
type Target struct {
	Track  string
	Effect string
	Kind   ControlKind
	Param  string // the parameter's key (ControlParam only)
}

// String renders the target the way a song addresses it.
func (t Target) String() string {
	node := t.Track
	if t.Effect != "" {
		node += "/" + t.Effect
	}
	switch t.Kind {
	case ControlPreset:
		return node + " preset"
	case ControlBypass:
		return node + " bypass"
	case ControlLevel:
		return node + " level"
	case ControlMute:
		return node + " mute"
	default:
		return node + " " + t.Param
	}
}

// NodePlan is one hosted plugin: where it sits and how it is controlled.
type NodePlan struct {
	Track  string `json:"track"`
	Effect string `json:"effect,omitempty"`
	Plugin string `json:"plugin"`
	Name   string `json:"name"`
	// Strip and Slot are AUM's 0-based channel index and chain slot.
	Strip int `json:"strip"`
	Slot  int `json:"slot"`
	// MIDI routes the track's notes to the node.
	MIDI bool `json:"midi,omitempty"`

	Bypass  Assignment   `json:"-"`
	Presets []PresetPlan `json:"presets,omitempty"`
	Params  []ParamPlan  `json:"params,omitempty"`

	dump device.ProbeDump
	home NodeState // the plugin's own declaration
	// refs is where the session keeps each of the dump's parameters, worked
	// out when the first one is asked for.
	refs []aum.ParamRef
}

// PresetPlan is one factory preset the session can load into a node: the
// Program Change that loads it, on Channel (1-based).
type PresetPlan struct {
	Name    string `json:"name"`
	Number  int    `json:"number"`
	Program int    `json:"program"`
	Channel int    `json:"channel"`
}

// event is the message that loads the preset.
func (pr PresetPlan) event() Event {
	return Event{Kind: Program, Channel: pr.Channel, Data1: pr.Program}
}

// ParamPlan is one parameter the song sets on a node.
type ParamPlan struct {
	// Key names the parameter within its plugin (its identifier) and Name is
	// what the plugin calls it.
	Key  string     `json:"key"`
	Name string     `json:"name"`
	At   Assignment `json:"-"`

	param device.ProbeParam
	// ref is where the session keeps the parameter's mapping: below the
	// node's slot, inside one collection per group of the plugin's parameter
	// tree.
	ref aum.ParamRef
}

// TrackPlan is one track's strip.
type TrackPlan struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel int    `json:"channel"`
	// ControlChannels are the further channels the track's instrument hears
	// (Track.ControlChannels), in order.
	ControlChannels []int `json:"control_channels,omitempty"`
	Strip           int   `json:"strip"`
	Drums           bool  `json:"drums,omitempty"`

	Level Assignment `json:"-"`
	Mute  Assignment `json:"-"`
	// Nodes is the instrument followed by the effects.
	Nodes []*NodePlan `json:"nodes"`

	homeLevel float64
	homeMute  bool
}

// Hears lists the MIDI channels the track's instrument hears: its own, then
// its control channels.
func (tp *TrackPlan) Hears() []int {
	return append([]int{tp.Channel}, tp.ControlChannels...)
}

// Plan is a song's session layout and control map.
type Plan struct {
	Tracks []*TrackPlan `json:"tracks"`
	// Master is the master strip's effects.
	Master      []*NodePlan `json:"master,omitempty"`
	MasterStrip int         `json:"master_strip"`
	// ControlChannels are the MIDI channels AUM's MIDI Control listens on.
	ControlChannels []int `json:"control_channels"`
	// Transport is the transport actions the session maps, by AUM target.
	Transport map[string]Assignment `json:"-"`
	// SequencerBypass switches the session's sequencer off (127) and on (0),
	// in a session that hosts one.
	SequencerBypass Assignment `json:"-"`
	// PresetsUnmapped is how many factory presets a session that maps them
	// all (session presets: all) had no Program Change left for.
	PresetsUnmapped int `json:"presets_unmapped,omitempty"`

	song  *Song
	nodes map[string]*NodePlan // "track/effect" -> node

	// sounds is the catalogue a calibrated song is compiled against and
	// notes what compiling it found worth saying. See calibrate.go.
	sounds Sounds
	notes  map[string]bool
}

// BrainStrip is the strip the brain sits on; the tracks follow it.
const BrainStrip = 0

// sequencerBypassCC is the controller on the global channel that bypasses the
// session's sequencer. It sits with the transport's (102..105, 108).
const sequencerBypassCC = 106

// firstInsertSlot is the slot of an audio strip's first insert: slot 0 is the
// strip's source and slot 1 its output node.
const firstInsertSlot = 2

// midiReservedCC reports the controllers MIDI itself gives a meaning the pool
// must not borrow: bank select, data entry, the (N)RPN selectors and the
// channel-mode messages.
func midiReservedCC(cc int) bool {
	switch cc {
	case 0, 6, 32, 38, 96, 97, 98, 99, 100, 101:
		return true
	}
	return cc >= 120
}

// ccPool hands out free controllers, channel by channel, downwards from
// poolTopChannel to just above the tracks.
type ccPool struct {
	channel, lowest int
	next            int
}

func (p *ccPool) take() (Assignment, error) {
	for p.channel >= p.lowest {
		for p.next <= 127 {
			cc := p.next
			p.next++
			if !midiReservedCC(cc) {
				return Assignment{Kind: MsgCC, Channel: p.channel, Number: cc}, nil
			}
		}
		p.channel--
		p.next = 0
	}
	return Assignment{}, fmt.Errorf("the song sets more controls than the session's control channels hold; use fewer tracks or set fewer parameters")
}

// PlanOptions is what a plan is made with beyond the song and the plugins.
type PlanOptions struct {
	// Sounds is the rig's sound catalogue, which a song with calibrate: true
	// is compiled against. nil knows no preset.
	Sounds Sounds
}

// NewPlan lays a song out against the plugins in cat. It resolves every
// plugin, preset and parameter the song names and gives every control its
// MIDI address; a name that does not resolve is reported with what would.
func NewPlan(s *Song, cat Catalog) (*Plan, error) {
	return NewPlanWith(s, cat, PlanOptions{})
}

// NewPlanWith is NewPlan with options.
func NewPlanWith(s *Song, cat Catalog, o PlanOptions) (*Plan, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var e errs
	p := &Plan{song: s, nodes: map[string]*NodePlan{}, Transport: map[string]Assignment{}, sounds: o.Sounds, notes: map[string]bool{}}

	newNode := func(at, track string, strip, slot int, pl Plugin, effect string) *NodePlan {
		// A plugin is named by its probe id or by any name that fits one.
		id, err := cat.Resolve(pl.Plugin)
		if err != nil {
			e.addf("%s/plugin: %v", at, err)
			id = pl.Plugin
		}
		dump := cat[id]
		n := &NodePlan{
			Track: track, Effect: effect, Plugin: id, Name: dump.Name,
			Strip: strip, Slot: slot,
			// An instrument always hears its track; an effect only when asked.
			// The master strip has no track of its own to hear.
			MIDI: track != MasterTrack && (pl.MIDI || effect == ""),
			dump: dump, home: pl.NodeState,
		}
		p.nodes[track+"/"+effect] = n
		return n
	}

	for i := range s.Tracks {
		t := &s.Tracks[i]
		at := fmt.Sprintf("/tracks/%d", i)
		tp := &TrackPlan{
			ID: t.ID, Name: t.TrackName(), Channel: s.TrackChannel(i), Strip: BrainStrip + 1 + i,
			Drums: t.Kind == "drums", homeMute: t.Mute,
		}
		if len(t.ControlChannels) > 0 {
			tp.ControlChannels = slices.Sorted(slices.Values(t.ControlChannels))
		}
		if t.Level != nil {
			tp.homeLevel = *t.Level
		}
		// AUM stores a strip as [source, output, inserts…]: the instrument is
		// slot 0, the output slot 1, and the effects follow from slot 2.
		tp.Nodes = append(tp.Nodes, newNode(at+"/instrument", t.ID, tp.Strip, 0, t.Instrument, ""))
		for j, fx := range t.Effects {
			tp.Nodes = append(tp.Nodes, newNode(fmt.Sprintf("%s/effects/%d", at, j), t.ID, tp.Strip, firstInsertSlot+j, fx, fx.EffectID()))
		}
		p.Tracks = append(p.Tracks, tp)
	}
	// The master strip is [bus source, hardware output, effects…]; its
	// effects are post-fader.
	p.MasterStrip = BrainStrip + 1 + len(s.Tracks)
	for j, fx := range s.Master {
		p.Master = append(p.Master, newNode(fmt.Sprintf("/master/%d", j), MasterTrack, p.MasterStrip, firstInsertSlot+j, fx, fx.EffectID()))
	}
	if err := e.err(); err != nil {
		return nil, err
	}

	// Collect what the song sets on each node, in a stable order: the
	// plugin's own declaration first, then scenes and ramps by name.
	for _, n := range p.allNodes() {
		p.collect(&e, n, n.home, "the track's declaration")
	}
	for _, name := range sortedKeys(s.Scenes) {
		for _, tid := range sortedKeys(s.Scenes[name]) {
			st := s.Scenes[name][tid]
			where := fmt.Sprintf("/scenes/%s/%s", name, tid)
			if n := p.nodes[tid+"/"]; n != nil {
				p.collect(&e, n, st.NodeState, where)
			}
			for _, fx := range sortedKeys(st.Effects) {
				if n := p.nodes[tid+"/"+fx]; n != nil {
					p.collect(&e, n, st.Effects[fx], where+"/effects/"+fx)
				}
			}
		}
	}
	for _, name := range sortedKeys(s.Sections) {
		for i, r := range s.Sections[name].Automation {
			if r.Effect == "" && strings.EqualFold(r.Param, "level") {
				continue
			}
			if n := p.nodes[r.Track+"/"+r.Effect]; n != nil {
				where := fmt.Sprintf("/sections/%s/automation/%d", name, i)
				if _, err := n.param(r.Param); err != nil {
					e.addf("%s/param: %v", where, err)
				}
			}
		}
	}
	if err := e.err(); err != nil {
		return nil, err
	}

	// A control a scene or ramp sets needs a home value on the track, so the
	// sections that do not set it know what to return to.
	p.checkHomes(&e)
	if err := e.err(); err != nil {
		return nil, err
	}

	// Addresses. Strips and transport take the convention's CCs on the global
	// channel; presets take its Program Changes; bypasses and parameters fill
	// the pool, which starts above every channel a track plays or hears on.
	lowest := 1
	for _, tp := range p.Tracks {
		for _, ch := range tp.Hears() {
			if ch >= lowest {
				lowest = ch + 1
			}
		}
	}
	pool := &ccPool{channel: poolTopChannel, lowest: lowest}
	globalUsed := map[int]bool{}
	for _, target := range []string{"Toggle Play", "Start Play", "Stop/Rewind", "Rewind", "Toggle Record", "Tap Tempo"} {
		cc, _ := device.ConventionTransportCC(target)
		p.Transport[target] = Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: cc}
		globalUsed[cc] = true
	}
	p.SequencerBypass = Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: sequencerBypassCC}
	globalUsed[sequencerBypassCC] = true
	var late []*TrackPlan // strips beyond the convention's eight
	for i, tp := range p.Tracks {
		vol, okV := device.ConventionMixerCC(i+1, "Volume")
		mute, okM := device.ConventionMixerCC(i+1, "Mute")
		if !okV || !okM {
			late = append(late, tp)
			continue
		}
		tp.Level = Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: vol}
		tp.Mute = Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: mute}
		globalUsed[vol], globalUsed[mute] = true, true
	}
	reserved := device.ConventionReservedCCs()
	nextGlobal := 0
	takeGlobal := func() Assignment {
		for ; nextGlobal <= 127; nextGlobal++ {
			if !reserved[nextGlobal] && !globalUsed[nextGlobal] {
				globalUsed[nextGlobal] = true
				return Assignment{Kind: MsgCC, Channel: GlobalChannel, Number: nextGlobal}
			}
		}
		return Assignment{}
	}
	for _, tp := range late {
		tp.Level, tp.Mute = takeGlobal(), takeGlobal()
	}

	for _, n := range p.allNodes() {
		var err error
		if n.Bypass, err = pool.take(); err != nil {
			return nil, err
		}
	}
	if s.SessionOptions().AllPresets() {
		if err := p.mapAllPresets(lowest); err != nil {
			return nil, err
		}
	} else {
		program := 0
		for _, n := range p.allNodes() {
			for i := range n.Presets {
				if program > 127 {
					return nil, fmt.Errorf("the song loads more than 128 presets; the session has one Program Change channel for them (session presets: all spreads them over every control channel)")
				}
				n.Presets[i].Program, n.Presets[i].Channel = program, GlobalChannel
				program++
			}
		}
	}
	for _, n := range p.allNodes() {
		for i := range n.Params {
			var err error
			if n.Params[i].At, err = pool.take(); err != nil {
				return nil, err
			}
		}
	}

	for ch := lowest; ch <= 16; ch++ {
		p.ControlChannels = append(p.ControlChannels, ch)
	}
	if err := p.calibrate(); err != nil {
		return nil, err
	}
	return p, nil
}

// mapAllPresets gives every factory preset of every node a Program Change:
// the instruments' first, then the effects', each node's in the plugin's own
// order, filling the global channel and then the control channels below it
// down to lowest. A preset's address therefore depends only on the session's
// layout, not on which presets the song names: a song can change to any of
// them, and any can be auditioned, without a new session.
//
// Presets beyond the room there is are left unmapped and counted; one the
// song names must fit.
func (p *Plan) mapAllPresets(lowest int) error {
	channel, program := GlobalChannel, 0
	take := func() (PresetPlan, bool) {
		if program > 127 {
			channel, program = channel-1, 0
		}
		if channel < lowest {
			return PresetPlan{}, false
		}
		pr := PresetPlan{Channel: channel, Program: program}
		program++
		return pr, true
	}
	var nodes []*NodePlan
	for _, tp := range p.Tracks {
		nodes = append(nodes, tp.Nodes[0])
	}
	for _, tp := range p.Tracks {
		nodes = append(nodes, tp.Nodes[1:]...)
	}
	nodes = append(nodes, p.Master...)
	for _, n := range nodes {
		named := n.Presets
		n.Presets = nil
		seen := map[string]bool{}
		for _, fp := range n.dump.FactoryPresets {
			// A plugin may list two presets under one name. A name can load
			// only one of them, and a session holds one Preset Load per name:
			// the first is the one.
			if key := strings.ToLower(fp.Name); seen[key] {
				continue
			} else {
				seen[key] = true
			}
			pr, ok := take()
			if !ok {
				p.PresetsUnmapped++
				continue
			}
			pr.Name, pr.Number = fp.Name, fp.Number
			n.Presets = append(n.Presets, pr)
		}
		for _, want := range named {
			if _, ok := n.Preset(want.Name); !ok {
				room := (GlobalChannel - lowest + 1) * 128
				return fmt.Errorf("%s loads %q, but a session that maps every preset (session presets: all) has room for %d and this preset lies beyond it; use fewer plugins, or drop presets: all",
					nodeLabel(n), want.Name, room)
			}
		}
	}
	return nil
}

// Preset returns the Program Change that loads one of the node's presets, by
// name (exactly, then ignoring case). ok is false when the session does not
// map that preset.
func (n *NodePlan) Preset(name string) (PresetPlan, bool) {
	for _, pr := range n.Presets {
		if pr.Name == name {
			return pr, true
		}
	}
	for _, pr := range n.Presets {
		if strings.EqualFold(pr.Name, name) {
			return pr, true
		}
	}
	return PresetPlan{}, false
}

// PresetEvent is the message that loads one of the node's presets; ok is
// false when the session does not map it.
func (n *NodePlan) PresetEvent(name string) (Event, bool) {
	pr, ok := n.Preset(name)
	return pr.event(), ok
}

// FactoryPresets lists the names of the node's plugin's factory presets, each
// name once.
func (n *NodePlan) FactoryPresets() []string {
	out := make([]string, 0, len(n.dump.FactoryPresets))
	seen := map[string]bool{}
	for _, fp := range n.dump.FactoryPresets {
		if key := strings.ToLower(fp.Name); !seen[key] {
			seen[key] = true
			out = append(out, fp.Name)
		}
	}
	return out
}

// HomePreset is the preset the node's plugin declares for itself, "" for none.
func (n *NodePlan) HomePreset() string { return n.home.Preset }

// allNodes lists every node in plan order: each track's instrument and
// effects, then the master's effects.
func (p *Plan) allNodes() []*NodePlan {
	var out []*NodePlan
	for _, tp := range p.Tracks {
		out = append(out, tp.Nodes...)
	}
	return append(out, p.Master...)
}

// collect resolves the preset and parameters a node state names and adds them
// to the node's plan.
func (p *Plan) collect(e *errs, n *NodePlan, st NodeState, where string) {
	if st.Preset != "" {
		if _, err := n.preset(st.Preset); err != nil {
			e.addf("%s/preset: %v", where, err)
		}
	}
	for _, name := range sortedKeys(st.Params) {
		pp, err := n.param(name)
		if err != nil {
			e.addf("%s/params/%s: %v", where, name, err)
			continue
		}
		if _, err := paramWire(pp.param, st.Params[name]); err != nil {
			e.addf("%s/params/%s: %v", where, name, err)
		}
	}
}

// checkHomes reports every control a scene or ramp sets that the track itself
// does not declare.
func (p *Plan) checkHomes(e *errs) {
	s := p.song
	needPreset := func(n *NodePlan, where string) {
		if n.home.Preset == "" {
			e.addf("%s/preset: %s has no preset of its own; give its plugin a preset so the other sections know what to return to", where, nodeLabel(n))
		}
	}
	needParam := func(n *NodePlan, name, where string) {
		pp, err := n.param(name)
		if err != nil {
			return // reported by collect
		}
		for home := range n.home.Params {
			if hp, err := n.param(home); err == nil && hp.Key == pp.Key {
				return
			}
		}
		e.addf("%s: %s does not declare %q; add it to the plugin's params so the other sections know what to return to", where, nodeLabel(n), pp.Name)
	}
	for _, name := range sortedKeys(s.Scenes) {
		for _, tid := range sortedKeys(s.Scenes[name]) {
			st := s.Scenes[name][tid]
			where := fmt.Sprintf("/scenes/%s/%s", name, tid)
			check := func(n *NodePlan, ns NodeState, where string) {
				if n == nil {
					return
				}
				if ns.Preset != "" {
					needPreset(n, where)
				}
				for _, pn := range sortedKeys(ns.Params) {
					needParam(n, pn, where+"/params/"+pn)
				}
			}
			check(p.nodes[tid+"/"], st.NodeState, where)
			for _, fx := range sortedKeys(st.Effects) {
				check(p.nodes[tid+"/"+fx], st.Effects[fx], where+"/effects/"+fx)
			}
		}
	}
	for _, name := range sortedKeys(s.Sections) {
		for i, r := range s.Sections[name].Automation {
			if r.Effect == "" && strings.EqualFold(r.Param, "level") {
				continue
			}
			if n := p.nodes[r.Track+"/"+r.Effect]; n != nil {
				needParam(n, r.Param, fmt.Sprintf("/sections/%s/automation/%d", name, i))
			}
		}
	}
}

// nodeLabel names a node for a message.
func nodeLabel(n *NodePlan) string {
	if n.Effect == "" {
		return fmt.Sprintf("%s's instrument", n.Track)
	}
	return fmt.Sprintf("%s's effect %s", n.Track, n.Effect)
}

// preset resolves a factory preset by name (exactly, then ignoring case) and
// adds it to the node's plan.
func (n *NodePlan) preset(name string) (*PresetPlan, error) {
	for i := range n.Presets {
		if strings.EqualFold(n.Presets[i].Name, name) {
			return &n.Presets[i], nil
		}
	}
	var found *device.ProbePreset
	for i := range n.dump.FactoryPresets {
		fp := &n.dump.FactoryPresets[i]
		if fp.Name == name {
			found = fp
			break
		}
		if found == nil && strings.EqualFold(fp.Name, name) {
			found = fp
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%s has no factory preset %q%s", n.Name, name, suggestPresets(n.dump, name))
	}
	n.Presets = append(n.Presets, PresetPlan{Name: found.Name, Number: found.Number})
	return &n.Presets[len(n.Presets)-1], nil
}

// param resolves a writable parameter by identifier, display name or key path
// (ignoring case) and adds it to the node's plan.
func (n *NodePlan) param(name string) (*ParamPlan, error) {
	at, key, err := findParam(n.dump, name)
	if err != nil {
		return nil, fmt.Errorf("%s %w", n.Name, err)
	}
	for i := range n.Params {
		if n.Params[i].Key == key {
			return &n.Params[i], nil
		}
	}
	if n.refs == nil {
		n.refs = aum.ParamRefs(n.dump.Parameters)
	}
	pr := n.dump.Parameters[at]
	n.Params = append(n.Params, ParamPlan{
		Key:   key,
		Name:  device.FirstNonEmpty(pr.DisplayName, pr.Identifier, key),
		param: pr,
		ref:   n.refs[at],
	})
	return &n.Params[len(n.Params)-1], nil
}

// paramKeys returns the key of every writable parameter of a dump, in the
// dump's order: its identifier, then display name, then key path, made unique.
// A key names the parameter within its plugin; where the session keeps the
// parameter's mapping is the session library's to say (aum.ParamRefs).
func paramKeys(dump device.ProbeDump) []string {
	used := map[string]bool{}
	keys := make([]string, len(dump.Parameters))
	for i, p := range dump.Parameters {
		if !p.Writable {
			continue
		}
		keys[i] = device.UniqueName(device.FirstNonEmpty(p.Identifier, p.DisplayName, p.KeyPath, "param_"+strconv.FormatUint(p.Address, 10)), used)
	}
	return keys
}

// findParam finds a writable parameter by name, returning its place in the
// dump and its key.
func findParam(dump device.ProbeDump, name string) (int, string, error) {
	keys := paramKeys(dump)
	want := strings.ToLower(strings.TrimSpace(name))
	match := func(pick func(device.ProbeParam) string) (int, int) {
		first, count := -1, 0
		for i, p := range dump.Parameters {
			if keys[i] != "" && strings.ToLower(pick(p)) == want {
				if first < 0 {
					first = i
				}
				count++
			}
		}
		return first, count
	}
	for _, pick := range []func(device.ProbeParam) string{
		func(p device.ProbeParam) string { return p.Identifier },
		func(p device.ProbeParam) string { return p.DisplayName },
		func(p device.ProbeParam) string { return p.KeyPath },
	} {
		switch i, count := match(pick); {
		case count == 1:
			return i, keys[i], nil
		case count > 1:
			return 0, "", fmt.Errorf("has %d parameters called %q; use the identifier (%s)", count, name, sameNamed(dump, keys, want))
		}
	}
	return 0, "", fmt.Errorf("has no writable parameter %q%s", name, suggestParams(dump, keys, want))
}

// sameNamed lists the identifiers of the writable parameters sharing a name.
func sameNamed(dump device.ProbeDump, keys []string, want string) string {
	var ids []string
	for i, p := range dump.Parameters {
		if keys[i] != "" && (strings.ToLower(p.DisplayName) == want || strings.ToLower(p.KeyPath) == want) {
			ids = append(ids, keys[i])
		}
	}
	return strings.Join(ids, ", ")
}

// maxSuggestions bounds the "did you mean" lists.
const maxSuggestions = 8

// suggest returns the candidates containing any word of want, shortest first.
func suggest(candidates []string, want string) []string {
	words := strings.Fields(strings.ToLower(want))
	var hits []string
	seen := map[string]bool{}
	for _, c := range candidates {
		lc := strings.ToLower(c)
		if c == "" || seen[lc] {
			continue
		}
		for _, w := range words {
			if len(w) >= 3 && strings.Contains(lc, w) {
				hits = append(hits, c)
				seen[lc] = true
				break
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return len(hits[i]) < len(hits[j]) })
	if len(hits) > maxSuggestions {
		hits = hits[:maxSuggestions]
	}
	return hits
}

func suggestParams(dump device.ProbeDump, keys []string, want string) string {
	var names []string
	for i, p := range dump.Parameters {
		if keys[i] != "" {
			names = append(names, device.FirstNonEmpty(p.DisplayName, p.Identifier))
		}
	}
	if hits := suggest(names, want); len(hits) > 0 {
		return " (did you mean: " + strings.Join(hits, ", ") + "?)"
	}
	return fmt.Sprintf(" (it has %d writable parameters)", len(names))
}

func suggestPresets(dump device.ProbeDump, want string) string {
	names := make([]string, len(dump.FactoryPresets))
	for i, p := range dump.FactoryPresets {
		names[i] = p.Name
	}
	if hits := suggest(names, want); len(hits) > 0 {
		return " (did you mean: " + strings.Join(hits, ", ") + "?)"
	}
	return fmt.Sprintf(" (it has %d factory presets)", len(names))
}

func suggestPlugins(cat Catalog, want string) string {
	var names []string
	for id, d := range cat {
		names = append(names, id+" ("+d.Name+")")
	}
	sort.Strings(names)
	if hits := suggest(names, want); len(hits) > 0 {
		return " (did you mean: " + strings.Join(hits, ", ") + "?)"
	}
	return ""
}

// paramWire converts a value written in a song into the 0..127 a CC carries
// for the parameter. AUM maps a CC's range onto the parameter's own range, so
// the wire value is the value's position in that range.
func paramWire(p device.ProbeParam, v Value) (int, error) {
	span := p.Max - p.Min
	pos := 0.0 // position in the range, 0..1
	switch {
	case !v.IsStr:
		if v.Num < p.Min || v.Num > p.Max {
			return 0, fmt.Errorf("%g is outside %s's range %g..%g", v.Num, device.FirstNonEmpty(p.DisplayName, p.Identifier), p.Min, p.Max)
		}
		if span > 0 {
			pos = (v.Num - p.Min) / span
		}
	case strings.HasSuffix(v.Str, "%"):
		pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v.Str, "%")), 64)
		if err != nil || pct < 0 || pct > 100 {
			return 0, fmt.Errorf("%q is not a percentage within 0%%..100%%", v.Str)
		}
		pos = pct / 100
	default:
		label := strings.ToLower(strings.TrimSpace(v.Str))
		idx := -1
		for i, vs := range p.ValueStrings {
			if strings.ToLower(strings.TrimSpace(vs)) == label {
				idx = i
				break
			}
		}
		switch {
		case idx >= 0 && len(p.ValueStrings) > 1:
			pos = float64(idx) / float64(len(p.ValueStrings)-1)
		case label == "on" || label == "true":
			pos = 1
		case label == "off" || label == "false":
			pos = 0
		default:
			hint := "write a number or NN%"
			if len(p.ValueStrings) > 0 && len(p.ValueStrings) <= 16 {
				hint = "its values are: " + strings.Join(p.ValueStrings, ", ")
			}
			return 0, fmt.Errorf("%q is not a value of %s (%s)", v.Str, device.FirstNonEmpty(p.DisplayName, p.Identifier), hint)
		}
	}
	return clamp7(int(math.Round(pos * 127))), nil
}

// The fader. A strip's level is stored as a linear gain (1 is unity). Its MIDI
// mapping follows gain = 2·(value/127)², measured on AUM by sweeping a mapped
// CC and reading the fader: 90 is unity, every halving of the value is 12 dB
// down, and 127 is +6 dB — the most a CC can reach, though the fader itself
// goes to +12.
const (
	// MaxLevelDB is the highest level a song can set.
	MaxLevelDB = 6.0
	// minLevelDB is treated as silence.
	minLevelDB = -90.0
)

// LevelGain converts a level in dB to the linear gain a session stores.
func LevelGain(db float64) float64 {
	if db <= minLevelDB {
		return 0
	}
	if db > MaxLevelDB {
		db = MaxLevelDB
	}
	return math.Pow(10, db/20)
}

// levelWire converts a level in dB to the CC value that moves the fader there.
func levelWire(db float64) int {
	return clamp7(int(math.Round(127 * math.Sqrt(LevelGain(db)/2))))
}

func clamp7(v int) int {
	if v < 0 {
		return 0
	}
	if v > 127 {
		return 127
	}
	return v
}

// boolWire is the CC value of a switch: on latches above the middle.
func boolWire(on bool) int {
	if on {
		return 127
	}
	return 0
}

// Track returns a track's plan by id.
func (p *Plan) Track(id string) *TrackPlan {
	for _, tp := range p.Tracks {
		if tp.ID == id {
			return tp
		}
	}
	return nil
}

// Node returns the plan of a track's instrument (effect "") or one of its
// effects; track MasterTrack addresses the master's effects.
func (p *Plan) Node(track, effect string) *NodePlan {
	return p.nodes[track+"/"+effect]
}

// Fingerprint identifies the plan's session: two songs with the same
// fingerprint need the same session, so a song whose fingerprint changed must
// be authored and loaded again before it plays right. It covers what the
// session holds: the tempo, the strips, the plugins and every mapping.
func (p *Plan) Fingerprint() string {
	var b strings.Builder
	// The tempo is the session's clock, which tempo-synced effects follow.
	fmt.Fprintf(&b, "tempo:%g|", p.song.Tempo)
	// The session options, when the song sets any (a song without them keeps
	// the fingerprint it had before they existed).
	if o := p.song.SessionOptions(); o != (SessionSpec{}) {
		fmt.Fprintf(&b, "S%v:%v:%s|", o.TrackTaps, o.FullControl, strings.ToLower(strings.TrimSpace(o.Hardware)))
		if o.AllPresets() {
			b.WriteString("Sall|")
		}
	}
	for _, tp := range p.Tracks {
		fmt.Fprintf(&b, "T%s:%d:%d:%s:%s|", tp.ID, tp.Channel, tp.Strip, tp.Level, tp.Mute)
		// The channels the instrument's route passes, when it passes more
		// than the track's own (a track without keeps its fingerprint).
		if len(tp.ControlChannels) > 0 {
			fmt.Fprintf(&b, "K%v|", tp.ControlChannels)
		}
	}
	for _, n := range p.allNodes() {
		fmt.Fprintf(&b, "N%s/%s:%s:%d:%d:%v:%s|", n.Track, n.Effect, n.Plugin, n.Strip, n.Slot, n.MIDI, n.Bypass)
		for _, pr := range n.Presets {
			fmt.Fprintf(&b, "P%s:%d:%d|", pr.Name, pr.Number, pr.Program)
			if pr.Channel != GlobalChannel {
				fmt.Fprintf(&b, "c%d|", pr.Channel)
			}
		}
		for _, pa := range n.Params {
			fmt.Fprintf(&b, "A%s:%s|", pa.Key, pa.At)
			// A parameter inside a group of its plugin's tree is mapped in
			// that group's collection. A session authored before that was
			// known keeps it where AUM does not look, so it is another
			// session; a parameter of a flat tree keeps its fingerprint.
			if len(pa.ref.Groups) > 0 {
				fmt.Fprintf(&b, "@%s|", pa.ref.Path())
			}
		}
	}
	return fmt.Sprintf("%016x", fnv64(b.String()))
}

// fnv64 is the FNV-1a hash of s.
func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
