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
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/engine"
	"github.com/teemow/mcp-midi-controller/internal/sanitize"
	"github.com/teemow/mcp-midi-controller/internal/transport/auv3midi"
)

// auv3midiTransport is the transport id the AUM session rig speaks (the LAN
// brain channel); auv3midiBrainEndpoint is the single logical endpoint it
// exposes (one brain per rig — the endpoint string is unused for routing). Both
// alias the transport package's canonical constants so the literals live in one
// place.
const (
	auv3midiTransport     = auv3midi.ID
	auv3midiBrainEndpoint = auv3midi.BrainEndpoint
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
				"session_id": {"type": "string", "description": "Staged session id (its staging-relative path without .aumproj, e.g. 'set' or 'Live sets/Set'). See list_aum_sessions."},
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
		Name: "import_aum_session",
		Description: "Import a staged AUM session as a rig of device instances derived from the session file's ACTUAL enabled MIDI mappings (the session is the single source of truth — no convention guessing). " +
			"By default it AUTO-CREATES devices: one session device (strip level/mute/solo/rec, full transport incl. tempo/metronome, system actions, built-in-node knobs, tap toggles) plus one device per hosted AUv3 node, every control pinned to the exact message type, number and MIDI channel its session mapping stores (banked multi-channel sessions just work). " +
			"Staged AUv3 probes are optional and only enrich node controls with human names/enum labels. Devices are tagged with the session id, so a re-import replaces the previous session's auto-created devices instead of piling up. " +
			"Inexpressible mappings (PBEND/CHPRS) are reported, not dropped. Set propose_only:true to never create — just propose the {name, device type, channel} suggestions.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"propose_only": {"type": "boolean", "description": "Only propose devices without creating any. Default false = auto-create where possible, propose the rest."}
			}
		}`),
	}, s.handleImportAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "author_aum_session",
		Description: "Author a new AUM session (.aumproj) from scratch and stage it for download to the iPad. Define ordered mixer channels (audio/midi), each optionally hosting AUv3 nodes sourced from staged probes (probe_id). " +
			"Each audio channel can also declare its audio routing: a built-in source (HW input / mix-bus read / file player), a fader/output node (send to a mix bus, or to a hardware output for the master/monitor), post-fader insert nodes, aux sends into extra mix buses, and a post-fader ProbeAudioTap (tap). Name/color sub-buses with mix_busses. This is the general routed/tapped authoring path the graded sessions also use. " +
			"By default the standard brain-control CC convention is baked in (mixer + transport + node-param CCs on channel 1, each tap's bypass on its own AutoToggle CC) so the session is brain-controllable with no hand-wiring; pass a convention object to customize it, or bare:true for an untouched placeholder session. Returns the build report and the download path. The last audio channel is treated as the master.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {"type": "string", "description": "Session title (private)."},
				"out_id": {"type": "string", "description": "Staging id (filename without .aumproj); defaults to a sanitized title."},
				"tempo": {"type": "number", "description": "BPM (default 120)."},
				"sample_rate": {"type": "number", "description": "Engine sample rate (default 48000)."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Hardware-I/O profile authored into hwBusses. \"builtin\" (default) = iPad mic+speaker only (device-independent; AUM repopulates on load). \"x32\" = the Behringer X32's 32-channel USB layout the real X32-rig sessions store (16 stereo pairs 0/1..30/31, no built-in), so HWInput/HWOutput channels pre-reference the desk and the master routes to the X32 main out (hw-bus index 0/1). AUM rebuilds hwBusses from the attached interface on load either way."},
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
								"description": "Pre-fader hosted AUv3 nodes (slot chain head): the source instrument followed by any pre-fader inserts.",
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
							},
							"source": {
								"type": "object",
								"description": "Audio strips only. A built-in slot0 audio source. Omit (or kind=instrument/none) to let the first hosted node head the chain.",
								"properties": {
									"kind": {"type": "string", "enum": ["instrument", "hwinput", "bus", "fileplayer", "none"], "description": "instrument/none = the first hosted node is the source (no built-in node); hwinput = read a hardware input bus; bus = read a mix bus (0 = master sum, for a master/submix strip); fileplayer = an empty AUM file player."},
									"hw_bus_index": {"type": "integer", "description": "hwinput: which hardware input bus."},
									"mono_select": {"type": "integer", "description": "hwinput: 0 stereo, 1 left, 2 right."},
									"bus_index": {"type": "integer", "description": "bus: which mix bus to read (0 = master sum)."}
								}
							},
							"output": {
								"type": "object",
								"description": "Audio strips only. The channel's fader/output routing node (placed at faderIndex). A normal channel sends to a mix bus; the master/monitor sends to a hardware output.",
								"properties": {
									"kind": {"type": "string", "enum": ["bus", "hardware", "none"], "description": "bus = BusDest into a mix bus (send to bus 0 to reach the master); hardware = HWOutput to a hardware output (the master/monitor; bus 0 = speaker / X32 main out)."},
									"bus_index": {"type": "integer", "description": "bus: which mix bus (0 = master sum)."},
									"hw_bus_index": {"type": "integer", "description": "hardware: which hardware output bus (0 = speaker / X32 main)."},
									"mono_select": {"type": "integer", "description": "hardware: 0 stereo, 1 left, 2 right."}
								}
							},
							"post_nodes": {
								"type": "array",
								"description": "Post-fader hosted AUv3 insert nodes (e.g. master FX), placed after the fader/output node, before any aux sends and the tap. Same item shape as nodes.",
								"items": {
									"type": "object",
									"properties": {
										"probe_id": {"type": "string", "description": "Staged auv3 probe id to host."},
										"probe_file": {"type": "string", "description": "Explicit probe dump path (overrides probe_id)."},
										"component_name": {"type": "string", "description": "Override the node's human name."},
										"preset": {"type": "integer", "description": "Optional factory preset index (AuPresetCtrl)."},
										"state": {"type": "object", "description": "Optional saved AU state (AuStateDoc) as fullState key -> string value.", "additionalProperties": {"type": "string"}}
									}
								}
							},
							"aux_sends": {
								"type": "array",
								"description": "Audio strips only. Post-fader aux sends: the channel's post-fader signal is also sent into extra mix buses while still flowing to its own output.",
								"items": {
									"type": "object",
									"properties": {
										"bus_index": {"type": "integer", "description": "Which mix bus to send into."},
										"amount": {"type": "number", "description": "Send level 0..1."}
									},
									"required": ["bus_index"]
								}
							},
							"tap": {"type": "boolean", "description": "Audio strips only. Append a post-fader ProbeAudioTap as the channel's last slot. With the default convention its bypass is mapped to its own AutoToggle CC so the brain can flip it."},
							"tap_node": {
								"type": "object",
								"description": "Audio strips only. Override the default ProbeAudioTap identity/state with a node from a staged probe (implies tap=true). Same item shape as nodes.",
								"properties": {
									"probe_id": {"type": "string", "description": "Staged ProbeAudioTap probe id."},
									"probe_file": {"type": "string", "description": "Explicit probe dump path (overrides probe_id)."},
									"component_name": {"type": "string", "description": "Override the tap's human name."},
									"preset": {"type": "integer", "description": "Optional factory preset index (AuPresetCtrl)."},
									"state": {"type": "object", "description": "Optional saved AU state (AuStateDoc), e.g. {\"probeAudioTapConfig\":\"...\"}.", "additionalProperties": {"type": "string"}}
								}
							}
						}
					}
				},
				"mix_busses": {
					"type": "array",
					"description": "Name and/or color specific mix buses (the Fast-Forward-style named sub-buses such as Drums Mix / Bass / Guitar). Unlisted buses stay the default unnamed/uncolored shape.",
					"items": {
						"type": "object",
						"properties": {
							"index": {"type": "integer", "description": "Which of the 16 mix buses (0..15)."},
							"name": {"type": "string", "description": "The bus customName (omit to leave it unnamed)."},
							"color": {"type": "object", "description": "Optional bus customColor as straight-alpha RGBA (each component 0..1).", "properties": {"r": {"type": "number"}, "g": {"type": "number"}, "b": {"type": "number"}, "a": {"type": "number"}}}
						},
						"required": ["index"]
					}
				},
				"bare": {"type": "boolean", "description": "Skip the default convention and author an untouched placeholder session (AUM's default, what an unmapped real session looks like). Ignored when a convention object is supplied."},
				"full_control": {"type": "boolean", "description": "Bank EVERY mappable target collision-free across channels (mixer/transport on the convention channel, node params + triggers banked from ch2 upward, CC then Note) instead of the single-channel convention. Use for dense multi-node sessions so node params never collide. Overrides bare/convention. See instrument_aum_session."},
				"convention": {
					"type": "object",
					"description": "Override the standard brain-control convention pre-assigned to the generated placeholders. Omit to bake the standard map (channel 1); set bare:true to skip it entirely.",
					"properties": {
						"channel": {"type": "integer", "description": "1-based MIDI/send channel the brain drives for the assigned CCs (1..16); stored on disk 0-based as channel-1. Default 1 (→ send channel 1)."},
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
				"decimation": {"type": "integer", "description": "Tap PCM decimation factor (default 4)."},
				"tap_name": {"type": "string", "description": "Name the embedded ProbeAudioTap streams under, so several taps can be told apart by get_audio_tap/probe_sound (name arg). Defaults to the session title. Multiple concurrently-streaming taps must use distinct names."}
			},
			"required": ["synth_probe", "brain_probe", "tap_probe", "host"]
		}`),
	}, s.handleAuthorLoopSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "instrument_aum_session",
		Description: "Give a session FULL control: bank every mappable target (mixer strips, transport, system, node reserved triggers, and every hosted plugin parameter) onto collision-free MIDI triggers, then re-stage it for download to the iPad. " +
			"The global channel (default 1) keeps the mixer/transport CC convention so a session-derived AUM mixer device still resolves; everything else banks from start_channel (default 2) upward, CC 0..127 then Note 0..127, advancing channels until 16. " +
			"This is also the \"update an existing session\" tool: with preserve_existing (default true) every already-enabled mapping is left untouched and routed around, so it is safe to re-run and to layer full control on top of a hand-mapped session. Overflowing targets (dense FX past channel 16) are reported, not fatal. dry_run returns the plan without writing. " +
			"Set add_probes:true (with host) to also EMBED the probe rig — a ProbeMidiBrain strip wired to AUM MIDI Control plus a ProbeAudioTap on tap_channel — so the golden session is self-contained: the agent drives it via the brain and hears it via the tap with nothing to add on the iPad.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id to instrument. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"out_id": {"type": "string", "description": "Staging id to write (defaults to <session>_golden)."},
				"global_channel": {"type": "integer", "description": "1-based channel for the global block (transport/system/preset) and the mixer-strip convention CCs. Default 1, or the session's existing convention channel when it already has one."},
				"start_channel": {"type": "integer", "description": "1-based channel the banked pool (node params, reserved triggers, non-convention strip targets) starts from. Default 2."},
				"use_notes": {"type": "boolean", "description": "Allow the pool to spill into the Note space once a channel's 128 CCs are full, before advancing channels. Default true. A control Note also reaches instruments on that channel, so use play_channels to exclude played channels."},
				"play_channels": {"type": "array", "items": {"type": "integer"}, "description": "1-based channels to exclude from Note allocation (their CC space is still used), so control Notes never sound on a channel an instrument plays."},
				"preserve_existing": {"type": "boolean", "description": "Leave already-enabled mappings untouched and route new ones around them. Default true (safe re-run / update)."},
				"add_probes": {"type": "boolean", "description": "Embed the probe rig so the golden session is self-contained and agent-controllable: append a ProbeMidiBrain MIDI strip (routed to AUM MIDI Control, merged into any existing matrix) and insert a ProbeAudioTap into tap_channel's chain. Requires host. The brain emits the banked CCs/Notes; the tap streams audio back."},
				"host": {"type": "string", "description": "Daemon host[:port] the embedded brain and tap dial back to (required when add_probes). E.g. \"demiurg.local:7800\"."},
				"tap_channel": {"type": "integer", "description": "0-based channel index to insert the ProbeAudioTap into (its audio is what get_audio_tap streams). Default 0. Pick the channel of the instrument you want to hear."},
				"tap_name": {"type": "string", "description": "Name the embedded ProbeAudioTap streams under, so several taps can be told apart by get_audio_tap/probe_sound (name arg). Defaults to the session title. Multiple concurrently-streaming taps must use distinct names."},
				"brain_probe": {"type": "string", "description": "Optional staged ProbeMidiBrain probe id (defaults to the known brain component if omitted)."},
				"brain_file": {"type": "string", "description": "Explicit ProbeMidiBrain probe dump path (overrides brain_probe)."},
				"tap_probe": {"type": "string", "description": "Optional staged ProbeAudioTap probe id (defaults to the known tap component if omitted)."},
				"tap_file": {"type": "string", "description": "Explicit ProbeAudioTap probe dump path (overrides tap_probe)."},
				"decimation": {"type": "integer", "description": "Tap feature decimation factor baked into the tap config (default 4)."},
				"dry_run": {"type": "boolean", "description": "Return the allocation report without writing a file."}
			}
		}`),
	}, s.handleInstrumentAUMSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "author_probe_session",
		Description: "Author a minimal probe rig .aumproj in one call: a MIDI strip hosting ProbeMidiBrain (the hands), an audio strip hosting ProbeAudioTap (the ears), and a master strip — no synth (the difference from author_loop_session). " +
			"Wires the brain's MIDI OUT to AUM's MIDI Control and authors the brain/tap AuStateDoc with the daemon host so both auto-connect on load. " +
			"By default the standard brain-control convention (channel 1) is baked in; set full_control:true to instead bank every mappable target collision-free (see instrument_aum_session).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"brain_probe": {"type": "string", "description": "Staged probe id of ProbeMidiBrain (run the auv3 probe for it once)."},
				"brain_file": {"type": "string", "description": "Explicit ProbeMidiBrain probe dump path (overrides brain_probe)."},
				"tap_probe": {"type": "string", "description": "Staged probe id of ProbeAudioTap."},
				"tap_file": {"type": "string", "description": "Explicit ProbeAudioTap probe dump path (overrides tap_probe)."},
				"host": {"type": "string", "description": "Daemon LAN host[:port] (e.g. \"box:7800\") embedded into the brain + tap config so they dial back automatically. Installation-specific; never committed."},
				"title": {"type": "string", "description": "Session title (default \"Probe Rig\")."},
				"out_id": {"type": "string", "description": "Staging id / filename stem (default from title)."},
				"tempo": {"type": "number", "description": "Session tempo BPM (default 120)."},
				"decimation": {"type": "integer", "description": "Tap PCM decimation factor (default 4)."},
				"tap_name": {"type": "string", "description": "Name the embedded ProbeAudioTap streams under, so several taps can be told apart by get_audio_tap/probe_sound (name arg). Defaults to the session title. Multiple concurrently-streaming taps must use distinct names."},
				"full_control": {"type": "boolean", "description": "Bank every mappable target collision-free instead of the single-channel convention."}
			},
			"required": ["brain_probe", "tap_probe", "host"]
		}`),
	}, s.handleAuthorProbeSession)

	s.mcp.AddTool(&mcp.Tool{
		Name: "author_graded_session",
		Description: "Author one (or all) of the graded reference sessions S1..S5 from scratch and stage it for download to the iPad. The ladder replicates the reference-project structures, each carrying a ProbeMidiBrain MIDI strip plus a post-fader ProbeAudioTap in EVERY audio channel, the brain wired to every instrument + AUM MIDI Control, and (by default) each tap's bypass on its own AutoToggle CC so the brain can flip any channel's tap. " +
			"Rungs: s1 one-synth (smallest path), s2 trio (3-instrument sum), s3 inputs (System collapse skeleton), s4 sub-mix (Kings Cross / Neon Ghosts sub-bus shape), s5 fast-forward (the full Fast-Forward-class replica: HW-input drums, named sub-buses, two MIDI strips, a monitor send, master FX). " +
			"By default synthetic placeholder instruments are hosted; pass synth_probe to host a real staged AUv3. Pass host to embed the brain/tap daemon config so they auto-connect on load.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"rung": {"type": "string", "enum": ["s1", "s2", "s3", "s4", "s5", "all"], "description": "Which rung to author: s1..s5, or \"all\" to stage every rung."},
				"host": {"type": "string", "description": "Daemon LAN host[:port] (e.g. \"box:7800\") embedded into the ProbeMidiBrain + ProbeAudioTap config so they dial back automatically. Installation-specific; never committed."},
				"synth_probe": {"type": "string", "description": "Staged auv3 probe id to host as the instrument on every instrument channel (its identity + params seed the node). Defaults to a synthetic placeholder synth."},
				"synth_file": {"type": "string", "description": "Explicit instrument probe dump path (overrides synth_probe)."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Override the hardware-I/O profile for the rung (default: each rung's natural profile — built-in for the pure-instrument rungs, x32 for the HW-I/O rungs S3/S5)."},
				"tempo": {"type": "number", "description": "Override the rung's tempo (BPM)."},
				"out_id": {"type": "string", "description": "Staging id / filename stem (default: the rung id, e.g. graded-s1-one-synth). Ignored for rung=all."},
				"bare": {"type": "boolean", "description": "Author bare placeholder sessions (no convention, no tap toggles assigned)."},
				"tap_name": {"type": "string", "description": "Name the ProbeAudioTap streams under (default: the session title). Only meaningful with host."},
				"decimation": {"type": "integer", "description": "Tap PCM decimation factor baked into the tap config (default 4). Only meaningful with host."}
			},
			"required": ["rung"]
		}`),
	}, s.handleAuthorGradedSession)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "edit_aum_session",
		Description: "Edit a staged session in place and re-stage it: assign MIDI-control mappings (collection/target/type/data1/channel, plus optional range min/max, cycle and invert), and set channel fader/mute/solo. Writes the result back as out_id (defaults to overwriting the source) for download to the iPad. Use get_aum_session to discover collection/target paths.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Staged session id to edit. See list_aum_sessions."},
				"file": {"type": "string", "description": "Explicit path to a .aumproj (overrides session_id)."},
				"out_id": {"type": "string", "description": "Staging id to write (defaults to session_id, overwriting it)."},
				"mappings": {
					"type": "array",
					"description": "Mapping assignments for a version-13 (specState) session. type codes (confirmed): 0=CC, 1=Note, 2=Program Change, 3=PBEND/CHPRS (data1 0=PBEND, 1=CHPRS); defaults to 0 (CC). channel is the raw 0-based on-disk channel (0 = MIDI/send ch1 … 15 = ch16), matching what get_aum_session reports; the brain drives it on channel+1. Optional per-mapping range (min/max, normalised 0..1), cycle (autoToggle) and invert (swap min/max) match AUM's mapping panel.",
					"items": {
						"type": "object",
						"properties": {
							"collection": {"type": "string"},
							"target": {"type": "string"},
							"type": {"type": "integer"},
							"data1": {"type": "integer"},
							"channel": {"type": "integer"},
							"min": {"type": "number", "description": "Input range minimum, normalised 0..1 (AUM's 0%). Default 0."},
							"max": {"type": "number", "description": "Input range maximum, normalised 0..1 (AUM's 100%). Default 1. For a Tempo (CHPRS) mapping, 35%..100% is min=0.3529, max=1."},
							"cycle": {"type": "boolean", "description": "AUM's \"Cycle\" flag (autoToggle): step through values on each non-zero message instead of latching >64."},
							"invert": {"type": "boolean", "description": "Invert the mapping (swap min/max). Applied after min/max, so invert:true with no range swaps the default 0..1 to 1..0."}
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

	// Session switching: the persisted PC-to-session registry behind the
	// brain's switcher row and AUM's hand-mapped global "Session Load"
	// actions. Handlers live in aum_session_switch.go.
	s.mcp.AddTool(&mcp.Tool{
		Name: "register_aum_session_switch",
		Description: "Pin a staged AUM session to a Program Change on the reserved session-switch channel (16), so it becomes switchable: by switch_aum_session and by the brain's on-device switcher row. " +
			"Programs are pinned forever (never renumbered) because the user hand-maps AUM's global \"Session Load\" action to each PC once via Learn — the returned setup line is that one-time wiring. Re-pushes the control-surface manifest so a connected brain shows the new entry.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session": {"type": "string", "description": "Staged session id to pin (see list_aum_sessions)."},
				"program": {"type": "integer", "description": "Explicit PC program 0..127 (default: the next free one)."}
			},
			"required": ["session"]
		}`),
	}, s.handleRegisterAUMSessionSwitch)

	s.mcp.AddTool(&mcp.Tool{
		Name: "list_aum_session_switches",
		Description: "List the session-switch registry: per entry the pinned PC program, the staged session, and the one-time AUM Learn wiring cheat-sheet line (\"AUM > MIDI Control > Session Load <name> <- PC <program> ch16\"). " +
			"Marks the daemon's current session.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleListAUMSessionSwitches)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "remove_aum_session_switch",
		Description: "Remove a session from the session-switch registry. Its program becomes a hole (other entries are never renumbered, so their hand-wired AUM mappings keep working); remember to also delete the matching Session Load action in AUM's MIDI Control.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session": {"type": "string", "description": "Registered session id (see list_aum_session_switches)."}
			},
			"required": ["session"]
		}`),
	}, s.handleRemoveAUMSessionSwitch)

	s.mcp.AddTool(&mcp.Tool{
		Name: "switch_aum_session",
		Description: "Switch AUM to a registered session: send its pinned Program Change through the brain (AUM fires the hand-mapped global Session Load), set it as the daemon's current session, re-import its rig and re-push the control-surface manifest — so the control_* tools, web UI and brain surface all target the new session. " +
			"Requires a connected brain and the one-time AUM wiring from register_aum_session_switch.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session": {"type": "string", "description": "Registered session id to switch to."},
				"program": {"type": "integer", "description": "Or the pinned PC program (overridden by session)."}
			}
		}`),
	}, s.handleSwitchAUMSession)
}

// --- list / get -----------------------------------------------------------

// aumSessionRow is the machine-readable per-file row for list_aum_sessions.
type aumSessionRow struct {
	ID   string `json:"id"`
	File string `json:"file"`
	// Path is the file's staging-dir-relative path. Staging mirrors the iPad's
	// AUM folder tree, so this is also where the session lives on the iPad.
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Version  int    `json:"version,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Mappings int    `json:"mappings,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) handleListAUMSessions(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := config.AUMSessionsDir()

	var rows []aumSessionRow
	walkErr := aum.WalkStaged(dir, func(rel, full, kind string, _ fs.FileInfo) {
		row := aumSessionRow{
			ID:   aum.StripExt(rel),
			File: path.Base(rel),
			Path: rel,
			Kind: kind,
		}
		summarizeStaged(full, &row)
		rows = append(rows, row)
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return structResult(fmt.Sprintf("no staged AUM sessions (%s does not exist yet); upload one from the iPad or author one with author_aum_session", dir), map[string]any{"sessions": []aumSessionRow{}}), nil
		}
		return textResult("read sessions dir: "+walkErr.Error(), true), nil
	}
	if len(rows) == 0 {
		return structResult(fmt.Sprintf("no staged AUM sessions in %s", dir), map[string]any{"sessions": []aumSessionRow{}}), nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

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
			extra := ""
			if m.Min != 0 || m.Max != 1 {
				extra += fmt.Sprintf(" range=%.4g..%.4g", m.Min, m.Max)
			}
			if m.AutoToggle {
				extra += " cycle"
			}
			fmt.Fprintf(&b, "      %s/%s -> %s (type=%d) data1=%d ch=%d (send ch%d)%s\n", m.Collection, m.Target, m.TypeName, m.Type, m.Data1, m.Channel, m.Channel+1, extra)
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
		"sessionID":     stagedRelID(path),
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
	for _, d := range s.eng.Devices() {
		if s.eng.IsUSBDevice(d.Name) {
			continue
		}
		if !d.HasControl() {
			continue
		}
		ch := d.ControlChannel()
		if !seen[ch] {
			seen[ch] = true
			out = append(out, ch)
		}
	}
	sort.Ints(out)
	return out
}

// --- import ---------------------------------------------------------------

// proposedBinding is one suggested rig binding derived from a session node (or
// the session-derived mixer). Channel is the 1-based send channel; the wire
// (0-based) channel a device binds on is Channel-1.
type proposedBinding struct {
	Name         string                 `json:"name"`
	Device       string                 `json:"device"`
	Channel      int                    `json:"channel"`
	Endpoint     string                 `json:"endpoint"`
	ChannelIndex int                    `json:"channelIndex"`
	Slot         int                    `json:"slot"`
	Component    *device.ProbeComponent `json:"component,omitempty"`
	MatchedProbe string                 `json:"matchedProbe,omitempty"`
	Note         string                 `json:"note,omitempty"`
}

// createdDevice is one device the import auto-created (bound), reported in the
// structured output alongside the proposals it could not create.
type createdDevice struct {
	Name     string `json:"name"`
	Device   string `json:"device"`
	Channel  int    `json:"channel"`  // 1-based send channel (cosmetic: controls pin their own)
	Endpoint string `json:"endpoint"` // auv3midi brain endpoint
	Kind     string `json:"kind"`     // "session" | "node"
	Controls int    `json:"controls"` // controls derived from the session's mappings
}

// aumImportOutcome is the machine-readable result of one session-rig import,
// shared by the import_aum_session tool and the auto-import path (session
// download / brain connect). surface carries the created devices' types +
// binding channels — the input for the controlSurface manifest push. It never
// leaves the package, hence the unexported fields.
type aumImportOutcome struct {
	sessionID  string
	title      string
	autoCreate bool
	rig        *aum.Rig
	sm         aum.SessionMap
	dumps      int
	matched    int
	unmatched  []string
	created    []createdDevice
	proposed   []proposedBinding
	replaced   []string
	surface    []surfaceSource
}

// surfaceSource is one auto-created device feeding the controlSurface
// manifest: its logical name, generated type, and the 1-based binding send
// channel (the fallback for controls that do not pin their own).
type surfaceSource struct {
	logical string
	dt      *device.DeviceType
	send    int
}

// importSessionRig is the import_aum_session core: derive the rig from the
// session file's enabled mappings, replace the previous session's auto-created
// devices, and create (or propose) this session's. Imports are serialized
// under aumImportMu because the auto-import callbacks (download / brain
// connect) can race a tool-driven import.
func (s *Server) importSessionRig(path string, proposeOnly bool) (*aumImportOutcome, error) {
	s.aumImportMu.Lock()
	defer s.aumImportMu.Unlock()

	sess, err := aum.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	sm := sess.Map()
	dumps := loadStagedProbeDumps()
	// The staging-relative id ("Live sets/Set"), so the outcome's session id
	// matches the current-session marker the download hook records. DeriveRig
	// sanitizes it into the device-type id prefix.
	sessionID := stagedRelID(path)

	// The rig comes straight from the session's enabled mappings (the file is
	// the single source of truth); staged probes only enrich node controls.
	rig, err := aum.DeriveRig(sess, sessionID, dumps)
	if err != nil {
		return nil, fmt.Errorf("derive rig: %w", err)
	}

	// Auto-create requires the auv3midi transport to be registered (the AUM rig
	// rides the brain channel). When it is missing — or the caller forced
	// propose_only — fall back to proposing every device.
	o := &aumImportOutcome{
		sessionID:  sessionID,
		title:      sess.Title(),
		autoCreate: !proposeOnly && s.eng.HasTransport(auv3midiTransport),
		rig:        rig,
		sm:         sm,
		dumps:      len(dumps),
	}

	used := map[string]bool{}
	var persistNeeded bool
	for _, rn := range rig.Nodes {
		if rn.MatchedProbe != "" {
			o.matched++
		} else {
			o.unmatched = append(o.unmatched, fmt.Sprintf("%s [%s/%s/%s]", rn.ComponentName, rn.Component.Type, rn.Component.Subtype, rn.Component.Manufacturer))
		}
	}

	// --- Session-rig lifecycle: a new import replaces the previous one -----
	// Every device a prior import auto-created carries a session tag; remove
	// them (tools, bindings AND their generated types) before creating this
	// session's rig so imports never pile up. Hand-bound devices (no tag) are
	// untouched. A session that derives no controls leaves the previous rig
	// alone — wiping a working rig over an unmapped session (e.g. an
	// accidental download) helps nobody.
	if o.autoCreate && rig.Controls() > 0 {
		retiredTypes := map[string]bool{}
		for _, d := range s.eng.Devices() {
			if d.Session == "" {
				continue
			}
			s.removeToolsForDevice(d.Name)
			s.eng.Unbind(d.Name)
			retiredTypes[d.DeviceID] = true
			o.replaced = append(o.replaced, d.Name)
			persistNeeded = true
		}
		sort.Strings(o.replaced)
		// Retire the replaced devices' generated types (registry + staged
		// YAML), unless a remaining device still references one. Same-session
		// ids get re-created right below; different-session ids would
		// otherwise accumulate forever.
		for _, d := range s.eng.Devices() {
			delete(retiredTypes, d.DeviceID)
		}
		for id := range retiredTypes {
			s.unregisterDeviceType(id)
		}
	}

	// --- The session device (strips/transport/system/built-ins/taps) -------
	if rig.Session != nil {
		logical := uniqueLogical("aum", used)
		send := clampSend(rig.SessionSendChannel)
		if o.autoCreate {
			if err := s.createAUMDevice(rig.Session, logical, send, sessionID); err != nil {
				o.proposed = append(o.proposed, proposedBinding{
					Name: logical, Device: rig.Session.ID, Channel: send,
					Endpoint: auv3midiBrainEndpoint, ChannelIndex: -1,
					Note: "session device (auto-create failed: " + err.Error() + ")",
				})
			} else {
				persistNeeded = true
				o.created = append(o.created, createdDevice{
					Name: logical, Device: rig.Session.ID, Channel: send,
					Endpoint: auv3midiBrainEndpoint, Kind: "session", Controls: len(rig.Session.Controls),
				})
				o.surface = append(o.surface, surfaceSource{logical: logical, dt: rig.Session, send: send})
			}
		} else {
			o.proposed = append(o.proposed, proposedBinding{
				Name: logical, Device: rig.Session.ID, Channel: send,
				Endpoint: auv3midiBrainEndpoint, ChannelIndex: -1,
				Note: fmt.Sprintf("session device (%d mapped control(s); propose-only)", len(rig.Session.Controls)),
			})
		}
	}

	// --- Hosted AUv3 nodes -> devices -------------------------------------
	for _, rn := range rig.Nodes {
		logical := uniqueLogical(rn.Base, used)
		comp := rn.Component
		pb := proposedBinding{
			Name:         logical,
			Endpoint:     auv3midiBrainEndpoint,
			ChannelIndex: rn.ChannelIndex,
			Slot:         rn.Slot,
			Component:    &comp,
			MatchedProbe: rn.MatchedProbe,
			Channel:      rn.SendChannel,
		}
		if rn.Type == nil {
			pb.Note = "no enabled MIDI mappings for this node — wire it (instrument_aum_session or map in AUM), then re-import"
			o.proposed = append(o.proposed, pb)
			continue
		}
		pb.Device = rn.Type.ID
		if rn.MatchedProbe == "" {
			pb.Note = "no staged probe matched (controls named from the session's target keys)"
		}
		if o.autoCreate {
			if err := s.createAUMDevice(rn.Type, logical, rn.SendChannel, sessionID); err != nil {
				pb.Note = strings.TrimSpace(pb.Note + " (auto-create failed: " + err.Error() + ")")
				o.proposed = append(o.proposed, pb)
				continue
			}
			persistNeeded = true
			o.created = append(o.created, createdDevice{
				Name: logical, Device: rn.Type.ID, Channel: rn.SendChannel,
				Endpoint: auv3midiBrainEndpoint, Kind: "node", Controls: len(rn.Type.Controls),
			})
			o.surface = append(o.surface, surfaceSource{logical: logical, dt: rn.Type, send: rn.SendChannel})
			continue
		}
		o.proposed = append(o.proposed, pb)
	}

	if persistNeeded {
		if err := s.persistDevices(); err != nil {
			// The devices are bound + tools generated; only persistence failed.
			o.created = append(o.created, createdDevice{Name: "(warning)", Device: "could not persist devices.yaml: " + err.Error()})
		}
	}
	return o, nil
}

func (s *Server) handleImportAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID   string `json:"session_id"`
		File        string `json:"file"`
		ProposeOnly bool   `json:"propose_only"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	path, err := resolveAUMSessionPath(args.File, args.SessionID)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	o, err := s.importSessionRig(path, args.ProposeOnly)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	// A tool-driven import that changed the rig — created devices, or replaced
	// the previous session's even when its own creations all failed — makes
	// its session the daemon's current session: the marker must track what the
	// rig models, and a brain reconnect re-imports this one. Notify watchers
	// and push the control-surface manifest, same as the auto-import path.
	if len(o.surface) > 0 || len(o.replaced) > 0 {
		s.setCurrentAUMSession(o.sessionID)
		s.notifyAUMRig("import_aum_session", o)
		s.pushControlSurface(o)
	}

	var b strings.Builder
	if o.autoCreate {
		fmt.Fprintf(&b, "import of %q: auto-created %d device(s) with %d control(s) from the session's mappings, %d node(s) total (%d matched a staged probe)\n",
			o.title, len(o.created), o.rig.Controls(), len(o.rig.Nodes), o.matched)
	} else {
		fmt.Fprintf(&b, "import of %q: proposing devices (%d mapped control(s)), %d node(s) total (%d matched a staged probe)\n",
			o.title, o.rig.Controls(), len(o.rig.Nodes), o.matched)
	}
	if len(o.replaced) > 0 {
		fmt.Fprintf(&b, "replaced %d device(s) from the previous session import: %s\n", len(o.replaced), strings.Join(o.replaced, ", "))
	}
	for _, cd := range o.created {
		if cd.Kind == "" {
			fmt.Fprintf(&b, "  %s\n", cd.Device) // a warning row
			continue
		}
		fmt.Fprintf(&b, "  created %-22s device=%s channel=%d endpoint=%s [%s, %d control(s)]\n", cd.Name, cd.Device, cd.Channel, cd.Endpoint, cd.Kind, cd.Controls)
	}
	for _, pb := range o.proposed {
		dev := pb.Device
		if dev == "" {
			dev = "?"
		}
		fmt.Fprintf(&b, "  %-24s device=%s channel=%d (chan%d/slot%d)", pb.Name, dev, pb.Channel, pb.ChannelIndex, pb.Slot)
		if pb.MatchedProbe != "" {
			fmt.Fprintf(&b, " probe=%s", pb.MatchedProbe)
		}
		if pb.Note != "" {
			fmt.Fprintf(&b, " — %s", pb.Note)
		}
		b.WriteByte('\n')
	}
	if len(o.unmatched) > 0 {
		fmt.Fprintf(&b, "node(s) without a staged probe (controls still derived from the session): %d\n", len(o.unmatched))
	}
	if n := len(o.rig.Skipped); n > 0 {
		fmt.Fprintf(&b, "%d enabled mapping(s) not expressible as controls:\n", n)
		const show = 10
		for i, sk := range o.rig.Skipped {
			if i >= show {
				fmt.Fprintf(&b, "  ... and %d more\n", n-show)
				break
			}
			fmt.Fprintf(&b, "  %s/%s (%s): %s\n", sk.Collection, sk.Target, sk.TypeName, sk.Reason)
		}
	}
	if len(o.proposed) > 0 {
		b.WriteString("review the proposal(s), then create with bind_device (transport auv3midi, endpoint \"brain\").\n")
	}

	structured := map[string]any{
		"sessionID":        o.sessionID,
		"title":            o.title,
		"autoCreated":      o.created,
		"proposedDevices":  o.proposed,
		"replacedDevices":  o.replaced,
		"derivedControls":  o.rig.Controls(),
		"skippedMappings":  o.rig.Skipped,
		"matchedNodes":     o.matched,
		"unmatchedNodes":   o.unmatched,
		"sessionMap":       o.sm,
		"stagedProbeCount": o.dumps,
	}
	return structResult(b.String(), structured), nil
}

// createAUMDevice stages a generated device type (register + persist) and binds
// a single-connection device on it over the auv3midi brain channel, generating
// its control tool. send is the 1-based send channel; the wire (0-based)
// channel stored on the device is send-1 (clamped to 0) — cosmetic for
// session-derived types, whose controls pin their own channels. session tags
// the device for the session-rig replace-on-reimport lifecycle. It does not
// persist devices.yaml — the caller batches that once.
func (s *Server) createAUMDevice(dt *device.DeviceType, logical string, send int, session string) error {
	if err := s.registerDeviceType(dt); err != nil {
		return err
	}
	wire := clampSend(send) - 1
	d := engine.Device{
		Name:     logical,
		DeviceID: dt.ID,
		Endpoint: auv3midiBrainEndpoint,
		Channel:  wire,
		Session:  session,
	}
	if err := s.eng.Bind(d); err != nil {
		return err
	}
	s.refreshToolsForDevice(d)
	return nil
}

// unregisterDeviceType retires a generated device type: drop it from the
// registry and delete its staged YAML — the inverse of registerDeviceType,
// used when a session re-import replaces the previous session's rig. Best
// effort: a missing file is fine, anything else is only logged.
func (s *Server) unregisterDeviceType(id string) {
	s.eng.Registry().Remove(id)
	if err := os.Remove(filepath.Join(config.DeviceTypesDir(), id+".yaml")); err != nil && !os.IsNotExist(err) {
		log.Printf("retire device type %q: %v", id, err)
	}
}

// clampSend normalizes a suggested 1-based send channel to the binding floor:
// DeriveRig reports 0 when it could not infer one, and channel 1 is the
// harmless default (session-derived controls pin their own channels anyway).
func clampSend(send int) int {
	if send < 1 {
		return 1
	}
	return send
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

// parseHardwareProfile maps the tool's "hardware" arg to an aum.HardwareProfile.
// "" / "builtin" is the device-independent default; "x32" enumerates the
// Behringer X32 USB buses. Any other value is a user-facing error.
func parseHardwareProfile(s string) (aum.HardwareProfile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "builtin":
		return aum.HardwareBuiltIn, nil
	case "x32":
		return aum.HardwareX32, nil
	default:
		return aum.HardwareBuiltIn, fmt.Errorf("/hardware: %q is not builtin|x32", s)
	}
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

// nodeArg is the JSON shape of one hosted AUv3 node in author tool input,
// shared by a channel's pre-fader nodes, post-fader nodes, and tap override.
type nodeArg struct {
	ProbeID       string            `json:"probe_id"`
	ProbeFile     string            `json:"probe_file"`
	ComponentName string            `json:"component_name"`
	Preset        *int              `json:"preset"`
	State         map[string]string `json:"state"`
}

// resolve turns a nodeArg into an aum.NodeSpec: it resolves the probe (id or
// explicit file) for the node's identity + mappable params, then applies the
// optional component-name / preset / state overrides. The caller prefixes the
// returned error with the JSON field path and supplies the loaded audio-unit
// default states (loaded once per author call, not per node).
func (a nodeArg) resolve(defs []device.AUv3DefaultState) (aum.NodeSpec, error) {
	if a.ProbeID == "" && a.ProbeFile == "" {
		return aum.NodeSpec{}, fmt.Errorf("provide probe_id or probe_file (a hosted node needs a probe for its identity + params)")
	}
	ppath, perr := resolveProbePath(a.ProbeFile, a.ProbeID)
	if perr != nil {
		return aum.NodeSpec{}, perr
	}
	dump, derr := readProbeDump(ppath)
	if derr != nil {
		return aum.NodeSpec{}, fmt.Errorf("read probe: %w", derr)
	}
	ns := aum.NodeSpecFromDump(dump)
	if a.ComponentName != "" {
		ns.ComponentName = a.ComponentName
	}
	if a.Preset != nil {
		ns.Preset = a.Preset
	}
	if len(a.State) > 0 {
		ns.StateDoc = stateDocBytes(a.State)
	}
	// Fill any fullState keys the per-call `state` arg did not set from this
	// audio unit's user-defined default state (capture_auv3_default_state).
	if derr := applyDefaultState(&ns, defs); derr != nil {
		return aum.NodeSpec{}, derr
	}
	return ns, nil
}

// sourceArg is the JSON shape of a channel's built-in slot0 audio source.
type sourceArg struct {
	Kind       string `json:"kind"`
	HWBusIndex int    `json:"hw_bus_index"`
	MonoSelect int    `json:"mono_select"`
	BusIndex   int    `json:"bus_index"`
}

// outputArg is the JSON shape of a channel's fader/output routing node.
type outputArg struct {
	Kind       string `json:"kind"`
	BusIndex   int    `json:"bus_index"`
	HWBusIndex int    `json:"hw_bus_index"`
	MonoSelect int    `json:"mono_select"`
}

// auxSendArg is the JSON shape of one post-fader aux send.
type auxSendArg struct {
	BusIndex int     `json:"bus_index"`
	Amount   float64 `json:"amount"`
}

// mixBusArg is the JSON shape of one named/colored mix bus.
type mixBusArg struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Color *struct {
		R float64 `json:"r"`
		G float64 `json:"g"`
		B float64 `json:"b"`
		A float64 `json:"a"`
	} `json:"color"`
}

// parseSourceKind maps the tool's channel source "kind" to an aum.SourceKind.
// "" / "none" is SourceNone (no built-in source node); any other value is a
// user-facing error.
func parseSourceKind(s string) (aum.SourceKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return aum.SourceNone, nil
	case "instrument":
		return aum.SourceInstrument, nil
	case "hwinput":
		return aum.SourceHWInput, nil
	case "bus":
		return aum.SourceBus, nil
	case "fileplayer":
		return aum.SourceFilePlayer, nil
	default:
		return aum.SourceNone, fmt.Errorf("%q is not instrument|hwinput|bus|fileplayer", s)
	}
}

// parseOutputKind maps the tool's channel output "kind" to an aum.OutputKind.
// "" / "none" is OutputNone (no fader/output node); any other value is a
// user-facing error.
func parseOutputKind(s string) (aum.OutputKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return aum.OutputNone, nil
	case "bus":
		return aum.OutputBus, nil
	case "hardware":
		return aum.OutputHardware, nil
	default:
		return aum.OutputNone, fmt.Errorf("%q is not bus|hardware", s)
	}
}

func (s *Server) handleAuthorAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Title      string  `json:"title"`
		OutID      string  `json:"out_id"`
		Tempo      float64 `json:"tempo"`
		SampleRate float64 `json:"sample_rate"`
		Hardware   string  `json:"hardware"`
		Channels   []struct {
			Kind      string       `json:"kind"`
			Title     string       `json:"title"`
			Fader     *float64     `json:"fader"`
			Muted     bool         `json:"muted"`
			Soloed    bool         `json:"soloed"`
			Nodes     []nodeArg    `json:"nodes"`
			Source    *sourceArg   `json:"source"`
			Output    *outputArg   `json:"output"`
			PostNodes []nodeArg    `json:"post_nodes"`
			AuxSends  []auxSendArg `json:"aux_sends"`
			Tap       bool         `json:"tap"`
			TapNode   *nodeArg     `json:"tap_node"`
		} `json:"channels"`
		MixBusses   []mixBusArg `json:"mix_busses"`
		Bare        bool        `json:"bare"`
		FullControl bool        `json:"full_control"`
		Convention  *struct {
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

	hardware, herr := parseHardwareProfile(args.Hardware)
	if herr != nil {
		return textResult(herr.Error(), true), nil
	}
	spec := aum.BuildSpec{
		Title:      args.Title,
		Tempo:      args.Tempo,
		SampleRate: args.SampleRate,
		Hardware:   hardware,
	}
	defaults := loadAUv3DefaultStates()
	for i, ch := range args.Channels {
		cs := aum.ChannelSpec{
			Kind:   aum.KindAudio,
			Title:  ch.Title,
			Fader:  ch.Fader,
			Muted:  ch.Muted,
			Soloed: ch.Soloed,
			Tap:    ch.Tap,
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
			ns, nerr := n.resolve(defaults)
			if nerr != nil {
				return textResult(fmt.Sprintf("/channels/%d/nodes/%d: %v", i, j, nerr), true), nil
			}
			cs.Nodes = append(cs.Nodes, ns)
		}
		if ch.Source != nil {
			kind, kerr := parseSourceKind(ch.Source.Kind)
			if kerr != nil {
				return textResult(fmt.Sprintf("/channels/%d/source/kind: %v", i, kerr), true), nil
			}
			if kind != aum.SourceNone {
				cs.Source = &aum.ChannelSource{
					Kind:       kind,
					HWBusIndex: ch.Source.HWBusIndex,
					MonoSelect: ch.Source.MonoSelect,
					BusIndex:   ch.Source.BusIndex,
				}
			}
		}
		if ch.Output != nil {
			kind, kerr := parseOutputKind(ch.Output.Kind)
			if kerr != nil {
				return textResult(fmt.Sprintf("/channels/%d/output/kind: %v", i, kerr), true), nil
			}
			if kind != aum.OutputNone {
				cs.Output = &aum.ChannelOutput{
					Kind:       kind,
					BusIndex:   ch.Output.BusIndex,
					HWBusIndex: ch.Output.HWBusIndex,
					MonoSelect: ch.Output.MonoSelect,
				}
			}
		}
		for j, n := range ch.PostNodes {
			ns, nerr := n.resolve(defaults)
			if nerr != nil {
				return textResult(fmt.Sprintf("/channels/%d/post_nodes/%d: %v", i, j, nerr), true), nil
			}
			cs.PostNodes = append(cs.PostNodes, ns)
		}
		for _, snd := range ch.AuxSends {
			cs.AuxSends = append(cs.AuxSends, aum.AuxSend{BusIndex: snd.BusIndex, Amount: snd.Amount})
		}
		// tap_node overrides the default tap identity and implies Tap so a
		// caller can request a tap by supplying the node alone.
		if ch.TapNode != nil {
			ns, nerr := ch.TapNode.resolve(defaults)
			if nerr != nil {
				return textResult(fmt.Sprintf("/channels/%d/tap_node: %v", i, nerr), true), nil
			}
			cs.TapNode = &ns
			cs.Tap = true
		}
		spec.Channels = append(spec.Channels, cs)
	}
	for i, mb := range args.MixBusses {
		ms := aum.MixBusSpec{Index: mb.Index, Name: mb.Name}
		if mb.Color != nil {
			ms.Color = &aum.RGBAColor{R: mb.Color.R, G: mb.Color.G, B: mb.Color.B, A: mb.Color.A}
		}
		if mb.Index < 0 || mb.Index > 15 {
			return textResult(fmt.Sprintf("/mix_busses/%d/index: %d out of range (0..15)", i, mb.Index), true), nil
		}
		spec.MixBusses = append(spec.MixBusses, ms)
	}
	switch {
	case args.FullControl:
		// Build a plain placeholder catalogue, then bank every target below.
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
	var instReport aum.InstrumentReport
	if args.FullControl {
		instReport, err = sess.Instrument(aum.InstrumentOptions{UseNotes: true})
		if err != nil {
			return textResult("instrument session: "+err.Error(), true), nil
		}
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

	id := device.FirstNonEmpty(sanitize.ID(args.OutID), sanitize.ID(args.Title), "session")
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
	if args.FullControl {
		writeFullControl(&b, instReport)
	} else if len(report.Overflow) > 0 {
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
	if args.FullControl {
		structured["instrumentReport"] = instReport
	}
	return structResult(b.String(), structured), nil
}

// --- author_loop_session --------------------------------------------------

// loopNodeSpec resolves a probe (id or explicit file) into a NodeSpec, returning
// a user-facing error string on failure. defs are the audio-unit default states
// loaded once by the caller (not re-read per node).
func loopNodeSpec(field, probeID, probeFile string, defs []device.AUv3DefaultState) (aum.NodeSpec, string) {
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
	ns := aum.NodeSpecFromDump(dump)
	if aerr := applyDefaultState(&ns, defs); aerr != nil {
		return aum.NodeSpec{}, fmt.Sprintf("%s: %v", field, aerr)
	}
	return ns, ""
}

// configureBrainTap authors the ProbeMidiBrain + ProbeAudioTap AuStateDoc so
// both auto-connect to the daemon on load: brain control enabled and tap
// streaming enabled, both pointed at host. A decimation <= 0 falls back to 4.
// A non-empty tapName is embedded in the tap config so the daemon can keep
// several taps apart (the tap dials /audio-stream with this name); empty leaves
// the tap un-named (the daemon then keys it by its remote address).
func configureBrainTap(brain, tap *aum.NodeSpec, host string, decimation int, tapName string) {
	brainCfg, _ := json.Marshal(map[string]any{"host": host, "controlEnabled": true})
	brain.StateDoc = map[string][]byte{"probeMidiBrainConfig": brainCfg}
	if decimation <= 0 {
		decimation = 4
	}
	tapCfg := map[string]any{"host": host, "streaming": true, "decimation": decimation}
	if strings.TrimSpace(tapName) != "" {
		tapCfg["name"] = tapName
	}
	tapJSON, _ := json.Marshal(tapCfg)
	tap.StateDoc = map[string][]byte{"probeAudioTapConfig": tapJSON}
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
		TapName     string  `json:"tap_name"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Host) == "" {
		return textResult("host: a daemon host[:port] is required so the brain and tap can dial back", true), nil
	}

	defaults := loadAUv3DefaultStates()
	brain, e := loopNodeSpec("brain_probe", args.BrainProbe, args.BrainFile, defaults)
	if e != "" {
		return textResult(e, true), nil
	}
	synth, e := loopNodeSpec("synth_probe", args.SynthProbe, args.SynthFile, defaults)
	if e != "" {
		return textResult(e, true), nil
	}
	tap, e := loopNodeSpec("tap_probe", args.TapProbe, args.TapFile, defaults)
	if e != "" {
		return textResult(e, true), nil
	}

	// Author the two plugins' AuStateDoc so they auto-connect to the daemon on
	// load: brain control + tap streaming both enabled, pointed at host. The
	// tap is named (default: the session title) so it can coexist with others.
	tapName := device.FirstNonEmpty(args.TapName, args.Title, "Agent Loop")
	configureBrainTap(&brain, &tap, args.Host, args.Decimation, tapName)
	if args.SynthPreset != nil {
		synth.Preset = args.SynthPreset
	}

	title := device.FirstNonEmpty(args.Title, "Agent Loop")
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

	id := device.FirstNonEmpty(sanitize.ID(args.OutID), sanitize.ID(title), "agent-loop")
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

// --- instrument -----------------------------------------------------------

func (s *Server) handleInstrumentAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID        string `json:"session_id"`
		File             string `json:"file"`
		OutID            string `json:"out_id"`
		GlobalChannel    int    `json:"global_channel"`
		StartChannel     int    `json:"start_channel"`
		UseNotes         *bool  `json:"use_notes"`
		PlayChannels     []int  `json:"play_channels"`
		PreserveExisting *bool  `json:"preserve_existing"`
		DryRun           bool   `json:"dry_run"`

		AddProbes  bool   `json:"add_probes"`
		BrainProbe string `json:"brain_probe"`
		BrainFile  string `json:"brain_file"`
		TapProbe   string `json:"tap_probe"`
		TapFile    string `json:"tap_file"`
		Host       string `json:"host"`
		TapChannel *int   `json:"tap_channel"`
		TapName    string `json:"tap_name"`
		Decimation int    `json:"decimation"`
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

	// Embed the probe rig (brain + tap) so the instrumented session is
	// self-contained and agent-controllable: the brain (routed to AUM MIDI
	// Control) emits the banked CCs/Notes, the tap streams audio back. This
	// runs before Instrument so the new strip/slot get banked too.
	var rigRep *aum.ProbeRigReport
	var tapName string
	if args.AddProbes {
		if strings.TrimSpace(args.Host) == "" {
			return textResult("add_probes: host (daemon host[:port]) is required so the brain and tap can dial back", true), nil
		}
		defaults := loadAUv3DefaultStates()
		brain := aum.ProbeBrainNode()
		if args.BrainProbe != "" || args.BrainFile != "" {
			b2, e := loopNodeSpec("brain_probe", args.BrainProbe, args.BrainFile, defaults)
			if e != "" {
				return textResult(e, true), nil
			}
			brain = b2
		}
		tap := aum.ProbeTapNode()
		if args.TapProbe != "" || args.TapFile != "" {
			t2, e := loopNodeSpec("tap_probe", args.TapProbe, args.TapFile, defaults)
			if e != "" {
				return textResult(e, true), nil
			}
			tap = t2
		}
		tapName = device.FirstNonEmpty(args.TapName, sess.Title())
		configureBrainTap(&brain, &tap, args.Host, args.Decimation, tapName)
		tapChan := 0
		if args.TapChannel != nil {
			tapChan = *args.TapChannel
		}
		rr, rerr := sess.AddProbeRig(aum.ProbeRigOptions{Brain: brain, Tap: tap, TapChannel: tapChan})
		if rerr != nil {
			return textResult("add probes: "+rerr.Error(), true), nil
		}
		rigRep = &rr
	}

	opts := aum.InstrumentOptions{
		GlobalChannel:    args.GlobalChannel,
		StartChannel:     args.StartChannel,
		UseNotes:         args.UseNotes == nil || *args.UseNotes,                 // default true
		PreserveExisting: args.PreserveExisting == nil || *args.PreserveExisting, // default true
		PlayChannels:     args.PlayChannels,
	}
	// Seed the global channel from the session's existing convention channel
	// (so re-instrumenting a wired session keeps the same convention channel)
	// unless the caller pinned one explicitly.
	if args.GlobalChannel == 0 {
		if wire, ok := sess.ConventionChannel(); ok {
			opts.GlobalChannel = wire + 1
		}
	}

	report, err := sess.Instrument(opts)
	if err != nil {
		return textResult("instrument session: "+err.Error(), true), nil
	}

	srcID := stagedRelID(path)
	id := sanitize.ID(args.OutID)
	if id == "" {
		id = sanitize.ID(srcID + "_golden")
	}

	var b strings.Builder
	if args.DryRun {
		fmt.Fprintf(&b, "instrument plan for %q (dry run, nothing written)\n", sess.Title())
	} else {
		fmt.Fprintf(&b, "instrumented %q\n", sess.Title())
	}
	if rigRep != nil {
		fmt.Fprintf(&b, "  embedded probe rig: brain at channel index %d (-> AUM MIDI Control), tap %q at channel index %d slot %d; host %q\n",
			rigRep.BrainChannel, tapName, rigRep.TapChannel, rigRep.TapSlot, args.Host)
	}
	writeInstrumentReport(&b, report)

	structured := map[string]any{
		"sessionID": srcID,
		"title":     sess.Title(),
		"dryRun":    args.DryRun,
		"report":    report,
	}
	if rigRep != nil {
		structured["probeRig"] = rigRep
		structured["tapName"] = tapName
	}
	if args.DryRun {
		return structResult(b.String(), structured), nil
	}

	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	if _, err := aum.Open(data); err != nil {
		return textResult("instrumented session failed re-decode: "+err.Error(), true), nil
	}
	outPath, file, err := stageAUMFile(id, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}
	fmt.Fprintf(&b, "staged -> %s\ndownload from the iPad: GET /aum-session/%s\n", outPath, file)
	structured["id"] = id
	structured["file"] = file
	structured["path"] = outPath
	structured["download"] = "/aum-session/" + file
	return structResult(b.String(), structured), nil
}

// writeFullControl renders the "full control" banking summary under an authored
// session (the full_control flag path), shared by the author handlers.
func writeFullControl(b *strings.Builder, r aum.InstrumentReport) {
	b.WriteString("  full control:\n")
	writeInstrumentReport(b, r)
}

// writeInstrumentReport renders an InstrumentReport as human text: the totals,
// the per-class breakdown, and the first overflowed targets.
func writeInstrumentReport(b *strings.Builder, r aum.InstrumentReport) {
	fmt.Fprintf(b, "  assigned %d (CC %d, Note %d, PC %d) across %d channel(s)", r.Assigned, r.CCs, r.Notes, r.PCs, r.ChannelsUsed)
	if r.Preserved > 0 {
		fmt.Fprintf(b, ", %d preserved", r.Preserved)
	}
	b.WriteByte('\n')
	if len(r.ByClass) > 0 {
		classes := make([]string, 0, len(r.ByClass))
		for c := range r.ByClass {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		b.WriteString("  by class:")
		for _, c := range classes {
			fmt.Fprintf(b, " %s=%d", c, r.ByClass[c])
		}
		b.WriteByte('\n')
	}
	if n := len(r.Overflow); n > 0 {
		fmt.Fprintf(b, "  %d target(s) overflowed channel 16 (left unassigned):\n", n)
		const show = 10
		for i, o := range r.Overflow {
			if i >= show {
				fmt.Fprintf(b, "    ... and %d more\n", n-show)
				break
			}
			fmt.Fprintf(b, "    %s\n", o)
		}
	}
}

// --- author_probe_session -------------------------------------------------

func (s *Server) handleAuthorProbeSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		BrainProbe  string  `json:"brain_probe"`
		BrainFile   string  `json:"brain_file"`
		TapProbe    string  `json:"tap_probe"`
		TapFile     string  `json:"tap_file"`
		Host        string  `json:"host"`
		Title       string  `json:"title"`
		OutID       string  `json:"out_id"`
		Tempo       float64 `json:"tempo"`
		Decimation  int     `json:"decimation"`
		TapName     string  `json:"tap_name"`
		FullControl bool    `json:"full_control"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Host) == "" {
		return textResult("host: a daemon host[:port] is required so the brain and tap can dial back", true), nil
	}

	defaults := loadAUv3DefaultStates()
	brain, e := loopNodeSpec("brain_probe", args.BrainProbe, args.BrainFile, defaults)
	if e != "" {
		return textResult(e, true), nil
	}
	tap, e := loopNodeSpec("tap_probe", args.TapProbe, args.TapFile, defaults)
	if e != "" {
		return textResult(e, true), nil
	}

	title := device.FirstNonEmpty(args.Title, "Probe Rig")
	configureBrainTap(&brain, &tap, args.Host, args.Decimation, device.FirstNonEmpty(args.TapName, title))
	tempo := args.Tempo
	if tempo <= 0 {
		tempo = 120
	}

	// Channel layout (0-based): 0 = MIDI brain, 1 = tap audio strip, 2 = master.
	spec := aum.BuildSpec{
		Title: title,
		Tempo: tempo,
		Channels: []aum.ChannelSpec{
			{Kind: aum.KindMIDI, Title: "Brain", Nodes: []aum.NodeSpec{brain}},
			{Kind: aum.KindAudio, Title: "Main", Nodes: []aum.NodeSpec{tap}},
			{Kind: aum.KindAudio, Title: "Master"},
		},
		// Brain MIDI OUT -> AUM MIDI Control (so transport / global MIDI control
		// see the brain's output). There is no synth to feed.
		Routes: []aum.MIDIRoute{{
			From: aum.MIDIEndpoint{Channel: 0, Slot: 0},
			To:   []aum.MIDIEndpoint{{Builtin: "MIDI Control"}},
		}},
	}
	// Default: bake the standard single-channel convention. full_control runs
	// the banking allocator on the placeholder catalogue instead.
	if !args.FullControl {
		spec.Convention = &aum.Convention{Channel: 1}
	}

	sess, report, err := aum.BuildSession(spec)
	if err != nil {
		return textResult("build session: "+err.Error(), true), nil
	}
	var instReport aum.InstrumentReport
	if args.FullControl {
		instReport, err = sess.Instrument(aum.InstrumentOptions{UseNotes: true})
		if err != nil {
			return textResult("instrument session: "+err.Error(), true), nil
		}
	}

	data, err := sess.Archive().Encode()
	if err != nil {
		return textResult("encode session: "+err.Error(), true), nil
	}
	if _, err := aum.Open(data); err != nil {
		return textResult("authored session failed re-decode: "+err.Error(), true), nil
	}

	id := device.FirstNonEmpty(sanitize.ID(args.OutID), sanitize.ID(title), "probe-rig")
	path, file, err := stageAUMFile(id, ".aumproj", data)
	if err != nil {
		return textResult("stage session: "+err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "authored probe session -> %s\n", path)
	fmt.Fprintf(&b, "  brain[ch0/slot0] -> AUM MIDI Control; tap[ch1/slot0]; master[ch2]\n")
	fmt.Fprintf(&b, "  %d channel(s), %d node(s), %d MIDI route(s); brain+tap configured for host %q\n", report.Channels, report.Nodes, report.Routes, args.Host)
	if args.FullControl {
		writeFullControl(&b, instReport)
	} else {
		fmt.Fprintf(&b, "  %d convention CC(s) on ch1\n", report.AssignedCCs)
	}
	fmt.Fprintf(&b, "next: load via the iPad (push & open), then play_notes / get_audio_tap\n")
	fmt.Fprintf(&b, "download from the iPad: GET /aum-session/%s\n", file)

	structured := map[string]any{
		"id":       id,
		"file":     file,
		"path":     path,
		"download": "/aum-session/" + file,
		"report":   report,
	}
	if args.FullControl {
		structured["instrumentReport"] = instReport
	}
	return structResult(b.String(), structured), nil
}

// --- author_graded_session ------------------------------------------------

// gradedRungs maps the tool's rung selector to the GradedSession id GradedSessions
// emits, so a short "s1".."s5" picks the right rung.
var gradedRungs = map[string]string{
	"s1": "graded-s1-one-synth",
	"s2": "graded-s2-trio",
	"s3": "graded-s3-inputs",
	"s4": "graded-s4-sub-mix",
	"s5": "graded-s5-fast-forward",
}

func (s *Server) handleAuthorGradedSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Rung       string  `json:"rung"`
		Host       string  `json:"host"`
		SynthProbe string  `json:"synth_probe"`
		SynthFile  string  `json:"synth_file"`
		Hardware   string  `json:"hardware"`
		Tempo      float64 `json:"tempo"`
		OutID      string  `json:"out_id"`
		Bare       bool    `json:"bare"`
		TapName    string  `json:"tap_name"`
		Decimation int     `json:"decimation"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	rung := strings.ToLower(strings.TrimSpace(args.Rung))
	if rung != "all" {
		if _, ok := gradedRungs[rung]; !ok {
			return textResult(fmt.Sprintf("/rung: %q is not s1|s2|s3|s4|s5|all", args.Rung), true), nil
		}
	}

	opts := aum.GradedOptions{NoConvention: args.Bare}
	if args.Hardware != "" {
		hw, herr := parseHardwareProfile(args.Hardware)
		if herr != nil {
			return textResult(herr.Error(), true), nil
		}
		opts.Hardware = hw
	}
	if args.SynthProbe != "" || args.SynthFile != "" {
		inst, e := loopNodeSpec("synth_probe", args.SynthProbe, args.SynthFile, loadAUv3DefaultStates())
		if e != "" {
			return textResult(e, true), nil
		}
		opts.Instrument = &inst
	}
	// Embed the daemon config so the brain + every tap auto-connect on load. A
	// single shared tap name is fine because the brain toggles one tap at a
	// time over its AutoToggle bypass CC (the documented probing flow).
	if strings.TrimSpace(args.Host) != "" {
		brain := aum.ProbeBrainNode()
		tap := aum.ProbeTapNode()
		configureBrainTap(&brain, &tap, args.Host, args.Decimation, device.FirstNonEmpty(args.TapName, "graded"))
		opts.Brain = &brain
		opts.Tap = &tap
	}

	sessions := aum.GradedSessions(opts)
	byID := make(map[string]aum.GradedSession, len(sessions))
	order := make([]string, 0, len(sessions))
	for _, gs := range sessions {
		byID[gs.ID] = gs
		order = append(order, gs.ID)
	}

	var toStage []aum.GradedSession
	if rung == "all" {
		for _, id := range order {
			toStage = append(toStage, byID[id])
		}
	} else {
		toStage = append(toStage, byID[gradedRungs[rung]])
	}

	type staged struct {
		ID          string `json:"id"`
		File        string `json:"file"`
		Path        string `json:"path"`
		Download    string `json:"download"`
		Channels    int    `json:"channels"`
		Nodes       int    `json:"nodes"`
		AssignedCCs int    `json:"assignedCCs"`
		Routes      int    `json:"routes"`
	}
	var results []staged
	var b strings.Builder
	for _, gs := range toStage {
		spec := gs.Spec
		if args.Tempo > 0 {
			spec.Tempo = args.Tempo
		}
		sess, report, err := aum.BuildSession(spec)
		if err != nil {
			return textResult(fmt.Sprintf("build %s: %v", gs.ID, err), true), nil
		}
		data, err := sess.Archive().Encode()
		if err != nil {
			return textResult(fmt.Sprintf("encode %s: %v", gs.ID, err), true), nil
		}
		if _, err := aum.Open(data); err != nil {
			return textResult(fmt.Sprintf("%s failed re-decode: %v", gs.ID, err), true), nil
		}
		id := gs.ID
		if rung != "all" && args.OutID != "" {
			id = sanitize.ID(args.OutID)
		}
		path, file, err := stageAUMFile(id, ".aumproj", data)
		if err != nil {
			return textResult(fmt.Sprintf("stage %s: %v", gs.ID, err), true), nil
		}
		results = append(results, staged{
			ID: id, File: file, Path: path, Download: "/aum-session/" + file,
			Channels: report.Channels, Nodes: report.Nodes, AssignedCCs: report.AssignedCCs, Routes: report.Routes,
		})
		fmt.Fprintf(&b, "%s (%s): %d channel(s), %d node(s), %d CC(s), %d route(s) -> %s\n",
			gs.Title, id, report.Channels, report.Nodes, report.AssignedCCs, report.Routes, path)
		fmt.Fprintf(&b, "  %s\n", gs.Description)
		fmt.Fprintf(&b, "  download from the iPad: GET /aum-session/%s\n", file)
	}

	structured := map[string]any{"rung": rung, "staged": results}
	return structResult(b.String(), structured), nil
}

// --- edit -----------------------------------------------------------------

func (s *Server) handleEditAUMSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		OutID     string `json:"out_id"`
		Mappings  []struct {
			Collection string   `json:"collection"`
			Target     string   `json:"target"`
			Type       int      `json:"type"`
			Data1      int      `json:"data1"`
			Channel    int      `json:"channel"`
			Min        *float64 `json:"min"`
			Max        *float64 `json:"max"`
			Cycle      *bool    `json:"cycle"`
			Invert     *bool    `json:"invert"`
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
		mp, ok := sess.FindMapping(m.Collection, m.Target)
		if !ok {
			return textResult(fmt.Sprintf("/mappings/%d: no mapping target %q in collection %q", i, m.Target, m.Collection), true), nil
		}
		if err := mp.Assign(m.Type, m.Data1, m.Channel); err != nil {
			return textResult(fmt.Sprintf("/mappings/%d: %v", i, err), true), nil
		}
		note := fmt.Sprintf("map %s/%s -> type=%d data1=%d ch=%d", m.Collection, m.Target, m.Type, m.Data1, m.Channel)
		// Range / invert: apply min/max if any of min, max or invert is given.
		if m.Min != nil || m.Max != nil || m.Invert != nil {
			lo, hi := 0.0, 1.0
			if m.Min != nil {
				lo = *m.Min
			}
			if m.Max != nil {
				hi = *m.Max
			}
			if m.Invert != nil && *m.Invert {
				lo, hi = hi, lo
			}
			if err := mp.SetRange(lo, hi); err != nil {
				return textResult(fmt.Sprintf("/mappings/%d: %v", i, err), true), nil
			}
			note += fmt.Sprintf(" range=%.4g..%.4g", lo, hi)
		}
		if m.Cycle != nil {
			if err := mp.SetAutoToggle(*m.Cycle); err != nil {
				return textResult(fmt.Sprintf("/mappings/%d: %v", i, err), true), nil
			}
			note += fmt.Sprintf(" cycle=%t", *m.Cycle)
		}
		applied = append(applied, note)
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
		// No out_id: edit the staged copy in place (the staging-relative id,
		// subfolders included), so the iPad's write-back lands in the same
		// AUM subfolder the session came from.
		id = stagedRelID(path)
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
		id = sanitize.ID(stagedRelID(path) + "_" + args.Collection)
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
// dir. Staging mirrors the iPad's AUM folder tree, so a session_id may carry
// subfolder segments (e.g. "Live sets/Set"); it is agent-supplied, so
// aum.SafeRelPath traversal-guards it (no "..", no absolute paths, no hidden
// segments) before it is joined under the staging dir.
func resolveAUMSessionPath(file, sessionID string) (string, error) {
	if file != "" {
		return file, nil
	}
	if sessionID == "" {
		return "", fmt.Errorf("provide /file or /session_id")
	}
	name := sessionID
	if !strings.HasSuffix(name, ".aumproj") {
		name += ".aumproj"
	}
	rel, ok := aum.SafeRelPath(name)
	if !ok {
		return "", fmt.Errorf("invalid session_id %q (must be a staging-relative session id, no traversal)", sessionID)
	}
	return filepath.Join(config.AUMSessionsDir(), filepath.FromSlash(rel)), nil
}

// stagedRelID recovers the staging-relative session id for a staged file path
// ("Live sets/Set" for <staging>/Live sets/Set.aumproj). Staging mirrors the
// iPad's AUM folder tree, so this id round-trips through resolveAUMSessionPath
// and matches what OnAUMSessionDownloaded records as the current session. A
// path outside the staging dir (an explicit /file arg) falls back to its
// basename.
func stagedRelID(p string) string {
	rel, err := filepath.Rel(config.AUMSessionsDir(), p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return aum.StripExt(filepath.Base(p))
	}
	return aum.StripExt(filepath.ToSlash(rel))
}

// stageAUMFile writes data as <id><ext> under the AUM staging dir, returning
// the full path and the staging-relative file path (for the download URL). id
// may carry subfolder segments ("Live sets/Set") because staging mirrors the
// iPad's AUM folder tree; callers either sanitize it or derive it from an
// already-staged path (stagedRelID), so it never escapes the staging dir.
func stageAUMFile(id, ext string, data []byte) (fullPath, file string, err error) {
	if id == "" {
		id = "session"
	}
	file = id + ext
	fullPath = filepath.Join(config.AUMSessionsDir(), filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", "", fmt.Errorf("create sessions dir: %w", err)
	}
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return "", "", err
	}
	// Every MCP-tool write counts as a staging change: bump the shared rev so
	// the iPad's manifest poll (GET /aum-session?rev=) sees authored/edited
	// files, not just receiver uploads.
	aum.BumpStagingRev(config.AUMSessionsDir())
	return fullPath, file, nil
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
