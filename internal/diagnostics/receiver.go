package diagnostics

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// maxMessageBytes bounds a single inbound WebSocket frame. A diagnostics
// snapshot carries a capped parameter-tree summary (<=128 entries) and the
// CoreMIDI endpoint/device list, so it is sizeable but bounded; 1 MiB is a
// generous cap that stops a hostile LAN client from forcing a huge allocation
// per read.
const maxMessageBytes = 1 << 20

// Register mounts the diagnostics WebSocket endpoint on mux. onConnect /
// onDisconnect (either may be nil) are invoked so the daemon can notify
// connected MCP clients that an appex started or stopped reporting; they must
// not block.
//
// Route:
//
//	GET /diagnostics   auv3-probe host-diagnostics stream (JSON snapshots)
func Register(mux *http.ServeMux, store *Store, onConnect func(remote string), onDisconnect func(remote string)) {
	mux.HandleFunc("/diagnostics", handleStream(store, onConnect, onDisconnect))
}

// handleStream upgrades to a WebSocket and drains the host-diagnostics contract:
// one full HostDiagnostics envelope per TEXT frame (compact single-line JSON),
// sent on connect, on a ~1 Hz cadence, and on route-change/interruption deltas.
// Each frame replaces the stored snapshot. Anything malformed is logged and
// skipped; a read error (client gone / shutdown) ends the session.
func handleStream(store *Store, onConnect, onDisconnect func(string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Clear the shared lanhttp absolute deadlines before upgrading, exactly
		// like internal/audiotap and internal/midicontrol: a long-lived
		// diagnostics stream must not be dropped after the receiver's 60s
		// Read/WriteTimeout. The loop is bound by r.Context() (cancelled on
		// disconnect / daemon shutdown) instead.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The producer is the native iPad app (no browser Origin), so
			// cross-origin checks do not apply on this LAN-only listener.
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			log.Printf("diagnostics receiver: accept: %v", err)
			return
		}
		// Read frames are bounded; a stalled/oversized client is closed.
		c.SetReadLimit(maxMessageBytes)

		remote := r.RemoteAddr
		store.Connect(remote)
		log.Printf("diagnostics connected from %s", remote)
		if onConnect != nil {
			onConnect(remote)
		}
		defer func() {
			store.Disconnect()
			log.Printf("diagnostics disconnected from %s", remote)
			if onDisconnect != nil {
				onDisconnect(remote)
			}
			_ = c.CloseNow()
		}()

		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				// Normal close / shutdown / client gone — nothing to log loudly.
				if !errors.Is(err, context.Canceled) &&
					websocket.CloseStatus(err) == -1 {
					log.Printf("diagnostics receiver: read from %s: %v", remote, err)
				}
				return
			}
			// Snapshots arrive as TEXT (JSON); ignore any other frame type.
			if typ != websocket.MessageText {
				continue
			}
			if !store.SetSnapshot(data) {
				log.Printf("diagnostics receiver: bad snapshot from %s", remote)
			}
		}
	}
}
