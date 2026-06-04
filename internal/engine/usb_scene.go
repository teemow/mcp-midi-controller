package engine

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/scene"
)

// rolandPatchWriteAddr is the Roland/Boss editor PATCH_WRITE command: a DT1 of
// "00 <slot>" to this address stores the live edit buffer into a stored slot
// (mutates device memory). It mirrors the mcpserver constant and is used to
// persist a recalled patch-level scene. See docs/research/sl-2.md.
const rolandPatchWriteAddr = 0x7F000104

// CaptureUSBPatch reads size bytes of a USB device's memory into a scene.USBPatch
// so the blob can be stored in a scene and written back on recall. It is the
// patch-level counterpart of capturing a control value into a scene: it dumps
// state (e.g. a Boss SL-2 temp patch) the fire-and-forget control surface cannot
// express. With region set, addr is an offset into that region and index selects
// a repeated block; the same (region, index, addr) is recorded so recall writes
// the blob back to where it came from.
func (e *Engine) CaptureUSBPatch(ctx context.Context, logical, region string, index int, addr int64, size int) (scene.USBPatch, error) {
	data, err := e.USBDump(ctx, logical, region, index, addr, size, 0)
	if err != nil {
		return scene.USBPatch{}, err
	}
	return scene.USBPatch{
		Region: region,
		Index:  index,
		Addr:   addr,
		Hex:    hex.EncodeToString(data),
	}, nil
}

// USBWritePatch writes a captured patch blob back to a USB device, running the
// protocol's pre-write handshake first and, when the patch names a Store slot,
// persisting the edit buffer into that slot afterwards (Roland PATCH_WRITE). It
// is gated by the two-key write model (usb_allow_writes AND the binding's
// Writable) unless dryRun is set. With dryRun it returns the exact frames that
// would be sent without sending them. SysEx readback bytes are 7-bit-safe, so
// the blob is written verbatim in a single data message.
func (e *Engine) USBWritePatch(ctx context.Context, logical string, p scene.USBPatch, dryRun bool) ([][]byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	if !dryRun && !e.usbWritesAllowed(c.binding) {
		return nil, fmt.Errorf("usb writes are disabled for %q (set usb_allow_writes and bind writable: true)", logical)
	}
	if p.Store != nil && c.profile.Protocol != device.USBProtocolRoland {
		return nil, fmt.Errorf("patch store-to-slot is only supported for roland-address-sysex devices, not %q", c.profile.Protocol)
	}
	data, err := hex.DecodeString(p.Hex)
	if err != nil {
		return nil, fmt.Errorf("usb patch hex: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("usb patch for %q is empty", logical)
	}
	abs, err := c.resolveAddr(p.Region, p.Index, p.Addr)
	if err != nil {
		return nil, err
	}
	write := c.codec.BuildWrite(abs, data)
	if write == nil {
		return nil, fmt.Errorf("protocol %q does not support writes", c.profile.Protocol)
	}

	frames := append(c.codec.BuildHandshake(), write)
	if p.Store != nil {
		if *p.Store < 0 || *p.Store > 127 {
			return nil, fmt.Errorf("usb patch store slot %d out of range [0,127]", *p.Store)
		}
		store := c.codec.BuildWrite(rolandPatchWriteAddr, []byte{0x00, byte(*p.Store)})
		if store == nil {
			return nil, fmt.Errorf("protocol %q cannot build a store command", c.profile.Protocol)
		}
		frames = append(frames, store)
	}

	if dryRun {
		return frames, nil
	}
	if err := c.send(ctx, e, frames...); err != nil {
		return nil, err
	}
	return frames, nil
}
