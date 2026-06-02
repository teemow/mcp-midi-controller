package device

import "fmt"

// Definition is a declarative description of a controllable device. It is the
// primary extension mechanism: adding a device means writing one of these (no
// Go code). The definition also doubles as the validation schema for the
// device's generated MCP tool.
//
// Note: the MIDI channel is intentionally NOT part of the definition. It is
// supplied by a binding, so a single definition can be bound on different
// channels (e.g. multiple pedals behind one WIDI Thru6 hub).
type Definition struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Manufacturer string `yaml:"manufacturer,omitempty"`
	Description  string `yaml:"description,omitempty"`

	// Transport is the backend id this device speaks: blemidi | osc | usbmidi.
	Transport string `yaml:"transport"`

	// SettleMS is an optional delay applied after a program change before CC
	// messages are sent during scene recall (some pedals need it).
	SettleMS int `yaml:"settle_ms,omitempty"`

	Controls []Control `yaml:"controls"`
}

// Control returns the named control, if present.
func (d *Definition) Control(name string) (*Control, bool) {
	for i := range d.Controls {
		if d.Controls[i].Name == name {
			return &d.Controls[i], true
		}
	}
	return nil, false
}

// ControlNames returns the control names in declaration order. Used to build
// the control-name enum in the generated MCP tool's input schema.
func (d *Definition) ControlNames() []string {
	names := make([]string, len(d.Controls))
	for i := range d.Controls {
		names[i] = d.Controls[i].Name
	}
	return names
}

// Validate checks the definition for internal consistency.
//
// TODO: flesh out (unique control names, addressing matches type, enum/range
// coherence, transport is known, etc.).
func (d *Definition) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("device definition: missing id")
	}
	if d.Transport == "" {
		return fmt.Errorf("device %q: missing transport", d.ID)
	}
	seen := map[string]bool{}
	for _, c := range d.Controls {
		if c.Name == "" {
			return fmt.Errorf("device %q: control with empty name", d.ID)
		}
		if seen[c.Name] {
			return fmt.Errorf("device %q: duplicate control %q", d.ID, c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}
