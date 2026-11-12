package song

// This file takes a song apart into stems and puts it together again from
// their recordings, for a song the iPad cannot compute in one pass.
//
// A stem is a group of the song's tracks. Each is a song of its own with the
// same bars: the group's tracks with their effects, faders, scenes and ramps,
// and nothing on the master. Recorded one after another from the top of AUM's
// transport, the recordings line up on the sample. The mix is a third kind of
// song: one track per stem that plays the stem's recording (a File Player),
// under the whole song's master effects with their scenes and ramps.
//
// What makes the recordings line up is that every stem's session and the
// mix's play the same lead-in before bar 1, and run on for the same tail after
// the last bar. Both are decided here, once, for all of them.

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Stem is one group of a song's tracks that is recorded together.
type Stem struct {
	Name   string   `json:"name"`
	Tracks []string `json:"tracks"`
}

// Stems lists the song's stems, in the order of their first track. A track's
// stem is its group, or itself when it names none: tracks that share a group
// form one stem, and a track without a group is a stem called by its id.
//
// groups, when it is not nil, replaces the document's grouping: stem name to
// track ids. A track it leaves out is a stem of its own.
//
// A stem is recorded from one session and the mix has a track per stem, so a
// stem holds at most maxTracks tracks and a song at most maxTracks stems. A
// song of no more tracks than that fits however it is grouped; one of more
// (StemsOnly) is an error here unless its grouping fits.
func (s *Song) Stems(groups map[string][]string) ([]Stem, error) {
	var e errs
	of := map[string]string{} // track id -> stem name
	known := map[string]bool{}
	for i := range s.Tracks {
		known[s.Tracks[i].ID] = true
	}
	grouped := map[string]bool{} // the tracks a group takes in
	if groups == nil {
		for i := range s.Tracks {
			t := &s.Tracks[i]
			of[t.ID] = t.ID
			if g := strings.TrimSpace(t.Group); g != "" {
				of[t.ID], grouped[t.ID] = g, true
			}
		}
	} else {
		for i := range s.Tracks {
			of[s.Tracks[i].ID] = s.Tracks[i].ID
		}
		listed := map[string]string{}
		for _, name := range sortedKeys(groups) {
			at := "/groups/" + name
			switch {
			case strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name:
				e.addf("%s: a group needs a name without blanks around it", at)
			case name == MasterTrack:
				e.addf("%s: %q is the master strip's name", at, MasterTrack)
			case len(groups[name]) == 0:
				e.addf("%s: list the tracks of the group", at)
			}
			for i, tid := range groups[name] {
				switch other, twice := listed[tid]; {
				case !known[tid]:
					e.addf("%s/%d: no track %q (%s)", at, i, tid, strings.Join(s.trackIDs(), ", "))
				case twice && other == name:
					e.addf("%s/%d: track %q is listed twice", at, i, tid)
				case twice:
					e.addf("%s/%d: track %q is in group %q already", at, i, tid, other)
				default:
					listed[tid] = name
					of[tid], grouped[tid] = name, true
				}
			}
		}
	}
	// A track in no group is a stem of its own, called by its id: a group of
	// that name would take it in unasked.
	clash := map[string]bool{}
	for i := range s.Tracks {
		id := s.Tracks[i].ID
		name := of[id]
		if !grouped[id] || name == id || !known[name] || grouped[name] || clash[name] {
			continue
		}
		clash[name] = true
		at := "/groups/" + name
		if groups == nil {
			at = fmt.Sprintf("/tracks/%d/group", i)
		}
		e.addf("%s: %q is the id of the track %q, which is in no group and so a stem of its own: name the group otherwise, or put %q in it", at, name, name, name)
	}
	if err := e.err(); err != nil {
		return nil, err
	}
	var out []Stem
	at := map[string]int{}
	for i := range s.Tracks {
		id := s.Tracks[i].ID
		name := of[id]
		j, ok := at[name]
		if !ok {
			j = len(out)
			at[name] = j
			out = append(out, Stem{Name: name})
		}
		out[j].Tracks = append(out[j].Tracks, id)
	}
	where, how := "/tracks", "give tracks a group: of their own"
	if groups != nil {
		where, how = "/groups", "list tracks under a group of their own"
	}
	for _, st := range out {
		if len(st.Tracks) > maxTracks {
			e.addf("%s: the stem %q has %d tracks (%s), and one session hosts %d at most: split it (%s)",
				where, st.Name, len(st.Tracks), strings.Join(st.Tracks, ", "), maxTracks, how)
		}
	}
	if len(out) > maxTracks {
		how = "put tracks into a group together (group: on a track)"
		if groups != nil {
			how = "list more tracks under one group"
		}
		e.addf("%s: %d tracks in %d stems (%s). One session hosts %d tracks at most, so a song of more is recorded a stem at a time and played as the mix of the recordings (record_song_stems, mix_song_stems). The mix has a track per stem, so %d stems fit at most: %s",
			where, len(s.Tracks), len(out), stemList(out), maxTracks, maxTracks, how)
	}
	if err := e.err(); err != nil {
		return nil, err
	}
	return out, nil
}

// stemList names stems for a message, each of more than one track with how
// many it has.
func stemList(stems []Stem) string {
	names := make([]string, len(stems))
	for i, st := range stems {
		names[i] = st.Name
		if len(st.Tracks) > 1 {
			names[i] = fmt.Sprintf("%s of %d", st.Name, len(st.Tracks))
		}
	}
	return strings.Join(names, ", ")
}

// trackIDs lists the song's track ids in order.
func (s *Song) trackIDs() []string {
	ids := make([]string, len(s.Tracks))
	for i := range s.Tracks {
		ids[i] = s.Tracks[i].ID
	}
	return ids
}

// StemLayout is what a song's stems and its mix have in common, so that the
// recordings line up: the lead-in each session's sequencer plays before bar 1
// and the bars every one of them runs on after the song's last.
type StemLayout struct {
	LeadBars int `json:"lead_bars"`
	TailBars int `json:"tail_bars"`
}

// defaultStemTail is how long stems ring out after the last bar when nothing
// else is asked for.
const defaultStemTail = 4 * time.Second

// MaxStemTailBars bounds the tail a stem is recorded with.
const MaxStemTailBars = 32

// StemLayout decides the lead-in and the tail of the song's stems. The lead-in
// is the one a session whose opening sound loads presets takes (so a stem
// with preset loads and one without, and the mix, take the same), or the
// song's own when it asks for a longer one. tailBars 0 takes the bars that
// cover four seconds.
func (s *Song) StemLayout(tailBars int) (StemLayout, error) {
	m, err := parseMeter(s.TimeSignature)
	if err != nil {
		return StemLayout{}, err
	}
	if s.Tempo <= 0 {
		return StemLayout{}, fmt.Errorf("/tempo: %g is not a tempo", s.Tempo)
	}
	bar := time.Duration(float64(m.barTicks) * float64(time.Minute) / (s.Tempo * PPQ))
	barsFor := func(d time.Duration) int {
		if bar <= 0 {
			return 1
		}
		return max(1, int(math.Ceil(float64(d)/float64(bar))))
	}
	l := StemLayout{LeadBars: max(barsFor(defaultPreroll), s.SessionOptions().LeadBars), TailBars: tailBars}
	switch {
	case tailBars < 0 || tailBars > MaxStemTailBars:
		return l, fmt.Errorf("/tail_bars: %d is not a tail (1..%d bars)", tailBars, MaxStemTailBars)
	case tailBars == 0:
		l.TailBars = min(barsFor(defaultStemTail), MaxStemTailBars)
	}
	return l, nil
}

// stemTailSection is the section a stem's song and the mix's end with: the
// bars the last notes ring out in.
const stemTailSection = "stem_tail"

// tailName is a section name for the tail that the song does not use.
func (s *Song) tailName() string {
	name := stemTailSection
	for {
		if _, taken := s.Sections[name]; !taken {
			return name
		}
		name += "_"
	}
}

// tailOf is the section that follows the song's last one for bars bars: no
// notes, the last section's scenes, and every control the last section ramped
// held where its ramp ended. So what rings out there rings out in the last
// section's sound, a fade that ended at silence stays there, and no preset is
// loaded under it. keep says which ramps are the derived song's.
func (s *Song) tailOf(bars int, keep func(Ramp) bool) Section {
	tail := Section{Bars: bars}
	if len(s.Arrangement) == 0 {
		return tail
	}
	last := s.Sections[s.Arrangement[len(s.Arrangement)-1]]
	tail.Scene = slices.Clone(last.Scene)
	for _, r := range last.Automation {
		if keep(r) {
			tail.Automation = append(tail.Automation, Ramp{Track: r.Track, Effect: r.Effect, Param: r.Param, From: r.To, To: r.To})
		}
	}
	return tail
}

// derive copies what a song's stems and its mix share with it: the clock and
// the form, with a tail after the last section. keepScene picks the entries of
// the scenes the derived song keeps (by track id, MasterTrack for the
// master's), keepRamp its ramps, and play its tracks' patterns.
func (s *Song) derive(title string, l StemLayout, keepScene func(track string) bool, keepRamp func(Ramp) bool, play bool) *Song {
	out := &Song{
		Title: title, Tempo: s.Tempo, TimeSignature: s.TimeSignature, Key: s.Key, Swing: s.Swing,
		Patterns: map[string]Pattern{}, Scenes: map[string]Scene{}, Sections: map[string]Section{},
		Session: &SessionSpec{Hardware: s.SessionOptions().Hardware, LeadBars: l.LeadBars},
	}
	for name, scene := range s.Scenes {
		kept := Scene{}
		for tid, st := range scene {
			if keepScene(tid) {
				kept[tid] = st
			}
		}
		out.Scenes[name] = kept
	}
	for name, sec := range s.Sections {
		d := Section{Bars: sec.Bars, Scene: slices.Clone(sec.Scene)}
		if play {
			for tid, ids := range sec.Play {
				if keepScene(tid) {
					if d.Play == nil {
						d.Play = map[string]StringList{}
					}
					d.Play[tid] = slices.Clone(ids)
				}
			}
		}
		for _, r := range sec.Automation {
			if keepRamp(r) {
				d.Automation = append(d.Automation, r)
			}
		}
		out.Sections[name] = d
	}
	tail := s.tailName()
	out.Sections[tail] = s.tailOf(l.TailBars, keepRamp)
	out.Arrangement = append(slices.Clone(s.Arrangement), tail)
	return out
}

// StemSong is the song that plays one stem alone: the stem's tracks as the
// whole song has them (plugins, effects, levels, scenes, ramps, channels) for
// the same bars, with nothing on the master, the layout's lead-in and its tail
// after the last bar. Its session hosts those tracks and no others, so the
// rest of the song costs the iPad nothing while the stem is recorded.
//
// A track keeps the channel it has in the whole song. In a song of more
// tracks than a session hosts (StemsOnly) there is no such channel: a track
// plays on the one it names, or on its position in the stem, and the stem's
// own check says when two of its tracks meet on one.
//
// The master's effects are left to the mix, all of them: a limiter must see
// the sum, and a reverb on the sum of the stems is the reverb the whole song
// has, whatever it does.
func (s *Song) StemSong(st Stem, title string, l StemLayout) (*Song, error) {
	in := map[string]bool{}
	for _, tid := range st.Tracks {
		in[tid] = true
	}
	keep := func(track string) bool { return in[track] }
	out := s.derive(title, l, keep, func(r Ramp) bool { return in[r.Track] }, true)
	out.Description = fmt.Sprintf("The stem %q of %q: %s alone, without the master's effects, for recording.", st.Name, s.Title, strings.Join(st.Tracks, ", "))
	out.Calibrate = s.Calibrate
	// Its sequence switches AUM's recorder off at the end (RecorderOffBeats).
	out.stem = true
	for i := range s.Tracks {
		if !in[s.Tracks[i].ID] {
			continue
		}
		t := s.Tracks[i]
		switch {
		case t.Channel != 0:
		case s.StemsOnly():
			// Channels are counted in the stem: st may be another grouping
			// than the document's, which TrackChannel goes by.
			t.Channel = len(out.Tracks) + 1
		default:
			// The channel the track has in the whole song: its control
			// channels were checked against that layout.
			t.Channel = s.TrackChannel(i)
		}
		t.Group = ""
		out.Tracks = append(out.Tracks, t)
	}
	if len(out.Tracks) != len(st.Tracks) {
		return nil, fmt.Errorf("stem %q names a track the song does not have (%s)", st.Name, strings.Join(st.Tracks, ", "))
	}
	// Only the patterns the stem plays: a song of files alone needs none.
	for _, sec := range out.Sections {
		for _, ids := range sec.Play {
			for _, id := range ids {
				out.Patterns[id] = s.Patterns[id]
			}
		}
	}
	if len(out.Patterns) == 0 && !out.filesOnly() {
		// The stem's tracks rest all through; a song still needs a pattern.
		for _, id := range sortedKeys(s.Patterns) {
			out.Patterns[id] = s.Patterns[id]
			break
		}
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("the stem %q: %w", st.Name, err)
	}
	return out, nil
}

// MixSong is the song that plays the stems' recordings together: one track
// per stem, each a File Player with the stem's file (files, by stem name, a
// path in AUM's Recordings) at unity, under the whole song's master effects
// with their scenes and ramps, for the same bars, lead-in and tail. The
// tracks' own faders, scenes and ramps are in the recordings.
func (s *Song) MixSong(stems []Stem, files map[string]string, title string, l StemLayout) (*Song, error) {
	master := func(track string) bool { return track == MasterTrack }
	out := s.derive(title, l, master, func(r Ramp) bool { return r.Track == MasterTrack }, false)
	names := make([]string, len(stems))
	for i, st := range stems {
		names[i] = st.Name
		file := files[st.Name]
		if file == "" {
			return nil, fmt.Errorf("the stem %q has no recording", st.Name)
		}
		out.Tracks = append(out.Tracks, Track{ID: st.Name, Instrument: Plugin{File: file}})
	}
	out.Description = fmt.Sprintf("The mix of %q from the recordings of its stems (%s), through its master effects.", s.Title, strings.Join(names, ", "))
	out.Master = slices.Clone(s.Master)
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("the mix: %w", err)
	}
	return out, nil
}
