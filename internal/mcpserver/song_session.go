package mcpserver

// This file is what the daemon keeps about a song's session: which session a
// song was authored into and from what, whether the iPad has fetched that
// file, and which session AUM has open as far as the daemon can know.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-device/device"
)

// songSessionRecord is the session a song was last authored into.
type songSessionRecord struct {
	Session     string `json:"session"`
	Fingerprint string `json:"fingerprint"`
	// SHA256 is the staged session file as it was authored. Pulled is the
	// file the iPad last fetched under that name: the two are equal once the
	// iPad has this very session.
	SHA256 string `json:"sha256,omitempty"`
	Pulled string `json:"pulled,omitempty"`
	// Options are the options the session was authored with.
	Options song.SessionSpec `json:"options,omitzero"`
}

// loadSongSessions reads the record of authored song sessions (empty when
// there is none yet).
func loadSongSessions() map[string]songSessionRecord {
	out := map[string]songSessionRecord{}
	data, err := os.ReadFile(config.SongSessionsPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// updateSongSessions changes the record of authored song sessions under its
// lock: change sees every record and reports whether it changed any.
func (s *Server) updateSongSessions(change func(all map[string]songSessionRecord) bool) error {
	s.songSessionsMu.Lock()
	defer s.songSessionsMu.Unlock()
	all := loadSongSessions()
	if !change(all) {
		return nil
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	path := config.SongSessionsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(path, data, 0o644)
}

// sessionFileName turns a song title into a file name AUM shows as the
// session's name: the title as written, minus what a path cannot hold.
func sessionFileName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < ' ':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(strings.Join(strings.Fields(b.String()), " "), ". ")
}

// fileSHA256 is the hex SHA-256 of a file, "" when it cannot be read.
func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return bytesSHA256(data)
}

func bytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stagedSessionPath is where a session id is staged.
func stagedSessionPath(id string) string {
	return filepath.Join(config.AUMSessionsDir(), filepath.FromSlash(id+".aumproj"))
}

// --- authoring ----------------------------------------------------------------

// songAuthorArgs is how a song's session is to be authored. A nil option
// takes the song's own session: block.
type songAuthorArgs struct {
	OutID       string
	TrackTaps   *bool
	FullControl *bool
	Hardware    string
}

// authoredSong is a staged song session.
type authoredSong struct {
	OutID, File, Path string
	SHA256            string
	Options           song.SessionSpec
	Report            song.SessionReport
}

// songSessionOptions resolves the options a song's session is authored with:
// the document's, overridden by what the caller passed.
func songSessionOptions(sg *song.Song, a songAuthorArgs) song.SessionSpec {
	o := sg.SessionOptions()
	if a.TrackTaps != nil {
		o.TrackTaps = *a.TrackTaps
	}
	if a.FullControl != nil {
		o.FullControl = *a.FullControl
	}
	if strings.TrimSpace(a.Hardware) != "" {
		o.Hardware = a.Hardware
	}
	o.Hardware = strings.ToLower(strings.TrimSpace(o.Hardware))
	return o
}

// songSessionID resolves the staging id a song's session goes to: the one
// asked for, else "Songs/<title>".
func songSessionID(cs *compiledSong, outID string) (string, error) {
	id := strings.TrimSpace(outID)
	if id == "" {
		id = "Songs/" + device.FirstNonEmpty(sessionFileName(cs.song.Title), cs.id)
	}
	rel, ok := aum.SafeRelPath(id + ".aumproj")
	if !ok {
		return "", fmt.Errorf("/out_id: %q is not a relative path inside the staging dir", outID)
	}
	return aum.StripExt(rel), nil
}

// authorSong builds a song's session, stages it and records it as the song's.
func (s *Server) authorSong(cs *compiledSong, a songAuthorArgs) (*authoredSong, error) {
	outID, err := songSessionID(cs, a.OutID)
	if err != nil {
		return nil, err
	}
	opt := songSessionOptions(cs.song, a)
	hardware, err := parseHardwareProfile(opt.Hardware)
	if err != nil {
		return nil, err
	}

	cat := songCatalog()
	opts := song.SessionOptions{
		Hardware: hardware, TrackTaps: opt.TrackTaps, FullControl: opt.FullControl,
		Brain: rigProbe(cat, aum.ProbeBrainNode()), Tap: rigProbe(cat, aum.ProbeTapNode()),
	}
	defaults := loadAUv3DefaultStates()
	opts.Prepare = func(ns *aum.NodeSpec) error { return applyDefaultState(ns, defaults) }

	sess, rep, err := cs.plan.BuildSession(opts)
	if err != nil {
		return nil, fmt.Errorf("author session: %w", err)
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		return nil, fmt.Errorf("encode session: %w", err)
	}
	if _, err := aum.Open(data); err != nil {
		return nil, fmt.Errorf("authored session failed re-decode: %w", err)
	}
	path, file, err := stageAUMFile(outID, ".aumproj", data)
	if err != nil {
		return nil, fmt.Errorf("stage session: %w", err)
	}
	out := &authoredSong{OutID: outID, File: file, Path: path, SHA256: bytesSHA256(data), Options: opt, Report: rep}
	err = s.updateSongSessions(func(all map[string]songSessionRecord) bool {
		rec := songSessionRecord{Session: outID, Fingerprint: cs.timeline.Fingerprint, SHA256: out.SHA256, Options: opt}
		// What the iPad holds under this name stays what it fetched.
		if prev, ok := all[cs.id]; ok && prev.Session == outID {
			rec.Pulled = prev.Pulled
		}
		all[cs.id] = rec
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("record song session: %w", err)
	}
	return out, nil
}

// describeSongLayout lists a song session's strips.
func describeSongLayout(b *strings.Builder, cs *compiledSong) {
	fmt.Fprintf(b, "  strip 1: Brain (ProbeMidiBrain), which plays every track and drives MIDI Control on channels %d-16\n", cs.plan.ControlChannels[0])
	for _, tp := range cs.plan.Tracks {
		names := make([]string, len(tp.Nodes))
		for i, n := range tp.Nodes {
			names[i] = n.Name
		}
		fmt.Fprintf(b, "  strip %d: %s on MIDI channel %d: %s\n", tp.Strip+1, tp.Name, tp.Channel, strings.Join(names, " > "))
	}
	fmt.Fprintf(b, "  strip %d: Master", cs.plan.MasterStrip+1)
	for _, n := range cs.plan.Master {
		fmt.Fprintf(b, " > %s", n.Name)
	}
	b.WriteString(" > ProbeAudioTap\n")
}

func (s *Server) handleAuthorSongSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song        string `json:"song"`
		OutID       string `json:"out_id"`
		TrackTaps   *bool  `json:"track_taps"`
		FullControl *bool  `json:"full_control"`
		Hardware    string `json:"hardware"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	au, err := s.authorSong(cs, songAuthorArgs{OutID: args.OutID, TrackTaps: args.TrackTaps, FullControl: args.FullControl, Hardware: args.Hardware})
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "authored the session of %q -> %s\n", cs.song.Title, au.Path)
	fmt.Fprintf(&b, "  %d strip(s), %d plugin node(s), %d mapping(s), %d preset load(s)", au.Report.Strips, au.Report.Nodes, au.Report.Mappings, au.Report.Presets)
	if au.Options.FullControl {
		fmt.Fprintf(&b, ", %d further target(s) mapped (%d did not fit)", au.Report.Banked, au.Report.Overflow)
	}
	b.WriteByte('\n')
	describeSongLayout(&b, cs)
	fmt.Fprintf(&b, "next: load_song has the iPad pull it and loads it in AUM (%s); then play_song\n", au.File)

	return structResult(b.String(), map[string]any{
		"song":     cs.id,
		"id":       au.OutID,
		"file":     au.File,
		"path":     au.Path,
		"download": "/aum-session/" + au.File,
		"report":   au.Report,
		"options":  au.Options,
		"layout":   cs.plan,
	}), nil
}

// --- what the iPad has pulled -------------------------------------------------

// sessionPulls signals every download of a staged session by the iPad.
type sessionPulls struct {
	mu sync.Mutex
	ch chan struct{}
}

// signal returns a channel that is closed at the next download.
func (p *sessionPulls) signal() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch == nil {
		p.ch = make(chan struct{})
	}
	return p.ch
}

func (p *sessionPulls) broadcast() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		close(p.ch)
		p.ch = nil
	}
}

// noteSessionPulled records that the iPad fetched the staged session id: every
// song authored into it now knows which file the iPad holds.
func (s *Server) noteSessionPulled(id string) {
	sha := fileSHA256(stagedSessionPath(id))
	if sha != "" {
		_ = s.updateSongSessions(func(all map[string]songSessionRecord) bool {
			changed := false
			for song, rec := range all {
				if rec.Session == id && rec.Pulled != sha {
					rec.Pulled = sha
					all[song] = rec
					changed = true
				}
			}
			return changed
		})
	}
	s.pulls.broadcast()
}

// waitPulled waits until the iPad holds the session file sha of song id, for
// at most d.
func (s *Server) waitPulled(ctx context.Context, songID, sha string, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		ch := s.pulls.signal()
		if loadSongSessions()[songID].Pulled == sha {
			return true
		}
		select {
		case <-ch:
		case <-deadline.C:
			return loadSongSessions()[songID].Pulled == sha
		case <-ctx.Done():
			return false
		}
	}
}

// --- which session AUM has open -----------------------------------------------

// loadedSession is the session the daemon loaded into AUM, valid for as long
// as the brain connection that followed the load lives.
type loadedSession struct {
	mu  sync.Mutex
	id  string
	gen uint64 // the brain connection it was seen with
	cur uint64 // the brain connection now
}

// brainConnected notes a new brain connection: whatever session was known to
// be open may have been replaced.
func (l *loadedSession) brainConnected() {
	l.mu.Lock()
	l.cur++
	l.mu.Unlock()
}

func (l *loadedSession) generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur
}

func (l *loadedSession) set(id string) {
	l.mu.Lock()
	l.id, l.gen = id, l.cur
	l.mu.Unlock()
}

func (l *loadedSession) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gen != l.cur {
		return ""
	}
	return l.id
}

// loadedSessionID is the staging id of the session AUM has open, "" when the
// daemon does not know: it knows only what it loaded itself, until the brain
// reconnects (a session load in AUM starts a new brain).
func (s *Server) loadedSessionID() string { return s.loaded.get() }
