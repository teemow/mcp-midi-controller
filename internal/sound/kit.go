package sound

import (
	"fmt"
	"math"
	"sort"
)

// Pad is one note of a drum machine that makes a sound.
type Pad struct {
	Note int `yaml:"note" json:"note"`
	// Lane is the guess at what the pad is, as a song's drum grid names it
	// (kick, snare, hat, …); "" when nothing fits.
	Lane string `yaml:"lane,omitempty" json:"lane,omitempty"`
	// PeakDB is the hit's sample peak and LevelDB the rms of its first tenth
	// of a second, in dBFS.
	PeakDB  float64 `yaml:"peak_db" json:"peak_db"`
	LevelDB float64 `yaml:"level_db" json:"level_db"`
	// LengthMS is how long the hit lasts (down 30 dB); Rings: longer than
	// the capture.
	LengthMS float64 `yaml:"length_ms" json:"length_ms"`
	Rings    bool    `yaml:"rings,omitempty" json:"rings,omitempty"`
	// CentroidHz is where the hit's energy sits, LowPct how much of it lies
	// under 600 Hz (a drum's body; a cymbal has none), and PitchHz its pitch
	// when it has one (toms, kicks).
	CentroidHz float64 `yaml:"centroid_hz" json:"centroid_hz"`
	LowPct     float64 `yaml:"low_pct" json:"low_pct"`
	PitchHz    float64 `yaml:"pitch_hz,omitempty" json:"pitch_hz,omitempty"`
}

const (
	padLengthBelowPeak = 30.0
	padHeadMS          = 100.0
	padFFT             = 4096
)

// MeasureHit analyses the capture of one short note of a drum machine. ok is
// false when the note made no sound: it is not a pad.
func MeasureHit(c Clip, note int) (Pad, bool) {
	p := Pad{Note: note}
	m, peak := mono(c)
	p.PeakDB = round1(dbfs(peak))
	if c.SampleRate <= 0 || len(m) == 0 || p.PeakDB <= silentDB {
		return p, false
	}
	sr := c.SampleRate
	hop := int(sr * envHopMS / 1000)
	env := envelope(m, hop)
	top, topAt := -180.0, 0
	for i, v := range env {
		if v > top {
			top, topAt = v, i
		}
	}
	onset := 0
	for i, v := range env {
		if v >= top-onsetBelowPeak {
			onset = i
			break
		}
	}
	p.Rings = true
	p.LengthMS = float64(len(env)-onset) * envHopMS
	for i := topAt; i < len(env); i++ {
		if env[i] <= top-padLengthBelowPeak {
			p.Rings = false
			p.LengthMS = float64(i-onset) * envHopMS
			break
		}
	}

	head := m[onset*hop : min(onset*hop+int(sr*padHeadMS/1000), len(m))]
	p.LevelDB = round1(rmsDB(head))
	if mag, binHz := spectrum(head, sr, padFFT); mag != nil {
		var sum, weighted, low float64
		for k, a := range mag {
			sum += a * a
			weighted += a * a * float64(k) * binHz
			if float64(k)*binHz < bodyHz {
				low += a * a
			}
		}
		if sum > 0 {
			p.CentroidHz = math.Round(weighted / sum)
			p.LowPct = math.Round(100 * low / sum)
		}
	}
	// A drum's pitch is read off its body, after the click of the attack.
	body := m[min(onset*hop+int(0.015*sr), len(m)):min(onset*hop+int(0.2*sr), len(m))]
	if track := pitchTrack(body, sr); len(track) > 0 {
		sort.Float64s(track)
		p.PitchHz = round1(track[len(track)/2])
	}
	return p, true
}

// Borders between the kinds of drum sound, by where the energy sits and how
// long the hit lasts. They are guesses: a kit decides what its pads are.
const (
	bodyHz       = 600.0  // a drum's body lies under this
	cymbalLowPct = 5.0    // a hat or cymbal has less of its energy there
	lowDrumHz    = 450.0  // kicks and toms sit below
	cymbalHz     = 3500.0 // hats and cymbals above
	kickPitchHz  = 110.0  // a low drum pitched under this may be the kick
	closedHatMS  = 180.0
	openHatMS    = 700.0
	tomPitchHigh = 400.0
)

// NameLanes guesses what each pad of a kit is and returns the kit's drum map
// (lane -> note). It sets every pad's Lane. Lanes are the names a drum grid
// uses; a second pad of a kind gets a number ("hat_2").
func NameLanes(pads []Pad) map[string]int {
	lanes := map[string]int{}
	give := func(p *Pad, lane string) {
		name := lane
		for n := 2; ; n++ {
			if _, taken := lanes[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s_%d", lane, n)
		}
		p.Lane = name
		lanes[name] = p.Note
	}

	// Low drums: the lowest-pitched one is the kick, the others are toms in
	// order of pitch.
	var low []*Pad
	for i := range pads {
		p := &pads[i]
		p.Lane = ""
		if p.CentroidHz < lowDrumHz || (p.PitchHz > 0 && p.PitchHz < tomPitchHigh && p.LowPct >= 50) {
			low = append(low, p)
		}
	}
	isLow := map[*Pad]bool{}
	for _, p := range low {
		isLow[p] = true
	}
	if len(low) > 0 {
		kick := low[0]
		for _, p := range low[1:] {
			// A kick is the deepest sound of a kit: the lowest centre of
			// energy wins, and of equals the first pad.
			if p.CentroidHz < kick.CentroidHz*0.8 {
				kick = p
			}
		}
		if kick.CentroidHz < lowDrumHz && (kick.PitchHz == 0 || kick.PitchHz < kickPitchHz*2) {
			give(kick, "kick")
		}
		var toms []*Pad
		for _, p := range low {
			if p.Lane == "" {
				toms = append(toms, p)
			}
		}
		sort.SliceStable(toms, func(i, j int) bool { return pitchOrCentroid(toms[i]) < pitchOrCentroid(toms[j]) })
		names := []string{"tom_low", "tom_mid", "tom_high"}
		switch len(toms) {
		case 1:
			names = []string{"tom_mid"}
		case 2:
			names = []string{"tom_low", "tom_high"}
		}
		for i, p := range toms {
			give(p, names[min(i, len(names)-1)])
		}
	}

	for i := range pads {
		p := &pads[i]
		if isLow[p] {
			continue
		}
		cymbal := p.CentroidHz >= cymbalHz && p.LowPct < cymbalLowPct
		switch {
		case cymbal && p.LengthMS < closedHatMS && !p.Rings:
			give(p, "hat")
		case cymbal && p.LengthMS < openHatMS && !p.Rings:
			give(p, "open_hat")
		case cymbal:
			give(p, "crash")
		default:
			// Between the drums and the cymbals: noise with a body.
			give(p, "snare")
		}
	}
	return lanes
}

func pitchOrCentroid(p *Pad) float64 {
	if p.PitchHz > 0 {
		return p.PitchHz
	}
	return p.CentroidHz
}
