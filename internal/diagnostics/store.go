// Package diagnostics is the off-MCP LAN listener that terminates the
// host-diagnostics stream produced by the auv3-probe AUv3 extensions
// (github.com/teemow/auv3-probe, ProbeMidiBrain / ProbeAudioTap) and keeps the
// latest snapshot in memory, giving an agent a read on "what can the plugin see
// about its host?" via the get_host_diagnostics MCP tool.
//
// Like internal/audiotap, internal/midicontrol, internal/auv3receiver and
// internal/aumreceiver it is a SEPARATE listener from the loopback-only MCP
// endpoint (the iPad cannot reach loopback). Its surface is intentionally tiny:
// a single WebSocket endpoint that ingests one JSON snapshot per frame into an
// in-memory store. It never touches the engine, transports, or hardware, and
// nothing is written to disk — diagnostics are a volatile rig signal, so they
// live only in RAM and are exposed read-only through MCP.
//
// The snapshot is the full `HostDiagnostics` envelope the appex assembles
// (transport/musicalContext/renderTime/render/audioUnit/midi/audioSession/
// coreMIDI/environment — see docs/auv3-extension.md, "Diagnostics protocol").
// The store keeps the raw envelope bytes verbatim so no field is lost when the
// appex schema grows ahead of the daemon, and decodes a small header (schema
// version, source, captured-at) for the human-readable summary.
package diagnostics

import (
	"encoding/json"
	"sync"
	"time"
)

// header is the small slice of the HostDiagnostics envelope the store decodes
// for metadata/summary; the rest of the envelope is preserved verbatim in raw.
// The JSON keys match the appex's Swift Codable property names exactly.
type header struct {
	SchemaVersion int    `json:"schemaVersion"`
	Source        string `json:"source"`
	CapturedAt    string `json:"capturedAt"`
}

// Store holds the latest host-diagnostics snapshot. A single appex streams at a
// time (one AUM insert reporting); a second connection simply overwrites the
// connection metadata and snapshot. All access is mutex-guarded — the WebSocket
// reader writes, MCP tool calls read, on different goroutines.
type Store struct {
	mu sync.Mutex

	connected     bool
	remote        string
	connectedAt   time.Time
	lastMessageAt time.Time
	snapshotAt    time.Time

	messages int64

	// raw is the most recent full envelope as received (compact JSON object);
	// hdr is the decoded header slice of it for the summary. raw is nil until
	// the first snapshot arrives.
	raw json.RawMessage
	hdr header
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{}
}

// Connect marks an appex as connected from remote and clears the previous
// snapshot so stale diagnostics from an earlier session are not reported as
// current.
func (s *Store) Connect(remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.connected = true
	s.remote = remote
	s.connectedAt = now
	s.lastMessageAt = now
	s.snapshotAt = time.Time{}
	s.raw = nil
	s.hdr = header{}
}

// Disconnect marks the appex as gone but keeps the last snapshot so a poll right
// after a drop still reports the final state (with a growing age).
func (s *Store) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
}

// SetSnapshot records one full HostDiagnostics envelope received on the wire.
// The bytes are copied and kept verbatim (so unknown/future fields survive),
// and a small header is decoded for the summary. A frame that does not parse as
// a JSON object is ignored (reported via ok=false) so a malformed producer
// cannot poison the store.
func (s *Store) SetSnapshot(data []byte) (ok bool) {
	var hdr header
	if err := json.Unmarshal(data, &hdr); err != nil {
		return false
	}
	raw := make(json.RawMessage, len(data))
	copy(raw, data)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.raw = raw
	s.hdr = hdr
	s.messages++
	s.snapshotAt = now
	s.lastMessageAt = now
	return true
}

// Snapshot is the read-only view returned by the get_host_diagnostics MCP tool:
// connection/age metadata plus the full HostDiagnostics envelope (verbatim) the
// appex last reported.
type Snapshot struct {
	Connected bool `json:"connected"`

	Source        string `json:"source,omitempty"`
	Remote        string `json:"remote,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	CapturedAt    string `json:"captured_at,omitempty"`

	ConnectedForMS   int64 `json:"connected_for_ms,omitempty"`
	SnapshotAgeMS    int64 `json:"snapshot_age_ms,omitempty"`
	LastMessageAgeMS int64 `json:"last_message_age_ms"`
	Messages         int64 `json:"messages"`

	// Diagnostics is the full HostDiagnostics envelope as received, embedded
	// inline so the agent gets every section/field the appex reported without
	// the daemon having to model the (evolving) schema. nil until the first
	// snapshot arrives.
	Diagnostics json.RawMessage `json:"diagnostics,omitempty"`
}

// Snapshot computes the current view: the last envelope, the decoded header
// metadata, and connection/age metadata.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := Snapshot{
		Connected:     s.connected,
		Remote:        s.remote,
		Source:        s.hdr.Source,
		SchemaVersion: s.hdr.SchemaVersion,
		CapturedAt:    s.hdr.CapturedAt,
		Messages:      s.messages,
		Diagnostics:   s.raw,
	}

	now := time.Now()
	if !s.lastMessageAt.IsZero() {
		snap.LastMessageAgeMS = now.Sub(s.lastMessageAt).Milliseconds()
	}
	if !s.snapshotAt.IsZero() {
		snap.SnapshotAgeMS = now.Sub(s.snapshotAt).Milliseconds()
	}
	if s.connected && !s.connectedAt.IsZero() {
		snap.ConnectedForMS = now.Sub(s.connectedAt).Milliseconds()
	}
	return snap
}
