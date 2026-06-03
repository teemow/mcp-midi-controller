package widi

import (
	"fmt"
	"sort"
	"strconv"
)

// Setting is a friendly, writable single-byte configuration setting: a stable
// key, the register it maps to, and an optional label->wire enum. It is the
// vocabulary the configuration tools expose, keeping the value semantics (e.g.
// "peripheral" -> 1) in the library rather than scattered across callers.
type Setting struct {
	Key      string
	Register Register
	Doc      string
	Enum     map[string]byte // label -> wire value; nil means a plain 0..127 int
}

// WritableSettings are the single-byte settings the configuration tools expose.
// The multi-byte CONNECT_ADDRESS group registers are handled separately (they
// are a pairing-changing, multi-register operation).
var WritableSettings = []Setting{
	{
		Key: "tx_power", Register: RegTXPower,
		Doc:  "BLE transmit power in dBm (range/battery trade-off).",
		Enum: txPowerEnum(),
	},
	{
		Key: "ble_role", Register: RegForceBLERole,
		Doc:  "BLE role. 'peripheral' pins the dongle as an advertiser so it only connects to a central (e.g. an iPad) and never to another dongle; 'auto' lets it negotiate.",
		Enum: map[string]byte{"auto": RoleAuto, "peripheral": RolePeripheral},
	},
	{
		Key: "power_saving", Register: RegPowerSaving,
		Doc:  "Enable BLE power-saving.",
		Enum: map[string]byte{"off": 0, "on": 1},
	},
	{
		Key: "prefer", Register: RegPreferLatencyJitter,
		Doc:  "Optimise the link for latency or jitter.",
		Enum: map[string]byte{"latency": 0, "jitter": 1},
	},
	{
		Key: "midi_in_thru", Register: RegMIDIInThru,
		Doc:  "Echo MIDI IN to THRU on the dongle.",
		Enum: map[string]byte{"off": 0, "on": 1},
	},
}

// SettingByKey resolves a writable setting by its key.
func SettingByKey(key string) (Setting, bool) {
	for _, s := range WritableSettings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// SettingKeys returns the writable setting keys, in table order.
func SettingKeys() []string {
	keys := make([]string, len(WritableSettings))
	for i, s := range WritableSettings {
		keys[i] = s.Key
	}
	return keys
}

// Labels returns the enum labels for a setting, sorted, or nil for int values.
func (s Setting) Labels() []string {
	if s.Enum == nil {
		return nil
	}
	out := make([]string, 0, len(s.Enum))
	for k := range s.Enum {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Encode resolves a caller-supplied value (an enum label, or a number for
// int/enum-by-wire) to the wire byte to write.
func (s Setting) Encode(value any) (byte, error) {
	if s.Enum != nil {
		if str, ok := value.(string); ok {
			if w, ok := s.Enum[str]; ok {
				return w, nil
			}
			// Allow a numeric string that names a defined wire value too.
			if n, err := strconv.Atoi(str); err == nil {
				return s.encodeWire(n)
			}
			return 0, fmt.Errorf("%s: must be one of %v", s.Key, s.Labels())
		}
		n, err := toInt(value)
		if err != nil {
			return 0, fmt.Errorf("%s: must be one of %v", s.Key, s.Labels())
		}
		return s.encodeWire(n)
	}
	// Plain integer setting.
	n, err := toInt(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %v", s.Key, err)
	}
	if n < 0 || n > 127 {
		return 0, fmt.Errorf("%s: value must be in [0, 127]", s.Key)
	}
	return byte(n), nil
}

// encodeWire accepts a raw wire value only if the enum defines it.
func (s Setting) encodeWire(n int) (byte, error) {
	for _, w := range s.Enum {
		if int(w) == n {
			return byte(n), nil
		}
	}
	return 0, fmt.Errorf("%s: must be one of %v", s.Key, s.Labels())
}

func txPowerEnum() map[string]byte {
	m := make(map[string]byte, len(TXPowerDBm))
	for i, dBm := range TXPowerDBm {
		m[fmt.Sprintf("%+d", dBm)] = byte(i)
	}
	return m
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64: // JSON numbers decode as float64
		if n != float64(int(n)) {
			return 0, fmt.Errorf("expected a whole number, got %v", n)
		}
		return int(n), nil
	case float32:
		return int(n), nil
	case string:
		return strconv.Atoi(n)
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}
