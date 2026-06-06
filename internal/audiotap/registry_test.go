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

func name(s *Store) string {
	if s == nil {
		return "<nil>"
	}
	return s.Name()
}
