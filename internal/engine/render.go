package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
)

// renderControl turns a validated control value into the transport event(s)
// that realize it on the wire. The MIDI channel comes from the binding (not the
// definition) unless the control pins its own (Control.Channel — e.g. a
// session-derived AUM control whose mapping rides a banked channel); for OSC it
// is ignored. Most controls render to a single event, but NRPN expands to the
// standard four-CC sequence.
//
// Errors returned here are device-level configuration problems (a control whose
// definition lacks the address its type needs, or a parametric control invoked
// without a number) and are wrapped as *device.ValidationError so the MCP layer
// surfaces them on the offending value path.
func renderControl(def *device.DeviceType, c *device.Control, channel int, r device.Resolved) ([]transport.Event, error) {
	ch := c.WireChannel(channel)
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
		if c.Bank {
			return bankProgramEvents(ch, prog)
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

// bankProgramEvents realizes a banked program change: a 14-bit preset index is
// split into a Bank Select pair (CC 0 = bank MSB, CC 32 = bank LSB, where bank =
// index/128) followed by the Program Change (index % 128). This lets one
// program_change control address more than 128 presets (e.g. a synth with
// hundreds of factory presets), which a bare 7-bit PC cannot reach.
func bankProgramEvents(ch, index int) ([]transport.Event, error) {
	// A full Bank Select is 14-bit (MSB + LSB) and the Program Change adds 7
	// more bits, so the addressable range is 0..(2^21 - 1). Capping at the
	// 14-bit bank keeps the MSB meaningful rather than always zero.
	if index < 0 || index > 0x1FFFFF {
		return nil, &device.ValidationError{Pointer: "/value", Msg: "banked program value must be in [0, 2097151] (14-bit bank + 7-bit program)"}
	}
	bank := index >> 7
	program := index & 0x7F
	status := statusByte(0xB0, ch)
	return []transport.Event{
		midiEvent(ch, []byte{status, 0, byte((bank >> 7) & 0x7F)}),
		midiEvent(ch, []byte{status, 32, byte(bank & 0x7F)}),
		midiEvent(ch, []byte{statusByte(0xC0, ch), byte(program)}),
	}, nil
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

// renderSysEx parses a whitespace-separated hex template and returns the message
// bytes. Recognised tokens (everything else is a literal hex byte):
//
//   - "%v"     substitute the wire value byte (0..127)
//   - "["      open a checksum region (bytes from here are accumulated)
//   - "]"      close the checksum region
//   - "%k"     emit the Roland address-based checksum of the region:
//     (0x80 - (Σregion & 0x7F)) & 0x7F
//
// A plain template like "F0 7D 01 %v F7" needs none of the region tokens. A
// Roland DT1 write uses them, e.g. (SL-2 temp-patch SLICER pattern):
//
//	"F0 41 10 00 00 00 00 1D 12 [ 20 00 10 00 %v ] %k F7"
//
// where the checksum covers the address + data bytes only.
func renderSysEx(template string, value int) ([]byte, error) {
	if strings.TrimSpace(template) == "" {
		return nil, &device.ValidationError{Pointer: "/control", Msg: "sysex control is missing a template"}
	}
	if value < 0 || value > 127 {
		return nil, &device.ValidationError{Pointer: "/value", Msg: "sysex value byte must be in [0, 127]"}
	}
	fields := strings.Fields(template)
	out := make([]byte, 0, len(fields))
	var sum int
	inSum := false
	addByte := func(b byte) {
		out = append(out, b)
		if inSum {
			sum += int(b)
		}
	}
	for _, f := range fields {
		switch f {
		case "%v":
			addByte(byte(value))
		case "[":
			inSum, sum = true, 0
		case "]":
			inSum = false
		case "%k":
			out = append(out, byte((0x80-(sum&0x7F))&0x7F))
		default:
			b, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(f), "0x"), 16, 8)
			if err != nil {
				return nil, &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("invalid sysex byte %q in template", f)}
			}
			addByte(byte(b))
		}
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
