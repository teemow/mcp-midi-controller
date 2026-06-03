package device

// ControlType is the wire encoding used to emit a control's value.
type ControlType string

const (
	ControlCC            ControlType = "cc"
	ControlProgramChange ControlType = "program_change"
	ControlNRPN          ControlType = "nrpn"
	ControlSysEx         ControlType = "sysex"
	ControlOSC           ControlType = "osc"
	ControlNoteOn        ControlType = "note_on"
	ControlNoteOff       ControlType = "note_off"
)

// ValueType describes how a control's accepted values are specified.
type ValueType string

const (
	ValueRange  ValueType = "range"  // integer min..max (default 0..127 for CC)
	ValueEnum   ValueType = "enum"   // named labels mapped to wire values
	ValueInt    ValueType = "int"    // arbitrary integer with optional bounds
	ValueFloat  ValueType = "float"  // float (typical for OSC), optional bounds
	ValueString ValueType = "string" // free string payload (e.g. OSC name fields)
)

// ValueSpec constrains and maps the values a control accepts. It doubles as the
// per-control validation schema surfaced through the generated MCP tool.
type ValueSpec struct {
	Type ValueType `yaml:"type"`

	Min  *float64 `yaml:"min,omitempty"`
	Max  *float64 `yaml:"max,omitempty"`
	Step *float64 `yaml:"step,omitempty"`

	// Unit is a human unit hint (e.g. "dB", "ms") for display only.
	Unit string `yaml:"unit,omitempty"`

	// Values maps enum labels to their wire values (used when Type == enum).
	Values map[string]int `yaml:"values,omitempty"`
}

// Control is a single addressable parameter on a device.
//
// Exactly one addressing field is meaningful per Type:
//   - cc:              CC
//   - nrpn:            NRPN
//   - program_change:  Program (optional; usually supplied as the value)
//   - sysex:           SysEx (hex template; "%v" = wire value, "[ ] %k" =
//     Roland address-based checksum region — see renderSysEx)
//   - osc:             Address
//   - note_on/off:     CC reused as the note number, or supplied as the value
type Control struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	Type        ControlType `yaml:"type"`

	CC      *int   `yaml:"cc,omitempty"`
	NRPN    *int   `yaml:"nrpn,omitempty"`
	Program *int   `yaml:"program,omitempty"`
	SysEx   string `yaml:"sysex,omitempty"`
	Address string `yaml:"address,omitempty"`

	// Parametric marks a control whose address number (CC/NRPN/program) is
	// supplied by the caller at invocation time rather than fixed here. Used by
	// the built-in generic-midi definition so any unmodeled device is still
	// controllable by raw number.
	Parametric bool `yaml:"parametric,omitempty"`

	Value ValueSpec `yaml:"value"`
}
