package engine

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/widi"
	"github.com/teemow/midi-transport"
)

// WIDI dongle configuration lives outside the normal control/scene surface: it
// is persistent flash config addressed to the dongle by its devID (not by MIDI
// channel), and it is request/reply rather than fire-and-forget. These methods
// drive it over the BLE-MIDI transport the engine already owns, reusing the
// single inbound listener + subscriber fan-out (StartInbound/subscribe) so they
// never open a second Listen on an endpoint — which the ALSA data plane cannot
// support — and so they coexist with a bound dongle's normal traffic.

// defaultWIDITimeout bounds a single register read/write round-trip.
const defaultWIDITimeout = 1500 * time.Millisecond

// WIDISetting is one register's value in a config snapshot.
type WIDISetting struct {
	Register  string `json:"register"`
	RegID     int    `json:"reg_id"`
	Supported bool   `json:"supported"`
	Value     any    `json:"value,omitempty"`
	Raw       string `json:"raw,omitempty"`   // hex of the decoded logical bytes
	Error     string `json:"error,omitempty"` // device error name when unsupported
}

// WIDIConfig is a decoded snapshot of a dongle's settings.
type WIDIConfig struct {
	Endpoint string        `json:"endpoint"`
	Product  string        `json:"product"`
	DevID    int           `json:"dev_id"`
	Settings []WIDISetting `json:"settings"`
}

// WIDIWriteResult reports a single-byte register write and its read-back.
type WIDIWriteResult struct {
	Endpoint string `json:"endpoint"`
	Setting  string `json:"setting"`
	Register string `json:"register"`
	Wrote    any    `json:"wrote"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
	Verified bool   `json:"verified"`
}

// ReadWIDIConfig sweeps the configuration registers of the WIDI dongle at
// endpoint (addressed by devID) and returns a decoded snapshot. Unsupported
// registers are reported with their device error rather than failing the sweep.
func (e *Engine) ReadWIDIConfig(ctx context.Context, endpoint string, devID byte, timeout time.Duration) (WIDIConfig, error) {
	cfg := WIDIConfig{Endpoint: endpoint, DevID: int(devID)}
	if p, ok := widi.ProductByDevID(devID); ok {
		cfg.Product = p.Name
	}
	for _, reg := range widi.ConfigRegisters {
		rep, err := e.widiRequest(ctx, endpoint, widi.BuildReadSetting(devID, reg), devID, widi.CmdReadSettings, timeout)
		setting := WIDISetting{Register: reg.Name(), RegID: int(reg)}
		switch {
		case err != nil:
			return cfg, fmt.Errorf("read %s: %w", reg.Name(), err)
		case rep.Kind == widi.ReplyError:
			setting.Supported = false
			setting.Error = rep.ErrName
		default:
			setting.Supported = true
			setting.Value = widi.Describe(reg, rep.Bytes)
			setting.Raw = fmt.Sprintf("% X", rep.Bytes)
		}
		cfg.Settings = append(cfg.Settings, setting)
	}
	return cfg, nil
}

// WriteWIDISetting writes a single-byte register and reads it back to confirm.
// The setting key resolves the register and the value vocabulary via the widi
// library (e.g. ble_role=peripheral).
func (e *Engine) WriteWIDISetting(ctx context.Context, endpoint string, devID byte, settingKey string, value any, timeout time.Duration) (WIDIWriteResult, error) {
	s, ok := widi.SettingByKey(settingKey)
	if !ok {
		return WIDIWriteResult{}, fmt.Errorf("unknown setting %q (known: %v)", settingKey, widi.SettingKeys())
	}
	wire, err := s.Encode(value)
	if err != nil {
		return WIDIWriteResult{}, err
	}
	res := WIDIWriteResult{
		Endpoint: endpoint,
		Setting:  s.Key,
		Register: s.Register.Name(),
		Wrote:    widi.Describe(s.Register, []byte{wire}),
	}

	// Read the current value first (best-effort context for the caller).
	if before, err := e.widiRequest(ctx, endpoint, widi.BuildReadSetting(devID, s.Register), devID, widi.CmdReadSettings, timeout); err == nil && before.Kind == widi.ReplySettings {
		res.Before = widi.Describe(s.Register, before.Bytes)
	}

	if _, err := e.widiRequest(ctx, endpoint, widi.BuildWriteByte(devID, s.Register, wire), devID, widi.CmdWriteSettings, timeout); err != nil {
		return res, fmt.Errorf("write %s: %w", s.Register.Name(), err)
	}

	after, err := e.widiRequest(ctx, endpoint, widi.BuildReadSetting(devID, s.Register), devID, widi.CmdReadSettings, timeout)
	if err != nil {
		return res, fmt.Errorf("read-back %s: %w", s.Register.Name(), err)
	}
	if after.Kind == widi.ReplySettings {
		res.After = widi.Describe(s.Register, after.Bytes)
		if b, ok := after.Byte(); ok {
			res.Verified = b == wire
		}
	}
	return res, nil
}

// SetWIDIGroup writes a wireless-MIDI group: up to four peer BLE MACs into the
// CONNECT_ADDRESS registers (unused slots cleared to FF×6). It can also force
// the BLE role and latency/jitter preference in the same operation. This is a
// pairing-changing, multi-register write kept off the plain control surface.
func (e *Engine) SetWIDIGroup(ctx context.Context, endpoint string, devID byte, peers []string, role, prefer string, timeout time.Duration) (WIDIConfig, error) {
	if len(peers) > len(widi.ConnectAddressRegisters) {
		return WIDIConfig{}, fmt.Errorf("at most %d group peers", len(widi.ConnectAddressRegisters))
	}
	for i, reg := range widi.ConnectAddressRegisters {
		var req []byte
		if i < len(peers) {
			mac, err := net.ParseMAC(peers[i])
			if err != nil {
				return WIDIConfig{}, fmt.Errorf("peer %d %q: %w", i+1, peers[i], err)
			}
			req, err = widi.BuildWriteAddress(devID, reg, mac)
			if err != nil {
				return WIDIConfig{}, err
			}
		} else {
			req = widi.BuildClearAddress(devID, reg)
		}
		if _, err := e.widiRequest(ctx, endpoint, req, devID, widi.CmdWriteSettings, timeout); err != nil {
			return WIDIConfig{}, fmt.Errorf("write %s: %w", reg.Name(), err)
		}
	}
	if err := e.applyGroupOptions(ctx, endpoint, devID, role, prefer, timeout); err != nil {
		return WIDIConfig{}, err
	}
	return e.ReadWIDIConfig(ctx, endpoint, devID, timeout)
}

// ClearWIDIGroup dissolves a wireless-MIDI group by writing FF×6 to all four
// CONNECT_ADDRESS slots.
func (e *Engine) ClearWIDIGroup(ctx context.Context, endpoint string, devID byte, timeout time.Duration) (WIDIConfig, error) {
	for _, reg := range widi.ConnectAddressRegisters {
		if _, err := e.widiRequest(ctx, endpoint, widi.BuildClearAddress(devID, reg), devID, widi.CmdWriteSettings, timeout); err != nil {
			return WIDIConfig{}, fmt.Errorf("clear %s: %w", reg.Name(), err)
		}
	}
	return e.ReadWIDIConfig(ctx, endpoint, devID, timeout)
}

// applyGroupOptions optionally sets role and latency/jitter preference.
func (e *Engine) applyGroupOptions(ctx context.Context, endpoint string, devID byte, role, prefer string, timeout time.Duration) error {
	if role != "" {
		if _, err := e.WriteWIDISetting(ctx, endpoint, devID, "ble_role", role, timeout); err != nil {
			return err
		}
	}
	if prefer != "" {
		if _, err := e.WriteWIDISetting(ctx, endpoint, devID, "prefer", prefer, timeout); err != nil {
			return err
		}
	}
	return nil
}

// widiRequest sends a WIDI SysEx request to endpoint and returns the matching
// reply. It ensures the inbound listener is up before sending (so the reply
// cannot race ahead of the subscription), then matches the first reply with the
// same devID and command (reply bit set). A device error reply is returned as a
// Reply (Kind == ReplyError), not an error; only timeouts and transport
// failures are errors.
func (e *Engine) widiRequest(ctx context.Context, endpoint string, req []byte, devID, cmd byte, timeout time.Duration) (widi.Reply, error) {
	if timeout <= 0 {
		timeout = defaultWIDITimeout
	}
	if err := e.StartInbound(ctx, defaultTransport, endpoint); err != nil {
		return widi.Reply{}, fmt.Errorf("listen %q: %w", endpoint, err)
	}
	sub, cancel := e.subscribe()
	defer cancel()

	if err := e.SendRaw(ctx, defaultTransport, endpoint, transport.Event{Kind: transport.MIDIEvent, Data: req}); err != nil {
		return widi.Reply{}, err
	}

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return widi.Reply{}, fmt.Errorf("timeout waiting for reply (cmd 0x%02X, dev 0x%02X)", cmd, devID)
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return widi.Reply{}, ctx.Err()
		case <-timer.C:
			return widi.Reply{}, fmt.Errorf("timeout waiting for reply (cmd 0x%02X, dev 0x%02X)", cmd, devID)
		case in, ok := <-sub:
			timer.Stop()
			if !ok {
				return widi.Reply{}, fmt.Errorf("inbound stream closed")
			}
			// The subscriber fan-out carries inbound from every endpoint. Match
			// only replies that arrived on the endpoint we queried (and over the
			// BLE-MIDI transport we sent on) so a second dongle sharing a devID
			// cannot answer for this one.
			if in.Transport != defaultTransport || in.Endpoint != endpoint {
				continue
			}
			data := in.Event.Data
			if !widi.IsReply(data) {
				continue
			}
			rep, err := widi.Decode(data)
			if err != nil || rep.DevID != devID || rep.Cmd&0x0F != cmd&0x0F {
				continue
			}
			return rep, nil
		}
	}
}
