// Package engine is the reusable core: it owns the device-type registry, the set
// of transports, the devices (the rig), the authoritative desired-state, and
// scene orchestration. The MCP daemon (and any future stdio adapter) is a thin
// layer on top of this package.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teemow/midi-device/device"
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

// inboundListener tracks a running inbound pump. gen is a unique generation so
// a pump's teardown only removes its own registry entry — a quickly
// restarted listener for the same endpoint (Stop then Start) must not be
// clobbered by the old pump's deferred cleanup.
type inboundListener struct {
	cancel func()
	gen    uint64
}

// Engine is the rig controller core.
type Engine struct {
	mu sync.RWMutex

	registry   *device.Registry
	transports map[string]transport.Transport
	devices    map[string]Device // logical -> device
	state      *DesiredState
	observed   *ObservedState
	connected  map[connKey]bool // open data paths

	// statePath, when set via EnableStatePersistence, is where desired-state is
	// written after each change so it survives a daemon restart. Empty disables
	// persistence (e.g. in tests).
	statePath string

	// usbAllowWrites is the daemon's master USB write gate (config
	// usb_allow_writes), mirrored here so engine-level USB writes (e.g. a
	// patch-level scene recall) obey the same two-key gate the MCP layer
	// enforces for the write tools: writes happen only when this is true AND
	// the target binding opts in with Writable. Default false = read-only.
	usbAllowWrites bool

	// connectMu serializes the connect-or-cached sequence so concurrent sends
	// to the same endpoint open it exactly once. It is held during the
	// transport's (possibly slow) Connect, so it is kept separate from mu to
	// avoid blocking binding/state reads.
	connectMu sync.Mutex

	// inbound holds the per-endpoint Listen goroutines, the fan-out
	// subscribers (verify_control / probe_feedback), the recent-event buffer
	// (MIDI-learn) and the notification hook. Guarded by inboundMu.
	inboundMu   sync.Mutex
	listening   map[connKey]*inboundListener // active inbound listeners
	listenSeq   uint64                       // monotonic listener generation
	subscribers map[int]chan InboundEvent    // verify/probe waiters
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
		devices:     map[string]Device{},
		state:       NewDesiredState(),
		observed:    NewObservedState(),
		connected:   map[connKey]bool{},
		listening:   map[connKey]*inboundListener{},
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

// SetUSBAllowWrites sets the engine's master USB write gate (config
// usb_allow_writes). It should be wired from the daemon config alongside the
// MCP server's WithUSBAllowWrites so engine-level USB writes (patch-level scene
// recall) and the MCP write tools share one gate. Default (unset) is false.
func (e *Engine) SetUSBAllowWrites(allow bool) {
	e.mu.Lock()
	e.usbAllowWrites = allow
	e.mu.Unlock()
}

// usbWritesAllowed reports whether USB writes may run for a device: both the
// master gate and the device's own Writable opt-in must be set (the two-key
// model from docs/usb-tools.md).
func (e *Engine) usbWritesAllowed(d Device) bool {
	e.mu.RLock()
	global := e.usbAllowWrites
	e.mu.RUnlock()
	return global && d.USBWritable()
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

// Close tears the engine down: it cancels every inbound listener and
// disconnects every open data path. It is best-effort — per-endpoint
// disconnect errors are joined into the returned error but never abort the
// teardown — and is meant to run on daemon shutdown.
func (e *Engine) Close(ctx context.Context) error {
	e.inboundMu.Lock()
	listeners := make([]*inboundListener, 0, len(e.listening))
	for k, l := range e.listening {
		listeners = append(listeners, l)
		delete(e.listening, k)
	}
	e.inboundMu.Unlock()
	for _, l := range listeners {
		l.cancel()
	}

	e.mu.Lock()
	keys := make([]connKey, 0, len(e.connected))
	for k := range e.connected {
		keys = append(keys, k)
	}
	e.connected = map[connKey]bool{}
	e.mu.Unlock()

	var errs []string
	for _, k := range keys {
		tr, ok := e.transports[k.transport]
		if !ok {
			continue
		}
		if err := tr.Disconnect(ctx, k.endpoint); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", k.transport, k.endpoint, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Registry exposes the device registry.
func (e *Engine) Registry() *device.Registry { return e.registry }

// State exposes the desired-state model.
func (e *Engine) State() *DesiredState { return e.state }

// Observed exposes the observed-state model (reverse-mapped inbound MIDI).
func (e *Engine) Observed() *ObservedState { return e.observed }

// Bind adds (or replaces) a device, validating each connection it carries: the
// control connection (if any) against the device type's transport, and the USB
// connection (if any) against the device type's USB profile and transport. A
// device must carry at least one connection, and every connection's transport
// must be one the device type speaks. Caller is responsible for (re)generating
// the MCP tools and emitting tools/list_changed.
func (e *Engine) Bind(d Device) error {
	def, ok := e.registry.Get(d.DeviceID)
	if !ok {
		return fmt.Errorf("bind %q: unknown device type %q", d.Name, d.DeviceID)
	}
	if !d.HasControl() && !d.HasUSB() {
		return fmt.Errorf("bind %q: device has no connections", d.Name)
	}
	if d.HasControl() {
		if _, ok := e.transports[def.Transport]; !ok {
			return fmt.Errorf("bind %q: no transport %q for device type %q", d.Name, def.Transport, d.DeviceID)
		}
	}
	if d.HasUSB() {
		if def.USB == nil {
			return fmt.Errorf("bind %q: usb connection requires a usb profile, but device type %q has none", d.Name, def.ID)
		}
		usbTr, _, _ := d.USBConnection()
		if usbTr != def.USB.Transport {
			return fmt.Errorf("bind %q: usb connection transport %q does not match device type %q usb transport %q", d.Name, usbTr, def.ID, def.USB.Transport)
		}
		if _, ok := e.transports[def.USB.Transport]; !ok {
			return fmt.Errorf("bind %q: no transport %q for usb connection of %q", d.Name, def.USB.Transport, def.ID)
		}
	}
	// Reject connections on a transport the device type does not speak (its
	// control transport or its USB transport) so a typo cannot silently bind an
	// unroutable connection.
	for tr := range d.Connections {
		switch {
		case tr == def.Transport:
		case def.USB != nil && tr == def.USB.Transport:
		default:
			return fmt.Errorf("bind %q: device type %q does not speak transport %q", d.Name, def.ID, tr)
		}
	}
	e.mu.Lock()
	e.devices[d.Name] = d
	e.mu.Unlock()
	return nil
}

// Unbind removes a device.
func (e *Engine) Unbind(logical string) {
	e.mu.Lock()
	delete(e.devices, logical)
	e.mu.Unlock()
}

// Devices returns the current devices.
func (e *Engine) Devices() []Device {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Device, 0, len(e.devices))
	for _, d := range e.devices {
		out = append(out, d)
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
	d, ok := e.devices[logical]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown logical device %q", logical)
	}
	def, ok := e.registry.Get(d.DeviceID)
	if !ok {
		return fmt.Errorf("logical %q: unknown device %q", logical, d.DeviceID)
	}
	c, ok := def.Control(control)
	if !ok {
		return &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("device %q has no control %q", def.ID, control)}
	}

	resolved, err := device.Resolve(c, value)
	if err != nil {
		return err
	}
	events, err := renderControl(def, c, d.ControlChannel(), resolved)
	if err != nil {
		return err
	}

	tr, ok := e.transports[def.Transport]
	if !ok {
		return fmt.Errorf("no transport %q for device %q", def.Transport, def.ID)
	}
	endpoint := d.ControlEndpoint()
	if err := e.ensureConnected(ctx, tr, endpoint); err != nil {
		return fmt.Errorf("connect %q: %w", endpoint, err)
	}
	for _, ev := range events {
		if err := tr.Send(ctx, endpoint, ev); err != nil {
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

// transportForDevice returns the transport id a device's control connection
// listens/sends on: its device type's transport. This is the device-aware form
// the inbound/reverse-map (control feedback/learn) paths use. The USB connection
// drives its own sessions over the type's USB transport (see usbContextFor),
// independent of this.
func (e *Engine) transportForDevice(d Device) string {
	return e.transportFor(d.DeviceID)
}

// IsUSBDevice reports whether the named logical device carries a USB
// editor/readback surface.
func (e *Engine) IsUSBDevice(logical string) bool {
	e.mu.RLock()
	d, ok := e.devices[logical]
	e.mu.RUnlock()
	return ok && d.HasUSB()
}

// device returns a copy of the named device.
func (e *Engine) device(logical string) (Device, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.devices[logical]
	return d, ok
}

// DeviceFor returns a copy of the named logical device.
func (e *Engine) DeviceFor(logical string) (Device, bool) { return e.device(logical) }

// usbDeviceForControl returns the device whose USB connection should answer a
// USB readback for a given control device. It prefers the control device's own
// USB connection, and otherwise falls back to any other device that shares the
// same device-type id and has a USB connection (scanned in sorted name order for
// a deterministic choice). It is how verify_control finds the USB readback path.
func (e *Engine) usbDeviceForControl(control Device) (Device, bool) {
	if control.HasUSB() {
		return control, true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	logicals := make([]string, 0, len(e.devices))
	for l := range e.devices {
		logicals = append(logicals, l)
	}
	sort.Strings(logicals)
	for _, l := range logicals {
		d := e.devices[l]
		if d.DeviceID == control.DeviceID && d.HasUSB() {
			return d, true
		}
	}
	return Device{}, false
}
