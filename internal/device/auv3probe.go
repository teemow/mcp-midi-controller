package device

import (
	"fmt"
	"strings"
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
type ProbeComponent struct {
	Type         string `json:"type"`
	Subtype      string `json:"subtype"`
	Manufacturer string `json:"manufacturer"`
}

// ProbeParam is one AUParameter as read from auAudioUnit.parameterTree. The
// JSON tags match the dump shape documented in docs/research/auv3-feedback.md.
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
}

// ProbeDump is the full parameter-tree dump for one plugin.
type ProbeDump struct {
	Component  ProbeComponent `json:"component"`
	Name       string         `json:"name"`
	Parameters []ProbeParam   `json:"parameters"`
}

// ProbeID derives the sanitized definition/file id for a dump (from the
// component subtype, falling back to the name). The off-daemon receiver names
// staged dumps <ProbeID>.json so import_auv3_probe and DefinitionFromProbe
// agree on the id.
func ProbeID(dump ProbeDump) string {
	return sanitizeID(firstNonEmpty(dump.Component.Subtype, dump.Name))
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

	usedNames := map[string]bool{}
	nextCC := opts.StartCC
	for _, p := range dump.Parameters {
		if !p.Writable {
			report.SkippedReadOnly = append(report.SkippedReadOnly, p)
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

// sanitizeName lowercases and reduces a label to a control-name-safe token:
// runs of non-alphanumeric characters collapse to a single underscore.
func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// sanitizeID is sanitizeName with a guaranteed non-empty fallback.
func sanitizeID(s string) string {
	return sanitizeName(s)
}

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
