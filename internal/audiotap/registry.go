package audiotap

import "sync"

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
type Registry struct {
	mu    sync.Mutex
	taps  map[string]*Store
	order []string // connection recency; most-recently connected last
}

// NewRegistry returns an empty tap registry.
func NewRegistry() *Registry {
	return &Registry{taps: map[string]*Store{}}
}

// Connect returns the named tap's Store, creating it on first use, and marks it
// connected from remote (recording it as the most recent). Re-connecting an
// existing name starts a fresh session on its Store (the window is cleared).
func (r *Registry) Connect(name, remote string) *Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.taps[name]
	if st == nil {
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

// snapshotStoresLocked returns the current Store set as a slice. Must hold r.mu.
func (r *Registry) snapshotStoresLocked() []*Store {
	out := make([]*Store, 0, len(r.taps))
	for _, st := range r.taps {
		out = append(out, st)
	}
	return out
}
