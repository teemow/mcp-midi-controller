// Package blemidi implements the BLE-MIDI transport on Linux via BlueZ over
// D-Bus. It owns discovery, pairing (Agent1) and connection, and reads/writes/
// notifies the BLE-MIDI GATT characteristic directly.
//
// BLE-MIDI GATT:
//   - Service UUID:        03B80E5A-EDE8-4B33-A751-6CE34EC4C700
//   - MIDI I/O char UUID:  7772E5DB-3868-4112-A1A9-F2669D106BF3
//     (write-without-response + notify; encryption required → must be paired)
//
// The characteristic payload uses BLE-MIDI's 13-bit millisecond timestamp
// framing (a header byte with the high 6 bits, then a timestamp byte with the
// low 7 bits, then the MIDI message). See the BLE-MIDI spec.
//
// Linux-only by design (Linux-first). A CoreBluetooth/WinRT backend can be
// added later behind transport.Transport.
package blemidi

import (
	"context"
	"errors"

	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// Service / characteristic UUIDs from the BLE-MIDI specification.
const (
	ServiceUUID = "03b80e5a-ede8-4b33-a751-6ce34ec4c700"
	IOCharUUID  = "7772e5db-3868-4112-a1a9-f2669d106bf3"
)

// Transport is the BLE-MIDI backend.
//
// TODO: hold a godbus connection to the system bus, the BlueZ Adapter1 object,
// and per-endpoint GATT characteristic handles + notify subscriptions.
type Transport struct {
	// TODO: dbusConn, adapter, devices map[string]*device, agent, etc.
}

// New returns a BLE-MIDI transport bound to the default BlueZ adapter.
//
// TODO: connect to the system D-Bus, locate the adapter (org.bluez), register
// an Agent1 for PIN handling.
func New() (*Transport, error) {
	return &Transport{}, nil
}

func (t *Transport) ID() string { return "blemidi" }

// Discover scans for BLE peripherals advertising the BLE-MIDI service.
//
// TODO: Adapter1.SetDiscoveryFilter(UUIDs=[ServiceUUID]) +
// Adapter1.StartDiscovery; collect Device1 objects from the object manager.
func (t *Transport) Discover(ctx context.Context) ([]transport.Endpoint, error) {
	return nil, errors.New("blemidi: Discover not implemented")
}

// Pair bonds with the peripheral (required: the MIDI char is encrypted).
//
// TODO: Device1.Pair via the registered Agent1; persist nothing (BlueZ stores
// the bond).
func (t *Transport) Pair(ctx context.Context, endpointID string) error {
	return errors.New("blemidi: Pair not implemented")
}

// Connect opens the GATT connection and resolves the MIDI I/O characteristic.
//
// TODO: Device1.Connect, wait for ServicesResolved, locate the IOCharUUID
// characteristic, StartNotify, and read once (spec requires an empty response).
func (t *Transport) Connect(ctx context.Context, endpointID string) error {
	return errors.New("blemidi: Connect not implemented")
}

func (t *Transport) Disconnect(ctx context.Context, endpointID string) error {
	return errors.New("blemidi: Disconnect not implemented")
}

// Send frames ev.Data as a BLE-MIDI packet and writes it (without response).
//
// TODO: encode([timestampHigh|0x80], [timestampLow|0x80], midiBytes...).
func (t *Transport) Send(ctx context.Context, endpointID string, ev transport.Event) error {
	return errors.New("blemidi: Send not implemented")
}

// Listen decodes inbound BLE-MIDI notifications into MIDI events.
//
// TODO: subscribe to characteristic PropertiesChanged (Value), strip BLE-MIDI
// timestamp framing, emit transport.Event on the returned channel.
func (t *Transport) Listen(ctx context.Context, endpointID string) (<-chan transport.Event, error) {
	return nil, errors.New("blemidi: Listen not implemented")
}
