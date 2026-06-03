package usbcodec

import (
	"bytes"
	"testing"
)

// TestEncodeIntVectors covers the int<W>x<B> family against the SL-2 vectors
// recovered from BOSS TONE STUDIO and confirmed live (docs/research/sl-2.md).
func TestEncodeIntVectors(t *testing.T) {
	cases := []struct {
		name string
		enc  string
		v    int
		want []byte
	}{
		// SYSTEM tempo: 1220 (=122.0 BPM ×10) -> 00 04 0C 04 (confirmed live).
		{"tempo int4x4", "int4x4", 1220, []byte{0x00, 0x04, 0x0C, 0x04}},
		// SYSTEM MIDI channel: stored 0-indexed (05 -> channel 6 to the user).
		{"midi-ch int1x7", "int1x7", 5, []byte{0x05}},
		// int1x7 upper bound.
		{"int1x7 max", "int1x7", 127, []byte{0x7F}},
		// int2x4: 8-bit value split into two nibbles, e.g. 0xAB -> 0A 0B.
		{"int2x4 nibbles", "int2x4", 0xAB, []byte{0x0A, 0x0B}},
		// int1xN: fewer significant bits in a single byte.
		{"int1x4", "int1x4", 9, []byte{0x09}},
		// int2x7: 14-bit MIDI value across two 7-bit bytes (1000 = 0x3E8).
		{"int2x7", "int2x7", 1000, []byte{0x07, 0x68}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.enc, tc.v)
			if err != nil {
				t.Fatalf("Encode(%q, %d): %v", tc.enc, tc.v, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("Encode(%q, %d) = % X, want % X", tc.enc, tc.v, got, tc.want)
			}
			// Round-trip back to the value.
			back, err := Decode(tc.enc, tc.want)
			if err != nil {
				t.Fatalf("Decode(%q, % X): %v", tc.enc, tc.want, err)
			}
			if back.(int) != tc.v {
				t.Fatalf("Decode(%q, % X) = %d, want %d", tc.enc, tc.want, back, tc.v)
			}
		})
	}
}

func TestEncodeIntBounds(t *testing.T) {
	// int1x7 only holds 7 bits; 128 overflows.
	if _, err := Encode("int1x7", 128); err == nil {
		t.Fatal("expected overflow error for int1x7 value 128")
	}
	// int2x4 holds 8 bits; 256 overflows.
	if _, err := Encode("int2x4", 256); err == nil {
		t.Fatal("expected overflow error for int2x4 value 256")
	}
	// Negative values are only valid through the offset path.
	if _, err := Encode("int1x7", -1); err == nil {
		t.Fatal("expected error for negative value without offset")
	}
}

func TestEncodeDecodeOffset(t *testing.T) {
	// PITCH: ofs 12 maps the logical -12..+12 onto wire 0..24 (sl-2.md).
	cases := []struct {
		v    int
		want byte
	}{
		{-12, 0x00},
		{0, 0x0C},
		{12, 0x18},
	}
	for _, tc := range cases {
		got, err := EncodeInt("int1x7", tc.v, 12)
		if err != nil {
			t.Fatalf("EncodeInt(int1x7, %d, 12): %v", tc.v, err)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("EncodeInt(int1x7, %d, 12) = % X, want %02X", tc.v, got, tc.want)
		}
		back, err := DecodeInt("int1x7", got, 12)
		if err != nil {
			t.Fatalf("DecodeInt(int1x7, % X, 12): %v", got, err)
		}
		if back != tc.v {
			t.Fatalf("DecodeInt round-trip = %d, want %d", back, tc.v)
		}
	}
}

func TestEncodeASCII(t *testing.T) {
	// SL-2 temp patch name is a 16-byte field (ascii16), space-padded.
	got, err := Encode("ascii16", "TREMOLO 5-02")
	if err != nil {
		t.Fatalf("Encode(ascii16): %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("ascii16 width = %d, want 16", len(got))
	}
	want := []byte("TREMOLO 5-02    ")
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode(ascii16) = %q, want %q", got, want)
	}
	dec, err := Decode("ascii16", got)
	if err != nil {
		t.Fatalf("Decode(ascii16): %v", err)
	}
	if dec.(string) != "TREMOLO 5-02" {
		t.Fatalf("Decode(ascii16) = %q, want %q", dec, "TREMOLO 5-02")
	}
	// Over-long input is truncated to the field width.
	long, _ := Encode("ascii4", "ABCDEFG")
	if string(long) != "ABCD" {
		t.Fatalf("ascii4 truncation = %q, want %q", long, "ABCD")
	}
}

func TestEncodeBytes(t *testing.T) {
	blob := []byte{0x01, 0x02, 0x03, 0x04}
	got, err := Encode("bytes4", blob)
	if err != nil {
		t.Fatalf("Encode(bytes4): %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("Encode(bytes4) = % X, want % X", got, blob)
	}
	// Short input is zero-padded to the field width.
	short, _ := Encode("bytes4", []byte{0xFF})
	if !bytes.Equal(short, []byte{0xFF, 0x00, 0x00, 0x00}) {
		t.Fatalf("bytes4 padding = % X", short)
	}
	dec, err := Decode("bytes4", got)
	if err != nil {
		t.Fatalf("Decode(bytes4): %v", err)
	}
	if !bytes.Equal(dec.([]byte), blob) {
		t.Fatalf("Decode(bytes4) = % X, want % X", dec, blob)
	}
}

func TestKnownEncodingAndWidth(t *testing.T) {
	known := map[string]int{
		"int1x7": 1, "int1x4": 1, "int2x4": 2, "int4x4": 4, "int2x7": 2,
		"ascii16": 16, "bytes32": 32,
	}
	for enc, width := range known {
		if !KnownEncoding(enc) {
			t.Errorf("KnownEncoding(%q) = false, want true", enc)
		}
		w, err := Width(enc)
		if err != nil {
			t.Errorf("Width(%q): %v", enc, err)
		}
		if w != width {
			t.Errorf("Width(%q) = %d, want %d", enc, w, width)
		}
	}
	for _, bad := range []string{"", "int", "intx", "int1x", "intax4", "int4x9", "ascii", "asciiX", "bytes", "float4", "int0x4"} {
		if KnownEncoding(bad) {
			t.Errorf("KnownEncoding(%q) = true, want false", bad)
		}
	}
}

func TestNumeric(t *testing.T) {
	for _, enc := range []string{"int1x7", "int4x4", "int2x7"} {
		if !Numeric(enc) {
			t.Errorf("Numeric(%q) = false, want true", enc)
		}
	}
	for _, enc := range []string{"ascii16", "bytes4", "bogus"} {
		if Numeric(enc) {
			t.Errorf("Numeric(%q) = true, want false", enc)
		}
	}
}
