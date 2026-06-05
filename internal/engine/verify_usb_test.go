package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

// verifyUSBYAML is a device with a BLE control ("patch") whose name matches a
// USB-profile param ("patch"), so verify_control can read the value back over
// USB and close the open loop. The control transport is the in-memory "fake"
// transport; the USB surface is the in-memory "usbmidi" transport.
const verifyUSBYAML = `id: vdev
name: Verify Device
transport: fake
controls:
  - name: patch
    type: cc
    cc: 30
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
  params:
    - name: patch
      region: system
      addr: 0x00
      enc: int1x7
`

// newVerifyUSBEngine binds a control surface ("v" on the fake transport) and a
// USB surface ("vusb" on the usbmidi transport) to the same device, so a
// verify_control on "v.patch" can read back over "vusb".
func newVerifyUSBEngine(t *testing.T) (*Engine, *fakeTransport, *fakeUSBTransport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vdev.yaml"), []byte(verifyUSBYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	ble := newFakeTransport()
	usb := newFakeUSBTransport("usbmidi")
	eng := New(reg, ble, usb)
	if err := eng.Bind(Device{Name: "v", DeviceID: "vdev", Endpoint: "EP1", Channel: 0}); err != nil {
		t.Fatalf("bind control: %v", err)
	}
	if err := eng.Bind(Device{Name: "vusb", DeviceID: "vdev", Connections: map[string]Connection{"usbmidi": {Endpoint: "USB1"}}}); err != nil {
		t.Fatalf("bind usb: %v", err)
	}
	return eng, ble, usb
}

func TestVerifyControlUSBReadbackConfirms(t *testing.T) {
	eng, _, usb := newVerifyUSBEngine(t)
	// No MIDI echo on the control transport, but the USB readback returns the
	// value we set (patch = 5), so verify confirms via USB.
	usb.reply = rolandReplier(t, []byte{0x05})

	res, err := eng.VerifyControl(context.Background(), "v", "patch", 5, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusConfirmed {
		t.Fatalf("status = %q, want confirmed (res=%+v)", res.Status, res)
	}
	if !res.USBChecked || res.USBSource != "usb:vusb" {
		t.Fatalf("usb fields = %+v", res)
	}
	if n, ok := res.USBValue.(int); !ok || n != 5 {
		t.Fatalf("usb value = %#v, want int 5", res.USBValue)
	}
}

func TestVerifyControlUSBReadbackMismatch(t *testing.T) {
	eng, _, usb := newVerifyUSBEngine(t)
	// The device actually holds 9, not the 5 we set -> USB readback overrides
	// the MIDI no_feedback with a mismatch.
	usb.reply = rolandReplier(t, []byte{0x09})

	res, err := eng.VerifyControl(context.Background(), "v", "patch", 5, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusMismatch {
		t.Fatalf("status = %q, want mismatch (res=%+v)", res.Status, res)
	}
	if !res.USBChecked || res.USBValue.(int) != 9 {
		t.Fatalf("usb fields = %+v", res)
	}
}

func TestVerifyControlNoUSBBindingUnaffected(t *testing.T) {
	// Without a USB binding for the device, verify behaves as before: a control
	// with no echo reports no_feedback and no USB fields.
	eng, _ := newTestEngine(t)
	res, err := eng.VerifyControl(context.Background(), "amp", "level", 100, 120*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusNoFeedback || res.USBChecked {
		t.Fatalf("res = %+v, want no_feedback and no usb check", res)
	}
}
