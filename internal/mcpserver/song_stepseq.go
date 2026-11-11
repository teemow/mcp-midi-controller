package mcpserver

// A song's step sequence (an instrument's sequence:, a UA Battalion's steps
// and lanes) is not sent: it is written into the node's saved state when the
// song's session is authored. It is written over the state the node already
// has from the rig's default state for its unit, else over the unit's base
// state (capture_auv3_default_state with base: true).

import (
	"errors"
	"fmt"
	"maps"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
)

// hasStepSequenceBase reports whether the rig has a state to write a node's
// step sequence over: a default state of its unit that holds the plugin's
// XML, or a base state.
func hasStepSequenceBase(n *song.NodePlan) bool {
	c := n.Component()
	for _, defs := range [][]device.AUv3DefaultState{loadAUv3DefaultStates(), loadAUv3BaseStates()} {
		def := findDefaultStateForComponent(defs, c)
		if def == nil {
			continue
		}
		if doc, err := def.StateDoc(); err == nil {
			if _, err := n.StepSequenceState(doc); err == nil {
				return true
			}
		}
	}
	return false
}

// applyStepSequence sets the state of a node whose song writes its step
// sequence. A node without one is left alone.
func applyStepSequence(n *song.NodePlan, ns *aum.NodeSpec, bases []device.AUv3DefaultState) error {
	if !n.HasStepSequence() {
		return nil
	}
	state, err := n.StepSequenceState(ns.StateDoc)
	if errors.Is(err, song.ErrNoBaseState) {
		def := findDefaultStateForComponent(bases, ns.Component)
		if def == nil {
			return fmt.Errorf("its sequence needs the plugin's base state, the saved state of its default patch, and the rig has none: "+
				"load a session with a %s of the default patch in AUM, save it there, and capture the node with capture_auv3_default_state {session_id, channel, slot, base: true}", n.Name)
		}
		base, derr := def.StateDoc()
		if derr != nil {
			return fmt.Errorf("the base state of %s: %w", n.Name, derr)
		}
		state, err = n.StepSequenceState(base)
	}
	if err != nil {
		return err
	}
	ns.StateDoc = maps.Clone(state)
	return nil
}
