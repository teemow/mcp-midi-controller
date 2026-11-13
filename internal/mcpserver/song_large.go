package mcpserver

// This file is the song tools' side of a song of more tracks than one session
// hosts (song.StemsOnly). Such a song is one document and several sessions:
// one per stem, which record_song_stems authors, loads and records, and the
// mix's, whose tracks play the recordings. It has no session of its own, so
// what plans one session (load_song, play_song, check_song, mix_song,
// author_song_session) refuses it and says what to do instead; save_song and
// get_song check and summarize it stem by stem.

import (
	"fmt"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// stemsOnlyText is the answer of a tool that needs a song's own session to a
// song that has none.
func stemsOnlyText(id string, sg *song.Song) string {
	return fmt.Sprintf("song %q has %d tracks, more than the %d one session hosts, so it has no session of its own to load, check, measure or play: it plays as its stems and their mix. "+
		"record_song_stems {song: %q, confirm: true} records each stem from a session of its own (get_song_stems follows it), and mix_song_stems {song: %q, confirm: true} writes the mix from the takes as the song %q, loads it and plays it. "+
		"The mix is a song like any other: load_song, play_song, check_song and mix_song take %q.",
		id, len(sg.Tracks), song.MaxSessionTracks, id, id, id+"_mix", id+"_mix")
}

// compileStems checks a song of more tracks than one session hosts against
// the staged plugins and takes it apart. Every plugin, preset and parameter
// is resolved on the whole document first, so a problem is reported with its
// place there; then each stem is planned and compiled as the song it is
// recorded from, which says what only a session's layout can: controls beyond
// what its channels hold, a calibrated preset's lanes and levels.
//
// The stems are the document's, with the tail the kept takes were made with,
// so the summary can say which of them are still of the song as it is.
func (s *Server) compileStems(id string, sg *song.Song) (*stemPlan, error) {
	if err := song.Check(sg, songCatalog()); err != nil {
		return nil, err
	}
	var plan *stemPlan
	var err error
	if kept, ok := loadSongStems()[id]; ok && kept.Layout.TailBars > 0 {
		plan, err = s.planStemsOf(id, sg, nil, kept.Layout.TailBars)
	}
	if plan == nil || err != nil {
		if plan, err = s.planStemsOf(id, sg, nil, 0); err != nil {
			return nil, err
		}
	}
	// The whole song's bars and sections: its notes, which need no session.
	if plan.notes, err = song.Compile(sg, nil); err != nil {
		return nil, err
	}
	return plan, nil
}

// stemsState is where the recordings of a song's stems stand.
type stemsState struct {
	// Ready says every stem has a take of the song as it is now, so
	// mix_song_stems can write the mix.
	Ready bool `json:"ready"`
	// Current are the stems with such a take, Changed those whose take is of
	// the song as it was before a change, and Missing those without one.
	Current []string `json:"current,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Missing []string `json:"missing,omitempty"`
	// Mix is the stored mix of the song, when there is one under its usual
	// id: a song of file tracks that load_song and play_song take.
	Mix string `json:"mix,omitempty"`
}

// summarizeStems renders a song taken apart by compileStems as text and as its
// structured summary: the whole song's form, and its tracks stem by stem,
// each with the channel it has in its stem's session.
func (s *Server) summarizeStems(p *stemPlan) (string, songSummary) {
	tl, src := p.notes, p.src
	sum := songSummary{
		ID: p.id, Title: src.Title, Tempo: tl.Tempo, Bars: tl.Bars, Seconds: tl.Duration().Seconds(),
		Tracks: []songTrackRow{}, Stems: p.stems, StemsOnly: true,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q (%s): %g BPM, %d bars, %s\n", src.Title, p.id, tl.Tempo, tl.Bars, clock(tl.Duration()))
	fmt.Fprintf(&b, "%d tracks in %d stems: more than the %d one session hosts, so the song has no session of its own and plays as its stems and their mix\n",
		len(src.Tracks), len(p.stems), song.MaxSessionTracks)
	sum.Sections = arrangementRows(&b, tl)

	groups := map[string]string{}
	for i := range src.Tracks {
		groups[src.Tracks[i].ID] = src.Tracks[i].Group
	}
	b.WriteString("stems (each is recorded alone, from a session of its own that hosts its tracks; a track's MIDI channel is its channel there):\n")
	var calibration []song.TrackVoicing
	for k, st := range p.stems {
		cs := p.songs[k]
		fmt.Fprintf(&b, "  %s: %d track(s)%s\n", st.Name, len(st.Tracks), stemStripText(src, st.Name, p.songs[k].catalog))
		for i := range cs.plan.Tracks {
			row := trackRow(&b, "  ", cs, i, groups[cs.plan.Tracks[i].ID])
			if row.File != "" {
				sum.Files++
			}
			sum.Tracks = append(sum.Tracks, row)
		}
		calibration = append(calibration, cs.plan.Calibration()...)
		sum.Warnings = append(sum.Warnings, cs.plan.Warnings()...)
	}
	if len(src.Master) > 0 {
		cat := p.songs[0].catalog
		names := make([]string, len(src.Master))
		for i, fx := range src.Master {
			names[i] = fx.Plugin
			if id, err := cat.Resolve(fx.Plugin); err == nil {
				names[i] = cat[id].Name
			}
		}
		fmt.Fprintf(&b, "  master: %s (in the mix: a stem is recorded without the master's effects)\n", strings.Join(names, " > "))
	}
	if src.Calibrate {
		sum.Calibration = calibration
		calibrationText(&b, calibration, sum.Warnings)
	}

	// Where a song of one session says what its session holds, this one says
	// what is recorded of it.
	state := &stemsState{}
	rep := stemsReportOf(p, loadSongStems()[p.id])
	for i, t := range rep.Stems {
		switch {
		case t.File == "":
			state.Missing = append(state.Missing, t.Stem)
		case t.Fingerprint != stemFingerprint(p.songs[i]):
			state.Changed = append(state.Changed, t.Stem)
		default:
			state.Current = append(state.Current, t.Stem)
		}
	}
	state.Ready = rep.Ready
	mixID := p.id + "_mix"
	if mix, _, err := s.songs.Load(mixID); err == nil && mix.HasFiles() {
		state.Mix = mixID
	}
	sum.Takes = state
	fmt.Fprintf(&b, "takes: %d of the %d stems have a take of the song as it is now", len(state.Current), len(p.stems))
	if len(state.Changed) > 0 {
		fmt.Fprintf(&b, "; changed since their take: %s", strings.Join(state.Changed, ", "))
	}
	if len(state.Missing) > 0 && len(state.Missing) < len(p.stems) {
		fmt.Fprintf(&b, "; no take: %s", strings.Join(state.Missing, ", "))
	}
	b.WriteByte('\n')
	if state.Ready {
		fmt.Fprintf(&b, "next: mix_song_stems {song: %q, confirm: true} writes the mix from the takes as the song %q, loads it and plays it\n", p.id, mixID)
	} else {
		fmt.Fprintf(&b, "next: record_song_stems {song: %q, confirm: true} records the stems without a current take (get_song_stems follows it), then mix_song_stems {song: %q, confirm: true}\n", p.id, p.id)
	}
	if state.Mix != "" {
		fmt.Fprintf(&b, "mix: the song %q, a song like any other (load_song, play_song, check_song and mix_song take it); mix_song_stems writes it anew\n", state.Mix)
	}

	if sum.Review = song.Review(src, tl, p.songs[0].catalog, s.songLibrary(p.id, p.songs[0].catalog)); len(sum.Review) > 0 {
		b.WriteString("review (the song as music; advice, none of it an error):\n")
		for _, r := range sum.Review {
			fmt.Fprintf(&b, "  %s: %s\n", r.About, r.Text)
		}
	}
	return b.String(), sum
}

// stemStripText says what a stem's strip in the mix is (song.StemStrip), as a
// suffix to the line that lists the stem: "" for a stem without one, which
// plays at unity and dry.
func stemStripText(sg *song.Song, stem string, cat song.Catalog) string {
	strip, ok := sg.StemStrips[stem]
	if !ok {
		return ""
	}
	parts := []string{"0 dB"}
	if strip.Level != nil {
		parts[0] = fmt.Sprintf("%g dB", *strip.Level)
	}
	for _, fx := range strip.Effects {
		name := fx.Plugin
		if id, err := cat.Resolve(fx.Plugin); err == nil {
			name = cat[id].Name
		}
		parts = append(parts, name)
	}
	return "; in the mix: " + strings.Join(parts, " > ")
}
