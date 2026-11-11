package song

// A review reads a song as a piece of music, where Validate reads it as a
// document. It is there because of what the first songs written with these
// tools had in common, whoever wrote them: none ran two and a half minutes,
// every one ended in a fade-out, every part was a loop of four or eight bars,
// none had more than five tracks, and all of them drew on the same few
// plugins. Nothing in the tools forced that, and nothing said a word about it
// either, so each song took the one before it for its model.
//
// A review says it. It is advice and never an error: a song that is meant to
// be short, or to fade, is saved and played as it is.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// What a review holds a song against.
const (
	// A full song runs fullSongMin to fullSongMax; one under shortSong gets a
	// remark on its length.
	shortSong   = 150 * time.Second
	fullSongMin = 3 * time.Minute
	fullSongMax = 4*time.Minute + 30*time.Second
	// fadeDropDB is how far a level ramp to the end of the last section has
	// to fall to count as a fade-out.
	fadeDropDB = 12.0
	// loopBars is the longest phrase that still counts as a short loop, in a
	// song of at least loopSongBars.
	loopBars     = 4
	loopSongBars = 16
	// fewTracks is the most tracks a review still calls few.
	fewTracks = 5
	// sameSection is how often a section is heard unchanged before a review
	// says so.
	sameSection = 3
	// mostNamed is how many plugins a remark lists by name.
	mostNamed = 4
)

// What a remark is about.
const (
	AboutLength = "length"
	AboutEnding = "ending"
	AboutForm   = "form"
	AboutTracks = "tracks"
	AboutSounds = "sounds"
)

// Remark is one thing a review says about a song.
type Remark struct {
	About string `json:"about"`
	Text  string `json:"text"`
}

// Voice is one plugin a song hosts with a factory preset it names for it.
type Voice struct {
	// Track is the track the plugin sits on, "master" for a master effect.
	Track string
	// Plugin is the probe id and Name what the plugin calls itself.
	Plugin string
	Name   string
	// Preset is "" for the plugin's default patch.
	Preset string
	// Instrument tells a track's instrument from an effect.
	Instrument bool
}

// Library is what a review compares a song with: the other songs of the rig
// and what is installed on it.
type Library struct {
	// Songs is the palette of every other stored song, by song id.
	Songs map[string][]Voice
	// Instruments and Effects count the installed plugins (0: not known).
	Instruments, Effects int
}

// Palette lists the plugins a song hosts: every track's instrument, once for
// each preset the song names for it (its own and its scenes'), every track's
// effects and the master's. A plugin the catalogue does not know is left out.
func (s *Song) Palette(cat Catalog) []Voice {
	var out []Voice
	add := func(track string, pl Plugin, instrument bool, presets ...string) {
		id, err := cat.Resolve(pl.Plugin)
		if err != nil {
			return
		}
		for _, preset := range presets {
			out = append(out, Voice{Track: track, Plugin: id, Name: cat[id].Name, Preset: preset, Instrument: instrument})
		}
	}
	for _, t := range s.Tracks {
		presets := []string{t.Instrument.Preset}
		for _, scene := range sortedKeys(s.Scenes) {
			preset := s.Scenes[scene][t.ID].Preset
			known := preset == ""
			for _, p := range presets {
				known = known || strings.EqualFold(p, preset)
			}
			if !known {
				presets = append(presets, preset)
			}
		}
		add(t.ID, t.Instrument, true, presets...)
		for _, e := range t.Effects {
			add(t.ID, e, false, e.Preset)
		}
	}
	for _, e := range s.Master {
		add("master", e, false, e.Preset)
	}
	return out
}

// Review says what stands out in a compiled song as a piece of music: its
// length, how it ends, how much of it is the same thing again, how many
// tracks it has, and which of its sounds the rig's other songs already have.
// A song without a note (a sound lab) gets no review.
func Review(s *Song, tl *Timeline, cat Catalog, lib Library) []Remark {
	if s == nil || tl == nil || len(s.Arrangement) == 0 {
		return nil
	}
	notes := 0
	for _, t := range tl.Tracks {
		notes += t.Notes
	}
	if notes == 0 {
		return nil
	}
	r := &review{song: s, tl: tl}
	r.length()
	r.ending()
	r.form()
	r.tracks()
	r.sounds(cat, lib)
	return r.out
}

type review struct {
	song *Song
	tl   *Timeline
	out  []Remark
}

func (r *review) addf(about, format string, a ...any) {
	r.out = append(r.out, Remark{About: about, Text: fmt.Sprintf(format, a...)})
}

// length remarks on a song that is over well before a full song would be.
func (r *review) length() {
	d := r.tl.Duration()
	bar := r.tl.TickTime(r.tl.BarTicks)
	if d >= shortSong || bar <= 0 {
		return
	}
	r.addf(AboutLength, "%s long (%d bars). A full song runs 3 to 4½ minutes: %d to %d bars at this tempo.",
		clockOf(d), r.tl.Bars,
		int(math.Ceil(float64(fullSongMin)/float64(bar))), int(float64(fullSongMax)/float64(bar)))
}

// ending remarks on a fade-out: level ramps that take at least half of the
// tracks playing in the last section down, to the section's end.
func (r *review) ending() {
	name := r.song.Arrangement[len(r.song.Arrangement)-1]
	last := r.song.Sections[name]
	faded := map[string]bool{}
	for _, a := range last.Automation {
		if a.Effect != "" || !strings.EqualFold(a.Param, "level") || a.From.IsStr || a.To.IsStr {
			continue
		}
		start := a.Bar
		if start < 1 {
			start = 1
		}
		toEnd := a.Bars <= 0 || start-1+a.Bars >= last.Bars
		if toEnd && a.From.Num-a.To.Num >= fadeDropDB {
			faded[a.Track] = true
		}
	}
	playing, down := 0, 0
	for _, t := range r.song.Tracks {
		if len(last.Play[t.ID]) == 0 {
			continue
		}
		playing++
		if faded[t.ID] {
			down++
		}
	}
	if down == 0 || 2*down < playing {
		return
	}
	who := "every track playing in it"
	if down < playing {
		who = fmt.Sprintf("%d of the %d tracks playing in it", down, playing)
	}
	r.addf(AboutEnding, "ends in a fade-out: %q ramps the level of %s down. A fade is the rare ending. "+
		"End on a last chord or hit and let it ring (a recording waits for the tail), stop together on a beat, or leave the last line to one instrument.",
		name, who)
}

// form remarks on sections heard unchanged several times and on a song made
// of short loops only.
func (r *review) form() {
	s := r.song

	// Sections that are the same under whatever name: what plays, with which
	// scenes and ramps, for how long.
	type group struct {
		ids   []string
		heard int
	}
	groups := map[string]*group{}
	var order []string
	for _, id := range s.Arrangement {
		key := sectionKey(s.Sections[id])
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		g.heard++
		known := false
		for _, have := range g.ids {
			known = known || have == id
		}
		if !known {
			g.ids = append(g.ids, id)
		}
	}
	var same []string
	for _, key := range order {
		g := groups[key]
		if g.heard < sameSection {
			continue
		}
		what := fmt.Sprintf("%q", g.ids[0])
		if len(g.ids) > 1 {
			also := make([]string, len(g.ids)-1)
			for i, id := range g.ids[1:] {
				also[i] = fmt.Sprintf("%q", id)
			}
			what += " (the same as " + strings.Join(also, ", ") + ")"
		}
		same = append(same, fmt.Sprintf("%s is heard %d times unchanged", what, g.heard))
	}
	if len(same) > 0 {
		r.addf(AboutForm, "%s. A repeat can be a section of its own with something changed: another scene, a track more or fewer, a pattern list with a different last entry.",
			strings.Join(same, "; "))
	}

	// The longest phrase any track plays before it starts over.
	m, err := parseMeter(s.TimeSignature)
	if err != nil || r.tl.Bars < loopSongBars {
		return
	}
	bars := map[string]int{}
	longest := 0
	for _, id := range s.Arrangement {
		sec := s.Sections[id]
		for _, ids := range sec.Play {
			phrase := 0
			for _, pid := range ids {
				n, ok := bars[pid]
				if !ok {
					if cp, err := compilePattern(pid, s.Patterns[pid], m, s.Swing, nil); err == nil {
						n = int(cp.ticks / m.barTicks)
					}
					bars[pid] = n
				}
				phrase += n
			}
			if phrase > sec.Bars {
				phrase = sec.Bars
			}
			if phrase > longest {
				longest = phrase
			}
		}
	}
	if longest > 0 && longest <= loopBars {
		r.addf(AboutForm, "no part has a phrase longer than %s, so every section is a short loop. A melody or a chord progression over 8 or 16 bars gives a section somewhere to go.",
			countOf(longest, "bar"))
	}
}

// sectionKey is what makes two sections the same to the ear.
func sectionKey(sec Section) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|", sec.Bars, strings.Join(sec.Scene, ","))
	for _, track := range sortedKeys(sec.Play) {
		fmt.Fprintf(&b, "%s=%s;", track, strings.Join(sec.Play[track], ","))
	}
	for _, a := range sec.Automation {
		fmt.Fprintf(&b, "|%s/%s/%s:%v>%v@%d+%d", a.Track, a.Effect, a.Param, a.From, a.To, a.Bar, a.Bars)
	}
	return b.String()
}

// tracks remarks on an arrangement of few tracks.
func (r *review) tracks() {
	if n := len(r.song.Tracks); n <= fewTracks {
		r.addf(AboutTracks, "%s; a song can have %d. A fuller arrangement adds what only some sections play: a second harmony part, a counter-melody, percussion beside the kit, a texture, an effects track.",
			countOf(n, "track"), maxTracks)
	}
}

// sounds remarks on what the song shares with the rig's other songs: the very
// preset of an instrument, and a palette with no plugin of its own.
func (r *review) sounds(cat Catalog, lib Library) {
	if len(lib.Songs) == 0 {
		return
	}
	mine := r.song.Palette(cat)
	ids := sortedKeys(lib.Songs)

	// Which other songs have a plugin, and which an instrument's preset.
	type key struct {
		plugin     string
		instrument bool
	}
	plugins := map[key]map[string]bool{}
	presets := map[string][]string{}
	for _, id := range ids {
		seen := map[string]bool{}
		for _, v := range lib.Songs[id] {
			k := key{v.Plugin, v.Instrument}
			if plugins[k] == nil {
				plugins[k] = map[string]bool{}
			}
			plugins[k][id] = true
			if pk := presetKey(v); v.Instrument && v.Preset != "" && !seen[pk] {
				seen[pk] = true
				presets[pk] = append(presets[pk], id)
			}
		}
	}

	var shared []string
	told := map[string]bool{}
	for _, v := range mine {
		pk := presetKey(v)
		if !v.Instrument || v.Preset == "" || len(presets[pk]) == 0 || told[v.Track+"\x00"+pk] {
			continue
		}
		told[v.Track+"\x00"+pk] = true
		item := fmt.Sprintf("%s has %s %q, as in %s", v.Track, v.Name, v.Preset, strings.Join(presets[pk], ", "))
		if n := len(cat[v.Plugin].FactoryPresets); n > 1 {
			item += fmt.Sprintf(" (one of its %d presets)", n)
		}
		shared = append(shared, item)
	}
	if n := len(shared); n > 0 {
		if n > mostNamed {
			shared = append(shared[:mostNamed], fmt.Sprintf("%d more", n-mostNamed))
		}
		r.addf(AboutSounds, "the same sound as another stored song: %s.", strings.Join(shared, "; "))
	}

	// A palette is worth a remark when none of it is new and there are songs
	// enough for that to mean something.
	if len(ids) < 2 {
		return
	}
	for _, instrument := range []bool{true, false} {
		kind, installed := "effect", lib.Effects
		if instrument {
			kind, installed = "instrument", lib.Instruments
		}
		type use struct {
			name  string
			songs int
		}
		var uses []use
		seen := map[string]bool{}
		fresh := false
		for _, v := range mine {
			if v.Instrument != instrument || seen[v.Plugin] {
				continue
			}
			seen[v.Plugin] = true
			n := len(plugins[key{v.Plugin, instrument}])
			fresh = fresh || n == 0
			uses = append(uses, use{v.Name, n})
		}
		// One effect is the limiter on the master more often than a choice.
		if len(uses) == 0 || fresh || (!instrument && len(uses) < 2) {
			continue
		}
		sort.SliceStable(uses, func(i, j int) bool { return uses[i].songs > uses[j].songs })
		named := make([]string, 0, mostNamed+1)
		for i, u := range uses {
			if i == mostNamed {
				named = append(named, fmt.Sprintf("%d more", len(uses)-mostNamed))
				break
			}
			named = append(named, fmt.Sprintf("%s (in %d)", u.name, u.songs))
		}
		text := fmt.Sprintf("every %s is one the %d other stored songs already use: %s.", kind, len(ids), strings.Join(named, ", "))
		if installed > 0 {
			used := 0
			for k := range plugins {
				if k.instrument == instrument {
					used++
				}
			}
			text += fmt.Sprintf(" %d %ss are installed and those songs use %d of them (list_song_plugins).", installed, kind, used)
		}
		r.addf(AboutSounds, "%s", text)
	}
}

// presetKey names an instrument's sound: the plugin and its preset.
func presetKey(v Voice) string {
	return v.Plugin + "\x00" + strings.ToLower(v.Preset)
}

// countOf renders a count with its noun: "1 bar", "4 bars".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// clockOf renders a duration as m:ss.
func clockOf(d time.Duration) string {
	sec := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
