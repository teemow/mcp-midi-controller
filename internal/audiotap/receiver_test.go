package audiotap

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	reg := NewRegistry()
	var connects, disconnects atomic.Int32

	mux := http.NewServeMux()
	Register(mux, reg,
		func(string, string) { connects.Add(1) },
		func(string, string) { disconnects.Add(1) },
	)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/audio-stream?name=probe"

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

	// Resolve the per-tap store the receiver created for ?name=probe.
	tapSnap := func() Snapshot {
		st, ok := reg.Get("probe")
		if !ok {
			return Snapshot{}
		}
		return st.Snapshot()
	}

	// Wait for the server to drain all three messages.
	deadline := time.Now().Add(2 * time.Second)
	var snap Snapshot
	for time.Now().Before(deadline) {
		snap = tapSnap()
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
	if snap.Name != "probe" {
		t.Fatalf("tap name = %q, want %q", snap.Name, "probe")
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
		if disconnects.Load() == 1 && !tapSnap().Connected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if disconnects.Load() != 1 {
		t.Fatalf("disconnects = %d, want 1", disconnects.Load())
	}
	if tapSnap().Connected {
		t.Fatal("expected store to report disconnected after close")
	}
}

// TestReceiverConcurrentNamedTaps dials two taps at once with distinct ?name=
// values and asserts each lands in its own store with its own audio — the
// "multiple named audio taps can connect" guarantee at the WebSocket layer.
func TestReceiverConcurrentNamedTaps(t *testing.T) {
	reg := NewRegistry()
	mux := http.NewServeMux()
	Register(mux, reg, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/audio-stream?name="
	dial := func(name string, sampleRate float64, sample float32) *websocket.Conn {
		c, _, err := websocket.Dial(ctx, base+name, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", name, err)
		}
		fmtMsg := []byte(`{"type":"format","encoding":"f32le","channels":1,"sampleRate":` +
			itoa(sampleRate) + `,"source":"` + name + `"}`)
		if err := c.Write(ctx, websocket.MessageText, fmtMsg); err != nil {
			t.Fatalf("write format %s: %v", name, err)
		}
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, math.Float32bits(sample))
		if err := c.Write(ctx, websocket.MessageBinary, buf); err != nil {
			t.Fatalf("write audio %s: %v", name, err)
		}
		return c
	}

	cA := dial("synth", 48000, 0.5)
	defer cA.Close(websocket.StatusNormalClosure, "done")
	cB := dial("drums", 44100, 0.25)
	defer cB.Close(websocket.StatusNormalClosure, "done")

	// Both taps appear, each with its own format + audio.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(reg.Names()) == 2 {
			a, okA := reg.Get("synth")
			b, okB := reg.Get("drums")
			if okA && okB && a.Snapshot().AudioMessages >= 1 && b.Snapshot().AudioMessages >= 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	a, okA := reg.Get("synth")
	b, okB := reg.Get("drums")
	if !okA || !okB {
		t.Fatalf("expected both taps registered, got names=%v", reg.Names())
	}
	sa, sb := a.Snapshot(), b.Snapshot()
	if !sa.Connected || !sb.Connected {
		t.Fatalf("both taps should be connected: synth=%v drums=%v", sa.Connected, sb.Connected)
	}
	if sa.SampleRate != 48000 || sb.SampleRate != 44100 {
		t.Fatalf("formats crossed over: synth=%.0f drums=%.0f", sa.SampleRate, sb.SampleRate)
	}
	if math.Abs(float64(sa.WindowPeak)-0.5) > 1e-6 || math.Abs(float64(sb.WindowPeak)-0.25) > 1e-6 {
		t.Fatalf("audio crossed over: synth peak=%v drums peak=%v", sa.WindowPeak, sb.WindowPeak)
	}
}

// itoa renders a whole-number float for the JSON format message above.
func itoa(f float64) string {
	return strconv.FormatInt(int64(f), 10)
}
