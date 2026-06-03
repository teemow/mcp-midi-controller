package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestParseAddrArg(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"number", float64(536870912), 0x20000000},
		{"hex string", "0x20000000", 0x20000000},
		{"plain hex", "20000000", 20000000}, // no 0x prefix -> decimal
		{"wire bytes", "20 00 00 00", 0x20000000},
		{"nil", nil, 0},
		{"empty string", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAddrArg(c.in)
			if err != nil {
				t.Fatalf("parseAddrArg(%v): %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("parseAddrArg(%v) = 0x%X, want 0x%X", c.in, got, c.want)
			}
		})
	}
}

func TestParseBytesArg(t *testing.T) {
	arr, err := parseBytesArg(json.RawMessage(`[0, 4, 12, 4]`))
	if err != nil {
		t.Fatalf("array: %v", err)
	}
	if len(arr) != 4 || arr[2] != 12 {
		t.Fatalf("array bytes = % X", arr)
	}

	hexSpaced, err := parseBytesArg(json.RawMessage(`"00 04 0C 04"`))
	if err != nil {
		t.Fatalf("hex spaced: %v", err)
	}
	if len(hexSpaced) != 4 || hexSpaced[2] != 0x0C {
		t.Fatalf("hex bytes = % X", hexSpaced)
	}

	hexPacked, err := parseBytesArg(json.RawMessage(`"00040C04"`))
	if err != nil {
		t.Fatalf("hex packed: %v", err)
	}
	if len(hexPacked) != 4 || hexPacked[3] != 0x04 {
		t.Fatalf("packed bytes = % X", hexPacked)
	}

	if _, err := parseBytesArg(json.RawMessage(`[300]`)); err == nil {
		t.Fatalf("expected out-of-range byte error")
	}
}

func TestHexAndASCIIBytes(t *testing.T) {
	if got := hexBytes([]byte{0x00, 0x1D}); got != "00 1D" {
		t.Fatalf("hexBytes = %q", got)
	}
	if got := hexBytes(nil); got != "(empty)" {
		t.Fatalf("hexBytes(nil) = %q", got)
	}
	if got := asciiBytes([]byte("Hi\x00!")); got != "Hi.!" {
		t.Fatalf("asciiBytes = %q", got)
	}
}
