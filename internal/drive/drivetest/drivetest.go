// Package drivetest is a stand-in for Google in tests: the OAuth endpoints of
// a device sign-in and the corner of the Drive API the daemon uses, with the
// rules of the real ones (a valid access token on every call, chunks in
// multiples of 256 KiB, an upload that resumes where the session stands) and
// switches to make it misbehave the way a network does.
//
// It serves everything under one base URL at Google's paths; point a client
// at it with drive.EndpointsAt(server.URL).
package drivetest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// ClientID and ClientSecret are the OAuth client the server knows.
	ClientID     = "test-client.apps.example"
	ClientSecret = "test-client-secret-5e7"

	folderMIME = "application/vnd.google-apps.folder"
	chunkUnit  = 256 << 10
)

// File is a file (or folder) in the stand-in Drive.
type File struct {
	ID          string
	Name        string
	MIMEType    string
	Description string
	Parent      string
	Data        []byte
}

// IsFolder reports whether the file is a folder.
func (f File) IsFolder() bool { return f.MIMEType == folderMIME }

type device struct {
	userCode string
	state    string // "pending", "approved", "denied", "expired"
}

type session struct {
	meta  File
	total int64
	data  []byte
}

// Server is the stand-in. Its exported fields may be set before use.
type Server struct {
	*httptest.Server
	t testing.TB

	// Account is what about.get reports.
	AccountName, AccountEmail string

	mu       sync.Mutex
	seq      int
	devices  map[string]*device // by device code
	refresh  map[string]bool    // valid refresh tokens
	access   map[string]bool    // valid access tokens
	files    []File
	sessions map[string]*session

	// faults
	slowDown     int
	chunkFails   []int
	cutAfter     int64 // > 0: the next chunk stops after this many bytes and the connection drops
	stallChunk   bool  // the next chunk gets no answer until its sender gives up
	shortBy      int64 // > 0: the next chunk is acknowledged this many bytes short
	damageUpload bool

	scopes   []string
	secrets  []string // every token and code handed out
	leaks    []string
	requests []string
}

// New starts a stand-in that stops when the test ends.
func New(t testing.TB) *Server {
	s := &Server{
		t:           t,
		AccountName: "Test User", AccountEmail: "user@example.com",
		devices: map[string]*device{}, refresh: map[string]bool{}, access: map[string]bool{},
		sessions: map[string]*session{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/code", s.handleDeviceCode)
	mux.HandleFunc("POST /token", s.handleToken)
	mux.HandleFunc("POST /revoke", s.handleRevoke)
	mux.HandleFunc("GET /drive/v3/about", s.authed(s.handleAbout))
	mux.HandleFunc("GET /drive/v3/files", s.authed(s.handleList))
	mux.HandleFunc("POST /drive/v3/files", s.authed(s.handleCreate))
	mux.HandleFunc("POST /upload/drive/v3/files", s.authed(s.handleUploadStart))
	mux.HandleFunc("PUT /upload/drive/v3/files", s.authed(s.handleUploadChunk))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		// A secret belongs in a header or a form body, never in a URL.
		for _, secret := range s.secrets {
			if strings.Contains(r.URL.String(), secret) {
				s.leaks = append(s.leaks, r.Method+" "+r.URL.Path+" carries a secret in its URL")
			}
		}
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// WriteClientFile writes the OAuth client file the server accepts, in the
// form the Google Cloud console offers for download.
func WriteClientFile(t testing.TB, path string) {
	t.Helper()
	doc := fmt.Sprintf(`{"installed": {"client_id": %q, "project_id": "test", "client_secret": %q}}`, ClientID, ClientSecret)
	if err := writeFile(path, doc); err != nil {
		t.Fatalf("write client file: %v", err)
	}
}

func (s *Server) next(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", prefix, s.seq)
}

// secret makes a token or code and remembers it, to spot it where it does
// not belong.
func (s *Server) secret(prefix string) string {
	v := s.next(prefix) + "-s3cr3t"
	s.secrets = append(s.secrets, v)
	return v
}

// --- what a test does to it -------------------------------------------------

// UserCodes returns the codes of the sign-ins waiting for the user.
func (s *Server) UserCodes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, d := range s.devices {
		if d.state == "pending" {
			out = append(out, d.userCode)
		}
	}
	return out
}

func (s *Server) setState(userCode, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.state = state
			return
		}
	}
	s.t.Errorf("drivetest: no sign-in with the user code %q", userCode)
}

// Approve is the user entering the code and approving the sign-in.
func (s *Server) Approve(userCode string) { s.setState(userCode, "approved") }

// Deny is the user declining it.
func (s *Server) Deny(userCode string) { s.setState(userCode, "denied") }

// Expire lets the code run out.
func (s *Server) Expire(userCode string) { s.setState(userCode, "expired") }

// SlowDown answers the next n polls of a sign-in with slow_down.
func (s *Server) SlowDown(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowDown = n
}

// ExpireAccessTokens makes every access token handed out so far invalid, as
// the passing of an hour does.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]bool{}
}

// RevokeAll ends every sign-in, as the user removing the app's access does.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access, s.refresh = map[string]bool{}, map[string]bool{}
}

// FailChunks answers the next upload requests with these statuses, one each,
// without taking their bytes.
func (s *Server) FailChunks(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkFails = append(s.chunkFails, statuses...)
}

// CutNextChunk takes only the first n bytes of the next chunk and then drops
// the connection without an answer.
func (s *Server) CutNextChunk(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cutAfter = n
}

// StallNextChunk leaves the next chunk without an answer, as a connection
// gone dead does, until its sender gives up.
func (s *Server) StallNextChunk() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stallChunk = true
}

// ShortenNextChunk keeps the next chunk n bytes short of what was sent and
// says so in its answer.
func (s *Server) ShortenNextChunk(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shortBy = n
}

// DropSessions forgets every upload session, as a week of waiting does.
func (s *Server) DropSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]*session{}
}

// DamageNextUpload stores the next completed upload with a flipped byte.
func (s *Server) DamageNextUpload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.damageUpload = true
}

// --- what a test asks it ----------------------------------------------------

// Files returns everything in the Drive, folders included, in the order it
// was created.
func (s *Server) Files() []File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]File(nil), s.files...)
}

// Path returns a file's place in the Drive: the names of its folders and its
// own, joined by "/".
func (s *Server) Path(f File) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := f.Name
	for parent := f.Parent; parent != "root"; {
		found := false
		for _, p := range s.files {
			if p.ID == parent {
				path, parent, found = p.Name+"/"+path, p.Parent, true
				break
			}
		}
		if !found {
			return "?/" + path
		}
	}
	return path
}

// Scopes returns the scopes sign-ins asked for.
func (s *Server) Scopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.scopes...)
}

// Secrets returns every token and code the server handed out, and the client
// secret: what must never show up in a log or a tool's answer.
func (s *Server) Secrets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{ClientSecret}, s.secrets...)
}

// Leaks returns the requests that carried a secret where it does not belong.
func (s *Server) Leaks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.leaks...)
}

// Requests returns the requests so far, as "METHOD /path".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// CountRequests counts the requests so far that are exactly "METHOD /path".
func (s *Server) CountRequests(request string) int {
	n := 0
	for _, r := range s.Requests() {
		if r == request {
			n++
		}
	}
	return n
}

// --- OAuth -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": "stand-in: " + code})
}

func apiError(w http.ResponseWriter, status int, reason, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": status, "message": message, "errors": []map[string]string{{"reason": reason, "message": message}},
	}})
}

func (s *Server) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != ClientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, r.FormValue("scope"))
	code := s.secret("device")
	d := &device{userCode: fmt.Sprintf("ABCD-%04d", s.seq), state: "pending"}
	s.devices[code] = d
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code": code, "user_code": d.userCode,
		"verification_url": s.URL + "/device", "expires_in": 1800, "interval": 5,
	})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != ClientID || r.FormValue("client_secret") != ClientSecret {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grant := func(refresh string) {
		access := s.secret("access")
		s.access[access] = true
		doc := map[string]any{"access_token": access, "expires_in": 3599, "token_type": "Bearer", "scope": "https://www.googleapis.com/auth/drive.file"}
		if refresh != "" {
			doc["refresh_token"] = refresh
		}
		writeJSON(w, http.StatusOK, doc)
	}
	switch r.FormValue("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		d := s.devices[r.FormValue("device_code")]
		switch {
		case d == nil:
			oauthError(w, http.StatusBadRequest, "invalid_grant")
		case s.slowDown > 0:
			s.slowDown--
			oauthError(w, http.StatusForbidden, "slow_down")
		case d.state == "pending":
			oauthError(w, http.StatusPreconditionRequired, "authorization_pending")
		case d.state == "denied":
			oauthError(w, http.StatusForbidden, "access_denied")
		case d.state == "expired":
			oauthError(w, http.StatusBadRequest, "expired_token")
		default:
			delete(s.devices, r.FormValue("device_code"))
			refresh := s.secret("refresh")
			s.refresh[refresh] = true
			grant(refresh)
		}
	case "refresh_token":
		if !s.refresh[r.FormValue("refresh_token")] {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		grant("")
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := r.FormValue("token")
	if !s.refresh[tok] {
		oauthError(w, http.StatusBadRequest, "invalid_token")
		return
	}
	delete(s.refresh, tok)
	s.access = map[string]bool{}
	w.WriteHeader(http.StatusOK)
}

// authed lets a request through when it carries a valid access token.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		valid := ok && s.access[tok]
		s.mu.Unlock()
		if !valid {
			apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
			return
		}
		next(w, r)
	}
}

// --- Drive -------------------------------------------------------------------

func (s *Server) handleAbout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"displayName": s.AccountName, "emailAddress": s.AccountEmail}})
}

// folderQuery is the one search the daemon makes: a folder by name under a
// parent.
var folderQuery = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)' and mimeType = '([^']*)' and '([^']*)' in parents and trashed = false$`)

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	m := folderQuery.FindStringSubmatch(r.URL.Query().Get("q"))
	if m == nil {
		apiError(w, http.StatusBadRequest, "invalid", "Invalid Value: q")
		return
	}
	name := strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(m[1])
	s.mu.Lock()
	defer s.mu.Unlock()
	found := []map[string]string{}
	for _, f := range s.files {
		if f.Name == name && f.MIMEType == m[2] && f.Parent == m[3] {
			found = append(found, map[string]string{"id": f.ID})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": found})
}

// metadata is the file resource a client sends.
type metadata struct {
	Name        string   `json:"name"`
	MIMEType    string   `json:"mimeType"`
	Description string   `json:"description"`
	Parents     []string `json:"parents"`
}

func (m metadata) file() File {
	f := File{Name: m.Name, MIMEType: m.MIMEType, Description: m.Description, Parent: "root"}
	if len(m.Parents) > 0 {
		f.Parent = m.Parents[0]
	}
	return f
}

// parentExists reports whether a parent id is the root or a known folder.
// Call with the lock held.
func (s *Server) parentExists(id string) bool {
	if id == "root" {
		return true
	}
	for _, f := range s.files {
		if f.ID == id && f.IsFolder() {
			return true
		}
	}
	return false
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var m metadata
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.Name == "" {
		apiError(w, http.StatusBadRequest, "badRequest", "Bad Request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := m.file()
	if !s.parentExists(f.Parent) {
		apiError(w, http.StatusNotFound, "notFound", "File not found: "+f.Parent)
		return
	}
	f.ID = s.next("file")
	s.files = append(s.files, f)
	writeJSON(w, http.StatusOK, map[string]string{"id": f.ID, "name": f.Name})
}

func (s *Server) handleUploadStart(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("uploadType") != "resumable" {
		apiError(w, http.StatusBadRequest, "badRequest", "only resumable uploads here")
		return
	}
	var m metadata
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.Name == "" {
		apiError(w, http.StatusBadRequest, "badRequest", "Bad Request")
		return
	}
	total, err := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "badRequest", "no X-Upload-Content-Length")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := m.file()
	if !s.parentExists(f.Parent) {
		apiError(w, http.StatusNotFound, "notFound", "File not found: "+f.Parent)
		return
	}
	id := s.next("upload")
	s.sessions[id] = &session{meta: f, total: total}
	w.Header().Set("Location", s.URL+"/upload/drive/v3/files?uploadType=resumable&upload_id="+id)
	w.WriteHeader(http.StatusOK)
}

var contentRange = regexp.MustCompile(`^bytes (?:(\d+)-(\d+)|\*)/(\d+)$`)

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[r.URL.Query().Get("upload_id")]
	if sess == nil {
		apiError(w, http.StatusNotFound, "notFound", "upload session not found")
		return
	}
	m := contentRange.FindStringSubmatch(r.Header.Get("Content-Range"))
	if m == nil {
		apiError(w, http.StatusBadRequest, "badRequest", "no Content-Range")
		return
	}
	if total, _ := strconv.ParseInt(m[3], 10, 64); total != sess.total {
		apiError(w, http.StatusBadRequest, "badRequest", "the total changed")
		return
	}
	incomplete := func() {
		if len(sess.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(sess.data)-1))
		}
		w.WriteHeader(http.StatusPermanentRedirect)
	}
	if m[1] == "" { // a status query
		if int64(len(sess.data)) == sess.total {
			s.complete(w, r, sess)
			return
		}
		incomplete()
		return
	}
	if s.stallChunk {
		s.stallChunk = false
		s.mu.Unlock()
		// Take the chunk off the wire and say nothing. Once the body is
		// read the server notices its sender hanging up; the timer only keeps
		// a sender that never does from holding the test.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		s.mu.Lock() // for the deferred unlock
		return
	}
	if len(s.chunkFails) > 0 {
		status := s.chunkFails[0]
		s.chunkFails = s.chunkFails[1:]
		_, _ = io.Copy(io.Discard, r.Body)
		reason := "backendError"
		if status == http.StatusForbidden {
			reason = "userRateLimitExceeded"
		}
		apiError(w, status, reason, "stand-in failure")
		return
	}
	first, _ := strconv.ParseInt(m[1], 10, 64)
	last, _ := strconv.ParseInt(m[2], 10, 64)
	body, err := io.ReadAll(r.Body)
	switch {
	case err != nil:
		return
	case first != int64(len(sess.data)):
		apiError(w, http.StatusBadRequest, "badRequest", fmt.Sprintf("chunk starts at %d, the session holds %d", first, len(sess.data)))
		return
	case int64(len(body)) != last-first+1:
		apiError(w, http.StatusBadRequest, "badRequest", "Content-Range does not match the body")
		return
	case last+1 != sess.total && len(body)%chunkUnit != 0:
		apiError(w, http.StatusBadRequest, "badRequest", "a chunk must be a multiple of 256 KiB")
		return
	}
	if s.cutAfter > 0 {
		sess.data = append(sess.data, body[:min(s.cutAfter, int64(len(body)))]...)
		s.cutAfter = 0
		panic(http.ErrAbortHandler) // the connection drops; the deferred unlock still runs
	}
	if s.shortBy > 0 {
		body = body[:max(0, int64(len(body))-s.shortBy)]
		s.shortBy = 0
	}
	sess.data = append(sess.data, body...)
	if int64(len(sess.data)) < sess.total {
		incomplete()
		return
	}
	s.complete(w, r, sess)
}

// complete turns a full session into a file and answers with it. Call with
// the lock held.
func (s *Server) complete(w http.ResponseWriter, r *http.Request, sess *session) {
	f := sess.meta
	f.ID = s.next("file")
	f.Data = sess.data
	if s.damageUpload {
		s.damageUpload = false
		f.Data = append([]byte(nil), f.Data...)
		f.Data[0] ^= 0xff
	}
	s.files = append(s.files, f)
	delete(s.sessions, r.URL.Query().Get("upload_id"))
	sum := md5.Sum(f.Data)
	writeJSON(w, http.StatusOK, map[string]string{
		"id": f.ID, "name": f.Name, "md5Checksum": hex.EncodeToString(sum[:]),
		"size": strconv.Itoa(len(f.Data)), "webViewLink": "https://drive.example/file/d/" + f.ID + "/view",
	})
}
