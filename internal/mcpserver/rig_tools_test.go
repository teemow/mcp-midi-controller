package mcpserver

import (
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
)

// ccPtr is a small helper for building CC controls in tests.
func ccPtr(n int) *int { return &n }

func rigTestServer(t *testing.T) (*Server, *device.Registry) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := device.NewRegistry()
	def := &device.DeviceType{
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

// TestListDevicesAvailable confirms list_devices with available=true folds in
// the device-type catalog (what was list_definitions): the rig is empty, but
// the catalog lists the loaded type, flagged not-known until a device uses it.
func TestListDevicesAvailable(t *testing.T) {
	s, _ := rigTestServer(t)

	res := call(t, s.handleListDevices, map[string]any{"available": true})
	if res.IsError {
		t.Fatalf("list_devices failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "testpedal") {
		t.Fatalf("text missing device type id:\n%s", resultText(res))
	}

	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want map", res.StructuredContent)
	}
	types, ok := sc["types"].([]deviceTypeSummary)
	if !ok {
		t.Fatalf("types is %T, want []deviceTypeSummary", sc["types"])
	}
	if len(types) != 1 {
		t.Fatalf("got %d types, want 1", len(types))
	}
	if types[0].ID != "testpedal" || types[0].Controls != 2 {
		t.Fatalf("unexpected type summary: %+v", types[0])
	}
	if types[0].Known {
		t.Fatalf("type should not be known (no device uses it yet): %+v", types[0])
	}

	// Without the flag, only the rig (no types) is reported.
	res = call(t, s.handleListDevices, map[string]any{})
	sc = res.StructuredContent.(map[string]any)
	if _, present := sc["types"]; present {
		t.Fatalf("types should be absent without available=true: %#v", sc)
	}
}

// TestDescribeDeviceByType confirms describe_device (what was get_definition)
// resolves a device type id to its full control detail.
func TestDescribeDeviceByType(t *testing.T) {
	s, _ := rigTestServer(t)

	if res := call(t, s.handleDescribeDevice, map[string]any{"device": "nope"}); !res.IsError {
		t.Fatal("expected error for unknown device")
	}

	res := call(t, s.handleDescribeDevice, map[string]any{"device": "testpedal"})
	if res.IsError {
		t.Fatalf("describe_device failed: %s", resultText(res))
	}
	view, ok := res.StructuredContent.(deviceTypeDetail)
	if !ok {
		t.Fatalf("structuredContent is %T, want deviceTypeDetail", res.StructuredContent)
	}
	if view.ID != "testpedal" || len(view.Controls) != 2 {
		t.Fatalf("unexpected view: %+v", view)
	}
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

func TestListDevices(t *testing.T) {
	s, _ := rigTestServer(t)

	// Empty rig: text hint + empty structured list.
	res := call(t, s.handleListDevices, map[string]any{})
	if res.IsError {
		t.Fatalf("list_devices failed: %s", resultText(res))
	}
	if sc, ok := res.StructuredContent.(map[string]any); ok {
		if b, ok := sc["devices"].([]deviceView); !ok || len(b) != 0 {
			t.Fatalf("expected empty devices, got %#v", sc["devices"])
		}
	} else {
		t.Fatalf("structuredContent is %T, want map", res.StructuredContent)
	}

	// Bind a device, then it shows up — with its type and a connection.
	if err := s.eng.Bind(engine.Device{Name: "lead", DeviceID: "testpedal", Endpoint: "ep1", Channel: 3}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	res = call(t, s.handleListDevices, map[string]any{})
	sc := res.StructuredContent.(map[string]any)
	devices := sc["devices"].([]deviceView)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	d := devices[0]
	if d.Name != "lead" || d.Type != "testpedal" || d.Channel != 3 || d.TypeName != "Test Pedal" {
		t.Fatalf("unexpected device view: %+v", d)
	}
	if len(d.Connections) != 1 || d.Connections[0].Transport != "blemidi" || d.Connections[0].Endpoint != "ep1" || d.Connections[0].Channel != 3 {
		t.Fatalf("connection not surfaced: %+v", d.Connections)
	}
	if !strings.Contains(resultText(res), "lead") {
		t.Fatalf("text missing device name:\n%s", resultText(res))
	}

	// And once bound, the catalog flags the type as known (in the rig).
	res = call(t, s.handleListDevices, map[string]any{"available": true})
	types := res.StructuredContent.(map[string]any)["types"].([]deviceTypeSummary)
	if len(types) != 1 || !types[0].Known {
		t.Fatalf("type should be known once a device uses it: %+v", types)
	}
}

// TestDescribeDeviceByName confirms describe_device resolves a device name (the
// rig instance) to its device type's detail, like it does a type id.
func TestDescribeDeviceByName(t *testing.T) {
	s, _ := rigTestServer(t)
	if err := s.eng.Bind(engine.Device{Name: "lead", DeviceID: "testpedal", Endpoint: "ep1", Channel: 0}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	res := call(t, s.handleDescribeDevice, map[string]any{"device": "lead"})
	if res.IsError {
		t.Fatalf("describe_device by name failed: %s", resultText(res))
	}
	if view := res.StructuredContent.(deviceTypeDetail); view.ID != "testpedal" {
		t.Fatalf("resolved to %q, want testpedal", view.ID)
	}
}
