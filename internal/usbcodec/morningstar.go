package usbcodec

// Morningstar implements the Morningstar editor protocol used by the ML10X (and
// the MC-series) over USB-MIDI SysEx. The framing was reverse-engineered by
// capturing the web editor's "read from device" (see docs/research/ml10x.md):
//
//	F0 00 21 24 <model> 00 <op1> <op2> <8-byte payload> <cksum> F7
//	         └ Morningstar mfg ┘  │  └ op2: read opcode
//	                              └ op1 (message class): 00 request / 01 status / 06 data
//	cksum = XOR(all preceding bytes) & 0x7F
//
// Reads are deterministic for op2 0x00/0x01 and 0x16 (the latter taking a bank
// in the payload); a full bank/preset dump streams several data blocks and is
// best-effort. The write opcodes are not yet decoded, so BuildWrite returns nil.
type Morningstar struct {
	mfg   []byte // manufacturer id (00 21 24)
	model byte   // model id (ML10X: 0x07)
}

// Message classes (op1) seen on the wire.
const (
	msClassRequest = 0x00 // host -> device
	msClassStatus  = 0x01 // device -> host: ack / small value
	msClassData    = 0x06 // device -> host: TLV data block
)

// msReadBank is the read opcode (op2) that takes a bank selector in the payload;
// other read opcodes use a zero payload.
const msReadBank = 0x16

// defaultMorningstarMfg / defaultMorningstarModel are applied when Config leaves
// them unset (the ML10X model id 0x07 was discovered live).
var defaultMorningstarMfg = []byte{0x00, 0x21, 0x24}

const defaultMorningstarModel = 0x07

// NewMorningstar builds a Morningstar codec, defaulting the manufacturer to
// 00 21 24 and the model to 0x07 (ML10X) when unset.
func NewMorningstar(cfg Config) *Morningstar {
	c := &Morningstar{
		mfg:   append([]byte(nil), cfg.Mfg...),
		model: defaultMorningstarModel,
	}
	if len(c.mfg) == 0 {
		c.mfg = append([]byte(nil), defaultMorningstarMfg...)
	}
	if len(cfg.Model) > 0 {
		c.model = cfg.Model[0]
	}
	return c
}

// Protocol returns the Morningstar protocol id.
func (c *Morningstar) Protocol() string { return ProtocolMorningstar }

// BuildIdentify returns nil: the Morningstar editor has no identity request, the
// device is identified by its replies to the read opcodes.
func (c *Morningstar) BuildIdentify() []byte { return nil }

// DecodeIdentity reports a recognised Morningstar frame as an Identity (mfg from
// the SysEx header, model in DeviceID). ok is false for any other frame.
func (c *Morningstar) DecodeIdentity(reply []byte) (Identity, bool) {
	if !c.isFrame(reply) {
		return Identity{}, false
	}
	return Identity{
		Manufacturer: c.mfg[len(c.mfg)-1],
		DeviceID:     c.model,
		Raw:          append([]byte(nil), reply...),
	}, true
}

// BuildRead frames a read request. addr's low 7 bits are the read opcode (op2);
// for the bank-read opcode (0x16) the next byte carries the bank in the payload.
// size is unused — the device streams whole TLV blocks. The matcher accepts any
// Morningstar frame; the session correlates streamed blocks by arrival.
func (c *Morningstar) BuildRead(addr int64, size int) ([]byte, func([]byte) bool) {
	op2 := byte(addr & 0x7F)
	var payload []byte
	if op2 == msReadBank {
		payload = []byte{byte((addr >> 8) & 0x7F)}
	}
	match := func(f []byte) bool { return c.isFrame(f) }
	return c.buildRequest(op2, payload...), match
}

// DecodeRead parses a reply frame, returning the read opcode (op2) as the
// address and the TLV/status payload as the data. ok is false for a non-frame.
func (c *Morningstar) DecodeRead(reply []byte) (int64, []byte, bool) {
	if !c.isFrame(reply) || len(reply) < 10 {
		return 0, nil, false
	}
	op2 := reply[7]
	data := append([]byte(nil), reply[8:len(reply)-2]...)
	return int64(op2), data, true
}

// BuildWrite returns nil: the Morningstar write opcodes are not yet decoded.
func (c *Morningstar) BuildWrite(addr int64, data []byte) []byte { return nil }

// BuildHandshake returns nil: no pre-write handshake is required.
func (c *Morningstar) BuildHandshake() [][]byte { return nil }

// checksum is the Morningstar trailer: XOR of all preceding bytes, masked to 7
// bits.
func (c *Morningstar) checksum(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	ck := b[0]
	for _, x := range b[1:] {
		ck ^= x
	}
	return ck & 0x7F
}

// buildRequest frames a read request: class 0x00, the given op2, then an 8-byte
// payload (zero-padded, as the editor sends), checksum + F7.
func (c *Morningstar) buildRequest(op2 byte, payload ...byte) []byte {
	p := make([]byte, 8)
	copy(p, payload)
	body := []byte{0xF0}
	body = append(body, c.mfg...)
	body = append(body, c.model, 0x00, msClassRequest, op2)
	body = append(body, p...)
	return append(body, c.checksum(body), 0xF7)
}

// isFrame reports whether f is a SysEx frame for this codec's mfg + model.
func (c *Morningstar) isFrame(f []byte) bool {
	if len(f) < 9 || f[0] != 0xF0 || f[4] != c.model {
		return false
	}
	for i, m := range c.mfg {
		if f[1+i] != m {
			return false
		}
	}
	return true
}

// TLVRecord is one 7F <id> <len> <value> entry inside a Morningstar data block.
type TLVRecord struct {
	ID    byte
	Value []byte
}

// ParseTLV walks a data-block payload of 7F-delimited records. It is exported so
// callers (the engine / a monitor) can decode the streamed data blocks.
func ParseTLV(p []byte) []TLVRecord {
	var recs []TLVRecord
	for i := 0; i+2 < len(p); {
		if p[i] != 0x7F {
			i++
			continue
		}
		id, n := p[i+1], int(p[i+2])
		end := i + 3 + n
		if end > len(p) {
			break
		}
		recs = append(recs, TLVRecord{ID: id, Value: append([]byte(nil), p[i+3:end]...)})
		i = end
	}
	return recs
}
