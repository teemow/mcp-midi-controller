package mcpserver

// This file measures a song's mix (mix_song): it plays the song and reads the
// master tap out for its whole length, so the levels of every bar and every
// section are known, and on request gives the level of each track in every
// section. A session with a tap on every track has those read out in the same
// pass, each telling its track by the name it dialled in under; without them
// every section is played once per track and measured at the master.
//
// Every pass is played live, as play_song plays a part of a song, and runs
// AUM's transport with it (unless asked not to): an instrument that plays by
// itself while the transport runs — a drum machine's own sequencer, set by a
// song's parameters alone — is heard as it is when the song is played. The
// session's own sequencer is bypassed meanwhile, as play_song does.
//
// A tap keeps only its last seconds, so the stream is read out as it
// arrives and reduced on the way: nothing of the audio is kept but the mean
// square and the sample peak of every hundredth of a second.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

// Waits of a mix measurement; variables for the tests.
var (
	// mixTail is listened to after the last bar, for what rings on.
	mixTail = 1500 * time.Millisecond
	// mixBetween separates two solo passes, so one's tail is not the next
	// one's level.
	mixBetween = 700 * time.Millisecond
	// mixPull is how often the tap is read out.
	mixPull = 400 * time.Millisecond
	// mixPreroll is a pass's preroll; 0 takes the player's default.
	mixPreroll time.Duration
)

const (
	// meterBlockMS is the meter's resolution.
	meterBlockMS = 10
	// clipDB is the sample peak from which a bar counts as clipped.
	clipDB = -0.3
	// mixSoloBars is how many bars of a section a solo pass plays.
	mixSoloBars = 4
	// mixWaitMost bounds get_song_mix's wait.
	mixWaitMost = 55 * time.Second
)

func (s *Server) registerSongMixTools(songArg string) {
	s.mcp.AddTool(&mcp.Tool{
		Name: "mix_song",
		Description: "Measure a song's mix: play it (the whole song, or from a section for a number of sections) and read the master tap out for its whole length. The report is rms and sample peak per bar and per section, the loudest and the clipped bars, how much headroom is left, and where the tap's stream stalled. " +
			"per_track: true adds the level of every track in every section — the table a balance is made from — and names a track that is silent where it has notes; a track without notes in a section that sounds by itself there (an instrument playing its own sequencer with the transport) has its row too. A session with a tap on every track (session: {track_taps: true}) has them read out in the same pass: each track at its own tap, after its fader and before the master's effects, over the whole section. Without those taps each section is played once per track (the first bars of it, solo) and measured at the master; solo: true asks for that although the taps are there. " +
			"Every pass is played live and runs AUM's transport with it, the session's sequencer bypassed meanwhile (as play_song with transport: true), so instruments that follow the host clock sound as they do in the song; transport: false keeps the transport stopped, for less DSP load, when nothing in the session needs it. " +
			"A song with a track that plays a file is played from its session's sequencer and from the top instead (the file starts at the top of the transport and nowhere else): from is refused, and per_track needs a tap on every track. " +
			"Returns at once; get_song_mix gives the progress and the report (stop_song ends the measurement). The song's session must be loaded (load_song).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"from": {"type": "string", "description": "Where to start: a section id or a 1-based position in the arrangement (default: the top)."},
				"sections": {"type": "integer", "minimum": 1, "description": "How many arrangement entries to play (default: to the end)."},
				"per_track": {"type": "boolean", "description": "Also measure every track in every section: at its own tap in the same pass when the session has track taps, else alone in a pass of its own."},
				"solo": {"type": "boolean", "description": "With per_track: measure the tracks by solo passes at the master even when the session has track taps (the level then includes the master's effects)."},
				"solo_bars": {"type": "integer", "minimum": 1, "maximum": 32, "description": "How many bars of a section a solo pass plays (default 4)."},
				"whole": {"type": "boolean", "description": "Report the whole range's bars and sections at the master (default true). false with per_track measures only the tracks."},
				"transport": {"type": "boolean", "description": "Run AUM's transport through every pass, with the session's sequencer bypassed (default true): an instrument that plays by itself with the transport (a drum machine's own sequencer) is silent without it. false keeps the transport stopped, which spares the DSP load a running transport adds, and measures only the tracks that have notes."}
			},
			"required": ["song"]
		}`),
	}, s.handleMixSong)

	s.mcp.AddTool(&mcp.Tool{
		Name:        "get_song_mix",
		Description: "The mix measurement mix_song started: how far it is, or its report once it is done. wait_ms waits for it to finish (up to 55 s per call).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"wait_ms": {"type": "integer", "minimum": 0, "maximum": 55000, "description": "Wait this long for the measurement to finish before answering."}
			}
		}`),
	}, s.handleGetSongMix)
}

// --- the meter ------------------------------------------------------------------

// meterBlock is the level of one stretch of meterBlockMS.
type meterBlock struct {
	sumSq float64 // of the mono mix
	peak  float64 // over every channel
	n     int     // frames
}

// tapMeter reads a tap's stream out as it arrives and reduces it to blocks.
type tapMeter struct {
	tap *audiotap.Store

	mu     sync.Mutex
	first  int64 // the epoch of block 0
	next   int64 // the epoch read up to
	rate   float64
	ch     int
	per    int // frames per block
	cur    meterBlock
	blocks []meterBlock
	// stalls counts stretches the stream did not deliver in time, or that
	// had scrolled out of the tap's window before they were read.
	stalls int

	// zero is the position of the present playback's first note. Every tap
	// has a stream of its own, so each meter keeps its own; only the
	// measuring goroutine touches it.
	zero float64
}

// newTapMeter starts metering at the tap's present position.
func newTapMeter(tap *audiotap.Store) *tapMeter {
	e := tap.MarkEpoch()
	return &tapMeter{tap: tap, first: e, next: e}
}

// pull reads out what arrived since the last pull.
func (m *tapMeter) pull() {
	m.mu.Lock()
	defer m.mu.Unlock()
	end := m.tap.MarkEpoch()
	if end <= m.next {
		return
	}
	clip, ok := m.tap.Segment(m.next, end)
	if !ok {
		// The stretch scrolled out of the tap's window before it was read:
		// count it, and keep its place in time with blocks of nothing.
		m.stalls++
		if m.per > 0 {
			for lost := int(end-m.next) / m.ch / m.per; lost > 0; lost-- {
				m.blocks = append(m.blocks, meterBlock{})
			}
		}
		m.next = end
		return
	}
	if !clip.Contiguous {
		m.stalls++
	}
	if m.per == 0 {
		m.rate, m.ch = clip.SampleRate, max(clip.Channels, 1)
		m.per = max(int(m.rate*meterBlockMS/1000), 1)
	}
	ch := m.ch
	frames := len(clip.Samples) / ch
	for f := 0; f < frames; f++ {
		var mono float64
		for c := 0; c < ch; c++ {
			v := float64(clip.Samples[f*ch+c])
			if a := math.Abs(v); a > m.cur.peak {
				m.cur.peak = a
			}
			mono += v
		}
		mono /= float64(ch)
		m.cur.sumSq += mono * mono
		if m.cur.n++; m.cur.n == m.per {
			m.blocks = append(m.blocks, m.cur)
			m.cur = meterBlock{}
		}
	}
	m.next += int64(frames * ch)
}

// run pulls until ctx ends.
func (m *tapMeter) run(ctx context.Context) {
	t := time.NewTicker(mixPull)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.pull()
			return
		case <-t.C:
			m.pull()
		}
	}
}

// position is the stream's present position in seconds since the meter began.
func (m *tapMeter) position() float64 {
	m.mu.Lock()
	first, rate, ch := m.first, m.rate, m.ch
	m.mu.Unlock()
	if rate == 0 {
		// Nothing read yet: take the format from the tap.
		rate, ch = m.tap.SampleRate(), 2
		if c := m.tap.Clip(1); c.Channels > 0 {
			ch = c.Channels
		}
	}
	if rate == 0 {
		return 0
	}
	return float64(m.tap.MarkEpoch()-first) / float64(ch) / rate
}

// since is the stream's present position in seconds since the playback's
// first note.
func (m *tapMeter) since() float64 { return m.position() - m.zero }

// levelSince is level for a stretch counted from the playback's first note.
func (m *tapMeter) levelSince(from, to float64) (rmsDB, peakDB float64, ok bool) {
	return m.level(m.zero+from, m.zero+to)
}

// level is the rms and the sample peak, in dBFS, of the stretch from..to
// (seconds since the meter began). ok is false when nothing was metered
// there.
func (m *tapMeter) level(from, to float64) (rmsDB, peakDB float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := max(int(math.Round(from*1000/meterBlockMS)), 0)
	b := min(int(math.Round(to*1000/meterBlockMS)), len(m.blocks))
	var sum, peak float64
	n := 0
	for _, blk := range m.blocks[min(a, len(m.blocks)):max(b, min(a, len(m.blocks)))] {
		sum += blk.sumSq
		n += blk.n
		if blk.peak > peak {
			peak = blk.peak
		}
	}
	if n == 0 {
		return -120, -120, false
	}
	return round1(audiotap.AmpToDBFS(math.Sqrt(sum / float64(n)))), round1(audiotap.AmpToDBFS(peak)), true
}

// --- the report -----------------------------------------------------------------

type mixBar struct {
	Bar     int     `json:"bar"`
	Section string  `json:"section"`
	RMSDB   float64 `json:"rms_db"`
	PeakDB  float64 `json:"peak_db"`
	Clipped bool    `json:"clipped,omitempty"`
}

type mixSection struct {
	Position int     `json:"position"`
	Name     string  `json:"name"`
	Bar      int     `json:"bar"`
	Bars     int     `json:"bars"`
	RMSDB    float64 `json:"rms_db"`
	PeakDB   float64 `json:"peak_db"`
}

// mixSolo is one track in one section: alone in a pass of its own, or at its
// own tap while the song played.
type mixSolo struct {
	Section string  `json:"section"`
	Track   string  `json:"track"`
	Bars    int     `json:"bars"`
	Notes   int     `json:"notes"`
	RMSDB   float64 `json:"rms_db"`
	PeakDB  float64 `json:"peak_db"`
	Silent  bool    `json:"silent,omitempty"`
}

type mixReport struct {
	Song  string  `json:"song"`
	Title string  `json:"title"`
	Tempo float64 `json:"tempo"`
	// Whole is the played range through: its level, bars and sections.
	RMSDB    float64      `json:"rms_db,omitempty"`
	PeakDB   float64      `json:"peak_db,omitempty"`
	TailDB   float64      `json:"tail_peak_db,omitempty"`
	Sections []mixSection `json:"sections,omitempty"`
	Bars     []mixBar     `json:"bars,omitempty"`
	// Tracks are the tracks' levels per section, and TracksAt where they were
	// measured: tracksAtTaps or tracksAtMaster.
	Tracks   []mixSolo `json:"tracks,omitempty"`
	TracksAt string    `json:"tracks_at,omitempty"`
	// Stalls counts stretches a tap's stream did not deliver.
	Stalls int `json:"stalls,omitempty"`
	// Findings say what stands out.
	Findings []string `json:"findings,omitempty"`
	Seconds  float64  `json:"seconds"`
}

// Where the tracks of a mix were measured.
const (
	// tracksAtTaps: each at its own tap, while the song played.
	tracksAtTaps = "taps"
	// tracksAtMaster: at the master tap, each in a solo pass.
	tracksAtMaster = "master"
)

func (r *mixReport) text() string {
	var b strings.Builder
	where := "the master tap"
	if r.TracksAt == tracksAtTaps {
		where = "the master tap and every track's own"
		if len(r.Bars) == 0 {
			where = "every track's own tap"
		}
	}
	fmt.Fprintf(&b, "mix of %q (%s) at %g BPM, measured at %s in %.0f s\n", r.Title, r.Song, r.Tempo, where, r.Seconds)
	if len(r.Bars) > 0 {
		fmt.Fprintf(&b, "whole: rms %.1f dBFS, peak %.1f dBFS, %d bar(s)\n", r.RMSDB, r.PeakDB, len(r.Bars))
		b.WriteString("sections:\n")
		for _, s := range r.Sections {
			fmt.Fprintf(&b, "  %2d  %-12s bars %3d-%-3d  rms %6.1f  peak %6.1f\n", s.Position, s.Name, s.Bar, s.Bar+s.Bars-1, s.RMSDB, s.PeakDB)
		}
		b.WriteString("bars (rms / peak dBFS):\n")
		for _, bar := range r.Bars {
			mark := ""
			if bar.Clipped {
				mark = "  CLIPPED"
			}
			fmt.Fprintf(&b, "  %3d %-12s %6.1f / %6.1f%s\n", bar.Bar, bar.Section, bar.RMSDB, bar.PeakDB, mark)
		}
	}
	if len(r.Tracks) > 0 {
		if r.TracksAt == tracksAtTaps {
			b.WriteString("tracks at their own taps, in the same pass (rms / peak dBFS):\n")
		} else {
			b.WriteString("tracks alone (rms / peak dBFS):\n")
		}
		// One row per track, one column per section, in the order measured.
		var sections, tracks []string
		cell := map[[2]string]mixSolo{}
		seenS, seenT := map[string]bool{}, map[string]bool{}
		for _, t := range r.Tracks {
			if !seenS[t.Section] {
				seenS[t.Section] = true
				sections = append(sections, t.Section)
			}
			if !seenT[t.Track] {
				seenT[t.Track] = true
				tracks = append(tracks, t.Track)
			}
			cell[[2]string{t.Track, t.Section}] = t
		}
		fmt.Fprintf(&b, "  %-10s", "")
		for _, sec := range sections {
			fmt.Fprintf(&b, " %-15s", sec)
		}
		b.WriteByte('\n')
		for _, tr := range tracks {
			fmt.Fprintf(&b, "  %-10s", tr)
			for _, sec := range sections {
				c, ok := cell[[2]string{tr, sec}]
				switch {
				case !ok:
					fmt.Fprintf(&b, " %-15s", "-")
				case c.Silent:
					fmt.Fprintf(&b, " %-15s", "SILENT")
				default:
					fmt.Fprintf(&b, " %-15s", fmt.Sprintf("%.1f / %.1f", c.RMSDB, c.PeakDB))
				}
			}
			b.WriteByte('\n')
		}
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}

// --- the job --------------------------------------------------------------------

// mixJob is one running or finished mix measurement.
type mixJob struct {
	mu      sync.Mutex
	song    string
	phase   string
	started time.Time
	report  *mixReport
	err     string
	done    chan struct{}
	cancel  context.CancelFunc
}

func (j *mixJob) setPhase(format string, a ...any) {
	j.mu.Lock()
	j.phase = fmt.Sprintf(format, a...)
	j.mu.Unlock()
}

func (j *mixJob) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// mixArgs is what a mix measurement was asked for.
type mixArgs struct {
	From, Count int
	Whole       bool
	PerTrack    bool
	SoloBars    int
	// TrackTaps are the tracks' own taps, by track id, when every track has
	// one and the tracks are to be measured there; nil measures them by solo
	// passes.
	TrackTaps map[string]*audiotap.Store
	// Transport runs AUM's transport through every pass; Sequencer says the
	// session hosts the sequencer, which is bypassed meanwhile.
	Transport, Sequencer bool
	// Files says a track of the song plays a file: the song is then played
	// from its session's sequencer, from the top, because a File Player
	// starts its file at the transport's top and nowhere else.
	Files bool
}

func (s *Server) handleMixSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song      string `json:"song"`
		From      string `json:"from"`
		Sections  int    `json:"sections"`
		PerTrack  bool   `json:"per_track"`
		Solo      bool   `json:"solo"`
		SoloBars  int    `json:"solo_bars"`
		Whole     *bool  `json:"whole"`
		Transport *bool  `json:"transport"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	from, err := songStart(cs.timeline, args.From)
	if err != nil {
		return textResult("/from: "+err.Error(), true), nil
	}
	ma := mixArgs{From: from, Count: args.Sections, PerTrack: args.PerTrack, SoloBars: args.SoloBars, Whole: args.Whole == nil || *args.Whole}
	if ma.SoloBars <= 0 {
		ma.SoloBars = mixSoloBars
	}
	if !ma.Whole && !ma.PerTrack {
		return textResult("whole: false leaves nothing to measure without per_track: true", true), nil
	}
	if busy := s.stemsBusy("mix_song"); busy != nil {
		return busy, nil
	}
	if s.midi == nil || !s.midi.Connected() {
		return textResult("mix_song: "+midicontrol.ErrNoBrain.Error()+" — load_song puts the song's session into AUM", true), nil
	}
	if s.audio == nil {
		return textResult("mix_song: no audio tap is configured", true), nil
	}
	rec, authored := loadSongSessions()[cs.id]
	ma.Transport, ma.Sequencer = args.Transport == nil || *args.Transport, rec.Sequence != ""
	// The session's master tap when it names itself, else the latest tap.
	tap, _ := s.songTap(rec.Session)
	if tap == nil {
		return textResult("mix_song: no ProbeAudioTap is streaming — load_song puts the song's session into AUM", true), nil
	}
	switch cur := s.loadedSessionID(); {
	case !authored || rec.Fingerprint != cs.timeline.Fingerprint:
		return textResult(fmt.Sprintf("mix_song: the session of %q is not authored for the song as it is now; load_song first", cs.id), true), nil
	case cur != "" && cur != rec.Session:
		return textResult(fmt.Sprintf("mix_song: AUM has %q open, not this song's session %q; load_song first", cur, rec.Session), true), nil
	}
	// A song with a track that plays a file is played from its sequencer and
	// from the top: the file starts there and cannot be placed.
	if ma.Files = cs.song.HasFiles(); ma.Files {
		switch {
		case from != 0:
			return textResult("mix_song: a track of this song plays a file, which starts at the top of AUM's transport: the song can only be measured from the top (leave out from; sections may end it early)", true), nil
		case !ma.Transport:
			return textResult("mix_song: a track of this song plays a file, which plays only while AUM's transport runs (leave out transport: false)", true), nil
		case !rec.sequenceCurrent(cs.timeline):
			return textResult(fmt.Sprintf("mix_song: the sequencer of %q does not hold the song as it is now, and a song with a file track plays from it; load_song first", rec.Session), true), nil
		case ma.PerTrack && args.Solo:
			return textResult("mix_song: a track of this song plays a file, so the tracks cannot be measured in solo passes of a section each (every pass would play the file's beginning); leave out solo and give the session a tap on every track (session: {track_taps: true})", true), nil
		}
	}
	// The tracks' own taps, when every track has one: they are read out
	// while the song plays, and no track needs a pass of its own.
	tapNote := ""
	if ma.PerTrack && !args.Solo {
		taps, missing := s.trackTaps(cs, rec.Session)
		switch {
		case len(missing) == 0:
			ma.TrackTaps = taps
		case len(taps) > 0 || rec.Options.TrackTaps:
			tapNote = fmt.Sprintf("\nno tap is connected for %s, so the tracks are measured by solo passes at the master", strings.Join(missing, ", "))
		}
	}
	if ma.Files && ma.PerTrack && ma.TrackTaps == nil {
		return textResult("mix_song: a track of this song plays a file, so the tracks cannot be measured in solo passes of a section each (every pass would play the file's beginning); they are measured at their own taps, which this session lacks: session: {track_taps: true} in the song, then load_song"+tapNote, true), nil
	}

	s.mixMu.Lock()
	defer s.mixMu.Unlock()
	if s.mix != nil && !s.mix.finished() {
		return textResult(fmt.Sprintf("mix_song: a measurement of %q is running (%s); get_song_mix follows it, stop_song ends it", s.mix.song, s.mix.phase), true), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &mixJob{song: cs.id, phase: "starting", started: time.Now(), done: make(chan struct{}), cancel: cancel}
	s.mix = job
	go func() {
		defer close(job.done)
		defer cancel()
		rep, err := s.runMix(ctx, job, cs, tap, ma)
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

	last := len(cs.timeline.Sections) - 1
	if ma.Count > 0 && from+ma.Count-1 < last {
		last = from + ma.Count - 1
	}
	whole := cs.timeline.TickTime(cs.timeline.Sections[last].Tick + int64(cs.timeline.Sections[last].Bars)*cs.timeline.BarTicks - cs.timeline.Sections[from].Tick)
	est := time.Duration(0)
	if ma.Whole || ma.TrackTaps != nil {
		est += whole + mixTail + 2*time.Second
	}
	var solos []soloPass
	if ma.PerTrack && ma.TrackTaps == nil {
		solos = s.mixSolos(cs, from, last, ma.SoloBars, ma.Transport)
		for _, so := range solos {
			est += cs.timeline.TickTime(int64(so.bars)*cs.timeline.BarTicks) + mixBetween + 2*time.Second
		}
	}
	text := fmt.Sprintf("measuring the mix of %q: about %s", cs.song.Title, clock(est))
	out := map[string]any{"song": cs.id, "estimate_seconds": est.Seconds(), "solo_passes": len(solos)}
	switch {
	case ma.TrackTaps != nil:
		text += fmt.Sprintf(" (the %d track(s) at their own taps, in the same pass)", len(ma.TrackTaps))
		out["tracks_at"] = tracksAtTaps
	case ma.PerTrack:
		text += fmt.Sprintf(" (%d solo pass(es))", len(solos))
		out["tracks_at"] = tracksAtMaster
	}
	if ma.Transport {
		text += ", with AUM's transport running"
	}
	out["transport"] = ma.Transport
	text += tapNote
	text += "\nget_song_mix gives the progress and the report (wait_ms waits for it); stop_song ends it"
	return structResult(text, out), nil
}

func (s *Server) handleGetSongMix(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		WaitMS int `json:"wait_ms"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	s.mixMu.Lock()
	job := s.mix
	s.mixMu.Unlock()
	if job == nil {
		return textResult("no mix has been measured (mix_song starts one)", false), nil
	}
	if wait := min(time.Duration(args.WaitMS)*time.Millisecond, mixWaitMost); wait > 0 {
		select {
		case <-job.done:
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if !job.finished() {
		return structResult(fmt.Sprintf("measuring the mix of %q: %s (%s so far)", job.song, job.phase, clock(time.Since(job.started))),
			map[string]any{"song": job.song, "done": false, "phase": job.phase}), nil
	}
	if job.report == nil {
		return textResult(fmt.Sprintf("the mix measurement of %q failed: %s", job.song, job.err), true), nil
	}
	text := job.report.text()
	if job.err != "" {
		text += "the measurement ended early: " + job.err + "\n"
	}
	return structResult(text, map[string]any{"song": job.song, "done": true, "report": job.report, "error": job.err}), nil
}

// soloPass is one track in one section.
type soloPass struct {
	index         int // the arrangement entry
	section       string
	track         string
	bars, notes   int
	startTick     int64
	sectionLength int
}

// mixSolos lists the solo passes of a range: every distinct section once, at
// its first occurrence, with every track that plays notes in it, for at most
// bars bars of it. quiet adds the tracks that play no notes there but whose
// strip is open: an instrument may sound by itself (its own sequencer, with
// the transport running), and nothing in the song says which does, so they
// are measured too and the ones that stay silent are left out of the report.
func (s *Server) mixSolos(cs *compiledSong, from, last, bars int, quiet bool) []soloPass {
	tl := cs.timeline
	var out []soloPass
	seen := map[string]bool{}
	for idx := from; idx <= last; idx++ {
		sec := tl.Sections[idx]
		if seen[sec.Name] {
			continue
		}
		seen[sec.Name] = true
		notes := map[string]int{}
		for _, e := range tl.Events {
			if e.Kind == song.NoteOn && e.Section == idx {
				notes[e.Track]++
			}
		}
		for _, tr := range tl.Tracks {
			if notes[tr.ID] == 0 && (!quiet || stripMuted(cs.plan.Track(tr.ID), sec.State)) {
				continue
			}
			out = append(out, soloPass{index: idx, section: sec.Name, track: tr.ID, bars: min(sec.Bars, bars), notes: notes[tr.ID], startTick: sec.Tick, sectionLength: sec.Bars})
		}
	}
	return out
}

// stripMuted reports whether a section's state leaves a track's strip muted.
func stripMuted(tp *song.TrackPlan, state []song.Event) bool {
	if tp == nil {
		return true
	}
	m := tp.MuteEvent(true)
	for _, e := range state {
		if e.Kind == m.Kind && e.Channel == m.Channel && e.Data1 == m.Data1 {
			return e.Data2 >= 64
		}
	}
	return tp.HomeMute()
}

// runMix does the measuring.
func (s *Server) runMix(ctx context.Context, job *mixJob, cs *compiledSong, tap *audiotap.Store, a mixArgs) (*mixReport, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	// The passes are played one after another: the host must not doze
	// between them either, or the next one starts into a stalled stream.
	defer s.hold(fmt.Sprintf("mix_song %q", cs.id))()
	tl := cs.timeline
	rep := &mixReport{Song: cs.id, Title: cs.song.Title, Tempo: tl.Tempo}
	began := time.Now()
	defer func() { rep.Seconds = math.Round(time.Since(began).Seconds()) }()

	// The master's meter, and one for every track that has a tap of its own.
	meter := newTapMeter(tap)
	meters := []*tapMeter{meter}
	meterOf := []string{song.MasterTap}
	trackMeters := map[string]*tapMeter{}
	for _, tp := range cs.plan.Tracks {
		if st := a.TrackTaps[tp.ID]; st != nil {
			trackMeters[tp.ID] = newTapMeter(st)
			meters, meterOf = append(meters, trackMeters[tp.ID]), append(meterOf, tp.ID)
		}
	}
	mctx, stopMeter := context.WithCancel(context.Background())
	var metering sync.WaitGroup
	for _, m := range meters {
		metering.Add(1)
		go func() { defer metering.Done(); m.run(mctx) }()
	}
	defer func() {
		stopMeter()
		metering.Wait()
		var stalled []string
		for i, m := range meters {
			if rep.Stalls += m.stalls; m.stalls > 0 {
				stalled = append(stalled, fmt.Sprintf("%s %d", meterOf[i], m.stalls))
			}
		}
		switch {
		case rep.Stalls > 0 && len(meters) == 1:
			rep.Findings = append(rep.Findings, fmt.Sprintf("the tap's stream stalled %d time(s) while measuring: the levels around a stall read low", rep.Stalls))
		case rep.Stalls > 0:
			rep.Findings = append(rep.Findings, fmt.Sprintf("the taps' streams stalled %d time(s) while measuring (%s): the levels around a stall read low", rep.Stalls, strings.Join(stalled, ", ")))
		}
	}()
	send := func(sctx context.Context, e song.Event) error { return s.sendBrain(sctx, songCommand(e)) }

	// play starts a playback and, once its first note is due, notes on every
	// meter where in its stream that note is: the zero the levels of the
	// playback are counted from.
	//
	// With the transport, every pass rewinds it, starts it with the first
	// note, and stops and rewinds it at the end — also when the pass is
	// stopped or fails (the player sends that however it ends) — with the
	// session's sequencer bypassed in between.
	play := func(opts song.PlayOptions) error {
		if a.Files {
			// From the session's sequencer, as play_song plays such a song:
			// the transport from the top, the files with it, the controls
			// from the clip. The player counts the bars from the end of the
			// lead-in.
			cc := func(at song.Assignment, value int) song.Event {
				return song.Event{Kind: song.Control, Channel: at.Channel, Data1: at.Number, Data2: value}
			}
			p := cs.plan
			if _, err := s.player.StartSequenced(cs.id, tl, tl.Sequence(), song.SequencedOptions{
				Rewind: []song.Event{cc(p.SequencerBypass, 0), cc(p.Transport["Rewind"], 127)},
				Start:  []song.Event{cc(p.Transport["Start Play"], 127)},
				Stop:   []song.Event{cc(p.Transport["Stop/Rewind"], 127), cc(p.Transport["Rewind"], 127)},
			}, send); err != nil {
				return err
			}
		} else if a.Transport {
			var before []song.Event
			opts.Rewind, before, opts.After = liveTransport(cs.plan, a.Sequencer)
			opts.Before = append(opts.Before, before...)
		}
		if !a.Files {
			opts.Preroll = mixPreroll
			if _, err := s.player.Start(cs.id, tl, opts, send); err != nil {
				return err
			}
		}
		zero := func(playedMS int64) {
			for _, m := range meters {
				m.zero = m.position() - float64(playedMS)/1000
			}
		}
		for {
			st := s.player.Status()
			switch {
			case st.Playing && st.PositionMS > 0:
				zero(st.PositionMS)
				return nil
			case !st.Playing && st.Outcome != "":
				if st.Outcome == "finished" {
					zero(st.DurationMS)
					return nil
				}
				return fmt.Errorf("the playback ended: %s", st.Outcome)
			}
			if err := sleepContext(ctx, 5*time.Millisecond); err != nil {
				s.player.Stop()
				return err
			}
		}
	}
	// until waits for the master's meter to reach a position of the
	// playback; it ends early when the playback was stopped from outside.
	until := func(pos float64, playing bool) error {
		for meter.since() < pos {
			if playing {
				if st := s.player.Status(); !st.Playing && st.Outcome != "finished" {
					return fmt.Errorf("the playback ended: %s", st.Outcome)
				}
			}
			if err := sleepContext(ctx, 20*time.Millisecond); err != nil {
				s.player.Stop()
				return err
			}
		}
		return nil
	}
	secs := func(ticks int64) float64 { return tl.TickTime(ticks).Seconds() }

	last := len(tl.Sections) - 1
	if a.Count > 0 && a.From+a.Count-1 < last {
		last = a.From + a.Count - 1
	}
	startTick := tl.Sections[a.From].Tick
	endTick := tl.Sections[last].Tick + int64(tl.Sections[last].Bars)*tl.BarTicks

	// plugin names a track's instrument, for a finding about the track.
	plugin := func(track string) string {
		if tp := cs.plan.Track(track); tp != nil {
			return " (" + tp.Nodes[0].Name + ")"
		}
		return ""
	}

	// The range played through: the master's levels and, where the tracks
	// have taps of their own, every track's.
	atTaps := a.PerTrack && len(trackMeters) > 0
	length := secs(endTick - startTick)
	if a.Whole || atTaps {
		job.setPhase("playing %s to %s", tl.Sections[a.From].Name, tl.Sections[last].Name)
		if err := play(song.PlayOptions{From: a.From, Count: last - a.From + 1}); err != nil {
			return rep, err
		}
		if err := until(length, true); err != nil {
			return rep, err
		}
		if a.Files {
			// The sequencer plays on to the song's end; the range may end
			// before it.
			s.player.Stop()
		}
		if err := until(length+mixTail.Seconds(), false); err != nil {
			return rep, err
		}
		for _, m := range meters {
			m.pull()
		}
	}
	if a.Whole {
		rep.RMSDB, rep.PeakDB, _ = meter.levelSince(0, length)
		_, rep.TailDB, _ = meter.levelSince(length, length+mixTail.Seconds())
		for idx := a.From; idx <= last; idx++ {
			sec := tl.Sections[idx]
			at := secs(sec.Tick - startTick)
			rms, peak, _ := meter.levelSince(at, at+secs(int64(sec.Bars)*tl.BarTicks))
			rep.Sections = append(rep.Sections, mixSection{Position: idx + 1, Name: sec.Name, Bar: sec.Bar, Bars: sec.Bars, RMSDB: rms, PeakDB: peak})
			for b := 0; b < sec.Bars; b++ {
				from := at + secs(int64(b)*tl.BarTicks)
				rms, peak, ok := meter.levelSince(from, from+secs(tl.BarTicks))
				if !ok {
					continue
				}
				rep.Bars = append(rep.Bars, mixBar{Bar: sec.Bar + b, Section: sec.Name, RMSDB: rms, PeakDB: peak, Clipped: peak >= clipDB})
			}
		}
		rep.Findings = append(rep.Findings, wholeFindings(rep)...)
	}

	switch {
	case atTaps:
		// Every track at its own tap, out of the pass just played: each
		// distinct section at its first occurrence, the whole of it.
		rep.TracksAt = tracksAtTaps
		// A track without notes in a section is read too, and kept when it
		// sounded there.
		for _, so := range s.mixSolos(cs, a.From, last, math.MaxInt, true) {
			at := secs(so.startTick - startTick)
			rms, peak, ok := trackMeters[so.track].levelSince(at, at+secs(int64(so.bars)*tl.BarTicks))
			if !ok {
				if so.notes > 0 {
					rep.Findings = append(rep.Findings, fmt.Sprintf("the tap of %s delivered no audio in %s: its level there is not known", so.track, so.section))
				}
				continue
			}
			row := mixSolo{Section: so.section, Track: so.track, Bars: so.bars, Notes: so.notes, RMSDB: rms, PeakDB: peak, Silent: peak <= silentDB}
			if so.notes == 0 && row.Silent {
				continue
			}
			rep.Tracks = append(rep.Tracks, row)
			if row.Silent {
				rep.Findings = append(rep.Findings, fmt.Sprintf("%s%s is silent at its tap in %s although it plays %d note(s) there: check_song tells whether its plugin still answers", so.track, plugin(so.track), so.section, so.notes))
			}
		}
		rep.Findings = append(rep.Findings, soloFindings(rep)...)
	case a.PerTrack:
		// No taps on the tracks: every track alone, at the master.
		rep.TracksAt = tracksAtMaster
		solos := s.mixSolos(cs, a.From, last, a.SoloBars, a.Transport)
		// The passes mute the other strips; the session is left with the
		// strips as the last pass's section has them, however it ends.
		var soloState []song.Event
		defer func() { s.restoreStrips(cs.plan, soloState) }()
		for i, so := range solos {
			job.setPhase("track %s alone in %s (%d of %d)", so.track, so.section, i+1, len(solos))
			if err := sleepContext(ctx, mixBetween); err != nil {
				return rep, err
			}
			// Only the track's notes are played, and every other strip is
			// muted as the pass begins (the section's sound has just opened
			// them), so an instrument that plays by itself with the transport
			// is not heard in another track's pass.
			var others []song.Event
			for _, tp := range cs.plan.Tracks {
				if tp.ID != so.track {
					others = append(others, tp.MuteEvent(true))
				}
			}
			soloState = tl.Sections[so.index].State
			if err := play(song.PlayOptions{From: so.index, Count: 1, Tracks: []string{so.track}, Before: others}); err != nil {
				return rep, err
			}
			played := secs(int64(so.bars) * tl.BarTicks)
			err := until(played, true)
			s.player.Stop()
			if err != nil {
				return rep, err
			}
			meter.pull()
			rms, peak, _ := meter.levelSince(0, played)
			row := mixSolo{Section: so.section, Track: so.track, Bars: so.bars, Notes: so.notes, RMSDB: rms, PeakDB: peak, Silent: peak <= silentDB}
			if so.notes == 0 && row.Silent {
				// No notes and no sound of its own there: nothing to report.
				continue
			}
			rep.Tracks = append(rep.Tracks, row)
			if row.Silent {
				rep.Findings = append(rep.Findings, fmt.Sprintf("%s%s is silent in %s although it plays %d note(s) there: check_song tells whether its plugin still answers", so.track, plugin(so.track), so.section, so.notes))
			}
		}
		rep.Findings = append(rep.Findings, soloFindings(rep)...)
	}
	return rep, nil
}

// restoreStrips puts every track's strip back to the mute a section's state
// gives it, after solo passes muted the others. It runs however the
// measurement ended, so it does not use its context, and it waits songMuteGap
// first: a mute and its reversal that reach AUM in one audio buffer can leave
// the strip at the first (see stripMutes).
func (s *Server) restoreStrips(plan *song.Plan, state []song.Event) {
	if len(state) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = sleepContext(ctx, songMuteGap)
	for _, tp := range plan.Tracks {
		_ = s.sendBrain(ctx, songCommand(tp.MuteEvent(stripMuted(tp, state))))
	}
}

// wholeFindings says what stands out in the whole pass.
func wholeFindings(r *mixReport) []string {
	var out []string
	if len(r.Bars) == 0 {
		return []string{"nothing was metered: the tap delivered no audio while the song played"}
	}
	var clipped []string
	loudest, quietest := r.Bars[0], r.Bars[0]
	for _, b := range r.Bars {
		if b.Clipped {
			clipped = append(clipped, fmt.Sprint(b.Bar))
		}
		if b.RMSDB > loudest.RMSDB {
			loudest = b
		}
		if b.RMSDB < quietest.RMSDB {
			quietest = b
		}
	}
	if len(clipped) > 0 {
		out = append(out, fmt.Sprintf("clipped (peak at %.1f dBFS or above) in bar(s) %s", clipDB, strings.Join(clipped, ", ")))
	} else {
		out = append(out, fmt.Sprintf("headroom: the highest peak is %.1f dBFS", r.PeakDB))
	}
	out = append(out, fmt.Sprintf("loudest bar %d (%s) at %.1f dBFS rms, quietest bar %d (%s) at %.1f", loudest.Bar, loudest.Section, loudest.RMSDB, quietest.Bar, quietest.Section, quietest.RMSDB))
	if r.PeakDB <= silentDB {
		out = append(out, "the song was silent at the tap: check_song tells what is missing")
	}
	return out
}

// soloFindings says what stands out between the tracks of each section.
func soloFindings(r *mixReport) []string {
	var out []string
	bySection := map[string][]mixSolo{}
	var order []string
	for _, t := range r.Tracks {
		if t.Silent {
			continue
		}
		if _, ok := bySection[t.Section]; !ok {
			order = append(order, t.Section)
		}
		bySection[t.Section] = append(bySection[t.Section], t)
	}
	for _, sec := range order {
		rows := bySection[sec]
		if len(rows) < 2 {
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].RMSDB > rows[j].RMSDB })
		top, low := rows[0], rows[len(rows)-1]
		if top.RMSDB-low.RMSDB >= 12 {
			out = append(out, fmt.Sprintf("in %s, %s (%.1f dBFS rms) is %.1f dB over %s (%.1f)", sec, top.Track, top.RMSDB, top.RMSDB-low.RMSDB, low.Track, low.RMSDB))
		}
	}
	return out
}
