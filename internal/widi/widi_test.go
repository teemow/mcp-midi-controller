package widi

import (
	"bytes"
	"net"
	"testing"
)

// hexBytes parses "F0 00 20" style strings into bytes for the test vectors.
func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	var out []byte
	for _, f := range bytes.Fields([]byte(s)) {
		var v int
		for _, c := range f {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= int(c - '0')
			case c >= 'A' && c <= 'F':
				v |= int(c-'A') + 10
			case c >= 'a' && c <= 'f':
				v |= int(c-'a') + 10
			default:
				t.Fatalf("bad hex %q", f)
			}
		}
		out = append(out, byte(v))
	}
	return out
}

func TestChecksum(t *testing.T) {
	// (cmd + Σdata) & 0x7F, from the worked example in docs/research/widi.md.
	if got := checksum(CmdReadSettings, []byte{0x00, 0x20}); got != 0x22 {
		t.Fatalf("checksum BLE_NAME read = 0x%02X, want 0x22", got)
	}
	if got := checksum(CmdWriteSettings, []byte{0x06, 0x01, 0x01, 0x00}); got != 0x0B {
		t.Fatalf("checksum role write = 0x%02X, want 0x0B", got)
	}
}

func TestBuildReadSetting(t *testing.T) {
	cases := []struct {
		devID byte
		reg   Register
		want  string
	}{
		// Thru6 (0x12) reads, verified live.
		{0x12, RegTXPower, "F0 00 20 63 0F 12 02 01 01 04 F7"},
		{0x12, RegBLEName, "F0 00 20 63 0F 12 02 00 20 22 F7"},
	}
	for _, c := range cases {
		got := BuildReadSetting(c.devID, c.reg)
		if want := hexBytes(t, c.want); !bytes.Equal(got, want) {
			t.Fatalf("BuildReadSetting(%#x, %s) = % X, want % X", c.devID, c.reg.Name(), got, want)
		}
	}
}

func TestBuildWriteByte(t *testing.T) {
	cases := []struct {
		devID byte
		reg   Register
		val   byte
		want  string
	}{
		// Set BLE role PERIPHERAL on the Thru6 (the anti-auto-connect write).
		{0x12, RegForceBLERole, RolePeripheral, "F0 00 20 63 0F 12 03 06 01 01 00 0B F7"},
		// The reversible MIDI_IN_THRU toggle verified live on a WIDI Master.
		{0x09, RegMIDIInThru, 1, "F0 00 20 63 0F 09 03 0E 01 01 00 13 F7"},
		{0x09, RegMIDIInThru, 0, "F0 00 20 63 0F 09 03 0E 01 00 00 12 F7"},
	}
	for _, c := range cases {
		got := BuildWriteByte(c.devID, c.reg, c.val)
		if want := hexBytes(t, c.want); !bytes.Equal(got, want) {
			t.Fatalf("BuildWriteByte(%#x, %s, %d) = % X, want % X", c.devID, c.reg.Name(), c.val, got, want)
		}
	}
}

func TestNibbleRoundTrip(t *testing.T) {
	in := []byte{0x00, 0x0E, 0x7F, 0xFF, 0x12, 0xAB}
	enc := encodeNibbles(in)
	if len(enc) != 2*len(in) {
		t.Fatalf("encoded len = %d, want %d", len(enc), 2*len(in))
	}
	for _, n := range enc {
		if n > 0x0F {
			t.Fatalf("nibble 0x%02X is not 7-bit-safe / 4-bit", n)
		}
	}
	if got := DecodeNibbles(enc); !bytes.Equal(got, in) {
		t.Fatalf("round-trip = % X, want % X", got, in)
	}
}

func TestBuildWriteAddressReversesMAC(t *testing.T) {
	mac, _ := net.ParseMAC("10:2E:AB:DA:AC:66")
	frame, err := BuildWriteAddress(0x12, RegConnectAddress1, mac)
	if err != nil {
		t.Fatalf("BuildWriteAddress: %v", err)
	}
	// Strip envelope: F0 H0..H3 devID cmd <data...> ck F7. data = regId, 0x06, 12 nibbles.
	data := frame[6 : len(frame)-2] // cmd .. before ck; first byte is cmd
	if data[0] != CmdWriteSettings {
		t.Fatalf("cmd = 0x%02X, want WRITE_SETTINGS", data[0])
	}
	if Register(data[1]) != RegConnectAddress1 || data[2] != 0x06 {
		t.Fatalf("reg/count header = % X", data[1:3])
	}
	wireBytes := DecodeNibbles(data[3:])
	// Wire order is reversed; reverse back must equal the display MAC.
	var got net.HardwareAddr
	for i := len(wireBytes) - 1; i >= 0; i-- {
		got = append(got, wireBytes[i])
	}
	if !bytes.Equal(got, mac) {
		t.Fatalf("decoded MAC = %s, want %s", got, mac)
	}
}

func TestBuildClearAddress(t *testing.T) {
	frame := BuildClearAddress(0x0B, RegConnectAddress3)
	rep := frame // not a reply, but reuse the nibble payload extraction
	data := rep[6 : len(rep)-2]
	payload := DecodeNibbles(data[3:])
	if !isAllFF(payload) || len(payload) != 6 {
		t.Fatalf("clear payload = % X, want FF x6", payload)
	}
}

func TestDecodeSettingsReply(t *testing.T) {
	// TX_POWER reply on a WIDI Jack: 0x0E = index 14 = +5 dBm.
	r, err := Decode(hexBytes(t, "F0 00 20 63 0F 0B 42 01 01 0E 00 52 F7"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Kind != ReplySettings || r.DevID != 0x0B || r.Register != RegTXPower {
		t.Fatalf("reply = %+v", r)
	}
	b, ok := r.Byte()
	if !ok || b != 0x0E {
		t.Fatalf("byte = 0x%02X ok=%v, want 0x0E", b, ok)
	}
	if got := Describe(RegTXPower, r.Bytes); got != "+5 dBm" {
		t.Fatalf("describe tx_power = %v, want +5 dBm", got)
	}
}

func TestDecodeRoleReply(t *testing.T) {
	auto, _ := Decode(hexBytes(t, "F0 00 20 63 0F 12 42 06 01 00 00 49 F7"))
	if got := Describe(RegForceBLERole, auto.Bytes); got != "auto" {
		t.Fatalf("role = %v, want auto", got)
	}
	peri, _ := Decode(hexBytes(t, "F0 00 20 63 0F 12 42 06 01 01 00 4A F7"))
	if got := Describe(RegForceBLERole, peri.Bytes); got != "peripheral" {
		t.Fatalf("role = %v, want peripheral", got)
	}
}

func TestDecodeWriteAck(t *testing.T) {
	r, err := Decode(hexBytes(t, "F0 00 20 63 0F 12 43 06 01 00 4A F7"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Kind != ReplyWriteAck || r.Register != RegForceBLERole {
		t.Fatalf("reply = %+v, want write-ack for FORCE_BLE_ROLE", r)
	}
}

func TestDecodeConnectAddressEmpty(t *testing.T) {
	r, err := Decode(hexBytes(t, "F0 00 20 63 0F 0B 42 07 06 0F 0F 0F 0F 0F 0F 0F 0F 0F 0F 0F 0F 03 F7"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(r.Bytes) != 6 || !isAllFF(r.Bytes) {
		t.Fatalf("connect address bytes = % X, want FF x6", r.Bytes)
	}
	if _, ok := r.MAC(); ok {
		t.Fatalf("empty slot should report no MAC")
	}
	if got := Describe(RegConnectAddress1, r.Bytes); got != "none" {
		t.Fatalf("describe = %v, want none", got)
	}
}

func TestDecodeConnectAddressMAC(t *testing.T) {
	// Synthesize a reply carrying 66 AC DA AB 2E 10 (wire/reversed order) so the
	// display MAC is 10:2E:AB:DA:AC:66.
	wire := []byte{0x66, 0xAC, 0xDA, 0xAB, 0x2E, 0x10}
	body := append([]byte{byte(RegConnectAddress2), 0x06}, encodeNibbles(wire)...)
	frame := Frame(0x0B, CmdReadSettings|ReplyBit, body)
	r, err := Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	mac, ok := r.MAC()
	if !ok || mac.String() != "10:2e:ab:da:ac:66" {
		t.Fatalf("MAC = %v ok=%v, want 10:2e:ab:da:ac:66", mac, ok)
	}
}

func TestDecodeError(t *testing.T) {
	r, err := Decode(hexBytes(t, "F0 00 20 63 0F 0B 42 7F 41 F7"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Kind != ReplyError || r.ErrCode != 0x7F || r.ErrName != "SYSX_UNKNOWN_PARAMETER" || r.ErrParam != 0x41 {
		t.Fatalf("error reply = %+v", r)
	}
}

func TestDecodeStatusSerial(t *testing.T) {
	r, err := Decode(hexBytes(t, "F0 00 20 63 0F 0B 41 00 1A 00 03 02 03 03 03 00 03 00 03 00 03 00 03 02 03 67 52 17 33 06 13 08 00 00 00 1E F7"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Kind != ReplyStatus || r.Status != StatusSerial {
		t.Fatalf("status reply = %+v", r)
	}
	if len(r.StatusData) == 0 {
		t.Fatalf("expected status payload")
	}
}

func TestIsReplyRejectsRequests(t *testing.T) {
	// A request (no reply bit) must not be mistaken for a reply.
	req := BuildReadSetting(0x12, RegTXPower)
	if IsReply(req) {
		t.Fatalf("request was classified as a reply: % X", req)
	}
}

func TestResolveDevID(t *testing.T) {
	if d, err := ResolveDevID("jack", -1); err != nil || d != 0x0B {
		t.Fatalf("jack -> 0x%02X err=%v, want 0x0B", d, err)
	}
	if d, err := ResolveDevID("", 0x12); err != nil || d != 0x12 {
		t.Fatalf("devID 0x12 -> 0x%02X err=%v", d, err)
	}
	if _, err := ResolveDevID("thru6", 0x09); err == nil {
		t.Fatalf("mismatched product/devID should error")
	}
	if _, err := ResolveDevID("", -1); err == nil {
		t.Fatalf("missing both should error")
	}
}

func TestSettingEncode(t *testing.T) {
	role, _ := SettingByKey("ble_role")
	if w, err := role.Encode("peripheral"); err != nil || w != RolePeripheral {
		t.Fatalf("encode peripheral = %d err=%v", w, err)
	}
	if _, err := role.Encode("bogus"); err == nil {
		t.Fatalf("bogus role should error")
	}
	tx, _ := SettingByKey("tx_power")
	if w, err := tx.Encode("+5"); err != nil || w != 14 {
		t.Fatalf("encode +5 dBm = %d err=%v, want 14", w, err)
	}
	if w, err := tx.Encode(float64(0)); err != nil || w != 0 {
		t.Fatalf("encode wire 0 = %d err=%v", w, err)
	}
}
