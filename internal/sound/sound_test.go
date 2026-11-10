package sound

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

const testRate = 48000.0

// hz is the frequency of a MIDI note.
func hz(note float64) float64 { return 440 * math.Pow(2, (note-69)/12) }

// tone renders a clip: silence for latency, then gen(t) for the held note,
// shaped by env(t since the note began, t since its release or -1).
func tone(totalMS, latencyMS, holdMS float64, gen func(t float64) float64, env func(on, off float64) float64) Clip {
	n := int(totalMS / 1000 * testRate)
	out := make([]float32, 0, 2*n)
	for i := 0; i < n; i++ {
		t := float64(i)/testRate - latencyMS/1000
		var v float64
		if t >= 0 {
			off := -1.0
			if t >= holdMS/1000 {
				off = t - holdMS/1000
			}
			v = gen(t) * env(t, off)
		}
		out = append(out, float32(v), float32(v))
	}
	return Clip{Samples: out, Channels: 2, SampleRate: testRate, HoldMS: latencyMS + holdMS}
}

func sine(f float64) func(float64) float64 {
	return func(t float64) float64 { return math.Sin(2 * math.Pi * f * t) }
}

// organ holds its level and stops 40 ms after the release.
func organ(amp float64) func(on, off float64) float64 {
	return func(on, off float64) float64 {
		a := amp * math.Min(1, on/0.01)
		if off >= 0 {
			a *= math.Max(0, 1-off/0.04)
		}
		return a
	}
}

func hasRemark(s Sound, part string) bool {
	for _, r := range s.Remarks {
		if strings.Contains(r, part) {
			return true
		}
	}
	return false
}

func TestMeasureNoteAtPitch(t *testing.T) {
	s := MeasureNote(tone(1300, 40, 700, sine(hz(57)), organ(0.25)), 57, 100)
	if s.Transpose != 0 || s.Sounds != "A3" || math.Abs(s.Cents) > 3 || s.Unpitched {
		t.Errorf("pitch = %s %+.0f cents, transpose %d (unpitched %v), want A3 at pitch", s.Sounds, s.Cents, s.Transpose, s.Unpitched)
	}
	// A sine of amplitude 0.25: -12 dBFS peak, 3 dB less rms.
	if math.Abs(s.PeakDB+12) > 0.3 || math.Abs(s.LevelDB+15) > 0.6 {
		t.Errorf("level = %.1f dBFS rms, %.1f peak, want about -15 and -12", s.LevelDB, s.PeakDB)
	}
	if !s.Sustains || s.Rings || s.ReleaseMS > 80 || math.Abs(s.LatencyMS-40) > 10 || s.AttackMS > 30 {
		t.Errorf("envelope = latency %.0f, attack %.0f, sustains %v, release %.0f (rings %v)", s.LatencyMS, s.AttackMS, s.Sustains, s.ReleaseMS, s.Rings)
	}
	if s.WobbleCents > 5 || len(s.Remarks) != 0 {
		t.Errorf("a steady note at pitch: wobble %.0f cents, remarks %v", s.WobbleCents, s.Remarks)
	}
	if len(s.Partials) == 0 || s.Partials[0].Note != "A3" || s.Partials[0].DB != 0 {
		t.Errorf("partials = %+v, want A3 first", s.Partials)
	}
}

// TestMeasureNoteOctaves checks the fact a song needs most: how far a preset
// sounds from the written note.
func TestMeasureNoteOctaves(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sounding  float64
		transpose int
		remark    string
	}{
		{"an octave below", 45, 12, "sounds an octave below the written note"},
		{"two octaves below", 33, 24, "sounds 2 octaves below the written note"},
		{"an octave above", 69, -12, "sounds an octave above the written note"},
		{"a fourth above", 62, -5, "5 semitone(s) off, not an octave"},
	} {
		s := MeasureNote(tone(1300, 30, 700, sine(hz(tc.sounding)), organ(0.2)), 57, 100)
		if s.Transpose != tc.transpose || !hasRemark(s, tc.remark) {
			t.Errorf("%s: transpose %d, remarks %v; want %d and %q", tc.name, s.Transpose, s.Remarks, tc.transpose, tc.remark)
		}
	}
}

func TestMeasureNoteEnvelope(t *testing.T) {
	// A pluck: full at once, 20 dB down every 150 ms.
	pluck := func(on, off float64) float64 { return 0.5 * math.Pow(10, -on/0.15) }
	s := MeasureNote(tone(1300, 20, 700, sine(hz(57)), pluck), 57, 100)
	if s.Sustains || math.Abs(s.DecayMS-150) > 30 {
		t.Errorf("a pluck: sustains %v, decay %.0f ms, want no sustain and about 150", s.Sustains, s.DecayMS)
	}
	// A pad: half a second of attack, and it rings on past the capture.
	pad := func(on, off float64) float64 { return 0.3 * math.Min(1, on/0.5) }
	s = MeasureNote(tone(1300, 20, 900, sine(hz(57)), pad), 57, 100)
	if !s.Sustains || s.AttackMS < 250 || !s.Rings {
		t.Errorf("a pad: sustains %v, attack %.0f ms, rings %v", s.Sustains, s.AttackMS, s.Rings)
	}
}

func TestMeasureNoteWobbleAndTuning(t *testing.T) {
	// A tape effect: the pitch swings 1.3 semitones either way, once a second.
	swing := math.Pow(2, 1.3/12) - 1
	wow := func(t float64) float64 {
		// The phase of a sine at f·(1 + swing·sin 2πt).
		return math.Sin(2 * math.Pi * hz(69) * (t - swing*math.Cos(2*math.Pi*t)/(2*math.Pi)))
	}
	s := MeasureNote(tone(1500, 20, 1200, wow, organ(0.2)), 69, 100)
	if s.WobbleCents < 150 || !hasRemark(s, "wanders") {
		t.Errorf("a wandering pitch: %.0f cents, remarks %v", s.WobbleCents, s.Remarks)
	}
	s = MeasureNote(tone(1300, 20, 700, sine(hz(57.35)), organ(0.2)), 57, 100)
	if s.Transpose != 0 || math.Abs(s.Cents-35) > 4 || !hasRemark(s, "out of tune by +35 cents") {
		t.Errorf("a sharp note: %s %+.0f cents, remarks %v", s.Sounds, s.Cents, s.Remarks)
	}
}

// TestMeasureNoteGlide checks a note that scoops into its pitch is described
// as that, and not as a pitch that wanders: a bass patch on a device started
// every note 140 cents flat and was on it after 150 ms.
func TestMeasureNoteGlide(t *testing.T) {
	target := hz(33)
	scoop := func(t float64) float64 {
		// The phase of a sine whose pitch rises to the target: 140 cents
		// flat at first, half of that every 80 ms.
		const tau = 0.08 / math.Ln2
		flat := math.Pow(2, -140.0/1200) - 1
		return math.Sin(2 * math.Pi * target * (t + flat*tau*(1-math.Exp(-t/tau))))
	}
	s := MeasureNote(tone(1300, 60, 900, scoop, organ(0.2)), 57, 100)
	if s.Sounds != "A1" || math.Abs(s.Cents) > 5 || s.Transpose != 24 {
		t.Errorf("pitch = %s %+.0f cents, transpose %d; want the note it settles on, A1", s.Sounds, s.Cents, s.Transpose)
	}
	if s.GlideCents > -40 || s.GlideCents < -150 || s.GlideMS < 60 || s.GlideMS > 320 || !hasRemark(s, "cents flat and reaches the note after") {
		t.Errorf("glide = %.0f cents over %.0f ms, remarks %v", s.GlideCents, s.GlideMS, s.Remarks)
	}
	if s.WobbleCents > 15 || hasRemark(s, "wanders") {
		t.Errorf("a scoop was taken for a wandering pitch: %.0f cents, %v", s.WobbleCents, s.Remarks)
	}
}

// TestFoldOctaves checks a track read an octave high in some frames is
// brought to its real pitch, and that a glide and a melody are left alone.
func TestFoldOctaves(t *testing.T) {
	track := func(hz ...float64) []pitchFrame {
		out := make([]pitchFrame, len(hz))
		for i, f := range hz {
			out[i] = pitchFrame{f, float64(i) * 30}
		}
		return out
	}
	// A lead whose second harmonic outweighs its fundamental.
	folded := foldOctaves(track(110.2, 55.1, 55.0, 110.1, 55.1, 220.3, 55.0, 110.0, 55.1, 55.0))
	for i, f := range folded {
		if math.Abs(f.hz-55) > 0.3 {
			t.Errorf("frame %d = %.1f Hz after folding, want 55", i, f.hz)
		}
	}
	for name, tr := range map[string][]pitchFrame{
		"a steady note": track(220, 220.1, 219.9, 220, 220.2, 220),
		"a glide":       track(50.6, 52.8, 54.6, 54.9, 55, 55, 55.1, 55),
		"a melody":      track(220, 220, 261.6, 261.6, 329.6, 329.6, 220, 220),
		"one octave up": track(220, 440, 440, 440, 440, 440, 440, 440, 440, 440),
	} {
		got := foldOctaves(tr)
		for i := range tr {
			if got[i] != tr[i] {
				t.Errorf("%s: frame %d changed from %.1f to %.1f Hz", name, i, tr[i].hz, got[i].hz)
			}
		}
	}
}

// TestSettlePitch checks which pitch a sound of several partials is given: a
// weak or detuned fundamental is still the pitch, and only a period nothing
// sounds at gives way to the strongest partial.
func TestSettlePitch(t *testing.T) {
	// A sequence patch on a device: the fundamental 10 dB under its octave
	// and 77 cents sharp of the period found.
	s := Sound{Note: 57, Hz: 110, Partials: []Partial{{Hz: 223, Note: "A3"}, {Hz: 439, Note: "A4", DB: -9}, {Hz: 115, Note: "A#2", DB: -10}}}
	setPitch(&s, 110)
	settlePitch(&s)
	if s.Sounds != "A2" || s.Transpose != 12 || len(s.Remarks) != 0 {
		t.Errorf("a detuned fundamental: %s, transpose %d, %v; want A2 and +12", s.Sounds, s.Transpose, s.Remarks)
	}
	// A note with a fifth under it: nothing sounds at their common period.
	s = Sound{Note: 69, Partials: []Partial{{Hz: 440, Note: "A4"}, {Hz: 293.7, Note: "D4", DB: -9}}}
	setPitch(&s, 146.8)
	settlePitch(&s)
	if s.Sounds != "A4" || s.Transpose != 0 || !hasRemark(s, "several notes sound at once") {
		t.Errorf("a layered fifth: %s, transpose %d, %v; want A4", s.Sounds, s.Transpose, s.Remarks)
	}
	// A brass patch with a sub-oscillator: at pitch, and the octave under it
	// is said.
	s = Sound{Note: 57, Partials: []Partial{{Hz: 220, Note: "A3"}, {Hz: 110, Note: "A2", DB: -8}}}
	setPitch(&s, 220)
	settlePitch(&s)
	describeLayers(&s)
	if s.Transpose != 0 || !hasRemark(s, "has an octave below it (-8 dB)") {
		t.Errorf("a sub-octave: transpose %d, %v", s.Transpose, s.Remarks)
	}
}

func TestMeasureNoteLayerAndSilence(t *testing.T) {
	// A preset that puts a fifth under every note, 9 dB down.
	layered := func(t float64) float64 {
		return math.Sin(2*math.Pi*hz(69)*t) + 0.35*math.Sin(2*math.Pi*hz(62)*t)
	}
	s := MeasureNote(tone(1300, 20, 700, layered, organ(0.2)), 69, 100)
	found := false
	for _, p := range s.Partials {
		found = found || p.Note == "D4"
	}
	if !found || !hasRemark(s, "also sounds") {
		t.Errorf("a layered fifth: partials %+v, remarks %v", s.Partials, s.Remarks)
	}

	silent := MeasureNote(Clip{Samples: make([]float32, 96000), Channels: 2, SampleRate: testRate, HoldMS: 700}, 57, 100)
	if !silent.Silent() || !silent.Unpitched || !hasRemark(silent, "silent") {
		t.Errorf("silence = %+v", silent)
	}
	noise := rand.New(rand.NewSource(1))
	s = MeasureNote(tone(1300, 20, 700, func(float64) float64 { return noise.Float64()*2 - 1 }, organ(0.2)), 57, 100)
	if !s.Unpitched || s.Silent() {
		t.Errorf("noise: unpitched %v, silent %v, sounds %q", s.Unpitched, s.Silent(), s.Sounds)
	}
}

// drum renders a drum hit: a pitched body and noise through a one-pole
// high-pass, both dying away.
func drum(bodyHz, bodyAmp, noiseAmp, brightness, lengthMS float64) Clip {
	r := rand.New(rand.NewSource(int64(bodyHz + lengthMS)))
	var prevIn, prevOut float64
	gen := func(t float64) float64 {
		in := r.Float64()*2 - 1
		// brightness 0 passes the noise as it is, 1 keeps only its top.
		out := brightness*(prevOut+in-prevIn) + (1-brightness)*in
		prevIn, prevOut = in, out
		return bodyAmp*math.Sin(2*math.Pi*bodyHz*t) + noiseAmp*out
	}
	env := func(on, off float64) float64 { return math.Pow(10, -1.5*on/(lengthMS/1000)) }
	return tone(1200, 15, 60, gen, env)
}

// block renders a hit whose energy sits between a drum's body and a cymbal:
// a cluster of partials from 700 to 2400 Hz, 150 ms long. A device's kit had
// such a pad where a closed hat was expected.
func block() Clip {
	gen := func(t float64) float64 {
		var v float64
		for i, f := range []float64{710, 930, 1180, 1420, 1790, 2360} {
			v += math.Sin(2*math.Pi*f*t+float64(i)) / 6
		}
		return v
	}
	env := func(on, off float64) float64 { return 0.5 * math.Pow(10, -1.5*on/0.15) }
	return tone(1200, 15, 60, gen, env)
}

func TestMeasureHitAndLanes(t *testing.T) {
	kit := map[int]Clip{
		49: drum(55, 0.8, 0.02, 0, 180),    // kick
		51: drum(190, 0.25, 0.5, 0.3, 160), // snare
		54: drum(0, 0, 0.4, 0.97, 60),      // closed hat
		56: drum(110, 0.6, 0.01, 0, 250),   // low tom
		58: drum(160, 0.6, 0.01, 0, 250),   // high tom
		63: drum(0, 0, 0.4, 0.97, 400),     // open hat
		66: drum(0, 0, 0.3, 0.97, 1500),    // crash
		60: block(),                        // something in the middle
	}
	var pads []Pad
	for note := 48; note <= 67; note++ {
		clip, ok := kit[note]
		if !ok {
			clip = Clip{Samples: make([]float32, 48000), Channels: 2, SampleRate: testRate, HoldMS: 60}
		}
		p, sounds := MeasureHit(clip, note)
		if sounds != ok {
			t.Fatalf("note %d: sounds %v, want %v", note, sounds, ok)
		}
		if sounds {
			pads = append(pads, p)
		}
	}
	lanes := NameLanes(pads)
	want := map[string]int{"kick": 49, "snare": 51, "hat": 54, "tom_low": 56, "tom_high": 58, "perc": 60, "open_hat": 63, "crash": 66}
	for lane, note := range want {
		if lanes[lane] != note {
			t.Errorf("lane %s = %d, want %d", lane, lanes[lane], note)
		}
	}
	if len(lanes) != len(want) {
		t.Errorf("lanes = %v, want %v", lanes, want)
	}
	for _, p := range pads {
		if p.Note == 49 && (math.Abs(p.PitchHz-55) > 4 || p.CentroidHz > 200 || p.Lane != "kick") {
			t.Errorf("kick = %+v", p)
		}
		if p.Note == 54 && (p.CentroidHz < 6000 || p.LengthMS > 120) {
			t.Errorf("closed hat = %+v", p)
		}
		if p.Note == 66 && !p.Rings && p.LengthMS < 700 {
			t.Errorf("crash = %+v", p)
		}
	}

	// A second pad of a kind is numbered, not dropped.
	two := NameLanes([]Pad{{Note: 42, CentroidHz: 9000, LengthMS: 50}, {Note: 44, CentroidHz: 9500, LengthMS: 70}})
	if two["hat"] != 42 || two["hat_2"] != 44 {
		t.Errorf("two hats = %v", two)
	}
}

func TestCatalog(t *testing.T) {
	c := NewCatalog(t.TempDir() + "/sounds")
	if s := c.Sound("aumu-syn1-acme", "Warm Pad"); s != nil {
		t.Fatalf("an empty catalogue knows a sound: %+v", s)
	}
	if ps, err := c.Plugins(); err != nil || len(ps) != 0 {
		t.Fatalf("plugins of an empty catalogue: %v, %v", ps, err)
	}
	if _, err := c.Put("aumu-syn1-acme", "PolySynth", "Warm Pad", Sound{Note: 57, Transpose: 12, LevelDB: -20, Sustains: true}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := c.Put("aumu-syn1-acme", "PolySynth", "", Sound{Note: 57, LevelDB: -14}); err != nil {
		t.Fatalf("put the default patch: %v", err)
	}
	if s := c.Sound("aumu-syn1-acme", "warm pad"); s == nil || s.Transpose != 12 {
		t.Fatalf("lookup without regard to case: %+v", s)
	}
	if s := c.Sound("aumu-syn1-acme", ""); s == nil || s.LevelDB != -14 {
		t.Fatalf("the default patch: %+v", s)
	}

	// A correction by hand outlives a new measurement.
	_, err := c.Correct("aumu-syn1-acme", "Warm Pad", func(s *Sound) []string {
		s.Transpose, s.Comment = 0, "the octave is the patch's sub oscillator"
		return []string{"transpose"}
	})
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	kept, err := c.Put("aumu-syn1-acme", "PolySynth", "Warm Pad", Sound{Note: 57, Transpose: 12, LevelDB: -19})
	if err != nil || kept.Transpose != 0 || kept.LevelDB != -19 || kept.Comment == "" {
		t.Fatalf("after measuring again: %+v, %v", kept, err)
	}
	if _, err := c.Correct("aumu-syn1-acme", "Nope", func(*Sound) []string { return nil }); err == nil {
		t.Error("a correction of an unmeasured preset was accepted")
	}

	// A kit's lanes set by hand also rename its pads.
	pads := []Pad{{Note: 49, CentroidHz: 80}, {Note: 51, CentroidHz: 1500}}
	lanes := NameLanes(pads)
	if _, err := c.Put("aumu-drm1-acme", "DrumBox", "Kit A", Sound{Pads: pads, Lanes: lanes}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Correct("aumu-drm1-acme", "Kit A", func(s *Sound) []string {
		s.Lanes = map[string]int{"kick": 49, "clap": 51}
		return []string{"lanes"}
	}); err != nil {
		t.Fatal(err)
	}
	again := []Pad{{Note: 49, CentroidHz: 80}, {Note: 51, CentroidHz: 1500}}
	kept, err = c.Put("aumu-drm1-acme", "DrumBox", "Kit A", Sound{Pads: again, Lanes: NameLanes(again)})
	if err != nil || kept.Lanes["clap"] != 51 || kept.Pads[1].Lane != "clap" {
		t.Fatalf("pinned lanes after measuring again: %+v, %v", kept, err)
	}

	ps, err := c.Plugins()
	if err != nil || len(ps) != 2 || ps[0].Plugin != "aumu-drm1-acme" || ps[1].Name != "PolySynth" || len(ps[1].Presets) != 2 {
		t.Fatalf("plugins = %+v, %v", ps, err)
	}
	if _, err := c.Put("../escape", "", "x", Sound{}); err == nil {
		t.Error("a plugin id with a path in it was accepted")
	}
}
