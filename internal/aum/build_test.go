package aum

import (
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

// buildTestSpec returns a representative authoring spec: two audio strips
// (the second is the master) plus a MIDI strip, with the first audio strip
// hosting one AUv3 node carrying two writable params and one read-only param.
func buildTestSpec() BuildSpec {
	fader := 0.7
	return BuildSpec{
		Title:      "Authored",
		Tempo:      128,
		SampleRate: 44100,
		Channels: []ChannelSpec{
			{
				Kind:  KindAudio,
				Title: "Synth",
				Fader: &fader,
				Nodes: []NodeSpec{
					{
						Component:     device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"},
						ComponentName: "Arturia: iSEM",
						AuMainParam:   "cutoff",
						Params: []device.ProbeParam{
							{Identifier: "cutoff", DisplayName: "Cutoff", Writable: true},
							{Identifier: "resonance", DisplayName: "Resonance", Writable: true},
							{Identifier: "meter", DisplayName: "Meter", Writable: false},
						},
					},
				},
			},
			{Kind: KindAudio, Title: "Master"},
			{Kind: KindMIDI, Title: "Keys In"},
		},
	}
}

// TestBuildSessionLayout authors a session and verifies the read model sees the
// authored channels, the hosted node's component identity, and a placeholder
// catalogue (no assigned mappings, since no convention was applied).
func TestBuildSessionLayout(t *testing.T) {
	s, report, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if report.Channels != 3 || report.Nodes != 1 {
		t.Fatalf("report channels/nodes = %d/%d, want 3/1", report.Channels, report.Nodes)
	}
	if report.AssignedCCs != 0 {
		t.Fatalf("AssignedCCs = %d, want 0 (no convention)", report.AssignedCCs)
	}

	if s.Version() != 13 || s.Encoding() != EncodingSpecState {
		t.Fatalf("version/encoding = %d/%v, want 13/specState", s.Version(), s.Encoding())
	}
	if s.Tempo() != 128 {
		t.Fatalf("tempo = %v, want 128", s.Tempo())
	}

	chans := s.Channels()
	if len(chans) != 3 {
		t.Fatalf("channels = %d, want 3", len(chans))
	}
	if chans[0].Kind != KindAudio || chans[1].Kind != KindAudio || chans[2].Kind != KindMIDI {
		t.Fatalf("kinds = %v/%v/%v", chans[0].Kind, chans[1].Kind, chans[2].Kind)
	}
	if chans[0].FaderLevel == nil || *chans[0].FaderLevel != 0.7 {
		t.Fatalf("chan0 fader = %v, want 0.7", chans[0].FaderLevel)
	}
	if chans[2].FaderLevel != nil {
		t.Fatalf("MIDI strip should have no fader")
	}
	if chans[0].Title != "Synth" {
		t.Fatalf("chan0 title = %q", chans[0].Title)
	}

	if len(chans[0].Nodes) != 1 {
		t.Fatalf("chan0 nodes = %d, want 1", len(chans[0].Nodes))
	}
	node := chans[0].Nodes[0]
	want := device.ProbeComponent{Type: "aumu", Subtype: "iSEM", Manufacturer: "Artu"}
	if node.Component == nil || !ComponentMatches(*node.Component, want) {
		t.Fatalf("node component = %+v, want %+v", node.Component, want)
	}
	if node.ComponentName != "Arturia: iSEM" {
		t.Fatalf("componentName = %q", node.ComponentName)
	}
	if node.AuMainParam != "cutoff" {
		t.Fatalf("AuMainParam = %q, want cutoff", node.AuMainParam)
	}

	// No convention → everything is an unassigned placeholder.
	if assigned := s.Mappings(false); len(assigned) != 0 {
		t.Fatalf("assigned mappings = %d, want 0", len(assigned))
	}
	all := s.Mappings(true)
	// The catalogue includes the strip controls and only the writable params.
	if _, ok := findMapping(all, "Channels/chan0/Channel controls", "Volume"); !ok {
		t.Fatalf("catalogue missing chan0 Volume")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "cutoff"); !ok {
		t.Fatalf("catalogue missing node cutoff param")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "resonance"); !ok {
		t.Fatalf("catalogue missing node resonance param")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "meter"); ok {
		t.Fatalf("read-only param should not be a mappable target")
	}
	if _, ok := findMapping(all, "Channels/chan0/slot0", "_AUMNode:Bypass"); !ok {
		t.Fatalf("catalogue missing reserved bypass target")
	}
	// The MIDI strip has mute/solo but no Volume or Rec enable.
	if _, ok := findMapping(all, "Channels/chan2/Channel controls", "Mute"); !ok {
		t.Fatalf("catalogue missing MIDI strip Mute")
	}
	if _, ok := findMapping(all, "Channels/chan2/Channel controls", "Volume"); ok {
		t.Fatalf("MIDI strip should have no Volume target")
	}
}

// TestBuildSessionRoundTrip encodes an authored session and re-opens it from
// bytes, asserting graph-equal stability across a further encode (the writer
// invariant) and that the read model survives the round-trip.
func TestBuildSessionRoundTrip(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got.Version() != 13 || len(got.Channels()) != 3 {
		t.Fatalf("re-opened session lost structure: v=%d channels=%d", got.Version(), len(got.Channels()))
	}

	data2, err := got.Archive().Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	got2, err := Decode(data2)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !GraphEqual(got.Archive(), got2) {
		t.Fatalf("authored archive is not stable across a round-trip")
	}
}

// TestBuildSessionConvention authors a session pre-wired to the CC convention
// and verifies the mixer CCs land on the first (non-master) audio strip, the
// master and MIDI strip are left as placeholders, and the node params take
// sequential CCs from the start CC.
func TestBuildSessionConvention(t *testing.T) {
	spec := buildTestSpec()
	spec.Convention = &Convention{Channel: 3}

	s, report, err := BuildSession(spec)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}

	// chan0 mixer block (audio ordinal 1) + two node params + the 6-target
	// global transport block = 12 assigned CCs.
	if report.AssignedCCs != 12 {
		t.Fatalf("AssignedCCs = %d, want 12", report.AssignedCCs)
	}
	if len(report.Overflow) != 0 {
		t.Fatalf("unexpected overflow: %v", report.Overflow)
	}

	// Mixer convention on chan0: Mute=21 Volume=22 Solo=45 Rec=53, channel 3.
	mixer := map[string]int{"Mute": 21, "Volume": 22, "Solo": 45, "Rec enable": 53}
	for target, wantCC := range mixer {
		m, ok := s.FindMapping("Channels/chan0/Channel controls", target)
		if !ok {
			t.Fatalf("missing chan0 %s", target)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != wantCC || m.Spec.Channel != 3 {
			t.Fatalf("chan0 %s spec = %+v, want CC %d ch 3", target, m.Spec, wantCC)
		}
	}

	// Node params took CC 30 and 31 in parameter order.
	cutoff, _ := s.FindMapping("Channels/chan0/slot0", "cutoff")
	if !cutoff.Spec.Enabled || cutoff.Spec.Data1 != 30 {
		t.Fatalf("cutoff spec = %+v, want CC 30", cutoff.Spec)
	}
	reso, _ := s.FindMapping("Channels/chan0/slot0", "resonance")
	if !reso.Spec.Enabled || reso.Spec.Data1 != 31 {
		t.Fatalf("resonance spec = %+v, want CC 31", reso.Spec)
	}

	// The master (chan1) and MIDI strip (chan2) keep placeholders.
	if m, ok := s.FindMapping("Channels/chan1/Channel controls", "Volume"); !ok || m.Spec.Enabled {
		t.Fatalf("master Volume should remain an unassigned placeholder: %+v (ok=%v)", m.Spec, ok)
	}
	if m, ok := s.FindMapping("Channels/chan2/Channel controls", "Mute"); !ok || m.Spec.Enabled {
		t.Fatalf("MIDI strip Mute should remain a placeholder: %+v (ok=%v)", m.Spec, ok)
	}

	// The transport block is wired on the Transport collection (CC 20 + 102-105
	// + 108), on the convention channel.
	transport := map[string]int{
		"Toggle Play": 20, "Start Play": 102, "Stop/Rewind": 103,
		"Rewind": 104, "Toggle Record": 105, "Tap Tempo": 108,
	}
	for target, wantCC := range transport {
		m, ok := s.FindMapping("Transport", target)
		if !ok {
			t.Fatalf("missing transport %s", target)
		}
		if !m.Spec.Enabled || m.Spec.Type != TypeCC || m.Spec.Data1 != wantCC || m.Spec.Channel != 3 {
			t.Fatalf("transport %s spec = %+v, want CC %d ch 3", target, m.Spec, wantCC)
		}
	}

	// The flat SessionMap now reports exactly the assigned mappings.
	if got := len(s.Map().Mappings); got != 12 {
		t.Fatalf("SessionMap mappings = %d, want 12", got)
	}

	// And it still round-trips graph-equal.
	data, err := s.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	reopened, err := Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got := len(reopened.Map().Mappings); got != 12 {
		t.Fatalf("re-opened SessionMap mappings = %d, want 12", got)
	}
}

// TestBuildSessionCatalogueFindings asserts the authored placeholder catalogue
// reflects the confirmed AUM key strings and specState encoding: the corrected
// node show/front keys, the extended transport surface, the global System
// actions, the per-channel ScrollToChannel target, and that placeholders are
// the confirmed specState shape (type 0, enabled false).
func TestBuildSessionCatalogueFindings(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	all := s.Mappings(true)

	present := []struct{ coll, target string }{
		{"Channels/chan0/slot0", "_AUMNode:FrontPlugin"},
		{"Channels/chan0/slot0", "_AUMNode:TogglePlugin"},
		{"Transport", "Previous bar"},
		{"Transport", "Next bar"},
		{"Transport", "Tempo"},
		{"Transport", "Metronome on/off"},
		{"System", "_AUM:ShowSelf"},
		{"System", "_AUM:HideAllPlugins"},
		{"System", "_AUM:UnSoloAll"},
		{"Channels/chan0/Channel controls", "ScrollToChannel"},
		{"Channels/chan2/Channel controls", "ScrollToChannel"},
	}
	for _, p := range present {
		if _, ok := findMapping(all, p.coll, p.target); !ok {
			t.Fatalf("catalogue missing %s / %s", p.coll, p.target)
		}
	}

	// The bogus pre-capture key must be gone.
	if _, ok := findMapping(all, "Channels/chan0/slot0", "_AUMNode:ShowPlugin"); ok {
		t.Fatalf("catalogue still has the non-existent _AUMNode:ShowPlugin key")
	}

	// A placeholder leaf is the confirmed specState shape: type 0, enabled false.
	ph, ok := findMapping(all, "Transport", "Previous bar")
	if !ok {
		t.Fatalf("missing placeholder under Transport")
	}
	if ph.Spec.Enabled || ph.Spec.Type != SpecStateTypeCC || ph.Spec.Encoding != EncodingSpecState {
		t.Fatalf("placeholder spec = %+v, want {type 0, enabled false, specState}", ph.Spec)
	}
}

// TestBuildSessionProgramChange authors a Program Change mapping onto a node's
// preset-load handle and verifies the confirmed specState PC code round-trips
// with the right label — the capability unblocked by confirming PC = 2.
func TestBuildSessionProgramChange(t *testing.T) {
	s, _, err := BuildSession(buildTestSpec())
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	// Bypass is a reserved trigger target on the node slot; assign a PC to it.
	if err := s.SetMapping("Channels/chan0/slot0", "_AUMNode:Bypass", SpecStateTypePC, 5, 1); err != nil {
		t.Fatalf("SetMapping PC: %v", err)
	}
	m, ok := s.FindMapping("Channels/chan0/slot0", "_AUMNode:Bypass")
	if !ok {
		t.Fatalf("mapping not found after assign")
	}
	if !m.Spec.Enabled || m.Spec.Type != SpecStateTypePC || m.Spec.Data1 != 5 {
		t.Fatalf("PC mapping = %+v, want enabled PC data1 5", m.Spec)
	}
	if m.Spec.TypeName() != "PC" {
		t.Fatalf("TypeName = %q, want PC", m.Spec.TypeName())
	}
}

// TestNodeSpecFromDump checks the probe-dump → NodeSpec convenience: the
// component tuple, the manufacturer-prefixed name, and the parameters carry
// over.
func TestNodeSpecFromDump(t *testing.T) {
	dump := device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: "dist", Manufacturer: "ACME", ManufacturerName: "Acme Audio"},
		Name:      "Crusher",
		Parameters: []device.ProbeParam{
			{Identifier: "drive", Writable: true},
		},
	}
	n := NodeSpecFromDump(dump)
	if !ComponentMatches(n.Component, dump.Component) {
		t.Fatalf("component = %+v", n.Component)
	}
	if n.ComponentName != "Acme Audio: Crusher" {
		t.Fatalf("componentName = %q, want %q", n.ComponentName, "Acme Audio: Crusher")
	}
	if len(n.Params) != 1 || n.Params[0].Identifier != "drive" {
		t.Fatalf("params not carried over: %+v", n.Params)
	}
}
