package mcpserver

import (
	"math"
	"strings"
	"testing"
	"time"
)

// fastMix shortens the waits of a mix measurement.
func fastMix(t *testing.T) {
	t.Helper()
	old := []time.Duration{mixTail, mixBetween, mixPull}
	mixTail, mixBetween, mixPull = 150*time.Millisecond, 60*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { mixTail, mixBetween, mixPull = old[0], old[1], old[2] })
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
	if res.IsError || !strings.Contains(resultText(res), `measuring the mix of "Night Drive"`) || !strings.Contains(resultText(res), "5 solo pass(es)") {
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
	r.notes[[2]int{1, 69}] = true
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
