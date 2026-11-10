package song

// This file authors a song's AUM session from its plan: the strips, the hosted
// plugins, the MIDI matrix that lets one ProbeMidiBrain play every track on its
// own channel, and the MIDI-control mappings behind every control the song
// uses.

import (
	"fmt"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// SessionOptions tunes how a song's session is authored.
type SessionOptions struct {
	// Brain and Tap are the staged probes of the rig's own plugins. They only
	// add what a probe knows beyond the component identity (the icon); nil
	// uses the identity alone.
	Brain, Tap *device.ProbeDump
	// Prepare, when set, sees every hosted plugin's node before the session is
	// built — the place to fill in an audio unit's saved default state.
	Prepare func(*aum.NodeSpec) error
	// Hardware is the hardware-I/O profile the master's output is authored
	// for.
	Hardware aum.HardwareProfile
	// TrackTaps adds a ProbeAudioTap after every track's fader, so each track
	// can be heard on its own. The master always has one.
	TrackTaps bool
	// FullControl also maps every other target of the session (every
	// parameter of every plugin) around the song's own mappings.
	FullControl bool
}

// SessionReport summarizes an authored session.
type SessionReport struct {
	Strips   int `json:"strips"`
	Nodes    int `json:"nodes"`
	Mappings int `json:"mappings"`
	Presets  int `json:"presets"`
	// Banked is how many further targets FullControl mapped, and Overflow how
	// many it found no room for.
	Banked   int `json:"banked,omitempty"`
	Overflow int `json:"overflow,omitempty"`
}

// emptyBrainProgram is a ProbeMidiBrain program that does nothing. The brain's
// own default is a demo that sends Program Changes on channel 1 as the
// transport runs, which a song's first track would take as patch changes.
const emptyBrainProgram = `{"sections":[],"footswitches":[],"outputChannel":16,"sceneChangeMode":"programChange","sceneCC":0}`

// BuildSession authors the session the plan describes.
func (p *Plan) BuildSession(o SessionOptions) (*aum.Session, SessionReport, error) {
	var rep SessionReport

	brain := aum.ProbeBrainNode()
	if o.Brain != nil {
		brain = aum.NodeSpecFromDump(*o.Brain)
	}
	brain.StateDoc = map[string][]byte{"probeMidiBrainProgram": []byte(emptyBrainProgram)}
	tap := aum.ProbeTapNode()
	if o.Tap != nil {
		tap = aum.NodeSpecFromDump(*o.Tap)
	}
	tap.StateDoc = map[string][]byte{"probeAudioTapConfig": []byte(`{"streaming":true}`)}

	nodeSpec := func(n *NodePlan) (aum.NodeSpec, error) {
		ns := aum.NodeSpecFromDump(n.dump)
		if o.Prepare != nil {
			if err := o.Prepare(&ns); err != nil {
				return ns, fmt.Errorf("%s: %w", nodeLabel(n), err)
			}
		}
		return ns, nil
	}

	spec := aum.BuildSpec{
		Title:    p.song.Title,
		Tempo:    p.song.Tempo,
		Hardware: o.Hardware,
		Channels: []aum.ChannelSpec{{Kind: aum.KindMIDI, Title: "Brain", Nodes: []aum.NodeSpec{brain}}},
	}
	route := aum.MIDIRoute{From: aum.MIDIEndpoint{Channel: BrainStrip, Slot: 0}}
	for _, tp := range p.Tracks {
		gain := LevelGain(tp.homeLevel)
		ch := aum.ChannelSpec{
			Kind: aum.KindAudio, Title: tp.Name, Fader: &gain, Muted: tp.homeMute,
			Output: &aum.ChannelOutput{Kind: aum.OutputBus, BusIndex: 0},
		}
		if o.TrackTaps {
			ch.Tap, ch.TapNode = true, &tap
		}
		for _, n := range tp.Nodes {
			ns, err := nodeSpec(n)
			if err != nil {
				return nil, rep, err
			}
			ch.Nodes = append(ch.Nodes, ns)
			if n.MIDI {
				route.To = append(route.To, aum.MIDIEndpoint{
					Channel: n.Strip, Slot: n.Slot,
					Filter: &aum.MIDIFilter{Channels: []int{tp.Channel}},
				})
			}
		}
		spec.Channels = append(spec.Channels, ch)
	}
	master := aum.ChannelSpec{
		Kind: aum.KindAudio, Title: "Master",
		Source: &aum.ChannelSource{Kind: aum.SourceBus, BusIndex: 0},
		Output: &aum.ChannelOutput{Kind: aum.OutputHardware, HWBusIndex: 0},
		Tap:    true, TapNode: &tap,
	}
	for _, n := range p.Master {
		ns, err := nodeSpec(n)
		if err != nil {
			return nil, rep, err
		}
		master.PostNodes = append(master.PostNodes, ns)
	}
	spec.Channels = append(spec.Channels, master)
	route.To = append(route.To, aum.MIDIEndpoint{
		Builtin: "MIDI Control",
		Filter:  &aum.MIDIFilter{Channels: p.ControlChannels},
	})
	spec.Routes = []aum.MIDIRoute{route}

	sess, built, err := aum.BuildSession(spec)
	if err != nil {
		return nil, rep, fmt.Errorf("build session: %w", err)
	}
	rep.Strips, rep.Nodes = built.Channels, built.Nodes

	// Every mapping is an edit of a placeholder the session already lists, so
	// index the catalogue once and assign into it.
	catalogue := sess.Mappings(true)
	index := make(map[string]int, len(catalogue))
	for i, m := range catalogue {
		index[m.Collection+"\x00"+m.Target] = i
	}
	assign := func(collection, target string, at Assignment) (*aum.Mapping, error) {
		i, ok := index[collection+"\x00"+target]
		if !ok {
			return nil, fmt.Errorf("the session has no mapping target %s / %s", collection, target)
		}
		m := &catalogue[i]
		if err := m.Assign(aum.SpecStateTypeCC, at.Number, at.Channel-1); err != nil {
			return nil, err
		}
		rep.Mappings++
		return m, nil
	}

	for target, at := range p.Transport {
		if _, err := assign("Transport", target, at); err != nil {
			return nil, rep, err
		}
	}
	tapNumber := 0
	// tapToggle maps a tap's bypass the way the rest of the rig expects it: a
	// CC of its own on the tap channel that flips the stream.
	tapToggle := func(strip, slot int) error {
		tapNumber++
		cc, ok := device.ConventionTapCC(tapNumber)
		if !ok {
			return nil // more taps than the convention has toggles for
		}
		m, err := assign(slotCollection(strip, slot), "_AUMNode:Bypass",
			Assignment{Kind: MsgCC, Channel: device.TapControlChannel, Number: cc})
		if err != nil {
			return err
		}
		return m.SetAutoToggle(true)
	}
	for _, tp := range p.Tracks {
		strip := fmt.Sprintf("Channels/chan%d/Channel controls", tp.Strip)
		if _, err := assign(strip, "Volume", tp.Level); err != nil {
			return nil, rep, err
		}
		if _, err := assign(strip, "Mute", tp.Mute); err != nil {
			return nil, rep, err
		}
		if o.TrackTaps {
			// A track's chain is its nodes, the fader, then the tap.
			if err := tapToggle(tp.Strip, len(tp.Nodes)+1); err != nil {
				return nil, rep, err
			}
		}
	}
	// The master's chain is the bus source, the fader, its effects, the tap.
	if err := tapToggle(p.MasterStrip, 2+len(p.Master)); err != nil {
		return nil, rep, err
	}

	for _, n := range p.allNodes() {
		coll := slotCollection(n.Strip, n.Slot)
		if _, err := assign(coll, "_AUMNode:Bypass", n.Bypass); err != nil {
			return nil, rep, err
		}
		for _, pp := range n.Params {
			if _, err := assign(coll, pp.Key, pp.At); err != nil {
				return nil, rep, err
			}
		}
		for _, pr := range n.Presets {
			if err := sess.AddPresetLoad(n.Strip, n.Slot, aum.PresetLoad{
				Name: pr.Name, Number: pr.Number, Program: pr.Program, Channel: GlobalChannel - 1,
			}); err != nil {
				return nil, rep, err
			}
			rep.Presets++
		}
		if n.home.Bypass != nil && *n.home.Bypass {
			// Open already bypassed, as the song will set it.
			if err := sess.SetNodeParam(n.Strip, n.Slot, "AUMNode.bypassed", true); err != nil {
				return nil, rep, err
			}
		}
	}

	if o.FullControl {
		play := make([]int, len(p.Tracks))
		for i, tp := range p.Tracks {
			play[i] = tp.Channel
		}
		ir, err := sess.Instrument(aum.InstrumentOptions{
			GlobalChannel:    GlobalChannel,
			StartChannel:     p.ControlChannels[0],
			UseNotes:         true,
			PlayChannels:     play,
			PreserveExisting: true,
		})
		if err != nil {
			return nil, rep, fmt.Errorf("map the remaining targets: %w", err)
		}
		rep.Banked, rep.Overflow = ir.Assigned, len(ir.Overflow)
	}
	return sess, rep, nil
}

// slotCollection is the MIDI-control collection of a node.
func slotCollection(strip, slot int) string {
	return fmt.Sprintf("Channels/chan%d/slot%d", strip, slot)
}
