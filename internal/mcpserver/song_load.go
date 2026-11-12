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
	// Its screen is read every songPullPoll for why the file does not come
	// (unless the app says so in a sync report), and a file whose conflict
	// the daemon resolved is given songPullResolved from then.
	songPullTimeout  = 60 * time.Second
	songPullPoll     = 3 * time.Second
	songPullResolved = 20 * time.Second
	// songPullSettle is how long the daemon's own record of a download is
	// given to catch up with a sync report that says the app holds the file.
	songPullSettle = 2 * time.Second
	// songDialTimeout bounds the wait for the loaded session's brain and tap.
	songDialTimeout = 45 * time.Second
	// songPresetWait follows the preset loads of a check, songSettleWait the
	// control changes after them.
	songPresetWait = 700 * time.Millisecond
	songSettleWait = 300 * time.Millisecond
	// A check note is held for songNoteHold, or as long as the sound
	// catalogue says the track's preset takes to rise to its level, up to
	// songNoteHoldMax; a track played a second time is held songNoteHoldMax.
	// The note is listened to for songNoteTail more. Before it the tap is
	// given up to songQuietWait to fall silent, and songFloorWait then
	// measures what was sounding anyway.
	songNoteHold    = 250 * time.Millisecond
	songNoteHoldMax = 1500 * time.Millisecond
	songNoteTail    = 450 * time.Millisecond
	songFloorWait   = 120 * time.Millisecond
	songQuietWait   = 3 * time.Second
	// songMuteGap is the least time between a strip's mute and its opposite
	// (see stripMutes). AUM's largest audio buffer is 46 ms.
	songMuteGap = 150 * time.Millisecond
)

// silentDB is the peak below which a track counts as silent.
const silentDB = -60.0

// songQuietPoll is how much audio waitQuiet looks at in one go.
const songQuietPoll = 50 * time.Millisecond

func (s *Server) registerSongLoadTools(songArg string) {
	s.mcp.AddTool(&mcp.Tool{
		Name: "load_song",
		Description: "Take a saved song to a session that plays, in one call: author its AUM session (skipped when the staged one is already the song's), have the iPad pull it (the probe app is brought to the front when it does not pull by itself; the step ends on the app's report of what its sync made of the session, or on its screen for an app that sends none: a session the app holds as a conflict is named, or resolved when the iPad's copy is one the daemon staged, and a copy the app failed at is named with its error; when the pull fails AUM is brought back to the front, because the probe app left there lets the iPad lock itself), load it in AUM and wait until AUM has finished loading (an AUM that is not running any more at that point, because it crashed under the load, is started again and loads the session once more; when it is gone again the step says so, and that the iPad is on its Home Screen and locks itself if AUM could not be brought back), wait for the session's brain and tap (the session is the daemon's current one from then on: its rig and control surface are in place whatever the iPad downloaded last), then check that every track answers a note (see check_song). " +
			"Each step is reported with what was seen; a step that fails ends the call and says what to do. A locked iPad is named as that: it has to be unlocked on the device, the daemon never does. Loading replaces the session open in AUM, so it needs confirm: true. " +
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
				"sequencer": {"type": "boolean", "description": "Host the sequencer with the song in it (default: yes when Atom | Piano Roll² is installed; see author_song_session). false leaves it out."},
				"hardware": {"type": "string", "enum": ["builtin", "x32"], "description": "Override the song's session option."}
			},
			"required": ["song"]
		}`),
	}, s.handleLoadSong)

	s.mcp.AddTool(&mcp.Tool{
		Name: "check_song",
		Description: "Tell whether a song will sound if it is played now: the brain is connected, the tap is streaming, AUM has the song's session open with its strips and reports no failed plugin, and every track answers a note. " +
			"Which session is open is read from the session's master tap, which dials in under the session's id; AUM's menu is opened for the name only when no tap names the session, or with ui: true. " +
			"The mixer is looked at either way: its strips, and a plugin that did not load, which is read off a screenshot: AUM draws its node with a ring of red stripes and says so nowhere else. " +
			"For the notes, the song's own sound is set (each track's declaration, no scene) and every strip muted; then each track in turn is unmuted, the master tap is let fall silent (what still rings in the strip, such as a reverb's tail, would drown a quiet note), and the lowest of its first notes is played and the tap measured. The note is held as long as the sound catalogue says the preset takes to rise to its level (a slow pad's attack), else briefly. A track that is silent is played once more, held longer, and named with its plugin when it is silent again — that is what a failed or crashed plugin and a missing MIDI route look like from outside. The song's own mutes are put back. " +
			"Not while a song plays. ui: false leaves AUM's screen alone.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"ui": {"type": "boolean", "description": "Look at AUM's screen (when the iPad UI tools are configured). Default: the mixer's strips, alerts and failed plugins are read, and AUM's menu is opened for the session's name only when no tap names the session. true opens the menu in any case, false leaves the screen alone."},
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

// ipadApp resolves an iPad tool's app argument to a bundle id: "aum" (the
// default) and a bundle id as ipad.Client.ResolveApp gives them. The alias of
// a sideloaded app ("probe") is looked up on the device when this host can
// (go-ios), else it is the id seen before. Every tool that takes an app
// resolves it here, so the alias works on any host that saw the full id once.
func (s *Server) ipadApp(ctx context.Context, app string) (string, error) {
	alias := strings.ToLower(strings.TrimSpace(app))
	id, err := s.ipad.ResolveApp(ctx, app)
	if err == nil {
		if ipad.IsApp(alias, id) {
			rememberIPadApp(alias, id)
		}
		return id, nil
	}
	// Only an alias fails to resolve: its lookup needs go-ios and the device.
	if known := knownIPadApps()[alias]; known != "" {
		return known, nil
	}
	return "", fmt.Errorf("%w; call ipad_launch once with the app's full bundle id and the daemon remembers it", err)
}

// probeApp resolves the probe app's bundle id: on the device when this host
// can (go-ios), else the id seen before.
func (s *Server) probeApp(ctx context.Context) (string, error) {
	return s.ipadApp(ctx, "probe")
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

	// loaded is what AUM showed once the session was open.
	loaded *ipad.AUMLoaded
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
		Sequencer   *bool  `json:"sequencer"`
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
		Author:   songAuthorArgs{OutID: args.OutID, TrackTaps: args.TrackTaps, FullControl: args.FullControl, Hardware: args.Hardware, Sequencer: args.Sequencer},
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

	// 1. Author, unless the staged file is this song's session already: its
	// layout, its options, and in its sequencer the notes as they are now. A
	// sequencer that cannot be had is authorSong's to report.
	start := time.Now()
	want := songSessionOptions(cs.song, author)
	seq, seqErr := songSequencerFor(cs, songCatalog(), author.Sequencer)
	// A session staged before taps had names is authored again, with them.
	current := authored && rec.Session == outID && rec.Fingerprint == cs.timeline.Fingerprint && rec.Options == want &&
		seqErr == nil && rec.Sequence == seq.fingerprint() && rec.TapNames &&
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

	// A locked iPad pulls nothing and brings no app to the front: that is
	// said before either is waited for.
	locked := s.ipadLocked(ctx)

	// 2. The iPad pulls the file.
	start = time.Now()
	switch {
	case rec.Pulled == rec.SHA256:
		rep.skip("pull", "the iPad already has this file")
	case locked:
		pull := pullLocked()
		rep.add("pull", false, time.Time{}, "%s", pull.detail)
		return done(pull.next)
	case s.waitPulled(ctx, cs.id, rec.SHA256, songPullGrace):
		rep.add("pull", true, start, "the iPad pulled the session")
	default:
		pull := s.pullWithProbeApp(ctx, cs.id, outID, rec)
		rep.add("pull", pull.ok, start, "%s", pull.detail)
		if !pull.ok {
			return done(pull.next)
		}
	}

	// 3. AUM loads it.
	if s.ipad == nil {
		rep.add("load", false, time.Time{}, "the iPad UI tools are not configured (ipad_wda_url), so AUM was not told to load it")
		return done(fmt.Sprintf("load %q in AUM by hand, then check_song", outID))
	}
	if locked {
		rep.add("load", false, time.Time{}, "%s: AUM cannot be brought to the front to load the session", ipadLockedText)
		return done(ipadUnlock + ", then load_song again")
	}
	start = time.Now()
	gen := s.loaded.generation()
	dir, name := path.Split(outID)
	var folders []string
	if dir = strings.Trim(dir, "/"); dir != "" {
		folders = strings.Split(dir, "/")
	}
	// An AUM that goes away under the load is started again and loads the
	// session once more (AUMOpenSession); what is left to say here is an AUM
	// that went away again.
	res, err := s.ipad.AUMOpenSession(ctx, folders, name)
	if err != nil {
		why, nowLocked := s.ipadFailure(ctx, err)
		switch {
		case nowLocked:
			rep.add("load", false, start, "%s", why)
			return done(ipadUnlock + ", then load_song again")
		case ipad.AppNotRunning(err):
			detail, next := s.aumGone(ctx, outID, err)
			rep.add("load", false, start, "%s", detail)
			return done(next)
		}
		rep.add("load", false, start, "%s", why)
		// AUM did not come forward: the probe app may still be, after a pull.
		return done(s.withFrontNote("look at the iPad (ipad_screenshot); AUM must be running and its mixer reachable, then load_song again"))
	}
	loaded, _ := res.Data.(ipad.AUMLoaded)
	rep.loaded = &loaded
	if !res.Verified {
		rep.add("load", false, start, "%s", res.Message)
		return done("look at the iPad (ipad_screenshot), then load_song again")
	}
	rep.add("load", true, start, "%s", res.Message)

	// 4. The new session's brain and tap dial in.
	start = time.Now()
	brain, tap, named := s.waitRig(ctx, gen, outID, songDialTimeout)
	if !brain || tap == nil {
		var missing []string
		if !brain {
			missing = append(missing, "the ProbeMidiBrain did not connect")
		}
		if tap == nil {
			missing = append(missing, "the ProbeAudioTap is not streaming")
		}
		rep.add("connect", false, start, "%s", strings.Join(missing, "; "))
		return done("open the plugin's window in AUM and read its status line: it names the daemon it dials. A plugin AUM shows as failed needs the session loaded again (load_song)")
	}
	s.loaded.set(outID)
	// AUM has this session open: it is the daemon's current one, whatever the
	// iPad downloaded last, and its rig and control surface are the ones in
	// place before the song is checked or played. A master tap that dialled
	// in under the session's id has said so already (OnAudioTapConnected);
	// for one that gives no name this is where the daemon learns it.
	s.sessionOpen(outID, "load_song loaded the session")
	if named {
		rep.add("connect", true, start, "brain and tap connected; the tap dialled in as %q", tap.Name())
	} else {
		rep.add("connect", true, start, "brain and tap connected; the tap gave no name (a ProbeAudioTap from before taps had names), so the session is known from AUM's screen only")
	}

	// 5. Does it sound?
	check := s.checkSong(ctx, cs, checkOptions{Sound: args.Check, Loaded: &loaded})
	rep.Check = &check
	rep.OK = check.OK
	if !check.OK {
		return done(check.Next)
	}
	return done("")
}

// aumGone ends a load step at an AUM that is not running although the load
// started it again once (ipad.AUMOpenSession): err is what WebDriverAgent
// said then. Left like that, the iPad is on its Home Screen, locks itself
// after its auto-lock time and needs its passcode by hand. So AUM is brought
// to the front one more time, with nothing loaded: an AUM that does not load
// the session still keeps the iPad awake. Where that fails too, the step says
// that the iPad is about to lock itself.
func (s *Server) aumGone(ctx context.Context, outID string, err error) (detail, next string) {
	const report = "its crash report on the iPad says where it failed (Settings > Privacy & Security > Analytics & Improvements > Analytics Data, the newest entry of AUM)"
	detail = fmt.Sprintf("AUM crashed or is not running: it went away at the load, and when the daemon started it again to load the session once more it did not stay up (%v)", err)
	if ctx.Err() != nil {
		// AUMOpenSession starts nothing again for a call that is over.
		detail = fmt.Sprintf("AUM crashed or is not running: it went away at the load (%v), and the call ended before the daemon started it again to load the session", err)
	}
	// The call may be over; AUM comes back all the same.
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pullAUMBackTimeout)
	defer cancel()
	if berr := s.ipad.BringToFront(bctx, ipad.AUMBundleID); berr != nil {
		if s.ipadLocked(bctx) {
			return fmt.Sprintf("%s. Now %s (%v)", detail, ipadLockedText, berr), ipadUnlock + ", then load_song again"
		}
		next = fmt.Sprintf("start AUM on the iPad before the iPad locks itself (ipad_launch without app, or by hand), then load_song again. If AUM goes away whenever it loads %q, %s", outID, report)
		if front, _ := s.ipad.Front(); front != ipad.HomeScreenBundleID {
			// AUM never came up for a session, so what was in front before
			// still is: the probe app after a pull, which the note names.
			return fmt.Sprintf("%s. The daemon could not bring it to the front after that either (%v), so AUM is not in front, and nothing else is known to keep the iPad awake: it locks itself after its auto-lock time and then needs its passcode entered on the device", detail, berr),
				s.withFrontNote(next)
		}
		return fmt.Sprintf("%s. The daemon could not bring it back to the front after that either (%v), so %s", detail, berr, ipadHomeScreen), next
	}
	return detail + ". The daemon then started AUM once more and loaded nothing, so the iPad is not left on its Home Screen, where nothing keeps it awake and it locks itself",
		fmt.Sprintf("AUM runs with no session loaded: load_song again. If AUM goes away whenever it loads %q, %s", outID, report)
}

// waitRig waits for a brain connection made after generation gen and for the
// tap of the session just loaded to stream: its master tap, which dials in
// under the session's id (named). A tap that gives no name is taken once the
// brain is there and none came under the name within songTapNameGrace. tap
// is nil when none streams.
func (s *Server) waitRig(ctx context.Context, gen uint64, session string, d time.Duration) (brain bool, tap *audiotap.Store, named bool) {
	deadline := time.Now().Add(d)
	var unnamedSince time.Time
	for {
		brain = s.midi != nil && s.midi.Connected() && s.loaded.generation() > gen
		tap, named = s.songTap(session)
		if !s.streaming(ctx, tap) {
			tap = nil
		}
		switch {
		case !brain || tap == nil:
			unnamedSince = time.Time{}
		case named:
			return brain, tap, named
		case unnamedSince.IsZero():
			unnamedSince = time.Now()
		case time.Since(unnamedSince) >= songTapNameGrace:
			return brain, tap, named
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return brain, tap, named
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// --- check_song -----------------------------------------------------------------

// look says how much of AUM's screen check_song reads.
type look int

const (
	// lookAuto reads the mixer, and opens AUM's menu for the session's name
	// only when no tap names the open session.
	lookAuto look = iota
	// lookAlways opens the menu as well, whatever the taps say.
	lookAlways
	// lookNever leaves AUM's screen alone.
	lookNever
)

// checkOptions selects what check_song looks at.
type checkOptions struct {
	// UI reads AUM's screen; Loaded is that reading when the caller has it
	// already.
	UI     look
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
	// FloorDB is the peak of what was sounding just before the note, when
	// that was above silence: the note had to stand 6 dB over it.
	FloorDB float64 `json:"floor_db,omitempty"`
	// HoldMS is how long the note was held (the last time it was played).
	HoldMS int64 `json:"hold_ms,omitempty"`
	Sounds bool  `json:"sounds"`
	// Tries is how often the note was played: a track that stays silent is
	// played a second time before it is called silent.
	Tries int `json:"tries,omitempty"`
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
		// A note that answered says when it was held longer than usual: for
		// the preset's attack, or because it was played again.
		held := ""
		if t.HoldMS > songNoteHold.Milliseconds() {
			held = fmt.Sprintf(", held %d ms", t.HoldMS)
		}
		switch {
		case t.Skipped != "":
			fmt.Fprintf(&b, "    %-10s %-22s not played: %s\n", t.Track, t.Plugin, t.Skipped)
		case t.Sounds && t.Tries > 1:
			fmt.Fprintf(&b, "    %-10s %-22s %-4s peak %6.1f dBFS%s (silent at first, answered when played again)\n", t.Track, t.Plugin, song.NoteName(t.Note), t.PeakDB, held)
		case t.Sounds:
			fmt.Fprintf(&b, "    %-10s %-22s %-4s peak %6.1f dBFS%s\n", t.Track, t.Plugin, song.NoteName(t.Note), t.PeakDB, held)
		default:
			floor := ""
			if t.FloorDB != 0 {
				floor = fmt.Sprintf(" over a floor of %.1f dBFS", t.FloorDB)
			}
			fmt.Fprintf(&b, "    %-10s %-22s %-4s SILENT (peak %.1f dBFS%s, played %d times)\n", t.Track, t.Plugin, song.NoteName(t.Note), t.PeakDB, floor, t.Tries)
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
	ui := lookAuto
	if args.UI != nil {
		ui = lookNever
		if *args.UI {
			ui = lookAlways
		}
	}
	check := s.checkSong(ctx, cs, checkOptions{UI: ui, Sound: args.Sound == nil || *args.Sound})
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

	// What the open session's master tap says: it dials in under the id of
	// the session it was authored into.
	named := s.tapSession()
	if named != "" && named != rec.Session {
		c.find("aum", false, "AUM has %q open (its ProbeAudioTap dialled in under that name), not the song's session %q", named, rec.Session)
		c.Next = "load_song"
		return c
	}

	// What AUM shows: its strips, what it reports or draws as failed, and in
	// its menu the session's name. The menu is opened only when no tap has
	// named the session, or when that was asked for.
	loaded := o.Loaded
	if loaded == nil && s.ipad != nil && o.UI != lookNever {
		read := s.ipad.AUMSession
		if named != "" && o.UI == lookAuto {
			read = s.ipad.AUMMixer
		}
		res, err := read(ctx)
		if err != nil {
			why, locked := s.ipadFailure(ctx, err)
			c.find("aum", false, "AUM's screen could not be read: %s", why)
			if locked {
				c.Next = ipadUnlock + ", then check_song again"
			}
		} else if l, ok := res.Data.(ipad.AUMLoaded); ok {
			loaded = &l
		}
	}
	if loaded != nil {
		// The session as the findings name it: by AUM's menu when that was
		// read, else by the tap.
		open := fmt.Sprintf("%q", loaded.Session)
		if loaded.Session == "" && named != "" {
			open = fmt.Sprintf("%q (its ProbeAudioTap dialled in under that name)", named)
		} else if want := path.Base(rec.Session); !strings.EqualFold(loaded.Session, want) {
			c.find("aum", false, "AUM has %q open, not the song's session %q", loaded.Session, want)
			c.Next = "load_song"
			return c
		}
		s.loaded.set(rec.Session)
		if missing := missingStrips(cs, loaded.Strips); len(missing) > 0 {
			c.find("aum", false, "AUM has %s open but no strip for: %s (strips: %s)", open, strings.Join(missing, ", "), strings.Join(loaded.Strips, ", "))
		} else {
			c.find("aum", true, "AUM has %s open with the song's %d strip(s)", open, len(loaded.Strips))
		}
		if len(loaded.Problems) > 0 {
			c.find("plugins", false, "AUM reports: %s", strings.Join(loaded.Problems, "; "))
		}
	} else if named != "" {
		s.loaded.set(rec.Session)
		c.find("aum", true, "AUM has %q open: its ProbeAudioTap dialled in under that name", named)
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
	tap, tapNamed := s.songTap(rec.Session)
	switch {
	case !s.streaming(ctx, tap):
		tap = nil
		c.find("tap", false, "no ProbeAudioTap is streaming")
	case tapNamed:
		c.find("tap", true, "ProbeAudioTap %q streaming%s", tap.Name(), s.trackTapsNote(cs, rec))
	default:
		c.find("tap", true, "ProbeAudioTap streaming")
	}
	if !brain || tap == nil {
		c.Next = "load_song loads the session again; if the plugins are there but do not connect, open one's window in AUM and read its status line"
		return c
	}

	if o.Sound {
		tracks, err := s.soundCheck(ctx, cs, tap)
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

// trackTapsNote says, for a session authored with a tap on every track, how
// many of those are connected, and names the tracks whose tap is not: mix_song
// then measures the tracks by solo passes. "" for a session without them.
func (s *Server) trackTapsNote(cs *compiledSong, rec songSessionRecord) string {
	if !rec.Options.TrackTaps {
		return ""
	}
	taps, missing := s.trackTaps(cs, rec.Session)
	if len(missing) == 0 {
		return fmt.Sprintf(", and the taps of its %d track(s)", len(taps))
	}
	return fmt.Sprintf("; no tap is connected for: %s", strings.Join(missing, ", "))
}

// soundCheck sets the song's own sound and plays every track's first note
// alone, measuring the tap (the master's) after each.
func (s *Server) soundCheck(ctx context.Context, cs *compiledSong, tap *audiotap.Store) ([]trackCheck, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	// A dozing host stalls the tap's stream and the waits between the notes.
	defer s.holdAwake()()
	if tap == nil {
		return nil, errors.New("no audio tap")
	}
	home, err := cs.plan.HomeState()
	if err != nil {
		return nil, err
	}
	if err := s.sendSongState(ctx, home); err != nil {
		return nil, err
	}

	first := checkNotes(cs.plan, cs.timeline)
	// Every strip is muted but the one being checked: what an earlier track
	// left ringing in its delay or reverb is then not taken for an answer,
	// nor does it drown a quiet one. The song's own mutes come back at the
	// end, whatever happens on the way.
	mutes := newStripMutes(s, cs.plan)
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, tp := range cs.plan.Tracks {
			_ = mutes.set(rctx, tp, tp.HomeMute())
		}
	}()
	for _, tp := range cs.plan.Tracks {
		if err := mutes.set(ctx, tp, true); err != nil {
			return nil, err
		}
	}
	var out []trackCheck
	for _, tp := range cs.plan.Tracks {
		row := trackCheck{Track: tp.ID, Plugin: tp.Nodes[0].Name}
		e, plays := first[tp.ID]
		if !plays {
			row.Skipped = "it plays no note in the song"
			if len(tp.ControlChannels) > 0 {
				row.Skipped = "it plays no note on its own channel in the song"
			}
			out = append(out, row)
			continue
		}
		row.Note = e.Data1
		hold := s.checkHold(tp)
		// A track that stays silent is played once more, its strip opened
		// again, before it is called silent: one lost message or a plugin
		// still busy with its preset is not a dead plugin. The second note is
		// held the longest, for a slow attack the catalogue does not know.
		for row.Tries < 2 && !row.Sounds {
			row.Tries++
			if row.Tries > 1 {
				hold = max(hold, songNoteHoldMax)
			}
			if err := mutes.set(ctx, tp, false); err != nil {
				return out, err
			}
			// What still rings in the strip, a reverb's tail or the first
			// try's own note, is let die before the floor is measured: a
			// note has to stand 6 dB over the floor to count.
			if err := s.waitQuiet(ctx, tap, songQuietWait); err != nil {
				return out, err
			}
			h, err := s.hearNote(ctx, tap, e.Channel, e.Data1, 100, hold, songNoteTail)
			if err != nil {
				return out, err
			}
			row.PeakDB, row.RMSDB = round1(h.PeakDB), round1(h.RMSDB)
			row.FloorDB = 0
			if h.FloorDB > silentDB {
				row.FloorDB = round1(h.FloorDB)
			}
			row.HoldMS = hold.Milliseconds()
			row.Sounds = h.Sounds()
		}
		if err := mutes.set(ctx, tp, true); err != nil {
			return out, err
		}
		out = append(out, row)
	}
	return out, nil
}

// checkNotes picks every track's check note: the first one it plays; of
// several that start together the lowest, which on a drum track is the kick
// and not a hat. Only a note on the track's own channel counts: one on a
// control channel mutes or randomises, and played alone it is silence.
func checkNotes(plan *song.Plan, tl *song.Timeline) map[string]song.Event {
	own := map[string]int{}
	for _, tp := range plan.Tracks {
		own[tp.ID] = tp.Channel
	}
	first := map[string]song.Event{}
	for _, e := range tl.Events {
		if e.Kind != song.NoteOn || e.Channel != own[e.Track] {
			continue
		}
		if have, seen := first[e.Track]; !seen || (e.Tick == have.Tick && e.Data1 < have.Data1) {
			first[e.Track] = e
		}
	}
	return first
}

// checkHold is how long a track's check note is held: as long as the sound
// catalogue says the track's preset takes from the note to its level (its
// latency and attack), at least songNoteHold and at most songNoteHoldMax. On a
// device a string pad held 250 ms peaked at -33 dBFS, 17 dB under what it
// reached held for 2 s, and did not stand 6 dB over what was ringing before.
func (s *Server) checkHold(tp *song.TrackPlan) time.Duration {
	hold := songNoteHold
	if s.sounds == nil || len(tp.Nodes) == 0 {
		return hold
	}
	n := tp.Nodes[0]
	m := s.sounds.Sound(n.Plugin, n.HomePreset())
	if m == nil || m.Silent() {
		return hold
	}
	rise := time.Duration((m.LatencyMS + m.AttackMS) * float64(time.Millisecond))
	return min(max(hold, rise), max(hold, songNoteHoldMax))
}

// waitQuiet waits until the audio the tap carries peaks below silentDB, for at
// most d. The tap's own level (settleAudio) calls a reverb's tail at -40 dBFS
// rms quiet, and that tail is then the floor a quiet note is measured against.
func (s *Server) waitQuiet(ctx context.Context, tap *audiotap.Store, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		from := tap.MarkEpoch()
		if err := sleepContext(ctx, songQuietPoll); err != nil {
			return err
		}
		clip, ok := tap.Segment(from, tap.MarkEpoch())
		if !ok {
			return nil // no audio came: hearNote says so
		}
		if peak, _ := clipLevels(clip); peak <= silentDB || !time.Now().Before(deadline) {
			return nil
		}
	}
}

// stripMutes sends strip mutes no closer together than AUM takes them. Two
// opposite mutes for one strip that reach AUM in the same audio buffer can
// leave the strip at the first of them: on a device a mute followed at once
// by its unmute left the strip muted in 2 of 10 tries, and in none of 24 when
// the two were 30 ms or more apart. So a strip's mute waits until songMuteGap
// has passed since its opposite was sent.
type stripMutes struct {
	s    *Server
	last map[string]stripMute
}

type stripMute struct {
	muted bool
	at    time.Time
}

// newStripMutes starts from a song's home state sent just now: every strip
// stands at the mute its track declares.
func newStripMutes(s *Server, plan *song.Plan) *stripMutes {
	m := &stripMutes{s: s, last: map[string]stripMute{}}
	now := time.Now()
	for _, tp := range plan.Tracks {
		m.last[tp.ID] = stripMute{muted: tp.HomeMute(), at: now}
	}
	return m
}

// set mutes or unmutes a track's strip.
func (m *stripMutes) set(ctx context.Context, tp *song.TrackPlan, muted bool) error {
	if last, sent := m.last[tp.ID]; sent && last.muted != muted {
		if err := sleepContext(ctx, songMuteGap-time.Since(last.at)); err != nil {
			return err
		}
	}
	err := m.s.sendBrain(ctx, songCommand(tp.MuteEvent(muted)))
	m.last[tp.ID] = stripMute{muted: muted, at: time.Now()}
	return err
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
