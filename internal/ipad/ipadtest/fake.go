// Package ipadtest is an in-memory WebDriverAgent for tests: it answers the
// endpoints internal/ipad uses and records what it was asked to do.
package ipadtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Request is one call the fake received.
type Request struct {
	Method string
	Path   string
	Body   map[string]any
}

// WDA is a fake WebDriverAgent.
type WDA struct {
	*httptest.Server

	mu sync.Mutex
	// Sources are served by GET …/source in turn; the last one repeats.
	Sources []json.RawMessage
	// Window is the session window size.
	Window [2]float64
	// Scale is the screen's pixels per point.
	Scale float64
	// Labels maps a predicate's quoted label to how many elements match
	// (button-first queries count separately: key "button:"+label).
	Labels map[string]int
	// BundleID is reported by /wda/activeAppInfo.
	BundleID string
	// FailSession makes POST /session answer with this W3C error code.
	FailSession string

	requests  []Request
	sourceIdx int
	sessions  int
	open      map[string]bool
}

// New starts a fake WDA serving source as AUM's tree.
func New(source json.RawMessage) *WDA {
	w := &WDA{
		Sources:  []json.RawMessage{source},
		Window:   [2]float64{1210, 834},
		Scale:    2,
		Labels:   map[string]int{},
		BundleID: "com.kymatica.AUM",
		open:     map[string]bool{},
	}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	return w
}

// Requests returns what the fake has been asked so far.
func (w *WDA) Requests() []Request {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Request(nil), w.requests...)
}

// OpenSessions is how many sessions were opened and not deleted.
func (w *WDA) OpenSessions() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.open)
}

// SetSources replaces the source sequence and restarts it.
func (w *WDA) SetSources(srcs ...json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Sources = srcs
	w.sourceIdx = 0
}

func reply(rw http.ResponseWriter, status int, value any, sid string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	body := map[string]any{"value": value}
	if sid != "" {
		body["sessionId"] = sid
	}
	_ = json.NewEncoder(rw).Encode(body)
}

func wdaError(rw http.ResponseWriter, status int, code, msg string) {
	reply(rw, status, map[string]any{"error": code, "message": msg, "traceback": "…"}, "")
}

func (w *WDA) serve(rw http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests = append(w.requests, Request{Method: r.Method, Path: r.URL.Path, Body: body})

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/status":
		reply(rw, 200, map[string]any{
			"ready": true, "message": "WebDriverAgent is ready to accept commands", "device": "ipad",
			"os":    map[string]any{"name": "iPadOS", "version": "27.2", "sdkVersion": "18.4"},
			"build": map[string]any{"version": "16.15.2"},
		}, "")
		return
	case r.Method == http.MethodGet && path == "/wda/screen":
		reply(rw, 200, map[string]any{"scale": w.Scale, "screenSize": map[string]any{"width": w.Window[0], "height": w.Window[1]}}, "")
		return
	case r.Method == http.MethodGet && path == "/wda/activeAppInfo":
		reply(rw, 200, map[string]any{"bundleId": w.BundleID, "pid": 1214, "name": ""}, "")
		return
	case r.Method == http.MethodGet && path == "/screenshot":
		reply(rw, 200, base64.StdEncoding.EncodeToString(w.png()), "")
		return
	case r.Method == http.MethodPost && path == "/session":
		if w.FailSession != "" {
			wdaError(rw, 500, w.FailSession, "cannot create session")
			return
		}
		w.sessions++
		sid := fmt.Sprintf("S%d", w.sessions)
		// Like WDA: one session at a time, a new one ends the previous.
		w.open = map[string]bool{sid: true}
		reply(rw, 200, map[string]any{"sessionId": sid, "capabilities": map[string]any{}}, sid)
		return
	}

	rest, ok := strings.CutPrefix(path, "/session/")
	if !ok {
		wdaError(rw, 404, "unknown command", path)
		return
	}
	sid, sub, _ := strings.Cut(rest, "/")
	if !w.open[sid] {
		wdaError(rw, 404, "invalid session id", "session "+sid+" does not exist")
		return
	}
	switch {
	case r.Method == http.MethodDelete && sub == "":
		delete(w.open, sid)
		reply(rw, 200, nil, "")
	case sub == "appium/settings", sub == "wda/tap", sub == "wda/touchAndHold", sub == "actions":
		reply(rw, 200, nil, sid)
	case sub == "window/size":
		reply(rw, 200, map[string]any{"width": w.Window[0], "height": w.Window[1]}, sid)
	case sub == "source":
		src := w.Sources[min(w.sourceIdx, len(w.Sources)-1)]
		w.sourceIdx++
		reply(rw, 200, src, sid)
	case sub == "elements":
		pred, _ := body["value"].(string)
		key := pred[strings.Index(pred, "label ==[c] ")+len("label ==[c] "):]
		if strings.HasPrefix(pred, "type == 'XCUIElementTypeButton'") {
			key = "button:" + key
		}
		n := 0 // ==[c] matches case-insensitively
		for k, v := range w.Labels {
			if strings.EqualFold(k, key) {
				n = v
			}
		}
		var found []map[string]string
		for i := 0; i < n; i++ {
			found = append(found, map[string]string{"ELEMENT": fmt.Sprintf("E%d", i)})
		}
		if found == nil {
			found = []map[string]string{}
		}
		reply(rw, 200, found, sid)
	case strings.HasPrefix(sub, "element/") && strings.HasSuffix(sub, "/click"):
		reply(rw, 200, nil, sid)
	default:
		wdaError(rw, 404, "unknown command", path)
	}
}

// png is a screenshot: the window at Scale, dark grey with a red top-left
// quarter.
func (w *WDA) png() []byte {
	pw, ph := int(w.Window[0]*w.Scale), int(w.Window[1]*w.Scale)
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			c := color.RGBA{30, 30, 30, 255}
			if x < pw/2 && y < ph/2 {
				c = color.RGBA{200, 0, 0, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
