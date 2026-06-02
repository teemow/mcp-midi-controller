package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// renderControl turns a validated control value into the transport event(s)
// that realize it on the wire. The MIDI channel comes from the binding (not the
// definition); for OSC it is ignored. Most controls render to a single event,
// but NRPN expands to the standard four-CC sequence.
//
// Errors returned here are device-level configuration problems (a control whose
// definition lacks the address its type needs, or a parametric control invoked
// without a number) and are wrapped as *device.ValidationError so the MCP layer
// surfaces them on the offending value path.
func renderControl(def *device.Definition, c *device.Control, channel int, r device.Resolved) ([]transport.Event, error) {
	ch := channel & 0x0F
	switch c.Type {
	case device.ControlCC:
		num, err := addressNumber(c, r, "cc", 127)
		if err != nil {
			return nil, err
		}
		v, err := dataByte(r.Int)
		if err != nil {
			return nil, err
		}
		return []transport.Event{midiEvent(ch, []byte{statusByte(0xB0, ch), byte(num), v})}, nil

	case device.ControlProgramChange:
		prog := r.Int
		if c.Program != nil && !c.Parametric {
			prog = *c.Program
		}
		p, err := dataByte(prog)
		if err != nil {
			return nil, err
		}
		return []transport.Event{midiEvent(ch, []byte{statusByte(0xC0, ch), p})}, nil

	case device.ControlNRPN:
		param, err := addressNumber(c, r, "nrpn", 16383)
		if err != nil {
			return nil, err
		}
		if r.Int < 0 || r.Int > 16383 {
			return nil, &device.ValidationError{Pointer: "/value", Msg: "NRPN value must be in [0, 16383]"}
		}
		return nrpnEvents(ch, param, r.Int), nil

	case device.ControlSysEx:
		data, err := renderSysEx(c.SysEx, r.Int)
		if err != nil {
			return nil, err
		}
		// SysEx carries no channel; emit the raw F0..F7 blob.
		return []transport.Event{{Kind: transport.MIDIEvent, Data: data}}, nil

	case device.ControlNoteOn, device.ControlNoteOff:
		note, err := addressNumber(c, r, "note", 127)
		if err != nil {
			return nil, err
		}
		vel, err := dataByte(r.Int)
		if err != nil {
			return nil, err
		}
		status := byte(0x90)
		if c.Type == device.ControlNoteOff {
			status = 0x80
		}
		return []transport.Event{midiEvent(ch, []byte{statusByte(status, ch), byte(note), vel})}, nil

	case device.ControlOSC:
		if c.Address == "" {
			return nil, &device.ValidationError{Pointer: "/control", Msg: "osc control is missing an address"}
		}
		return []transport.Event{{Kind: transport.OSCEvent, OSCAddr: c.Address, OSCArgs: []any{oscArg(r)}}}, nil

	default:
		return nil, &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("unsupported control type %q", c.Type)}
	}
}

// addressNumber resolves the CC/NRPN/note address for a control: the parametric
// number supplied at call time, else the fixed value from the definition.
func addressNumber(c *device.Control, r device.Resolved, kind string, max int) (int, error) {
	var num int
	switch {
	case c.Parametric:
		if !r.HasNumber {
			return 0, &device.ValidationError{
				Pointer: "/value",
				Msg:     fmt.Sprintf("parametric %s control needs an object {\"number\": N, \"value\": V}", kind),
			}
		}
		num = r.Number
	case kind == "nrpn" && c.NRPN != nil:
		num = *c.NRPN
	case c.CC != nil: // cc and note controls both carry the number in CC
		num = *c.CC
	default:
		return 0, &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("%s control has no number", kind)}
	}
	if num < 0 || num > max {
		ptr := "/control"
		if c.Parametric {
			ptr = "/value/number"
		}
		return 0, &device.ValidationError{Pointer: ptr, Msg: fmt.Sprintf("%s number must be in [0, %d]", kind, max)}
	}
	return num, nil
}

// nrpnEvents builds the standard NRPN sequence: select the parameter (CC 99/98)
// then send the 14-bit data entry (CC 6/38).
func nrpnEvents(ch, param, value int) []transport.Event {
	status := statusByte(0xB0, ch)
	cc := func(controller, v int) transport.Event {
		return midiEvent(ch, []byte{status, byte(controller), byte(v & 0x7F)})
	}
	return []transport.Event{
		cc(99, (param>>7)&0x7F),
		cc(98, param&0x7F),
		cc(6, (value>>7)&0x7F),
		cc(38, value&0x7F),
	}
}

// renderSysEx parses a whitespace-separated hex template (e.g. "F0 7D 01 %v
// F7"), substituting "%v" with the wire value, and returns the message bytes.
func renderSysEx(template string, value int) ([]byte, error) {
	if strings.TrimSpace(template) == "" {
		return nil, &device.ValidationError{Pointer: "/control", Msg: "sysex control is missing a template"}
	}
	if value < 0 || value > 127 {
		return nil, &device.ValidationError{Pointer: "/value", Msg: "sysex value byte must be in [0, 127]"}
	}
	fields := strings.Fields(template)
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		if f == "%v" {
			out = append(out, byte(value))
			continue
		}
		b, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(f), "0x"), 16, 8)
		if err != nil {
			return nil, &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("invalid sysex byte %q in template", f)}
		}
		out = append(out, byte(b))
	}
	return out, nil
}

// oscArg maps a resolved value to its OSC argument type (int32 / float32 /
// string), matching the X32's expectations.
func oscArg(r device.Resolved) any {
	switch r.Type {
	case device.ValueFloat:
		return float32(r.Float)
	case device.ValueString:
		return r.Str
	default:
		return int32(r.Int)
	}
}

func midiEvent(ch int, data []byte) transport.Event {
	return transport.Event{Kind: transport.MIDIEvent, Channel: ch, Data: data}
}

// statusByte applies the 0-15 channel to a channel-voice status nibble.
func statusByte(status byte, ch int) byte {
	return (status & 0xF0) | byte(ch&0x0F)
}

func dataByte(v int) (byte, error) {
	if v < 0 || v > 127 {
		return 0, &device.ValidationError{Pointer: "/value", Msg: "MIDI data byte must be in [0, 127]"}
	}
	return byte(v), nil
}
