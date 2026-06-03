package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DesiredState is the server's authoritative model of the rig: per logical
// device, the last value sent for each control. MIDI is mostly one-way, so this
// (not a hardware read-back) is the source of truth. It is persisted to the
// state dir so a daemon restart can resume the last applied state, and may be
// reconciled from inbound MIDI when the user tweaks hardware by hand.
type DesiredState struct {
	mu     sync.RWMutex
	values map[string]map[string]any // logical -> control -> value

	// saveMu serializes Save so concurrent control sends (each calling
	// persistState) cannot interleave writes to the state file.
	saveMu sync.Mutex
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

// ClearDevice drops all recorded values for a logical device. Used by Exact
// scene recall so a device ends up holding exactly the scene's values.
func (s *DesiredState) ClearDevice(logical string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, logical)
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

// Save persists the desired-state to path as JSON (creating the parent dir), so
// a daemon restart can resume the last applied state. Values round-trip through
// JSON, so integers reload as float64 — which device.Resolve accepts.
func (s *DesiredState) Save(path string) error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.values, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	// Serialize saves so two concurrent persisters cannot interleave, and write
	// to a temp file + rename so a reader (or a crash mid-write) never sees a
	// truncated/partial state file.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".desired-state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Load reads a persisted desired-state from path, replacing the in-memory
// values. A missing file is not an error (a fresh install starts empty).
func (s *DesiredState) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	values := map[string]map[string]any{}
	if err := json.Unmarshal(b, &values); err != nil {
		return err
	}
	s.mu.Lock()
	s.values = values
	s.mu.Unlock()
	return nil
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

// Observed is one reverse-mapped inbound observation: the logical value a
// control was seen to take (decoded from inbound MIDI), the raw wire value it
// was decoded from, the transport endpoint it arrived on, and when. Unlike
// desired-state (what we told the rig to do) this is what the rig told us — the
// basis for verify_control and reconciling hand-tweaks on the hardware.
type Observed struct {
	Value  any       `json:"value"`
	Wire   int       `json:"wire"`
	Source string    `json:"source"`
	Time   time.Time `json:"time"`
}

// ObservedState mirrors DesiredState but records inbound observations rather
// than outbound intent. It is volatile (never persisted): it reflects only what
// has actually been heard from the hardware this session.
type ObservedState struct {
	mu     sync.RWMutex
	values map[string]map[string]Observed // logical -> control -> observation
}

// NewObservedState returns an empty observed-state.
func NewObservedState() *ObservedState {
	return &ObservedState{values: map[string]map[string]Observed{}}
}

// Set records an observation for a control of a logical device.
func (s *ObservedState) Set(logical, control string, o Observed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[logical] == nil {
		s.values[logical] = map[string]Observed{}
	}
	s.values[logical][control] = o
}

// Device returns a copy of the recorded observations for a logical device.
func (s *ObservedState) Device(logical string) map[string]Observed {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]Observed{}
	for k, v := range s.values[logical] {
		out[k] = v
	}
	return out
}

// Snapshot returns a copy of the full observed-state.
func (s *ObservedState) Snapshot() map[string]map[string]Observed {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]map[string]Observed{}
	for dev, controls := range s.values {
		c := map[string]Observed{}
		for k, v := range controls {
			c[k] = v
		}
		out[dev] = c
	}
	return out
}
