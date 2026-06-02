// Package engine is the reusable core: it owns the device registry, the set of
// transports, the bindings (logical devices), the authoritative desired-state,
// and scene orchestration. The MCP daemon (and any future stdio adapter) is a
// thin layer on top of this package.
package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// Engine is the rig controller core.
type Engine struct {
	mu sync.RWMutex

	registry   *device.Registry
	transports map[string]transport.Transport
	bindings   map[string]Binding // logical -> binding
	state      *DesiredState
}

// New constructs an engine from a device registry and the available transports.
func New(reg *device.Registry, transports ...transport.Transport) *Engine {
	tm := map[string]transport.Transport{}
	for _, t := range transports {
		tm[t.ID()] = t
	}
	return &Engine{
		registry:   reg,
		transports: tm,
		bindings:   map[string]Binding{},
		state:      NewDesiredState(),
	}
}

// Registry exposes the device registry.
func (e *Engine) Registry() *device.Registry { return e.registry }

// State exposes the desired-state model.
func (e *Engine) State() *DesiredState { return e.state }

// Bind adds a logical device. Caller is responsible for (re)generating the MCP
// tool and emitting tools/list_changed.
func (e *Engine) Bind(b Binding) error {
	if _, ok := e.registry.Get(b.DeviceID); !ok {
		return fmt.Errorf("bind %q: unknown device definition %q", b.Logical, b.DeviceID)
	}
	if _, ok := e.transports[e.transportFor(b.DeviceID)]; !ok {
		return fmt.Errorf("bind %q: no transport for device %q", b.Logical, b.DeviceID)
	}
	e.mu.Lock()
	e.bindings[b.Logical] = b
	e.mu.Unlock()
	return nil
}

// Unbind removes a logical device.
func (e *Engine) Unbind(logical string) {
	e.mu.Lock()
	delete(e.bindings, logical)
	e.mu.Unlock()
}

// Bindings returns the current bindings.
func (e *Engine) Bindings() []Binding {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Binding, 0, len(e.bindings))
	for _, b := range e.bindings {
		out = append(out, b)
	}
	return out
}

// SetControl validates value against the control's spec, renders it to a
// transport event, sends it, and records desired-state.
//
// TODO: implement value validation (returning a structured error with an
// RFC-6901 path for the MCP layer to surface as IsError), rendering
// (CC/PC/NRPN/SysEx/OSC), endpoint send, and state.Set.
func (e *Engine) SetControl(ctx context.Context, logical, control string, value any) error {
	e.mu.RLock()
	b, ok := e.bindings[logical]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown logical device %q", logical)
	}
	def, ok := e.registry.Get(b.DeviceID)
	if !ok {
		return fmt.Errorf("logical %q: unknown device %q", logical, b.DeviceID)
	}
	if _, ok := def.Control(control); !ok {
		return fmt.Errorf("device %q has no control %q", def.ID, control)
	}
	return fmt.Errorf("SetControl: not implemented")
}

func (e *Engine) transportFor(deviceID string) string {
	if d, ok := e.registry.Get(deviceID); ok {
		return d.Transport
	}
	return ""
}
