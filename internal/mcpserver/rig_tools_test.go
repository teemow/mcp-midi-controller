package mcpserver

import (
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// ccPtr is a small helper for building CC controls in tests.
func ccPtr(n int) *int { return &n }

func rigTestServer(t *testing.T) (*Server, *device.Registry) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := device.NewRegistry()
	def := &device.Definition{
		ID:           "testpedal",
		Name:         "Test Pedal",
		Manufacturer: "Acme",
		Transport:    "blemidi",
		Controls: []device.Control{
			{Name: "level", Type: device.ControlCC, CC: ccPtr(17), Value: device.ValueSpec{Type: device.ValueRange}},
			{Name: "mode", Type: device.ControlCC, CC: ccPtr(18), Value: device.ValueSpec{Type: device.ValueEnum, Values: map[string]int{"off": 0, "on": 127}}},
		},
	}
	if err := reg.AddDefinition(def); err != nil {
		t.Fatalf("add definition: %v", err)
	}
	eng := engine.New(reg, fakeBLE{})
	s := New(eng)
	return s, reg
}

func TestListDefinitions(t *testing.T) {
	s, _ := rigTestServer(t)

	res := call(t, s.handleListDefinitions, map[string]any{})
	if res.IsError {
		t.Fatalf("list_definitions failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "testpedal") {
		t.Fatalf("text missing definition id:\n%s", resultText(res))
	}

	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want map", res.StructuredContent)
	}
	defs, ok := sc["definitions"].([]definitionSummary)
	if !ok {
		t.Fatalf("definitions is %T, want []definitionSummary", sc["definitions"])
	}
	if len(defs) != 1 {
		t.Fatalf("got %d definitions, want 1", len(defs))
	}
	if defs[0].ID != "testpedal" || defs[0].Controls != 2 {
		t.Fatalf("unexpected summary: %+v", defs[0])
	}
}

func TestGetDefinition(t *testing.T) {
	s, _ := rigTestServer(t)

	// Unknown id is an error result, not a protocol error.
	if res := call(t, s.handleGetDefinition, map[string]any{"id": "nope"}); !res.IsError {
		t.Fatal("expected error for unknown definition")
	}

	res := call(t, s.handleGetDefinition, map[string]any{"id": "testpedal"})
	if res.IsError {
		t.Fatalf("get_definition failed: %s", resultText(res))
	}
	view, ok := res.StructuredContent.(definitionView)
	if !ok {
		t.Fatalf("structuredContent is %T, want definitionView", res.StructuredContent)
	}
	if view.ID != "testpedal" || len(view.Controls) != 2 {
		t.Fatalf("unexpected view: %+v", view)
	}
	// The enum control carries its label->wire map through the view.
	var mode *controlView
	for i := range view.Controls {
		if view.Controls[i].Name == "mode" {
			mode = &view.Controls[i]
		}
	}
	if mode == nil {
		t.Fatal("mode control missing from view")
	}
	if mode.Value.Values["on"] != 127 {
		t.Fatalf("mode enum values lost: %+v", mode.Value.Values)
	}
}

func TestListBindings(t *testing.T) {
	s, _ := rigTestServer(t)

	// Empty rig: text hint + empty structured list.
	res := call(t, s.handleListBindings, map[string]any{})
	if res.IsError {
		t.Fatalf("list_bindings failed: %s", resultText(res))
	}
	if sc, ok := res.StructuredContent.(map[string]any); ok {
		if b, ok := sc["bindings"].([]bindingView); !ok || len(b) != 0 {
			t.Fatalf("expected empty bindings, got %#v", sc["bindings"])
		}
	} else {
		t.Fatalf("structuredContent is %T, want map", res.StructuredContent)
	}

	// Bind a device, then it shows up.
	if err := s.eng.Bind(engine.Binding{Logical: "lead", Endpoint: "ep1", Channel: 3, DeviceID: "testpedal"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	res = call(t, s.handleListBindings, map[string]any{})
	sc := res.StructuredContent.(map[string]any)
	bindings := sc["bindings"].([]bindingView)
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(bindings))
	}
	b := bindings[0]
	if b.Logical != "lead" || b.Device != "testpedal" || b.Channel != 3 || b.DeviceName != "Test Pedal" {
		t.Fatalf("unexpected binding view: %+v", b)
	}
	if !strings.Contains(resultText(res), "lead") {
		t.Fatalf("text missing logical name:\n%s", resultText(res))
	}
}

// resolveByLogical confirms get_definition resolves a logical device name to its
// definition (like describe_device).
func TestGetDefinitionByLogical(t *testing.T) {
	s, _ := rigTestServer(t)
	if err := s.eng.Bind(engine.Binding{Logical: "lead", Endpoint: "ep1", Channel: 0, DeviceID: "testpedal"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	res := call(t, s.handleGetDefinition, map[string]any{"id": "lead"})
	if res.IsError {
		t.Fatalf("get_definition by logical failed: %s", resultText(res))
	}
	if view := res.StructuredContent.(definitionView); view.ID != "testpedal" {
		t.Fatalf("resolved to %q, want testpedal", view.ID)
	}
}
