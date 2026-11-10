package song

import (
	"strings"
	"testing"
)

func TestParseNote(t *testing.T) {
	for in, want := range map[string]int{
		"C4": 60, "A4": 69, "A1": 33, "C#4": 61, "Db4": 61, "Bb2": 46, "b2": 47, "C-1": 0, "G9": 127, "36": 36,
	} {
		if got, err := parseNote(in); err != nil || got != want {
			t.Errorf("parseNote(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "H2", "A", "C#", "A10", "200", "x"} {
		if _, err := parseNote(in); err == nil {
			t.Errorf("parseNote(%q) succeeded, want an error", in)
		}
	}
	if got := NoteName(61); got != "C#4" {
		t.Errorf("NoteName(61) = %q, want C#4", got)
	}
}

func TestParseStep(t *testing.T) {
	for in, want := range map[string]int64{
		"": 120, "1/16": 120, "1/8": 240, "1/4": 480, "1/2": 960, "1": 1920, "1/32": 60, "1/8t": 160, "1/16T": 80, "3/16": 360,
	} {
		if got, err := parseStep(in); err != nil || got != want {
			t.Errorf("parseStep(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"x", "1/0", "0/4", "1/7", "1/"} {
		if _, err := parseStep(in); err == nil {
			t.Errorf("parseStep(%q) succeeded, want an error", in)
		}
	}
}

func TestParseMeter(t *testing.T) {
	for in, want := range map[string]int64{"": 1920, "4/4": 1920, "3/4": 1440, "6/8": 1440, "7/8": 1680} {
		m, err := parseMeter(in)
		if err != nil || m.barTicks != want {
			t.Errorf("parseMeter(%q) bar = %d, %v; want %d", in, m.barTicks, err, want)
		}
	}
	for _, in := range []string{"4", "4/3", "0/4", "x/y"} {
		if _, err := parseMeter(in); err == nil {
			t.Errorf("parseMeter(%q) succeeded, want an error", in)
		}
	}
}

// fourFour is the meter the pattern tests run in.
var fourFour = meter{num: 4, den: 4, barTicks: 4 * PPQ}

func TestMelodicGrid(t *testing.T) {
	p := Pattern{Step: "1/8", Gate: 1, Notes: StringList{"A3 - C4 . [E4 G4]! . A3:50 -"}}
	cp, err := compilePattern("p", p, fourFour, 0, &Track{ID: "t"})
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	if cp.ticks != 4*PPQ {
		t.Errorf("pattern length = %d ticks, want one bar", cp.ticks)
	}
	want := []patNote{
		{at: 0, dur: 480, note: 57, vel: 96},                 // A3 held through its tie
		{at: 480, dur: 240, note: 60, vel: 96},               // C4
		{at: 960, dur: 240, note: 64, vel: 96 + accentBoost}, // the accented chord
		{at: 960, dur: 240, note: 67, vel: 96 + accentBoost},
		{at: 1440, dur: 480, note: 57, vel: 50}, // an explicit velocity, held
	}
	if len(cp.notes) != len(want) {
		t.Fatalf("got %d notes, want %d: %+v", len(cp.notes), len(want), cp.notes)
	}
	for i := range want {
		if cp.notes[i] != want[i] {
			t.Errorf("note %d = %+v, want %+v", i, cp.notes[i], want[i])
		}
	}
}

func TestGridDefaultsAndTransposition(t *testing.T) {
	// Default step 1/16, default gate 0.9; the track and the pattern both
	// transpose.
	p := Pattern{Transpose: -12, Notes: StringList{"C4 . . . . . . . . . . . . . . ."}}
	cp, err := compilePattern("p", p, fourFour, 0, &Track{ID: "t", Transpose: 7})
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	if len(cp.notes) != 1 || cp.notes[0].note != 55 || cp.notes[0].dur != 108 {
		t.Fatalf("notes = %+v, want one G3 of 108 ticks", cp.notes)
	}
	if _, err := compilePattern("p", Pattern{Transpose: 100, Notes: StringList{"C4 . . . . . . . . . . . . . . ."}}, fourFour, 0, &Track{}); err == nil {
		t.Error("a transposition out of the MIDI range compiled")
	}
}

func TestSwing(t *testing.T) {
	p := Pattern{Step: "1/8", Notes: StringList{"C4 C4 C4 C4 C4 C4 C4 C4"}}
	cp, err := compilePattern("p", p, fourFour, 0.5, &Track{})
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	// Every second eighth is half a step late.
	for i, n := range cp.notes {
		want := int64(i) * 240
		if i%2 == 1 {
			want += 120
		}
		if n.at != want {
			t.Errorf("note %d at %d, want %d", i, n.at, want)
		}
	}

	// A swung note's length follows the swung grid: at gate 1 every note ends
	// exactly where the next begins, long step or short.
	p.Gate = 1
	cp, err = compilePattern("p", p, fourFour, 0.33, &Track{})
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	for i := 0; i+1 < len(cp.notes); i++ {
		if end, next := cp.notes[i].at+cp.notes[i].dur, cp.notes[i+1].at; end != next {
			t.Errorf("note %d ends at %d, the next starts at %d", i, end, next)
		}
	}
}

func TestDrumGrid(t *testing.T) {
	p := Pattern{Drums: map[string]string{
		"kick":  "x... x... x... x...",
		"snare": "....X.......X...",
		"hat":   "x.o.x.o.x.o.x.o.",
		"zap":   "...............x",
	}}
	track := &Track{ID: "d", Kind: "drums", DrumMap: map[string]string{"zap": "D3"}}
	cp, err := compilePattern("beat", p, fourFour, 0, track)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	byNote := map[int][]patNote{}
	for _, n := range cp.notes {
		byNote[n.note] = append(byNote[n.note], n)
	}
	if got := len(byNote[36]); got != 4 {
		t.Errorf("kick hits = %d, want 4", got)
	}
	if got := byNote[38]; len(got) != 2 || got[0].at != 480 || got[0].vel != 96+accentBoost {
		t.Errorf("snare = %+v, want two accented hits from beat 2", got)
	}
	if got := byNote[42]; len(got) != 8 || got[1].vel != 53 {
		t.Errorf("hat = %+v, want eight hits, every second soft", got)
	}
	if got := byNote[50]; len(got) != 1 || got[0].at != 15*120 {
		t.Errorf("zap = %+v, want one hit on the last sixteenth (the track's own lane)", got)
	}
}

// TestGridErrors checks the mistakes a hand-written grid makes are named.
func TestGridErrors(t *testing.T) {
	drums := &Track{Kind: "drums"}
	for name, tc := range map[string]struct {
		p     Pattern
		track *Track
		want  string
	}{
		"short bar":       {Pattern{Notes: StringList{"A1 . . . A1 . . . | A1 . ."}}, &Track{}, "bar 1 has 8 steps, a bar holds 16"},
		"not whole bars":  {Pattern{Notes: StringList{"A1 . . ."}}, &Track{}, "not whole bars"},
		"uneven voices":   {Pattern{Step: "1/4", Notes: StringList{"A1 . . .", "C2 . . . C2 . . ."}}, &Track{}, "8 steps, but the pattern's other lines have 4"},
		"uneven lanes":    {Pattern{Drums: map[string]string{"kick": "x...x...x...x...", "snare": "....x.......x.."}}, drums, "15 steps"},
		"bad note":        {Pattern{Step: "1/4", Notes: StringList{"A1 H2 . ."}}, &Track{}, `step 2: "H2" is not a note`},
		"bad drum step":   {Pattern{Drums: map[string]string{"kick": "x..y"}}, drums, `"y" is not a drum step`},
		"unknown lane":    {Pattern{Drums: map[string]string{"gong": "x...x...x...x..."}}, drums, `no drum lane "gong"`},
		"open chord":      {Pattern{Step: "1/4", Notes: StringList{"[A1 C2 . . ."}}, &Track{}, "missing its ]"},
		"bad velocity":    {Pattern{Step: "1/4", Notes: StringList{"A1:300 . . ."}}, &Track{}, "must be 1..127"},
		"too long":        {Pattern{Bars: 1, Step: "1/4", Notes: StringList{"A1 . . . A1 . . ."}}, &Track{}, "the grid is 8 steps but 1 bar(s) hold 4"},
		"bad event":       {Pattern{Events: []NoteEvent{{At: 0, Dur: 0, Note: "A1"}}}, &Track{}, "dur > 0"},
		"bad event note":  {Pattern{Events: []NoteEvent{{At: 0, Dur: 1, Note: "nope"}}}, &Track{}, "is not a note"},
		"bad step":        {Pattern{Step: "1/7", Notes: StringList{"A1"}}, &Track{}, "/step"},
		"empty chord":     {Pattern{Step: "1/4", Notes: StringList{"[] . . ."}}, &Track{}, "an empty chord"},
		"chord and junk":  {Pattern{Step: "1/4", Notes: StringList{"[A1 C2]x . . ."}}, &Track{}, "after a chord's ]"},
		"event on a drum": {Pattern{Events: []NoteEvent{{At: 0, Dur: 1, Note: "gong"}}}, drums, `no drum lane "gong"`},
	} {
		_, err := compilePattern("p", tc.p, fourFour, 0, tc.track)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
}

func TestPatternLength(t *testing.T) {
	// A grid shorter than its declared bars is padded with silence.
	cp, err := compilePattern("p", Pattern{Bars: 2, Step: "1/4", Notes: StringList{"A1 . . ."}}, fourFour, 0, &Track{})
	if err != nil || cp.ticks != 2*4*PPQ {
		t.Errorf("declared length = %d, %v; want two bars", cp.ticks, err)
	}
	// Events alone size the pattern to the bar that holds them.
	cp, err = compilePattern("p", Pattern{Events: []NoteEvent{{At: 5, Dur: 1, Note: "A2 C3", Velocity: 80}}}, fourFour, 0, &Track{})
	if err != nil || cp.ticks != 2*4*PPQ || len(cp.notes) != 2 || cp.notes[0].at != 5*PPQ || cp.notes[0].vel != 80 {
		t.Errorf("events pattern = %+v, %v; want two bars with a two-note chord on beat 6", cp, err)
	}
	// An event extends the pattern by where it starts, not by its tail: a note
	// late in bar 1 that rings into bar 2 leaves the pattern one bar long.
	cp, err = compilePattern("p", Pattern{Step: "1/4", Notes: StringList{"C3 . . ."}, Events: []NoteEvent{{At: 3.5, Dur: 1, Note: "E3"}}}, fourFour, 0, &Track{})
	if err != nil || cp.ticks != 4*PPQ {
		t.Errorf("pattern with a ringing event = %d ticks, %v; want one bar", cp.ticks, err)
	}
	// A leading tie has nothing to hold and is silence.
	cp, err = compilePattern("p", Pattern{Step: "1/4", Notes: StringList{"- A1 - ."}}, fourFour, 0, &Track{})
	if err != nil || len(cp.notes) != 1 || cp.notes[0].at != PPQ {
		t.Errorf("leading tie = %+v, %v; want one note on beat 2", cp, err)
	}
}
