package drive_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/drive"
	"github.com/teemow/mcp-midi-controller/internal/drive/drivetest"
)

const chunk = 256 << 10

// rig is a client pointed at a stand-in Google, with its files.
type rig struct {
	t         *testing.T
	fake      *drivetest.Server
	client    *drive.Client
	credsPath string
	tokenPath string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{
		t: t, fake: drivetest.New(t),
		credsPath: filepath.Join(dir, "config", "google-drive-client.json"),
		tokenPath: filepath.Join(dir, "state", "google-drive-token.json"),
	}
	drivetest.WriteClientFile(t, r.credsPath)
	r.client = r.newClient()
	t.Cleanup(func() {
		if leaks := r.fake.Leaks(); len(leaks) > 0 {
			t.Errorf("secrets went where they do not belong: %v", leaks)
		}
	})
	return r
}

// newClient makes a client for the rig's stand-in that does not wait between
// tries.
func (r *rig) newClient(more ...drive.Option) *drive.Client {
	return drive.New(r.credsPath, r.tokenPath, append([]drive.Option{
		drive.WithEndpoints(drive.EndpointsAt(r.fake.URL)),
		drive.WithChunkSize(chunk),
		drive.WithBackoff(func(int) time.Duration { return 0 }),
		drive.WithPollInterval(2 * time.Millisecond),
	}, more...)...)
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// signIn runs a whole sign-in, with the user approving it.
func (r *rig) signIn() {
	r.t.Helper()
	da, err := r.client.StartDeviceAuth(ctx(r.t))
	if err != nil {
		r.t.Fatalf("StartDeviceAuth: %v", err)
	}
	r.fake.Approve(da.UserCode)
	if err := r.client.AwaitDeviceAuth(ctx(r.t), da); err != nil {
		r.t.Fatalf("AwaitDeviceAuth: %v", err)
	}
}

// noSecrets fails the test when text holds a token, a code or the client
// secret.
func (r *rig) noSecrets(what, text string) {
	r.t.Helper()
	for _, secret := range r.fake.Secrets() {
		if strings.Contains(text, secret) {
			r.t.Errorf("%s carries a secret: %s", what, text)
		}
	}
}

// payload is a file of n bytes that differ along its length, so a chunk in
// the wrong place shows.
func payload(t *testing.T, n int) (string, []byte) {
	t.Helper()
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	path := filepath.Join(t.TempDir(), "take.flac")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestLoadCredentials(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, content := range map[string]string{
		"console.json": `{"installed": {"client_id": "id-1", "project_id": "p", "client_secret": "sec-1", "redirect_uris": ["http://localhost"]}}`,
		"web.json":     `{"web": {"client_id": "id-1", "client_secret": "sec-1"}}`,
		"flat.json":    `{"client_id": "id-1", "client_secret": "sec-1"}`,
	} {
		creds, err := drive.LoadCredentials(write(name, content))
		if err != nil || creds.ClientID != "id-1" || creds.ClientSecret != "sec-1" {
			t.Errorf("%s: %+v, %v", name, creds, err)
		}
	}
	for name, content := range map[string]string{
		"no-secret.json": `{"installed": {"client_id": "id-1"}}`,
		"garbage.json":   `client_secret = hunter2-not-json`,
		"empty.json":     `{}`,
	} {
		_, err := drive.LoadCredentials(write(name, content))
		if !errors.Is(err, drive.ErrNoClient) {
			t.Errorf("%s: err = %v, want ErrNoClient", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error quotes the file: %v", name, err)
		}
	}
	if _, err := drive.LoadCredentials(filepath.Join(dir, "absent.json")); !errors.Is(err, drive.ErrNoClient) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing file: %v", err)
	}
}

// TestSignIn runs the device sign-in: the code and link to show, the one
// scope asked for, the wait for the user, and the sign-in kept owner-only.
func TestSignIn(t *testing.T) {
	r := newRig(t)
	if r.client.Connected() {
		t.Fatal("connected before any sign-in")
	}
	if _, err := r.client.About(ctx(t)); !errors.Is(err, drive.ErrNotConnected) {
		t.Fatalf("About before the sign-in: %v", err)
	}

	da, err := r.client.StartDeviceAuth(ctx(t))
	if err != nil {
		t.Fatalf("StartDeviceAuth: %v", err)
	}
	if !strings.HasPrefix(da.UserCode, "ABCD-") || da.VerificationURL != r.fake.URL+"/device" || time.Until(da.ExpiresAt) < 29*time.Minute {
		t.Errorf("device auth = %+v", da)
	}
	if scopes := r.fake.Scopes(); len(scopes) != 1 || scopes[0] != "https://www.googleapis.com/auth/drive.file" {
		t.Errorf("asked for the scopes %v, want drive.file alone", scopes)
	}
	r.noSecrets("the device auth as printed", fmt.Sprintf("%+v %v %#v %s", *da, da, da, *da))
	if creds, err := r.client.Credentials(); err != nil || strings.Contains(fmt.Sprintf("%+v %v %#v", creds, &creds, creds), drivetest.ClientSecret) {
		t.Errorf("the credentials as printed carry the client secret (%v)", err)
	}

	// The user takes a moment, and Google asks the client to slow down once.
	r.fake.SlowDown(1)
	done := make(chan error, 1)
	go func() { done <- r.client.AwaitDeviceAuth(ctx(t), da) }()
	select {
	case err := <-done:
		t.Fatalf("the wait ended before the user approved: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if r.client.Connected() {
		t.Fatal("connected before the user approved")
	}
	r.fake.Approve(da.UserCode)
	if err := <-done; err != nil {
		t.Fatalf("AwaitDeviceAuth: %v", err)
	}

	if !r.client.Connected() {
		t.Fatal("not connected after the sign-in")
	}
	info, err := os.Stat(r.tokenPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v, mode %v; want owner-only", err, info.Mode().Perm())
	}
	acct, err := r.client.About(ctx(t))
	if err != nil || acct.Name != "Test User" || acct.Email != "user@example.com" {
		t.Errorf("About = %+v, %v", acct, err)
	}
}

func TestSignInDeclinedExpiredCancelled(t *testing.T) {
	r := newRig(t)
	start := func() *drive.DeviceAuth {
		da, err := r.client.StartDeviceAuth(ctx(t))
		if err != nil {
			t.Fatalf("StartDeviceAuth: %v", err)
		}
		return da
	}
	da := start()
	r.fake.Deny(da.UserCode)
	if err := r.client.AwaitDeviceAuth(ctx(t), da); !errors.Is(err, drive.ErrAuthDenied) {
		t.Errorf("declined: %v", err)
	}
	da = start()
	r.fake.Expire(da.UserCode)
	if err := r.client.AwaitDeviceAuth(ctx(t), da); !errors.Is(err, drive.ErrAuthExpired) {
		t.Errorf("expired at Google: %v", err)
	}
	// The code's own lifetime ends the wait too, without asking Google.
	da = start()
	da.ExpiresAt = time.Now().Add(-time.Second)
	if err := r.client.AwaitDeviceAuth(ctx(t), da); !errors.Is(err, drive.ErrAuthExpired) {
		t.Errorf("expired here: %v", err)
	}
	da = start()
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.client.AwaitDeviceAuth(short, da); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancelled: %v", err)
	}
	if r.client.Connected() {
		t.Error("connected although no sign-in was approved")
	}
	if _, err := os.Stat(r.tokenPath); err == nil {
		t.Error("a token file exists although no sign-in was approved")
	}

	// Without a client file there is nothing to sign in with.
	if err := os.Remove(r.credsPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.StartDeviceAuth(ctx(t)); !errors.Is(err, drive.ErrNoClient) {
		t.Errorf("sign-in without a client file: %v", err)
	}
}

// TestAccessTokenIsRefreshed: an access token Google no longer takes is
// replaced without anyone noticing; a sign-in Google has ended is reported as
// such and forgotten.
func TestAccessTokenIsRefreshed(t *testing.T) {
	r := newRig(t)
	r.signIn()
	if _, err := r.client.About(ctx(t)); err != nil {
		t.Fatalf("About: %v", err)
	}
	if n := r.fake.CountRequests("POST /token"); n != 1 {
		t.Fatalf("%d token requests after a sign-in and a call, want the sign-in's one", n)
	}

	r.fake.ExpireAccessTokens()
	if _, err := r.client.About(ctx(t)); err != nil {
		t.Fatalf("About with an expired access token: %v", err)
	}
	if n := r.fake.CountRequests("POST /token"); n != 2 {
		t.Errorf("%d token requests, want one refresh", n)
	}
	// The fresh token is kept: the next call needs no refresh.
	if _, err := r.client.About(ctx(t)); err != nil || r.fake.CountRequests("POST /token") != 2 {
		t.Errorf("the refreshed token was not kept: %v, %d token requests", err, r.fake.CountRequests("POST /token"))
	}
	if info, err := os.Stat(r.tokenPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file after a refresh: %v, %v", err, info.Mode().Perm())
	}

	r.fake.RevokeAll()
	_, err := r.client.About(ctx(t))
	if !errors.Is(err, drive.ErrNotConnected) || !strings.Contains(err.Error(), "expired or was revoked") {
		t.Fatalf("About after the access was removed: %v", err)
	}
	r.noSecrets("the error", err.Error())
	if r.client.Connected() {
		t.Error("still connected after Google ended the sign-in")
	}

	// A sign-in belongs to its OAuth client.
	r.signIn()
	if err := os.WriteFile(r.credsPath, []byte(`{"client_id": "another", "client_secret": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.client.Connected() {
		t.Error("connected with a sign-in made for another OAuth client")
	}
	if _, err := r.client.About(ctx(t)); !errors.Is(err, drive.ErrNotConnected) {
		t.Errorf("About with another OAuth client: %v", err)
	}
}

func TestDisconnect(t *testing.T) {
	r := newRig(t)
	if had, err := r.client.Disconnect(ctx(t)); had || err != nil {
		t.Errorf("Disconnect without a sign-in = %v, %v", had, err)
	}
	r.signIn()
	if had, err := r.client.Disconnect(ctx(t)); !had || err != nil {
		t.Fatalf("Disconnect = %v, %v", had, err)
	}
	if _, err := os.Stat(r.tokenPath); err == nil || r.client.Connected() {
		t.Error("the sign-in is still on disk")
	}
	if r.fake.CountRequests("POST /revoke") != 1 {
		t.Error("Google was not told to revoke the sign-in")
	}
	// Revoked at Google: a fresh sign-in is needed, the old token is dead.
	r.signIn()
	r.fake.RevokeAll()
	if had, err := r.client.Disconnect(ctx(t)); !had || err != nil {
		t.Errorf("Disconnect of a sign-in Google already ended = %v, %v", had, err)
	}
}

// TestEnsureFolder: a path of folder names is created once, level by level,
// and found again after that.
func TestEnsureFolder(t *testing.T) {
	r := newRig(t)
	r.signIn()
	if id, err := r.client.EnsureFolder(ctx(t), ""); err != nil || id != "root" {
		t.Fatalf("the top level = %q, %v", id, err)
	}
	if len(r.fake.Files()) != 0 {
		t.Fatal("the top level needed a folder")
	}
	id, err := r.client.EnsureFolder(ctx(t), "Music/ Songs /")
	if err != nil {
		t.Fatalf("EnsureFolder: %v", err)
	}
	files := r.fake.Files()
	if len(files) != 2 || !files[1].IsFolder() || files[1].ID != id || r.fake.Path(files[1]) != "Music/Songs" {
		t.Fatalf("folders = %+v (id %q)", files, id)
	}
	again, err := r.client.EnsureFolder(ctx(t), "Music/Songs")
	if err != nil || again != id || len(r.fake.Files()) != 2 {
		t.Errorf("the second time: id %q (%v), %d folders; want the same id and no new folder", again, err, len(r.fake.Files()))
	}
	// A sibling under the same parent, with a name that needs quoting.
	odd, err := r.client.EnsureFolder(ctx(t), `Music/Mia's \ takes`)
	if err != nil || odd == id {
		t.Fatalf("a quoted name: %q, %v", odd, err)
	}
	if same, _ := r.client.EnsureFolder(ctx(t), `Music/Mia's \ takes`); same != odd || len(r.fake.Files()) != 3 {
		t.Errorf("the quoted name is not found again: %q vs %q, %d folders", same, odd, len(r.fake.Files()))
	}
}

// upload sends the payload and checks the file that arrives.
func (r *rig) upload(path string, want []byte, req drive.UploadRequest) drive.File {
	r.t.Helper()
	req.Path = path
	if req.Name == "" {
		req.Name = "Night Drive.flac"
	}
	file, err := r.client.Upload(ctx(r.t), req)
	if err != nil {
		r.t.Fatalf("Upload: %v", err)
	}
	var stored *drivetest.File
	for _, f := range r.fake.Files() {
		if f.ID == file.ID {
			stored = &f
		}
	}
	if stored == nil {
		r.t.Fatalf("the Drive holds no file %q", file.ID)
	}
	if !bytes.Equal(stored.Data, want) {
		r.t.Fatalf("the file in the Drive is not the local one (%d bytes, want %d)", len(stored.Data), len(want))
	}
	if file.Size != int64(len(want)) || file.Name != req.Name || !strings.Contains(file.Link, file.ID) {
		r.t.Errorf("the answer = %+v", file)
	}
	return file
}

// uploads counts the files (not folders) in the Drive.
func (r *rig) uploads() int {
	n := 0
	for _, f := range r.fake.Files() {
		if !f.IsFolder() {
			n++
		}
	}
	return n
}

// TestUpload sends a file in chunks into a folder and checks what the Drive
// holds: the bytes, the name, the type, the description, the place.
func TestUpload(t *testing.T) {
	r := newRig(t)
	path, data := payload(t, 3*chunk+12345)
	if _, err := r.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x"}); !errors.Is(err, drive.ErrNotConnected) {
		t.Fatalf("Upload before the sign-in: %v", err)
	}
	r.signIn()
	folder, err := r.client.EnsureFolder(ctx(t), "Songs")
	if err != nil {
		t.Fatal(err)
	}
	var progress []int64
	file := r.upload(path, data, drive.UploadRequest{
		MIMEType: "audio/flac", FolderID: folder, Description: "a take",
		Progress: func(sent, total int64) {
			if total != int64(len(data)) {
				t.Errorf("progress total = %d", total)
			}
			progress = append(progress, sent)
		},
	})
	if n := r.fake.CountRequests("PUT /upload/drive/v3/files"); n != 4 {
		t.Errorf("%d upload requests, want 4 chunks", n)
	}
	if fmt.Sprint(progress) != fmt.Sprint([]int64{chunk, 2 * chunk, 3 * chunk, int64(len(data))}) {
		t.Errorf("progress = %v", progress)
	}
	stored := r.fake.Files()[1]
	if stored.MIMEType != "audio/flac" || stored.Description != "a take" || r.fake.Path(stored) != "Songs/Night Drive.flac" {
		t.Errorf("stored as %q (%s, %q)", r.fake.Path(stored), stored.MIMEType, stored.Description)
	}
	if file.MD5 == "" {
		t.Error("the answer carries no checksum")
	}

	// A file smaller than a chunk goes up in one request, to the top level.
	small, smallData := payload(t, 1000)
	r.upload(small, smallData, drive.UploadRequest{Name: "small.wav"})
	if got := r.fake.Path(r.fake.Files()[2]); got != "small.wav" {
		t.Errorf("the small file is at %q, want the top level", got)
	}
}

// TestUploadResumes makes the connection misbehave in every way it can and
// checks the file arrives whole, once.
func TestUploadResumes(t *testing.T) {
	for name, trouble := range map[string]func(r *rig) func(sent, total int64){
		"server errors": func(r *rig) func(int64, int64) {
			r.fake.FailChunks(503, 500, 502)
			return nil
		},
		"a rate limit": func(r *rig) func(int64, int64) {
			r.fake.FailChunks(429, 403)
			return nil
		},
		"a connection dropped mid-chunk": func(r *rig) func(int64, int64) {
			r.fake.CutNextChunk(100_000)
			return nil
		},
		"a connection dropped after the whole chunk": func(r *rig) func(int64, int64) {
			r.fake.CutNextChunk(chunk)
			return nil
		},
		"a connection gone dead": func(r *rig) func(int64, int64) {
			// The chunk gets no answer; the client gives the request up
			// after its time and carries on.
			r.client = r.newClient(drive.WithRequestTimeouts(10*time.Second, 300*time.Millisecond))
			r.fake.StallNextChunk()
			return nil
		},
		"a chunk taken only in part": func(r *rig) func(int64, int64) {
			r.fake.ShortenNextChunk(1000)
			return nil
		},
		"trouble on a later chunk": func(r *rig) func(int64, int64) {
			return once(func() { r.fake.CutNextChunk(7); r.fake.FailChunks(503) })
		},
		"an access token that expires on the way": func(r *rig) func(int64, int64) {
			return once(r.fake.ExpireAccessTokens)
		},
		"a session Drive forgot": func(r *rig) func(int64, int64) {
			return once(r.fake.DropSessions)
		},
		"a session Drive forgot while the connection was down": func(r *rig) func(int64, int64) {
			return once(func() { r.fake.FailChunks(503); r.fake.DropSessions() })
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.signIn()
			path, data := payload(t, 3*chunk+12345)
			r.upload(path, data, drive.UploadRequest{Progress: trouble(r)})
			if n := r.uploads(); n != 1 {
				t.Errorf("the Drive holds %d files, want the one", n)
			}
		})
	}
}

// once returns a progress callback that runs f after the first chunk.
func once(f func()) func(sent, total int64) {
	done := false
	return func(int64, int64) {
		if !done {
			done = true
			f()
		}
	}
}

// TestUploadGivesUp: what is not worth another try fails at once, what is
// fails after a bounded number of tries, and no error tells a secret.
func TestUploadGivesUp(t *testing.T) {
	r := newRig(t)
	r.signIn()
	path, _ := payload(t, 2*chunk)

	r.fake.FailChunks(400)
	_, err := r.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x"})
	var ae *drive.APIError
	if !errors.As(err, &ae) || ae.Status != 400 || !strings.Contains(err.Error(), "stand-in failure") {
		t.Errorf("a refused chunk: %v", err)
	}
	if n := r.fake.CountRequests("PUT /upload/drive/v3/files"); n != 1 {
		t.Errorf("a refused chunk was sent %d times", n)
	}

	fails := make([]int, 40)
	for i := range fails {
		fails[i] = 503
	}
	r.fake.FailChunks(fails...)
	_, err = r.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "times in a row") {
		t.Fatalf("an upload that never gets through: %v", err)
	}
	r.noSecrets("the error", err.Error())
	if strings.Contains(err.Error(), "upload_id") {
		t.Errorf("the error names the upload session: %v", err)
	}
	if r.uploads() != 0 {
		t.Error("a failed upload left a file in the Drive")
	}

	// A folder that is not there (or not the daemon's to see). A fresh
	// stand-in: the one above still has failures queued.
	fresh := newRig(t)
	fresh.signIn()
	if _, err := fresh.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x", FolderID: "gone"}); !errors.As(err, &ae) || ae.Status != 404 {
		t.Errorf("an upload into a missing folder: %v", err)
	}
	// A file damaged on the way is reported, not passed off as uploaded.
	fresh.fake.DamageNextUpload()
	if _, err := fresh.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x"}); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("a damaged upload: %v", err)
	}
	// Nothing to send.
	empty := filepath.Join(t.TempDir(), "empty.flac")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.client.Upload(ctx(t), drive.UploadRequest{Path: empty, Name: "x"}); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("an empty file: %v", err)
	}
	if _, err := fresh.client.Upload(ctx(t), drive.UploadRequest{Path: empty + ".absent", Name: "x"}); err == nil {
		t.Error("a missing file was uploaded")
	}
}

// TestUploadStopsWhenCancelled: a cancelled upload ends at once, also while
// it waits to try again.
func TestUploadStopsWhenCancelled(t *testing.T) {
	r := newRig(t)
	r.signIn()
	slow := drive.New(r.credsPath, r.tokenPath,
		drive.WithEndpoints(drive.EndpointsAt(r.fake.URL)), drive.WithChunkSize(chunk),
		drive.WithBackoff(func(int) time.Duration { return time.Hour }))
	path, _ := payload(t, 2*chunk)
	r.fake.FailChunks(503)
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := slow.Upload(c, drive.UploadRequest{Path: path, Name: "x"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("a cancelled upload: %v after %v", err, time.Since(start))
	}
}

// TestUploadSessionMustStayOnTheUploadHost: the session URI is where the file
// and the access token go next, so one that points elsewhere is refused.
func TestUploadSessionMustStayOnTheUploadHost(t *testing.T) {
	r := newRig(t)
	r.signIn()
	var elsewhere []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		elsewhere = append(elsewhere, req.Method+" "+req.Header.Get("Authorization"))
	}))
	defer other.Close()
	// A proxy in front of the stand-in that rewrites the session URI.
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fwd, _ := http.NewRequest(req.Method, r.fake.URL+req.URL.RequestURI(), req.Body)
		fwd.Header = req.Header.Clone()
		resp, err := http.DefaultClient.Do(fwd)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if loc := resp.Header.Get("Location"); loc != "" {
			w.Header().Set("Location", other.URL+"/steal?upload_id=1")
		}
		w.WriteHeader(resp.StatusCode)
	}))
	defer front.Close()
	ends := drive.EndpointsAt(r.fake.URL)
	ends.Upload = front.URL + "/upload/drive/v3"
	c := drive.New(r.credsPath, r.tokenPath, drive.WithEndpoints(ends), drive.WithChunkSize(chunk))
	path, _ := payload(t, 1000)
	_, err := c.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "no usable upload session") {
		t.Errorf("an upload session on another host: %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("the client followed it: %v", elsewhere)
	}
}

// TestFolderUsable: a folder the daemon made stays usable under its id when
// the user renames or moves it, and stops being usable in the bin. What the
// user made by hand is not the daemon's to see at all.
func TestFolderUsable(t *testing.T) {
	r := newRig(t)
	r.signIn()
	usable := func(id string) bool {
		t.Helper()
		ok, err := r.client.FolderUsable(ctx(t), id)
		if err != nil {
			t.Fatalf("FolderUsable(%q): %v", id, err)
		}
		return ok
	}
	// A folder of the same name made by hand: not found, a second one is made.
	theirs := r.fake.AddFolder("Songs", "root")
	id, err := r.client.EnsureFolder(ctx(t), "Songs")
	if err != nil || id == theirs {
		t.Fatalf("EnsureFolder = %q, %v; want a folder of the daemon's own beside the user's %q", id, err, theirs)
	}
	if !usable("root") || !usable(id) || usable(theirs) || usable("no-such-id") || usable("") {
		t.Errorf("usable: root %v, own %v, the user's %v, unknown %v, empty %v", usable("root"), usable(id), usable(theirs), usable("no-such-id"), usable(""))
	}
	// A file is not a folder.
	path, _ := payload(t, 100)
	file, err := r.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x", FolderID: id})
	if err != nil || usable(file.ID) {
		t.Errorf("a file counts as a usable folder (%v)", err)
	}
	// An upload into the user's own folder is refused, as Drive refuses it.
	var ae *drive.APIError
	if _, err := r.client.Upload(ctx(t), drive.UploadRequest{Path: path, Name: "x", FolderID: theirs}); !errors.As(err, &ae) || ae.Status != 404 {
		t.Errorf("an upload into a folder the daemon cannot see: %v", err)
	}

	// Renamed and moved, it is the same folder; by name it is no longer found.
	r.fake.Rename(id, "Takes")
	r.fake.Move(id, theirs)
	if !usable(id) {
		t.Error("a renamed and moved folder is no longer usable")
	}
	if again, err := r.client.EnsureFolder(ctx(t), "Songs"); err != nil || again == id || again == theirs {
		t.Errorf("EnsureFolder after the move = %q, %v; want a new folder", again, err)
	}
	// In the bin it is of no use, and is not found by name either.
	r.fake.Rename(id, "Songs")
	r.fake.Move(id, "root")
	r.fake.Trash(id)
	if usable(id) {
		t.Error("a folder in the bin is usable")
	}
	// An ended sign-in is an error, not "unusable": not knowing must not
	// make a caller create a second folder.
	r.fake.RevokeAll()
	if _, err := r.client.FolderUsable(ctx(t), id); !errors.Is(err, drive.ErrNotConnected) {
		t.Errorf("FolderUsable without a sign-in: %v", err)
	}
}
