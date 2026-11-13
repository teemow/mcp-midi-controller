package mcpserver

// This file records a song a group of tracks at a time with AUM's own
// recorder, and mixes the groups from their recordings: for a song the iPad
// cannot compute in one pass, and for one of more tracks than a session
// hosts, which plays in no other way (song_large.go).
//
// A stem is a group of the song's tracks (song.Stems). record_song_stems takes
// them one after another: it authors and loads the stem's own session, which
// hosts those tracks and nothing else of the song, arms the master strip, has
// AUM's recorder run with the transport while the session's sequencer plays
// the stem, and finds the take in AUM's list of recordings. What it found is
// kept (song-stems.json), so mix_song_stems can write the mix from it at any
// time after: a song whose tracks are those files, under the whole song's
// master effects.
//
// AUM's recorder writes on the iPad, in the audio thread: no wireless link
// carries the audio, the file begins with the transport's first sample, and
// the take ends on a bar line. So stems recorded minutes apart line up on the
// sample when File Players start them together at the transport's top.
//
// Measured on a device (AUM 1.4.8): the transport's Toggle Record and a
// strip's Rec enable, mapped to a controller, latch by value as a mute does.
// 127 starts the recorder, and the transport with it when it stands; a second
// 127 changes nothing; 0 ends the take, on the next bar line while the
// transport runs and at once when it stands. A second 0 before that bar line
// withdraws the first: the recorder runs on. A recorder left on records on
// through a stopped transport. So one message ends a take, and it is the
// stem's own sequence that sends it (song.RecorderOffBeats); the daemon
// switches the recorder off only where it knows the sequence has not, with
// the transport stopped. The take is <session>/<strip> - <tempo> bpm -
// <nnn>.wav in AUM's Recordings; AUM's list of them gives each file's date,
// size and format and no length. A File Player gives the length, in frames.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/lanhttp"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

var (
	// stemArmSettle is how long AUM is given to arm the master after the
	// message that does it, before its screen is read for the arm.
	stemArmSettle = 400 * time.Millisecond
	// stemEndMargin is added to the bar the transport runs on for after the
	// stem's last bar line, where its sequence has the recorder end the take.
	stemEndMargin = 500 * time.Millisecond
	// stemListWait is how long a finished take is given to appear in AUM's
	// list of recordings, which is read every stemListPoll. A take that has
	// not appeared after stemOffAfter was not ended by the stem's sequence:
	// the daemon then switches the recorder off itself, once.
	stemListWait = 20 * time.Second
	stemListPoll = 2 * time.Second
	stemOffAfter = 6 * time.Second
	// stemLoadEstimate is what loading a stem's session is reckoned at when
	// the job says how long it will take.
	stemLoadEstimate = 75 * time.Second
	// stemsWaitMost bounds get_song_stems' wait.
	stemsWaitMost = 55 * time.Second
)

// stemWholeBeat is how far from a whole number of beats a take may be and
// still count as whole beats long. A take that began with the transport and
// ended on a bar line was 7 frames short of its beats on a device, 0.0003 of
// a beat at 120 BPM.
const stemWholeBeat = 0.005

// --- what is kept -------------------------------------------------------------

// stemTake is one stem and its recording.
type stemTake struct {
	Stem   string   `json:"stem"`
	Tracks []string `json:"tracks"`
	// Session is the staged session the stem was recorded from.
	Session string `json:"session,omitempty"`
	// File is the take in AUM's Recordings folder, by its path there: what a
	// track of the mix names as its file.
	File string `json:"file,omitempty"`
	// Listed is what AUM's list of recordings says of the file: its date,
	// its size and its format.
	Listed string `json:"listed,omitempty"`
	// Frames, Seconds and Beats are the file's length as a File Player that
	// has it loaded shows it, which is where the daemon can read it: they are
	// filled in when the mix is loaded (mix_song_stems, check_song), and 0
	// until then. Whole says whether that is a whole number of beats, nil
	// while the length is not known. Format is the file's format as shown
	// there.
	Frames  int64   `json:"frames,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Beats   float64 `json:"beats,omitempty"`
	Whole   *bool   `json:"whole_beats,omitempty"`
	Format  string  `json:"format,omitempty"`
	// PeakDB and RMSDB are the take as the master's tap heard it while it
	// was recorded; Stalls counts where the tap's stream did not deliver
	// (the levels read low there; the file is not affected).
	PeakDB *float64 `json:"peak_db,omitempty"`
	RMSDB  *float64 `json:"rms_db,omitempty"`
	Stalls int      `json:"tap_stalls,omitempty"`
	// Fingerprint identifies what was recorded: the stem's session and the
	// notes and control changes its sequencer played.
	Fingerprint string `json:"fingerprint,omitempty"`
	RecordedAt  string `json:"recorded_at,omitempty"`
	// Notes are what else there is to say of the take.
	Notes []string `json:"notes,omitempty"`
	// Kept marks a take from an earlier run that this run left alone, and
	// Error says why a stem has no take.
	Kept  bool   `json:"kept,omitempty"`
	Error string `json:"error,omitempty"`
}

// songStems is the stems a song was last recorded as.
type songStems struct {
	Song   string          `json:"song"`
	Layout song.StemLayout `json:"layout"`
	Stems  []stemTake      `json:"stems"`
}

// groups is the grouping the stems were recorded with, as song.Stems takes it.
func (st songStems) groups() map[string][]string {
	out := map[string][]string{}
	for _, t := range st.Stems {
		out[t.Stem] = slices.Clone(t.Tracks)
	}
	return out
}

// take returns the kept take of a stem with these tracks, nil when there is
// none.
func (st songStems) take(stem song.Stem) *stemTake {
	for i := range st.Stems {
		if st.Stems[i].Stem == stem.Name && slices.Equal(st.Stems[i].Tracks, stem.Tracks) {
			return &st.Stems[i]
		}
	}
	return nil
}

// loadSongStems reads the record of recorded stems (empty when there is none).
func loadSongStems() map[string]songStems {
	out := map[string]songStems{}
	data, err := os.ReadFile(config.SongStemsPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// saveSongStems writes one song's stems into the record.
func (s *Server) saveSongStems(st songStems) error {
	s.stemsMu.Lock()
	defer s.stemsMu.Unlock()
	all := loadSongStems()
	all[st.Song] = st
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	p := config.SongStemsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return lanhttp.WriteFileAtomic(p, data, 0o644)
}

// --- the plan -----------------------------------------------------------------

// stemPlan is a song taken apart: its stems, each as a compiled song of its
// own, and what they share.
type stemPlan struct {
	id     string
	src    *song.Song
	layout song.StemLayout
	stems  []song.Stem
	songs  []*compiledSong
	// notes is the whole song's notes without a session's controls, where a
	// summary of a song of more tracks than one session hosts needs its form
	// (compileStems); nil otherwise.
	notes *song.Timeline
}

// stemSongID is the id a stem's song is known by. No stored song has it: a
// stored id holds no slash.
func stemSongID(songID, stem string) string { return songID + "/stem/" + stem }

// stemFingerprint identifies what a stem's take holds: its session, and what
// the session's sequencer plays in it.
func stemFingerprint(cs *compiledSong) string {
	return cs.timeline.Fingerprint + "/" + cs.timeline.Sequence().Fingerprint()
}

// planStems takes a stored song apart. groups replaces the document's
// grouping when it is not nil; tailBars 0 takes the default tail.
func (s *Server) planStems(id string, groups map[string][]string, tailBars int) (*stemPlan, error) {
	src, _, err := s.songs.Load(id)
	if err != nil {
		return nil, fmt.Errorf("/song: %w", err)
	}
	return s.planStemsOf(song.ID(id), src, groups, tailBars)
}

// planStemsOf takes a song apart, stored or not: each stem is planned and
// compiled as the song it is recorded from, and nothing is asked of the whole
// song but that it is a valid document. So a song of more tracks than one
// session hosts is planned here, and nowhere else.
func (s *Server) planStemsOf(id string, src *song.Song, groups map[string][]string, tailBars int) (*stemPlan, error) {
	var err error
	p := &stemPlan{id: id, src: src}
	if p.stems, err = src.Stems(groups); err != nil {
		return nil, err
	}
	if p.layout, err = src.StemLayout(tailBars); err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, st := range p.stems {
		title := sessionFileName(src.Title + " stem " + st.Name)
		// Two stems whose names differ only in what a file name cannot hold
		// would share a session and a folder of recordings.
		if other, taken := seen[strings.ToLower(title)]; taken {
			return nil, fmt.Errorf("the stems %q and %q would both be recorded as %q: give one another name", other, st.Name, title)
		}
		seen[strings.ToLower(title)] = st.Name
		sg, err := src.StemSong(st, title, p.layout)
		if err != nil {
			return nil, err
		}
		cs, err := s.compileSong(stemSongID(p.id, st.Name), sg)
		if err != nil {
			return nil, fmt.Errorf("the stem %q does not compile against the staged plugins: %w", st.Name, err)
		}
		p.songs = append(p.songs, cs)
	}
	return p, nil
}

// expectedBeats is how many beats a stem's take is long at the least: the
// lead-in, the song and the tail.
func (p *stemPlan) expectedBeats() float64 {
	if len(p.songs) == 0 {
		return 0
	}
	tl := p.songs[0].timeline
	return tl.Sequence().Duration().Seconds() * tl.Tempo / 60
}

// --- AUM's screen -------------------------------------------------------------

// stemDeck is what recording stems asks of AUM's screen: its list of
// recordings, the master's record-arm, and what a File Player shows. The
// daemon reads and taps AUM's UI for them; the tests put a stand-in here.
type stemDeck interface {
	// Recordings lists the files of a session's folder in AUM's Recordings.
	// A folder that is not there yet has none, and is no error.
	Recordings(ctx context.Context, folder string) ([]ipad.AUMFileEntry, error)
	// Arm makes sure the master strip is armed for recording, or is not. It
	// says whether it had to tap for that: false when AUM showed it so
	// already.
	Arm(ctx context.Context, on bool) (tapped bool, err error)
	// FilePlayer reads what the File Player of a strip shows.
	FilePlayer(ctx context.Context, strip string) (filePlayerView, error)
}

// filePlayerView is what a File Player's panel shows of its file.
type filePlayerView struct {
	// File is the selected file's name, "" for a player without one.
	File string `json:"file,omitempty"`
	// Frames is its length and Rate its sample rate; Format the line AUM
	// prints of it ("WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz").
	Frames int64   `json:"frames,omitempty"`
	Rate   float64 `json:"sample_rate,omitempty"`
	Format string  `json:"format,omitempty"`
	// Loop and Sync are the two switches as shown ("ON", "OFF").
	Loop string `json:"loop,omitempty"`
	Sync string `json:"sync,omitempty"`
	// Text is every row, for a message.
	Text string `json:"text,omitempty"`
}

// The rows of AUM's FILE PLAYER panel: "Duration 1'151'993 frames ·  00:23.9"
// and "Format WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz".
var (
	reFrames     = regexp.MustCompile(`([0-9][0-9'’,. ]*)\s*frames`)
	reSampleRate = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*(k?)Hz`)
)

// parseFilePlayer reads a FILE PLAYER panel's rows: the file is the row under
// "Selected file", the others are a label and a value.
func parseFilePlayer(rows []ipad.AUMRow) filePlayerView {
	var v filePlayerView
	var parts []string
	for i, r := range rows {
		label, value := strings.TrimSpace(r.Label), strings.TrimSpace(r.Value)
		if label == "" || (value == "" && i > 0 && strings.TrimSpace(rows[i-1].Value) == label) {
			continue // a value AUM also lists as a row of its own
		}
		switch strings.ToLower(label) {
		case "selected file":
			// The next row without a value is the file's name.
			for _, n := range rows[i+1:] {
				if strings.TrimSpace(n.Value) != "" || strings.EqualFold(strings.TrimSpace(n.Label), "options") {
					break
				}
				v.File = strings.TrimSpace(n.Label)
				break
			}
			continue
		case "duration":
			if m := reFrames.FindStringSubmatch(value); m != nil {
				digits := strings.Map(func(c rune) rune {
					if c >= '0' && c <= '9' {
						return c
					}
					return -1
				}, m[1])
				v.Frames, _ = strconv.ParseInt(digits, 10, 64)
			}
		case "format":
			v.Format = value
			if m := reSampleRate.FindStringSubmatch(value); m != nil {
				v.Rate, _ = strconv.ParseFloat(m[1], 64)
				if m[2] == "k" {
					v.Rate *= 1000
				}
			}
		case "loop":
			v.Loop = value
		case "sync":
			v.Sync = value
		}
		if value != "" {
			parts = append(parts, label+" "+value)
		} else if label != v.File {
			parts = append(parts, label)
		}
	}
	if v.File != "" {
		parts = append([]string{v.File}, parts...)
	}
	v.Text = strings.Join(parts, " | ")
	return v
}

// ipadDeck is the stemDeck that drives AUM over WebDriverAgent.
type ipadDeck struct{ c *ipad.Client }

func (d ipadDeck) Recordings(ctx context.Context, folder string) ([]ipad.AUMFileEntry, error) {
	res, err := d.c.AUMFiles(ctx, ipad.AUMRequest{Action: "recordings", Path: []string{folder}})
	// FILES stays open on the list it showed: close it, whatever it showed.
	defer func() { _, _ = d.c.AUMPanel(context.WithoutCancel(ctx), ipad.AUMRequest{Action: "close"}) }()
	var none *ipad.NoFolderError
	switch {
	case errors.As(err, &none):
		return nil, nil
	case err != nil:
		return nil, err
	}
	entries, _ := res.Data.([]ipad.AUMFileEntry)
	return entries, nil
}

func (d ipadDeck) Arm(ctx context.Context, on bool) (bool, error) {
	res, err := d.c.AUMChannel(ctx, ipad.AUMRequest{Action: "arm", Channel: "Master", On: &on})
	if err != nil {
		return false, err
	}
	if !res.Verified {
		return true, fmt.Errorf("%s", res.Message)
	}
	return !strings.Contains(res.Message, "already"), nil
}

func (d ipadDeck) FilePlayer(ctx context.Context, strip string) (filePlayerView, error) {
	res, err := d.c.AUMFilePlayer(ctx, ipad.AUMRequest{Action: "read", Channel: strip})
	// The panel stays open over the mixer: close it, whatever it showed.
	defer func() { _, _ = d.c.AUMPanel(context.WithoutCancel(ctx), ipad.AUMRequest{Action: "close"}) }()
	if err != nil {
		return filePlayerView{}, err
	}
	rows, _ := res.Data.([]ipad.AUMRow)
	return parseFilePlayer(rows), nil
}

// aumDeck is the deck the stems tools use, nil without the iPad UI tools.
func (s *Server) aumDeck() stemDeck {
	if s.deck != nil {
		return s.deck
	}
	if s.ipad == nil {
		return nil
	}
	return ipadDeck{s.ipad}
}

// setLength notes a take's length down as a File Player showed it, and whether
// that is whole beats at tempo.
func (t *stemTake) setLength(v filePlayerView, tempo float64) {
	if v.Frames <= 0 || v.Rate <= 0 {
		return
	}
	t.Frames, t.Format = v.Frames, v.Format
	t.Seconds = float64(v.Frames) / v.Rate
	t.Beats = t.Seconds * tempo / 60
	whole := math.Abs(t.Beats-math.Round(t.Beats)) <= stemWholeBeat
	t.Whole = &whole
}

// reTakeNumber is the number AUM ends a take's name with ("Master - 120 bpm -
// 004"), counting up in the session's folder.
var reTakeNumber = regexp.MustCompile(`(\d+)(?:\.[A-Za-z0-9]+)?$`)

// newRecording picks the take out of a folder's list: the file that was not
// there before. Of several, the master's with the highest number is the one:
// AUM's list is not always in the order of the takes.
func newRecording(before, after []ipad.AUMFileEntry) (ipad.AUMFileEntry, bool) {
	was := map[string]bool{}
	for _, e := range before {
		was[e.Name] = true
	}
	number := func(e ipad.AUMFileEntry) int {
		n := -1
		if m := reTakeNumber.FindStringSubmatch(e.Name); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		if strings.HasPrefix(strings.ToLower(e.Name), "master") {
			n += 1 << 20 // the master's before any other strip's
		}
		return n
	}
	var take ipad.AUMFileEntry
	found := false
	for _, e := range after {
		if !was[e.Name] && (!found || number(e) > number(take)) {
			take, found = e, true
		}
	}
	return take, found
}

// recordingFile is a listed take as a File Player's file: its path in AUM's
// Recordings. AUM's list prints a file's name without its extension and the
// format last in the line under it ("12. Nov 2026 at 21:23:42 • 4.4 MiB •
// wav"); AUM records WAV.
func recordingFile(folder string, e ipad.AUMFileEntry) string {
	name := e.Name
	ext := "wav"
	if parts := strings.Split(e.Detail, "•"); len(parts) > 1 {
		if last := strings.ToLower(strings.TrimSpace(parts[len(parts)-1])); last != "" && !strings.ContainsAny(last, " ./") {
			ext = last
		}
	}
	if !strings.HasSuffix(strings.ToLower(name), "."+ext) {
		name += "." + ext
	}
	return folder + "/" + name
}

// --- the job ------------------------------------------------------------------

// stemsReport is what record_song_stems found.
type stemsReport struct {
	Song   string          `json:"song"`
	Title  string          `json:"title"`
	Tempo  float64         `json:"tempo"`
	Layout song.StemLayout `json:"layout"`
	// ExpectedBeats is the lead-in, the song and the tail in beats: what
	// every take is long at the least.
	ExpectedBeats float64    `json:"expected_beats"`
	Stems         []stemTake `json:"stems"`
	Findings      []string   `json:"findings,omitempty"`
	Seconds       float64    `json:"seconds,omitempty"`
	// Ready says every stem of the song has a take of the song as it is now.
	Ready bool   `json:"ready"`
	Next  string `json:"next,omitempty"`
}

func (r *stemsReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the stems of %q (%s) at %g BPM: a lead-in of %d bar(s), the song, a tail of %d bar(s): %s beats each at the least\n",
		r.Title, r.Song, r.Tempo, r.Layout.LeadBars, r.Layout.TailBars, trimFloat(r.ExpectedBeats))
	for _, t := range r.Stems {
		fmt.Fprintf(&b, "  %-10s %s\n", t.Stem, strings.Join(t.Tracks, ", "))
		switch {
		case t.Error != "":
			fmt.Fprintf(&b, "             NOT RECORDED: %s\n", t.Error)
			if t.File != "" {
				fmt.Fprintf(&b, "             its take from before is kept: %s\n", t.File)
			}
			for _, n := range t.Notes {
				fmt.Fprintf(&b, "             note: %s\n", n)
			}
			continue
		case t.File == "":
			b.WriteString("             not recorded\n")
			continue
		}
		fmt.Fprintf(&b, "             %s", t.File)
		if t.Kept {
			b.WriteString(" (recorded before; the stem has not changed)")
		}
		b.WriteByte('\n')
		switch {
		case t.Whole == nil:
			fmt.Fprintf(&b, "             AUM lists it as %q; its length is read from its File Player when the mix is loaded (mix_song_stems)\n", t.Listed)
		case *t.Whole:
			fmt.Fprintf(&b, "             %d frames, %s, %s beats: whole beats long\n", t.Frames, clockMillis(t.Seconds), trimFloat(t.Beats))
		default:
			fmt.Fprintf(&b, "             %d frames, %s, %s beats: NOT whole beats long\n", t.Frames, clockMillis(t.Seconds), trimFloat(t.Beats))
		}
		if t.PeakDB != nil && t.RMSDB != nil {
			fmt.Fprintf(&b, "             at the tap while it was recorded: rms %.1f, peak %.1f dBFS", *t.RMSDB, *t.PeakDB)
			if t.Stalls > 0 {
				fmt.Fprintf(&b, " (the tap's stream stalled %d time(s): that reads low, the file is not affected)", t.Stalls)
			}
			b.WriteByte('\n')
		}
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "             note: %s\n", n)
		}
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	if r.Seconds > 0 {
		fmt.Fprintf(&b, "took %s\n", clock(time.Duration(r.Seconds)*time.Second))
	}
	if r.Next != "" {
		fmt.Fprintf(&b, "next: %s\n", r.Next)
	}
	return b.String()
}

// clockMillis renders seconds as m:ss.mmm.
func clockMillis(seconds float64) string {
	ms := int64(math.Round(seconds * 1000))
	return fmt.Sprintf("%d:%02d.%03d", ms/60000, ms/1000%60, ms%1000)
}

// trimFloat renders a number with up to three decimals.
func trimFloat(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

// stemsJob is one running or finished stem recording.
type stemsJob struct {
	mu      sync.Mutex
	song    string
	phase   string
	started time.Time
	report  *stemsReport
	err     string
	done    chan struct{}
	cancel  context.CancelFunc
}

func (j *stemsJob) setPhase(format string, a ...any) {
	j.mu.Lock()
	j.phase = fmt.Sprintf(format, a...)
	j.mu.Unlock()
}

func (j *stemsJob) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// stemsRunning names the song whose stems are being recorded, "" when none.
func (s *Server) stemsRunning() string {
	s.stemsJobMu.Lock()
	defer s.stemsJobMu.Unlock()
	if s.stems != nil && !s.stems.finished() {
		return s.stems.song
	}
	return ""
}

// stemsBusy is the answer of a tool that would load a session or play while
// a song's stems are being recorded, nil when none are: the recording loads
// its own sessions and plays its own passes, and either would end a take.
func (s *Server) stemsBusy(tool string) *mcp.CallToolResult {
	if running := s.stemsRunning(); running != "" {
		return textResult(fmt.Sprintf("%s: the stems of %q are being recorded, a session and a pass at a time (get_song_stems follows it); stop_song ends that recording", tool, running), true)
	}
	return nil
}

func (s *Server) registerSongStemTools(songArg string) {
	s.mcp.AddTool(&mcp.Tool{
		Name: "record_song_stems",
		Description: "Record a song a group of tracks at a time with AUM's own recorder, for a song the iPad cannot compute in one pass (AUM's load near 100 %, dropped audio), and for one of more than 12 tracks, which no single session hosts and which plays in no other way. " +
			"A stem is the tracks that share a group (group: on a track; a track without one is a stem of its own), or the grouping given as groups. For each stem the daemon authors and loads a session of its own that hosts only those tracks, with their effects, faders, scenes and ramps and nothing on the master, arms the master strip, and has AUM record while the session's sequencer plays the stem from the transport's top; then it finds the take in AUM's Recordings. " +
			"AUM's recorder writes on the iPad: no wireless link, a file that begins with the transport's first sample and ends on a beat, in the bit depth of AUM's settings. So the takes line up on the sample. Every stem's session plays the same lead-in and the same tail of bars after the last one. " +
			"The master's effects are left to the mix, all of them. A stem that is already recorded from the song as it is now is kept (again: true records it again). Each load replaces the session open in AUM, so it needs confirm: true. " +
			"Returns at once; get_song_stems gives the progress and the report: each file's name in AUM's Recordings, its length and whether that is whole beats. stop_song ends it. Then mix_song_stems.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"groups": {"type": "object", "additionalProperties": {"type": "array", "items": {"type": "string"}}, "description": "The grouping, in place of the document's: stem name to track ids. A track left out is a stem of its own. At most 12 tracks to a stem and 12 stems."},
				"stems": {"type": "array", "items": {"type": "string"}, "description": "Record only these stems (default: every stem that has no take of the song as it is now)."},
				"again": {"type": "boolean", "description": "Record a stem again although its take is of the song as it is now."},
				"tail_bars": {"type": "integer", "minimum": 1, "maximum": 32, "description": "How many bars every stem runs on after the song's last, for what rings out (default: the bars that cover four seconds). The mix has the same tail."},
				"confirm": {"type": "boolean", "description": "Required: every stem's session replaces the one open in AUM, and unsaved changes there are lost."}
			},
			"required": ["song"]
		}`),
	}, s.handleRecordSongStems)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_song_stems",
		Description: "The stem recording record_song_stems started: how far it is, or its report once it is done (wait_ms waits for it, up to 55 s per call). With song and no recording running: the stems kept for that song, each with its file in AUM's Recordings, its length, and whether it is of the song as it is now.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"song": {"type": "string", "description": "Song id: report the stems kept for it."},
				"wait_ms": {"type": "integer", "minimum": 0, "maximum": 55000, "description": "Wait this long for the running recording to finish before answering."}
			}
		}`),
	}, s.handleGetSongStems)

	s.mcp.AddTool(&mcp.Tool{
		Name: "mix_song_stems",
		Description: "Mix a song from the recordings of its stems (record_song_stems): write the mix as a song of its own, load it and play it. " +
			"The mix has one track per stem, each a File Player with the stem's file through the stem's strip (the song's stems: a level and effects; at unity and dry for a stem without one), and the whole song's master effects with their scenes and ramps, for the same bars, lead-in and tail. It is stored as <song>_mix (mix names another id) and is a song like any other: load_song, play_song, mix_song and check_song take it. Writing it again replaces it, so a stem's level and effects in the mix are changed in the song's stems, which needs no new take. " +
			"Then the call loads the mix (load_song: it replaces the session open in AUM, so confirm: true) and plays it from the session's sequencer, recording it at the master's tap unless record is false (play_song record: true; upload as there). " +
			"It refuses when a stem has no take, or a take of the song as it was before a change (stale: true mixes those all the same).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"mix": {"type": "string", "description": "Song id to store the mix under (default \"<song>_mix\")."},
				"confirm": {"type": "boolean", "description": "Required: the mix's session replaces the one open in AUM."},
				"play": {"type": "boolean", "description": "Play the mix once it is loaded (default true). false writes and loads it only."},
				"record": {"type": "boolean", "description": "Record the mix at the master's tap while it plays (default true; see play_song record)."},
				"upload": {"type": "boolean", "description": "As play_song upload: true uploads the recording to Google Drive, false keeps it on the host."},
				"stale": {"type": "boolean", "description": "Mix also from takes of the song as it was before it changed."}
			},
			"required": ["song"]
		}`),
	}, s.handleMixSongStems)
}

// stemsArgs is what a stem recording was asked for.
type stemsArgs struct {
	Only  []string
	Again bool
}

func (s *Server) handleRecordSongStems(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song     string              `json:"song"`
		Groups   map[string][]string `json:"groups"`
		Stems    []string            `json:"stems"`
		Again    bool                `json:"again"`
		TailBars int                 `json:"tail_bars"`
		Confirm  bool                `json:"confirm"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Song) == "" {
		return textResult("/song: name the song (see list_songs)", true), nil
	}
	plan, err := s.planStems(args.Song, args.Groups, args.TailBars)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	for _, name := range args.Stems {
		if !slices.ContainsFunc(plan.stems, func(st song.Stem) bool { return st.Name == name }) {
			names := make([]string, len(plan.stems))
			for i, st := range plan.stems {
				names[i] = st.Name
			}
			return textResult(fmt.Sprintf("/stems: the song has no stem %q (%s)", name, strings.Join(names, ", ")), true), nil
		}
	}
	if probe, id := sequencerProbe(songCatalog()); probe == nil {
		return textResult(fmt.Sprintf("record_song_stems: the stems are played by the session's sequencer, locked to AUM's transport, and the sequencer Atom | Piano Roll² (%s) is not among the staged AUv3 probes; install it and open the auv3-probe app on the iPad so it is scanned", id), true), nil
	}
	deck := s.aumDeck()
	if deck == nil {
		return textResult("record_song_stems: the iPad UI tools are not configured (ipad_wda_url): every stem's session is loaded in AUM and its take read from AUM's list of recordings", true), nil
	}
	if !args.Confirm {
		return textResult("record_song_stems needs confirm: true: every stem's session replaces the one open in AUM, and unsaved changes there are lost", true), nil
	}
	if s.midi == nil {
		return textResult("record_song_stems: "+midicontrol.ErrNoBrain.Error(), true), nil
	}
	if st := s.player.Status(); st.Playing {
		return textResult(fmt.Sprintf("record_song_stems: %q is playing; stop_song first", st.Song), true), nil
	}
	s.mixMu.Lock()
	measuring := s.mix != nil && !s.mix.finished()
	s.mixMu.Unlock()
	if measuring {
		return textResult("record_song_stems: a mix measurement is running (get_song_mix); stop_song ends it", true), nil
	}

	s.stemsJobMu.Lock()
	defer s.stemsJobMu.Unlock()
	if s.stems != nil && !s.stems.finished() {
		return textResult(fmt.Sprintf("record_song_stems: the stems of %q are being recorded (%s); get_song_stems follows it, stop_song ends it", s.stems.song, s.stems.phase), true), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &stemsJob{song: plan.id, phase: "starting", started: time.Now(), done: make(chan struct{}), cancel: cancel}
	s.stems = job
	todo := s.stemsTodo(plan, stemsArgs{Only: args.Stems, Again: args.Again})
	go func() {
		defer close(job.done)
		defer cancel()
		rep, err := s.runStems(ctx, job, plan, deck, todo)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.report = rep
		switch {
		case errors.Is(err, context.Canceled):
			job.err = "stopped"
		case err != nil:
			job.err = err.Error()
		}
		job.phase = "done"
	}()

	pass := plan.songs[0].timeline.Sequence().Duration()
	est := time.Duration(len(todo)) * (stemLoadEstimate + pass + 15*time.Second)
	var names []string
	for _, i := range todo {
		names = append(names, plan.stems[i].Name)
	}
	text := fmt.Sprintf("recording %d of the %d stem(s) of %q with AUM's recorder: %s; about %s (%s a pass, and a session to load for each)",
		len(todo), len(plan.stems), plan.src.Title, strings.Join(names, ", "), clock(est), clock(pass))
	if len(todo) == 0 {
		text = fmt.Sprintf("every one of the %d stem(s) of %q has a take of the song as it is now (again: true records them again)", len(plan.stems), plan.src.Title)
	}
	text += "\nget_song_stems gives the progress and the report (wait_ms waits for it); stop_song ends it"
	return structResult(text, map[string]any{"song": plan.id, "stems": plan.stems, "recording": names, "layout": plan.layout, "estimate_seconds": est.Seconds()}), nil
}

// stemsTodo lists the stems a run records, by their place in the plan: the
// ones asked for, or all of them, less those whose kept take is of the stem
// as it is now.
func (s *Server) stemsTodo(plan *stemPlan, a stemsArgs) []int {
	kept := loadSongStems()[plan.id]
	var out []int
	for i, st := range plan.stems {
		if len(a.Only) > 0 && !slices.Contains(a.Only, st.Name) {
			continue
		}
		if t := kept.take(st); !a.Again && t != nil && kept.Layout == plan.layout && t.File != "" && t.Fingerprint == stemFingerprint(plan.songs[i]) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// stemsReportOf is the report of a plan against the takes kept for it.
func stemsReportOf(plan *stemPlan, kept songStems) *stemsReport {
	rep := &stemsReport{Song: plan.id, Title: plan.src.Title, Tempo: plan.src.Tempo, Layout: plan.layout, ExpectedBeats: plan.expectedBeats(), Ready: true}
	for i, st := range plan.stems {
		take := stemTake{Stem: st.Name, Tracks: st.Tracks}
		if t := kept.take(st); t != nil && kept.Layout == plan.layout {
			take = *t
			take.Kept = true
			if take.File != "" && take.Fingerprint != stemFingerprint(plan.songs[i]) {
				take.Notes = append(slices.Clone(take.Notes), "the stem has changed since this take: its notes, its sounds or its controls (record_song_stems records it again)")
				rep.Ready = false
			}
		}
		if take.File == "" {
			rep.Ready = false
		}
		rep.Stems = append(rep.Stems, take)
	}
	return rep
}

// runStems records the stems todo of a plan, one after another.
func (s *Server) runStems(ctx context.Context, job *stemsJob, plan *stemPlan, deck stemDeck, todo []int) (*stemsReport, error) {
	// A pass is minutes long and a session is loaded between two: the host
	// must not doze through either.
	defer s.holdAwake()()
	began := time.Now()
	kept := loadSongStems()[plan.id]
	if kept.Layout != plan.layout {
		// Takes with another lead-in or tail do not line up with these.
		kept = songStems{}
	}
	rec := songStems{Song: plan.id, Layout: plan.layout}
	for _, st := range plan.stems {
		take := stemTake{Stem: st.Name, Tracks: st.Tracks}
		if t := kept.take(st); t != nil {
			take = *t
		}
		rec.Stems = append(rec.Stems, take)
	}
	report := func() *stemsReport {
		rep := stemsReportOf(plan, rec)
		for i := range rep.Stems {
			// Kept is what this run left alone.
			rep.Stems[i].Kept = rep.Stems[i].File != "" && !slices.Contains(todo, i)
		}
		rep.Seconds = math.Round(time.Since(began).Seconds())
		if rep.Ready {
			rep.Next = fmt.Sprintf("mix_song_stems {song: %q, confirm: true} writes the mix from these files, loads it and plays it", plan.id)
		} else {
			rep.Next = "record_song_stems again records what is missing; the stems that have a take are kept"
		}
		return rep
	}

	for n, i := range todo {
		st, cs := plan.stems[i], plan.songs[i]
		job.setPhase("stem %s (%d of %d): loading its session", st.Name, n+1, len(todo))
		take, err := s.recordStem(ctx, job, cs, st, deck, fmt.Sprintf("stem %s (%d of %d)", st.Name, n+1, len(todo)))
		if take.File != "" {
			rec.Stems[i] = take
			if serr := s.saveSongStems(rec); serr != nil {
				rec.Stems[i].Notes = append(rec.Stems[i].Notes, "the take could not be noted down: "+serr.Error())
			}
		}
		if err != nil {
			// A stem that was not recorded keeps the take it had, if any.
			rep := report()
			rep.Stems[i].Error, rep.Stems[i].Kept = take.Error, false
			rep.Stems[i].Notes = append(rep.Stems[i].Notes, take.Notes...)
			rep.Ready = false
			rep.Next = "record_song_stems again records what is missing; the stems that have a take are kept"
			if !errors.Is(err, context.Canceled) {
				rep.Findings = append(rep.Findings, fmt.Sprintf("the recording ended at the stem %s: %v", st.Name, err))
			}
			return rep, err
		}
	}
	return report(), nil
}

// recordStem loads a stem's session and records one pass of it with AUM's
// recorder. The take it returns says what was recorded, or why nothing was;
// the error ends the whole recording.
func (s *Server) recordStem(ctx context.Context, job *stemsJob, cs *compiledSong, st song.Stem, deck stemDeck, label string) (stemTake, error) {
	take := stemTake{Stem: st.Name, Tracks: st.Tracks}
	fail := func(format string, a ...any) (stemTake, error) {
		take.Error = fmt.Sprintf(format, a...)
		return take, errors.New(take.Error)
	}
	seqOn := true
	noTaps := false
	load, err := s.runLoadSong(ctx, cs, loadSongArgs{Author: songAuthorArgs{Sequencer: &seqOn, TrackTaps: &noTaps}, Check: true})
	if err != nil {
		return fail("%v", err)
	}
	take.Session = load.Session
	switch {
	case ctx.Err() != nil:
		return take, ctx.Err()
	case load.Check == nil:
		// A step before the check failed: there is no session to record.
		return fail("its session did not load:\n%s", strings.TrimRight(load.text(""), "\n"))
	case !load.OK:
		// The session is loaded and connected; what the check found is said,
		// and the stem is recorded: a check can call a track silent that plays.
		for _, f := range load.Check.Findings {
			if !f.OK {
				take.Notes = append(take.Notes, fmt.Sprintf("the check after the load: %s: %s", f.Step, f.Detail))
			}
		}
	}
	sessions := loadSongSessions()
	rec := sessions[cs.id]
	if !rec.sequenceCurrent(cs.timeline) {
		return fail("the session %s does not hold the stem in its sequencer", rec.Session)
	}
	folder := path.Base(rec.Session)

	cc := func(at song.Assignment, value int) song.Event {
		return song.Event{Kind: song.Control, Channel: at.Channel, Data1: at.Number, Data2: value}
	}
	send := func(sctx context.Context, e song.Event) error { return s.sendBrain(sctx, songCommand(e)) }
	p := cs.plan
	record := p.Transport["Toggle Record"]
	// recorderOff switches AUM's recorder off, with the transport stopped:
	// the take then ends at once. It does not use the pass's context, which
	// may be over.
	recorderOff := func() {
		octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = send(octx, cc(record, 0))
		_ = sleepContext(octx, stemArmSettle)
	}

	// A recorder left on from before would make the start no start, and its
	// take would be found as this pass's: it is switched off before AUM's
	// list is read. The transport stands (the load left it stopped).
	recorderOff()
	job.setPhase("%s: reading AUM's recordings of %q", label, folder)
	before, err := deck.Recordings(ctx, folder)
	if err != nil {
		if ctx.Err() != nil {
			return take, ctx.Err()
		}
		why, _ := s.ipadFailure(ctx, err)
		return fail("AUM's list of recordings could not be read before the take: %s", why)
	}

	// Arm the master: by its mapping, and then by AUM's screen, which says
	// whether the mapping armed it and arms it when not.
	job.setPhase("%s: arming the master", label)
	if err := send(ctx, cc(p.MasterRec, 127)); err != nil {
		return fail("the master could not be armed: %v", err)
	}
	if err := sleepContext(ctx, stemArmSettle); err != nil {
		return take, err
	}
	switch tapped, err := deck.Arm(ctx, true); {
	case ctx.Err() != nil:
		return take, ctx.Err()
	case err != nil:
		// A mixer of many strips keeps the master off screen and out of
		// AUM's tree. The mapping arms it all the same; a take that does not
		// appear says when it did not.
		why, _ := s.ipadFailure(ctx, err)
		take.Notes = append(take.Notes, fmt.Sprintf("the master's REC button could not be read after its mapped control armed it (%s)", why))
	case tapped:
		take.Notes = append(take.Notes, "the master was armed by a tap on its REC button: its mapped control had not armed it")
	}
	// However the pass ends, the master is not left armed.
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = send(dctx, cc(p.MasterRec, 0))
		_ = sleepContext(dctx, stemArmSettle)
		_, _ = deck.Arm(dctx, false)
	}()

	// The pass: the recorder starts the transport and the sequencer plays the
	// stem. Half a beat before the stem's last bar line the sequence itself
	// switches the recorder off, which ends the take on that bar line; the
	// transport stops a bar later. The daemon sends no off of its own
	// meanwhile: a second one would withdraw the first.
	tap, _ := s.songTap(rec.Session)
	var meter *tapMeter
	mctx, stopMeter := context.WithCancel(context.Background())
	var metering sync.WaitGroup
	if tap != nil {
		meter = newTapMeter(tap)
		metering.Add(1)
		go func() { defer metering.Done(); meter.run(mctx) }()
	}
	stopMetering := sync.OnceFunc(func() { stopMeter(); metering.Wait() })
	defer stopMetering()

	seq := cs.timeline.Sequence()
	bar := cs.timeline.TickTime(cs.timeline.BarTicks)
	ended := make(chan song.Status, 1)
	// The recorder latches by value: 127 is on, and starts the transport.
	job.setPhase("%s: recording, %s", label, clock(seq.Duration()))
	_, err = s.player.StartSequenced(cs.id, cs.timeline, seq, song.SequencedOptions{
		Rewind: []song.Event{cc(p.SequencerBypass, 0), cc(p.Transport["Rewind"], 127)},
		Start:  []song.Event{cc(record, 127)},
		Tail:   bar + stemEndMargin,
		Stop:   []song.Event{cc(p.Transport["Stop/Rewind"], 127), cc(p.Transport["Rewind"], 127)},
		OnEnd:  func(st song.Status) { ended <- st },
	}, send)
	if err != nil {
		return fail("the pass could not start: %v", err)
	}
	var final song.Status
	select {
	case final = <-ended:
	case <-ctx.Done():
		// stop_song stopped the player already; wait for its last messages.
		s.player.Stop()
		final = <-ended
	}
	if final.Outcome != "finished" {
		// The pass did not reach the sequence's own off: the transport is
		// stopped now, and the recorder must not record on.
		recorderOff()
	}
	stopMetering()
	if meter != nil {
		if rms, peak, ok := meter.level(0, meter.position()); ok {
			take.RMSDB, take.PeakDB, take.Stalls = &rms, &peak, meter.stalls
		}
	}
	if ctx.Err() != nil {
		if final.Outcome == "finished" {
			// Stopped after the pass, before its take was found: a take the
			// sequence did not end would record on through the stopped
			// transport. The off changes nothing when the sequence ended it.
			recorderOff()
		}
		return take, ctx.Err()
	}
	if final.Outcome != "finished" {
		return fail("the pass ended early: %s", final.Outcome)
	}

	// The take is the file AUM's list has now and did not have before.
	job.setPhase("%s: finding the take in AUM's recordings", label)
	var entry ipad.AUMFileEntry
	found, nudged := false, false
	var lerr error
	for looking := time.Now(); ; {
		var after []ipad.AUMFileEntry
		if after, lerr = deck.Recordings(ctx, folder); lerr == nil {
			if entry, found = newRecording(before, after); found {
				break
			}
		}
		if time.Since(looking) > stemListWait {
			break
		}
		if !nudged && time.Since(looking) > stemOffAfter {
			// No take: the sequence did not end it, and the recorder may be
			// recording on through the stopped transport. Switched off now it
			// ends at once, later than the stem: such a take is not whole
			// beats long, which the mix's load says.
			nudged = true
			recorderOff()
			continue
		}
		if err := sleepContext(ctx, stemListPoll); err != nil {
			if !nudged {
				// Stopped while the take was looked for: as above, the
				// recorder must not record on.
				recorderOff()
			}
			return take, err
		}
	}
	if found && nudged {
		take.Notes = append(take.Notes, "AUM listed no take after the pass until the daemon switched its recorder off: the stem's sequence had not ended it, so the take ran on after the stem and is not whole beats long")
	}
	switch {
	case lerr != nil && !found:
		why, _ := s.ipadFailure(ctx, lerr)
		return fail("AUM's list of recordings could not be read after the take: %s", why)
	case !found:
		return fail("AUM lists no new recording under %q: its recorder did not run (was the master armed, and did the transport start?)", folder)
	}
	take.File, take.Listed = recordingFile(folder, entry), entry.Detail
	take.Fingerprint = stemFingerprint(cs)
	take.RecordedAt = time.Now().Format(time.RFC3339)
	if take.PeakDB != nil && *take.PeakDB <= silentDB {
		take.Notes = append(take.Notes, "the master's tap heard silence through the whole pass: the stem's instruments did not sound (check_song on a song of these tracks tells which)")
	}
	return take, nil
}

func (s *Server) handleGetSongStems(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song   string `json:"song"`
		WaitMS int    `json:"wait_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	s.stemsJobMu.Lock()
	job := s.stems
	s.stemsJobMu.Unlock()
	// A recording that runs is what there is to report, whatever song was
	// named; so is the last one's report when none was. With a song and no
	// recording running, the takes kept for it are: they also say what a mix
	// read of their lengths since, and whether the song has changed.
	if job != nil && (args.Song == "" || !job.finished()) {
		if wait := min(time.Duration(args.WaitMS)*time.Millisecond, stemsWaitMost); wait > 0 {
			select {
			case <-job.done:
			case <-time.After(wait):
			case <-ctx.Done():
			}
		}
		job.mu.Lock()
		defer job.mu.Unlock()
		if !job.finished() {
			return structResult(fmt.Sprintf("recording the stems of %q: %s (%s so far)", job.song, job.phase, clock(time.Since(job.started))),
				map[string]any{"song": job.song, "done": false, "phase": job.phase}), nil
		}
		if job.report == nil {
			return textResult(fmt.Sprintf("the stem recording of %q failed: %s", job.song, job.err), true), nil
		}
		text := job.report.text()
		if job.err != "" {
			text += "the recording ended early: " + job.err + "\n"
		}
		res := structResult(text, map[string]any{"song": job.song, "done": true, "report": job.report, "error": job.err})
		res.IsError = job.err != "" && job.err != "stopped"
		return res, nil
	}
	if strings.TrimSpace(args.Song) == "" {
		return textResult("no stems have been recorded since the daemon started (record_song_stems starts a recording; song: names a song whose kept stems to report)", false), nil
	}
	kept, ok := loadSongStems()[song.ID(args.Song)]
	if !ok {
		return textResult(fmt.Sprintf("no stems are kept for %q (record_song_stems records them)", song.ID(args.Song)), false), nil
	}
	plan, err := s.planStems(args.Song, kept.groups(), kept.Layout.TailBars)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	rep := stemsReportOf(plan, kept)
	if rep.Ready {
		rep.Next = fmt.Sprintf("mix_song_stems {song: %q, confirm: true}", plan.id)
	} else {
		rep.Next = "record_song_stems records what is missing or has changed"
	}
	out := map[string]any{"song": plan.id, "done": true, "report": rep}
	if left := plan.src.StripsLeftOut(plan.stems); len(left) > 0 {
		out["strips_left_out"] = left
	}
	return structResult(rep.text()+stripsLeftOutText(plan.src, plan.stems), out), nil
}

// --- the mix ------------------------------------------------------------------

func (s *Server) handleMixSongStems(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song    string `json:"song"`
		Mix     string `json:"mix"`
		Confirm bool   `json:"confirm"`
		Play    *bool  `json:"play"`
		Record  *bool  `json:"record"`
		Upload  *bool  `json:"upload"`
		Stale   bool   `json:"stale"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Song) == "" {
		return textResult("/song: name the song (see list_songs)", true), nil
	}
	id := song.ID(args.Song)
	if running := s.stemsRunning(); running != "" {
		return textResult(fmt.Sprintf("mix_song_stems: the stems of %q are being recorded (get_song_stems); stop_song ends it", running), true), nil
	}
	kept, ok := loadSongStems()[id]
	if !ok {
		return textResult(fmt.Sprintf("mix_song_stems: no stems are kept for %q; record_song_stems records them", id), true), nil
	}
	plan, err := s.planStems(args.Song, kept.groups(), kept.Layout.TailBars)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	rep := stemsReportOf(plan, kept)
	files := map[string]string{}
	var missing, stale []string
	for i, t := range rep.Stems {
		switch {
		case t.File == "":
			missing = append(missing, t.Stem)
		case t.Fingerprint != stemFingerprint(plan.songs[i]):
			stale = append(stale, t.Stem)
		}
		files[t.Stem] = t.File
	}
	switch {
	case len(missing) > 0:
		return textResult(fmt.Sprintf("mix_song_stems: no take is kept for the stem(s) %s of %q; record_song_stems records them\n%s", strings.Join(missing, ", "), id, rep.text()), true), nil
	case len(stale) > 0 && !args.Stale:
		return textResult(fmt.Sprintf("mix_song_stems: the stem(s) %s of %q have changed since their takes; record_song_stems records them again, or stale: true mixes the takes as they are\n%s", strings.Join(stale, ", "), id, rep.text()), true), nil
	}

	mixID := song.ID(args.Mix)
	if mixID == "" {
		mixID = id + "_mix"
	}
	if mixID == id {
		return textResult("/mix: the mix needs an id of its own, not the song's", true), nil
	}
	if old, _, err := s.songs.Load(mixID); err == nil && old != nil && !old.HasFiles() {
		return textResult(fmt.Sprintf("/mix: a song %q is stored already and is no mix of files; name another id with mix", mixID), true), nil
	}
	mix, err := plan.src.MixSong(plan.stems, files, sessionFileName(plan.src.Title+" mix"), plan.layout)
	if err != nil {
		return textResult("mix_song_stems: "+err.Error(), true), nil
	}
	body, err := mix.Marshal()
	if err != nil {
		return textResult("mix_song_stems: "+err.Error(), true), nil
	}
	head := fmt.Sprintf("# The mix of %q (%s) from the recordings of its stems, written by mix_song_stems on %s.\n"+
		"# A track's level and effects are that stem's strip, from the song's stems; change them there: mix_song_stems writes this document anew.\n",
		plan.src.Title, id, time.Now().Format("2006-01-02 15:04"))
	if _, err := s.songs.Save(mixID, append([]byte(head), body...)); err != nil {
		return textResult("mix_song_stems: the mix could not be stored: "+err.Error(), true), nil
	}
	cs, err := s.compileSong(mixID, mix)
	if err != nil {
		return textResult(fmt.Sprintf("mix_song_stems: the mix %q is stored but does not compile against the staged plugins: %v", mixID, err), true), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "wrote the mix of %q as the song %q: %d file track(s)", plan.src.Title, mixID, len(mix.Tracks))
	if len(mix.Master) > 0 {
		fmt.Fprintf(&b, " under %d master effect(s)", len(mix.Master))
	}
	b.WriteByte('\n')
	for i, t := range rep.Stems {
		fmt.Fprintf(&b, "  %-10s %s%s\n", t.Stem, t.File, stemStripText(plan.src, plan.stems[i], cs.catalog))
	}
	b.WriteString(stripsLeftOutText(plan.src, plan.stems))
	if len(stale) > 0 {
		fmt.Fprintf(&b, "note: the stem(s) %s have changed since their takes; the mix plays the takes as they are\n", strings.Join(stale, ", "))
	}
	out := map[string]any{"song": id, "mix": mixID, "files": files, "stale": stale}
	if left := plan.src.StripsLeftOut(plan.stems); len(left) > 0 {
		out["strips_left_out"] = left
	}

	if s.ipad != nil && !args.Confirm {
		b.WriteString("not loaded: loading replaces the session open in AUM and needs confirm: true (or load_song the mix)\n")
		return structResult(b.String(), out), nil
	}
	seqOn := true
	load, err := s.runLoadSong(ctx, cs, loadSongArgs{Author: songAuthorArgs{Sequencer: &seqOn}, Check: true})
	if err != nil {
		return textResult(b.String()+err.Error(), true), nil
	}
	out["load"] = load
	title := fmt.Sprintf("%q is loaded in AUM as %s", mix.Title, load.Session)
	if !load.OK {
		title = fmt.Sprintf("%q is NOT ready to play", mix.Title)
	}
	b.WriteString(load.text(title))
	if !load.OK {
		res := structResult(b.String(), out)
		res.IsError = true
		return res, nil
	}
	// The File Players of the loaded mix say how long each take is: that is
	// noted down with the takes, and decides whether the mix is played.
	trouble := s.noteStemLengths(&kept, load.Check, plan)
	out["stems"] = kept.Stems
	if len(trouble) > 0 {
		for _, t := range trouble {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		fmt.Fprintf(&b, "not played: the takes do not line up as they are. record_song_stems {again: true, stems: [...]} records a stem again; play_song {song: %q} plays the mix all the same\n", mixID)
		res := structResult(b.String(), out)
		res.IsError = true
		return res, nil
	}
	if args.Play != nil && !*args.Play {
		fmt.Fprintf(&b, "next: play_song {song: %q, record: true}\n", mixID)
		return structResult(b.String(), out), nil
	}

	play := map[string]any{"song": mixID, "record": args.Record == nil || *args.Record}
	if args.Upload != nil {
		play["upload"] = *args.Upload
	}
	raw, _ := json.Marshal(play)
	res, err := s.handlePlaySong(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: raw}})
	if err != nil {
		return nil, err
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && !strings.HasPrefix(tc.Text, structuredJSONLabel) {
			b.WriteString(tc.Text)
		}
	}
	out["play"] = res.StructuredContent
	final := structResult(b.String(), out)
	final.IsError = res.IsError
	return final, nil
}

// noteStemLengths takes the lengths the mix's File Players showed (a check of
// the loaded mix) into the kept takes, and says what of them keeps the stems
// from lining up: a take that is not whole beats long, one shorter than the
// lead-in, the song and the tail, or takes of different lengths that end
// before the song does. A take whose length was not shown is no trouble.
func (s *Server) noteStemLengths(kept *songStems, check *checkReport, plan *stemPlan) []string {
	if check == nil {
		return nil
	}
	var trouble []string
	want := plan.expectedBeats()
	changed := false
	for _, f := range check.Files {
		for i := range kept.Stems {
			t := &kept.Stems[i]
			if t.Stem != f.Track || !f.Loaded || f.Frames == 0 {
				continue
			}
			t.setLength(f.view, plan.src.Tempo)
			changed = true
			switch {
			case t.Whole != nil && !*t.Whole:
				trouble = append(trouble, fmt.Sprintf("the take of %s (%s) is %s beats long, not whole beats: it did not begin with the transport or did not end on a bar line", t.Stem, t.File, trimFloat(t.Beats)))
			case t.Beats < want-stemWholeBeat:
				trouble = append(trouble, fmt.Sprintf("the take of %s (%s) is %s beats long, shorter than the lead-in, the song and the tail (%s beats)", t.Stem, t.File, trimFloat(t.Beats), trimFloat(want)))
			}
		}
	}
	if changed {
		_ = s.saveSongStems(*kept)
	}
	return trouble
}
