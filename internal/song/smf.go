package song

// This file writes a compiled song as a Standard MIDI File (format 1), so the
// same notes can go into a sequencer or a DAW: a conductor track with the
// tempo, the time signature and a marker per section, one track per song
// track with its notes and its pedalling, and one track with the control
// messages.

import (
	"bytes"
	"encoding/binary"
	"math"
)

// SMF renders the timeline as a Standard MIDI File.
func (t *Timeline) SMF() []byte {
	var tracks [][]byte

	// Conductor track.
	var c smfTrack
	c.meta(0, 0x03, []byte(t.Title))
	usPerQuarter := uint32(math.Round(60_000_000 / t.Tempo))
	c.meta(0, 0x51, []byte{byte(usPerQuarter >> 16), byte(usPerQuarter >> 8), byte(usPerQuarter)})
	c.meta(0, 0x58, []byte{byte(t.MeterNum), byte(math.Log2(float64(t.MeterDen))), 24, 8})
	for _, sec := range t.Sections {
		c.meta(sec.Tick, 0x06, []byte(sec.Name))
	}
	tracks = append(tracks, c.end(t.Ticks))

	for _, ts := range t.Tracks {
		var tr smfTrack
		tr.meta(0, 0x03, []byte(ts.Name))
		for _, e := range t.Events {
			if e.Track == ts.ID {
				tr.event(e)
			}
		}
		tracks = append(tracks, tr.end(t.Ticks))
	}

	var ctl smfTrack
	ctl.meta(0, 0x03, []byte("Control"))
	controls := 0
	for _, sec := range t.Sections[:min(1, len(t.Sections))] {
		// The opening state, so the file stands on its own.
		for _, e := range sec.State {
			ctl.event(e)
			controls++
		}
	}
	for _, e := range t.Events {
		if (e.Kind == Program || e.Kind == Control) && !e.pedal() {
			ctl.event(e)
			controls++
		}
	}
	if controls > 0 {
		tracks = append(tracks, ctl.end(t.Ticks))
	}

	var out bytes.Buffer
	out.WriteString("MThd")
	_ = binary.Write(&out, binary.BigEndian, uint32(6))
	_ = binary.Write(&out, binary.BigEndian, uint16(1))
	_ = binary.Write(&out, binary.BigEndian, uint16(len(tracks)))
	_ = binary.Write(&out, binary.BigEndian, uint16(PPQ))
	for _, tr := range tracks {
		out.WriteString("MTrk")
		_ = binary.Write(&out, binary.BigEndian, uint32(len(tr)))
		out.Write(tr)
	}
	return out.Bytes()
}

// smfTrack accumulates one track chunk's events, which must be added in tick
// order.
type smfTrack struct {
	buf  bytes.Buffer
	tick int64
}

// delta writes the variable-length time since the track's previous event.
func (t *smfTrack) delta(tick int64) {
	if tick < t.tick {
		tick = t.tick
	}
	writeVarLen(&t.buf, uint32(tick-t.tick))
	t.tick = tick
}

func (t *smfTrack) meta(tick int64, kind byte, data []byte) {
	t.delta(tick)
	t.buf.Write([]byte{0xFF, kind})
	writeVarLen(&t.buf, uint32(len(data)))
	t.buf.Write(data)
}

func (t *smfTrack) event(e Event) {
	ch := byte(e.Channel-1) & 0x0F
	t.delta(e.Tick)
	switch e.Kind {
	case NoteOn:
		t.buf.Write([]byte{0x90 | ch, byte(e.Data1), byte(e.Data2)})
	case NoteOff:
		t.buf.Write([]byte{0x80 | ch, byte(e.Data1), 0})
	case Control:
		t.buf.Write([]byte{0xB0 | ch, byte(e.Data1), byte(e.Data2)})
	case Program:
		t.buf.Write([]byte{0xC0 | ch, byte(e.Data1)})
	}
}

// end closes the track at the song's end and returns its bytes.
func (t *smfTrack) end(tick int64) []byte {
	t.meta(tick, 0x2F, nil)
	return t.buf.Bytes()
}

// writeVarLen writes v as a MIDI variable-length quantity.
func writeVarLen(buf *bytes.Buffer, v uint32) {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7F) | 0x80
	}
	buf.Write(tmp[i:])
}
