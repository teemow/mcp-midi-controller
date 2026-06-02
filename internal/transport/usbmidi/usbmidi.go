// Package usbmidi implements the (bonus) USB/ALSA MIDI transport via
// gitlab.com/gomidi/midi. Endpoints are ALSA sequencer port names; there is no
// pairing.
//
// This backend requires CGO + ALSA headers when using the rtmidi driver.
package usbmidi

import (
	"context"
	"errors"

	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// Transport is the USB/ALSA MIDI backend.
//
// TODO: wrap gomidi out/in ports; FindOutPort / FindInPort by name.
type Transport struct{}

// New returns a USB-MIDI transport.
func New() (*Transport, error) { return &Transport{}, nil }

func (t *Transport) ID() string { return "usbmidi" }

// Discover lists ALSA MIDI ports.
//
// TODO: midi.GetOutPorts() / GetInPorts().
func (t *Transport) Discover(ctx context.Context) ([]transport.Endpoint, error) {
	return nil, errors.New("usbmidi: Discover not implemented")
}

// Pair is a no-op for USB-MIDI.
func (t *Transport) Pair(ctx context.Context, endpointID string) error { return nil }

func (t *Transport) Connect(ctx context.Context, endpointID string) error {
	return errors.New("usbmidi: Connect not implemented")
}

func (t *Transport) Disconnect(ctx context.Context, endpointID string) error { return nil }

// Send writes raw MIDI bytes to the named ALSA out port.
//
// TODO: out.Send(ev.Data).
func (t *Transport) Send(ctx context.Context, endpointID string, ev transport.Event) error {
	return errors.New("usbmidi: Send not implemented")
}

// Listen streams inbound MIDI from the named ALSA in port.
//
// TODO: midi.ListenTo(in, ...) → transport.Event.
func (t *Transport) Listen(ctx context.Context, endpointID string) (<-chan transport.Event, error) {
	return nil, errors.New("usbmidi: Listen not implemented")
}
