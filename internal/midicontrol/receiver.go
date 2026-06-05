package midicontrol

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// maxMessageBytes bounds a single inbound WebSocket frame. The brain rarely
// sends anything back (it is a command sink); 64 KiB is a generous cap that
// stops a hostile LAN client from forcing a huge allocation per read.
const maxMessageBytes = 1 << 16

// Register mounts the midi-control WebSocket endpoint on mux. onConnect /
// onDisconnect (either may be nil) are invoked so the daemon can notify
// connected MCP clients that the brain (the agent's "hands") came or went; they
// must not block.
//
// Route:
//
//	GET /midi-control   ProbeMidiBrain control channel (daemon -> brain commands)
func Register(mux *http.ServeMux, hub *Hub, onConnect func(remote string), onDisconnect func(remote string)) {
	mux.HandleFunc("/midi-control", handleControl(hub, onConnect, onDisconnect))
}

// handleControl upgrades to a WebSocket, registers the connection with the hub
// (so MCP tools can push command frames to it), and then blocks reading until
// the brain disconnects or the daemon shuts down. The brain is a command sink:
// any TEXT/BINARY frames it sends are drained and ignored (reserved for future
// acks), but the read is what keeps the connection alive and detects loss.
func handleControl(hub *Hub, onConnect, onDisconnect func(string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Clear the shared lanhttp absolute deadlines before upgrading, exactly
		// like internal/audiotap: a long-lived control channel must not be
		// dropped after the receiver's 60s Read/WriteTimeout. The loop is bound
		// by r.Context() (cancelled on disconnect / daemon shutdown) instead.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The peer is the native iPad app (no browser Origin), so
			// cross-origin checks do not apply on this LAN-only listener.
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			log.Printf("midi-control receiver: accept: %v", err)
			return
		}
		c.SetReadLimit(maxMessageBytes)

		remote := r.RemoteAddr
		hub.Connect(remote, c)
		log.Printf("midi-control connected from %s", remote)
		if onConnect != nil {
			onConnect(remote)
		}
		defer func() {
			hub.Disconnect(c)
			log.Printf("midi-control disconnected from %s", remote)
			if onDisconnect != nil {
				onDisconnect(remote)
			}
			_ = c.CloseNow()
		}()

		ctx := r.Context()
		for {
			_, _, err := c.Read(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) &&
					websocket.CloseStatus(err) == -1 {
					log.Printf("midi-control receiver: read from %s: %v", remote, err)
				}
				return
			}
			// Inbound frames are reserved for future acks; drain and ignore.
		}
	}
}
