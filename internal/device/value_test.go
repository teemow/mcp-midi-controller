package device

import (
	"errors"
	"math"
	"testing"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func TestResolveFloatRejectsNonFinite(t *testing.T) {
	c := &Control{Type: ControlCC, CC: iptr(17), Value: ValueSpec{Type: ValueFloat, Min: f64(0), Max: f64(1)}}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := Resolve(c, v); err == nil {
			t.Errorf("Resolve(%v): expected error for non-finite value", v)
		}
	}
}

func TestResolveNRPNDefaultRange(t *testing.T) {
	c := &Control{Type: ControlNRPN, NRPN: iptr(5), Value: ValueSpec{Type: ValueRange}}
	if _, err := Resolve(c, float64(16383)); err != nil {
		t.Errorf("NRPN value 16383 should be in range: %v", err)
	}
	if _, err := Resolve(c, float64(16384)); err == nil {
		t.Errorf("NRPN value 16384 should be out of range")
	}
}

func TestResolveRange(t *testing.T) {
	c := &Control{Type: ControlCC, CC: iptr(17), Value: ValueSpec{Type: ValueRange, Min: f64(0), Max: f64(127)}}

	r, err := Resolve(c, float64(64)) // JSON numbers arrive as float64
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Int != 64 {
		t.Fatalf("Int = %d, want 64", r.Int)
	}

	if _, err := Resolve(c, float64(200)); err == nil {
		t.Fatalf("expected out-of-range error")
	}
	if _, err := Resolve(c, float64(64.5)); err == nil {
		t.Fatalf("expected non-integer error")
	}
	if _, err := Resolve(c, "nope"); err == nil {
		t.Fatalf("expected type error")
	}
}

func TestResolveRangeDefaultsTo127(t *testing.T) {
	c := &Control{Type: ControlCC, CC: iptr(1), Value: ValueSpec{Type: ValueRange}}
	if _, err := Resolve(c, float64(127)); err != nil {
		t.Fatalf("127 should be in default range: %v", err)
	}
	if _, err := Resolve(c, float64(128)); err == nil {
		t.Fatalf("128 should exceed the default 0..127 range")
	}
}

func TestResolveEnum(t *testing.T) {
	c := &Control{Type: ControlCC, CC: iptr(28), Value: ValueSpec{Type: ValueEnum, Values: map[string]int{"off": 0, "on": 127}}}

	r, err := Resolve(c, "on")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Int != 127 {
		t.Fatalf("Int = %d, want 127", r.Int)
	}

	// A raw wire value that matches an enum entry is accepted.
	if r, err := Resolve(c, float64(0)); err != nil || r.Int != 0 {
		t.Fatalf("wire value 0 = (%+v, %v)", r, err)
	}
	// A label that is not defined is rejected.
	if _, err := Resolve(c, "halfway"); err == nil {
		t.Fatalf("expected unknown-label error")
	}
	// A wire value not present in the enum is rejected.
	if _, err := Resolve(c, float64(64)); err == nil {
		t.Fatalf("expected invalid wire value error")
	}
}

func TestResolveFloatAndString(t *testing.T) {
	fc := &Control{Type: ControlOSC, Address: "/ch/01/mix/fader", Value: ValueSpec{Type: ValueFloat, Min: f64(0), Max: f64(1)}}
	if r, err := Resolve(fc, 0.75); err != nil || r.Float != 0.75 {
		t.Fatalf("float resolve = (%+v, %v)", r, err)
	}
	if _, err := Resolve(fc, 1.5); err == nil {
		t.Fatalf("expected float out-of-range error")
	}

	sc := &Control{Type: ControlOSC, Address: "/ch/01/config/name", Value: ValueSpec{Type: ValueString}}
	if r, err := Resolve(sc, "vocals"); err != nil || r.Str != "vocals" {
		t.Fatalf("string resolve = (%+v, %v)", r, err)
	}
	if _, err := Resolve(sc, float64(3)); err == nil {
		t.Fatalf("expected string type error")
	}
}

func TestResolveParametric(t *testing.T) {
	c := &Control{Type: ControlCC, Parametric: true, Value: ValueSpec{Type: ValueRange, Min: f64(0), Max: f64(127)}}

	r, err := Resolve(c, map[string]any{"number": float64(74), "value": float64(100)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.HasNumber || r.Number != 74 || r.Int != 100 {
		t.Fatalf("parametric resolve = %+v", r)
	}

	// A bad value inside the object reports the nested pointer.
	_, err = Resolve(c, map[string]any{"number": float64(74), "value": float64(999)})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Pointer != "/value/value" {
		t.Fatalf("expected /value/value pointer, got %v", err)
	}

	// A bare scalar is treated as the value with no number (e.g. parametric PC).
	if r, err := Resolve(c, float64(5)); err != nil || r.HasNumber {
		t.Fatalf("bare scalar parametric = (%+v, %v)", r, err)
	}
}

func TestValidationErrorPointer(t *testing.T) {
	c := &Control{Type: ControlCC, CC: iptr(17), Value: ValueSpec{Type: ValueRange, Min: f64(0), Max: f64(127)}}
	_, err := Resolve(c, float64(200))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Pointer != "/value" {
		t.Fatalf("Pointer = %q, want /value", ve.Pointer)
	}
}
