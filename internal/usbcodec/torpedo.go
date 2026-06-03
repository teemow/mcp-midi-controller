package usbcodec

// Torpedo is a placeholder codec for the Two Notes Opus, which speaks the
// proprietary Torpedo Remote protocol over a raw 64-byte vendor HID pipe (no
// USB-MIDI, no report ids; see docs/research/opus.md). The command layout inside
// that pipe is undocumented and the editor is Mac/Win/iOS only, so the semantic
// protocol is blocked on a non-Linux capture.
//
// To stay non-destructive, this codec NEVER synthesises a request: it only
// supports monitoring (DecodeRead passes through whatever the device emits) and
// operator-supplied frame replay (handled above this layer). All build methods
// return nil.
type Torpedo struct{}

// TorpedoReportLen is the Opus's fixed in/out interrupt report length.
const TorpedoReportLen = 64

// NewTorpedo builds the Torpedo placeholder codec. Config is ignored.
func NewTorpedo(cfg Config) *Torpedo { return &Torpedo{} }

// Protocol returns the Torpedo protocol id.
func (c *Torpedo) Protocol() string { return ProtocolTorpedo }

// BuildIdentify returns nil: no identity request is known.
func (c *Torpedo) BuildIdentify() []byte { return nil }

// DecodeIdentity always reports no identity.
func (c *Torpedo) DecodeIdentity(reply []byte) (Identity, bool) { return Identity{}, false }

// BuildRead returns nil: no read request is synthesised (the command layout is
// unknown and a wrong write could change device state).
func (c *Torpedo) BuildRead(addr int64, size int) ([]byte, func([]byte) bool) { return nil, nil }

// DecodeRead passes through a monitored report as opaque data (address 0). ok is
// false for an empty report. This is the only supported read path: observe what
// the Opus emits on its own.
func (c *Torpedo) DecodeRead(reply []byte) (int64, []byte, bool) {
	if len(reply) == 0 {
		return 0, nil, false
	}
	return 0, append([]byte(nil), reply...), true
}

// BuildWrite returns nil: writes are never synthesised.
func (c *Torpedo) BuildWrite(addr int64, data []byte) []byte { return nil }

// BuildHandshake returns nil.
func (c *Torpedo) BuildHandshake() [][]byte { return nil }
