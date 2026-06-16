package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teemow/midi-device/device"
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

// RecallScene replays a stored scene live over each device's transport: for
// each referenced device it renders the scene's controls to wire events, sends
// the program changes first, waits the device's settle_ms, then sends the
// remaining events — the live equivalent of the footswitch compile ordering.
// Desired-state is updated as it goes.
//
// Each device routes over the transport its device type speaks, resolved like
// any other send: a device type with transport auv3midi reaches the
// ProbeMidiBrain LAN channel (the brain re-emits inside AUM), a hardware device
// reaches BLE/USB/OSC — there is no per-recall override, the transport is the
// device type's property.
//
// In Additive mode the scene layers over the current desired-state; in Exact
// mode each referenced device is first reset so its desired-state ends up
// exactly the scene's values. A scene that references an unbound device is an
// error (its channel/endpoint is only known from the binding), mirroring
// CompileFootswitchScene. Returns any non-fatal warnings.
func (e *Engine) RecallScene(ctx context.Context, sc *scene.Scene, mode scene.RecallMode) ([]string, error) {
	if sc == nil {
		return nil, fmt.Errorf("nil scene")
	}
	if mode == "" {
		mode = scene.Additive
	}

	// Snapshot devices so we do not hold the lock across sends/sleeps.
	e.mu.RLock()
	devices := make(map[string]Device, len(e.devices))
	for k, v := range e.devices {
		devices[k] = v
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
		d, ok := devices[logical]
		if !ok {
			return warnings, fmt.Errorf("scene references unbound device %q; bind it (bind_device) so its channel is known", logical)
		}
		def, ok := e.registry.Get(d.DeviceID)
		if !ok {
			return warnings, fmt.Errorf("device %q: unknown definition %q", logical, d.DeviceID)
		}
		if len(controls) == 0 {
			continue
		}

		// In exact mode the device is reset to exactly the scene's values.
		if mode == scene.Exact {
			e.state.ClearDevice(logical)
		}

		// A scene names no transport: split this device's parameter values into
		// opaque USB-memory blobs (realized over the device's USB connection)
		// and ordinary controls (rendered to wire events over the control
		// transport). Which path a value takes is decided here from its shape,
		// not from the scene.
		var patches []scene.USBPatch
		names := make([]string, 0, len(controls))
		for n, v := range controls {
			if p, ok := scene.AsUSBPatch(v); ok {
				patches = append(patches, p)
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)

		// Ordinary controls: resolve and open the device type's transport, then
		// send each event over it. auv3midi resolves to the brain channel,
		// hardware to BLE/etc. A device carrying only a USB blob (no control
		// values) never touches a control transport.
		if len(names) > 0 {
			endpoint := d.ControlEndpoint()
			tr, ok := e.transports[def.Transport]
			if !ok {
				return warnings, fmt.Errorf("no transport %q for device %q", def.Transport, def.ID)
			}
			if err := e.ensureConnected(ctx, tr, endpoint); err != nil {
				return warnings, fmt.Errorf("connect %q: %w", endpoint, err)
			}
			send := func(ev transport.Event) error {
				return tr.Send(ctx, endpoint, ev)
			}

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
				events, err := renderControl(def, c, d.ControlChannel(), resolved)
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

			// Record desired-state for the ordinary controls we applied. USB
			// blobs are not desired-state (they are captured, not "set").
			for _, name := range names {
				e.state.Set(logical, name, controls[name])
			}
		}

		// USB-memory blobs: write each captured blob back over the device's USB
		// connection (state the control surface cannot reach, e.g. an SL-2
		// slicer pattern). These are gated; with writes disabled the rest of the
		// scene still recalls and the skipped blob is reported as a warning
		// rather than aborting the recall.
		for _, p := range patches {
			if !d.HasUSB() {
				return warnings, fmt.Errorf("scene usb patch for %q has no usb connection; bind it with transport usbmidi/usbhid", logical)
			}
			if !e.usbWritesAllowed(d) {
				warnings = append(warnings, fmt.Sprintf("skipped usb patch for %q: usb writes disabled (set usb_allow_writes and bind writable: true)", logical))
				continue
			}
			if _, err := e.USBWritePatch(ctx, logical, p, false); err != nil {
				return warnings, fmt.Errorf("usb patch %q: %w", logical, err)
			}
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
