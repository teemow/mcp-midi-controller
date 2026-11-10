package mcpserver

import (
	"strings"
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/sound"
)

// calibratedSong is toolSong written against the catalogue: plugins by name,
// no transposition, the drum lanes left to the kit.
const calibratedSong = `title: Night Drive
tempo: 300
calibrate: true
tracks:
  - id: bass
    level: -6
    instrument: {plugin: PolySynth, preset: Deep Bass}
  - id: pad
    instrument: {plugin: polysynth, preset: Warm Pad}
    effects:
      - {id: echo, plugin: tape, bypass: true}
  - id: drums
    kind: drums
    instrument: {plugin: DrumBox}
patterns:
  bass: {step: 1/8, notes: "A1 . A1 . A1 . G1 ."}
  chords: {step: 1/4, gate: 1, notes: "[A3 C4 E4] - - -"}
  beat: {drums: {kick: "x...x...x...x...", snare: "....x.......x..."}}
scenes:
  open:
    pad: {preset: Bright Pad, effects: {echo: {bypass: false}}}
sections:
  verse: {bars: 1, play: {bass: bass, pad: chords, drums: beat}}
  chorus: {bars: 1, scene: open, play: {bass: bass, pad: chords, drums: beat}}
arrangement: [verse, chorus]
`

// TestCalibratedSong writes a song against the sound catalogue: plugins by
// name, notes in sounding pitch, levels as loudness, lanes from the kit. What
// is not measured is said, and what is measured is applied to what is played.
func TestCalibratedSong(t *testing.T) {
	r := newSongRig(t)
	s := r.s

	// Nothing measured yet: the song is saved, and says what plays as written.
	res := call(t, s.handleSaveSong, map[string]any{"yaml": calibratedSong})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("save: %s", text)
	}
	for _, want := range []string{
		"ch2  pad        PolySynth > TapeDelay",
		"calibrated against the sound catalogue (level 0 = -20 dBFS rms",
		"bass       Deep Bass: not measured, plays as written",
		`warning: drums: the default patch of DrumBox is not in the sound catalogue`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("summary lacks %q:\n%s", want, text)
		}
	}

	// The catalogue as an audition in the lab would have left it.
	put := func(plugin, name, preset string, m sound.Sound) {
		t.Helper()
		m.PeakDB = m.LevelDB + 3
		if _, err := s.sounds.Put(plugin, name, preset, m); err != nil {
			t.Fatal(err)
		}
	}
	put("syn1", "PolySynth", "Deep Bass", sound.Sound{Transpose: 24, LevelDB: -12, Sustains: true})
	put("syn1", "PolySynth", "Warm Pad", sound.Sound{Transpose: 12, LevelDB: -44, Sustains: true, Remarks: []string{"sounds an octave below the written note"}})
	put("syn1", "PolySynth", "Bright Pad", sound.Sound{Transpose: -12, LevelDB: -20, Sustains: true})
	put("drm1", "DrumBox", "", sound.Sound{LevelDB: -14, Unpitched: true, Pads: []sound.Pad{{Note: 49, Lane: "kick"}, {Note: 51, Lane: "snare"}}, Lanes: map[string]int{"kick": 49, "snare": 51}})

	res = call(t, s.handleSaveSong, map[string]any{"yaml": calibratedSong})
	text = resultText(res)
	if res.IsError {
		t.Fatalf("save with the catalogue: %s", text)
	}
	for _, want := range []string{
		"bass       Deep Bass: notes +24 semitone(s), level -8.0 dB",
		"pad        Warm Pad: notes +12 semitone(s), level +24.0 dB",
		"pad        Bright Pad: notes -12 semitone(s), level +0.0 dB",
		"drums      (default patch): a kit of 2 pad(s), level -6.0 dB",
		`warning: pad: "Warm Pad" of PolySynth needs the fader at +24.0 dB for level 0, and it ends at +6: it plays 18.0 dB too quiet; raise the plugin's volume`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("calibrated summary lacks %q:\n%s", want, text)
		}
	}

	// What is played: the bass two octaves up, the pad an octave up in the
	// verse and an octave down in the chorus, the drums on the kit's pads.
	r.connect()
	if res := call(t, s.handleAuthorSongSession, map[string]any{"song": "night_drive"}); res.IsError {
		t.Fatalf("author: %s", resultText(res))
	}
	if res := call(t, s.handlePlaySong, map[string]any{"song": "night_drive", "preroll_ms": 10}); res.IsError {
		t.Fatalf("play: %s", resultText(res))
	}
	for s.player.Status().Playing {
	}
	r.mu.Lock()
	notes := map[int][]int{}
	for _, cmd := range r.Commands {
		if cmd.Type == "noteOn" {
			notes[cmd.Channel] = append(notes[cmd.Channel], cmd.Note)
		}
	}
	r.mu.Unlock()
	if len(notes[1]) != 8 || notes[1][0] != 57 { // A1 (33) + 24
		t.Errorf("bass notes = %v, want A3 (57) first", notes[1])
	}
	if len(notes[2]) != 6 || notes[2][0] != 69 || notes[2][3] != 45 { // A3 (57) +12, then -12
		t.Errorf("pad notes = %v, want A4 (69) in the verse and A2 (45) in the chorus", notes[2])
	}
	for _, n := range notes[3] {
		if n != 49 && n != 51 {
			t.Errorf("drum note %d is not one of the kit's pads (49, 51)", n)
		}
	}

	// A lane the kit does not have is refused with the pads it has.
	noClap := strings.Replace(calibratedSong, `snare: "....x.......x..."`, `clap: "....x.......x..."`, 1)
	res = call(t, s.handleSaveSong, map[string]any{"yaml": noClap, "dry_run": true})
	if !res.IsError || !strings.Contains(resultText(res), `has no pad for lane "clap" (it has: kick, snare)`) {
		t.Fatalf("a lane the kit lacks: %s", resultText(res))
	}

	// get_song_plugin shows what is measured next to each preset, by name too.
	res = call(t, s.handleGetSongPlugin, map[string]any{"plugin": "polysynth", "query": "pad"})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "presets (2 of 2 matching, 2 measured)") ||
		!strings.Contains(text, "Warm Pad  [transpose +12, -44 dBFS, sustains, sounds an octave below the written note]") {
		t.Fatalf("get_song_plugin with the catalogue: %s", text)
	}
	if res := call(t, s.handleGetSongPlugin, map[string]any{"plugin": "o"}); !res.IsError || !strings.Contains(resultText(res), "fits 2 plugins") {
		t.Fatalf("an ambiguous plugin name: %s", resultText(res))
	}
}
