package mcpserver

// This file measures a song's mix (mix_song): it plays the song and reads the
// master tap out for its whole length, so the levels of every bar and every
// section are known, and on request plays every section once per track for
// the level of each track in it.
//
// The tap keeps only its last seconds, so the stream is read out as it
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
			"per_track: true then plays each section once per track (the first bars of it, solo) and adds the level of every track in every section — the table a balance is made from — and names a track that is silent where it has notes. " +
			"Returns at once; get_song_mix gives the progress and the report (stop_song ends the measurement). The song's session must be loaded (load_song).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				` + songArg + `,
				"from": {"type": "string", "description": "Where to start: a section id or a 1-based position in the arrangement (default: the top)."},
				"sections": {"type": "integer", "minimum": 1, "description": "How many arrangement entries to play (default: to the end)."},
				"per_track": {"type": "boolean", "description": "Also measure every track alone in every section."},
				"solo_bars": {"type": "integer", "minimum": 1, "maximum": 32, "description": "How many bars of a section a solo pass plays (default 4)."},
				"whole": {"type": "boolean", "description": "Play the whole range through (default true). false with per_track measures only the tracks."}
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

// mixSolo is one track alone in one section.
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
	// Tracks are the solo passes.
	Tracks []mixSolo `json:"tracks,omitempty"`
	// Stalls counts stretches the tap's stream did not deliver.
	Stalls int `json:"stalls,omitempty"`
	// Findings say what stands out.
	Findings []string `json:"findings,omitempty"`
	Seconds  float64  `json:"seconds"`
}

func (r *mixReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mix of %q (%s) at %g BPM, measured at the master tap in %.0f s\n", r.Title, r.Song, r.Tempo, r.Seconds)
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
		b.WriteString("tracks alone (rms / peak dBFS):\n")
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
}

func (s *Server) handleMixSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song     string `json:"song"`
		From     string `json:"from"`
		Sections int    `json:"sections"`
		PerTrack bool   `json:"per_track"`
		SoloBars int    `json:"solo_bars"`
		Whole    *bool  `json:"whole"`
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
	if s.midi == nil || !s.midi.Connected() {
		return textResult("mix_song: "+midicontrol.ErrNoBrain.Error()+" — load_song puts the song's session into AUM", true), nil
	}
	if s.audio == nil {
		return textResult("mix_song: no audio tap is configured", true), nil
	}
	tap, ok := s.audio.Active()
	if !ok || !tap.Connected() {
		return textResult("mix_song: no ProbeAudioTap is streaming — load_song puts the song's session into AUM", true), nil
	}
	rec, authored := loadSongSessions()[cs.id]
	switch cur := s.loadedSessionID(); {
	case !authored || rec.Fingerprint != cs.timeline.Fingerprint:
		return textResult(fmt.Sprintf("mix_song: the session of %q is not authored for the song as it is now; load_song first", cs.id), true), nil
	case cur != "" && cur != rec.Session:
		return textResult(fmt.Sprintf("mix_song: AUM has %q open, not this song's session %q; load_song first", cur, rec.Session), true), nil
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
	if ma.Whole {
		est += whole + mixTail + 2*time.Second
	}
	solos := s.mixSolos(cs, from, last, ma.SoloBars)
	if ma.PerTrack {
		for _, so := range solos {
			est += cs.timeline.TickTime(int64(so.bars)*cs.timeline.BarTicks) + mixBetween + 2*time.Second
		}
	}
	text := fmt.Sprintf("measuring the mix of %q: about %s", cs.song.Title, clock(est))
	if ma.PerTrack {
		text += fmt.Sprintf(" (%d solo pass(es))", len(solos))
	}
	text += "\nget_song_mix gives the progress and the report (wait_ms waits for it); stop_song ends it"
	return structResult(text, map[string]any{"song": cs.id, "estimate_seconds": est.Seconds(), "solo_passes": len(solos)}), nil
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

// soloPass is one track alone in one section.
type soloPass struct {
	index         int // the arrangement entry
	section       string
	track         string
	bars, notes   int
	startTick     int64
	sectionLength int
}

// mixSolos lists the solo passes of a range: every distinct section once, at
// its first occurrence, with every track that plays notes in it.
func (s *Server) mixSolos(cs *compiledSong, from, last, bars int) []soloPass {
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
			if notes[tr.ID] == 0 {
				continue
			}
			out = append(out, soloPass{index: idx, section: sec.Name, track: tr.ID, bars: min(sec.Bars, bars), notes: notes[tr.ID], startTick: sec.Tick, sectionLength: sec.Bars})
		}
	}
	return out
}

// runMix does the measuring.
func (s *Server) runMix(ctx context.Context, job *mixJob, cs *compiledSong, tap *audiotap.Store, a mixArgs) (*mixReport, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	tl := cs.timeline
	rep := &mixReport{Song: cs.id, Title: cs.song.Title, Tempo: tl.Tempo}
	began := time.Now()
	defer func() { rep.Seconds = math.Round(time.Since(began).Seconds()) }()

	meter := newTapMeter(tap)
	mctx, stopMeter := context.WithCancel(context.Background())
	meterDone := make(chan struct{})
	go func() { meter.run(mctx); close(meterDone) }()
	defer func() {
		stopMeter()
		<-meterDone
		rep.Stalls = meter.stalls
		if rep.Stalls > 0 {
			rep.Findings = append(rep.Findings, fmt.Sprintf("the tap's stream stalled %d time(s) while measuring: the levels around a stall read low", rep.Stalls))
		}
	}()
	send := func(sctx context.Context, e song.Event) error { return s.sendBrain(sctx, songCommand(e)) }

	// play starts a playback and returns the meter position of its first
	// note, once that note is due.
	play := func(opts song.PlayOptions) (float64, error) {
		if _, err := s.player.Start(cs.id, tl, opts, send); err != nil {
			return 0, err
		}
		for {
			st := s.player.Status()
			switch {
			case st.Playing && st.PositionMS > 0:
				return meter.position() - float64(st.PositionMS)/1000, nil
			case !st.Playing && st.Outcome != "":
				if st.Outcome == "finished" {
					return meter.position() - float64(st.DurationMS)/1000, nil
				}
				return 0, fmt.Errorf("the playback ended: %s", st.Outcome)
			}
			if err := sleepContext(ctx, 5*time.Millisecond); err != nil {
				s.player.Stop()
				return 0, err
			}
		}
	}
	// until waits for the meter to reach a position; it ends early when the
	// playback was stopped from outside.
	until := func(pos float64, playing bool) error {
		for meter.position() < pos {
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

	if a.Whole {
		job.setPhase("playing %s to %s", tl.Sections[a.From].Name, tl.Sections[last].Name)
		t0, err := play(song.PlayOptions{From: a.From, Count: last - a.From + 1})
		if err != nil {
			return rep, err
		}
		length := secs(endTick - startTick)
		if err := until(t0+length, true); err != nil {
			return rep, err
		}
		if err := until(t0+length+mixTail.Seconds(), false); err != nil {
			return rep, err
		}
		meter.pull()
		rep.RMSDB, rep.PeakDB, _ = meter.level(t0, t0+length)
		_, rep.TailDB, _ = meter.level(t0+length, t0+length+mixTail.Seconds())
		for idx := a.From; idx <= last; idx++ {
			sec := tl.Sections[idx]
			at := t0 + secs(sec.Tick-startTick)
			rms, peak, _ := meter.level(at, at+secs(int64(sec.Bars)*tl.BarTicks))
			rep.Sections = append(rep.Sections, mixSection{Position: idx + 1, Name: sec.Name, Bar: sec.Bar, Bars: sec.Bars, RMSDB: rms, PeakDB: peak})
			for b := 0; b < sec.Bars; b++ {
				from := at + secs(int64(b)*tl.BarTicks)
				rms, peak, ok := meter.level(from, from+secs(tl.BarTicks))
				if !ok {
					continue
				}
				rep.Bars = append(rep.Bars, mixBar{Bar: sec.Bar + b, Section: sec.Name, RMSDB: rms, PeakDB: peak, Clipped: peak >= clipDB})
			}
		}
		rep.Findings = append(rep.Findings, wholeFindings(rep)...)
	}

	if a.PerTrack {
		solos := s.mixSolos(cs, a.From, last, a.SoloBars)
		for i, so := range solos {
			job.setPhase("track %s alone in %s (%d of %d)", so.track, so.section, i+1, len(solos))
			if err := sleepContext(ctx, mixBetween); err != nil {
				return rep, err
			}
			t0, err := play(song.PlayOptions{From: so.index, Count: 1, Tracks: []string{so.track}})
			if err != nil {
				return rep, err
			}
			length := secs(int64(so.bars) * tl.BarTicks)
			err = until(t0+length, true)
			s.player.Stop()
			if err != nil {
				return rep, err
			}
			meter.pull()
			rms, peak, _ := meter.level(t0, t0+length)
			row := mixSolo{Section: so.section, Track: so.track, Bars: so.bars, Notes: so.notes, RMSDB: rms, PeakDB: peak, Silent: peak <= silentDB}
			rep.Tracks = append(rep.Tracks, row)
			if row.Silent {
				plugin := ""
				if tp := cs.plan.Track(so.track); tp != nil {
					plugin = " (" + tp.Nodes[0].Name + ")"
				}
				rep.Findings = append(rep.Findings, fmt.Sprintf("%s%s is silent in %s although it plays %d note(s) there: check_song tells whether its plugin still answers", so.track, plugin, so.section, so.notes))
			}
		}
		rep.Findings = append(rep.Findings, soloFindings(rep)...)
	}
	return rep, nil
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
