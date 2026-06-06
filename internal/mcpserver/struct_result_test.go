package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStructResultEmbedsJSON verifies structResult both keeps the structured
// payload for compliant clients AND inlines a serialized-JSON text block so
// text-only clients (e.g. Cursor, which ignores structuredContent) still feed
// the full data to the model.
func TestStructResultEmbedsJSON(t *testing.T) {
	type payload struct {
		Note   string `json:"note"`
		F0Hz   int    `json:"f0_hz"`
		Hidden bool   `json:"hidden_field"`
	}
	want := payload{Note: "A4", F0Hz: 440, Hidden: true}

	res := structResult("detected A4", want)

	// StructuredContent is preserved verbatim for clients that read it.
	if got, ok := res.StructuredContent.(payload); !ok || got != want {
		t.Fatalf("StructuredContent = %#v, want %#v", res.StructuredContent, want)
	}

	// The human summary and the serialized JSON both reach a text-only client.
	text := resultText(res)
	if !strings.Contains(text, "detected A4") {
		t.Fatalf("text missing human summary:\n%s", text)
	}
	// hidden_field is absent from the human summary but must be recoverable from
	// the inlined JSON — this is the whole point of the mitigation.
	if !strings.Contains(text, "hidden_field") {
		t.Fatalf("text missing inlined structured JSON (hidden_field):\n%s", text)
	}

	// The inlined block must be valid, complete JSON the model can parse.
	idx := strings.Index(text, "{")
	if idx < 0 {
		t.Fatalf("no JSON object found in text:\n%s", text)
	}
	var round payload
	if err := json.Unmarshal([]byte(text[idx:]), &round); err != nil {
		t.Fatalf("inlined JSON does not round-trip: %v\n%s", err, text[idx:])
	}
	if round != want {
		t.Fatalf("round-tripped JSON = %#v, want %#v", round, want)
	}
}

// TestStructResultNoEmbed verifies the escape hatch keeps the structured
// payload off the text channel (used for large/binary data like base64 PCM).
func TestStructResultNoEmbed(t *testing.T) {
	res := structResultNoEmbed("audio clip: 1024 frames", map[string]any{
		"pcm_base64": "QUJDRA==",
		"samples":    1024,
	})

	if len(res.Content) != 1 {
		t.Fatalf("expected exactly one content block, got %d", len(res.Content))
	}
	if _, ok := res.Content[0].(*mcp.TextContent); !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	if strings.Contains(resultText(res), "pcm_base64") {
		t.Fatalf("no-embed result must not inline the structured payload:\n%s", resultText(res))
	}
	if res.StructuredContent == nil {
		t.Fatal("StructuredContent must still be set for programmatic clients")
	}
}

// TestStructResultNilStructured verifies a nil payload does not append an
// empty/garbage JSON block.
func TestStructResultNilStructured(t *testing.T) {
	res := structResult("nothing here", nil)
	if len(res.Content) != 1 {
		t.Fatalf("expected one content block for nil structured, got %d", len(res.Content))
	}
}
