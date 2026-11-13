package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/mcp-midi-controller/internal/ipad"
	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

// stemSong is three tracks in two stems under a master effect, at a tempo
// whose bar is 0.8 s: a lead-in of two bars, two bars of song.
const stemSong = `title: "Stem Song"
tempo: 300
tracks:
  - id: bass
    instrument: {plugin: syn1, preset: Deep Bass}
  - id: pad
    group: band
    level: -3
    instrument: {plugin: syn1, preset: Warm Pad}
  - id: drums
    group: band
    kind: drums
    instrument: {plugin: drm1}
master:
  - {id: echo, plugin: dly1, params: {Mix: 0.2}}
patterns:
  bass: {step: 1/8, notes: "A1 . A1 . A1 . G1 ."}
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
scenes:
  open:
    pad: {level: 0}
    master: {effects: {echo: {params: {Mix: 0.4}}}}
sections:
  verse: {bars: 1, play: {bass: bass, drums: beat}}
  chorus: {bars: 1, scene: open, play: {bass: bass, pad: chords, drums: beat}}
arrangement: [verse, chorus]
`

// rigDeck is AUM's screen as the stems tools read it, over the rig: the
// recordings its recorder made, the master's REC button, and the File Players
// of the session it has open.
type rigDeck struct {
	r *songRig

	mu   sync.Mutex
	seen map[string]int
	// afterTake is called when a folder's list has grown since it was last
	// read: the daemon has found a take and loads the next session.
	afterTake func(folder string)
	// slow is how long a reading of a list takes.
	slow time.Duration
	// sorts are the sorts AUM's list shows at the readings, in turn, the last
	// one repeating; none is ipadtest.SortLastOpened, the newest take first.
	sorts []string
	// older are the takes each folder held before the rig recorded, the
	// newest first, and others the root's folders of other sessions.
	older  map[string][]string
	others []string
	// readings lists the readings, in order: "top <folder>" for a folder's
	// first page, "whole <folder>" for all of it, "root <folder>" for a root
	// read through without the folder.
	readings []string
	// armTaps counts the taps on REC the daemon needed.
	armTaps int
	// missing are the strips whose File Player came up without its file.
	missing map[string]bool
}

// rigTopRows is how many rows the first page of a folder of AUM's
// Recordings shows.
const rigTopRows = 12

func (d *rigDeck) Recordings(ctx context.Context, folder string) (stemList, error) {
	d.mu.Lock()
	slow := d.slow
	sort := ipadtest.SortLastOpened
	if n := len(d.sorts); n > 0 {
		sort, d.sorts = d.sorts[0], d.sorts[min(1, n-1):]
	}
	older := slices.Clone(d.older[folder])
	others := slices.Clone(d.others)
	d.mu.Unlock()
	if slow > 0 {
		select {
		case <-time.After(slow):
		case <-ctx.Done():
			return stemList{}, ctx.Err()
		}
	}
	d.r.mu.Lock()
	takes := slices.Clone(d.r.Takes[folder])
	var root []string
	for f := range d.r.Takes {
		if len(d.r.Takes[f]) > 0 {
			root = append(root, f)
		}
	}
	d.r.mu.Unlock()
	for f := range d.older {
		if !slices.Contains(root, f) {
			root = append(root, f)
		}
	}
	slices.Sort(root)
	var out stemList
	var names []string
	for i := len(takes) - 1; i >= 0; i-- { // the newest first, as AUM lists them
		names = append(names, takes[i].Name)
	}
	names = append(names, older...)
	switch {
	case len(names) == 0:
		out = stemList{Missing: true, Root: append(others, root...)}
	case sort == ipadtest.SortLastOpened:
		out = stemList{Top: true, Sort: sort}
		names = names[:min(len(names), rigTopRows)]
	default:
		// Sorted another way the takes are in no order of theirs: by name.
		out = stemList{Sort: sort}
		slices.Sort(names)
	}
	for _, n := range names {
		out.Takes = append(out.Takes, ipad.AUMFileEntry{Name: n, Detail: "12. Nov 2026 at 21:23:42 • 4.4 MiB • wav"})
	}
	d.mu.Lock()
	switch {
	case out.Missing:
		d.readings = append(d.readings, "root "+folder)
	case out.Top:
		d.readings = append(d.readings, "top "+folder)
	default:
		d.readings = append(d.readings, "whole "+folder)
	}
	had, read := d.seen[folder]
	if d.seen == nil {
		d.seen = map[string]int{}
	}
	d.seen[folder] = len(takes)
	next := d.afterTake
	d.mu.Unlock()
	if read && len(takes) > had && next != nil {
		next(folder)
	}
	return out, nil
}

// readingsOf lists the deck's readings since the n-th.
func (d *rigDeck) readingsOf(n int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.readings[min(n, len(d.readings)):])
}

func (d *rigDeck) Arm(_ context.Context, on bool) (bool, error) {
	d.r.mu.Lock()
	defer d.r.mu.Unlock()
	if d.r.Armed == on {
		return false, nil
	}
	d.r.Armed = on
	d.mu.Lock()
	d.armTaps++
	d.mu.Unlock()
	return true, nil
}

func (d *rigDeck) FilePlayer(_ context.Context, strip string) (filePlayerView, error) {
	d.r.mu.Lock()
	session := d.r.Session
	takes := d.r.Takes
	d.r.mu.Unlock()
	d.mu.Lock()
	gone := d.missing[strip]
	d.mu.Unlock()
	file := strings.TrimPrefix(stagedFilePlayers(d.r.t, session)[strip], song.RecordingsFolder+"/")
	folder, name := path.Split(file)
	for _, take := range takes[strings.TrimSuffix(folder, "/")] {
		if !gone && take.Name+".wav" == name {
			return filePlayerView{File: name, Frames: take.Frames, Rate: rigSampleRate, Format: "WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz", Loop: "OFF", Sync: "OFF", Text: name}, nil
		}
	}
	return filePlayerView{}, nil
}

// stagedFilePlayers reads the files a staged session's File Players are saved
// with, by strip title.
func stagedFilePlayers(t *testing.T, id string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(stagedSessionPath(id))
	if err != nil {
		t.Fatalf("the rig's session %q is not staged: %v", id, err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatalf("open the staged %q: %v", id, err)
	}
	a := sess.Archive()
	objects := func(v any) []any {
		arr, _ := a.Deref(v).(map[string]any)
		objs, _ := arr["NS.objects"].([]any)
		return objs
	}
	root, _ := a.Root().(map[string]any)
	strips := objects(root["nodeArchives"])
	out := map[string]string{}
	for i, ch := range sess.Channels() {
		if i >= len(strips) || len(objects(strips[i])) == 0 {
			continue
		}
		node, _ := a.Deref(objects(strips[i])[0]).(map[string]any)
		state, _ := a.Deref(node["archiveNodeState"]).(map[string]any)
		keys, _ := state["NS.keys"].([]any)
		vals, _ := state["NS.objects"].([]any)
		for k := range keys {
			if name, _ := a.Deref(keys[k]).(string); name == "FilePlayerPath" && k < len(vals) {
				out[ch.Title], _ = a.Deref(vals[k]).(string)
			}
		}
	}
	return out
}

// stemsRig is a rig with the stem song stored, the sequencer staged, AUM's
// recorder and screen stood in for, and the waits of a take cut short.
func stemsRig(t *testing.T) (*songRig, *rigDeck) {
	t.Helper()
	return stemsRigWith(t, stemSong, "Songs/Stem Song stem bass.aumproj", "Songs/Stem Song stem band.aumproj", "Songs/Stem Song mix.aumproj")
}

// stemsRigWith is stemsRig for another song at the stem song's tempo: doc is
// stored, and the rig's iPad pulls the staged files when they are offered.
func stemsRigWith(t *testing.T, doc string, files ...string) (*songRig, *rigDeck) {
	t.Helper()
	r := newSongRig(t)
	stageSequencer(t)
	arm, margin, wait, poll, off := stemArmSettle, stemEndMargin, stemListWait, stemListPoll, stemOffAfter
	stemArmSettle, stemEndMargin, stemListWait, stemListPoll, stemOffAfter = 10*time.Millisecond, 30*time.Millisecond, 2*time.Second, 20*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() {
		stemArmSettle, stemEndMargin, stemListWait, stemListPoll, stemOffAfter = arm, margin, wait, poll, off
	})
	// A bar is 0.8 s. A stem's sequence is a lead-in of two bars, two bars of
	// song and a tail of one, and switches the recorder off half a beat (0.1
	// s) before its end.
	r.Bar, r.TakeTempo, r.ClipOff = 800*time.Millisecond, "300", 3900*time.Millisecond
	// Something sounds while the transport runs, as a stem does.
	r.SelfPlay = func(ch int) float64 {
		if ch == 1 {
			return 0.1
		}
		return 0
	}
	deck := &rigDeck{r: r, older: map[string][]string{}}
	r.s.deck = deck
	if res := call(t, r.s.handleSaveSong, map[string]any{"yaml": doc}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	for _, file := range files {
		r.pullStaged(file)
	}
	return r, deck
}

// The mixers of the stem song's sessions.
var (
	stemBassStrips = []ipadtest.Strip{{Title: "Brain"}, {Title: "bass", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"}}
	stemBandStrips = []ipadtest.Strip{{Title: "Brain"}, {Title: "pad", Level: "-3.0 dB"}, {Title: "drums", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"}}
	stemMixStrips  = []ipadtest.Strip{{Title: "Brain"}, {Title: "bass", Level: "+0.0 dB"}, {Title: "band", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"}}
)

// opens has the rig's AUM load a session the next time the daemon asks.
func (r *songRig) opens(name string, strips []ipadtest.Strip) {
	r.mu.Lock()
	r.Session = "Songs/" + name
	r.mu.Unlock()
	r.aumLoads("Songs", name, strips)
}

// stemsReportOfJob waits for the running stem recording and returns its
// report.
func stemsReportOfJob(t *testing.T, s *Server) (*stemsReport, string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		res := call(t, s.handleGetSongStems, map[string]any{"wait_ms": 2000})
		out, _ := res.StructuredContent.(map[string]any)
		if done, _ := out["done"].(bool); done {
			rep, _ := out["report"].(*stemsReport)
			if rep == nil {
				t.Fatalf("the stem recording has no report: %s", resultText(res))
			}
			return rep, resultText(res)
		}
		if res.IsError || time.Now().After(deadline) {
			t.Fatalf("the stem recording did not finish: %s", resultText(res))
		}
	}
}

// globalCCs lists the rig's controls on the global channel among the
// commands it got, as controller*1000+value, for the controllers given.
func globalCCs(cmds []midicontrol.Command, controllers ...int) []int {
	var out []int
	for _, cmd := range cmds {
		if cmd.Type == "cc" && cmd.Channel == song.GlobalChannel && slices.Contains(controllers, cmd.Controller) {
			out = append(out, cmd.Controller*1000+cmd.Value)
		}
	}
	return out
}

// TestRecordSongStems records a song's two stems with the rig's recorder and
// mixes them: each stem from a session of its own with the master armed, the
// take found in AUM's list, the mix written as a song of file tracks, loaded
// and played, and the takes' lengths read from its File Players.
func TestRecordSongStems(t *testing.T) {
	r, deck := stemsRig(t)
	s := r.s

	if res := call(t, s.handleGetSong, map[string]any{"song": "stem_song"}); !strings.Contains(resultText(res), "stems (record_song_stems") || !strings.Contains(resultText(res), "band       pad, drums") {
		t.Fatalf("the song's summary does not list its stems: %s", resultText(res))
	}
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song"}); !res.IsError || !strings.Contains(resultText(res), "needs confirm: true") {
		t.Fatalf("a stem recording without confirm was accepted: %s", resultText(res))
	}
	if res := call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true}); !res.IsError || !strings.Contains(resultText(res), "no stems are kept") {
		t.Fatalf("a mix without stems: %s", resultText(res))
	}

	// The first stem's session is loaded when the tool asks; the second when
	// the first take has been found.
	r.opens("Stem Song stem bass", stemBassStrips)
	deck.afterTake = func(folder string) {
		if folder == "Stem Song stem bass" {
			r.opens("Stem Song stem band", stemBandStrips)
		}
	}
	res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "recording 2 of the 2 stem(s)") || !strings.Contains(text, "bass, band") {
		t.Fatalf("record: %s", text)
	}
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true}); !res.IsError || !strings.Contains(resultText(res), "are being recorded") {
		t.Fatalf("a second recording beside the first was accepted: %s", resultText(res))
	}
	// Nothing else loads a session or plays meanwhile.
	for name, h := range map[string]func() string{
		"play_song": func() string { return resultText(call(t, s.handlePlaySong, map[string]any{"song": "stem_song"})) },
		"load_song": func() string {
			return resultText(call(t, s.handleLoadSong, map[string]any{"song": "stem_song", "confirm": true}))
		},
		"check_song": func() string { return resultText(call(t, s.handleCheckSong, map[string]any{"song": "stem_song"})) },
		"mix_song":   func() string { return resultText(call(t, s.handleMixSong, map[string]any{"song": "stem_song"})) },
		"mix_song_stems": func() string {
			return resultText(call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true}))
		},
		"open_sound_lab": func() string {
			return resultText(call(t, s.handleOpenSoundLab, map[string]any{"plugins": []string{"syn1"}, "confirm": true}))
		},
		"audition_sound": func() string {
			return resultText(call(t, s.handleAuditionSound, map[string]any{"song": "stem_song", "track": "bass"}))
		},
	} {
		if text := h(); !strings.Contains(text, "are being recorded") {
			t.Errorf("%s while the stems are recorded: %s", name, text)
		}
	}
	rep, text := stemsReportOfJob(t, s)
	if !rep.Ready || len(rep.Stems) != 2 || rep.Layout != (song.StemLayout{LeadBars: 2, TailBars: 1}) || rep.ExpectedBeats != 20 {
		t.Fatalf("report = %+v\n%s", rep, text)
	}
	// Neither stem had a folder in AUM's Recordings: the root was read
	// through once, for the bass, and that answered for the band; each take
	// was found on its folder's first page, the newest take first.
	if got, want := deck.readingsOf(0), []string{"root Stem Song stem bass", "top Stem Song stem bass", "top Stem Song stem band"}; !slices.Equal(got, want) {
		t.Errorf("AUM's recordings were read as %q, want %q", got, want)
	}
	for i, want := range []struct{ stem, file, session string }{
		{"bass", "Stem Song stem bass/Master - 300 bpm - 001.wav", "Songs/Stem Song stem bass"},
		{"band", "Stem Song stem band/Master - 300 bpm - 001.wav", "Songs/Stem Song stem band"},
	} {
		take := rep.Stems[i]
		if take.Stem != want.stem || take.File != want.file || take.Session != want.session || take.Error != "" || take.Kept {
			t.Errorf("take %d = %+v, want %+v", i, take, want)
		}
		if !strings.Contains(take.Listed, "MiB") || take.Fingerprint == "" || take.PeakDB == nil || *take.PeakDB < -30 {
			t.Errorf("take %d = %+v, want what AUM lists of it, its fingerprint and the level the tap heard", i, take)
		}
	}
	for _, want := range []string{"Stem Song stem band/Master - 300 bpm - 001.wav", "its length is read from its File Player when the mix is loaded", "next: mix_song_stems"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	if deck.armTaps != 0 {
		t.Errorf("the master was armed and disarmed with %d tap(s): its mapped control did not do it", deck.armTaps)
	}

	// What the rig was sent, per stem: the recorder off before anything (one
	// left on), arm, recorder on, the transport's stop, disarm. The off that
	// ends the take is the sequence's own: the daemon sends none meanwhile, a
	// second one would withdraw it.
	one := []int{105000, 107127, 105127, 103127, 107000}
	cmds := r.waitCommands(func(cmds []midicontrol.Command) bool { return len(globalCCs(cmds, 103, 105, 107)) >= 2*len(one) })
	if got := globalCCs(cmds, 103, 105, 107); !slices.Equal(got, append(slices.Clone(one), one...)) {
		t.Errorf("the recorder's controls = %v, want twice %v", got, one)
	}

	// A stem's session hosts its tracks alone, and nothing on the master.
	data, err := os.ReadFile(stagedSessionPath("Songs/Stem Song stem band"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := aum.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	chans := sess.Channels()
	if len(chans) != 4 || chans[1].Title != "pad" || chans[2].Title != "drums" || len(chans[3].Nodes) != 3 {
		t.Errorf("the band's session has %d strips (%+v), want brain, pad, drums and a master of source, output and tap", len(chans), chans)
	}
	if stemsIDs := loadSongSessions(); stemsIDs["stem_song/stem/band"].Options.TrackTaps || stemsIDs["stem_song/stem/band"].Sequence == "" {
		t.Errorf("the band's session record = %+v, want the sequencer and no track taps", stemsIDs["stem_song/stem/band"])
	}

	// Nothing changed: there is nothing to record.
	res = call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "every one of the 2 stem(s)") {
		t.Fatalf("record again: %s", text)
	}
	rep, text = stemsReportOfJob(t, s)
	if !rep.Ready || !rep.Stems[0].Kept || !strings.Contains(text, "recorded before") {
		t.Fatalf("report of a run with nothing to do = %+v\n%s", rep, text)
	}

	// The mix: written, loaded, its File Players read, played.
	r.opens("Stem Song mix", stemMixStrips)
	res = call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true, "record": false})
	text = resultText(res)
	if res.IsError {
		t.Fatalf("mix: %s", text)
	}
	for _, want := range []string{
		`wrote the mix of "Stem Song" as the song "stem_song_mix": 2 file track(s) under 1 master effect(s)`,
		`"Stem Song mix" is loaded in AUM as Songs/Stem Song mix`,
		"sound   ok    no track has a note to answer",
		"files   ok    every File Player has its file\n",
		`bass       file "Stem Song stem bass/Master - 300 bpm - 001.wav": 191993 frames, 0:04.000, 19.999 beats (whole beats)`,
		"the session's sequencer plays",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the mix's answer lacks %q:\n%s", want, text)
		}
	}
	if st := s.player.Status(); !st.Playing || st.Song != "stem_song_mix" || st.Mode != song.ModeSequenced {
		t.Errorf("playback after the mix = %+v, want the mix from its sequencer", st)
	}
	s.player.Stop()

	mix, raw, err := s.songs.Load("stem_song_mix")
	if err != nil {
		t.Fatalf("the mix is not stored: %v", err)
	}
	if len(mix.Tracks) != 2 || !mix.Tracks[1].IsFile() || mix.Tracks[1].ID != "band" || len(mix.Master) != 1 || mix.Session.LeadBars != 2 ||
		mix.Arrangement[len(mix.Arrangement)-1] != "stem_tail" || !strings.HasPrefix(string(raw), "# The mix of") {
		t.Errorf("the stored mix:\n%s", raw)
	}
	kept := loadSongStems()["stem_song"]
	if len(kept.Stems) != 2 || kept.Stems[0].Frames != 191993 || kept.Stems[0].Whole == nil || !*kept.Stems[0].Whole || kept.Stems[1].Format == "" {
		t.Errorf("the kept takes after the mix = %+v, want their lengths", kept.Stems)
	}
	if res := call(t, s.handleGetSongStems, map[string]any{"song": "stem_song"}); !strings.Contains(resultText(res), "191993 frames, 0:04.000, 19.999 beats: whole beats long") {
		t.Errorf("get_song_stems after the mix: %s", resultText(res))
	}

	// The mix is a song: its summary says what plays, and it plays whole,
	// from the top, from the sequencer.
	if res := call(t, s.handleGetSong, map[string]any{"song": "stem_song_mix"}); !strings.Contains(resultText(res), `plays "Stem Song stem band/Master - 300 bpm - 001.wav" from AUM's Recordings`) ||
		!strings.Contains(resultText(res), "a lead-in of 2 bar(s)") {
		t.Errorf("the mix's summary: %s", resultText(res))
	}
	for _, args := range []map[string]any{{"mode": "live"}, {"from": "chorus"}, {"tracks": []string{"bass"}}} {
		args["song"] = "stem_song_mix"
		if res := call(t, s.handlePlaySong, args); !res.IsError || !strings.Contains(resultText(res), "plays a file") {
			t.Errorf("play_song %v of a song of files: %s", args, resultText(res))
		}
	}
	if res := call(t, s.handleMixSong, map[string]any{"song": "stem_song_mix", "from": "chorus"}); !res.IsError || !strings.Contains(resultText(res), "only be measured from the top") {
		t.Errorf("mix_song from a section of a song of files: %s", resultText(res))
	}
	if res := call(t, s.handleMixSong, map[string]any{"song": "stem_song_mix", "per_track": true}); !res.IsError || !strings.Contains(resultText(res), "track_taps: true") {
		t.Errorf("mix_song per track of a song of files without track taps: %s", resultText(res))
	}

	// A File Player that came up without its file: the mix is not played.
	deck.mu.Lock()
	deck.missing = map[string]bool{"band": true}
	deck.mu.Unlock()
	r.opens("Stem Song mix", stemMixStrips)
	res = call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true, "record": false})
	if text := resultText(res); !res.IsError || !strings.Contains(text, "NOT ready to play") || !strings.Contains(text, `band wants "Stem Song stem band/Master - 300 bpm - 001.wav" and its File Player shows no file`) ||
		!strings.Contains(text, "NOT LOADED") || s.player.Status().Playing {
		t.Errorf("a mix with a File Player without its file: %s", text)
	}
	deck.mu.Lock()
	deck.missing = nil
	deck.mu.Unlock()

	// A changed stem: its take is of the song as it was, and the mix refuses
	// it until it is recorded again; only that stem is.
	edited := strings.Replace(stemSong, `"A1 . A1 . A1 . G1 ."`, `"A1 A1 A1 . E1 . G1 ."`, 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": edited}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	if res := call(t, s.handleGetSongStems, map[string]any{"song": "stem_song"}); !strings.Contains(resultText(res), "the stem has changed since this take") {
		t.Errorf("get_song_stems after a change: %s", resultText(res))
	}
	if res := call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true}); !res.IsError || !strings.Contains(resultText(res), "the stem(s) bass of \"stem_song\" have changed since their takes") {
		t.Errorf("a mix from a stale take: %s", resultText(res))
	}
	deck.afterTake = nil
	r.opens("Stem Song stem bass", stemBassStrips)
	res = call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1})
	if text := resultText(res); res.IsError || !strings.Contains(text, "recording 1 of the 2 stem(s)") {
		t.Fatalf("record after a change: %s", text)
	}
	rep, text = stemsReportOfJob(t, s)
	if !rep.Ready || rep.Stems[0].File != "Stem Song stem bass/Master - 300 bpm - 002.wav" || rep.Stems[0].Kept || !rep.Stems[1].Kept {
		t.Errorf("report after the change = %+v\n%s", rep, text)
	}
}

// TestRecordSongStemsFailures covers what keeps a stem from being recorded:
// a grouping that does not fit the song, a master that will not arm by its
// mapping, a recorder that makes no take, and a recording that is stopped.
func TestRecordSongStemsFailures(t *testing.T) {
	r, deck := stemsRig(t)
	s := r.s
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"groups": map[string]any{"a": []string{"nope"}}}, `no track "nope"`},
		{map[string]any{"stems": []string{"nope"}}, `the song has no stem "nope" (bass, band)`},
		{map[string]any{"tail_bars": 99}, "/tail_bars"},
		{map[string]any{"song": "nope"}, "/song"},
	} {
		args := map[string]any{"song": "stem_song", "confirm": true}
		for k, v := range tc.args {
			args[k] = v
		}
		if res := call(t, s.handleRecordSongStems, args); !res.IsError || !strings.Contains(resultText(res), tc.want) {
			t.Errorf("record %v: %s (want %q)", tc.args, resultText(res), tc.want)
		}
	}

	// The recorder makes no take (the master does not stay armed): the stem
	// is reported as not recorded, and the recording ends there.
	r.opens("Stem Song stem bass", stemBassStrips)
	r.mu.Lock()
	r.Bar = 0
	r.mu.Unlock()
	disarm := make(chan struct{})
	go func() {
		// Disarm the master once the recorder runs: the take is then not kept.
		for {
			select {
			case <-disarm:
				return
			case <-time.After(5 * time.Millisecond):
			}
			r.mu.Lock()
			if r.Recording {
				r.Armed = false
			}
			r.mu.Unlock()
		}
	}()
	res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1, "stems": []string{"bass"}})
	if res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	deadline := time.Now().Add(60 * time.Second)
	var text string
	for {
		res = call(t, s.handleGetSongStems, map[string]any{"wait_ms": 2000})
		text = resultText(res)
		if out, _ := res.StructuredContent.(map[string]any); out["done"] == true || res.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the recording did not end: %s", text)
		}
	}
	close(disarm)
	if !res.IsError || !strings.Contains(text, `NOT RECORDED: AUM lists no new recording under "Stem Song stem bass"`) || !strings.Contains(text, "the recording ended at the stem bass") {
		t.Errorf("a pass without a take: %s", text)
	}
	if _, kept := loadSongStems()["stem_song"]; kept {
		t.Error("a stem without a take was noted down")
	}

	// Stopped: the pass ends, the recorder is switched off and the master
	// disarmed.
	r.mu.Lock()
	r.Bar = 800 * time.Millisecond
	r.mu.Unlock()
	_ = deck
	r.opens("Stem Song stem bass", stemBassStrips)
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1, "stems": []string{"bass"}}); res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		r.mu.Lock()
		recording := r.Recording
		r.mu.Unlock()
		if recording {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the recorder did not start")
		}
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		res = call(t, s.handleGetSongStems, map[string]any{})
		if out, _ := res.StructuredContent.(map[string]any); out["done"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stopped recording did not end: %s", resultText(res))
		}
	}
	if text := resultText(res); res.IsError || !strings.Contains(text, "the recording ended early: stopped") {
		t.Errorf("a stopped recording: %s", text)
	}
	r.waitCommands(func(cmds []midicontrol.Command) bool {
		cc := globalCCs(cmds, 105, 107)
		return len(cc) >= 2 && cc[len(cc)-1] == 107000
	})
	r.mu.Lock()
	recording, armed, running := r.Recording, r.Armed, r.Running
	r.mu.Unlock()
	if recording || armed || running {
		t.Errorf("after a stopped recording: recorder %v, master armed %v, transport %v; want all off", recording, armed, running)
	}

	// Stopped after the pass, while its take is looked for, and the sequencer
	// did not switch the recorder off: the daemon does, before it disarms, so
	// the recorder does not record on through the stopped transport. Its own
	// off after a pass is held back here, so that the stop comes first.
	r.mu.Lock()
	r.ClipOff = 0
	r.mu.Unlock()
	wait, off := stemListWait, stemOffAfter
	stemListWait, stemOffAfter = 10*time.Second, time.Minute
	r.opens("Stem Song stem bass", stemBassStrips)
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1, "stems": []string{"bass"}}); res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	for deadline, started := time.Now().Add(30*time.Second), false; ; time.Sleep(10 * time.Millisecond) {
		r.mu.Lock()
		recording, running := r.Recording, r.Running
		r.mu.Unlock()
		started = started || (recording && running)
		if started && recording && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the pass did not end with the recorder still on")
		}
	}
	if res := call(t, s.handleStopSong, map[string]any{}); res.IsError {
		t.Fatalf("stop: %s", resultText(res))
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		res = call(t, s.handleGetSongStems, map[string]any{})
		if out, _ := res.StructuredContent.(map[string]any); out["done"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the recording stopped after its pass did not end: %s", resultText(res))
		}
	}
	stemListWait, stemOffAfter = wait, off
	if text := resultText(res); res.IsError || !strings.Contains(text, "the recording ended early: stopped") {
		t.Errorf("a recording stopped after its pass: %s", text)
	}
	sent := r.waitCommands(func(cmds []midicontrol.Command) bool {
		cc := globalCCs(cmds, 103, 105, 107)
		return len(cc) >= 4 && cc[len(cc)-1] == 107000
	})
	if cc := globalCCs(sent, 103, 105, 107); !slices.Equal(cc[len(cc)-4:], []int{105127, 103127, 105000, 107000}) {
		t.Errorf("the controls of a pass stopped while its take was looked for = %v, want on, the stop, off, disarm", cc[len(cc)-4:])
	}
	r.mu.Lock()
	recording = r.Recording
	r.mu.Unlock()
	if recording {
		t.Error("a recording stopped after its pass left AUM's recorder on")
	}

	// A sequencer that does not switch the recorder off: AUM lists no take
	// after the pass, so the daemon switches the recorder off itself, with
	// the transport stopped. The take ran on after the stem: the report says
	// so, and the mix, whose load reads the take's length, is not played.
	r.mu.Lock()
	r.ClipOff = 0
	r.mu.Unlock()
	r.opens("Stem Song stem bass", stemBassStrips)
	// The stopped pass above left a take in the folder: the next session is
	// loaded when this run's take has been found, not when that one is seen.
	// One reading of the list takes longer than a take is given to appear,
	// as on an iPad with many recordings: the recorder is switched off and
	// the list read once more all the same.
	wait = stemListWait
	stemListWait = stemOffAfter + 50*time.Millisecond
	defer func() { stemListWait = wait }()
	deck.mu.Lock()
	deck.seen = nil
	deck.slow = stemListWait + 50*time.Millisecond
	deck.mu.Unlock()
	deck.afterTake = func(folder string) {
		if folder == "Stem Song stem bass" {
			r.opens("Stem Song stem band", stemBandStrips)
		}
	}
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1}); res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	rep, text := stemsReportOfJob(t, s)
	if !rep.Ready || len(rep.Stems[0].Notes) == 0 || !strings.Contains(text, "until the daemon switched its recorder off") {
		t.Fatalf("report of takes the sequence did not end = %+v\n%s", rep, text)
	}
	// The recorder was switched off once more than a pass with a sequence
	// that ends its take: after the transport's stop.
	cmds := r.waitCommands(func(cmds []midicontrol.Command) bool { return len(globalCCs(cmds, 103, 105)) >= 4 })
	if cc := globalCCs(cmds, 103, 105); !slices.Equal(cc[len(cc)-4:], []int{105000, 105127, 103127, 105000}) {
		t.Errorf("the recorder's controls of the last pass = %v, want off, on, the stop, off", cc[len(cc)-4:])
	}
	r.opens("Stem Song mix", stemMixStrips)
	res = call(t, s.handleMixSongStems, map[string]any{"song": "stem_song", "confirm": true, "record": false})
	if text := resultText(res); !res.IsError || !strings.Contains(text, "not whole beats: it did not begin with the transport or did not end on a bar line") ||
		!strings.Contains(text, "not played") || s.player.Status().Playing {
		t.Errorf("a mix of takes that are not whole beats long: %s", text)
	}
}

// stemsDone waits for the running stem recording to end and returns
// get_song_stems' last answer, a failed recording's too.
func stemsDone(t *testing.T, s *Server) (*mcp.CallToolResult, string) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); ; {
		res := call(t, s.handleGetSongStems, map[string]any{"wait_ms": 2000})
		if out, _ := res.StructuredContent.(map[string]any); out["done"] == true || res.IsError {
			return res, resultText(res)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stem recording did not end: %s", resultText(res))
		}
	}
}

// TestTakeIn picks a pass's take out of its folder as read before and after
// the pass. A folder whose list shows the newest take first is read at its
// first page, and there the take is; a folder sorted otherwise is read whole.
// A folder read at its top before and whole after (its sort changed between)
// tells nothing: the older takes below its first page would all look new.
func TestTakeIn(t *testing.T) {
	takes := func(names ...string) []ipad.AUMFileEntry {
		out := make([]ipad.AUMFileEntry, len(names))
		for i, n := range names {
			out[i] = ipad.AUMFileEntry{Name: n, Detail: "13. Nov 2026 at 00:12:57 • 4.0 MiB • wav"}
		}
		return out
	}
	// A folder of 30 takes, the newest first; its first page holds 12.
	var all []string
	for i := 30; i > 0; i-- {
		all = append(all, fmt.Sprintf("Master - 120 bpm - %03d", i))
	}
	const take = "Master - 120 bpm - 031"
	top := func(names ...string) stemList {
		return stemList{Takes: takes(names[:min(len(names), 12)]...), Top: true, Sort: ipadtest.SortLastOpened}
	}
	whole := func(names ...string) stemList { return stemList{Takes: takes(names...), Sort: "NAME"} }
	withTake := append([]string{take}, all...)
	for _, tc := range []struct {
		name          string
		before, after stemList
		want          string // the take found, "" for none
		resorted      bool
	}{
		{"top, then top", top(all...), top(withTake...), take, false},
		{"top, then top: no take", top(all...), top(all...), "", false},
		{"no folder, then top", stemList{Missing: true}, top(take), take, false},
		{"no folder, then still none", stemList{Missing: true}, stemList{Missing: true}, "", false},
		{"whole, then whole", whole(all...), whole(withTake...), take, false},
		{"whole, then top", whole(all...), top(withTake...), take, false},
		{"top, then whole", top(all...), whole(withTake...), "", true},
		{"top, then whole: no take", top(all...), whole(all...), "", true},
	} {
		got, found, resorted := takeIn(tc.before, tc.after)
		if found != (tc.want != "") || (found && got.Name != tc.want) || resorted != tc.resorted {
			t.Errorf("%s: take %q (found %v), resorted %v; want %q, resorted %v", tc.name, got.Name, found, resorted, tc.want, tc.resorted)
		}
	}
}

// TestStemsRoot: a root of AUM's Recordings read through for one stem's
// missing folder answers for later stems whose folders it did not hold, and
// not for those it held; before any such reading it answers for none.
func TestStemsRoot(t *testing.T) {
	var r stemsRoot
	if r.lacks("Song stem bass") {
		t.Fatal("a root not read yet lacks a folder")
	}
	r.note([]string{"Older Song", "Song stem band"}, "bass")
	if !r.lacks("Song stem bass") || !r.lacks("Song stem keys") || r.lacks("Song stem band") || r.lacks("Older Song") || r.stem != "bass" {
		t.Fatalf("root = %+v", r)
	}
	r.note([]string{}, "keys")
	if !r.lacks("Song stem band") {
		t.Fatal("an empty root read through holds a folder")
	}
}

// A folder read at its top before a pass and whole after it, its sort changed
// in between, says nothing of which take is new: the stem is not taken from
// it, and the recorder is switched off before the master is disarmed, in case
// the sequence did not end the take.
func TestRecordSongStemsSortedAnew(t *testing.T) {
	r, deck := stemsRig(t)
	s := r.s
	deck.mu.Lock()
	for i := 30; i > 0; i-- {
		deck.older["Stem Song stem bass"] = append(deck.older["Stem Song stem bass"], fmt.Sprintf("Master - 120 bpm - %03d", i))
	}
	deck.sorts = []string{ipadtest.SortLastOpened, "NAME"}
	deck.mu.Unlock()
	r.opens("Stem Song stem bass", stemBassStrips)
	if res := call(t, s.handleRecordSongStems, map[string]any{"song": "stem_song", "confirm": true, "tail_bars": 1, "stems": []string{"bass"}}); res.IsError {
		t.Fatalf("record: %s", resultText(res))
	}
	res, text := stemsDone(t, s)
	if !res.IsError || !strings.Contains(text, `AUM's list of "Stem Song stem bass" was sorted "LAST OPENED" before the pass, newest first, and is sorted "NAME" now: which take is new cannot be told`) {
		t.Fatalf("a list sorted anew between the readings:\n%s", text)
	}
	if got, want := deck.readingsOf(0), []string{"top Stem Song stem bass", "whole Stem Song stem bass"}; !slices.Equal(got, want) {
		t.Errorf("readings = %q, want %q", got, want)
	}
	if _, kept := loadSongStems()["stem_song"]; kept {
		t.Error("a take that could not be told was noted down")
	}
	tail := []int{103127, 105000, 107000}
	cmds := r.waitCommands(func(cmds []midicontrol.Command) bool {
		cc := globalCCs(cmds, 103, 105, 107)
		return len(cc) >= 3 && slices.Equal(cc[len(cc)-3:], tail)
	})
	if cc := globalCCs(cmds, 103, 105, 107); len(cc) < 3 || !slices.Equal(cc[len(cc)-3:], tail) {
		t.Errorf("the recorder's controls of the pass end in %v, want the stop, off, disarm %v", cc[max(0, len(cc)-3):], tail)
	}
}

// TestParseFilePlayer reads the rows of AUM's FILE PLAYER panel as a device
// showed them, for a take that began with the transport and ended on a bar
// line, and for one that was ended with the transport stopped.
func TestParseFilePlayer(t *testing.T) {
	rows := func(file, duration, beats string) []ipad.AUMRow {
		return []ipad.AUMRow{
			{Label: "Selected file"}, {Label: file},
			{Label: "Duration", Value: duration}, {Label: duration},
			{Label: "Format", Value: "WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz"}, {Label: "WAVE (LPCM) · 2 ch · 16 bit · 48000 Hz"},
			{Label: "Options"}, {Label: "Loop", Value: "OFF"}, {Label: "Sync", Value: "OFF"},
			{Label: "Original BPM", Value: "120.0"}, {Label: "Number of Beats", Value: beats}, {Label: "Rate", Value: "1.000"}, {Label: "Beat Offset", Value: "0"},
		}
	}
	v := parseFilePlayer(rows("Master - 120 bpm - 002.wav", "1'151'993 frames ·  00:23.9", "47.9997"))
	if v.File != "Master - 120 bpm - 002.wav" || v.Frames != 1151993 || v.Rate != 48000 || v.Loop != "OFF" || v.Sync != "OFF" || !strings.Contains(v.Format, "16 bit") {
		t.Fatalf("view = %+v", v)
	}
	if !strings.HasPrefix(v.Text, "Master - 120 bpm - 002.wav | Duration 1'151'993 frames") || strings.Count(v.Text, "WAVE") != 1 {
		t.Errorf("text = %q, want every row once, the file first", v.Text)
	}
	var take stemTake
	take.setLength(v, 120)
	if take.Whole == nil || !*take.Whole || take.Beats < 47.999 || take.Beats > 48 || take.Seconds < 23.999 {
		t.Errorf("a take 7 frames short of 48 beats = %+v, want whole beats", take)
	}
	take = stemTake{}
	take.setLength(parseFilePlayer(rows("Master - 120 bpm - 001.wav", "8'493'824 frames ·  02:56.9", "353.909")), 120)
	if take.Whole == nil || *take.Whole || take.Frames != 8493824 {
		t.Errorf("a take of 353.909 beats = %+v, want not whole beats", take)
	}
	// A player without a file shows no name and no length.
	if v := parseFilePlayer([]ipad.AUMRow{{Label: "Selected file"}, {Label: "Options"}, {Label: "Loop", Value: "OFF"}}); v.File != "" || v.Frames != 0 {
		t.Errorf("an empty player = %+v", v)
	}
	take = stemTake{}
	take.setLength(filePlayerView{}, 120)
	if take.Whole != nil {
		t.Error("a take without a length was called whole or not")
	}
}

func TestRecordingFile(t *testing.T) {
	for _, tc := range []struct{ name, detail, want string }{
		{"Master - 120 bpm - 002", "12. Nov 2026 at 21:23:42 • 4.4 MiB • wav", "Stem/Master - 120 bpm - 002.wav"},
		{"Master - 120 bpm - 002.wav", "12. Nov 2026 at 21:23:42 • 4.4 MiB • wav", "Stem/Master - 120 bpm - 002.wav"},
		{"Master - 92.5 bpm - 001", "", "Stem/Master - 92.5 bpm - 001.wav"},
		{"take", "1. Jan 2026 at 00:00:00 • 1 MiB • aiff", "Stem/take.aiff"},
	} {
		if got := recordingFile("Stem", ipad.AUMFileEntry{Name: tc.name, Detail: tc.detail}); got != tc.want {
			t.Errorf("recordingFile(%q, %q) = %q, want %q", tc.name, tc.detail, got, tc.want)
		}
	}
	before := []ipad.AUMFileEntry{{Name: "Master - 120 bpm - 001"}}
	after := []ipad.AUMFileEntry{{Name: "Master - 120 bpm - 002"}, {Name: "low - 120 bpm - 001"}, {Name: "Master - 120 bpm - 001"}}
	if e, ok := newRecording(before, after); !ok || e.Name != "Master - 120 bpm - 002" {
		t.Errorf("the new recording = %+v, %v; want the master's new take", e, ok)
	}
	if _, ok := newRecording(after, after); ok {
		t.Error("a list that did not grow has a new recording")
	}
	// AUM's list is not always in the order of the takes: the new take with
	// the highest number is the pass's.
	unordered := []ipad.AUMFileEntry{{Name: "Master - 120 bpm - 003"}, {Name: "Master - 120 bpm - 005"}, {Name: "Master - 120 bpm - 004"}, {Name: "Master - 120 bpm - 001"}}
	if e, ok := newRecording(before, unordered); !ok || e.Name != "Master - 120 bpm - 005" {
		t.Errorf("the new recording of an unordered list = %+v, %v; want take 005", e, ok)
	}
}
