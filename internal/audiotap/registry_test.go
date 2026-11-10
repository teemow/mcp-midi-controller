package audiotap

import "testing"

// TestRegistryKeepsNamedTapsSeparate is the core multi-tap guarantee: two named
// taps connect concurrently, each keeps its own window/levels, and neither
// clobbers the other (the old single Store could not do this).
func TestRegistryKeepsNamedTapsSeparate(t *testing.T) {
	r := NewRegistry()

	synth := r.Connect("synth", "10.0.0.1:5000")
	synth.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000, Source: "Synth"})
	synth.AppendAudio([]float32{0.5, 0.5, 0.5, 0.5})

	drums := r.Connect("drums", "10.0.0.2:5000")
	drums.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 44100, Source: "Drums"})
	drums.AppendAudio([]float32{0.1})

	// The first tap's window survived the second connecting.
	if got := synth.Snapshot().WindowSamples; got != 4 {
		t.Fatalf("synth window = %d, want 4 (clobbered by drums?)", got)
	}
	if got := drums.Snapshot().WindowSamples; got != 1 {
		t.Fatalf("drums window = %d, want 1", got)
	}

	// Both are addressable by name and report their own identity.
	if st, ok := r.Get("synth"); !ok || st.Snapshot().Name != "synth" {
		t.Fatalf("Get(synth) failed: ok=%v", ok)
	}
	if st, ok := r.Get("drums"); !ok || st.Snapshot().SampleRate != 44100 {
		t.Fatalf("Get(drums) wrong store")
	}

	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("Names = %v, want 2 entries", names)
	}
}

// TestRegistryGetBySource resolves a tap by its producer-supplied format source
// label, not just its registry key — the convenience MCP tools rely on.
func TestRegistryGetBySource(t *testing.T) {
	r := NewRegistry()
	st := r.Connect("10.0.0.9:6000", "10.0.0.9:6000") // un-named producer (keyed by remote)
	st.SetFormat(Format{Source: "Lead"})

	got, ok := r.Get("Lead")
	if !ok || got != st {
		t.Fatalf("Get by source %q failed: ok=%v", "Lead", ok)
	}
}

// TestRegistryActivePrefersConnected returns the most-recently-connected tap
// that is still streaming, falling back to the newest seen otherwise.
func TestRegistryActivePrefersConnected(t *testing.T) {
	r := NewRegistry()
	a := r.Connect("a", "x")
	r.Connect("b", "y")

	// b is the most recent → Active.
	if st, ok := r.Active(); !ok || st.Name() != "b" {
		t.Fatalf("Active = %q, want b", name(st))
	}

	// Drop b: a is the only connected tap → Active.
	r.Disconnect("b")
	if st, ok := r.Active(); !ok || st != a {
		t.Fatalf("Active after b dropped = %q, want a", name(st))
	}

	// Empty registry has no active tap.
	if _, ok := NewRegistry().Active(); ok {
		t.Fatal("empty registry should have no active tap")
	}
}

// TestRegistryNewerConnectionIsTheTap checks a tap that dials again under its
// name: the newer connection takes the tap over, what the older one still
// sends is dropped, and the older one's end leaves the tap connected.
func TestRegistryNewerConnectionIsTheTap(t *testing.T) {
	r := NewRegistry()
	old := r.Open("Songs/A/master", "10.0.0.1:5000")
	old.SetFormat(Format{Channels: 1, SampleRate: 48000, Source: "Songs/A/master"})
	old.AppendAudio([]float32{0.5, 0.5})

	cur := r.Open("Songs/A/master", "10.0.0.1:5001")
	if cur.Store() != old.Store() {
		t.Fatal("the same name got a second store")
	}
	cur.SetFormat(Format{Channels: 2, SampleRate: 44100, Source: "Songs/A/master"})
	cur.AppendAudio([]float32{0.1, 0.1})

	// The older connection is still open for a moment.
	old.AppendAudio([]float32{0.9, 0.9, 0.9})
	old.SetFormat(Format{Channels: 1, SampleRate: 48000})
	old.SetFeatures(0.9, 0.9)
	st := cur.Store()
	snap := st.Snapshot()
	if snap.WindowSamples != 1 || snap.SampleRate != 44100 || snap.RMS != 0 || snap.WindowPeak > 0.2 {
		t.Fatalf("the replaced connection still reached the tap: %+v", snap)
	}
	if old.Close() {
		t.Error("the end of the replaced connection marked the tap as gone")
	}
	if !st.Connected() {
		t.Fatal("the tap is not connected although its newer connection is open")
	}
	if !cur.Close() || st.Connected() {
		t.Errorf("the end of the present connection left the tap connected (%v)", st.Connected())
	}
}

// TestRegistryConnected lists the streaming taps, the most recent first.
func TestRegistryConnected(t *testing.T) {
	r := NewRegistry()
	if got := r.Connected(); len(got) != 0 {
		t.Fatalf("an empty registry lists %d connected tap(s)", len(got))
	}
	r.Connect("a", "x")
	r.Connect("b", "y")
	r.Connect("c", "z")
	r.Disconnect("b")
	var got []string
	for _, st := range r.Connected() {
		got = append(got, st.Name())
	}
	if len(got) != 2 || got[0] != "c" || got[1] != "a" {
		t.Fatalf("connected = %v, want c then a", got)
	}
}

func name(s *Store) string {
	if s == nil {
		return "<nil>"
	}
	return s.Name()
}
