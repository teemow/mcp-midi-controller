package ipad

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGoIOS answers `ios <args>` from a table keyed by the joined args.
func fakeGoIOS(onPath bool, answers map[string]string) (*GoIOS, *int) {
	calls := 0
	g := &GoIOS{
		Bin: "ios",
		lookPath: func(string) (string, error) {
			if !onPath {
				return "", errors.New("not found")
			}
			return "/usr/bin/ios", nil
		},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls++
			out, ok := answers[strings.Join(args, " ")]
			if !ok {
				return nil, errors.New("exit status 1")
			}
			return []byte(out), nil
		},
	}
	return g, &calls
}

func TestDiagnose(t *testing.T) {
	cases := []struct {
		name    string
		onPath  bool
		answers map[string]string
		want    Layers
	}{
		{"no go-ios", false, nil, Layers{}},
		{"no tunnel", true, map[string]string{"tunnel ls": "[]"}, Layers{GoIOS: true}},
		{"no image", true, map[string]string{"tunnel ls": `[{"udid":"x"}]`, "image list": "[]"}, Layers{GoIOS: true, Tunnel: true}},
		{"all up", true, map[string]string{"tunnel ls": `[{"udid":"x"}]`, "image list": `[{"signature":"…"}]`}, Layers{GoIOS: true, Tunnel: true, DiskImage: true}},
	}
	for _, c := range cases {
		g, _ := fakeGoIOS(c.onPath, c.answers)
		if got := g.Diagnose(context.Background()); got != c.want {
			t.Errorf("%s: Diagnose = %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := New("http://x").Diagnose(context.Background()); got != (Layers{}) {
		t.Errorf("client without go-ios = %+v", got)
	}
}

func TestResolveApp(t *testing.T) {
	g, calls := fakeGoIOS(true, map[string]string{
		"apps": `[{"CFBundleIdentifier":"com.example.other"},{"CFBundleIdentifier":"XTL-TEAM.com.teemow.auv3probe"}]`,
	})
	c := New("http://x", WithGoIOS(g))
	ctx := context.Background()
	for in, want := range map[string]string{"": AUMBundleID, "AUM": AUMBundleID, "com.example.app": "com.example.app"} {
		if got, err := c.ResolveApp(ctx, in); err != nil || got != want {
			t.Errorf("ResolveApp(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for i := 0; i < 2; i++ {
		if got, err := c.ResolveApp(ctx, "probe"); err != nil || got != "XTL-TEAM.com.teemow.auv3probe" {
			t.Fatalf("ResolveApp(probe) = %q, %v", got, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("the probe lookup should be cached, ran `ios apps` %d times", *calls)
	}

	// The wrapped {"apps": […]} form decodes too.
	g, _ = fakeGoIOS(true, map[string]string{"apps": `{"apps":[{"CFBundleIdentifier":"com.teemow.auv3probe"}]}`})
	if got, err := New("http://x", WithGoIOS(g)).ResolveApp(ctx, "probe"); err != nil || got != "com.teemow.auv3probe" {
		t.Fatalf("wrapped apps = %q, %v", got, err)
	}

	g, _ = fakeGoIOS(true, map[string]string{"apps": `[]`})
	if _, err := New("http://x", WithGoIOS(g)).ResolveApp(ctx, "probe"); err == nil || !strings.Contains(err.Error(), "no installed app") {
		t.Fatalf("missing probe error = %v", err)
	}
	if _, err := New("http://x").ResolveApp(ctx, "probe"); err == nil || !strings.Contains(err.Error(), "full bundle ID") {
		t.Fatalf("no go-ios error = %v", err)
	}
}
