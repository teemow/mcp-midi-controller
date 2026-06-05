package device

import "testing"

// ccByName indexes a device type's CC controls by name -> CC number for terse
// assertions.
func ccByName(d *DeviceType) map[string]int {
	m := map[string]int{}
	for i := range d.Controls {
		c := &d.Controls[i]
		if c.Type == ControlCC && c.CC != nil {
			m[c.Name] = *c.CC
		}
	}
	return m
}

func TestDeviceTypeFromProbeBasics(t *testing.T) {
	dump := ProbeDump{
		Component: ProbeComponent{Type: "aufx", Subtype: "test", Manufacturer: "acme"},
		Name:      "Test FX",
		Parameters: []ProbeParam{
			{Address: 1, Identifier: "drive", DisplayName: "Drive", Writable: true},
			{Address: 2, Identifier: "tone", DisplayName: "Tone", Writable: true},
			{Address: 3, Identifier: "meter", DisplayName: "Meter", Writable: false},
		},
	}
	def, report, err := DeviceTypeFromProbe(dump, ProbeOptions{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if def.Transport != "auv3midi" {
		t.Fatalf("transport = %q, want auv3midi", def.Transport)
	}
	if len(def.Controls) != 2 {
		t.Fatalf("controls = %d, want 2 (read-only skipped)", len(def.Controls))
	}
	if len(report.SkippedReadOnly) != 1 {
		t.Fatalf("skipped read-only = %d, want 1", len(report.SkippedReadOnly))
	}
	// First writable param takes the first non-reserved CC at/after StartCC=30.
	// CC 30..34 are all reserved (mixer Mute/Volume on n=4,5 + Bank Select 32),
	// so the first free CC is 35.
	cc := ccByName(def)
	if cc["drive"] != 35 {
		t.Fatalf("drive CC = %d, want 35", cc["drive"])
	}
}

func TestDeviceTypeFromProbeReservedCCsSkipped(t *testing.T) {
	// The generator must skip the reserved AUM mixer/transport + MIDI-reserved
	// band. From StartCC=30 the first two free CCs are 35 and 41 (30,33=Mute,
	// 31,34=Volume, 32=Bank Select, 36,37,39,40=mixer, 38=Data Entry LSB).
	dump := ProbeDump{
		Component: ProbeComponent{Subtype: "rsvd"},
		Parameters: []ProbeParam{
			{Address: 1, Identifier: "a", Writable: true},
			{Address: 2, Identifier: "b", Writable: true},
		},
	}
	def, _, err := DeviceTypeFromProbe(dump, ProbeOptions{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	cc := ccByName(def)
	if cc["a"] != 35 {
		t.Fatalf("a CC = %d, want 35", cc["a"])
	}
	if cc["b"] != 41 {
		t.Fatalf("b CC = %d, want 41", cc["b"])
	}
	// None of the assigned CCs may fall in the reserved set.
	reserved := ConventionReservedCCs()
	for name, n := range cc {
		if reserved[n] {
			t.Fatalf("control %q got reserved CC %d", name, n)
		}
	}
}

func TestDeviceTypeFromProbePresetEnum(t *testing.T) {
	dump := ProbeDump{
		Component: ProbeComponent{Subtype: "syn"},
		FactoryPresets: []ProbePreset{
			{Number: 0, Name: "Init"},
			{Number: 1, Name: "Lead"},
			{Number: -1, Name: "User Patch"}, // negative: not recallable by PC
		},
	}
	def, report, err := DeviceTypeFromProbe(dump, ProbeOptions{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if report.PresetControl != "preset" || report.Presets != 2 || report.BankSelect {
		t.Fatalf("preset report = %+v, want name=preset count=2 bank=false", report)
	}
	pc, ok := def.Control("preset")
	if !ok {
		t.Fatal("missing preset control")
	}
	if pc.Type != ControlProgramChange {
		t.Fatalf("preset type = %q, want program_change", pc.Type)
	}
	if pc.Bank {
		t.Fatal("preset must not be banked for 2 presets")
	}
	if pc.Value.Type != ValueEnum {
		t.Fatalf("preset value type = %q, want enum", pc.Value.Type)
	}
	if pc.Value.Values["Lead"] != 1 {
		t.Fatalf("enum Lead = %d, want 1", pc.Value.Values["Lead"])
	}
	// The preset control is first (preset-first).
	if def.Controls[0].Name != "preset" {
		t.Fatalf("first control = %q, want preset", def.Controls[0].Name)
	}
}

func TestDeviceTypeFromProbePresetBankSelect(t *testing.T) {
	// 200 factory presets => recall needs Bank Select; >PresetEnumMax(default
	// 128) => range, not enum.
	var presets []ProbePreset
	for i := 0; i < 200; i++ {
		presets = append(presets, ProbePreset{Number: i, Name: ""})
	}
	dump := ProbeDump{Component: ProbeComponent{Subtype: "big"}, FactoryPresets: presets}
	def, report, err := DeviceTypeFromProbe(dump, ProbeOptions{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !report.BankSelect {
		t.Fatal("expected BankSelect for 200 presets")
	}
	pc, _ := def.Control("preset")
	if !pc.Bank {
		t.Fatal("preset control must be banked")
	}
	if pc.Value.Type != ValueRange || pc.Value.Max == nil || *pc.Value.Max != 199 {
		t.Fatalf("preset value = %+v, want range max=199", pc.Value)
	}
}

func TestDeviceTypeFromProbeCurationBudgetAndGroups(t *testing.T) {
	// A small budget forces curation: with explicit selection + group
	// representatives prioritized, every group is covered before the budget runs
	// out and the rest overflow.
	dump := ProbeDump{
		Component: ProbeComponent{Subtype: "grp"},
		Parameters: []ProbeParam{
			{Address: 1, Identifier: "a1", Group: "A", Writable: true},
			{Address: 2, Identifier: "a2", Group: "A", Writable: true},
			{Address: 3, Identifier: "b1", Group: "B", Writable: true},
			{Address: 4, Identifier: "explicit", Group: "C", Writable: true},
		},
	}
	// Budget of 3 CCs (StartCC..MaxCC with the band already excluded). Use a
	// narrow, reservation-free window: CC 70..72 are not in the reserved set.
	def, report, err := DeviceTypeFromProbe(dump, ProbeOptions{
		StartCC: 70, MaxCC: 72, Select: []string{"explicit"},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	cc := ccByName(def)
	// tier1 explicit first, then one representative per remaining group (A, B).
	if _, ok := cc["explicit"]; !ok {
		t.Fatalf("explicit param must be mapped, got %v", cc)
	}
	if _, ok := cc["a1"]; !ok {
		t.Fatalf("group A representative (a1) must be mapped, got %v", cc)
	}
	if _, ok := cc["b1"]; !ok {
		t.Fatalf("group B representative (b1) must be mapped, got %v", cc)
	}
	// a2 (second member of group A) overflows the 3-CC budget.
	if len(report.Overflow) != 1 || report.Overflow[0].Identifier != "a2" {
		t.Fatalf("overflow = %+v, want [a2]", report.Overflow)
	}
}

func TestDeviceTypeFromProbeMacroCollapse(t *testing.T) {
	dump := ProbeDump{
		Component: ProbeComponent{Subtype: "mac"},
		Parameters: []ProbeParam{
			{Address: 10, Identifier: "macro", Writable: true, DependentParameters: []uint64{11, 12}},
			{Address: 11, Identifier: "derived1", Writable: true},
			{Address: 12, Identifier: "derived2", Writable: true},
		},
	}
	def, report, err := DeviceTypeFromProbe(dump, ProbeOptions{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(def.Controls) != 1 || def.Controls[0].Name != "macro" {
		t.Fatalf("controls = %+v, want only the macro", def.Controls)
	}
	if len(report.DerivedSkipped) != 2 {
		t.Fatalf("derived skipped = %d, want 2", len(report.DerivedSkipped))
	}
	if len(report.MacroControls) != 1 || report.MacroControls[0] != "macro" {
		t.Fatalf("macro controls = %v, want [macro]", report.MacroControls)
	}
}
