package mcpserver

// This file records what an audio tap hears into a file: a song while it
// plays (play_song with record), or whatever sounds for a while
// (start_recording). The tap's stream is written as it arrives
// (audiotap.Capture), so a recording has no seams; when it ends, the silence
// around the music is cut and the take becomes a FLAC or WAV file under the
// state dir (internal/recording). upload_recording (drive_tools.go) puts one
// into Google Drive.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/recording"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

const (
	// defaultSilenceDB is the level under which the recorder hears silence.
	defaultSilenceDB = -60.0
	// maxRecordingSeconds bounds a recording, so one that is never stopped
	// ends by itself.
	maxRecordingSeconds = 30 * 60
)

// recorderState is the one recording the daemon makes at a time, and how the
// last one ended.
type recorderState struct {
	mu      sync.Mutex
	current *activeRecording
	last    *recordingResult

	// quietFor is how long the tap must have been silent after a song before
	// its recording ends, maxTail the longest it waits for that, and poll how
	// often it looks. maxRun is the longest a recording without a length runs
	// by the clock, for a tap that stops streaming before the capture reaches
	// its length. Zero takes the defaults; tests shorten them.
	quietFor, maxTail, poll, maxRun time.Duration
}

func (r *recorderState) timing() (quietFor, maxTail, poll, maxRun time.Duration) {
	quietFor, maxTail, poll = 1500*time.Millisecond, 10*time.Second, 50*time.Millisecond
	maxRun = maxRecordingSeconds*time.Second + 5*time.Second
	if r.quietFor > 0 {
		quietFor = r.quietFor
	}
	if r.maxTail > 0 {
		maxTail = r.maxTail
	}
	if r.poll > 0 {
		poll = r.poll
	}
	if r.maxRun > 0 {
		maxRun = r.maxRun
	}
	return
}

// activeRecording is a recording under way.
type activeRecording struct {
	meta    recording.Meta
	capture *audiotap.Capture
	format  recording.Format
	trim    bool
	upload  bool
	// seconds is the length asked for; 0 runs until stopped (or, for a song,
	// until the song and its tail have ended).
	seconds float64
	forSong bool

	mu    sync.Mutex
	phase string

	stop     chan struct{} // closed by stop_recording
	stopOnce sync.Once
	done     chan struct{} // closed once the recording is finished
	marked   sync.Once

	// awake releases the hold that keeps the host awake while the recording
	// runs: a host that dozes reads the tap's stream late, and the tap drops
	// what it cannot send.
	awake func()
}

func (ar *activeRecording) setPhase(p string) {
	ar.mu.Lock()
	ar.phase = p
	ar.mu.Unlock()
}

func (ar *activeRecording) getPhase() string {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.phase
}

// sender wraps a song's sender so the recording knows where the music starts:
// when the first note goes out, whatever the tap heard before it (a tail still
// ringing, a preset loading) no longer counts as the take's first sound. A nil
// recording leaves the sender as it is.
func (ar *activeRecording) sender(next song.Sender) song.Sender {
	if ar == nil {
		return next
	}
	return func(ctx context.Context, e song.Event) error {
		if e.Kind == song.NoteOn {
			ar.marked.Do(ar.capture.SoundFromHere)
		}
		return next(ctx, e)
	}
}

// recordingResult is how a recording ended.
type recordingResult struct {
	Meta  recording.Meta `json:"recording"`
	Path  string         `json:"path,omitempty"`
	Error string         `json:"error,omitempty"`
	// Upload is where an upload asked for with the recording stands:
	// "uploading", "uploaded" or the reason it failed.
	Upload string `json:"upload,omitempty"`
}

// recordOptions is what a recording is started with.
type recordOptions struct {
	Name      string
	Tap       string
	Seconds   float64
	Format    string
	Trim      *bool
	SilenceDB *float64
	Upload    bool
	// Song and Title name the song being recorded (play_song).
	Song, Title string
}

func (s *Server) recordings() *recording.Store { return recording.NewStore(config.RecordingsDir()) }

// loadConfig reads the config as it is now, so a changed upload folder or
// retention applies without a restart. A config that does not read falls back
// to the defaults.
func loadConfig() config.Config {
	cfg, err := config.Load()
	if err != nil {
		return config.Default()
	}
	return cfg
}

// beginRecording starts capturing a tap. The caller then runs the recording
// (runRecording) or gives it up (abortRecording).
func (s *Server) beginRecording(opts recordOptions) (*activeRecording, error) {
	format, err := recording.ParseFormat(opts.Format)
	if err != nil {
		return nil, err
	}
	if opts.Seconds < 0 || opts.Seconds > maxRecordingSeconds {
		return nil, fmt.Errorf("a recording runs for up to %d seconds", maxRecordingSeconds)
	}
	silenceDB := defaultSilenceDB
	if opts.SilenceDB != nil {
		silenceDB = *opts.SilenceDB
	}
	if silenceDB > 0 || silenceDB < -120 {
		return nil, errors.New("silence_db is a level between -120 and 0 dBFS")
	}
	if s.audio == nil {
		return nil, errors.New("this daemon has no audio tap receiver")
	}
	tap, ok := s.resolveTap(opts.Tap)
	if !ok {
		if strings.TrimSpace(opts.Tap) != "" {
			return nil, fmt.Errorf("no audio tap named %q; known taps: %s", opts.Tap, tapNamesText(s.audio.Names()))
		}
		return nil, errors.New("no audio tap has connected yet: load a session with a ProbeAudioTap in AUM (a song's session has one on its master)")
	}

	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	if cur := s.rec.current; cur != nil {
		return nil, fmt.Errorf("the recording %q is still running (stop_recording ends it)", cur.meta.Name)
	}
	store := s.recordings()
	now := time.Now()
	title := strings.TrimSpace(opts.Name)
	if title == "" {
		title = opts.Title
	}
	if title == "" {
		title = "Recording"
	}
	name, err := store.NewName(title, now)
	if err != nil {
		return nil, err
	}
	// Nothing is running, so any capture file still there was abandoned.
	store.RemoveStaleParts()
	// A timed recording ends itself at exactly its length.
	limit := float64(maxRecordingSeconds)
	if opts.Seconds > 0 {
		limit = opts.Seconds
	}
	capture, err := tap.StartCapture(store.PartPath(name), float32(math.Pow(10, silenceDB/20)), int64(math.Ceil(limit*tap.SampleRate())))
	if err != nil {
		return nil, fmt.Errorf("%w: load a session with a ProbeAudioTap in AUM", err)
	}
	ar := &activeRecording{
		meta:    recording.Meta{Name: name, Song: opts.Song, Title: title, Tap: tap.Name(), RecordedAt: now},
		capture: capture, format: format, trim: opts.Trim == nil || *opts.Trim,
		upload:  opts.Upload || (opts.Song != "" && loadConfig().GoogleDrive.AutoUpload),
		seconds: opts.Seconds, forSong: opts.Song != "",
		phase: "recording", stop: make(chan struct{}), done: make(chan struct{}),
		awake: s.holdAwake(),
	}
	s.rec.current = ar
	return ar, nil
}

// abortRecording gives up a recording that was begun but whose song never
// started: nothing is kept.
func (s *Server) abortRecording(ar *activeRecording) {
	if ar == nil {
		return
	}
	_, _ = ar.capture.Stop()
	ar.awake()
	// Only this recording's capture file: once current is cleared below, a
	// new recording may start, and its capture must not be swept away.
	_ = os.Remove(s.recordings().PartPath(ar.meta.Name))
	s.rec.mu.Lock()
	if s.rec.current == ar {
		s.rec.current = nil
	}
	s.rec.mu.Unlock()
	close(ar.done)
}

// runRecording waits for the recording's end and finishes it. It runs on its
// own goroutine.
func (s *Server) runRecording(ar *activeRecording) {
	quietFor, maxTail, poll, maxRun := s.rec.timing()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	// wait blocks until the next look is due; false when the recording was
	// stopped or its capture ended by itself.
	wait := func() bool {
		if ar.capture.State().Ended != "" {
			return false
		}
		select {
		case <-ar.stop:
			return false
		case <-tick.C:
			return true
		}
	}

	switch {
	case ar.forSong:
		// Until the song has ended (or was stopped), then until its tail has
		// rung out: the tap silent for a while, or the longest tail allowed.
		running := true
		for running && s.player.Status().Playing {
			running = wait()
		}
		ar.setPhase("tail")
		for tail := time.Now(); running && time.Since(tail) < maxTail; running = wait() {
			st := ar.capture.State()
			if quiet := float64(st.Frames-1-st.LastSound) / st.SampleRate; quiet >= quietFor.Seconds() {
				break
			}
		}
	case ar.seconds > 0:
		// Until the tap has delivered that much audio, at which the capture
		// ends itself; the clock only ends a recording whose tap went away.
		deadline := time.Now().Add(time.Duration(ar.seconds*float64(time.Second)) + 5*time.Second)
		for wait() && time.Now().Before(deadline) {
		}
	default:
		// Until stopped, or until the capture reaches its length. A tap that
		// stops streaming never gets it there, so the clock bounds it too:
		// a recording nobody stops must not run, and hold the host awake,
		// for ever.
		deadline := time.Now().Add(maxRun)
		for wait() && time.Now().Before(deadline) {
		}
	}
	s.finishRecording(ar)
}

// finishRecording stops the capture, turns it into a file and records how
// that went. The host stays held awake until the file is written and, when
// asked for, uploaded.
func (s *Server) finishRecording(ar *activeRecording) {
	defer ar.awake()
	ar.setPhase("finishing")
	store := s.recordings()
	st, err := ar.capture.Stop()
	if ar.seconds > 0 && st.Ended == audiotap.EndedFull {
		st.Ended = "" // a timed recording that ran its time ended as asked
	}
	res := &recordingResult{Meta: ar.meta}
	var meta recording.Meta
	if err == nil {
		meta, err = store.Finish(ar.meta, st, ar.format, ar.trim)
	}
	if err != nil {
		store.RemoveStaleParts()
		res.Error = err.Error()
		log.Printf("recording %q failed: %v", ar.meta.Name, err)
	} else {
		res.Meta, res.Path = meta, store.AudioPath(meta)
		if ar.upload {
			res.Upload = "uploading"
		}
		log.Printf("recorded %q: %.1f s, peak %.1f dBFS, %d stall(s)", meta.Name, meta.Seconds, meta.PeakDBFS, len(meta.Stalls))
		if gone, _ := store.Prune(loadConfig().RecordingsKeep); len(gone) > 0 {
			log.Printf("recordings: deleted the %d oldest (recordings_keep)", len(gone))
		}
	}

	s.rec.mu.Lock()
	s.rec.last = res
	if s.rec.current == ar {
		s.rec.current = nil
	}
	s.rec.mu.Unlock()
	close(ar.done)

	event := map[string]any{"event": "finished", "name": ar.meta.Name}
	if res.Error != "" {
		event["event"], event["error"] = "failed", res.Error
	} else {
		event["path"], event["seconds"] = res.Path, meta.Seconds
	}
	s.broadcast("recording", event)

	if res.Error == "" && ar.upload {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		uploaded, _, uerr := s.uploadRecording(ctx, meta, "", "", false)
		outcome := "uploaded"
		event = map[string]any{"event": "uploaded", "name": meta.Name}
		if uerr != nil {
			outcome = driveProblem(uerr)
			event["event"], event["error"] = "upload failed", outcome
			log.Printf("recording %q: upload failed: %v", meta.Name, uerr)
		} else {
			event["link"] = uploaded.Drive.Link
		}
		s.rec.mu.Lock()
		if s.rec.last == res {
			updated := *res
			updated.Upload = outcome
			if uerr == nil {
				updated.Meta = uploaded
			}
			s.rec.last = &updated
		}
		s.rec.mu.Unlock()
		s.broadcast("recording", event)
	}
}

// --- rendering ----------------------------------------------------------------

// clockTenths renders seconds as m:ss.t.
func clockTenths(seconds float64) string {
	tenths := int64(math.Round(seconds * 10))
	return fmt.Sprintf("%d:%02d.%d", tenths/600, tenths/10%60, tenths%10)
}

// missingAudioSeconds is how much less audio than time a recording may hold
// before audio counts as missing: a batch on its way and the drift between
// the tap's clock and the host's account for less.
const missingAudioSeconds = 0.1

// megabytes renders a file size.
func megabytes(bytes int64) string { return fmt.Sprintf("%.1f MB", float64(bytes)/1e6) }

// layoutText renders a channel count.
func layoutText(channels int) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	}
	return fmt.Sprintf("%d channels", channels)
}

// describeRecording renders a finished recording.
func describeRecording(m recording.Meta, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q: %s, %s at %d Hz, %d-bit %s, %s\n", m.Name, clockTenths(m.Seconds), layoutText(m.Channels), m.SampleRate,
		m.BitDepth, strings.ToUpper(string(m.Format)), megabytes(m.Bytes))
	fmt.Fprintf(&b, "  peak %.2f dBFS, mean %.2f dBFS", m.PeakDBFS, m.RMSDBFS)
	if m.Clipped > 0 {
		fmt.Fprintf(&b, ", %d sample(s) over full scale", m.Clipped)
	}
	switch {
	case m.Silent:
		b.WriteString("; silent throughout (nothing reached the silence level, so nothing was cut)")
	case m.TrimmedLeadMS > 0 || m.TrimmedTailMS > 0 || m.FadeOutMS > 0:
		fmt.Fprintf(&b, "; cut %.2f s before the first sound and %.2f s after the last", float64(m.TrimmedLeadMS)/1000, float64(m.TrimmedTailMS)/1000)
		if m.FadeOutMS > 0 {
			fmt.Fprintf(&b, ", the last %.2f s faded out", float64(m.FadeOutMS)/1000)
		}
	}
	b.WriteByte('\n')
	if n := len(m.Stalls); n > 0 {
		fmt.Fprintf(&b, "  the tap's stream arrived late %d time(s) while recording, at", n)
		for i, st := range m.Stalls {
			if i == 4 {
				fmt.Fprintf(&b, " … (%d more)", n-i)
				break
			}
			fmt.Fprintf(&b, " %s (%d ms)", clockTenths(float64(st.Frame)/float64(m.SampleRate)-float64(m.TrimmedLeadMS)/1000), st.GapMS)
		}
		// The tap drops what it cannot send, so audio that came late may also
		// be audio that is missing. The clock tells: less audio than time.
		if missing := m.WallSeconds - m.CapturedSeconds; missing > missingAudioSeconds {
			fmt.Fprintf(&b, ": about %.2f s of audio never arrived, so the take has a break there", missing)
		} else {
			b.WriteString(": all of the audio arrived, so the take is whole")
		}
		b.WriteByte('\n')
	}
	if m.Ended != "" {
		fmt.Fprintf(&b, "  it ended early: %s\n", m.Ended)
	}
	if path != "" {
		fmt.Fprintf(&b, "  file: %s\n", path)
	}
	if m.Drive != nil {
		fmt.Fprintf(&b, "  Google Drive: %s\n", m.Drive.Link)
	}
	return b.String()
}

// recordingStatus is a recording under way, for a tool's answer.
type recordingStatus struct {
	Name    string  `json:"name"`
	Song    string  `json:"song,omitempty"`
	Tap     string  `json:"tap"`
	Phase   string  `json:"phase"`
	Seconds float64 `json:"seconds"`
	Stalls  int     `json:"stalls"`
	Format  string  `json:"format"`
	Upload  bool    `json:"upload,omitempty"`
}

func (ar *activeRecording) status() recordingStatus {
	st := ar.capture.State()
	return recordingStatus{
		Name: ar.meta.Name, Song: ar.meta.Song, Tap: ar.meta.Tap, Phase: ar.getPhase(),
		Seconds: float64(st.Frames) / st.SampleRate, Stalls: len(st.Stalls), Format: string(ar.format), Upload: ar.upload,
	}
}

func (rs recordingStatus) text() string {
	what := map[string]string{
		"recording": "recording",
		"tail":      "the song has ended; waiting for its tail to ring out",
		"finishing": "writing the file",
	}[rs.Phase]
	text := fmt.Sprintf("%q from the tap %q: %s so far (%s)", rs.Name, rs.Tap, clockTenths(rs.Seconds), what)
	if rs.Stalls > 0 {
		text += fmt.Sprintf(", %d stall(s) in the stream", rs.Stalls)
	}
	return text
}

// describeResult renders how the last recording ended.
func describeResult(res *recordingResult) string {
	if res.Error != "" {
		return fmt.Sprintf("the recording %q failed: %s\n", res.Meta.Name, res.Error)
	}
	text := describeRecording(res.Meta, res.Path)
	switch res.Upload {
	case "", "uploaded":
	case "uploading":
		text += "  uploading to Google Drive (list_recordings shows the link once it is there)\n"
	default:
		text += "  the upload to Google Drive failed: " + res.Upload + "\n"
	}
	return text
}

// --- tools ----------------------------------------------------------------------

func (s *Server) registerRecordingTools() {
	if s.audio == nil {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "start_recording",
		Description: "Record what an audio tap hears into a file, from now until stop_recording, or for a number of seconds. The tap's stream is written as it arrives, so the recording has no seams; when it ends, the silence before the first sound and after the last is cut. " +
			"To record a song, use play_song with record instead: that recording follows the song and ends by itself. One recording runs at a time; list_recordings shows it and the finished ones.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "What to call the recording (default \"Recording\"); the date and time are added."},
				"seconds": {"type": "number", "exclusiveMinimum": 0, "maximum": 1800, "description": "Stop after this many seconds of audio. Default: until stop_recording (30 minutes at most)."},
				"tap": {"type": "string", "description": "Which tap to record (default: the most recently active one; a song's session has one on its master)."},
				"format": {"type": "string", "enum": ["flac", "wav"], "description": "flac (default): 24 bit, lossless, about two thirds the size. wav: 32-bit float, the stream's own samples."},
				"trim": {"type": "boolean", "description": "Cut the silence around the sound (default true)."},
				"silence_db": {"type": "number", "minimum": -120, "maximum": 0, "description": "The level under which the recorder hears silence, in dBFS (default -60). Raise it for a session with a noise floor."},
				"upload": {"type": "boolean", "description": "Upload the recording to Google Drive once it is finished (see upload_recording)."}
			}
		}`),
	}, s.handleStartRecording)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "stop_recording",
		Description: "End the running recording now and return the file it became: its path, length and levels. A song being recorded plays on (stop_song ends that). With nothing running it reports how the last recording ended.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, s.handleStopRecording)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "list_recordings",
		Description: "List the recordings: the one running now, and the finished ones, newest first, each with its file, length, levels, any stall of the tap's stream while it was made, and its Google Drive link once uploaded.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {"type": "integer", "minimum": 1, "description": "Most recordings to list (default 20)."}
			}
		}`),
	}, s.handleListRecordings)
}

func (s *Server) handleStartRecording(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name      string   `json:"name"`
		Seconds   float64  `json:"seconds"`
		Tap       string   `json:"tap"`
		Format    string   `json:"format"`
		Trim      *bool    `json:"trim"`
		SilenceDB *float64 `json:"silence_db"`
		Upload    bool     `json:"upload"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	ar, err := s.beginRecording(recordOptions{
		Name: args.Name, Tap: args.Tap, Seconds: args.Seconds, Format: args.Format,
		Trim: args.Trim, SilenceDB: args.SilenceDB, Upload: args.Upload,
	})
	if err != nil {
		return textResult("start_recording: "+err.Error(), true), nil
	}
	go s.runRecording(ar)
	st := ar.status()
	text := fmt.Sprintf("recording %q from the tap %q", st.Name, st.Tap)
	if args.Seconds > 0 {
		text += fmt.Sprintf(" for %g s", args.Seconds)
	} else {
		text += " until stop_recording"
	}
	return structResult(text+"\nlist_recordings shows it; the file is written when it ends", map[string]any{"recording": st}), nil
}

func (s *Server) handleStopRecording(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.rec.mu.Lock()
	ar := s.rec.current
	s.rec.mu.Unlock()
	if ar != nil {
		ar.stopOnce.Do(func() { close(ar.stop) })
		select {
		case <-ar.done:
		case <-ctx.Done():
			return textResult("stop_recording: the recording is still being written (list_recordings shows it when it is done)", true), nil
		}
	}
	s.rec.mu.Lock()
	res := s.rec.last
	s.rec.mu.Unlock()
	switch {
	case res == nil:
		return structResult("no recording is running and none was made since the daemon started", map[string]any{}), nil
	case ar == nil:
		return structResult("no recording is running; the last one:\n"+describeResult(res), res), nil
	}
	if res.Error != "" {
		out := structResult(describeResult(res), res)
		out.IsError = true
		return out, nil
	}
	return structResult("recorded "+describeResult(res), res), nil
}

func (s *Server) handleListRecordings(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	store := s.recordings()
	all, err := store.List()
	if err != nil {
		return textResult("list recordings: "+err.Error(), true), nil
	}
	s.rec.mu.Lock()
	ar, last := s.rec.current, s.rec.last
	s.rec.mu.Unlock()

	out := map[string]any{"dir": store.Dir(), "total": len(all)}
	var b strings.Builder
	if ar != nil {
		st := ar.status()
		out["running"] = st
		fmt.Fprintf(&b, "running: %s\n", st.text())
	}
	if last != nil && (last.Error != "" || (last.Upload != "" && last.Upload != "uploaded")) {
		// What a list of files cannot show: a recording that failed, an
		// upload that is under way or failed.
		out["last"] = last
		if last.Error != "" {
			fmt.Fprintf(&b, "last: %s", describeResult(last))
		} else if last.Upload == "uploading" {
			fmt.Fprintf(&b, "last: %q is being uploaded to Google Drive\n", last.Meta.Name)
		} else {
			fmt.Fprintf(&b, "last: the upload of %q to Google Drive failed: %s\n", last.Meta.Name, last.Upload)
		}
	}
	if len(all) > args.Limit {
		all = all[:args.Limit]
	}
	rows := make([]recordingResult, len(all))
	for i, m := range all {
		rows[i] = recordingResult{Meta: m, Path: store.AudioPath(m)}
	}
	out["recordings"] = rows
	if len(rows) == 0 {
		fmt.Fprintf(&b, "no recordings in %s yet (play_song with record, or start_recording)\n", store.Dir())
	} else {
		fmt.Fprintf(&b, "%d recording(s) in %s, newest first:\n", len(rows), store.Dir())
		for _, r := range rows {
			b.WriteString(describeRecording(r.Meta, r.Path))
		}
	}
	return structResult(b.String(), out), nil
}
