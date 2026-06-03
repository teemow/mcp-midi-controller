package widi

import (
	"fmt"
	"net"
)

// checksum is the WIDI SysEx checksum: (cmd + Σdata) & 0x7F.
func checksum(cmd byte, data []byte) byte {
	sum := int(cmd)
	for _, b := range data {
		sum += int(b)
	}
	return byte(sum & 0x7F)
}

// Frame assembles a complete WIDI SysEx message:
//
//	F0  00 20 63 0F  <devID>  <cmd>  <data...>  <checksum>  F7
func Frame(devID, cmd byte, data []byte) []byte {
	out := make([]byte, 0, len(data)+9)
	out = append(out, 0xF0)
	out = append(out, Header[:]...)
	out = append(out, devID, cmd)
	out = append(out, data...)
	out = append(out, checksum(cmd, data), 0xF7)
	return out
}

// encodeNibbles splits each byte into two 7-bit SysEx bytes, low nibble first
// (the app's assignByteToBufferNibbles): byte b -> [b&0x0F, (b>>4)&0x0F].
func encodeNibbles(b []byte) []byte {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, x&0x0F, (x>>4)&0x0F)
	}
	return out
}

// DecodeNibbles reverses encodeNibbles: low | (high << 4). A trailing odd
// nibble (e.g. an unexpected checksum byte) is ignored.
func DecodeNibbles(nib []byte) []byte {
	out := make([]byte, 0, len(nib)/2)
	for i := 0; i+1 < len(nib); i += 2 {
		out = append(out, (nib[i]&0x0F)|((nib[i+1]&0x0F)<<4))
	}
	return out
}

// BuildReadSetting builds a READ_SETTINGS request for a register, using the
// register's declared logical length.
func BuildReadSetting(devID byte, reg Register) []byte {
	return Frame(devID, CmdReadSettings, []byte{byte(reg), reg.Len()})
}

// BuildWriteByte builds a WRITE_SETTINGS request for a single-byte register
// (writeByteSettings): data = [regId, 0x01, low-nibble, high-nibble].
func BuildWriteByte(devID byte, reg Register, val byte) []byte {
	return Frame(devID, CmdWriteSettings, append([]byte{byte(reg), 0x01}, encodeNibbles([]byte{val})...))
}

// BuildWriteAddress builds a WRITE_SETTINGS request for a 6-byte CONNECT_ADDRESS
// register (writeReverseByteArraySettings): the MAC is written byte-reversed
// (display order -> wire order), then nibble-encoded. data = [regId, 0x06,
// <12 nibbles>].
func BuildWriteAddress(devID byte, reg Register, mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, fmt.Errorf("widi: CONNECT_ADDRESS needs a 6-byte MAC, got %d bytes", len(mac))
	}
	rev := reverse6(mac)
	return Frame(devID, CmdWriteSettings, append([]byte{byte(reg), 0x06}, encodeNibbles(rev[:])...)), nil
}

// BuildClearAddress builds the request that clears a CONNECT_ADDRESS slot by
// writing the FF×6 empty sentinel.
func BuildClearAddress(devID byte, reg Register) []byte {
	empty := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	return Frame(devID, CmdWriteSettings, append([]byte{byte(reg), 0x06}, encodeNibbles(empty)...))
}

// BuildReadStatus builds a READ_STATUS request.
func BuildReadStatus(devID byte, s Status) []byte {
	return Frame(devID, CmdReadStatus, []byte{byte(s)})
}

// BuildAction builds an ACTION request. The licensing actions (ACTIVATE/
// UNACTIVATE) require a special token checksum and must not be built here.
func BuildAction(devID, actionID byte) ([]byte, error) {
	if actionID == ActionActivate || actionID == ActionUnactivate {
		return nil, fmt.Errorf("widi: ACTIVATE/UNACTIVATE use a token checksum and are out of scope")
	}
	return Frame(devID, CmdAction, []byte{actionID}), nil
}

// reverse6 returns the 6 bytes in reverse order (display <-> wire endianness).
func reverse6(b net.HardwareAddr) [6]byte {
	var out [6]byte
	for i := 0; i < 6; i++ {
		out[i] = b[5-i]
	}
	return out
}
