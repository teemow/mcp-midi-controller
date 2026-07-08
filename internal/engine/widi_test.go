package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/widi"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
)

// fakeDongle is a transport double that emulates a WIDI dongle: it answers
// READ_SETTINGS / WRITE_SETTINGS round-trips from an in-memory register store
// so the engine's request/reply orchestration can be tested without hardware.
// It registers as "blemidi" because the WIDI engine methods default to that
// transport.
type fakeDongle struct {
	devID byte

	mu          sync.Mutex
	regs        map[widi.Register]byte
	unsupported map[widi.Register]bool
	inbound     chan transport.Event
}

func newFakeDongle(devID byte) *fakeDongle {
	return &fakeDongle{
		devID: devID,
		regs: map[widi.Register]byte{
			widi.RegForceBLERole: widi.RoleAuto,
			widi.RegTXPower:      14, // +5 dBm
			widi.RegMIDIInThru:   1,
		},
		unsupported: map[widi.Register]bool{
			widi.RegBLEPHYSwitch:          true,
			widi.RegInternalClockTempoBPM: true,
			widi.RegInternalClockTempoMS:  true,
		},
		inbound: make(chan transport.Event, 64),
	}
}

func (f *fakeDongle) ID() string                                             { return "blemidi" }
func (f *fakeDongle) Discover(context.Context) ([]transport.Endpoint, error) { return nil, nil }
func (f *fakeDongle) Pair(context.Context, string) error                     { return nil }
func (f *fakeDongle) Connect(context.Context, string) error                  { return nil }
func (f *fakeDongle) Disconnect(context.Context, string) error               { return nil }

func (f *fakeDongle) Send(_ context.Context, _ string, ev transport.Event) error {
	if reply := f.respond(ev.Data); reply != nil {
		f.inbound <- transport.Event{Kind: transport.MIDIEvent, Data: reply}
	}
	return nil
}

func (f *fakeDongle) Listen(ctx context.Context, _ string) (<-chan transport.Event, error) {
	out := make(chan transport.Event, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-f.inbound:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// respond emulates the dongle's reply to a WIDI request.
func (f *fakeDongle) respond(req []byte) []byte {
	if len(req) < 9 || req[0] != 0xF0 || req[5] != f.devID {
		return nil
	}
	cmd := req[6]
	data := req[7 : len(req)-2] // strip cmd-preceding header is already gone; drop checksum+F7
	reg := widi.Register(data[0])

	f.mu.Lock()
	defer f.mu.Unlock()
	switch cmd {
	case widi.CmdReadSettings:
		if f.unsupported[reg] {
			return []byte{0xF0, 0x00, 0x20, 0x63, 0x0F, f.devID, cmd | widi.ReplyBit, 127, 0x41, 0xF7}
		}
		if reg.Kind() == widi.KindAddress {
			ff := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
			body := append([]byte{byte(reg), 0x06}, encodeTestNibbles(ff)...)
			return widi.Frame(f.devID, cmd|widi.ReplyBit, body)
		}
		val := f.regs[reg]
		body := append([]byte{byte(reg), 0x01}, encodeTestNibbles([]byte{val})...)
		return widi.Frame(f.devID, cmd|widi.ReplyBit, body)
	case widi.CmdWriteSettings:
		if reg.Kind() != widi.KindAddress {
			// data = [reg, count, low, high]
			f.regs[reg] = (data[2] & 0x0F) | ((data[3] & 0x0F) << 4)
		}
		body := []byte{byte(reg), 0x01, 0x00, 0x00}
		return widi.Frame(f.devID, cmd|widi.ReplyBit, body)
	}
	return nil
}

func encodeTestNibbles(b []byte) []byte {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, x&0x0F, (x>>4)&0x0F)
	}
	return out
}

func newWIDIEngine(devID byte) (*Engine, *fakeDongle) {
	d := newFakeDongle(devID)
	return New(device.NewRegistry(), d), d
}

func TestWriteWIDISettingVerified(t *testing.T) {
	eng, _ := newWIDIEngine(0x12)
	res, err := eng.WriteWIDISetting(context.Background(), "AA:BB", 0x12, "ble_role", "peripheral", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.Verified {
		t.Fatalf("expected verified write, got %+v", res)
	}
	if res.Before != "auto" || res.After != "peripheral" || res.Wrote != "peripheral" {
		t.Fatalf("res = %+v", res)
	}
}

func TestWriteWIDISettingUnknown(t *testing.T) {
	eng, _ := newWIDIEngine(0x12)
	if _, err := eng.WriteWIDISetting(context.Background(), "AA:BB", 0x12, "bogus", "x", 200*time.Millisecond); err == nil {
		t.Fatalf("expected error for unknown setting")
	}
}

func TestReadWIDIConfig(t *testing.T) {
	eng, _ := newWIDIEngine(0x0B)
	cfg, err := eng.ReadWIDIConfig(context.Background(), "AA:BB", 0x0B, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.Product != "WIDI Jack" || cfg.DevID != 0x0B {
		t.Fatalf("cfg meta = %+v", cfg)
	}
	by := map[string]WIDISetting{}
	for _, s := range cfg.Settings {
		by[s.Register] = s
	}
	if s := by["FORCE_BLE_ROLE"]; !s.Supported || s.Value != "auto" {
		t.Fatalf("role = %+v", s)
	}
	if s := by["TX_POWER"]; !s.Supported || s.Value != "+5 dBm" {
		t.Fatalf("tx_power = %+v", s)
	}
	if s := by["BLE_PHY_SWITCH"]; s.Supported || s.Error != "SYSX_UNKNOWN_PARAMETER" {
		t.Fatalf("phy switch should be unsupported, got %+v", s)
	}
	if s := by["CONNECT_ADDRESS_1"]; !s.Supported || s.Value != "none" {
		t.Fatalf("connect address = %+v", s)
	}
}

func TestClearWIDIGroup(t *testing.T) {
	eng, _ := newWIDIEngine(0x12)
	cfg, err := eng.ClearWIDIGroup(context.Background(), "AA:BB", 0x12, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("clear group: %v", err)
	}
	for _, s := range cfg.Settings {
		if s.Register == "CONNECT_ADDRESS_1" && s.Value != "none" {
			t.Fatalf("slot 1 = %+v, want none", s)
		}
	}
}
