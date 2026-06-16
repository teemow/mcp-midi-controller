package mcpserver

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

func TestEditJSONField(t *testing.T) {
	base := device.StateEntry{Text: `{"host":"a","gain":1}`}

	t.Run("set string preserves siblings and order", func(t *testing.T) {
		got, format, err := editStateEntryField(base, "host", []byte(`"b"`), false)
		if err != nil || format != "json" {
			t.Fatalf("format=%q err=%v", format, err)
		}
		if got.Text != `{"host":"b","gain":1}` {
			t.Fatalf("text = %q", got.Text)
		}
	})

	t.Run("set number", func(t *testing.T) {
		got, _, err := editStateEntryField(base, "gain", []byte(`2`), false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != `{"host":"a","gain":2}` {
			t.Fatalf("text = %q", got.Text)
		}
	})

	t.Run("delete removes the key", func(t *testing.T) {
		got, _, err := editStateEntryField(base, "gain", nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != `{"host":"a"}` {
			t.Fatalf("text = %q", got.Text)
		}
	})

	t.Run("append into array", func(t *testing.T) {
		arr := device.StateEntry{Text: `{"v":[1,2]}`}
		got, _, err := editStateEntryField(arr, "v.-1", []byte(`3`), false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != `{"v":[1,2,3]}` {
			t.Fatalf("text = %q", got.Text)
		}
	})

	t.Run("invalid value json is rejected", func(t *testing.T) {
		if _, _, err := editStateEntryField(base, "host", []byte(`not json`), false); err == nil {
			t.Fatal("expected error for invalid value")
		}
	})
}

func TestEditXMLField(t *testing.T) {
	body := `<Preset><PARAM id="cutoff" value="0.5"/><PARAM id="res" value="0.2"/></Preset>`

	t.Run("set attr by predicate, sibling untouched", func(t *testing.T) {
		got, format, err := editStateEntryField(device.StateEntry{Text: body}, "PARAM[@id=cutoff]/@value", []byte(`"0.8"`), false)
		if err != nil || format != "xml" {
			t.Fatalf("format=%q err=%v", format, err)
		}
		if !strings.Contains(got.Text, `id="cutoff" value="0.8"`) {
			t.Fatalf("cutoff not set: %s", got.Text)
		}
		if !strings.Contains(got.Text, `id="res" value="0.2"`) {
			t.Fatalf("res sibling changed: %s", got.Text)
		}
	})

	t.Run("set attr by index", func(t *testing.T) {
		got, _, err := editStateEntryField(device.StateEntry{Text: body}, "PARAM[1]/@value", []byte(`0.9`), false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, `id="res" value="0.9"`) {
			t.Fatalf("index target wrong: %s", got.Text)
		}
	})

	t.Run("delete attribute", func(t *testing.T) {
		got, _, err := editStateEntryField(device.StateEntry{Text: body}, "PARAM[@id=res]/@value", nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Text, `id="res" value=`) {
			t.Fatalf("attribute not deleted: %s", got.Text)
		}
	})

	t.Run("set element text", func(t *testing.T) {
		doc := `<Preset><Name>old</Name></Preset>`
		got, _, err := editStateEntryField(device.StateEntry{Text: doc}, "Name", []byte(`"new"`), false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, `<Name>new</Name>`) {
			t.Fatalf("text not set: %s", got.Text)
		}
	})

	t.Run("set root attribute", func(t *testing.T) {
		doc := `<Preset version="1"/>`
		got, _, err := editStateEntryField(device.StateEntry{Text: doc}, "@version", []byte(`"2"`), false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, `version="2"`) {
			t.Fatalf("root attr not set: %s", got.Text)
		}
	})

	t.Run("missing element segment errors", func(t *testing.T) {
		if _, _, err := editStateEntryField(device.StateEntry{Text: body}, "PARAM[@id=none]/@value", []byte(`"x"`), false); err == nil {
			t.Fatal("expected no-match error")
		}
	})

	t.Run("preserves the binary prefix", func(t *testing.T) {
		prefix := base64.StdEncoding.EncodeToString([]byte("VC2!"))
		e := device.StateEntry{Prefix: prefix, Text: body}
		got, _, err := editStateEntryField(e, "PARAM[@id=cutoff]/@value", []byte(`"0.1"`), false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Prefix != prefix {
			t.Fatalf("prefix changed: %q", got.Prefix)
		}
		raw, berr := got.Bytes()
		if berr != nil {
			t.Fatal(berr)
		}
		if !strings.HasPrefix(string(raw), "VC2!") {
			t.Fatalf("prefix not re-prepended: %q", string(raw)[:8])
		}
	})
}

func TestEditStateEntryFieldRejectsOpaque(t *testing.T) {
	e := device.StateEntry{Base64: base64.StdEncoding.EncodeToString([]byte{0x00, 0x01, 0x02})}
	if _, _, err := editStateEntryField(e, "anything", []byte(`1`), false); err == nil {
		t.Fatal("expected base64 entry to be rejected")
	}
	if _, _, err := editStateEntryField(device.StateEntry{Text: "just words"}, "x", []byte(`1`), false); err == nil {
		t.Fatal("expected unstructured text to be rejected")
	}
}

// TestSetFieldToolRoundTrip exercises the MCP tool: create a JSON default,
// edit a field, and confirm get reflects it.
func TestSetFieldToolRoundTrip(t *testing.T) {
	s := newAUMServer(t)

	if res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "brain",
		"component": map[string]any{"type": "aumi", "subtype": "brn1", "manufacturer": "Acme"},
		"state": map[string]any{
			"probeMidiBrainConfig": map[string]any{"text": `{"host":"old:7800","controlEnabled":false}`},
		},
	}); res.IsError {
		t.Fatalf("set failed: %s", resultText(res))
	}

	res := call(t, s.handleSetAUv3DefaultStateField, map[string]any{
		"id": "brain", "key": "probeMidiBrainConfig", "path": "host", "value": "new:7800",
	})
	if res.IsError {
		t.Fatalf("set field failed: %s", resultText(res))
	}

	res = call(t, s.handleSetAUv3DefaultStateField, map[string]any{
		"id": "brain", "key": "probeMidiBrainConfig", "path": "controlEnabled", "value": true,
	})
	if res.IsError {
		t.Fatalf("set bool field failed: %s", resultText(res))
	}

	res = call(t, s.handleGetAUv3DefaultState, map[string]any{"id": "brain"})
	text := resultText(res)
	if !strings.Contains(text, "new:7800") || !strings.Contains(text, "controlEnabled") {
		t.Fatalf("get does not reflect edits:\n%s", text)
	}
}

func TestSetFieldToolMissingKey(t *testing.T) {
	s := newAUMServer(t)
	if res := call(t, s.handleSetAUv3DefaultState, map[string]any{
		"id":        "brain",
		"component": map[string]any{"type": "aumi", "subtype": "brn1", "manufacturer": "Acme"},
		"state":     map[string]any{"cfg": map[string]any{"text": `{"a":1}`}},
	}); res.IsError {
		t.Fatalf("set failed: %s", resultText(res))
	}
	res := call(t, s.handleSetAUv3DefaultStateField, map[string]any{
		"id": "brain", "key": "nope", "path": "a", "value": 1,
	})
	if !res.IsError || !strings.Contains(resultText(res), "no fullState key") {
		t.Fatalf("expected missing-key error, got: %s", resultText(res))
	}
}
