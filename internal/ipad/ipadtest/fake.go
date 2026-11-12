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
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	// FailSession makes POST /session answer with this W3C error code:
	// every session, or with FailBundle set only one bound to that bundle id.
	FailSession string
	FailBundle  string
	// Locked is the iPad on its lock screen: /wda/locked says so, and a
	// session bound to an app fails the way WDA's does, since a locked iPad
	// brings no app to the front.
	Locked bool
	// Screens, when set, replaces Sources: GET …/source serves
	// Screens[n], n being the number of gestures (taps, double-taps,
	// long-presses, touch actions, element clicks, keys) so far; the last
	// one repeats. A test lists the screen after each gesture.
	Screens []json.RawMessage
	// SourceFunc, when set, replaces Sources and Screens: GET …/source
	// serves what it returns for the number of gestures so far and the
	// number of reads since the last gesture. It models a screen that also
	// changes by itself, such as a session that finishes loading.
	SourceFunc func(gestures, reads int) json.RawMessage
	// Fields are the values of the text fields on screen; typing into one
	// replaces its value.
	Fields []string
	// Bright lists screenshot regions (x, y, w, h in points) drawn light
	// grey, e.g. lit MIDI routing cells.
	Bright [][4]float64
	// RedRings lists node frames (x, y, w, h in points) whose ring is drawn
	// red in the screenshot, the way AUM draws a node whose plugin failed.
	RedRings [][4]float64
	// BrightScreens, when set, replaces Bright after each gesture like
	// Screens: BrightScreens[n] after n gestures.
	BrightScreens [][][4]float64
	// Apps are other apps' windows by bundle id: a session opened with one
	// of these ids reads that app's tree and taps and clicks in it, and its
	// gestures are not counted among AUM's. Set it before the first request.
	Apps map[string]*ProbeApp
	// AUMCrashAt and AUMCrashes make AUM go away under a session bound to
	// it, as a crash does: the AUMCrashAt-th gesture since AUM was started
	// takes it down, AUMCrashes times. That gesture is answered and not
	// counted. Every later call of the session gets WDA's "is not running,
	// possibly crashed", and the Home Screen is in front. A new session bound
	// to AUM starts it again, with its gestures counted from 0.
	AUMCrashAt, AUMCrashes int
	// AUMStaysDown keeps an AUM that went away from starting again: a new
	// session bound to it fails with WDA's "Cannot launch".
	AUMStaysDown bool
	// AfterLaunch, when set, replaces SourceFunc once a session has started
	// AUM: a freshly started AUM shows its own screen, not the one it had.
	AfterLaunch func(gestures, reads int) json.RawMessage

	requests  []Request
	mark      int // requests before the last SetScreens
	sourceIdx int
	sessions  int
	gestures  int
	reads     int // source reads since the last gesture
	open      map[string]bool
	bound     map[string]string // session -> the bundle id it was opened with
	fronted   []string          // the bundle ids of the bound sessions, in order
	aumDown   bool              // AUM is not running
	launches  int               // how often a session started AUM

	appMu  sync.Mutex
	appEls []appElement // the elements found in an app's window ("A<n>")
}

// appElement is a button found in an app's window.
type appElement struct {
	app   *ProbeApp
	label string
	rect  [4]float64
}

// aumBundle is AUM's bundle id, homeScreen the Home Screen's.
const (
	aumBundle  = "com.kymatica.AUM"
	homeScreen = "com.apple.springboard"
)

// New starts a fake WDA serving source as AUM's tree.
func New(source json.RawMessage) *WDA {
	w := &WDA{
		Sources:  []json.RawMessage{source},
		Window:   [2]float64{1210, 834},
		Scale:    2,
		Labels:   map[string]int{},
		BundleID: aumBundle,
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

// Fronted lists the apps brought to the front, in order: the bundle id of
// every session that was opened bound to an app. The last one is the app in
// front.
func (w *WDA) Fronted() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.fronted...)
}

// AUMDown reports whether AUM went away and was not started again: the Home
// Screen is in front then, whatever Fronted lists last.
func (w *WDA) AUMDown() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.aumDown
}

// Launches is how often a session started AUM because it was not running.
func (w *WDA) Launches() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.launches
}

// StopAUM takes AUM down now, as when it was closed or crashed while no
// session drove it: the next session bound to it starts it again.
func (w *WDA) StopAUM() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.aumDown = true
}

// SetAUMCrashes makes AUM go away at its at-th gesture, times times (see
// AUMCrashAt).
func (w *WDA) SetAUMCrashes(at, times int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.AUMCrashAt, w.AUMCrashes = at, times
}

// SetLocked puts the iPad on its lock screen, or takes it off.
func (w *WDA) SetLocked(locked bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Locked = locked
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
		front := w.BundleID
		if w.aumDown {
			front = homeScreen
		}
		reply(rw, 200, map[string]any{"bundleId": front, "pid": 1214, "name": ""}, "")
		return
	case r.Method == http.MethodGet && path == "/screenshot":
		reply(rw, 200, base64.StdEncoding.EncodeToString(w.png()), "")
		return
	case r.Method == http.MethodGet && path == "/wda/locked":
		reply(rw, 200, w.Locked, "")
		return
	case r.Method == http.MethodPost && path == "/session":
		caps, _ := body["capabilities"].(map[string]any)
		match, _ := caps["alwaysMatch"].(map[string]any)
		bundle, _ := match["bundleId"].(string)
		if w.FailSession != "" && (w.FailBundle == "" || w.FailBundle == bundle) {
			wdaError(rw, 500, w.FailSession, "cannot create session")
			return
		}
		if w.Locked && bundle != "" {
			wdaError(rw, 500, "unknown error", "Unable to launch "+bundle+" because the device was not, or could not be, unlocked.")
			return
		}
		// Like WDA: a session starts its app when that is not running.
		if w.aumDown && bundle == aumBundle {
			if w.AUMStaysDown {
				wdaError(rw, 500, "session not created", "Cannot launch "+bundle+" application. Make sure the correct bundle identifier has been provided in capabilities and check the device log for possible crash report occurrences")
				return
			}
			w.aumDown = false
			w.launches++
			w.gestures, w.reads = 0, 0
			if w.AfterLaunch != nil {
				w.SourceFunc = w.AfterLaunch
			}
		}
		w.sessions++
		sid := fmt.Sprintf("S%d", w.sessions)
		// Like WDA: one session at a time, a new one ends the previous.
		w.open = map[string]bool{sid: true}
		w.bound = map[string]string{sid: bundle}
		if bundle != "" {
			w.fronted = append(w.fronted, bundle)
		}
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
	if app := w.Apps[w.bound[sid]]; app != nil {
		// The app keeps its own state, and what a click starts in it may
		// take a while: it is served without the fake's lock.
		w.mu.Unlock()
		served := w.serveApp(rw, r.Method, sid, sub, body, app)
		w.mu.Lock()
		if served {
			return
		}
	}
	if w.aumDown && w.bound[sid] == aumBundle && (r.Method != http.MethodDelete || sub != "") {
		wdaError(rw, 500, "invalid element state", "The application under test with bundle id '"+aumBundle+"' is not running, possibly crashed")
		return
	}
	switch {
	case r.Method == http.MethodDelete && sub == "":
		delete(w.open, sid)
		reply(rw, 200, nil, "")
	case sub == "appium/settings":
		reply(rw, 200, nil, sid)
	case sub == "wda/tap", sub == "wda/doubleTap", sub == "wda/touchAndHold", sub == "actions", sub == "wda/keys":
		w.gesture(sid)
		reply(rw, 200, nil, sid)
	case sub == "window/size":
		reply(rw, 200, map[string]any{"width": w.Window[0], "height": w.Window[1]}, sid)
	case sub == "source":
		var src json.RawMessage
		if w.SourceFunc != nil {
			src = w.SourceFunc(w.gestures, w.reads)
			w.reads++
		} else if len(w.Screens) > 0 {
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

// gesture counts a gesture of a session, or takes AUM down with it (see
// AUMCrashAt). w.mu must be held.
func (w *WDA) gesture(sid string) {
	if w.AUMCrashes > 0 && w.bound[sid] == aumBundle && w.gestures+1 == w.AUMCrashAt {
		w.AUMCrashes--
		w.aumDown = true
		return
	}
	w.gestures++
	w.reads = 0
}

// serveApp answers what a session bound to another app asks of that app's
// window: its tree, a tap, the buttons a predicate finds and a button's frame
// or click. It reports false for what the fake answers the same for any app.
func (w *WDA) serveApp(rw http.ResponseWriter, method, sid, sub string, body map[string]any, app *ProbeApp) bool {
	switch {
	case sub == "source":
		reply(rw, 200, app.source(), sid)
	case sub == "wda/tap":
		x, _ := body["x"].(float64)
		y, _ := body["y"].(float64)
		app.tap(x, y)
		reply(rw, 200, nil, sid)
	case sub == "elements" && body["using"] == "predicate string":
		pred, _ := body["value"].(string)
		found := []map[string]string{}
		// Every button of the app is a target; a text is found by no query.
		if !strings.HasPrefix(pred, "type == 'XCUIElementTypeButton'") {
			reply(rw, 200, found, sid)
			return true
		}
		add := func(label string, rect [4]float64) {
			w.appEls = append(w.appEls, appElement{app, label, rect})
			found = append(found, map[string]string{"ELEMENT": fmt.Sprintf("A%d", len(w.appEls)-1)})
		}
		if _, quoted, byName := strings.Cut(pred, "name =="); byName {
			// A name is an identifier, or the label of a button without one.
			if name, err := strconv.Unquote(strings.TrimSpace(quoted)); err == nil {
				labels, rects := app.named(name)
				w.appMu.Lock()
				for i := range rects {
					add(labels[i], rects[i])
				}
				w.appMu.Unlock()
			}
			reply(rw, 200, found, sid)
			return true
		}
		_, quoted, _ := strings.Cut(pred, "label ==")
		fold := strings.HasPrefix(quoted, "[c]")
		label, err := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(quoted, "[c]")))
		if err == nil {
			w.appMu.Lock()
			for _, rect := range app.buttons(label, fold) {
				add(label, rect)
			}
			w.appMu.Unlock()
		}
		reply(rw, 200, found, sid)
	case strings.HasPrefix(sub, "element/A"):
		id, op, _ := strings.Cut(strings.TrimPrefix(sub, "element/A"), "/")
		n, err := strconv.Atoi(id)
		w.appMu.Lock()
		known := err == nil && n >= 0 && n < len(w.appEls)
		var e appElement
		if known {
			e = w.appEls[n]
		}
		w.appMu.Unlock()
		switch {
		case !known:
			wdaError(rw, 404, "no such element", id)
		case op == "rect":
			reply(rw, 200, map[string]any{"x": e.rect[0], "y": e.rect[1], "width": e.rect[2], "height": e.rect[3]}, sid)
		case op == "click" && method == http.MethodPost:
			// The element is the one found: gone when its row has moved.
			if !e.app.click(e.label, e.rect) {
				wdaError(rw, 404, "stale element reference", e.label)
				return true
			}
			reply(rw, 200, nil, sid)
		default:
			wdaError(rw, 404, "unknown command", sub)
		}
	default:
		return false
	}
	return true
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
		w.gesture(sid)
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
	for _, r := range w.RedRings {
		cx, cy, half := (r[0]+r[2]/2)*w.Scale, (r[1]+r[3]/2)*w.Scale, r[2]/2*w.Scale
		// The node sits on the dark mixer, whatever the test put there.
		fill(image.Rect(int(cx-1.4*half), int(cy-1.4*half), int(cx+1.4*half), int(cy+1.4*half)), color.RGBA{30, 30, 30, 255})
		for y := int(cy - half); y <= int(cy+half); y++ {
			for x := int(cx - half); x <= int(cx+half); x++ {
				if d := math.Hypot(float64(x)-cx, float64(y)-cy) / half; d >= 0.72 && d <= 0.97 {
					img.SetRGBA(x, y, color.RGBA{150, 33, 33, 255})
				}
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
