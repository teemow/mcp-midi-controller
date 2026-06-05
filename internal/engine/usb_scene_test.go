package engine

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/scene"
)

// writableUSBEngine returns the SL-2 test engine with the gate open and the
// USB binding marked writable, so patch writes are permitted.
func writableUSBEngine(t *testing.T) (*Engine, *fakeUSBTransport) {
	t.Helper()
	eng, ft := newUSBTestEngine(t)
	eng.SetUSBAllowWrites(true)
	if err := eng.Bind(Device{Name: "sl2usb", DeviceID: "sl2test", Connections: map[string]Connection{"usbmidi": {Endpoint: "USB1", Writable: true}}}); err != nil {
		t.Fatalf("rebind writable: %v", err)
	}
	return eng, ft
}

func TestCaptureUSBPatchRoundTrip(t *testing.T) {
	eng, ft := newUSBTestEngine(t)
	want := []byte{0x00, 0x01, 0x02, 0x03, 0x40, 0x41, 0x42, 0x43}
	ft.reply = rolandReplier(t, want)

	p, err := eng.CaptureUSBPatch(context.Background(), "sl2usb", "patches", 1, 0x00, len(want))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if p.Region != "patches" || p.Index != 1 || p.Addr != 0 {
		t.Fatalf("patch location = %+v", p)
	}
	if got := p.Hex; got != hex.EncodeToString(want) {
		t.Fatalf("patch hex = %q, want %q", got, hex.EncodeToString(want))
	}
}

func TestUSBWritePatchDryRunFrames(t *testing.T) {
	eng, ft := writableUSBEngine(t)

	p := scene.USBPatch{Region: "patches", Index: 1, Addr: 0x00, Hex: "00010203"}
	frames, err := eng.USBWritePatch(context.Background(), "sl2usb", p, true)
	if err != nil {
		t.Fatalf("write dry: %v", err)
	}
	// Handshake (editor-comm mode) + the patch write, no store.
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2 (handshake + write)", len(frames))
	}
	if len(ft.sent) != 0 {
		t.Fatalf("dry run must not send, sent %d", len(ft.sent))
	}
	// The write frame: F0 41 10 <model5> 12 <addr4> <data4> ck F7. Addr is the
	// resolved patches[1] base 0x20000000 + 1*0x00100000 = 0x20100000.
	w := frames[1]
	addr := w[9:13]
	if addr[0] != 0x20 || addr[1] != 0x10 || addr[2] != 0x00 || addr[3] != 0x00 {
		t.Fatalf("write addr = % X, want 20 10 00 00", addr)
	}
	if data := w[13:17]; data[0] != 0x00 || data[1] != 0x01 || data[2] != 0x02 || data[3] != 0x03 {
		t.Fatalf("write data = % X, want 00 01 02 03", data)
	}
}

func TestUSBWritePatchStoreSlotFrames(t *testing.T) {
	eng, _ := writableUSBEngine(t)
	slot := 7
	p := scene.USBPatch{Region: "patches", Index: 0, Addr: 0x00, Hex: "0102", Store: &slot}
	frames, err := eng.USBWritePatch(context.Background(), "sl2usb", p, true)
	if err != nil {
		t.Fatalf("write dry: %v", err)
	}
	// Handshake + write + PATCH_WRITE store.
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3 (handshake + write + store)", len(frames))
	}
	store := frames[2]
	if a := store[9:13]; a[0] != 0x7F || a[1] != 0x00 || a[2] != 0x01 || a[3] != 0x04 {
		t.Fatalf("store addr = % X, want 7F 00 01 04 (PATCH_WRITE)", a)
	}
	if d := store[13:15]; d[0] != 0x00 || d[1] != byte(slot) {
		t.Fatalf("store data = % X, want 00 07", d)
	}
}

func TestUSBWritePatchGated(t *testing.T) {
	// Default engine: gate closed, binding not writable.
	eng, ft := newUSBTestEngine(t)
	p := scene.USBPatch{Region: "patches", Index: 0, Addr: 0x00, Hex: "0102"}
	if _, err := eng.USBWritePatch(context.Background(), "sl2usb", p, false); err == nil {
		t.Fatalf("expected a gated-write error with writes disabled")
	}
	if len(ft.sent) != 0 {
		t.Fatalf("nothing should be sent when the gate is closed, sent %d", len(ft.sent))
	}

	// Open the gate: the write goes through (handshake + write).
	eng2, ft2 := writableUSBEngine(t)
	if _, err := eng2.USBWritePatch(context.Background(), "sl2usb", p, false); err != nil {
		t.Fatalf("write with gate open: %v", err)
	}
	if len(ft2.sent) < 2 {
		t.Fatalf("expected handshake + write to be sent, sent %d", len(ft2.sent))
	}
}

func TestRecallSceneUSBPatch(t *testing.T) {
	patch := scene.USBPatch{Region: "patches", Index: 0, Addr: 0x00, Hex: "0102"}

	// Gate closed: the rest of the scene recalls, the blob is skipped with a
	// warning instead of aborting.
	eng, ft := newUSBTestEngine(t)
	sc := &scene.Scene{Name: "t", Devices: map[string]map[string]any{"sl2usb": {scene.USBPatchControl: patch}}}
	warnings, err := eng.RecallScene(context.Background(), sc, scene.Additive)
	if err != nil {
		t.Fatalf("recall (gate closed): %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one skip warning, got %v", warnings)
	}
	if len(ft.sent) != 0 {
		t.Fatalf("nothing should be sent with the gate closed, sent %d", len(ft.sent))
	}

	// Gate open + writable: the blob is written back.
	eng2, ft2 := writableUSBEngine(t)
	warnings, err = eng2.RecallScene(context.Background(), sc, scene.Additive)
	if err != nil {
		t.Fatalf("recall (gate open): %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(ft2.sent) < 2 {
		t.Fatalf("expected handshake + write to be sent, sent %d", len(ft2.sent))
	}
}

func TestRecallSceneUSBPatchUnbound(t *testing.T) {
	eng, _ := newUSBTestEngine(t)
	sc := &scene.Scene{Name: "t", Devices: map[string]map[string]any{"ghost": {scene.USBPatchControl: scene.USBPatch{Hex: "01"}}}}
	if _, err := eng.RecallScene(context.Background(), sc, scene.Additive); err == nil {
		t.Fatalf("expected an error for a usb patch referencing an unbound device")
	}
}
