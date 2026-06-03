package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

func cc(n int) *int { return &n }

func TestControlToolSchemaPerControlOneOf(t *testing.T) {
	def := &device.Definition{
		ID:        "d",
		Name:      "Dev",
		Transport: "blemidi",
		Controls: []device.Control{
			{Name: "level", Type: device.ControlCC, CC: cc(17), Value: device.ValueSpec{Type: device.ValueRange, Min: f(0), Max: f(127)}},
			{Name: "mode", Type: device.ControlCC, CC: cc(28), Value: device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"off": 0, "on": 127}}},
			{Name: "raw", Type: device.ControlCC, Parametric: true, Value: device.ValueSpec{Type: device.ValueRange}},
		},
	}

	raw := controlToolSchema(def)

	var schema struct {
		Type       string `json:"type"`
		Properties struct {
			Settings struct {
				Type  string `json:"type"`
				Items struct {
					OneOf []struct {
						Properties struct {
							Control struct {
								Const string `json:"const"`
							} `json:"control"`
							Value map[string]any `json:"value"`
						} `json:"properties"`
					} `json:"oneOf"`
				} `json:"items"`
			} `json:"settings"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, raw)
	}
	if schema.Type != "object" {
		t.Fatalf("top-level type = %q, want object", schema.Type)
	}
	oneOf := schema.Properties.Settings.Items.OneOf
	if len(oneOf) != 3 {
		t.Fatalf("want 3 oneOf entries, got %d", len(oneOf))
	}

	byControl := map[string]map[string]any{}
	for _, o := range oneOf {
		byControl[o.Properties.Control.Const] = o.Properties.Value
	}

	// level: integer with min/max bounds.
	lev := byControl["level"]
	if lev["type"] != "integer" {
		t.Fatalf("level value type = %v, want integer", lev["type"])
	}
	if lev["minimum"] != float64(0) || lev["maximum"] != float64(127) {
		t.Fatalf("level bounds = %v..%v", lev["minimum"], lev["maximum"])
	}

	// mode: enum carrying labels and wire ints.
	mode := byControl["mode"]
	enum, ok := mode["enum"].([]any)
	if !ok || len(enum) == 0 {
		t.Fatalf("mode enum = %#v", mode["enum"])
	}
	var sawOff bool
	for _, e := range enum {
		if e == "off" {
			sawOff = true
		}
	}
	if !sawOff {
		t.Fatalf("mode enum missing the off label: %#v", enum)
	}

	// raw: parametric -> object with number+value.
	rawc := byControl["raw"]
	if rawc["type"] != "object" {
		t.Fatalf("parametric value type = %v, want object", rawc["type"])
	}
	props, ok := rawc["properties"].(map[string]any)
	if !ok || props["number"] == nil || props["value"] == nil {
		t.Fatalf("parametric value props = %#v", rawc["properties"])
	}
}

func TestDescribeValueSpec(t *testing.T) {
	cases := []struct {
		name string
		c    device.Control
		want string
	}{
		{"range", device.Control{Type: device.ControlCC, Value: device.ValueSpec{Type: device.ValueRange}}, "0..127"},
		{"enum", device.Control{Type: device.ControlCC, Value: device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"off": 0, "on": 127}}}, "enum {off=0, on=127}"},
		{"float unit", device.Control{Type: device.ControlOSC, Value: device.ValueSpec{Type: device.ValueFloat, Min: f(0), Max: f(1), Unit: "dB"}}, "float 0..1 (dB)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeValueSpec(&tc.c); got != tc.want {
				t.Fatalf("describeValueSpec = %q, want %q", got, tc.want)
			}
		})
	}
}

func f(v float64) *float64 { return &v }
