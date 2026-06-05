package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
)

// maxClipFrames bounds the PCM returned by get_audio_clip so a single call
// cannot ship the whole multi-second window as base64. It is a FRAME cap (one
// sample per channel), so a stereo clip ships 2× the float32s. 32768 frames is
// ~0.68 s at 48 kHz — enough for the agent to run its own analysis; pass
// duration_ms for less. The full, lossless per-probe segment is the WAV that
// probe_sound writes (wav_path), not this base64 view.
const maxClipFrames = 1 << 15 // 32768 frames

// registerAudioTools wires the read-only get_audio_tap tool, the agent's "ears":
// the latest levels (RMS/peak), a short peak-envelope waveform, and connection
// metadata for the ProbeAudioTap stream terminated by internal/audiotap. It is
// only registered when an audio store is attached (WithAudioTap); like the other
// rig-reasoning reads it never mutates anything and emits structuredContent.
func (s *Server) registerAudioTools() {
	if s.audio == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "get_audio_tap",
		Description: "Read the live audio tap (the agent's \"ears\"): whether a ProbeAudioTap insert is streaming, the latest RMS/peak levels, window-derived peak/RMS over the last few seconds, a short peak-envelope waveform, and connection/age metadata. " +
			"Also returns trusted Go-computed musical analysis over the rolling window so you do NOT need to fetch and DSP base64 PCM: detected pitch (f0 Hz, nearest note, cents offset, confidence), harmonic partials (frequency, dBFS, harmonic number) with a harmonic-to-noise ratio, loudness/dynamics (RMS/peak dBFS, crest factor), and onset activity (count + ms since last attack). " +
			"Audio is captured from an AUM channel by the auv3-probe ProbeAudioTap AUv3 and streamed over the LAN; nothing is stored on disk. Emits structuredContent with the full snapshot including the analysis block.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleGetAudioTap)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_audio_clip",
		Description: "Fetch the most recent full-rate PCM from the audio tap as base64-encoded little-endian float32 (f32le), interleaved across channels, plus the sample rate and channel count, so the agent can run its own analysis (pitch, onset, timbre, stereo image). " +
			"Bounded to a fraction of a second of the rolling window; pass duration_ms to request a specific span. For the lossless per-probe segment use probe_sound's wav_path. Returns structuredContent with {encoding, sample_rate, channels, samples, pcm_base64} where samples is the frame count.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"duration_ms": {"type": "integer", "minimum": 1, "description": "How many milliseconds of the most recent audio to return (default/cap ~0.7s at 48 kHz)."}
			}
		}`),
	}, s.handleGetAudioClip)

	s.registerProbeSound()
	s.registerCompareTools()
}

func (s *Server) handleGetAudioTap(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	snap := s.audio.Snapshot()
	return structResult(describeAudioSnapshot(snap), snap), nil
}

// describeAudioSnapshot renders the human-readable text for an audio-tap
// Snapshot: connection state, levels, window stats, spectral and (trusted)
// musical analysis. Shared by get_audio_tap and probe_sound so both surface the
// same numbers without parsing base64 PCM.
func describeAudioSnapshot(snap audiotap.Snapshot) string {
	var b strings.Builder
	if !snap.Connected {
		if snap.LastMessageAgeMS == 0 && snap.AudioMessages == 0 {
			b.WriteString("no audio tap has connected yet; insert ProbeAudioTap on an AUM channel and enable streaming to the daemon")
		} else {
			fmt.Fprintf(&b, "audio tap disconnected (last message %dms ago)", snap.LastMessageAgeMS)
		}
		return b.String()
	}

	src := snap.Source
	if src == "" {
		src = "audio tap"
	}
	fmt.Fprintf(&b, "%s connected from %s", src, snap.Remote)
	if snap.SampleRate > 0 {
		fmt.Fprintf(&b, " (%gch %s @ %.0f Hz)", float64(snap.Channels), snap.Encoding, snap.SampleRate)
	}
	fmt.Fprintf(&b, "\n  levels: rms=%.4f peak=%.4f (%dms ago)", snap.RMS, snap.Peak, snap.FeaturesAgeMS)
	fmt.Fprintf(&b, "\n  window: rms=%.4f peak=%.4f over %.1fs (%d samples)",
		snap.WindowRMS, snap.WindowPeak, snap.WindowSeconds, snap.WindowSamples)
	if sp := snap.Spectral; sp != nil {
		fmt.Fprintf(&b, "\n  spectral: centroid=%.0f Hz flatness=%.3f (%d-pt FFT)", sp.CentroidHz, sp.Flatness, sp.FFTSize)
	}
	if an := snap.Analysis; an != nil {
		if an.Note != "" {
			fmt.Fprintf(&b, "\n  pitch: %s %.1f Hz (%+.0f cents) confidence=%.2f", an.Note, an.F0Hz, an.Cents, an.Confidence)
		} else {
			fmt.Fprintf(&b, "\n  pitch: none (confidence=%.2f)", an.Confidence)
		}
		fmt.Fprintf(&b, "\n  dynamics: rms=%.1f dBFS peak=%.1f dBFS crest=%.1f dB", an.RMSDBFS, an.PeakDBFS, an.CrestDB)
		if len(an.Partials) > 0 {
			b.WriteString("\n  partials:")
			for _, p := range an.Partials {
				if p.Harmonic > 0 {
					fmt.Fprintf(&b, " %.0fHz(%.0fdB,h%d)", p.FreqHz, p.DB, p.Harmonic)
				} else {
					fmt.Fprintf(&b, " %.0fHz(%.0fdB)", p.FreqHz, p.DB)
				}
			}
			fmt.Fprintf(&b, " hnr=%.1f dB", an.HNRDb)
		}
		fmt.Fprintf(&b, "\n  onsets: count=%d", an.OnsetCount)
		if an.MSSinceOnset >= 0 {
			fmt.Fprintf(&b, " last=%.0fms ago", an.MSSinceOnset)
		}
	}
	return b.String()
}

func (s *Server) handleGetAudioClip(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		DurationMS int `json:"duration_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}

	// Peek at the current sample rate to translate duration_ms into frames
	// (cheap accessor, no analysis).
	sampleRate := s.audio.SampleRate()
	maxFrames := maxClipFrames
	if args.DurationMS > 0 && sampleRate > 0 {
		want := int(math.Ceil(float64(args.DurationMS) / 1000.0 * sampleRate))
		if want > 0 && want < maxFrames {
			maxFrames = want
		}
	}

	clip := s.audio.Clip(maxFrames)
	if len(clip.Samples) == 0 {
		return structResult("no audio captured yet; insert ProbeAudioTap and enable streaming", map[string]any{
			"connected": clip.Connected,
			"samples":   0,
		}), nil
	}

	ch := clip.Channels
	if ch < 1 {
		ch = 1
	}
	frames := len(clip.Samples) / ch

	// Encode interleaved float32 little-endian, then base64.
	raw := make([]byte, len(clip.Samples)*4)
	for i, v := range clip.Samples {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	durMS := 0.0
	if clip.SampleRate > 0 {
		durMS = float64(frames) / clip.SampleRate * 1000
	}
	msg := fmt.Sprintf("audio clip: %d frames (%.0f ms) %dch %s @ %.0f Hz",
		frames, durMS, ch, clip.Encoding, clip.SampleRate)
	return structResult(msg, map[string]any{
		"connected":   clip.Connected,
		"encoding":    clip.Encoding,
		"sample_rate": clip.SampleRate,
		"channels":    ch,
		"samples":     frames,
		"duration_ms": durMS,
		"pcm_base64":  encoded,
	}), nil
}
