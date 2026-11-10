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
	"image/draw"
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
	// Screens, when set, replaces Sources: GET …/source serves
	// Screens[n], n being the number of gestures (taps, double-taps,
	// long-presses, touch actions, element clicks, keys) so far; the last
	// one repeats. A test lists the screen after each gesture.
	Screens []json.RawMessage
	// Fields are the values of the text fields on screen; typing into one
	// replaces its value.
	Fields []string
	// Bright lists screenshot regions (x, y, w, h in points) drawn light
	// grey, e.g. lit MIDI routing cells.
	Bright [][4]float64
	// BrightScreens, when set, replaces Bright after each gesture like
	// Screens: BrightScreens[n] after n gestures.
	BrightScreens [][][4]float64

	requests  []Request
	mark      int // requests before the last SetScreens
	sourceIdx int
	sessions  int
	gestures  int
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

// Gestures is how many gestures the fake has received.
func (w *WDA) Gestures() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gestures
}

// SetScreens sets the gesture-driven screen sequence and restarts it.
func (w *WDA) SetScreens(srcs ...json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Screens = srcs
	w.gestures = 0
	w.mark = len(w.requests)
}

// GestureLog lists the gesture requests since the last SetScreens, in order, as
// "kind x,y" strings ("tap 120,33", "doubleTap 300,400", "hold 1,2",
// "actions", "click", "keys").
func (w *WDA) GestureLog() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, r := range w.requests[w.mark:] {
		_, sub, _ := strings.Cut(strings.TrimPrefix(r.Path, "/session/"), "/")
		switch {
		case sub == "wda/tap":
			out = append(out, fmt.Sprintf("tap %v,%v", r.Body["x"], r.Body["y"]))
		case sub == "wda/doubleTap":
			out = append(out, fmt.Sprintf("doubleTap %v,%v", r.Body["x"], r.Body["y"]))
		case sub == "wda/touchAndHold":
			out = append(out, fmt.Sprintf("hold %v,%v", r.Body["x"], r.Body["y"]))
		case sub == "actions":
			out = append(out, "actions")
		case strings.HasSuffix(sub, "/click"):
			out = append(out, "click")
		case sub == "wda/keys":
			out = append(out, "keys")
		}
	}
	return out
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
	case sub == "appium/settings":
		reply(rw, 200, nil, sid)
	case sub == "wda/tap", sub == "wda/doubleTap", sub == "wda/touchAndHold", sub == "actions", sub == "wda/keys":
		w.gestures++
		reply(rw, 200, nil, sid)
	case sub == "window/size":
		reply(rw, 200, map[string]any{"width": w.Window[0], "height": w.Window[1]}, sid)
	case sub == "source":
		var src json.RawMessage
		if len(w.Screens) > 0 {
			src = w.Screens[min(w.gestures, len(w.Screens)-1)]
		} else {
			src = w.Sources[min(w.sourceIdx, len(w.Sources)-1)]
			w.sourceIdx++
		}
		reply(rw, 200, src, sid)
	case sub == "elements" && body["using"] == "class name":
		found := []map[string]string{}
		if body["value"] == "XCUIElementTypeTextField" {
			for i := range w.Fields {
				found = append(found, map[string]string{"ELEMENT": fmt.Sprintf("F%d", i)})
			}
		}
		reply(rw, 200, found, sid)
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
	case strings.HasPrefix(sub, "element/"):
		w.element(rw, r.Method, sid, strings.TrimPrefix(sub, "element/"), body)
	default:
		wdaError(rw, 404, "unknown command", path)
	}
}

// element answers …/element/<id>/<op>: click, clear, value (type), rect and
// attribute/value. Text fields are "F<n>", label matches "E<n>".
func (w *WDA) element(rw http.ResponseWriter, method, sid, rest string, body map[string]any) {
	id, op, _ := strings.Cut(rest, "/")
	field := -1
	if _, err := fmt.Sscanf(id, "F%d", &field); err != nil || field >= len(w.Fields) {
		field = -1
	}
	switch {
	case op == "click":
		w.gestures++
		reply(rw, 200, nil, sid)
	case op == "clear" && field >= 0:
		w.Fields[field] = ""
		reply(rw, 200, nil, sid)
	case op == "value" && method == http.MethodPost && field >= 0:
		chars, _ := body["value"].([]any)
		var b strings.Builder
		for _, c := range chars {
			fmt.Fprint(&b, c)
		}
		w.Fields[field] += b.String()
		reply(rw, 200, nil, sid)
	case op == "rect":
		if field >= 0 {
			reply(rw, 200, map[string]any{"x": 100, "y": 200 + 50*field, "width": 300, "height": 36}, sid)
			return
		}
		reply(rw, 200, map[string]any{"x": 10, "y": 20, "width": 100, "height": 40}, sid)
	case op == "attribute/value" && field >= 0:
		reply(rw, 200, w.Fields[field], sid)
	default:
		wdaError(rw, 404, "no such element", id+"/"+op)
	}
}

// png is a screenshot: the window at Scale, dark grey with a red top-left
// quarter.
func (w *WDA) png() []byte {
	pw, ph := int(w.Window[0]*w.Scale), int(w.Window[1]*w.Scale)
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	fill := func(r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	fill(img.Bounds(), color.RGBA{30, 30, 30, 255})
	fill(image.Rect(0, 0, pw/2, ph/2), color.RGBA{200, 0, 0, 255})
	bright := w.Bright
	if len(w.BrightScreens) > 0 {
		bright = w.BrightScreens[min(w.gestures, len(w.BrightScreens)-1)]
	}
	for _, b := range bright {
		px := func(v float64) int { return int(v * w.Scale) }
		fill(image.Rect(px(b[0]), px(b[1]), px(b[0]+b[2]), px(b[1]+b[3])), color.RGBA{90, 90, 90, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
