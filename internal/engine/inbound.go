package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
)

// InboundEvent is a decoded inbound transport event annotated with its source.
// MIDI is fire-and-forget, so "feedback" is built by capturing these inbound
// events and reverse-mapping them back to logical controls.
type InboundEvent struct {
	Transport string          `json:"transport"`
	Endpoint  string          `json:"endpoint"`
	Event     transport.Event `json:"-"`
	Time      time.Time       `json:"time"`

	// Decoded MIDI fields (best-effort; zero for non-MIDI / undecodable).
	Kind    string `json:"kind"`             // cc | program_change | note_on | note_off | ""
	Channel int    `json:"channel"`          // 0-15 for channel-voice messages
	Number  int    `json:"number,omitempty"` // cc#/note# (not meaningful for program_change)
	Value   int    `json:"value"`            // cc value / program / velocity
	HasNum  bool   `json:"has_number"`
}

// Observation is one reverse-mapped inbound event: a device's control was seen
// to take a value, decoded from an inbound message on some endpoint. Device is
// the device's name (the rig instance), matching the device-arg vocabulary the
// tools speak.
type Observation struct {
	Device  string    `json:"device"`
	Control string    `json:"control"`
	Value   any       `json:"value"`
	Wire    int       `json:"wire"`
	Source  string    `json:"source"`
	Time    time.Time `json:"time"`
}

// SetInboundHook registers a callback fired for every inbound event (after
// reverse-mapping), used by the MCP layer to emit notifications. Passing nil
// clears it.
func (e *Engine) SetInboundHook(fn func(InboundEvent, []Observation)) {
	e.inboundMu.Lock()
	e.onInbound = fn
	e.inboundMu.Unlock()
}

// StartInbound opens (if needed) and begins listening on an endpoint's inbound
// stream. It is idempotent per (transport, endpoint): a second call is a no-op
// while the first listener is alive. transportID defaults to blemidi.
func (e *Engine) StartInbound(ctx context.Context, transportID, endpoint string) error {
	if transportID == "" {
		transportID = defaultTransport
	}
	tr, ok := e.transports[transportID]
	if !ok {
		return fmt.Errorf("unknown transport %q", transportID)
	}
	key := connKey{transportID, endpoint}

	e.inboundMu.Lock()
	if _, running := e.listening[key]; running {
		e.inboundMu.Unlock()
		return nil
	}
	e.inboundMu.Unlock()

	if err := e.ensureConnected(ctx, tr, endpoint); err != nil {
		return fmt.Errorf("connect %q: %w", endpoint, err)
	}

	lctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.Listen(lctx, endpoint)
	if err != nil {
		cancel()
		return fmt.Errorf("listen %q: %w", endpoint, err)
	}

	e.inboundMu.Lock()
	// Re-check under the lock: a concurrent StartInbound may have won the race.
	if _, running := e.listening[key]; running {
		e.inboundMu.Unlock()
		cancel()
		return nil
	}
	e.listenSeq++
	gen := e.listenSeq
	e.listening[key] = &inboundListener{cancel: cancel, gen: gen}
	e.inboundMu.Unlock()

	go e.pump(lctx, transportID, endpoint, key, gen, ch)
	return nil
}

// StartInboundForDevices starts an inbound listener for every distinct
// (transport, endpoint) referenced by the current devices. Per-endpoint
// failures are collected and returned together so a single unreachable
// endpoint does not abort the rest.
func (e *Engine) StartInboundForDevices(ctx context.Context) error {
	type te struct{ transport, endpoint string }
	seen := map[te]bool{}
	for _, d := range e.Devices() {
		key := te{e.transportForDevice(d), d.ControlEndpoint()}
		if key.transport == "" || seen[key] {
			continue
		}
		seen[key] = true
	}
	var errs []string
	for key := range seen {
		if err := e.StartInbound(ctx, key.transport, key.endpoint); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("start inbound: %v", errs)
	}
	return nil
}

// StopInbound cancels the inbound listener for an endpoint, if any.
func (e *Engine) StopInbound(transportID, endpoint string) {
	if transportID == "" {
		transportID = defaultTransport
	}
	key := connKey{transportID, endpoint}
	e.inboundMu.Lock()
	l := e.listening[key]
	delete(e.listening, key)
	e.inboundMu.Unlock()
	if l != nil {
		l.cancel()
	}
}

// pump consumes one endpoint's inbound stream: decode, reverse-map into
// observed-state, append to the learn buffer, and fan out to subscribers and
// the notification hook. It exits when the channel closes (ctx done).
func (e *Engine) pump(ctx context.Context, transportID, endpoint string, key connKey, gen uint64, ch <-chan transport.Event) {
	defer func() {
		e.inboundMu.Lock()
		// Only clear if we are still the registered listener: a Stop+Start for
		// the same endpoint registers a new generation that this old pump must
		// not delete.
		if l, ok := e.listening[key]; ok && l.gen == gen {
			delete(e.listening, key)
		}
		e.inboundMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			e.handleInbound(transportID, endpoint, ev)
		}
	}
}

// handleInbound decodes one event, records observations, buffers it for learn,
// and dispatches it to subscribers and the notification hook.
func (e *Engine) handleInbound(transportID, endpoint string, ev transport.Event) {
	in := decodeInbound(transportID, endpoint, ev)
	obs := e.reverseMap(in)
	for _, o := range obs {
		e.observed.Set(o.Device, o.Control, Observed{Value: o.Value, Wire: o.Wire, Source: o.Source, Time: o.Time})
	}

	e.inboundMu.Lock()
	e.recent = append(e.recent, in)
	if len(e.recent) > recentCap {
		e.recent = e.recent[len(e.recent)-recentCap:]
	}
	// Fan out to subscribers while holding inboundMu: subscribe()'s cancel
	// closes the channel under the same lock, so sending here cannot race a
	// close (which would panic). Sends are non-blocking, so the lock is not
	// held for any meaningful time.
	for _, c := range e.subscribers {
		select {
		case c <- in:
		default: // a slow subscriber must not block the pump
		}
	}
	hook := e.onInbound
	e.inboundMu.Unlock()

	// The hook calls back into the MCP layer (notifications); keep it outside
	// the lock to avoid lock-ordering surprises.
	if hook != nil {
		hook(in, obs)
	}
}

// decodeInbound annotates a raw transport event with best-effort decoded MIDI
// fields (channel-voice messages only; SysEx/real-time/OSC are passed through
// with Kind == "").
func decodeInbound(transportID, endpoint string, ev transport.Event) InboundEvent {
	in := InboundEvent{
		Transport: transportID,
		Endpoint:  endpoint,
		Event:     ev,
		Time:      time.Now(),
		Channel:   ev.Channel,
	}
	if ev.Kind == transport.OSCEvent {
		// OSC carries no MIDI channel/number; the address + args are read
		// directly off in.Event during reverse-mapping. Kind "osc" lets the
		// reverse-map and notification paths treat it as decodable.
		if ev.OSCAddr != "" {
			in.Kind = "osc"
		}
		return in
	}
	if ev.Kind != transport.MIDIEvent || len(ev.Data) == 0 {
		return in
	}
	status := ev.Data[0]
	in.Channel = int(status & 0x0F)
	switch status & 0xF0 {
	case 0xB0:
		if len(ev.Data) >= 3 {
			in.Kind, in.Number, in.HasNum, in.Value = "cc", int(ev.Data[1]), true, int(ev.Data[2])
		}
	case 0xC0:
		if len(ev.Data) >= 2 {
			in.Kind, in.Value = "program_change", int(ev.Data[1])
		}
	case 0x90:
		if len(ev.Data) >= 3 {
			in.Kind, in.Number, in.HasNum, in.Value = "note_on", int(ev.Data[1]), true, int(ev.Data[2])
		}
	case 0x80:
		if len(ev.Data) >= 3 {
			in.Kind, in.Number, in.HasNum, in.Value = "note_off", int(ev.Data[1]), true, int(ev.Data[2])
		}
	}
	return in
}

// reverseMap turns a decoded inbound event into zero or more observations by
// matching it against every binding on its (endpoint, channel) and finding the
// definition control whose address+type the message realizes. The wire value
// is mapped back to its logical form (enum label, range int) via reverseValue.
func (e *Engine) reverseMap(in InboundEvent) []Observation {
	switch in.Kind {
	case "":
		return nil // undecodable / channel-less (SysEx, real-time) — learn only
	case "osc":
		return e.reverseMapOSC(in)
	}
	var out []Observation
	for _, d := range e.Devices() {
		if e.transportForDevice(d) != in.Transport || d.ControlEndpoint() != in.Endpoint {
			continue
		}
		def, ok := e.registry.Get(d.DeviceID)
		if !ok {
			continue
		}
		for i := range def.Controls {
			c := &def.Controls[i]
			// Program change carries the channel; note/cc match channel too.
			// The control's pinned channel (if any) overrides the binding's.
			if c.WireChannel(d.ControlChannel()) != in.Channel {
				continue
			}
			if !controlMatches(c, in) {
				continue
			}
			out = append(out, Observation{
				Device:  d.Name,
				Control: c.Name,
				Value:   reverseValue(c, in.Value),
				Wire:    in.Value,
				Source:  in.Endpoint,
				Time:    in.Time,
			})
		}
	}
	return out
}

// reverseMapOSC reconciles an inbound OSC reply (e.g. an X32 /xremote mirror or
// a direct-address echo) into observations by matching its address against the
// osc controls of every binding on the same (transport, endpoint). There is no
// channel to match — OSC is addressed by path. The first argument carries the
// value (X32 replies a single int/float/string per address).
func (e *Engine) reverseMapOSC(in InboundEvent) []Observation {
	addr := in.Event.OSCAddr
	if addr == "" {
		return nil
	}
	value, wire := oscObservedValue(in.Event.OSCArgs)
	var out []Observation
	for _, d := range e.Devices() {
		if e.transportForDevice(d) != in.Transport || d.ControlEndpoint() != in.Endpoint {
			continue
		}
		def, ok := e.registry.Get(d.DeviceID)
		if !ok {
			continue
		}
		for i := range def.Controls {
			c := &def.Controls[i]
			if c.Type != device.ControlOSC || c.Address != addr {
				continue
			}
			out = append(out, Observation{
				Device:  d.Name,
				Control: c.Name,
				Value:   value,
				Wire:    wire,
				Source:  in.Endpoint,
				Time:    in.Time,
			})
		}
	}
	return out
}

// oscObservedValue extracts the logical value and a best-effort integer "wire"
// form from an OSC reply's arguments. The logical value preserves the original
// type (int32/float32/string); wire is the rounded integer where numeric, used
// only for the coarse Observed.Wire field.
func oscObservedValue(args []any) (value any, wire int) {
	if len(args) == 0 {
		return nil, 0
	}
	switch v := args[0].(type) {
	case int32:
		return v, int(v)
	case int:
		return v, v
	case float32:
		return v, int(v)
	case float64:
		return v, int(v)
	default:
		return v, 0
	}
}

// controlMatches reports whether a decoded inbound message realizes a control.
// NRPN is intentionally not matched here (reassembling the 4-CC sequence is out
// of scope for v1; raw NRPN component CCs simply do not map to a control).
func controlMatches(c *device.Control, in InboundEvent) bool {
	switch c.Type {
	case device.ControlCC:
		if in.Kind != "cc" {
			return false
		}
		return c.Parametric || (c.CC != nil && *c.CC == in.Number)
	case device.ControlProgramChange:
		return in.Kind == "program_change"
	case device.ControlNoteOn:
		return in.Kind == "note_on" && (c.Parametric || (c.CC != nil && *c.CC == in.Number))
	case device.ControlNoteOff:
		return in.Kind == "note_off" && (c.Parametric || (c.CC != nil && *c.CC == in.Number))
	default:
		return false
	}
}

// reverseValue maps a wire value back to the most useful logical form for a
// control: an enum label when the wire value names one, otherwise the integer.
func reverseValue(c *device.Control, wire int) any {
	if c.Value.Type == device.ValueEnum {
		for label, w := range c.Value.Values {
			if w == wire {
				return label
			}
		}
	}
	return wire
}

// subscribe registers a buffered channel that receives every inbound event
// until the returned cancel func is called. Used by verify_control and
// probe_feedback to await echoes.
func (e *Engine) subscribe() (<-chan InboundEvent, func()) {
	ch := make(chan InboundEvent, 32)
	e.inboundMu.Lock()
	id := e.nextSubID
	e.nextSubID++
	e.subscribers[id] = ch
	e.inboundMu.Unlock()
	return ch, func() {
		e.inboundMu.Lock()
		if c, ok := e.subscribers[id]; ok {
			delete(e.subscribers, id)
			close(c)
		}
		e.inboundMu.Unlock()
	}
}
