package mcpserver

import (
	"sort"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// This file holds the machine-readable view shapes the user-facing read tools
// emit as structuredContent, plus the helpers that build them. The surface
// speaks only the three concepts: a *device* (one instance in your rig), a
// *device type* (a kind of gear), and (elsewhere) a *scene*. There is no
// separate "definition"/"binding" vocabulary — list_devices / describe_device
// (tools.go) cover both the rig and the device-type catalog.

// connectionView is one transport address of a device: where it is reachable
// over a given transport. It is how list_devices surfaces a device's
// connection(s) — the common single-transport device has one, a multi-transport
// device (e.g. an SL-2 on BLE + USB) has several. USB reports whether this is a
// USB editor/readback connection (as opposed to a control transport).
type connectionView struct {
	Transport string `json:"transport"`
	Endpoint  string `json:"endpoint,omitempty"`
	Channel   int    `json:"channel,omitempty"`
	Writable  bool   `json:"writable,omitempty"`
	USB       bool   `json:"usb,omitempty"`
}

// deviceView is the machine-readable shape of one device in the rig for
// list_devices. Type is the device-type id it is an instance of (TypeName its
// friendly name); Transport is that type's control transport. The flat
// Endpoint/Channel/USB* fields are convenience accessors for the common
// single-transport device; Connections carries the full per-transport detail.
type deviceView struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	TypeName  string `json:"type_name,omitempty"`
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Channel   int    `json:"channel,omitempty"`
	// USB reports whether the device carries a USB editor/readback connection,
	// with USBTransport/USBEndpoint describing it and Writable its write opt-in.
	USB          bool             `json:"usb"`
	USBTransport string           `json:"usb_transport,omitempty"`
	USBEndpoint  string           `json:"usb_endpoint,omitempty"`
	Writable     bool             `json:"writable,omitempty"`
	Connections  []connectionView `json:"connections"`
}

// deviceTypeSummary is the per-type row for the list_devices catalog (the
// available device types you could add). Known reports whether a device in your
// rig already uses this type (it is "in your rig" rather than merely
// "available").
type deviceTypeSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Transport    string `json:"transport"`
	Controls     int    `json:"controls"`
	USB          bool   `json:"usb"`
	Known        bool   `json:"known"`
}

// deviceViewFor builds the structured view of one bound device, resolving its
// device type (for the friendly name + control transport) and its connection(s).
func (s *Server) deviceViewFor(d engine.Device) deviceView {
	v := deviceView{
		Name:     d.Name,
		Type:     d.DeviceID,
		Endpoint: d.ControlEndpoint(),
		Channel:  d.ControlChannel(),
		USB:      d.HasUSB(),
	}
	if usbTr, conn, ok := d.USBConnection(); ok {
		v.USBTransport = usbTr
		v.USBEndpoint = conn.Endpoint
		v.Writable = conn.Writable
	}
	controlTransport := ""
	if def, ok := s.eng.Registry().Get(d.DeviceID); ok {
		v.TypeName = def.Name
		v.Transport = def.Transport
		controlTransport = def.Transport
	}
	v.Connections = buildConnections(d, controlTransport)
	return v
}

// buildConnections renders a device's connection(s) for the structured view.
// The flat single-transport shorthand synthesizes one entry on the device
// type's control transport; an explicit Connections map is rendered key-sorted.
func buildConnections(d engine.Device, controlTransport string) []connectionView {
	if len(d.Connections) == 0 {
		if d.Endpoint == "" && d.Channel == 0 {
			return nil
		}
		return []connectionView{{
			Transport: controlTransport,
			Endpoint:  d.Endpoint,
			Channel:   d.Channel,
		}}
	}
	keys := make([]string, 0, len(d.Connections))
	for k := range d.Connections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]connectionView, 0, len(keys))
	for _, k := range keys {
		c := d.Connections[k]
		out = append(out, connectionView{
			Transport: k,
			Endpoint:  c.Endpoint,
			Channel:   c.Channel,
			Writable:  c.Writable,
			USB:       k == device.USBTransportMIDI || k == device.USBTransportHID,
		})
	}
	return out
}

// deviceTypeCatalog returns every loaded device type (the gear you could add),
// each flagged Known when a device in the rig already uses it.
func (s *Server) deviceTypeCatalog() []deviceTypeSummary {
	used := map[string]bool{}
	for _, d := range s.eng.Devices() {
		used[d.DeviceID] = true
	}
	defs := s.eng.Registry().All()
	rows := make([]deviceTypeSummary, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, deviceTypeSummary{
			ID:           d.ID,
			Name:         d.Name,
			Manufacturer: d.Manufacturer,
			Transport:    d.Transport,
			Controls:     len(d.Controls),
			USB:          d.USB != nil,
			Known:        used[d.ID],
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// valueSpecView / controlView / deviceTypeDetail are the machine-readable shape
// of a device type for describe_device. They carry json tags (device.* uses
// yaml tags), so they decode predictably for the web client / agents.
type valueSpecView struct {
	Type   string         `json:"type,omitempty"`
	Min    *float64       `json:"min,omitempty"`
	Max    *float64       `json:"max,omitempty"`
	Step   *float64       `json:"step,omitempty"`
	Unit   string         `json:"unit,omitempty"`
	Values map[string]int `json:"values,omitempty"`
}

type controlView struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Type        string        `json:"type"`
	CC          *int          `json:"cc,omitempty"`
	NRPN        *int          `json:"nrpn,omitempty"`
	Program     *int          `json:"program,omitempty"`
	SysEx       string        `json:"sysex,omitempty"`
	Address     string        `json:"address,omitempty"`
	Parametric  bool          `json:"parametric,omitempty"`
	Value       valueSpecView `json:"value"`
}

type deviceTypeDetail struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Manufacturer string        `json:"manufacturer,omitempty"`
	Description  string        `json:"description,omitempty"`
	Transport    string        `json:"transport"`
	SettleMS     int           `json:"settle_ms,omitempty"`
	USB          bool          `json:"usb"`
	Controls     []controlView `json:"controls"`
}

func newDeviceTypeDetail(d *device.DeviceType) deviceTypeDetail {
	v := deviceTypeDetail{
		ID:           d.ID,
		Name:         d.Name,
		Manufacturer: d.Manufacturer,
		Description:  d.Description,
		Transport:    d.Transport,
		SettleMS:     d.SettleMS,
		USB:          d.USB != nil,
		Controls:     make([]controlView, 0, len(d.Controls)),
	}
	for i := range d.Controls {
		c := &d.Controls[i]
		v.Controls = append(v.Controls, controlView{
			Name:        c.Name,
			Description: c.Description,
			Type:        string(c.Type),
			CC:          c.CC,
			NRPN:        c.NRPN,
			Program:     c.Program,
			SysEx:       c.SysEx,
			Address:     c.Address,
			Parametric:  c.Parametric,
			Value: valueSpecView{
				Type:   string(c.Value.Type),
				Min:    c.Value.Min,
				Max:    c.Value.Max,
				Step:   c.Value.Step,
				Unit:   c.Value.Unit,
				Values: c.Value.Values,
			},
		})
	}
	return v
}
