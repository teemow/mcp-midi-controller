// Package craft holds what the craft tools know as data: the capabilities a
// technique can ask for, the technique cards that say what a craft does, the
// plugin cards that say how one plugin provides a capability, and the rig's
// order of preference between plugins. See docs/craft-cards.md.
//
// A technique names capabilities (eq.highpass), never a plugin; a plugin card
// says how one plugin provides them; the rig's order of preference picks the
// plugin. That keeps a technique true when the rig changes, and makes a new
// plugin usable by every technique once it has a card.
//
// The capabilities are a closed list in Go: a new one is a code change, with
// its parameters, their units and ranges, and the measurement profile_plugin
// checks a plugin with. Cards are YAML and checked against the list when they
// load: an unknown capability or parameter is an error.
package craft

import (
	"fmt"
	"slices"
	"sort"
)

// Units of a capability's parameters and of a measurement.
const (
	UnitHz      = "Hz"
	UnitDB      = "dB"
	UnitDBFS    = "dBFS"
	UnitDBTP    = "dBTP"
	UnitLUFS    = "LUFS"
	UnitLU      = "LU"
	UnitMS      = "ms"
	UnitRatio   = ":1"
	UnitDBOct   = "dB/oct"
	UnitPercent = "%"
	UnitQ       = "Q"
	UnitNone    = ""
)

// Param is one parameter of a capability (or an option of a transform): a
// number in a unit within a range, or one of a few labels.
type Param struct {
	Name string  `json:"name"`
	Unit string  `json:"unit,omitempty"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	// Values are the labels of an enumerated parameter; a card then writes
	// one of them rather than a number.
	Values []string `json:"values,omitempty"`
	Doc    string   `json:"doc"`
}

// Enumerated reports whether the parameter takes a label rather than a number.
func (p Param) Enumerated() bool { return len(p.Values) > 0 }

// Range renders the parameter's range for a person.
func (p Param) Range() string {
	if p.Enumerated() {
		return fmt.Sprintf("one of %v", p.Values)
	}
	return fmt.Sprintf("%g..%g %s", p.Min, p.Max, p.Unit)
}

// Measurement is what is measured at a tap: by profile_plugin while it sweeps
// one parameter of a plugin, and by a technique's verify before and after.
type Measurement string

// The measurements.
const (
	MeasureBandLevel      Measurement = "band_level"       // level of a band of frequencies (dB)
	MeasureLevel          Measurement = "level"            // rms level (dBFS)
	MeasureTruePeak       Measurement = "true_peak"        // oversampled peak (dBTP)
	MeasureLoudness       Measurement = "loudness"         // integrated loudness, ITU-R BS.1770 (LUFS)
	MeasureLoudnessRange  Measurement = "loudness_range"   // loudness range (LU)
	MeasurePeakToLoudness Measurement = "peak_to_loudness" // true peak above the integrated loudness (dB)
	MeasureCrestFactor    Measurement = "crest_factor"     // peak above rms (dB)
	MeasureBrightness     Measurement = "brightness"       // spectral centroid (Hz)
	MeasureSideShare      Measurement = "side_share"       // share of the side signal, optionally in a band (%)
	MeasureCorrelation    Measurement = "correlation"      // stereo correlation (-1..1)
)

// measurements are the known measurements: their unit and whether they read a
// band of frequencies (required for band_level, optional for side_share).
var measurements = map[Measurement]struct {
	unit string
	band bandUse
	doc  string
}{
	MeasureBandLevel:      {UnitDB, bandRequired, "the level of a band of frequencies"},
	MeasureLevel:          {UnitDBFS, bandNone, "the rms level"},
	MeasureTruePeak:       {UnitDBTP, bandNone, "the true peak, oversampled"},
	MeasureLoudness:       {UnitLUFS, bandNone, "the integrated loudness after ITU-R BS.1770"},
	MeasureLoudnessRange:  {UnitLU, bandNone, "the loudness range"},
	MeasurePeakToLoudness: {UnitDB, bandNone, "the true peak above the integrated loudness"},
	MeasureCrestFactor:    {UnitDB, bandNone, "the peak above the rms level"},
	MeasureBrightness:     {UnitHz, bandNone, "the spectral centroid"},
	MeasureSideShare:      {UnitPercent, bandOptional, "the share of the side signal"},
	MeasureCorrelation:    {UnitNone, bandNone, "the correlation of left and right, -1 to 1"},
}

type bandUse int

const (
	bandNone bandUse = iota
	bandOptional
	bandRequired
)

// Measurements lists the known measurements, sorted.
func Measurements() []Measurement {
	out := make([]Measurement, 0, len(measurements))
	for m := range measurements {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// Unit is the unit a measurement reads in.
func (m Measurement) Unit() string { return measurements[m].unit }

// Doc says what a measurement reads.
func (m Measurement) Doc() string { return measurements[m].doc }

// Known reports whether m is one of the measurements.
func (m Measurement) Known() bool {
	_, ok := measurements[m]
	return ok
}

// Direction is how a reading moves while a parameter rises.
type Direction string

// The directions.
const (
	Rises Direction = "rises"
	Falls Direction = "falls"
)

// Band is a band of frequencies a check reads: relative to a frequency
// parameter of the capability (below, above or around it), or fixed in Hz.
type Band struct {
	Below  string  `json:"below,omitempty"`
	Above  string  `json:"above,omitempty"`
	Around string  `json:"around,omitempty"`
	FromHz float64 `json:"from_hz,omitempty"`
	ToHz   float64 `json:"to_hz,omitempty"`
}

// Check is how profile_plugin tells that a plugin provides a capability: it
// sweeps one parameter and reads a measurement at the tap, which has to move
// the way the capability says.
type Check struct {
	Sweep   string      `json:"sweep"`
	Measure Measurement `json:"measure"`
	Band    *Band       `json:"band,omitempty"`
	Moves   Direction   `json:"moves"`
}

// Capability is one thing a plugin can do, by its parameters.
type Capability struct {
	Name   string  `json:"name"`
	Title  string  `json:"title"`
	Params []Param `json:"params"`
	Check  Check   `json:"check"`
}

// Param returns the capability's parameter called name.
func (c Capability) Param(name string) (Param, bool) {
	for _, p := range c.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// ParamNames lists the capability's parameters in their order.
func (c Capability) ParamNames() []string {
	out := make([]string, len(c.Params))
	for i, p := range c.Params {
		out[i] = p.Name
	}
	return out
}

// Transform is a writing technique's verb: what changes material the agent
// wrote (a voicing, a bass locked to the kick). Like a capability it is code,
// with the options a card may give it.
type Transform struct {
	Name    string  `json:"name"`
	Title   string  `json:"title"`
	Options []Param `json:"options"`
}

// Option returns the transform's option called name.
func (t Transform) Option(name string) (Param, bool) {
	for _, p := range t.Options {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Vocabulary is what cards are checked against: the capabilities and the
// transforms. Default is the daemon's; tests may give their own.
type Vocabulary struct {
	Capabilities map[string]Capability
	Transforms   map[string]Transform
}

// Capability returns the capability called name.
func (v Vocabulary) Capability(name string) (Capability, bool) {
	c, ok := v.Capabilities[name]
	return c, ok
}

// CapabilityNames lists the capabilities, sorted.
func (v Vocabulary) CapabilityNames() []string {
	out := make([]string, 0, len(v.Capabilities))
	for n := range v.Capabilities {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TransformNames lists the transforms, sorted.
func (v Vocabulary) TransformNames() []string {
	out := make([]string, 0, len(v.Transforms))
	for n := range v.Transforms {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Default is the daemon's vocabulary: every capability below and every
// transform. No transform exists yet; the composer's and the drummer's add
// theirs with their code.
func Default() Vocabulary {
	v := Vocabulary{Capabilities: map[string]Capability{}, Transforms: map[string]Transform{}}
	for _, c := range capabilities {
		v.Capabilities[c.Name] = c
	}
	return v
}

// Capabilities lists the daemon's capabilities, sorted by name.
func Capabilities() []Capability {
	out := slices.Clone(capabilities)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Parameters shared by several capabilities.
var (
	slopeParam = Param{Name: "slope", Unit: UnitDBOct, Min: 6, Max: 96, Doc: "how steeply the cut falls"}
	gainParam  = Param{Name: "gain", Unit: UnitDB, Min: -24, Max: 24, Doc: "the boost (positive) or cut (negative)"}
)

// capabilities is the closed list. A new capability is a code change: its
// parameters with unit and range, and the check profile_plugin measures.
var capabilities = []Capability{
	{
		Name:  "eq.highpass",
		Title: "Cut what lies below a frequency",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 10, Max: 2000, Doc: "where the cut begins"},
			slopeParam,
		},
		Check: Check{Sweep: "freq", Measure: MeasureBandLevel, Band: &Band{Below: "freq"}, Moves: Falls},
	},
	{
		Name:  "eq.lowpass",
		Title: "Cut what lies above a frequency",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 200, Max: 22000, Doc: "where the cut begins"},
			slopeParam,
		},
		Check: Check{Sweep: "freq", Measure: MeasureBrightness, Moves: Rises},
	},
	{
		Name:  "eq.low_shelf",
		Title: "Raise or lower everything below a frequency",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 20, Max: 2000, Doc: "the shelf's corner"},
			gainParam,
		},
		Check: Check{Sweep: "gain", Measure: MeasureBandLevel, Band: &Band{Below: "freq"}, Moves: Rises},
	},
	{
		Name:  "eq.high_shelf",
		Title: "Raise or lower everything above a frequency",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 1000, Max: 20000, Doc: "the shelf's corner"},
			gainParam,
		},
		Check: Check{Sweep: "gain", Measure: MeasureBandLevel, Band: &Band{Above: "freq"}, Moves: Rises},
	},
	{
		Name:  "eq.bell",
		Title: "Raise or lower a band around a frequency",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 20, Max: 20000, Doc: "the band's centre"},
			gainParam,
			{Name: "q", Unit: UnitQ, Min: 0.1, Max: 30, Doc: "how narrow the band is"},
		},
		Check: Check{Sweep: "gain", Measure: MeasureBandLevel, Band: &Band{Around: "freq"}, Moves: Rises},
	},
	{
		Name:  "dynamics.compressor",
		Title: "Turn down what rises above a threshold",
		Params: []Param{
			{Name: "threshold", Unit: UnitDB, Min: -60, Max: 0, Doc: "the level above which it turns down"},
			{Name: "ratio", Unit: UnitRatio, Min: 1, Max: 20, Doc: "how much it turns down: 4 lets 1 dB through of every 4 above the threshold"},
			{Name: "attack", Unit: UnitMS, Min: 0.01, Max: 500, Doc: "how fast it turns down"},
			{Name: "release", Unit: UnitMS, Min: 1, Max: 5000, Doc: "how fast it lets go"},
			{Name: "makeup", Unit: UnitDB, Min: 0, Max: 30, Doc: "the gain added after it"},
		},
		Check: Check{Sweep: "threshold", Measure: MeasureCrestFactor, Moves: Rises},
	},
	{
		Name:  "dynamics.limiter",
		Title: "Keep the peaks under a ceiling",
		Params: []Param{
			{Name: "ceiling", Unit: UnitDBFS, Min: -12, Max: 0, Doc: "the highest peak it lets through"},
			{Name: "gain", Unit: UnitDB, Min: 0, Max: 24, Doc: "the gain into it, which raises the loudness"},
			{Name: "release", Unit: UnitMS, Min: 1, Max: 2000, Doc: "how fast it lets go"},
		},
		Check: Check{Sweep: "ceiling", Measure: MeasureTruePeak, Moves: Rises},
	},
	{
		Name:  "color.saturation",
		Title: "Add harmonics by driving a signal into a curve",
		Params: []Param{
			{Name: "drive", Unit: UnitDB, Min: 0, Max: 36, Doc: "how hard it is driven"},
			{Name: "mix", Unit: UnitPercent, Min: 0, Max: 100, Doc: "how much of the driven signal is heard"},
		},
		Check: Check{Sweep: "drive", Measure: MeasureBrightness, Moves: Rises},
	},
	{
		Name:  "stereo.width",
		Title: "Narrow or widen the stereo image",
		Params: []Param{
			{Name: "width", Unit: UnitPercent, Min: 0, Max: 200, Doc: "0 is mono, 100 leaves it as it is"},
		},
		Check: Check{Sweep: "width", Measure: MeasureSideShare, Moves: Rises},
	},
	{
		Name:  "stereo.mono_bass",
		Title: "Make everything below a frequency mono",
		Params: []Param{
			{Name: "freq", Unit: UnitHz, Min: 20, Max: 500, Doc: "below it the signal is mono"},
		},
		Check: Check{Sweep: "freq", Measure: MeasureSideShare, Band: &Band{FromHz: 20, ToHz: 150}, Moves: Falls},
	},
	{
		Name:  "level.gain",
		Title: "Raise or lower the level",
		Params: []Param{
			gainParam,
		},
		Check: Check{Sweep: "gain", Measure: MeasureLevel, Moves: Rises},
	},
}
