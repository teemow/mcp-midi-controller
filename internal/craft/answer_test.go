package craft

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestAnswerShapeAndNormalization(t *testing.T) {
	ans := NewAnswer("drums", nil, nil, "7c1e0123456789ab", nil, nil, false)
	if err := ans.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	raw, err := json.Marshal(ans)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	gotKeys := make([]string, 0, len(m))
	for k := range m {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	wantKeys := []string{"applied", "checks", "craft", "patch", "revision", "techniques", "why"}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("answer keys = %v, want %v", gotKeys, wantKeys)
	}
	for _, k := range []string{"techniques", "why", "checks"} {
		s, ok := m[k].([]any)
		if !ok || len(s) != 0 {
			t.Errorf("%s = %#v, want empty JSON array []", k, m[k])
		}
	}
	if p, ok := m["patch"].(map[string]any); !ok || len(p) != 0 {
		t.Errorf("patch = %#v, want empty JSON object {}", m["patch"])
	}
	if m["applied"] != false {
		t.Errorf("applied = %#v, want false", m["applied"])
	}

	full := NewAnswer(
		"mixing",
		[]string{"mixing.highpass_non_bass"},
		map[string]any{"tracks": map[string]any{"pad": map[string]any{"effects": map[string]any{"hp": map[string]any{"place": "first", "plugin": "eq1"}}}}},
		"0123456789abcdef",
		[]string{"pad has low-end energy below its lowest note"},
		[]string{"20..80 Hz fell by 8 dB"},
		true,
	)
	sum := full.Summary()
	for _, want := range []string{
		"mixing: applied, revision 0123456789abcdef [mixing.highpass_non_bass]",
		"why: pad has low-end energy below its lowest note",
		"check: 20..80 Hz fell by 8 dB",
	} {
		if !strings.Contains(sum, want) {
			t.Errorf("Summary lacks %q:\n%s", want, sum)
		}
	}

	if err := (Answer{Craft: "nope", Revision: "abc"}).Validate(); err == nil {
		t.Error("Validate accepted an unknown craft")
	}
	if err := (Answer{Craft: "drums"}).Validate(); err == nil {
		t.Error("Validate accepted an empty revision")
	}
}
