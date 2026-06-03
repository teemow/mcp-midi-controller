package aum

// This file is the codec for an AUM mapping leaf's MIDI trigger. AUM stores the
// trigger in one of two version-dependent encodings (see
// docs/research/aum-session.md → "The mapping leaf — two encodings"):
//
//   - packed `spec` (session version 8 / 10): one int packing type+data1+channel.
//   - decomposed `specState` (session version 13 and the standalone
//     .aum_midimap): explicit {enabled, data1, type} + a sibling `channel`.
//
// Both carry the same logical fields; specState just makes them explicit and
// adds the `enabled` flag. The packed form encodes "unassigned" as a
// type-default leaf with data1 == 0 (the placeholder rule). This codec is the
// regression oracle the read model and the round-trip editor share.

// Message-type codes — the high bits of a packed spec, and the value of
// specState.type. Only CC and Note are confirmed from the corpus; the
// value/trigger defaults are the unassigned-placeholder encodings; Program
// Change is the leading (unconfirmed) candidate per the research doc.
const (
	// TypeCC is Control Change (confirmed: Volume = CC 7 across the corpus).
	TypeCC = 0
	// TypeProgramChange is the leading candidate for Program Change (the
	// disabled Session Load default was type 2). UNCONFIRMED — no enabled PC
	// mapping exists in the corpus to verify. See the research doc open items.
	TypeProgramChange = 2
	// TypeValueDefault is the unassigned placeholder for a continuous/value
	// target (`0x2000|ch`, i.e. type 4 / data1 0).
	TypeValueDefault = 4
	// TypeNote is a note message (strongly supported: mute/solo/rec carry
	// notes 60/62/64 across the version-10 sessions).
	TypeNote = 5
	// TypeTriggerDefault is the unassigned placeholder for a trigger/show
	// action (`0x3000|ch`, i.e. type 6 / data1 0).
	TypeTriggerDefault = 6
)

// Spec is the decoded MIDI trigger of a mapping leaf, normalized across both
// on-disk encodings.
//
// Channel is the raw on-disk channel value and its meaning differs by encoding:
// in the packed form it is the 0-based wire channel (0 → MIDI ch 1); in the
// decomposed form it is AUM's channel field where 0 == OMNI and 1..16 are the
// MIDI channels. Encoding records which applies. Enabled is the placeholder
// filter: a leaf is a real mapping only when Enabled is true.
type Spec struct {
	Type     int
	Data1    int
	Channel  int
	Enabled  bool
	Encoding Encoding
}

// Encoding identifies which on-disk leaf encoding a Spec came from / targets.
type Encoding int

const (
	// EncodingPacked is the packed `spec` int (session version 8 / 10).
	EncodingPacked Encoding = iota
	// EncodingSpecState is the decomposed `specState` dict (session version 13
	// and the standalone .aum_midimap).
	EncodingSpecState
)

// EncodingForVersion returns the leaf encoding a session of the given schema
// version uses. Empirically version >= 13 uses specState; 8/10 use packed spec.
// The boundary is treated as ">= 13" rather than "== 13" so a future version
// keeps the modern encoding.
func EncodingForVersion(version int) Encoding {
	if version >= 13 {
		return EncodingSpecState
	}
	return EncodingPacked
}

// DecodePackedSpec splits a packed `spec` int into its message type, data byte
// and (0-based) channel. Verified bit layout:
//
//	channel = spec & 0x0F
//	data1   = (spec >> 4) & 0x7F
//	type    = spec >> 11
func DecodePackedSpec(spec int) (typ, data1, channel int) {
	channel = spec & 0x0F
	data1 = (spec >> 4) & 0x7F
	typ = spec >> 11
	return typ, data1, channel
}

// EncodePackedSpec packs a message type, data byte and 0-based channel back
// into a `spec` int: spec = (type << 11) | (data1 << 4) | channel.
func EncodePackedSpec(typ, data1, channel int) int {
	return (typ << 11) | ((data1 & 0x7F) << 4) | (channel & 0x0F)
}

// packedAssigned reports whether a packed spec is a real assignment rather than
// an unassigned placeholder. The placeholder is the type-default with data1 0:
// `0x2000|ch` (type 4) for value targets, `0x3000|ch` (type 6) for triggers.
func packedAssigned(spec int) bool {
	typ, data1, _ := DecodePackedSpec(spec)
	if data1 == 0 && (typ == TypeValueDefault || typ == TypeTriggerDefault) {
		return false
	}
	return true
}

// decodePacked turns a packed spec into a Spec, applying the placeholder rule.
func decodePacked(spec int) Spec {
	typ, data1, ch := DecodePackedSpec(spec)
	return Spec{
		Type:     typ,
		Data1:    data1,
		Channel:  ch,
		Enabled:  packedAssigned(spec),
		Encoding: EncodingPacked,
	}
}
