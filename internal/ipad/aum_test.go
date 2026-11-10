package ipad

import (
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

func parseAUM(t *testing.T, a ipadtest.AUM) AUMState {
	t.Helper()
	root, err := ParseSource(a.Source())
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	return ParseAUM(root, Size{Width: 1210, Height: 834})
}

func f(v float64) *float64 { return &v }

func TestParseAUM(t *testing.T) {
	st := parseAUM(t, ipadtest.DefaultAUM())

	if st.Orientation != "landscape" || !st.MenuOpen || st.StartScreen {
		t.Fatalf("orientation/menu/start: %+v", st)
	}
	// The session comes from the open menu, never from the meter's stale label.
	if st.Session != "test-session (mcp test)" {
		t.Fatalf("session = %q", st.Session)
	}
	closed := ipadtest.DefaultAUM()
	closed.Session = ""
	if st := parseAUM(t, closed); st.Session != "" || st.MenuOpen {
		t.Fatalf("menu closed: session %q menu %v", st.Session, st.MenuOpen)
	}
	if st.TempoBPM == nil || *st.TempoBPM != 120 {
		t.Fatalf("tempo: %v", st.TempoBPM)
	}
	if st.Position != "001:1  00:00.0" || st.DSP != "3 %" {
		t.Fatalf("position %q dsp %q", st.Position, st.DSP)
	}

	// The square node buttons repeat titles; only the six title buttons count.
	if len(st.Channels) != 6 {
		t.Fatalf("want 6 channels, got %d: %+v", len(st.Channels), st.Channels)
	}
	midi := st.Channels[0]
	if midi.Kind != "midi" || midi.Number != 1 || midi.Strip != 1 || midi.LevelLabel != "" {
		t.Fatalf("MIDI strip: %+v", midi)
	}
	synth := st.Channels[1]
	if synth.Kind != "audio" || synth.Number != 2 || synth.Name != "Synth One" || synth.LevelDB == nil || *synth.LevelDB != -1 || !synth.OnScreen {
		t.Fatalf("audio strip: %+v", synth)
	}
	if drum := st.Channels[2]; drum.LevelDB != nil || drum.LevelLabel != "-∞ dB" {
		t.Fatalf("-∞ strip: %+v", drum)
	}
	if last := st.Channels[5]; last.OnScreen || last.Strip != 6 {
		t.Fatalf("scrolled-away strip should be off screen: %+v", last)
	}
	if !st.Channels[4].OnScreen {
		t.Fatalf("strip ending at the window edge is on screen: %+v", st.Channels[4])
	}
}

func TestParseAUMStartScreen(t *testing.T) {
	st := parseAUM(t, ipadtest.AUM{Tempo: "98.5", StartScreen: true})
	if !st.StartScreen || st.MenuOpen || len(st.Channels) != 0 {
		t.Fatalf("start screen: %+v", st)
	}
	if st.TempoBPM == nil || *st.TempoBPM != 98.5 {
		t.Fatalf("fractional tempo: %v", st.TempoBPM)
	}
}

func TestChannelSelector(t *testing.T) {
	st := parseAUM(t, ipadtest.DefaultAUM())
	for sel, want := range map[string]string{
		"2":             "2: Synth One",
		"MIDI 1":        "MIDI 1",
		"midi 1":        "MIDI 1",
		"drum":          "3: Drum Machine",
		"5: Bass Synth": "5: Bass Synth",
	} {
		ch, err := st.Channel(sel)
		if err != nil || ch.Title != want {
			t.Errorf("Channel(%q) = %q, %v; want %q", sel, ch.Title, err, want)
		}
	}
	for sel, wantErr := range map[string]string{
		"1":     "no audio channel 1",
		"synth": "matches 4 channels",
		"organ": "no channel matches",
		"   ":   "empty",
	} {
		if _, err := st.Channel(sel); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Channel(%q) error = %v, want %q", sel, err, wantErr)
		}
	}
}

func TestExpectationMismatches(t *testing.T) {
	st := parseAUM(t, ipadtest.DefaultAUM())
	moved := st
	moved.Position = "001:2  00:00.5"

	cases := []struct {
		name string
		e    Expectation
		prev *AUMState
		want []string // substrings, one per mismatch; nil = match
	}{
		{"tempo hit", Expectation{TempoBPM: f(120.02)}, nil, nil},
		{"tempo miss", Expectation{TempoBPM: f(128)}, nil, []string{"want 128 BPM, got 120"}},
		{"tempo changed", Expectation{NotTempoBPM: f(110)}, nil, nil},
		{"tempo unchanged", Expectation{NotTempoBPM: f(120)}, nil, []string{"still 120"}},
		{"session", Expectation{Session: "TEST-SESSION (mcp test)"}, nil, nil},
		{"session with extension", Expectation{Session: "test-session (mcp test).aumproj"}, nil, nil},
		{"session miss", Expectation{Session: "test-session"}, nil, []string{`want "test-session"`}},
		{"meter label ignored", Expectation{Session: "Old Session"}, nil, []string{`want "Old Session"`}},
		{"level hit", Expectation{Channels: []ChannelExpectation{{Channel: "2", LevelDB: f(-1.3)}}}, nil, nil},
		{"level miss", Expectation{Channels: []ChannelExpectation{{Channel: "2", LevelDB: f(-6)}}}, nil, []string{"want -6.0 dB, got -1.0 dB"}},
		{"level tolerance", Expectation{Channels: []ChannelExpectation{{Channel: "2", LevelDB: f(-6), ToleranceDB: 5}}}, nil, nil},
		{"level -inf", Expectation{Channels: []ChannelExpectation{{Channel: "drum", LevelDB: f(-200)}}}, nil, nil},
		{"level not -inf", Expectation{Channels: []ChannelExpectation{{Channel: "2", LevelDB: f(-100)}}}, nil, []string{"want -∞ dB"}},
		{"level changed", Expectation{Channels: []ChannelExpectation{{Channel: "2", NotLevelDB: f(-6)}}}, nil, nil},
		{"level unchanged", Expectation{Channels: []ChannelExpectation{{Channel: "2", NotLevelDB: f(-1)}}}, nil, []string{"level still -1.0 dB"}},
		{"midi strip has no level", Expectation{Channels: []ChannelExpectation{{Channel: "MIDI 1", LevelDB: f(0)}}}, nil, []string{"no fader level"}},
		{"unknown channel", Expectation{Channels: []ChannelExpectation{{Channel: "9", LevelDB: f(0)}}}, nil, []string{"no audio channel 9"}},
		{"transport first read", Expectation{Transport: "playing"}, nil, []string{"needs a second read"}},
		{"transport playing", Expectation{Transport: "playing"}, &st, []string{"stays at"}},
		{"transport stopped", Expectation{Transport: "stopped"}, &st, nil},
		{"two misses", Expectation{TempoBPM: f(90), Session: "x"}, nil, []string{"tempo", "session"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.e.Mismatches(c.prev, st)
			if len(got) != len(c.want) {
				t.Fatalf("mismatches %q, want %d like %q", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Fatalf("mismatch %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
	if got := (Expectation{Transport: "playing"}).Mismatches(&st, moved); len(got) != 0 {
		t.Fatalf("advancing position should read as playing: %q", got)
	}
	noName := st
	noName.Session = ""
	if got := (Expectation{Session: "x"}).Mismatches(nil, noName); len(got) != 1 || !strings.Contains(got[0], "target menu") {
		t.Fatalf("hidden session name: %q", got)
	}
}

func TestExpectationValidate(t *testing.T) {
	for _, e := range []Expectation{
		{},
		{Transport: "running"},
		{Channels: []ChannelExpectation{{Channel: "2"}}},
		{Channels: []ChannelExpectation{{LevelDB: f(0)}}},
	} {
		if e.Validate() == nil {
			t.Errorf("Validate(%+v) = nil, want an error", e)
		}
	}
	if err := (Expectation{Transport: "stopped"}).Validate(); err != nil {
		t.Fatalf("valid expectation rejected: %v", err)
	}
}

func TestAUMTarget(t *testing.T) {
	for name, want := range map[string][2]float64{
		"play":     {156, 33},
		"menu":     {1185, 33},
		"keyboard": {52, 817},
	} {
		x, y, err := AUMTarget(name, Size{Width: 1210, Height: 834})
		if err != nil || x != want[0] || y != want[1] {
			t.Errorf("AUMTarget(%q) = %g,%g,%v; want %v", name, x, y, err, want)
		}
	}
	// Portrait keeps the right-hand offsets.
	if x, _, _ := AUMTarget("menu", Size{Width: 834, Height: 1210}); x != 809 {
		t.Errorf("portrait menu x = %g, want 809", x)
	}
	if _, _, err := AUMTarget("record", Size{}); err == nil || !strings.Contains(err.Error(), "known: tempo") {
		t.Errorf("record must stay unknown, got %v", err)
	}
}
