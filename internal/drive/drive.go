// Package drive puts files into the user's Google Drive: the OAuth sign-in of
// a device without a browser, the access token kept fresh from then on, and a
// resumable upload that picks up where a dropped connection left it.
//
// The OAuth client is the user's own (a client id and secret from their Google
// Cloud project, read from a file in the config dir). The daemon asks for one
// scope, drive.file: it can create files and see the files it created, and
// nothing else in the Drive.
//
// What the sign-in yields is a secret. It lives in one file in the state dir,
// readable by its owner only, and leaves this package in an Authorization
// header and in no other way: no error, log line or return value carries a
// token, the client secret or the device code.
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
)

// Scope is the one scope the daemon asks for: per-file access to the files it
// creates. It is the narrowest scope that can create a file, and one of the
// few a device sign-in may ask for.
const Scope = "https://www.googleapis.com/auth/drive.file"

// folderMIME marks a Drive file as a folder.
const folderMIME = "application/vnd.google-apps.folder"

// Endpoints are the URLs the client talks to.
type Endpoints struct {
	// DeviceCode starts a device sign-in, Token trades its code (and later the
	// refresh token) for an access token, Revoke ends a sign-in.
	DeviceCode, Token, Revoke string
	// API is the Drive API's base, Upload the base of its upload endpoints.
	API, Upload string
}

// GoogleEndpoints are Google's.
func GoogleEndpoints() Endpoints {
	return Endpoints{
		DeviceCode: "https://oauth2.googleapis.com/device/code",
		Token:      "https://oauth2.googleapis.com/token",
		Revoke:     "https://oauth2.googleapis.com/revoke",
		API:        "https://www.googleapis.com/drive/v3",
		Upload:     "https://www.googleapis.com/upload/drive/v3",
	}
}

// EndpointsAt are the endpoints of a server that hosts all of them under one
// base URL, at Google's paths: a stand-in for tests.
func EndpointsAt(base string) Endpoints {
	base = strings.TrimSuffix(base, "/")
	return Endpoints{
		DeviceCode: base + "/device/code",
		Token:      base + "/token",
		Revoke:     base + "/revoke",
		API:        base + "/drive/v3",
		Upload:     base + "/upload/drive/v3",
	}
}

// Credentials are the user's OAuth client.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// String keeps the secret out of anything that prints the credentials.
func (c Credentials) String() string { return "OAuth client " + c.ClientID }

// GoString does the same for %#v.
func (c Credentials) GoString() string { return c.String() }

// ErrNoClient: there is no OAuth client file yet.
var ErrNoClient = errors.New("no Google OAuth client")

// ErrNotConnected: the user has not signed the daemon in (or the sign-in has
// ended).
var ErrNotConnected = errors.New("not connected to Google Drive")

// LoadCredentials reads an OAuth client file: the JSON the Google Cloud
// console offers for download ({"installed": {"client_id", "client_secret"}})
// or the two keys on their own.
func LoadCredentials(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, fmt.Errorf("%w: %s does not exist", ErrNoClient, path)
		}
		return Credentials{}, fmt.Errorf("%w: %v", ErrNoClient, err)
	}
	type keys struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	var doc struct {
		keys
		Installed *keys `json:"installed"`
		Web       *keys `json:"web"`
	}
	// A parse error is reported without its text: it may quote the file.
	if err := json.Unmarshal(data, &doc); err != nil {
		return Credentials{}, fmt.Errorf("%w: %s is not JSON", ErrNoClient, path)
	}
	k := doc.keys
	switch {
	case doc.Installed != nil:
		k = *doc.Installed
	case doc.Web != nil:
		k = *doc.Web
	}
	if k.ClientID == "" || k.ClientSecret == "" {
		return Credentials{}, fmt.Errorf("%w: %s holds no client_id and client_secret", ErrNoClient, path)
	}
	return Credentials(k), nil
}

// Client talks to Google Drive as the signed-in user.
type Client struct {
	endpoints Endpoints
	http      *http.Client
	credsPath string
	tokenPath string
	chunkSize int64
	backoff   func(failures int) time.Duration
	pollEvery time.Duration
	// callTimeout bounds one small request (a token, a lookup), chunkTimeout
	// one request that carries a chunk of a file.
	callTimeout, chunkTimeout time.Duration
	now                       func() time.Time

	// mu serializes everything that reads or replaces the token file.
	mu sync.Mutex
}

// Option adjusts a Client.
type Option func(*Client)

// WithEndpoints points the client at other endpoints than Google's.
func WithEndpoints(e Endpoints) Option { return func(c *Client) { c.endpoints = e } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithChunkSize sets how much of a file one upload request carries. Drive
// wants a multiple of 256 KiB.
func WithChunkSize(n int64) Option { return func(c *Client) { c.chunkSize = n } }

// WithBackoff sets how long the client waits before it tries again after the
// given number of failures in a row.
func WithBackoff(f func(failures int) time.Duration) Option {
	return func(c *Client) { c.backoff = f }
}

// WithPollInterval sets how often a sign-in under way asks whether the user
// has approved it, in place of the interval Google names. For tests.
func WithPollInterval(d time.Duration) Option { return func(c *Client) { c.pollEvery = d } }

// WithRequestTimeouts sets how long one request may take: a small one (a
// token, a lookup) and one that carries a chunk of a file. A request that
// runs out of time is tried again like any other that failed.
func WithRequestTimeouts(call, chunk time.Duration) Option {
	return func(c *Client) { c.callTimeout, c.chunkTimeout = call, chunk }
}

// chunkUnit is the granularity Drive asks of every chunk but the last.
const chunkUnit = 256 << 10

// New returns a client that reads the OAuth client from credsPath and keeps
// the sign-in in tokenPath. Both are read when they are needed, so a client
// file put in place later is found without a restart.
func New(credsPath, tokenPath string, opts ...Option) *Client {
	c := &Client{
		endpoints: GoogleEndpoints(),
		// No timeout on the client: each request gets its own (callTimeout,
		// chunkTimeout), on top of its caller's context.
		http:      &http.Client{},
		credsPath: credsPath,
		tokenPath: tokenPath,
		chunkSize: 32 * chunkUnit, // 8 MiB
		// A chunk gets five minutes: enough for 8 MiB on a line of 250 kbit/s,
		// and short enough that a connection gone dead is noticed.
		callTimeout:  time.Minute,
		chunkTimeout: 5 * time.Minute,
		backoff: func(failures int) time.Duration {
			return min(time.Second<<(failures-1), 32*time.Second)
		},
		now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Credentials reads the OAuth client file.
func (c *Client) Credentials() (Credentials, error) { return LoadCredentials(c.credsPath) }

// --- token --------------------------------------------------------------------

// token is the sign-in as kept on disk.
type token struct {
	// ClientID is the OAuth client the sign-in belongs to: a refresh token is
	// of no use with another client.
	ClientID     string    `json:"client_id"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	Scope        string    `json:"scope,omitempty"`
}

// loadToken reads the token file; nil when there is none (or it is unusable).
func (c *Client) loadToken() *token {
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return nil
	}
	var t token
	if json.Unmarshal(data, &t) != nil || t.RefreshToken == "" {
		return nil
	}
	return &t
}

// saveToken writes the token file, readable by its owner only.
func (c *Client) saveToken(t *token) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.tokenPath), 0o700); err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(c.tokenPath, data, 0o600)
}

// Connected reports whether a sign-in for the current OAuth client is on
// disk. It does not ask Google whether the sign-in still holds; About does.
func (c *Client) Connected() bool {
	creds, err := c.Credentials()
	if err != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.loadToken()
	return t != nil && t.ClientID == creds.ClientID
}

// expiryMargin is how long before its end an access token is replaced.
const expiryMargin = time.Minute

// accessToken returns a valid access token, refreshing it when it is about to
// expire or when fresh demands a new one.
func (c *Client) accessToken(ctx context.Context, fresh bool) (string, error) {
	creds, err := c.Credentials()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.loadToken()
	if t == nil || t.ClientID != creds.ClientID {
		return "", ErrNotConnected
	}
	if !fresh && t.AccessToken != "" && c.now().Before(t.Expiry.Add(-expiryMargin)) {
		return t.AccessToken, nil
	}
	var grant tokenGrant
	err = c.postForm(ctx, c.endpoints.Token, url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"refresh_token": {t.RefreshToken},
		"grant_type":    {"refresh_token"},
	}, &grant)
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) && oe.Code == "invalid_grant" {
			// Google no longer honours the refresh token: it was revoked, went
			// unused for months, or the app is in "Testing" and a week is over.
			_ = os.Remove(c.tokenPath)
			return "", fmt.Errorf("%w: Google no longer accepts the sign-in (it expired or was revoked)", ErrNotConnected)
		}
		return "", fmt.Errorf("refresh the Google sign-in: %w", err)
	}
	if grant.AccessToken == "" {
		return "", errors.New("refresh the Google sign-in: the answer holds no access token")
	}
	t.AccessToken = grant.AccessToken
	t.Expiry = c.now().Add(time.Duration(grant.ExpiresIn) * time.Second)
	if grant.RefreshToken != "" {
		t.RefreshToken = grant.RefreshToken
	}
	if err := c.saveToken(t); err != nil {
		return "", fmt.Errorf("keep the Google sign-in: %w", err)
	}
	return t.AccessToken, nil
}

// tokenGrant is the token endpoint's answer.
type tokenGrant struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// OAuthError is an error answer of the OAuth endpoints.
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("Google answered %s: %s", e.Code, e.Description)
	}
	if e.Code != "" {
		return "Google answered " + e.Code
	}
	return fmt.Sprintf("Google answered HTTP %d", e.Status)
}

// maxAnswerBytes bounds how much of an answer is read.
const maxAnswerBytes = 1 << 20

// postForm posts a form to an OAuth endpoint and decodes the answer into out.
// The form carries secrets, so nothing of the request appears in an error.
func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if resp.StatusCode != http.StatusOK {
		oe := &OAuthError{Status: resp.StatusCode}
		var doc struct {
			Error       string `json:"error"`
			ErrorCode   string `json:"error_code"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &doc) == nil {
			oe.Code, oe.Description = doc.Error, doc.Description
			if oe.Code == "" {
				oe.Code = doc.ErrorCode
			}
		}
		return oe
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("the answer from Google is not the JSON expected")
	}
	return nil
}

// transportError strips a failed request down to what went wrong. The error
// net/http returns quotes the URL, which for an upload session is a
// capability of its own.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request failed: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

// --- sign-in --------------------------------------------------------------------

// DeviceAuth is a sign-in under way: the user opens VerificationURL on any
// device, enters UserCode and approves.
type DeviceAuth struct {
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time

	clientID   string
	deviceCode string
	interval   time.Duration
}

// String keeps the device code, which only the daemon may hold, out of
// anything that prints the sign-in.
func (d DeviceAuth) String() string {
	return fmt.Sprintf("sign-in with the code %s at %s", d.UserCode, d.VerificationURL)
}

// GoString does the same for %#v.
func (d DeviceAuth) GoString() string { return d.String() }

// StartDeviceAuth begins a sign-in and returns what to show the user.
func (c *Client) StartDeviceAuth(ctx context.Context) (*DeviceAuth, error) {
	creds, err := c.Credentials()
	if err != nil {
		return nil, err
	}
	var doc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	err = c.postForm(ctx, c.endpoints.DeviceCode, url.Values{
		"client_id": {creds.ClientID},
		"scope":     {Scope},
	}, &doc)
	if err != nil {
		return nil, fmt.Errorf("start the Google sign-in: %w", err)
	}
	link := doc.VerificationURL
	if link == "" {
		link = doc.VerificationURI
	}
	if doc.DeviceCode == "" || doc.UserCode == "" || link == "" {
		return nil, errors.New("start the Google sign-in: the answer holds no code")
	}
	if doc.Interval < 1 {
		doc.Interval = 5
	}
	if doc.ExpiresIn < 1 {
		doc.ExpiresIn = 1800
	}
	return &DeviceAuth{
		UserCode: doc.UserCode, VerificationURL: link,
		ExpiresAt: c.now().Add(time.Duration(doc.ExpiresIn) * time.Second),
		clientID:  creds.ClientID, deviceCode: doc.DeviceCode,
		interval: time.Duration(doc.Interval) * time.Second,
	}, nil
}

// ErrAuthDenied: the user declined the sign-in. ErrAuthExpired: the code ran
// out before it was entered.
var (
	ErrAuthDenied  = errors.New("the sign-in was declined")
	ErrAuthExpired = errors.New("the code expired before it was entered")
)

// AwaitDeviceAuth waits until the user has approved the sign-in, then keeps
// it. It returns when the sign-in is in place, was declined, has expired or
// ctx ends.
func (c *Client) AwaitDeviceAuth(ctx context.Context, da *DeviceAuth) error {
	interval, slower := da.interval, 5*time.Second
	if c.pollEvery > 0 {
		interval, slower = c.pollEvery, c.pollEvery
	}
	for {
		if !c.now().Before(da.ExpiresAt) {
			return ErrAuthExpired
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		creds, err := c.Credentials()
		if err != nil {
			return err
		}
		if creds.ClientID != da.clientID {
			return errors.New("the OAuth client changed while the sign-in was under way: start again")
		}
		var grant tokenGrant
		err = c.postForm(ctx, c.endpoints.Token, url.Values{
			"client_id":     {creds.ClientID},
			"client_secret": {creds.ClientSecret},
			"device_code":   {da.deviceCode},
			"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &grant)
		var oe *OAuthError
		switch {
		case err == nil:
			if grant.RefreshToken == "" || grant.AccessToken == "" {
				return errors.New("the sign-in was approved, but the answer holds no token to keep")
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.saveToken(&token{
				ClientID: creds.ClientID, RefreshToken: grant.RefreshToken, AccessToken: grant.AccessToken,
				Expiry: c.now().Add(time.Duration(grant.ExpiresIn) * time.Second), Scope: grant.Scope,
			})
		case errors.As(err, &oe):
			switch oe.Code {
			case "authorization_pending":
			case "slow_down":
				interval += slower
			case "access_denied":
				return ErrAuthDenied
			case "expired_token":
				return ErrAuthExpired
			default:
				return fmt.Errorf("the sign-in failed: %w", err)
			}
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			// The network, not an answer: the next poll may get through.
		}
	}
}

// Disconnect ends the sign-in: Google is told to revoke it, and the token
// file is deleted either way. It reports whether there was a sign-in.
func (c *Client) Disconnect(ctx context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.loadToken()
	if t == nil {
		_ = os.Remove(c.tokenPath)
		return false, nil
	}
	err := c.postForm(ctx, c.endpoints.Revoke, url.Values{"token": {t.RefreshToken}}, nil)
	if rerr := os.Remove(c.tokenPath); rerr != nil && !os.IsNotExist(rerr) {
		return true, rerr
	}
	var oe *OAuthError
	if errors.As(err, &oe) && oe.Code == "invalid_token" {
		err = nil // already revoked
	}
	if err != nil {
		return true, fmt.Errorf("the sign-in is deleted here, but Google did not confirm revoking it: %w", err)
	}
	return true, nil
}

// --- API ------------------------------------------------------------------------

// APIError is an error answer of the Drive API.
type APIError struct {
	Status  int
	Message string
	Reason  string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Google Drive answered HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Google Drive answered HTTP %d", e.Status)
}

// apiError reads the error out of an answer.
func apiError(resp *http.Response) *APIError {
	ae := &APIError{Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	var doc struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &doc) == nil {
		ae.Message = doc.Error.Message
		if len(doc.Error.Errors) > 0 {
			ae.Reason = doc.Error.Errors[0].Reason
		}
	}
	return ae
}

// do sends a request as the signed-in user, with timeout as the longest the
// request may take until its answer is read. build makes the request afresh
// (on the context it is given), so it can be sent again with a new access
// token when the first is refused. Closing the answer's body ends the request.
func (c *Client) do(ctx context.Context, timeout time.Duration, build func(ctx context.Context) (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		req, err := build(rctx)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			return nil, transportError(err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			cancel()
			continue
		}
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
}

// cancelOnClose releases a request's context once its answer has been read.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// getJSON GETs an API path and decodes the answer.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.do(ctx, c.callTimeout, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.endpoints.API+path+"?"+query.Encode(), nil)
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(out)
}

// Account is whose Drive the daemon is signed in to.
type Account struct {
	Name  string `json:"displayName"`
	Email string `json:"emailAddress"`
}

// About asks Google whose Drive this is. It is also the check that the
// sign-in still holds.
func (c *Client) About(ctx context.Context) (Account, error) {
	var doc struct {
		User Account `json:"user"`
	}
	if err := c.getJSON(ctx, "/about", url.Values{"fields": {"user(displayName,emailAddress)"}}, &doc); err != nil {
		return Account{}, err
	}
	return doc.User, nil
}

// quote renders a string for a Drive search query.
func quote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// EnsureFolder returns the id of the folder at path (names separated by "/"),
// creating what is missing; "" is the Drive's top level and yields "root".
//
// With the drive.file scope the daemon only sees folders it made itself, so a
// folder of the same name that the user created by hand is not found, and a
// second one is made beside it. The folder the daemon made can be renamed
// into or moved under any other afterwards; it is found again by its name
// under its parent, so a renamed one is made anew.
func (c *Client) EnsureFolder(ctx context.Context, path string) (string, error) {
	parent := "root"
	for _, name := range strings.Split(path, "/") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var found struct {
			Files []struct {
				ID string `json:"id"`
			} `json:"files"`
		}
		err := c.getJSON(ctx, "/files", url.Values{
			"q":        {fmt.Sprintf("name = %s and mimeType = %s and %s in parents and trashed = false", quote(name), quote(folderMIME), quote(parent))},
			"fields":   {"files(id)"},
			"spaces":   {"drive"},
			"pageSize": {"10"},
		}, &found)
		if err != nil {
			return "", fmt.Errorf("look for the folder %q: %w", name, err)
		}
		if len(found.Files) > 0 {
			parent = found.Files[0].ID
			continue
		}
		meta, err := json.Marshal(map[string]any{"name": name, "mimeType": folderMIME, "parents": []string{parent}})
		if err != nil {
			return "", err
		}
		resp, err := c.do(ctx, c.callTimeout, func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoints.API+"/files?fields=id", strings.NewReader(string(meta)))
			if err == nil {
				req.Header.Set("Content-Type", "application/json; charset=UTF-8")
			}
			return req, err
		})
		if err != nil {
			return "", fmt.Errorf("create the folder %q: %w", name, err)
		}
		var made struct {
			ID string `json:"id"`
		}
		if resp.StatusCode != http.StatusOK {
			err = apiError(resp)
		} else {
			err = json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(&made)
		}
		_ = resp.Body.Close()
		if err == nil && made.ID == "" {
			err = errors.New("the answer holds no id")
		}
		if err != nil {
			return "", fmt.Errorf("create the folder %q: %w", name, err)
		}
		parent = made.ID
	}
	return parent, nil
}
