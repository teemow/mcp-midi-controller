package mcpserver

// apply_technique with verify: the card's change measured the way the card
// says. The song's session plays a pass of what the card is applied to before
// the change, the patched song is stored and its session loaded again, and
// the same pass is played after it. Both are read at the master tap, and the
// card's measurement compares them. A miss is reported, not hidden, and the
// change stays: the agent decides what to do about it.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/audiotap"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/measure"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

const (
	// verifyBars is how many bars a verify pass plays by default.
	verifyBars = 4
	// verifySkip is left out at a pass's start: the tap's stream runs behind
	// the playback's clock, and a plugin settles after its first notes.
	verifySkip = 500 * time.Millisecond
)

// verifyReport is what apply_technique's verify found.
type verifyReport struct {
	Song      string       `json:"song"`
	Technique string       `json:"technique"`
	Target    string       `json:"target"`
	Measure   string       `json:"measure"`
	Band      []float64    `json:"band,omitempty"`
	Unit      string       `json:"unit,omitempty"`
	Before    float64      `json:"before"`
	After     float64      `json:"after"`
	Change    float64      `json:"change"`
	Expect    string       `json:"expect"`
	Value     float64      `json:"value"`
	Met       bool         `json:"met"`
	Seconds   float64      `json:"seconds"`
	Answer    craft.Answer `json:"answer"`
	Session   string       `json:"session"`
}

func (r *verifyReport) text() string {
	var b strings.Builder
	verdict := "MISSED"
	if r.Met {
		verdict = "met"
	}
	band := ""
	if len(r.Band) == 2 {
		band = fmt.Sprintf(" %g..%g Hz", r.Band[0], r.Band[1])
	}
	fmt.Fprintf(&b, "verify %s on %s of %q: %s%s %s\n", r.Technique, r.Target, r.Song, r.Measure, band, verdict)
	fmt.Fprintf(&b, "  before %.2f %s, after %.2f %s (%+.2f)\n", r.Before, r.Unit, r.After, r.Unit, r.Change)
	switch r.Expect {
	case "falls", "rises":
		fmt.Fprintf(&b, "  the card asks that it %s by %g %s or more\n", r.Expect, r.Value, r.Unit)
	default:
		fmt.Fprintf(&b, "  the card asks %s %g %s\n", strings.ReplaceAll(r.Expect, "_", " "), r.Value, r.Unit)
	}
	b.WriteString(r.Answer.Summary())
	fmt.Fprintf(&b, "\nsession %s loaded again with the change; measured in %.0f s", r.Session, r.Seconds)
	return b.String()
}

// met judges a card's expectation: falls and rises compare after against
// before, at_most and at_least the reading after.
func met(expect string, before, after, value float64) bool {
	switch expect {
	case "falls":
		return before-after >= value
	case "rises":
		return after-before >= value
	case "at_most":
		return after <= value
	case "at_least":
		return after >= value
	}
	return false
}

// startVerify checks what verify needs and starts it in the background.
func (s *Server) startVerify(_ context.Context, args applyArgs, lib *craft.Library, rig craft.Rig, t *craft.Technique) (*mcp.CallToolResult, error) {
	switch {
	case t.Verify == nil:
		return textResult(fmt.Sprintf("verify: %s says nothing to measure (it has no verify); apply it without verify", t.ID), true), nil
	case !args.Apply:
		return textResult("verify: needs apply: true, since the change is measured in the song's session as it is loaded with it", true), nil
	case s.ipad != nil && !args.Confirm:
		return textResult("verify: needs confirm: true: the song's session is loaded again with the change, which replaces what AUM has open", true), nil
	case s.midi == nil || !s.midi.Connected():
		return textResult("verify: "+midicontrol.ErrNoBrain.Error()+" — load_song puts the song's session into AUM", true), nil
	case s.audio == nil:
		return textResult("verify: no audio tap is configured", true), nil
	}
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return fail, nil
	}
	rec, authored := loadSongSessions()[cs.id]
	switch cur := s.loadedSessionID(); {
	case !authored || rec.Fingerprint != cs.timeline.Fingerprint:
		return textResult(fmt.Sprintf("verify: the session of %q is not authored for the song as it is now; load_song first, so the change is measured against the song as it is", cs.id), true), nil
	case cur != "" && cur != rec.Session:
		return textResult(fmt.Sprintf("verify: AUM has %q open, not this song's session %q; load_song first", cur, rec.Session), true), nil
	}
	// The patch is worked out now, so a card that cannot apply says so at once.
	if _, err := s.planTechnique(cs.id, cs.song, args, lib, rig, t); err != nil {
		return textResult(err.Error(), true), nil
	}
	_, fail = s.startCraftJob("apply_technique", cs.id, func(ctx context.Context, job *craftJob) (string, any, error) {
		rep, err := s.runVerify(ctx, job, args, lib, rig, t)
		if rep == nil {
			return "", nil, err
		}
		return rep.text(), rep, err
	})
	if fail != nil {
		return fail, nil
	}
	return structResult(fmt.Sprintf("verifying %s on %q in the background: a pass before, the change stored and the session loaded again, a pass after (get_craft_job follows it, stop_song ends it)", t.ID, cs.song.Title),
		map[string]any{"song": cs.id, "technique": t.ID, "job": "apply_technique"}), nil
}

func (s *Server) runVerify(ctx context.Context, job *craftJob, args applyArgs, lib *craft.Library, rig craft.Rig, t *craft.Technique) (*verifyReport, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	began := time.Now()
	cs, fail := s.loadSong(args.Song)
	if fail != nil {
		return nil, fmt.Errorf("%s", resultText(fail))
	}
	target, err := techniqueTarget(cs, t, args)
	if err != nil {
		return nil, err
	}
	env := craftEnv(cs, target, args.Section)
	rep := &verifyReport{Song: cs.id, Technique: t.ID, Target: targetName(target), Measure: string(t.Verify.Measure), Unit: t.Verify.Unit, Expect: t.Verify.Expect}
	if rep.Value, err = t.Verify.Value.Eval(env); err != nil {
		return nil, fmt.Errorf("verify: %s: %v", t.Verify.Value, err)
	}
	for _, e := range t.Verify.Band {
		x, err := e.Eval(env)
		if err != nil {
			return nil, fmt.Errorf("verify: band %s: %v", e, err)
		}
		rep.Band = append(rep.Band, x)
	}
	pass := verifyPass{from: 0, bars: args.Bars}
	if args.Section != "" {
		if pass.from, err = songStart(cs.timeline, args.Section); err != nil {
			return nil, fmt.Errorf("/section: %v", err)
		}
	}
	if pass.bars <= 0 {
		pass.bars = verifyBars
	}
	if target.Track != song.MasterTrack {
		pass.tracks = []string{target.Track}
	}

	rec := loadSongSessions()[cs.id]
	tap, _ := s.songTap(rec.Session)
	if tap == nil {
		return nil, fmt.Errorf("no ProbeAudioTap is streaming — load_song puts the song's session into AUM")
	}
	job.setPhase("playing the pass before the change")
	before, err := s.verifyMeasure(ctx, cs, tap, pass, rep)
	if err != nil {
		return nil, fmt.Errorf("the pass before the change: %w", err)
	}
	rep.Before = before

	job.setPhase("storing the change")
	res, err := s.RunCraftTool(t.Craft, cs.id, true, func(id string, sg *song.Song, _ []byte) (CraftPlan, error) {
		a, err := s.planTechnique(id, sg, args, lib, rig, t)
		if err != nil {
			return CraftPlan{}, err
		}
		return CraftPlan{Techniques: []string{t.ID}, Patch: a.Patch, Why: a.Why, Checks: a.Checks}, nil
	})
	if err != nil || res.IsError {
		return nil, fmt.Errorf("storing the change: %s", resultText(res))
	}
	if ans, ok := res.StructuredContent.(craft.Answer); ok {
		rep.Answer = ans
	}

	job.setPhase("loading the session with the change")
	cs, fail = s.loadSong(args.Song)
	if fail != nil {
		return nil, fmt.Errorf("%s", resultText(fail))
	}
	lr, err := s.runLoadSong(ctx, cs, loadSongArgs{Check: false})
	if err != nil {
		return nil, fmt.Errorf("loading the session with the change: %w", err)
	}
	if !lr.OK {
		return nil, fmt.Errorf("loading the session with the change: %s", lr.text("not loaded"))
	}
	rep.Session = lr.Session
	if tap, _ = s.songTap(lr.Session); tap == nil {
		return nil, fmt.Errorf("no ProbeAudioTap streams after the load")
	}

	job.setPhase("playing the pass after the change")
	after, err := s.verifyMeasure(ctx, cs, tap, pass, rep)
	if err != nil {
		return nil, fmt.Errorf("the pass after the change: %w", err)
	}
	rep.After = after
	rep.Change = round2(after - before)
	rep.Met = met(rep.Expect, before, after, rep.Value)
	rep.Seconds = math.Round(time.Since(began).Seconds())
	verdict := "met"
	if !rep.Met {
		verdict = "MISSED"
	}
	// The answer said verify had not run; now it has.
	checks := rep.Answer.Checks[:0:0]
	for _, c := range rep.Answer.Checks {
		if !strings.HasPrefix(c, "verify: ") {
			checks = append(checks, c)
		}
	}
	rep.Answer.Checks = append(checks, fmt.Sprintf("verify: %s %s: %.2f before, %.2f after (%+.2f %s)", t.Verify, verdict, before, after, rep.Change, rep.Unit))
	return rep, nil
}

// verifyPass is what a verify plays: from an arrangement entry, some bars,
// only some tracks' notes (none: all).
type verifyPass struct {
	from, bars int
	tracks     []string
}

// verifyMeasure plays one pass and reads the card's measurement from it.
func (s *Server) verifyMeasure(ctx context.Context, cs *compiledSong, tap *audiotap.Store, pass verifyPass, rep *verifyReport) (float64, error) {
	sig, err := s.grabPass(ctx, cs, tap, pass)
	if err != nil {
		return 0, err
	}
	v, err := measure.Measure(sig, rep.Measure, rep.Band)
	if err != nil {
		return 0, err
	}
	return round2(v), nil
}

// grabPass plays a pass live and keeps what the tap carried from its first
// note on, for the pass's bars (or its section's, when that is shorter).
func (s *Server) grabPass(ctx context.Context, cs *compiledSong, tap *audiotap.Store, pass verifyPass) (measure.Signal, error) {
	tl := cs.timeline
	sec := tl.Sections[pass.from]
	bars := min(pass.bars, sec.Bars)
	length := tl.TickTime(int64(bars) * tl.BarTicks)
	send := func(sctx context.Context, e song.Event) error { return s.sendBrain(sctx, songCommand(e)) }
	if _, err := s.player.Start(cs.id, tl, song.PlayOptions{From: pass.from, Count: 1, Tracks: pass.tracks}, send); err != nil {
		return measure.Signal{}, err
	}
	defer s.player.Stop()
	for {
		st := s.player.Status()
		if st.Playing && st.PositionMS > 0 {
			break
		}
		if !st.Playing && st.Outcome != "" {
			return measure.Signal{}, fmt.Errorf("the playback ended before its first note: %s", st.Outcome)
		}
		if err := sleepContext(ctx, 5*time.Millisecond); err != nil {
			return measure.Signal{}, err
		}
	}
	g := newTapGrab(tap)
	end := time.Now().Add(length)
	for time.Now().Before(end) {
		if err := sleepContext(ctx, mixPull); err != nil {
			return measure.Signal{}, err
		}
		g.pull()
	}
	g.pull()
	return g.signal(verifySkip)
}

// tapGrab reads a tap's stream out as it arrives and keeps it, so a pass
// longer than the tap's window is kept whole.
type tapGrab struct {
	tap     *audiotap.Store
	next    int64
	rate    float64
	ch      int
	samples []float32
	stalls  int
}

func newTapGrab(tap *audiotap.Store) *tapGrab {
	return &tapGrab{tap: tap, next: tap.MarkEpoch()}
}

func (g *tapGrab) pull() {
	end := g.tap.MarkEpoch()
	if end <= g.next {
		return
	}
	clip, ok := g.tap.Segment(g.next, end)
	g.next = end
	if !ok {
		g.stalls++
		return
	}
	if !clip.Contiguous {
		g.stalls++
	}
	g.rate, g.ch = clip.SampleRate, max(clip.Channels, 1)
	g.samples = append(g.samples, clip.Samples...)
}

// signal is what was kept, without its first skip.
func (g *tapGrab) signal(skip time.Duration) (measure.Signal, error) {
	if g.rate <= 0 || len(g.samples) == 0 {
		return measure.Signal{}, fmt.Errorf("the tap delivered nothing while the pass played")
	}
	if g.stalls > 0 {
		return measure.Signal{}, fmt.Errorf("the tap's stream stalled %d time(s) during the pass; measure again", g.stalls)
	}
	cut := int(skip.Seconds()*g.rate) * g.ch
	if cut >= len(g.samples) {
		return measure.Signal{}, fmt.Errorf("the pass was too short to measure")
	}
	return measure.Signal{Samples: g.samples[cut:], Channels: g.ch, SampleRate: g.rate}, nil
}

func targetName(t craft.Target) string {
	switch {
	case t.Track == song.MasterTrack && t.Section != "":
		return "the master in section " + t.Section
	case t.Track == song.MasterTrack:
		return "the master"
	case t.Section != "":
		return "track " + t.Track + " in section " + t.Section
	}
	return "track " + t.Track
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
