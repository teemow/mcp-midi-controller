package mcpserver

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

// largeSong is thirteen tracks, one more than a session hosts, in two stems:
// low (t1 to t7) and high (t8 to t13), under a master effect, at the stem
// song's tempo. extra adds lines to a track's declaration, by its position.
func largeSong(extra map[int]string) string {
	var b strings.Builder
	b.WriteString("title: \"Big Song\"\ntempo: 300\ntracks:\n")
	var play []string
	for i := range 13 {
		group, preset := "low", "Deep Bass"
		if i >= 7 {
			group, preset = "high", "Warm Pad"
		}
		fmt.Fprintf(&b, "  - id: t%d\n    group: %s\n    instrument: {plugin: syn1, preset: %s}\n%s", i+1, group, preset, extra[i])
		play = append(play, fmt.Sprintf("t%d: riff", i+1))
	}
	b.WriteString("master:\n  - {id: echo, plugin: dly1, params: {Mix: 0.2}}\n")
	b.WriteString("patterns:\n  riff: {step: 1/8, notes: \"A1 . A1 . A1 . G1 .\"}\n")
	b.WriteString("scenes:\n  open:\n    master: {effects: {echo: {params: {Mix: 0.4}}}}\n")
	fmt.Fprintf(&b, "sections:\n  verse: {bars: 1, play: {%s}}\n  chorus: {bars: 1, scene: open, play: {%s}}\narrangement: [verse, chorus]\n",
		strings.Join(play, ", "), strings.Join(play, ", "))
	return b.String()
}

// TestLargeSongTools takes a song of more tracks than one session hosts
// through the tools that need no iPad: it is saved, checked stem by stem and
// summarized with its stems; what would load, check, measure or play its own
// session refuses it and says what to do instead; its notes are exported.
func TestLargeSongTools(t *testing.T) {
	s := newSongServer(t)

	// What does not resolve, or meets on a channel, is reported with its
	// place in the whole document.
	for _, tc := range []struct{ name, doc, want string }{
		{"an unknown plugin in the second stem", strings.Replace(largeSong(nil), "  - id: t12\n    group: high\n    instrument: {plugin: syn1,", "  - id: t12\n    group: high\n    instrument: {plugin: organ,", 1),
			`/tracks/11/instrument/plugin: no staged AUv3 probe "organ"`},
		{"two tracks of a stem on one channel", largeSong(map[int]string{8: "    channel: 1\n"}), `/tracks/8/channel: MIDI channel 1 is already track "t8"'s in the stem "high"`},
	} {
		if res := call(t, s.handleSaveSong, map[string]any{"yaml": tc.doc}); !res.IsError || !strings.Contains(resultText(res), tc.want) {
			t.Errorf("%s: %s\nwant an error that says %q", tc.name, resultText(res), tc.want)
		}
	}
	// Seven tracks that stand alone beside the six of high are eight stems,
	// which fit; thirteen that all stand alone do not.
	alone := strings.ReplaceAll(largeSong(nil), "    group: low\n", "")
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": alone, "dry_run": true}); res.IsError || !strings.Contains(resultText(res), "13 tracks in 8 stems") {
		t.Errorf("thirteen tracks in eight stems: %s", resultText(res))
	}
	ungrouped := strings.ReplaceAll(strings.ReplaceAll(largeSong(nil), "    group: low\n", ""), "    group: high\n", "")
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": ungrouped}); !res.IsError || !strings.Contains(resultText(res), "/tracks: 13 tracks in 13 stems") || !strings.Contains(resultText(res), "12 stems fit at most") {
		t.Errorf("thirteen tracks in no group: %s", resultText(res))
	}
	if ids, _ := s.songs.List(); len(ids) != 0 {
		t.Fatalf("a refused song was stored: %v", ids)
	}

	// t9 names a channel of its own: it is counted in its stem.
	doc := largeSong(map[int]string{8: "    channel: 12\n"})
	res := call(t, s.handleSaveSong, map[string]any{"yaml": doc})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("save: %s", text)
	}
	for _, want := range []string{
		`saved "Big Song" (big_song): 300 BPM, 2 bars`,
		"13 tracks in 2 stems: more than the 12 one session hosts",
		"   1  verse        bars 1-1",
		"  low: 7 track(s)\n    ch1  t1         PolySynth  (8 notes)",
		"  high: 6 track(s)\n    ch1  t8         PolySynth  (8 notes)\n    ch12 t9 ",
		"  master: TapeDelay (in the mix:",
		"takes: 0 of the 2 stems have a take of the song as it is now\n",
		`next: record_song_stems {song: "big_song", confirm: true}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the summary lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "session:") {
		t.Errorf("the summary speaks of a session the song does not have:\n%s", text)
	}
	out, _ := res.StructuredContent.(map[string]any)
	sum, _ := out["summary"].(songSummary)
	if !sum.StemsOnly || len(sum.Tracks) != 13 || len(sum.Stems) != 2 || sum.Tracks[7].Channel != 1 || sum.Tracks[8].Channel != 12 || sum.Tracks[9].Channel != 3 ||
		sum.Tracks[7].Group != "high" || sum.Bars != 2 || sum.Takes == nil || sum.Takes.Ready || len(sum.Takes.Missing) != 2 || sum.Session != "" {
		t.Errorf("the structured summary = %+v (takes %+v)", sum, sum.Takes)
	}

	res = call(t, s.handleGetSong, map[string]any{"song": "big_song"})
	if text := resultText(res); res.IsError || !strings.Contains(text, "13 tracks in 2 stems") || !strings.Contains(text, "title: \"Big Song\"") {
		t.Errorf("get_song: %s", text)
	}
	res = call(t, s.handleListSongs, nil)
	if text := resultText(res); !strings.Contains(text, "13 track(s) in 2 stems (record_song_stems, mix_song_stems)") {
		t.Errorf("list_songs: %s", text)
	}

	// The song has no session of its own: nothing loads, checks, measures or
	// plays one, and each says what does play the song.
	for name, h := range map[string]func() string{
		"load_song": func() string {
			return resultText(call(t, s.handleLoadSong, map[string]any{"song": "big_song", "confirm": true}))
		},
		"play_song":  func() string { return resultText(call(t, s.handlePlaySong, map[string]any{"song": "big_song"})) },
		"check_song": func() string { return resultText(call(t, s.handleCheckSong, map[string]any{"song": "big_song"})) },
		"mix_song":   func() string { return resultText(call(t, s.handleMixSong, map[string]any{"song": "big_song"})) },
		"author_song_session": func() string {
			return resultText(call(t, s.handleAuthorSongSession, map[string]any{"song": "big_song"}))
		},
		"audition_sound": func() string {
			return resultText(call(t, s.handleAuditionSound, map[string]any{"song": "big_song", "track": "t1"}))
		},
	} {
		text := h()
		for _, want := range []string{
			`song "big_song" has 13 tracks, more than the 12 one session hosts`,
			`record_song_stems {song: "big_song", confirm: true}`,
			`mix_song_stems {song: "big_song", confirm: true}`,
			`take "big_song_mix"`,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s of a song without a session of its own lacks %q: %s", name, want, text)
			}
		}
	}
	if recs := loadSongSessions(); len(recs) != 0 {
		t.Errorf("a session was authored for the song: %+v", recs)
	}

	// The notes need no session: a track per song track, and no control track.
	res = call(t, s.handleExportSongMIDI, map[string]any{"song": "big_song"})
	if text := resultText(res); res.IsError || !strings.Contains(text, "13 track(s), 104 notes, 2 bars at 300 BPM") || !strings.Contains(text, "the notes alone") {
		t.Fatalf("export: %s", text)
	}
	data, err := os.ReadFile(filepath.Join(config.SongExportsDir(), "big_song.mid"))
	if err != nil || len(data) < 14 || string(data[:4]) != "MThd" {
		t.Fatalf("exported file: %v, %d bytes", err, len(data))
	}
	if chunks := binary.BigEndian.Uint16(data[10:12]); chunks != 14 {
		t.Errorf("the exported file has %d track chunks, want the conductor's and one per track (14)", chunks)
	}

	// Taken apart, every stem is a song of one session with channels of its
	// own, and the whole song is planned nowhere.
	plan, err := s.planStems("big_song", nil, 0)
	if err != nil {
		t.Fatalf("planStems: %v", err)
	}
	if len(plan.songs) != 2 || len(plan.songs[0].plan.Tracks) != 7 || plan.songs[1].plan.Tracks[0].Channel != 1 || plan.songs[1].plan.Tracks[1].Channel != 12 ||
		plan.songs[1].plan.ControlChannels[0] != 13 || plan.songs[0].plan.ControlChannels[0] != 8 {
		t.Errorf("the stems' plans: low %d tracks, controls from %d; high's channels %d, %d, controls from %d", len(plan.songs[0].plan.Tracks), plan.songs[0].plan.ControlChannels[0],
			plan.songs[1].plan.Tracks[0].Channel, plan.songs[1].plan.Tracks[1].Channel, plan.songs[1].plan.ControlChannels[0])
	}
	if _, err := s.compileSong("big_song", plan.src); err == nil || !strings.Contains(err.Error(), "no session of its own") {
		t.Errorf("the whole song compiled as one session: err = %v", err)
	}
	// A grouping given to record_song_stems is held to the same sizes.
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "big_song", "confirm": true, "groups": map[string][]string{"all": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12", "t13"}}}); !res.IsError ||
		!strings.Contains(resultText(res), `/groups: the stem "all" has 13 tracks`) {
		t.Errorf("record_song_stems with one group of thirteen: %s", resultText(res))
	}

	// A song of twelve tracks is one session, as it always was.
	twelve := strings.Replace(largeSong(nil), "  - id: t13\n    group: high\n    instrument: {plugin: syn1, preset: Warm Pad}\n", "", 1)
	twelve = strings.ReplaceAll(strings.Replace(twelve, "Big Song", "Twelve", 1), ", t13: riff", "")
	res = call(t, s.handleSaveSong, map[string]any{"yaml": twelve})
	if text := resultText(res); res.IsError || !strings.Contains(text, "tracks:\n  ch1  t1 ") || !strings.Contains(text, "  ch12 t12 ") ||
		!strings.Contains(text, "session: not authored yet (load_song)") || strings.Contains(text, "no session of its own") {
		t.Errorf("a song of twelve tracks: %s", text)
	}
}

// TestRecordLargeSongStems records a song of thirteen tracks with the rig's
// recorder and mixes it: each stem from a session of its own whose channels
// are the stem's, the mix a song of two file tracks that loads and plays.
func TestRecordLargeSongStems(t *testing.T) {
	r, deck := stemsRigWith(t, largeSong(nil), "Songs/Big Song stem low.aumproj", "Songs/Big Song stem high.aumproj", "Songs/Big Song mix.aumproj")
	s := r.s
	strips := func(titles ...string) []ipadtest.Strip {
		out := []ipadtest.Strip{{Title: "Brain"}}
		for _, title := range titles {
			out = append(out, ipadtest.Strip{Title: title, Level: "+0.0 dB"})
		}
		return append(out, ipadtest.Strip{Title: "Master", Level: "+0.0 dB"})
	}
	low, high := strips("t1", "t2", "t3", "t4", "t5", "t6", "t7"), strips("t8", "t9", "t10", "t11", "t12", "t13")

	r.opens("Big Song stem low", low)
	deck.afterTake = func(folder string) {
		if folder == "Big Song stem low" {
			r.opens("Big Song stem high", high)
		}
	}
	res := call(t, s.handleRecordSongStems, map[string]any{"song": "big_song", "confirm": true, "tail_bars": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "recording 2 of the 2 stem(s)") || !strings.Contains(text, "low, high") {
		t.Fatalf("record: %s", text)
	}
	rep, text := stemsReportOfJob(t, s)
	if !rep.Ready || len(rep.Stems) != 2 || rep.Stems[0].File != "Big Song stem low/Master - 300 bpm - 001.wav" || rep.Stems[1].File != "Big Song stem high/Master - 300 bpm - 001.wav" ||
		rep.Stems[1].Session != "Songs/Big Song stem high" || rep.Stems[0].Error != "" || len(rep.Stems[1].Tracks) != 6 {
		t.Fatalf("report = %+v\n%s", rep, text)
	}

	// The second stem's session hosts its six tracks, the first of them on
	// MIDI channel 1 as the first stem's first is.
	data, err := os.ReadFile(stagedSessionPath("Songs/Big Song stem high"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if chans := sess.Channels(); len(chans) != 8 || chans[1].Title != "t8" || chans[6].Title != "t13" {
		t.Errorf("the high stem's session has %d strips, want brain, six tracks and the master", len(chans))
	}
	recs := loadSongSessions()
	if recs["big_song/stem/high"].Sequence == "" || recs["big_song/stem/low"].Session != "Songs/Big Song stem low" {
		t.Errorf("the stems' session records = %+v", recs)
	}
	if _, whole := recs["big_song"]; whole {
		t.Errorf("the whole song was authored into a session: %+v", recs["big_song"])
	}

	// The song's summary says the takes are there.
	if text := resultText(call(t, s.handleGetSong, map[string]any{"song": "big_song"})); !strings.Contains(text, "takes: 2 of the 2 stems have a take of the song as it is now\n") ||
		!strings.Contains(text, `next: mix_song_stems {song: "big_song", confirm: true}`) {
		t.Errorf("get_song after the stems are recorded: %s", text)
	}

	// The mix: a song of one session, written, loaded and played.
	r.opens("Big Song mix", strips("low", "high"))
	res = call(t, s.handleMixSongStems, map[string]any{"song": "big_song", "confirm": true, "record": false})
	text = resultText(res)
	if res.IsError {
		t.Fatalf("mix: %s", text)
	}
	for _, want := range []string{
		`wrote the mix of "Big Song" as the song "big_song_mix": 2 file track(s) under 1 master effect(s)`,
		`"Big Song mix" is loaded in AUM as Songs/Big Song mix`,
		`low        file "Big Song stem low/Master - 300 bpm - 001.wav": 191993 frames, 0:04.000, 19.999 beats (whole beats)`,
		`high       file "Big Song stem high/Master - 300 bpm - 001.wav": `,
		"the session's sequencer plays",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the mix's answer lacks %q:\n%s", want, text)
		}
	}
	if st := s.player.Status(); !st.Playing || st.Song != "big_song_mix" || st.Mode != song.ModeSequenced {
		t.Errorf("playback after the mix = %+v, want the mix from its sequencer", st)
	}
	s.player.Stop()
	mix, _, err := s.songs.Load("big_song_mix")
	if err != nil || len(mix.Tracks) != 2 || mix.StemsOnly() || !mix.Tracks[0].IsFile() || len(mix.Master) != 1 {
		t.Errorf("the stored mix = %+v, %v", mix, err)
	}
	if text := resultText(call(t, s.handleGetSong, map[string]any{"song": "big_song"})); !strings.Contains(text, `mix: the song "big_song_mix"`) {
		t.Errorf("get_song after the mix: %s", text)
	}

	// A change to one track: its stem's take is of the song as it was.
	edited := strings.Replace(largeSong(nil), "  - id: t2\n    group: low\n    instrument: {plugin: syn1, preset: Deep Bass}", "  - id: t2\n    group: low\n    instrument: {plugin: syn1, preset: Bright Pad}", 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": edited})
	if text := resultText(res); res.IsError || !strings.Contains(text, "takes: 1 of the 2 stems have a take of the song as it is now; changed since their take: low\n") {
		t.Errorf("save after a change to one stem: %s", text)
	}
}
