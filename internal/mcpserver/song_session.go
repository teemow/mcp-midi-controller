package mcpserver

// This file is what the daemon keeps about a song's session: which session a
// song was authored into and from what, whether the iPad has fetched that
// file, and which session AUM has open as far as the daemon can know (what
// the session's taps say about that is in song_taps.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// Served are the sizes in bytes of the files the iPad fetched under that
	// name, the latest last (servedKept of them): what a copy in AUM's folder
	// that came from the daemon is as large as. See ownCopy.
	Served []int64 `json:"served,omitempty"`
	// Rev is the staging rev the session was staged at: a sync report of the
	// probe app says what the app made of this file when its rev is this one
	// or a later one. 0 in a record from before the rev was kept.
	Rev int64 `json:"rev,omitempty"`
	// Options are the options the session was authored with.
	Options song.SessionSpec `json:"options,omitzero"`
	// Sequencer is the probe id of the sequencer the session hosts and
	// Sequence the fingerprint of the song it holds (song.Sequence); both
	// empty for a session without one.
	Sequencer string `json:"sequencer,omitempty"`
	Sequence  string `json:"sequence,omitempty"`
	// TapNames says the session's taps were authored with their names
	// (song.TapName). A session staged before taps had names lacks it, and
	// load_song authors that one again.
	TapNames bool `json:"tap_names,omitempty"`
}

// sequenceCurrent reports whether the recorded session's sequencer holds the
// song as tl has it now, in a session whose layout still matches.
func (r songSessionRecord) sequenceCurrent(tl *song.Timeline) bool {
	return r.Sequence != "" && r.Fingerprint == tl.Fingerprint && r.Sequence == tl.Sequence().Fingerprint()
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

// servedKept is how many sizes of fetched files a record keeps.
const servedKept = 6

// addServed appends the size of a file the iPad fetched to a record's sizes.
func addServed(sizes []int64, size int64) []int64 {
	if size <= 0 || slices.Contains(sizes, size) {
		return sizes
	}
	sizes = append(slices.Clone(sizes), size)
	return sizes[max(0, len(sizes)-servedKept):]
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
	// Sequencer asks for the sequencer with the song in it (true), or for a
	// session without it (false). nil hosts it when it is installed.
	Sequencer *bool
}

// authoredSong is a staged song session.
type authoredSong struct {
	OutID, File, Path string
	SHA256            string
	Options           song.SessionSpec
	Report            song.SessionReport
	// Sequencer is what the session's sequencer is and holds, nil without one.
	Sequencer *songSequencer
}

// songSequencer is the sequencer a song's session hosts: the plugin's staged
// probe, its probe id, and the song laid out for it.
type songSequencer struct {
	Probe    *device.ProbeDump
	ID       string
	Sequence *song.Sequence
}

// sequencerProbe finds the staged probe of the sequencer a song's session can
// host, and its probe id; nil when the plugin was never scanned.
func sequencerProbe(cat song.Catalog) (*device.ProbeDump, string) {
	id := device.ProbeID(device.ProbeDump{Component: song.AtomComponent})
	if d, ok := cat[id]; ok {
		return &d, id
	}
	return nil, id
}

// songSequencerFor decides whether a song's session hosts the sequencer: it
// does whenever the plugin is installed, unless asked not to (want false).
// Asked for (want true) and not installed is an error.
func songSequencerFor(cs *compiledSong, cat song.Catalog, want *bool) (*songSequencer, error) {
	probe, id := sequencerProbe(cat)
	switch {
	case want != nil && !*want:
		return nil, nil
	case probe != nil:
		return &songSequencer{Probe: probe, ID: id, Sequence: cs.timeline.Sequence()}, nil
	case want != nil:
		return nil, fmt.Errorf("/sequencer: the sequencer Atom | Piano Roll² (%s) is not among the staged AUv3 probes; install it and open the auv3-probe app on the iPad so it is scanned, or leave sequencer out to play the song live", id)
	}
	return nil, nil
}

// fingerprint is what the record keeps of the sequencer's song, "" for none.
func (q *songSequencer) fingerprint() string {
	if q == nil {
		return ""
	}
	return q.Sequence.Fingerprint()
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
		// The taps dial in under the session's id: see song_taps.go.
		Session: outID,
	}
	defaults, bases := loadAUv3DefaultStates(), loadAUv3BaseStates()
	opts.Prepare = func(n *song.NodePlan, ns *aum.NodeSpec) error {
		if err := applyDefaultState(ns, defaults); err != nil {
			return err
		}
		return applyStepSequence(n, ns, bases)
	}
	seq, err := songSequencerFor(cs, cat, a.Sequencer)
	if err != nil {
		return nil, err
	}
	if seq != nil {
		opts.Sequencer, opts.Sequence = seq.Probe, seq.Sequence
	}

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
	// The file this one replaces may be the one the iPad holds; its size is
	// then one a copy in AUM's folder can have (also for a record from before
	// sizes were kept).
	held, _ := os.ReadFile(stagedSessionPath(outID))
	path, file, err := stageAUMFile(outID, ".aumproj", data)
	if err != nil {
		return nil, fmt.Errorf("stage session: %w", err)
	}
	out := &authoredSong{OutID: outID, File: file, Path: path, SHA256: bytesSHA256(data), Options: opt, Report: rep, Sequencer: seq}
	// The rev the staging gave the dir, read after it: that rev, or a later
	// one when the app uploaded a file in the same moment. Later is safe: a
	// report of that rev lists this file all the same.
	rev := aum.StagingRev(config.AUMSessionsDir())
	err = s.updateSongSessions(func(all map[string]songSessionRecord) bool {
		rec := songSessionRecord{Session: outID, Fingerprint: cs.timeline.Fingerprint, SHA256: out.SHA256, Rev: rev, Options: opt, TapNames: true}
		if seq != nil {
			rec.Sequencer, rec.Sequence = seq.ID, seq.fingerprint()
		}
		// What the iPad holds under this name stays what it fetched. It may
		// have fetched this very file already, between its staging and now.
		if prev, ok := all[cs.id]; ok && prev.Session == outID {
			rec.Pulled, rec.Served = prev.Pulled, prev.Served
			if prev.Pulled != "" && bytesSHA256(held) == prev.Pulled {
				rec.Served = addServed(rec.Served, int64(len(held)))
			}
		}
		if s.pulls.has(outID, out.SHA256) {
			rec.Pulled, rec.Served = out.SHA256, addServed(rec.Served, int64(len(data)))
		}
		all[cs.id] = rec
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("record song session: %w", err)
	}
	return out, nil
}

// describeSongLayout lists a song session's strips; seq is its sequencer, nil
// without one.
func describeSongLayout(b *strings.Builder, cs *compiledSong, seq *songSequencer) {
	fmt.Fprintf(b, "  strip 1: Brain (ProbeMidiBrain), which plays every track and drives MIDI Control on channels %d-16\n", cs.plan.ControlChannels[0])
	if seq != nil {
		q := seq.Sequence
		fmt.Fprintf(b, "           + %s holding the whole song (%d notes, %d control changes) after a lead-in of %d bar(s), routed like the brain\n",
			seq.Probe.Name, len(q.Notes), len(q.Controls), q.LeadBars)
	}
	for _, tp := range cs.plan.Tracks {
		names := make([]string, len(tp.Nodes))
		for i, n := range tp.Nodes {
			names[i] = n.Name
		}
		controls := ""
		if len(tp.ControlChannels) > 0 {
			controls = " (and control notes on " + joinInts(tp.ControlChannels, ", ") + ")"
		}
		fmt.Fprintf(b, "  strip %d: %s on MIDI channel %d%s: %s\n", tp.Strip+1, tp.Name, tp.Channel, controls, strings.Join(names, " > "))
	}
	fmt.Fprintf(b, "  strip %d: Master", cs.plan.MasterStrip+1)
	for _, n := range cs.plan.Master {
		fmt.Fprintf(b, " > %s", n.Name)
	}
	b.WriteString(" > ProbeAudioTap\n")
}

// songTapNames are the names a song session's taps dial in under, by strip:
// the master's and, with track taps, every track's. order lists the strips
// as the mixer has them, the master last.
func songTapNames(cs *compiledSong, au *authoredSong) (names map[string]string, order []string) {
	names = map[string]string{}
	if au.Options.TrackTaps {
		for _, tp := range cs.plan.Tracks {
			names[tp.ID] = song.TapName(au.OutID, tp.ID)
			order = append(order, tp.ID)
		}
	}
	names[song.MasterTap] = song.TapName(au.OutID, song.MasterTap)
	return names, append(order, song.MasterTap)
}

func (s *Server) handleAuthorSongSession(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song        string `json:"song"`
		OutID       string `json:"out_id"`
		TrackTaps   *bool  `json:"track_taps"`
		FullControl *bool  `json:"full_control"`
		Hardware    string `json:"hardware"`
		Sequencer   *bool  `json:"sequencer"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	au, err := s.authorSong(cs, songAuthorArgs{OutID: args.OutID, TrackTaps: args.TrackTaps, FullControl: args.FullControl, Hardware: args.Hardware, Sequencer: args.Sequencer})
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
	describeSongLayout(&b, cs, au.Sequencer)
	taps, order := songTapNames(cs, au)
	b.WriteString("  taps, as get_audio_tap names them:")
	for _, strip := range order {
		fmt.Fprintf(&b, " %q", taps[strip])
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "next: load_song has the iPad pull it and loads it in AUM (%s); then play_song\n", au.File)
	switch {
	case au.Sequencer != nil:
		b.WriteString("play_song starts AUM's transport and the sequencer plays the song; after a change to the notes it plays live until the session is authored and loaded again (load_song)\n")
	case args.Sequencer == nil:
		_, id := sequencerProbe(songCatalog())
		fmt.Fprintf(&b, "the song plays live through the brain: the sequencer Atom | Piano Roll² (%s) is not among the staged AUv3 probes\n", id)
	}

	return structResult(b.String(), map[string]any{
		"song":      cs.id,
		"id":        au.OutID,
		"file":      au.File,
		"path":      au.Path,
		"download":  "/aum-session/" + au.File,
		"report":    au.Report,
		"options":   au.Options,
		"taps":      taps,
		"layout":    cs.plan,
		"sequenced": au.Sequencer != nil,
	}), nil
}

// --- what the iPad has pulled -------------------------------------------------

// sessionPulls signals every download of a staged session by the iPad, and
// remembers which file of each session it fetched last.
type sessionPulls struct {
	mu   sync.Mutex
	ch   chan struct{}
	last map[string]string // session id -> the fetched file's SHA-256
}

// fetched notes that the iPad fetched a session's file.
func (p *sessionPulls) fetched(id, sha string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]string{}
	}
	p.last[id] = sha
}

// has reports whether the file the iPad fetched last of a session is sha.
func (p *sessionPulls) has(id, sha string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return sha != "" && p.last[id] == sha
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
	if data, err := os.ReadFile(stagedSessionPath(id)); err == nil {
		sha, size := bytesSHA256(data), int64(len(data))
		s.pulls.fetched(id, sha)
		_ = s.updateSongSessions(func(all map[string]songSessionRecord) bool {
			changed := false
			for song, rec := range all {
				if rec.Session == id && (rec.Pulled != sha || !slices.Contains(rec.Served, size)) {
					rec.Pulled, rec.Served = sha, addServed(rec.Served, size)
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

// loadedSession is the session the daemon loaded into AUM or saw on AUM's
// screen, valid for as long as the brain connection that followed lives.
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
// daemon does not know. A session whose master tap dials in under its name
// says so itself, for as long as it is open. Without such a tap the daemon
// knows only what it loaded or saw itself, until the brain reconnects (a
// session load in AUM starts a new brain).
func (s *Server) loadedSessionID() string {
	if id := s.tapSession(); id != "" {
		return id
	}
	return s.loaded.get()
}
