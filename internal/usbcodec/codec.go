// This file defines the shared Codec interface that the request/reply session
// drives, plus the per-protocol framing factory. The four protocol codecs
// (roland, morningstar, neuro, torpedo) were lifted out of the read-only spike
// in cmd/usb-probe/main.go (which stays as the reference) and given a common
// surface so the engine can speak to any USB device through one abstraction
// (see docs/usb-tools.md).
//
// The interface is deliberately small: identify, read, write, and an optional
// pre-write handshake. Address-based protocols (Roland) carry the address on
// the wire so reads can be matched to replies; HID protocols (Neuro, Torpedo)
// have no address echo, so their DecodeRead returns the raw report payload and
// the session correlates by request order.
package usbcodec

import "fmt"

// Protocol codec ids. These MUST match the device.USBProtocol* constants; they
// are duplicated here because the device package imports usbcodec (for the value
// encodings), so usbcodec cannot import device without an import cycle.
const (
	ProtocolRoland      = "roland-address-sysex" // Boss/Roland RQ1/DT1
	ProtocolMorningstar = "morningstar-sysex"    // Morningstar editor TLV
	ProtocolNeuro       = "neuro-hid"            // Source Audio Neuro HID
	ProtocolTorpedo     = "torpedo-hid"          // Two Notes Torpedo Remote HID
)

// Identity is a decoded device identity. For SysEx protocols it is parsed from a
// Universal Identity Reply; Raw holds the original frame for the caller to log.
type Identity struct {
	Manufacturer byte   // SysEx manufacturer id (single-byte form)
	DeviceID     byte   // SysEx device id the reply came from
	Family       []byte // device family code (2 bytes, as transmitted)
	Member       []byte // device family member (2 bytes)
	Revision     []byte // software revision (4 bytes)
	Raw          []byte // the full reply frame
}

// Config carries the protocol-independent identity/geometry a codec needs. The
// engine fills it from a device's USBProfile (Identity + AddrBytes/SizeBytes).
// Zero fields fall back to per-protocol defaults in the constructors.
type Config struct {
	Mfg       []byte // SysEx manufacturer id (1 byte for Roland, 3 for Morningstar)
	Model     []byte // model id bytes
	DeviceID  byte   // SysEx device id
	AddrBytes int    // address field width (address-based protocols)
	SizeBytes int    // size field width (address-based protocols)
}

// Codec frames requests and decodes replies for one USB editor/readback
// protocol. All build methods return nil when the protocol does not support the
// operation (e.g. Morningstar/Torpedo writes), and the session must check for
// nil before sending.
type Codec interface {
	// Protocol returns the codec's protocol id (one of the Protocol* constants).
	Protocol() string

	// BuildIdentify returns the frame that asks the device to identify itself,
	// or nil if the protocol has no identity request.
	BuildIdentify() []byte

	// DecodeIdentity parses an identity reply; ok is false if reply is not a
	// recognised identity frame for this protocol.
	DecodeIdentity(reply []byte) (id Identity, ok bool)

	// BuildRead frames a request to read size bytes at addr and returns a
	// matcher reporting whether an inbound frame is this read's reply. A nil
	// matcher means replies cannot be correlated by content (HID), so the
	// session must correlate by request order.
	BuildRead(addr int64, size int) (req []byte, match func([]byte) bool)

	// DecodeRead extracts the (address, data) of a read reply. The address is 0
	// for protocols that do not echo it (HID); ok is false for a non-reply frame.
	DecodeRead(reply []byte) (addr int64, data []byte, ok bool)

	// BuildWrite frames a request that writes data at addr, or nil if the
	// protocol's write framing is unknown/unsupported.
	BuildWrite(addr int64, data []byte) []byte

	// BuildHandshake returns the frames that must be sent before a write (e.g.
	// Roland editor-comm mode), or nil if none are required.
	BuildHandshake() [][]byte
}

// New constructs the codec for a protocol id from a Config, applying per-protocol
// defaults for any zero fields.
func New(protocol string, cfg Config) (Codec, error) {
	switch protocol {
	case ProtocolRoland:
		return NewRoland(cfg), nil
	case ProtocolMorningstar:
		return NewMorningstar(cfg), nil
	case ProtocolNeuro:
		return NewNeuro(cfg), nil
	case ProtocolTorpedo:
		return NewTorpedo(cfg), nil
	default:
		return nil, fmt.Errorf("usbcodec: unknown protocol %q", protocol)
	}
}

// identityRequest is the Universal Non-Realtime Identity Request, broadcast to
// all device ids (0x7F). Any compliant MIDI device answers with an Identity
// Reply.
var identityRequest = []byte{0xF0, 0x7E, 0x7F, 0x06, 0x01, 0xF7}

// decodeIdentityReply parses F0 7E <dev> 06 02 <mfg> <family×2> <member×2>
// <rev×4> F7 (single-byte manufacturer form). ok is false otherwise.
func decodeIdentityReply(f []byte) (Identity, bool) {
	if len(f) < 15 || f[0] != 0xF0 || f[1] != 0x7E || f[3] != 0x06 || f[4] != 0x02 {
		return Identity{}, false
	}
	return Identity{
		DeviceID:     f[2],
		Manufacturer: f[5],
		Family:       append([]byte(nil), f[6:8]...),
		Member:       append([]byte(nil), f[8:10]...),
		Revision:     append([]byte(nil), f[10:14]...),
		Raw:          append([]byte(nil), f...),
	}, true
}

// intToBytes splits v into n big-endian 8-bit bytes. Roland/Neuro wire addresses
// are written so each byte is already 7-bit-safe, so this reproduces them.
func intToBytes(v int64, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[n-1-i] = byte(v >> (8 * uint(i)))
	}
	return out
}

// bytesToInt reassembles a big-endian byte run into an int64.
func bytesToInt(b []byte) int64 {
	var v int64
	for _, x := range b {
		v = v<<8 | int64(x)
	}
	return v
}
