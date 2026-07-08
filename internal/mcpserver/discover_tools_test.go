package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-transport"
)

// fakeDiscoverBLE is a blemidi transport that returns a fixed set of endpoints
// from Discover, so the endpoint source of discover_devices has something to
// surface in a unit test.
type fakeDiscoverBLE struct{ eps []transport.Endpoint }

func (f fakeDiscoverBLE) ID() string { return "blemidi" }
func (f fakeDiscoverBLE) Discover(context.Context) ([]transport.Endpoint, error) {
	return f.eps, nil
}
func (fakeDiscoverBLE) Pair(context.Context, string) error       { return nil }
func (fakeDiscoverBLE) Connect(context.Context, string) error    { return nil }
func (fakeDiscoverBLE) Disconnect(context.Context, string) error { return nil }
func (fakeDiscoverBLE) Send(context.Context, string, transport.Event) error {
	return nil
}
func (fakeDiscoverBLE) Listen(context.Context, string) (<-chan transport.Event, error) {
	ch := make(chan transport.Event)
	return ch, nil
}

func TestDiscoverDevicesAggregatesAllSources(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	reg := device.NewRegistry()
	// A loaded type the discovered endpoint name should match by name.
	if err := reg.AddDefinition(&device.DeviceType{ID: "h90", Name: "H90", Transport: "blemidi"}); err != nil {
		t.Fatalf("seed h90 type: %v", err)
	}
	ble := fakeDiscoverBLE{eps: []transport.Endpoint{
		{ID: "10:2E:AA", Name: "H90 Pedal", Transport: "blemidi", Paired: true},
		{ID: "10:2E:BB", Name: "Unknown Box", Transport: "blemidi"},
	}}
	s := New(engine.New(reg, ble, fakeAUv3{}))

	// Catalog source: a staged probe (gtr1) → an importable device type.
	stageProbe(t)

	// Session source: author a session hosting that probe, wired to the
	// convention so the node's channel is inferable.
	if res := call(t, s.handleAuthorAUMSession, map[string]any{
		"out_id": "rig",
		"channels": []any{
			map[string]any{"kind": "audio", "title": "Gitarre", "nodes": []any{map[string]any{"probe_id": "gtr1"}}},
			map[string]any{"kind": "audio", "title": "Master"},
		},
		"convention": map[string]any{"channel": 3, "start_cc": 30},
	}); res.IsError {
		t.Fatalf("author failed: %s", resultText(res))
	}

	res := call(t, s.handleDiscoverDevices, map[string]any{})
	if res.IsError {
		t.Fatalf("discover_devices failed: %s", resultText(res))
	}

	structured, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content is %T, want map", res.StructuredContent)
	}
	devs, ok := structured["devices"].([]discoveredDevice)
	if !ok {
		t.Fatalf("devices is %T, want []discoveredDevice", structured["devices"])
	}

	var endpoint, catalog, session []discoveredDevice
	for _, d := range devs {
		switch d.Source {
		case "endpoint":
			endpoint = append(endpoint, d)
		case "catalog":
			catalog = append(catalog, d)
		case "session":
			session = append(session, d)
		default:
			t.Fatalf("unexpected source %q", d.Source)
		}
	}

	if len(endpoint) != 2 {
		t.Fatalf("got %d endpoint candidates, want 2", len(endpoint))
	}
	// The named endpoint matched the loaded h90 type; the unknown one did not.
	var matched, unmatched *discoveredDevice
	for i := range endpoint {
		if endpoint[i].SuggestedType == "h90" {
			matched = &endpoint[i]
		} else {
			unmatched = &endpoint[i]
		}
	}
	if matched == nil || !matched.TypeKnown || matched.Transport != "blemidi" || matched.Endpoint != "10:2E:AA" {
		t.Fatalf("H90 endpoint did not suggest the h90 type with bind coords: %+v", matched)
	}
	if unmatched == nil || unmatched.SuggestedType != "" {
		t.Fatalf("unknown endpoint should suggest no type: %+v", unmatched)
	}

	// Catalog: the staged probe is surfaced as an auv3midi candidate; its type
	// is not yet loaded (import not run).
	if len(catalog) != 1 {
		t.Fatalf("got %d catalog candidates, want 1", len(catalog))
	}
	if catalog[0].SuggestedType != "gtr1" || catalog[0].TypeKnown {
		t.Fatalf("catalog candidate wrong: %+v", catalog[0])
	}
	if catalog[0].Transport != "auv3midi" || catalog[0].Endpoint != "brain" {
		t.Fatalf("catalog candidate missing auv3midi coords: %+v", catalog[0])
	}

	// Session: the hosted node matched gtr1, on its convention channel (send 3).
	if len(session) != 1 {
		t.Fatalf("got %d session candidates, want 1", len(session))
	}
	if session[0].SuggestedType != "gtr1" || session[0].Channel != 3 || session[0].SessionID != "rig" {
		t.Fatalf("session candidate wrong: %+v", session[0])
	}

	text := resultText(res)
	for _, want := range []string{"transport endpoints", "AUv3 catalog", "AUM session nodes"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q section:\n%s", want, text)
		}
	}
}

func TestDiscoverDevicesEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := New(engine.New(device.NewRegistry(), fakeBLE{}))

	res := call(t, s.handleDiscoverDevices, map[string]any{})
	if res.IsError {
		t.Fatalf("discover_devices failed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "no devices discovered") {
		t.Fatalf("expected empty discovery message:\n%s", resultText(res))
	}
}
