package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/midicontrol"
)

// probeDefaultDurationMS is how long probe_sound holds the notes (and thus how
// much sustain it captures) when the caller does not specify duration_ms. It is
// longer than play_notes' 500ms default so the analysis window is comfortably
// full of sustain when the snapshot is taken (the harness uses 800ms too).
const probeDefaultDurationMS = 800

// Probe-capture tuning. settleFloorRMS is the reported level below which the tap
// is considered quiet enough to start a clean capture; settleTimeout bounds the
// wait so a noisy rig never hangs the probe. The retention caps bound the WAVs
// written under config.AudioClipsDir().
const (
	settleFloorRMS    = 0.01
	settleTimeout     = 2 * time.Second
	settlePollEvery   = 40 * time.Millisecond
	maxProbeClips     = 64
	maxProbeClipBytes = 256 << 20 // 256 MiB
)

// registerProbeSound wires the compound probe_sound tool, the sound-engineer
// iteration loop collapsed into one round trip: optionally set/verify controls,
// play notes (blocking through the sustain), snapshot the audio analysis DURING
// the sustain (before note-off), then return that analysis. It requires the
// audio store (the "ears"), so it is only registered alongside get_audio_tap.
func (s *Server) registerProbeSound() {
	if s.audio == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "probe_sound",
		Description: "Sound-engineer iteration loop in ONE call: optionally apply settings[] (a bound-device control via {device,control,value} — with verify:true to confirm the inbound echo — or a raw CC via {cc,value,channel} over the ProbeMidiBrain channel), then play notes/note for duration_ms through the brain, then return the trusted audio analysis captured DURING the sustain (just before note-off, so harmonics/loudness reflect the held tone, not the release tail). " +
			"This replaces the old three-call loop (control_* -> play_notes -> get_audio_tap) so an agent can tweak a parameter and immediately read how the sound changed. " +
			"The analysis block matches get_audio_tap: detected pitch (f0/note/cents/confidence), harmonic partials + HNR, loudness/crest (dBFS), and onset activity. Emits structuredContent {settings_applied, notes, velocity, duration_ms, channel, snapshot}.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"settings": {
					"type": "array",
					"description": "Parameter changes to apply before playing. Each item is either a bound-device control {device, control, value, verify?, verify_timeout_ms?} (validated/verified via the engine) or a raw control change {cc, value, channel?} sent over the brain channel.",
					"items": {
						"type": "object",
						"properties": {
							"device": {"type": "string", "description": "The device's name in your rig (use with control+value)."},
							"control": {"type": "string", "description": "Control name on the device."},
							"cc": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Raw CC controller number (alternative to device/control); sent over the brain channel."},
							"value": {"description": "Control value (device path) or 0-127 CC value (cc path)."},
							"channel": {"type": "integer", "minimum": 1, "maximum": 16, "description": "MIDI channel 1-16 for a raw cc (default 1)."},
							"verify": {"type": "boolean", "description": "For a device control, set then wait for an inbound echo and classify confirmed|no_feedback|mismatch."},
							"verify_timeout_ms": {"type": "integer", "minimum": 0, "description": "Verify echo timeout in ms (default engine timeout)."}
						}
					}
				},
				"notes": {"type": "array", "items": {"type": "integer", "minimum": 0, "maximum": 127}, "description": "MIDI note numbers 0-127 to play (e.g. a chord)."},
				"note": {"type": "integer", "minimum": 0, "maximum": 127, "description": "Single note shortcut (alternative to notes)."},
				"velocity": {"type": "integer", "minimum": 1, "maximum": 127, "description": "Note-on velocity (default 100)."},
				"duration_ms": {"type": "integer", "minimum": 0, "description": "How long to hold the notes before snapshotting + note-off (default 800, max 10000)."},
				"channel": {"type": "integer", "minimum": 1, "maximum": 16, "description": "MIDI channel 1-16 for the notes (default 1)."},
				"tap": {"type": "string", "description": "Which audio tap to listen on (its name or format source label). Omit to use the most-recently-active tap. See get_audio_tap's 'taps'."}
			}
		}`),
	}, s.handleProbeSound)
}

// probeSetting is one entry in probe_sound's settings[]: either a bound-device
// control (Device+Control, optionally Verify) or a raw CC (CC set).
type probeSetting struct {
	Device          string `json:"device"`
	Control         string `json:"control"`
	CC              *int   `json:"cc"`
	Value           any    `json:"value"`
	Channel         int    `json:"channel"`
	Verify          bool   `json:"verify"`
	VerifyTimeoutMS int    `json:"verify_timeout_ms"`
}

func (s *Server) handleProbeSound(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Serialize the whole excite→settle→capture cycle so probes never overlap on
	// the shared tap: each one analyses a clean, isolated segment.
	s.probeMu.Lock()
	defer s.probeMu.Unlock()

	var args struct {
		Settings   []probeSetting `json:"settings"`
		Notes      []int          `json:"notes"`
		Note       *int           `json:"note"`
		Velocity   *int           `json:"velocity"`
		DurationMS *int           `json:"duration_ms"`
		Channel    int            `json:"channel"`
		Tap        string         `json:"tap"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}

	// Resolve the tap to listen on up front so the whole excite→capture cycle
	// reads one consistent tap (named, or the most-recently-active).
	tap, ok := s.resolveTap(args.Tap)
	if !ok {
		msg := "no audio tap is connected; insert ProbeAudioTap on an AUM channel and enable streaming"
		if args.Tap != "" {
			msg = fmt.Sprintf("no audio tap named %q; known taps: %s", args.Tap, tapNamesText(s.audio.Names()))
		}
		return textResult(msg, true), nil
	}

	// 1) Apply settings (device controls and/or raw CCs) before exciting.
	applied := make([]map[string]any, 0, len(args.Settings))
	for i, set := range args.Settings {
		switch {
		case set.Device != "" && set.Control != "":
			entry := map[string]any{"device": set.Device, "control": set.Control, "value": set.Value}
			if set.Verify {
				res, err := s.eng.VerifyControl(ctx, set.Device, set.Control, set.Value,
					time.Duration(set.VerifyTimeoutMS)*time.Millisecond)
				if err != nil {
					return settingError(i, err), nil
				}
				entry["verify"] = res
			} else if err := s.eng.SetControl(ctx, set.Device, set.Control, set.Value); err != nil {
				return settingError(i, err), nil
			}
			applied = append(applied, entry)
		case set.CC != nil:
			val, ok := toInt(set.Value)
			if !ok {
				return textResult(fmt.Sprintf("/settings/%d/value: cc value must be an integer 0-127", i), true), nil
			}
			ch := clampChannel(set.Channel)
			cc := clamp7(*set.CC)
			v := clamp7(val)
			if err := s.sendBrain(ctx, midicontrol.Command{Type: "cc", Channel: ch, Controller: cc, Value: v}); err != nil {
				return textResult(fmt.Sprintf("/settings/%d: send cc failed: %v", i, err), true), nil
			}
			applied = append(applied, map[string]any{"cc": cc, "value": v, "channel": ch})
		default:
			return textResult(fmt.Sprintf("/settings/%d: provide device+control or cc", i), true), nil
		}
	}

	// 2) Resolve the notes to play and the hold duration.
	notes := args.Notes
	if args.Note != nil {
		notes = append(notes, *args.Note)
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
	duration := probeDefaultDurationMS
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

	// 3) Excite and capture a clean, isolated segment: settle so a prior tail
	// does not bleed in, mark the start epoch, note-on, hold through the sustain,
	// mark the end epoch, note-off, then extract + analyse exactly that segment
	// (not the shared rolling window) and write it to disk as a stereo WAV.
	snap, wavPath, capErr := s.exciteAndCapture(ctx, tap, notes, velocity, duration, ch)
	if capErr != nil {
		return textResult(capErr.Error(), true), nil
	}

	// Remember this probe's snapshot and recover the previous one so we can
	// auto-report a delta vs the last probe (the in-loop A/B the agent gets for
	// free, on top of the explicit capture_audio_snapshot / compare_audio).
	s.audioSnapsMu.Lock()
	prev := s.lastProbe
	cur := snap
	s.lastProbe = &cur
	s.audioSnapsMu.Unlock()

	return probeResult(applied, notes, velocity, duration, ch, snap, prev, wavPath), nil
}

// exciteAndCapture runs the probe lifecycle and returns the analysis snapshot
// plus the path of the WAV written for the captured segment (empty when no notes
// were played or the write failed). With no notes it degrades to a plain read of
// the live tap (a pure analysis probe).
func (s *Server) exciteAndCapture(ctx context.Context, tap *audiotap.Store, notes []int, velocity, duration, ch int) (audiotap.Snapshot, string, error) {
	if len(notes) == 0 {
		return tap.Snapshot(), "", nil
	}

	// Wait for any prior reverb/sustain tail to fall below the floor so it does
	// not contaminate the segment (bounded by settleTimeout).
	s.settleAudio(ctx, tap)

	start := tap.MarkEpoch()
	for _, n := range notes {
		if err := s.sendBrain(ctx, midicontrol.Command{Type: "noteOn", Channel: ch, Note: n, Velocity: velocity}); err != nil {
			return audiotap.Snapshot{}, "", fmt.Errorf("probe_sound (note-on) failed: %w", err)
		}
	}
	if duration > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(duration) * time.Millisecond):
		}
	}
	end := tap.MarkEpoch()

	// Note-off promptly: the segment range was already marked at `end`, so the
	// release tail (which arrives after note-off) falls outside [start,end).
	for _, n := range notes {
		if err := s.sendBrain(ctx, midicontrol.Command{Type: "noteOff", Channel: ch, Note: n}); err != nil {
			return audiotap.Snapshot{}, "", fmt.Errorf("probe_sound (note-off) failed: %w", err)
		}
	}

	snap, clip, ok := tap.SegmentSnapshot(start, end)
	if !ok {
		// The segment scrolled out of the window (e.g. a very long hold) — fall
		// back to a live snapshot so the probe still returns analysis.
		return tap.Snapshot(), "", nil
	}
	return snap, s.writeProbeWAV(clip, notes), nil
}

// settleAudio blocks until the tap's reported level drops below settleFloorRMS
// or settleTimeout elapses, so a prior probe's tail does not leak into the next
// capture. It uses the ~10 Hz reported RMS (recent level) rather than the
// whole-window RMS, which lingers because the multi-second window still holds
// the previous loud samples.
func (s *Server) settleAudio(ctx context.Context, tap *audiotap.Store) {
	deadline := time.Now().Add(settleTimeout)
	for {
		if tap.Level() < settleFloorRMS {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settlePollEvery):
		}
	}
}

// writeProbeWAV writes the captured segment to a stereo float32 WAV under
// config.AudioClipsDir() and prunes the dir to the retention budget, returning
// the path (empty on failure so the probe still returns its analysis).
func (s *Server) writeProbeWAV(clip audiotap.Clip, notes []int) string {
	if len(clip.Samples) == 0 {
		return ""
	}
	dir := config.AudioClipsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := fmt.Sprintf("probe-%s-%s.wav", time.Now().Format("20060102-150405.000"), notesTag(notes))
	path := filepath.Join(dir, name)
	if err := audiotap.WriteWAV(path, clip); err != nil {
		return ""
	}
	_ = audiotap.PruneDir(dir, maxProbeClips, maxProbeClipBytes)
	return path
}

// notesTag renders the played notes into a short filename fragment, e.g. "n69"
// or "n60_64_67"; empty notes become "n".
func notesTag(notes []int) string {
	return "n" + joinInts(notes, "_")
}

// settingError maps an engine SetControl/VerifyControl failure to a tool error
// with an RFC-6901 pointer rooted at the failing settings index (SEP-1303), so
// the model can self-correct the specific entry.
func settingError(i int, err error) *mcp.CallToolResult {
	var ve *device.ValidationError
	if errors.As(err, &ve) {
		return textResult(fmt.Sprintf("/settings/%d%s: %s", i, ve.Pointer, ve.Msg), true)
	}
	return textResult(fmt.Sprintf("/settings/%d: %v", i, err), true)
}

// probeResult assembles probe_sound's combined text + structuredContent: a
// one-line summary of what was applied/played, then the shared audio-snapshot
// rendering so the analysis reads identically to get_audio_tap.
func probeResult(applied []map[string]any, notes []int, velocity, duration, ch int, snap audiotap.Snapshot, prev *audiotap.Snapshot, wavPath string) *mcp.CallToolResult {
	var b strings.Builder
	if len(notes) > 0 {
		fmt.Fprintf(&b, "probe_sound: applied %d setting(s), played [%s] vel=%d for %dms on ch%d",
			len(applied), joinInts(notes, ","), velocity, duration, ch)
	} else {
		fmt.Fprintf(&b, "probe_sound: applied %d setting(s), read analysis (no notes played)", len(applied))
	}
	b.WriteString("\n")
	b.WriteString(describeAudioSnapshot(snap))
	if wavPath != "" {
		fmt.Fprintf(&b, "\n  segment wav: %s", wavPath)
	}

	structured := map[string]any{
		"settings_applied": applied,
		"notes":            notes,
		"velocity":         velocity,
		"duration_ms":      duration,
		"channel":          ch,
		"snapshot":         snap,
	}
	if wavPath != "" {
		structured["wav_path"] = wavPath
	}
	if prev != nil {
		deltaText, deltaMap := audioDelta(*prev, snap)
		fmt.Fprintf(&b, "\ndelta vs previous probe:\n%s", deltaText)
		structured["delta"] = deltaMap
	}
	return structResult(b.String(), structured)
}

// toInt coerces a JSON-decoded value (float64 from encoding/json, or an int/
// json.Number) to an int, reporting whether it was numeric.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
