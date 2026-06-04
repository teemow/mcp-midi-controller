package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
			"Audio is captured from an AUM channel by the auv3-probe ProbeAudioTap AUv3 and streamed over the LAN; nothing is stored on disk. Emits structuredContent with the full snapshot.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleGetAudioTap)
}

func (s *Server) handleGetAudioTap(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	snap := s.audio.Snapshot()

	var b strings.Builder
	if !snap.Connected {
		if snap.LastMessageAgeMS == 0 && snap.AudioMessages == 0 {
			b.WriteString("no audio tap has connected yet; insert ProbeAudioTap on an AUM channel and enable streaming to the daemon")
		} else {
			fmt.Fprintf(&b, "audio tap disconnected (last message %dms ago)", snap.LastMessageAgeMS)
		}
		return structResult(b.String(), snap), nil
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
	return structResult(b.String(), snap), nil
}
