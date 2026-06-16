package mcpserver

import (
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/mcp-midi-controller/internal/diagnostics"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// TestGetHostDiagnostics fills a store with a representative envelope and checks
// the tool surfaces the headline fields in text and the full envelope verbatim
// in structuredContent.
func TestGetHostDiagnostics(t *testing.T) {
	store := diagnostics.NewStore()
	store.Connect("ipad")
	envelope := `{"schemaVersion":1,"source":"ProbeMidiBrain","capturedAt":"2026-06-05T09:11:00Z",` +
		`"transport":{"available":true,"moving":true},` +
		`"musicalContext":{"available":true,"tempo":128.0},` +
		`"audioUnit":{"available":true,"audioUnitName":"ProbeMidiBrain","componentType":"aumi","parameterTree":{"count":3}},` +
		`"midi":{"available":true,"hostMIDIProtocol":"MIDI 2.0","audioUnitMIDIProtocol":"MIDI 1.0"},` +
		`"audioSession":{"sampleRate":48000.0,"outputPorts":["Speaker [Speaker]"]},` +
		`"environment":{"thermalState":"nominal"}}`
	if !store.SetSnapshot([]byte(envelope)) {
		t.Fatal("SetSnapshot rejected a valid envelope")
	}

	s := New(engine.New(device.NewRegistry(), fakeBLE{}), WithDiagnostics(store))

	res := call(t, s.handleGetHostDiagnostics, map[string]any{})
	if res.IsError {
		t.Fatalf("get_host_diagnostics failed: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{"ProbeMidiBrain", "aumi", "128.0 BPM", "MIDI 2.0", "48000 Hz", "nominal"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}

	snap, ok := res.StructuredContent.(diagnostics.Snapshot)
	if !ok {
		t.Fatalf("structuredContent is %T, want diagnostics.Snapshot", res.StructuredContent)
	}
	if snap.Source != "ProbeMidiBrain" || !snap.Connected {
		t.Fatalf("snapshot metadata wrong: %+v", snap)
	}
	if len(snap.Diagnostics) == 0 {
		t.Fatal("structuredContent missing the full diagnostics envelope")
	}
}

// TestGetHostDiagnosticsEmpty checks the tool gives a helpful message before any
// extension has reported.
func TestGetHostDiagnosticsEmpty(t *testing.T) {
	s := New(engine.New(device.NewRegistry(), fakeBLE{}), WithDiagnostics(diagnostics.NewStore()))
	res := call(t, s.handleGetHostDiagnostics, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "no host diagnostics yet") {
		t.Fatalf("unexpected empty message: %s", resultText(res))
	}
}
