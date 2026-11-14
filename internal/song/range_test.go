package song

import (
	"math"
	"strings"
	"testing"

	"github.com/teemow/aum-session-go/aum"
	"github.com/teemow/midi-device/device"
)

// hpFreq is a frequency exposed and shown linearly from 10 Hz to 30 kHz: a CC
// over all of it moves in steps of about 236 Hz.
var hpFreq = device.ProbeParam{Identifier: "freq", DisplayName: "Frequency", KeyPath: "freq", Min: 10, Max: 30000, Writable: true, UnitName: "Hz"}

// cutoff is AUHighPassFilter's Cutoff Frequency as its probe dump has it: 10 Hz
// to 22050 Hz, shown on a log scale (flags 0xc0c00000: writable, readable,
// high resolution, DisplayLogarithmic). AUM moves it along that scale.
var cutoff = device.ProbeParam{Identifier: "0", DisplayName: "Cutoff Frequency", KeyPath: "0", Min: 10, Max: 22050, Writable: true, Readable: true, UnitName: "Hz", Flags: 0xc0c00000}

// rangeCatalog is the test rig with a high-pass whose frequency is hpFreq.
func rangeCatalog() Catalog {
	cat := testCatalog()
	cat["hipass"] = device.ProbeDump{
		Component: device.ProbeComponent{Type: "aufx", Subtype: "Hpf1", Manufacturer: "Test"},
		Name:      "Test High Pass",
		Parameters: []device.ProbeParam{
			hpFreq,
			{Identifier: "mix", DisplayName: "Mix", KeyPath: "mix", Max: 1, Writable: true},
		},
	}
	return cat
}

// rangeSong high-passes a pad at 80 Hz through a mapping range of 40 to
// 250 Hz. A scene opens the filter to 145 Hz and a ramp sweeps the range.
const rangeSong = `
title: Range Song
tempo: 120
tracks:
  - id: pad
    instrument: {plugin: synth, preset: Warm Pad}
    effects:
      - id: hp
        plugin: hipass
        params: {Frequency: 80, Mix: 1}
        range: {Frequency: [40, 250]}
patterns:
  a: {step: 1/4, notes: "A3 - - -"}
scenes:
  open:
    pad:
      effects: {hp: {params: {Frequency: 145}}}
sections:
  one: {bars: 1, play: {pad: a}}
  two:
    bars: 2
    scene: open
    play: {pad: a}
    automation:
      - {track: pad, effect: hp, param: Frequency, from: 40, to: 250, bar: 2}
arrangement: [one, two]
`

// withoutRange is rangeSong with the whole frequency mapped: the song as it
// was written before mapping ranges.
func withoutRange(doc string) string {
	return strings.Replace(doc, "        range: {Frequency: [40, 250]}\n", "", 1)
}

func rangePlan(t *testing.T, doc string) *Plan {
	t.Helper()
	p, err := NewPlan(mustParse(t, doc), rangeCatalog())
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

// TestRangeWire checks the 128 steps are spread over the mapping range: an
// 80 Hz high-pass that the whole range cannot reach is within a step there.
func TestRangeWire(t *testing.T) {
	// Over the whole range 80 Hz is CC 0, which is 10 Hz.
	if w, err := paramWire(hpFreq, Number(80)); err != nil || w != 0 {
		t.Fatalf("paramWire(80 Hz) = %d, %v; want 0", w, err)
	}
	for _, tc := range []struct {
		v    Value
		want int
	}{
		{Number(40), 0}, {Number(250), 127}, {Number(145), 64}, {Number(80), 24},
		// A share is of the parameter's whole range, as without a range:
		// 0.5 % is 159.95 Hz.
		{Value{Str: "0.5%", IsStr: true}, 73},
	} {
		got, err := rangeWire(hpFreq, 40, 250, tc.v)
		if err != nil || got != tc.want {
			t.Errorf("rangeWire(%s) = %d, %v; want %d", tc.v, got, err, tc.want)
		}
	}
	// CC 24 sets 40 + 24/127 of 210 Hz: within half a step (0.83 Hz) of 80.
	w, _ := rangeWire(hpFreq, 40, 250, Number(80))
	step := 210.0 / 127
	if got := 40 + float64(w)*step; math.Abs(got-80) > step/2 {
		t.Errorf("CC %d sets %.2f Hz, more than half a step (%.2f Hz) from 80", w, got, step/2)
	}
	for _, tc := range []struct {
		v    Value
		want string
	}{
		{Number(300), "300 is outside Frequency's mapping range 40..250"},
		{Number(20), "20 is outside Frequency's mapping range 40..250"},
		{Value{Str: "10%", IsStr: true}, "Frequency at 10% is 3009, outside its mapping range 40..250"},
		{Number(40000), "40000 is outside Frequency's range 10..30000"},
	} {
		if _, err := rangeWire(hpFreq, 40, 250, tc.v); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rangeWire(%s): %v, want an error containing %q", tc.v, err, tc.want)
		}
	}
}

// valuesOf lists the values a timeline sends to one control, in order.
func valuesOf(events []Event, at Assignment) []int {
	var out []int
	for _, ev := range events {
		if ev.Kind == Control && ev.Channel == at.Channel && ev.Data1 == at.Number {
			out = append(out, ev.Data2)
		}
	}
	return out
}

// TestRangeCompile checks every value the song sets the parameter to (its
// declaration, a scene, a ramp) goes through its mapping range.
func TestRangeCompile(t *testing.T) {
	s := mustParse(t, rangeSong)
	p := rangePlan(t, rangeSong)
	hp := p.Node("pad", "hp")
	freq, err := hp.param("Frequency")
	if err != nil {
		t.Fatal(err)
	}
	if len(freq.Range) != 2 || freq.Range[0] != 40 || freq.Range[1] != 250 {
		t.Fatalf("Frequency's range = %v, want [40 250]", freq.Range)
	}
	if mix, _ := hp.param("Mix"); mix.Range != nil {
		t.Errorf("Mix has the range %v, though the song gives it none", mix.Range)
	}
	home, err := p.HomeState()
	if err != nil {
		t.Fatalf("HomeState: %v", err)
	}
	if v, _ := stateValue(home, freq.At); v != 24 {
		t.Errorf("the declaration's 80 Hz is sent as %d, want 24", v)
	}
	tl, err := Compile(s, p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got := valuesOf(tl.Events, freq.At)
	want := map[int]bool{24: false, 64: false, 0: false, 127: false} // 80 Hz, the scene's 145 Hz, the ramp's ends
	for _, v := range got {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	for v, seen := range want {
		if !seen {
			t.Errorf("Frequency never takes %d: %v", v, got)
		}
	}
}

// TestRangeChangesNothingElse checks a range changes its own parameter's
// mapping and values and nothing else: every other mapping and message is
// the one the song had without it, so a song without a range compiles to the
// session and the MIDI it did before.
func TestRangeChangesNothingElse(t *testing.T) {
	ranged, plain := rangePlan(t, rangeSong), rangePlan(t, withoutRange(rangeSong))
	freq, _ := ranged.Node("pad", "hp").param("Frequency")
	if pf, _ := plain.Node("pad", "hp").param("Frequency"); pf.At != freq.At || pf.Range != nil {
		t.Fatalf("Frequency at %s (range %v) without the range, %s with it: the range moved its address", pf.At, pf.Range, freq.At)
	}

	mappings := func(p *Plan) map[string]aum.Mapping {
		t.Helper()
		sess, _, err := p.BuildSession(SessionOptions{})
		if err != nil {
			t.Fatalf("BuildSession: %v", err)
		}
		data, err := sess.Archive().Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		re, err := aum.Open(data)
		if err != nil {
			t.Fatalf("re-open: %v", err)
		}
		out := map[string]aum.Mapping{}
		for _, m := range re.Mappings(true) {
			out[m.Collection+"/"+m.Target] = m
		}
		return out
	}
	with, without := mappings(ranged), mappings(plain)
	hp := ranged.Node("pad", "hp")
	path := slotCollection(hp.Strip, hp.Slot) + "/freq"
	m, ok := with[path]
	if !ok || !m.Spec.Enabled || m.Spec.Data1 != freq.At.Number || m.Spec.Channel != freq.At.Channel-1 {
		t.Fatalf("%s = %+v (found %v), want Frequency's CC %s", path, m, ok, freq.At)
	}
	// AUM stores the range as the two ends' places in the whole range.
	if lo, hi := (40.0-10)/29990, (250.0-10)/29990; math.Abs(m.Min-lo) > 1e-12 || math.Abs(m.Max-hi) > 1e-12 {
		t.Errorf("Frequency's mapping spans %g..%g, want %g..%g", m.Min, m.Max, lo, hi)
	}
	if len(with) != len(without) {
		t.Fatalf("%d mapping targets with the range, %d without", len(with), len(without))
	}
	for key, w := range without {
		if key == path {
			continue
		}
		if got := with[key]; got.Spec != w.Spec || got.Min != w.Min || got.Max != w.Max || got.AutoToggle != w.AutoToggle {
			t.Errorf("%s is %+v with the range, %+v without", key, got, w)
		}
	}

	// The MIDI: the same messages but the range's own.
	other := func(p *Plan, doc string) []Event {
		t.Helper()
		tl, err := Compile(mustParse(t, doc), p)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		var out []Event
		for _, ev := range tl.Events {
			if ev.Kind != Control || ev.Channel != freq.At.Channel || ev.Data1 != freq.At.Number {
				out = append(out, ev)
			}
		}
		return out
	}
	a, b := other(ranged, rangeSong), other(plain, withoutRange(rangeSong))
	if len(a) != len(b) {
		t.Fatalf("%d other messages with the range, %d without", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("message %d is %+v with the range, %+v without", i, a[i], b[i])
		}
	}
}

// TestRangeFingerprint checks a range is part of the session: adding or
// moving one needs the session loaded again, a value within it does not.
func TestRangeFingerprint(t *testing.T) {
	ranged := rangePlan(t, rangeSong).Fingerprint()
	plain := rangePlan(t, withoutRange(rangeSong)).Fingerprint()
	moved := rangePlan(t, strings.Replace(rangeSong, "[40, 250]", "[20, 400]", 1)).Fingerprint()
	value := rangePlan(t, strings.Replace(rangeSong, "{Frequency: 80, Mix: 1}", "{Frequency: 95, Mix: 1}", 1)).Fingerprint()
	if ranged == plain || moved == ranged {
		t.Errorf("fingerprints without a range %s, with it %s, with another %s: want three different ones", plain, ranged, moved)
	}
	if value != ranged {
		t.Error("a value within the range changed the fingerprint; it plays live")
	}
}

// TestRangeModulator checks a modulator's depth is sent the way its target's
// mapping takes it: over the target's mapping range.
func TestRangeModulator(t *testing.T) {
	ranged := func(doc string) string {
		return strings.Replace(withWashHome(doc), "params: {Cutoff: 0.5}}", "params: {Cutoff: 0.5}, range: {Cutoff: [0.2, 0.8]}}", 1)
	}
	// The drop's depth of 0.1..0.9 reaches past the range.
	if _, err := modPlan(t, ranged); err == nil || !strings.Contains(err.Error(), "/scenes/drop/pad/modulators/sweep/depth: 0.1 is outside Cutoff's mapping range 0.2..0.8") {
		t.Errorf("plan: %v, want the drop's depth refused", err)
	}
	within := func(doc string) string {
		return strings.Replace(ranged(doc), "depth: [0.1, 0.9]", "depth: [0.3, 0.7]", 1)
	}
	p, err := modPlan(t, within)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// 0.25 and 0.75 of Cutoff, over 0.2..0.8: CC 11 and 116 (32 and 95
	// over the whole range).
	if d := p.Node("pad", "sweep").Mod.Depth; d != [2]int{11, 116} {
		t.Errorf("sweep's depth = %v, want [11 116]", d)
	}
	if _, err := Compile(mustParse(t, within(modSong)), p); err != nil {
		t.Errorf("Compile: %v", err)
	}
}

// TestRangeChecks checks the ways a range can be wrong are each named with
// their place in the document.
func TestRangeChecks(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"one number":    {"[40, 250]", "[40]", "/tracks/0/effects/0/range/Frequency: a mapping range is two numbers"},
		"upside down":   {"[40, 250]", "[250, 40]", "/tracks/0/effects/0/range/Frequency: 250..40: the lowest value comes first"},
		"empty":         {"[40, 250]", "[40, 40]", "/tracks/0/effects/0/range/Frequency: 40..40: the lowest value comes first"},
		"on a file":     {"instrument: {plugin: synth, preset: Warm Pad}", `instrument: {file: "x/y.wav", range: {Gain: [0, 1]}}`, "/tracks/0/instrument/range: a file has no parameters to map"},
		"master effect": {"arrangement: [one, two]", "arrangement: [one, two]\nmaster: [{plugin: hipass, params: {Frequency: 80}, range: {Frequency: [80, 40]}}]", "/master/0/range/Frequency: 80..40"},
	} {
		_, err := Parse([]byte(strings.Replace(rangeSong, tc.from, tc.to, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"no such param":   {"range: {Frequency: [40, 250]}", "range: {Freqency: [40, 250]}", `/tracks/0/effects/0/range/Freqency: Test High Pass has no writable parameter "Freqency"`},
		"beyond its own":  {"[40, 250]", "[5, 250]", "/tracks/0/effects/0/range/Frequency: 5..250 lies outside Frequency's range 10..30000"},
		"not declared":    {"range: {Frequency: [40, 250]}", "range: {Frequency: [40, 250], Mix: [0, 0.5]}", "/tracks/0/effects/0/range/Mix: the plugin's params do not set Mix"},
		"named twice":     {"range: {Frequency: [40, 250]}", "range: {Frequency: [40, 250], freq: [20, 400]}", `"Frequency" and "freq" are both Frequency; give it one range`},
		"value outside":   {"{Frequency: 80, Mix: 1}", "{Frequency: 30, Mix: 1}", "/params/Frequency: 30 is outside Frequency's mapping range 40..250"},
		"scene outside":   {"params: {Frequency: 145}", "params: {Frequency: 300}", "/scenes/open/pad/effects/hp/params/Frequency: 300 is outside Frequency's mapping range 40..250"},
		"a share outside": {"params: {Frequency: 145}", `params: {Frequency: "50%"}`, "Frequency at 50% is 15005, outside its mapping range 40..250"},
	} {
		doc := strings.Replace(rangeSong, tc.from, tc.to, 1)
		if name == "not declared" {
			doc = strings.Replace(doc, "{Frequency: 80, Mix: 1}", "{Frequency: 80}", 1)
		}
		_, err := NewPlan(mustParse(t, doc), rangeCatalog())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
	// A ramp beyond the range is found where the song compiles.
	doc := strings.Replace(rangeSong, "from: 40, to: 250", "from: 40, to: 400", 1)
	if _, err := Compile(mustParse(t, doc), rangePlan(t, doc)); err == nil || !strings.Contains(err.Error(), "400 is outside Frequency's mapping range 40..250") {
		t.Errorf("a ramp past the range: %v", err)
	}
}

// TestRangeStemStrip checks a stem's strip takes ranges like a track does,
// and its mix plays through them.
func TestRangeStemStrip(t *testing.T) {
	doc := strings.Replace(rangeSong, "arrangement: [one, two]",
		"arrangement: [one, two]\nstems:\n  pad:\n    effects: [{id: low, plugin: hipass, params: {Frequency: 80}, range: {Frequency: [40, 250]}}]", 1)
	if _, err := NewPlan(mustParse(t, doc), rangeCatalog()); err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	bad := strings.Replace(doc, "params: {Frequency: 80}, range", "params: {Frequency: 30}, range", 1)
	if _, err := NewPlan(mustParse(t, bad), rangeCatalog()); err == nil || !strings.Contains(err.Error(), "/stems/pad/effects/0/params/Frequency: 30 is outside Frequency's mapping range 40..250") {
		t.Errorf("a stem's value past its range: %v", err)
	}
	s := mustParse(t, doc)
	stems, err := s.Stems(nil)
	if err != nil {
		t.Fatalf("Stems: %v", err)
	}
	l, err := s.StemLayout(0)
	if err != nil {
		t.Fatalf("StemLayout: %v", err)
	}
	mix, err := s.MixSong(stems, map[string]string{"pad": "Range Song stem pad/Master - 120 bpm - 001.wav"}, "Range Song mix", l)
	if err != nil {
		t.Fatalf("MixSong: %v", err)
	}
	p, err := NewPlan(mix, rangeCatalog())
	if err != nil {
		t.Fatalf("plan of the mix: %v", err)
	}
	low, err := p.Node("pad", "low").param("Frequency")
	if err != nil || len(low.Range) != 2 || low.Range[0] != 40 || low.Range[1] != 250 {
		t.Errorf("the mix's pad/low Frequency has range %v (%v), want [40 250]", low.Range, err)
	}
}

// TestLogTravel checks a parameter its plugin shows on a log scale is sent
// along that scale, as AUM moves it: the cutoffs measured on AUHighPassFilter
// at the tap (docs/songs.md, Status, "Mapping ranges").
func TestLogTravel(t *testing.T) {
	if !LogTravel(cutoff) {
		t.Fatal("the DisplayLogarithmic flag alone does not make a log travel")
	}
	withBool := hpFreq
	withBool.DisplayLogarithmic = true
	if !LogTravel(withBool) || LogTravel(hpFreq) {
		t.Errorf("LogTravel: DisplayLogarithmic %v, plain %v; want true, false", LogTravel(withBool), LogTravel(hpFreq))
	}
	fromZero := withBool
	fromZero.Min = 0
	if LogTravel(fromZero) {
		t.Error("a range from 0 has no log scale, yet LogTravel says it has")
	}
	// The CCs played on the rig, and the cutoffs they set: 10·2205^(cc/127).
	for _, tc := range []struct {
		cc   int
		want float64 // Hz
	}{{0, 10}, {23, 40.3}, {76, 1001.8}, {102, 4844.6}, {127, 22050}} {
		if got := ValueAt(cutoff, float64(tc.cc)/127); math.Abs(got-tc.want) > 0.001*tc.want+0.05 {
			t.Errorf("CC %d sets %.1f Hz, want %.1f", tc.cc, got, tc.want)
		}
		if w, err := paramWire(cutoff, Number(tc.want)); err != nil || w != tc.cc {
			t.Errorf("paramWire(%g Hz) = %d, %v; want %d", tc.want, w, err, tc.cc)
		}
	}
	// A share is of the travel: 50 % is the middle of the scale, about
	// 470 Hz, as AUM's slider shows it.
	if w, _ := paramWire(cutoff, Value{Str: "50%", IsStr: true}); w != 64 {
		t.Errorf("50%% is sent as %d, want 64", w)
	}
	for _, x := range []float64{10, 33.3, 80, 1000, 22050} {
		if got := ValueAt(cutoff, Travel(cutoff, x)); math.Abs(got-x) > 1e-9*x {
			t.Errorf("ValueAt(Travel(%g)) = %g", x, got)
		}
	}
}

// TestLogTravelRange checks a mapping range of a log parameter: AUM stores
// its ends as places on the log scale, and spreads the CC's steps evenly
// along it, so an 80 Hz high-pass over 40..250 Hz is within a step of 80 Hz.
func TestLogTravelRange(t *testing.T) {
	w, err := rangeWire(cutoff, 40, 250, Number(80))
	if err != nil || w != 48 {
		t.Fatalf("rangeWire(80 Hz over 40..250) = %d, %v; want 48", w, err)
	}
	a, b := Travel(cutoff, 40), Travel(cutoff, 250)
	got := ValueAt(cutoff, a+float64(w)/127*(b-a))
	step := CCStep(cutoff, 40, 250, 80)
	if math.Abs(got-80) > step/2 {
		t.Errorf("CC %d sets %.2f Hz, more than half a step (%.2f Hz) from 80", w, got, step/2)
	}
	// 40·6.25^(1/127) - 40: the steps grow along the scale, about 1.2 Hz at 80 Hz.
	if step < 1.1 || step > 1.3 {
		t.Errorf("a CC step near 80 Hz over 40..250 is %.3f Hz, want about 1.2", step)
	}
	if lin := CCStep(hpFreq, 40, 250, 80); math.Abs(lin-210.0/127) > 1e-9 {
		t.Errorf("a linear parameter's step is %g, want %g everywhere", lin, 210.0/127)
	}
	if whole := CCStep(cutoff, 0, 0, 1000); whole < 55 || whole > 65 {
		t.Errorf("a CC step near 1 kHz over the whole range is %.1f Hz, want about 61", whole)
	}
	for _, tc := range []struct {
		v    Value
		want int
	}{{Number(40), 0}, {Number(250), 127}, {Number(100), 64}} {
		if got, err := rangeWire(cutoff, 40, 250, tc.v); err != nil || got != tc.want {
			t.Errorf("rangeWire(%s) = %d, %v; want %d", tc.v, got, err, tc.want)
		}
	}
	if _, err := rangeWire(cutoff, 40, 250, Value{Str: "50%", IsStr: true}); err == nil || !strings.Contains(err.Error(), "Cutoff Frequency at 50% is 469.574") {
		t.Errorf("50%% over 40..250: %v, want it refused as 469.6 Hz", err)
	}

	// The session's mapping spans the two ends' places on the log scale.
	cat := rangeCatalog()
	dump := cat["hipass"]
	dump.Parameters[0] = cutoff
	dump.Parameters[0].Identifier, dump.Parameters[0].KeyPath, dump.Parameters[0].DisplayName = "freq", "freq", "Frequency"
	cat["hipass"] = dump
	p, err := NewPlan(mustParse(t, rangeSong), cat)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	freq, err := p.Node("pad", "hp").param("Frequency")
	if err != nil {
		t.Fatal(err)
	}
	if lo, hi := freq.mapRange(); math.Abs(lo-a) > 1e-12 || math.Abs(hi-b) > 1e-12 {
		t.Errorf("the mapping spans %g..%g, want %g..%g", lo, hi, a, b)
	}
	home, err := p.HomeState()
	if err != nil {
		t.Fatalf("HomeState: %v", err)
	}
	if v, _ := stateValue(home, freq.At); v != 48 {
		t.Errorf("the declaration's 80 Hz is sent as %d, want 48", v)
	}
	// A session authored with the linear places has another fingerprint, so
	// it is authored again; a linear parameter's range keeps its own.
	if lin := rangePlan(t, rangeSong).Fingerprint(); lin == p.Fingerprint() {
		t.Error("a range on a log scale has the fingerprint of the same range on a linear one")
	}
}
