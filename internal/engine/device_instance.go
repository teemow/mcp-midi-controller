package engine

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/teemow/midi-device/device"
	"gopkg.in/yaml.v3"
)

// Device is the central controllable instance: one named device of a device
// *type* (DeviceID), plus where it is. "Where it is" is a set of transport
// connections — there is no "control surface" vs "USB surface" split and no
// per-instance transport override. Which transport(s) a device speaks is a
// property of its device type; an instance only supplies the address(es).
//
// The common single-transport device uses the flat Endpoint/Channel shorthand,
// which is the device's single connection on its device type's transport. A
// device that speaks several transports (e.g. a Boss SL-2: BLE for live CC and a
// USB editor port for deep memory) lists each in Connections (transport id ->
// endpoint/channel). The device type knows which parameter travels over which
// transport, so the engine routes each parameter internally: control parameters
// over the type's transport, addressed-memory (USB) parameters over the type's
// USB transport.
//
// Devices are persisted (devices.yaml) so the daemon restores the rig on
// restart. Adding/removing a device generates/removes the corresponding MCP
// tools and emits tools/list_changed.
type Device struct {
	// Name is the device's instance name; it names the generated tools
	// (control_<name>, <name>_*) and is the key used by scenes and
	// desired-state. It identifies one controllable instance, as opposed to
	// DeviceID (the device *type*).
	Name string `yaml:"name"`

	// DeviceID is the device type id this device is an instance of.
	DeviceID string `yaml:"type"`

	// Endpoint / Channel are the flat single-connection shorthand: the address
	// of the device's single connection on its device type's transport. They
	// are mutually exclusive with Connections (use one or the other).
	Endpoint string `yaml:"endpoint,omitempty"`
	Channel  int    `yaml:"channel,omitempty"`

	// Connections lists each transport the device is reachable on, keyed by
	// transport id (blemidi | osc | usbmidi | usbhid | auv3midi). It is used for
	// multi-transport devices; a single-transport device uses the flat
	// Endpoint/Channel shorthand instead.
	Connections map[string]Connection `yaml:"connections,omitempty"`

	// Session tags a device auto-created by an AUM session import with the
	// staged session id it derives from. A new import removes every
	// session-tagged device before creating the new session's rig, so imports
	// replace each other instead of piling up. Empty for hand-bound devices
	// (which imports never touch).
	Session string `yaml:"session,omitempty"`
}

// Connection is one transport address of a device: where the device is reachable
// over a given transport. Writable is the per-connection half of the two-key USB
// write gate (it only applies to a USB connection).
type Connection struct {
	// Endpoint is the transport endpoint id (BLE address/name, ALSA port, OSC
	// host:port, or "VID:PID"/hidraw path for usbhid).
	Endpoint string `yaml:"endpoint,omitempty"`

	// Channel is the MIDI channel (0-15). Ignored for OSC/USB.
	Channel int `yaml:"channel,omitempty"`

	// Writable opts a USB connection in to write tools (set_param,
	// write_pattern, recall_pattern, select_preset) and engine-level USB writes
	// (patch-level scene recall). It is the per-device half of the two-key write
	// gate: writes are only ever performed when the daemon's usb_allow_writes is
	// on AND this is set. Default false = read-only.
	Writable bool `yaml:"writable,omitempty"`
}

// isUSBTransportID reports whether a transport id is a USB editor/readback
// transport (as opposed to a control transport like blemidi/osc/auv3midi). It is
// how the connection accessors tell a device's control connection from its USB
// connection without consulting the device type.
func isUSBTransportID(id string) bool {
	return id == device.USBTransportMIDI || id == device.USBTransportHID
}

// HasControl reports whether the device has a control connection (a connection
// on a non-USB transport, or the flat shorthand).
func (d Device) HasControl() bool {
	if len(d.Connections) == 0 {
		return true // the flat shorthand is the control connection
	}
	for tr := range d.Connections {
		if !isUSBTransportID(tr) {
			return true
		}
	}
	return false
}

// HasUSB reports whether the device carries a USB editor/readback connection.
func (d Device) HasUSB() bool {
	for tr := range d.Connections {
		if isUSBTransportID(tr) {
			return true
		}
	}
	return false
}

// ControlEndpoint is the control connection's endpoint, or "" when the device
// has no control connection (a USB-only device).
func (d Device) ControlEndpoint() string {
	c, ok := d.controlConn()
	if !ok {
		return ""
	}
	return c.Endpoint
}

// ControlChannel is the control connection's MIDI channel, or 0 when the device
// has no control connection.
func (d Device) ControlChannel() int {
	c, ok := d.controlConn()
	if !ok {
		return 0
	}
	return c.Channel
}

// controlConn resolves the device's control connection: the flat shorthand for a
// single-transport device, otherwise the connection on its (single) non-USB
// transport.
func (d Device) controlConn() (Connection, bool) {
	if len(d.Connections) == 0 {
		return Connection{Endpoint: d.Endpoint, Channel: d.Channel}, true
	}
	for tr, c := range d.Connections {
		if !isUSBTransportID(tr) {
			return c, true
		}
	}
	return Connection{}, false
}

// USBConnection resolves the device's USB editor connection: the transport id
// and connection of its usbmidi/usbhid connection, if any.
func (d Device) USBConnection() (string, Connection, bool) {
	for _, tr := range []string{device.USBTransportMIDI, device.USBTransportHID} {
		if c, ok := d.Connections[tr]; ok {
			return tr, c, true
		}
	}
	return "", Connection{}, false
}

// USBWritable reports the per-device half of the two-key USB write gate: a USB
// connection that has opted in to writes. It is only one key — callers must
// still AND it with the daemon's master usb_allow_writes gate before performing
// a write (see usbWritesAllowed).
func (d Device) USBWritable() bool {
	_, c, ok := d.USBConnection()
	return ok && c.Writable
}

// ConnectionsMap returns the device's connections as a transport-keyed map,
// expanding the flat shorthand against primary (the device type's transport). It
// is used by the bind path to merge a new connection while preserving the
// others.
func (d Device) ConnectionsMap(primary string) map[string]Connection {
	out := map[string]Connection{}
	if len(d.Connections) > 0 {
		for k, v := range d.Connections {
			out[k] = v
		}
		return out
	}
	out[primary] = Connection{Endpoint: d.Endpoint, Channel: d.Channel}
	return out
}

// WithConnections returns a copy of d carrying the given transport-keyed
// connections, collapsing to the flat shorthand when there is exactly one
// connection on the primary (device type) transport and using the Connections
// map otherwise.
func (d Device) WithConnections(primary string, m map[string]Connection) Device {
	d.Endpoint, d.Channel, d.Connections = "", 0, nil
	if len(m) == 1 {
		if c, ok := m[primary]; ok {
			d.Endpoint, d.Channel = c.Endpoint, c.Channel
			return d
		}
	}
	d.Connections = m
	return d
}

// LoadDevicesFile reads devices.yaml (a top-level YAML sequence of devices,
// matching the documented rig-as-code format). A missing file is not an error:
// it returns no devices so a fresh install starts empty.
func LoadDevicesFile(path string) ([]Device, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Device
	if err := yaml.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveDevicesFile persists devices to path (creating the parent dir), sorted by
// logical name for stable, diff-friendly output.
func SaveDevicesFile(path string, devices []Device) error {
	sorted := append([]Device(nil), devices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	b, err := yaml.Marshal(sorted)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
