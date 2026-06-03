package device

import "testing"

func TestDefinitionValidateAddressing(t *testing.T) {
	cc := func(n int) *int { return &n }
	bound := func(v float64) *float64 { return &v }

	cases := []struct {
		name    string
		def     Definition
		wantErr bool
	}{
		{
			name: "good cc",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "level", Type: ControlCC, CC: cc(17), Value: ValueSpec{Type: ValueRange}},
			}},
		},
		{
			name: "cc missing number",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "level", Type: ControlCC, Value: ValueSpec{Type: ValueRange}},
			}},
			wantErr: true,
		},
		{
			name: "parametric cc needs no number",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "cc", Type: ControlCC, Parametric: true, Value: ValueSpec{Type: ValueRange}},
			}},
		},
		{
			name: "osc missing address",
			def: Definition{ID: "d", Transport: "osc", Controls: []Control{
				{Name: "fader", Type: ControlOSC, Value: ValueSpec{Type: ValueFloat}},
			}},
			wantErr: true,
		},
		{
			name: "sysex missing template",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "x", Type: ControlSysEx, Value: ValueSpec{Type: ValueRange}},
			}},
			wantErr: true,
		},
		{
			name: "enum without values",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "sw", Type: ControlCC, CC: cc(1), Value: ValueSpec{Type: ValueEnum}},
			}},
			wantErr: true,
		},
		{
			name: "min greater than max",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "x", Type: ControlCC, CC: cc(1), Value: ValueSpec{Type: ValueRange, Min: bound(100), Max: bound(10)}},
			}},
			wantErr: true,
		},
		{
			name: "unknown control type",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "x", Type: "weird", Value: ValueSpec{Type: ValueRange}},
			}},
			wantErr: true,
		},
		{
			name:    "missing transport",
			def:     Definition{ID: "d"},
			wantErr: true,
		},
		{
			name: "duplicate control names",
			def: Definition{ID: "d", Transport: "blemidi", Controls: []Control{
				{Name: "x", Type: ControlCC, CC: cc(1), Value: ValueSpec{Type: ValueRange}},
				{Name: "x", Type: ControlCC, CC: cc(2), Value: ValueSpec{Type: ValueRange}},
			}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRegistryAddDefinition(t *testing.T) {
	r := NewRegistry()

	cc := 17
	good := &Definition{ID: "newdev", Name: "New Device", Transport: "blemidi", Controls: []Control{
		{Name: "level", Type: ControlCC, CC: &cc, Value: ValueSpec{Type: ValueRange}},
	}}
	if err := r.AddDefinition(good); err != nil {
		t.Fatalf("add good: %v", err)
	}
	got, ok := r.Get("newdev")
	if !ok || got.Name != "New Device" {
		t.Fatalf("round-trip failed: %+v ok=%v", got, ok)
	}

	// Invalid definitions are rejected and not inserted.
	bad := &Definition{ID: "baddev", Transport: "blemidi", Controls: []Control{
		{Name: "x", Type: ControlCC, Value: ValueSpec{Type: ValueRange}}, // cc missing
	}}
	if err := r.AddDefinition(bad); err == nil {
		t.Fatal("expected AddDefinition to reject an invalid definition")
	}
	if _, ok := r.Get("baddev"); ok {
		t.Fatal("invalid definition should not have been inserted")
	}
}
