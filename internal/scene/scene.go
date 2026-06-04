// Package scene defines the scene model and persistence. A scene is a named
// snapshot of only the controls that have been explicitly set, so scenes stay
// small, partial and layerable. For preset-based devices it stores the program
// number plus CC overrides.
package scene

// RecallMode controls how a scene is applied.
type RecallMode string

const (
	// Additive applies the scene's values over the current state (good for
	// stacking a base tone + a per-section overlay).
	Additive RecallMode = "additive"
	// Exact resets each referenced device to exactly the scene's values.
	Exact RecallMode = "exact"
)

// Scene is a named, partial snapshot of the rig.
type Scene struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`

	// Devices maps logical device name -> control name -> value. Only controls
	// that were set are present.
	Devices map[string]map[string]any `yaml:"devices"`

	// USB maps a logical USB device name -> a captured memory blob recalled over
	// USB. It is the patch-level part of a scene: state the fire-and-forget
	// control surface cannot reach (e.g. a Boss SL-2 slicer pattern/type, which
	// is not BLE/PC/CC-addressable) is captured as a raw device-memory blob and
	// written back on recall. Optional and absent for CC-only scenes. See
	// docs/usb-tools.md.
	USB map[string]USBPatch `yaml:"usb,omitempty"`
}

// USBPatch is a captured blob of a USB device's memory stored in a scene. On
// recall the blob is written back over USB at the resolved address (running the
// protocol's pre-write handshake), optionally followed by a store-to-slot
// command so the recalled patch is persisted into device memory rather than
// left only in the live edit buffer.
type USBPatch struct {
	// Region/Index/Addr locate where the blob is written, exactly like a USB
	// read/write: with Region set, Addr is an offset into that named region and
	// Index selects a repeated block; with no Region, Addr is absolute.
	Region string `yaml:"region,omitempty"`
	Index  int    `yaml:"index,omitempty"`
	Addr   int64  `yaml:"addr,omitempty"`

	// Hex is the captured memory as a lower-case hex string (no separators).
	// SysEx readback bytes are inherently 7-bit-safe, so the blob is written
	// back verbatim in a single data message.
	Hex string `yaml:"hex"`

	// Store, when set, is a stored slot the live edit buffer is written into
	// after the blob lands (Roland PATCH_WRITE) — i.e. persist the recalled
	// patch into memory. Nil leaves the write in the temporary/edit buffer (the
	// live sound) only. It is Roland-protocol specific.
	Store *int `yaml:"store,omitempty"`
}
