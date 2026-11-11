package mcpserver

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// fastMix shortens the waits of a mix measurement.
func fastMix(t *testing.T) {
	t.Helper()
	old := []time.Duration{mixTail, mixBetween, mixPull, mixPreroll}
	mixTail, mixBetween, mixPull, mixPreroll = 150*time.Millisecond, 60*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { mixTail, mixBetween, mixPull, mixPreroll = old[0], old[1], old[2], old[3] })
}

// mixRig is a rig playing toolSong's session: the bass loud, the pad very
// quiet, the drums in between.
func mixRig(t *testing.T) *songRig {
	t.Helper()
	r := newSongRig(t)
	fastMix(t)
	r.Voice = func(ch, note int) (float64, float64) {
		hz := 440 * math.Pow(2, float64(note-69)/12)
		switch ch {
		case 1:
			return hz, 0.25
		case 2:
			return hz, 0.01
		}
		return hz, 0.1
	}
	if res := call(t, r.s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	return r
}

// TestMixSong measures a song's mix: every bar and section of the whole
// song, then every track alone in every section.
func TestMixSong(t *testing.T) {
	r := mixRig(t)
	s := r.s
	if res := call(t, s.handleGetSongMix, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "no mix has been measured") {
		t.Fatalf("get_song_mix before any: %s", resultText(res))
	}
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), "no ProbeMidiBrain connected") {
		t.Fatalf("mix without a brain: %s", resultText(res))
	}
	r.connect()
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), "is not authored for the song as it is now") {
		t.Fatalf("mix without a session: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}

	res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "solo_bars": 1})
	if res.IsError || !strings.Contains(resultText(res), `measuring the mix of "Night Drive"`) || !strings.Contains(resultText(res), "6 solo pass(es)") {
		t.Fatalf("mix: %s", resultText(res))
	}
	if again := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); !again.IsError || !strings.Contains(resultText(again), "is running") {
		t.Fatalf("a second measurement was started: %s", resultText(again))
	}
	if res := call(t, s.handleGetSongMix, map[string]any{}); res.IsError || !strings.Contains(resultText(res), "measuring the mix") {
		t.Fatalf("progress: %s", resultText(res))
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("report: %s", text)
	}
	s.mixMu.Lock()
	rep := s.mix.report
	s.mixMu.Unlock()
	if rep == nil || len(rep.Bars) != 2 || len(rep.Sections) != 2 || len(rep.Tracks) != 5 {
		t.Fatalf("report = %+v\n%s", rep, text)
	}
	for _, want := range []string{
		"whole: rms", "sections:", "   1  verse        bars   1-1", "   2  chorus       bars   2-2",
		"tracks alone (rms / peak dBFS):", "verse", "chorus", "headroom: the highest peak is",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	// The bass alone: a sine of 0.25 half of the time (eighths, every second
	// one a rest, gate 0.9): about -19.5 dBFS rms, peak -12.
	var bass, pad mixSolo
	for _, tr := range rep.Tracks {
		if tr.Section == "chorus" && tr.Track == "bass" {
			bass = tr
		}
		if tr.Section == "chorus" && tr.Track == "pad" {
			pad = tr
		}
	}
	if math.Abs(bass.PeakDB+12) > 1 || math.Abs(bass.RMSDB+19.5) > 2 {
		t.Errorf("bass alone = rms %.1f peak %.1f, want about -19.5 and -12", bass.RMSDB, bass.PeakDB)
	}
	if pad.Notes != 3 || pad.Silent || bass.RMSDB-pad.RMSDB < 12 {
		t.Errorf("pad alone = %+v (bass %+v)", pad, bass)
	}
	if !strings.Contains(text, "in chorus, bass (") || !strings.Contains(text, "dB over pad") {
		t.Errorf("the report does not say the pad is far under the bass:\n%s", text)
	}
	// The pad plays only in the chorus: the verse has no pass for it.
	for _, tr := range rep.Tracks {
		if tr.Section == "verse" && tr.Track == "pad" {
			t.Errorf("a solo pass for a track that plays nothing in the section: %+v", tr)
		}
	}
	// Both bars carry the bass: about the same level, nothing clipped.
	for _, b := range rep.Bars {
		if b.Clipped || b.RMSDB > -10 || b.RMSDB < -30 {
			t.Errorf("bar %+v", b)
		}
	}
}

// TestMixSongSilentTrackAndStop checks a track that is silent where it has
// notes is named, and that stop_song ends a measurement.
func TestMixSongSilentTrackAndStop(t *testing.T) {
	r := mixRig(t)
	s := r.s
	r.Voice = func(ch, note int) (float64, float64) {
		if ch == 3 { // the drum machine is dead
			return 0, 0
		}
		return 440 * math.Pow(2, float64(note-69)/12), 0.2
	}
	r.connect()
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "whole": false, "from": "verse", "sections": 1, "solo_bars": 1}); res.IsError {
		t.Fatalf("mix: %s", resultText(res))
	}
	res := call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "drums (DrumBox) is silent in verse although it plays 6 note(s) there") || !strings.Contains(text, "SILENT") {
		t.Fatalf("report with a dead drum machine: %s", text)
	}
	if strings.Contains(text, "whole:") {
		t.Errorf("whole: false still measured the whole song:\n%s", text)
	}

	// Another session in AUM: nothing is played into it.
	s.loaded.set("Songs/Other")
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); !res.IsError || !strings.Contains(resultText(res), `AUM has "Songs/Other" open`) {
		t.Fatalf("mix into another session: %s", resultText(res))
	}
	s.loaded.set("Songs/Night Drive")

	// stop_song ends a running measurement.
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("mix: %s", resultText(res))
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !s.player.Status().Playing; {
		time.Sleep(5 * time.Millisecond)
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 10000})
	if text := resultText(res); !strings.Contains(text, "the measurement ended early") {
		t.Fatalf("report after stop_song: %s", text)
	}
}

// TestMixSongTrackTaps measures the tracks of a session that has a tap on
// every track: each at its own tap while the song plays once, with no solo
// pass, and by solo passes again when a tap is missing or they are asked for.
func TestMixSongTrackTaps(t *testing.T) {
	r := mixRig(t)
	s := r.s
	r.Session = "Songs/Night Drive"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong + "session: {track_taps: true}\n"}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError ||
		!strings.Contains(resultText(res), `"Songs/Night Drive/bass" "Songs/Night Drive/pad" "Songs/Night Drive/drums" "Songs/Night Drive/master"`) {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	bassNotes := func() (n int) {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, cmd := range r.Commands {
			if cmd.Type == "noteOn" && cmd.Channel == 1 {
				n++
			}
		}
		return n
	}

	res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true})
	if text := resultText(res); res.IsError || !strings.Contains(text, "(the 3 track(s) at their own taps, in the same pass)") || strings.Contains(text, "solo pass") {
		t.Fatalf("mix: %s", text)
	}
	if sc := res.StructuredContent.(map[string]any); sc["solo_passes"] != 0 || sc["tracks_at"] != "taps" {
		t.Errorf("mix = %v, want no solo pass and the tracks at their taps", sc)
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("report: %s", text)
	}
	s.mixMu.Lock()
	rep := s.mix.report
	s.mixMu.Unlock()
	if rep == nil || len(rep.Bars) != 2 || len(rep.Sections) != 2 || len(rep.Tracks) != 5 || rep.TracksAt != "taps" {
		t.Fatalf("report = %+v\n%s", rep, text)
	}
	for _, want := range []string{
		"measured at the master tap and every track's own",
		"whole: rms", "tracks at their own taps, in the same pass (rms / peak dBFS):",
		"in chorus, bass (", "dB over pad",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	// The song was played once: four bass notes a bar, two bars.
	if n := bassNotes(); n != 8 {
		t.Errorf("the bass played %d note(s), want the 8 of one pass through the song", n)
	}
	// Each tap carries its own track: the bass as in a solo pass (a sine of
	// 0.25, half of the time: about -19.5 dBFS rms, peak -12), the pad far
	// under it, and no row for a track that plays nothing in a section.
	cell := map[string]mixSolo{}
	for _, tr := range rep.Tracks {
		cell[tr.Section+"/"+tr.Track] = tr
	}
	for _, sec := range []string{"verse", "chorus"} {
		bass := cell[sec+"/bass"]
		if math.Abs(bass.PeakDB+12) > 1 || math.Abs(bass.RMSDB+19.5) > 2 || bass.Bars != 1 || bass.Notes != 4 {
			t.Errorf("bass at its tap in %s = %+v, want about -19.5 rms and -12 peak over the bar", sec, bass)
		}
	}
	// The pad's chord: three sines of 0.01, at most -30.5 dBFS together.
	pad := cell["chorus/pad"]
	if pad.Notes != 3 || pad.Silent || cell["chorus/bass"].RMSDB-pad.RMSDB < 12 || pad.PeakDB > -29 {
		t.Errorf("pad at its tap = %+v (bass %+v): want it far under the bass, and not the bass's level", pad, cell["chorus/bass"])
	}
	if _, ok := cell["verse/pad"]; ok {
		t.Errorf("a level for a track that plays nothing in the section: %+v", cell["verse/pad"])
	}
	// The master carries all of them.
	for _, sec := range rep.Sections {
		if sec.RMSDB < cell[sec.Name+"/bass"].RMSDB-0.5 {
			t.Errorf("the master in %s (%.1f) is under the bass alone (%.1f)", sec.Name, sec.RMSDB, cell[sec.Name+"/bass"].RMSDB)
		}
	}

	// Only the tracks: still one pass, and nothing of the master.
	res = call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "whole": false, "from": "chorus"})
	if res.IsError {
		t.Fatalf("mix of the tracks only: %s", resultText(res))
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	if text := resultText(res); res.IsError || !strings.Contains(text, "measured at every track's own tap") || strings.Contains(text, "whole:") ||
		!strings.Contains(text, "tracks at their own taps") || strings.Contains(text, "verse") {
		t.Fatalf("report of the tracks only: %s", text)
	}

	// A dead instrument is silent at its own tap.
	r.mu.Lock()
	r.Voice = func(ch, note int) (float64, float64) {
		if ch == 3 {
			return 0, 0
		}
		return 440 * math.Pow(2, float64(note-69)/12), 0.2
	}
	r.mu.Unlock()
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "sections": 1}); res.IsError {
		t.Fatalf("mix with a dead drum machine: %s", resultText(res))
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	if text := resultText(res); res.IsError || !strings.Contains(text, "drums (DrumBox) is silent at its tap in verse although it plays 6 note(s) there") {
		t.Fatalf("report with a dead drum machine: %s", text)
	}

	// Solo passes on request: the tracks at the master, as without the taps.
	before := bassNotes()
	res = call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "solo": true, "solo_bars": 1, "whole": false, "sections": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "(3 solo pass(es))") {
		t.Fatalf("mix with solo passes asked for: %s", text)
	}
	res = call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	if text := resultText(res); res.IsError || !strings.Contains(text, "tracks alone (rms / peak dBFS):") || !strings.Contains(text, "measured at the master tap in") {
		t.Fatalf("report of the solo passes: %s", text)
	}
	if n := bassNotes() - before; n != 4 {
		t.Errorf("the bass played %d note(s) in the solo passes of one section, want its 4", n)
	}

	// A track's tap is gone: the tracks are measured by solo passes, and the
	// answer says why.
	r.mu.Lock()
	r.trackTaps[2].Disconnect()
	r.mu.Unlock()
	res = call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "solo_bars": 1, "whole": false, "sections": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "(3 solo pass(es))") ||
		!strings.Contains(text, "no tap is connected for pad, so the tracks are measured by solo passes at the master") {
		t.Fatalf("mix with a track's tap gone: %s", text)
	}
	if res := call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000}); res.IsError {
		t.Fatalf("report with a track's tap gone: %s", resultText(res))
	}
}

// selfSong is toolSong with drums that play no notes: the drum machine plays
// its own sequencer, as long as AUM's transport runs.
var selfSong = strings.ReplaceAll(toolSong, ", drums: beat}}", "}}")

// selfPlayingDrums makes the rig's drum machine (channel 3) play by itself
// while the transport runs: a sine of 0.1, -20 dBFS peak and -23 rms.
func selfPlayingDrums(r *songRig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SelfPlay = func(ch int) float64 {
		if ch == 3 {
			return 0.1
		}
		return 0
	}
}

// transportCCs lists the transport's and the sequencer bypass's messages the
// rig received, as controller*1000+value.
func (r *songRig) transportCCs() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for _, cmd := range r.Commands {
		if cmd.Type == "cc" && cmd.Channel == 15 {
			switch cmd.Controller {
			case rigStartCC, rigStopCC, rigRewindCC, rigBypassCC:
				out = append(out, cmd.Controller*1000+cmd.Value)
			}
		}
	}
	return out
}

// mixReportOf waits for the running measurement and returns its report.
func mixReportOf(t *testing.T, s *Server) (*mixReport, string) {
	t.Helper()
	res := call(t, s.handleGetSongMix, map[string]any{"wait_ms": 40000})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("report: %s", text)
	}
	s.mixMu.Lock()
	defer s.mixMu.Unlock()
	if s.mix.report == nil {
		t.Fatalf("no report: %s", text)
	}
	return s.mix.report, text
}

// TestMixSongTransport measures a song whose drums play no notes, made by an
// instrument that plays by itself while AUM's transport runs: every pass
// runs the transport with the session's sequencer bypassed, the drums are in
// the whole song's levels and have their row in every section, each solo
// pass has the other strips muted, and stop_song leaves the transport
// stopped at the top.
func TestMixSongTransport(t *testing.T) {
	r := mixRig(t)
	s := r.s
	stageSequencer(t)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": selfSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	r.connect()
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	cs, fail := s.loadSong("night_drive")
	if fail != nil {
		t.Fatal(resultText(fail))
	}
	r.strips(cs.plan)
	selfPlayingDrums(r)

	// Each section once per track, the drums and the pad where it plays
	// nothing included: nothing in the song says they are silent there.
	res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "solo_bars": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "(6 solo pass(es)), with AUM's transport running") {
		t.Fatalf("mix: %s", text)
	}
	rep, text := mixReportOf(t, s)
	cell := map[string]mixSolo{}
	for _, tr := range rep.Tracks {
		cell[tr.Section+"/"+tr.Track] = tr
	}
	for _, sec := range []string{"verse", "chorus"} {
		drums, ok := cell[sec+"/drums"]
		if !ok || drums.Notes != 0 || drums.Silent || math.Abs(drums.PeakDB+20) > 1 || math.Abs(drums.RMSDB+23) > 1.5 {
			t.Errorf("the drums alone in %s = %+v (measured %v), want their own sequencer's -23 rms and -20 peak\n%s", sec, drums, ok, text)
		}
		// The drum machine's strip is muted in the bass's pass: the bass's
		// peak is its own sine's.
		if bass := cell[sec+"/bass"]; math.Abs(bass.PeakDB+12) > 1 {
			t.Errorf("the bass alone in %s = %+v, want its own -12 dBFS peak (the drums muted)", sec, bass)
		}
	}
	if pad, ok := cell["verse/pad"]; ok {
		t.Errorf("a row for a track that neither plays notes nor sounds in the section: %+v", pad)
	}
	// The whole song carries the drums with the bass.
	for _, sec := range rep.Sections {
		if bass := cell[sec.Name+"/bass"]; sec.RMSDB < bass.RMSDB+1 {
			t.Errorf("the master in %s reads %.1f dBFS rms, the bass alone %.1f: the drums are missing", sec.Name, sec.RMSDB, bass.RMSDB)
		}
	}
	// Seven passes, each rewinding and starting the transport with the
	// sequencer bypassed, then stopping and rewinding it and putting the
	// sequencer back in line.
	pass := []int{106127, 104127, 102127, 103127, 104127, 106000}
	var want []int
	for range 7 {
		want = append(want, pass...)
	}
	if got := r.transportCCs(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the transport and the sequencer's bypass got %v, want %v for each of the 7 passes", got, pass)
	}
	r.mu.Lock()
	running, bypassed := r.Running, r.Bypassed
	r.mu.Unlock()
	if running || bypassed {
		t.Errorf("after the measurement the transport runs: %v, the sequencer is bypassed: %v", running, bypassed)
	}
	r.wantMuted("after the solo passes")

	// stop_song in the middle of a solo pass: the transport is stopped and
	// rewound, the sequencer back in line, the strips open again.
	before := len(r.transportCCs())
	if res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true, "whole": false, "sections": 1, "solo_bars": 1}); res.IsError {
		t.Fatalf("mix: %s", resultText(res))
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		running := r.Running
		r.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the transport was not started")
		}
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	if _, text := mixReportOf(t, s); !strings.Contains(text, "the measurement ended early: stopped") {
		t.Fatalf("report after stop_song: %s", text)
	}
	got := r.transportCCs()[before:]
	if n := len(got); n < 6 || fmt.Sprint(got[n-3:]) != fmt.Sprint([]int{103127, 104127, 106000}) || got[0] != 106127 {
		t.Errorf("a measurement stopped in a pass sent %v, want it to end with the stop, the rewind and the sequencer back in line", got)
	}
	r.mu.Lock()
	running, bypassed = r.Running, r.Bypassed
	r.mu.Unlock()
	if running || bypassed {
		t.Errorf("after stop_song the transport runs: %v, the sequencer is bypassed: %v", running, bypassed)
	}
	r.wantMuted("after stop_song")

	// transport: false keeps the transport stopped: the drums are not heard.
	before = len(r.transportCCs())
	res = call(t, s.handleMixSong, map[string]any{"song": "night_drive", "sections": 1, "transport": false})
	if text := resultText(res); res.IsError || strings.Contains(text, "transport running") {
		t.Fatalf("mix without the transport: %s", text)
	}
	rep, text = mixReportOf(t, s)
	if len(rep.Sections) != 1 || math.Abs(rep.Sections[0].PeakDB+12) > 1 {
		t.Errorf("without the transport the verse peaks at %+v, want the bass's -12 alone\n%s", rep.Sections, text)
	}
	if got := r.transportCCs()[before:]; len(got) != 0 {
		t.Errorf("a measurement without the transport sent %v", got)
	}
}

// TestMixSongTransportTrackTaps reads a self-playing drum machine at its own
// tap in the one pass, which runs the transport.
func TestMixSongTransportTrackTaps(t *testing.T) {
	r := mixRig(t)
	s := r.s
	r.Session = "Songs/Night Drive"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": selfSong + "session: {track_taps: true}\n"}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	r.connect()
	selfPlayingDrums(r)
	res := call(t, s.handleMixSong, map[string]any{"song": "night_drive", "per_track": true})
	if text := resultText(res); res.IsError || !strings.Contains(text, "in the same pass), with AUM's transport running") {
		t.Fatalf("mix: %s", text)
	}
	rep, text := mixReportOf(t, s)
	cell := map[string]mixSolo{}
	for _, tr := range rep.Tracks {
		cell[tr.Section+"/"+tr.Track] = tr
	}
	for _, sec := range []string{"verse", "chorus"} {
		drums, ok := cell[sec+"/drums"]
		if !ok || drums.Notes != 0 || drums.Silent || math.Abs(drums.PeakDB+20) > 1 || math.Abs(drums.RMSDB+23) > 1.5 {
			t.Errorf("the drums at their tap in %s = %+v (measured %v), want -23 rms and -20 peak\n%s", sec, drums, ok, text)
		}
	}
	if pad, ok := cell["verse/pad"]; ok {
		t.Errorf("a row for a track that neither plays notes nor sounds in the section: %+v", pad)
	}
	// One pass, the transport rewound, started, stopped and rewound; the
	// session has no sequencer to bypass.
	if got := r.transportCCs(); fmt.Sprint(got) != fmt.Sprint([]int{104127, 102127, 103127, 104127}) {
		t.Errorf("the transport got %v, want one pass's rewind, start, stop and rewind", got)
	}
}

// TestTapMeter checks the meter's arithmetic: levels of stretches of a
// stream read out in pieces.
func TestTapMeter(t *testing.T) {
	r := newSongRig(t)
	r.connect()
	r.mu.Lock()
	tap := r.tap
	r.mu.Unlock()
	m := newTapMeter(tap)
	// Half a second of silence, then a held note.
	time.Sleep(500 * time.Millisecond)
	m.pull()
	r.mu.Lock()
	r.notes[[2]int{1, 69}] = &rigNote{on: r.frame, off: -1}
	r.mu.Unlock()
	time.Sleep(600 * time.Millisecond)
	m.pull()
	if _, peak, ok := m.level(0, 0.4); !ok || peak > -100 {
		t.Errorf("the silent stretch peaks at %.1f dBFS (ok %v)", peak, ok)
	}
	// The rig's sine of 0.2: -14 dBFS peak, -17 rms.
	rms, peak, ok := m.level(0.7, 1.0)
	if !ok || math.Abs(peak+14) > 0.5 || math.Abs(rms+17) > 0.5 {
		t.Errorf("the note measures rms %.1f peak %.1f (ok %v), want -17 and -14", rms, peak, ok)
	}
	if pos := m.position(); pos < 1.0 || pos > 1.6 {
		t.Errorf("position = %.2f s after 1.1 s", pos)
	}
	if _, _, ok := m.level(50, 60); ok {
		t.Error("a stretch that was never metered has a level")
	}
}
