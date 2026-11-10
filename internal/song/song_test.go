package song

import (
	"os"
	"strings"
	"testing"

	"github.com/teemow/midi-device/device"
)

// testCatalog is a rig of made-up plugins: a synth, a drum machine and two
// effects, with the presets and parameters the tests address.
func testCatalog() Catalog {
	param := func(id, name string, min, max float64, labels ...string) device.ProbeParam {
		return device.ProbeParam{Identifier: id, DisplayName: name, KeyPath: id, Min: min, Max: max, Writable: true, ValueStrings: labels}
	}
	presets := func(names ...string) []device.ProbePreset {
		out := make([]device.ProbePreset, len(names))
		for i, n := range names {
			out[i] = device.ProbePreset{Number: i, Name: n}
		}
		return out
	}
	return Catalog{
		"synth": {
			Component: device.ProbeComponent{Type: "aumu", Subtype: "Syn1", Manufacturer: "Test"},
			Name:      "Test Synth",
			Parameters: []device.ProbeParam{
				param("cutoff", "Cutoff", 0, 1),
				param("res", "Resonance", 0, 1),
				param("wave", "Waveform", 0, 2, "Saw", "Square", "Sine"),
				{Identifier: "meter", DisplayName: "Meter", Max: 1}, // read-only
			},
			FactoryPresets: presets("Init", "Warm Pad", "Bright Lead", "Deep Bass"),
		},
		"drums": {
			Component:      device.ProbeComponent{Type: "aumu", Subtype: "Drm1", Manufacturer: "Test"},
			Name:           "Test Drums",
			FactoryPresets: presets("Kit 808", "Kit 909"),
		},
		"delay": {
			Component:  device.ProbeComponent{Type: "aufx", Subtype: "Dly1", Manufacturer: "Test"},
			Name:       "Test Delay",
			Parameters: []device.ProbeParam{param("mix", "Mix", 0, 1), param("time", "Time", 0, 2000)},
		},
		"reverb": {
			Component:      device.ProbeComponent{Type: "aufx", Subtype: "Rvb1", Manufacturer: "Test"},
			Name:           "Test Reverb",
			Parameters:     []device.ProbeParam{param("0", "Mix", 0, 1), param("1", "Size", 0, 1)},
			FactoryPresets: presets("Small Room", "Large Hall"),
		},
	}
}

// testSong is a small complete song: two melodic tracks and drums, a verse and
// a chorus that differ in preset, parameters, effects, level and mute.
const testSong = `
title: Test Song
tempo: 120
tracks:
  - id: bass
    instrument: {plugin: synth, preset: Deep Bass, params: {Cutoff: 0.25}}
    effects:
      - {id: echo, plugin: delay, bypass: true, params: {Mix: 0.2}}
    level: -3
  - id: lead
    transpose: 12
    mute: true
    instrument: {plugin: synth, preset: Warm Pad}
    effects:
      - {plugin: delay}
      - {id: space, plugin: reverb, preset: Small Room}
  - id: drums
    kind: drums
    instrument: {plugin: drums}
    drum_map: {zap: "50"}
master:
  - {id: glue, plugin: reverb, bypass: true}
patterns:
  bass_a:
    step: 1/8
    notes: "A1 . A1 . A1 . A1 . | G1 . G1 . G1 - - ."
  lead_a:
    step: 1/8
    notes:
      - "A3 - C4 . [E4 G4]! . A3:50 ."
  beat:
    drums:
      kick:  "x...x...x...x..."
      snare: "....X.......X..."
      hat:   "x.o.x.o.x.o.x.o."
      zap:   "...............x"
scenes:
  chorus:
    bass:
      params: {Cutoff: 80%}
      effects: {echo: {bypass: false}}
    lead:
      mute: false
      level: -6
      preset: Bright Lead
      effects: {space: {preset: Large Hall}}
    master:
      effects: {glue: {bypass: false}}
sections:
  verse:
    bars: 2
    play: {bass: bass_a, drums: beat}
  chorus:
    bars: 2
    scene: chorus
    play: {bass: bass_a, lead: lead_a, drums: beat}
arrangement: [verse, chorus, verse]
`

func mustParse(t *testing.T, doc string) *Song {
	t.Helper()
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func mustPlan(t *testing.T, s *Song) *Plan {
	t.Helper()
	p, err := NewPlan(s, testCatalog())
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(testSong, "tempo: 120", "tempo: 120\nbpm: 120", 1)))
	if err == nil || !strings.Contains(err.Error(), "bpm") {
		t.Fatalf("Parse with an unknown field: %v, want an error naming it", err)
	}
}

// TestValidateReportsEveryProblem checks one pass names every broken
// reference, each by its place in the document.
func TestValidateReportsEveryProblem(t *testing.T) {
	doc := testSong
	for _, r := range [][2]string{
		{"play: {bass: bass_a, drums: beat}", "play: {bass: nope, drums: beat, ghost: beat}"},
		{"scene: chorus", "scene: [chorus, missing]"},
		{"arrangement: [verse, chorus, verse]", "arrangement: [verse, bridge]"},
		{"effects: {echo: {bypass: false}}", "effects: {flanger: {bypass: false}}"},
	} {
		if !strings.Contains(doc, r[0]) {
			t.Fatalf("test song no longer contains %q", r[0])
		}
		doc = strings.Replace(doc, r[0], r[1], 1)
	}
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse succeeded, want problems")
	}
	for _, want := range []string{
		`/sections/verse/play/bass/0: no pattern "nope"`,
		`/sections/verse/play/ghost: no track "ghost"`,
		`/sections/chorus/scene/1: no scene "missing"`,
		`/arrangement/1: no section "bridge"`,
		`/scenes/chorus/bass/effects/flanger: "bass" has no effect "flanger" (it has echo)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("problems do not include %q:\n%v", want, err)
		}
	}
}

func TestValidateChecksTracks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"duplicate id":     {"- id: lead", "- id: bass", `duplicate track "bass"`},
		"channel clash":    {"- id: lead", "- id: lead\n    channel: 1", "MIDI channel 1 is already track"},
		"drums on melodic": {"play: {bass: bass_a, drums: beat}", "play: {bass: beat}", "is not a drum track"},
		"bad tempo":        {"tempo: 120", "tempo: 5", "is not a tempo"},
		"duplicate effect": {"- {plugin: delay}", "- {plugin: delay}\n      - {plugin: delay}", `duplicate effect "delay"`},
		"endless section":  {"verse:\n    bars: 2", "verse:\n    bars: 300000", "a section is 1..256 bars long"},
		"endless pattern":  {"lead_a:\n    step: 1/8", "lead_a:\n    bars: 5000\n    step: 1/8", "is not a length (1..64 bars)"},
		"master state":     {"master:\n      effects: {glue: {bypass: false}}", "master:\n      level: -3", "only the master's effects can be set"},
	} {
		if !strings.Contains(testSong, tc.from) {
			t.Fatalf("%s: test song no longer contains %q", name, tc.from)
		}
		_, err := Parse([]byte(strings.Replace(testSong, tc.from, tc.to, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	if ids, err := st.List(); err != nil || len(ids) != 0 {
		t.Fatalf("List on an empty store = %v, %v", ids, err)
	}
	if _, err := st.Save("Test Song!", []byte(testSong)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := st.Save("broken", []byte("title: x")); err == nil {
		t.Fatal("Save accepted a song that does not validate")
	}
	ids, err := st.List()
	if err != nil || len(ids) != 1 || ids[0] != "test_song" {
		t.Fatalf("List = %v, %v, want [test_song]", ids, err)
	}
	s, raw, err := st.Load("test_song")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Title != "Test Song" || string(raw) != testSong {
		t.Fatalf("Load returned title %q and %d bytes; want the saved text back", s.Title, len(raw))
	}
	if err := st.Delete("test_song"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := st.Load("test_song"); err == nil {
		t.Fatal("Load after Delete succeeded")
	}
	if _, err := st.Save("../..", []byte(testSong)); err == nil {
		t.Fatal("Save accepted an id with no usable characters")
	}

	// List only names what Load can find: a file placed by hand under a name
	// that is not a normalized id is not a stored song.
	if _, err := st.Save("kept", []byte(testSong)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(st.dir+"/Night-Drive.yaml", []byte(testSong), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if ids, err := st.List(); err != nil || len(ids) != 1 || ids[0] != "kept" {
		t.Fatalf("List = %v, %v, want [kept]", ids, err)
	}
}

// TestMarshalRoundTrip checks a song survives being written back as YAML.
func TestMarshalRoundTrip(t *testing.T) {
	s := mustParse(t, testSong)
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse of the marshalled song: %v\n%s", err, out)
	}
	a, err := Compile(s, mustPlan(t, s))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	b, err := Compile(again, mustPlan(t, again))
	if err != nil {
		t.Fatalf("Compile of the marshalled song: %v", err)
	}
	if len(a.Events) != len(b.Events) || a.Fingerprint != b.Fingerprint {
		t.Fatalf("the marshalled song compiles to %d events (%s), the original to %d (%s)",
			len(b.Events), b.Fingerprint, len(a.Events), a.Fingerprint)
	}
}

// TestDocsExample keeps docs/songs.md honest: the complete song it shows is a
// song this package accepts and compiles.
func TestDocsExample(t *testing.T) {
	doc, err := os.ReadFile("../../docs/songs.md")
	if err != nil {
		t.Fatalf("read the songs doc: %v", err)
	}
	_, rest, ok := strings.Cut(string(doc), "```yaml\ntitle:")
	body, _, closed := strings.Cut(rest, "\n```")
	if !ok || !closed {
		t.Fatal("docs/songs.md has no complete example (a yaml block starting with title:)")
	}
	s, err := Parse([]byte("title:" + body))
	if err != nil {
		t.Fatalf("the example in docs/songs.md is not a valid song: %v", err)
	}
	tl, err := Compile(s, nil)
	if err != nil {
		t.Fatalf("the example in docs/songs.md does not compile: %v", err)
	}
	if tl.Bars != 16 || len(tl.Tracks) != 3 {
		t.Errorf("the example compiles to %d bars and %d tracks, want 16 and 3", tl.Bars, len(tl.Tracks))
	}
}
