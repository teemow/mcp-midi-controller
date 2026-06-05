package aum

// This file is the Phase-3 authoring path: BuildSession synthesizes a complete
// AUM session (.aumproj) from a high-level BuildSpec — ordered mixer strips,
// hosted AUv3 nodes (their identity + mappable parameters taken from the
// plugins' probe dumps), a parallel nodeArchives chain, and a full
// midiCtrlState placeholder catalogue — optionally pre-wired to the server's CC
// convention (docs/research/aum.md, internal/device/definitions/aum.yaml).
//
// Authoring is "template-clone + mutate", not pure synthesis. Rather than
// reconstructing AUM's exact class hierarchy and full default object set from
// nothing, BuildSession builds on the same known-good graph shape the synthetic
// Template() defines (the same NSKeyedArchiver class defs, root AUMSession key
// set, transport clock, and decomposed-specState placeholder leaves AUM
// accepts), parameterized by the spec. The plan's `go:embed template.aumproj`
// became a code-built template because the privacy rule forbids committing a
// real session (see template.go); "clone the template" therefore means "reuse
// that template's proven structure" here.
//
// Privacy: an authored session is a private rig snapshot (channel/plugin names)
// and is only ever staged under the gitignored state dir, never committed — the
// same posture as a read session map.

import (
	"fmt"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

// BuildSpec describes a session to author from scratch. The zero value is not
// useful; supply at least one Channel. Tempo/SampleRate fall back to the
// template defaults (120 BPM / 48000 Hz) when zero. The authored session is
// always written in the modern version-13 / decomposed-specState encoding.
type BuildSpec struct {
	Title      string        // session title (private; caller-supplied, may be "")
	Tempo      float64       // BPM; 0 → 120
	SampleRate float64       // engine sample rate; 0 → 48000
	Channels   []ChannelSpec // ordered mixer strips; the last audio strip is the master

	// Convention, when non-nil, pre-assigns the server CC convention to the
	// generated channel-control and node-parameter placeholders, turning a
	// blank session into one already wired to the convention. When nil every
	// leaf stays an unassigned placeholder — AUM's default, and what an
	// untouched real session looks like.
	Convention *Convention

	// Routes, when non-empty, authors the inter-node MIDI routing matrix
	// (root["midiMatrixState"]) so e.g. a brain node's MIDI OUT feeds a synth
	// node and AUM's MIDI Control. Endpoints reference channels/slots in this
	// spec. See SetMIDIRoutes.
	Routes []MIDIRoute
}

// ChannelSpec is one mixer strip to author. Fader applies only to audio strips
// (MIDI strips have no fader); a nil Fader on an audio strip defaults to unity.
type ChannelSpec struct {
	Kind   ChannelKind // KindAudio or KindMIDI
	Title  string      // channel name (private)
	Fader  *float64    // initial fader level (audio only); nil → 1.0
	Muted  bool
	Soloed bool
	Nodes  []NodeSpec // the slot chain (hosted AUv3 plugins), in order
}

// NodeSpec is one hosted AUv3 node to author. Its on-disk identity
// (audioComponentDescription + componentName) is stamped from Component /
// ComponentName, and its writable Params become the slot's mappable
// midiCtrlState targets — exactly the data a probe dump carries, so
// NodeSpecFromDump is the usual way to build one.
type NodeSpec struct {
	Component     device.ProbeComponent // {type,subtype,manufacturer} tuple → audioComponentDescription
	ComponentName string                // human "Manufacturer: Plugin"
	AuMainParam   string                // optional headline param keyPath (archiveNodeState.AuMainParam)
	Params        []device.ProbeParam   // the plugin's parameters; the writable ones become mappable targets

	// StateDoc, when non-empty, is authored into the node's
	// archiveNodeState["AuStateDoc"] — the plugin's saved fullState as
	// key -> raw bytes (e.g. {"probeMidiBrainProgram": <JSON>}). Used to
	// pre-configure our own plugins (brain program, tap host/streaming).
	StateDoc map[string][]byte
	// Preset, when non-nil, sets the node's AuPresetCtrl (factory preset index).
	Preset *int
}

// NodeSpecFromDump builds a NodeSpec from an AUv3 probe dump: the component
// tuple stamps the node identity, the dump name (prefixed with the
// manufacturer when known) becomes the componentName, and the dump's parameters
// become the slot's mappable targets.
func NodeSpecFromDump(dump device.ProbeDump) NodeSpec {
	name := dump.Name
	if dump.Component.ManufacturerName != "" && name != "" {
		name = dump.Component.ManufacturerName + ": " + name
	}
	return NodeSpec{
		Component:     dump.Component,
		ComponentName: name,
		Params:        dump.Parameters,
	}
}

// Convention configures how BuildSession pre-wires the generated catalogue to
// the server's CC convention. It mirrors the two conventions the server owns:
// the AUM mixer convention for per-channel Volume/Mute/Solo/Rec (aum.yaml,
// channels 1..8), and the AUv3 per-plugin convention for node parameters (one
// CC each, from NodeStartCC, in parameter order).
type Convention struct {
	// Channel is the MIDI channel every assigned CC rides, in AUM's specState
	// convention (1..16; 0 = OMNI). Out-of-range values fall back to 1. The
	// whole session shares one channel here — splitting plugins onto their own
	// MIDI channels is a binding concern handled above this library.
	Channel int
	// NodeStartCC is the first CC assigned to a node's parameters (default 30,
	// matching device.ProbeOptions). Numbering restarts per node, so two nodes'
	// params nominally share CCs on the shared Channel; per-node channels
	// disambiguate them at binding time.
	NodeStartCC int
	// NodeMaxCC caps node-parameter CCs (default 127). Parameters past the cap
	// stay unassigned placeholders and are listed in BuildReport.Overflow.
	NodeMaxCC int
}

// BuildReport summarizes an authored session: the counts that went in and, when
// a Convention was applied, how many CCs were assigned and which node-parameter
// targets overflowed the CC cap (and so remain placeholders).
type BuildReport struct {
	Channels    int      // mixer strips authored
	Nodes       int      // hosted AUv3 nodes authored
	Targets     int      // placeholder mapping targets enumerated (the catalogue size)
	AssignedCCs int      // convention CCs assigned (0 when Convention is nil)
	Overflow    []string // node-parameter target paths beyond the convention CC cap
	Routes      int      // MIDI routes authored into midiMatrixState
}

// channelControl is one strip-level mappable target and whether it is a
// trigger (Mute/Solo/Rec) versus a continuous value (Volume).
type channelControl struct {
	name    string
	trigger bool
}

// audioChannelControls / midiChannelControls are the strip-level targets AUM
// enumerates under a channel's "Channel controls" collection. Audio strips
// carry the full set; MIDI strips (no fader, no record) carry only mute/solo.
var (
	audioChannelControls = []channelControl{
		{"Volume", false}, {"Mute", true}, {"Solo", true}, {"Rec enable", true},
		{"ScrollToChannel", true},
	}
	midiChannelControls = []channelControl{
		{"Mute", true}, {"Solo", true}, {"ScrollToChannel", true},
	}
)

// nodeReservedTargets are the per-slot reserved trigger targets AUM enumerates
// alongside an AUv3 node's own parameters. Key strings confirmed from the probe
// capture: Bypass, "Show & Front Plugin" (FrontPlugin) and "Show / Hide Plugin"
// (TogglePlugin). The per-preset "_AUMNode:PresetLoadCtrl/<idx>:<prog>:<name>"
// targets are dynamic (one per saved preset) and only appear once a preset
// exists, so they are not enumerated here.
var nodeReservedTargets = []string{
	"_AUMNode:Bypass",
	"_AUMNode:FrontPlugin",
	"_AUMNode:TogglePlugin",
}

// transportTargets are the Transport collection actions enumerated as
// placeholders. The first six are the convention-wired actions (see
// conventionTransportCC); the rest are confirmed AUM targets that are
// catalogued but left unwired so an authored session exposes AUM's full
// transport surface.
var transportTargets = []string{
	"Toggle Play", "Start Play", "Stop/Rewind", "Rewind", "Toggle Record", "Tap Tempo",
	"Previous bar", "Next bar", "Tempo", "Metronome on/off",
}

// systemTargets are the global System collection actions AUM enumerates
// (confirmed key strings from the probe capture). They are catalogued as
// placeholders; none are convention-wired.
var systemTargets = []string{
	"_AUM:ShowSelf", "_AUM:HideAllPlugins", "_AUM:UnSoloAll",
}

// BuildSession authors a complete session from spec, returning the typed
// Session (ready to .Map(), .Archive().Encode(), or further edit) plus a
// report. It builds the channel strips, the parallel node chains (AUv3 nodes
// carrying their component identity + state), and the full midiCtrlState
// placeholder catalogue, then — when spec.Convention is set — assigns the
// convention CCs in place via the same editor the round-trip path uses.
func BuildSession(spec BuildSpec) (*Session, BuildReport, error) {
	var report BuildReport

	tempo := spec.Tempo
	if tempo == 0 {
		tempo = 120
	}
	sampleRate := spec.SampleRate
	if sampleRate == 0 {
		sampleRate = 48000
	}

	a := &Archive{Archiver: "NSKeyedArchiver", Version: 100000, Objects: []any{"$null"}}
	b := a.NewBuilder()

	// The master is the last audio strip (per the research doc); the mixer
	// convention numbers only the non-master audio strips.
	masterPos := lastAudioIndex(spec.Channels)

	// --- Mixer strips + parallel node chains ---
	stripUIDs := make([]any, 0, len(spec.Channels))
	nodeArchUIDs := make([]any, 0, len(spec.Channels))
	for i, ch := range spec.Channels {
		fields := map[string]any{
			"index":  int64(i),
			"muted":  ch.Muted,
			"soloed": ch.Soloed,
		}
		if ch.Title != "" {
			fields["title"] = b.Intern(ch.Title)
		}
		class := "AUMAudioStrip"
		if ch.Kind == KindMIDI {
			class = "AUMMIDIStrip"
		} else {
			fader := 1.0
			if ch.Fader != nil {
				fader = *ch.Fader
			}
			fields["faderLevel"] = fader
		}
		strip := keyedObj(b, class, "AUMStrip", fields)
		stripUIDs = append(stripUIDs, b.Intern(strip))

		nodeUIDs := make([]any, 0, len(ch.Nodes))
		for slot, n := range ch.Nodes {
			nodeUIDs = append(nodeUIDs, b.Intern(buildNode(b, n, i, slot)))
			report.Nodes++
		}
		nodeArchUIDs = append(nodeArchUIDs, b.Intern(newNSArray(b, nodeUIDs)))
		report.Channels++
	}
	channels := newNSArray(b, stripUIDs)
	nodeArchives := newNSArray(b, nodeArchUIDs)

	// --- midiCtrlState placeholder catalogue ---
	chanCollKeys := make([]any, 0, len(spec.Channels))
	chanCollObjs := make([]any, 0, len(spec.Channels))
	for i, ch := range spec.Channels {
		collKeys := []any{b.Intern("Channel controls")}
		collObjs := []any{b.Intern(buildChannelControls(b, ch, &report))}

		for slot, n := range ch.Nodes {
			collKeys = append(collKeys, b.Intern(fmt.Sprintf("slot%d", slot)))
			collObjs = append(collObjs, b.Intern(buildSlotCatalogue(b, n, &report)))
		}
		chanCollKeys = append(chanCollKeys, b.Intern(fmt.Sprintf("chan%d", i)))
		chanCollObjs = append(chanCollObjs, b.Intern(newNSDict(b, collKeys, collObjs)))
	}
	channelsColl := newNSDict(b, chanCollKeys, chanCollObjs)

	transport := buildTransport(b, &report)
	system := buildSystem(b, &report)
	midiCtrlState := newNSDict(b,
		[]any{b.Intern("Transport"), b.Intern("System"), b.Intern("Channels")},
		[]any{b.Intern(transport), b.Intern(system), b.Intern(channelsColl)},
	)

	clock := newNSDict(b, []any{b.Intern("clockTempo")}, []any{b.Intern(tempo)})

	rootFields := map[string]any{
		"version":             int64(13),
		"sampleRate":          sampleRate,
		"channels":            b.Intern(channels),
		"nodeArchives":        b.Intern(nodeArchives),
		"midiCtrlState":       b.Intern(midiCtrlState),
		"transportClockState": b.Intern(clock),
	}
	if spec.Title != "" {
		rootFields["title"] = b.Intern(spec.Title)
	}
	root := keyedObj(b, "AUMSession", "", rootFields)
	a.Top = map[string]any{"root": b.Intern(root)}

	s := NewSession(a)

	if spec.Convention != nil {
		if err := applyConvention(s, spec, masterPos, &report); err != nil {
			return nil, report, err
		}
	}

	// Author per-node saved state (AuStateDoc) and preset selection.
	for ci, ch := range spec.Channels {
		for slot, n := range ch.Nodes {
			if len(n.StateDoc) > 0 {
				if err := s.SetAuStateDoc(ci, slot, n.StateDoc); err != nil {
					return nil, report, err
				}
			}
			if n.Preset != nil {
				if err := s.SetPreset(ci, slot, *n.Preset); err != nil {
					return nil, report, err
				}
			}
		}
	}

	// Author the inter-node MIDI routing matrix.
	if len(spec.Routes) > 0 {
		if err := s.SetMIDIRoutes(spec.Routes); err != nil {
			return nil, report, err
		}
		report.Routes = len(spec.Routes)
	}

	return s, report, nil
}

// buildNode builds one AUMNodeArchive for a hosted AUv3 node: its component
// identity, human name, and an archiveNodeState carrying bypass state (and the
// headline param keyPath when supplied).
func buildNode(b *Builder, n NodeSpec, channel, slot int) map[string]any {
	stateKeys := []any{b.Intern("AUMNode.bypassed")}
	stateObjs := []any{b.Intern(false)}
	if n.AuMainParam != "" {
		stateKeys = append(stateKeys, b.Intern("AuMainParam"))
		stateObjs = append(stateObjs, b.Intern(n.AuMainParam))
	}
	nodeState := newNSDict(b, stateKeys, stateObjs)

	fields := map[string]any{
		"archiveDescClass":          b.Intern("AUXNodeDescription"),
		"audioComponentDescription": b.Intern(EncodeComponentDesc(n.Component)),
		"archiveNodeState":          b.Intern(nodeState),
		"parentChannel":             int64(channel),
		"parentSlot":                int64(slot),
	}
	if n.ComponentName != "" {
		fields["componentName"] = b.Intern(n.ComponentName)
	}
	return keyedObj(b, "AUMNodeArchive", "", fields)
}

// buildChannelControls builds the "Channel controls" collection for a strip:
// one placeholder leaf per strip-level target (Volume value, Mute/Solo/Rec
// triggers for audio; Mute/Solo for MIDI).
func buildChannelControls(b *Builder, ch ChannelSpec, report *BuildReport) map[string]any {
	controls := audioChannelControls
	if ch.Kind == KindMIDI {
		controls = midiChannelControls
	}
	keys := make([]any, 0, len(controls))
	objs := make([]any, 0, len(controls))
	for _, ctl := range controls {
		keys = append(keys, b.Intern(ctl.name))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	return newNSDict(b, keys, objs)
}

// buildSlotCatalogue builds a node slot's collection: a value placeholder per
// writable parameter (AUM only maps writable params) keyed by the param's
// target name, plus the reserved bypass/show triggers.
func buildSlotCatalogue(b *Builder, n NodeSpec, report *BuildReport) map[string]any {
	keys := []any{}
	objs := []any{}
	used := map[string]bool{}
	for _, p := range n.Params {
		if !p.Writable {
			continue
		}
		keys = append(keys, b.Intern(uniqueTarget(paramTarget(p), used)))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	for _, r := range nodeReservedTargets {
		keys = append(keys, b.Intern(r))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	return newNSDict(b, keys, objs)
}

// buildTransport builds the Transport collection: trigger placeholders for the
// standard actions plus the "Receive MMC" bool (a plain flag, not a leaf).
func buildTransport(b *Builder, report *BuildReport) map[string]any {
	keys := make([]any, 0, len(transportTargets)+1)
	objs := make([]any, 0, len(transportTargets)+1)
	for _, t := range transportTargets {
		keys = append(keys, b.Intern(t))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	keys = append(keys, b.Intern("Receive MMC"))
	objs = append(objs, b.Intern(false))
	return newNSDict(b, keys, objs)
}

// buildSystem builds the global System collection: trigger placeholders for the
// app-level actions AUM enumerates (Switch to AUM, Hide all plugins, Un-solo
// all). These are catalogued but not convention-wired.
func buildSystem(b *Builder, report *BuildReport) map[string]any {
	keys := make([]any, 0, len(systemTargets))
	objs := make([]any, 0, len(systemTargets))
	for _, t := range systemTargets {
		keys = append(keys, b.Intern(t))
		objs = append(objs, b.Intern(placeholderLeaf(b)))
		report.Targets++
	}
	return newNSDict(b, keys, objs)
}

// applyConvention assigns the server CC convention onto the freshly-built
// placeholder catalogue, in place, via the round-trip editor. Channel controls
// of the non-master audio strips (ordinal 1..8) take the AUM mixer CCs; each
// node's writable params take sequential CCs from NodeStartCC.
func applyConvention(s *Session, spec BuildSpec, masterPos int, report *BuildReport) error {
	conv := spec.Convention
	channel := conv.Channel
	if channel < 0 || channel > 16 {
		channel = 1
	}
	startCC := conv.NodeStartCC
	if startCC == 0 {
		startCC = 30
	}
	maxCC := conv.NodeMaxCC
	if maxCC == 0 {
		maxCC = 127
	}

	audioOrdinal := 0
	for i, ch := range spec.Channels {
		coll := fmt.Sprintf("Channels/chan%d", i)

		if ch.Kind == KindAudio && i != masterPos {
			audioOrdinal++
			for _, ctl := range audioChannelControls {
				cc, ok := conventionMixerCC(audioOrdinal, ctl.name)
				if !ok {
					continue
				}
				if err := s.SetMapping(coll+"/Channel controls", ctl.name, TypeCC, cc, channel); err != nil {
					return err
				}
				report.AssignedCCs++
			}
		}

		for slot, n := range ch.Nodes {
			slotColl := fmt.Sprintf("%s/slot%d", coll, slot)
			used := map[string]bool{}
			cc := startCC
			for _, p := range n.Params {
				if !p.Writable {
					continue
				}
				target := uniqueTarget(paramTarget(p), used)
				if cc > maxCC {
					report.Overflow = append(report.Overflow, slotColl+"/"+target)
					continue
				}
				if err := s.SetMapping(slotColl, target, TypeCC, cc, channel); err != nil {
					return err
				}
				report.AssignedCCs++
				cc++
			}
		}
	}

	// Global transport block: a session-wide control surface the brain drives
	// for play/stop/record/tempo. Wired once (not per channel) onto the
	// Transport collection's verified trigger targets.
	for _, t := range transportTargets {
		cc, ok := conventionTransportCC(t)
		if !ok {
			continue
		}
		if err := s.SetMapping("Transport", t, TypeCC, cc, channel); err != nil {
			return err
		}
		report.AssignedCCs++
	}
	return nil
}

// conventionMixerCC returns the AUM mixer-convention CC for a non-master audio
// strip's channel control. n is the 1-based audio-channel ordinal; ok is false
// outside the convention's documented 1..8 range (aum.yaml) or for a target
// with no mixer CC. Formulae mirror internal/device/definitions/aum.yaml.
func conventionMixerCC(n int, target string) (int, bool) {
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

// conventionTransportCC returns the brain-control convention CC for a global
// Transport-collection target. ok is false for targets the convention does not
// cover. The CC numbers and target mapping mirror docs/research/aum.md
// ("Transport / system") — the undefined-CC block (102-110) plus the single
// CC 20 "Toggle play". Only the targets buildTransport actually enumerates (and
// whose on-disk key strings are corpus-verified) are wired; Previous/Next bar,
// Tempo value and Metronome are part of the documented convention but their AUM
// key strings are not yet corpus-confirmed, so they are left for a capture (see
// the plan's Pillar 1 transport-catalogue row) and intentionally omitted here.
func conventionTransportCC(target string) (int, bool) {
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

// lastAudioIndex returns the index of the last KindAudio channel (the master),
// or -1 when there is none.
func lastAudioIndex(channels []ChannelSpec) int {
	idx := -1
	for i, ch := range channels {
		if ch.Kind == KindAudio {
			idx = i
		}
	}
	return idx
}

// paramTarget is the midiCtrlState target key for a parameter, preferring the
// stable AU identifier, then displayName, then keyPath, then a synthesized
// addr-based name (mirroring how AUM keys node params by name/identifier).
func paramTarget(p device.ProbeParam) string {
	switch {
	case p.Identifier != "":
		return p.Identifier
	case p.DisplayName != "":
		return p.DisplayName
	case p.KeyPath != "":
		return p.KeyPath
	default:
		return fmt.Sprintf("param_%d", p.Address)
	}
}

// uniqueTarget disambiguates a target key within one collection, suffixing
// _2, _3, … on collision so no two leaves share a key (NS dictionaries key by
// string). It records the chosen name in used.
func uniqueTarget(base string, used map[string]bool) string {
	if base == "" {
		base = "param"
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	used[name] = true
	return name
}
