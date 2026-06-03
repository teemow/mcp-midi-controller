package mcpserver

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

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
)

// registerAuthoringTools wires the device-authoring path: build a definition
// draft, add controls to it (manually or from a MIDI-learn capture), then
// validate + persist it so it hot-loads without a daemon restart. This is the
// "extend the rig without writing Go" mechanism.
func (s *Server) registerAuthoringTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name:        "create_device_definition",
		Description: "Begin authoring a new device definition (a draft held in memory). Add controls with add_control, then persist with save_device_definition.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Definition id (also the YAML filename); lowercase, no spaces."},
				"name": {"type": "string", "description": "Human-readable device name."},
				"manufacturer": {"type": "string"},
				"description": {"type": "string"},
				"transport": {"type": "string", "description": "Transport the device speaks: blemidi | osc | usbmidi."},
				"settle_ms": {"type": "integer", "description": "Optional delay after a program change before CCs during scene recall."}
			},
			"required": ["id", "name", "transport"]
		}`),
	}, s.handleCreateDeviceDefinition)

	s.mcp.AddTool(&mcp.Tool{
		Name: "add_control",
		Description: "Add a control to a definition draft. Provide addressing for its type (cc/nrpn/program/sysex/address) and a value spec, " +
			"or set from_learn=true to pre-fill type+number from the most recent learn_capture.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"device": {"type": "string", "description": "Draft id (from create_device_definition)."},
				"name": {"type": "string", "description": "Control name (unique within the device)."},
				"type": {"type": "string", "enum": ["cc", "nrpn", "program_change", "sysex", "osc", "note_on", "note_off"]},
				"description": {"type": "string"},
				"cc": {"type": "integer", "description": "CC number (cc/note types)."},
				"nrpn": {"type": "integer", "description": "NRPN parameter number."},
				"program": {"type": "integer", "description": "Fixed program number (program_change)."},
				"sysex": {"type": "string", "description": "SysEx hex template; %v is the wire value."},
				"address": {"type": "string", "description": "OSC address."},
				"parametric": {"type": "boolean", "description": "Address number supplied at call time."},
				"value": {
					"type": "object",
					"description": "Value spec.",
					"properties": {
						"type": {"type": "string", "enum": ["range", "enum", "int", "float", "string"]},
						"min": {"type": "number"},
						"max": {"type": "number"},
						"step": {"type": "number"},
						"unit": {"type": "string"},
						"values": {"type": "object", "description": "enum label -> wire value map"}
					}
				},
				"from_learn": {"type": "boolean", "description": "Pre-fill type and number from the latest learn_capture."}
			},
			"required": ["device", "name"]
		}`),
	}, s.handleAddControl)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "save_device_definition",
		Description: "Validate a definition draft, write it to the user devices dir, hot-load it into the registry, regenerate control_<logical> tools for any binding using it, and return an AUM mapping cheat-sheet.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device":{"type":"string","description":"Draft id to persist."}},"required":["device"]}`),
	}, s.handleSaveDeviceDefinition)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_auv3_probes",
		Description: "List the staged AUv3 parameter-tree dumps (shipped by the off-daemon auv3-probe receiver) available for import_auv3_probe, " +
			"with a one-line summary (name, total params, writable count) per dump.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListAUv3Probes)

	s.mcp.AddTool(&mcp.Tool{
		Name: "import_auv3_probe",
		Description: "Ingest an AUv3 parameter-tree dump (the AUv3 analog of a USB patch dump; AUM cannot echo MIDI). " +
			"mode=draft builds a device definition draft (one cc per writable param, convention CC from 30, range 0-127, AU metadata in the description) ready for save_device_definition. " +
			"mode=diff compares the dump against an existing definition and reports uncovered params, stale controls, and unit/enum mismatches.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file": {"type": "string", "description": "Explicit path to a dump JSON. If omitted, device_id selects <device_id>.json from the staging dir."},
				"device_id": {"type": "string", "description": "Staged dump id (draft mode) / existing definition id to diff against (diff mode). Optional in draft mode if file is given."},
				"mode": {"type": "string", "enum": ["draft", "diff"], "description": "draft = build a definition draft; diff = compare against an existing definition. Default draft."}
			}
		}`),
	}, s.handleImportAUv3Probe)
}

func (s *Server) handleCreateDeviceDefinition(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Manufacturer string `json:"manufacturer"`
		Description  string `json:"description"`
		Transport    string `json:"transport"`
		SettleMS     int    `json:"settle_ms"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.ID == "" || args.Name == "" || args.Transport == "" {
		return textResult("/id, /name and /transport are required", true), nil
	}

	d := &device.Definition{
		ID:           args.ID,
		Name:         args.Name,
		Manufacturer: args.Manufacturer,
		Description:  args.Description,
		Transport:    args.Transport,
		SettleMS:     args.SettleMS,
	}
	s.draftsMu.Lock()
	s.drafts[args.ID] = d
	s.draftsMu.Unlock()

	note := ""
	if !s.eng.HasTransport(args.Transport) {
		note = fmt.Sprintf(" (warning: transport %q is not registered; known: %s)", args.Transport, strings.Join(s.eng.TransportIDs(), ", "))
	}
	return textResult(fmt.Sprintf("created draft %q (%s, transport=%s); add controls with add_control%s", args.ID, args.Name, args.Transport, note), false), nil
}

func (s *Server) handleAddControl(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device      string `json:"device"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
		CC          *int   `json:"cc"`
		NRPN        *int   `json:"nrpn"`
		Program     *int   `json:"program"`
		SysEx       string `json:"sysex"`
		Address     string `json:"address"`
		Parametric  bool   `json:"parametric"`
		Value       *struct {
			Type   string         `json:"type"`
			Min    *float64       `json:"min"`
			Max    *float64       `json:"max"`
			Step   *float64       `json:"step"`
			Unit   string         `json:"unit"`
			Values map[string]int `json:"values"`
		} `json:"value"`
		FromLearn bool `json:"from_learn"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" || args.Name == "" {
		return textResult("/device and /name are required", true), nil
	}

	s.draftsMu.Lock()
	defer s.draftsMu.Unlock()
	draft, ok := s.drafts[args.Device]
	if !ok {
		return textResult(fmt.Sprintf("no draft %q; start one with create_device_definition", args.Device), true), nil
	}

	c := device.Control{
		Name:        args.Name,
		Description: args.Description,
		Type:        device.ControlType(args.Type),
		CC:          args.CC,
		NRPN:        args.NRPN,
		Program:     args.Program,
		SysEx:       args.SysEx,
		Address:     args.Address,
		Parametric:  args.Parametric,
	}

	// from_learn pre-fills type and number from the most recent capture; any
	// explicit fields above still win where set.
	var learnNote string
	if args.FromLearn {
		captured, ok := s.eng.LearnCapture()
		if !ok {
			return textResult("from_learn: nothing captured yet; run learn_start then move a control, then retry", true), nil
		}
		if c.Type == "" {
			c.Type = device.ControlType(captured.Type)
		}
		if captured.HasNumber && c.CC == nil && (c.Type == device.ControlCC || c.Type == device.ControlNoteOn || c.Type == device.ControlNoteOff) {
			n := captured.Number
			c.CC = &n
		}
		learnNote = fmt.Sprintf(" (from learn: %s number=%d ch=%d)", captured.Type, captured.Number, captured.Channel)
	}

	if args.Value != nil {
		c.Value = device.ValueSpec{
			Type:   device.ValueType(args.Value.Type),
			Min:    args.Value.Min,
			Max:    args.Value.Max,
			Step:   args.Value.Step,
			Unit:   args.Value.Unit,
			Values: args.Value.Values,
		}
	} else if c.Type != device.ControlOSC {
		// Sensible default for MIDI controls: the 0..127 CC range.
		c.Value = device.ValueSpec{Type: device.ValueRange}
	}

	// Validate the candidate (including uniqueness) on a copy before mutating
	// the draft, so a rejected control leaves the draft untouched.
	candidate := *draft
	candidate.Controls = append(append([]device.Control(nil), draft.Controls...), c)
	if err := candidate.Validate(); err != nil {
		return textResult("add_control rejected: "+err.Error(), true), nil
	}
	draft.Controls = candidate.Controls

	return textResult(fmt.Sprintf("added control %q [%s] to draft %q (%d control(s) total)%s", c.Name, c.Type, args.Device, len(draft.Controls), learnNote), false), nil
}

func (s *Server) handleSaveDeviceDefinition(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Device == "" {
		return textResult("/device: required", true), nil
	}

	s.draftsMu.Lock()
	draft, ok := s.drafts[args.Device]
	s.draftsMu.Unlock()
	if !ok {
		return textResult(fmt.Sprintf("no draft %q; start one with create_device_definition", args.Device), true), nil
	}
	if len(draft.Controls) == 0 {
		return textResult(fmt.Sprintf("draft %q has no controls; add some with add_control first", args.Device), true), nil
	}
	if err := draft.Validate(); err != nil {
		return textResult("validation failed: "+err.Error(), true), nil
	}
	if !s.eng.HasTransport(draft.Transport) {
		return textResult(fmt.Sprintf("/transport: %q is not a registered transport (known: %s)", draft.Transport, strings.Join(s.eng.TransportIDs(), ", ")), true), nil
	}

	// Persist to the user devices dir (overrides the bundled definition of the
	// same id by name, per the loader's precedence).
	b, err := yaml.Marshal(draft)
	if err != nil {
		return textResult("encode definition: "+err.Error(), true), nil
	}
	dir := config.DevicesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return textResult("create devices dir: "+err.Error(), true), nil
	}
	path := filepath.Join(dir, draft.ID+".yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return textResult("write definition: "+err.Error(), true), nil
	}

	// Hot-load a copy into the registry so further draft edits do not mutate the
	// live definition.
	loaded := *draft
	loaded.Controls = append([]device.Control(nil), draft.Controls...)
	if err := s.eng.Registry().AddDefinition(&loaded); err != nil {
		return textResult("register definition: "+err.Error(), true), nil
	}

	// Regenerate the tool(s) for every binding that uses this definition (a
	// control_<logical> tool, or the USB tool family for a USB binding).
	var regenerated []string
	for _, bind := range s.eng.Bindings() {
		if bind.DeviceID == draft.ID {
			s.removeToolsForBinding(bind.Logical, s.eng.IsUSBBinding(bind.Logical))
			s.addToolsForBinding(bind)
			regenerated = append(regenerated, bind.Logical)
		}
	}
	sort.Strings(regenerated)

	s.draftsMu.Lock()
	delete(s.drafts, args.Device)
	s.draftsMu.Unlock()

	var out strings.Builder
	fmt.Fprintf(&out, "saved device definition %q to %s (%d control(s))\n", draft.ID, path, len(draft.Controls))
	if len(regenerated) > 0 {
		fmt.Fprintf(&out, "regenerated control tool(s) for: %s\n", strings.Join(regenerated, ", "))
	}
	out.WriteString("\n")
	out.WriteString(aumCheatSheet(draft, s.eng.Bindings()))
	return textResult(out.String(), false), nil
}

func (s *Server) handleListAUv3Probes(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := config.AUv3ProbesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return textResult(fmt.Sprintf("no staged AUv3 probes (%s does not exist yet); run the auv3-probe receiver and ship a dump from the iPad", dir), false), nil
		}
		return textResult("read probes dir: "+err.Error(), true), nil
	}

	var rows []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		dump, derr := readProbeDump(filepath.Join(dir, e.Name()))
		if derr != nil {
			rows = append(rows, fmt.Sprintf("  %-24s (unreadable: %v)", id, derr))
			continue
		}
		writable := 0
		for _, p := range dump.Parameters {
			if p.Writable {
				writable++
			}
		}
		rows = append(rows, fmt.Sprintf("  %-24s %s: %d params, %d writable", id, dump.Name, len(dump.Parameters), writable))
	}
	if len(rows) == 0 {
		return textResult(fmt.Sprintf("no staged AUv3 probes in %s", dir), false), nil
	}
	sort.Strings(rows)
	return textResult(fmt.Sprintf("staged AUv3 probes in %s:\n%s", dir, strings.Join(rows, "\n")), false), nil
}

func (s *Server) handleImportAUv3Probe(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		File     string `json:"file"`
		DeviceID string `json:"device_id"`
		Mode     string `json:"mode"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	mode := args.Mode
	if mode == "" {
		mode = "draft"
	}

	path, err := resolveProbePath(args.File, args.DeviceID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	dump, err := readProbeDump(path)
	if err != nil {
		return textResult("read probe dump: "+err.Error(), true), nil
	}

	switch mode {
	case "draft":
		return s.importAUv3Draft(dump, path), nil
	case "diff":
		return s.importAUv3Diff(dump, args.DeviceID), nil
	default:
		return textResult(fmt.Sprintf("/mode: %q is not draft|diff", mode), true), nil
	}
}

// importAUv3Draft builds a definition draft from the dump, stores it under its
// derived id (so the existing save_device_definition persists it), and returns
// the build report plus an AUM cheat-sheet preview.
func (s *Server) importAUv3Draft(dump device.ProbeDump, path string) *mcp.CallToolResult {
	def, report, err := device.DefinitionFromProbe(dump, device.ProbeOptions{})
	if err != nil {
		return textResult("build draft: "+err.Error(), true)
	}

	loaded := *def
	loaded.Controls = append([]device.Control(nil), def.Controls...)
	s.draftsMu.Lock()
	s.drafts[def.ID] = &loaded
	s.draftsMu.Unlock()

	var out strings.Builder
	fmt.Fprintf(&out, "imported %s -> draft %q (%s): %d control(s) from %d writable param(s)\n",
		filepath.Base(path), def.ID, def.Name, len(def.Controls), len(def.Controls)+len(report.Overflow))
	if len(report.Overflow) > 0 {
		fmt.Fprintf(&out, "OVERFLOW: %d writable param(s) did not fit the CC cap and got no control (curate onto a second channel/file):\n", len(report.Overflow))
		for _, p := range report.Overflow {
			fmt.Fprintf(&out, "  - %s\n", p.DisplayName)
		}
	}
	if len(report.SkippedReadOnly) > 0 {
		fmt.Fprintf(&out, "skipped %d read-only param(s) (AUM can only map writable params)\n", len(report.SkippedReadOnly))
	}
	fmt.Fprintf(&out, "review the draft, then persist it with save_device_definition device=%q\n\n", def.ID)
	out.WriteString(aumCheatSheet(def, s.eng.Bindings()))
	return textResult(out.String(), false)
}

// importAUv3Diff compares the dump against an existing definition (by device_id
// or the dump-derived id) and returns the coverage/correctness report.
func (s *Server) importAUv3Diff(dump device.ProbeDump, deviceID string) *mcp.CallToolResult {
	targetID := deviceID
	if targetID == "" {
		if d, _, err := device.DefinitionFromProbe(dump, device.ProbeOptions{}); err == nil {
			targetID = d.ID
		}
	}
	def, ok := s.eng.Registry().Get(targetID)
	if !ok {
		return textResult(fmt.Sprintf("no definition %q to diff against; pass /device_id of an existing definition (or import in draft mode first)", targetID), true)
	}

	diff := device.DiffProbeAgainstDefinition(dump, def)
	var out strings.Builder
	fmt.Fprintf(&out, "diff of probe %q against definition %q:\n", dump.Name, def.ID)
	if !diff.HasFindings() {
		out.WriteString("  no findings — the definition covers every writable param and matches its units/enums\n")
		return textResult(out.String(), false)
	}
	if len(diff.MissingFromDefinition) > 0 {
		fmt.Fprintf(&out, "MISSING (writable params with no control — uncovered functionality): %d\n", len(diff.MissingFromDefinition))
		for _, p := range diff.MissingFromDefinition {
			fmt.Fprintf(&out, "  - %s\n", probeParamLine(p))
		}
	}
	if len(diff.StaleControls) > 0 {
		stale := append([]string(nil), diff.StaleControls...)
		sort.Strings(stale)
		fmt.Fprintf(&out, "STALE (controls with no matching param — likely wrong/renamed): %d\n", len(stale))
		for _, c := range stale {
			fmt.Fprintf(&out, "  - %s\n", c)
		}
	}
	if len(diff.Mismatches) > 0 {
		ms := append([]device.ProbeMismatch(nil), diff.Mismatches...)
		sort.Slice(ms, func(i, j int) bool { return ms[i].Control < ms[j].Control })
		fmt.Fprintf(&out, "MISMATCH (unit/enum discrepancies): %d\n", len(ms))
		for _, m := range ms {
			fmt.Fprintf(&out, "  - %s (param %s): %s\n", m.Control, m.Param, m.Detail)
		}
	}
	return textResult(out.String(), false)
}

func probeParamLine(p device.ProbeParam) string {
	s := p.DisplayName
	if p.Identifier != "" && p.Identifier != p.DisplayName {
		s = fmt.Sprintf("%s (%s)", p.DisplayName, p.Identifier)
	}
	if len(p.ValueStrings) > 0 {
		s += fmt.Sprintf(" [%d values]", len(p.ValueStrings))
	}
	return s
}

// resolveProbePath turns the import args into a dump file path: an explicit
// file wins, otherwise <device_id>.json under the staging dir.
func resolveProbePath(file, deviceID string) (string, error) {
	if file != "" {
		return file, nil
	}
	if deviceID == "" {
		return "", fmt.Errorf("provide /file or /device_id")
	}
	name := deviceID
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	return filepath.Join(config.AUv3ProbesDir(), name), nil
}

func readProbeDump(path string) (device.ProbeDump, error) {
	var dump device.ProbeDump
	b, err := os.ReadFile(path)
	if err != nil {
		return dump, err
	}
	if err := json.Unmarshal(b, &dump); err != nil {
		return dump, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return dump, nil
}

// aumCheatSheet renders the per-control (channel, CC) mapping a user must
// configure in AUM's MIDI matrix (or the plugin's MIDI-learn) so the device's
// server-invented CC convention matches the host. Channels come from any
// binding(s) of this definition (a definition can be bound on several
// channels); when unbound it lists the CC convention without a channel.
func aumCheatSheet(def *device.Definition, bindings []engine.Binding) string {
	var channels []int
	for _, b := range bindings {
		if b.DeviceID == def.ID {
			channels = append(channels, b.Channel)
		}
	}
	sort.Ints(channels)

	var b strings.Builder
	fmt.Fprintf(&b, "AUM mapping cheat-sheet for %q:\n", def.Name)
	if len(channels) == 0 {
		b.WriteString("  (not bound yet — bind_device to assign a MIDI channel, then map these CCs in AUM)\n")
	}

	// Only CC/note controls are CC-mappable in AUM; program changes and OSC are
	// addressed differently.
	var rows []string
	for i := range def.Controls {
		c := &def.Controls[i]
		switch c.Type {
		case device.ControlCC, device.ControlNoteOn, device.ControlNoteOff:
			if c.CC != nil {
				rows = append(rows, fmt.Sprintf("  %-24s CC %d", c.Name, *c.CC))
			}
		case device.ControlProgramChange:
			rows = append(rows, fmt.Sprintf("  %-24s Program Change", c.Name))
		}
	}
	if len(rows) == 0 {
		b.WriteString("  (no CC/PC controls to map)\n")
		return b.String()
	}
	if len(channels) > 0 {
		humanChannels := make([]string, len(channels))
		for i, ch := range channels {
			humanChannels[i] = fmt.Sprintf("%d", ch+1) // 0-based wire -> human 1..16
		}
		fmt.Fprintf(&b, "  MIDI channel(s): %s (human 1..16)\n", strings.Join(humanChannels, ", "))
	}
	sort.Strings(rows)
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n")
	return b.String()
}
