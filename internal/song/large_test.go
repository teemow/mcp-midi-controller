package song

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// manyTracks writes a song of n melodic tracks that all play one pattern.
// group gives a track's group by its position ("" for none) and extra what
// else its declaration holds, as YAML lines indented by four blanks.
func manyTracks(n int, group func(i int) string, extra func(i int) string) string {
	var b strings.Builder
	b.WriteString("title: Many\ntempo: 120\ntracks:\n")
	play := make([]string, n)
	for i := range n {
		fmt.Fprintf(&b, "  - id: t%d\n    instrument: {plugin: synth, preset: Warm Pad, params: {Cutoff: 0.5}}\n", i+1)
		if g := group(i); g != "" {
			fmt.Fprintf(&b, "    group: %s\n", g)
		}
		if extra != nil {
			b.WriteString(extra(i))
		}
		play[i] = fmt.Sprintf("t%d: riff", i+1)
	}
	b.WriteString("master:\n  - {id: glue, plugin: reverb, params: {Mix: 0.2}}\n")
	b.WriteString("patterns:\n  riff: {step: 1/4, notes: \"C3 E3 G3 .\"}\n  ctl: {step: 1/4, channel: 12, notes: \"C1 . . .\"}\n")
	b.WriteString("scenes:\n  open:\n    t1: {params: {Cutoff: 0.9}}\n    master: {effects: {glue: {params: {Mix: 0.4}}}}\n")
	fmt.Fprintf(&b, "sections:\n  a: {bars: 2, play: {%s}}\n  b: {bars: 2, scene: open, play: {%s}}\narrangement: [a, b]\n", strings.Join(play, ", "), strings.Join(play, ", "))
	return b.String()
}

// thirds groups tracks by threes: g1 is the first three, g2 the next.
func thirds(i int) string { return fmt.Sprintf("g%d", i/3+1) }

// TestTwelveTracksKeepTheirSessions pins what a song of twelve tracks or
// fewer makes of itself, as it was before a song could have more: the
// fingerprint of its session, of what its sequencer plays, and of a stem's.
// A stored song whose fingerprint moved would have its session and every
// take of its stems called out of date.
func TestTwelveTracksKeepTheirSessions(t *testing.T) {
	// Two tracks that have swapped their channels; and eleven with a control
	// channel where the twelfth would play.
	swapped := func(i int) string {
		switch i {
		case 4:
			return "    channel: 11\n"
		case 10:
			return "    channel: 5\n"
		}
		return ""
	}
	twelve := manyTracks(12, thirds, swapped)
	eleven := manyTracks(11, thirds, func(i int) string {
		if i == 10 {
			return "    channel: 5\n    control_channels: [12]\n"
		}
		return swapped(i)
	})
	eleven = strings.Replace(eleven, "t11: riff", "t11: [riff, ctl]", 1)
	// The fingerprints of the session, of the sequence, of the stem's session
	// and of the stem's sequence, as the code before this change gave them.
	for _, tc := range []struct {
		name, doc string
		stem      int
		want      [4]string
	}{
		{"three tracks", strings.Replace(testSong, "  - id: lead\n", "  - id: lead\n    group: band\n", 1), 1,
			[4]string{"119de2421092b0ff", "5d1870b1b1c06e25", "b63825b3e4590927", "ec6a7a9337a43ff9"}},
		{"twelve tracks", twelve, 3,
			[4]string{"0c550983f45beb4e", "75ac0150d19151f1", "d722d0632235de55", "79023091a8da8961"}},
		{"eleven tracks and a control channel", eleven, 3,
			[4]string{"0b434522fce98d2c", "ce3b59e53c31e7fa", "f2393a99c57a4e09", "77fa7f55143c9610"}},
	} {
		s := mustParse(t, tc.doc)
		tl, err := Compile(s, mustPlan(t, s))
		if err != nil {
			t.Fatalf("%s: Compile: %v", tc.name, err)
		}
		stems, err := s.Stems(nil)
		if err != nil {
			t.Fatalf("%s: Stems: %v", tc.name, err)
		}
		l, err := s.StemLayout(0)
		if err != nil {
			t.Fatalf("%s: StemLayout: %v", tc.name, err)
		}
		st, err := s.StemSong(stems[tc.stem], "Stem", l)
		if err != nil {
			t.Fatalf("%s: StemSong: %v", tc.name, err)
		}
		stl, err := Compile(st, mustPlan(t, st))
		if err != nil {
			t.Fatalf("%s: the stem does not compile: %v", tc.name, err)
		}
		got := [4]string{tl.Fingerprint, tl.Sequence().Fingerprint(), stl.Fingerprint, stl.Sequence().Fingerprint()}
		if got != tc.want {
			t.Errorf("%s: fingerprints (session, sequence, stem's session, stem's sequence) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// with adds lines to the declaration of the tracks at the positions given.
func with(lines map[int]string) func(int) string {
	return func(i int) string { return lines[i] }
}

// TestStemsOnlyFits checks what lets a song have more tracks than a session
// hosts: stems of at most twelve tracks each, and at most twelve of them.
func TestStemsOnlyFits(t *testing.T) {
	// Fifteen tracks in five stems of three.
	s := mustParse(t, manyTracks(15, thirds, nil))
	if !s.StemsOnly() {
		t.Fatal("a song of fifteen tracks is not taken for one of stems only")
	}
	stems, err := s.Stems(nil)
	if err != nil || len(stems) != 5 || stems[4].Name != "g5" || len(stems[4].Tracks) != 3 {
		t.Fatalf("stems = %+v, %v; want five of three", stems, err)
	}
	// Twelve tracks fit however they are grouped, and are no song of stems.
	if small := mustParse(t, manyTracks(12, func(int) string { return "" }, nil)); small.StemsOnly() {
		t.Error("a song of twelve tracks is taken for one of stems only")
	}

	for _, tc := range []struct {
		name string
		n    int
		by   func(int) string
		want []string
	}{
		{"thirteen tracks in no group", 13, func(int) string { return "" },
			[]string{"/tracks: 13 tracks in 13 stems (t1, t2,", "One session hosts 12 tracks at most", "12 stems fit at most", "group: on a track"}},
		{"a stem of fourteen", 14, func(int) string { return "all" },
			[]string{`/tracks: the stem "all" has 14 tracks (t1, t2,`, "one session hosts 12 at most", "split it"}},
		{"thirteen stems of three", 39, thirds,
			[]string{"/tracks: 39 tracks in 13 stems (g1 of 3, g2 of 3,", "12 stems fit at most"}},
		{"a stem too large among others", 16, func(i int) string {
			if i < 13 {
				return "big"
			}
			return ""
		}, []string{`the stem "big" has 13 tracks`}},
	} {
		_, err := Parse([]byte(manyTracks(tc.n, tc.by, nil)))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v, want it to say %q", tc.name, err, want)
			}
		}
		// The stem's size is the one thing wrong: no channel past the twelfth
		// is reported on top of it.
		if strings.Contains(err.Error(), "is not within 1..12") {
			t.Errorf("%s: the error also counts channels: %v", tc.name, err)
		}
	}

	// A grouping given as an argument is held to the same.
	for _, tc := range []struct {
		groups map[string][]string
		want   string
	}{
		{map[string][]string{"a": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12", "t13"}}, `/groups: the stem "a" has 13 tracks`},
		{map[string][]string{"a": {"t1", "t2"}}, "/groups: 15 tracks in 14 stems (a of 2, t3,"},
	} {
		if _, err := s.Stems(tc.groups); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Stems(%v): err = %v, want %q", tc.groups, err, tc.want)
		}
	}
	halves := map[string][]string{"low": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}, "high": {"t9", "t10", "t11", "t12", "t13", "t14", "t15"}}
	if stems, err := s.Stems(halves); err != nil || len(stems) != 2 || len(stems[0].Tracks) != 8 {
		t.Errorf("Stems of two halves = %+v, %v", stems, err)
	}
}

// channelSong is fifteen tracks in five stems of three. In g2, t4 names its
// channel and a control channel and plays control notes on it; t1 names the
// same two in g1.
func channelSong(lines map[int]string) string {
	all := map[int]string{0: "    channel: 9\n    control_channels: [12]\n", 3: "    channel: 9\n    control_channels: [12]\n"}
	for i, l := range lines {
		all[i] = l
	}
	return strings.ReplaceAll(manyTracks(15, thirds, with(all)), "t4: riff", "t4: [riff, ctl]")
}

// TestStemsOnlyChannels checks a song of stems only counts its MIDI channels
// in each stem: a track's default channel is its position there, and a
// channel or a control channel is taken within the stem alone.
func TestStemsOnlyChannels(t *testing.T) {
	s := mustParse(t, channelSong(nil))
	want := []int{9, 2, 3, 9, 2, 3, 1, 2, 3, 1, 2, 3, 1, 2, 3}
	for i := range s.Tracks {
		if got := s.TrackChannel(i); got != want[i] {
			t.Errorf("track %s plays on channel %d, want %d: its own, or its position in its stem", s.Tracks[i].ID, got, want[i])
		}
	}
	// The notes compile without a session, on those channels.
	tl, err := Compile(s, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(tl.Tracks) != 15 || tl.Tracks[3].Channel != 9 || tl.Tracks[3].ControlNotes == 0 || tl.Tracks[4].Channel != 2 || tl.Fingerprint != "" {
		t.Errorf("the notes' tracks = %+v, want fifteen on their stems' channels and t4's control notes", tl.Tracks)
	}

	own := "  own: {step: 1/4, channel: 2, notes: \"C1 . . .\"}\n  ctl:"
	for _, tc := range []struct {
		name  string
		lines map[int]string
		edit  [2]string
		want  string
	}{
		{name: "a channel named twice in a stem", lines: map[int]string{4: "    channel: 9\n"},
			want: `/tracks/4/channel: MIDI channel 9 is already track "t4"'s in the stem "g2"`},
		{name: "a channel named where the next track sits", lines: map[int]string{3: "    channel: 2\n"},
			want: `/tracks/4/channel: MIDI channel 2 is already track "t4"'s in the stem "g2"`},
		{name: "a channel past the twelfth", lines: map[int]string{3: "    channel: 13\n"},
			want: "/tracks/3/channel: 13 is not within 1..12"},
		{name: "a control channel another track of the stem plays on", lines: map[int]string{4: "    control_channels: [3]\n"},
			want: `/tracks/4/control_channels/0: MIDI channel 3 is track "t6"'s in the stem "g2" (give that track another channel)`},
		{name: "a control channel two tracks of a stem listen on", lines: map[int]string{4: "    control_channels: [12]\n"},
			want: `/tracks/4/control_channels/0: MIDI channel 12 is already a control channel of track "t4" in the stem "g2"`},
		{name: "a control channel that is the track's own", lines: map[int]string{4: "    control_channels: [2]\n"},
			want: "/tracks/4/control_channels/0: MIDI channel 2 is the track's own channel"},
		{name: "control notes on a channel the track does not list", edit: [2]string{"t5: riff", "t5: [riff, ctl]"},
			want: `pattern "ctl" sends on channel 12, which is not one of "t5"'s control_channels (none)`},
		{name: "control notes on the channel the track has in its stem", edit: [2]string{"t5: riff", "t5: [riff, own]"},
			want: `pattern "own" sends on channel 2, which is "t5"'s own channel`},
	} {
		doc := strings.Replace(channelSong(tc.lines), "  ctl:", own, 1)
		if tc.edit[0] != "" {
			doc = strings.ReplaceAll(doc, tc.edit[0], tc.edit[1])
		}
		if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
}

// TestStemsOnlyPlan checks how a song of stems only is planned: not as one
// session, but its names on the whole document and each stem as a song of its
// own, with the channels counted in the stem and the mix a track per stem.
func TestStemsOnlyPlan(t *testing.T) {
	doc := channelSong(nil)
	s := mustParse(t, doc)
	if _, err := NewPlan(s, testCatalog()); err == nil || !strings.Contains(err.Error(), "no session of its own") {
		t.Errorf("NewPlan of fifteen tracks: err = %v, want it refused as one session", err)
	}
	if err := Check(s, testCatalog()); err != nil {
		t.Errorf("Check: %v", err)
	}
	// What a name does not resolve to is reported where the document has it:
	// t14 is the second track of its stem, and its ramp the stem's first.
	bad := strings.Replace(doc, "  - id: t14\n    instrument: {plugin: synth,", "  - id: t14\n    instrument: {plugin: organ,", 1)
	if err := Check(mustParse(t, bad), testCatalog()); err == nil || !strings.Contains(err.Error(), `/tracks/13/instrument/plugin: no staged AUv3 probe "organ"`) {
		t.Errorf("Check of an unknown plugin: err = %v, want its place in the whole document", err)
	}
	bad = strings.Replace(doc, "  b: {bars: 2, scene: open,", "  b: {bars: 2, scene: open, automation: [{track: t1, param: Cutoff, from: 0, to: 1}, {track: t14, param: Wobble, from: 0, to: 1}],", 1)
	if err := Check(mustParse(t, bad), testCatalog()); err == nil || !strings.Contains(err.Error(), "/sections/b/automation/1/param: Test Synth has no writable parameter") {
		t.Errorf("Check of an unknown parameter in a ramp: err = %v, want its place in the whole document", err)
	}

	whole, err := Compile(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.StemLayout(0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	at := 0 // the stem's first track in the whole song
	for k, st := range stems {
		sg, err := s.StemSong(st, "Many stem "+st.Name, l)
		if err != nil {
			t.Fatalf("StemSong %s: %v", st.Name, err)
		}
		p := mustPlan(t, sg)
		tl, err := Compile(sg, p)
		if err != nil {
			t.Fatalf("the stem %s does not compile: %v", st.Name, err)
		}
		wantCh, pool := []int{1, 2, 3}, 4
		if k < 2 {
			// A named channel and a control channel: the controls start above.
			wantCh, pool = []int{9, 2, 3}, 13
		}
		for j, tp := range p.Tracks {
			if tp.Channel != wantCh[j] || tp.Strip != BrainStrip+1+j {
				t.Errorf("stem %s: track %s is on channel %d, strip %d; want channel %d, strip %d", st.Name, tp.ID, tp.Channel, tp.Strip, wantCh[j], BrainStrip+1+j)
			}
			if got, want := tl.Tracks[j].Notes, whole.Tracks[at+j].Notes; got != want || got == 0 {
				t.Errorf("stem %s: track %s plays %d notes, in the whole song %d", st.Name, tp.ID, got, want)
			}
		}
		if p.ControlChannels[0] != pool {
			t.Errorf("stem %s: the control channels start at %d, want %d, above the stem's own tracks", st.Name, p.ControlChannels[0], pool)
		}
		if len(sg.Master) != 0 || sg.StemsOnly() {
			t.Errorf("stem %s: %d tracks, master %+v", st.Name, len(sg.Tracks), sg.Master)
		}
		files[st.Name] = "Many stem " + st.Name + "/Master - 120 bpm - 001.wav"
		at += len(st.Tracks)
	}

	// The mix is a song of one session: a file per stem under the master.
	mix, err := s.MixSong(stems, files, "Many mix", l)
	if err != nil {
		t.Fatalf("MixSong: %v", err)
	}
	if len(mix.Tracks) != 5 || mix.StemsOnly() || !mix.Tracks[4].IsFile() || mix.Tracks[4].ID != "g5" || len(mix.Master) != 1 {
		t.Errorf("the mix = %d tracks, master %+v; want a file per stem under the song's master effect", len(mix.Tracks), mix.Master)
	}
	if _, err := Compile(mix, mustPlan(t, mix)); err != nil {
		t.Errorf("the mix does not compile: %v", err)
	}

	// Another grouping, given as an argument: channels are counted in it. t4
	// keeps the channel it names, the others take their place in the stem.
	halves := map[string][]string{"low": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}, "high": {"t9", "t10", "t11", "t12", "t13", "t14", "t15"}}
	// t1 and t4 both name channel 9 and both listen on 12: fine in two stems,
	// not in one.
	regrouped, err := s.Stems(halves)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StemSong(regrouped[0], "low", l); err == nil || !strings.Contains(err.Error(), `the stem "low": `) || !strings.Contains(err.Error(), `/tracks/3/channel: MIDI channel 9 is already track "t1"'s`) {
		t.Errorf("a stem whose tracks meet on a channel: err = %v", err)
	}
	high, err := s.StemSong(regrouped[1], "high", l)
	if err != nil {
		t.Fatalf("StemSong high: %v", err)
	}
	for j, tr := range high.Tracks {
		if tr.Channel != j+1 {
			t.Errorf("regrouped: track %s is on channel %d, want its place in the stem, %d", tr.ID, tr.Channel, j+1)
		}
	}
}

// TestDocsLargeExample keeps docs/songs.md honest about a song of more than
// twelve tracks: the piece it shows is one this package accepts, in the stems
// and on the channels the text says.
func TestDocsLargeExample(t *testing.T) {
	doc, err := os.ReadFile("../../docs/songs.md")
	if err != nil {
		t.Fatalf("read the songs doc: %v", err)
	}
	_, rest, ok := strings.Cut(string(doc), "```yaml\ntitle: Chamber Piece")
	body, _, closed := strings.Cut(rest, "\n```")
	if !ok || !closed {
		t.Fatal("docs/songs.md has no example of a song of more than twelve tracks (a yaml block starting with title: Chamber Piece)")
	}
	s, err := Parse([]byte("title: Chamber Piece" + body))
	if err != nil {
		t.Fatalf("the example in docs/songs.md is not a valid song: %v", err)
	}
	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.StemsOnly() || len(s.Tracks) != 14 || len(stems) != 5 || stems[1].Name != "winds" || len(stems[1].Tracks) != 4 {
		t.Errorf("the example is %d tracks in the stems %+v, want fourteen in five", len(s.Tracks), stems)
	}
	if b := s.StemStrips["brass"]; len(s.StemStrips) != 2 || b.Level == nil || *b.Level != -2 || len(b.Effects) != 1 {
		t.Errorf("the example's strips in the mix are %+v, want the strings' and the brass's", s.StemStrips)
	}
	tl, err := Compile(s, nil)
	if err != nil {
		t.Fatalf("the example in docs/songs.md does not compile: %v", err)
	}
	if tl.Bars != 16 {
		t.Errorf("the example compiles to %d bars, want 16", tl.Bars)
	}
	// The first track of every stem plays on channel 1.
	for i, id := range []string{"violins1", "flute", "horns", "piano", "timpani"} {
		for j := range s.Tracks {
			if s.Tracks[j].ID == id && s.TrackChannel(j) != 1 {
				t.Errorf("stem %d: %s plays on channel %d, want 1", i, id, s.TrackChannel(j))
			}
		}
	}
	l, err := s.StemLayout(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stems {
		if _, err := s.StemSong(st, "Chamber Piece stem "+st.Name, l); err != nil {
			t.Errorf("the stem %s: %v", st.Name, err)
		}
	}
}

// TestStemStrips checks a song's own strips for its stems in their mix: what
// is accepted and refused, with its place in the document, that the mix plays
// each stem's recording through its strip, and that a stem is recorded
// without it.
func TestStemStrips(t *testing.T) {
	strips := "stems:\n" +
		"  g1:\n    level: -2.5\n    effects:\n      - {id: echo, plugin: delay, params: {Mix: 0.3}}\n      - {id: room, plugin: reverb, preset: Large Hall, params: {Size: 0.8}}\n" +
		"  g3: {effects: [{plugin: reverb, params: {Mix: 0.1}}]}\n"
	plain := channelSong(nil)
	doc := strings.Replace(plain, "patterns:\n", strips+"patterns:\n", 1)
	s := mustParse(t, doc)
	if err := Check(s, testCatalog()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	for _, tc := range []struct{ name, from, to, want string }{
		{"a stem the song does not have", "  g3: {", "  g9: {", `/stems/g9: the song has no stem "g9" (its stems are g1, g2, g3, g4, g5)`},
		{"a level above the fader", "level: -2.5", "level: 7", "/stems/g1/level: 7 dB is above the fader's +6 dB"},
		{"two effects of one id", "{id: room, plugin: reverb", "{id: echo, plugin: reverb", `/stems/g1/effects/1/id: duplicate effect "echo"`},
		{"a file as an effect", "{id: echo, plugin: delay,", `{id: echo, file: "x/y.wav", plugin: delay,`, "/stems/g1/effects/0/file: only a track's instrument can be a file"},
	} {
		bad := strings.Replace(doc, tc.from, tc.to, 1)
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// What only the staged plugins can tell is said by Check, with the place
	// in the document as well.
	for _, tc := range []struct{ name, from, to, want string }{
		{"an unknown plugin", "plugin: delay,", "plugin: organ,", `/stems/g1/effects/0/plugin: no staged AUv3 probe "organ"`},
		{"an unknown parameter", "params: {Size: 0.8}", "params: {Wobble: 0.8}", "/stems/g1/effects/1/params/Wobble: Test Reverb has no writable parameter"},
		{"a value out of range", "params: {Mix: 0.3}", "params: {Mix: 3}", "/stems/g1/effects/0/params/Mix:"},
		{"an unknown preset", "preset: Large Hall", "preset: Cathedral", "/stems/g1/effects/1/preset:"},
	} {
		bad := mustParse(t, strings.Replace(doc, tc.from, tc.to, 1))
		if err := Check(bad, testCatalog()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}

	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.StemLayout(0)
	if err != nil {
		t.Fatal(err)
	}
	// A stem is recorded without its strip: its song, and so its take, is the
	// same with and without one.
	without := mustParse(t, plain)
	files := map[string]string{}
	for _, st := range stems {
		sg, err := s.StemSong(st, "Many stem "+st.Name, l)
		if err != nil {
			t.Fatalf("StemSong %s: %v", st.Name, err)
		}
		bare, err := without.StemSong(st, "Many stem "+st.Name, l)
		if err != nil {
			t.Fatalf("StemSong %s without strips: %v", st.Name, err)
		}
		if sg.StemStrips != nil {
			t.Errorf("stem %s carries the strips %+v", st.Name, sg.StemStrips)
		}
		a, err := Compile(sg, mustPlan(t, sg))
		if err != nil {
			t.Fatal(err)
		}
		b, err := Compile(bare, mustPlan(t, bare))
		if err != nil {
			t.Fatal(err)
		}
		if a.Fingerprint != b.Fingerprint || a.Sequence().Fingerprint() != b.Sequence().Fingerprint() {
			t.Errorf("stem %s: the strips change its session (%s, %s) from (%s, %s)", st.Name, a.Fingerprint, a.Sequence().Fingerprint(), b.Fingerprint, b.Sequence().Fingerprint())
		}
		files[st.Name] = "Many stem " + st.Name + "/Master - 120 bpm - 001.wav"
	}

	// The mix plays each recording through its stem's strip, and a stem
	// without one at unity and dry.
	mix, err := s.MixSong(stems, files, "Many mix", l)
	if err != nil {
		t.Fatalf("MixSong: %v", err)
	}
	g1, g2, g3 := mix.Tracks[0], mix.Tracks[1], mix.Tracks[2]
	if g1.ID != "g1" || g1.Level == nil || *g1.Level != -2.5 || len(g1.Effects) != 2 || g1.Effects[1].Preset != "Large Hall" {
		t.Errorf("the mix's g1 = level %v, effects %+v; want -2.5 dB through the delay and the reverb", g1.Level, g1.Effects)
	}
	if g2.Level != nil || len(g2.Effects) != 0 {
		t.Errorf("the mix's g2 = level %v, effects %+v; want unity and dry", g2.Level, g2.Effects)
	}
	if g3.Level != nil || len(g3.Effects) != 1 {
		t.Errorf("the mix's g3 = level %v, effects %+v; want unity through the reverb", g3.Level, g3.Effects)
	}
	if mix.StemStrips != nil {
		t.Errorf("the mix carries the strips %+v; its tracks are them", mix.StemStrips)
	}
	p := mustPlan(t, mix)
	tl, err := Compile(mix, p)
	if err != nil {
		t.Fatalf("the mix does not compile: %v", err)
	}
	if n := len(p.Tracks[0].Nodes); n != 3 || p.Tracks[0].Nodes[1].Name != "Test Delay" || p.Tracks[0].Nodes[2].Name != "Test Reverb" {
		t.Errorf("the mix's first strip hosts %d nodes, want the file player, the delay and the reverb", n)
	}
	unity := mustParse(t, plain)
	plainMix, err := unity.MixSong(stems, files, "Many mix", l)
	if err != nil {
		t.Fatal(err)
	}
	ptl, err := Compile(plainMix, mustPlan(t, plainMix))
	if err != nil {
		t.Fatal(err)
	}
	if tl.Fingerprint == ptl.Fingerprint {
		t.Errorf("the mix's session is the same with and without the strips (%s)", tl.Fingerprint)
	}
	// The mix is stored as a document and read back as the same song.
	body, err := mix.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back := mustParse(t, string(body))
	if !strings.Contains(string(body), "level: -2.5") || len(back.Tracks[0].Effects) != 2 || back.Tracks[0].Effects[0].EffectID() != "echo" {
		t.Errorf("the stored mix lost the strip:\n%s", body)
	}

	if left := s.StripsLeftOut(stems); len(left) != 0 {
		t.Errorf("the document's own stems leave out the strips %v", left)
	}

	// Takes of another grouping are of other stems: a strip was written for
	// the tracks of the document's stem, and plays for no other, not even a
	// stem of its name. The strips a mix of them leaves out are named.
	for _, tc := range []struct {
		name   string
		groups map[string][]string
		left   []string
	}{
		{"other names", map[string][]string{"front": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}, "back": {"t9", "t10", "t11", "t12", "t13", "t14", "t15"}}, []string{"g1", "g3"}},
		{"a strip's name for other tracks", map[string][]string{"g1": {"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}, "g3": {"t9", "t10", "t11", "t12", "t13", "t14", "t15"}}, []string{"g1", "g3"}},
	} {
		other, err := s.Stems(tc.groups)
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, st := range other {
			files[st.Name] = st.Name + "/Master - 120 bpm - 001.wav"
		}
		m, err := s.MixSong(other, files, "Many mix", l)
		if err != nil {
			t.Fatalf("%s: MixSong: %v", tc.name, err)
		}
		for _, tr := range m.Tracks {
			if tr.Level != nil || len(tr.Effects) != 0 {
				t.Errorf("%s: the mix's %s plays through %v dB, %+v; want unity and dry", tc.name, tr.ID, tr.Level, tr.Effects)
			}
		}
		if left := s.StripsLeftOut(other); !slices.Equal(left, tc.left) {
			t.Errorf("%s: strips left out = %v, want %v", tc.name, left, tc.left)
		}
	}

	// A song of one session has stems too, and strips for them: a track
	// without a group is a stem by its id.
	small := strings.Replace(testSong, "patterns:\n", "stems:\n  bass: {level: -3}\npatterns:\n", 1)
	if _, err := Parse([]byte(small)); err != nil {
		t.Errorf("a strip for an ungrouped track's stem: %v", err)
	}
}
