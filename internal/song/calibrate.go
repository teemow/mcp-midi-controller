package song

// This file compiles a song against the rig's sound catalogue (calibrate:
// true). What a preset was measured to sound like replaces what had to be
// corrected by hand in the document:
//
//   - notes are sounding pitch: a track's notes are shifted by the
//     transposition of the preset it has in each section;
//   - levels are loudness: a track's level is added to what brings its
//     preset's reference note to ReferenceLevelDB, again per section;
//   - a drum track's lanes are its kit's pads.
//
// A preset the catalogue does not know plays as written, and is noted.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Voicing is what the sound catalogue knows of one preset of an instrument.
type Voicing struct {
	// Transpose is the semitones to add to a written note so it sounds as
	// written.
	Transpose int
	// LevelDB is the rms of the preset's reference note at unity, in dBFS.
	LevelDB float64
	// Lanes is a kit's drum map (lane -> note); nil for a melodic preset.
	Lanes map[string]int
}

// Sounds looks a preset of a plugin up in the catalogue; preset "" is the
// plugin's default patch. ok is false when it was never measured.
type Sounds func(plugin, preset string) (v Voicing, ok bool)

// ReferenceLevelDB is the level a calibrated track's reference note has at
// the track's level 0: every preset is trimmed to it, so a level in dB means
// the same loudness whatever the preset.
const ReferenceLevelDB = -20.0

// TrackVoicing is how one preset a track uses is calibrated.
type TrackVoicing struct {
	Track  string `json:"track"`
	Plugin string `json:"plugin"`
	// Preset is the preset's name, "" for the plugin's default patch.
	Preset string `json:"preset"`
	// Measured is false when the catalogue does not know the preset: it then
	// plays as written.
	Measured bool `json:"measured"`
	// Transpose is the shift of the track's notes while it has the preset,
	// TrimDB what is added to its level, and Pads the size of a kit's drum
	// map.
	Transpose int     `json:"transpose"`
	TrimDB    float64 `json:"trim_db"`
	Pads      int     `json:"pads,omitempty"`
}

func (p *Plan) calibrating() bool { return p.song.Calibrate }

// note records something worth telling whoever wrote the song.
func (p *Plan) note(format string, a ...any) {
	p.notes[fmt.Sprintf(format, a...)] = true
}

// Warnings lists what calibrating the song found: presets that are not
// measured, levels the fader cannot give.
func (p *Plan) Warnings() []string {
	out := make([]string, 0, len(p.notes))
	for n := range p.notes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// presetLabel names a preset for a message.
func presetLabel(preset string) string {
	if preset == "" {
		return "the default patch"
	}
	return fmt.Sprintf("%q", preset)
}

// voicing returns the measured voicing of a track's instrument with a
// preset. ok is false when the song is not calibrated or the preset is not
// measured (which is noted).
func (p *Plan) voicing(tp *TrackPlan, preset string) (Voicing, bool) {
	if !p.calibrating() {
		return Voicing{}, false
	}
	n := tp.Nodes[0]
	if p.sounds != nil {
		if v, ok := p.sounds(n.Plugin, preset); ok {
			return v, true
		}
	}
	p.note("%s: %s of %s is not in the sound catalogue, so it plays as written (audition_sound measures it)", tp.ID, presetLabel(preset), n.Name)
	return Voicing{}, false
}

// sectionPreset is the preset a node has in a section: its own, unless one of
// the section's scenes names another.
func (p *Plan) sectionPreset(sec Section, n *NodePlan) string {
	preset := n.home.Preset
	for _, sn := range sec.Scene {
		ts, ok := p.song.Scenes[sn][n.Track]
		if !ok {
			continue
		}
		if n.Effect == "" {
			if ts.Preset != "" {
				preset = ts.Preset
			}
		} else if fx, ok := ts.Effects[n.Effect]; ok && fx.Preset != "" {
			preset = fx.Preset
		}
	}
	return preset
}

// pitchShift is the shift of a melodic track's notes while it has a preset.
func (p *Plan) pitchShift(tp *TrackPlan, preset string) int {
	v, _ := p.voicing(tp, preset)
	return v.Transpose
}

// kit is the drum map of the kit a drum track has with a preset, nil when it
// is not measured as one.
func (p *Plan) kit(tp *TrackPlan, preset string) map[string]int {
	v, ok := p.voicing(tp, preset)
	if !ok {
		return nil
	}
	if len(v.Lanes) == 0 {
		p.note("%s: %s of %s is in the sound catalogue, but not as a kit, so its lanes play General MIDI's notes (audition_sound kit=true finds the pads)", tp.ID, presetLabel(preset), tp.Nodes[0].Name)
		return nil
	}
	return v.Lanes
}

// trim is what a preset's level is short of the reference.
func (p *Plan) trim(tp *TrackPlan, preset string) float64 {
	v, ok := p.voicing(tp, preset)
	if !ok {
		return 0
	}
	return math.Round((ReferenceLevelDB-v.LevelDB)*10) / 10
}

// fader is the fader position that gives a track its level while it has a
// preset: the level as written, or with the preset's trim when the song is
// calibrated. The fader ends at MaxLevelDB; a level beyond it is clamped and
// noted.
func (p *Plan) fader(tp *TrackPlan, preset string, level float64) float64 {
	if !p.calibrating() {
		return level
	}
	want := level + p.trim(tp, preset)
	if want > MaxLevelDB {
		p.note("%s: %s of %s needs the fader at %+.1f dB for level %g, and it ends at +%g: it plays %.1f dB too quiet; raise the plugin's volume",
			tp.ID, presetLabel(preset), tp.Nodes[0].Name, want, level, MaxLevelDB, want-MaxLevelDB)
		return MaxLevelDB
	}
	return want
}

// homeFader is the fader a track's strip is authored with.
func (p *Plan) homeFader(tp *TrackPlan) float64 {
	return p.fader(tp, tp.Nodes[0].home.Preset, tp.homeLevel)
}

// presetsOf lists the presets a track's instrument has anywhere in the song:
// its own first, then the scenes' in the scenes' order.
func (p *Plan) presetsOf(tp *TrackPlan) []string {
	out := []string{tp.Nodes[0].home.Preset}
	seen := map[string]bool{strings.ToLower(out[0]): true}
	for _, name := range sortedKeys(p.song.Scenes) {
		if ts, ok := p.song.Scenes[name][tp.ID]; ok && ts.Preset != "" && !seen[strings.ToLower(ts.Preset)] {
			seen[strings.ToLower(ts.Preset)] = true
			out = append(out, ts.Preset)
		}
	}
	return out
}

// Calibration lists how every preset the song's tracks use is calibrated,
// nil for a song that is not.
func (p *Plan) Calibration() []TrackVoicing {
	if !p.calibrating() {
		return nil
	}
	var out []TrackVoicing
	for _, tp := range p.Tracks {
		for _, preset := range p.presetsOf(tp) {
			v, ok := p.voicing(tp, preset)
			row := TrackVoicing{Track: tp.ID, Plugin: tp.Nodes[0].Name, Preset: preset, Measured: ok}
			if ok {
				row.TrimDB = p.trim(tp, preset)
				if tp.Drums {
					row.Pads = len(v.Lanes)
				} else {
					row.Transpose = v.Transpose
				}
			}
			out = append(out, row)
		}
	}
	return out
}

// calibrate runs once when a plan is made: it looks every preset the song
// uses up and computes every section's levels, so that what there is to say
// about the calibration is known before anything is compiled or authored.
func (p *Plan) calibrate() error {
	if !p.calibrating() {
		return nil
	}
	p.Calibration()
	for _, tp := range p.Tracks {
		p.homeFader(tp)
	}
	for _, name := range sortedKeys(p.song.Sections) {
		if _, err := p.sectionState(name, p.song.Sections[name]); err != nil {
			return err
		}
	}
	return nil
}
