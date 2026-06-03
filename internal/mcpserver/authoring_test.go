package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// fakeBLE is a no-op transport that claims the "blemidi" id so authored
// definitions (which default to blemidi) pass the registered-transport check.
type fakeBLE struct{}

func (fakeBLE) ID() string                                             { return "blemidi" }
func (fakeBLE) Discover(context.Context) ([]transport.Endpoint, error) { return nil, nil }
func (fakeBLE) Pair(context.Context, string) error                     { return nil }
func (fakeBLE) Connect(context.Context, string) error                  { return nil }
func (fakeBLE) Disconnect(context.Context, string) error               { return nil }
func (fakeBLE) Send(context.Context, string, transport.Event) error    { return nil }
func (fakeBLE) Listen(context.Context, string) (<-chan transport.Event, error) {
	ch := make(chan transport.Event)
	return ch, nil
}

func call(t *testing.T, h mcp.ToolHandler, args any) *mcp.CallToolResult {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: b}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestAuthoringRoundTrip(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	reg := device.NewRegistry()
	eng := engine.New(reg, fakeBLE{})
	s := New(eng)

	// Create a draft.
	res := call(t, s.handleCreateDeviceDefinition, map[string]any{
		"id": "mypedal", "name": "My Pedal", "transport": "blemidi",
	})
	if res.IsError {
		t.Fatalf("create failed: %s", resultText(res))
	}

	// Add a CC control.
	res = call(t, s.handleAddControl, map[string]any{
		"device": "mypedal", "name": "level", "type": "cc", "cc": 17,
		"value": map[string]any{"type": "range", "min": 0, "max": 127},
	})
	if res.IsError {
		t.Fatalf("add_control failed: %s", resultText(res))
	}

	// Add a program-change control.
	res = call(t, s.handleAddControl, map[string]any{
		"device": "mypedal", "name": "preset", "type": "program_change",
		"value": map[string]any{"type": "range", "min": 0, "max": 99},
	})
	if res.IsError {
		t.Fatalf("add_control (pc) failed: %s", resultText(res))
	}

	// Save it.
	res = call(t, s.handleSaveDeviceDefinition, map[string]any{"device": "mypedal"})
	if res.IsError {
		t.Fatalf("save failed: %s", resultText(res))
	}
	out := resultText(res)
	if !strings.Contains(out, "AUM mapping cheat-sheet") {
		t.Fatalf("save output missing cheat-sheet:\n%s", out)
	}
	if !strings.Contains(out, "CC 17") {
		t.Fatalf("cheat-sheet missing CC 17:\n%s", out)
	}

	// The file was written.
	path := filepath.Join(cfgDir, "mcp-midi-controller", "devices", "mypedal.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("definition file not written: %v", err)
	}

	// And it hot-loaded into the registry.
	def, ok := reg.Get("mypedal")
	if !ok {
		t.Fatal("definition not registered after save")
	}
	if len(def.Controls) != 2 {
		t.Fatalf("registered def has %d controls, want 2", len(def.Controls))
	}

	// The draft is consumed.
	res = call(t, s.handleSaveDeviceDefinition, map[string]any{"device": "mypedal"})
	if !res.IsError {
		t.Fatal("expected an error saving a consumed draft")
	}
}

func TestAddControlRejectsInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	eng := engine.New(device.NewRegistry(), fakeBLE{})
	s := New(eng)

	if res := call(t, s.handleCreateDeviceDefinition, map[string]any{
		"id": "d", "name": "D", "transport": "blemidi",
	}); res.IsError {
		t.Fatalf("create failed: %s", resultText(res))
	}

	// A CC control without a number must be rejected (and not mutate the draft).
	res := call(t, s.handleAddControl, map[string]any{
		"device": "d", "name": "bad", "type": "cc",
		"value": map[string]any{"type": "range"},
	})
	if !res.IsError {
		t.Fatalf("expected add_control to reject a cc without a number")
	}
}
