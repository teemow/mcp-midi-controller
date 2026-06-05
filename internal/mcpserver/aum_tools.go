package mcpserver

// This file is Phase 4b: the MCP surface over the internal/aum read/edit/author
// library. The read tools (list/get/diff/import) reason about staged AUM
// sessions; the write tools (author/edit/export) generate .aumproj /
// .aum_midimap files into the same staging dir the aum receiver serves, so the
// iPad can download them back into AUM. All session files are private rig
// snapshots and live only under the gitignored state dir
// (config.AUMSessionsDir()), never committed.
//
// Every tool validates its arguments in-handler and returns failures as
// CallToolResult{IsError:true} text (so the model can self-correct) rather than
// as protocol errors, and the read/author tools emit structuredContent JSON
// alongside the human text via structResult, matching the rig-reasoning tools.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/sanitize"
)

// registerAUMTools wires the AUM session tools: read (list/get/diff/import) and
// write (author/edit/export). They are pure file tools — they never touch the
// engine or hardware — so they are always registered regardless of the USB
// write gate.
func (s *Server) registerAUMTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "list_aum_sessions",
		Description: "List the staged AUM session (.aumproj) and standalone MIDI-map (.aum_midimap) files — the ones uploaded from the iPad via the aum receiver and the ones authored/edited by these tools. " +
			"One line per file: id, kind, title, version, channel and mapping counts. Use get_aum_session for a session's full layout.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListAUMSessions)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_aum_session",
		Description: "Get one staged AUM session's full flat layout: version, tempo, every mixer channel (kind/title/fader/mute/solo) with its hosted AUv3 nodes (component tuple, componentName), and every assigned MIDI-control mapping (collection/target/type/data1/channel). Emits the SessionMap as structuredContent.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id (its filename without .aumproj). See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."}
			}
		}`),
	}, s.handleGetAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "diff_aum_session",
		Description: "Compare a staged session's channel-control mappings against the server AUM mixer CC convention (and surface the rig's bound MIDI channels for context). Reports which Volume/Mute/Solo/Rec targets are wired to their convention CC, which are unassigned, and which mismatch — i.e. whether the session is already wired to the convention or not yet.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."}
			}
		}`),
	}, s.handleDiffAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "import_aum_session",
		Description: "Read a staged session and propose rig bindings from it: each hosted AUv3 node is matched (by component tuple) against the staged AUv3 probes, yielding a suggested {logical, device, channel} binding (endpoint is left for you to fill). Also returns the matched/unmatched nodes and the full SessionMap. Run the auv3 probe first so nodes can match.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."}
			}
		}`),
	}, s.handleImportAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "author_aum_session",
		Description: "Author a new AUM session (.aumproj) from scratch and stage it for download to the iPad. Define ordered mixer channels (audio/midi), each optionally hosting AUv3 nodes sourced from staged probes (probe_id). By default the standard brain-control CC convention is baked in (mixer + transport + node-param CCs on channel 1) so the session is brain-controllable with no hand-wiring; pass a convention object to customize it, or bare:true for an untouched placeholder session. Returns the build report and the download path. The last audio channel is treated as the master.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {"type": "string", "description": "Session title (private)."},
				"out_id": {"type": "string", "description": "Staging id (filename without .aumproj); defaults to a sanitized title."},
				"tempo": {"type": "number", "description": "BPM (default 120)."},
				"sample_rate": {"type": "number", "description": "Engine sample rate (default 48000)."},
				"channels": {
					"type": "array",
					"description": "Ordered mixer strips.",
					"items": {
						"type": "object",
						"properties": {
							"kind": {"type": "string", "enum": ["audio", "midi"], "description": "Strip kind (default audio)."},
							"title": {"type": "string"},
							"fader": {"type": "number", "description": "Initial fader level (audio only)."},
							"muted": {"type": "boolean"},
							"soloed": {"type": "boolean"},
							"nodes": {
								"type": "array",
								"description": "Hosted AUv3 nodes (slot chain).",
								"items": {
									"type": "object",
									"properties": {
										"probe_id": {"type": "string", "description": "Staged auv3 probe id to host (its identity + params seed the node)."},
										"probe_file": {"type": "string", "description": "Explicit probe dump path (overrides probe_id)."},
										"component_name": {"type": "string", "description": "Override the node's human name."},
										"preset": {"type": "integer", "description": "Optional factory preset index (AuPresetCtrl)."},
										"state": {"type": "object", "description": "Optional saved AU state (AuStateDoc) as fullState key -> string value, stored as UTF-8 bytes. For our plugins e.g. {\"probeMidiBrainConfig\":\"{\\\"host\\\":\\\"box:7800\\\",\\\"controlEnabled\\\":true}\"} or {\"probeAudioTapConfig\":\"...\"}.", "additionalProperties": {"type": "string"}}
									}
								}
							}
						}
					}
				},
				"bare": {"type": "boolean", "description": "Skip the default convention and author an untouched placeholder session (AUM's default, what an unmapped real session looks like). Ignored when a convention object is supplied."},
				"convention": {
					"type": "object",
					"description": "Override the standard brain-control convention pre-assigned to the generated placeholders. Omit to bake the standard map (channel 1); set bare:true to skip it entirely.",
					"properties": {
						"channel": {"type": "integer", "description": "MIDI channel for assigned CCs (specState 1..16; 0 = OMNI). Default 1."},
						"start_cc": {"type": "integer", "description": "First CC for node params (default 30)."},
						"max_cc": {"type": "integer", "description": "Cap for node-param CCs (default 127)."}
					}
				},
				"routes": {
					"type": "array",
					"description": "Inter-node MIDI routes authored into midiMatrixState. Each connects one node's MIDI OUT (from) to one or more destinations (to: a node {channel,slot} or a builtin like \"MIDI Control\"/\"Keyboard\"). channel/slot are 0-based indices into channels[].",
					"items": {
						"type": "object",
						"properties": {
							"from": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}}, "required": ["channel", "slot"]},
							"to": {"type": "array", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}, "builtin": {"type": "string"}}}}
						},
						"required": ["from", "to"]
					}
				}
			},
			"required": ["channels"]
		}`),
	}, s.handleAuthorAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "author_loop_session",
		Description: "Author a ready-to-run agent-loop .aumproj in one call: a MIDI strip hosting ProbeMidiBrain (the hands), an audio strip hosting the synth with ProbeAudioTap inserted right after it (the ears), and a master strip. " +
			"Wires the brain's MIDI OUT to the synth and to AUM's MIDI Control, and authors the brain/tap AuStateDoc with the daemon host so both auto-connect on load. " +
			"After loading via the iPad's one-tap link, drive it with play_notes/send_midi/set_transport and read it back with get_audio_tap/get_audio_clip.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"synth_probe": {"type": "string", "description": "Staged probe id of the synth/instrument AUv3 to host on the audio strip."},
				"synth_file": {"type": "string", "description": "Explicit synth probe dump path (overrides synth_probe)."},
				"brain_probe": {"type": "string", "description": "Staged probe id of ProbeMidiBrain (run the auv3 probe for it once)."},
				"brain_file": {"type": "string", "description": "Explicit ProbeMidiBrain probe dump path (overrides brain_probe)."},
				"tap_probe": {"type": "string", "description": "Staged probe id of ProbeAudioTap."},
				"tap_file": {"type": "string", "description": "Explicit ProbeAudioTap probe dump path (overrides tap_probe)."},
				"host": {"type": "string", "description": "Daemon LAN host[:port] (e.g. \"box:7800\") embedded into the brain + tap config so they dial back automatically. Installation-specific; never committed."},
				"synth_preset": {"type": "integer", "description": "Optional factory preset index for the synth (AuPresetCtrl)."},
				"title": {"type": "string", "description": "Session title (default \"Agent Loop\")."},
				"out_id": {"type": "string", "description": "Staging id / filename stem (default from title)."},
				"tempo": {"type": "number", "description": "Session tempo BPM (default 120)."},
				"decimation": {"type": "integer", "description": "Tap PCM decimation factor (default 4)."}
			},
			"required": ["synth_probe", "brain_probe", "tap_probe", "host"]
		}`),
	}, s.handleAuthorLoopSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "edit_aum_session",
		Description: "Edit a staged session in place and re-stage it: assign MIDI-control mappings (collection/target/type/data1/channel), and set channel fader/mute/solo. Writes the result back as out_id (defaults to overwriting the source) for download to the iPad. Use get_aum_session to discover collection/target paths.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id to edit. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"out_id": {"type": "string", "description": "Staging id to write (defaults to session_id, overwriting it)."},
				"mappings": {
					"type": "array",
					"description": "Mapping assignments for a version-13 (specState) session. type codes (confirmed): 0=CC, 1=Note, 2=Program Change, 3=PBEND/CHPRS (data1 0=PBEND, 1=CHPRS); defaults to 0 (CC). channel is specState 1..16 (0 = OMNI).",
					"items": {
						"type": "object",
						"properties": {
							"collection": {"type": "string"},
							"target": {"type": "string"},
							"type": {"type": "integer"},
							"data1": {"type": "integer"},
							"channel": {"type": "integer"}
						},
						"required": ["collection", "target", "data1"]
					}
				},
				"faders": {"type": "array", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "level": {"type": "number"}}, "required": ["channel", "level"]}},
				"mutes": {"type": "array", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "muted": {"type": "boolean"}}, "required": ["channel", "muted"]}},
				"solos": {"type": "array", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "soloed": {"type": "boolean"}}, "required": ["channel", "soloed"]}},
				"presets": {"type": "array", "description": "Set a node's factory preset (AuPresetCtrl).", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}, "preset": {"type": "integer"}}, "required": ["channel", "slot", "preset"]}},
				"configs": {"type": "array", "description": "Set a node's saved AU state (AuStateDoc) as fullState key -> string value (stored as UTF-8 bytes). E.g. for ProbeMidiBrain {\"probeMidiBrainConfig\":\"{...}\"}.", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}, "state": {"type": "object", "additionalProperties": {"type": "string"}}}, "required": ["channel", "slot", "state"]}},
				"routes": {"type": "array", "description": "Replace midiMatrixState with these inter-node MIDI routes (see author_aum_session).", "items": {"type": "object", "properties": {"from": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}}, "required": ["channel", "slot"]}, "to": {"type": "array", "items": {"type": "object", "properties": {"channel": {"type": "integer"}, "slot": {"type": "integer"}, "builtin": {"type": "string"}}}}}, "required": ["from", "to"]}}
			}
		}`),
	}, s.handleEditAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "export_aum_midimap",
		Description: "Export one collection of a staged session's assigned mappings as a standalone .aum_midimap file (the per-collection Save/Load format AUM can import), staged for download to the iPad. collection is a flattened path from get_aum_session, e.g. \"Channels/chan0/Channel controls\".",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"collection": {"type": "string", "description": "Flattened collection path to export."},
				"name": {"type": "string", "description": "_collection_map_name to write (defaults to the collection path)."},
				"out_id": {"type": "string", "description": "Staging id for the .aum_midimap (defaults to <session>_<collection>)."}
			},
			"required": ["collection"]
		}`),
	}, s.handleExportAUMMidiMap)
}

// --- list / get -----------------------------------------------------------

// aumSessionRow is the machine-readable per-file row for list_aum_sessions.
type aumSessionRow struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Version  int    `json:"version,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Mappings int    `json:"mappings,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) handleListAUMSessions(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := config.AUMSessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return structResult(fmt.Sprintf("no staged AUM sessions (%s does not exist yet); upload one from the iPad or author one with author_aum_session", dir), map[string]any{"sessions": []aumSessionRow{}}), nil
		}
		return textResult("read sessions dir: "+err.Error(), true), nil
	}

	var rows []aumSessionRow
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		kind := aum.FileKind(e.Name())
		if kind == "" {
			continue
		}
		row := aumSessionRow{
			ID:   aum.StripExt(e.Name()),
			File: e.Name(),
			Kind: kind,
		}
		summarizeStaged(filepath.Join(dir, e.Name()), &row)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return structResult(fmt.Sprintf("no staged AUM sessions in %s", dir), map[string]any{"sessions": []aumSessionRow{}}), nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].File < rows[j].File })

	var b strings.Builder
	fmt.Fprintf(&b, "staged AUM files in %s (use get_aum_session for full layout):\n", dir)
	for _, r := range rows {
		if r.Error != "" {
			fmt.Fprintf(&b, "  %-28s [%s] (unreadable: %s)\n", r.ID, r.Kind, r.Error)
			continue
		}
		title := r.Title
		if title == "" {
			title = "(untitled)"
		}
		if r.Kind == "session" {
			fmt.Fprintf(&b, "  %-28s [session] %s v%d: %d channels, %d mappings\n", r.ID, title, r.Version, r.Channels, r.Mappings)
		} else {
			fmt.Fprintf(&b, "  %-28s [midimap] %s: %d mappings\n", r.ID, title, r.Mappings)
		}
	}
	return structResult(b.String(), map[string]any{"sessions": rows}), nil
}

// summarizeStaged fills the parsed fields of a list row from disk, recording a
// decode failure on the row rather than failing the whole listing.
func summarizeStaged(path string, row *aumSessionRow) {
	sum := aum.SummarizeFile(path)
	if sum.Err != nil {
		row.Error = sum.Err.Error()
		return
	}
	row.Title = sum.Title
	row.Version = sum.Version
	row.Channels = sum.Channels
	row.Mappings = sum.Mappings
}

func (s *Server) handleGetAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}
	sm := sess.Map()

	var b strings.Builder
	title := sess.Title()
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "%s — v%d", title, sm.Version)
	if sm.Tempo > 0 {
		fmt.Fprintf(&b, ", %g BPM", sm.Tempo)
	}
	fmt.Fprintf(&b, " (%d channels, %d assigned mappings)\n", len(sm.Channels), len(sm.Mappings))
	for _, ch := range sm.Channels {
		fmt.Fprintf(&b, "  [%d] %s %q", ch.Index, ch.Kind, ch.Title)
		if ch.FaderLevel != nil {
			fmt.Fprintf(&b, " fader=%.3g", *ch.FaderLevel)
		}
		if ch.Muted {
			b.WriteString(" muted")
		}
		if ch.Soloed {
			b.WriteString(" soloed")
		}
		b.WriteByte('\n')
		for _, n := range ch.Nodes {
			name := n.ComponentName
			if name == "" {
				name = n.ArchiveDescClass
			}
			fmt.Fprintf(&b, "      slot%d %s", n.Slot, name)
			if n.Component != nil {
				fmt.Fprintf(&b, " [%s/%s/%s]", n.Component.Type, n.Component.Subtype, n.Component.Manufacturer)
			}
			b.WriteByte('\n')
		}
	}
	if len(sm.Mappings) > 0 {
		b.WriteString("  mappings:\n")
		for _, m := range sm.Mappings {
			fmt.Fprintf(&b, "      %s/%s -> %s (type=%d) data1=%d ch=%d\n", m.Collection, m.Target, m.TypeName, m.Type, m.Data1, m.Channel)
		}
	}
	return structResult(b.String(), sm), nil
}

// --- diff -----------------------------------------------------------------

func (s *Server) handleDiffAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}

	rep := sess.CheckConvention()
	boundChannels := s.boundMIDIChannels()

	verdict := "not wired to the convention yet"
	switch {
	case rep.Expected == 0:
		verdict = "no convention targets (no non-master audio strips)"
	case rep.Wired == rep.Expected:
		verdict = "fully wired to the convention"
	case rep.Wired > 0:
		verdict = "partially wired to the convention"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "convention diff for %q: %s (%d/%d mixer+transport targets wired)\n", sess.Title(), verdict, rep.Wired, rep.Expected)
	if len(boundChannels) > 0 {
		hc := make([]string, len(boundChannels))
		for i, ch := range boundChannels {
			hc[i] = fmt.Sprintf("%d", ch+1) // 0-based wire -> human 1..16
		}
		fmt.Fprintf(&b, "rig bound MIDI channel(s): %s (human 1..16)\n", strings.Join(hc, ", "))
	}
	for _, c := range rep.Checks {
		switch c.Status {
		case "ok":
			fmt.Fprintf(&b, "  ok       %s/%s = CC %d\n", c.Collection, c.Target, c.ExpectedCC)
		case "missing":
			fmt.Fprintf(&b, "  missing  %s/%s (convention CC %d, unassigned)\n", c.Collection, c.Target, c.ExpectedCC)
		case "mismatch":
			fmt.Fprintf(&b, "  mismatch %s/%s = CC %d (convention CC %d)\n", c.Collection, c.Target, c.ActualCC, c.ExpectedCC)
		}
	}

	structured := map[string]any{
		"sessionID":     aum.StripExt(filepath.Base(path)),
		"title":         sess.Title(),
		"version":       sess.Version(),
		"verdict":       verdict,
		"convention":    rep,
		"boundChannels": boundChannels,
	}
	return structResult(b.String(), structured), nil
}

// boundMIDIChannels returns the sorted, de-duplicated set of MIDI channels the
// rig's non-USB bindings ride on (the channels a session's mappings would line
// up against).
func (s *Server) boundMIDIChannels() []int {
	seen := map[int]bool{}
	var out []int
	for _, b := range s.eng.Bindings() {
		if s.eng.IsUSBBinding(b.Logical) {
			continue
		}
		if !seen[b.Channel] {
			seen[b.Channel] = true
			out = append(out, b.Channel)
		}
	}
	sort.Ints(out)
	return out
}

// --- import ---------------------------------------------------------------

// proposedBinding is one suggested rig binding derived from a session node.
type proposedBinding struct {
	Logical      string                 `json:"logical"`
	Device       string                 `json:"device"`
	Channel      int                    `json:"channel"`
	Endpoint     string                 `json:"endpoint"`
	ChannelIndex int                    `json:"channelIndex"`
	Slot         int                    `json:"slot"`
	Component    *device.ProbeComponent `json:"component,omitempty"`
	MatchedProbe string                 `json:"matchedProbe,omitempty"`
	Note         string                 `json:"note,omitempty"`
}

func (s *Server) handleImportAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}
	sm := sess.Map()
	dumps := loadStagedProbeDumps()

	// channelOf returns a suggested wire MIDI channel for a node: the channel of
	// any assigned mapping under the node's chan collection, converted from the
	// specState convention (1..16; 0 = OMNI) to the 0-based wire form.
	channelOf := func(chanIndex int) (int, bool) {
		prefix := fmt.Sprintf("Channels/chan%d/", chanIndex)
		for _, m := range sm.Mappings {
			if strings.HasPrefix(m.Collection, prefix) && m.Channel >= 1 {
				return m.Channel - 1, true
			}
		}
		return 0, false
	}

	used := map[string]bool{}
	var proposed []proposedBinding
	var unmatched []string
	matched := 0
	for ci, ch := range sm.Channels {
		for _, n := range ch.Nodes {
			if n.Component == nil {
				continue // built-in node, not a hosted AUv3 plugin
			}
			pb := proposedBinding{
				ChannelIndex: ci,
				Slot:         n.Slot,
				Component:    n.Component,
			}
			logicalBase := firstNonEmptyStr(ch.Title, n.ComponentName, n.Component.Subtype)
			pb.Logical = uniqueLogical(sanitize.ID(logicalBase), used)

			if dump := findProbeForComponent(dumps, *n.Component); dump != nil {
				if def, _, derr := device.DefinitionFromProbe(*dump, device.ProbeOptions{}); derr == nil {
					pb.Device = def.ID
				}
				pb.MatchedProbe = device.ProbeID(*dump)
				matched++
			} else {
				pb.Note = "no staged probe matches this component; run the auv3 probe for this plugin, then re-import"
				unmatched = append(unmatched, fmt.Sprintf("%s [%s/%s/%s]", n.ComponentName, n.Component.Type, n.Component.Subtype, n.Component.Manufacturer))
			}
			if wire, ok := channelOf(ci); ok {
				pb.Channel = wire
			} else {
				pb.Note = strings.TrimSpace(pb.Note + " (channel defaulted to 0 — no assigned mapping to infer it)")
			}
			proposed = append(proposed, pb)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "import of %q: %d hosted node(s), %d matched a staged probe\n", sess.Title(), len(proposed), matched)
	if len(proposed) == 0 {
		b.WriteString("  (no hosted AUv3 nodes to propose bindings for)\n")
	}
	for _, pb := range proposed {
		dev := pb.Device
		if dev == "" {
			dev = "?"
		}
		fmt.Fprintf(&b, "  %-24s device=%s channel=%d (chan%d/slot%d)", pb.Logical, dev, pb.Channel, pb.ChannelIndex, pb.Slot)
		if pb.MatchedProbe != "" {
			fmt.Fprintf(&b, " probe=%s", pb.MatchedProbe)
		}
		if pb.Note != "" {
			fmt.Fprintf(&b, " — %s", pb.Note)
		}
		b.WriteByte('\n')
	}
	if len(unmatched) > 0 {
		fmt.Fprintf(&b, "unmatched node(s) (no probe): %d\n", len(unmatched))
	}
	b.WriteString("review, then create with bind_device (set the endpoint).\n")

	structured := map[string]any{
		"sessionID":        aum.StripExt(filepath.Base(path)),
		"title":            sess.Title(),
		"proposedBindings": proposed,
		"matchedNodes":     matched,
		"unmatchedNodes":   unmatched,
		"sessionMap":       sm,
		"stagedProbeCount": len(dumps),
	}
	return structResult(b.String(), structured), nil
}

// --- author ---------------------------------------------------------------

// routeArg is the JSON shape of one MIDI route in author/edit tool input.
type routeArg struct {
	From struct {
		Channel int `json:"channel"`
		Slot    int `json:"slot"`
	} `json:"from"`
	To []struct {
		Channel int    `json:"channel"`
		Slot    int    `json:"slot"`
		Builtin string `json:"builtin"`
	} `json:"to"`
}

// buildRoutes converts the tool-input routes into aum.MIDIRoute values.
func buildRoutes(in []routeArg) ([]aum.MIDIRoute, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]aum.MIDIRoute, 0, len(in))
	for i, r := range in {
		if len(r.To) == 0 {
			return nil, fmt.Errorf("/routes/%d/to: at least one destination is required", i)
		}
		route := aum.MIDIRoute{From: aum.MIDIEndpoint{Channel: r.From.Channel, Slot: r.From.Slot}}
		for _, d := range r.To {
			route.To = append(route.To, aum.MIDIEndpoint{Channel: d.Channel, Slot: d.Slot, Builtin: d.Builtin})
		}
		out = append(out, route)
	}
	return out, nil
}

// stateDocBytes converts a string->string AuStateDoc map into the
// key -> raw-bytes form aum.SetAuStateDoc expects (values stored as UTF-8).
func stateDocBytes(in map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = []byte(v)
	}
	return out
}

func (s *Server) handleAuthorAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Title      string  `json:"title"`
		OutID      string  `json:"out_id"`
		Tempo      float64 `json:"tempo"`
		SampleRate float64 `json:"sample_rate"`
		Channels   []struct {
			Kind   string   `json:"kind"`
			Title  string   `json:"title"`
			Fader  *float64 `json:"fader"`
			Muted  bool     `json:"muted"`
			Soloed bool     `json:"soloed"`
			Nodes  []struct {
				ProbeID       string            `json:"probe_id"`
				ProbeFile     string            `json:"probe_file"`
				ComponentName string            `json:"component_name"`
				Preset        *int              `json:"preset"`
				State         map[string]string `json:"state"`
			} `json:"nodes"`
		} `json:"channels"`
		Bare       bool `json:"bare"`
		Convention *struct {
			Channel int `json:"channel"`
			StartCC int `json:"start_cc"`
			MaxCC   int `json:"max_cc"`
		} `json:"convention"`
		Routes []routeArg `json:"routes"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if len(args.Channels) == 0 {
		return textResult("/channels: at least one channel is required", true), nil
	}

	spec := aum.BuildSpec{
		Title:      args.Title,
		Tempo:      args.Tempo,
		SampleRate: args.SampleRate,
	}
	for i, ch := range args.Channels {
		cs := aum.ChannelSpec{
			Kind:   aum.KindAudio,
			Title:  ch.Title,
			Fader:  ch.Fader,
			Muted:  ch.Muted,
			Soloed: ch.Soloed,
		}
		switch ch.Kind {
		case "", "audio":
			cs.Kind = aum.KindAudio
		case "midi":
			cs.Kind = aum.KindMIDI
		default:
			return textResult(fmt.Sprintf("/channels/%d/kind: %q is not audio|midi", i, ch.Kind), true), nil
		}
		for j, n := range ch.Nodes {
			if n.ProbeID == "" && n.ProbeFile == "" {
				return textResult(fmt.Sprintf("/channels/%d/nodes/%d: provide probe_id or probe_file (a hosted node needs a probe for its identity + params)", i, j), true), nil
			}
			ppath, perr := resolveProbePath(n.ProbeFile, n.ProbeID)
			if perr != nil {
				return textResult(fmt.Sprintf("/channels/%d/nodes/%d: %v", i, j, perr), true), nil
			}
			dump, derr := readProbeDump(ppath)
			if derr != nil {
				return textResult(fmt.Sprintf("/channels/%d/nodes/%d: read probe: %v", i, j, derr), true), nil
			}
			ns := aum.NodeSpecFromDump(dump)
			if n.ComponentName != "" {
				ns.ComponentName = n.ComponentName
			}
			if n.Preset != nil {
				ns.Preset = n.Preset
			}
			if len(n.State) > 0 {
				ns.StateDoc = stateDocBytes(n.State)
			}
			cs.Nodes = append(cs.Nodes, ns)
		}
		spec.Channels = append(spec.Channels, cs)
	}
	switch {
	case args.Convention != nil:
		spec.Convention = &aum.Convention{
			Channel:     args.Convention.Channel,
			NodeStartCC: args.Convention.StartCC,
			NodeMaxCC:   args.Convention.MaxCC,
		}
	case !args.Bare:
		// Default: bake the standard brain-control convention (channel 1) so the
		// authored session is brain-controllable with no hand-wiring.
		spec.Convention = &aum.Convention{Channel: 1}
	}
	routes, rerr := buildRoutes(args.Routes)
	if rerr != nil {
		return textResult(rerr.Error(), true), nil
	}
	spec.Routes = routes

	sess, report, err := aum.BuildSession(spec)
	if err != nil {
		return textResult("build session: "+err.Error(), true), nil
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	// Validate the bytes re-open before staging, so we never hand the iPad a
	// file these tools cannot read back.
	if _, err := aum.Open(data); err != nil {
		return textResult("authored session failed re-decode: "+err.Error(), true), nil
	}

	id := firstNonEmptyStr(sanitize.ID(args.OutID), sanitize.ID(args.Title), "session")
	path, file, err := stageAUMFile(id, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "authored session -> %s\n", path)
	fmt.Fprintf(&b, "  %d channel(s), %d node(s), %d mapping target(s)", report.Channels, report.Nodes, report.Targets)
	if spec.Convention != nil {
		fmt.Fprintf(&b, ", %d CC(s) assigned", report.AssignedCCs)
	}
	if report.Routes > 0 {
		fmt.Fprintf(&b, ", %d MIDI route(s)", report.Routes)
	}
	b.WriteByte('\n')
	if len(report.Overflow) > 0 {
		fmt.Fprintf(&b, "  %d node-param target(s) overflowed the CC cap and stayed unassigned\n", len(report.Overflow))
	}
	fmt.Fprintf(&b, "download from the iPad: GET /aum-session/%s\n", file)

	structured := map[string]any{
		"id":       id,
		"file":     file,
		"path":     path,
		"download": "/aum-session/" + file,
		"report":   report,
	}
	return structResult(b.String(), structured), nil
}

// --- author_loop_session --------------------------------------------------

// loopNodeSpec resolves a probe (id or explicit file) into a NodeSpec, returning
// a user-facing error string on failure.
func loopNodeSpec(field, probeID, probeFile string) (aum.NodeSpec, string) {
	if probeID == "" && probeFile == "" {
		return aum.NodeSpec{}, fmt.Sprintf("%s: provide a probe id or file", field)
	}
	ppath, perr := resolveProbePath(probeFile, probeID)
	if perr != nil {
		return aum.NodeSpec{}, fmt.Sprintf("%s: %v", field, perr)
	}
	dump, derr := readProbeDump(ppath)
	if derr != nil {
		return aum.NodeSpec{}, fmt.Sprintf("%s: read probe: %v", field, derr)
	}
	return aum.NodeSpecFromDump(dump), ""
}

func (s *Server) handleAuthorLoopSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SynthProbe  string  `json:"synth_probe"`
		SynthFile   string  `json:"synth_file"`
		BrainProbe  string  `json:"brain_probe"`
		BrainFile   string  `json:"brain_file"`
		TapProbe    string  `json:"tap_probe"`
		TapFile     string  `json:"tap_file"`
		Host        string  `json:"host"`
		SynthPreset *int    `json:"synth_preset"`
		Title       string  `json:"title"`
		OutID       string  `json:"out_id"`
		Tempo       float64 `json:"tempo"`
		Decimation  int     `json:"decimation"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Host) == "" {
		return textResult("host: a daemon host[:port] is required so the brain and tap can dial back", true), nil
	}

	brain, e := loopNodeSpec("brain_probe", args.BrainProbe, args.BrainFile)
	if e != "" {
		return textResult(e, true), nil
	}
	synth, e := loopNodeSpec("synth_probe", args.SynthProbe, args.SynthFile)
	if e != "" {
		return textResult(e, true), nil
	}
	tap, e := loopNodeSpec("tap_probe", args.TapProbe, args.TapFile)
	if e != "" {
		return textResult(e, true), nil
	}

	// Author the two plugins' AuStateDoc so they auto-connect to the daemon on
	// load: brain control + tap streaming both enabled, pointed at host.
	brainCfg, _ := json.Marshal(map[string]any{"host": args.Host, "controlEnabled": true})
	brain.StateDoc = map[string][]byte{"probeMidiBrainConfig": brainCfg}
	decim := args.Decimation
	if decim <= 0 {
		decim = 4
	}
	tapCfg, _ := json.Marshal(map[string]any{"host": args.Host, "streaming": true, "decimation": decim})
	tap.StateDoc = map[string][]byte{"probeAudioTapConfig": tapCfg}
	if args.SynthPreset != nil {
		synth.Preset = args.SynthPreset
	}

	title := firstNonEmptyStr(args.Title, "Agent Loop")
	tempo := args.Tempo
	if tempo <= 0 {
		tempo = 120
	}

	// Channel layout (0-based): 0 = MIDI brain, 1 = synth+tap insert, 2 = master.
	spec := aum.BuildSpec{
		Title: title,
		Tempo: tempo,
		// Auto-bake the standard brain-control convention so the loop session is
		// brain-controllable (mixer + transport + node-param CCs) with zero
		// hand-wiring; the brain and agent both speak this map.
		Convention: &aum.Convention{Channel: 1},
		Channels: []aum.ChannelSpec{
			{Kind: aum.KindMIDI, Title: "Brain", Nodes: []aum.NodeSpec{brain}},
			{Kind: aum.KindAudio, Title: "Synth", Nodes: []aum.NodeSpec{synth, tap}},
			{Kind: aum.KindAudio, Title: "Master"},
		},
		// Brain MIDI OUT -> synth (slot 0 of the audio strip) + AUM MIDI Control
		// (so transport / global MIDI control also see the brain's output).
		Routes: []aum.MIDIRoute{{
			From: aum.MIDIEndpoint{Channel: 0, Slot: 0},
			To: []aum.MIDIEndpoint{
				{Channel: 1, Slot: 0},
				{Builtin: "MIDI Control"},
			},
		}},
	}

	sess, report, err := aum.BuildSession(spec)
	if err != nil {
		return textResult("build session: "+err.Error(), true), nil
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	if _, err := aum.Open(data); err != nil {
		return textResult("authored session failed re-decode: "+err.Error(), true), nil
	}

	id := firstNonEmptyStr(sanitize.ID(args.OutID), sanitize.ID(title), "agent-loop")
	path, file, err := stageAUMFile(id, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "authored loop session -> %s\n", path)
	fmt.Fprintf(&b, "  brain[ch0/slot0] -> synth[ch1/slot0] + AUM MIDI Control; tap[ch1/slot1] inserted after synth\n")
	fmt.Fprintf(&b, "  %d channel(s), %d node(s), %d MIDI route(s), %d convention CC(s) on ch1; brain+tap configured for host %q\n", report.Channels, report.Nodes, report.Routes, report.AssignedCCs, args.Host)
	fmt.Fprintf(&b, "next: load via the iPad (push & open), then play_notes / get_audio_tap\n")
	fmt.Fprintf(&b, "download from the iPad: GET /aum-session/%s\n", file)

	structured := map[string]any{
		"id":       id,
		"file":     file,
		"path":     path,
		"download": "/aum-session/" + file,
		"report":   report,
	}
	return structResult(b.String(), structured), nil
}

// --- edit -----------------------------------------------------------------

func (s *Server) handleEditAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		OutID     string `json:"out_id"`
		Mappings  []struct {
			Collection string `json:"collection"`
			Target     string `json:"target"`
			Type       int    `json:"type"`
			Data1      int    `json:"data1"`
			Channel    int    `json:"channel"`
		} `json:"mappings"`
		Faders []struct {
			Channel int     `json:"channel"`
			Level   float64 `json:"level"`
		} `json:"faders"`
		Mutes []struct {
			Channel int  `json:"channel"`
			Muted   bool `json:"muted"`
		} `json:"mutes"`
		Solos []struct {
			Channel int  `json:"channel"`
			Soloed  bool `json:"soloed"`
		} `json:"solos"`
		Presets []struct {
			Channel int `json:"channel"`
			Slot    int `json:"slot"`
			Preset  int `json:"preset"`
		} `json:"presets"`
		Configs []struct {
			Channel int               `json:"channel"`
			Slot    int               `json:"slot"`
			State   map[string]string `json:"state"`
		} `json:"configs"`
		Routes []routeArg `json:"routes"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if len(args.Mappings)+len(args.Faders)+len(args.Mutes)+len(args.Solos)+len(args.Presets)+len(args.Configs)+len(args.Routes) == 0 {
		return textResult("no edits given (provide mappings/faders/mutes/solos/presets/configs/routes)", true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}

	var applied []string
	for i, m := range args.Mappings {
		if m.Collection == "" || m.Target == "" {
			return textResult(fmt.Sprintf("/mappings/%d: collection and target are required", i), true), nil
		}
		if err := sess.SetMapping(m.Collection, m.Target, m.Type, m.Data1, m.Channel); err != nil {
			return textResult(fmt.Sprintf("/mappings/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("map %s/%s -> type=%d data1=%d ch=%d", m.Collection, m.Target, m.Type, m.Data1, m.Channel))
	}
	for i, f := range args.Faders {
		if err := sess.SetFader(f.Channel, f.Level); err != nil {
			return textResult(fmt.Sprintf("/faders/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("fader ch%d = %.3g", f.Channel, f.Level))
	}
	for i, mu := range args.Mutes {
		if err := sess.SetMute(mu.Channel, mu.Muted); err != nil {
			return textResult(fmt.Sprintf("/mutes/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("mute ch%d = %t", mu.Channel, mu.Muted))
	}
	for i, so := range args.Solos {
		if err := sess.SetSolo(so.Channel, so.Soloed); err != nil {
			return textResult(fmt.Sprintf("/solos/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("solo ch%d = %t", so.Channel, so.Soloed))
	}
	for i, p := range args.Presets {
		if err := sess.SetPreset(p.Channel, p.Slot, p.Preset); err != nil {
			return textResult(fmt.Sprintf("/presets/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("preset ch%d slot%d = %d", p.Channel, p.Slot, p.Preset))
	}
	for i, c := range args.Configs {
		if len(c.State) == 0 {
			return textResult(fmt.Sprintf("/configs/%d/state: at least one entry is required", i), true), nil
		}
		if err := sess.SetAuStateDoc(c.Channel, c.Slot, stateDocBytes(c.State)); err != nil {
			return textResult(fmt.Sprintf("/configs/%d: %v", i, err), true), nil
		}
		applied = append(applied, fmt.Sprintf("config ch%d slot%d (%d key(s))", c.Channel, c.Slot, len(c.State)))
	}
	if len(args.Routes) > 0 {
		routes, rerr := buildRoutes(args.Routes)
		if rerr != nil {
			return textResult(rerr.Error(), true), nil
		}
		if err := sess.SetMIDIRoutes(routes); err != nil {
			return textResult("routes: "+err.Error(), true), nil
		}
		applied = append(applied, fmt.Sprintf("midi matrix: %d route(s)", len(routes)))
	}

	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	if _, err := aum.Open(data); err != nil {
		return textResult("edited session failed re-decode: "+err.Error(), true), nil
	}

	id := sanitize.ID(args.OutID)
	if id == "" {
		id = aum.StripExt(filepath.Base(path))
	}
	outPath, file, err := stageAUMFile(id, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "edited session -> %s (%d edit(s))\n", outPath, len(applied))
	for _, a := range applied {
		fmt.Fprintf(&b, "  %s\n", a)
	}
	fmt.Fprintf(&b, "download from the iPad: GET /aum-session/%s\n", file)

	structured := map[string]any{
		"id":       id,
		"file":     file,
		"path":     outPath,
		"download": "/aum-session/" + file,
		"applied":  applied,
	}
	return structResult(b.String(), structured), nil
}

// --- export midimap -------------------------------------------------------

func (s *Server) handleExportAUMMidiMap(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID  string `json:"session_id"`
		File       string `json:"file"`
		Collection string `json:"collection"`
		Name       string `json:"name"`
		OutID      string `json:"out_id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.Collection == "" {
		return textResult("/collection: required (a flattened collection path from get_aum_session)", true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	sess, err := aum.OpenFile(path)
	if err != nil {
		return textResult("open session: "+err.Error(), true), nil
	}

	// Only assigned mappings in this collection export; warn (not error) when
	// none, so the caller learns the collection path was wrong/empty.
	n := 0
	for _, m := range sess.Mappings(false) {
		if m.Collection == args.Collection {
			n++
		}
	}
	if n == 0 {
		return textResult(fmt.Sprintf("no assigned mappings in collection %q (nothing to export); see get_aum_session for valid collection paths", args.Collection), true), nil
	}

	archive, err := sess.ExportMidiMap(args.Collection, args.Name)
	if err != nil {
		return textResult("export midimap: "+err.Error(), true), nil
	}
	data, err := archive.Encode()
	if err != nil {
		return textResult("encode midimap: "+err.Error(), true), nil
	}
	if _, err := aum.OpenMidiMap(data); err != nil {
		return textResult("exported midimap failed re-decode: "+err.Error(), true), nil
	}

	id := sanitize.ID(args.OutID)
	if id == "" {
		id = sanitize.ID(aum.StripExt(filepath.Base(path)) + "_" + args.Collection)
	}
	outPath, file, err := stageAUMFile(id, ".aum_midimap", data)
	if err != nil {
		return textResult("stage midimap: "+err.Error(), true), nil
	}

	b := fmt.Sprintf("exported %d mapping(s) from %q -> %s\ndownload from the iPad: GET /aum-session/%s\n", n, args.Collection, outPath, file)
	structured := map[string]any{
		"id":       id,
		"file":     file,
		"path":     outPath,
		"download": "/aum-session/" + file,
		"mappings": n,
	}
	return structResult(b, structured), nil
}

// --- shared helpers -------------------------------------------------------

// resolveAUMSessionPath turns the {file|session_id} args into a staged .aumproj
// path: an explicit file wins, otherwise <session_id>.aumproj under the staging
// dir. session_id is agent-supplied so it must be a bare id (no path), the same
// traversal guard resolveProbePath uses.
func resolveAUMSessionPath(file, sessionID string) (string, error) {
	if file != "" {
		return file, nil
	}
	if sessionID == "" {
		return "", fmt.Errorf("provide /file or /session_id")
	}
	if sessionID != filepath.Base(sessionID) || strings.Contains(sessionID, "..") ||
		strings.ContainsRune(sessionID, '/') || strings.ContainsRune(sessionID, os.PathSeparator) {
		return "", fmt.Errorf("invalid session_id %q (must be a bare session id, no path)", sessionID)
	}
	name := sessionID
	if !strings.HasSuffix(name, ".aumproj") {
		name += ".aumproj"
	}
	return filepath.Join(config.AUMSessionsDir(), name), nil
}

// stageAUMFile writes data as <id><ext> under the AUM staging dir, returning the
// full path and the bare filename (for the download URL). id is sanitized by the
// caller; the staging dir is created if missing.
func stageAUMFile(id, ext string, data []byte) (path, file string, err error) {
	if id == "" {
		id = "session"
	}
	dir := config.AUMSessionsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create sessions dir: %w", err)
	}
	file = id + ext
	path = filepath.Join(dir, file)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", err
	}
	return path, file, nil
}

// loadStagedProbeDumps reads every staged AUv3 probe dump, skipping unreadable
// ones (a bad dump should not block a session import).
func loadStagedProbeDumps() []device.ProbeDump {
	dir := config.AUv3ProbesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []device.ProbeDump
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if dump, derr := readProbeDump(filepath.Join(dir, e.Name())); derr == nil {
			out = append(out, dump)
		}
	}
	return out
}

// findProbeForComponent returns the staged dump whose component tuple matches c,
// or nil. It is the import-side of aum.Node.MatchProbe (which needs the typed
// node; here we match a flat SessionMap component).
func findProbeForComponent(dumps []device.ProbeDump, c device.ProbeComponent) *device.ProbeDump {
	for i := range dumps {
		if aum.ComponentMatches(c, dumps[i].Component) {
			return &dumps[i]
		}
	}
	return nil
}

// uniqueLogical disambiguates a logical name within one import, suffixing _2,
// _3, … on collision so two nodes never propose the same logical.
func uniqueLogical(base string, used map[string]bool) string {
	if base == "" {
		base = "node"
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	used[name] = true
	return name
}
