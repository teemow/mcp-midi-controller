package audiotap

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// maxMessageBytes bounds a single WebSocket frame. Audio chunks are small
// (TapStreamer drains ~10 ms at a time); 1 MiB is a generous cap that stops a
// hostile LAN client from forcing a huge allocation per read.
const maxMessageBytes = 1 << 20

// Register mounts the audio-stream WebSocket endpoint on mux. onConnect /
// onDisconnect (either may be nil) are invoked so the daemon can notify
// connected MCP clients that a tap came or went; they must not block.
//
// Route:
//
//	GET /audio-stream   ProbeAudioTap WebSocket (format + PCM + features)
func Register(mux *http.ServeMux, store *Store, onConnect func(remote string), onDisconnect func(remote string)) {
	mux.HandleFunc("/audio-stream", handleStream(store, onConnect, onDisconnect))
}

// handleStream upgrades to a WebSocket and drains the ProbeAudioTap contract:
// one TEXT "format" message, BINARY little-endian Float32 mono PCM, and ~10 Hz
// TEXT "features" messages. Anything malformed is logged and skipped; a read
// error (client gone / shutdown) ends the session.
func handleStream(store *Store, onConnect, onDisconnect func(string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The shared lanhttp.Serve sets a 60s Read/WriteTimeout on the server
		// (good against slow POSTers on the other receiver routes). Those become
		// absolute deadlines on the hijacked connection and coder/websocket does
		// not reset them, so a long-lived tap would be dropped after 60s. Clear
		// them here before upgrading; the read loop is bounded by r.Context()
		// (cancelled on client disconnect / daemon shutdown) instead.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The producer is the native iPad app (no browser Origin), so
			// cross-origin checks do not apply on this LAN-only listener.
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			log.Printf("audio-tap receiver: accept: %v", err)
			return
		}
		// Read frames are bounded; a stalled/oversized client is closed.
		c.SetReadLimit(maxMessageBytes)

		remote := r.RemoteAddr
		store.Connect(remote)
		log.Printf("audio-tap connected from %s", remote)
		if onConnect != nil {
			onConnect(remote)
		}
		defer func() {
			store.Disconnect()
			log.Printf("audio-tap disconnected from %s", remote)
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
					log.Printf("audio-tap receiver: read from %s: %v", remote, err)
				}
				return
			}
			switch typ {
			case websocket.MessageText:
				handleText(store, data)
			case websocket.MessageBinary:
				store.AppendAudio(decodeFloat32LE(data))
			}
		}
	}
}

// wireMessage is the discriminated-union envelope shared by the TEXT messages
// (format / features) of the audio-stream contract.
type wireMessage struct {
	Type string `json:"type"`
	// format
	Encoding   string  `json:"encoding"`
	Channels   int     `json:"channels"`
	SampleRate float64 `json:"sampleRate"`
	Source     string  `json:"source"`
	// features
	RMS  float32 `json:"rms"`
	Peak float32 `json:"peak"`
}

func handleText(store *Store, data []byte) {
	var msg wireMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("audio-tap receiver: bad text message: %v", err)
		return
	}
	switch msg.Type {
	case "format":
		store.SetFormat(Format{
			Encoding:   msg.Encoding,
			Channels:   msg.Channels,
			SampleRate: msg.SampleRate,
			Source:     msg.Source,
		})
	case "features":
		store.SetFeatures(msg.RMS, msg.Peak)
	}
}

// decodeFloat32LE reinterprets a binary frame as little-endian Float32 mono PCM.
// A trailing partial sample (len not a multiple of 4) is ignored.
func decodeFloat32LE(data []byte) []float32 {
	n := len(data) / 4
	if n == 0 {
		return nil
	}
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return out
}
