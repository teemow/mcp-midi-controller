package ipad

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
)

// The first viewport contains five or six strips; the add tile's viewport
// contains four. AUM can keep that viewport after adding, or follow the new
// tail and still list only four strips. Neither count is a session total.
func TestAUMChannelAddAcrossViewports(t *testing.T) {
	for _, action := range []string{"add_audio", "add_midi", "restore", "import"} {
		for _, from := range []float64{0, 600} {
			for _, outcome := range []string{"keep_old_tail", "follow_tail", "unchanged", "more_old_strips", "different_old_view"} {
				t.Run(fmt.Sprintf("%s/from_%g/%s", action, from, outcome), func(t *testing.T) {
					start := ipadtest.AUM{Tempo: "120", Strips: wideStrips, Scroll: from, OnScreenOnly: true}
					end := start
					end.Scroll = ipadtest.MaxScroll(len(end.Strips))
					menu := end
					menu.AddMenu = true
					after := end
					wantAdded := outcome == "keep_old_tail" || outcome == "follow_tail"
					newStrips := []ipadtest.Strip{{Title: "CHAN 10", Level: "+0.0 dB"}}
					switch action {
					case "add_midi":
						newStrips = []ipadtest.Strip{{Title: "MIDI 10"}}
					case "restore":
						newStrips = []ipadtest.Strip{{Title: "Restored", Level: "+0.0 dB"}}
					case "import":
						newStrips = []ipadtest.Strip{{Title: "Imported MIDI"}, {Title: "Imported audio", Level: "+0.0 dB"}}
					}
					if wantAdded {
						after.Strips = append(slices.Clone(end.Strips), newStrips...)
					}
					switch outcome {
					case "keep_old_tail":
						// Keep room for both imported strips while still
						// showing more strips than the add tile's viewport.
						after.Scroll += float64(len(newStrips)-1) * defaultPitch
					case "follow_tail":
						after.Scroll = ipadtest.MaxScroll(len(after.Strips))
					case "more_old_strips":
						// The add failed but scrolling exposes another existing
						// strip. A count increase must not verify this operation.
						after.Scroll -= 100
					case "different_old_view":
						// No old tail strip is in view, but neither is the tile:
						// these unfamiliar strips are not evidence of an add.
						after.Scroll = 0
					}
					win := Size{Width: 1210, Height: 834}
					beforeState := ParseAUM(mustParse(t, end.Source()), win)
					afterState := ParseAUM(mustParse(t, after.Source()), win)
					if st := ParseAUM(mustParse(t, start.Source()), win); st.AddTile != nil {
						t.Fatal("fixture must start away from the add tile")
					}
					if outcome == "follow_tail" && len(afterState.Channels) != len(beforeState.Channels) {
						t.Fatal("fixture must retain the visible strip count after adding")
					}
					if outcome == "more_old_strips" && len(afterState.Channels) <= len(beforeState.Channels) {
						t.Fatal("fixture must expose more existing strips without adding")
					}

					screens := []ipadtest.AUM{start, end, menu}
					req := AUMRequest{Action: action}
					wantGestures := 3 // scroll, open the tile, choose its item
					if action == "restore" || action == "import" {
						pop := end
						pop.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "IMPORT", Rows: [][2]string{
							{"Restore Removed Channel", ""}, {"Scratch (mcp test)", ""},
						}}}
						screens = append(screens, pop)
						wantGestures++
						if action == "import" {
							req.Name = "Scratch (mcp test)"
							pick := end
							pick.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "Scratch (mcp test)", Buttons: []string{"IMPORT 2 CHANNELS"}}}
							screens = append(screens, pick)
							wantGestures++
							// A successful import also dismisses the remaining
							// popover, after verifying the new strips behind it.
							if wantAdded {
								covered := after
								covered.Overlays = pick.Overlays
								screens = append(screens, covered)
								wantGestures++
							}
						}
					}
					screens = append(screens, after)
					w, c := opFake(t, screens...)
					res, err := c.AUMChannel(context.Background(), req)
					if err != nil || res.Verified != wantAdded {
						t.Fatalf("%s: verified %v, message %q, error %v", action, res.Verified, res.Message, err)
					}
					if wantAdded {
						for _, ch := range newStrips {
							if !strings.Contains(res.Message, ch.Title) {
								t.Errorf("message %q does not identify added strip %q", res.Message, ch.Title)
							}
						}
						if action == "add_audio" || action == "add_midi" {
							if ch, ok := res.Data.(AUMChannel); !ok || ch.Title != newStrips[0].Title || ch.Kind != strings.TrimPrefix(action, "add_") {
								t.Errorf("added strip = %+v", res.Data)
							}
						}
					}
					if g := w.GestureLog(); len(g) != wantGestures || g[0] != "actions" {
						t.Fatalf("gestures = %v, want one scroll and %d taps", g, wantGestures-1)
					}
				})
			}
		}
	}
}

// A whole imported session can fill the window with new strips, leaving no
// old strip to align against. The add tile establishes that this is the end.
func TestAUMChannelImportReplacesVisibleTail(t *testing.T) {
	start := ipadtest.AUM{Tempo: "120", Strips: wideStrips, OnScreenOnly: true}
	end := start
	end.Scroll = ipadtest.MaxScroll(len(end.Strips))
	menu := end
	menu.AddMenu = true
	pop := end
	pop.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "IMPORT", Rows: [][2]string{{"Scratch (mcp test)", ""}}}}
	pick := end
	pick.Overlays = []ipadtest.Overlay{{Kind: "popover", Title: "Scratch (mcp test)", Buttons: []string{"IMPORT 6 CHANNELS"}}}
	after := end
	after.Strips = slices.Clone(end.Strips)
	for i := 0; i < 6; i++ {
		after.Strips = append(after.Strips, ipadtest.Strip{Title: fmt.Sprintf("Imported %d", i+1), Level: "+0.0 dB"})
	}
	after.Scroll = ipadtest.MaxScroll(len(after.Strips))
	w, c := opFake(t, start, end, menu, pop, pick, after)
	res, err := c.AUMChannel(context.Background(), AUMRequest{Action: "import", Name: "Scratch (mcp test)"})
	if err != nil || !res.Verified || !strings.Contains(res.Message, "Imported 6") {
		t.Fatalf("import: %+v, %v", res, err)
	}
	if g := w.GestureLog(); len(g) != 5 || g[0] != "actions" {
		t.Fatalf("import gestures = %v", g)
	}
}
