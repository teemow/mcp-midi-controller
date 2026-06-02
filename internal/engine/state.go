package engine

import "sync"

// DesiredState is the server's authoritative model of the rig: per logical
// device, the last value sent for each control. MIDI is mostly one-way, so this
// (not a hardware read-back) is the source of truth. It is persisted to the
// state dir so a daemon restart can resume the last applied state, and may be
// reconciled from inbound MIDI when the user tweaks hardware by hand.
type DesiredState struct {
	mu     sync.RWMutex
	values map[string]map[string]any // logical -> control -> value
}

// NewDesiredState returns an empty desired-state.
func NewDesiredState() *DesiredState {
	return &DesiredState{values: map[string]map[string]any{}}
}

// Set records the value applied to a control of a logical device.
func (s *DesiredState) Set(logical, control string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[logical] == nil {
		s.values[logical] = map[string]any{}
	}
	s.values[logical][control] = value
}

// Device returns a copy of the recorded control values for a logical device.
func (s *DesiredState) Device(logical string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{}
	for k, v := range s.values[logical] {
		out[k] = v
	}
	return out
}

// Snapshot returns a copy of the full desired-state, used by save_scene.
func (s *DesiredState) Snapshot() map[string]map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]map[string]any{}
	for dev, controls := range s.values {
		c := map[string]any{}
		for k, v := range controls {
			c[k] = v
		}
		out[dev] = c
	}
	return out
}
