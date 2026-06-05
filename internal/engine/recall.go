package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/scene"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// SaveScene snapshots the current desired-state into a named scene. When
// devices is non-empty the snapshot is restricted to those logical devices
// (others are dropped), so a scene can capture just a subset of the rig. Only
// devices that actually have recorded values are stored.
func (e *Engine) SaveScene(name, description string, devices []string) (*scene.Scene, error) {
	if name == "" {
		return nil, fmt.Errorf("scene name is required")
	}
	snap := e.state.Snapshot()

	var filter map[string]bool
	if len(devices) > 0 {
		filter = make(map[string]bool, len(devices))
		for _, d := range devices {
			filter[d] = true
		}
	}

	sc := &scene.Scene{
		Name:        name,
		Description: description,
		Devices:     map[string]map[string]any{},
	}
	for logical, controls := range snap {
		if filter != nil && !filter[logical] {
			continue
		}
		if len(controls) == 0 {
			continue
		}
		cp := make(map[string]any, len(controls))
		for k, v := range controls {
			cp[k] = v
		}
		sc.Devices[logical] = cp
	}
	// Surface a clear error if the caller asked for a device that has no
	// recorded state (likely a typo), rather than silently saving nothing.
	// Ranging over a nil filter is a no-op (the no-filter case).
	for d := range filter {
		if _, ok := sc.Devices[d]; !ok {
			return nil, fmt.Errorf("device %q has no desired-state to snapshot", d)
		}
	}
	return sc, nil
}

// RecallOptions configures how RecallSceneVia applies a scene.
type RecallOptions struct {
	// Mode selects additive (layer) vs exact (reset+replace) recall.
	Mode scene.RecallMode

	// Sink, when non-nil, receives every rendered wire event (program changes
	// first, then the settle delay, then the rest) instead of each device's
	// hardware transport — used to drive recall through the ProbeMidiBrain LAN
	// channel, where the brain re-emits the MIDI inside AUM. The per-device
	// binding is still required (it supplies the MIDI channel), but no hardware
	// connection is opened. USB patch blobs are skipped (the brain has no USB
	// surface) and reported as warnings. Desired-state is still updated.
	Sink func(ctx context.Context, ev transport.Event) error
}

// RecallScene replays a stored scene live over each device's hardware transport:
// for each referenced device it renders the scene's controls to wire events,
// sends the program changes first, waits the device's settle_ms, then sends the
// remaining events — the live equivalent of the footswitch compile ordering.
// Desired-state is updated as it goes.
//
// In Additive mode the scene layers over the current desired-state; in Exact
// mode each referenced device is first reset so its desired-state ends up
// exactly the scene's values. A scene that references an unbound device is an
// error (its channel/endpoint is only known from the binding), mirroring
// CompileFootswitchScene. Returns any non-fatal warnings.
func (e *Engine) RecallScene(ctx context.Context, sc *scene.Scene, mode scene.RecallMode) ([]string, error) {
	return e.RecallSceneVia(ctx, sc, RecallOptions{Mode: mode})
}

// RecallSceneVia is RecallScene with explicit options. With opts.Sink set the
// rendered wire events are routed to the sink (the brain channel) rather than to
// hardware transports; otherwise it behaves exactly like RecallScene.
func (e *Engine) RecallSceneVia(ctx context.Context, sc *scene.Scene, opts RecallOptions) ([]string, error) {
	if sc == nil {
		return nil, fmt.Errorf("nil scene")
	}
	mode := opts.Mode
	if mode == "" {
		mode = scene.Additive
	}
	viaBrain := opts.Sink != nil

	// Snapshot bindings so we do not hold the lock across sends/sleeps.
	e.mu.RLock()
	bindings := make(map[string]Binding, len(e.bindings))
	for k, v := range e.bindings {
		bindings[k] = v
	}
	e.mu.RUnlock()

	logicals := make([]string, 0, len(sc.Devices))
	for l := range sc.Devices {
		logicals = append(logicals, l)
	}
	sort.Strings(logicals)

	var warnings []string
	for _, logical := range logicals {
		controls := sc.Devices[logical]
		b, ok := bindings[logical]
		if !ok {
			return warnings, fmt.Errorf("scene references unbound device %q; bind it (bind_device) so its channel is known", logical)
		}
		def, ok := e.registry.Get(b.DeviceID)
		if !ok {
			return warnings, fmt.Errorf("device %q: unknown definition %q", logical, b.DeviceID)
		}
		if len(controls) == 0 {
			continue
		}

		// send routes one event either to the brain sink or to the device's
		// hardware transport. The hardware transport is only resolved/connected
		// when not going through the brain.
		var tr transport.Transport
		if !viaBrain {
			tr, ok = e.transports[def.Transport]
			if !ok {
				return warnings, fmt.Errorf("no transport %q for device %q", def.Transport, def.ID)
			}
			if err := e.ensureConnected(ctx, tr, b.Endpoint); err != nil {
				return warnings, fmt.Errorf("connect %q: %w", b.Endpoint, err)
			}
		}
		send := func(ev transport.Event) error {
			if viaBrain {
				return opts.Sink(ctx, ev)
			}
			return tr.Send(ctx, b.Endpoint, ev)
		}

		// In exact mode the device is reset to exactly the scene's values.
		if mode == scene.Exact {
			e.state.ClearDevice(logical)
		}

		// Deterministic control order, then split PCs from the rest.
		names := make([]string, 0, len(controls))
		for n := range controls {
			names = append(names, n)
		}
		sort.Strings(names)

		var pcs, rest []transport.Event
		var hadPC bool
		for _, name := range names {
			c, ok := def.Control(name)
			if !ok {
				return warnings, fmt.Errorf("device %q (%s) has no control %q", logical, def.ID, name)
			}
			resolved, err := device.Resolve(c, controls[name])
			if err != nil {
				return warnings, fmt.Errorf("%s.%s: %w", logical, name, err)
			}
			events, err := renderControl(def, c, b.Channel, resolved)
			if err != nil {
				return warnings, fmt.Errorf("%s.%s: %w", logical, name, err)
			}
			if c.Type == device.ControlProgramChange {
				pcs = append(pcs, events...)
				hadPC = true
			} else {
				rest = append(rest, events...)
			}
		}

		for _, ev := range pcs {
			if err := send(ev); err != nil {
				return warnings, fmt.Errorf("send program change to %s: %w", logical, err)
			}
		}
		// Give the device time to load its preset before the CC overrides land.
		if hadPC && len(rest) > 0 && def.SettleMS > 0 {
			if err := sleepCtx(ctx, time.Duration(def.SettleMS)*time.Millisecond); err != nil {
				return warnings, err
			}
		}
		for _, ev := range rest {
			if err := send(ev); err != nil {
				return warnings, fmt.Errorf("send to %s: %w", logical, err)
			}
		}

		// Record desired-state for everything we applied.
		for _, name := range names {
			e.state.Set(logical, name, controls[name])
		}
	}

	// Patch-level USB blobs: write each captured blob back over USB (state the
	// control surface cannot reach, e.g. an SL-2 slicer pattern). These are
	// gated; with writes disabled the rest of the scene still recalls and the
	// skipped blob is reported as a warning rather than aborting the recall.
	// The brain channel has no USB surface, so over-the-brain recall skips them.
	usbLogicals := make([]string, 0, len(sc.USB))
	for l := range sc.USB {
		usbLogicals = append(usbLogicals, l)
	}
	sort.Strings(usbLogicals)
	if viaBrain && len(usbLogicals) > 0 {
		for _, logical := range usbLogicals {
			warnings = append(warnings, fmt.Sprintf("skipped usb patch for %q: recall via brain has no usb surface (use hardware recall for usb blobs)", logical))
		}
		usbLogicals = nil
	}
	for _, logical := range usbLogicals {
		b, ok := bindings[logical]
		if !ok {
			return warnings, fmt.Errorf("scene references unbound usb device %q; bind it (bind_device, transport usbmidi/usbhid) so its endpoint is known", logical)
		}
		if !b.HasUSB() {
			return warnings, fmt.Errorf("scene usb patch %q has no usb surface; bind it with transport usbmidi/usbhid", logical)
		}
		if !e.usbWritesAllowed(b) {
			warnings = append(warnings, fmt.Sprintf("skipped usb patch for %q: usb writes disabled (set usb_allow_writes and bind writable: true)", logical))
			continue
		}
		if _, err := e.USBWritePatch(ctx, logical, sc.USB[logical], false); err != nil {
			return warnings, fmt.Errorf("usb patch %q: %w", logical, err)
		}
	}

	e.persistState()
	return warnings, nil
}

// sleepCtx sleeps for d, returning early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
