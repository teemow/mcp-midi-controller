package ipad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GoIOS runs the go-ios CLI (`ios`). The daemon only uses it read-only: to
// tell which layer of the device stack is missing when WebDriverAgent is not
// reachable, and to resolve sideloaded apps' team-prefixed bundle IDs.
type GoIOS struct {
	// Bin is the executable ("ios" on PATH by default).
	Bin string
	// run and lookPath are replaced in tests.
	run      func(ctx context.Context, bin string, args ...string) ([]byte, error)
	lookPath func(string) (string, error)
}

// NewGoIOS returns a GoIOS for bin ("" = "ios").
func NewGoIOS(bin string) *GoIOS {
	if bin == "" {
		bin = "ios"
	}
	return &GoIOS{Bin: bin, run: runCommand, lookPath: exec.LookPath}
}

func runCommand(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// go-ios logs JSON to stderr; only stdout carries the answer.
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, truncate(lastLine(string(ee.Stderr)), 300))
		}
		return out, fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return out, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// Available reports whether the go-ios executable can be found.
func (g *GoIOS) Available() bool {
	if g == nil || g.lookPath == nil {
		return false
	}
	_, err := g.lookPath(g.Bin)
	return err == nil
}

// Layers is the state of the device stack under WebDriverAgent.
type Layers struct {
	// GoIOS is false when the `ios` executable is missing (the other fields
	// are then unknown).
	GoIOS bool `json:"go_ios"`
	// Tunnel is true when a go-ios tunnel to a device is up.
	Tunnel bool `json:"tunnel"`
	// DiskImage is true when a developer disk image is mounted.
	DiskImage bool `json:"disk_image"`
}

// Diagnose checks the layers WebDriverAgent needs, top-down.
func (g *GoIOS) Diagnose(ctx context.Context) Layers {
	var l Layers
	if !g.Available() {
		return l
	}
	l.GoIOS = true
	out, err := g.run(ctx, g.Bin, "tunnel", "ls")
	if err != nil || !strings.Contains(string(out), `"udid"`) {
		return l
	}
	l.Tunnel = true
	out, err = g.run(ctx, g.Bin, "image", "list")
	l.DiskImage = err == nil && strings.Contains(string(out), `"signature"`)
	return l
}

// BundleIDs lists the installed apps' bundle identifiers.
func (g *GoIOS) BundleIDs(ctx context.Context) ([]string, error) {
	if !g.Available() {
		return nil, fmt.Errorf("go-ios (%q) not found on PATH", g.binName())
	}
	out, err := g.run(ctx, g.Bin, "apps")
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	if err := json.Unmarshal(out, &list); err != nil {
		var wrapped struct {
			Apps []map[string]any `json:"apps"`
		}
		if err2 := json.Unmarshal(out, &wrapped); err2 != nil {
			return nil, fmt.Errorf("decode `ios apps`: %w", err)
		}
		list = wrapped.Apps
	}
	ids := make([]string, 0, len(list))
	for _, a := range list {
		if id, _ := a["CFBundleIdentifier"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (g *GoIOS) binName() string {
	if g == nil {
		return "ios"
	}
	return g.Bin
}

// App aliases: AUM ships under a fixed ID; apps sideloaded with xtool carry a
// team prefix (XTL-<TEAMID>.<id>), so they are matched by suffix on the device.
var appSuffixes = map[string]string{
	"probe": "com.teemow.auv3probe",
}

// IsApp reports whether bundleID is the sideloaded app known by alias
// ("probe"): its id as built, or that id behind a signing-team prefix.
func IsApp(alias, bundleID string) bool {
	suffix, ok := appSuffixes[strings.ToLower(strings.TrimSpace(alias))]
	return ok && (bundleID == suffix || strings.HasSuffix(bundleID, "."+suffix))
}

// ResolveApp turns an app argument into a bundle ID: "aum" (the default when
// empty), "probe" (AUv3 Probe, resolved on the device), or a bundle ID as is.
func (c *Client) ResolveApp(ctx context.Context, app string) (string, error) {
	app = strings.TrimSpace(app)
	switch strings.ToLower(app) {
	case "", "aum":
		return AUMBundleID, nil
	}
	suffix, ok := appSuffixes[strings.ToLower(app)]
	if !ok {
		return app, nil
	}
	c.appsMu.Lock()
	defer c.appsMu.Unlock()
	if id := c.apps[suffix]; id != "" {
		return id, nil
	}
	ids, err := c.goios.BundleIDs(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w (pass the full bundle ID instead)", app, err)
	}
	for _, id := range ids {
		if id == suffix || strings.HasSuffix(id, "."+suffix) {
			c.apps[suffix] = id
			return id, nil
		}
	}
	return "", fmt.Errorf("no installed app matching *%s", suffix)
}

// Diagnose checks the device stack under WDA (nil-safe when no go-ios).
func (c *Client) Diagnose(ctx context.Context) Layers {
	if c.goios == nil {
		return Layers{}
	}
	return c.goios.Diagnose(ctx)
}

// HasGoIOS reports whether a go-ios CLI is configured and on PATH.
func (c *Client) HasGoIOS() bool { return c.goios.Available() }
