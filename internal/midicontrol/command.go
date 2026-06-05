package midicontrol

// CommandFromMIDI decodes a raw MIDI 1.0 channel/realtime message into the
// brain command frame the LAN channel speaks. ok is false for messages the
// brain protocol does not model (pitch bend, channel pressure, SysEx, clock),
// so callers skip them — the brain cannot re-emit them inside AUM.
//
// It lives with Command (its target type) so both the auv3midi transport (the
// real send path) and any other caller share one decode.
func CommandFromMIDI(data []byte) (Command, bool) {
	if len(data) == 0 {
		return Command{}, false
	}
	status := data[0]
	// System real-time transport (single-byte status).
	switch status {
	case 0xFA:
		return Command{Type: "transport", Action: "start"}, true
	case 0xFB:
		return Command{Type: "transport", Action: "continue"}, true
	case 0xFC:
		return Command{Type: "transport", Action: "stop"}, true
	}
	if status < 0x80 {
		return Command{}, false
	}
	ch := int(status&0x0F) + 1 // wire 0-based nibble -> brain 1-based channel
	d1 := func() int {
		if len(data) > 1 {
			return int(data[1] & 0x7F)
		}
		return 0
	}
	d2 := func() int {
		if len(data) > 2 {
			return int(data[2] & 0x7F)
		}
		return 0
	}
	switch status & 0xF0 {
	case 0x90: // note-on (velocity 0 is a note-off by convention)
		if d2() == 0 {
			return Command{Type: "noteOff", Channel: ch, Note: d1()}, true
		}
		return Command{Type: "noteOn", Channel: ch, Note: d1(), Velocity: d2()}, true
	case 0x80: // note-off
		return Command{Type: "noteOff", Channel: ch, Note: d1(), Velocity: d2()}, true
	case 0xB0: // control change
		return Command{Type: "cc", Channel: ch, Controller: d1(), Value: d2()}, true
	case 0xC0: // program change
		return Command{Type: "pc", Channel: ch, Program: d1()}, true
	default:
		return Command{}, false
	}
}
