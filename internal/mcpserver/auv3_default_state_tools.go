package mcpserver

// User-defined per-audio-unit default states (docs/auv3-state-authoring.md): a
// persistent, human-readable YAML per audio unit, stored rig-as-code under
// config.AUv3DefaultStatesDir(), that the AUM author can apply automatically to
// any node of that unit. These tools let an agent help the user capture and
// inspect them. capture_auv3_default_state harvests a real node's fullState from
// a staged session and classifies each leaf into the richest round-trip-safe
// encoding (text / text+prefix / base64), so opaque-looking third-party state
// (JUCE XML, JSON synths) lands as editable text instead of blind base64.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/sanitize"
)

func (s *Server) registerAUv3DefaultStateTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "capture_auv3_default_state",
		Description: "Capture a hosted AUv3 node's saved fullState from a staged AUM session into a persistent, per-audio-unit default state (YAML under the config dir). " +
			"This is how a user obtains opaque third-party plugin state they cannot hand-write. Each fullState key is classified into the richest round-trip-safe encoding: readable plugins (our own JSON, JUCE XML, JSON synths) land as editable `text`, truly opaque blobs (FabFilter, iSEM ISEMPatch) as `base64`. " +
			"The audio-unit identity is taken from the node's component tuple; the file is matched to nodes by that tuple on author. An identity-only node (no saved state) captures nothing.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id (filename without .aumproj). See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"channel": {"type": "integer", "description": "Mixer channel index of the node."},
				"slot": {"type": "integer", "description": "Slot index of the node within the channel chain."},
				"out_id": {"type": "string", "description": "Default-state file id (defaults to the sanitized component subtype)."},
				"name": {"type": "string", "description": "Optional human label stored in the file."},
				"merge": {"type": "boolean", "description": "Merge captured keys into an existing file for this audio unit (default false = replace)."}
			},
			"required": ["channel", "slot"]
		}`),
	}, s.handleCaptureAUv3DefaultState)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_auv3_default_states",
		Description: "List the user-defined per-audio-unit default states (YAML files under the config dir): id, component tuple, human name, and per-key encoding (text/base64) counts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListAUv3DefaultStates)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_auv3_default_state",
		Description: "Get one user-defined audio-unit default state: its component tuple, name, and every fullState key with its encoding, size, and (for text entries) the readable value — so an agent can inspect and help the user edit it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Default-state file id (its filename without .yaml). See list_auv3_default_states."}
			},
			"required": ["id"]
		}`),
	}, s.handleGetAUv3DefaultState)

	s.mcp.AddTool(&mcp.Tool{
		Name: "set_auv3_default_state",
		Description: "Create or edit a user-defined per-audio-unit default state by hand (the complement of capture_auv3_default_state). " +
			"Provide the component tuple and one or more fullState keys, each in exactly one encoding: `text` (UTF-8, e.g. an edited JUCE XML / JSON body), `text`+`prefix` (text body behind a base64 binary header), or `base64` (opaque binary). " +
			"This is the write side of the get→edit→set loop. Once saved, the AUM author applies it automatically to any node whose component tuple matches.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Default-state file id (its filename without .yaml). Created if absent."},
				"component": {
					"type": "object",
					"description": "Audio-unit identity tuple the default is matched on.",
					"properties": {
						"type": {"type": "string"},
						"subtype": {"type": "string"},
						"manufacturer": {"type": "string"},
						"manufacturer_name": {"type": "string"}
					},
					"required": ["type", "subtype", "manufacturer"]
				},
				"name": {"type": "string", "description": "Optional human label."},
				"state": {
					"type": "object",
					"description": "fullState keys -> {text|prefix|base64}. Identity keys (type/subtype/manufacturer/version) are rejected; they are re-derived from the component.",
					"additionalProperties": {
						"type": "object",
						"properties": {
							"text": {"type": "string"},
							"prefix": {"type": "string", "description": "base64 of a leading binary header that precedes text."},
							"base64": {"type": "string"}
						}
					}
				},
				"merge": {"type": "boolean", "description": "Merge the given keys into an existing file for this id (default false = replace its state)."}
			},
			"required": ["id", "component", "state"]
		}`),
	}, s.handleSetAUv3DefaultState)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "delete_auv3_default_state",
		Description: "Delete a user-defined per-audio-unit default state file by id. After this, nodes of that audio unit author identity-only again (unless a per-call state is given).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Default-state file id (its filename without .yaml). See list_auv3_default_states."}
			},
			"required": ["id"]
		}`),
	}, s.handleDeleteAUv3DefaultState)

	s.mcp.AddTool(&mcp.Tool{
		Name: "set_auv3_default_state_field",
		Description: "Edit one field inside a structured (JSON or JUCE-style XML) `text` entry of a default state, leaving every other field and the binary prefix untouched (capture-and-mutate, docs/auv3-state-authoring.md Tier 1). " +
			"The entry's format is auto-detected. JSON paths use gjson/sjson dot syntax (e.g. `host`, `mixer.0.gain`, `voices.-1` to append). " +
			"XML paths are slash-separated element steps relative to the root's children, each optionally `Name[index]` (0-based) or `Name[@attr=value]`, with a trailing `@attr` to target an attribute (else the element's text); e.g. `PARAM[@id=cutoff]/@value`. " +
			"Opaque base64 entries have no addressable structure and are rejected (use set_auv3_default_state to replace them wholesale).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Default-state file id. See list_auv3_default_states."},
				"key": {"type": "string", "description": "The fullState key whose value to edit. See get_auv3_default_state."},
				"path": {"type": "string", "description": "Field path within the entry (JSON dot path, or XML element/attribute path)."},
				"value": {"description": "New value. For JSON: any JSON value. For XML: a scalar (string/number/bool). Omit when delete is true."},
				"delete": {"type": "boolean", "description": "Delete the field at path instead of setting it (default false)."}
			},
			"required": ["id", "key", "path"]
		}`),
	}, s.handleSetAUv3DefaultStateField)
}

func (s *Server) handleCaptureAUv3DefaultState(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		Channel   int    `json:"channel"`
		Slot      int    `json:"slot"`
		OutID     string `json:"out_id"`
		Name      string `json:"name"`
		Merge     bool   `json:"merge"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}

	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return textResult("read session: "+err.Error(), true), nil
	}
	sess, err := aum.Open(data)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}

	node, ok := hostedNodeAt(sess, args.Channel, args.Slot)
	if !ok {
		return textResult(fmt.Sprintf("no hosted AUv3 node at channel %d slot %d (use get_aum_session to see the layout)", args.Channel, args.Slot), true), nil
	}
	doc, err := sess.NodeAuStateDoc(args.Channel, args.Slot)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if len(doc) == 0 {
		return textResult(fmt.Sprintf("node %s at channel %d slot %d is identity-only (no saved fullState to capture)", node.ComponentName, args.Channel, args.Slot), true), nil
	}

	entries := device.ClassifyStateDoc(doc)
	def := device.AUv3DefaultState{
		Component: *node.Component,
		Name:      device.FirstNonEmpty(args.Name, node.ComponentName),
		State:     entries,
	}

	id := device.FirstNonEmpty(sanitize.ID(args.OutID), sanitize.ID(node.Component.Subtype), sanitize.ID(node.ComponentName))
	if id == "" {
		return textResult("cannot derive a default-state id (empty subtype/name); pass out_id", true), nil
	}
	outPath := filepath.Join(config.AUv3DefaultStatesDir(), id+".yaml")

	if args.Merge {
		if existing, lerr := loadAUv3DefaultState(outPath); lerr == nil {
			merged := existing.State
			if merged == nil {
				merged = map[string]device.StateEntry{}
			}
			for k, v := range def.State {
				merged[k] = v
			}
			def.State = merged
			if def.Name == "" {
				def.Name = existing.Name
			}
		}
	}

	if err := def.Validate(); err != nil {
		return textResult("captured state is invalid: "+err.Error(), true), nil
	}
	if err := writeAUv3DefaultState(outPath, def); err != nil {
		return textResult(err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "captured %s (%s/%s/%s) -> %s\n",
		def.Name, def.Component.Type, def.Component.Subtype, def.Component.Manufacturer, outPath)
	keys := sortedStateEntryKeys(def.State)
	text, b64 := 0, 0
	for _, k := range keys {
		e := def.State[k]
		raw, _ := e.Bytes()
		kind := "base64"
		switch {
		case e.Base64 != "":
			b64++
		case e.Prefix != "":
			kind = "text+prefix"
			text++
		default:
			kind = "text"
			text++
		}
		fmt.Fprintf(&b, "  %-26s %-12s %6dB\n", k, kind, len(raw))
	}
	fmt.Fprintf(&b, "%d key(s): %d editable text, %d opaque base64\n", len(keys), text, b64)

	structured := map[string]any{
		"id":        id,
		"path":      outPath,
		"component": def.Component,
		"name":      def.Name,
		"keys":      keys,
		"text":      text,
		"base64":    b64,
	}
	return structResult(b.String(), structured), nil
}

func (s *Server) handleListAUv3DefaultStates(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := config.AUv3DefaultStatesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return structResult("no user-defined default states yet (capture_auv3_default_state creates them)", []any{}), nil
		}
		return textResult("read default-states dir: "+err.Error(), true), nil
	}
	type row struct {
		ID        string                `json:"id"`
		Component device.ProbeComponent `json:"component"`
		Name      string                `json:"name,omitempty"`
		Keys      int                   `json:"keys"`
		Text      int                   `json:"text"`
		Base64    int                   `json:"base64"`
	}
	var rows []row
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		def, lerr := loadAUv3DefaultState(filepath.Join(dir, e.Name()))
		if lerr != nil {
			continue
		}
		r := row{
			ID:        strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml"),
			Component: def.Component,
			Name:      def.Name,
			Keys:      len(def.State),
		}
		for _, se := range def.State {
			if se.Base64 != "" {
				r.Base64++
			} else {
				r.Text++
			}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	var b strings.Builder
	fmt.Fprintf(&b, "%d user-defined default state(s):\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-20s %s/%s/%s  %q  %d key(s) (%d text, %d base64)\n",
			r.ID, r.Component.Type, r.Component.Subtype, r.Component.Manufacturer, r.Name, r.Keys, r.Text, r.Base64)
	}
	return structResult(b.String(), rows), nil
}

func (s *Server) handleGetAUv3DefaultState(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	id := strings.TrimSpace(args.ID)
	path, perr := defaultStatePath(id)
	if perr != nil {
		return textResult(perr.Error(), true), nil
	}
	def, err := loadAUv3DefaultState(path)
	if err != nil {
		return textResult("read default state: "+err.Error(), true), nil
	}

	type keyInfo struct {
		Key      string `json:"key"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
		Text     string `json:"text,omitempty"`
	}
	var infos []keyInfo
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s/%s/%s)\n", def.Name, def.Component.Type, def.Component.Subtype, def.Component.Manufacturer)
	for _, k := range sortedStateEntryKeys(def.State) {
		e := def.State[k]
		raw, berr := e.Bytes()
		if berr != nil {
			return textResult(fmt.Sprintf("state[%q]: %v", k, berr), true), nil
		}
		enc := "base64"
		ki := keyInfo{Key: k, Size: len(raw)}
		switch {
		case e.Base64 != "":
			// opaque binary: enc stays "base64"
		case e.Prefix != "":
			enc = "text+prefix"
			ki.Text = e.Text
		default:
			enc = "text"
			ki.Text = e.Text
		}
		ki.Encoding = enc
		infos = append(infos, ki)
		fmt.Fprintf(&b, "  %-26s %-12s %6dB\n", k, enc, len(raw))
	}

	structured := map[string]any{
		"id":        id,
		"component": def.Component,
		"name":      def.Name,
		"keys":      infos,
	}
	return structResult(b.String(), structured), nil
}

func (s *Server) handleSetAUv3DefaultState(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID        string                       `json:"id"`
		Component device.ProbeComponent        `json:"component"`
		Name      string                       `json:"name"`
		State     map[string]device.StateEntry `json:"state"`
		Merge     bool                         `json:"merge"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, perr := defaultStatePath(args.ID)
	if perr != nil {
		return textResult(perr.Error(), true), nil
	}
	if len(args.State) == 0 {
		return textResult("state is empty (provide at least one fullState key)", true), nil
	}

	def := device.AUv3DefaultState{
		Component: args.Component,
		Name:      args.Name,
		State:     args.State,
	}
	if args.Merge {
		if existing, lerr := loadAUv3DefaultState(path); lerr == nil {
			merged := existing.State
			if merged == nil {
				merged = map[string]device.StateEntry{}
			}
			for k, v := range args.State {
				merged[k] = v
			}
			def.State = merged
			if def.Name == "" {
				def.Name = existing.Name
			}
			if def.Component.Type == "" && def.Component.Subtype == "" && def.Component.Manufacturer == "" {
				def.Component = existing.Component
			}
		}
	}

	if err := def.Validate(); err != nil {
		return textResult("default state is invalid: "+err.Error(), true), nil
	}
	if err := writeAUv3DefaultState(path, def); err != nil {
		return textResult(err.Error(), true), nil
	}

	keys := sortedStateEntryKeys(def.State)
	var b strings.Builder
	fmt.Fprintf(&b, "saved %s (%s/%s/%s) -> %s\n",
		device.FirstNonEmpty(def.Name, "(unnamed)"), def.Component.Type, def.Component.Subtype, def.Component.Manufacturer, path)
	fmt.Fprintf(&b, "%d key(s): %s\n", len(keys), strings.Join(keys, ", "))
	structured := map[string]any{
		"id":        strings.TrimSpace(args.ID),
		"path":      path,
		"component": def.Component,
		"name":      def.Name,
		"keys":      keys,
	}
	return structResult(b.String(), structured), nil
}

func (s *Server) handleDeleteAUv3DefaultState(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, perr := defaultStatePath(args.ID)
	if perr != nil {
		return textResult(perr.Error(), true), nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return textResult(fmt.Sprintf("no default state with id %q", strings.TrimSpace(args.ID)), true), nil
		}
		return textResult("delete default state: "+err.Error(), true), nil
	}
	return structResult(fmt.Sprintf("deleted %s", path), map[string]any{"id": strings.TrimSpace(args.ID), "path": path}), nil
}

// defaultStatePath validates a bare default-state id (no path separators or
// traversal) and returns its YAML path under the config dir.
func defaultStatePath(id string) (string, error) {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(strings.TrimSuffix(id, ".yaml"), ".yml")
	if id == "" || id != filepath.Base(id) || strings.Contains(id, "..") || strings.ContainsRune(id, '/') {
		return "", fmt.Errorf("invalid id (must be a bare default-state id, no path)")
	}
	return filepath.Join(config.AUv3DefaultStatesDir(), id+".yaml"), nil
}

// loadAUv3DefaultStates reads every user-defined default state, skipping
// unreadable ones (a bad file should not block authoring). It is the
// default-state counterpart of loadStagedProbeDumps.
func loadAUv3DefaultStates() []device.AUv3DefaultState {
	dir := config.AUv3DefaultStatesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []device.AUv3DefaultState
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		if def, lerr := loadAUv3DefaultState(filepath.Join(dir, e.Name())); lerr == nil {
			out = append(out, def)
		}
	}
	return out
}

// findDefaultStateForComponent returns the default state whose component tuple
// matches c, or nil. Matching is by the {type,subtype,manufacturer} tuple
// (the same key aum.ComponentMatches uses to link nodes to probes).
func findDefaultStateForComponent(defs []device.AUv3DefaultState, c device.ProbeComponent) *device.AUv3DefaultState {
	for i := range defs {
		if aum.ComponentMatches(c, defs[i].Component) {
			return &defs[i]
		}
	}
	return nil
}

// applyDefaultState merges the user-defined audio-unit default fullState for
// ns.Component into ns.StateDoc, beneath any keys already present — so a
// per-call `state` arg wins, the audio-unit default fills the rest, and an
// audio unit with no default stays identity-only. It is a no-op when no default
// matches; it errors only when a matching default's entries do not resolve
// (e.g. invalid base64), so a broken default the user meant to apply fails
// loudly rather than silently authoring the wrong state.
func applyDefaultState(ns *aum.NodeSpec, defs []device.AUv3DefaultState) error {
	def := findDefaultStateForComponent(defs, ns.Component)
	if def == nil {
		return nil
	}
	doc, err := def.StateDoc()
	if err != nil {
		return fmt.Errorf("default state %s/%s/%s: %w",
			def.Component.Type, def.Component.Subtype, def.Component.Manufacturer, err)
	}
	if len(doc) == 0 {
		return nil
	}
	if ns.StateDoc == nil {
		ns.StateDoc = make(map[string][]byte, len(doc))
	}
	for k, v := range doc {
		if _, ok := ns.StateDoc[k]; ok {
			continue
		}
		ns.StateDoc[k] = v
	}
	return nil
}

// hostedNodeAt returns the hosted AUv3 node (Component != nil) at the given
// channel index and slot, or ok=false.
func hostedNodeAt(sess *aum.Session, channel, slot int) (aum.Node, bool) {
	for _, ch := range sess.Channels() {
		if ch.Index != channel {
			continue
		}
		for _, n := range ch.Nodes {
			if n.Slot == slot && n.Component != nil {
				return n, true
			}
		}
	}
	return aum.Node{}, false
}

func loadAUv3DefaultState(path string) (device.AUv3DefaultState, error) {
	var def device.AUv3DefaultState
	b, err := os.ReadFile(path)
	if err != nil {
		return def, err
	}
	if err := yaml.Unmarshal(b, &def); err != nil {
		return def, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return def, nil
}

func writeAUv3DefaultState(path string, def device.AUv3DefaultState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create default-states dir: %w", err)
	}
	out, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal default state: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write default state: %w", err)
	}
	return nil
}

func isYAMLFile(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml")
}

func sortedStateEntryKeys(m map[string]device.StateEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
