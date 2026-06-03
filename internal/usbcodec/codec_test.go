package usbcodec

import (
	"bytes"
	"testing"
)

// sl2 is the confirmed Boss SL-2 identity/geometry (docs/research/sl-2.md).
func sl2() *Roland {
	return NewRoland(Config{
		Model:    []byte{0x00, 0x00, 0x00, 0x00, 0x1D},
		DeviceID: 0x10,
	})
}

func TestNewDispatch(t *testing.T) {
	cases := map[string]string{
		ProtocolRoland:      ProtocolRoland,
		ProtocolMorningstar: ProtocolMorningstar,
		ProtocolNeuro:       ProtocolNeuro,
		ProtocolTorpedo:     ProtocolTorpedo,
	}
	for proto, want := range cases {
		c, err := New(proto, Config{})
		if err != nil {
			t.Fatalf("New(%q): %v", proto, err)
		}
		if c.Protocol() != want {
			t.Errorf("New(%q).Protocol() = %q, want %q", proto, c.Protocol(), want)
		}
	}
	if _, err := New("bogus", Config{}); err == nil {
		t.Fatal("New(bogus): expected error")
	}
}

func TestRolandBuildRQ1(t *testing.T) {
	c := sl2()
	// SYSTEM_TEMPO read: addr 10 00 00 00, size 00 00 00 04 (spike sl2Regs).
	req, _ := c.BuildRead(0x10000000, 4)
	// F0 41 10 00 00 00 00 1D 11 | 10 00 00 00 | 00 00 00 04 | cksum | F7
	// checksum = 0x80 - (sum(addr+size)&0x7F) = 0x80 - 0x14 = 0x6C.
	want := []byte{
		0xF0, 0x41, 0x10, 0x00, 0x00, 0x00, 0x00, 0x1D, 0x11,
		0x10, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x04,
		0x6C, 0xF7,
	}
	if !bytes.Equal(req, want) {
		t.Fatalf("BuildRead RQ1 = % X\n            want % X", req, want)
	}
}

func TestRolandReadRoundTrip(t *testing.T) {
	c := sl2()
	req, match := c.BuildRead(0x10000000, 4)
	if len(req) == 0 {
		t.Fatal("empty RQ1")
	}
	// A DT1 reply echoing the address with the tempo data 00 04 0C 04 (122.0 BPM).
	reply := c.BuildWrite(0x10000000, []byte{0x00, 0x04, 0x0C, 0x04})
	if !match(reply) {
		t.Fatal("matcher did not match the DT1 reply at the requested address")
	}
	// A DT1 at a different address must not match.
	other := c.BuildWrite(0x20000000, []byte{0x00})
	if match(other) {
		t.Fatal("matcher matched a DT1 at a different address")
	}
	addr, data, ok := c.DecodeRead(reply)
	if !ok {
		t.Fatal("DecodeRead: not ok")
	}
	if addr != 0x10000000 {
		t.Errorf("DecodeRead addr = %#X, want 0x10000000", addr)
	}
	if !bytes.Equal(data, []byte{0x00, 0x04, 0x0C, 0x04}) {
		t.Errorf("DecodeRead data = % X, want 00 04 0C 04", data)
	}
}

func TestRolandIdentity(t *testing.T) {
	c := sl2()
	if !bytes.Equal(c.BuildIdentify(), []byte{0xF0, 0x7E, 0x7F, 0x06, 0x01, 0xF7}) {
		t.Fatalf("BuildIdentify = % X", c.BuildIdentify())
	}
	reply := []byte{0xF0, 0x7E, 0x10, 0x06, 0x02, 0x41, 0x1D, 0x02, 0x00, 0x00, 0x01, 0x00, 0x02, 0x03, 0xF7}
	id, ok := c.DecodeIdentity(reply)
	if !ok {
		t.Fatal("DecodeIdentity: not ok")
	}
	if id.Manufacturer != 0x41 {
		t.Errorf("Manufacturer = %#X, want 0x41", id.Manufacturer)
	}
	if id.DeviceID != 0x10 {
		t.Errorf("DeviceID = %#X, want 0x10", id.DeviceID)
	}
	if _, ok := c.DecodeIdentity([]byte{0xF0, 0x7E, 0x10, 0x06, 0x01, 0xF7}); ok {
		t.Error("DecodeIdentity matched an Identity Request")
	}
}

func TestRolandHandshake(t *testing.T) {
	c := sl2()
	hs := c.BuildHandshake()
	if len(hs) != 1 {
		t.Fatalf("BuildHandshake returned %d frames, want 1", len(hs))
	}
	// It must be a DT1 to 7F 00 00 01 carrying 0x01.
	addr, data, ok := c.decodeDT1(hs[0])
	if !ok {
		t.Fatal("handshake is not a decodable DT1")
	}
	if !bytes.Equal(addr, []byte{0x7F, 0x00, 0x00, 0x01}) {
		t.Errorf("handshake addr = % X, want 7F 00 00 01", addr)
	}
	if !bytes.Equal(data, []byte{0x01}) {
		t.Errorf("handshake data = % X, want 01", data)
	}
}

func TestMorningstarBuildRequest(t *testing.T) {
	c := NewMorningstar(Config{})
	req, _ := c.BuildRead(0x00, 0)
	// F0 00 21 24 07 00 00 00 + 8 zero payload + cksum + F7.
	want := []byte{
		0xF0, 0x00, 0x21, 0x24, 0x07, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x72, 0xF7,
	}
	if !bytes.Equal(req, want) {
		t.Fatalf("BuildRead = % X\n        want % X", req, want)
	}
}

func TestMorningstarBankRead(t *testing.T) {
	c := NewMorningstar(Config{})
	// Bank read opcode 0x16 with bank 2 -> op2 0x16, payload[0] = 0x02.
	req, _ := c.BuildRead(int64(msReadBank)|(2<<8), 0)
	if req[7] != msReadBank {
		t.Errorf("op2 = %#X, want %#X", req[7], msReadBank)
	}
	if req[8] != 0x02 {
		t.Errorf("payload[0] = %#X, want 0x02", req[8])
	}
}

func TestMorningstarDecodeReadTLV(t *testing.T) {
	c := NewMorningstar(Config{})
	// A data block (op1 0x06) for op2 0x16 carrying one TLV record: id 02 = "AB".
	body := []byte{0xF0, 0x00, 0x21, 0x24, 0x07, 0x00, msClassData, 0x16, 0x7F, 0x02, 0x02, 0x41, 0x42}
	frame := append(body, c.checksum(body), 0xF7)

	addr, data, ok := c.DecodeRead(frame)
	if !ok {
		t.Fatal("DecodeRead: not ok")
	}
	if addr != 0x16 {
		t.Errorf("addr (op2) = %#X, want 0x16", addr)
	}
	recs := ParseTLV(data)
	if len(recs) != 1 {
		t.Fatalf("ParseTLV records = %d, want 1", len(recs))
	}
	if recs[0].ID != 0x02 || !bytes.Equal(recs[0].Value, []byte("AB")) {
		t.Errorf("TLV = id %#X val %q, want id 0x02 val \"AB\"", recs[0].ID, recs[0].Value)
	}
	// A frame for a different model must not decode.
	if _, _, ok := c.DecodeRead([]byte{0xF0, 0x00, 0x21, 0x24, 0x08, 0x00, 0x06, 0x00, 0x00, 0xF7}); ok {
		t.Error("DecodeRead accepted a non-matching model")
	}
}

func TestMorningstarNoWrite(t *testing.T) {
	c := NewMorningstar(Config{})
	if c.BuildWrite(0, []byte{0x01}) != nil {
		t.Error("BuildWrite should be nil (write opcodes undecoded)")
	}
	if c.BuildHandshake() != nil {
		t.Error("BuildHandshake should be nil")
	}
}

func TestNeuro(t *testing.T) {
	c := NewNeuro(Config{})
	req, match := c.BuildRead(0x0800A0, 32)
	if !bytes.Equal(req, []byte{0x36, 0x08, 0x00, 0xA0}) {
		t.Fatalf("BuildRead = % X, want 36 08 00 A0", req)
	}
	// Build a 38-byte input report: report id + 32 data bytes + padding.
	data := make([]byte, neuroDumpLen)
	for i := range data {
		data[i] = byte(i)
	}
	report := append([]byte{0x00}, data...)
	report = append(report, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // padding
	if !match(report) {
		t.Fatal("matcher rejected a non-empty report")
	}
	addr, got, ok := c.DecodeRead(report)
	if !ok {
		t.Fatal("DecodeRead: not ok")
	}
	if addr != 0 {
		t.Errorf("addr = %d, want 0 (HID has no address echo)", addr)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("DecodeRead data = % X, want % X", got, data)
	}
	// A preset select is 0x77 <preset>.
	if !bytes.Equal(c.BuildWrite(5, nil), []byte{0x77, 0x05}) {
		t.Errorf("BuildWrite = % X, want 77 05", c.BuildWrite(5, nil))
	}
	if c.BuildIdentify() != nil {
		t.Error("BuildIdentify should be nil for HID")
	}
}

func TestTorpedoPlaceholder(t *testing.T) {
	c := NewTorpedo(Config{})
	if c.BuildIdentify() != nil {
		t.Error("BuildIdentify should be nil")
	}
	if req, m := c.BuildRead(0, 0); req != nil || m != nil {
		t.Error("BuildRead must synthesise nothing")
	}
	if c.BuildWrite(0, []byte{0x01}) != nil {
		t.Error("BuildWrite must synthesise nothing")
	}
	if c.BuildHandshake() != nil {
		t.Error("BuildHandshake should be nil")
	}
	// Monitoring passes a report through verbatim.
	report := bytes.Repeat([]byte{0xAB}, TorpedoReportLen)
	_, data, ok := c.DecodeRead(report)
	if !ok || !bytes.Equal(data, report) {
		t.Errorf("DecodeRead passthrough = % X ok=%v", data, ok)
	}
	if _, _, ok := c.DecodeRead(nil); ok {
		t.Error("DecodeRead of empty report should not be ok")
	}
}

// TestCodecsSatisfyInterface is a compile-time check that each concrete codec
// implements Codec.
func TestCodecsSatisfyInterface(t *testing.T) {
	var _ Codec = (*Roland)(nil)
	var _ Codec = (*Morningstar)(nil)
	var _ Codec = (*Neuro)(nil)
	var _ Codec = (*Torpedo)(nil)
}
