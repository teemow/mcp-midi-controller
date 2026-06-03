package device

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ValidationError reports that a control value failed its spec. Pointer is an
// RFC-6901 JSON pointer relative to a single control invocation (e.g. "/value",
// "/value/number" or "/control"); the MCP layer prefixes it with the batch
// position so the model gets a precise, self-correcting path (SEP-1303).
type ValidationError struct {
	Pointer string
	Msg     string
}

func (e *ValidationError) Error() string {
	if e.Pointer == "" {
		return e.Msg
	}
	return e.Pointer + ": " + e.Msg
}

// Resolved is a control value after validation. Exactly one of Int/Float/Str is
// meaningful, selected by Type. Number carries the parametric address number
// (cc#/nrpn#/note#) supplied at call time for parametric controls.
type Resolved struct {
	Type      ValueType
	Int       int
	Float     float64
	Str       string
	Number    int
	HasNumber bool
}

// Resolve validates raw against the control's value spec and returns the wire
// value. For parametric controls (generic-midi) the caller may pass an object
// {"number": N, "value": V}; the number becomes the CC/NRPN/note address while
// the value is validated against the spec. Non-object input is treated as the
// value alone (e.g. a parametric program_change whose value is the program).
func Resolve(c *Control, raw any) (Resolved, error) {
	// The implicit range domain depends on the control: NRPN carries a 14-bit
	// value (0..16383), every other MIDI control is the 7-bit CC domain.
	rangeHi := 127
	if c.Type == ControlNRPN {
		rangeHi = 16383
	}
	if c.Parametric {
		if m, ok := raw.(map[string]any); ok {
			r, err := resolveValue(&c.Value, m["value"], "/value/value", rangeHi)
			if err != nil {
				return Resolved{}, err
			}
			if n, ok := m["number"]; ok {
				num, err := toInt(n, "/value/number")
				if err != nil {
					return Resolved{}, err
				}
				r.Number = num
				r.HasNumber = true
			}
			return r, nil
		}
	}
	return resolveValue(&c.Value, raw, "/value", rangeHi)
}

func resolveValue(spec *ValueSpec, raw any, ptr string, rangeHi int) (Resolved, error) {
	switch spec.Type {
	case ValueEnum:
		return resolveEnum(spec, raw, ptr)
	case ValueInt:
		return resolveBoundedInt(spec, raw, ptr, ValueInt, nil, nil)
	case ValueFloat:
		return resolveFloat(spec, raw, ptr)
	case ValueString:
		return resolveString(raw, ptr)
	case ValueRange, "":
		// range defaults to the control's wire domain when bounds are omitted
		// (0..127 for CC, 0..16383 for NRPN).
		lo, hi := 0, rangeHi
		return resolveBoundedInt(spec, raw, ptr, ValueRange, &lo, &hi)
	default:
		return Resolved{}, &ValidationError{ptr, fmt.Sprintf("control has unknown value type %q", spec.Type)}
	}
}

func resolveEnum(spec *ValueSpec, raw any, ptr string) (Resolved, error) {
	if len(spec.Values) == 0 {
		return Resolved{}, &ValidationError{ptr, "enum control has no values defined"}
	}
	if s, ok := raw.(string); ok {
		if wire, ok := spec.Values[s]; ok {
			return Resolved{Type: ValueEnum, Int: wire}, nil
		}
		return Resolved{}, &ValidationError{ptr, fmt.Sprintf("must be one of %s", enumLabels(spec.Values))}
	}
	// Accept a raw wire value too, as long as it is one the enum defines.
	n, err := toInt(raw, ptr)
	if err != nil {
		return Resolved{}, &ValidationError{ptr, fmt.Sprintf("must be one of %s", enumLabels(spec.Values))}
	}
	for _, w := range spec.Values {
		if w == n {
			return Resolved{Type: ValueEnum, Int: n}, nil
		}
	}
	return Resolved{}, &ValidationError{ptr, fmt.Sprintf("must be one of %s", enumLabels(spec.Values))}
}

func resolveBoundedInt(spec *ValueSpec, raw any, ptr string, kind ValueType, defLo, defHi *int) (Resolved, error) {
	n, err := toInt(raw, ptr)
	if err != nil {
		return Resolved{}, err
	}
	lo, hasLo := defLo, defLo != nil
	hi, hasHi := defHi, defHi != nil
	if spec.Min != nil {
		v := int(*spec.Min)
		lo, hasLo = &v, true
	}
	if spec.Max != nil {
		v := int(*spec.Max)
		hi, hasHi = &v, true
	}
	if hasLo && n < *lo {
		return Resolved{}, &ValidationError{ptr, boundsMsg(lo, hi, spec.Unit)}
	}
	if hasHi && n > *hi {
		return Resolved{}, &ValidationError{ptr, boundsMsg(lo, hi, spec.Unit)}
	}
	return Resolved{Type: kind, Int: n}, nil
}

func resolveFloat(spec *ValueSpec, raw any, ptr string) (Resolved, error) {
	f, err := toFloat(raw, ptr)
	if err != nil {
		return Resolved{}, err
	}
	// NaN/±Inf compare false against any bound, so they would slip past the
	// range checks below and render to a garbage wire value. Reject them.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Resolved{}, &ValidationError{ptr, "must be a finite number"}
	}
	if spec.Min != nil && f < *spec.Min {
		return Resolved{}, &ValidationError{ptr, floatBoundsMsg(spec)}
	}
	if spec.Max != nil && f > *spec.Max {
		return Resolved{}, &ValidationError{ptr, floatBoundsMsg(spec)}
	}
	return Resolved{Type: ValueFloat, Float: f}, nil
}

func resolveString(raw any, ptr string) (Resolved, error) {
	s, ok := raw.(string)
	if !ok {
		return Resolved{}, &ValidationError{ptr, fmt.Sprintf("expected a string, got %s", typeName(raw))}
	}
	return Resolved{Type: ValueString, Str: s}, nil
}

// toInt coerces a JSON/YAML-decoded value to an int, rejecting fractional
// numbers. JSON decodes numbers as float64; YAML decodes them as int.
func toInt(raw any, ptr string) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case uint:
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		return int(v), nil
	case uint64:
		return int(v), nil
	case float32:
		return floatToInt(float64(v), ptr)
	case float64:
		return floatToInt(v, ptr)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, nil
		}
		return 0, &ValidationError{ptr, fmt.Sprintf("expected a number, got %q", v)}
	default:
		return 0, &ValidationError{ptr, fmt.Sprintf("expected a number, got %s", typeName(raw))}
	}
}

func floatToInt(f float64, ptr string) (int, error) {
	if f != math.Trunc(f) {
		return 0, &ValidationError{ptr, fmt.Sprintf("expected a whole number, got %v", f)}
	}
	return int(f), nil
}

func toFloat(raw any, ptr string) (float64, error) {
	switch v := raw.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, nil
		}
		return 0, &ValidationError{ptr, fmt.Sprintf("expected a number, got %q", v)}
	default:
		return 0, &ValidationError{ptr, fmt.Sprintf("expected a number, got %s", typeName(raw))}
	}
}

func enumLabels(values map[string]int) string {
	labels := make([]string, 0, len(values))
	for k := range values {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	return "{" + strings.Join(labels, ", ") + "}"
}

func boundsMsg(lo, hi *int, unit string) string {
	var b strings.Builder
	b.WriteString("must be an integer")
	switch {
	case lo != nil && hi != nil:
		fmt.Fprintf(&b, " in [%d, %d]", *lo, *hi)
	case lo != nil:
		fmt.Fprintf(&b, " >= %d", *lo)
	case hi != nil:
		fmt.Fprintf(&b, " <= %d", *hi)
	}
	if unit != "" {
		fmt.Fprintf(&b, " (%s)", unit)
	}
	return b.String()
}

func floatBoundsMsg(spec *ValueSpec) string {
	var b strings.Builder
	b.WriteString("must be a number")
	switch {
	case spec.Min != nil && spec.Max != nil:
		fmt.Fprintf(&b, " in [%g, %g]", *spec.Min, *spec.Max)
	case spec.Min != nil:
		fmt.Fprintf(&b, " >= %g", *spec.Min)
	case spec.Max != nil:
		fmt.Fprintf(&b, " <= %g", *spec.Max)
	}
	if spec.Unit != "" {
		fmt.Fprintf(&b, " (%s)", spec.Unit)
	}
	return b.String()
}

func typeName(raw any) string {
	if raw == nil {
		return "null"
	}
	return fmt.Sprintf("%T", raw)
}
