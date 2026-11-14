package craft

import (
	"encoding/json"
	"testing"
)

type unitValue struct {
	Num  float64 `json:"num"`
	Unit string  `json:"unit"`
}

func (v unitValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"num": v.Num, "unit": v.Unit})
}

type effectSpec struct {
	Plugin string             `json:"plugin"`
	Params map[string]float64 `json:"params"` // no omitempty: a nil map is written as null
	Place  *string            `json:"place"`
}

func TestCleanPatchDropsTypedNilsAndKeepsRemovals(t *testing.T) {
	var noPlace *string
	patch := map[string]any{
		"tracks": map[string]any{
			"pad": map[string]any{
				"instrument": map[string]any{
					"params": map[string]any(nil),
					"preset": nil, // the one removal
				},
				"effects": map[string]map[string]any{
					"echo": {"params": map[string]float64(nil), "bypass": false},
					"hp":   nil,
				},
				"level": noPlace,
			},
			"bass": map[string]any{
				"effects": map[string]any{
					"eq": effectSpec{Plugin: "eq1"},
					"lp": &effectSpec{Plugin: "eq2", Params: map[string]float64{"Freq": 8000}},
				},
				"instrument": map[string]any{"params": map[string]any{"Cutoff": unitValue{Num: 0.4, Unit: "%"}}},
			},
		},
		"sections": map[string]any{
			"verse": map[string]any{"play": map[string]any{"drums": []string(nil), "bass": []any{"a", map[string]any(nil)}}},
		},
		"arrangement": []string{"verse"},
	}
	got, err := CleanPatch(patch)
	if err != nil {
		t.Fatalf("CleanPatch: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"arrangement":["verse"],` +
		`"sections":{"verse":{"play":{"bass":["a",null]}}},` +
		`"tracks":{"bass":{"effects":{"eq":{"plugin":"eq1"},"lp":{"params":{"Freq":8000},"plugin":"eq2"}},` +
		`"instrument":{"params":{"Cutoff":{"num":0.4,"unit":"%"}}}},` +
		`"pad":{"effects":{"echo":{"bypass":false}},"instrument":{"preset":null}}}}`
	if string(raw) != want {
		t.Errorf("CleanPatch marshals as\n%s\nwant\n%s", raw, want)
	}
	// The input is not changed.
	if _, ok := patch["tracks"].(map[string]any)["pad"].(map[string]any)["instrument"].(map[string]any)["params"]; !ok {
		t.Error("CleanPatch changed the plan's own patch")
	}
}

func TestCleanPatchOfNothing(t *testing.T) {
	for _, p := range []map[string]any{nil, {}, {"tracks": map[string]any(nil)}} {
		got, err := CleanPatch(p)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("CleanPatch(%#v) = %#v, %v; want an empty map", p, got, err)
		}
	}
}
