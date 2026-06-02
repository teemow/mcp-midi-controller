// Package osc implements the OSC/UDP transport, used for the Behringer X32.
// Endpoints are host:port targets; there is no pairing.
package osc

import (
	"context"
	"errors"

	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// Transport is the OSC/UDP backend.
//
// TODO: hold a UDP socket; optionally probe the X32 with /info for discovery.
type Transport struct{}

// New returns an OSC transport.
func New() (*Transport, error) { return &Transport{}, nil }

func (t *Transport) ID() string { return "osc" }

// Discover may broadcast an /info / /xinfo query to find an X32 on the LAN.
//
// TODO: optional UDP broadcast probe; for now endpoints are configured as
// host:port.
func (t *Transport) Discover(ctx context.Context) ([]transport.Endpoint, error) {
	return nil, errors.New("osc: Discover not implemented")
}

// Pair is a no-op for OSC.
func (t *Transport) Pair(ctx context.Context, endpointID string) error { return nil }

// Connect resolves and stores the UDP address for endpointID (host:port).
func (t *Transport) Connect(ctx context.Context, endpointID string) error {
	return errors.New("osc: Connect not implemented")
}

func (t *Transport) Disconnect(ctx context.Context, endpointID string) error { return nil }

// Send marshals ev.OSCAddr + ev.OSCArgs into an OSC message and writes it.
//
// TODO: encode OSC packet (address, type tag string, args) and WriteToUDP.
func (t *Transport) Send(ctx context.Context, endpointID string, ev transport.Event) error {
	return errors.New("osc: Send not implemented")
}

// Listen receives OSC replies (e.g. /node dumps) for state reconciliation.
//
// TODO: read from the UDP socket, parse OSC, emit transport.Event.
func (t *Transport) Listen(ctx context.Context, endpointID string) (<-chan transport.Event, error) {
	return nil, errors.New("osc: Listen not implemented")
}
