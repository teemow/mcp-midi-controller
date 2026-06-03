package engine

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Binding maps a transport endpoint + MIDI channel to a device definition,
// producing a named logical device. One endpoint (e.g. a WIDI Thru6 hub) can
// host several logical devices on different channels.
//
// Bindings are persisted (bindings.yaml) so the daemon restores the rig on
// restart. Adding/removing a binding generates/removes the corresponding
// control_<logical> MCP tool and emits tools/list_changed.
type Binding struct {
	// Logical is the logical device name; it names the generated tool
	// (control_<logical>) and is the key used by scenes and desired-state.
	Logical string `yaml:"logical"`

	// Endpoint is the transport endpoint id (BLE address/name, ALSA port,
	// USB-HID "VID:PID", or OSC host:port).
	Endpoint string `yaml:"endpoint"`

	// Channel is the MIDI channel (0-15). Ignored for OSC and for USB bindings
	// (the USB editor protocols are addressed, not channel-scoped).
	Channel int `yaml:"channel"`

	// DeviceID is the device definition id this endpoint+channel speaks.
	DeviceID string `yaml:"device"`

	// Transport optionally overrides the backend this binding uses. When empty
	// the binding speaks the device's control transport (def.Transport) — the
	// fire-and-forget control surface. Set to a USB transport (usbmidi/usbhid)
	// to select the device's USB editor/readback surface instead (def.USB),
	// producing a USB binding. One physical device can therefore carry both a
	// control binding and a USB binding (same device id, different logical
	// names and transports). See engine.Bind and docs/usb-tools.md.
	Transport string `yaml:"transport,omitempty"`

	// Writable opts a USB binding in to write tools (set_param, write_pattern,
	// recall_pattern, select_preset). It is the per-binding half of the write
	// gate: writes are only ever exposed when the daemon's usb_allow_writes is
	// on AND the binding is writable. It is meaningless (ignored) for control
	// bindings. Default false keeps a USB binding read-only.
	Writable bool `yaml:"writable,omitempty"`
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
