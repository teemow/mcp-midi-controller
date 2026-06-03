// Package widi implements the CME WIDI BLE-MIDI dongle configuration protocol:
// the in-band SysEx register/status interface shared by the WIDI Master, Jack,
// Bud Pro, Thru6 BT, Core and their OEM rebrands.
//
// It is a pure, transport-agnostic library: it builds request SysEx frames and
// decodes reply frames as plain byte slices. The request/reply orchestration
// (sending over BLE-MIDI and matching the reply on the inbound stream) lives in
// the engine, which owns the transport. This split keeps the wire format fully
// unit-testable without hardware.
//
// The wire format is reverse-engineered from the official WIDI App and verified
// live against real hardware; see docs/research/widi.md for the annotated
// protocol notes and the captured byte vectors the tests assert against.
package widi

// Header is the 4-byte CME OEM SysEx header used by every WIDI product
// (manufacturer id 00 20 63, product-group byte 0F).
var Header = [4]byte{0x00, 0x20, 0x63, 0x0F}

// Command ids occupy the low nibble of the command byte. Replies echo the
// command with bit 0x40 set.
const (
	CmdAction        byte = 0x00 // device actions (reboot, factory reset, erase bonds)
	CmdReadStatus    byte = 0x01 // read-only telemetry (serial, BLE role/state, ...)
	CmdReadSettings  byte = 0x02 // read a config register
	CmdWriteSettings byte = 0x03 // write a config register
)

// ReplyBit is set in a reply's command byte (e.g. 0x42 = READ_SETTINGS reply).
const ReplyBit byte = 0x40

// Register identifies a WIDI configuration register (the v1.g enum in the app).
type Register byte

const (
	RegBLEName               Register = 0  // device name (string, write-only in practice)
	RegTXPower               Register = 1  // BLE transmit power index (see TXPowerDBm)
	RegBLEPHYSwitch          Register = 2  // PHY preference (unsupported on many products)
	RegScanDuration          Register = 3  // central scan window (word-encoded)
	RegAdvertiseDuration     Register = 4  // peripheral advertise window (word-encoded)
	RegPowerSaving           Register = 5  // power-saving on/off
	RegForceBLERole          Register = 6  // 0 = AUTO, 1 = PERIPHERAL
	RegConnectAddress1       Register = 7  // wireless-group peer 1 MAC (6 bytes)
	RegConnectAddress2       Register = 8  // wireless-group peer 2 MAC
	RegConnectAddress3       Register = 9  // wireless-group peer 3 MAC
	RegConnectAddress4       Register = 10 // wireless-group peer 4 MAC
	RegPreferLatencyJitter   Register = 11 // 0 = prefer latency, 1 = prefer jitter
	RegInternalClockTempoBPM Register = 12 // internal MIDI-clock tempo, BPM (unsupported on many)
	RegInternalClockTempoMS  Register = 13 // internal MIDI-clock tempo, ms (unsupported on many)
	RegMIDIInThru            Register = 14 // MIDI IN -> THRU echo on/off
)

// RegKind classifies how a register's payload is encoded on the wire, which
// determines how a reply's bytes are interpreted.
type RegKind int

const (
	KindByte    RegKind = iota // single logical byte, low-nibble-first
	KindWord                   // wider 7-bit "word" encoding (durations)
	KindAddress                // 6-byte MAC, byte-reversed on the wire
	KindString                 // nibble-encoded characters
)

type regInfo struct {
	name string
	leng byte // number of logical bytes the register holds
	kind RegKind
}

var registers = map[Register]regInfo{
	RegBLEName:               {"BLE_NAME", 32, KindString},
	RegTXPower:               {"TX_POWER", 1, KindByte},
	RegBLEPHYSwitch:          {"BLE_PHY_SWITCH", 1, KindByte},
	RegScanDuration:          {"SCAN_DURATION", 1, KindWord},
	RegAdvertiseDuration:     {"ADVERTISE_DURATION", 1, KindWord},
	RegPowerSaving:           {"POWER_SAVING", 1, KindByte},
	RegForceBLERole:          {"FORCE_BLE_ROLE", 1, KindByte},
	RegConnectAddress1:       {"CONNECT_ADDRESS_1", 6, KindAddress},
	RegConnectAddress2:       {"CONNECT_ADDRESS_2", 6, KindAddress},
	RegConnectAddress3:       {"CONNECT_ADDRESS_3", 6, KindAddress},
	RegConnectAddress4:       {"CONNECT_ADDRESS_4", 6, KindAddress},
	RegPreferLatencyJitter:   {"PREFER_LATENCY_JITTER", 1, KindByte},
	RegInternalClockTempoBPM: {"INTERNAL_CLOCK_TEMPO_BPM", 1, KindByte},
	RegInternalClockTempoMS:  {"INTERNAL_CLOCK_TEMPO_MS", 1, KindByte},
	RegMIDIInThru:            {"MIDI_IN_THRU", 1, KindByte},
}

// Name returns the register's symbolic name (e.g. "FORCE_BLE_ROLE").
func (r Register) Name() string {
	if info, ok := registers[r]; ok {
		return info.name
	}
	return "REG_" + itoa(int(r))
}

// Len returns the register's length in logical bytes.
func (r Register) Len() byte {
	if info, ok := registers[r]; ok {
		return info.leng
	}
	return 1
}

// Kind returns how the register's payload is encoded.
func (r Register) Kind() RegKind {
	if info, ok := registers[r]; ok {
		return info.kind
	}
	return KindByte
}

// ConnectAddressRegisters are the four wireless-group peer slots, in order.
var ConnectAddressRegisters = []Register{
	RegConnectAddress1, RegConnectAddress2, RegConnectAddress3, RegConnectAddress4,
}

// ConfigRegisters is the read sweep used to snapshot a dongle's settings, in a
// stable, human-sensible order.
var ConfigRegisters = []Register{
	RegTXPower, RegBLEPHYSwitch, RegScanDuration, RegAdvertiseDuration,
	RegPowerSaving, RegForceBLERole,
	RegConnectAddress1, RegConnectAddress2, RegConnectAddress3, RegConnectAddress4,
	RegPreferLatencyJitter, RegInternalClockTempoBPM, RegInternalClockTempoMS,
	RegMIDIInThru,
}

// Status identifies a read-only telemetry value (the v1.h enum in the app).
type Status byte

const (
	StatusSerial       Status = 0 // serial number (also carries fw/hw version)
	StatusBLERole      Status = 1 // central/peripheral, actual
	StatusBLEState     Status = 2
	StatusBLEConnAddr  Status = 3 // connected peer MAC
	StatusConnRSSI     Status = 4
	StatusConnInterval Status = 5
	StatusMTUSize      Status = 6
	StatusBLEPHY       Status = 7 // 1M / 2M / coded
	StatusUSBIC        Status = 8 // USB chip status (Bud Pro / Uhost / WIDIFLEX USB)
)

// Action ids for CmdAction. The licensing actions (ACTIVATE/UNACTIVATE) use a
// special checksum and are intentionally not built by this library.
const (
	ActionDisconnect          byte = 0
	ActionRebootBootloader    byte = 1
	ActionActivate            byte = 2
	ActionResetFactoryDefault byte = 3
	ActionRebootNormal        byte = 4
	ActionEraseAllBonds       byte = 5
	ActionUnactivate          byte = 6
	ActionLoop                byte = 7
)

// BLE role values for RegForceBLERole.
const (
	RoleAuto       byte = 0
	RolePeripheral byte = 1
)

// TXPowerDBm maps the RegTXPower index (0..14) to its transmit power in dBm.
var TXPowerDBm = []int{-20, -18, -15, -12, -10, -9, -6, -5, -3, 0, 1, 2, 3, 4, 5}

// TXPowerIndex returns the RegTXPower index for a dBm value, if it is one of
// the 15 supported levels.
func TXPowerIndex(dBm int) (byte, bool) {
	for i, v := range TXPowerDBm {
		if v == dBm {
			return byte(i), true
		}
	}
	return 0, false
}

// TXPowerDBmForIndex returns the dBm for a RegTXPower index.
func TXPowerDBmForIndex(idx byte) (int, bool) {
	if int(idx) < len(TXPowerDBm) {
		return TXPowerDBm[idx], true
	}
	return 0, false
}

// sysexErrors maps the device's error reply codes to their symbolic names.
var sysexErrors = map[byte]string{
	127: "SYSX_UNKNOWN_PARAMETER",
	126: "SYSEX_WRONG_CHECKSUM",
	125: "SYSEX_WRONG_EOX",
	124: "SYSEX_PARAM_OUT_OF_RANGE",
	123: "SYSEX_BLE_NOT_CONNECTED",
	122: "SYSEX_PARAM_UNEXPECTED",
}

// itoa is a tiny dependency-free int->string for register fallbacks.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
