package sanitize

import "testing"

func TestID(t *testing.T) {
	cases := map[string]string{
		"New Fast Forward.aumproj": "new_fast_forward_aumproj",
		"  Hello  World  ":         "hello_world",
		"Manufacturer: Plug-In":    "manufacturer_plug_in",
		"___trim___":               "trim",
		"a..b/../c":                "a_b_c",
		"already_ok_123":           "already_ok_123",
		"":                         "",
		"!!!":                      "",
	}
	for in, want := range cases {
		if got := ID(in); got != want {
			t.Errorf("ID(%q) = %q, want %q", in, got, want)
		}
	}
}
