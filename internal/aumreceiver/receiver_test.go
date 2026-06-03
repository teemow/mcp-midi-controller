package aumreceiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/aum"
)

// templateBytes returns a valid .aumproj byte stream (the synthetic template).
func templateBytes(t *testing.T) []byte {
	t.Helper()
	data, err := aum.Template().Encode()
	if err != nil {
		t.Fatalf("encode template: %v", err)
	}
	return data
}

func TestUploadStagesSessionAndNotifies(t *testing.T) {
	dir := t.TempDir()

	var gotRes Result
	called := 0
	ts := httptest.NewServer(Handler(dir, func(r Result) {
		gotRes = r
		called++
	}))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/aum-session?name=New%20Fast%20Forward.aumproj",
		"application/octet-stream", bytes.NewReader(templateBytes(t)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var res Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res.ID != "new_fast_forward" {
		t.Fatalf("id = %q, want new_fast_forward", res.ID)
	}
	if res.Version != 13 || res.Channels != 3 {
		t.Fatalf("res = %+v, want version=13 channels=3", res)
	}

	staged := filepath.Join(dir, "new_fast_forward.aumproj")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("expected staged file %s: %v", staged, err)
	}
	// The staged bytes must re-open as the same session.
	sess, err := aum.OpenFile(staged)
	if err != nil {
		t.Fatalf("staged file is not a decodable session: %v", err)
	}
	if sess.Version() != 13 {
		t.Fatalf("staged version = %d, want 13", sess.Version())
	}

	if called != 1 || gotRes.Staged != staged {
		t.Fatalf("onStaged called %d times, res=%+v", called, gotRes)
	}
}

func TestUploadFallsBackToTitleForID(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil))
	defer ts.Close()

	// No ?name=, so the id derives from the session title ("Template").
	resp, err := http.Post(ts.URL+"/aum-session", "application/octet-stream", bytes.NewReader(templateBytes(t)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var res Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res.ID != "template" {
		t.Fatalf("id = %q, want template (from title)", res.ID)
	}
}

func TestUploadRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil))
	defer ts.Close()

	t.Run("GET on upload route is the manifest, not 405", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/aum-session")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (manifest)", resp.StatusCode)
		}
	})

	t.Run("non-session body is rejected", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/aum-session?name=x.aumproj",
			"application/octet-stream", strings.NewReader("not a bplist"))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("empty body is rejected", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/aum-session?name=x.aumproj",
			"application/octet-stream", strings.NewReader(""))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("healthz is ok", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET healthz: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

func TestManifestAndDownloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil))
	defer ts.Close()

	// Stage a session by writing it directly (as the aum tools do).
	want := templateBytes(t)
	if err := os.WriteFile(filepath.Join(dir, "demo.aumproj"), want, 0o644); err != nil {
		t.Fatalf("seed staged file: %v", err)
	}

	// Manifest lists it with a parsed summary.
	resp, err := http.Get(ts.URL + "/aum-session")
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	var man Manifest
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	_ = resp.Body.Close()
	if len(man.Sessions) != 1 {
		t.Fatalf("manifest sessions = %d, want 1", len(man.Sessions))
	}
	e := man.Sessions[0]
	if e.File != "demo.aumproj" || e.Kind != "session" || e.Version != 13 || e.Channels != 3 {
		t.Fatalf("manifest entry = %+v, want demo.aumproj session v13 ch3", e)
	}

	// Download returns the staged bytes verbatim.
	resp, err = http.Get(ts.URL + "/aum-session/demo.aumproj")
	if err != nil {
		t.Fatalf("GET download: %v", err)
	}
	got := readAll(t, resp)
	_ = resp.Body.Close()
	if !bytes.Equal(got, want) {
		t.Fatalf("download bytes differ from staged (%d vs %d)", len(got), len(want))
	}
}

func TestDownloadRejectsTraversalAndMissing(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil))
	defer ts.Close()

	t.Run("missing file is 404", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/aum-session/nope.aumproj")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("non-staged extension is rejected", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		resp, err := http.Get(ts.URL + "/aum-session/secret.txt")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.Bytes()
}
