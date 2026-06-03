package usbcodec

// Neuro implements the Source Audio Neuro editor protocol used by the EQ2 (and
// sibling One Series pedals) over the device's vendor HID interface — NOT
// USB-MIDI (see docs/research/eq-2.md). The framing was reverse-engineered from
// the C4 Synth (same 29A4 vendor / Neuro HID framework):
//
//	write 0x36 <a2> <a1> <a0>   -> dump 32 bytes from 24-bit address a2a1a0
//	write 0x77 <preset 0-127>   -> select (program change) a preset  [WRITE]
//
// The reply is an input report whose byte[1:33] holds the 32 dumped bytes (HID
// reports prefix a report id, 0x00 for unnumbered reports). HID has no address
// echo, so DecodeRead returns address 0 and the session correlates by request
// order; BuildRead's matcher accepts any non-empty report.
type Neuro struct{}

const (
	neuroCmdDump   = 0x36 // dump 32 bytes from a 24-bit address
	neuroCmdSelect = 0x77 // select a preset (a write: changes device state)
	neuroDumpLen   = 32   // bytes returned by a dump
	neuroDataStart = 1    // input report data begins after the report-id byte
)

// NewNeuro builds a Neuro HID codec. The HID framing needs no identity/geometry,
// so Config is ignored.
func NewNeuro(cfg Config) *Neuro { return &Neuro{} }

// Protocol returns the Neuro protocol id.
func (c *Neuro) Protocol() string { return ProtocolNeuro }

// BuildIdentify returns nil: the Neuro HID channel has no identity request.
func (c *Neuro) BuildIdentify() []byte { return nil }

// DecodeIdentity always reports no identity (HID has no identity reply).
func (c *Neuro) DecodeIdentity(reply []byte) (Identity, bool) { return Identity{}, false }

// BuildRead frames a 0x36 dump of 32 bytes at the 24-bit addr. size is fixed by
// the protocol and ignored. The matcher accepts any non-empty input report.
func (c *Neuro) BuildRead(addr int64, size int) ([]byte, func([]byte) bool) {
	req := []byte{neuroCmdDump, byte(addr >> 16), byte(addr >> 8), byte(addr)}
	match := func(rep []byte) bool { return len(rep) > 0 }
	return req, match
}

// DecodeRead returns the 32-byte data window (report byte[1:33]). The address is
// 0 — HID replies do not echo it. ok is false for an empty report.
func (c *Neuro) DecodeRead(reply []byte) (int64, []byte, bool) {
	if len(reply) <= neuroDataStart {
		return 0, nil, false
	}
	end := neuroDataStart + neuroDumpLen
	if end > len(reply) {
		end = len(reply)
	}
	return 0, append([]byte(nil), reply[neuroDataStart:end]...), true
}

// BuildWrite frames a 0x77 preset-select using byte(addr) as the preset index.
// On the Neuro a "write" is a program change (it selects a stored preset rather
// than mutating memory); data is unused.
func (c *Neuro) BuildWrite(addr int64, data []byte) []byte {
	return []byte{neuroCmdSelect, byte(addr)}
}

// BuildHandshake returns nil: no pre-write handshake is required.
func (c *Neuro) BuildHandshake() [][]byte { return nil }
