// Package usbcodec implements the value encodings and (later) the per-protocol
// framing used by the USB editor/readback protocols (see docs/usb-tools.md).
//
// This file ports the SL-2 editor's value encodings (from the decompiled
// "BOSS TONE STUDIO" config/constant.js + utilities/converter.js, see
// docs/research/sl-2.md). A USB parameter's on-the-wire representation is named
// by an encoding string in the device profile's param map (the `enc` field):
//
//	int1x7   1 byte, 7-bit value (0..127)            — the common CC-sized int
//	int1xN   1 byte, N significant bits (int1x1..1x6) — fewer-bit ints
//	int2x4   2 bytes, 4 bits each (8-bit, nibbles)    — e.g. 0xAB -> 0A 0B
//	int4x4   4 bytes, 4 bits each (16-bit, nibbles)   — e.g. tempo 1220 -> 00 04 0C 04
//	int2x7   2 bytes, 7 bits each (14-bit MIDI value) — e.g. 1000 -> 07 68
//	ascii<N> fixed-length, space-padded ASCII text    — e.g. patch names (ascii16)
//	bytes<N> opaque N-byte blob                        — full patch/preset dumps
//
// The int encodings share one scheme: `int<W>x<B>` splits the value into W
// big-endian groups of B bits, one group per wire byte (low bits). int1x7 is
// just W=1,B=7; int4x4 is W=4,B=4; etc. A signed offset (`ofs`) is applied per
// param, not baked into the encoding: the wire value is the logical value plus
// ofs (e.g. PITCH ofs:12 maps -12..+12 onto wire 0..24). See EncodeInt/DecodeInt.
package usbcodec

import (
	"fmt"
	"strconv"
	"strings"
)

// encKind classifies a parsed encoding string.
type encKind int

const (
	kindInt   encKind = iota // int<W>x<B>: W bytes, B bits each, big-endian
	kindASCII                // ascii<N>: N-byte space-padded text
	kindBytes                // bytes<N>: N-byte opaque blob
)

// encSpec is a parsed encoding string.
type encSpec struct {
	kind  encKind
	bytes int // wire width in bytes (W, or N)
	bits  int // bits per byte (B); only meaningful for kindInt
}

// parseEncoding parses an encoding string into its spec. Returns an error for
// any unknown or malformed encoding so callers (e.g. DeviceType.Validate) can
// reject bad profiles early.
func parseEncoding(enc string) (encSpec, error) {
	switch {
	case strings.HasPrefix(enc, "int"):
		// int<W>x<B>
		rest := enc[len("int"):]
		x := strings.IndexByte(rest, 'x')
		if x <= 0 || x == len(rest)-1 {
			return encSpec{}, fmt.Errorf("usbcodec: malformed int encoding %q (want int<bytes>x<bits>)", enc)
		}
		w, err := strconv.Atoi(rest[:x])
		if err != nil || w < 1 || w > 8 {
			return encSpec{}, fmt.Errorf("usbcodec: bad byte count in encoding %q", enc)
		}
		b, err := strconv.Atoi(rest[x+1:])
		if err != nil || b < 1 || b > 8 {
			return encSpec{}, fmt.Errorf("usbcodec: bad bit count in encoding %q (want 1..8)", enc)
		}
		return encSpec{kind: kindInt, bytes: w, bits: b}, nil
	case strings.HasPrefix(enc, "ascii"):
		n, err := strconv.Atoi(enc[len("ascii"):])
		if err != nil || n < 1 {
			return encSpec{}, fmt.Errorf("usbcodec: bad length in encoding %q (want ascii<N>)", enc)
		}
		return encSpec{kind: kindASCII, bytes: n}, nil
	case strings.HasPrefix(enc, "bytes"):
		n, err := strconv.Atoi(enc[len("bytes"):])
		if err != nil || n < 1 {
			return encSpec{}, fmt.Errorf("usbcodec: bad length in encoding %q (want bytes<N>)", enc)
		}
		return encSpec{kind: kindBytes, bytes: n}, nil
	default:
		return encSpec{}, fmt.Errorf("usbcodec: unknown encoding %q", enc)
	}
}

// KnownEncoding reports whether enc is a recognised, well-formed encoding. It is
// used by DeviceType.Validate to reject bad profile param maps.
func KnownEncoding(enc string) bool {
	_, err := parseEncoding(enc)
	return err == nil
}

// Width returns the fixed wire width, in bytes, of an encoding. Useful to derive
// an RQ1/read size from a param's encoding.
func Width(enc string) (int, error) {
	s, err := parseEncoding(enc)
	if err != nil {
		return 0, err
	}
	return s.bytes, nil
}

// Numeric reports whether enc decodes to an integer (the int<W>x<B> family).
func Numeric(enc string) bool {
	s, err := parseEncoding(enc)
	return err == nil && s.kind == kindInt
}

// Encode renders a logical value into the wire bytes for the given encoding.
// v must be an int for the int<W>x<B> encodings, a string for ascii<N>, and a
// []byte (or string, taken as raw bytes) for bytes<N>. It does not apply a
// signed offset; use EncodeInt for offset-carrying numeric params.
func Encode(enc string, v any) ([]byte, error) {
	s, err := parseEncoding(enc)
	if err != nil {
		return nil, err
	}
	switch s.kind {
	case kindInt:
		n, ok := asInt(v)
		if !ok {
			return nil, fmt.Errorf("usbcodec: encoding %q needs an integer value, got %T", enc, v)
		}
		return encodeInt(s, n)
	case kindASCII:
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("usbcodec: encoding %q needs a string value, got %T", enc, v)
		}
		return encodeASCII(s.bytes, str), nil
	case kindBytes:
		raw, err := asBytes(v)
		if err != nil {
			return nil, fmt.Errorf("usbcodec: encoding %q needs bytes/string, %w", enc, err)
		}
		return encodeBytes(s.bytes, raw), nil
	default:
		return nil, fmt.Errorf("usbcodec: unhandled encoding %q", enc)
	}
}

// Decode parses wire bytes into a logical value. The int<W>x<B> encodings return
// an int, ascii<N> returns a (trailing-trimmed) string, and bytes<N> returns the
// raw []byte. It does not apply a signed offset; use DecodeInt for offset params.
func Decode(enc string, b []byte) (any, error) {
	s, err := parseEncoding(enc)
	if err != nil {
		return nil, err
	}
	switch s.kind {
	case kindInt:
		return decodeInt(s, b)
	case kindASCII:
		return decodeASCII(s.bytes, b), nil
	case kindBytes:
		return decodeBytes(s.bytes, b), nil
	default:
		return nil, fmt.Errorf("usbcodec: unhandled encoding %q", enc)
	}
}

// EncodeInt encodes a numeric param's logical value into wire bytes, applying a
// signed offset (wire = v + ofs). It errors if enc is not an int<W>x<B>
// encoding. ofs is 0 for the common unsigned case.
func EncodeInt(enc string, v, ofs int) ([]byte, error) {
	s, err := parseEncoding(enc)
	if err != nil {
		return nil, err
	}
	if s.kind != kindInt {
		return nil, fmt.Errorf("usbcodec: EncodeInt: %q is not a numeric encoding", enc)
	}
	return encodeInt(s, v+ofs)
}

// DecodeInt decodes a numeric param's wire bytes into its logical value,
// removing a signed offset (logical = wire - ofs). It errors if enc is not an
// int<W>x<B> encoding.
func DecodeInt(enc string, b []byte, ofs int) (int, error) {
	s, err := parseEncoding(enc)
	if err != nil {
		return 0, err
	}
	if s.kind != kindInt {
		return 0, fmt.Errorf("usbcodec: DecodeInt: %q is not a numeric encoding", enc)
	}
	n, err := decodeInt(s, b)
	if err != nil {
		return 0, err
	}
	return n - ofs, nil
}

// encodeInt splits v into s.bytes big-endian groups of s.bits bits each.
func encodeInt(s encSpec, v int) ([]byte, error) {
	if v < 0 {
		return nil, fmt.Errorf("usbcodec: value %d is negative (apply ofs first)", v)
	}
	total := s.bytes * s.bits
	if total < 64 && v >= (1<<total) {
		return nil, fmt.Errorf("usbcodec: value %d does not fit in %d bits", v, total)
	}
	mask := (1 << s.bits) - 1
	out := make([]byte, s.bytes)
	for i := 0; i < s.bytes; i++ {
		shift := s.bits * (s.bytes - 1 - i)
		out[i] = byte((v >> shift) & mask)
	}
	return out, nil
}

// decodeInt reassembles a value from s.bytes big-endian groups of s.bits bits.
func decodeInt(s encSpec, b []byte) (int, error) {
	if len(b) < s.bytes {
		return 0, fmt.Errorf("usbcodec: need %d bytes to decode, got %d", s.bytes, len(b))
	}
	mask := (1 << s.bits) - 1
	v := 0
	for i := 0; i < s.bytes; i++ {
		v = (v << s.bits) | (int(b[i]) & mask)
	}
	return v, nil
}

// encodeASCII renders s as exactly n bytes: truncated if longer, space-padded if
// shorter (the Boss/Roland name convention).
func encodeASCII(n int, s string) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	copy(out, []byte(s))
	return out
}

// decodeASCII reads up to n bytes and trims trailing spaces and NULs.
func decodeASCII(n int, b []byte) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimRight(string(b), " \x00")
}

// encodeBytes renders raw as exactly n bytes (truncated or zero-padded).
func encodeBytes(n int, raw []byte) []byte {
	out := make([]byte, n)
	copy(out, raw)
	return out
}

// decodeBytes returns a copy of up to n bytes.
func decodeBytes(n int, b []byte) []byte {
	if len(b) > n {
		b = b[:n]
	}
	return append([]byte(nil), b...)
}

// asInt coerces the common JSON/YAML-decoded numeric kinds to int.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(n), float64(n) == float64(int(n))
	case float64:
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}

// asBytes accepts a []byte (used verbatim) or a string (its raw bytes).
func asBytes(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case string:
		return []byte(b), nil
	default:
		return nil, fmt.Errorf("got %T", v)
	}
}
