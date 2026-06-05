package engine

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/scene"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// defaultOSCPort is the X32's OSC/UDP control port, used when a binding's OSC
// endpoint omits an explicit port (X-Air/XR would be 10024 — a different
// protocol surface we do not target here).
const defaultOSCPort = 10023

// FootswitchEvent is one outgoing wire event in the on-device scene schema the
// "three" BLE-MIDI footswitch parses. The footswitch is a faithful player: it
// emits these in order and honours delay_ms *after* each send, so this is where
// recall ordering and per-device settle windows are baked in. Field set mirrors
// ble-midi-footswitch/data/scenes/README.md.
type FootswitchEvent struct {
	Type       string `json:"type"`
	Channel    int    `json:"channel,omitempty"`
	Controller *int   `json:"controller,omitempty"`
	Value      *int   `json:"value,omitempty"`
	Program    *int   `json:"program,omitempty"`
	Note       *int   `json:"note,omitempty"`
	Velocity   *int   `json:"velocity,omitempty"`
	Bytes      []int  `json:"bytes,omitempty"`

	// OSC fields (Type == "osc"): an OSC/UDP message the footswitch sends to a
	// mixer such as the X32 during replay. Host/Port are the UDP target
	// resolved from the device's binding endpoint at compile time, so the
	// scene is self-describing and the footswitch needs no rig config. OSCTypes
	// is the OSC type-tag string (one rune per arg: 'f' float32, 'i' int32,
	// 's' string); it is carried explicitly because JSON numbers are
	// type-ambiguous (the X32 needs to know float 1.0 from int 1).
	OSCAddr  string `json:"osc_addr,omitempty"`
	OSCTypes string `json:"osc_types,omitempty"`
	OSCArgs  []any  `json:"osc_args,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`

	DelayMs int `json:"delay_ms,omitempty"`
}

// FootswitchTrigger is how AUM selects this scene over the inbound MIDI link
// (one message from AUM -> the footswitch replays the scene). Type is the
// firmware vocabulary: program_change | control_change | note_on.
type FootswitchTrigger struct {
	Type    string `json:"type"`
	Channel int    `json:"channel"`
	Number  int    `json:"number"`
	Value   *int   `json:"value,omitempty"`
}

// FootswitchScene is the compiled, pushable unit (a scene as the footswitch
// stores it). It marshals to exactly the JSON the device expects.
//
// It is a one-way, derived export of a scene.Scene, never a source of truth: a
// scene.Scene holds the rig's parameter settings keyed by device (and names no
// transport), while CompileFootswitchScene flattens one scene into the ordered
// wire events a dumb BLE footswitch replays. The footswitch cannot realize a
// device's USB-memory blob (a USBPatch parameter value), so those are dropped
// during compile with a warning — they only recall through the live engine path
// (RecallScene), which writes them over the device's USB connection.
type FootswitchScene struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Bank    int                `json:"bank,omitempty"`
	Trigger *FootswitchTrigger `json:"trigger,omitempty"`
	Events  []FootswitchEvent  `json:"events"`
}

// FootswitchCompileOptions carries the footswitch-specific metadata that the
// server-side scene model does not itself hold (trigger, bank, file id).
type FootswitchCompileOptions struct {
	ID      string             // file stem; defaults to a sanitised scene name
	Bank    int                // matrix display digit 1..9 (0 = list position)
	Trigger *FootswitchTrigger // inbound match; nil = foot-only scene
}

// CompileFootswitchScene turns a stored scene into the footswitch's on-device
// schema, resolving recall semantics at compile time so the device stays dumb:
//
//   - each bound MIDI device's controls are rendered to wire events,
//   - program changes are ordered before the device's other events,
//   - the device's settle_ms is baked as a delay after its last program change,
//   - NRPN expands to its four-CC sequence, SysEx is emitted verbatim,
//   - OSC devices (e.g. the X32) render to "osc" events carrying the address,
//     args, type-tag and the UDP target resolved from their binding endpoint,
//     which the footswitch sends over WiFi during replay.
//
// Devices that are not bound cause an error (the channel — or, for OSC, the
// host:port — is only known from the binding). Returns the compiled scene and
// any non-fatal warnings.
func (e *Engine) CompileFootswitchScene(sc *scene.Scene, opts FootswitchCompileOptions) (*FootswitchScene, []string, error) {
	if sc == nil {
		return nil, nil, fmt.Errorf("nil scene")
	}

	// Snapshot the devices so we do not hold the lock across rendering.
	e.mu.RLock()
	devices := make(map[string]Device, len(e.devices))
	for k, v := range e.devices {
		devices[k] = v
	}
	e.mu.RUnlock()

	out := &FootswitchScene{
		ID:      opts.ID,
		Name:    sc.Name,
		Bank:    opts.Bank,
		Trigger: opts.Trigger,
		Events:  []FootswitchEvent{},
	}
	if out.ID == "" {
		out.ID = sanitizeSceneID(sc.Name)
	}

	var warnings []string

	logicals := make([]string, 0, len(sc.Devices))
	for l := range sc.Devices {
		logicals = append(logicals, l)
	}
	sort.Strings(logicals)

	for _, logical := range logicals {
		controls := sc.Devices[logical]
		if len(controls) == 0 {
			continue
		}
		d, ok := devices[logical]
		if !ok {
			return nil, nil, fmt.Errorf("scene references unbound device %q; bind it (bind_device) so its channel is known", logical)
		}
		def, ok := e.registry.Get(d.DeviceID)
		if !ok {
			return nil, nil, fmt.Errorf("device %q: unknown definition %q", logical, d.DeviceID)
		}
		// OSC devices need the UDP target from the control endpoint; warn (and
		// skip the device) rather than emit unsendable OSC events if it is
		// missing or malformed.
		var oscHost string
		var oscPort int
		if def.Transport == "osc" {
			oscHost, oscPort = splitOSCEndpoint(d.ControlEndpoint())
			if oscHost == "" {
				warnings = append(warnings, fmt.Sprintf("skipped %q: OSC device has no host in its control endpoint %q (bind it to host:port, e.g. 192.168.1.50:%d)", logical, d.ControlEndpoint(), defaultOSCPort))
				continue
			}
		}

		var pcs, rest []FootswitchEvent

		// Deterministic control order. USB-memory blobs (USBPatch parameter
		// values) cannot be expressed as footswitch wire events — the footswitch
		// only speaks MIDI/OSC, not the device's USB editor protocol — so they
		// are skipped here with a warning and recall only through RecallScene.
		names := make([]string, 0, len(controls))
		var skippedPatch bool
		for n, v := range controls {
			if _, ok := scene.AsUSBPatch(v); ok {
				skippedPatch = true
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		if skippedPatch {
			warnings = append(warnings, fmt.Sprintf("skipped usb memory patch for %q: the footswitch cannot realize USB memory blobs (recall the scene live instead)", logical))
		}

		for _, name := range names {
			c, ok := def.Control(name)
			if !ok {
				return nil, nil, fmt.Errorf("device %q (%s) has no control %q", logical, def.ID, name)
			}
			resolved, err := device.Resolve(c, controls[name])
			if err != nil {
				return nil, nil, fmt.Errorf("%s.%s: %w", logical, name, err)
			}
			events, err := renderControl(def, c, d.ControlChannel(), resolved)
			if err != nil {
				return nil, nil, fmt.Errorf("%s.%s: %w", logical, name, err)
			}
			for _, ev := range events {
				fe, isPC, err := eventToFootswitch(ev, oscHost, oscPort)
				if err != nil {
					return nil, nil, fmt.Errorf("%s.%s: %w", logical, name, err)
				}
				if isPC {
					pcs = append(pcs, fe)
				} else {
					rest = append(rest, fe)
				}
			}
		}

		// Bake per-device settle as a delay after the last program change, so
		// the device has time to load its preset before the CC overrides land.
		if def.SettleMS > 0 && len(pcs) > 0 && len(rest) > 0 {
			pcs[len(pcs)-1].DelayMs = def.SettleMS
		}
		out.Events = append(out.Events, pcs...)
		out.Events = append(out.Events, rest...)
	}

	return out, warnings, nil
}

// eventToFootswitch converts a rendered transport event into the footswitch
// schema, returning whether it is a program change (for ordering). MIDI events
// become their typed wire form; OSC events become an "osc" event addressed to
// the OSC host:port resolved from the device's binding. OSC events never sort
// as program changes.
func eventToFootswitch(ev transport.Event, oscHost string, oscPort int) (FootswitchEvent, bool, error) {
	switch ev.Kind {
	case transport.MIDIEvent:
		return midiToFootswitch(ev)
	case transport.OSCEvent:
		if oscHost == "" {
			return FootswitchEvent{}, false, fmt.Errorf("OSC event has no UDP target (binding endpoint must be host:port)")
		}
		return FootswitchEvent{
			Type:     "osc",
			OSCAddr:  ev.OSCAddr,
			OSCTypes: oscTypeTag(ev.OSCArgs),
			OSCArgs:  ev.OSCArgs,
			Host:     oscHost,
			Port:     oscPort,
		}, false, nil
	default:
		return FootswitchEvent{}, false, fmt.Errorf("unsupported transport event kind for a footswitch scene")
	}
}

// oscTypeTag derives the OSC type-tag (one rune per arg) from the rendered
// argument Go types produced by renderControl's oscArg: float32 -> 'f',
// string -> 's', everything else (int32) -> 'i'. The footswitch uses it to
// encode each arg with the right OSC type, which JSON alone cannot disambiguate.
func oscTypeTag(args []any) string {
	var b strings.Builder
	for _, a := range args {
		switch a.(type) {
		case float32, float64:
			b.WriteByte('f')
		case string:
			b.WriteByte('s')
		default:
			b.WriteByte('i')
		}
	}
	return b.String()
}

// splitOSCEndpoint parses a binding's OSC endpoint into a UDP host and port.
// A bare host (no ":port") defaults to the X32 control port; an empty endpoint
// yields an empty host so the caller can warn and skip.
func splitOSCEndpoint(endpoint string) (string, int) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", 0
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		// No port component: treat the whole string as the host.
		return endpoint, defaultOSCPort
	}
	port, perr := strconv.Atoi(portStr)
	if perr != nil || port <= 0 || port > 65535 {
		port = defaultOSCPort
	}
	if host == "" {
		return "", 0
	}
	return host, port
}

// midiToFootswitch converts a rendered MIDI transport event into the footswitch
// schema, returning whether it is a program change (for ordering).
func midiToFootswitch(ev transport.Event) (FootswitchEvent, bool, error) {
	if ev.Kind != transport.MIDIEvent {
		return FootswitchEvent{}, false, fmt.Errorf("cannot represent a non-MIDI event in a footswitch scene")
	}
	d := ev.Data
	if len(d) == 0 {
		return FootswitchEvent{}, false, fmt.Errorf("empty MIDI event")
	}
	status := d[0]

	if status == 0xF0 { // SysEx: emit the full F0..F7 blob verbatim.
		bytes := make([]int, len(d))
		for i, b := range d {
			bytes[i] = int(b)
		}
		return FootswitchEvent{Type: "sysex", Bytes: bytes}, false, nil
	}

	ch := int(status&0x0F) + 1 // firmware channels are 1..16
	switch status & 0xF0 {
	case 0xB0: // control change
		if len(d) < 3 {
			return FootswitchEvent{}, false, fmt.Errorf("short CC event")
		}
		controller, value := int(d[1]), int(d[2])
		return FootswitchEvent{Type: "cc", Channel: ch, Controller: &controller, Value: &value}, false, nil
	case 0xC0: // program change
		if len(d) < 2 {
			return FootswitchEvent{}, false, fmt.Errorf("short program-change event")
		}
		prog := int(d[1])
		return FootswitchEvent{Type: "program_change", Channel: ch, Program: &prog}, true, nil
	case 0x90: // note on
		if len(d) < 3 {
			return FootswitchEvent{}, false, fmt.Errorf("short note-on event")
		}
		note, vel := int(d[1]), int(d[2])
		return FootswitchEvent{Type: "note_on", Channel: ch, Note: &note, Velocity: &vel}, false, nil
	case 0x80: // note off
		if len(d) < 3 {
			return FootswitchEvent{}, false, fmt.Errorf("short note-off event")
		}
		note, vel := int(d[1]), int(d[2])
		return FootswitchEvent{Type: "note_off", Channel: ch, Note: &note, Velocity: &vel}, false, nil
	default:
		return FootswitchEvent{}, false, fmt.Errorf("unsupported MIDI status 0x%02X for a footswitch scene", status)
	}
}

// sanitizeSceneID reduces a scene name to a safe file stem (the footswitch
// sanitises again, but matching it here keeps the JSON id == the filename).
func sanitizeSceneID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "scene"
	}
	return b.String()
}
