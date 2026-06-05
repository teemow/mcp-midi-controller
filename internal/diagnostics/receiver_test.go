package diagnostics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestReceiverEndToEnd dials the receiver as a real WebSocket client, sends a
// full HostDiagnostics envelope as one TEXT frame, and asserts the store and the
// connect/disconnect callbacks reflect it (including verbatim passthrough of a
// field the daemon does not model).
func TestReceiverEndToEnd(t *testing.T) {
	store := NewStore()
	var connects, disconnects atomic.Int32

	mux := http.NewServeMux()
	Register(mux, store,
		func(string) { connects.Add(1) },
		func(string) { disconnects.Add(1) },
	)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/diagnostics"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// A representative envelope: header fields the store decodes plus a nested
	// section (audioUnit) the store keeps verbatim without modeling it.
	envelope := `{"schemaVersion":1,"source":"ProbeMidiBrain","capturedAt":"2026-06-05T09:11:00Z",` +
		`"audioUnit":{"available":true,"audioUnitName":"ProbeMidiBrain","componentType":"aumi"},` +
		`"midi":{"available":true,"hostMIDIProtocol":"MIDI 2.0"}}`
	if err := c.Write(ctx, websocket.MessageText, []byte(envelope)); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var snap Snapshot
	for time.Now().Before(deadline) {
		snap = store.Snapshot()
		if snap.Messages >= 1 && snap.Source != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !snap.Connected {
		t.Fatal("expected connected")
	}
	if snap.Source != "ProbeMidiBrain" || snap.SchemaVersion != 1 {
		t.Fatalf("header not ingested: %+v", snap)
	}
	if snap.CapturedAt != "2026-06-05T09:11:00Z" {
		t.Fatalf("capturedAt = %q", snap.CapturedAt)
	}
	if len(snap.Diagnostics) == 0 {
		t.Fatal("expected diagnostics envelope to be stored")
	}
	// The full envelope must survive verbatim, including sections the daemon
	// does not model.
	var got struct {
		AudioUnit struct {
			AudioUnitName string `json:"audioUnitName"`
			ComponentType string `json:"componentType"`
		} `json:"audioUnit"`
		MIDI struct {
			HostMIDIProtocol string `json:"hostMIDIProtocol"`
		} `json:"midi"`
	}
	if err := json.Unmarshal(snap.Diagnostics, &got); err != nil {
		t.Fatalf("stored envelope not valid JSON: %v", err)
	}
	if got.AudioUnit.AudioUnitName != "ProbeMidiBrain" || got.AudioUnit.ComponentType != "aumi" {
		t.Fatalf("audioUnit passthrough lost: %+v", got)
	}
	if got.MIDI.HostMIDIProtocol != "MIDI 2.0" {
		t.Fatalf("midi passthrough lost: %+v", got)
	}
	if connects.Load() != 1 {
		t.Fatalf("connects = %d, want 1", connects.Load())
	}

	_ = c.Close(websocket.StatusNormalClosure, "done")

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if disconnects.Load() == 1 && !store.Snapshot().Connected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if disconnects.Load() != 1 {
		t.Fatalf("disconnects = %d, want 1", disconnects.Load())
	}
	if store.Snapshot().Connected {
		t.Fatal("expected store to report disconnected after close")
	}
}

// TestSetSnapshotRejectsMalformed asserts a non-JSON frame is ignored rather
// than poisoning the store.
func TestSetSnapshotRejectsMalformed(t *testing.T) {
	store := NewStore()
	store.Connect("test")
	if store.SetSnapshot([]byte("not json")) {
		t.Fatal("expected malformed frame to be rejected")
	}
	if snap := store.Snapshot(); snap.Messages != 0 || len(snap.Diagnostics) != 0 {
		t.Fatalf("malformed frame mutated store: %+v", snap)
	}
}
