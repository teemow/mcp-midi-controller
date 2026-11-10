package audiotap

import (
	"slices"
	"sync"
)

// keepDisconnected is how many taps that are not in use the registry keeps
// when a tap under a new name connects: the most recently connected ones.
//
// A tap that dropped is still read. With nothing streaming, Active (and so a
// get_audio_tap without a name) answers with the tap that connected last, and
// a tool asks by name for the last window of a tap it was listed a moment
// ago. Both concern the newest taps, so a few are enough. Each one kept holds
// its Store's window of 4 MiB (windowCapacity), and a tap without a name
// never returns to its Store: it is keyed by its remote address and port, so
// every connection of it is a tap of its own. Four cost 16 MiB.
const keepDisconnected = 4

// Registry holds the set of concurrently-connected audio taps, each keyed by a
// stable name and backed by its own Store (rolling window + levels). Several
// ProbeAudioTap inserts can therefore stream at once — one per AUM channel you
// want to "hear" — without clobbering one another, which the old single Store
// could not do (a second connection overwrote the first).
//
// The name is the tap's identity on the wire: the receiver derives it from the
// /audio-stream "name" (or "tap") query parameter, falling back to the client's
// remote address so even un-named producers still get distinct slots. MCP tools
// address a tap by that name (or by its format Source label), defaulting to the
// most-recently-active tap when none is given.
//
// The registry does not grow with the taps it has seen: it keeps the taps that
// are in use and the keepDisconnected most recently connected of the others,
// and forgets the rest when a tap under a new name connects (forgetLocked).
type Registry struct {
	mu    sync.Mutex
	taps  map[string]*Store
	order []string // connection recency; most-recently connected last
}

// NewRegistry returns an empty tap registry.
func NewRegistry() *Registry {
	return &Registry{taps: map[string]*Store{}}
}

// Connect returns the named tap's Store, creating it on first use (and anew
// once the registry forgot the tap), and marks it connected from remote
// (recording it as the most recent). Re-connecting an existing name starts a
// fresh session on its Store (the window is cleared).
func (r *Registry) Connect(name, remote string) *Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.taps[name]
	if st == nil {
		// Only a tap under a new name makes the registry grow, so only that
		// makes it forget: taps that drop together and dial in again under
		// their names all find their Stores, however many they are.
		r.forgetLocked()
		st = NewStore()
		st.setName(name)
		r.taps[name] = st
	}
	r.touchLocked(name)
	st.Connect(remote)
	return st
}

// Disconnect marks the named tap gone (its last window/levels are kept so a
// poll right after a drop still reports the final state). Unknown names are a
// no-op.
func (r *Registry) Disconnect(name string) {
	r.mu.Lock()
	st := r.taps[name]
	r.mu.Unlock()
	if st != nil {
		st.Disconnect()
	}
}

// Adopt registers an already-built Store under name and marks it the most
// recent. It is the entry point for callers (and tests) that construct a tap
// Store directly rather than through a live Connect.
func (r *Registry) Adopt(name string, st *Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st.setName(name)
	r.taps[name] = st
	r.touchLocked(name)
	// The adopted tap is the most recent one, so it stays whether or not it
	// is connected.
	r.forgetLocked()
}

// Get returns the Store for a tap addressed by name: first by its registry key,
// then (as a convenience for agents) by its format Source label. ok is false
// when nothing matches.
func (r *Registry) Get(name string) (*Store, bool) {
	r.mu.Lock()
	if st, ok := r.taps[name]; ok {
		r.mu.Unlock()
		return st, true
	}
	stores := r.snapshotStoresLocked()
	r.mu.Unlock()
	for _, st := range stores {
		if st.Source() == name {
			return st, true
		}
	}
	return nil, false
}

// Active returns the most useful default tap: the most-recently-connected tap
// that is still streaming, or — if none is streaming — the most-recently-seen
// tap overall. ok is false when no tap has ever connected.
func (r *Registry) Active() (*Store, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.order) - 1; i >= 0; i-- {
		if st := r.taps[r.order[i]]; st != nil && st.Connected() {
			return st, true
		}
	}
	if n := len(r.order); n > 0 {
		return r.taps[r.order[n-1]], true
	}
	return nil, false
}

// Names returns the known tap names, most-recently connected last.
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Snapshots returns a Snapshot for every known tap, keyed by name. Each
// Snapshot's analysis runs after the registry lock is released.
func (r *Registry) Snapshots() map[string]Snapshot {
	r.mu.Lock()
	stores := make(map[string]*Store, len(r.taps))
	for n, st := range r.taps {
		stores[n] = st
	}
	r.mu.Unlock()
	out := make(map[string]Snapshot, len(stores))
	for n, st := range stores {
		out[n] = st.Snapshot()
	}
	return out
}

// touchLocked moves name to the end of the recency order (most recent). Must
// hold r.mu.
func (r *Registry) touchLocked(name string) {
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.order = append(r.order, name)
}

// forgetLocked drops the taps that are not in use from the registry, all but
// the keepDisconnected most recently connected of them. Must hold r.mu.
//
// A dropped Store is only out of the registry's reach. Whoever holds it (a
// meter in the middle of a measurement) reads it as before, and a tap that
// dials in again under its name gets a new Store, not the dropped one.
func (r *Registry) forgetLocked() {
	gone := 0
	for i := len(r.order) - 1; i >= 0; i-- {
		name := r.order[i]
		if st := r.taps[name]; st != nil && st.inUse() {
			continue
		}
		if gone++; gone > keepDisconnected {
			delete(r.taps, name)
			r.order = slices.Delete(r.order, i, i+1)
		}
	}
}

// inUse reports whether the registry has to keep the tap: it is streaming, or
// a recording of it runs. A recording goes on when its tap dials in again
// (Capture), which the tap can only do while its name leads to this Store.
func (s *Store) inUse() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected || len(s.captures) > 0
}

// snapshotStoresLocked returns the current Store set as a slice. Must hold r.mu.
func (r *Registry) snapshotStoresLocked() []*Store {
	out := make([]*Store, 0, len(r.taps))
	for _, st := range r.taps {
		out = append(out, st)
	}
	return out
}
