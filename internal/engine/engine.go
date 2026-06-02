// Package engine is the reusable core: it owns the device registry, the set of
// transports, the bindings (logical devices), the authoritative desired-state,
// and scene orchestration. The MCP daemon (and any future stdio adapter) is a
// thin layer on top of this package.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// defaultTransport is the transport used when a tool call omits one. BLE-MIDI
// is the only transport that pairs, so it is the sensible default for the
// pair/raw escape hatches.
const defaultTransport = "blemidi"

// connKey identifies an open data path: a transport id plus an endpoint id.
type connKey struct {
	transport string
	endpoint  string
}

// Engine is the rig controller core.
type Engine struct {
	mu sync.RWMutex

	registry   *device.Registry
	transports map[string]transport.Transport
	bindings   map[string]Binding // logical -> binding
	state      *DesiredState
	connected  map[connKey]bool // open data paths

	// connectMu serializes the connect-or-cached sequence so concurrent sends
	// to the same endpoint open it exactly once. It is held during the
	// transport's (possibly slow) Connect, so it is kept separate from mu to
	// avoid blocking binding/state reads.
	connectMu sync.Mutex
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
		connected:  map[connKey]bool{},
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

// SetControl validates value against the control's spec, renders it to one or
// more transport events, ensures the endpoint is connected, sends them, and
// records desired-state. Validation failures are returned as a
// *device.ValidationError carrying an RFC-6901 path so the MCP layer can point
// the model at the offending field; everything else is a plain error.
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
	c, ok := def.Control(control)
	if !ok {
		return &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("device %q has no control %q", def.ID, control)}
	}

	resolved, err := device.Resolve(c, value)
	if err != nil {
		return err
	}
	events, err := renderControl(def, c, b.Channel, resolved)
	if err != nil {
		return err
	}

	tr, ok := e.transports[def.Transport]
	if !ok {
		return fmt.Errorf("no transport %q for device %q", def.Transport, def.ID)
	}
	if err := e.ensureConnected(ctx, tr, b.Endpoint); err != nil {
		return fmt.Errorf("connect %q: %w", b.Endpoint, err)
	}
	for _, ev := range events {
		if err := tr.Send(ctx, b.Endpoint, ev); err != nil {
			return fmt.Errorf("send to %s: %w", logical, err)
		}
	}
	e.state.Set(logical, control, value)
	return nil
}

// DiscoverEndpoints scans every transport and returns the aggregated reachable
// endpoints. Per-transport errors are tolerated (e.g. OSC/USB have no discovery
// yet) unless every transport fails and none returned anything.
func (e *Engine) DiscoverEndpoints(ctx context.Context) ([]transport.Endpoint, error) {
	var (
		out  []transport.Endpoint
		errs []string
	)
	for _, id := range e.transportIDs() {
		eps, err := e.transports[id].Discover(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		out = append(out, eps...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("discover failed: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// PairEndpoint bonds with an endpoint (BLE) and opens its data path so it is
// ready for control. transportID defaults to "blemidi" (the only transport that
// pairs).
func (e *Engine) PairEndpoint(ctx context.Context, transportID, endpoint string) error {
	if transportID == "" {
		transportID = defaultTransport
	}
	tr, ok := e.transports[transportID]
	if !ok {
		return fmt.Errorf("unknown transport %q", transportID)
	}
	if err := tr.Pair(ctx, endpoint); err != nil {
		return err
	}
	return e.ensureConnected(ctx, tr, endpoint)
}

// SendRaw is the untracked escape hatch: it ensures the endpoint is connected
// and emits a pre-built event verbatim (raw MIDI bytes or an OSC address).
func (e *Engine) SendRaw(ctx context.Context, transportID, endpoint string, ev transport.Event) error {
	if transportID == "" {
		transportID = defaultTransport
	}
	tr, ok := e.transports[transportID]
	if !ok {
		return fmt.Errorf("unknown transport %q", transportID)
	}
	if err := e.ensureConnected(ctx, tr, endpoint); err != nil {
		return fmt.Errorf("connect %q: %w", endpoint, err)
	}
	return tr.Send(ctx, endpoint, ev)
}

// ensureConnected opens (once) the data path to an endpoint, caching success so
// repeated control sends do not re-pair/re-open it.
func (e *Engine) ensureConnected(ctx context.Context, tr transport.Transport, endpoint string) error {
	key := connKey{tr.ID(), endpoint}
	if e.isConnected(key) {
		return nil
	}

	e.connectMu.Lock()
	defer e.connectMu.Unlock()
	// Re-check now that we hold the connect lock: another goroutine may have
	// opened it while we waited.
	if e.isConnected(key) {
		return nil
	}
	if err := tr.Connect(ctx, endpoint); err != nil {
		return err
	}
	e.mu.Lock()
	e.connected[key] = true
	e.mu.Unlock()
	return nil
}

func (e *Engine) isConnected(key connKey) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.connected[key]
}

// transportIDs returns the transport ids in a stable (sorted) order so output
// like discovery is deterministic.
func (e *Engine) transportIDs() []string {
	ids := make([]string, 0, len(e.transports))
	for id := range e.transports {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (e *Engine) transportFor(deviceID string) string {
	if d, ok := e.registry.Get(deviceID); ok {
		return d.Transport
	}
	return ""
}
