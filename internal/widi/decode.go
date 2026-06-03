package widi

import (
	"fmt"
	"net"
)

// ReplyKind classifies a decoded WIDI reply.
type ReplyKind int

const (
	ReplySettings ReplyKind = iota // READ_SETTINGS reply (carries register value)
	ReplyWriteAck                  // WRITE_SETTINGS acknowledgement
	ReplyStatus                    // READ_STATUS reply
	ReplyError                     // 10-byte error reply
)

// Reply is a decoded WIDI SysEx reply.
type Reply struct {
	Kind  ReplyKind
	DevID byte
	Cmd   byte // raw command byte (reply bit set), low nibble = original command

	// Settings / WriteAck.
	Register Register
	Count    int    // logical byte count reported by the device
	Bytes    []byte // decoded logical bytes (nibble-decoded; raw for word/string)

	// Status.
	Status     Status
	StatusData []byte // raw status payload (encoding is status-specific)

	// Error.
	ErrCode  byte
	ErrName  string
	ErrParam byte

	Raw []byte // the original reply bytes
}

// IsReply reports whether b is a CME WIDI reply: a SysEx framed with the CME
// header whose command byte has the reply bit (0x40) set.
func IsReply(b []byte) bool {
	return len(b) >= 8 && b[0] == 0xF0 &&
		b[1] == Header[0] && b[2] == Header[1] && b[3] == Header[2] && b[4] == Header[3] &&
		(b[6]&ReplyBit) == ReplyBit &&
		b[len(b)-1] == 0xF7
}

// Decode parses a CME WIDI reply. It returns an error only when b is not a
// well-formed WIDI reply; device-reported errors are returned as a Reply with
// Kind == ReplyError so callers can inspect the code.
func Decode(b []byte) (Reply, error) {
	if !IsReply(b) {
		return Reply{}, fmt.Errorf("widi: not a CME WIDI reply: % X", b)
	}
	r := Reply{DevID: b[5], Cmd: b[6], Raw: b}

	// The 10-byte form is an error reply: F0 H×4 devID cmd errId param F7.
	if len(b) == 10 {
		if name, ok := sysexErrors[b[7]]; ok {
			r.Kind = ReplyError
			r.ErrCode = b[7]
			r.ErrName = name
			r.ErrParam = b[8]
			return r, nil
		}
	}

	body := b[7 : len(b)-1] // strip the trailing F7; a checksum byte may remain
	switch b[6] & 0x0F {
	case CmdReadSettings, CmdWriteSettings:
		if len(body) < 2 {
			return Reply{}, fmt.Errorf("widi: short settings reply: % X", b)
		}
		r.Register = Register(body[0])
		r.Count = int(body[1])
		nib := body[2:]
		if 2*r.Count <= len(nib) {
			nib = nib[:2*r.Count]
		}
		r.Bytes = DecodeNibbles(nib)
		if b[6]&0x0F == CmdWriteSettings {
			r.Kind = ReplyWriteAck
		} else {
			r.Kind = ReplySettings
		}
		return r, nil
	case CmdReadStatus:
		if len(body) < 1 {
			return Reply{}, fmt.Errorf("widi: short status reply: % X", b)
		}
		r.Kind = ReplyStatus
		r.Status = Status(body[0])
		r.StatusData = body[1:]
		return r, nil
	default:
		return Reply{}, fmt.Errorf("widi: unsupported reply command 0x%02X: % X", b[6], b)
	}
}

// Byte returns the first decoded logical byte of a settings reply.
func (r Reply) Byte() (byte, bool) {
	if len(r.Bytes) == 0 {
		return 0, false
	}
	return r.Bytes[0], true
}

// MAC interprets a CONNECT_ADDRESS settings reply: the 6 logical bytes are in
// wire (reversed) order, so they are reversed back to display order. The second
// return is false when the slot is the FF×6 empty sentinel (no peer set).
func (r Reply) MAC() (net.HardwareAddr, bool) {
	if len(r.Bytes) != 6 {
		return nil, false
	}
	if isAllFF(r.Bytes) {
		return nil, false
	}
	mac := make(net.HardwareAddr, 6)
	for i := 0; i < 6; i++ {
		mac[i] = r.Bytes[5-i]
	}
	return mac, true
}

// Describe renders a settings reply's value into a human-friendly form for the
// given register: an enum label, a dBm string, a MAC or "none", etc. It returns
// the value as a Go value (string/int/nil) suitable for JSON.
func Describe(reg Register, bytes []byte) any {
	switch reg {
	case RegTXPower:
		if len(bytes) > 0 {
			if dBm, ok := TXPowerDBmForIndex(bytes[0]); ok {
				return fmt.Sprintf("%+d dBm", dBm)
			}
		}
	case RegForceBLERole:
		if len(bytes) > 0 {
			if bytes[0] == RolePeripheral {
				return "peripheral"
			}
			return "auto"
		}
	case RegPowerSaving, RegMIDIInThru:
		if len(bytes) > 0 {
			if bytes[0] == 0 {
				return "off"
			}
			return "on"
		}
	case RegPreferLatencyJitter:
		if len(bytes) > 0 {
			if bytes[0] == 0 {
				return "latency"
			}
			return "jitter"
		}
	case RegConnectAddress1, RegConnectAddress2, RegConnectAddress3, RegConnectAddress4:
		if len(bytes) == 6 {
			if isAllFF(bytes) {
				return "none"
			}
			mac := make(net.HardwareAddr, 6)
			for i := 0; i < 6; i++ {
				mac[i] = bytes[5-i]
			}
			return mac.String()
		}
	}
	// Fallback: present the raw logical bytes.
	if len(bytes) == 1 {
		return int(bytes[0])
	}
	return fmt.Sprintf("% X", bytes)
}

func isAllFF(b []byte) bool {
	for _, x := range b {
		if x != 0xFF {
			return false
		}
	}
	return len(b) > 0
}
