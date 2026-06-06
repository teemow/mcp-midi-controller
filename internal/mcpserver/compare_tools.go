package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
)

// registerCompareTools wires the A/B comparison tools — capture_audio_snapshot
// and compare_audio — so an agent can answer "did that tweak make it brighter /
// louder / more harmonic?" with trusted, signed deltas instead of eyeballing
// two analysis blocks. Like the other audio tools it needs the audio store (the
// "ears"), so it is only registered alongside get_audio_tap.
func (s *Server) registerCompareTools() {
	if s.audio == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "capture_audio_snapshot",
		Description: "Capture the current audio-tap analysis under a label so it can later be compared with compare_audio. Stores the same trusted snapshot get_audio_tap returns (pitch, partials+HNR, loudness/crest, onset, spectral centroid/flatness) keyed by label, in memory only. " +
			"Typical A/B flow: capture_audio_snapshot {label:\"a\"}, change a CC (send_midi / probe_sound), capture_audio_snapshot {label:\"b\"}, then compare_audio {a:\"a\", b:\"b\"}. Emits structuredContent {label, stored, snapshot}.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"label": {"type": "string", "description": "Name to store the current snapshot under (overwrites an existing label)."},
				"name": {"type": "string", "description": "Which audio tap to capture (its name or format source label). Omit to use the most-recently-active tap. See get_audio_tap's 'taps'."}
			},
			"required": ["label"]
		}`),
	}, s.handleCaptureAudioSnapshot)

	s.mcp.AddTool(&mcp.Tool{
		Name: "compare_audio",
		Description: "Compare two previously captured audio snapshots (capture_audio_snapshot) and return signed deltas b-a: loudness (rms/peak/crest dBFS), pitch (cents, +=sharper), brightness (spectral centroid Hz +=brighter, flatness +=noisier), harmonics (HNR dB, partial count), and onset count. " +
			"Use after changing a parameter to confirm the effect: a louder CC yields +dBFS, a brighter/tone-up CC yields +centroid, a detune yields +cents. Emits structuredContent {a, b, delta}.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string", "description": "Label of the baseline snapshot."},
				"b": {"type": "string", "description": "Label of the snapshot to compare against the baseline (delta = b - a)."}
			},
			"required": ["a", "b"]
		}`),
	}, s.handleCompareAudio)
}

func (s *Server) handleCaptureAudioSnapshot(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Label string `json:"label"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Label) == "" {
		return textResult("provide a non-empty label to store the snapshot under", true), nil
	}

	st, ok := s.resolveTap(args.Name)
	if !ok {
		msg := "no audio tap is connected; insert ProbeAudioTap on an AUM channel and enable streaming"
		if args.Name != "" {
			msg = fmt.Sprintf("no audio tap named %q; known taps: %s", args.Name, tapNamesText(s.audio.Names()))
		}
		return textResult(msg, true), nil
	}
	snap := st.Snapshot()
	s.audioSnapsMu.Lock()
	s.audioSnaps[args.Label] = snap
	stored := len(s.audioSnaps)
	s.audioSnapsMu.Unlock()

	msg := fmt.Sprintf("captured audio snapshot %q (%d stored)\n%s", args.Label, stored, describeAudioSnapshot(snap))
	return structResult(msg, map[string]any{
		"label":    args.Label,
		"stored":   stored,
		"snapshot": snap,
	}), nil
}

func (s *Server) handleCompareAudio(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if args.A == "" || args.B == "" {
		return textResult("provide two snapshot labels: a (baseline) and b", true), nil
	}

	s.audioSnapsMu.Lock()
	snapA, okA := s.audioSnaps[args.A]
	snapB, okB := s.audioSnaps[args.B]
	s.audioSnapsMu.Unlock()
	if !okA {
		return textResult(fmt.Sprintf("/a: no snapshot captured under label %q (use capture_audio_snapshot first)", args.A), true), nil
	}
	if !okB {
		return textResult(fmt.Sprintf("/b: no snapshot captured under label %q (use capture_audio_snapshot first)", args.B), true), nil
	}

	deltaText, deltaMap := audioDelta(snapA, snapB)
	msg := fmt.Sprintf("compare audio %q -> %q:\n%s", args.A, args.B, deltaText)
	return structResult(msg, map[string]any{
		"a":     args.A,
		"b":     args.B,
		"delta": deltaMap,
	}), nil
}

// audioMetrics is the flat, comparable view extracted from a Snapshot's
// Analysis/Spectral blocks. The has* flags mark which dimensions had enough
// signal to compare (silence/noise leaves pitch and harmonics unset).
type audioMetrics struct {
	hasF0 bool
	f0Hz  float64
	note  string

	hasLevel bool
	rmsDBFS  float64
	peakDBFS float64
	crestDB  float64

	hasSpectral bool
	centroidHz  float64
	flatness    float64

	hasHarmonics bool
	hnrDB        float64
	partials     int

	onsetCount int
}

// metricsOf flattens a Snapshot into the dimensions compare_audio reports.
// Dynamics come from the Analysis block when present (trusted dBFS) and fall
// back to the window RMS/peak so a comparison is still possible from levels
// alone.
func metricsOf(s audiotap.Snapshot) audioMetrics {
	var m audioMetrics
	if a := s.Analysis; a != nil {
		m.hasLevel = true
		m.rmsDBFS = a.RMSDBFS
		m.peakDBFS = a.PeakDBFS
		m.crestDB = a.CrestDB
		m.onsetCount = a.OnsetCount
		if a.Note != "" && a.F0Hz > 0 {
			m.hasF0 = true
			m.f0Hz = a.F0Hz
			m.note = a.Note
			m.hasHarmonics = true
			m.hnrDB = a.HNRDb
			m.partials = len(a.Partials)
		}
	} else if s.WindowRMS > 0 {
		m.hasLevel = true
		m.rmsDBFS = audiotap.AmpToDBFS(float64(s.WindowRMS))
		m.peakDBFS = audiotap.AmpToDBFS(float64(s.WindowPeak))
		m.crestDB = m.peakDBFS - m.rmsDBFS
	}
	if sp := s.Spectral; sp != nil {
		m.hasSpectral = true
		m.centroidHz = sp.CentroidHz
		m.flatness = sp.Flatness
	}
	return m
}

// audioDelta computes the signed b-a deltas across loudness, pitch, brightness,
// harmonics and onsets, returning indented human text and a machine-readable
// map. A dimension is only reported when both snapshots had signal for it.
func audioDelta(prev, cur audiotap.Snapshot) (string, map[string]any) {
	a := metricsOf(prev)
	b := metricsOf(cur)
	m := map[string]any{}
	var sb strings.Builder

	if a.hasLevel && b.hasLevel {
		dRMS := b.rmsDBFS - a.rmsDBFS
		dPeak := b.peakDBFS - a.peakDBFS
		dCrest := b.crestDB - a.crestDB
		m["rms_dbfs_delta"] = dRMS
		m["peak_dbfs_delta"] = dPeak
		m["crest_db_delta"] = dCrest
		fmt.Fprintf(&sb, "\n  loudness: rms %+.1f dB, peak %+.1f dB, crest %+.1f dB", dRMS, dPeak, dCrest)
	}

	if a.hasF0 && b.hasF0 {
		cents := 1200 * math.Log2(b.f0Hz/a.f0Hz)
		m["f0_cents_delta"] = cents
		m["f0_hz_a"] = a.f0Hz
		m["f0_hz_b"] = b.f0Hz
		fmt.Fprintf(&sb, "\n  pitch: %+.0f cents (%s %.1fHz -> %s %.1fHz)", cents, a.note, a.f0Hz, b.note, b.f0Hz)
	}

	if a.hasSpectral && b.hasSpectral {
		dCent := b.centroidHz - a.centroidHz
		dFlat := b.flatness - a.flatness
		m["centroid_hz_delta"] = dCent
		m["flatness_delta"] = dFlat
		fmt.Fprintf(&sb, "\n  brightness: centroid %+.0f Hz, flatness %+.3f", dCent, dFlat)
	}

	if a.hasHarmonics && b.hasHarmonics {
		dHNR := b.hnrDB - a.hnrDB
		m["hnr_db_delta"] = dHNR
		m["partials_a"] = a.partials
		m["partials_b"] = b.partials
		fmt.Fprintf(&sb, "\n  harmonics: hnr %+.1f dB, partials %d -> %d", dHNR, a.partials, b.partials)
	}

	dOnset := b.onsetCount - a.onsetCount
	m["onset_count_delta"] = dOnset
	fmt.Fprintf(&sb, "\n  onsets: %d -> %d (%+d)", a.onsetCount, b.onsetCount, dOnset)

	text := strings.TrimPrefix(sb.String(), "\n")
	if text == "" {
		text = "no comparable analysis (insufficient signal in one or both snapshots)"
	}
	return text, m
}
