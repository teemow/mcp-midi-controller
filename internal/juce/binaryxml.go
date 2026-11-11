// Package juce reads and writes the binary wrapper JUCE plugins put around the
// XML of their saved state (AudioProcessor::copyXmlToBinary): the magic
// "VC2!", a little-endian uint32 holding the length of the XML text in bytes,
// the text, and a NUL. A plugin reads at most that many bytes of text, so a
// state whose XML is edited needs the length written again.
package juce

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Magic is the first four bytes of a JUCE binary XML block.
const Magic = "VC2!"

// HeaderLen is the length of the header: the magic and the length word.
const HeaderLen = 8

// IsBinaryXML reports whether b starts with JUCE's binary XML header.
func IsBinaryXML(b []byte) bool {
	return len(b) >= HeaderLen && string(b[:len(Magic)]) == Magic
}

// IsHeader reports whether b is exactly a JUCE binary XML header (the magic
// and a length word, nothing more): the binary prefix a state entry keeps in
// front of its XML text.
func IsHeader(b []byte) bool {
	return len(b) == HeaderLen && IsBinaryXML(b)
}

// Header is the header of a block whose XML text is n bytes long.
func Header(n int) []byte {
	h := make([]byte, HeaderLen)
	copy(h, Magic)
	binary.LittleEndian.PutUint32(h[len(Magic):], uint32(n))
	return h
}

// DecodeBinaryXML returns the XML text of a JUCE binary XML block: as many
// bytes as its length word says, without the NUL after them.
func DecodeBinaryXML(b []byte) (string, error) {
	if !IsBinaryXML(b) {
		return "", fmt.Errorf("not a JUCE binary XML block (it does not start with %q)", Magic)
	}
	n := int(binary.LittleEndian.Uint32(b[len(Magic):HeaderLen]))
	body := b[HeaderLen:]
	if n > len(body) {
		return "", fmt.Errorf("JUCE binary XML: the header says %d bytes of text, the block holds %d", n, len(body))
	}
	text := body[:n]
	// A length word written by hand may count the NUL as well.
	text = bytes.TrimRight(text, "\x00")
	return string(text), nil
}

// EncodeBinaryXML wraps XML text the way JUCE writes it: the header with the
// text's length, the text, and a NUL.
func EncodeBinaryXML(xml string) []byte {
	out := make([]byte, 0, HeaderLen+len(xml)+1)
	out = append(out, Header(len(xml))...)
	out = append(out, xml...)
	return append(out, 0)
}
