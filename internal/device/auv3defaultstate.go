package device

import (
	"encoding/base64"
	"fmt"
	"sort"
	"unicode/utf8"
)

// This file is the user-defined default-state model: a persistent, per-audio-unit
// default fullState (AuStateDoc) the AUM author applies automatically to any node
// of that audio unit. It is a human-readable YAML container (config dir, rig-as-
// code) separate from the volatile probe dumps. See docs/auv3-state-authoring.md.
//
// The honest hybrid: our own plugins' state and a large class of third-party
// plugins (JUCE XML, JSON synths) are UTF-8 text a user/agent can read and edit;
// the rest (FabFilter, iSEM's ISEMPatch, etc.) is opaque binary that can only be
// round-tripped, so it is stored base64. Capture classifies each fullState leaf
// into the richest round-trip-safe encoding via ClassifyStateDoc.

// AUv3DefaultState is one audio unit's user-defined default fullState. It is
// matched to a session node by the component tuple (type+subtype+manufacturer);
// the filename is only a human handle. The identity tuple is NOT re-stored in
// the AuStateDoc here — aum.buildAuStateDoc re-derives it from the component.
type AUv3DefaultState struct {
	Component ProbeComponent        `yaml:"component"`
	Name      string                `yaml:"name,omitempty"`
	State     map[string]StateEntry `yaml:"state"`
}

// StateEntry is one fullState key's value, in exactly one round-trip-safe
// encoding:
//
//   - Text: the value is exact UTF-8 (our own JSON config, a JUCE/synth XML or
//     JSON body, …). Stored verbatim; bytes = []byte(Text). When the on-disk
//     blob is a short binary header followed by a text body (e.g. JUCE's "VC2!"
//     prefix before the XML), Prefix holds the base64 of that leading header and
//     the bytes are Prefix-decoded || Text.
//   - Base64: opaque binary (FabFilter FFBS, iSEM ISEMPatch, compressed/bplist
//     state, …). bytes = base64-decode(Base64).
//
// Exactly one of Text / Base64 is set (validated). Prefix is only valid with
// Text. JSON and XML are stored as Text (they are UTF-8); structured field-level
// editing parses them on demand and is layered on top later — the storage model
// stays a byte-exact text/binary split so capture round-trips losslessly.
type StateEntry struct {
	Text   string `yaml:"text,omitempty" json:"text,omitempty"`
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Base64 string `yaml:"base64,omitempty" json:"base64,omitempty"`
}

// Bytes resolves a single entry to its wire bytes, validating the encoding.
func (e StateEntry) Bytes() ([]byte, error) {
	hasText := e.Text != "" || e.Prefix != ""
	hasB64 := e.Base64 != ""
	switch {
	case hasText && hasB64:
		return nil, fmt.Errorf("state entry sets both text/prefix and base64 (use exactly one encoding)")
	case !hasText && !hasB64:
		// An explicitly empty value is a legitimate empty blob.
		return []byte{}, nil
	case hasB64:
		b, err := base64.StdEncoding.DecodeString(e.Base64)
		if err != nil {
			return nil, fmt.Errorf("state entry base64: %w", err)
		}
		return b, nil
	default: // text (with optional binary prefix)
		var out []byte
		if e.Prefix != "" {
			p, err := base64.StdEncoding.DecodeString(e.Prefix)
			if err != nil {
				return nil, fmt.Errorf("state entry prefix: %w", err)
			}
			out = append(out, p...)
		}
		return append(out, []byte(e.Text)...), nil
	}
}

// StateDoc resolves every entry to bytes, returning the fullState key -> bytes
// map aum.NodeSpec.StateDoc / SetAuStateDoc expect. Keys are validated to be
// non-empty and to exclude the identity keys (which the author re-derives).
func (d AUv3DefaultState) StateDoc() (map[string][]byte, error) {
	out := make(map[string][]byte, len(d.State))
	for _, k := range sortedStateKeys(d.State) {
		if k == "" {
			return nil, fmt.Errorf("state has an empty key")
		}
		switch k {
		case "type", "subtype", "manufacturer", "version":
			return nil, fmt.Errorf("state key %q is an AuStateDoc identity key (re-derived from the component, do not store it)", k)
		}
		b, err := d.State[k].Bytes()
		if err != nil {
			return nil, fmt.Errorf("state[%q]: %w", k, err)
		}
		out[k] = b
	}
	return out, nil
}

// Validate checks the component identity and that every entry resolves.
func (d AUv3DefaultState) Validate() error {
	if d.Component.Type == "" || d.Component.Subtype == "" || d.Component.Manufacturer == "" {
		return fmt.Errorf("component needs type, subtype and manufacturer")
	}
	if len(d.State) == 0 {
		return fmt.Errorf("state is empty (nothing to apply)")
	}
	_, err := d.StateDoc()
	return err
}

// ClassifyStateDoc turns a harvested fullState (key -> raw bytes) into a map of
// StateEntry, picking the richest round-trip-safe encoding per leaf (Tier-0 text
// detection from docs/auv3-state-authoring.md):
//
//   - pure UTF-8 text -> Text
//   - a short binary header followed by an XML/text body -> Text + Prefix
//   - anything else (binary / compressed / bplist) -> Base64
//
// Every result is verified to round-trip back to the input bytes; a leaf that
// would not (an over-eager text split) falls back to Base64, so capture is
// always lossless.
func ClassifyStateDoc(doc map[string][]byte) map[string]StateEntry {
	out := make(map[string]StateEntry, len(doc))
	for k, b := range doc {
		out[k] = classifyStateEntry(b)
	}
	return out
}

// maxTextPrefix bounds how many leading binary bytes a text body may hide behind
// (e.g. JUCE's "VC2!" + a length word). Beyond this the blob is treated as binary.
const maxTextPrefix = 16

func classifyStateEntry(b []byte) StateEntry {
	if len(b) == 0 {
		return StateEntry{}
	}
	if isPrintableUTF8(b) {
		return StateEntry{Text: string(b)}
	}
	// A short binary header (length/version word) before an XML/text body: split
	// it off so the readable part is editable while the header round-trips.
	if i := textBodyStart(b); i > 0 {
		body := b[i:]
		if isPrintableUTF8(body) {
			return StateEntry{
				Prefix: base64.StdEncoding.EncodeToString(b[:i]),
				Text:   string(body),
			}
		}
	}
	return StateEntry{Base64: base64.StdEncoding.EncodeToString(b)}
}

// textBodyStart returns the index of an XML declaration / root element that
// begins within the first maxTextPrefix bytes (after a short binary header), or
// -1. It anchors on "<?xml" or "<<alpha" so it does not fire on stray '<' bytes
// inside binary.
func textBodyStart(b []byte) int {
	limit := maxTextPrefix
	if limit > len(b)-1 {
		limit = len(b) - 1
	}
	for i := 1; i <= limit; i++ {
		if b[i] != '<' {
			continue
		}
		rest := b[i:]
		if hasPrefix(rest, "<?xml") {
			return i
		}
		if len(rest) >= 2 && isAlpha(rest[1]) {
			return i
		}
	}
	return -1
}

// isPrintableUTF8 reports whether b is valid UTF-8 made only of printable runes
// and common whitespace — i.e. safe and useful to store as a YAML text scalar.
func isPrintableUTF8(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func hasPrefix(b []byte, s string) bool {
	if len(b) < len(s) {
		return false
	}
	return string(b[:len(s)]) == s
}

func sortedStateKeys(m map[string]StateEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
