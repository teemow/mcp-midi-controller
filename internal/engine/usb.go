package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
	"github.com/teemow/mcp-midi-controller/internal/usbcodec"
)

// This file is the engine's USB editor/readback surface: a request/reply
// session (generalising the read-only cmd/usb-probe spike) plus the USB API the
// MCP tools call. It layers on the existing transports (usbmidi over ALSA
// rawmidi, usbhid over hidraw) and the per-protocol codecs in package usbcodec,
// driven by a device's USB profile (device.USBProfile). See docs/usb-tools.md.
//
// A device can be bound twice — once for its BLE/OSC control surface and once
// for its USB surface — so USB sends update the SAME desired-state map, letting
// BLE and USB edits to the same logical name reconcile.

// usbReplyWindow is how long a request waits for a matching reply before giving
// up. It matches the read-only spike's default collection window; the USB
// editor protocols answer well within it.
const usbReplyWindow = 1500 * time.Millisecond

// usbDumpChunk is the default per-read size for a multi-read dump. 32 is the
// fixed Neuro HID dump width and a safe granularity for the addressed SysEx
// protocols too (each chunked read is matched to its own reply).
const usbDumpChunk = 32

// errUSBNoReply is returned when no matching reply arrived within the window.
var errUSBNoReply = errors.New("no reply within window")

// USBIdentityResult is a decoded device identity returned by USBIdentify.
type USBIdentityResult struct {
	Manufacturer byte   `json:"manufacturer"`
	DeviceID     byte   `json:"device_id"`
	Family       []byte `json:"family,omitempty"`
	Member       []byte `json:"member,omitempty"`
	Revision     []byte `json:"revision,omitempty"`
	Raw          []byte `json:"raw,omitempty"`
}

// USBBlock is one instance of a repeated region (e.g. an SL-2 pattern slot or
// an EQ2 preset slot): its index and resolved base address.
type USBBlock struct {
	Index int   `json:"index"`
	Addr  int64 `json:"addr"`
}

// USBProbeResult is what usb_probe learns about an unbound endpoint: the
// decoded identity (when the protocol has one) and any frames seen.
type USBProbeResult struct {
	Transport string             `json:"transport"`
	Endpoint  string             `json:"endpoint"`
	Protocol  string             `json:"protocol"`
	Identity  *USBIdentityResult `json:"identity,omitempty"`
	Frames    [][]byte           `json:"frames,omitempty"`
}

// usbContext bundles a resolved USB binding: the binding, its definition + USB
// profile, the protocol codec, and the transport/endpoint to drive.
type usbContext struct {
	binding   Binding
	def       *device.Definition
	profile   *device.USBProfile
	codec     usbcodec.Codec
	transport string
	endpoint  string
}

// usbContext resolves a logical name to its USB binding context, erroring if it
// is not a USB binding or the profile/codec is invalid.
func (e *Engine) usbContextFor(logical string) (*usbContext, error) {
	b, ok := e.binding(logical)
	if !ok {
		return nil, fmt.Errorf("unknown logical device %q", logical)
	}
	def, ok := e.registry.Get(b.DeviceID)
	if !ok {
		return nil, fmt.Errorf("logical %q: unknown device %q", logical, b.DeviceID)
	}
	if def.USB == nil {
		return nil, fmt.Errorf("device %q has no usb profile", def.ID)
	}
	if !isUSBTransport(b.Transport) {
		return nil, fmt.Errorf("%q is not a usb binding (transport %q)", logical, b.Transport)
	}
	codec, err := usbCodecFor(def.USB)
	if err != nil {
		return nil, fmt.Errorf("device %q: %w", def.ID, err)
	}
	endpoint := b.Endpoint
	if endpoint == "" {
		endpoint = def.USB.Endpoint
	}
	return &usbContext{
		binding:   b,
		def:       def,
		profile:   def.USB,
		codec:     codec,
		transport: b.Transport,
		endpoint:  endpoint,
	}, nil
}

// usbCodecFor builds the protocol codec for a USB profile, filling the codec
// Config from the profile's identity and address geometry. A zero manufacturer
// is left unset so the codec applies its per-protocol default (e.g. the
// Morningstar 00 21 24 manufacturer).
func usbCodecFor(p *device.USBProfile) (usbcodec.Codec, error) {
	cfg := usbcodec.Config{AddrBytes: p.AddrBytes, SizeBytes: p.SizeBytes}
	if p.Identity != nil {
		if p.Identity.Mfg != 0 {
			cfg.Mfg = []byte{byte(p.Identity.Mfg)}
		}
		if p.Identity.Model != "" {
			model, err := parseHexBytes(p.Identity.Model)
			if err != nil {
				return nil, fmt.Errorf("usb identity model %q: %w", p.Identity.Model, err)
			}
			cfg.Model = model
		}
		cfg.DeviceID = byte(p.Identity.Device)
	}
	return usbcodec.New(p.Protocol, cfg)
}

// parseHexBytes parses a space-separated hex-byte string ("00 00 00 00 1D")
// into its bytes. An empty string yields no bytes.
func parseHexBytes(s string) ([]byte, error) {
	fields := strings.Fields(s)
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseUint(strings.TrimPrefix(f, "0x"), 16, 8)
		if err != nil {
			return nil, fmt.Errorf("bad hex byte %q", f)
		}
		out = append(out, byte(v))
	}
	return out, nil
}

// usbSession drives request/reply exchanges over one transport+endpoint. It
// reuses the engine's inbound fan-out (a single pump per endpoint) rather than
// opening a second listener, so it composes with observed-state/monitoring.
type usbSession struct {
	e         *Engine
	transport string
	endpoint  string
	window    time.Duration
}

// newUSBSession ensures the endpoint is connected and has an inbound pump
// running, then returns a session over it.
func (e *Engine) newUSBSession(ctx context.Context, transportID, endpoint string, window time.Duration) (*usbSession, error) {
	tr, ok := e.transports[transportID]
	if !ok {
		return nil, fmt.Errorf("unknown transport %q", transportID)
	}
	if endpoint == "" {
		return nil, fmt.Errorf("usb: empty endpoint for transport %q", transportID)
	}
	if err := e.ensureConnected(ctx, tr, endpoint); err != nil {
		return nil, fmt.Errorf("connect %q: %w", endpoint, err)
	}
	if err := e.StartInbound(ctx, transportID, endpoint); err != nil {
		return nil, fmt.Errorf("listen %q: %w", endpoint, err)
	}
	if window <= 0 {
		window = usbReplyWindow
	}
	return &usbSession{e: e, transport: transportID, endpoint: endpoint, window: window}, nil
}

// usbEvent wraps request bytes in the transport event kind for this session's
// transport: a raw HID report for usbhid, otherwise raw MIDI/SysEx bytes.
func (s *usbSession) usbEvent(b []byte) transport.Event {
	if s.transport == device.USBTransportHID {
		return transport.Event{Kind: transport.RawEvent, Data: b}
	}
	return transport.Event{Kind: transport.MIDIEvent, Data: b}
}

// request sends req then returns the first inbound frame on this endpoint that
// satisfies match (a nil match accepts the first frame — for HID, which cannot
// correlate by content). It subscribes before sending so a fast reply is not
// missed.
func (s *usbSession) request(ctx context.Context, req []byte, match func([]byte) bool) ([]byte, error) {
	sub, cancel := s.e.subscribe()
	defer cancel()

	tr := s.e.transports[s.transport]
	if err := tr.Send(ctx, s.endpoint, s.usbEvent(req)); err != nil {
		return nil, fmt.Errorf("send to %s: %w", s.endpoint, err)
	}

	deadline := time.NewTimer(s.window)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errUSBNoReply
		case in, ok := <-sub:
			if !ok {
				return nil, errUSBNoReply
			}
			if in.Transport != s.transport || in.Endpoint != s.endpoint {
				continue
			}
			raw := in.Event.Data
			if len(raw) == 0 {
				continue
			}
			if match == nil || match(raw) {
				return append([]byte(nil), raw...), nil
			}
		}
	}
}

// USBIdentify sends the device's identity request and decodes the reply.
func (e *Engine) USBIdentify(ctx context.Context, logical string) (*USBIdentityResult, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	req := c.codec.BuildIdentify()
	if req == nil {
		return nil, fmt.Errorf("protocol %q has no identity request", c.profile.Protocol)
	}
	sess, err := e.newUSBSession(ctx, c.transport, c.endpoint, usbReplyWindow)
	if err != nil {
		return nil, err
	}
	raw, err := sess.request(ctx, req, func(f []byte) bool {
		_, ok := c.codec.DecodeIdentity(f)
		return ok
	})
	if err != nil {
		return nil, err
	}
	id, ok := c.codec.DecodeIdentity(raw)
	if !ok {
		return nil, fmt.Errorf("undecodable identity reply")
	}
	return identityResult(id), nil
}

// USBRead reads size bytes at an address. When region is set, addr is an offset
// added to that region's base (index selects the instance of a repeated
// region). It returns the (echoed) address and the data bytes.
func (e *Engine) USBRead(ctx context.Context, logical, region string, index int, addr int64, size int) (int64, []byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return 0, nil, err
	}
	abs, err := c.resolveAddr(region, index, addr)
	if err != nil {
		return 0, nil, err
	}
	sess, err := e.newUSBSession(ctx, c.transport, c.endpoint, usbReplyWindow)
	if err != nil {
		return 0, nil, err
	}
	return c.read(ctx, sess, abs, size)
}

// USBDump reads size bytes starting at an address by issuing one read per chunk
// (defaulting to usbDumpChunk) and concatenating the data, so blocks larger
// than a single protocol read are returned whole.
func (e *Engine) USBDump(ctx context.Context, logical, region string, index int, addr int64, size, chunk int) ([]byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	base, err := c.resolveAddr(region, index, addr)
	if err != nil {
		return nil, err
	}
	if chunk <= 0 {
		chunk = usbDumpChunk
	}
	if size <= 0 {
		return nil, fmt.Errorf("usb dump: size must be positive")
	}
	sess, err := e.newUSBSession(ctx, c.transport, c.endpoint, usbReplyWindow)
	if err != nil {
		return nil, err
	}
	var out []byte
	for off := 0; off < size; off += chunk {
		n := chunk
		if off+n > size {
			n = size - off
		}
		_, data, err := c.read(ctx, sess, base+int64(off), n)
		if err != nil {
			return out, fmt.Errorf("dump at 0x%X: %w", base+int64(off), err)
		}
		out = append(out, data...)
	}
	return out, nil
}

// USBWrite writes data at an absolute address (running the protocol's pre-write
// handshake first, e.g. Roland editor-comm mode). With dryRun it builds and
// returns the exact frame(s) without sending. It returns the write frame bytes.
func (e *Engine) USBWrite(ctx context.Context, logical string, addr int64, data []byte, dryRun bool) ([]byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	frame := c.codec.BuildWrite(addr, data)
	if frame == nil {
		return nil, fmt.Errorf("protocol %q does not support writes", c.profile.Protocol)
	}
	if dryRun {
		return frame, nil
	}
	if err := c.send(ctx, e, append(c.codec.BuildHandshake(), frame)...); err != nil {
		return nil, err
	}
	return frame, nil
}

// USBGetParam reads a named parameter and decodes it to its logical value
// (enum label, int, string or raw bytes per the param's encoding).
func (e *Engine) USBGetParam(ctx context.Context, logical, name string) (any, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	par, ok := c.profile.Param(name)
	if !ok {
		return nil, fmt.Errorf("device %q has no usb param %q", c.def.ID, name)
	}
	width, err := usbcodec.Width(par.Enc)
	if err != nil {
		return nil, err
	}
	abs, err := c.resolveAddr(par.Region, 0, par.Addr)
	if err != nil {
		return nil, err
	}
	sess, err := e.newUSBSession(ctx, c.transport, c.endpoint, usbReplyWindow)
	if err != nil {
		return nil, err
	}
	_, data, err := c.read(ctx, sess, abs, width)
	if err != nil {
		return nil, err
	}
	return decodeParam(par, data)
}

// USBSetParam encodes value per the named param's encoding and writes it,
// recording the logical value in desired-state (so BLE and USB edits to the
// same logical reconcile). With dryRun it returns the frame without sending.
func (e *Engine) USBSetParam(ctx context.Context, logical, name string, value any, dryRun bool) ([]byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	par, ok := c.profile.Param(name)
	if !ok {
		return nil, fmt.Errorf("device %q has no usb param %q", c.def.ID, name)
	}
	wire, err := encodeParam(par, value)
	if err != nil {
		return nil, err
	}
	abs, err := c.resolveAddr(par.Region, 0, par.Addr)
	if err != nil {
		return nil, err
	}
	frame, err := e.USBWrite(ctx, logical, abs, wire, dryRun)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		e.state.Set(logical, name, value)
		e.persistState()
	}
	return frame, nil
}

// USBReadbackParam reads a named param over USB and reports whether it matches
// want, returning the decoded actual value too. It is the ground-truth readback
// verify_control uses to close the BLE open loop: want is encoded to its wire
// bytes the same way USBSetParam would send it, then compared byte-for-byte to
// what the device actually holds. A want the param cannot encode (wrong
// label/out of range) yields matched=false with a nil error so the caller can
// still report the observed value.
func (e *Engine) USBReadbackParam(ctx context.Context, logical, name string, want any) (got any, matched bool, err error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, false, err
	}
	par, ok := c.profile.Param(name)
	if !ok {
		return nil, false, fmt.Errorf("device %q has no usb param %q", c.def.ID, name)
	}
	width, err := usbcodec.Width(par.Enc)
	if err != nil {
		return nil, false, err
	}
	abs, err := c.resolveAddr(par.Region, 0, par.Addr)
	if err != nil {
		return nil, false, err
	}
	sess, err := e.newUSBSession(ctx, c.transport, c.endpoint, usbReplyWindow)
	if err != nil {
		return nil, false, err
	}
	_, data, err := c.read(ctx, sess, abs, width)
	if err != nil {
		return nil, false, err
	}
	got, err = decodeParam(par, data)
	if err != nil {
		return nil, false, err
	}
	if wire, encErr := encodeParam(par, want); encErr == nil {
		matched = bytes.Equal(data, wire)
	}
	return got, matched, nil
}

// USBListBlocks returns the instances of a repeated region (index + resolved
// base address). It errors if the region is unknown or not repeated.
func (e *Engine) USBListBlocks(_ context.Context, logical, region string) ([]USBBlock, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	r, ok := c.profile.Regions[region]
	if !ok {
		return nil, fmt.Errorf("device %q has no usb region %q", c.def.ID, region)
	}
	if r.Count <= 0 {
		return nil, fmt.Errorf("usb region %q is not a repeated block (count 0)", region)
	}
	out := make([]USBBlock, r.Count)
	for i := 0; i < r.Count; i++ {
		out[i] = USBBlock{Index: i, Addr: r.Base + int64(i)*r.Stride}
	}
	return out, nil
}

// USBProbe identifies (and, for protocols without an identity request, briefly
// monitors) an UNBOUND endpoint so a new device can be authored. It does not
// require a binding: the protocol is supplied by the caller.
func (e *Engine) USBProbe(ctx context.Context, transportID, endpoint, protocol string) (*USBProbeResult, error) {
	codec, err := usbcodec.New(protocol, usbcodec.Config{})
	if err != nil {
		return nil, err
	}
	sess, err := e.newUSBSession(ctx, transportID, endpoint, usbReplyWindow)
	if err != nil {
		return nil, err
	}
	res := &USBProbeResult{Transport: transportID, Endpoint: endpoint, Protocol: protocol}
	if req := codec.BuildIdentify(); req != nil {
		raw, err := sess.request(ctx, req, func(f []byte) bool {
			_, ok := codec.DecodeIdentity(f)
			return ok
		})
		if err == nil {
			if id, ok := codec.DecodeIdentity(raw); ok {
				res.Identity = identityResult(id)
				res.Frames = append(res.Frames, raw)
			}
		}
		return res, nil
	}
	// No identity request (HID): listen briefly for any unsolicited frame.
	frames, _ := e.USBMonitor(ctx, transportID, endpoint, usbReplyWindow)
	res.Frames = frames
	return res, nil
}

// USBMonitor drains every inbound frame seen on a transport+endpoint within the
// window (unsolicited HID reports, hand-tweak SysEx, etc.) and returns them.
func (e *Engine) USBMonitor(ctx context.Context, transportID, endpoint string, window time.Duration) ([][]byte, error) {
	if window <= 0 {
		window = usbReplyWindow
	}
	sub, cancel := e.subscribe()
	defer cancel()
	if _, err := e.newUSBSession(ctx, transportID, endpoint, window); err != nil {
		return nil, err
	}
	var out [][]byte
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return out, nil
		case <-deadline.C:
			return out, nil
		case in, ok := <-sub:
			if !ok {
				return out, nil
			}
			if in.Transport == transportID && in.Endpoint == endpoint && len(in.Event.Data) > 0 {
				out = append(out, append([]byte(nil), in.Event.Data...))
			}
		}
	}
}

// USBMonitorLogical is USBMonitor addressed by a bound USB logical name.
func (e *Engine) USBMonitorLogical(ctx context.Context, logical string, window time.Duration) ([][]byte, error) {
	c, err := e.usbContextFor(logical)
	if err != nil {
		return nil, err
	}
	return e.USBMonitor(ctx, c.transport, c.endpoint, window)
}

// read issues one codec read of size bytes at abs and decodes the reply.
func (c *usbContext) read(ctx context.Context, sess *usbSession, abs int64, size int) (int64, []byte, error) {
	req, match := c.codec.BuildRead(abs, size)
	if req == nil {
		return 0, nil, fmt.Errorf("protocol %q does not support addressed reads", c.profile.Protocol)
	}
	raw, err := sess.request(ctx, req, match)
	if err != nil {
		return 0, nil, err
	}
	ra, data, ok := c.codec.DecodeRead(raw)
	if !ok {
		return 0, nil, fmt.Errorf("undecodable read reply")
	}
	return ra, data, nil
}

// send transmits one or more frames in order (handshake then write), connecting
// the endpoint on demand. Used for writes, which expect no reply.
func (c *usbContext) send(ctx context.Context, e *Engine, frames ...[]byte) error {
	tr, ok := e.transports[c.transport]
	if !ok {
		return fmt.Errorf("unknown transport %q", c.transport)
	}
	if err := e.ensureConnected(ctx, tr, c.endpoint); err != nil {
		return fmt.Errorf("connect %q: %w", c.endpoint, err)
	}
	kind := transport.MIDIEvent
	if c.transport == device.USBTransportHID {
		kind = transport.RawEvent
	}
	for _, f := range frames {
		if f == nil {
			continue
		}
		if err := tr.Send(ctx, c.endpoint, transport.Event{Kind: kind, Data: f}); err != nil {
			return fmt.Errorf("send to %s: %w", c.endpoint, err)
		}
	}
	return nil
}

// resolveAddr turns a (region, index, addr) triple into an absolute address.
// With no region, addr is absolute. With a region, addr is an offset added to
// the region's base; for a repeated region (count > 0) index selects the
// instance (base + index*stride).
func (c *usbContext) resolveAddr(region string, index int, addr int64) (int64, error) {
	if region == "" {
		return addr, nil
	}
	r, ok := c.profile.Regions[region]
	if !ok {
		return 0, fmt.Errorf("unknown region %q", region)
	}
	base := r.Base
	if r.Count > 0 {
		if index < 0 || index >= r.Count {
			return 0, fmt.Errorf("region %q index %d out of range [0,%d)", region, index, r.Count)
		}
		base += int64(index) * r.Stride
	}
	return base + addr, nil
}

// identityResult converts a codec Identity into the engine's result type.
func identityResult(id usbcodec.Identity) *USBIdentityResult {
	return &USBIdentityResult{
		Manufacturer: id.Manufacturer,
		DeviceID:     id.DeviceID,
		Family:       id.Family,
		Member:       id.Member,
		Revision:     id.Revision,
		Raw:          id.Raw,
	}
}

// encodeParam renders a param's logical value to its wire bytes, resolving enum
// labels (via the param's Values map), applying the signed offset for numeric
// encodings, and enforcing Min/Max for numeric params.
func encodeParam(par *device.USBParam, value any) ([]byte, error) {
	if usbcodec.Numeric(par.Enc) {
		n, err := paramNumeric(par, value)
		if err != nil {
			return nil, err
		}
		if par.Min != nil && n < *par.Min {
			return nil, fmt.Errorf("value %d below min %d", n, *par.Min)
		}
		if par.Max != nil && n > *par.Max {
			return nil, fmt.Errorf("value %d above max %d", n, *par.Max)
		}
		return usbcodec.EncodeInt(par.Enc, n, par.Ofs)
	}
	return usbcodec.Encode(par.Enc, value)
}

// paramNumeric resolves a numeric param's logical integer value from an input,
// accepting an enum label (mapped via Values) or any JSON/YAML numeric kind.
func paramNumeric(par *device.USBParam, value any) (int, error) {
	if s, ok := value.(string); ok {
		if w, ok := par.Values[s]; ok {
			return w, nil
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n, nil
		}
		return 0, fmt.Errorf("unknown value %q for param %q", s, par.Name)
	}
	n, ok := asNumeric(value)
	if !ok {
		return 0, fmt.Errorf("param %q needs a number or label, got %T", par.Name, value)
	}
	return n, nil
}

// decodeParam decodes a param's wire bytes to its logical value: an enum label
// when the value names one, an int for numeric encodings (offset removed), a
// string for ascii, or raw bytes otherwise.
func decodeParam(par *device.USBParam, data []byte) (any, error) {
	if usbcodec.Numeric(par.Enc) {
		n, err := usbcodec.DecodeInt(par.Enc, data, par.Ofs)
		if err != nil {
			return nil, err
		}
		for label, w := range par.Values {
			if w == n {
				return label, nil
			}
		}
		return n, nil
	}
	return usbcodec.Decode(par.Enc, data)
}

// asNumeric coerces a JSON/YAML-decoded numeric value to int (requiring whole
// numbers for floats).
func asNumeric(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	case float32:
		return int(n), float64(n) == float64(int(n))
	default:
		return 0, false
	}
}
