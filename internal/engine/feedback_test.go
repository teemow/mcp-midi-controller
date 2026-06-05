package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/device"
	"github.com/teemow/mcp-midi-controller/internal/transport"
)

// fakeTransport is an in-memory transport for the feedback tests. It records
// sent events and, when echo is set, mirrors each sent MIDI message back onto
// its inbound stream (optionally transformed) so verify/probe paths can be
// exercised without hardware.
type fakeTransport struct {
	mu      sync.Mutex
	sent    []transport.Event
	inbound chan transport.Event
	echo    func([]byte) []byte // nil = no echo; returns echo bytes or nil
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{inbound: make(chan transport.Event, 64)}
}

func (f *fakeTransport) ID() string { return "fake" }

func (f *fakeTransport) Discover(context.Context) ([]transport.Endpoint, error) { return nil, nil }
func (f *fakeTransport) Pair(context.Context, string) error                     { return nil }
func (f *fakeTransport) Connect(context.Context, string) error                  { return nil }
func (f *fakeTransport) Disconnect(context.Context, string) error               { return nil }

func (f *fakeTransport) Send(_ context.Context, _ string, ev transport.Event) error {
	f.mu.Lock()
	f.sent = append(f.sent, ev)
	echo := f.echo
	f.mu.Unlock()
	if echo != nil {
		if out := echo(ev.Data); out != nil {
			f.inbound <- transport.Event{Kind: transport.MIDIEvent, Data: out}
		}
	}
	return nil
}

func (f *fakeTransport) Listen(ctx context.Context, _ string) (<-chan transport.Event, error) {
	out := make(chan transport.Event, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-f.inbound:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

const testDeviceYAML = `id: testdev
name: Test Device
transport: fake
controls:
  - name: level
    type: cc
    cc: 17
    value: { type: range, min: 0, max: 127 }
  - name: mode
    type: cc
    cc: 28
    value: { type: enum, values: { off: 0, on: 127 } }
  - name: preset
    type: program_change
    value: { type: range, min: 0, max: 127 }
`

// newTestEngine builds an engine with the test definition loaded, the fake
// transport registered, and a logical device "amp" bound on channel 4.
func newTestEngine(t *testing.T) (*Engine, *fakeTransport) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "testdev.yaml"), []byte(testDeviceYAML), 0o644); err != nil {
		t.Fatalf("write def: %v", err)
	}
	reg := device.NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("load def: %v", err)
	}
	ft := newFakeTransport()
	eng := New(reg, ft)
	if err := eng.Bind(Device{Name: "amp", DeviceID: "testdev", Endpoint: "EP1", Channel: 4}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return eng, ft
}

func TestDecodeInbound(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		kind string
		ch   int
		num  int
		val  int
	}{
		{"cc", []byte{0xB4, 17, 100}, "cc", 4, 17, 100},
		{"program_change", []byte{0xC1, 5}, "program_change", 1, 0, 5},
		{"note_on", []byte{0x92, 60, 90}, "note_on", 2, 60, 90},
		{"note_off", []byte{0x83, 60, 0}, "note_off", 3, 60, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: c.data})
			if in.Kind != c.kind || in.Channel != c.ch || in.Value != c.val {
				t.Fatalf("decode = %+v", in)
			}
			if c.kind != "program_change" && in.Number != c.num {
				t.Fatalf("number = %d, want %d", in.Number, c.num)
			}
		})
	}
}

func TestDecodeInboundSysExUndecoded(t *testing.T) {
	in := decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xF0, 0x7D, 0xF7}})
	if in.Kind != "" {
		t.Fatalf("sysex should not decode to a channel-voice kind, got %q", in.Kind)
	}
}

func TestReverseMapCCAndEnumAndPC(t *testing.T) {
	eng, _ := newTestEngine(t)

	// CC 17 on channel 4 -> amp.level = 100 (range, stays an int).
	obs := eng.reverseMap(decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 17, 100}}))
	if len(obs) != 1 || obs[0].Device != "amp" || obs[0].Control != "level" || obs[0].Value != 100 {
		t.Fatalf("level obs = %+v", obs)
	}

	// CC 28 value 127 -> amp.mode = "on" (enum reverse-mapped to its label).
	obs = eng.reverseMap(decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 28, 127}}))
	if len(obs) != 1 || obs[0].Control != "mode" || obs[0].Value != "on" {
		t.Fatalf("mode obs = %+v", obs)
	}

	// Program change -> amp.preset.
	obs = eng.reverseMap(decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xC4, 7}}))
	if len(obs) != 1 || obs[0].Control != "preset" || obs[0].Value != 7 {
		t.Fatalf("preset obs = %+v", obs)
	}
}

func TestReverseMapWrongChannelOrEndpoint(t *testing.T) {
	eng, _ := newTestEngine(t)
	// Wrong channel (5, binding is on 4) -> no match.
	if obs := eng.reverseMap(decodeInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB5, 17, 100}})); len(obs) != 0 {
		t.Fatalf("wrong-channel obs = %+v", obs)
	}
	// Wrong endpoint -> no match.
	if obs := eng.reverseMap(decodeInbound("fake", "OTHER", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 17, 100}})); len(obs) != 0 {
		t.Fatalf("wrong-endpoint obs = %+v", obs)
	}
}

func TestHandleInboundUpdatesObserved(t *testing.T) {
	eng, _ := newTestEngine(t)
	eng.handleInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 17, 64}})
	got := eng.Observed().Device("amp")
	o, ok := got["level"]
	if !ok || o.Wire != 64 || o.Value != 64 || o.Source != "EP1" {
		t.Fatalf("observed = %+v", got)
	}
}

func TestVerifyControlConfirmed(t *testing.T) {
	eng, ft := newTestEngine(t)
	ft.echo = func(b []byte) []byte { return b } // perfect echo
	res, err := eng.VerifyControl(context.Background(), "amp", "level", 100, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusConfirmed || res.Observed != 100 || res.Source != "EP1" {
		t.Fatalf("res = %+v", res)
	}
}

func TestVerifyControlNoFeedback(t *testing.T) {
	eng, _ := newTestEngine(t) // no echo configured
	res, err := eng.VerifyControl(context.Background(), "amp", "level", 100, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusNoFeedback {
		t.Fatalf("res = %+v, want no_feedback", res)
	}
}

func TestVerifyControlMismatch(t *testing.T) {
	eng, ft := newTestEngine(t)
	ft.echo = func(b []byte) []byte {
		out := append([]byte(nil), b...)
		out[2] = 42 // echo a different value than the 100 we set
		return out
	}
	res, err := eng.VerifyControl(context.Background(), "amp", "level", 100, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Status != StatusMismatch || res.Observed != 42 || res.Expected != 100 {
		t.Fatalf("res = %+v, want mismatch", res)
	}
}

func TestLearnStartCapture(t *testing.T) {
	eng, ft := newTestEngine(t)
	if err := eng.LearnStart(context.Background(), "fake", "EP1"); err != nil {
		t.Fatalf("learn_start: %v", err)
	}
	ft.inbound <- transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 74, 55}}

	var captured LearnedControl
	ok := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if captured, ok = eng.LearnCapture(); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok || captured.Type != "cc" || captured.Number != 74 || captured.Value != 55 || captured.Channel != 4 {
		t.Fatalf("captured = %+v ok=%v", captured, ok)
	}
}

func TestLearnCaptureIgnoresStale(t *testing.T) {
	eng, ft := newTestEngine(t)
	// An event that predates learn_start must not be captured.
	eng.handleInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 10, 1}})
	time.Sleep(5 * time.Millisecond)
	if err := eng.LearnStart(context.Background(), "fake", "EP1"); err != nil {
		t.Fatalf("learn_start: %v", err)
	}
	_ = ft
	if c, ok := eng.LearnCapture(); ok {
		t.Fatalf("expected nothing captured, got %+v", c)
	}
}

func TestProbeFeedback(t *testing.T) {
	eng, ft := newTestEngine(t)
	ft.echo = func(b []byte) []byte { return b }
	results, err := eng.ProbeFeedback(context.Background(), "amp", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	byControl := map[string]ProbeResult{}
	for _, r := range results {
		byControl[r.Control] = r
	}
	for _, name := range []string{"level", "mode", "preset"} {
		r, ok := byControl[name]
		if !ok {
			t.Fatalf("missing result for %q", name)
		}
		if r.Status != StatusConfirmed || len(r.Sources) != 1 || r.Sources[0] != "EP1" {
			t.Fatalf("%s result = %+v", name, r)
		}
	}
}

func TestReverseMapOSCFloatAndInt(t *testing.T) {
	// Reuses the OSC device/transport fixtures from footswitch_test.go: logical
	// "x32" (def x32mini) bound on the "osc" transport at this endpoint.
	const ep = "10.0.0.1:10023"
	eng := newOSCFootswitchTestEngine(t, ep)

	// A mirrored fader value reverse-maps to x32.ch01_fader, preserving the float.
	in := decodeInbound("osc", ep,
		transport.Event{Kind: transport.OSCEvent, OSCAddr: "/ch/01/mix/fader", OSCArgs: []any{float32(0.42)}})
	if in.Kind != "osc" {
		t.Fatalf("kind = %q, want osc", in.Kind)
	}
	obs := eng.reverseMap(in)
	if len(obs) != 1 || obs[0].Device != "x32" || obs[0].Control != "ch01_fader" {
		t.Fatalf("fader obs = %+v", obs)
	}
	if f, ok := obs[0].Value.(float32); !ok || f != 0.42 {
		t.Fatalf("fader value = %#v, want float32 0.42", obs[0].Value)
	}

	// A goscene echo reverse-maps to x32.scene_recall.
	obs = eng.reverseMap(decodeInbound("osc", ep,
		transport.Event{Kind: transport.OSCEvent, OSCAddr: "/-action/goscene", OSCArgs: []any{int32(7)}}))
	if len(obs) != 1 || obs[0].Control != "scene_recall" || obs[0].Wire != 7 {
		t.Fatalf("scene obs = %+v", obs)
	}
}

func TestReverseMapOSCUnknownAddressOrEndpoint(t *testing.T) {
	const ep = "10.0.0.1:10023"
	eng := newOSCFootswitchTestEngine(t, ep)
	// Address that no control models -> no observation.
	if obs := eng.reverseMap(decodeInbound("osc", ep,
		transport.Event{Kind: transport.OSCEvent, OSCAddr: "/ch/99/mix/fader", OSCArgs: []any{float32(0.1)}})); len(obs) != 0 {
		t.Fatalf("unknown-address obs = %+v", obs)
	}
	// Right address, wrong endpoint -> no observation.
	if obs := eng.reverseMap(decodeInbound("osc", "other:10023",
		transport.Event{Kind: transport.OSCEvent, OSCAddr: "/ch/01/mix/fader", OSCArgs: []any{float32(0.1)}})); len(obs) != 0 {
		t.Fatalf("wrong-endpoint obs = %+v", obs)
	}
}

func TestInboundHookFires(t *testing.T) {
	eng, _ := newTestEngine(t)
	var (
		mu   sync.Mutex
		hits []Observation
	)
	eng.SetInboundHook(func(_ InboundEvent, obs []Observation) {
		mu.Lock()
		hits = append(hits, obs...)
		mu.Unlock()
	})
	eng.handleInbound("fake", "EP1", transport.Event{Kind: transport.MIDIEvent, Data: []byte{0xB4, 17, 9}})
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || hits[0].Control != "level" || hits[0].Wire != 9 {
		t.Fatalf("hook hits = %+v", hits)
	}
}
