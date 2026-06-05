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
