// Package busy keeps track of what the daemon is in the middle of: a song
// playing, a recording at a tap, a stem recording, a mix measurement, a tool
// call that has not returned. A restart cuts all of them, so whoever restarts
// the daemon asks first: GET /busy on the loopback listener answers "idle", or
// "busy" and one line for each of them. The macOS self-update
// (scripts/deploy-mac.sh --update) holds a restart for as long as the answer is
// busy, up to an hour.
package busy

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Activity is one thing the daemon is in the middle of.
type Activity struct {
	// What names it, e.g. `play_song "battalion"`.
	What string
	// Since is when it began.
	Since time.Time
}

// Tracker holds the activities under way. The zero value is not usable; New
// makes one.
type Tracker struct {
	now func() time.Time

	mu     sync.Mutex
	next   uint64
	active map[uint64]Activity
}

// New returns a Tracker with nothing under way.
func New() *Tracker {
	return &Tracker{now: time.Now, active: map[uint64]Activity{}}
}

// Begin records that what has begun, until the end it returns is called.
// Calling end more than once ends it once.
func (t *Tracker) Begin(what string) (end func()) {
	t.mu.Lock()
	t.next++
	id := t.next
	t.active[id] = Activity{What: what, Since: t.now()}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.active, id)
			t.mu.Unlock()
		})
	}
}

// Active returns what is under way, the oldest first.
func (t *Tracker) Active() []Activity {
	t.mu.Lock()
	ids := make([]uint64, 0, len(t.active))
	for id := range t.active {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Activity, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.active[id])
	}
	t.mu.Unlock()
	return out
}

// Report renders what is under way as GET /busy answers it: "idle", or "busy"
// and one line for each activity, the oldest first, with how long it has run.
func (t *Tracker) Report() string {
	active := t.Active()
	if len(active) == 0 {
		return "idle\n"
	}
	now := t.now()
	var b strings.Builder
	b.WriteString("busy\n")
	for _, a := range active {
		fmt.Fprintf(&b, "%s, for %s (since %s)\n", a.What, now.Sub(a.Since).Round(time.Second), a.Since.Format("15:04:05"))
	}
	return b.String()
}

// Handler serves Report as text/plain. The answer is never cached: it is the
// state of this moment.
func (t *Tracker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(t.Report()))
	})
}
