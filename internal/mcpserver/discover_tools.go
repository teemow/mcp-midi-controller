package mcpserver

// discover_devices is Phase 8: one unified discovery surface over every place a
// device can come from. It aggregates the three feeders into a single list of
// candidates, each annotated with WHERE it was found (a transient discovery
// source, never stored on the bound device) and a SUGGESTED device type, so
// adding a device is identical regardless of source — pass the candidate's
// transport + endpoint + suggestedType (+ channel) to bind_device.
//
// The three sources:
//   - "endpoint": a reachable transport endpoint (BLE/USB/OSC via
//     Transport.Discover), the same scan discover_endpoints performs.
//   - "catalog": a staged AUv3 probe dump — an importable device type (transport
//     auv3midi), the same corpus list_auv3_probes browses.
//   - "session": a hosted AUv3 node inside a loaded AUM session — a device in the
//     iPad rig, matched to a staged probe and placed on its matrix-derived
//     channel, the same nodes import_aum_session walks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/midi-device/device"
)

// discoveredDevice is one candidate surfaced by discover_devices. Source is the
// transient discovery provenance (how we found it); the rest is what bind_device
// needs to add it. Once bound, none of the discovery metadata is kept on the
// device — a device is what it is, not how it was found.
type discoveredDevice struct {
	// Source is where this candidate was discovered: "endpoint" (a reachable
	// transport endpoint), "catalog" (a staged AUv3 probe — an importable device
	// type), or "session" (a hosted AUv3 node in a loaded AUM session).
	Source string `json:"source"`
	// Name is the friendly label (endpoint name, plugin name, or node/channel).
	Name string `json:"name,omitempty"`
	// SuggestedType is the device-type id to bind with; TypeKnown reports whether
	// that type is already loaded in the registry. A catalog/session AUv3 type is
	// typically not yet loaded — stage it with import_auv3_probe (catalog) or
	// import_aum_session (session) first, which auto-creates the device for you.
	SuggestedType string `json:"suggestedType,omitempty"`
	TypeKnown     bool   `json:"typeKnown"`
	// Transport + Endpoint + Channel are the bind_device coordinates, uniform
	// across sources. Channel is the 1-based send channel (0 = unknown).
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Channel   int    `json:"channel,omitempty"`
	Paired    bool   `json:"paired,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	// SessionID / ProbeID carry the originating session / staged probe id (the
	// session and catalog provenance). Note explains the recommended next step.
	SessionID string `json:"sessionId,omitempty"`
	ProbeID   string `json:"probeId,omitempty"`
	Note      string `json:"note,omitempty"`
}

func (s *Server) registerDiscoverTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "discover_devices",
		Description: "Unified discovery: every device you could add, from all sources, in one list. Aggregates " +
			"reachable transport endpoints (BLE/USB/OSC — what discover_endpoints scans), the staged AUv3 probe catalog " +
			"(importable device types — what list_auv3_probes browses), and the hosted AUv3 nodes of loaded AUM sessions " +
			"(the iPad rig — what import_aum_session walks). Each candidate is annotated with its discovery source " +
			"(\"endpoint\"|\"catalog\"|\"session\", transient — never stored on the device) and a suggested device type, " +
			"plus the transport/endpoint/channel to add it. Adding is identical regardless of source: bind_device with " +
			"those coordinates (catalog/session AUv3 types are staged for you by import_auv3_probe / import_aum_session).",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, s.handleDiscoverDevices)
}

func (s *Server) handleDiscoverDevices(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var (
		out   []discoveredDevice
		notes []string
	)

	// --- Source 1: reachable transport endpoints -------------------------
	eps, err := s.eng.DiscoverEndpoints(ctx)
	if err != nil {
		// Tolerate a failed scan: the catalog/session sources are independent
		// and may still surface candidates.
		notes = append(notes, "endpoint scan failed: "+err.Error())
	}
	for _, ep := range eps {
		dd := discoveredDevice{
			Source:    "endpoint",
			Name:      ep.Name,
			Transport: ep.Transport,
			Endpoint:  ep.ID,
			Paired:    ep.Paired,
			Connected: ep.Connected,
		}
		if id, ok := s.suggestTypeForEndpoint(ep.Name, ep.Transport); ok {
			dd.SuggestedType = id
			dd.TypeKnown = true
		} else {
			dd.Note = "pick a device type (see list_devices available=true), then bind_device"
		}
		out = append(out, dd)
	}

	// Staged probe dumps back both the catalog source and the session-node
	// matching, so load them once.
	dumps := loadStagedProbeDumps()

	// --- Source 2: the AUv3 probe catalog (importable device types) ------
	out = append(out, s.catalogCandidates(dumps)...)

	// --- Source 3: hosted AUv3 nodes of loaded AUM sessions --------------
	sessionCands, sessionNote := s.sessionCandidates(dumps)
	out = append(out, sessionCands...)
	if sessionNote != "" {
		notes = append(notes, sessionNote)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})

	return structResult(s.renderDiscovered(out, notes), map[string]any{
		"devices": out,
		"counts":  countBySource(out),
	}), nil
}

// catalogCandidates turns every staged probe dump into a catalog candidate: an
// importable AUv3 device type (transport auv3midi). TypeKnown reports whether
// the derived type is already loaded (import already run); otherwise the note
// points at import_auv3_probe.
func (s *Server) catalogCandidates(dumps []device.ProbeDump) []discoveredDevice {
	out := make([]discoveredDevice, 0, len(dumps))
	for _, dump := range dumps {
		id := device.ProbeID(dump)
		dd := discoveredDevice{
			Source:        "catalog",
			Name:          device.FirstNonEmpty(dump.Name, id),
			SuggestedType: id,
			TypeKnown:     s.typeKnown(id),
			Transport:     auv3midiTransport,
			Endpoint:      auv3midiBrainEndpoint,
			ProbeID:       id,
		}
		if !dd.TypeKnown {
			dd.Note = fmt.Sprintf("import_auv3_probe device_id=%q to stage the device type, then bind_device", id)
		} else {
			dd.Note = "device type already staged; bind_device (transport auv3midi, endpoint \"brain\")"
		}
		out = append(out, dd)
	}
	return out
}

// sessionCandidates walks every loaded AUM session and emits one candidate per
// hosted AUv3 node, matched to a staged probe (the suggested type) and placed on
// its matrix-derived 1-based send channel. The second return is a non-fatal note
// (e.g. the sessions dir does not exist yet).
func (s *Server) sessionCandidates(dumps []device.ProbeDump) ([]discoveredDevice, string) {
	dir := config.AUMSessionsDir()

	var out []discoveredDevice
	walkErr := aum.WalkStaged(dir, func(rel, full, kind string, _ fs.FileInfo) {
		if kind != aum.KindSession {
			return
		}
		sessionID := aum.StripExt(rel)
		sess, oerr := aum.OpenFile(full)
		if oerr != nil {
			return // a bad session should not block discovery of the rest
		}
		sm := sess.Map()
		for ci, ch := range sm.Channels {
			for _, n := range ch.Nodes {
				if n.Component == nil {
					continue // built-in node, not a hosted AUv3 plugin
				}
				dd := discoveredDevice{
					Source:    "session",
					Name:      device.FirstNonEmpty(n.ComponentName, ch.Title, n.Component.Subtype),
					Transport: auv3midiTransport,
					Endpoint:  auv3midiBrainEndpoint,
					SessionID: sessionID,
				}
				if dump := findProbeForComponent(dumps, *n.Component); dump != nil {
					id := device.ProbeID(*dump)
					dd.SuggestedType = id
					dd.TypeKnown = s.typeKnown(id)
					dd.ProbeID = id
				} else {
					dd.Note = "no staged probe matches this node — run the auv3 probe for this plugin, then re-discover"
				}
				if send, ok := sessionChannelOf(sm, ci); ok {
					dd.Channel = send
				} else if dd.Note == "" {
					dd.Note = "channel not inferable (session not wired to the convention yet)"
				}
				out = append(out, dd)
			}
		}
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, "read sessions dir: " + walkErr.Error()
	}
	return out, ""
}

// suggestTypeForEndpoint best-effort matches a discovered endpoint to a loaded
// device type by name, so a freshly discovered "H90" BLE peripheral suggests the
// h90 type. It only matches a type whose transport equals the endpoint's (a USB
// port never suggests a BLE-only type), and requires the type's id or name to
// appear in the endpoint name (normalized), to keep false positives down.
func (s *Server) suggestTypeForEndpoint(name, transport string) (string, bool) {
	norm := normalizeName(name)
	if norm == "" {
		return "", false
	}
	var match string
	for _, d := range s.eng.Registry().All() {
		if d.Transport != transport {
			continue
		}
		id := normalizeName(d.ID)
		nm := normalizeName(d.Name)
		if (id != "" && strings.Contains(norm, id)) || (nm != "" && strings.Contains(norm, nm)) {
			// Prefer the longest (most specific) match.
			if len(d.ID) > len(match) {
				match = d.ID
			}
		}
	}
	return match, match != ""
}

// typeKnown reports whether a device-type id is already loaded in the registry.
func (s *Server) typeKnown(id string) bool {
	_, ok := s.eng.Registry().Get(id)
	return ok
}

// sessionChannelOf returns the suggested 1-based send channel for a session
// channel index: the channel of any assigned mapping under that strip, converted
// from the raw 0-based on-disk channel (0 = send ch1). Mirrors the channelOf
// logic in import_aum_session.
func sessionChannelOf(sm aum.SessionMap, chanIndex int) (int, bool) {
	prefix := fmt.Sprintf("Channels/chan%d/", chanIndex)
	for _, m := range sm.Mappings {
		if strings.HasPrefix(m.Collection, prefix) && m.Channel >= 0 {
			return m.Channel + 1, true
		}
	}
	return 0, false
}

// normalizeName lowercases a name and strips every non-alphanumeric rune, so
// "H90 Pedal" and "h90" both reduce to comparable forms.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// countBySource tallies candidates per discovery source for the structured
// output.
func countBySource(devs []discoveredDevice) map[string]int {
	counts := map[string]int{"endpoint": 0, "catalog": 0, "session": 0}
	for _, d := range devs {
		counts[d.Source]++
	}
	return counts
}

// renderDiscovered builds the human-readable rendering: candidates grouped by
// source, with any non-fatal source notes appended.
func (s *Server) renderDiscovered(devs []discoveredDevice, notes []string) string {
	var b strings.Builder
	c := countBySource(devs)
	fmt.Fprintf(&b, "discovered %d device(s): %d endpoint, %d catalog, %d session\n",
		len(devs), c["endpoint"], c["catalog"], c["session"])

	for _, src := range []string{"endpoint", "catalog", "session"} {
		first := true
		for _, d := range devs {
			if d.Source != src {
				continue
			}
			if first {
				fmt.Fprintf(&b, "%s:\n", sourceHeading(src))
				first = false
			}
			b.WriteString("  ")
			b.WriteString(renderCandidateLine(d))
			b.WriteByte('\n')
		}
	}
	if len(devs) == 0 {
		b.WriteString("no devices discovered (no endpoints, no staged probes, no loaded sessions)\n")
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

func sourceHeading(src string) string {
	switch src {
	case "endpoint":
		return "transport endpoints"
	case "catalog":
		return "AUv3 catalog (staged probes)"
	case "session":
		return "AUM session nodes"
	default:
		return src
	}
}

// renderCandidateLine renders one candidate as a single human line: name, the
// suggested type (with a ? when not yet loaded), the bind coordinates and any
// note.
func renderCandidateLine(d discoveredDevice) string {
	var b strings.Builder
	name := d.Name
	if name == "" {
		name = "(unnamed)"
	}
	fmt.Fprintf(&b, "%-24s", name)
	if d.SuggestedType != "" {
		known := ""
		if !d.TypeKnown {
			known = " (not loaded)"
		}
		fmt.Fprintf(&b, " type=%s%s", d.SuggestedType, known)
	} else {
		b.WriteString(" type=?")
	}
	if d.Transport != "" {
		fmt.Fprintf(&b, " transport=%s", d.Transport)
	}
	if d.Endpoint != "" {
		fmt.Fprintf(&b, " endpoint=%q", d.Endpoint)
	}
	if d.Channel > 0 {
		fmt.Fprintf(&b, " channel=%d", d.Channel)
	}
	switch d.Source {
	case "endpoint":
		fmt.Fprintf(&b, " (paired=%t, connected=%t)", d.Paired, d.Connected)
	case "session":
		fmt.Fprintf(&b, " (session=%s)", d.SessionID)
	}
	if d.Note != "" {
		fmt.Fprintf(&b, " — %s", d.Note)
	}
	return b.String()
}
