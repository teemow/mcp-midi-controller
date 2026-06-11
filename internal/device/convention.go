package device

// convention.go is the single source of truth for the server's AUM CC
// convention — the per-strip mixer CCs (Volume/Mute/Solo/Rec) and the global
// transport CCs (play/stop/record/...). The authoring path (internal/aum) wires
// sessions to it, and DeviceTypeFromProbe steers generated CCs clear of it; both
// derive from these functions so the two cannot drift. The convention lives in
// this leaf package (internal/aum imports device, not the other way round) so
// the authoring side and the probe generator share one definition.

// ConventionMixerCC returns the AUM mixer-convention CC for a non-master audio
// strip's channel control. n is the 1-based audio-channel ordinal; ok is false
// outside the documented 1..8 range or for a target with no mixer CC.
func ConventionMixerCC(n int, target string) (int, bool) {
	if n < 1 || n > 8 {
		return 0, false
	}
	switch target {
	case "Mute":
		return 18 + 3*n, true
	case "Volume":
		return 19 + 3*n, true
	case "Solo":
		return 44 + n, true
	case "Rec enable":
		return 52 + n, true
	default:
		return 0, false
	}
}

// ConventionTransportCC returns the brain-control convention CC for a global
// Transport-collection target. ok is false for targets the convention does not
// cover. The CC numbers mirror docs/research/aum.md ("Transport / system").
func ConventionTransportCC(target string) (int, bool) {
	switch target {
	case "Toggle Play":
		return 20, true
	case "Start Play":
		return 102, true
	case "Stop/Rewind":
		return 103, true
	case "Rewind":
		return 104, true
	case "Toggle Record":
		return 105, true
	case "Tap Tempo":
		return 108, true
	default:
		return 0, false
	}
}

// TapControlChannel is the reserved 1-based MIDI channel the convention uses
// for ProbeAudioTap bypass toggles. Tap toggles ride this channel on their own
// — not the shared mixer/node/transport channel a binding supplies — so a tap
// CC can never collide with the mixer (CC 20..76) or node-parameter (CC 30+)
// blocks regardless of the binding channel. The mixer/node convention channel
// must therefore stay in 1..15 (channel 16 is reserved here). Stored 0-based on
// disk as TapControlChannel-1 (= 15), like every other AUM channel field.
const TapControlChannel = 16

// SessionSwitchChannel is the reserved 1-based MIDI channel for cross-session
// Session Load Program Changes: the daemon's session-switch registry pins one
// PC program per staged session, and the user hand-maps AUM's global "Session
// Load" actions (never stored in the .aumproj) to PCs on this channel. It
// shares channel 16 with the tap toggles deliberately — taps are CCs, session
// switches are PCs, distinct message types that cannot collide — while staying
// clear of the preset-load PCs, which ride the binding channel (1..15). See
// docs/research/aum.md ("Session / Preset load").
const SessionSwitchChannel = 16

const (
	// tapStartCC / tapMaxCC bound the contiguous CC block tap-bypass toggles
	// occupy on TapControlChannel: 77..95, a band clear of the mixer (≤76),
	// transport/system (≥102) and the MIDI-reserved 96..101 / 120..127 ranges,
	// so the tap block stays distinct even when read on a shared channel. 19
	// slots cover the largest planned session (S5, ≤18 audio channels).
	tapStartCC = 77
	tapMaxCC   = 95
)

// ConventionTapCC returns the convention CC for the n-th post-fader ProbeAudioTap
// (1-based, in channel order). Each tap's _AUMNode:Bypass is mapped to this CC on
// TapControlChannel with AutoToggle ("Cycle") on, so a single brain CC flips that
// tap's stream on/off. ok is false for n < 1 or once the tap block is exhausted
// (n past the 77..95 range) — an overflowed tap stays an unassigned placeholder.
func ConventionTapCC(n int) (int, bool) {
	if n < 1 {
		return 0, false
	}
	cc := tapStartCC + (n - 1)
	if cc > tapMaxCC {
		return 0, false
	}
	return cc, true
}

// conventionMixerTargets / conventionTransportTargets are the targets the
// convention wires, used to derive the reserved-CC set from the same formulae
// (instead of re-listing the numbers, which would drift).
var (
	conventionMixerTargets     = []string{"Mute", "Volume", "Solo", "Rec enable"}
	conventionTransportTargets = []string{"Toggle Play", "Start Play", "Stop/Rewind", "Rewind", "Toggle Record", "Tap Tempo"}
)

// ConventionReservedCCs is the set of CCs a probe-derived parameter must avoid:
// the MIDI-reserved controllers (Bank Select, Data Entry, RPN/NRPN selectors,
// channel-mode) plus the AUM mixer + transport convention band. The convention
// CCs come straight from ConventionMixerCC/ConventionTransportCC, so this set
// tracks the convention automatically rather than re-deriving its formulae.
func ConventionReservedCCs() map[int]bool {
	r := map[int]bool{}
	for _, cc := range []int{0, 32, 6, 38, 96, 97, 98, 99, 100, 101} {
		r[cc] = true // Bank Select / Data Entry / RPN+NRPN selectors
	}
	for cc := 120; cc <= 127; cc++ {
		r[cc] = true // channel-mode messages
	}
	for n := 1; n <= 8; n++ {
		for _, target := range conventionMixerTargets {
			if cc, ok := ConventionMixerCC(n, target); ok {
				r[cc] = true
			}
		}
	}
	for _, target := range conventionTransportTargets {
		if cc, ok := ConventionTransportCC(target); ok {
			r[cc] = true
		}
	}
	return r
}
