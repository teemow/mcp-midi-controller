// Package ipad drives the USB-connected iPad's user interface — AUM in
// particular — through WebDriverAgent (WDA), the XCTest runner that the go-ios
// toolchain starts on the device and forwards to a local port. It is the
// daemon's second, MIDI-independent channel into AUM: the agent can look at the
// screen, read AUM's accessibility tree (tempo, transport position, channel
// titles, fader levels, the session name) and tap, press or swipe by label or
// by position. Its main job is verification: after the daemon sends MIDI into
// AUM, read AUM's own UI back to prove the message landed.
//
// The device-side stack (go-ios tunnel, developer disk image, WebDriverAgent)
// is not started here; the auv3-probe repository's scripts/ipad runs it and
// docs/ipad-control.md describes it. This package only talks to the WDA HTTP
// endpoint (and, for diagnostics and app-alias lookup, to the go-ios CLI).
package ipad

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AUMBundleID is AUM's bundle identifier (App Store, not account-specific).
const AUMBundleID = "com.kymatica.AUM"

// DefaultWDAURL is where scripts/ipad (auv3-probe) forwards WebDriverAgent.
const DefaultWDAURL = "http://127.0.0.1:8100"

// Client talks to one WebDriverAgent. WDA serves a single session at a time
// (opening a session ends the previous one), so every operation that needs a
// session holds mu for its whole open → act → close cycle.
type Client struct {
	base  string
	hc    *http.Client
	goios *GoIOS

	mu sync.Mutex

	appsMu sync.Mutex
	apps   map[string]string // alias suffix -> resolved bundle ID
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (tests).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithGoIOS sets the go-ios CLI used for diagnostics and app-alias lookup.
// Without it (or with a nil value) both degrade gracefully.
func WithGoIOS(g *GoIOS) Option { return func(c *Client) { c.goios = g } }

// New returns a client for the WDA endpoint at baseURL (e.g.
// http://127.0.0.1:8100).
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		base: strings.TrimRight(baseURL, "/"),
		hc:   &http.Client{Timeout: 60 * time.Second},
		apps: map[string]string{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL is the WDA endpoint this client talks to.
func (c *Client) BaseURL() string { return c.base }

// Error is a WebDriverAgent error response (or a transport failure reaching it).
type Error struct {
	Status  int    // HTTP status; 0 when WDA was not reachable at all
	Code    string // W3C error code, e.g. "no such element"
	Message string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return "WebDriverAgent not reachable: " + e.Message
	}
	if e.Code != "" {
		return fmt.Sprintf("WebDriverAgent: %s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("WebDriverAgent: HTTP %d: %s", e.Status, e.Message)
}

// Unreachable reports whether err means WDA could not be reached at all (as
// opposed to WDA answering with an error).
func Unreachable(err error) bool {
	var we *Error
	return errors.As(err, &we) && we.Status == 0
}

// SessionLost reports whether err means WDA no longer knows the session: WDA
// serves one session at a time, so another client opening one ends ours.
func SessionLost(err error) bool {
	var we *Error
	return errors.As(err, &we) && we.Code == "invalid session id"
}

// call performs one WDA request and decodes the "value" member of the reply
// into out (when out is non-nil). WDA wraps every reply as {"value": …}; an
// error reply carries {"value": {"error": code, "message": …}}.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return &Error{Message: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Status: resp.StatusCode, Message: "read reply: " + err.Error()}
	}
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return &Error{Status: resp.StatusCode, Message: "undecodable reply: " + truncate(string(raw), 200)}
	}
	var werr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if len(env.Value) > 0 && env.Value[0] == '{' {
		_ = json.Unmarshal(env.Value, &werr)
	}
	if resp.StatusCode >= 300 || werr.Error != "" {
		msg := werr.Message
		if msg == "" {
			msg = truncate(string(raw), 200)
		}
		return &Error{Status: resp.StatusCode, Code: werr.Error, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(env.Value, out); err != nil {
			return fmt.Errorf("decode WebDriverAgent %s %s: %w", method, path, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Status is WDA's /status reply, flattened.
type Status struct {
	Ready      bool   `json:"ready"`
	Message    string `json:"message,omitempty"`
	OS         string `json:"os,omitempty"`
	OSVersion  string `json:"os_version,omitempty"`
	SDKVersion string `json:"sdk_version,omitempty"`
	Device     string `json:"device,omitempty"`
	WDAVersion string `json:"wda_version,omitempty"`
}

// Status reads WDA's /status. It needs no session, so it never disturbs one.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var v struct {
		Ready   bool   `json:"ready"`
		Message string `json:"message"`
		Device  string `json:"device"`
		OS      struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			SDKVersion string `json:"sdkVersion"`
		} `json:"os"`
		Build struct {
			Version string `json:"version"`
		} `json:"build"`
	}
	if err := c.call(ctx, http.MethodGet, "/status", nil, &v); err != nil {
		return Status{}, err
	}
	return Status{
		Ready: v.Ready, Message: v.Message, Device: v.Device,
		OS: v.OS.Name, OSVersion: v.OS.Version, SDKVersion: v.OS.SDKVersion,
		WDAVersion: v.Build.Version,
	}, nil
}

// AppInfo is the app WDA reports in the foreground.
type AppInfo struct {
	BundleID string `json:"bundle_id"`
	PID      int    `json:"pid"`
	Name     string `json:"name,omitempty"`
}

// ActiveApp reads WDA's /wda/activeAppInfo. In iPadOS windowed multitasking
// this can be SpringBoard even while an app is on screen.
func (c *Client) ActiveApp(ctx context.Context) (AppInfo, error) {
	var v struct {
		BundleID string `json:"bundleId"`
		PID      int    `json:"pid"`
		Name     string `json:"name"`
	}
	if err := c.call(ctx, http.MethodGet, "/wda/activeAppInfo", nil, &v); err != nil {
		return AppInfo{}, err
	}
	return AppInfo{BundleID: v.BundleID, PID: v.PID, Name: v.Name}, nil
}

// Screenshot returns the screen as PNG bytes (device pixels; Retina iPads have
// two pixels per point). It needs no session but still holds the client lock so
// it never interleaves with a gesture.
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b64 string
	if err := c.call(ctx, http.MethodGet, "/screenshot", nil, &b64); err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	return png, nil
}

// Session is one WDA session, bound to an app when opened with a bundle ID.
type Session struct {
	c        *Client
	id       string
	BundleID string
}

// WithApp opens a session bound to bundleID ("" = unbound), runs fn, and
// always closes the session again. Binding matters: in iPadOS windowed
// multitasking the "active application" is SpringBoard, so an unbound session
// sees the Home Screen. The capabilities mirror scripts/ipad:
//
//   - forceAppLaunch=false brings a running app forward instead of relaunching
//     it (and launches it when it is not running);
//   - shouldTerminateApp=false keeps WDA from killing the app when the session
//     ends — its default would close AUM and drop the loaded session;
//   - waitForIdleTimeout=0 / animationCoolOffTimeout=0: an endless animation
//     (AUM spins a slot's icon while its node picker is open) otherwise makes
//     every action wait for XCTest's ~45 s idle timeout.
func (c *Client) WithApp(ctx context.Context, bundleID string, fn func(*Session) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	caps := map[string]any{"shouldWaitForQuiescence": false}
	if bundleID != "" {
		caps["bundleId"] = bundleID
		caps["forceAppLaunch"] = false
		caps["shouldTerminateApp"] = false
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, http.MethodPost, "/session",
		map[string]any{"capabilities": map[string]any{"alwaysMatch": caps}}, &created); err != nil {
		return err
	}
	if created.SessionID == "" {
		return &Error{Status: http.StatusOK, Message: "session reply carried no sessionId"}
	}
	s := &Session{c: c, id: created.SessionID, BundleID: bundleID}
	defer func() {
		// Close on a fresh context: the caller's may already be done, and a
		// leaked session would block the next one until WDA times it out.
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.call(cctx, http.MethodDelete, "/session/"+s.id, nil, nil)
	}()
	if err := c.call(ctx, http.MethodPost, s.path("/appium/settings"),
		map[string]any{"settings": map[string]any{"waitForIdleTimeout": 0, "animationCoolOffTimeout": 0}}, nil); err != nil {
		return err
	}
	return fn(s)
}

func (s *Session) path(p string) string { return "/session/" + s.id + p }

// Size is a window size in points.
type Size struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Orientation is "landscape" or "portrait".
func (z Size) Orientation() string {
	if z.Width > z.Height {
		return "landscape"
	}
	return "portrait"
}

// WindowSize is the bound app's window size in points (it rotates with the
// iPad, so it also tells the orientation).
func (s *Session) WindowSize(ctx context.Context) (Size, error) {
	var z Size
	err := s.c.call(ctx, http.MethodGet, s.path("/window/size"), nil, &z)
	return z, err
}

// Source reads the bound app's accessibility tree.
func (s *Session) Source(ctx context.Context) (*Element, error) {
	var raw json.RawMessage
	if err := s.c.call(ctx, http.MethodGet, s.path("/source?format=json"), nil, &raw); err != nil {
		return nil, err
	}
	return ParseSource(raw)
}

// TapAt taps a point (in points, in the app's current orientation).
func (s *Session) TapAt(ctx context.Context, x, y float64) error {
	return s.c.call(ctx, http.MethodPost, s.path("/wda/tap"), map[string]any{"x": x, "y": y}, nil)
}

// Hold long-presses a point for d.
func (s *Session) Hold(ctx context.Context, x, y float64, d time.Duration) error {
	return s.c.call(ctx, http.MethodPost, s.path("/wda/touchAndHold"),
		map[string]any{"x": x, "y": y, "duration": d.Seconds()}, nil)
}

// Swipe drags from (x1,y1) to (x2,y2) over d as a W3C touch action: down, move,
// up. WDA's own drag endpoint presses before it moves, which AUM reads as a
// long-press.
func (s *Session) Swipe(ctx context.Context, x1, y1, x2, y2 float64, d time.Duration) error {
	return s.c.call(ctx, http.MethodPost, s.path("/actions"), map[string]any{"actions": []any{map[string]any{
		"type": "pointer", "id": "finger", "parameters": map[string]any{"pointerType": "touch"},
		"actions": []any{
			map[string]any{"type": "pointerMove", "duration": 0, "x": x1, "y": y1},
			map[string]any{"type": "pointerDown", "button": 0},
			map[string]any{"type": "pointerMove", "duration": d.Milliseconds(), "x": x2, "y": y2},
			map[string]any{"type": "pointerUp", "button": 0},
		},
	}}}, nil)
}

// ErrNoSuchLabel is returned by TapLabel when no element carries the label.
var ErrNoSuchLabel = errors.New("no element with that label")

// TapLabel taps the element whose accessibility label equals label
// (case-insensitive), preferring a button: a button's own text child usually
// carries the same label. It retries for up to wait so a tap right after a
// screen change still finds its target. It returns how many elements matched
// (the first one is tapped).
func (s *Session) TapLabel(ctx context.Context, label string, wait time.Duration) (int, error) {
	wanted := "label ==[c] " + strconv.Quote(label)
	predicates := []string{"type == 'XCUIElementTypeButton' AND " + wanted, wanted}
	deadline := time.Now().Add(wait)
	for {
		for _, p := range predicates {
			var found []map[string]string
			if err := s.c.call(ctx, http.MethodPost, s.path("/elements"),
				map[string]any{"using": "predicate string", "value": p}, &found); err != nil {
				return 0, err
			}
			if len(found) == 0 {
				continue
			}
			id := elementID(found[0])
			if id == "" {
				return 0, &Error{Status: http.StatusOK, Message: "element reply carried no id"}
			}
			if err := s.c.call(ctx, http.MethodPost, s.path("/element/"+id+"/click"), map[string]any{}, nil); err != nil {
				return 0, err
			}
			return len(found), nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("%w %q", ErrNoSuchLabel, label)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// elementID picks the element reference out of a W3C/JSONWP element object.
func elementID(m map[string]string) string {
	if id := m["ELEMENT"]; id != "" {
		return id
	}
	if id := m["element-6066-11e4-a52e-4f735466cecf"]; id != "" {
		return id
	}
	for _, v := range m {
		return v
	}
	return ""
}
