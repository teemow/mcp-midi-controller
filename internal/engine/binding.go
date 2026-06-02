package engine

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
	// or OSC host:port).
	Endpoint string `yaml:"endpoint"`

	// Channel is the MIDI channel (0-15). Ignored for OSC.
	Channel int `yaml:"channel"`

	// DeviceID is the device definition id this endpoint+channel speaks.
	DeviceID string `yaml:"device"`
}

// Bindings is the persisted set of bindings.
type Bindings struct {
	Bindings []Binding `yaml:"bindings"`
}
