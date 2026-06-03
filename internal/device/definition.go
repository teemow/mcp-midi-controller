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

	// USB is the optional USB editor/readback profile. It describes the device's
	// USB read/write surface (a different protocol shape than the fire-and-forget
	// control surface above) and is consumed by the USB binding kind. A nil USB
	// means the device has no USB surface. See usb.go and docs/usb-tools.md.
	USB *USBProfile `yaml:"usb,omitempty"`
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

// Validate checks the definition for internal consistency: it must have an id
// and transport, control names must be unique and non-empty, and each control's
// addressing must match its type (cc needs a CC number, osc needs an address,
// sysex needs a template, etc.) unless the control is parametric (the address
// number is supplied at call time). It does not check that the transport is one
// the engine has registered — that is enforced at bind time.
func (d *Definition) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("device definition: missing id")
	}
	if d.Transport == "" {
		return fmt.Errorf("device %q: missing transport", d.ID)
	}
	seen := map[string]bool{}
	for i := range d.Controls {
		c := &d.Controls[i]
		if c.Name == "" {
			return fmt.Errorf("device %q: control with empty name", d.ID)
		}
		if seen[c.Name] {
			return fmt.Errorf("device %q: duplicate control %q", d.ID, c.Name)
		}
		seen[c.Name] = true
		if err := c.validate(); err != nil {
			return fmt.Errorf("device %q, control %q: %w", d.ID, c.Name, err)
		}
	}
	if d.USB != nil {
		if err := d.USB.validate(d.ID); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one control's addressing and value spec coherence.
func (c *Control) validate() error {
	switch c.Type {
	case ControlCC:
		if !c.Parametric && c.CC == nil {
			return fmt.Errorf("cc control needs a cc number (or parametric: true)")
		}
	case ControlNRPN:
		if !c.Parametric && c.NRPN == nil {
			return fmt.Errorf("nrpn control needs an nrpn number (or parametric: true)")
		}
	case ControlNoteOn, ControlNoteOff:
		if !c.Parametric && c.CC == nil {
			return fmt.Errorf("%s control needs a cc field for the note number (or parametric: true)", c.Type)
		}
	case ControlSysEx:
		if c.SysEx == "" {
			return fmt.Errorf("sysex control needs a sysex template")
		}
	case ControlOSC:
		if c.Address == "" {
			return fmt.Errorf("osc control needs an address")
		}
	case ControlProgramChange:
		// program is optional: the value supplies the program number.
	case "":
		return fmt.Errorf("control has no type")
	default:
		return fmt.Errorf("unknown control type %q", c.Type)
	}
	return c.Value.validate()
}

// validate checks a value spec's bounds/enum coherence.
func (v *ValueSpec) validate() error {
	switch v.Type {
	case ValueEnum:
		if len(v.Values) == 0 {
			return fmt.Errorf("enum value spec has no values")
		}
	case ValueRange, ValueInt, ValueFloat, ValueString, "":
		if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
			return fmt.Errorf("value spec min %g is greater than max %g", *v.Min, *v.Max)
		}
	default:
		return fmt.Errorf("unknown value type %q", v.Type)
	}
	return nil
}
