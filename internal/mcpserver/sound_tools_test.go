package mcpserver

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/teemow/mcp-midi-controller/internal/ipad/ipadtest"
	"github.com/teemow/mcp-midi-controller/internal/sound"
)

// fastAuditions shortens the waits of audition_sound.
func fastAuditions(t *testing.T) {
	t.Helper()
	old := []time.Duration{auditionLoadWait, auditionHold, auditionTail, auditionHitHold, auditionHitTail}
	auditionLoadWait, auditionHold, auditionTail = 20*time.Millisecond, 350*time.Millisecond, 120*time.Millisecond
	auditionHitHold, auditionHitTail = 40*time.Millisecond, 120*time.Millisecond
	t.Cleanup(func() {
		auditionLoadWait, auditionHold, auditionTail, auditionHitHold, auditionHitTail = old[0], old[1], old[2], old[3], old[4]
	})
}

// presetVoice is a synth that sounds like its presets are named: the last
// Program Change 0..3 on the global channel decides the octave and the level
// (the dump's order: Init, Deep Bass, Warm Pad, Bright Pad).
func presetVoice(r *songRig) func(ch, note int) (float64, float64) {
	return func(ch, note int) (float64, float64) {
		// r.mu is held by the caller.
		program := -1
		for _, pc := range r.Programs {
			if pc.Channel == 15 && pc.Program <= 3 {
				program = pc.Program
			}
		}
		pitch := 440 * math.Pow(2, float64(note-69)/12)
		switch program {
		case 1: // Deep Bass: two octaves down
			return pitch / 4, 0.3
		case 2: // Warm Pad: an octave down, quiet
			return pitch / 2, 0.03
		case 3: // Bright Pad: an octave up
			return pitch * 2, 0.2
		}
		return pitch, 0.2
	}
}

// labRig is a rig for the lab of the synth and the drum machine: channel 1
// is the synth, channel 2 a drum machine with three pads.
func labRig(t *testing.T) *songRig {
	t.Helper()
	r := newSongRig(t)
	fastAuditions(t)
	synth := presetVoice(r)
	r.Voice = func(ch, note int) (float64, float64) {
		if ch != 2 {
			return synth(ch, note)
		}
		switch note {
		case 49:
			return 55, 0.5 // a kick
		case 51:
			return 170, 0.4 // a tom
		case 54:
			return 9000, 0.2 // a hat
		}
		return 0, 0
	}
	return r
}

var labStrips = []ipadtest.Strip{
	{Title: "Brain"}, {Title: "PolySynth", Level: "+0.0 dB"}, {Title: "DrumBox", Level: "+0.0 dB"}, {Title: "Master", Level: "+0.0 dB"},
}

// TestSoundLab opens a lab and auditions presets in it: every preset is
// reachable, each is measured for what it sounds like, and the measurements
// land in the catalogue.
func TestSoundLab(t *testing.T) {
	r := labRig(t)
	s := r.s
	if res := call(t, s.handleAuditionSound, map[string]any{"plugin": "syn1"}); !res.IsError || !strings.Contains(resultText(res), "there is no sound lab yet") {
		t.Fatalf("audition without a lab: %s", resultText(res))
	}
	if res := call(t, s.handleOpenSoundLab, map[string]any{"plugins": []string{"syn1"}}); !res.IsError || !strings.Contains(resultText(res), "needs confirm: true") {
		t.Fatalf("a lab without confirm: %s", resultText(res))
	}
	if res := call(t, s.handleOpenSoundLab, map[string]any{"plugins": []string{"dly1"}, "confirm": true}); !res.IsError || !strings.Contains(resultText(res), "is not an instrument") {
		t.Fatalf("a lab of an effect: %s", resultText(res))
	}

	r.pullStaged("Songs/Sound Lab.aumproj")
	r.aumLoads("Songs", "Sound Lab", labStrips)
	res := call(t, s.handleOpenSoundLab, map[string]any{"plugins": []string{"polysynth", "drm1"}, "confirm": true})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("open the lab: %s", text)
	}
	for _, want := range []string{
		"the sound lab is loaded in AUM",
		"4 preset load(s)",
		"polysynth        PolySynth: 4 of 4 preset(s) mapped",
		"drumbox          DrumBox: 0 of 0 preset(s) mapped",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lab report lacks %q:\n%s", want, text)
		}
	}

	// Three presets in one call, by a word and by name.
	res = call(t, s.handleAuditionSound, map[string]any{"plugin": "syn1", "query": "pad", "presets": []string{"Deep Bass"}})
	text = resultText(res)
	if res.IsError {
		t.Fatalf("audition: %s", text)
	}
	for _, want := range []string{
		"PolySynth on track polysynth of \"sound_lab\", A3 at velocity 100",
		"Deep Bass: sounds A1 +0c, transpose +24",
		"- sounds 2 octaves below the written note",
		"Warm Pad: sounds A2 +0c, transpose +12",
		"Bright Pad: sounds A4 +0c, transpose -12",
		"stored in the sound catalogue (3)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("audition report lacks %q:\n%s", want, text)
		}
	}
	warm := s.sounds.Sound("syn1", "Warm Pad")
	bass := s.sounds.Sound("syn1", "Deep Bass")
	if warm == nil || bass == nil || warm.Transpose != 12 || bass.Transpose != 24 || !warm.Sustains {
		t.Fatalf("catalogue: warm %+v, bass %+v", warm, bass)
	}
	// The pad is 20 dB under the bass, and the catalogue says so.
	if d := bass.LevelDB - warm.LevelDB; math.Abs(d-20) > 1.5 {
		t.Errorf("Deep Bass is %.1f dB over Warm Pad, want about 20", d)
	}
	// The track declares no preset: afterwards its sound is whatever was
	// loaded last, and nothing was sent to put one back.
	r.mu.Lock()
	programs := len(r.Programs)
	r.mu.Unlock()
	if programs != 3 {
		t.Errorf("%d preset loads for three presets", programs)
	}

	// A preset the plugin does not have, and the sound as it is.
	if res := call(t, s.handleAuditionSound, map[string]any{"track": "polysynth", "preset": "Nope"}); !res.IsError || !strings.Contains(resultText(res), `PolySynth has no factory preset "Nope"`) {
		t.Fatalf("audition of an unknown preset: %s", resultText(res))
	}
	if res := call(t, s.handleAuditionSound, map[string]any{}); !res.IsError || !strings.Contains(resultText(res), "name the track or the plugin") {
		t.Fatalf("audition without a target: %s", resultText(res))
	}

	// The drum machine's pads.
	res = call(t, s.handleAuditionSound, map[string]any{"track": "drumbox", "kit": true, "from": 48, "to": 55})
	text = resultText(res)
	if res.IsError {
		t.Fatalf("kit audition: %s", text)
	}
	for _, want := range []string{"3 pad(s)", " 49 C#3  kick", " 51 D#3  tom_mid", " 54 F#3  hat", "drum map (guessed"} {
		if !strings.Contains(text, want) {
			t.Errorf("kit report lacks %q:\n%s", want, text)
		}
	}
	kit := s.sounds.Sound("drm1", "")
	if kit == nil || kit.Lanes["kick"] != 49 || kit.Lanes["hat"] != 54 || len(kit.Pads) != 3 {
		t.Fatalf("the kit in the catalogue: %+v", kit)
	}

	// The catalogue lists what was measured, and takes a correction that the
	// next measurement keeps.
	res = call(t, s.handleListSounds, map[string]any{"plugin": "polysynth", "query": "octave"})
	if text := resultText(res); !strings.Contains(text, "3 measured preset(s)") || !strings.Contains(text, "Deep Bass") {
		t.Fatalf("list: %s", text)
	}
	res = call(t, s.handleSetSound, map[string]any{"plugin": "drm1", "lanes": map[string]int{"kick": 49, "snare": 51, "hat": 54}, "comment": "51 is the snare of this kit"})
	if res.IsError || !strings.Contains(resultText(res), " 51 D#3  snare") {
		t.Fatalf("set_sound: %s", resultText(res))
	}
	if res := call(t, s.handleAuditionSound, map[string]any{"track": "drumbox", "kit": true, "from": 48, "to": 55}); res.IsError || !strings.Contains(resultText(res), " 51 D#3  snare") {
		t.Fatalf("kit audition after the correction: %s", resultText(res))
	}
	if res := call(t, s.handleSetSound, map[string]any{"plugin": "syn1", "preset": "Init", "transpose": 12}); !res.IsError {
		t.Fatalf("a correction of an unmeasured preset: %s", resultText(res))
	}
}

// TestAuditionInASong auditions in a song's own session: presets the session
// does not map are refused with what to do, and with presets: all the
// candidate is heard through the track's chain and not stored.
func TestAuditionInASong(t *testing.T) {
	r := newSongRig(t)
	fastAuditions(t)
	r.Voice = presetVoice(r)
	s := r.s
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": toolSong}); res.IsError {
		t.Fatalf("save: %s", resultText(res))
	}
	r.pullStaged("Songs/Night Drive.aumproj")
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	if res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true}); res.IsError {
		t.Fatalf("load: %s", resultText(res))
	}
	res := call(t, s.handleAuditionSound, map[string]any{"song": "night_drive", "track": "bass", "preset": "Bright Pad"})
	if !res.IsError || !strings.Contains(resultText(res), `has no Program Change for "Bright Pad"`) || !strings.Contains(resultText(res), "session: {presets: all}") {
		t.Fatalf("audition of an unmapped preset: %s", resultText(res))
	}

	all := toolSong + "session: {presets: all}\n"
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": all}); res.IsError || !strings.Contains(resultText(res), "run load_song") {
		t.Fatalf("save with presets: all: %s", resultText(res))
	}
	if res := call(t, s.handleAuditionSound, map[string]any{"song": "night_drive", "track": "bass", "preset": "Bright Pad"}); !res.IsError || !strings.Contains(resultText(res), "not authored for the song as it is now") {
		t.Fatalf("audition before the new session is loaded: %s", resultText(res))
	}
	r.aumLoads("Songs", "Night Drive", nightDriveStrips)
	if res := call(t, s.handleLoadSong, map[string]any{"song": "night_drive", "confirm": true}); res.IsError {
		t.Fatalf("load with presets: all: %s", resultText(res))
	}
	r.mu.Lock()
	r.Programs = nil
	r.mu.Unlock()

	// The pad track has an effect: the audition is the chain's, not stored.
	res = call(t, s.handleAuditionSound, map[string]any{"song": "night_drive", "track": "pad", "preset": "Init"})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "not stored: the track has effects") {
		t.Fatalf("audition through a chain: %s", text)
	}
	if got := s.sounds.Sound("syn1", "Init"); got != nil {
		t.Errorf("a measurement through an effect chain was stored: %+v", got)
	}
	// Afterwards the track has its own preset back: the candidate's load,
	// then the pad's own Warm Pad.
	r.mu.Lock()
	programs := append([]int(nil), programNumbers(r)...)
	r.mu.Unlock()
	if len(programs) != 2 || programs[0] == programs[1] {
		t.Errorf("preset loads = %v, want the candidate and then the track's own", programs)
	}

	// The bass has no effects and nothing set: its audition is stored. And a
	// song can now change to that preset without a new session.
	res = call(t, s.handleAuditionSound, map[string]any{"song": "night_drive", "track": "bass", "preset": "Bright Pad", "note": "C3"})
	if text := resultText(res); res.IsError || !strings.Contains(text, "Bright Pad: sounds C4 +0c, transpose -12") || !strings.Contains(text, "stored in the sound catalogue") {
		t.Fatalf("audition on a clean track: %s", text)
	}
	changed := strings.Replace(all, "instrument: {plugin: syn1, preset: Deep Bass}", "instrument: {plugin: syn1, preset: Bright Pad}", 1)
	if res := call(t, s.handleSaveSong, map[string]any{"yaml": changed}); res.IsError || strings.Contains(resultText(res), "out of date") {
		t.Fatalf("changing to an auditioned preset needed a new session: %s", resultText(res))
	}

	// Another session in AUM: nothing is played into it.
	s.loaded.set("Songs/Other")
	if res := call(t, s.handleAuditionSound, map[string]any{"song": "night_drive", "track": "bass", "preset": "Init"}); !res.IsError || !strings.Contains(resultText(res), `AUM has "Songs/Other" open`) {
		t.Fatalf("audition into another session: %s", resultText(res))
	}
}

// programNumbers lists the programs received; r.mu must be held.
func programNumbers(r *songRig) []int {
	var out []int
	for _, pc := range r.Programs {
		out = append(out, pc.Program)
	}
	return out
}

// TestDescribeSound checks a measured preset reads as one line with its
// remarks under it.
func TestDescribeSound(t *testing.T) {
	text := describeSound("Glass Keys", &sound.Sound{
		Sounds: "A2", Cents: -4, Transpose: 12, LevelDB: -31.5, PeakDB: -22, AttackMS: 5, DecayMS: 240, ReleaseMS: 600, Rings: true,
		Remarks: []string{"sounds an octave below the written note"},
	}, false)
	want := "  Glass Keys: sounds A2 -4c, transpose +12 | rms -31.5 peak -22.0 dBFS | attack 5 ms, decays in 240 ms, release 600 ms+\n    - sounds an octave below the written note\n"
	if text != want {
		t.Errorf("describeSound =\n%q\nwant\n%q", text, want)
	}
	if text := describeSound("", &sound.Sound{Unpitched: true, Sustains: true}, false); !strings.Contains(text, "(default patch): no pitch") {
		t.Errorf("the default patch: %q", text)
	}
}
