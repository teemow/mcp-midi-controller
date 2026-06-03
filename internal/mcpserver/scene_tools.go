package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/scene"
)

// registerSceneTools wires the scene tools that are backed by the engine: a
// real list_scenes and export_scene_to_footswitch (compile + optional push).
func (s *Server) registerSceneTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_scenes",
		Description: "List saved scenes (YAML files in the rig-as-code scenes dir).",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleListScenes)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "save_scene",
		Description: "Snapshot the current desired-state (last values sent) as a named scene. Pass devices to restrict the snapshot to a subset of logical devices.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"},"devices":{"type":"array","items":{"type":"string"}}},"required":["name"]}`),
	}, s.handleSaveScene)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "recall_scene",
		Description: "Replay a saved scene live: program changes first, per-device settle delay, then the remaining CC/OSC events. mode=additive (default) layers over current state; mode=exact resets each referenced device to exactly the scene's values.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"mode":{"type":"string","enum":["additive","exact"]}},"required":["name"]}`),
	}, s.handleRecallScene)

	s.mcp.AddTool(&mcp.Tool{
		Name: "export_scene_to_footswitch",
		Description: "Compile a saved scene into the 'three' footswitch's on-device schema " +
			"(program-change before CC, per-device settle baked in; OSC devices such as the " +
			"X32 become 'osc' events with the UDP target baked from their binding) and " +
			"optionally push it over HTTP. Omit footswitch (or set dry_run) to preview the " +
			"compiled JSON without sending.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "Saved scene name to compile."},
				"footswitch": {"type": "string", "description": "Base URL of the footswitch, e.g. http://three.local. Omit to only preview."},
				"id": {"type": "string", "description": "On-device file stem; defaults to a sanitised scene name."},
				"bank": {"type": "integer", "description": "Matrix display digit 1..9 (0 = list position)."},
				"trigger": {
					"type": "object",
					"description": "Inbound MIDI message AUM sends to select this scene live. Omit for a foot-only scene.",
					"properties": {
						"type": {"type": "string", "enum": ["program_change", "control_change", "note_on"]},
						"channel": {"type": "integer", "description": "1..16, or 0 to match any channel."},
						"number": {"type": "integer", "description": "PC number / CC number / note."},
						"value": {"type": "integer", "description": "control_change only: also require this value."}
					},
					"required": ["type", "number"]
				},
				"dry_run": {"type": "boolean", "description": "Compile and return the JSON without pushing."}
			},
			"required": ["name"]
		}`),
	}, s.handleExportSceneToFootswitch)
}

func (s *Server) handleSaveScene(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Devices     []string `json:"devices"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Name == "" {
		return textResult("/name: required", true), nil
	}
	sc, err := s.eng.SaveScene(args.Name, args.Description, args.Devices)
	if err != nil {
		return textResult("save_scene failed: "+err.Error(), true), nil
	}
	if err := s.scenes.Save(sc); err != nil {
		return textResult("could not persist scene: "+err.Error(), true), nil
	}
	n := 0
	for _, c := range sc.Devices {
		n += len(c)
	}
	return textResult(fmt.Sprintf("saved scene %q (%d device(s), %d control(s))", sc.Name, len(sc.Devices), n), false), nil
}

func (s *Server) handleRecallScene(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Name == "" {
		return textResult("/name: required", true), nil
	}
	var mode scene.RecallMode
	switch strings.ToLower(strings.TrimSpace(args.Mode)) {
	case "", "additive":
		mode = scene.Additive
	case "exact":
		mode = scene.Exact
	default:
		return textResult("/mode: must be additive or exact", true), nil
	}

	sc, err := s.scenes.Load(args.Name)
	if err != nil {
		return textResult(fmt.Sprintf("cannot load scene %q: %v", args.Name, err), true), nil
	}
	warnings, err := s.eng.RecallScene(ctx, sc, mode)
	if err != nil {
		return textResult("recall_scene failed: "+err.Error(), true), nil
	}

	warn := ""
	if len(warnings) > 0 {
		warn = "\nwarnings:\n  - " + strings.Join(warnings, "\n  - ")
	}
	return textResult(fmt.Sprintf("recalled scene %q (%s) onto %d device(s)%s", sc.Name, mode, len(sc.Devices), warn), false), nil
}

func (s *Server) handleListScenes(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	names, err := s.scenes.List()
	if err != nil {
		return textResult("list_scenes failed: "+err.Error(), true), nil
	}
	if len(names) == 0 {
		return textResult("no scenes saved yet", false), nil
	}
	return textResult(strings.Join(names, "\n"), false), nil
}

func (s *Server) handleExportSceneToFootswitch(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name       string `json:"name"`
		Footswitch string `json:"footswitch"`
		ID         string `json:"id"`
		Bank       int    `json:"bank"`
		Trigger    *struct {
			Type    string `json:"type"`
			Channel int    `json:"channel"`
			Number  int    `json:"number"`
			Value   *int   `json:"value"`
		} `json:"trigger"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Name == "" {
		return textResult("/name: required", true), nil
	}

	sc, err := s.scenes.Load(args.Name)
	if err != nil {
		return textResult(fmt.Sprintf("cannot load scene %q: %v", args.Name, err), true), nil
	}

	opts := engine.FootswitchCompileOptions{ID: args.ID, Bank: args.Bank}
	if args.Trigger != nil {
		tt, err := normalizeTriggerType(args.Trigger.Type)
		if err != nil {
			return textResult("/trigger/type: "+err.Error(), true), nil
		}
		opts.Trigger = &engine.FootswitchTrigger{
			Type:    tt,
			Channel: args.Trigger.Channel,
			Number:  args.Trigger.Number,
			Value:   args.Trigger.Value,
		}
	}

	fs, warnings, err := s.eng.CompileFootswitchScene(sc, opts)
	if err != nil {
		return textResult("compile failed: "+err.Error(), true), nil
	}

	body, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return textResult("encode compiled scene: "+err.Error(), true), nil
	}

	warn := ""
	if len(warnings) > 0 {
		warn = "warnings:\n  - " + strings.Join(warnings, "\n  - ") + "\n"
	}

	// Preview mode: no target, or explicit dry run.
	if args.Footswitch == "" || args.DryRun {
		return textResult(fmt.Sprintf("%scompiled scene %q (%d event(s)) — not pushed:\n%s", warn, fs.ID, len(fs.Events), body), false), nil
	}

	status, respBody, err := pushFootswitchScene(ctx, args.Footswitch, fs.ID, body)
	if err != nil {
		return textResult(fmt.Sprintf("%spush to %s failed: %v", warn, args.Footswitch, err), true), nil
	}
	if status < 200 || status >= 300 {
		return textResult(fmt.Sprintf("%spush to %s rejected (HTTP %d): %s", warn, args.Footswitch, status, respBody), true), nil
	}
	return textResult(fmt.Sprintf("%spushed scene %q (%d event(s)) to %s: %s", warn, fs.ID, len(fs.Events), args.Footswitch, strings.TrimSpace(respBody)), false), nil
}

// normalizeTriggerType maps the accepted aliases to the firmware vocabulary.
func normalizeTriggerType(t string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "program_change", "pc":
		return "program_change", nil
	case "control_change", "cc":
		return "control_change", nil
	case "note_on", "note":
		return "note_on", nil
	default:
		return "", fmt.Errorf("must be one of program_change | control_change | note_on")
	}
}

// pushFootswitchScene POSTs the compiled scene JSON to <base>/scenes?id=<id>.
func pushFootswitchScene(ctx context.Context, base, id string, body []byte) (int, string, error) {
	url := strings.TrimRight(base, "/") + "/scenes?id=" + id
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(rb), nil
}
