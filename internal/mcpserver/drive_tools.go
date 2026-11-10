package mcpserver

// This file puts recordings into the user's Google Drive (internal/drive).
//
// The daemon runs without a screen, so it signs in the way a TV does:
// connect_google_drive returns a link and a short code, the user opens the
// link on any device and approves, and the daemon keeps the sign-in from then
// on. Neither the sign-in, the client secret nor the code the daemon polls
// with ever appears in a tool's answer or a log line; a tool shows the link,
// the user's code and, once signed in, whose Drive it is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/drive"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/recording"
)

// driveState is the daemon's Google Drive client and the sign-in under way.
type driveState struct {
	// opts adjust the client (tests point it at a stand-in).
	opts []drive.Option

	mu      sync.Mutex
	client  *drive.Client
	pending *pendingSignIn
	// lastSignIn is how the last sign-in that did not succeed ended.
	lastSignIn string

	// uploadMu lets one upload run at a time, so a recording asked for twice
	// is not sent twice.
	uploadMu sync.Mutex
}

// pendingSignIn is a sign-in waiting for the user.
type pendingSignIn struct {
	auth   *drive.DeviceAuth
	cancel context.CancelFunc
}

// WithGoogleDriveOptions adjusts the daemon's Google Drive client. Tests use
// it to point the client at a stand-in for Google.
func WithGoogleDriveOptions(opts ...drive.Option) Option {
	return func(s *Server) { s.drv.opts = append(s.drv.opts, opts...) }
}

// driveClient returns the daemon's Drive client. The OAuth client file and
// the sign-in are read when they are needed, so both can appear later.
func (s *Server) driveClient() *drive.Client {
	s.drv.mu.Lock()
	defer s.drv.mu.Unlock()
	if s.drv.client == nil {
		s.drv.client = drive.New(config.GoogleDriveClientPath(), config.GoogleDriveTokenPath(), s.drv.opts...)
	}
	return s.drv.client
}

// driveSetupHint says how the OAuth client gets where the daemon looks for
// it. It follows an error that names the file.
const driveSetupHint = `create an OAuth client of the type "TVs and Limited Input devices" in your own Google Cloud project and save its JSON there (scripts/setup-google-drive.sh walks through it; docs/recording.md)`

// driveProblem turns a Drive error into what the user can do about it.
func driveProblem(err error) string {
	switch {
	case errors.Is(err, drive.ErrNoClient):
		return err.Error() + ": " + driveSetupHint
	case errors.Is(err, drive.ErrNotConnected):
		return err.Error() + ": run connect_google_drive and approve the sign-in"
	}
	return err.Error()
}

func (s *Server) registerDriveTools() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "get_google_drive",
		Description: "Whether the daemon can upload to Google Drive: is the OAuth client in place, is it signed in and to whose Drive, which folder uploads go to, and whether recordings of songs are uploaded by themselves. " +
			"While a sign-in waits for approval it shows the link and the code again.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleGetGoogleDrive)

	s.mcp.AddTool(&mcp.Tool{
		Name: "connect_google_drive",
		Description: "Sign the daemon in to the user's Google Drive, once. Returns a link and a short code: the user opens the link on any device, enters the code and approves; get_google_drive says when that has happened. " +
			"The daemon asks for the narrowest access that can create a file (drive.file: the files it uploads itself, nothing else in the Drive) and keeps the sign-in in its state dir. " +
			"It needs the user's own OAuth client first; without one this says how to create it. disconnect ends the sign-in instead.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"disconnect": {"type": "boolean", "description": "End the sign-in: revoke it at Google and delete it here."}
			}
		}`),
	}, s.handleConnectGoogleDrive)

	s.mcp.AddTool(&mcp.Tool{
		Name: "upload_recording",
		Description: "Upload a recording to the user's Google Drive and return its link. The upload is resumable: it picks up after a dropped connection, and the file's checksum in the Drive is compared with the local one. " +
			"The file is private to its owner until they share it. It goes into the folder set in the config (google_drive.folder; the Drive's top level when unset), which the daemon creates on first use and keeps using when it is renamed or moved in the Drive. " +
			"A recording that is already in the Drive is not sent again unless again is set.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"recording": {"type": "string", "description": "The recording's name (see list_recordings). Default: the newest."},
				"name": {"type": "string", "description": "The file's name in the Drive (default: the recording's file name)."},
				"folder": {"type": "string", "description": "The Drive folder, instead of the configured one: a name, or several separated by \"/\"."},
				"again": {"type": "boolean", "description": "Upload it even though it was uploaded before (this makes a second file)."}
			}
		}`),
	}, s.handleUploadRecording)
}

// driveStatus is the machine-readable answer of get_google_drive.
type driveStatus struct {
	// ClientFile is where the OAuth client is read from; Client whether a
	// usable one is there.
	ClientFile string         `json:"client_file"`
	Client     bool           `json:"client"`
	Connected  bool           `json:"connected"`
	Account    *drive.Account `json:"account,omitempty"`
	Folder     string         `json:"folder"`
	AutoUpload bool           `json:"auto_upload"`
	Pending    *signInPrompt  `json:"pending,omitempty"`
	Problem    string         `json:"problem,omitempty"`
}

// signInPrompt is what the user needs to approve a sign-in.
type signInPrompt struct {
	URL       string    `json:"url"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (p *pendingSignIn) prompt() *signInPrompt {
	return &signInPrompt{URL: p.auth.VerificationURL, Code: p.auth.UserCode, ExpiresAt: p.auth.ExpiresAt}
}

func (p *signInPrompt) text() string {
	return fmt.Sprintf("open %s on any device, enter the code %s and approve (the code is valid for %d more minutes)",
		p.URL, p.Code, max(1, int(time.Until(p.ExpiresAt).Minutes())))
}

// folderText renders an upload folder.
func folderText(folder string) string {
	if strings.Trim(strings.TrimSpace(folder), "/") == "" {
		return "the Drive's top level"
	}
	return fmt.Sprintf("the folder %q", folder)
}

func (s *Server) handleGetGoogleDrive(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client := s.driveClient()
	cfg := loadConfig().GoogleDrive
	st := driveStatus{ClientFile: config.GoogleDriveClientPath(), Folder: cfg.Folder, AutoUpload: cfg.AutoUpload}
	var b strings.Builder

	s.drv.mu.Lock()
	pending, lastSignIn := s.drv.pending, s.drv.lastSignIn
	s.drv.mu.Unlock()

	_, err := client.Credentials()
	st.Client = err == nil
	switch {
	case err != nil:
		st.Problem = driveProblem(err)
		fmt.Fprintf(&b, "Google Drive: not set up. %s\n", st.Problem)
	case pending != nil:
		st.Pending = pending.prompt()
		fmt.Fprintf(&b, "Google Drive: a sign-in is waiting for approval: %s\n", st.Pending.text())
	case !client.Connected():
		b.WriteString("Google Drive: not signed in (connect_google_drive)")
		if lastSignIn != "" {
			st.Problem = lastSignIn
			fmt.Fprintf(&b, "; the last sign-in did not complete: %s", lastSignIn)
		}
		b.WriteByte('\n')
	default:
		// Ask Google: a sign-in on disk may have been revoked since.
		acct, aerr := client.About(ctx)
		switch {
		case aerr == nil:
			st.Connected, st.Account = true, &acct
			fmt.Fprintf(&b, "Google Drive: signed in as %s <%s>\n", acct.Name, acct.Email)
		case errors.Is(aerr, drive.ErrNotConnected):
			st.Problem = driveProblem(aerr)
			fmt.Fprintf(&b, "Google Drive: %s\n", st.Problem)
		default:
			// Signed in as far as the daemon knows; Google did not answer.
			st.Connected, st.Problem = true, aerr.Error()
			fmt.Fprintf(&b, "Google Drive: signed in, but Google could not be asked right now: %v\n", aerr)
		}
	}
	fmt.Fprintf(&b, "  uploads go to %s (google_drive.folder in config.yaml)\n", folderText(cfg.Folder))
	if cfg.AutoUpload {
		b.WriteString("  recordings of songs are uploaded by themselves (google_drive.auto_upload)\n")
	} else {
		b.WriteString("  a recording is uploaded when upload_recording is called, or when it was started with upload\n")
	}
	return structResult(b.String(), st), nil
}

func (s *Server) handleConnectGoogleDrive(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Disconnect bool `json:"disconnect"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	client := s.driveClient()

	if args.Disconnect {
		s.drv.mu.Lock()
		if p := s.drv.pending; p != nil {
			p.cancel()
			s.drv.pending = nil
		}
		s.drv.lastSignIn = ""
		s.drv.mu.Unlock()
		had, err := client.Disconnect(ctx)
		switch {
		case err != nil:
			return textResult("connect_google_drive: "+err.Error(), true), nil
		case !had:
			return structResult("Google Drive was not signed in", map[string]any{"connected": false}), nil
		}
		log.Printf("google drive: signed out")
		return structResult("signed out of Google Drive: the sign-in is revoked at Google and deleted here", map[string]any{"connected": false}), nil
	}

	if _, err := client.Credentials(); err != nil {
		return textResult("connect_google_drive: "+driveProblem(err), true), nil
	}
	if client.Connected() {
		acct, err := client.About(ctx)
		if err == nil {
			return structResult(fmt.Sprintf("already signed in to Google Drive as %s <%s> (disconnect ends that)", acct.Name, acct.Email),
				map[string]any{"connected": true, "account": acct}), nil
		}
		if !errors.Is(err, drive.ErrNotConnected) {
			return textResult("connect_google_drive: the daemon is signed in, but Google could not be asked right now: "+err.Error(), true), nil
		}
		// The sign-in on disk no longer holds: sign in afresh.
	}

	s.drv.mu.Lock()
	defer s.drv.mu.Unlock()
	if p := s.drv.pending; p != nil && time.Until(p.auth.ExpiresAt) > time.Minute {
		prompt := p.prompt()
		return structResult("a sign-in is already waiting for approval: "+prompt.text(), map[string]any{"connected": false, "pending": prompt}), nil
	}
	auth, err := client.StartDeviceAuth(ctx)
	if err != nil {
		return textResult("connect_google_drive: "+driveProblem(err), true), nil
	}
	if p := s.drv.pending; p != nil {
		p.cancel()
	}
	// The wait outlives this call: the user approves whenever they get to it.
	wctx, cancel := context.WithDeadline(context.Background(), auth.ExpiresAt)
	p := &pendingSignIn{auth: auth, cancel: cancel}
	s.drv.pending, s.drv.lastSignIn = p, ""
	go s.awaitSignIn(wctx, client, p)

	prompt := p.prompt()
	text := "to sign the daemon in to Google Drive: " + prompt.text() + "\n" +
		"Google names the app and what it asks for: to see and manage only the files it creates itself. get_google_drive says when the sign-in has arrived."
	return structResult(text, map[string]any{"connected": false, "pending": prompt}), nil
}

// awaitSignIn waits for the user to approve a sign-in and records how it
// ended.
func (s *Server) awaitSignIn(ctx context.Context, client *drive.Client, p *pendingSignIn) {
	defer p.cancel()
	err := client.AwaitDeviceAuth(ctx, p.auth)
	if errors.Is(err, context.DeadlineExceeded) {
		err = drive.ErrAuthExpired
	}
	s.drv.mu.Lock()
	superseded := s.drv.pending != p
	if !superseded {
		s.drv.pending = nil
		if err != nil {
			s.drv.lastSignIn = err.Error()
		}
	}
	s.drv.mu.Unlock()
	if superseded {
		return // disconnected or replaced by a newer sign-in
	}
	if err != nil {
		log.Printf("google drive: the sign-in did not complete: %v", err)
		s.broadcast("google_drive", map[string]any{"event": "sign-in failed", "error": err.Error()})
		return
	}
	log.Printf("google drive: signed in")
	s.broadcast("google_drive", map[string]any{"event": "signed in"})
}

// uploadRecording sends a recording to the Drive and notes the copy in its
// record. name and folder override the file's name and the configured folder.
// A recording already in the Drive is returned as it is (fresh false) unless
// again is set.
func (s *Server) uploadRecording(ctx context.Context, m recording.Meta, name, folder string, again bool) (out recording.Meta, fresh bool, err error) {
	s.drv.uploadMu.Lock()
	defer s.drv.uploadMu.Unlock()
	store := s.recordings()
	// Read the record again under the lock: another call may have just
	// uploaded it.
	if cur, gerr := store.Get(m.Name); gerr == nil {
		m = cur
	}
	if m.Drive != nil && !again {
		return m, false, nil
	}
	client := s.driveClient()
	// Say what is missing before anything is sent.
	if _, err := client.Credentials(); err != nil {
		return m, false, err
	}
	if !client.Connected() {
		return m, false, drive.ErrNotConnected
	}
	if strings.TrimSpace(folder) == "" {
		folder = loadConfig().GoogleDrive.Folder
	}
	folder = strings.Trim(strings.TrimSpace(folder), "/")
	folderID, err := uploadFolder(ctx, client, folder)
	if err != nil {
		return m, false, err
	}
	if name = strings.TrimSpace(name); name == "" {
		name = m.File
	} else if !strings.Contains(name, ".") {
		name += "." + string(m.Format)
	}
	started := time.Now()
	file, err := client.Upload(ctx, drive.UploadRequest{
		Path: store.AudioPath(m), Name: name, MIMEType: m.Format.MIMEType(), FolderID: folderID,
		Description: fmt.Sprintf("Recorded by mcp-midi-controller on %s: %s, peak %.1f dBFS, mean %.1f dBFS.",
			m.RecordedAt.Format("2006-01-02 15:04"), clockTenths(m.Seconds), m.PeakDBFS, m.RMSDBFS),
	})
	if err != nil {
		return m, false, err
	}
	log.Printf("google drive: uploaded the recording %q (%s) in %s", m.Name, megabytes(m.Bytes), time.Since(started).Round(time.Second))
	m.Drive = &recording.DriveCopy{FileID: file.ID, Name: file.Name, Link: file.Link, Folder: folder, UploadedAt: time.Now()}
	if err := store.Save(m); err != nil {
		return m, true, fmt.Errorf("the file is in the Drive (%s), but noting that failed: %w", file.Link, err)
	}
	return m, true, nil
}

// uploadFolder returns the id of the Drive folder that uploads into the
// folder path go to. The daemon remembers the folder it made for a path: the
// user may rename it or move it anywhere in their Drive, and uploads keep
// going into it. Only when that folder is gone (in the bin, deleted, or made
// under another OAuth client) is one looked up by name, or made, again. The
// caller holds uploadMu, which also serializes the memory file.
func uploadFolder(ctx context.Context, client *drive.Client, path string) (string, error) {
	if path == "" {
		return "root", nil
	}
	known := map[string]string{}
	if data, err := os.ReadFile(config.GoogleDriveFoldersPath()); err == nil {
		_ = json.Unmarshal(data, &known)
	}
	if id := known[path]; id != "" {
		usable, err := client.FolderUsable(ctx, id)
		if err != nil {
			// Not knowing is no reason to make a second folder.
			return "", fmt.Errorf("look at the upload folder: %w", err)
		}
		if usable {
			return id, nil
		}
	}
	id, err := client.EnsureFolder(ctx, path)
	if err != nil {
		return "", err
	}
	known[path] = id
	if data, err := json.MarshalIndent(known, "", "  "); err == nil {
		if err := os.MkdirAll(filepath.Dir(config.GoogleDriveFoldersPath()), 0o755); err == nil {
			_ = lanhttp.WriteFileAtomic(config.GoogleDriveFoldersPath(), data, 0o644)
		}
	}
	return id, nil
}

func (s *Server) handleUploadRecording(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Recording string `json:"recording"`
		Name      string `json:"name"`
		Folder    string `json:"folder"`
		Again     bool   `json:"again"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	store := s.recordings()
	var m recording.Meta
	if strings.TrimSpace(args.Recording) == "" {
		all, err := store.List()
		if err != nil {
			return textResult("upload_recording: "+err.Error(), true), nil
		}
		if len(all) == 0 {
			return textResult("upload_recording: there is no recording yet (play_song with record, or start_recording)", true), nil
		}
		m = all[0]
	} else {
		var err error
		if m, err = store.Get(args.Recording); err != nil {
			s.rec.mu.Lock()
			running := s.rec.current != nil && s.rec.current.meta.Name == strings.TrimSpace(args.Recording)
			s.rec.mu.Unlock()
			if running {
				return textResult(fmt.Sprintf("upload_recording: %q is still being recorded", args.Recording), true), nil
			}
			return textResult(fmt.Sprintf("upload_recording: no recording %q (see list_recordings)", args.Recording), true), nil
		}
	}

	m, fresh, err := s.uploadRecording(ctx, m, args.Name, args.Folder, args.Again)
	if err != nil {
		return textResult("upload_recording: "+driveProblem(err), true), nil
	}
	out := map[string]any{"recording": m.Name, "uploaded": fresh, "drive": m.Drive, "link": m.Drive.Link}
	if !fresh {
		return structResult(fmt.Sprintf("%q is already in Google Drive as %q (uploaded %s; set again to send it once more)\n%s",
			m.Name, m.Drive.Name, m.Drive.UploadedAt.Format("2006-01-02 15:04"), m.Drive.Link), out), nil
	}
	return structResult(fmt.Sprintf("uploaded %q (%s) to Google Drive, into %s, as %q\n%s",
		m.Name, megabytes(m.Bytes), folderText(m.Drive.Folder), m.Drive.Name, m.Drive.Link), out), nil
}
