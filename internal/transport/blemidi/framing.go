package blemidi

import "time"

// BLE-MIDI packet framing (BLE-MIDI spec, "Packet Encoding").
//
// A characteristic payload is one or more MIDI messages prefixed with a 13-bit
// millisecond timestamp, split into a header byte and per-message timestamp
// bytes:
//
//	byte 0:        1 0 t t t t t t   header  (high 6 timestamp bits)
//	byte 1:        1 t t t t t t t   timestamp (low 7 bits), precedes a message
//	bytes 2..:     MIDI status + data
//	(repeat timestamp byte + message for further messages in the same packet)
//
// Status and timestamp bytes both have the high bit set; the positional rule
// "a timestamp byte precedes every status byte (and every running-status
// message)" disambiguates them on decode. Data bytes always have the high bit
// clear.

// FrameMessage wraps a single MIDI message in a minimal BLE-MIDI packet using
// the current wall-clock millisecond as the timestamp. The pedals ignore the
// timestamp value (it matters only for jitter-correction on ordered streams),
// so a coarse monotonic-ish counter is sufficient.
func FrameMessage(midi []byte) []byte {
	return frameMessageAt(midi, timestamp13())
}

// frameMessageAt is the deterministic core of FrameMessage, taking the 13-bit
// timestamp explicitly so tests can assert exact bytes.
func frameMessageAt(midi []byte, ts uint16) []byte {
	ts &= 0x1FFF
	header := byte(0x80) | byte(ts>>7)
	tsLow := byte(0x80) | byte(ts&0x7F)
	out := make([]byte, 0, len(midi)+2)
	out = append(out, header, tsLow)
	out = append(out, midi...)
	return out
}

// timestamp13 returns the low 13 bits of the current Unix millisecond clock.
func timestamp13() uint16 {
	return uint16(time.Now().UnixMilli()) & 0x1FFF
}

// DecodePacket strips BLE-MIDI timestamp framing from one received packet and
// returns the MIDI messages it carries. Each result is a complete message:
// status byte + data bytes, a single-byte system real-time message, or a full
// 0xF0..0xF7 SysEx blob. Running status, system real-time and SysEx are
// handled; a truncated trailing message is dropped.
func DecodePacket(p []byte) [][]byte {
	if len(p) < 2 {
		return nil
	}
	var msgs [][]byte
	var running byte // last channel-voice status (for running status)
	i := 1           // skip the header byte
	for i < len(p) {
		// A timestamp byte (high bit set) precedes every status byte and every
		// running-status message. Consume it when present.
		if p[i]&0x80 != 0 {
			i++
			if i >= len(p) {
				break
			}
		}

		var status byte
		if p[i]&0x80 != 0 {
			status = p[i]
			i++
		} else {
			status = running // running status: reuse the previous status
		}
		if status == 0 {
			break // data byte with no established running status
		}

		if status == 0xF0 { // SysEx: collect data until the 0xF7 terminator
			msg := []byte{0xF0}
			for i < len(p) {
				b := p[i]
				if b == 0xF7 {
					msg = append(msg, b)
					i++
					break
				}
				if b&0x80 != 0 { // timestamp byte preceding the terminator
					i++
					continue
				}
				msg = append(msg, b)
				i++
			}
			msgs = append(msgs, msg)
			running = 0
			continue
		}

		n := dataLen(status)
		if i+n > len(p) {
			break // truncated message
		}
		msg := make([]byte, 0, n+1)
		msg = append(msg, status)
		msg = append(msg, p[i:i+n]...)
		msgs = append(msgs, msg)
		i += n

		if status < 0xF0 {
			running = status // running status applies only to channel-voice
		} else {
			running = 0
		}
	}
	return msgs
}

// dataLen returns the number of data bytes that follow a given MIDI status
// byte (excluding the status byte itself).
func dataLen(status byte) int {
	switch {
	case status >= 0xF8: // system real-time (clock/start/stop/...)
		return 0
	case status >= 0xF0: // system common
		switch status {
		case 0xF1, 0xF3: // MTC quarter-frame, song select
			return 1
		case 0xF2: // song position pointer
			return 2
		default: // 0xF4,0xF5,0xF6 tune request, 0xF7 (unpaired)
			return 0
		}
	case status >= 0xC0 && status <= 0xDF: // program change, channel pressure
		return 1
	default: // 0x80-0xBF (note/poly/cc), 0xE0-0xEF (pitch bend)
		return 2
	}
}

// channelOf extracts the 0-based MIDI channel from a channel-voice message. It
// returns ok=false for system messages (real-time, common, SysEx) which carry
// no channel.
func channelOf(midi []byte) (int, bool) {
	if len(midi) == 0 {
		return 0, false
	}
	s := midi[0]
	if s >= 0x80 && s < 0xF0 {
		return int(s & 0x0F), true
	}
	return 0, false
}
