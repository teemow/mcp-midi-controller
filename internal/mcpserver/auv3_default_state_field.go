package mcpserver

// Tier 1 of docs/auv3-state-authoring.md: field-level mutation of a structured
// text entry. capture/get classify a fullState leaf into editable `text` (JSON
// or a JUCE-style XML body, optionally behind a base64 binary `prefix`);
// set_auv3_default_state_field edits one path inside that body and leaves
// everything else — sibling fields and the binary prefix — untouched. JSON edits
// go through tidwall/sjson (it rewrites only the addressed path, preserving key
// order and formatting); XML edits go through beevik/etree. Opaque base64 leaves
// have no addressable structure and are rejected.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/beevik/etree"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/teemow/mcp-midi-controller/internal/device"
)

func (s *Server) handleSetAUv3DefaultStateField(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID     string          `json:"id"`
		Key    string          `json:"key"`
		Path   string          `json:"path"`
		Value  json.RawMessage `json:"value"`
		Delete bool            `json:"delete"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	statePath, perr := defaultStatePath(args.ID)
	if perr != nil {
		return textResult(perr.Error(), true), nil
	}
	if strings.TrimSpace(args.Key) == "" {
		return textResult("key is required (the fullState key to edit)", true), nil
	}
	if strings.TrimSpace(args.Path) == "" {
		return textResult("path is required (the field path within the entry)", true), nil
	}
	if !args.Delete && len(args.Value) == 0 {
		return textResult("value is required unless delete is true", true), nil
	}

	def, err := loadAUv3DefaultState(statePath)
	if err != nil {
		return textResult("read default state: "+err.Error(), true), nil
	}
	entry, ok := def.State[args.Key]
	if !ok {
		return textResult(fmt.Sprintf("no fullState key %q in default state %q (see get_auv3_default_state)", args.Key, args.ID), true), nil
	}

	newEntry, format, eerr := editStateEntryField(entry, args.Path, args.Value, args.Delete)
	if eerr != nil {
		return textResult(eerr.Error(), true), nil
	}
	def.State[args.Key] = newEntry
	if verr := def.Validate(); verr != nil {
		return textResult("edited default state is invalid: "+verr.Error(), true), nil
	}
	if werr := writeAUv3DefaultState(statePath, def); werr != nil {
		return textResult(werr.Error(), true), nil
	}

	verb := "set"
	if args.Delete {
		verb = "deleted"
	}
	body := newEntry.Text
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s path %q in %s/%s (%s)\n", verb, args.Key, args.Path, args.ID, args.Key, format)
	fmt.Fprintf(&b, "--- %s now (%dB) ---\n%s\n", args.Key, len(body), truncateForReport(body, 4000))
	structured := map[string]any{
		"id":     strings.TrimSpace(args.ID),
		"key":    args.Key,
		"path":   args.Path,
		"format": format,
		"text":   body,
	}
	return structResult(b.String(), structured), nil
}

// editStateEntryField applies a single set-or-delete at path inside a structured
// text entry, returning the updated entry, the detected format ("json"/"xml"),
// or an error. The binary prefix (if any) is preserved verbatim; only the text
// body is rewritten. Opaque (base64) and unstructured text entries are rejected.
func editStateEntryField(e device.StateEntry, path string, value json.RawMessage, del bool) (device.StateEntry, string, error) {
	if e.Base64 != "" {
		return e, "", fmt.Errorf("entry is opaque base64 (no addressable structure); recapture or replace it with set_auv3_default_state")
	}
	body := e.Text
	if strings.TrimSpace(body) == "" {
		return e, "", fmt.Errorf("entry has no text body to edit")
	}

	// Detect the format from the body: if the whole body parses as JSON it is
	// edited as JSON, otherwise it is treated as XML (an unparseable body fails
	// in editXMLField with a clear "neither JSON nor XML" error).
	switch {
	case gjson.Valid(body):
		out, err := editJSONField(body, path, value, del)
		if err != nil {
			return e, "json", err
		}
		e.Text = out
		return e, "json", nil
	default:
		out, err := editXMLField(body, path, value, del)
		if err != nil {
			return e, "xml", err
		}
		e.Text = out
		return e, "xml", nil
	}
}

// editJSONField sets or deletes a gjson/sjson dot-path inside a JSON document,
// rewriting only the addressed path (key order and formatting of the rest are
// preserved). value is the raw JSON to store (any JSON type).
func editJSONField(body, path string, value json.RawMessage, del bool) (string, error) {
	if del {
		out, err := sjson.Delete(body, path)
		if err != nil {
			return "", fmt.Errorf("delete json path %q: %w", path, err)
		}
		return out, nil
	}
	raw := strings.TrimSpace(string(value))
	if !gjson.Valid(raw) {
		return "", fmt.Errorf("value is not valid JSON: %s", raw)
	}
	out, err := sjson.SetRaw(body, path, raw)
	if err != nil {
		return "", fmt.Errorf("set json path %q: %w", path, err)
	}
	return out, nil
}

// editXMLField navigates an XML body by a slash path and sets or deletes an
// attribute or element. Path grammar (relative to the root element's children):
//
//	Section/PARAM[@id=cutoff]/@value   -> attribute on the matched element
//	Section/PARAM[2]/@value            -> 0-based index among same-named siblings
//	Section/Note                       -> the element's text content
//	@enabled                           -> an attribute on the root element
//
// A trailing "@name" segment targets an attribute; otherwise the resolved
// element's text is the target. value must be a JSON scalar (string/number/bool)
// for set; objects/arrays are rejected (XML attrs/text are scalar).
func editXMLField(body, path string, value json.RawMessage, del bool) (string, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(body); err != nil {
		return "", fmt.Errorf("entry text is neither valid JSON nor XML: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return "", fmt.Errorf("xml entry has no root element")
	}

	steps, attr, err := parseXMLPath(path)
	if err != nil {
		return "", err
	}
	cur := root
	for i, st := range steps {
		child := selectXMLChild(cur, st)
		if child == nil {
			return "", fmt.Errorf("xml path segment %d (%q) matched no element", i+1, st.raw)
		}
		cur = child
	}

	if attr != "" {
		if del {
			if cur.RemoveAttr(attr) == nil {
				return "", fmt.Errorf("attribute @%s not found", attr)
			}
		} else {
			scalar, serr := xmlScalar(value)
			if serr != nil {
				return "", serr
			}
			cur.CreateAttr(attr, scalar)
		}
	} else {
		switch {
		case del:
			parent := cur.Parent()
			if parent == nil {
				return "", fmt.Errorf("cannot delete the root element")
			}
			parent.RemoveChild(cur)
		default:
			scalar, serr := xmlScalar(value)
			if serr != nil {
				return "", serr
			}
			cur.SetText(scalar)
		}
	}

	out, err := doc.WriteToString()
	if err != nil {
		return "", fmt.Errorf("re-encode xml: %w", err)
	}
	return out, nil
}

// xmlStep is one element-selection segment of an XML path.
type xmlStep struct {
	raw      string
	name     string
	index    int    // -1 when not index-selected
	predAttr string // attribute-equality predicate key ("" when none)
	predVal  string
}

// parseXMLPath splits an XML path into element steps plus an optional trailing
// attribute target (a final "@name" segment).
func parseXMLPath(path string) (steps []xmlStep, attr string, err error) {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return nil, "", fmt.Errorf("empty path segment in %q", path)
		}
		if strings.HasPrefix(seg, "@") {
			if i != len(segs)-1 {
				return nil, "", fmt.Errorf("attribute segment %q must be last", seg)
			}
			attr = strings.TrimPrefix(seg, "@")
			if attr == "" {
				return nil, "", fmt.Errorf("empty attribute name in %q", path)
			}
			return steps, attr, nil
		}
		st := xmlStep{raw: seg, index: -1}
		if open := strings.IndexByte(seg, '['); open >= 0 {
			if !strings.HasSuffix(seg, "]") {
				return nil, "", fmt.Errorf("malformed predicate in segment %q", seg)
			}
			st.name = seg[:open]
			pred := seg[open+1 : len(seg)-1]
			if strings.HasPrefix(pred, "@") {
				kv := strings.SplitN(strings.TrimPrefix(pred, "@"), "=", 2)
				if len(kv) != 2 || kv[0] == "" {
					return nil, "", fmt.Errorf("predicate %q must be [@attr=value]", seg)
				}
				st.predAttr = kv[0]
				st.predVal = strings.Trim(kv[1], `"'`)
			} else {
				n, cerr := strconv.Atoi(pred)
				if cerr != nil || n < 0 {
					return nil, "", fmt.Errorf("index predicate %q must be a non-negative integer", seg)
				}
				st.index = n
			}
		} else {
			st.name = seg
		}
		if st.name == "" {
			return nil, "", fmt.Errorf("empty element name in segment %q", seg)
		}
		steps = append(steps, st)
	}
	return steps, "", nil
}

// selectXMLChild returns the child of parent matching one path step, or nil.
func selectXMLChild(parent *etree.Element, st xmlStep) *etree.Element {
	cands := parent.SelectElements(st.name)
	switch {
	case st.predAttr != "":
		for _, c := range cands {
			if c.SelectAttrValue(st.predAttr, "") == st.predVal {
				return c
			}
		}
		return nil
	case st.index >= 0:
		if st.index < len(cands) {
			return cands[st.index]
		}
		return nil
	default:
		if len(cands) > 0 {
			return cands[0]
		}
		return nil
	}
}

// xmlScalar renders a JSON scalar value as the string an XML attribute/text can
// hold, preserving the user's numeric literal. Objects and arrays are rejected.
func xmlScalar(value json.RawMessage) (string, error) {
	s := strings.TrimSpace(string(value))
	switch {
	case s == "" || s == "null":
		return "", nil
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		return "", fmt.Errorf("xml attribute/text needs a scalar value, got %s", s)
	case strings.HasPrefix(s, `"`):
		var str string
		if err := json.Unmarshal(value, &str); err != nil {
			return "", fmt.Errorf("decode string value: %w", err)
		}
		return str, nil
	default:
		return s, nil
	}
}

// truncateForReport caps a body for human-readable tool output, cutting on a
// rune boundary so the truncated tail stays valid UTF-8.
func truncateForReport(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more bytes)", len(s)-cut)
}
