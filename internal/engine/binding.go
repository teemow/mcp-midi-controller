package engine

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Binding maps a device definition to one named logical device that may expose
// two surfaces of the SAME physical device at once:
//
//   - a control surface (Endpoint + Channel over the device's control
//     transport) — the fire-and-forget CC/PC/note path that generates the
//     control_<logical> tool, and
//   - an optional USB editor/readback surface (USB) — the addressed
//     editor protocol (def.USB) over usbmidi/usbhid that generates the USB tool
//     family (usb_read, <logical>_get_param, capture_usb_patch, ...).
//
// One endpoint (e.g. a WIDI Thru6 hub) can host several logical devices on
// different channels; a single logical device can carry both surfaces so the
// control tools and the USB tools refer to one pedal under one name.
//
// Bindings are persisted (bindings.yaml) so the daemon restores the rig on
// restart. Adding/removing a binding generates/removes the corresponding MCP
// tools and emits tools/list_changed.
type Binding struct {
	// Logical is the logical device name; it names the generated tools
	// (control_<logical>, <logical>_*) and is the key used by scenes and
	// desired-state.
	Logical string `yaml:"logical"`

	// Endpoint is the control-surface transport endpoint id (BLE address/name,
	// ALSA port, or OSC host:port). Empty means the logical has no control
	// surface (USB-only).
	Endpoint string `yaml:"endpoint,omitempty"`

	// Channel is the control-surface MIDI channel (0-15). Ignored for OSC.
	Channel int `yaml:"channel,omitempty"`

	// DeviceID is the device definition id this binding speaks.
	DeviceID string `yaml:"device"`

	// Transport optionally overrides the control-surface backend. When empty
	// the control surface uses the device's control transport (def.Transport).
	Transport string `yaml:"transport,omitempty"`

	// USB is the optional editor/readback surface for the same physical device.
	// When set, the device definition must carry a USB profile (def.USB) whose
	// transport matches USB.Transport. Absent for control-only devices.
	USB *USBSurface `yaml:"usb,omitempty"`
}

// USBSurface describes the USB editor/readback surface of a logical device: the
// usbmidi/usbhid transport and endpoint the addressed editor protocol (def.USB)
// runs over, plus the per-binding write opt-in. It is the USB half of a
// Binding; a logical device that has one exposes the USB tool family alongside
// (or instead of) its control_<logical> tool.
type USBSurface struct {
	// Transport is the USB transport id (usbmidi | usbhid). It must match the
	// device definition's usb profile transport.
	Transport string `yaml:"transport"`

	// Endpoint is the USB transport endpoint (ALSA rawmidi port substring for
	// usbmidi, "VID:PID" for usbhid). Empty falls back to the profile's
	// default endpoint (def.USB.Endpoint).
	Endpoint string `yaml:"endpoint,omitempty"`

	// Writable opts the USB surface in to write tools (set_param,
	// write_pattern, recall_pattern, select_preset) and engine-level USB writes
	// (patch-level scene recall). It is the per-binding half of the two-key
	// write gate: writes are only ever performed when the daemon's
	// usb_allow_writes is on AND this is set. Default false = read-only.
	Writable bool `yaml:"writable,omitempty"`
}

// HasControl reports whether the binding exposes a fire-and-forget control
// surface. Every binding does except a USB-only one (a USB surface with no
// control endpoint). A control binding may legitimately have an empty endpoint
// (e.g. an OSC device awaiting its host:port), so the test is "not USB-only"
// rather than "endpoint set".
func (b Binding) HasControl() bool { return b.USB == nil || b.Endpoint != "" }

// HasUSB reports whether the binding carries a USB editor/readback surface.
func (b Binding) HasUSB() bool { return b.USB != nil }

// USBWritable reports the per-binding half of the two-key USB write gate: a USB
// surface that has opted in to writes. It is only one key — callers must still
// AND it with the daemon's master usb_allow_writes gate before performing a
// write (see usbWritesAllowed).
func (b Binding) USBWritable() bool { return b.USB != nil && b.USB.Writable }

// normalizeBinding migrates a legacy USB binding — one whose top-level
// Transport named a USB transport (the pre-USBSurface "separate logical name"
// model) — into the current shape where the USB surface lives in USB. New
// bindings already carry USB and pass through unchanged.
func normalizeBinding(b Binding) Binding {
	if b.USB == nil && isUSBTransport(b.Transport) {
		b.USB = &USBSurface{Transport: b.Transport, Endpoint: b.Endpoint}
		b.Transport = ""
		b.Endpoint = ""
		b.Channel = 0
	}
	return b
}

// Bindings is the persisted set of bindings.
type Bindings struct {
	Bindings []Binding `yaml:"bindings"`
}

// LoadBindingsFile reads bindings.yaml (a top-level YAML sequence of bindings,
// matching the documented rig-as-code format). A missing file is not an error:
// it returns no bindings so a fresh install starts empty.
func LoadBindingsFile(path string) ([]Binding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Binding
	if err := yaml.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveBindingsFile persists bindings to path (creating the parent dir), sorted
// by logical name for stable, diff-friendly output.
func SaveBindingsFile(path string, bindings []Binding) error {
	sorted := append([]Binding(nil), bindings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Logical < sorted[j].Logical })
	b, err := yaml.Marshal(sorted)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
