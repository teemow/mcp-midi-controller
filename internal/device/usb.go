package device

import (
	"fmt"

	"github.com/teemow/mcp-midi-controller/internal/usbcodec"
)

// USBProfile describes a device's USB editor/readback surface, alongside (and
// independent of) the BLE/OSC control surface in Controls. It pairs a protocol
// codec (Go, by name) with a declarative address/parameter map (this struct).
// One Definition can carry both a control surface and a USB profile, so a single
// device serves the BLE binding and the USB read/write binding (see
// docs/usb-tools.md). A nil USB means the device has no USB surface.
//
// The map is data; the codec is code. Adding a same-family device (e.g. another
// Boss compact) is mostly a new map reusing an existing Protocol codec.
type USBProfile struct {
	// Protocol selects the Go codec that frames requests/replies (one of the
	// USBProtocol* constants), e.g. roland-address-sysex.
	Protocol string `yaml:"protocol"`

	// Transport is the backend that moves frames: usbmidi (ALSA rawmidi) or
	// usbhid (hidraw). It is distinct from the Definition's control Transport.
	Transport string `yaml:"transport"`

	// Endpoint is an optional default endpoint hint (an ALSA port-name substring
	// for usbmidi, or a VID:PID / hidraw path for usbhid). The concrete endpoint
	// normally comes from the binding; this is only a convenience default.
	Endpoint string `yaml:"endpoint,omitempty"`

	// Identity is the SysEx device identity (manufacturer/model/device id). It is
	// required for the SysEx protocols (roland/morningstar) and unused for HID.
	Identity *USBIdentity `yaml:"identity,omitempty"`

	// AddrBytes / SizeBytes are the address and size field widths for
	// address-based protocols (e.g. Roland uses 4 and 4).
	AddrBytes int `yaml:"addr_bytes,omitempty"`
	SizeBytes int `yaml:"size_bytes,omitempty"`

	// Handshake names an optional pre-write handshake the codec must run (e.g.
	// editor-comm-mode for Roland). Empty means none.
	Handshake string `yaml:"handshake,omitempty"`

	// Regions are named base addresses (with optional repetition) that params
	// resolve against, e.g. system / temp / patches.
	Regions map[string]Region `yaml:"regions,omitempty"`

	// Params is the semantic parameter map: named, encoded values at an address
	// (within a region, when set).
	Params []USBParam `yaml:"params,omitempty"`
}

// USBIdentity is a SysEx device identity. Model is written as space-separated hex
// bytes (e.g. "00 00 00 00 1D"); Mfg/Device are single bytes (0x.. in YAML).
type USBIdentity struct {
	Mfg    int    `yaml:"mfg,omitempty"`
	Model  string `yaml:"model,omitempty"`
	Device int    `yaml:"device,omitempty"`
}

// Region is a named area of device memory. Base is the area's base address.
// Count/Stride describe a repeated block (e.g. the SL-2's 88 patches at
// stride 0x00100000): the i-th instance's base is Base + i*Stride. A scalar
// region leaves Count zero.
type Region struct {
	Base   int64 `yaml:"base"`
	Count  int   `yaml:"count,omitempty"`
	Stride int64 `yaml:"stride,omitempty"`
}

// USBParam is one named, encoded value in the device's USB memory map. Addr is
// the parameter's address; when Region is set it is an offset added to that
// region's base. Enc names the wire encoding (see internal/usbcodec). Min/Max
// bound an integer param; Values maps enum labels to wire values; Ofs is a
// signed offset applied around the encoding (wire = logical + Ofs).
type USBParam struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Region      string         `yaml:"region,omitempty"`
	Addr        int64          `yaml:"addr"`
	Enc         string         `yaml:"enc"`
	Min         *int           `yaml:"min,omitempty"`
	Max         *int           `yaml:"max,omitempty"`
	Ofs         int            `yaml:"ofs,omitempty"`
	Values      map[string]int `yaml:"values,omitempty"`
}

// USB protocol codec ids. SysEx protocols carry a device identity; HID protocols
// move raw vendor reports.
const (
	USBProtocolRoland      = "roland-address-sysex" // Boss/Roland RQ1/DT1
	USBProtocolMorningstar = "morningstar-sysex"    // Morningstar editor TLV
	USBProtocolNeuro       = "neuro-hid"            // Source Audio Neuro HID
	USBProtocolTorpedo     = "torpedo-hid"          // Two Notes Torpedo Remote HID
)

// USB transport ids.
const (
	USBTransportMIDI = "usbmidi" // ALSA rawmidi (SysEx)
	USBTransportHID  = "usbhid"  // hidraw (vendor reports)
)

// usbSysExProtocols are the protocols that require a SysEx Identity.
var usbSysExProtocols = map[string]bool{
	USBProtocolRoland:      true,
	USBProtocolMorningstar: true,
}

// usbKnownProtocols is the set of codec ids the engine can drive.
var usbKnownProtocols = map[string]bool{
	USBProtocolRoland:      true,
	USBProtocolMorningstar: true,
	USBProtocolNeuro:       true,
	USBProtocolTorpedo:     true,
}

// usbKnownTransports is the set of USB transports.
var usbKnownTransports = map[string]bool{
	USBTransportMIDI: true,
	USBTransportHID:  true,
}

// Param returns the named USB parameter, if present.
func (p *USBProfile) Param(name string) (*USBParam, bool) {
	for i := range p.Params {
		if p.Params[i].Name == name {
			return &p.Params[i], true
		}
	}
	return nil, false
}

// ParamNames returns the USB parameter names in declaration order.
func (p *USBProfile) ParamNames() []string {
	names := make([]string, len(p.Params))
	for i := range p.Params {
		names[i] = p.Params[i].Name
	}
	return names
}

// validate checks the USB profile for internal consistency: a known protocol and
// transport, a SysEx identity where the protocol needs one, region defs that are
// well-formed, and params with unique names, a known encoding, a region ref that
// resolves, and coherent bounds/enum specs. devID is used only in error messages.
func (p *USBProfile) validate(devID string) error {
	if !usbKnownProtocols[p.Protocol] {
		return fmt.Errorf("device %q usb: unknown protocol %q", devID, p.Protocol)
	}
	if !usbKnownTransports[p.Transport] {
		return fmt.Errorf("device %q usb: unknown transport %q", devID, p.Transport)
	}
	if usbSysExProtocols[p.Protocol] && p.Identity == nil {
		return fmt.Errorf("device %q usb: protocol %q requires an identity", devID, p.Protocol)
	}

	for name, r := range p.Regions {
		if name == "" {
			return fmt.Errorf("device %q usb: region with empty name", devID)
		}
		if r.Count < 0 {
			return fmt.Errorf("device %q usb: region %q has negative count", devID, name)
		}
		if r.Count > 0 && r.Stride <= 0 {
			return fmt.Errorf("device %q usb: region %q has count %d but no positive stride", devID, name, r.Count)
		}
	}

	seen := map[string]bool{}
	for i := range p.Params {
		par := &p.Params[i]
		if par.Name == "" {
			return fmt.Errorf("device %q usb: param with empty name", devID)
		}
		if seen[par.Name] {
			return fmt.Errorf("device %q usb: duplicate param %q", devID, par.Name)
		}
		seen[par.Name] = true
		if err := par.validate(p); err != nil {
			return fmt.Errorf("device %q usb, param %q: %w", devID, par.Name, err)
		}
	}
	return nil
}

// validate checks one USB param against its profile.
func (par *USBParam) validate(p *USBProfile) error {
	if par.Enc == "" {
		return fmt.Errorf("missing enc")
	}
	if !usbcodec.KnownEncoding(par.Enc) {
		return fmt.Errorf("unknown encoding %q", par.Enc)
	}
	if par.Region != "" {
		if _, ok := p.Regions[par.Region]; !ok {
			return fmt.Errorf("references unknown region %q", par.Region)
		}
	}
	if par.Min != nil && par.Max != nil && *par.Min > *par.Max {
		return fmt.Errorf("min %d is greater than max %d", *par.Min, *par.Max)
	}
	return nil
}
