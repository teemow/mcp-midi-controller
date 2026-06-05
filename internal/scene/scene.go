// Package scene defines the scene model and persistence. A scene is a named
// snapshot of only the controls that have been explicitly set, so scenes stay
// small, partial and layerable. For preset-based devices it stores the program
// number plus CC overrides.
//
// A scene is the *source of truth*: parameter settings across the whole rig,
// keyed by device. It names no transport. A captured device-memory blob (e.g. a
// Boss SL-2 slicer pattern) is not a separate section but just one parameter
// value of its device — an opaque value the engine realizes over that device's
// USB connection internally on recall. The compiled engine.FootswitchScene is a
// derived, one-way export of a scene (see engine/footswitch.go), never a source
// of truth.
package scene

import "gopkg.in/yaml.v3"

// USBPatchControl is the reserved control name under which a captured
// device-memory blob (USBPatch) is stored inside Scene.Devices. It is an
// ordinary per-device parameter value, distinguished from CC/PC controls only
// by carrying an opaque memory blob rather than a scalar; recall routes it over
// the device's USB connection (see AsUSBPatch). The scene model itself names no
// transport — the device type decides which parameter travels over which.
const USBPatchControl = "usb_patch"

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
	// that were set are present. A value is usually a scalar (CC/PC/NRPN/SysEx
	// control value); the reserved control name USBPatchControl instead carries
	// a USBPatch, an opaque device-memory blob the engine realizes over the
	// device's USB connection on recall (state the fire-and-forget control
	// surface cannot reach, e.g. a Boss SL-2 slicer pattern). The scene names no
	// transport: which path a value takes is decided from the device type, not
	// the scene. See docs/usb-tools.md.
	Devices map[string]map[string]any `yaml:"devices"`
}

// USBPatch is a captured blob of a USB device's memory, stored as one parameter
// value of its device inside Scene.Devices (under USBPatchControl). On recall
// the blob is written back over USB at the resolved address (running the
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

// AsUSBPatch interprets an opaque scene parameter value as a captured
// device-memory blob, reporting whether it is one. It is how recall and compile
// tell a memory blob (realized over USB) from an ordinary scalar control value
// without the scene having to name a transport: a USBPatch (just captured,
// in-memory) is returned directly, while a value reloaded from YAML arrives as a
// map and is recognised by its "hex" field. Anything else (a scalar) is not a
// patch.
func AsUSBPatch(v any) (USBPatch, bool) {
	switch p := v.(type) {
	case USBPatch:
		return p, true
	case *USBPatch:
		if p == nil {
			return USBPatch{}, false
		}
		return *p, true
	case map[string]any:
		if _, ok := p["hex"]; !ok {
			return USBPatch{}, false
		}
		// Re-marshal the decoded mapping through YAML so the fields land on the
		// typed struct with the right kinds (addr int64, store *int, …).
		b, err := yaml.Marshal(p)
		if err != nil {
			return USBPatch{}, false
		}
		var out USBPatch
		if err := yaml.Unmarshal(b, &out); err != nil {
			return USBPatch{}, false
		}
		return out, true
	default:
		return USBPatch{}, false
	}
}
