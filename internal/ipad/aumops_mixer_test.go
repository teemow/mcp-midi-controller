package ipad

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

func init() { mixerSettle = 20 * time.Millisecond }

// wideStrips are a mix of seven stems as the rig's AUM showed it: nine
// strips, of which five fit in the window.
var wideStrips = []ipadtest.Strip{
	{Title: "Brain"},
	{Title: "piano", Level: "+0.0 dB"},
	{Title: "violins", Level: "-1.0 dB"},
	{Title: "low_strings", Level: "-2.0 dB"},
	{Title: "woodwinds1", Level: "-3.0 dB"},
	{Title: "woodwinds2", Level: "-3.0 dB"},
	{Title: "brass", Level: "-4.0 dB"},
	{Title: "percussion", Level: "-6.0 dB"},
	{Title: "Master", Level: "+0.0 dB"},
}

func stripTitlesOf(strips []ipadtest.Strip) []string {
	out := make([]string, len(strips))
	for i, s := range strips {
		out[i] = s.Title
	}
	return out
}

// playerRows are the rows a stem's FILE PLAYER shows.
func playerRows(stem string) [][2]string {
	return [][2]string{{"Selected file", ""}, {stem + ".wav", ""}, {"Duration", "1'151'993 frames · 00:23.9"}, {"Loop", "OFF"}, {"Sync", "OFF"}}
}

func wideFake(t *testing.T, strips []ipadtest.Strip) (*ipadtest.WDA, *ipadtest.WideMixer, *Client) {
	t.Helper()
	w := ipadtest.New(ipadtest.MixerAUM().Source())
	t.Cleanup(w.Close)
	players := map[string][][2]string{}
	for _, s := range strips {
		if s.Level != "" && s.Title != "Master" {
			players[s.Title] = playerRows(s.Title)
		}
	}
	return w, ipadtest.NewWideMixer(w, "Rhapsody mix", strips, players), New(w.URL)
}

// TestParseAUMMixerScroll reads how far the mixer is scrolled from its scroll
// bar, and keeps out of the tree what AUM keeps out of it.
func TestParseAUMMixerScroll(t *testing.T) {
	// AUM at the mixer's first strip: five strips and the add tile's column,
	// which reaches four points into the window.
	st := parseCaptured(t, "aum-menu-closing.json")
	if st.MixerScroll == nil || *st.MixerScroll != 0 || !st.atMixerStart() || st.AddTile == nil {
		t.Fatalf("captured mixer: scroll %v, add tile %v", st.MixerScroll, st.AddTile)
	}

	win := Size{Width: 1210, Height: 834}
	at := func(scroll float64) AUMState {
		a := ipadtest.AUM{Tempo: "120", Strips: wideStrips, Scroll: scroll, OnScreenOnly: true}
		return ParseAUM(mustParse(t, a.Source()), win)
	}
	start := at(0)
	if got := strings.Join(stripTitlesOf(wideStrips[:5]), ","); strings.Join(titlesOf(start.Channels), ",") != got || start.AddTile != nil || !start.atMixerStart() {
		t.Fatalf("at the start: strips %v, add tile %v, scroll %v", titlesOf(start.Channels), start.AddTile, start.MixerScroll)
	}
	end := at(ipadtest.MaxScroll(len(wideStrips)))
	if strings.Join(titlesOf(end.Channels), ",") != "woodwinds2,brass,percussion,Master" || end.AddTile == nil || *end.MixerScroll != 100 || end.atMixerStart() {
		t.Fatalf("at the end: strips %v, add tile %v, scroll %v", titlesOf(end.Channels), end.AddTile, *end.MixerScroll)
	}
	if mid := at(600); *mid.MixerScroll != 49 || mid.atMixerStart() {
		t.Fatalf("scrolled 600 points: %v %%", *mid.MixerScroll)
	}
}

func titlesOf(chs []AUMChannel) []string {
	out := make([]string, len(chs))
	for i, ch := range chs {
		out[i] = ch.Title
	}
	return out
}

// TestAUMMixerReadsEveryStrip is the rig's mix of seven stems: the tree
// holds five of its nine strips at a time, and the read walks the mixer for
// the others, from wherever it was scrolled to, and leaves it at its start.
func TestAUMMixerReadsEveryStrip(t *testing.T) {
	want := stripTitlesOf(wideStrips)
	for _, from := range []float64{0, 600, ipadtest.MaxScroll(len(wideStrips))} {
		w, m, c := wideFake(t, wideStrips)
		m.SetScroll(from)
		res, err := c.AUMMixer(context.Background())
		if err != nil {
			t.Fatalf("from %.0f: %v", from, err)
		}
		got := res.Data.(AUMLoaded)
		if !slices.Equal(got.Strips, want) || !strings.Contains(res.Message, "9 strip(s)") {
			t.Errorf("from %.0f: strips %v (%s), want %v", from, got.Strips, res.Message, want)
		}
		if m.Scroll() != 0 {
			t.Errorf("from %.0f: the mixer is left scrolled %.0f points, want at its start", from, m.Scroll())
		}
		drags := m.Drags()
		if len(drags) == 0 || len(drags) > 6 {
			t.Errorf("from %.0f: %d drags", from, len(drags))
		}
		for _, g := range drags {
			// Sideways, a step at most: two reads share a strip.
			if g.DY != 0 || math.Abs(g.DX) > 1210-2*242-20 || math.Abs(g.Y-834*0.55) > 0.01 {
				t.Errorf("from %.0f: drag %+v", from, g)
			}
		}
		if n := w.Gestures(); n != len(drags) {
			t.Errorf("from %.0f: %d gestures, want only the %d drags", from, n, len(drags))
		}
	}
}

// TestAUMMixerFindsAMissingStrip checks a strip that is really missing stays
// missing, wherever it would be.
func TestAUMMixerFindsAMissingStrip(t *testing.T) {
	strips := slices.Clone(wideStrips)
	strips = slices.Delete(strips, 6, 7) // brass
	_, m, c := wideFake(t, strips)
	m.SetScroll(300)
	res, err := c.AUMMixer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := res.Data.(AUMLoaded)
	if !slices.Equal(got.Strips, stripTitlesOf(strips)) || slices.Contains(got.Strips, "brass") {
		t.Fatalf("strips = %v", got.Strips)
	}
}

// TestAUMSessionWideMixer reads the session's name from the menu and every
// strip of a mixer wider than the window.
func TestAUMSessionWideMixer(t *testing.T) {
	_, m, c := wideFake(t, wideStrips)
	res, err := c.AUMSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := res.Data.(AUMLoaded)
	if got.Session != "Rhapsody mix" || !slices.Equal(got.Strips, stripTitlesOf(wideStrips)) || !res.Verified {
		t.Fatalf("session = %+v (%s)", got, res.Message)
	}
	if res.State.MenuOpen || m.Scroll() != 0 {
		t.Fatalf("menu open %v, mixer scrolled %.0f", res.State.MenuOpen, m.Scroll())
	}
}

// TestAUMFilePlayerOffScreen reads the File Players of strips that are not in
// the tree: right of the window, and left of it once the mixer has scrolled.
func TestAUMFilePlayerOffScreen(t *testing.T) {
	_, m, c := wideFake(t, wideStrips)
	ctx := context.Background()
	for _, stem := range []string{"percussion", "brass", "piano", "woodwinds2"} {
		res, err := c.AUMFilePlayer(ctx, AUMRequest{Action: "read", Channel: stem})
		if err != nil {
			t.Fatalf("%s: %v", stem, err)
		}
		rows, _ := res.Data.([]AUMRow)
		if len(rows) < 2 || rows[1].Label != stem+".wav" {
			t.Fatalf("%s: rows %+v", stem, rows)
		}
		// The deck closes the panel after each read.
		if _, err := c.AUMPanel(ctx, AUMRequest{Action: "close"}); err != nil {
			t.Fatalf("%s: close: %v", stem, err)
		}
	}
	if got := m.Opened(); !slices.Equal(got, []string{"percussion", "brass", "piano", "woodwinds2"}) {
		t.Fatalf("opened = %v", got)
	}
	// A strip the mixer does not have is looked for along the whole of it.
	_, err := c.AUMFilePlayer(ctx, AUMRequest{Action: "read", Channel: "harp"})
	if err == nil || !strings.Contains(err.Error(), `no channel matches "harp"`) || !strings.Contains(err.Error(), `#9 "Master"`) {
		t.Fatalf("harp: %v", err)
	}
}

// TestAUMChannelOffScreen arms the master right of the window and brings a
// strip into view by its position, counted from the mixer's first strip.
func TestAUMChannelOffScreen(t *testing.T) {
	_, m, c := wideFake(t, wideStrips)
	ctx := context.Background()
	res, err := c.AUMChannel(ctx, AUMRequest{Action: "scroll_to", Channel: "Master"})
	if err != nil || !res.Verified {
		t.Fatalf("scroll_to Master: %+v, %v", res, err)
	}
	if m.Scroll() == 0 {
		t.Fatal("the mixer did not scroll to the master")
	}
	// From the end, #2 is the second strip of the mixer, not of the tree.
	res, err = c.AUMChannel(ctx, AUMRequest{Action: "scroll_to", Channel: "#2"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, `"piano"`) {
		t.Fatalf("scroll_to #2: %+v, %v", res, err)
	}
	if _, err := c.AUMChannel(ctx, AUMRequest{Action: "scroll_to", Channel: "#10"}); err == nil || !strings.Contains(err.Error(), "no strip #10: the mixer has 9") {
		t.Fatalf("scroll_to #10: %v", err)
	}
}
