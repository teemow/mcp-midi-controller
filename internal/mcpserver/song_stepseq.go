package mcpserver

// What a song writes into a node's saved state (an instrument's sequence:,
// samples: and locks:, a UA Battalion's steps and lanes, its samples and the
// parameters its variation leaves alone) is not sent: it is written into the
// node's saved state when the song's session is authored. It is written over
// the state the node already has from the rig's default state for its unit,
// else over the unit's base state (capture_auv3_default_state with base:
// true).

import (
	"errors"
	"fmt"
	"maps"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
)

// hasStateBase reports whether the rig has a state to write what a song
// writes into a node's saved state over: a default state of its unit that
// holds the plugin's XML, or a base state.
func hasStateBase(n *song.NodePlan) bool {
	c := n.Component()
	for _, defs := range [][]device.AUv3DefaultState{loadAUv3DefaultStates(), loadAUv3BaseStates()} {
		def := findDefaultStateForComponent(defs, c)
		if def == nil {
			continue
		}
		if doc, err := def.StateDoc(); err == nil {
			if _, err := n.AuthoredState(doc); err == nil {
				return true
			}
		}
	}
	return false
}

// applyAuthoredState sets the state of a node whose song writes into it. A
// node the song writes nothing into is left alone.
func applyAuthoredState(n *song.NodePlan, ns *aum.NodeSpec, bases []device.AUv3DefaultState) error {
	if !n.WritesState() {
		return nil
	}
	state, err := n.AuthoredState(ns.StateDoc)
	if errors.Is(err, song.ErrNoBaseState) {
		def := findDefaultStateForComponent(bases, ns.Component)
		if def == nil {
			return fmt.Errorf("the song writes %s into its saved state, which needs the plugin's base state, the saved state of its default patch, and the rig has none: "+
				"load a session with a %s of the default patch in AUM, save it there, and capture the node with capture_auv3_default_state {session_id, channel, slot, base: true}", n.StateWrites(), n.Name)
		}
		base, derr := def.StateDoc()
		if derr != nil {
			return fmt.Errorf("the base state of %s: %w", n.Name, derr)
		}
		state, err = n.AuthoredState(base)
	}
	if err != nil {
		return err
	}
	ns.StateDoc = maps.Clone(state)
	return nil
}
