package audiotap

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

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

// tapAddr is the name of a tap that sends none: the address and port its i-th
// connection comes from.
func tapAddr(i int) string {
	return fmt.Sprintf("192.0.2.10:%d", 50000+i)
}

// cycle connects a tap without a name and drops it again, n times, the i-th
// connection being tapAddr(from+i).
func cycle(r *Registry, from, n int) {
	for i := from; i < from+n; i++ {
		r.Connect(tapAddr(i), tapAddr(i))
		r.Disconnect(tapAddr(i))
	}
}

// TestRegistryForgetsOldDisconnectedTaps is the bound on the registry's
// memory: a tap without a name is a new tap with every connection, and many
// such connections leave the connected taps and the few most recently
// connected of the others, not a Store each.
func TestRegistryForgetsOldDisconnectedTaps(t *testing.T) {
	r := NewRegistry()
	const cycles = 100
	// Two taps stream all along: one from before the first cycle, one that
	// connects half way through.
	first := r.Connect("first", "192.0.2.1:40000")
	var second *Store
	most := 0
	for i := 0; i < cycles; i++ {
		if i == cycles/2 {
			second = r.Connect("second", "192.0.2.2:40000")
		}
		st := r.Connect(tapAddr(i), tapAddr(i))
		st.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000, Source: fmt.Sprintf("strip %d", i)})
		st.AppendAudio(make([]float32, i+1)) // the length of its window tells a Store apart
		r.Disconnect(tapAddr(i))
		most = max(most, len(r.Names()))
	}
	// The connected ones, the ones kept at the last connection, and the tap
	// of that connection, which has dropped since.
	if bound := 2 + keepDisconnected + 1; most > bound {
		t.Fatalf("the registry held %d taps at a time, want at most %d", most, bound)
	}
	if len(r.taps) != len(r.order) {
		t.Fatalf("%d stores for %d names", len(r.taps), len(r.order))
	}

	// What is left: both connected taps and the newest of the others, in the
	// order they connected in.
	want := []string{"first", "second"}
	for i := cycles - 1 - keepDisconnected; i < cycles; i++ {
		want = append(want, tapAddr(i))
	}
	names := r.Names()
	if !slices.Equal(names, want) {
		t.Fatalf("Names = %q, want %q", names, want)
	}
	snaps := r.Snapshots()
	if len(snaps) != len(want) {
		t.Fatalf("Snapshots holds %d taps, want the %d of Names", len(snaps), len(want))
	}
	for _, n := range want {
		st, ok := r.Get(n)
		if !ok || st.Name() != n {
			t.Fatalf("Get(%q) = %q, %v; want the tap Names lists", n, name(st), ok)
		}
		snap, ok := snaps[n]
		if !ok || snap.Name != n {
			t.Fatalf("Snapshots has no tap %q", n)
		}
		if connected := n == "first" || n == "second"; snap.Connected != connected || st.Connected() != connected {
			t.Errorf("tap %q: connected = %v, want %v", n, snap.Connected, connected)
		}
	}
	// A kept tap that dropped still has its last window, and answers to its
	// source label as well.
	newest := cycles - 1
	st, ok := r.Get(fmt.Sprintf("strip %d", newest))
	if !ok || st.Name() != tapAddr(newest) || st.Snapshot().WindowSamples != newest+1 {
		t.Fatalf("the newest dropped tap by its source = %q, %v; want its Store with its last window", name(st), ok)
	}
	// A forgotten one answers to neither.
	if st, ok := r.Get(tapAddr(0)); ok {
		t.Errorf("Get of a forgotten tap's name found %q", name(st))
	}
	if st, ok := r.Get("strip 0"); ok {
		t.Errorf("Get of a forgotten tap's source found %q", name(st))
	}

	// The default tap is the one streaming that connected last. With none
	// streaming it is the tap connected last, which was kept.
	if st, ok := r.Active(); !ok || st != second {
		t.Fatalf("Active = %q, want the connected tap %q", name(st), "second")
	}
	r.Disconnect("first")
	r.Disconnect("second")
	if st, ok := r.Active(); !ok || st.Name() != tapAddr(newest) {
		t.Fatalf("Active with no tap streaming = %q, want the newest %q", name(st), tapAddr(newest))
	}

	// Nothing is forgotten before a new tap connects. Then the two that
	// streamed for so long are the oldest of the dropped ones, and go.
	if got := r.Names(); !slices.Equal(got, want) {
		t.Fatalf("Names after the drops = %q, want %q still", got, want)
	}
	next := r.Connect("next", "192.0.2.3:40000")
	want = nil
	for i := cycles - keepDisconnected; i < cycles; i++ {
		want = append(want, tapAddr(i))
	}
	want = append(want, "next")
	if got := r.Names(); !slices.Equal(got, want) {
		t.Fatalf("Names after a new tap = %q, want %q", got, want)
	}
	if st, ok := r.Active(); !ok || st != next {
		t.Fatalf("Active = %q, want the new tap", name(st))
	}
	if _, ok := r.Get("first"); ok || len(r.Snapshots()) != len(want) {
		t.Fatalf("a forgotten tap is still there: Get(first) %v, %d snapshots for %d names", ok, len(r.Snapshots()), len(want))
	}
	if first.Connected() || first.Name() != "first" {
		t.Errorf("the forgotten Store changed: %q, connected %v", first.Name(), first.Connected())
	}
}

// TestRegistryForgottenStoreStaysUsable is a tool that holds a tap's Store
// across the tap's drop: the Store reads as before once the registry forgot
// it, and the tap dialling in again writes to a Store of its own.
func TestRegistryForgottenStoreStaysUsable(t *testing.T) {
	r := NewRegistry()
	held := r.Connect("strip", "192.0.2.1:40000")
	held.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000, Source: "Strip"})
	held.AppendAudio([]float32{0.5, 0.5, 0.5})
	end := held.MarkEpoch()
	r.Disconnect("strip")

	// It is kept for as long as it is among the most recent ones.
	cycle(r, 0, keepDisconnected)
	if st, ok := r.Get("strip"); !ok || st != held {
		t.Fatalf("Get(strip) = %q, %v after %d newer taps; want it kept", name(st), ok, keepDisconnected)
	}
	cycle(r, keepDisconnected, 1)
	if st, ok := r.Get("strip"); ok {
		t.Fatalf("Get(strip) = %q after %d newer taps; want it forgotten", name(st), keepDisconnected+1)
	}
	if st, ok := r.Get("Strip"); ok {
		t.Fatalf("Get by the forgotten tap's source found %q", name(st))
	}

	if clip, ok := held.Segment(0, end); !ok || len(clip.Samples) != 3 {
		t.Fatalf("the held Store's segment = %d sample(s), %v; want its 3", len(clip.Samples), ok)
	}

	again := r.Connect("strip", "192.0.2.1:40001")
	if again == held {
		t.Fatal("the tap dialled in again into the Store the registry had forgotten")
	}
	again.SetFormat(Format{Encoding: "f32le", Channels: 2, SampleRate: 44100})
	again.AppendAudio([]float32{0.1, 0.1})
	if snap := held.Snapshot(); snap.Connected || snap.WindowSamples != 3 || snap.SampleRate != 48000 || snap.WindowPeak != 0.5 {
		t.Fatalf("the new connection reached the held Store: %+v", snap)
	}
	if st, ok := r.Get("strip"); !ok || st != again || !st.Connected() {
		t.Fatalf("Get(strip) = %q, %v; want the new connection's Store", name(st), ok)
	}
	// The held Store is still one to write to and read from.
	held.AppendAudio([]float32{0.5})
	if got := held.Snapshot().WindowSamples; got != 4 {
		t.Errorf("the held Store's window = %d sample(s), want 4", got)
	}
	if got := again.Snapshot().WindowSamples; got != 1 {
		t.Errorf("the new Store's window = %d frame(s), want its own 1", got)
	}
}

// TestRegistryKnownNamesFindTheirStores is a set of taps that drop together
// and dial in again under their names (the host slept, the network hiccuped):
// no new name connects, so every one of them comes back to its Store, however
// many they are.
func TestRegistryKnownNamesFindTheirStores(t *testing.T) {
	r := NewRegistry()
	const taps = 3 * keepDisconnected
	stores := make([]*Store, taps)
	for i := range stores {
		stores[i] = r.Connect(fmt.Sprintf("session/strip-%d", i), tapAddr(i))
	}
	for i := range stores {
		r.Disconnect(fmt.Sprintf("session/strip-%d", i))
	}
	for i, want := range stores {
		if got := r.Connect(fmt.Sprintf("session/strip-%d", i), tapAddr(taps+i)); got != want {
			t.Fatalf("strip %d dialled in again into another Store", i)
		}
	}
	if got := len(r.Names()); got != taps {
		t.Fatalf("%d taps, want the %d connected ones", got, taps)
	}
}

// TestRegistryKeepsARecordedTap is a recording that outlasts its tap's
// connection: the tap stays in the registry however many others come and go,
// so that it dials in again into the Store the recording reads from. Once the
// recording is stopped, the tap is forgotten like any other.
func TestRegistryKeepsARecordedTap(t *testing.T) {
	r := NewRegistry()
	format := Format{Encoding: "f32le", Channels: 1, SampleRate: 48000}
	tap := r.Connect("master", "192.0.2.1:40000")
	tap.SetFormat(format)
	c, err := tap.StartCapture(filepath.Join(t.TempDir(), "take.f32"), 0.5, 0)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	defer func() { _, _ = c.Stop() }()
	tap.AppendAudio([]float32{0.9, 0.9})
	r.Disconnect("master")

	cycle(r, 0, 3*keepDisconnected)
	if st, ok := r.Get("master"); !ok || st != tap {
		t.Fatalf("Get(master) = %q, %v; want the recorded tap kept", name(st), ok)
	}
	if again := r.Connect("master", "192.0.2.1:40001"); again != tap {
		t.Fatal("the recorded tap dialled in again into another Store")
	}
	tap.SetFormat(format)
	tap.AppendAudio([]float32{0.9})
	if st := c.State(); st.Frames != 3 || len(st.Stalls) != 1 || !st.Stalls[0].Reconnect {
		t.Fatalf("the recording = %+v, want 3 frames across one reconnect", st)
	}

	if _, err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	r.Disconnect("master")
	cycle(r, 3*keepDisconnected, keepDisconnected+1)
	if st, ok := r.Get("master"); ok {
		t.Fatalf("Get(master) = %q after the recording; want it forgotten", name(st))
	}
}

// TestRegistryAdoptForgetsToo checks the other way into the registry: an
// adopted Store counts like a tap that connected, and stays as the most
// recent one although it never streamed.
func TestRegistryAdoptForgetsToo(t *testing.T) {
	r := NewRegistry()
	live := r.Connect("live", "192.0.2.1:40000")
	var want []string
	for i := 0; i < 3*keepDisconnected; i++ {
		r.Adopt(tapAddr(i), NewStore())
		if i >= 2*keepDisconnected {
			want = append(want, tapAddr(i))
		}
	}
	if got := r.Names(); !slices.Equal(got, append([]string{"live"}, want...)) {
		t.Fatalf("Names = %q, want the connected tap and the newest adopted %q", got, want)
	}
	if st, ok := r.Active(); !ok || st != live {
		t.Fatalf("Active = %q, want the connected tap", name(st))
	}
}

// TestRegistryConcurrentCycles lets taps come and go on several connections
// at once while the tools' reads run, for the race detector, and checks the
// bound: what was connected at the last new connection, and what was kept
// then.
func TestRegistryConcurrentCycles(t *testing.T) {
	r := NewRegistry()
	const workers, cycles = 4, 20
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < cycles; i++ {
				addr := fmt.Sprintf("192.0.2.%d:%d", 20+w, 50000+i)
				st := r.Connect(addr, addr)
				st.SetFormat(Format{Encoding: "f32le", Channels: 1, SampleRate: 48000, Source: addr})
				st.AppendAudio([]float32{0.25})
				if got, ok := r.Get(addr); !ok || got != st {
					t.Errorf("Get(%q) while it is connected = %q, %v", addr, name(got), ok)
				}
				if _, ok := r.Active(); !ok {
					t.Error("no active tap while one is connected")
				}
				if _, ok := r.Snapshots()[addr]; !ok {
					t.Errorf("Snapshots misses the connected tap %q", addr)
				}
				_ = r.Names()
				r.Disconnect(addr)
			}
		}()
	}
	wg.Wait()
	if got, bound := len(r.Names()), workers+keepDisconnected; got > bound {
		t.Fatalf("%d taps left, want at most %d", got, bound)
	}
	if len(r.taps) != len(r.order) || len(r.Snapshots()) != len(r.order) {
		t.Fatalf("%d stores and %d snapshots for %d names", len(r.taps), len(r.Snapshots()), len(r.order))
	}
}
