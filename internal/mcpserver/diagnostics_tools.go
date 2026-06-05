package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/diagnostics"
)

// registerDiagnosticsTools wires the read-only get_host_diagnostics tool: the
// latest host-diagnostics snapshot an auv3-probe extension reported (transport,
// AU identity/capabilities, MIDI protocol negotiation, AVAudioSession, CoreMIDI
// graph, runtime environment) terminated by internal/diagnostics. It is only
// registered when a diagnostics store is attached (WithDiagnostics); like the
// other rig-reasoning reads it never mutates anything and emits
// structuredContent.
func (s *Server) registerDiagnosticsTools() {
	if s.diag == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "get_host_diagnostics",
		Description: "Read the live host diagnostics an auv3-probe AUv3 extension (ProbeMidiBrain / ProbeAudioTap) reports while hosted in AUM: \"what can the plugin actually see about its host?\". " +
			"Returns the full snapshot the appex assembles — transport + musical context (tempo, time signature, playback/record/cycle, position), the render AudioTimeStamp, the AU's own identity and capabilities (names, component type, channel/MPE support, latency, parameter-tree summary, presets), MIDI protocol negotiation (MIDI 1.0 vs 2.0) and MIDI-CI profiles, the full AVAudioSession surface (route, channels, latencies, sample rate, category), the CoreMIDI endpoint/device graph, and the runtime environment (thermal state, low-power mode, memory, OS/device). " +
			"Diagnostics are streamed over the LAN ~1 Hz (and on route/interruption changes); nothing is stored on disk. Emits structuredContent with connection metadata plus the full envelope under \"diagnostics\".",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleGetHostDiagnostics)
}

func (s *Server) handleGetHostDiagnostics(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	snap := s.diag.Snapshot()
	return structResult(describeDiagnosticsSnapshot(snap), snap), nil
}

// diagSummary is the slice of the HostDiagnostics envelope describe parses for
// the human-readable text. The JSON keys match the appex's Swift Codable
// property names. It is intentionally partial — the full envelope is returned
// verbatim in structuredContent; this is only the at-a-glance line.
type diagSummary struct {
	Transport struct {
		Available bool `json:"available"`
		Moving    bool `json:"moving"`
		Recording bool `json:"recording"`
	} `json:"transport"`
	MusicalContext struct {
		Available bool    `json:"available"`
		Tempo     float64 `json:"tempo"`
	} `json:"musicalContext"`
	AudioUnit struct {
		Available     bool   `json:"available"`
		AudioUnitName string `json:"audioUnitName"`
		ComponentType string `json:"componentType"`
		ParameterTree struct {
			Count int `json:"count"`
		} `json:"parameterTree"`
	} `json:"audioUnit"`
	MIDI struct {
		HostMIDIProtocol      string `json:"hostMIDIProtocol"`
		AudioUnitMIDIProtocol string `json:"audioUnitMIDIProtocol"`
	} `json:"midi"`
	AudioSession struct {
		SampleRate  float64  `json:"sampleRate"`
		OutputPorts []string `json:"outputPorts"`
	} `json:"audioSession"`
	CoreMIDI struct {
		Sources      []json.RawMessage `json:"sources"`
		Destinations []json.RawMessage `json:"destinations"`
	} `json:"coreMIDI"`
	Environment struct {
		ThermalState        string `json:"thermalState"`
		LowPowerModeEnabled bool   `json:"lowPowerModeEnabled"`
	} `json:"environment"`
}

// describeDiagnosticsSnapshot renders the human-readable text for a
// host-diagnostics Snapshot: connection state plus the headline fields from the
// last envelope. The full envelope rides along in structuredContent, so this is
// just an at-a-glance summary.
func describeDiagnosticsSnapshot(snap diagnostics.Snapshot) string {
	var b strings.Builder
	if len(snap.Diagnostics) == 0 {
		if snap.Connected {
			b.WriteString("host-diagnostics stream connected; awaiting first snapshot")
		} else if snap.LastMessageAgeMS == 0 && snap.Messages == 0 {
			b.WriteString("no host diagnostics yet; load ProbeMidiBrain or ProbeAudioTap in AUM and enable reporting to the daemon")
		} else {
			fmt.Fprintf(&b, "host-diagnostics disconnected (last message %dms ago)", snap.LastMessageAgeMS)
		}
		return b.String()
	}

	src := snap.Source
	if src == "" {
		src = "auv3-probe"
	}
	if snap.Connected {
		fmt.Fprintf(&b, "%s reporting from %s (snapshot %dms ago, schema v%d)", src, snap.Remote, snap.SnapshotAgeMS, snap.SchemaVersion)
	} else {
		fmt.Fprintf(&b, "%s disconnected; last snapshot %dms ago (schema v%d)", src, snap.SnapshotAgeMS, snap.SchemaVersion)
	}

	var d diagSummary
	if err := json.Unmarshal(snap.Diagnostics, &d); err != nil {
		// Envelope is opaque to this daemon version; the full payload is still
		// in structuredContent.
		fmt.Fprintf(&b, "\n  (envelope not summarised: %v)", err)
		return b.String()
	}

	if d.AudioUnit.Available {
		name := d.AudioUnit.AudioUnitName
		if name == "" {
			name = "unknown"
		}
		fmt.Fprintf(&b, "\n  unit: %s [%s] (%d params)", name, d.AudioUnit.ComponentType, d.AudioUnit.ParameterTree.Count)
	}
	if d.Transport.Available {
		state := "stopped"
		if d.Transport.Moving {
			state = "playing"
		}
		if d.Transport.Recording {
			state += "+rec"
		}
		fmt.Fprintf(&b, "\n  transport: %s", state)
		if d.MusicalContext.Available && d.MusicalContext.Tempo > 0 {
			fmt.Fprintf(&b, " @ %.1f BPM", d.MusicalContext.Tempo)
		}
	}
	if d.MIDI.HostMIDIProtocol != "" || d.MIDI.AudioUnitMIDIProtocol != "" {
		fmt.Fprintf(&b, "\n  midi: host=%s unit=%s", orNone(d.MIDI.HostMIDIProtocol), orNone(d.MIDI.AudioUnitMIDIProtocol))
	}
	if d.AudioSession.SampleRate > 0 {
		fmt.Fprintf(&b, "\n  audio session: %.0f Hz", d.AudioSession.SampleRate)
		if len(d.AudioSession.OutputPorts) > 0 {
			fmt.Fprintf(&b, " out=%s", strings.Join(d.AudioSession.OutputPorts, ","))
		}
	}
	if n := len(d.CoreMIDI.Sources); n > 0 || len(d.CoreMIDI.Destinations) > 0 {
		fmt.Fprintf(&b, "\n  coremidi: %d sources, %d destinations", n, len(d.CoreMIDI.Destinations))
	}
	if d.Environment.ThermalState != "" {
		fmt.Fprintf(&b, "\n  environment: thermal=%s", d.Environment.ThermalState)
		if d.Environment.LowPowerModeEnabled {
			b.WriteString(" low-power")
		}
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
