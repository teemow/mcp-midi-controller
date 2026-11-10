package mcpserver

// This file takes a saved song to a session that plays (load_song) and tells
// whether a loaded session will sound (check_song). Both replace steps that
// were done by hand around the song tools: staging, getting the iPad to pull
// the file, loading it in AUM, waiting for the brain and the tap, and playing
// every instrument once to hear whether it is there.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

// Waits of load_song and check_song. They are variables so the tests need not
// sit them out.
var (
	// songPullGrace is how long a staged session is given to be pulled
	// without help: the probe app pulls by itself while it is in front.
	songPullGrace = 4 * time.Second
	// songPullTimeout bounds the wait once the probe app was brought forward.
	songPullTimeout = 60 * time.Second
	// songDialTimeout bounds the wait for the loaded session's brain and tap.
	songDialTimeout = 45 * time.Second
	// songPresetWait follows the preset loads of a check, songSettleWait the
	// control changes after them.
	songPresetWait = 700 * time.Millisecond
	songSettleWait = 300 * time.Millisecond
	// A check note is held for songNoteHold and listened to for songNoteTail
	// more; songFloorWait before it measures what was sounding anyway.
	songNoteHold  = 250 * time.Millisecond
	songNoteTail  = 450 * time.Millisecond
	songFloorWait = 120 * time.Millisecond
)

// silentDB is the peak below which a track counts as silent.
const silentDB = -60.0

func (s *Server) registerSongLoadTools(songArg string) {
	s.mcp.AddTool(&mcp.Tool{
		Name: "load_song",
		Description: "Take a saved song to a session that plays, in one call: author its AUM session (skipped when the staged one is already the song's), have the iPad pull it (the probe app is brought to the front when it does not pull by itself), load it in AUM and wait until AUM has finished loading, wait for the session's brain and tap, then check that every track answers a note (see check_song). " +
			"Each step is reported with what was seen; a step that fails ends the call and says what to do. Loading replaces the session open in AUM, so it needs confirm: true. " +
			"Call it after save_song says the session is out of date, and again whenever a plugin failed.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"confirm": {"type": "boolean", "description": "Required (true): loading replaces the session open in AUM, and unsaved changes there are lost."},
				"out_id": {"type": "string", "description": "Staging id of the session (default: the one the song was last authored into, else \"Songs/<title>\")."},
				"reauthor": {"type": "boolean", "description": "Author and stage the session again even when the staged one matches the song."},
				"check": {"type": "boolean", "description": "Play every track's first note and listen (default true)."},
				"track_taps": {"type": "boolean", "description": "Override the song's session option."},
				"full_control": {"type": "boolean", "description": "Override the song's session option."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Override the song's session option."}
			},
			"required": ["song"]
		}`),
	}, s.handleLoadSong)

	s.mcp.AddTool(&mcp.Tool{
		Name: "check_song",
		Description: "Tell whether a song will sound if it is played now: the brain is connected, the tap is streaming, AUM shows the song's session with its strips and reports no failed plugin, and every track answers a note. " +
			"For the last, the song's own sound is set (each track's declaration, no scene), every track's first note is played alone and the tap is measured: a track that stays silent is named with its plugin — that is what a failed or crashed plugin and a missing MIDI route look like from outside. " +
			"Not while a song plays. ui: false skips the look at AUM (which opens and closes its menu).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"ui": {"type": "boolean", "description": "Read the session name, strips and alerts from AUM's screen (default true when the iPad UI tools are configured)."},
				"sound": {"type": "boolean", "description": "Play every track's first note and listen (default true)."}
			},
			"required": ["song"]
		}`),
	}, s.handleCheckSong)
}

// --- the probe app --------------------------------------------------------------

// knownIPadApps reads the remembered bundle ids (alias -> id).
func knownIPadApps() map[string]string {
	out := map[string]string{}
	if data, err := os.ReadFile(config.IPadAppsPath()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// rememberIPadApp stores an app's bundle id for hosts that cannot look it up.
func rememberIPadApp(alias, id string) {
	apps := knownIPadApps()
	if apps[alias] == id {
		return
	}
	apps[alias] = id
	data, err := json.MarshalIndent(apps, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(config.StateDir(), 0o755) == nil {
		_ = lanhttp.WriteFileAtomic(config.IPadAppsPath(), data, 0o644)
	}
}

// probeApp resolves the probe app's bundle id: on the device when this host
// can (go-ios), else the id seen before.
func (s *Server) probeApp(ctx context.Context) (string, error) {
	id, err := s.ipad.ResolveApp(ctx, "probe")
	if err == nil && ipad.IsApp("probe", id) {
		rememberIPadApp("probe", id)
		return id, nil
	}
	if known := knownIPadApps()["probe"]; known != "" {
		return known, nil
	}
	if err == nil {
		err = errors.New("its bundle id is not known")
	}
	return "", fmt.Errorf("%w; call ipad_launch once with the app's full bundle id and the daemon remembers it", err)
}

// syncProbeApp brings the probe app to the front and starts its session
// sync: the app pulls staged sessions only while it is in front.
func (s *Server) syncProbeApp(ctx context.Context) error {
	app, err := s.probeApp(ctx)
	if err != nil {
		return err
	}
	return s.ipad.WithApp(ctx, app, func(sess *ipad.Session) error {
		// The sessions tab holds the sync button; either may be missing in
		// another version of the app, and being in front is what counts.
		_, _ = sess.TapLabel(ctx, "AUM Sessions", 2*time.Second)
		for _, label := range []string{"Sync", "Resync"} {
			if _, err := sess.TapLabel(ctx, label, time.Second); err == nil {
				break
			}
		}
		return nil
	})
}

// --- load_song ------------------------------------------------------------------

// loadStep is one step of load_song or one finding of check_song.
type loadStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	MS     int64  `json:"ms,omitempty"`
	// Skipped marks a step that was not needed.
	Skipped bool `json:"skipped,omitempty"`
}

// loadReport collects the steps of a load.
type loadReport struct {
	Song    string       `json:"song"`
	Session string       `json:"session"`
	Steps   []loadStep   `json:"steps"`
	Check   *checkReport `json:"check,omitempty"`
	OK      bool         `json:"ok"`
	// Next says what to do when a step failed.
	Next string `json:"next,omitempty"`
}

func (r *loadReport) add(step string, ok bool, since time.Time, format string, a ...any) {
	st := loadStep{Step: step, OK: ok, Detail: fmt.Sprintf(format, a...)}
	if !since.IsZero() {
		st.MS = time.Since(since).Milliseconds()
	}
	r.Steps = append(r.Steps, st)
}

func (r *loadReport) skip(step, format string, a ...any) {
	r.Steps = append(r.Steps, loadStep{Step: step, OK: true, Skipped: true, Detail: fmt.Sprintf(format, a...)})
}

func (r *loadReport) text(title string) string {
	var b strings.Builder
	b.WriteString(title)
	b.WriteByte('\n')
	for i, st := range r.Steps {
		mark := "ok  "
		switch {
		case st.Skipped:
			mark = "skip"
		case !st.OK:
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  %d. %-7s %s  %s", i+1, st.Step, mark, st.Detail)
		if st.MS >= 100 {
			fmt.Fprintf(&b, " (%.1f s)", float64(st.MS)/1000)
		}
		b.WriteByte('\n')
	}
	if r.Check != nil {
		b.WriteString(r.Check.text())
	}
	if r.Next != "" {
		fmt.Fprintf(&b, "next: %s\n", r.Next)
	}
	return b.String()
}

// loadSongArgs is what load_song was asked for.
type loadSongArgs struct {
	Author   songAuthorArgs
	Reauthor bool
	// Check plays every track's first note once the session is loaded.
	Check bool
}

func (s *Server) handleLoadSong(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song        string `json:"song"`
		Confirm     bool   `json:"confirm"`
		OutID       string `json:"out_id"`
		Reauthor    bool   `json:"reauthor"`
		Check       *bool  `json:"check"`
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
	if s.ipad != nil && !args.Confirm {
		return textResult("load_song needs confirm: true: loading replaces the session open in AUM, and unsaved changes there are lost", true), nil
	}
	rep, err := s.runLoadSong(ctx, cs, loadSongArgs{
		Author:   songAuthorArgs{OutID: args.OutID, TrackTaps: args.TrackTaps, FullControl: args.FullControl, Hardware: args.Hardware},
		Reauthor: args.Reauthor,
		Check:    args.Check == nil || *args.Check,
	})
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	return loadResult(cs, rep), nil
}

// loadResult renders a load's report as a tool result.
func loadResult(cs *compiledSong, rep *loadReport) *mcp.CallToolResult {
	title := fmt.Sprintf("%q is loaded in AUM as %s and ready to play", cs.song.Title, rep.Session)
	if !rep.OK {
		title = fmt.Sprintf("%q is NOT ready to play", cs.song.Title)
	}
	res := structResult(rep.text(title), rep)
	res.IsError = !rep.OK
	return res
}

// runLoadSong takes a compiled song to a loaded session, step by step. The
// report says how far it got; an error is only what keeps it from starting.
func (s *Server) runLoadSong(ctx context.Context, cs *compiledSong, args loadSongArgs) (*loadReport, error) {
	author := args.Author
	rec, authored := loadSongSessions()[cs.id]
	if author.OutID == "" && authored {
		author.OutID = rec.Session
	}
	outID, err := songSessionID(cs, author.OutID)
	if err != nil {
		return nil, err
	}
	rep := &loadReport{Song: cs.id, Session: outID}
	done := func(next string) (*loadReport, error) {
		rep.Next = next
		return rep, nil
	}

	if st := s.player.Status(); st.Playing {
		s.player.Stop()
	}

	// 1. Author, unless the staged file is this song's session already.
	start := time.Now()
	want := songSessionOptions(cs.song, author)
	current := authored && rec.Session == outID && rec.Fingerprint == cs.timeline.Fingerprint && rec.Options == want &&
		rec.SHA256 != "" && fileSHA256(stagedSessionPath(outID)) == rec.SHA256
	if current && !args.Reauthor {
		rep.skip("author", "the staged %s is the song's session", outID)
	} else {
		au, err := s.authorSong(cs, author)
		if err != nil {
			rep.add("author", false, start, "%v", err)
			return done("fix the song (save_song reports what does not resolve) and load it again")
		}
		rep.add("author", true, start, "%s: %d strip(s), %d plugin(s), %d mapping(s), %d preset load(s)",
			au.File, au.Report.Strips, au.Report.Nodes, au.Report.Mappings, au.Report.Presets)
	}
	rec = loadSongSessions()[cs.id]

	// 2. The iPad pulls the file.
	start = time.Now()
	switch {
	case rec.Pulled == rec.SHA256:
		rep.skip("pull", "the iPad already has this file")
	case s.waitPulled(ctx, cs.id, rec.SHA256, songPullGrace):
		rep.add("pull", true, start, "the iPad pulled the session")
	default:
		note := "the probe app was brought to the front"
		if s.ipad == nil {
			note = "the iPad UI tools are not configured, so the probe app was not brought forward"
		} else if err := s.syncProbeApp(ctx); err != nil {
			note = "the probe app could not be brought to the front: " + err.Error()
		}
		if !s.waitPulled(ctx, cs.id, rec.SHA256, songPullTimeout) {
			rep.add("pull", false, start, "the iPad did not pull the session (%s)", note)
			return done("open the AUv3 Probe app on the iPad (it pulls staged sessions while it is in front and reaches the daemon), then load_song again")
		}
		rep.add("pull", true, start, "the iPad pulled the session (%s)", note)
	}

	// 3. AUM loads it.
	if s.ipad == nil {
		rep.add("load", false, time.Time{}, "the iPad UI tools are not configured (ipad_wda_url), so AUM was not told to load it")
		return done(fmt.Sprintf("load %q in AUM by hand, then check_song", outID))
	}
	start = time.Now()
	gen := s.loaded.generation()
	dir, name := path.Split(outID)
	var folders []string
	if dir = strings.Trim(dir, "/"); dir != "" {
		folders = strings.Split(dir, "/")
	}
	res, err := s.ipad.AUMOpenSession(ctx, folders, name)
	if err != nil {
		rep.add("load", false, start, "%v", err)
		return done("look at the iPad (ipad_screenshot); AUM must be running and its mixer reachable, then load_song again")
	}
	loaded, _ := res.Data.(ipad.AUMLoaded)
	if !res.Verified {
		rep.add("load", false, start, "%s", res.Message)
		return done("look at the iPad (ipad_screenshot), then load_song again")
	}
	rep.add("load", true, start, "%s", res.Message)

	// 4. The new session's brain and tap dial in.
	start = time.Now()
	brain, tap := s.waitRig(ctx, gen, songDialTimeout)
	if !brain || !tap {
		var missing []string
		if !brain {
			missing = append(missing, "the ProbeMidiBrain did not connect")
		}
		if !tap {
			missing = append(missing, "the ProbeAudioTap is not streaming")
		}
		rep.add("connect", false, start, "%s", strings.Join(missing, "; "))
		return done("open the plugin's window in AUM and read its status line: it names the daemon it dials. A plugin AUM shows as failed needs the session loaded again (load_song)")
	}
	s.loaded.set(outID)
	rep.add("connect", true, start, "brain and tap connected")

	// 5. Does it sound?
	check := s.checkSong(ctx, cs, checkOptions{Sound: args.Check, Loaded: &loaded})
	rep.Check = &check
	rep.OK = check.OK
	if !check.OK {
		return done(check.Next)
	}
	return done("")
}

// waitRig waits for a brain connection made after generation gen and for a
// tap whose stream advances.
func (s *Server) waitRig(ctx context.Context, gen uint64, d time.Duration) (brain, tap bool) {
	deadline := time.Now().Add(d)
	for {
		brain = s.midi != nil && s.midi.Connected() && s.loaded.generation() > gen
		tap = s.tapStreaming(ctx)
		if (brain && tap) || time.Now().After(deadline) || ctx.Err() != nil {
			return brain, tap
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// tapStreaming reports whether the active tap delivers audio right now: it is
// connected and its sample count moves. A stream that hiccups is given a few
// tenths of a second.
func (s *Server) tapStreaming(ctx context.Context) bool {
	if s.audio == nil {
		return false
	}
	st, ok := s.audio.Active()
	if !ok || !st.Connected() {
		return false
	}
	before := st.MarkEpoch()
	for i := 0; i < 8; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(80 * time.Millisecond):
		}
		if st.MarkEpoch() > before {
			return true
		}
	}
	return false
}

// --- check_song -----------------------------------------------------------------

// checkOptions selects what check_song looks at.
type checkOptions struct {
	// UI reads AUM's screen; Loaded is that reading when the caller has it
	// already.
	UI     bool
	Loaded *ipad.AUMLoaded
	// Sound plays every track's first note and listens.
	Sound bool
}

// trackCheck is how one track answered its note.
type trackCheck struct {
	Track  string `json:"track"`
	Plugin string `json:"plugin"`
	// Note is the note played, the first one the track plays in the song.
	Note   int     `json:"note,omitempty"`
	PeakDB float64 `json:"peak_db,omitempty"`
	RMSDB  float64 `json:"rms_db,omitempty"`
	Sounds bool    `json:"sounds"`
	// Skipped says why the track was not played.
	Skipped string `json:"skipped,omitempty"`
}

// checkReport is check_song's result.
type checkReport struct {
	Song     string       `json:"song"`
	Session  string       `json:"session,omitempty"`
	Findings []loadStep   `json:"findings"`
	Tracks   []trackCheck `json:"tracks,omitempty"`
	OK       bool         `json:"ok"`
	Next     string       `json:"next,omitempty"`
}

func (c *checkReport) find(step string, ok bool, format string, a ...any) {
	c.Findings = append(c.Findings, loadStep{Step: step, OK: ok, Detail: fmt.Sprintf(format, a...)})
	if !ok {
		c.OK = false
	}
}

func (c *checkReport) text() string {
	var b strings.Builder
	for _, f := range c.Findings {
		mark := "ok  "
		if !f.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  %-7s %s  %s\n", f.Step, mark, f.Detail)
	}
	for _, t := range c.Tracks {
		switch {
		case t.Skipped != "":
			fmt.Fprintf(&b, "    %-10s %-22s not played: %s\n", t.Track, t.Plugin, t.Skipped)
		case t.Sounds:
			fmt.Fprintf(&b, "    %-10s %-22s %-4s peak %6.1f dBFS\n", t.Track, t.Plugin, song.NoteName(t.Note), t.PeakDB)
		default:
			fmt.Fprintf(&b, "    %-10s %-22s %-4s SILENT (peak %.1f dBFS)\n", t.Track, t.Plugin, song.NoteName(t.Note), t.PeakDB)
		}
	}
	return b.String()
}

func (s *Server) handleCheckSong(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song  string `json:"song"`
		UI    *bool  `json:"ui"`
		Sound *bool  `json:"sound"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	if st := s.player.Status(); st.Playing {
		return textResult(fmt.Sprintf("check_song: %q is playing; stop_song first (the check plays notes of its own)", st.Song), true), nil
	}
	check := s.checkSong(ctx, cs, checkOptions{
		UI:    s.ipad != nil && (args.UI == nil || *args.UI),
		Sound: args.Sound == nil || *args.Sound,
	})
	title := fmt.Sprintf("%q is ready to play", cs.song.Title)
	if !check.OK {
		title = fmt.Sprintf("%q is NOT ready to play", cs.song.Title)
	}
	text := title + "\n" + check.text()
	if check.Next != "" {
		text += "next: " + check.Next + "\n"
	}
	res := structResult(text, check)
	res.IsError = !check.OK
	return res, nil
}

// checkSong runs the checks. It never fails as a call: everything it finds is
// in the report.
func (s *Server) checkSong(ctx context.Context, cs *compiledSong, o checkOptions) checkReport {
	c := checkReport{Song: cs.id, OK: true}
	rec, authored := loadSongSessions()[cs.id]
	c.Session = rec.Session
	switch {
	case !authored:
		c.find("session", false, "the song has no authored session")
		c.Next = "load_song"
		return c
	case rec.Fingerprint != cs.timeline.Fingerprint:
		c.find("session", false, "the song's tempo, plugins, controls or session options changed since %s was authored", rec.Session)
		c.Next = "load_song"
		return c
	}

	// What AUM shows.
	loaded := o.Loaded
	if loaded == nil && o.UI && s.ipad != nil {
		res, err := s.ipad.AUMSession(ctx)
		if err != nil {
			c.find("aum", false, "AUM's screen could not be read: %v", err)
		} else if l, ok := res.Data.(ipad.AUMLoaded); ok {
			loaded = &l
		}
	}
	if loaded != nil {
		want := path.Base(rec.Session)
		if !strings.EqualFold(loaded.Session, want) {
			c.find("aum", false, "AUM has %q open, not the song's session %q", loaded.Session, want)
			c.Next = "load_song"
			return c
		}
		s.loaded.set(rec.Session)
		if missing := missingStrips(cs, loaded.Strips); len(missing) > 0 {
			c.find("aum", false, "AUM has %q open but no strip for: %s (strips: %s)", loaded.Session, strings.Join(missing, ", "), strings.Join(loaded.Strips, ", "))
		} else {
			c.find("aum", true, "AUM has %q open with the song's %d strip(s)", loaded.Session, len(loaded.Strips))
		}
		if len(loaded.Problems) > 0 {
			c.find("plugins", false, "AUM reports: %s", strings.Join(loaded.Problems, "; "))
		}
	} else if cur := s.loadedSessionID(); cur != "" && cur != rec.Session {
		c.find("aum", false, "AUM has %q open (loaded by load_song), not the song's session %q", cur, rec.Session)
		c.Next = "load_song"
		return c
	}

	// The brain and the tap.
	brain := s.midi != nil && s.midi.Connected()
	if brain {
		c.find("brain", true, "ProbeMidiBrain connected from %s", s.midi.Remote())
	} else {
		c.find("brain", false, "%s", midicontrol.ErrNoBrain.Error())
	}
	tap := s.tapStreaming(ctx)
	if tap {
		c.find("tap", true, "ProbeAudioTap streaming")
	} else {
		c.find("tap", false, "no ProbeAudioTap is streaming")
	}
	if !brain || !tap {
		c.Next = "load_song loads the session again; if the plugins are there but do not connect, open one's window in AUM and read its status line"
		return c
	}

	if o.Sound {
		tracks, err := s.soundCheck(ctx, cs)
		c.Tracks = tracks
		var silent []string
		for _, t := range tracks {
			if t.Skipped == "" && !t.Sounds {
				silent = append(silent, fmt.Sprintf("%s (%s)", t.Track, t.Plugin))
			}
		}
		switch {
		case err != nil:
			c.find("sound", false, "the check could not play: %v", err)
		case len(silent) > 0:
			c.find("sound", false, "silent: %s", strings.Join(silent, ", "))
			c.Next = "a silent track is a plugin that failed to load or crashed (AUM shows its node as failed), or one that does not answer this note: load_song loads the session again; if it stays silent, audition the plugin on its own"
		default:
			c.find("sound", true, "every track answers its first note")
		}
	}
	if !c.OK && c.Next == "" {
		c.Next = "load_song loads the session again"
	}
	return c
}

// missingStrips lists the song's strips AUM does not show: the brain's, one
// per track, the master's. A strip's title is its name, or carries it.
func missingStrips(cs *compiledSong, strips []string) []string {
	want := []string{"Brain"}
	for _, tp := range cs.plan.Tracks {
		want = append(want, tp.Name)
	}
	want = append(want, "Master")
	var missing []string
	used := make([]bool, len(strips))
next:
	for _, w := range want {
		for i, got := range strips {
			if !used[i] && strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
				used[i] = true
				continue next
			}
		}
		missing = append(missing, w)
	}
	return missing
}

// soundCheck sets the song's own sound and plays every track's first note
// alone, measuring the tap after each.
func (s *Server) soundCheck(ctx context.Context, cs *compiledSong) ([]trackCheck, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	// A dozing host stalls the tap's stream and the waits between the notes.
	defer s.holdAwake()()
	tap, ok := s.audio.Active()
	if !ok {
		return nil, errors.New("no audio tap")
	}
	home, err := cs.plan.HomeState()
	if err != nil {
		return nil, err
	}
	if err := s.sendSongState(ctx, home); err != nil {
		return nil, err
	}

	first := map[string]song.Event{}
	for _, e := range cs.timeline.Events {
		if e.Kind == song.NoteOn {
			if _, seen := first[e.Track]; !seen {
				first[e.Track] = e
			}
		}
	}
	var out []trackCheck
	for _, tp := range cs.plan.Tracks {
		row := trackCheck{Track: tp.ID, Plugin: tp.Nodes[0].Name}
		e, plays := first[tp.ID]
		if !plays {
			row.Skipped = "it plays no note in the song"
			out = append(out, row)
			continue
		}
		row.Note = e.Data1
		if tp.HomeMute() {
			if err := s.sendBrain(ctx, songCommand(tp.MuteEvent(false))); err != nil {
				return out, err
			}
		}
		h, err := s.hearNote(ctx, tap, e.Channel, e.Data1, 100, songNoteHold, songNoteTail)
		if tp.HomeMute() {
			_ = s.sendBrain(ctx, songCommand(tp.MuteEvent(true)))
		}
		if err != nil {
			return out, err
		}
		row.PeakDB, row.RMSDB = round1(h.PeakDB), round1(h.RMSDB)
		row.Sounds = h.Sounds()
		out = append(out, row)
	}
	return out, nil
}

// sendSongState sends a state's messages the way playback does: the preset
// loads, a pause for the plugins to load them, then everything a load would
// overwrite, and a pause again.
func (s *Server) sendSongState(ctx context.Context, events []song.Event) error {
	presets := 0
	for _, e := range events {
		if e.Kind == song.Program {
			presets++
			if err := s.sendBrain(ctx, songCommand(e)); err != nil {
				return err
			}
		}
	}
	if presets > 0 {
		if err := sleepContext(ctx, songPresetWait); err != nil {
			return err
		}
	}
	for _, e := range events {
		if e.Kind != song.Program {
			if err := s.sendBrain(ctx, songCommand(e)); err != nil {
				return err
			}
		}
	}
	return sleepContext(ctx, songSettleWait)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// heard is what the tap carried around one note.
type heard struct {
	// PeakDB and RMSDB are the note's level; FloorDB is the peak of what was
	// sounding just before it.
	PeakDB, RMSDB, FloorDB float64
	// Clip is the audio from the note-on to the end of the tail.
	Clip audiotap.Clip
}

// Sounds reports whether the note was audible: above silence, and above what
// was sounding before it (the tail of an earlier note is not an answer).
func (h heard) Sounds() bool {
	return h.PeakDB > silentDB && h.PeakDB > h.FloorDB+6
}

// hearNote plays one note and returns what the tap carried: it lets an
// earlier sound die away, measures the floor, holds the note and listens on
// for its tail.
func (s *Server) hearNote(ctx context.Context, tap *audiotap.Store, ch, note, vel int, hold, tail time.Duration) (heard, error) {
	s.settleAudio(ctx, tap)
	floorStart := tap.MarkEpoch()
	if err := sleepContext(ctx, songFloorWait); err != nil {
		return heard{}, err
	}
	start := tap.MarkEpoch()
	if err := s.sendBrain(ctx, midicontrol.Command{Type: "noteOn", Channel: ch, Note: note, Velocity: vel}); err != nil {
		return heard{}, err
	}
	werr := sleepContext(ctx, hold)
	// The note ends whatever happened to the wait.
	if err := s.sendBrain(context.WithoutCancel(ctx), midicontrol.Command{Type: "noteOff", Channel: ch, Note: note}); err != nil {
		return heard{}, err
	}
	if werr != nil {
		return heard{}, werr
	}
	if err := sleepContext(ctx, tail); err != nil {
		return heard{}, err
	}
	end := tap.MarkEpoch()

	h := heard{PeakDB: -math.MaxFloat64, RMSDB: -math.MaxFloat64, FloorDB: -180}
	if floor, ok := tap.Segment(floorStart, start); ok {
		h.FloorDB, _ = clipLevels(floor)
	}
	clip, ok := tap.Segment(start, end)
	if !ok {
		return heard{}, errors.New("the tap delivered no audio for the note (its stream stalled)")
	}
	h.Clip = clip
	h.PeakDB, h.RMSDB = clipLevels(clip)
	return h, nil
}

// clipLevels returns a clip's sample peak (over every channel) and the rms of
// its mono mix, in dBFS.
func clipLevels(clip audiotap.Clip) (peakDB, rmsDB float64) {
	ch := max(clip.Channels, 1)
	frames := len(clip.Samples) / ch
	var peak, sum float64
	for f := 0; f < frames; f++ {
		var mono float64
		for c := 0; c < ch; c++ {
			v := float64(clip.Samples[f*ch+c])
			if a := math.Abs(v); a > peak {
				peak = a
			}
			mono += v
		}
		mono /= float64(ch)
		sum += mono * mono
	}
	if frames == 0 {
		return -180, -180
	}
	return audiotap.AmpToDBFS(peak), audiotap.AmpToDBFS(math.Sqrt(sum / float64(frames)))
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
