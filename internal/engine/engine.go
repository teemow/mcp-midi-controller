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
	"time"

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
	observed   *ObservedState
	connected  map[connKey]bool // open data paths

	// statePath, when set via EnableStatePersistence, is where desired-state is
	// written after each change so it survives a daemon restart. Empty disables
	// persistence (e.g. in tests).
	statePath string

	// connectMu serializes the connect-or-cached sequence so concurrent sends
	// to the same endpoint open it exactly once. It is held during the
	// transport's (possibly slow) Connect, so it is kept separate from mu to
	// avoid blocking binding/state reads.
	connectMu sync.Mutex

	// inbound holds the per-endpoint Listen goroutines, the fan-out
	// subscribers (verify_control / probe_feedback), the recent-event buffer
	// (MIDI-learn) and the notification hook. Guarded by inboundMu.
	inboundMu   sync.Mutex
	listening   map[connKey]func()        // active inbound listeners -> cancel
	subscribers map[int]chan InboundEvent // verify/probe waiters
	nextSubID   int
	recent      []InboundEvent                    // ring buffer for MIDI-learn
	learnSince  time.Time                         // learn_capture only returns events after this
	onInbound   func(InboundEvent, []Observation) // notification hook (mcpserver)
}

// recentCap bounds the MIDI-learn ring buffer.
const recentCap = 64

// New constructs an engine from a device registry and the available transports.
func New(reg *device.Registry, transports ...transport.Transport) *Engine {
	tm := map[string]transport.Transport{}
	for _, t := range transports {
		tm[t.ID()] = t
	}
	return &Engine{
		registry:    reg,
		transports:  tm,
		bindings:    map[string]Binding{},
		state:       NewDesiredState(),
		observed:    NewObservedState(),
		connected:   map[connKey]bool{},
		listening:   map[connKey]func(){},
		subscribers: map[int]chan InboundEvent{},
	}
}

// EnableStatePersistence points the engine at a desired-state file, loading any
// existing state immediately and writing it back after each subsequent change.
// A missing file is not an error.
func (e *Engine) EnableStatePersistence(path string) error {
	e.mu.Lock()
	e.statePath = path
	e.mu.Unlock()
	return e.state.Load(path)
}

// persistState writes desired-state to disk if persistence is enabled. Errors
// are intentionally swallowed: a failed cache write must not break a control
// send (the in-memory state is still authoritative for the session).
func (e *Engine) persistState() {
	e.mu.RLock()
	path := e.statePath
	e.mu.RUnlock()
	if path == "" {
		return
	}
	_ = e.state.Save(path)
}

// Registry exposes the device registry.
func (e *Engine) Registry() *device.Registry { return e.registry }

// State exposes the desired-state model.
func (e *Engine) State() *DesiredState { return e.state }

// Observed exposes the observed-state model (reverse-mapped inbound MIDI).
func (e *Engine) Observed() *ObservedState { return e.observed }

// bindingKind distinguishes the two binding shapes a device can have.
type bindingKind int

const (
	// bindingControl is the default: the fire-and-forget control surface over
	// the device's control transport (def.Transport).
	bindingControl bindingKind = iota
	// bindingUSB is the USB editor/readback surface: a usbmidi/usbhid transport
	// binding against a device that carries a def.USB profile.
	bindingUSB
)

// isUSBTransport reports whether a transport id selects the USB
// editor/readback surface (as opposed to the control surface).
func isUSBTransport(id string) bool {
	return id == device.USBTransportMIDI || id == device.USBTransportHID
}

// kindOf classifies a binding given its device definition. A binding whose
// Transport names a USB transport and whose device carries a USB profile is a
// USB binding; everything else is a control binding (the control transport may
// itself be usbmidi as a bonus, but that is distinct from a USB profile
// binding).
func kindOf(b Binding, def *device.Definition) bindingKind {
	if isUSBTransport(b.Transport) && def.USB != nil {
		return bindingUSB
	}
	return bindingControl
}

// Bind adds a logical device, resolving whether it is a control binding or a
// USB binding (and validating the matching transport is registered). Caller is
// responsible for (re)generating the MCP tool and emitting tools/list_changed.
func (e *Engine) Bind(b Binding) error {
	def, ok := e.registry.Get(b.DeviceID)
	if !ok {
		return fmt.Errorf("bind %q: unknown device definition %q", b.Logical, b.DeviceID)
	}
	switch kindOf(b, def) {
	case bindingUSB:
		if b.Transport != def.USB.Transport {
			return fmt.Errorf("bind %q: binding transport %q does not match device %q usb transport %q", b.Logical, b.Transport, def.ID, def.USB.Transport)
		}
		if _, ok := e.transports[b.Transport]; !ok {
			return fmt.Errorf("bind %q: no transport %q for usb device %q", b.Logical, b.Transport, def.ID)
		}
	default:
		// A control binding that names a USB transport but whose device has no
		// USB profile is a misconfiguration: surface it rather than silently
		// treating it as a control binding.
		if isUSBTransport(b.Transport) && def.USB == nil {
			return fmt.Errorf("bind %q: transport %q requires a usb profile, but device %q has none", b.Logical, b.Transport, def.ID)
		}
		if _, ok := e.transports[def.Transport]; !ok {
			return fmt.Errorf("bind %q: no transport for device %q", b.Logical, b.DeviceID)
		}
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
	e.persistState()
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

// HasTransport reports whether a transport with the given id is registered.
func (e *Engine) HasTransport(id string) bool {
	_, ok := e.transports[id]
	return ok
}

// TransportIDs returns the registered transport ids in sorted order.
func (e *Engine) TransportIDs() []string { return e.transportIDs() }

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

// transportForBinding returns the transport id a binding actually uses: its
// own Transport for a USB binding, otherwise the device's control transport.
// This is the binding-aware form the inbound/reverse-map paths must use so a
// USB binding's endpoint is listened to on its USB transport, not the control
// transport.
func (e *Engine) transportForBinding(b Binding) string {
	if isUSBTransport(b.Transport) {
		return b.Transport
	}
	return e.transportFor(b.DeviceID)
}

// IsUSBBinding reports whether the named logical device is bound to a device's
// USB editor/readback surface (rather than its control surface).
func (e *Engine) IsUSBBinding(logical string) bool {
	e.mu.RLock()
	b, ok := e.bindings[logical]
	e.mu.RUnlock()
	if !ok {
		return false
	}
	def, ok := e.registry.Get(b.DeviceID)
	if !ok {
		return false
	}
	return kindOf(b, def) == bindingUSB
}

// binding returns a copy of the named binding.
func (e *Engine) binding(logical string) (Binding, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	b, ok := e.bindings[logical]
	return b, ok
}

// BindingFor returns a copy of the named logical device's binding.
func (e *Engine) BindingFor(logical string) (Binding, bool) { return e.binding(logical) }

// usbBindingForDevice returns a USB binding (if any) whose definition is the
// same device id, e.g. the "sl2-usb" USB binding that shares device "sl-2" with
// the "sl2" control binding. It is how verify_control finds the USB readback
// path for a control. Bindings are scanned in sorted logical order for a
// deterministic choice when several USB bindings share one device.
func (e *Engine) usbBindingForDevice(deviceID string) (Binding, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	logicals := make([]string, 0, len(e.bindings))
	for l := range e.bindings {
		logicals = append(logicals, l)
	}
	sort.Strings(logicals)
	for _, l := range logicals {
		b := e.bindings[l]
		if b.DeviceID != deviceID {
			continue
		}
		def, ok := e.registry.Get(b.DeviceID)
		if !ok {
			continue
		}
		if kindOf(b, def) == bindingUSB {
			return b, true
		}
	}
	return Binding{}, false
}
