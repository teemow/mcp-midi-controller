package usbcodec

import "bytes"

// Roland implements the Boss/Roland address-based SysEx editor protocol
// (RQ1/DT1) used by the Boss SL-2 and friends. The framing was decompiled from
// "BOSS TONE STUDIO for SL-2" and verified live (see docs/research/sl-2.md); the
// proven helpers (buildRQ1/rolandChecksum/decodeDT1) are lifted from the spike.
//
// A read is an RQ1 (command 0x11) carrying an addr+size; the device answers with
// a DT1 (command 0x12) echoing the addr and returning the data. A write is a DT1
// to the same address. Before any write the editor must enter editor-comm mode
// (BuildHandshake), or the device ignores writes.
type Roland struct {
	mfg       byte   // manufacturer id (0x41)
	model     []byte // address-map model id (SL-2: 00 00 00 00 1D)
	device    byte   // SysEx device id (SL-2: 0x10)
	addrBytes int    // address field width (4)
	sizeBytes int    // size field width (4)
}

const (
	rolandMfg     = 0x41 // Roland MIDI manufacturer id
	rolandRQ1     = 0x11 // Data Request 1 (read)
	rolandDT1     = 0x12 // Data Set 1 (write / reply)
	rolandDefAddr = 4    // default address/size field widths
)

// rolandEditorCommAddr / rolandEditorCommOn are the editor-comm-mode handshake:
// a DT1 write of 0x01 to address 7F 00 00 01 puts the device in editor mode so
// it accepts subsequent writes (see docs/research/sl-2.md).
const rolandEditorCommAddr = 0x7F000001

// NewRoland builds a Roland codec, defaulting the manufacturer to 0x41 and the
// address/size widths to 4 when unset.
func NewRoland(cfg Config) *Roland {
	c := &Roland{
		mfg:       rolandMfg,
		model:     append([]byte(nil), cfg.Model...),
		device:    cfg.DeviceID,
		addrBytes: cfg.AddrBytes,
		sizeBytes: cfg.SizeBytes,
	}
	if len(cfg.Mfg) > 0 {
		c.mfg = cfg.Mfg[0]
	}
	if c.addrBytes == 0 {
		c.addrBytes = rolandDefAddr
	}
	if c.sizeBytes == 0 {
		c.sizeBytes = rolandDefAddr
	}
	return c
}

// Protocol returns the Roland protocol id.
func (c *Roland) Protocol() string { return ProtocolRoland }

// BuildIdentify returns the Universal Identity Request.
func (c *Roland) BuildIdentify() []byte {
	return append([]byte(nil), identityRequest...)
}

// DecodeIdentity parses a Universal Identity Reply.
func (c *Roland) DecodeIdentity(reply []byte) (Identity, bool) {
	return decodeIdentityReply(reply)
}

// BuildRead frames an RQ1 read of size bytes at addr and a matcher for the DT1
// reply at the same address.
func (c *Roland) BuildRead(addr int64, size int) ([]byte, func([]byte) bool) {
	a := intToBytes(addr, c.addrBytes)
	s := intToBytes(int64(size), c.sizeBytes)
	want := append([]byte(nil), a...)
	match := func(f []byte) bool {
		ra, _, ok := c.decodeDT1(f)
		return ok && bytes.Equal(ra, want)
	}
	return c.buildRQ1(a, s), match
}

// DecodeRead parses a DT1 reply into its address and data payload.
func (c *Roland) DecodeRead(reply []byte) (int64, []byte, bool) {
	a, data, ok := c.decodeDT1(reply)
	if !ok {
		return 0, nil, false
	}
	return bytesToInt(a), data, true
}

// BuildWrite frames a DT1 that writes data at addr.
func (c *Roland) BuildWrite(addr int64, data []byte) []byte {
	return c.buildDT1(intToBytes(addr, c.addrBytes), data)
}

// BuildHandshake returns the editor-comm-mode DT1 that must precede any write.
func (c *Roland) BuildHandshake() [][]byte {
	a := intToBytes(rolandEditorCommAddr, c.addrBytes)
	return [][]byte{c.buildDT1(a, []byte{0x01})}
}

// rolandChecksum is Roland's address+size(+data) checksum: the value that makes
// the 7-bit sum of all preceding bytes come out to 0.
func rolandChecksum(b []byte) byte {
	sum := 0
	for _, x := range b {
		sum += int(x)
	}
	return byte((0x80 - (sum & 0x7F)) & 0x7F)
}

// header returns F0 <mfg> <dev> <model…> ready to receive a command byte.
func (c *Roland) header() []byte {
	out := []byte{0xF0, c.mfg, c.device}
	return append(out, c.model...)
}

// buildRQ1 frames a Data Request 1: <header> 11 <addr…> <size…> <cksum> F7.
func (c *Roland) buildRQ1(addr, size []byte) []byte {
	out := append(c.header(), rolandRQ1)
	out = append(out, addr...)
	out = append(out, size...)
	body := append(append([]byte(nil), addr...), size...)
	return append(out, rolandChecksum(body), 0xF7)
}

// buildDT1 frames a Data Set 1: <header> 12 <addr…> <data…> <cksum> F7.
func (c *Roland) buildDT1(addr, data []byte) []byte {
	out := append(c.header(), rolandDT1)
	out = append(out, addr...)
	out = append(out, data...)
	body := append(append([]byte(nil), addr...), data...)
	return append(out, rolandChecksum(body), 0xF7)
}

// decodeDT1 parses a DT1 frame matching this codec's mfg/model
// (F0 <mfg> <dev> <model…> 12 <addr…> <data…> <cksum> F7) into the address and
// data bytes. ok is false if the frame is not a matching DT1.
func (c *Roland) decodeDT1(f []byte) (addr, data []byte, ok bool) {
	hdr := 3 + len(c.model) + 1 // F0 mfg dev | model | 12
	if len(f) < hdr+c.addrBytes+1+1 || f[0] != 0xF0 || f[1] != c.mfg {
		return nil, nil, false
	}
	for i, m := range c.model {
		if f[3+i] != m {
			return nil, nil, false
		}
	}
	if f[3+len(c.model)] != rolandDT1 {
		return nil, nil, false
	}
	// Reject a frame without the SysEx EOX terminator (truncated reply).
	if f[len(f)-1] != 0xF7 {
		return nil, nil, false
	}
	addr = append([]byte(nil), f[hdr:hdr+c.addrBytes]...)
	data = append([]byte(nil), f[hdr+c.addrBytes:len(f)-2]...) // strip cksum + F7
	// Verify Roland's address+data checksum so a corrupted reply is not
	// accepted as valid readback data.
	body := append(append([]byte(nil), addr...), data...)
	if rolandChecksum(body) != f[len(f)-2] {
		return nil, nil, false
	}
	return addr, data, true
}
