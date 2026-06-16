package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
)

// fakeUSBTransport is an in-memory transport for the USB engine tests. Its ID is
// configurable so it can stand in for usbmidi (SysEx) or usbhid (raw reports).
// When reply is set, each sent event is answered by pushing the produced frames
// onto the inbound stream, exercising the request/reply session without
// hardware.
type fakeUSBTransport struct {
	id      string
	mu      sync.Mutex
	sent    []transport.Event
	inbound chan transport.Event
	reply   func(transport.Event) []transport.Event
}

func newFakeUSBTransport(id string) *fakeUSBTransport {
	return &fakeUSBTransport{id: id, inbound: make(chan transport.Event, 64)}
}

func (f *fakeUSBTransport) ID() string { return f.id }

func (f *fakeUSBTransport) Discover(context.Context) ([]transport.Endpoint, error) {
	return []transport.Endpoint{{ID: "USB1", Name: "USB1", Transport: f.id, Paired: true}}, nil
}
func (f *fakeUSBTransport) Pair(context.Context, string) error       { return nil }
func (f *fakeUSBTransport) Connect(context.Context, string) error    { return nil }
func (f *fakeUSBTransport) Disconnect(context.Context, string) error { return nil }

func (f *fakeUSBTransport) Send(_ context.Context, _ string, ev transport.Event) error {
	f.mu.Lock()
	f.sent = append(f.sent, ev)
	reply := f.reply
	f.mu.Unlock()
	if reply != nil {
		for _, r := range reply(ev) {
			f.inbound <- r
		}
	}
	return nil
}

func (f *fakeUSBTransport) Listen(ctx context.Context, _ string) (<-chan transport.Event, error) {
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

// sl2TestYAML is a Roland (SL-2-like) device carrying both a control surface and
// a usb profile, used by the USB engine tests.
const sl2TestYAML = `id: sl2test
name: SL-2 Test
transport: blemidi
controls:
  - name: level
    type: cc
    cc: 17
    value: { type: range, min: 0, max: 127 }
usb:
  protocol: roland-address-sysex
  transport: usbmidi
  addr_bytes: 4
  size_bytes: 4
  identity:
    mfg: 0x41
    model: "00 00 00 00 1D"
    device: 0x10
  regions:
    system:
      base: 0x10000000
    patches:
      base: 0x20000000
      count: 4
      stride: 0x00100000
  params:
    - name: tempo
      region: system
      addr: 0x00
      enc: int4x4
    - name: midi_channel
      region: system
      addr: 0x08
      enc: int1x7
      ofs: 0
`

// newUSBTestEngine loads the SL-2 test device, registers a fake usbmidi
// transport, and binds the logical "sl2usb" to its USB surface.
func newUSBTestEngine(t *testing.T) (*Engine, *fakeUSBTransport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sl2test.yaml"), []byte(sl2TestYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	ft := newFakeUSBTransport("usbmidi")
	eng := New(reg, ft)
	if err := eng.Bind(Device{Name: "sl2usb", DeviceID: "sl2test", Connections: map[string]Connection{"usbmidi": {Endpoint: "USB1"}}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng, ft
}

// rolandReplier answers identity requests with a fixed Identity Reply and RQ1
// reads with a DT1 echoing the requested address and returning data.
func rolandReplier(t *testing.T, data []byte) func(transport.Event) []transport.Event {
	t.Helper()
	const modelLen = 5
	return func(ev transport.Event) []transport.Event {
		req := ev.Data
		if len(req) >= 2 && req[0] == 0xF0 && req[1] == 0x7E {
			// Universal Identity Reply: F0 7E dev 06 02 mfg fam x2 mem x2 rev x4 F7.
			reply := []byte{0xF0, 0x7E, 0x10, 0x06, 0x02, 0x41, 0x1D, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0xF7}
			return []transport.Event{{Kind: transport.MIDIEvent, Data: reply}}
		}
		// RQ1: F0 41 dev <model5> 11 <addr4> <size4> ck F7. addr starts after
		// F0 mfg dev + model + command = 3 + modelLen + 1 = 9.
		const addrOff = 3 + modelLen + 1
		if len(req) < addrOff+4 || req[1] != 0x41 || req[3+modelLen] != 0x11 {
			return nil
		}
		addr := req[addrOff : addrOff+4]
		// Build a DT1 reply: F0 41 dev <model5> 12 <addr4> <data...> ck F7.
		out := []byte{0xF0, 0x41, 0x10, 0x00, 0x00, 0x00, 0x00, 0x1D, 0x12}
		out = append(out, addr...)
		out = append(out, data...)
		// Roland address+data checksum (the decoder now verifies it).
		body := append(append([]byte(nil), addr...), data...)
		sum := 0
		for _, b := range body {
			sum += int(b)
		}
		ck := byte((0x80 - (sum & 0x7F)) & 0x7F)
		out = append(out, ck, 0xF7)
		return []transport.Event{{Kind: transport.MIDIEvent, Data: out}}
	}
}

func TestUSBBindingKind(t *testing.T) {
	eng, _ := newUSBTestEngine(t)
	if !eng.IsUSBDevice("sl2usb") {
		t.Fatalf("sl2usb should be a USB device")
	}

	// A control binding on the same device (flat shorthand) routes over the
	// device type's control transport (blemidi), which is NOT registered in this
	// engine, so Bind must fail — confirming the flat connection resolves to the
	// control transport, not the USB one.
	if err := eng.Bind(Device{Name: "sl2ctl", DeviceID: "sl2test", Endpoint: "AA:BB", Channel: 1}); err == nil {
		t.Fatalf("control binding should fail without its control transport registered")
	}
}

func TestUSBBindTransportMismatch(t *testing.T) {
	eng, _ := newUSBTestEngine(t)
	// usbhid transport does not match the device's usb transport (usbmidi).
	err := eng.Bind(Device{Name: "bad", DeviceID: "sl2test", Connections: map[string]Connection{"usbhid": {Endpoint: "USB1"}}})
	if err == nil {
		t.Fatalf("expected mismatch error binding usbhid against a usbmidi usb profile")
	}
}

func TestUSBIdentify(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	ft.reply = rolandReplier(t, nil)
	id, err := eng.USBIdentify(context.Background(), "sl2usb")
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if id.Manufacturer != 0x41 || id.DeviceID != 0x10 {
		t.Fatalf("identity = %+v", id)
	}
}

func TestUSBReadAndGetParam(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	// Tempo 1220 -> int4x4 nibbles 00 04 0C 04.
	ft.reply = rolandReplier(t, []byte{0x00, 0x04, 0x0C, 0x04})

	gotAddr, data, err := eng.USBRead(context.Background(), "sl2usb", "system", 0, 0x00, 4)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if gotAddr != 0x10000000 {
		t.Fatalf("addr = 0x%X, want 0x10000000", gotAddr)
	}
	if len(data) != 4 || data[1] != 0x04 || data[2] != 0x0C {
		t.Fatalf("data = % X", data)
	}

	v, err := eng.USBGetParam(context.Background(), "sl2usb", "tempo")
	if err != nil {
		t.Fatalf("get_param: %v", err)
	}
	if n, ok := v.(int); !ok || n != 1220 {
		t.Fatalf("tempo = %#v, want int 1220", v)
	}
}

func TestUSBSetParamDryRunAndDesiredState(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	ft.reply = rolandReplier(t, nil)

	// Dry run builds the frame but sends nothing and records no desired-state.
	frame, err := eng.USBSetParam(context.Background(), "sl2usb", "tempo", 1220, true)
	if err != nil {
		t.Fatalf("set_param dry: %v", err)
	}
	// Frame: F0 41 10 <model5> 12 <addr4> <data4> ck F7; data at offset 13.
	if len(frame) < 18 {
		t.Fatalf("frame too short: % X", frame)
	}
	data := frame[13:17]
	if data[0] != 0x00 || data[1] != 0x04 || data[2] != 0x0C || data[3] != 0x04 {
		t.Fatalf("encoded tempo = % X, want 00 04 0C 04", data)
	}
	if len(ft.sent) != 0 {
		t.Fatalf("dry run should not send, sent %d", len(ft.sent))
	}
	if v, ok := eng.State().Device("sl2usb")["tempo"]; ok {
		t.Fatalf("dry run should not record desired-state, got %v", v)
	}

	// A real set sends (handshake + write) and records desired-state.
	if _, err := eng.USBSetParam(context.Background(), "sl2usb", "tempo", 1220, false); err != nil {
		t.Fatalf("set_param: %v", err)
	}
	if len(ft.sent) < 2 {
		t.Fatalf("expected handshake + write, sent %d", len(ft.sent))
	}
	if eng.State().Device("sl2usb")["tempo"] != 1220 {
		t.Fatalf("desired-state tempo = %v, want 1220", eng.State().Device("sl2usb")["tempo"])
	}
}

func TestUSBSetParamBounds(t *testing.T) {
	eng, _ := newUSBTestEngine(t)
	// midi_channel has no bounds in the test profile, so this checks the
	// encode path accepts a plain int; tempo overflow checks the encoding cap.
	if _, err := eng.USBSetParam(context.Background(), "sl2usb", "tempo", 1<<20, true); err == nil {
		t.Fatalf("expected overflow error for an int4x4 value that does not fit")
	}
}

func TestUSBListBlocks(t *testing.T) {
	eng, _ := newUSBTestEngine(t)
	blocks, err := eng.USBListBlocks(context.Background(), "sl2usb", "patches")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(blocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(blocks))
	}
	if blocks[0].Addr != 0x20000000 || blocks[1].Addr != 0x20100000 {
		t.Fatalf("block addrs = %+v", blocks)
	}

	if _, err := eng.USBListBlocks(context.Background(), "sl2usb", "system"); err == nil {
		t.Fatalf("expected error listing a non-repeated region")
	}
}

func TestUSBProbe(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	ft.reply = rolandReplier(t, nil)
	res, err := eng.USBProbe(context.Background(), "usbmidi", "USB1", "roland-address-sysex")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Identity == nil || res.Identity.Manufacturer != 0x41 {
		t.Fatalf("probe identity = %+v", res.Identity)
	}
}

func TestUSBMonitor(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	// Push an unsolicited frame shortly after monitoring starts.
	go func() {
		time.Sleep(30 * time.Millisecond)
		ft.inbound <- transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xF0, 0x41, 0x10, 0x00, 0xF7}}
	}()
	frames, err := eng.USBMonitor(context.Background(), "usbmidi", "USB1", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if len(frames) != 1 || frames[0][1] != 0x41 {
		t.Fatalf("frames = %v", frames)
	}
}

func TestUSBReadNonUSBBinding(t *testing.T) {
	eng, _ := newTestEngine(t) // "amp" is a control binding on the fake transport
	if _, _, err := eng.USBRead(context.Background(), "amp", "", 0, 0, 4); err == nil {
		t.Fatalf("expected error reading a non-USB binding")
	}
}

func TestParseHexBytes(t *testing.T) {
	b, err := parseHexBytes("00 00 00 00 1D")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(b) != 5 || b[4] != 0x1D {
		t.Fatalf("parsed = % X", b)
	}
	if _, err := parseHexBytes("zz"); err == nil {
		t.Fatalf("expected error for bad hex")
	}
}
