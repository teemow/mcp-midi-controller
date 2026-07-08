package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/midi-transport"
	"github.com/teemow/midi-transport/midicontrol"
)

// maxNoteDurationMS caps how long play_notes will hold a note (and block the
// tool call) before sending note-off, so an agent loop cannot hang the daemon.
const maxNoteDurationMS = 10_000

// registerMidiTools wires the agent's "hands": play_notes / send_midi /
// set_transport. Their PRIMARY target is the ProbeMidiBrain LAN control channel
// (internal/midicontrol), which makes the brain AUv3 emit the MIDI on its host
// MIDI-out — so notes/CC/transport flow through AUM's routing exactly like a
// real controller. When an explicit endpoint is supplied the call instead falls
// back to a hardware transport (BLE) via the engine's send_raw path. These
// tools are always registered; without a brain hub the LAN path simply reports
// "no brain connected" and the caller must pass an endpoint.
func (s *Server) registerMidiTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "play_notes",
		Description: "Play one or more MIDI notes through the ProbeMidiBrain LAN channel (the agent's \"hands\"): the brain AUv3 emits note-on, holds for duration_ms, then note-off, so the notes flow through AUM's MIDI routing into the synth. " +
			"Use this to excite a synth and then read the result with get_audio_tap / get_audio_clip. " +
			"Alternatively pass sequence[] to play a timed phrase of note and CC steps in ONE call: steps run back-to-back by default, an explicit at_ms overlaps notes (legato/glide on a mono synth) or fires a CC mid-phrase. " +
			"Pass an endpoint (and optional transport) to send over a hardware MIDI transport (BLE) instead of the brain channel. The call blocks for the held/sequence duration (capped at 10s) so the audio tap can be read immediately after.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"notes": {"type": "array", "items": {"type": "integer", "minimum": 0, "maximum": 127}, "description": "MIDI note numbers 0-127."},
				"note": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Single note shortcut (alternative to notes)."},
				"velocity": {"type": "integer", "minimum": 1, "maximum": 127, "description": "Note-on velocity (default 100; also the default for sequence note steps)."},
				"duration_ms": {"type": "integer", "minimum": 0, "description": "How long to hold the notes before note-off (default 500, max 10000)."},
				"channel": {"type": "integer", "minimum": 1, "maximum": 16, "description": "MIDI channel 1-16 (default 1)."},
				"sequence": ` + sequenceSchema + `,
				"transport": {"type": "string", "description": "Hardware transport id (e.g. blemidi) when endpoint is set."},
				"endpoint": {"type": "string", "description": "Hardware endpoint id; when set, bypasses the brain channel and sends over the transport."}
			}
		}`),
	}, s.handlePlayNotes)

	s.mcp.AddTool(&mcp.Tool{
		Name: "send_midi",
		Description: "Send a single MIDI channel message through the ProbeMidiBrain LAN channel: control change (cc), program change (pc), note-on or note-off. " +
			"Use cc to tweak a synth parameter mapped to a CC and then re-read get_audio_tap. Pass an endpoint (and optional transport) to send over a hardware MIDI transport (BLE) instead.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"kind": {"type": "string", "enum": ["cc", "pc", "noteOn", "noteOff"], "description": "Message kind."},
				"channel": {"type": "integer", "minimum": 1, "maximum": 16, "description": "MIDI channel 1-16 (default 1)."},
				"controller": {"type": "integer", "minimum": 0, "maximum": 127, "description": "CC number (kind=cc)."},
				"value": {"type": "integer", "minimum": 0, "maximum": 127, "description": "CC value (kind=cc)."},
				"program": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Program number (kind=pc)."},
				"note": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Note number (kind=noteOn/noteOff)."},
				"velocity": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Velocity (kind=noteOn/noteOff)."},
				"transport": {"type": "string", "description": "Hardware transport id when endpoint is set."},
				"endpoint": {"type": "string", "description": "Hardware endpoint id; when set, bypasses the brain channel."}
			},
			"required": ["kind"]
		}`),
	}, s.handleSendMidi)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "set_transport",
		Description: "Send a MIDI transport message (start / stop / continue) through the ProbeMidiBrain LAN channel so AUM's transport (and anything following MIDI clock/transport) reacts. Pass an endpoint (and optional transport) to send over a hardware MIDI transport (BLE) instead.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["start", "stop", "continue"], "description": "Transport action."},
				"transport": {"type": "string", "description": "Hardware transport id when endpoint is set."},
				"endpoint": {"type": "string", "description": "Hardware endpoint id; when set, bypasses the brain channel."}
			},
			"required": ["action"]
		}`),
	}, s.handleSetTransport)
}

// midiTarget captures the optional hardware fallback fields shared by the tools.
type midiTarget struct {
	Transport string `json:"transport"`
	Endpoint  string `json:"endpoint"`
}

// usesHardware reports whether the caller asked to bypass the brain channel.
func (t midiTarget) usesHardware() bool { return t.Endpoint != "" }

// sendRawBytes routes raw MIDI bytes to a hardware transport via the engine.
func (s *Server) sendRawBytes(ctx context.Context, t midiTarget, data []byte) error {
	return s.eng.SendRaw(ctx, t.Transport, t.Endpoint, transport.Event{
		Kind: transport.MIDIEvent,
		Data: data,
	})
}

// sendBrain pushes a command to the LAN brain channel, returning a friendly
// error when no brain is connected.
func (s *Server) sendBrain(ctx context.Context, cmd midicontrol.Command) error {
	if s.midi == nil {
		return midicontrol.ErrNoBrain
	}
	return s.midi.Send(ctx, cmd)
}

func clampChannel(ch int) int {
	if ch < 1 {
		return 1
	}
	if ch > 16 {
		return 16
	}
	return ch
}

func (s *Server) handlePlayNotes(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Notes      []int     `json:"notes"`
		Note       *int      `json:"note"`
		Velocity   *int      `json:"velocity"`
		DurationMS *int      `json:"duration_ms"`
		Channel    int       `json:"channel"`
		Sequence   []seqStep `json:"sequence"`
		midiTarget
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}

	notes := args.Notes
	if args.Note != nil {
		notes = append(notes, *args.Note)
	}
	if len(args.Sequence) > 0 {
		if len(notes) > 0 {
			return textResult("provide either sequence or notes/note, not both", true), nil
		}
		return s.playSequence(ctx, args.Sequence, args.Velocity, args.Channel, args.midiTarget)
	}
	if len(notes) == 0 {
		return textResult("provide notes (array), note (single) or sequence (timed phrase)", true), nil
	}
	for i, n := range notes {
		if n < 0 || n > 127 {
			return textResult(fmt.Sprintf("/notes/%d: note must be in [0, 127]", i), true), nil
		}
	}
	velocity := 100
	if args.Velocity != nil {
		velocity = clamp7(*args.Velocity)
	}
	duration := 500
	if args.DurationMS != nil {
		duration = *args.DurationMS
	}
	if duration < 0 {
		duration = 0
	}
	if duration > maxNoteDurationMS {
		duration = maxNoteDurationMS
	}
	ch := clampChannel(args.Channel)

	hardware := args.usesHardware()
	noteOn := func(n int) error {
		if hardware {
			return s.sendRawBytes(ctx, args.midiTarget, []byte{0x90 | byte(ch-1), byte(n), byte(velocity)})
		}
		return s.sendBrain(ctx, midicontrol.Command{Type: "noteOn", Channel: ch, Note: n, Velocity: velocity})
	}
	noteOff := func(n int) error {
		if hardware {
			return s.sendRawBytes(ctx, args.midiTarget, []byte{0x80 | byte(ch-1), byte(n), 0})
		}
		return s.sendBrain(ctx, midicontrol.Command{Type: "noteOff", Channel: ch, Note: n})
	}

	// Note-on for every note, tracking what is sounding so a mid-sequence
	// failure does not leave a hung chord: we send note-off for the started
	// notes (best effort) before returning the error.
	started := make([]int, 0, len(notes))
	for _, n := range notes {
		if err := noteOn(n); err != nil {
			for _, on := range started {
				_ = noteOff(on)
			}
			return textResult("play_notes (note-on) failed: "+err.Error(), true), nil
		}
		started = append(started, n)
	}

	// Hold, then note-off. Respect context cancellation (client gone / shutdown).
	if duration > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(duration) * time.Millisecond):
		}
	}

	for _, n := range notes {
		if err := noteOff(n); err != nil {
			return textResult("play_notes (note-off) failed: "+err.Error(), true), nil
		}
	}

	path := "brain channel"
	if hardware {
		path = fmt.Sprintf("%s/%s", orDefault(args.Transport, "blemidi"), args.Endpoint)
	}
	return structResult(
		fmt.Sprintf("played %d note(s) [%s] vel=%d for %dms on ch%d via %s",
			len(notes), joinInts(notes, ","), velocity, duration, ch, path),
		map[string]any{
			"notes":       notes,
			"velocity":    velocity,
			"duration_ms": duration,
			"channel":     ch,
			"via":         path,
		}), nil
}

// playSequence is play_notes' sequence branch: compile the steps, run them
// against the wall clock (blocking through the span like a held note does), and
// summarize what was played.
func (s *Server) playSequence(ctx context.Context, steps []seqStep, velocity *int, channel int, target midiTarget) (*mcp.CallToolResult, error) {
	defVel, ch := resolveSeqDefaults(velocity, channel)
	events, span, err := compileSequence(steps, defVel, ch)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if err := runSequence(ctx, s.seqSender(target), events); err != nil {
		return textResult("play_notes (sequence) failed: "+err.Error(), true), nil
	}

	path := "brain channel"
	if target.usesHardware() {
		path = fmt.Sprintf("%s/%s", orDefault(target.Transport, "blemidi"), target.Endpoint)
	}
	notes := seqNotes(events)
	noteOns, ccs := seqCounts(events)
	return structResult(
		fmt.Sprintf("played sequence: %d step(s) (%d note-on(s), %d cc(s)) over %dms, notes [%s], via %s",
			len(steps), noteOns, ccs, span, joinInts(notes, ","), path),
		map[string]any{
			"sequence_steps": len(steps),
			"note_ons":       noteOns,
			"ccs":            ccs,
			"span_ms":        span,
			"notes":          notes,
			"channel":        ch,
			"via":            path,
		}), nil
}

func (s *Server) handleSendMidi(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Kind       string `json:"kind"`
		Channel    int    `json:"channel"`
		Controller int    `json:"controller"`
		Value      int    `json:"value"`
		Program    int    `json:"program"`
		Note       int    `json:"note"`
		Velocity   int    `json:"velocity"`
		midiTarget
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	ch := clampChannel(args.Channel)
	hardware := args.usesHardware()

	var (
		raw []byte
		cmd midicontrol.Command
	)
	switch args.Kind {
	case "cc":
		raw = []byte{0xB0 | byte(ch-1), byte(clamp7(args.Controller)), byte(clamp7(args.Value))}
		cmd = midicontrol.Command{Type: "cc", Channel: ch, Controller: clamp7(args.Controller), Value: clamp7(args.Value)}
	case "pc":
		raw = []byte{0xC0 | byte(ch-1), byte(clamp7(args.Program))}
		cmd = midicontrol.Command{Type: "pc", Channel: ch, Program: clamp7(args.Program)}
	case "noteOn":
		raw = []byte{0x90 | byte(ch-1), byte(clamp7(args.Note)), byte(clamp7(args.Velocity))}
		cmd = midicontrol.Command{Type: "noteOn", Channel: ch, Note: clamp7(args.Note), Velocity: clamp7(args.Velocity)}
	case "noteOff":
		raw = []byte{0x80 | byte(ch-1), byte(clamp7(args.Note)), byte(clamp7(args.Velocity))}
		cmd = midicontrol.Command{Type: "noteOff", Channel: ch, Note: clamp7(args.Note), Velocity: clamp7(args.Velocity)}
	default:
		return textResult("/kind: must be one of cc, pc, noteOn, noteOff", true), nil
	}

	var err error
	if hardware {
		err = s.sendRawBytes(ctx, args.midiTarget, raw)
	} else {
		err = s.sendBrain(ctx, cmd)
	}
	if err != nil {
		return textResult("send_midi failed: "+err.Error(), true), nil
	}

	path := "brain channel"
	if hardware {
		path = fmt.Sprintf("%s/%s", orDefault(args.Transport, "blemidi"), args.Endpoint)
	}
	return structResult(fmt.Sprintf("sent %s on ch%d via %s", args.Kind, ch, path), map[string]any{
		"kind":    args.Kind,
		"channel": ch,
		"via":     path,
	}), nil
}

func (s *Server) handleSetTransport(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Action string `json:"action"`
		midiTarget
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	var status byte
	switch args.Action {
	case "start":
		status = 0xFA
	case "continue":
		status = 0xFB
	case "stop":
		status = 0xFC
	default:
		return textResult("/action: must be one of start, stop, continue", true), nil
	}

	hardware := args.usesHardware()
	var err error
	if hardware {
		err = s.sendRawBytes(ctx, args.midiTarget, []byte{status})
	} else {
		err = s.sendBrain(ctx, midicontrol.Command{Type: "transport", Action: args.Action})
	}
	if err != nil {
		return textResult("set_transport failed: "+err.Error(), true), nil
	}

	path := "brain channel"
	if hardware {
		path = fmt.Sprintf("%s/%s", orDefault(args.Transport, "blemidi"), args.Endpoint)
	}
	return structResult(fmt.Sprintf("transport %s via %s", args.Action, path), map[string]any{
		"action": args.Action,
		"via":    path,
	}), nil
}

func clamp7(v int) int {
	if v < 0 {
		return 0
	}
	if v > 127 {
		return 127
	}
	return v
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// joinInts renders a slice of ints to a sep-joined string, e.g. "60,64,67".
// Shared by the note-list summaries (play_notes / probe_sound) and the WAV
// filename tag so the rendering lives in one place.
func joinInts(nums []int, sep string) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, sep)
}
