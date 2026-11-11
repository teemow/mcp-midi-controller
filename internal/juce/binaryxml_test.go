package juce

import (
	"bytes"
	"strings"
	"testing"
)

func TestBinaryXMLRoundTrip(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?><state a="ä"/>`
	b := EncodeBinaryXML(xml)
	if !IsBinaryXML(b) {
		t.Fatal("encoded block is not recognised")
	}
	if got, want := b[4:8], []byte{byte(len(xml)), 0, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("length word % x, want % x", got, want)
	}
	if b[len(b)-1] != 0 {
		t.Error("the block does not end in a NUL")
	}
	got, err := DecodeBinaryXML(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != xml {
		t.Errorf("decoded %q, want %q", got, xml)
	}
}

func TestBinaryXMLLengthAboveOneByte(t *testing.T) {
	// A state of Battalion's size: the length word holds bytes of 0x80 and
	// more, which a block keeps as they are.
	xml := "<s>" + strings.Repeat("x", 164576) + "</s>"
	b := EncodeBinaryXML(xml)
	if got, want := b[4:8], []byte{0xe7, 0x82, 0x02, 0x00}; !bytes.Equal(got, want) {
		t.Errorf("length word % x, want % x", got, want)
	}
	got, err := DecodeBinaryXML(b)
	if err != nil || got != xml {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestDecodeBinaryXMLReadsOnlyTheLength(t *testing.T) {
	b := append(Header(5), []byte("<a/>x trailing")...)
	got, err := DecodeBinaryXML(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<a/>x" {
		t.Errorf("decoded %q, want the first 5 bytes", got)
	}
}

func TestDecodeBinaryXMLErrors(t *testing.T) {
	if _, err := DecodeBinaryXML([]byte("<?xml?>")); err == nil {
		t.Error("plain XML decoded as a JUCE block")
	}
	if _, err := DecodeBinaryXML(append(Header(100), "<a/>"...)); err == nil {
		t.Error("a length beyond the block decoded")
	}
}

func TestIsHeader(t *testing.T) {
	if !IsHeader(Header(42)) {
		t.Error("a header is not a header")
	}
	if IsHeader(append(Header(1), 'x')) || IsHeader([]byte("VC2")) || IsHeader([]byte("ABCD\x00\x00\x00\x00")) {
		t.Error("something else passed as a header")
	}
}
