package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/teemow/midi-device/device"
)

// Feedback classification used by verify_control.
const (
	StatusConfirmed  = "confirmed"   // echo arrived and matched the value sent
	StatusNoFeedback = "no_feedback" // nothing echoed before the timeout
	StatusMismatch   = "mismatch"    // echo arrived but with a different value
	StatusSkipped    = "skipped"     // control cannot be probed/verified (sysex/osc/nrpn)
)

// defaultVerifyTimeout is how long verify_control waits for an echo on top of
// the device's settle_ms.
const defaultVerifyTimeout = 800 * time.Millisecond

// VerifyResult is the outcome of verify_control: did the rig echo back the
// value we sent? MIDI is fire-and-forget, so this only works for devices whose
// CC-OUT/PC-OUT is enabled (or a second listening transport).
type VerifyResult struct {
	Device      string `json:"device"`
	Control     string `json:"control"`
	Status      string `json:"status"`
	HasExpected bool   `json:"has_expected"`
	Expected    int    `json:"expected,omitempty"`
	Observed    int    `json:"observed,omitempty"`
	Source      string `json:"source,omitempty"`

	// USBChecked is set when a same-device USB binding mapped this control's
	// parameter and a USB readback was attempted. USBValue is the decoded actual
	// value the device reported over USB, and USBSource names the USB binding
	// that supplied it. When a readback runs it is authoritative (it observes
	// the persisted device value rather than a fire-and-forget echo), so it
	// drives Status — closing the open loop that a MIDI-only verify leaves for
	// devices with no CC/PC echo (e.g. the SL-2). See docs/usb-tools.md.
	USBChecked bool   `json:"usb_checked,omitempty"`
	USBValue   any    `json:"usb_value,omitempty"`
	USBSource  string `json:"usb_source,omitempty"`
}

// VerifyControl sends a value to a control, then waits (settle_ms + timeout) for
// an inbound echo on any listening source and classifies the result. A
// non-positive timeout uses defaultVerifyTimeout.
func (e *Engine) VerifyControl(ctx context.Context, logical, control string, value any, timeout time.Duration) (VerifyResult, error) {
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	d, def, c, err := e.lookupControl(logical, control)
	if err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{Device: logical, Control: control}

	expected, hasExpected, err := expectedWire(def, c, d.ControlChannel(), value)
	if err != nil {
		return VerifyResult{}, err
	}
	res.HasExpected, res.Expected = hasExpected, expected

	// Make sure we are listening on this endpoint before we drive it, so the
	// echo cannot race ahead of the subscription.
	if err := e.StartInbound(ctx, def.Transport, d.ControlEndpoint()); err != nil {
		return VerifyResult{}, fmt.Errorf("verify: %w", err)
	}
	sub, cancel := e.subscribe()
	defer cancel()

	if err := e.SetControl(ctx, logical, control, value); err != nil {
		return VerifyResult{}, err
	}

	deadline := time.Now().Add(settleDelay(def) + timeout)
	matches := e.awaitEcho(ctx, sub, logical, control, deadline)
	if len(matches) == 0 {
		res.Status = StatusNoFeedback
	} else {
		last := matches[len(matches)-1]
		res.Observed, res.Source = last.Wire, last.Source
		switch {
		case !hasExpected:
			res.Status = StatusConfirmed // cannot compare precisely; any echo counts
		case last.Wire == expected:
			res.Status = StatusConfirmed
		default:
			res.Status = StatusMismatch
		}
	}

	// USB readback: if a USB binding shares this control's device and its profile
	// maps a param of the same name, read the actual stored value over USB. This
	// is the authoritative check (it observes real device memory, not an echo),
	// so it overrides the MIDI verdict when available. Best-effort: any failure
	// leaves the MIDI result untouched.
	e.usbReadback(ctx, d, def, control, value, &res)
	return res, nil
}

// usbReadback augments a VerifyResult with a USB ground-truth readback when a
// same-device USB binding maps the control's parameter. It is a no-op (leaving
// the MIDI verdict in place) when there is no such binding/param, no USB
// transport, or the read fails.
func (e *Engine) usbReadback(ctx context.Context, control Device, def *device.DeviceType, name string, want any, res *VerifyResult) {
	if def.USB == nil {
		return
	}
	if _, ok := def.USB.Param(name); !ok {
		return // this control has no USB counterpart — common, best-effort
	}
	ub, ok := e.usbDeviceForControl(control)
	if !ok {
		return
	}
	got, matched, err := e.USBReadbackParam(ctx, ub.Name, name, want)
	if err != nil {
		return
	}
	res.USBChecked = true
	res.USBValue = got
	res.USBSource = "usb:" + ub.Name
	if matched {
		res.Status = StatusConfirmed
	} else {
		res.Status = StatusMismatch
	}
}

// ProbeResult records which sources echoed a control during probe_feedback —
// the empirical capability matrix the agent consults to decide whether
// verify_control is worth attempting for a given (device, control).
type ProbeResult struct {
	Device  string   `json:"device"`
	Control string   `json:"control"`
	Status  string   `json:"status"`
	Sources []string `json:"sources,omitempty"`
}

// defaultProbeTimeout is the per-control echo wait used by probe_feedback.
const defaultProbeTimeout = 500 * time.Millisecond

// ProbeFeedback sweeps the controls of one logical device (or every bound
// device when logical == "") and records, per control, which transport sources
// echoed it back. It drives the rig: each probed control is (re)sent its
// current desired value, or a derived safe value when none has been set.
func (e *Engine) ProbeFeedback(ctx context.Context, logical string, timeout time.Duration) ([]ProbeResult, error) {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	devices := e.Devices()
	if logical != "" {
		devices = filterDevices(devices, logical)
		if len(devices) == 0 {
			return nil, fmt.Errorf("unknown logical device %q", logical)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })

	var out []ProbeResult
	for _, d := range devices {
		def, ok := e.registry.Get(d.DeviceID)
		if !ok {
			continue
		}
		// Listen on the endpoint up front so echoes from any control land.
		_ = e.StartInbound(ctx, def.Transport, d.ControlEndpoint())
		for i := range def.Controls {
			c := &def.Controls[i]
			pr := ProbeResult{Device: d.Name, Control: c.Name}
			value, ok := e.probeValue(d.Name, c)
			if !ok {
				pr.Status = StatusSkipped
				out = append(out, pr)
				continue
			}
			sub, cancel := e.subscribe()
			if err := e.SetControl(ctx, d.Name, c.Name, value); err != nil {
				cancel()
				pr.Status = StatusSkipped
				out = append(out, pr)
				continue
			}
			deadline := time.Now().Add(settleDelay(def) + timeout)
			matches := e.awaitEcho(ctx, sub, d.Name, c.Name, deadline)
			cancel()
			pr.Sources = distinctSources(matches)
			if len(pr.Sources) > 0 {
				pr.Status = StatusConfirmed
			} else {
				pr.Status = StatusNoFeedback
			}
			out = append(out, pr)
		}
	}
	return out, nil
}

// LearnStart begins MIDI-learn: it ensures inbound listening (on the given
// endpoint, or all bound endpoints when endpoint == "") and marks "now" as the
// capture cut-off so learn_capture ignores stale traffic. transportID defaults
// to blemidi.
func (e *Engine) LearnStart(ctx context.Context, transportID, endpoint string) error {
	if endpoint == "" {
		if err := e.StartInboundForDevices(ctx); err != nil {
			return err
		}
	} else if err := e.StartInbound(ctx, transportID, endpoint); err != nil {
		return err
	}
	e.inboundMu.Lock()
	e.learnSince = time.Now()
	e.inboundMu.Unlock()
	return nil
}

// LearnedControl is a single captured inbound message, the raw material for
// authoring a control definition (the CC/note number and channel the user
// moved on the hardware).
type LearnedControl struct {
	Transport string    `json:"transport"`
	Endpoint  string    `json:"endpoint"`
	Channel   int       `json:"channel"`
	Type      string    `json:"type"`
	Number    int       `json:"number,omitempty"`
	HasNumber bool      `json:"has_number"`
	Value     int       `json:"value"`
	Time      time.Time `json:"time"`
}

// LearnCapture returns the most recent decodable channel-voice message captured
// since the last LearnStart, or ok == false if nothing has been moved yet.
func (e *Engine) LearnCapture() (LearnedControl, bool) {
	e.inboundMu.Lock()
	defer e.inboundMu.Unlock()
	for i := len(e.recent) - 1; i >= 0; i-- {
		in := e.recent[i]
		if in.Time.Before(e.learnSince) {
			break // recent is time-ordered; older entries cannot qualify
		}
		if in.Kind == "" {
			continue
		}
		return LearnedControl{
			Transport: in.Transport,
			Endpoint:  in.Endpoint,
			Channel:   in.Channel,
			Type:      in.Kind,
			Number:    in.Number,
			HasNumber: in.HasNum,
			Value:     in.Value,
			Time:      in.Time,
		}, true
	}
	return LearnedControl{}, false
}

// awaitEcho collects observations matching (logical, control) from a subscriber
// channel until the deadline. It returns as soon as the deadline passes, or the
// channel closes; ctx cancellation also stops it.
func (e *Engine) awaitEcho(ctx context.Context, sub <-chan InboundEvent, logical, control string, deadline time.Time) []Observation {
	var matches []Observation
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return matches
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return matches
		case <-timer.C:
			return matches
		case in, ok := <-sub:
			timer.Stop()
			if !ok {
				return matches
			}
			for _, o := range e.reverseMap(in) {
				if o.Device == logical && o.Control == control {
					matches = append(matches, o)
				}
			}
		}
	}
}

// lookupControl resolves a (logical, control) pair to its binding, definition
// and control, mirroring SetControl's resolution so verify/probe report the
// same errors.
func (e *Engine) lookupControl(logical, control string) (Device, *device.DeviceType, *device.Control, error) {
	e.mu.RLock()
	d, ok := e.devices[logical]
	e.mu.RUnlock()
	if !ok {
		return Device{}, nil, nil, fmt.Errorf("unknown logical device %q", logical)
	}
	def, ok := e.registry.Get(d.DeviceID)
	if !ok {
		return Device{}, nil, nil, fmt.Errorf("logical %q: unknown device %q", logical, d.DeviceID)
	}
	c, ok := def.Control(control)
	if !ok {
		return Device{}, nil, nil, &device.ValidationError{Pointer: "/control", Msg: fmt.Sprintf("device %q has no control %q", def.ID, control)}
	}
	return d, def, c, nil
}

// probeValue picks the value to drive a control with during probe_feedback: the
// current desired value if one has been set, else a derived neutral value. It
// returns ok == false for controls that cannot be meaningfully probed.
func (e *Engine) probeValue(logical string, c *device.Control) (any, bool) {
	if v, ok := e.state.Device(logical)[c.Name]; ok {
		return v, true
	}
	switch c.Type {
	case device.ControlCC, device.ControlProgramChange, device.ControlNoteOn, device.ControlNoteOff:
		if c.Parametric {
			return nil, false // needs a caller-supplied number; nothing safe to invent
		}
		if c.Value.Type == device.ValueEnum {
			// Pick the lowest wire value so the choice is deterministic.
			best, has := 0, false
			for _, w := range c.Value.Values {
				if !has || w < best {
					best, has = w, true
				}
			}
			if has {
				return best, true
			}
			return nil, false
		}
		if c.Value.Min != nil {
			return int(*c.Value.Min), true
		}
		return 0, true
	default:
		return nil, false // sysex / osc / nrpn: no safe sweep value
	}
}

// expectedWire returns the wire value a control set should put on the bus, used
// by verify_control to compare against the echo. It reuses the render path so
// the comparison matches exactly what was sent. ok == false for controls whose
// echo cannot be compared by a single value (nrpn/sysex/osc).
func expectedWire(def *device.DeviceType, c *device.Control, channel int, value any) (int, bool, error) {
	r, err := device.Resolve(c, value)
	if err != nil {
		return 0, false, err
	}
	events, err := renderControl(def, c, channel, r)
	if err != nil {
		return 0, false, err
	}
	switch c.Type {
	case device.ControlCC, device.ControlNoteOn, device.ControlNoteOff:
		if len(events) == 1 && len(events[0].Data) >= 3 {
			return int(events[0].Data[2]), true, nil
		}
	case device.ControlProgramChange:
		if len(events) == 1 && len(events[0].Data) >= 2 {
			return int(events[0].Data[1]), true, nil
		}
	}
	return 0, false, nil
}

// settleDelay is the device's post-program-change settle window as a Duration.
func settleDelay(def *device.DeviceType) time.Duration {
	if def.SettleMS <= 0 {
		return 0
	}
	return time.Duration(def.SettleMS) * time.Millisecond
}

func filterDevices(devices []Device, logical string) []Device {
	var out []Device
	for _, d := range devices {
		if d.Name == logical {
			out = append(out, d)
		}
	}
	return out
}

func distinctSources(obs []Observation) []string {
	set := map[string]bool{}
	for _, o := range obs {
		if o.Source != "" {
			set[o.Source] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
