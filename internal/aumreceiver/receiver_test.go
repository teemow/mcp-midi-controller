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

	"github.com/teemow/aum-session-go/aum"
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
	}, nil))
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

func TestUploadWithPathMirrorsFolderStructure(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
	defer ts.Close()

	// ?path= stages the file verbatim under its iPad-relative path.
	resp, err := http.Post(ts.URL+"/aum-session?path=Live%20sets/New%20Fast%20Forward.aumproj",
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
	if res.Path != "Live sets/New Fast Forward.aumproj" {
		t.Fatalf("path = %q, want the verbatim relative path", res.Path)
	}
	if res.ID != "Live sets/New Fast Forward" {
		t.Fatalf("id = %q, want path without extension", res.ID)
	}
	staged := filepath.Join(dir, "Live sets", "New Fast Forward.aumproj")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("expected staged file %s: %v", staged, err)
	}

	// The manifest lists the nested file with its relative path, and the
	// download route serves it by that path.
	mresp, err := http.Get(ts.URL + "/aum-session")
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	var man Manifest
	if err := json.NewDecoder(mresp.Body).Decode(&man); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	_ = mresp.Body.Close()
	if len(man.Sessions) != 1 {
		t.Fatalf("manifest sessions = %d, want 1", len(man.Sessions))
	}
	e := man.Sessions[0]
	if e.Path != "Live sets/New Fast Forward.aumproj" || e.File != "New Fast Forward.aumproj" {
		t.Fatalf("manifest entry = %+v, want nested path + bare filename", e)
	}

	dresp, err := http.Get(ts.URL + "/aum-session/Live%20sets/New%20Fast%20Forward.aumproj")
	if err != nil {
		t.Fatalf("GET nested download: %v", err)
	}
	got := readAll(t, dresp)
	_ = dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK || !bytes.Equal(got, templateBytes(t)) {
		t.Fatalf("nested download status=%d bytes=%d", dresp.StatusCode, len(got))
	}
}

func TestUploadRejectsTraversalPath(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
	defer ts.Close()

	for _, p := range []string{"../escape.aumproj", "a/../../b.aumproj", ".hidden/x.aumproj", "x.txt"} {
		resp, err := http.Post(ts.URL+"/aum-session?path="+strings.ReplaceAll(p, " ", "%20"),
			"application/octet-stream", bytes.NewReader(templateBytes(t)))
		if err != nil {
			t.Fatalf("POST %q: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("path %q: status = %d, want 400", p, resp.StatusCode)
		}
	}
}

func TestDeleteNestedPrunesEmptyFolders(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
	defer ts.Close()

	nested := filepath.Join(dir, "Live sets", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "demo.aumproj"), templateBytes(t), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/aum-session/Live%20sets/deep/demo.aumproj", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "Live sets")); !os.IsNotExist(err) {
		t.Fatalf("emptied subfolder was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("staging root must survive pruning: %v", err)
	}
}

func TestDeleteAllClearsNestedFiles(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
	defer ts.Close()

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range []string{"root.aumproj", filepath.Join("sub", "nested.aumproj")} {
		if err := os.WriteFile(filepath.Join(dir, p), templateBytes(t), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/aum-session", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE all: %v", err)
	}
	var res DeleteAllResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if res.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", res.Deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("emptied subfolder was not pruned (err=%v)", err)
	}
}

func TestUploadFallsBackToTitleForID(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
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
	ts := httptest.NewServer(Handler(dir, nil, nil))
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
	ts := httptest.NewServer(Handler(dir, nil, nil))
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

// TestManifestRevBumpsAndShortCircuits covers the staging rev contract the
// iPad's sync engine polls against: every write (upload, delete, clear-all)
// bumps the manifest rev, and GET /aum-session?rev=<current> answers 304
// without a body.
func TestManifestRevBumpsAndShortCircuits(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
	defer ts.Close()

	manifestRev := func() int64 {
		t.Helper()
		resp, err := http.Get(ts.URL + "/aum-session")
		if err != nil {
			t.Fatalf("GET manifest: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var man Manifest
		if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return man.Rev
	}

	if rev := manifestRev(); rev != 0 {
		t.Fatalf("fresh staging rev = %d, want 0", rev)
	}

	// Upload bumps.
	resp, err := http.Post(ts.URL+"/aum-session?path=Live%20sets/Demo.aumproj",
		"application/octet-stream", bytes.NewReader(templateBytes(t)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	rev := manifestRev()
	if rev != 1 {
		t.Fatalf("rev after upload = %d, want 1", rev)
	}

	// Polling with the current rev short-circuits to 304.
	resp, err = http.Get(ts.URL + "/aum-session?rev=1")
	if err != nil {
		t.Fatalf("GET manifest?rev=1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("poll with current rev: status = %d, want 304", resp.StatusCode)
	}
	// A stale rev still yields the full manifest.
	resp, err = http.Get(ts.URL + "/aum-session?rev=0")
	if err != nil {
		t.Fatalf("GET manifest?rev=0: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll with stale rev: status = %d, want 200", resp.StatusCode)
	}

	// Delete bumps.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/aum-session/Live%20sets/Demo.aumproj", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = dresp.Body.Close()
	if rev = manifestRev(); rev != 2 {
		t.Fatalf("rev after delete = %d, want 2", rev)
	}

	// Clear-all on an already-empty dir does NOT bump (nothing changed)…
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/aum-session", nil)
	dresp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE all: %v", err)
	}
	_ = dresp.Body.Close()
	if rev = manifestRev(); rev != 2 {
		t.Fatalf("rev after no-op clear = %d, want 2", rev)
	}

	// …but a clear-all that removes files does.
	resp, err = http.Post(ts.URL+"/aum-session?name=again.aumproj",
		"application/octet-stream", bytes.NewReader(templateBytes(t)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/aum-session", nil)
	dresp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE all: %v", err)
	}
	_ = dresp.Body.Close()
	if rev = manifestRev(); rev != 4 {
		t.Fatalf("rev after upload+clear = %d, want 4", rev)
	}
}

func TestDownloadInvokesOnDownloaded(t *testing.T) {
	dir := t.TempDir()
	var downloaded []string
	ts := httptest.NewServer(Handler(dir, nil, func(file string) {
		downloaded = append(downloaded, file)
	}))
	defer ts.Close()

	if err := os.WriteFile(filepath.Join(dir, "demo.aumproj"), templateBytes(t), 0o644); err != nil {
		t.Fatalf("seed staged file: %v", err)
	}

	resp, err := http.Get(ts.URL + "/aum-session/demo.aumproj")
	if err != nil {
		t.Fatalf("GET download: %v", err)
	}
	_ = readAll(t, resp)
	_ = resp.Body.Close()

	if len(downloaded) != 1 || downloaded[0] != "demo.aumproj" {
		t.Fatalf("onDownloaded calls = %v, want [demo.aumproj]", downloaded)
	}

	// A failed download (missing file) must not fire the callback.
	resp, err = http.Get(ts.URL + "/aum-session/nope.aumproj")
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	_ = resp.Body.Close()
	if len(downloaded) != 1 {
		t.Fatalf("onDownloaded fired on a 404 (calls = %v)", downloaded)
	}
}

func TestDownloadRejectsTraversalAndMissing(t *testing.T) {
	dir := t.TempDir()
	ts := httptest.NewServer(Handler(dir, nil, nil))
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
