package device

import (
	"fmt"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/sanitize"
)

// This file turns an AUv3 parameter-tree dump (produced by the off-daemon
// cmd/auv3-probe utility, see docs/research/auv3-feedback.md) into a device
// Definition draft and diffs a dump against an existing definition. Because AUM
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

// ProbeID derives the sanitized definition/file id for a dump (from the
// component subtype, falling back to the name). The off-daemon receiver names
// staged dumps <ProbeID>.json so import_auv3_probe and DefinitionFromProbe
// agree on the id.
func ProbeID(dump ProbeDump) string {
	return sanitizeName(firstNonEmpty(dump.Component.Subtype, dump.Name))
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

// ProbeOptions tunes DefinitionFromProbe. Zero value uses the project defaults
// (CC convention starting at 30, capping at 127, enum for <=8 valueStrings).
type ProbeOptions struct {
	// ID overrides the derived definition id (otherwise from Component.Subtype
	// or Name, sanitized).
	ID string
	// Name overrides the derived definition name (otherwise the dump Name).
	Name string
	// StartCC is the first convention CC assigned (default 30, per
	// docs/research/auv3-plugins.md).
	StartCC int
	// MaxCC is the last usable CC (default 127). Params that would exceed it are
	// reported in ProbeBuildReport.Overflow rather than dropped silently.
	MaxCC int
	// EnumMax is the largest valueStrings count still modeled as an enum control
	// (default 8). Above it the param becomes a plain range control.
	EnumMax int
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
	return o
}

// ProbeBuildReport accompanies a generated Definition: it records the writable
// params that did not fit in the CC range (so a human can curate them onto a
// second channel/file) and the read-only params that were skipped (not
// AUM-mappable).
type ProbeBuildReport struct {
	// Overflow lists writable params beyond the CC cap (start..max). They are
	// reported, not dropped, so coverage gaps are explicit.
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
}

// DefinitionFromProbe builds a device.Definition draft from a parameter-tree
// dump. Transport is blemidi (the AUM-over-BLE path). One cc control is emitted
// per writable param in tree order, assigning the convention CC starting at
// opts.StartCC. The wire value spec is range 0-127 (AUM scales the 7-bit CC
// onto the AU float range); the AU displayName/range/unit/valueStrings are
// recorded in the control Description. Small indexed params (valueStrings,
// <=opts.EnumMax) become enum controls. The returned definition is validated.
func DefinitionFromProbe(dump ProbeDump, opts ProbeOptions) (*Definition, ProbeBuildReport, error) {
	opts = opts.withDefaults()
	var report ProbeBuildReport

	id := opts.ID
	if id == "" {
		id = ProbeID(dump)
	}
	if id == "" {
		return nil, report, fmt.Errorf("auv3 probe: cannot derive a definition id (empty subtype and name)")
	}
	name := firstNonEmpty(opts.Name, dump.Name, id)

	def := &Definition{
		ID:           id,
		Name:         name,
		Manufacturer: dump.Component.Manufacturer,
		Description:  fmt.Sprintf("Generated from an AUv3 parameter-tree probe (component %s/%s).", dump.Component.Type, dump.Component.Subtype),
		Transport:    "blemidi",
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

	usedNames := map[string]bool{}
	nextCC := opts.StartCC
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
		if nextCC > opts.MaxCC {
			report.Overflow = append(report.Overflow, p)
			continue
		}
		cc := nextCC
		nextCC++

		c := Control{
			Name:        uniqueName(sanitizeName(p.label()), usedNames),
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
		return nil, report, fmt.Errorf("auv3 probe: generated definition is invalid: %w", err)
	}
	return def, report, nil
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
	if u := firstNonEmpty(p.UnitName, p.Unit); u != "" {
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
	for i, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			l = fmt.Sprintf("value_%d", i)
		}
		// On the rare duplicate label, disambiguate so no entry is lost.
		key := l
		for j := 1; ; j++ {
			if _, clash := m[key]; !clash {
				break
			}
			key = fmt.Sprintf("%s_%d", l, j)
		}
		m[key] = i
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
// definition. MissingFromDefinition is uncovered plugin functionality (writable
// params with no control); StaleControls are definition controls with no
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
// definition. A param matches a control when the control's name equals the
// sanitized identifier, displayName, or keyPath of the param. It reports
// uncovered writable params, stale controls, and enum/unit mismatches on the
// matched pairs.
func DiffProbeAgainstDefinition(dump ProbeDump, def *Definition) ProbeDiff {
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
		want := firstNonEmpty(p.UnitName, p.Unit)
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

// uniqueName ensures a control name is unique within a definition by suffixing
// _2, _3, … on collision. It records the chosen name in used.
func uniqueName(base string, used map[string]bool) string {
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

func firstNonEmpty(ss ...string) string {
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
