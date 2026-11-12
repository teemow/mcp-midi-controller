package song

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teemow/aum-session-go/aum"
)

// fileSong plays two recordings under a master effect, with a level scene and
// a ramp on a file track and on the master.
const fileSong = `
title: File Song
tempo: 120
tracks:
  - id: strings
    level: -2
    instrument: {file: "Night Drive stem strings/Master - 120 bpm - 001.wav"}
    effects:
      - {id: echo, plugin: delay, params: {Mix: 0.2}}
  - id: winds
    instrument: {file: "Night Drive stem winds/Master - 120 bpm - 003.wav"}
master:
  - {id: glue, plugin: reverb, preset: Small Room, params: {Mix: 0.3}}
scenes:
  loud:
    strings: {level: 0, effects: {echo: {params: {Mix: 0.4}}}}
    winds: {mute: true}
    master: {effects: {glue: {params: {Mix: 0.5}}}}
sections:
  a: {bars: 2}
  b:
    bars: 2
    scene: loud
    automation:
      - {track: winds, param: level, from: -12, to: 0}
arrangement: [a, b]
session: {lead_bars: 2}
`

func TestFileTrackParses(t *testing.T) {
	s := mustParse(t, fileSong)
	if !s.Tracks[0].IsFile() || !s.HasFiles() || !s.filesOnly() {
		t.Fatalf("the tracks are not taken for file tracks: %+v", s.Tracks)
	}
	// A song of files alone has no notes, and needs no pattern.
	tl, err := Compile(s, mustPlan(t, s))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if tl.Bars != 4 || tl.Tracks[0].Notes != 0 {
		t.Errorf("timeline = %d bars, %d notes; want 4 bars and no notes", tl.Bars, tl.Tracks[0].Notes)
	}
	// The document survives a round trip.
	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	again := mustParse(t, string(data))
	if again.Tracks[1].Instrument.File != s.Tracks[1].Instrument.File || again.Session.LeadBars != 2 {
		t.Errorf("round trip lost the file or the lead-in:\n%s", data)
	}
}

func TestFileTrackChecks(t *testing.T) {
	for _, tc := range []struct{ name, old, new, want string }{
		{"a plugin beside the file", `instrument: {file: "Night Drive stem winds/Master - 120 bpm - 003.wav"}`,
			`instrument: {plugin: synth, file: "a/b.wav"}`, "a plugin or a file, not both"},
		{"a preset", `instrument: {file: "Night Drive stem winds/Master - 120 bpm - 003.wav"}`,
			`instrument: {file: "a/b.wav", preset: Init}`, "a file has no preset"},
		{"parameters", `instrument: {file: "Night Drive stem winds/Master - 120 bpm - 003.wav"}`,
			`instrument: {file: "a/b.wav", params: {Cutoff: 1}}`, "a file has no parameters"},
		{"an absolute path", `"Night Drive stem winds/Master - 120 bpm - 003.wav"`, `"/var/b.wav"`, "not a path relative to AUM's Recordings"},
		{"a path out of the folder", `"Night Drive stem winds/Master - 120 bpm - 003.wav"`, `"../b.wav"`, "not a path inside AUM's Recordings"},
		{"no extension", `"Night Drive stem winds/Master - 120 bpm - 003.wav"`, `"a/Master - 120 bpm - 003"`, "no file extension"},
		{"a drum kind", "  - id: winds\n", "  - id: winds\n    kind: drums\n", "has no notes, so no kind"},
		{"a transposition", "  - id: winds\n", "  - id: winds\n    transpose: 12\n", "a file is not transposed"},
		{"a pattern to play", "  a: {bars: 2}", "  a: {bars: 2, play: {winds: riff}}", `"winds" plays a file`},
		{"a scene with a preset", "    winds: {mute: true}", "    winds: {mute: true, preset: Init}", `"winds" plays a file: a scene can set its level`},
		{"a ramp of a parameter", "{track: winds, param: level, from: -12, to: 0}", "{track: winds, param: Cutoff, from: 0, to: 1}", "its level and its effects can be automated"},
		{"a file as an effect", "{id: echo, plugin: delay, params: {Mix: 0.2}}", `{id: echo, plugin: delay, file: "a/b.wav", params: {Mix: 0.2}}`, "only a track's instrument can be a file"},
		{"a lead-in that is none", "session: {lead_bars: 2}", "session: {lead_bars: 99}", "/session/lead_bars"},
		{"a group called master", "  - id: winds\n", "  - id: winds\n    group: master\n", "/tracks/1/group"},
	} {
		doc := strings.Replace(fileSong, tc.old, tc.new, 1)
		if doc == fileSong {
			t.Fatalf("%s: the document was not changed", tc.name)
		}
		if tc.name == "a pattern to play" {
			doc += "patterns:\n  riff: {step: 1/4, notes: \"C3 . . .\"}\n"
		}
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
	// A song with an instrument still needs a pattern.
	doc := strings.Replace(fileSong, `instrument: {file: "Night Drive stem winds/Master - 120 bpm - 003.wav"}`, "instrument: {plugin: synth}", 1)
	if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "a song needs at least one pattern") {
		t.Errorf("a song with an instrument and no pattern: err = %v", err)
	}
}

// TestFileTrackPlan checks what the plan makes of a file track: a File Player
// where an instrument would sit, a strip with its level and mute, and no
// control of a plugin.
func TestFileTrackPlan(t *testing.T) {
	s := mustParse(t, fileSong)
	p := mustPlan(t, s)
	tp := p.Track("strings")
	n := tp.Nodes[0]
	if !n.IsFile() || n.Name != FilePlayerName || n.Slot != 0 || n.MIDI || tp.File() != "Night Drive stem strings/Master - 120 bpm - 001.wav" {
		t.Fatalf("the instrument node of a file track = %+v", n)
	}
	if (n.Bypass != Assignment{}) || len(n.Params) != 0 || len(n.Presets) != 0 {
		t.Errorf("a File Player was given controls: %+v", n)
	}
	if echo := p.Node("strings", "echo"); echo == nil || echo.Slot != firstInsertSlot || echo.Bypass.Number == 0 && echo.Bypass.Channel == 0 {
		t.Errorf("the file track's effect = %+v, want it at the first insert with a bypass of its own", echo)
	}
	if tp.Level == p.Track("winds").Level || tp.Level.Channel != GlobalChannel {
		t.Errorf("levels = %v and %v, want one each on the global channel", tp.Level, p.Track("winds").Level)
	}

	// Another file is another session; so is another lead-in.
	other := mustParse(t, strings.Replace(fileSong, "001.wav", "002.wav", 1))
	if mustPlan(t, other).Fingerprint() == p.Fingerprint() {
		t.Error("the plan's fingerprint does not cover the file")
	}

	// The song's sound: the strips' levels and mutes and the effects, with
	// nothing for the File Players themselves.
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, e := range tl.Sections[0].State {
		if e.Kind == Control && e.Channel == 0 {
			t.Errorf("the opening sound holds a message without an address (a File Player's bypass?): %+v", e)
		}
	}
	level := func(section int, track string) int { return levelOf(tl, p, section, track) }
	if level(0, "strings") != levelWire(-2) || level(1, "strings") != levelWire(0) {
		t.Errorf("the file track's level = %d then %d, want -2 dB then the scene's 0 dB", level(0, "strings"), level(1, "strings"))
	}
	if level(1, "winds") != levelWire(-12) {
		t.Errorf("the ramped file track opens its section at %d, want the ramp's -12 dB (%d)", level(1, "winds"), levelWire(-12))
	}
}

// TestBuildSessionFilePlayer authors a session with file tracks and reads the
// encoded file back: the File Player as each strip's source, its saved state
// as AUM writes one that plays a file once with the transport, the effect
// after the output, and no MIDI route to it.
func TestBuildSessionFilePlayer(t *testing.T) {
	s := mustParse(t, fileSong)
	p := mustPlan(t, s)
	prepared := 0
	sess, rep, err := p.BuildSession(SessionOptions{Prepare: func(*NodePlan, *aum.NodeSpec) error { prepared++; return nil }})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if rep.Files != 2 || prepared != 2 {
		t.Errorf("report = %+v with %d plugins prepared; want 2 files and the two effects prepared", rep, prepared)
	}
	data, err := sess.Archive().Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	re, err := aum.Open(data)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	chans := re.Channels()
	if len(chans) != 4 {
		t.Fatalf("%d strips, want brain, two tracks, master", len(chans))
	}
	// strings: the File Player, the output, the delay.
	chain := chans[1].Nodes
	if len(chain) != 3 || chain[0].ArchiveDescClass != "FilePlayerNodeDescription" || chain[0].Component != nil ||
		chain[1].ArchiveDescClass != "BusDestDescription" || chain[2].Component == nil || chain[2].Component.Subtype != "Dly1" {
		t.Fatalf("strings chain = %+v, want File Player, output, effect", chain)
	}
	if fl := chans[1].FaderLevel; fl == nil || *fl < 0.79 || *fl > 0.80 {
		t.Errorf("strings fader = %v, want the gain of -2 dB", fl)
	}
	state := func(strip int) map[string]any {
		t.Helper()
		a := re.Archive()
		root, _ := a.Root().(map[string]any)
		node, _ := a.Deref(nsObjects(a, nsObjects(a, root["nodeArchives"])[strip])[0]).(map[string]any)
		dict, _ := a.Deref(node["archiveNodeState"]).(map[string]any)
		keys, _ := dict["NS.keys"].([]any)
		st := map[string]any{}
		for _, k := range keys {
			name, _ := a.Deref(k).(string)
			st[name] = a.Deref(nsValue(a, dict, name))
		}
		if len(st) == 0 {
			t.Fatalf("the File Player on strip %d has no saved state", strip)
		}
		return st
	}
	want := map[string]any{
		"FilePlayerPath":       "AUM Recordings/Night Drive stem strings/Master - 120 bpm - 001.wav",
		"FilePlayerEnabled":    true,
		"FilePlayerLoop":       false,
		"FilePlayerSync":       false,
		"FilePlayerNormalize":  false,
		"FilePlayerUserRate":   1.0,
		"FilePlayerBeatOffset": 0.0,
		"FilePlayerTempo":      120.0,
	}
	got := state(1)
	for key, w := range want {
		if got[key] != w {
			t.Errorf("File Player state %s = %#v, want %#v", key, got[key], w)
		}
	}
	if _, has := got["FilePlayerURLBookmark"]; has {
		t.Error("the File Player carries a bookmark, which only the device can make")
	}
	if path := state(2)["FilePlayerPath"]; path != "AUM Recordings/Night Drive stem winds/Master - 120 bpm - 003.wav" {
		t.Errorf("the second File Player's path = %v", path)
	}

	// No route plays a File Player, and its slot has no bypass mapped; the
	// strip's level and mute and the effect's controls are mapped.
	got2 := map[string]aum.Mapping{}
	for _, m := range re.Mappings(false) {
		got2[m.Collection+"/"+m.Target] = m
	}
	if _, mapped := got2["Channels/chan1/slot0/_AUMNode:Bypass"]; mapped {
		t.Error("the File Player's slot has a bypass mapping")
	}
	for _, path := range []string{"Channels/chan1/Channel controls/Volume", "Channels/chan2/Channel controls/Mute", "Channels/chan1/slot2/_AUMNode:Bypass", "Channels/chan1/slot2/mix"} {
		if _, ok := got2[path]; !ok {
			t.Errorf("no mapping for %s", path)
		}
	}
	// The master can be armed for AUM's recorder, in every session.
	if m, ok := got2["Channels/chan3/Channel controls/Rec enable"]; !ok || m.Spec.Data1 != p.MasterRec.Number || m.Spec.Channel != GlobalChannel-1 {
		t.Errorf("the master's Rec enable = %+v, want %s", m.Spec, p.MasterRec)
	}
	for name, n := range matrixFilters(t, re) {
		if strings.HasPrefix(name, "chan1/slot0") || strings.HasPrefix(name, "chan2/slot0") {
			t.Errorf("a MIDI route ends at a File Player: %s (%d)", name, n)
		}
	}
}

// TestSequenceLeadBars checks the lead-in a song asks for: at the least the
// bars it says, and never fewer than its opening sound needs.
func TestSequenceLeadBars(t *testing.T) {
	tl, _ := compileTest(t, fileSong)
	q := tl.Sequence()
	if q.LeadBars != 2 || q.Lead() != 4*time.Second {
		t.Errorf("lead-in = %d bars (%s), want the 2 bars the song asks for", q.LeadBars, q.Lead())
	}
	if len(q.Notes) != 0 || len(q.Controls) == 0 {
		t.Errorf("the sequence of a song of files = %d notes, %d controls; want no notes and the song's controls", len(q.Notes), len(q.Controls))
	}
	plain, _ := compileTest(t, strings.Replace(fileSong, "session: {lead_bars: 2}", "", 1))
	if plain.Sequence().LeadBars != 1 {
		t.Errorf("lead-in without lead_bars = %d bars, want 1", plain.Sequence().LeadBars)
	}
	if plain.Sequence().Fingerprint() == q.Fingerprint() {
		t.Error("the sequence's fingerprint does not cover the lead-in")
	}
}

func TestStems(t *testing.T) {
	s := mustParse(t, strings.Replace(testSong, "  - id: lead\n", "  - id: lead\n    group: band\n", 1))
	s.Tracks[2].Group = "band" // drums
	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatalf("Stems: %v", err)
	}
	want := []Stem{{Name: "bass", Tracks: []string{"bass"}}, {Name: "band", Tracks: []string{"lead", "drums"}}}
	if len(stems) != 2 || stems[0].Name != want[0].Name || !slices.Equal(stems[1].Tracks, want[1].Tracks) {
		t.Errorf("stems = %+v, want %+v", stems, want)
	}

	// A grouping given replaces the document's; a track left out stands alone.
	stems, err = s.Stems(map[string][]string{"low": {"bass", "drums"}})
	if err != nil || len(stems) != 2 || stems[0].Name != "low" || !slices.Equal(stems[0].Tracks, []string{"bass", "drums"}) || stems[1].Name != "lead" {
		t.Errorf("stems of a given grouping = %+v, %v", stems, err)
	}
	for _, tc := range []struct {
		groups map[string][]string
		want   string
	}{
		{map[string][]string{"a": {"bass", "nope"}}, `no track "nope"`},
		{map[string][]string{"a": {"bass"}, "b": {"bass"}}, `is in group "a" already`},
		{map[string][]string{"a": {"bass", "bass"}}, "listed twice"},
		{map[string][]string{"a": {}}, "list the tracks"},
		{map[string][]string{"master": {"bass"}}, "the master strip's name"},
	} {
		if _, err := s.Stems(tc.groups); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Stems(%v): err = %v, want %q", tc.groups, err, tc.want)
		}
	}
}

func TestStemLayout(t *testing.T) {
	s := mustParse(t, testSong)
	l, err := s.StemLayout(0)
	if err != nil || l.LeadBars != 1 || l.TailBars != 2 {
		t.Errorf("layout at 120 BPM in 4/4 = %+v, %v; want a lead-in of 1 bar and the 2 bars that cover four seconds", l, err)
	}
	// Short bars: the lead-in covers the preset loads, whichever stem has any.
	fast := &Song{Tempo: 240, TimeSignature: "3/8"}
	if l, err := fast.StemLayout(3); err != nil || l.LeadBars != 4 || l.TailBars != 3 {
		t.Errorf("layout at 240 BPM in 3/8 = %+v, %v; want a lead-in of 4 bars (1.2 s) and the 3 bars asked for", l, err)
	}
	if _, err := s.StemLayout(99); err == nil {
		t.Error("a tail of 99 bars was accepted")
	}
	// A song that asks for a longer lead-in keeps it.
	fast.Session = &SessionSpec{LeadBars: 6}
	if l, _ := fast.StemLayout(1); l.LeadBars != 6 {
		t.Errorf("lead-in of a song that asks for 6 bars = %d", l.LeadBars)
	}
}

// TestStemSong derives a stem's song and the mix's and compiles both: the
// stem's tracks alone with their sound and nothing on the master, the mix's
// files under the master's effects, the same bars, lead-in and tail.
func TestStemSong(t *testing.T) {
	doc := strings.Replace(testSong, "arrangement: [verse, chorus, verse]", `arrangement: [verse, chorus]`, 1)
	doc = strings.Replace(doc, "  chorus:\n    bars: 2\n    scene: chorus\n", `  chorus:
    bars: 2
    scene: chorus
    automation:
      - {track: lead, param: level, from: -6, to: -20}
      - {track: master, effect: glue, param: Mix, from: 0.2, to: 0.6}
`, 1)
	doc = strings.Replace(doc, "  - {id: glue, plugin: reverb, bypass: true}", "  - {id: glue, plugin: reverb, bypass: true, params: {Mix: 0.2}}", 1)
	s := mustParse(t, doc)
	s.Tracks[1].Group, s.Tracks[2].Group = "band", "band"
	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.StemLayout(3)
	if err != nil {
		t.Fatal(err)
	}

	band, err := s.StemSong(stems[1], "Test Song stem band", l)
	if err != nil {
		t.Fatalf("StemSong: %v", err)
	}
	if len(band.Tracks) != 2 || band.Tracks[0].ID != "lead" || band.Tracks[0].Channel != 2 || band.Tracks[1].Channel != 3 || band.Tracks[0].Group != "" {
		t.Errorf("the stem's tracks = %+v, want lead and drums on the channels they have in the whole song", band.Tracks)
	}
	if len(band.Master) != 0 {
		t.Errorf("the stem has master effects: %+v", band.Master)
	}
	if _, has := band.Scenes["chorus"]["master"]; has {
		t.Error("the stem's scene still sets the master")
	}
	if _, has := band.Scenes["chorus"]["bass"]; has {
		t.Error("the stem's scene still sets another stem's track")
	}
	if _, has := band.Sections["verse"].Play["bass"]; has {
		t.Error("the stem's section still plays another stem's track")
	}
	if _, has := band.Patterns["bass_a"]; has {
		t.Error("the stem keeps a pattern it does not play")
	}
	tail := band.Arrangement[len(band.Arrangement)-1]
	sec := band.Sections[tail]
	if tail != "stem_tail" || sec.Bars != 3 || len(sec.Play) != 0 || !slices.Equal(sec.Scene, StringList{"chorus"}) {
		t.Errorf("the tail = %q %+v, want 3 bars of the last section's scene and no notes", tail, sec)
	}
	// The lead's fade ended at -20 dB: the tail holds it there. The master's
	// ramp is not the stem's.
	if len(sec.Automation) != 1 || sec.Automation[0].Track != "lead" || sec.Automation[0].From.Num != -20 || sec.Automation[0].To.Num != -20 {
		t.Errorf("the tail's ramps = %+v, want the lead's level held at -20 dB", sec.Automation)
	}
	if band.Session == nil || band.Session.LeadBars != l.LeadBars || band.Session.TrackTaps {
		t.Errorf("the stem's session = %+v, want the layout's lead-in and no track taps", band.Session)
	}
	bandPlan := mustPlan(t, band)
	tl, err := Compile(band, bandPlan)
	if err != nil {
		t.Fatalf("the stem does not compile: %v", err)
	}
	whole, err := Compile(s, mustPlan(t, s))
	if err != nil {
		t.Fatal(err)
	}
	// The stem's sequence switches AUM's recorder off itself, half a beat
	// before its end; the whole song's does not, nor the mix's.
	recorderOff := func(q *Sequence) []int64 {
		var at []int64
		for _, e := range q.Controls {
			if e.Kind == Control && e.Channel == GlobalChannel && e.Data1 == bandPlan.Transport["Toggle Record"].Number {
				if e.Data2 != 0 {
					t.Errorf("the sequence switches the recorder on: %+v", e)
				}
				at = append(at, e.Tick)
			}
		}
		return at
	}
	if q := tl.Sequence(); !slices.Equal(recorderOff(q), []int64{q.Ticks - PPQ/2}) {
		t.Errorf("the stem's sequence switches the recorder off at %v, want once, half a beat before its end (%d)", recorderOff(q), q.Ticks-PPQ/2)
	}
	if at := recorderOff(whole.Sequence()); len(at) != 0 {
		t.Errorf("the whole song's sequence switches the recorder off at %v", at)
	}
	if tl.Bars != whole.Bars+3 || tl.Sequence().LeadBars != l.LeadBars {
		t.Errorf("the stem is %d bars after a lead-in of %d, want the song's %d and the tail's 3 after %d", tl.Bars, tl.Sequence().LeadBars, whole.Bars, l.LeadBars)
	}
	// The stem plays the notes the tracks play in the whole song, where they
	// play them.
	if got, want := notesOf(tl, "lead"), notesOf(whole, "lead"); !slices.Equal(got, want) {
		t.Errorf("the stem's lead plays %v, the song's %v", got, want)
	}
	if level := levelOf(tl, bandPlan, len(tl.Sections)-1, "lead"); level != levelWire(-20) {
		t.Errorf("the lead's level in the tail = %d, want the fade's end, -20 dB (%d)", level, levelWire(-20))
	}

	// The other stem has one track and rests nowhere a pattern is needed for.
	bass, err := s.StemSong(stems[0], "Test Song stem bass", l)
	if err != nil || len(bass.Tracks) != 1 || len(bass.Patterns) != 1 {
		t.Fatalf("the bass stem = %+v, %v", bass, err)
	}

	// The mix: a file per stem, the master's effects with their scene and
	// ramp, the same bars.
	files := map[string]string{"bass": "Test Song stem bass/Master - 120 bpm - 001.wav", "band": "Test Song stem band/Master - 120 bpm - 001.wav"}
	mix, err := s.MixSong(stems, files, "Test Song mix", l)
	if err != nil {
		t.Fatalf("MixSong: %v", err)
	}
	if len(mix.Tracks) != 2 || mix.Tracks[0].ID != "bass" || mix.Tracks[1].Instrument.File != files["band"] || mix.Tracks[0].Level != nil {
		t.Errorf("the mix's tracks = %+v, want a file track per stem at unity", mix.Tracks)
	}
	if len(mix.Master) != 1 || mix.Master[0].EffectID() != "glue" || len(mix.Patterns) != 0 {
		t.Errorf("the mix = master %+v, patterns %v; want the song's master effect and no pattern", mix.Master, mix.Patterns)
	}
	if st := mix.Scenes["chorus"]; len(st) != 1 || st["master"].Effects["glue"].Bypass == nil {
		t.Errorf("the mix's chorus scene = %+v, want the master's entry alone", st)
	}
	if ramps := mix.Sections["chorus"].Automation; len(ramps) != 1 || ramps[0].Track != MasterTrack {
		t.Errorf("the mix's chorus ramps = %+v, want the master's alone", ramps)
	}
	if held := mix.Sections["stem_tail"].Automation; len(held) != 1 || held[0].From.Num != 0.6 {
		t.Errorf("the mix's tail = %+v, want the master's ramp held at its end", held)
	}
	data, err := mix.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again := mustParse(t, string(data))
	mixTL, err := Compile(again, mustPlan(t, again))
	if err != nil {
		t.Fatalf("the mix does not compile from its document: %v\n%s", err, data)
	}
	if at := recorderOff(mixTL.Sequence()); len(at) != 0 {
		t.Errorf("the mix's sequence switches the recorder off at %v", at)
	}
	if mixTL.Bars != tl.Bars || mixTL.Sequence().LeadBars != tl.Sequence().LeadBars {
		t.Errorf("the mix is %d bars after %d, the stem %d after %d", mixTL.Bars, mixTL.Sequence().LeadBars, tl.Bars, tl.Sequence().LeadBars)
	}
	if _, err := s.MixSong(stems, map[string]string{"bass": files["bass"]}, "Test Song mix", l); err == nil || !strings.Contains(err.Error(), `"band" has no recording`) {
		t.Errorf("a mix without a stem's file: err = %v", err)
	}
}

// TestPlayerSequencedFinish checks what ends a recording with a sequenced
// playback: the finish goes out ahead of the song's end, the stop a wait
// after it, and a playback stopped before its start finishes nothing.
func TestPlayerSequencedFinish(t *testing.T) {
	tl, _ := fastSong(t)
	q := tl.Sequence()
	ev := func(n int) Event { return Event{Kind: Control, Channel: GlobalChannel, Data1: n, Data2: 127} }
	opts := SequencedOptions{
		Rewind: []Event{ev(104)}, Start: []Event{ev(105)},
		Finish: []Event{{Kind: Control, Channel: GlobalChannel, Data1: 105}}, FinishLead: 50 * time.Millisecond, FinishWait: 80 * time.Millisecond,
		Stop: []Event{ev(103)},
	}
	var p Player
	rec := &recorder{}
	if _, err := p.StartSequenced("song", tl, q, opts, rec.send); err != nil {
		t.Fatalf("StartSequenced: %v", err)
	}
	st := waitDone(t, &p)
	if st.Outcome != "finished" {
		t.Fatalf("outcome = %q", st.Outcome)
	}
	var got []int
	at := map[int]time.Time{}
	rec.mu.Lock()
	for i, e := range rec.events {
		if e.Kind == Control && e.Channel == GlobalChannel {
			got = append(got, e.Data1*1000+e.Data2)
			at[e.Data1*1000+e.Data2] = rec.at[i]
		}
	}
	rec.mu.Unlock()
	if want := []int{104127, 105127, 105000, 103127}; !slices.Equal(got, want) {
		t.Fatalf("sent %v, want rewind, start, finish, stop (%v)", got, want)
	}
	if run := at[105000].Sub(at[105127]); run < q.Duration()-150*time.Millisecond || run > q.Duration() {
		t.Errorf("the finish came %s after the start, want just before the sequence's %s", run, q.Duration())
	}
	if wait := at[103127].Sub(at[105000]); wait < 80*time.Millisecond {
		t.Errorf("the stop came %s after the finish, want 80 ms or more", wait)
	}

	// Stopped while it plays: the finish still goes out before the stop.
	rec = &recorder{}
	if _, err := p.StartSequenced("song", tl, q, opts, rec.send); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for started := false; !started && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, e := range rec.snapshot() {
			started = started || (e.Data1 == 105 && e.Data2 == 127)
		}
	}
	p.Stop()
	waitDone(t, &p)
	got = got[:0]
	for _, e := range rec.snapshot() {
		if e.Kind == Control && e.Channel == GlobalChannel {
			got = append(got, e.Data1*1000+e.Data2)
		}
	}
	if want := []int{104127, 105127, 105000, 103127}; !slices.Equal(got, want) {
		t.Errorf("stopped while playing: sent %v, want %v", got, want)
	}

	// Stopped before the start went out: there is nothing to finish.
	rec = &recorder{}
	slow := func(ctx context.Context, e Event) error {
		if e.Data1 == 104 {
			go p.Stop()
			time.Sleep(20 * time.Millisecond)
		}
		return rec.send(ctx, e)
	}
	if _, err := p.StartSequenced("song", tl, q, opts, slow); err != nil {
		t.Fatal(err)
	}
	waitDone(t, &p)
	for _, e := range rec.snapshot() {
		if e.Data1 == 105 {
			t.Errorf("a playback stopped before its start sent the recorder's control: %+v", e)
		}
	}
}
