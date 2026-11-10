package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/ipad"
)

// The aum_* tools are named AUM operations over USB: each finds its target
// in AUM's accessibility tree (or at the position auv3-probe's AUM map
// measured), sends the gestures in one WebDriverAgent session, and re-reads
// AUM to verify the result. They complement the raw ipad_* gestures.

func enumJSON(values []string) string {
	b, _ := json.Marshal(values)
	return string(b)
}

const aumVerifyNote = " Every call re-reads AUM after acting and reports verified true/false with what it saw (verified false is a normal result, not a tool error), the gestures sent, and AUM's state as structuredContent."

const aumCareNote = " Operations that change or lose state need confirm: true; take an ipad_screenshot first and, per AGENTS.md, act only on a scratch session or a ' (mcp test)' copy, never an original session."

const channelProp = `"channel": {"type": "string", "description": "Strip selector: a number (\"2\" = the strip titled \"2: …\" or \"CHAN 2\"), \"MIDI 1\", \"#3\" (the third strip from the left, also for custom titles), or part of the title."}`

const confirmProp = `"confirm": {"type": "boolean", "description": "Required (true) for actions that change or lose state."}`

const pathProp = `"path": {"type": "array", "items": {"type": "string"}, "description": "Row labels to tap in turn first (folders, collections, filter sections)."}`

// registerAUMUITools wires the gesture primitives and the aum_* operations.
func (s *Server) registerAUMUITools() {
	if s.ipad == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_drag",
		Description: "Press at the first point, move through the others (resting at each), hold, then release — one touch, like scripts/ipad drag. Use it where AUM needs the finger down: reorder nodes (press a node, move 90 points right, hold, then up/down) and channels (drag a title up 100 points, then sideways), reveal a node's DSP % and latency, move or resize plugin windows. " +
			"ipad_swipe is enough for faders, knobs and scrolling. Coordinates are points.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"points": {"type": "array", "minItems": 2, "maxItems": 10, "items": {"type": "object", "properties": {"x": {"type": "number"}, "y": {"type": "number"}}, "required": ["x", "y"]}},
				"move_ms": {"type": "integer", "minimum": 50, "maximum": 5000, "description": "Duration of each leg (default 400)."},
				"rest_ms": {"type": "integer", "minimum": 0, "maximum": 5000, "description": "Pause at each point (default 150)."},
				"hold_ms": {"type": "integer", "minimum": 0, "maximum": 10000, "description": "Hold before release (default 1000)."},
				"press_ms": {"type": "integer", "minimum": 0, "maximum": 5000, "description": "Pause after pressing, before the first move (default 0)."}
			},
			"required": ["points"]
		}`),
	}, s.handleIPadDrag)

	s.mcp.AddTool(&mcp.Tool{
		Name: "ipad_text",
		Description: "Text entry in an app: fields lists the text fields, search fields and text views on screen with their values and frames; type clears field `field` (default 0, the first) and types text into it, reading the value back (enter true then presses Return); enter presses Return on the on-screen keyboard. " +
			"AUM's name fields (SAVE, Edit Channel, bus names) and number dialogs (Set level, Edit Value) are text fields; Return applies the SAVE panel and the channel name, dialogs need their Set/OK button. Check with fields before saving under a typed name.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + ipadAppProp + `,
				"action": {"type": "string", "enum": ["fields", "type", "enter"]},
				"text": {"type": "string", "description": "Text to type (action type)."},
				"field": {"type": "integer", "minimum": 0, "description": "Index into the fields list (default 0)."},
				"enter": {"type": "boolean", "description": "Press Return after typing."}
			},
			"required": ["action"]
		}`),
	}, s.handleIPadText)

	panels := make([]string, len(ipad.AUMPanels))
	var panelDocs []string
	for i, p := range ipad.AUMPanels {
		panels[i] = p.Name
		panelDocs = append(panelDocs, p.Name+" = "+p.Description)
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_panel",
		Description: "Open, close or read AUM's screens. open panel: " + strings.Join(panelDocs, "; ") + ". " +
			"close closes the front-most thing (an alert via Cancel, a popover by tapping outside it, the routing matrix, a side panel, the tempo bar, the keyboard). read lists every open popover, side panel and alert with its title, rows (text → value) and buttons." + aumVerifyNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["open", "close", "read"]},
				"panel": {"type": "string", "enum": ` + enumJSON(panels) + `, "description": "The screen to open."}
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_panel", s.ipad.AUMPanel))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_channel",
		Description: "AUM mixer channels: add_audio / add_midi (the add tile right of the last strip), restore (IMPORT → Restore Removed Channel), import (name: a session; appends all its channels), edit (opens EDIT CHANNEL), rename (name; a custom name replaces the whole title, \"\" restores AUM's), bookmark, duplicate, remove (confirm; REMOVE CHANNEL → TAP TO CONFIRM), " +
			"move (drag the title onto target strip), set_level (level_db via the Set level dialog), reset_level (double-tap the fader: back to Settings → Fader Level), mute / solo / arm (on true/false, or flip; read from the button's Selected state), scroll_to (bring an off-screen strip into view)." + aumVerifyNote + aumCareNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMChannelActions) + `},
				` + channelProp + `,
				"name": {"type": "string", "description": "New title (rename) or session (import)."},
				"target": {"type": "string", "description": "Strip to move onto (move), e.g. \"#1\"."},
				"level_db": {"type": "number", "description": "Fader level in dB (set_level)."},
				"on": {"type": "boolean", "description": "Wanted state for mute/solo/arm (omit to flip)."},
				` + confirmProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_channel", s.ipad.AUMChannel))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_node",
		Description: "AUM nodes and slots of a strip: open (tap: plugin window, built-in popover, Destinations, or the node picker for an empty slot), menu (long-press: lists POST FADER, PRE FADER, SELECT, REPLACE, RELOAD, SWAP, REMOVE SLOT, COPY HERE, MOVE HERE), select, replace (opens the picker), reload, pre_fader, post_fader, swap (onto the selected node), " +
			"copy_here / move_here (an empty slot, after select), add_slot (+1 or double-tap below the last insert), remove_slot (an empty slot), bypass / unbypass (swipe left/right; read back from the node's place off the wire), eject (confirm; bypass then ⏏, answering Confirm Eject's OK), move (steps: slots, negative = up), meter (main meter shows this point), " +
			"window_close / window_mode (value DEFAULT, CUSTOM, MAXIMIZED, FULLSCREEN) on the front plugin window. node: \"source\", \"output\", an insert slot number, or a node label (\"LP\"); get_aum_ui lists each strip's nodes." + aumVerifyNote + aumCareNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMNodeActions) + `},
				` + channelProp + `,
				"node": {"type": "string", "description": "Node selector: source, output, an insert slot number, or a node label."},
				"steps": {"type": "integer", "description": "Slots to move (move); negative = up."},
				"value": {"type": "string", "description": "Window mode (window_mode)."},
				` + confirmProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_node", s.ipad.AUMNode))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_transport",
		Description: "AUM transport, tempo and recording: state (playing or stopped, read from the position moving), play, stop, toggle, rewind, locate (bars: drag the transport by whole bars), set_tempo (bpm: tempo bar → double-tap → Edit Value), nudge_tempo (steps of 0.1 BPM via ‹ ›), metronome (on, or flip), " +
			"record (confirm: starts recording every armed channel — after the pre-roll when stopped, at the next sync quantum when playing — and a second call stops the take; takes land in FILES → Recordings), unarm_all (long-press record → Un-arm recording → Yes). Play starts every synced plugin." + aumVerifyNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMTransportActions) + `},
				"bpm": {"type": "number", "exclusiveMinimum": 0, "description": "Tempo (set_tempo)."},
				"steps": {"type": "integer", "description": "0.1-BPM steps (nudge_tempo), negative = slower."},
				"bars": {"type": "integer", "description": "Bars to move (locate), negative = back."},
				"on": {"type": "boolean", "description": "Metronome state (omit to flip)."},
				` + confirmProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_transport", s.ipad.AUMTransport))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_files",
		Description: "AUM's FILES, SAVE, CLEAR and NOTES: list (FILES → Sessions, with path for folders: each row and its saved time), recordings (FILES → Recordings; path into a session's folder lists its takes), load (confirm; taps the session: AUM loads it at once, then the menu's session name is checked), " +
			"save (confirm; types name into SAVE and presses Return, which saves at once and overwrites a session of that name), rename (confirm; long-press → share sheet → Rename), clear (confirm; CLEAR → Sure?), notes (open the session notes)." + aumVerifyNote + aumCareNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMFilesActions) + `},
				"name": {"type": "string", "description": "Session name (load, save, rename)."},
				"new_name": {"type": "string", "description": "New session name (rename)."},
				` + pathProp + `,
				` + confirmProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_files", s.ipad.AUMFiles))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_midi_routing",
		Description: "AUM's MIDI routing matrix: read (sources along the top, destinations on the right, and the connected cells — read from a screenshot, since AUM's tree lacks them), connect / disconnect (source, destination: names or parts of them; checks the cell before and after), destination (opens a destination's panel: Connect MIDI Sources, All Notes Off, the MIDI filter; path taps further rows such as a filter section)." + aumVerifyNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMRoutingActions) + `},
				"source": {"type": "string"},
				"destination": {"type": "string"},
				` + pathProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_midi_routing", s.ipad.AUMMIDIRouting))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_midi_control",
		Description: "AUM's MIDI CONTROL (mappings of transport, system actions and every channel's controls and nodes): read/open (path walks into collections and controls, e.g. [\"Channels\", \"CHAN 2: …\", \"Channel controls\", \"Volume\"]; a control opens its mapping editor: channel, type, number, LEARN, range), filter (value ALL, ASSIGNED, UNUSED), learn (LEARN in the open mapping editor; then send the MIDI), " +
			"menu (the ⋮ of section setting, item value: Set MIDI Channels, Save, Load, Copy, Paste, Clear All, Delete All; name supplies a dialog's value; Clear All, Delete All, Set MIDI Channels, Paste and Load need confirm: AUM applies them without asking), add (value ADD TEMPO in Tempo Presets, ADD PRESET in a plugin's Preset Load with name), main_parameter (in a plugin parameter's editor: make it the node's knob)." + aumVerifyNote + aumCareNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMMIDIControlActions) + `},
				` + pathProp + `,
				"setting": {"type": "string", "description": "Section whose ⋮ menu to open (menu)."},
				"value": {"type": "string", "description": "Filter, menu item or ADD TEMPO / ADD PRESET."},
				"name": {"type": "string", "description": "Value for a dialog (Set MIDI Channels' channel, Save's file name) or the preset to add."},
				` + confirmProp + `
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_midi_control", s.ipad.AUMMIDIControl))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_settings",
		Description: "AUM's Settings (global, not part of a session): read (every row and its value, scrolling the panel), set (setting: a row label such as \"Confirm Eject\", \"Bit Depth\", \"Sample Rate\"; value as AUM prints it: ON, OFF, INT 24, 48 kHz, …; steps the value button until it reads value, or picks it from the list it opens). Change settings only when asked, and set them back afterwards." + aumVerifyNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMSettingsActions) + `},
				"setting": {"type": "string"},
				"value": {"type": "string"}
			},
			"required": ["action"]
		}`),
	}, s.aumHandler("aum_settings", s.ipad.AUMSettings))

	s.mcp.AddTool(&mcp.Tool{
		Name: "aum_file_player",
		Description: "A File Player channel: open/read (FILE PLAYER: selected file, duration, format, Loop, Sync, Original BPM, Number of Beats, Rate, Beat Offset, Normalize), set (setting Loop, Sync or Normalize; value ON/OFF), load (name: the sound file; path: SOUNDFILES folders, e.g. [\"AUM Recordings\", \"<session>\"]; loads at once), play_enable (the PLAY ENABLE toggle; on, or flip). The player runs with the transport." + aumVerifyNote,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ` + enumJSON(ipad.AUMFilePlayerActions) + `},
				` + channelProp + `,
				"setting": {"type": "string"},
				"value": {"type": "string"},
				"name": {"type": "string", "description": "Sound file to load."},
				` + pathProp + `,
				"on": {"type": "boolean"}
			},
			"required": ["action", "channel"]
		}`),
	}, s.aumHandler("aum_file_player", s.ipad.AUMFilePlayer))
}

// aumHandler decodes a tool call into an AUMRequest, runs op and renders the
// result.
func (s *Server) aumHandler(tool string, op func(context.Context, ipad.AUMRequest) (ipad.AUMResult, error)) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var r ipad.AUMRequest
		if res := decodeArgs(req, &r); res != nil {
			return res, nil
		}
		res, err := op(ctx, r)
		if err != nil {
			var we *ipad.Error
			if errors.As(err, &we) {
				return s.ipadError(ctx, err), nil
			}
			return textResult(tool+" "+r.Action+": "+err.Error(), true), nil
		}
		return structResultNoEmbed(describeAUMResult(res), res), nil
	}
}

// describeAUMResult renders an operation's outcome for text-only clients:
// the verdict, the gestures, the readout and a short state summary.
func describeAUMResult(res ipad.AUMResult) string {
	var b strings.Builder
	mark := "verified"
	if !res.Verified {
		mark = "NOT verified"
	}
	fmt.Fprintf(&b, "%s: %s — %s\n", res.Action, mark, res.Message)
	if len(res.Steps) > 0 {
		b.WriteString("gestures: " + strings.Join(res.Steps, "; ") + "\n")
	}
	if res.Data != nil {
		if raw, err := json.Marshal(res.Data); err == nil && string(raw) != "null" {
			if len(raw) > 6000 {
				raw = append(raw[:6000], []byte("…")...)
			}
			b.WriteString("data: " + string(raw) + "\n")
		}
	}
	b.WriteString("\n" + describeAUMState(res.State))
	return b.String()
}

func (s *Server) handleIPadDrag(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App     string       `json:"app"`
		Points  []ipad.Point `json:"points"`
		MoveMS  int          `json:"move_ms"`
		RestMS  int          `json:"rest_ms"`
		HoldMS  *int         `json:"hold_ms"`
		PressMS int          `json:"press_ms"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	if len(args.Points) < 2 {
		return textResult("points needs at least two {x, y}", true), nil
	}
	t := ipad.DragTiming{
		Move:  time.Duration(args.MoveMS) * time.Millisecond,
		Rest:  time.Duration(args.RestMS) * time.Millisecond,
		Press: time.Duration(args.PressMS) * time.Millisecond,
	}
	if args.HoldMS != nil {
		t.Hold = time.Duration(max(*args.HoldMS, 1)) * time.Millisecond
	}
	app, err := s.ipad.ResolveApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error { return sess.Drag(ctx, args.Points, t) })
	if err != nil {
		return s.ipadError(ctx, err), nil
	}
	var parts []string
	for _, p := range args.Points {
		parts = append(parts, fmt.Sprintf("(%g,%g)", p.X, p.Y))
	}
	return textResult(fmt.Sprintf("dragged %s in %s", strings.Join(parts, " -> "), app), false), nil
}

func (s *Server) handleIPadText(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		App    string `json:"app"`
		Action string `json:"action"`
		Text   string `json:"text"`
		Field  int    `json:"field"`
		Enter  bool   `json:"enter"`
	}
	if res := decodeArgs(req, &args); res != nil {
		return res, nil
	}
	app, err := s.ipad.ResolveApp(ctx, args.App)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	var msg string
	var fields []ipad.TextField
	err = s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		switch args.Action {
		case "fields":
			var err error
			if fields, err = sess.TextFields(ctx); err != nil {
				return err
			}
			if len(fields) == 0 {
				msg = "no text fields on screen"
				return nil
			}
			var b strings.Builder
			for _, f := range fields {
				fmt.Fprintf(&b, "%d %s at (%g,%g %gx%g): %q\n", f.Index, f.Kind, f.Rect.X, f.Rect.Y, f.Rect.Width, f.Rect.Height, f.Value)
			}
			msg = strings.TrimSpace(b.String())
		case "type":
			v, err := sess.TypeText(ctx, args.Field, args.Text)
			if err != nil {
				return err
			}
			msg = fmt.Sprintf("field %d now reads %q", args.Field, v)
			if args.Enter {
				if err := sess.PressReturn(ctx); err != nil {
					return err
				}
				msg += "; pressed Return"
			}
		case "enter":
			if err := sess.PressReturn(ctx); err != nil {
				return err
			}
			msg = "pressed Return"
		default:
			return fmt.Errorf("action must be fields, type or enter")
		}
		return nil
	})
	if err != nil {
		var we *ipad.Error
		if errors.As(err, &we) {
			return s.ipadError(ctx, err), nil
		}
		return textResult(err.Error(), true), nil
	}
	if fields != nil {
		return structResultNoEmbed(msg, map[string]any{"fields": fields}), nil
	}
	return textResult(msg, false), nil
}
