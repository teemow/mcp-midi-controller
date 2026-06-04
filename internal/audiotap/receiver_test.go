package audiotap

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestReceiverEndToEnd dials the receiver as a real WebSocket client, sends the
// full ProbeAudioTap contract (format + binary PCM + features), and asserts the
// store and the connect/disconnect callbacks reflect it.
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/audio-stream"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// 1) format
	if err := c.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"format","encoding":"f32le","channels":1,"sampleRate":11025,"source":"ProbeAudioTap"}`)); err != nil {
		t.Fatalf("write format: %v", err)
	}
	// 2) audio (binary LE float32)
	samples := []float32{0.5, -0.5, 0.25, -0.25}
	buf := make([]byte, len(samples)*4)
	for i, v := range samples {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	if err := c.Write(ctx, websocket.MessageBinary, buf); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	// 3) features
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"features","rms":0.123,"peak":0.456}`)); err != nil {
		t.Fatalf("write features: %v", err)
	}

	// Wait for the server to drain all three messages.
	deadline := time.Now().Add(2 * time.Second)
	var snap Snapshot
	for time.Now().Before(deadline) {
		snap = store.Snapshot()
		if snap.AudioMessages >= 1 && snap.FeatureMessages >= 1 && snap.Source != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !snap.Connected {
		t.Fatal("expected connected")
	}
	if snap.Source != "ProbeAudioTap" || snap.SampleRate != 11025 {
		t.Fatalf("format not ingested: %+v", snap)
	}
	if snap.WindowSamples != len(samples) {
		t.Fatalf("WindowSamples = %d, want %d", snap.WindowSamples, len(samples))
	}
	if math.Abs(float64(snap.WindowPeak)-0.5) > 1e-6 {
		t.Fatalf("WindowPeak = %v, want 0.5", snap.WindowPeak)
	}
	if snap.RMS != 0.123 || snap.Peak != 0.456 {
		t.Fatalf("features not ingested: rms=%v peak=%v", snap.RMS, snap.Peak)
	}
	if connects.Load() != 1 {
		t.Fatalf("connects = %d, want 1", connects.Load())
	}

	_ = c.Close(websocket.StatusNormalClosure, "done")

	// The disconnect callback fires from the server read loop after the close.
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
