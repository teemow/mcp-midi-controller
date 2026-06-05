package device

import (
	"fmt"
	"sort"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/sanitize"
)

// This file turns an AUv3 parameter-tree dump (produced by the off-daemon
// cmd/auv3-probe utility, see docs/research/auv3-feedback.md) into a device
// DeviceType draft and diffs a dump against an existing device type. Because AUM
// does not echo MIDI, the parameter tree is the only truth-source for verifying
// that a plugin's YAML is correct and covers the plugin's maximum controllable
// functionality. Enumeration is instance-independent, so any instance of the
// plugin yields the same tree as the one AUM is hosting.

// ProbeComponent identifies the AudioUnit component the dump came from. The
// fields mirror AudioComponentDescription (type/subtype/manufacturer are FourCC
// codes rendered as strings, e.g. "aumu"/"aufx").
//
// ManufacturerName and Version are richer, human-readable metadata the iPad app
// reads from AVAudioUnitComponent (they are optional so older dumps without them
// still decode).
type ProbeComponent struct {
	Type             string `json:"type"`
	Subtype          string `json:"subtype"`
	Manufacturer     string `json:"manufacturer"`
	ManufacturerName string `json:"manufacturerName,omitempty"`
	Version          string `json:"version,omitempty"`
}

// ProbeParam is one AUParameter as read from auAudioUnit.parameterTree. The
// JSON tags match the dump shape documented in docs/research/auv3-feedback.md.
//
// The Min/Max/Value fields are always finite: the iPad app sanitizes the AU's
// non-finite values (±Inf, NaN — common for unbounded gain/log-scaled params)
// to finite sentinels before encoding, because neither JSON nor Go's
// encoding/json can represent them. NonFinite records that this happened so the
// real bound is not silently mistaken for a literal value.
//
// Flags is the raw AUParameter flag bitfield; the decoded booleans below
// surface the ones useful for authoring (e.g. logarithmic display, high-res).
// Group is the displayName of the parameter's parent AUParameterGroup, so the
// (flattened) tree's hierarchy is not lost. All of these are optional.
type ProbeParam struct {
	Address      uint64   `json:"address"`
	KeyPath      string   `json:"keyPath"`
	Identifier   string   `json:"identifier"`
	DisplayName  string   `json:"displayName"`
	Min          float64  `json:"min"`
	Max          float64  `json:"max"`
	Value        float64  `json:"value"`
	Unit         string   `json:"unit"`
	UnitName     string   `json:"unitName"`
	ValueStrings []string `json:"valueStrings"`
	Writable     bool     `json:"writable"`
	Readable     bool     `json:"readable"`

	// Optional richer metadata (added 2026-06; absent in older dumps).
	Group              string `json:"group,omitempty"`
	Flags              uint32 `json:"flags,omitempty"`
	DisplayLogarithmic bool   `json:"displayLogarithmic,omitempty"`
	DisplayExponential bool   `json:"displayExponential,omitempty"`
	IsHighResolution   bool   `json:"isHighResolution,omitempty"`
	IsRampable         bool   `json:"isRampable,omitempty"`
	IsMeta             bool   `json:"isMeta,omitempty"`
	// DependentParameters lists the addresses of parameters whose value is
	// derived from this one (AUParameter.dependentParameters). A non-empty list
	// marks this as a meta/macro control: changing it moves the listed params,
	// so the authoring side knows not to also map them independently.
	DependentParameters []uint64 `json:"dependentParameters,omitempty"`
	// NonFinite is set when the AU reported a non-finite min/max/value that was
	// clamped to a finite sentinel for transport (e.g. "max=+inf").
	NonFinite string `json:"nonFinite,omitempty"`
}

// ProbePreset is one preset exposed by the AudioUnit (name + number). Factory
// presets carry numbers >= 0; user presets carry negative numbers (the AU
// convention). The number is what a Program Change recalls when AUM maps PC to
// the plugin node's preset, so it is the handle a scene uses to recall a preset
// by name.
type ProbePreset struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
}

// ProbeDump is the full parameter-tree dump for one plugin. The fields after
// Parameters are optional richer metadata (added 2026-06) and decode to their
// zero value for older dumps that predate them.
type ProbeDump struct {
	Component  ProbeComponent `json:"component"`
	Name       string         `json:"name"`
	Parameters []ProbeParam   `json:"parameters"`

	ShortName      string        `json:"shortName,omitempty"`
	FactoryPresets []ProbePreset `json:"factoryPresets,omitempty"`
	// UserPresets are the user-saved presets (auAudioUnit.userPresets). Like
	// factory presets they are recallable by Program Change through AUM, so they
	// are first-class scene material (an agent can recall "my Lead patch" by its
	// number). Their names are installation-specific, so they only ever live in
	// the gitignored state dir / user config — never in committed artifacts.
	UserPresets []ProbePreset `json:"userPresets,omitempty"`

	// ChannelCapabilities mirrors auAudioUnit.channelCapabilities: a flat list
	// of [in, out] count pairs the unit supports, where -1 means "any". It tells
	// the authoring side whether a plugin is mono/stereo/multi-out.
	ChannelCapabilities []int `json:"channelCapabilities,omitempty"`
	// Latency / TailTime are auAudioUnit.latency / .tailTime in seconds. Latency
	// is the processing delay (relevant for the open-loop control posture);
	// TailTime is how long the unit keeps producing output after input stops
	// (reverbs/delays). Optional; 0 (the common case) is omitted.
	Latency  float64 `json:"latency,omitempty"`
	TailTime float64 `json:"tailTime,omitempty"`
	// SupportsUserPresets is auAudioUnit.supportsUserPresets — whether the unit
	// can persist user presets. We deliberately do NOT dump userPresets contents
	// (names are user/installation state; see public-vs-private rule).
	SupportsUserPresets bool `json:"supportsUserPresets,omitempty"`
}

// ProbeID derives the sanitized device-type/file id for a dump (from the
// component subtype, falling back to the name). The off-daemon receiver names
// staged dumps <ProbeID>.json so import_auv3_probe and DeviceTypeFromProbe
// agree on the id.
func ProbeID(dump ProbeDump) string {
	return sanitizeName(FirstNonEmpty(dump.Component.Subtype, dump.Name))
}

// label returns the most human-friendly identifier for a parameter, preferring
// the AU identifier (stable), then displayName, then keyPath, then the address.
func (p ProbeParam) label() string {
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

// ProbeOptions tunes DeviceTypeFromProbe. Zero value uses the project defaults
// (CC convention starting at 30, capping at 127, enum for <=8 valueStrings,
// preset enum up to one PC bank).
type ProbeOptions struct {
	// ID overrides the derived device-type id (otherwise from Component.Subtype
	// or Name, sanitized).
	ID string
	// Name overrides the derived device-type name (otherwise the dump Name).
	Name string
	// StartCC is the first convention CC assigned (default 30, per
	// docs/research/auv3-plugins.md).
	StartCC int
	// MaxCC is the last usable CC (default 127). Params that would exceed the
	// curated CC budget are reported in ProbeBuildReport.Overflow rather than
	// dropped silently.
	MaxCC int
	// EnumMax is the largest valueStrings count still modeled as an enum control
	// (default 8). Above it the param becomes a plain range control.
	EnumMax int
	// Select lists params (matched by identifier, displayName, or keyPath) to
	// map first — ahead of group representatives and other writable params —
	// when the writable-param count exceeds the curated CC budget. Empty means
	// "no explicit curation": fall back to one representative per group, then
	// remaining non-meta params, then meta params, in parameter-tree order.
	Select []string
	// PresetEnumMax is the largest recallable-preset count still modeled as an
	// enum (preset name -> program number) on the preset control (default 128,
	// one Program Change bank). Above it the preset control is a plain range
	// 0..maxNumber recalled by number.
	PresetEnumMax int
}

func (o ProbeOptions) withDefaults() ProbeOptions {
	if o.StartCC == 0 {
		o.StartCC = 30
	}
	if o.MaxCC == 0 {
		o.MaxCC = 127
	}
	if o.EnumMax == 0 {
		o.EnumMax = 8
	}
	if o.PresetEnumMax == 0 {
		o.PresetEnumMax = 128
	}
	return o
}

// ProbeBuildReport accompanies a generated DeviceType: it records the writable
// params that did not fit the curated CC budget (so a human can curate them onto
// a second channel/file), the read-only params that were skipped (not
// AUM-mappable), and the preset control that was synthesized.
type ProbeBuildReport struct {
	// Overflow lists writable params that did not fit the curated CC budget
	// (start..max minus the reserved AUM convention band). They are reported,
	// not dropped, so coverage gaps are explicit.
	Overflow []ProbeParam
	// SkippedReadOnly lists params that are not writable; AUM can only map
	// writable params, so these get no control.
	SkippedReadOnly []ProbeParam
	// MacroControls names the generated controls whose param is a meta/macro
	// (has dependentParameters): mapping the macro's CC moves several other
	// params, so a human/agent should map the macro and not separately fight its
	// derived params.
	MacroControls []string
	// DerivedSkipped lists writable params that are driven by a macro/meta
	// param (their address appears in another param's dependentParameters) and
	// are not themselves a macro. They get no independent CC because the macro
	// moves them; they are reported (not silently dropped) so a human can
	// curate any they still want mapped directly.
	DerivedSkipped []ProbeParam
	// PresetControl is the name of the generated preset (Program Change) control,
	// empty when the dump exposes no recallable (number >= 0) presets.
	PresetControl string
	// Presets is the number of recallable presets folded into the preset control.
	Presets int
	// BankSelect reports that the preset control spans more than 128 presets, so
	// recall emits Bank Select (CC 0/32) before the Program Change.
	BankSelect bool
}

// DeviceTypeFromProbe builds a device.DeviceType straight from a parameter-tree
// dump (a probe dump is a throwaway intermediary, not a standing concept). The
// transport is auv3midi — the LAN channel into AUM that drives the hosted AUv3
// plugin.
//
// The generator is preset-first and bounded:
//
//   - Preset-first: recallable presets (number >= 0) become the primary control,
//     a single Program Change ("preset"). For big synths preset recall dominates
//     per-param tweaking. More than 128 presets are addressed by Bank Select +
//     Program Change (Control.Bank).
//   - Curated CC subset within budget: a single MIDI channel has only 128 CCs,
//     and the AUM mixer/transport convention already claims a band of them, so
//     writable params cannot all be mapped for the larger plugins. Params are
//     selected by priority (explicit opts.Select > one representative per group >
//     remaining non-meta params > meta params) and assigned the available CCs in
//     opts.StartCC..opts.MaxCC, skipping the reserved convention band. Params
//     beyond the budget are reported in ProbeBuildReport.Overflow, not dropped.
//
// Read-only params are skipped (AUM can only map writable params); macro/meta
// params keep their CC while the params they drive are skipped (the macro's CC
// moves them). Small indexed params (valueStrings, <=opts.EnumMax) become enum
// controls; the AU displayName/range/unit/valueStrings are recorded in the
// control Description. The returned device type is validated.
func DeviceTypeFromProbe(dump ProbeDump, opts ProbeOptions) (*DeviceType, ProbeBuildReport, error) {
	opts = opts.withDefaults()
	var report ProbeBuildReport

	id := opts.ID
	if id == "" {
		id = ProbeID(dump)
	}
	if id == "" {
		return nil, report, fmt.Errorf("auv3 probe: cannot derive a device-type id (empty subtype and name)")
	}
	name := FirstNonEmpty(opts.Name, dump.Name, id)

	def := &DeviceType{
		ID:           id,
		Name:         name,
		Manufacturer: dump.Component.Manufacturer,
		Description:  fmt.Sprintf("Generated from an AUv3 parameter-tree probe (component %s/%s).", dump.Component.Type, dump.Component.Subtype),
		Transport:    "auv3midi",
	}

	usedNames := map[string]bool{}

	// Preset-first: synthesize the preset control before the per-param CCs so it
	// reads as the device's primary control.
	if pc, count, ok := presetControl(dump, opts); ok {
		usedNames[pc.Name] = true
		def.Controls = append(def.Controls, pc)
		report.PresetControl = pc.Name
		report.Presets = count
		report.BankSelect = pc.Bank
	}

	// A macro/meta param drives the params it lists in dependentParameters.
	// Pre-compute that driven set so each derived param is skipped (the macro's
	// CC already moves it) rather than consuming its own convention CC.
	derived := map[uint64]bool{}
	for _, p := range dump.Parameters {
		for _, a := range p.DependentParameters {
			derived[a] = true
		}
	}

	// Build the candidate set: writable params that are not driven by a macro.
	var candidates []ProbeParam
	for _, p := range dump.Parameters {
		if !p.Writable {
			report.SkippedReadOnly = append(report.SkippedReadOnly, p)
			continue
		}
		// Skip params driven by a macro (but keep a param that is itself a
		// macro, even if some macro also drives it).
		if derived[p.Address] && len(p.DependentParameters) == 0 {
			report.DerivedSkipped = append(report.DerivedSkipped, p)
			continue
		}
		candidates = append(candidates, p)
	}

	// Curate the candidates into a priority order, then assign them the CCs that
	// are free in the convention band. Anything past the budget is overflow.
	ordered := curateParams(candidates, opts)
	ccs := availableCCs(opts.StartCC, opts.MaxCC)
	for i, p := range ordered {
		if i >= len(ccs) {
			report.Overflow = append(report.Overflow, p)
			continue
		}
		cc := ccs[i]
		c := Control{
			Name:        UniqueName(sanitizeName(p.label()), usedNames),
			Description: probeParamDescription(p),
			Type:        ControlCC,
			CC:          &cc,
		}
		if n := len(p.ValueStrings); n > 0 && n <= opts.EnumMax {
			c.Value = ValueSpec{Type: ValueEnum, Values: enumValues(p.ValueStrings)}
		} else {
			c.Value = ValueSpec{Type: ValueRange}
		}
		if len(p.DependentParameters) > 0 {
			report.MacroControls = append(report.MacroControls, c.Name)
		}
		def.Controls = append(def.Controls, c)
	}

	if err := def.Validate(); err != nil {
		return nil, report, fmt.Errorf("auv3 probe: generated device type is invalid: %w", err)
	}
	return def, report, nil
}

// presetControl synthesizes the preset (Program Change) control from a dump's
// factory + user presets. Only presets with a non-negative number are recallable
// (a Program Change byte is unsigned; the AU convention numbers user presets
// negatively). The control is an enum (name -> program number) when the count
// fits opts.PresetEnumMax and every preset is named, so an agent can recall a
// preset by name; otherwise it is a plain range 0..maxNumber recalled by number.
// When the highest number exceeds 127 the control is banked (Control.Bank), so
// recall emits Bank Select + Program Change. ok is false when no preset is
// recallable.
func presetControl(dump ProbeDump, opts ProbeOptions) (Control, int, bool) {
	type preset struct {
		num  int
		name string
	}
	seen := map[int]bool{}
	var list []preset
	add := func(ps []ProbePreset) {
		for _, p := range ps {
			if p.Number < 0 || seen[p.Number] {
				continue
			}
			seen[p.Number] = true
			list = append(list, preset{num: p.Number, name: strings.TrimSpace(p.Name)})
		}
	}
	add(dump.FactoryPresets)
	add(dump.UserPresets)
	if len(list) == 0 {
		return Control{}, 0, false
	}
	sort.Slice(list, func(i, j int) bool { return list[i].num < list[j].num })
	maxNum := list[len(list)-1].num

	c := Control{
		Name:        "preset",
		Description: fmt.Sprintf("Recall a preset by Program Change (%d preset(s), numbers 0..%d).", len(list), maxNum),
		Type:        ControlProgramChange,
		Bank:        maxNum > 127,
	}

	named := true
	for _, p := range list {
		if p.name == "" {
			named = false
			break
		}
	}
	if len(list) <= opts.PresetEnumMax && named {
		values := make(map[string]int, len(list))
		used := map[string]bool{}
		for _, p := range list {
			values[UniqueName(p.name, used)] = p.num
		}
		c.Value = ValueSpec{Type: ValueEnum, Values: values}
	} else {
		lo, hi := float64(0), float64(maxNum)
		c.Value = ValueSpec{Type: ValueRange, Min: &lo, Max: &hi}
	}
	return c, len(list), true
}

// availableCCs lists the CCs in start..max that a probe-derived param may take,
// skipping the reserved convention band (ConventionReservedCCs) so a node
// parameter never collides with the AUM mixer/transport CCs or a MIDI-reserved
// controller. The result is the curated CC budget.
func availableCCs(start, max int) []int {
	reserved := ConventionReservedCCs()
	var out []int
	for cc := start; cc <= max; cc++ {
		if reserved[cc] {
			continue
		}
		out = append(out, cc)
	}
	return out
}

// curateParams orders candidate params by priority so that, when the CC budget
// is smaller than the candidate count, the most useful params are mapped first:
//
//  1. explicit selection (opts.Select, matched by identifier/displayName/keyPath)
//  2. one representative per parameter group (so coverage spans the groups
//     instead of exhausting the budget within a single group)
//  3. remaining non-meta params, in parameter-tree order
//  4. remaining meta params, in parameter-tree order (lowest priority)
//
// Within every tier parameter-tree order is preserved, so the output is stable.
func curateParams(candidates []ProbeParam, opts ProbeOptions) []ProbeParam {
	sel := map[string]bool{}
	for _, s := range opts.Select {
		if k := sanitizeName(s); k != "" {
			sel[k] = true
		}
	}
	isSelected := func(p ProbeParam) bool {
		for _, k := range matchKeys(p) {
			if sel[k] {
				return true
			}
		}
		return false
	}

	placed := make([]bool, len(candidates))
	var tier1, tier2, tier3, tier4 []ProbeParam

	if len(sel) > 0 {
		for i, p := range candidates {
			if isSelected(p) {
				tier1 = append(tier1, p)
				placed[i] = true
			}
		}
	}

	groupSeen := map[string]bool{}
	for i, p := range candidates {
		if placed[i] {
			if p.Group != "" {
				groupSeen[p.Group] = true // an explicit pick already represents its group
			}
			continue
		}
		if p.Group == "" || groupSeen[p.Group] {
			continue
		}
		groupSeen[p.Group] = true
		tier2 = append(tier2, p)
		placed[i] = true
	}

	for i, p := range candidates {
		if placed[i] || p.IsMeta {
			continue
		}
		tier3 = append(tier3, p)
		placed[i] = true
	}
	for i, p := range candidates {
		if placed[i] {
			continue
		}
		tier4 = append(tier4, p)
	}

	out := make([]ProbeParam, 0, len(candidates))
	out = append(out, tier1...)
	out = append(out, tier2...)
	out = append(out, tier3...)
	out = append(out, tier4...)
	return out
}

// probeParamDescription renders the AU metadata we keep alongside a control so
// the convention CC stays the wire value while the real range/unit/enum is
// recorded for humans and the diff.
func probeParamDescription(p ProbeParam) string {
	parts := []string{}
	if p.DisplayName != "" {
		parts = append(parts, p.DisplayName)
	}
	meta := []string{fmt.Sprintf("addr=%d", p.Address)}
	if p.KeyPath != "" {
		meta = append(meta, "keyPath="+p.KeyPath)
	}
	meta = append(meta, fmt.Sprintf("range=%g..%g", p.Min, p.Max))
	if u := FirstNonEmpty(p.UnitName, p.Unit); u != "" {
		meta = append(meta, "unit="+u)
	}
	if len(p.ValueStrings) > 0 {
		meta = append(meta, "values="+strings.Join(p.ValueStrings, "|"))
	}
	if n := len(p.DependentParameters); n > 0 {
		meta = append(meta, fmt.Sprintf("macro=drives:%d", n))
	}
	parts = append(parts, "[AU "+strings.Join(meta, " ")+"]")
	return strings.Join(parts, " ")
}

// enumValues maps each valueStrings label to its index (the AU value for an
// indexed param). Labels are kept verbatim so they read naturally in the tool.
func enumValues(labels []string) map[string]int {
	m := make(map[string]int, len(labels))
	used := map[string]bool{}
	for i, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			l = fmt.Sprintf("value_%d", i)
		}
		// On the rare duplicate label, disambiguate so no entry is lost.
		m[UniqueName(l, used)] = i
	}
	return m
}

// ProbeMismatch is one correctness discrepancy between a definition control and
// the probed parameter it maps to.
type ProbeMismatch struct {
	Control string
	Param   string
	Detail  string
}

// ProbeDiff is the coverage + correctness report of a dump against an existing
// device type. MissingFromDefinition is uncovered plugin functionality (writable
// params with no control); StaleControls are device-type controls with no
// matching param (likely wrong/renamed); Mismatches are unit/enum
// discrepancies on matched controls.
type ProbeDiff struct {
	MissingFromDefinition []ProbeParam
	StaleControls         []string
	Mismatches            []ProbeMismatch
}

// HasFindings reports whether the diff surfaced anything actionable.
func (d ProbeDiff) HasFindings() bool {
	return len(d.MissingFromDefinition) > 0 || len(d.StaleControls) > 0 || len(d.Mismatches) > 0
}

// DiffProbeAgainstDefinition compares a live parameter-tree dump to an existing
// device type. A param matches a control when the control's name equals the
// sanitized identifier, displayName, or keyPath of the param. It reports
// uncovered writable params, stale controls, and enum/unit mismatches on the
// matched pairs.
func DiffProbeAgainstDefinition(dump ProbeDump, def *DeviceType) ProbeDiff {
	var diff ProbeDiff
	if def == nil {
		diff.MissingFromDefinition = writableParams(dump)
		return diff
	}

	// Index params by their candidate match keys.
	paramByKey := map[string]int{}
	for i := range dump.Parameters {
		for _, k := range matchKeys(dump.Parameters[i]) {
			if _, ok := paramByKey[k]; !ok {
				paramByKey[k] = i
			}
		}
	}

	matchedParam := make([]bool, len(dump.Parameters))
	for ci := range def.Controls {
		c := &def.Controls[ci]
		idx, ok := paramByKey[sanitizeName(c.Name)]
		if !ok {
			diff.StaleControls = append(diff.StaleControls, c.Name)
			continue
		}
		matchedParam[idx] = true
		if m, ok := controlParamMismatch(c, dump.Parameters[idx]); ok {
			diff.Mismatches = append(diff.Mismatches, m)
		}
	}

	for i := range dump.Parameters {
		p := dump.Parameters[i]
		if p.Writable && !matchedParam[i] {
			diff.MissingFromDefinition = append(diff.MissingFromDefinition, p)
		}
	}
	return diff
}

// controlParamMismatch checks a matched control/param pair for enum and unit
// discrepancies. The wire range stays 0-127 by convention, so range itself is
// not flagged; enum membership and the declared unit are the meaningful checks.
func controlParamMismatch(c *Control, p ProbeParam) (ProbeMismatch, bool) {
	mk := func(detail string) (ProbeMismatch, bool) {
		return ProbeMismatch{Control: c.Name, Param: p.label(), Detail: detail}, true
	}

	indexed := len(p.ValueStrings) > 0
	isEnum := c.Value.Type == ValueEnum
	switch {
	case indexed && !isEnum:
		return mk(fmt.Sprintf("param is indexed (%d values) but control is %q, not enum", len(p.ValueStrings), valueTypeOr(c.Value.Type, "range")))
	case !indexed && isEnum:
		return mk("control is enum but param has no valueStrings")
	case indexed && isEnum && len(c.Value.Values) != len(p.ValueStrings):
		return mk(fmt.Sprintf("enum has %d values but param has %d", len(c.Value.Values), len(p.ValueStrings)))
	}

	if c.Value.Unit != "" {
		want := FirstNonEmpty(p.UnitName, p.Unit)
		if want != "" && !strings.EqualFold(c.Value.Unit, want) {
			return mk(fmt.Sprintf("unit %q does not match param unit %q", c.Value.Unit, want))
		}
	}
	return ProbeMismatch{}, false
}

func valueTypeOr(t ValueType, def string) string {
	if t == "" {
		return def
	}
	return string(t)
}

// matchKeys returns the sanitized candidate names a control may use to refer to
// this param.
func matchKeys(p ProbeParam) []string {
	var keys []string
	for _, s := range []string{p.Identifier, p.DisplayName, p.KeyPath} {
		if k := sanitizeName(s); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func writableParams(dump ProbeDump) []ProbeParam {
	var out []ProbeParam
	for _, p := range dump.Parameters {
		if p.Writable {
			out = append(out, p)
		}
	}
	return out
}

// sanitizeName reduces a label to a control-name-safe token (the shared
// identifier rule: lowercase, non-alphanumeric runs collapse to one underscore).
func sanitizeName(s string) string { return sanitize.ID(s) }

// UniqueName ensures a name is unique within a set by suffixing _2, _3, … on
// collision, recording the chosen name in used. It is the shared de-dup helper
// for generated control names, enum labels and AUM target keys (so callers do
// not each re-implement the suffix loop). An empty base becomes "param".
func UniqueName(base string, used map[string]bool) string {
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

// FirstNonEmpty returns the first non-empty string in ss, or "". It is the
// shared "prefer x, else y, else z" helper used across the device and mcpserver
// packages.
func FirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// --- Diagnostics ---------------------------------------------------------
//
// A probe run on the iPad does more than produce dumps: some plugins fail to
// instantiate, some have no parameter tree, some have an empty one, and some
// had non-finite values sanitized. The successful dumps go to /auv3-probe; the
// full picture of a run — including the failures, which never produce a dump —
// is POSTed to /auv3-probe/diagnostics as a ProbeReport so every outcome is
// recorded on the receiver, not just lost in the app UI.

// ProbeRunDevice is the (non-identifying) device context for a probe run. It
// deliberately omits the device's user-assigned name to keep the report free of
// personal/identifying detail (see .cursor/rules/public-vs-private.mdc).
type ProbeRunDevice struct {
	Model         string `json:"model,omitempty"`
	SystemName    string `json:"systemName,omitempty"`
	SystemVersion string `json:"systemVersion,omitempty"`
}

// ProbeRunResult is the outcome for one plugin in a probe run. Status is one of
// "sent" (dump POSTed ok), "probed" (probed but not sent — no receiver),
// "empty" (no AUM-mappable parameters), or "failed" (Error explains why).
type ProbeRunResult struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Component ProbeComponent `json:"component"`
	Status    string         `json:"status"`
	Error     string         `json:"error,omitempty"`
	Params    int            `json:"params"`
	Writable  int            `json:"writable"`
	Sanitized int            `json:"sanitized,omitempty"`
}

// ProbeReport is the full diagnostic record of one probe run, POSTed to the
// receiver's /auv3-probe/diagnostics endpoint.
type ProbeReport struct {
	App       string           `json:"app,omitempty"`
	StartedAt string           `json:"startedAt,omitempty"`
	Device    ProbeRunDevice   `json:"device,omitempty"`
	Results   []ProbeRunResult `json:"results"`
}

// Summary tallies the run's outcomes by status for a one-line log/return.
func (r ProbeReport) Summary() (total, sent, empty, failed int) {
	total = len(r.Results)
	for _, res := range r.Results {
		switch res.Status {
		case "sent":
			sent++
		case "empty":
			empty++
		case "failed":
			failed++
		}
	}
	return total, sent, empty, failed
}
