package mcpserver

// apply_technique: a settings card applied to a track, a section, the master
// or the song. The patch comes from craft.Library.Apply; this file works out
// what the card reads from the song (its tempo, beat and bar, the lowest note
// of what it is applied to), the chain it goes into, and answers in the one
// shape every craft tool answers in (RunCraftTool). With verify it plays what
// changed and measures before and after (craft_verify.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

const applyTechniqueDescription = "Apply a settings technique card to a song: to a track (track: its id), to the master or the whole song (track: \"master\", the default for a card that applies to the master or the song), or to one section of a track or the master (section, for a card that applies to sections: the effect is declared bypassed and a scene switches it on there). " +
	"For every capability the card needs it takes the first plugin in the rig's order of preference (craft/prefer.yaml) whose plugin card and probe dump carry it at the card's precision, along the parameter's travel and through its mapping range; when none does it names the closest plugin and how far off it comes, and changes nothing. " +
	"The card's values are worked out for the song (tempo, beat_ms, bar_ms, the lowest note of what it is applied to; section.energy when the song gives its sections one) and converted into the plugin's own units; the effect goes where the card places it (first, last, or before or after the effect that provides another capability; applied again, it is set where it is). " +
	"Answers in the craft shape (craft, techniques, patch, revision, why, checks, applied). Without apply the patch is only checked (patch_song stores it later, against the same revision); with apply: true it is stored through save_song. " +
	"verify: true (with apply and confirm) plays what changed before and after and measures it the way the card says; it runs in the background like mix_song, counts as busy, and reports through get_craft_job."

func (s *Server) registerApplyTechnique() {
	s.mcp.AddTool(&mcp.Tool{
		Name:        "apply_technique",
		Description: applyTechniqueDescription,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"song": {"type": "string", "description": "Song id (see list_songs)."},
				"technique": {"type": "string", "description": "The card's id (see list_techniques), e.g. mastering.subsonic_highpass."},
				"track": {"type": "string", "description": "A track's id, or \"master\" (the default for a card that applies to the master or the song)."},
				"section": {"type": "string", "description": "For a card that applies to sections: the section it is applied to. Otherwise the section whose energy the card reads."},
				"id": {"type": "string", "description": "The id of the effect a card of one capability adds (default: the card's name, e.g. subsonic_highpass)."},
				"apply": {"type": "boolean", "description": "Store the patched song through save_song (default false: only check it)."},
				"verify": {"type": "boolean", "description": "Play what changed before and after and measure it as the card says (needs apply and confirm). Runs in the background: get_craft_job."},
				"confirm": {"type": "boolean", "description": "With verify: the song's session is loaded again with the change (loading replaces what AUM has open)."},
				"bars": {"type": "integer", "minimum": 1, "maximum": 32, "description": "With verify: how many bars are played before and after (default 4)."}
			},
			"required": ["song", "technique"]
		}`),
	}, s.handleApplyTechnique)
}

type applyArgs struct {
	Song      string `json:"song"`
	Technique string `json:"technique"`
	Track     string `json:"track"`
	Section   string `json:"section"`
	ID        string `json:"id"`
	Apply     bool   `json:"apply"`
	Verify    bool   `json:"verify"`
	Confirm   bool   `json:"confirm"`
	Bars      int    `json:"bars"`
}

func (s *Server) handleApplyTechnique(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args applyArgs
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	lib, rig := loadCraft()
	t, ok := lib.Technique(args.Technique)
	if !ok {
		return textResult(fmt.Sprintf("/technique: no technique card %q%s%s", args.Technique, suggestTechniques(lib, args.Technique), problemsText(lib)), true), nil
	}
	if args.Verify {
		return s.startVerify(ctx, args, lib, rig, t)
	}
	return s.RunCraftTool(t.Craft, args.Song, args.Apply, func(id string, sg *song.Song, _ []byte) (CraftPlan, error) {
		a, err := s.planTechnique(id, sg, args, lib, rig, t)
		if err != nil {
			return CraftPlan{}, err
		}
		return CraftPlan{Techniques: []string{t.ID}, Patch: a.Patch, Why: a.Why, Checks: a.Checks}, nil
	})
}

// planTechnique works out the patch of a card for a stored song: its target,
// the chain there and the song's values.
func (s *Server) planTechnique(id string, sg *song.Song, args applyArgs, lib *craft.Library, rig craft.Rig, t *craft.Technique) (*craft.Applied, error) {
	cs, err := s.compileSong(id, sg)
	if err != nil {
		return nil, fmt.Errorf("the song does not compile as it is stored: %v", err)
	}
	target, err := techniqueTarget(cs, t, args)
	if err != nil {
		return nil, err
	}
	env := craftEnv(cs, target, args.Section)
	return lib.Apply(t, target, rig, env)
}

// techniqueTarget is what a card is applied to in a song: the track (or the
// master), the section for a card that applies to sections, and the chain
// there now.
func techniqueTarget(cs *compiledSong, t *craft.Technique, args applyArgs) (craft.Target, error) {
	target := craft.Target{Track: strings.TrimSpace(args.Track), ID: strings.TrimSpace(args.ID)}
	if target.Track == "" {
		if !slices.Contains(t.AppliesTo, "master") && !slices.Contains(t.AppliesTo, "song") {
			return target, fmt.Errorf("/track: %s applies to %s: name the track", t.ID, strings.Join(t.AppliesTo, ", "))
		}
		target.Track = song.MasterTrack
	}
	var chain []song.Plugin
	if target.Track == song.MasterTrack {
		chain = cs.song.Master
	} else {
		found := false
		var ids []string
		for _, tr := range cs.song.Tracks {
			ids = append(ids, tr.ID)
			if tr.ID == target.Track {
				chain, found = tr.Effects, true
			}
		}
		if !found {
			return target, fmt.Errorf("/track: the song has no track %q (tracks: %s, or master)", target.Track, strings.Join(ids, ", "))
		}
	}
	for _, fx := range chain {
		e := craft.ChainEntry{ID: fx.EffectID()}
		if n := cs.plan.Node(target.Track, e.ID); n != nil {
			if d, ok := cs.catalog[n.Plugin]; ok {
				e.Plugin = craft.ComponentTuple(d.Component)
			}
		}
		target.Chain = append(target.Chain, e)
	}
	if sec := strings.TrimSpace(args.Section); sec != "" {
		s, ok := cs.song.Sections[sec]
		if !ok {
			return target, fmt.Errorf("/section: the song has no section %q", sec)
		}
		if slices.Contains(t.AppliesTo, "section") {
			target.Section = sec
			target.Scenes = append([]string{}, s.Scene...)
		}
	}
	return target, nil
}

// craftEnv is what a card's values read from the song: its tempo, a beat and a
// bar in milliseconds, and the lowest note that sounds where the card is
// applied (a track, or every pitched track for the master), in a section when
// one is named.
func craftEnv(cs *compiledSong, target craft.Target, section string) map[string]float64 {
	env := map[string]float64{}
	tempo := cs.song.Tempo
	if tempo <= 0 {
		return env
	}
	beat := 60000 / tempo
	env[craft.VarTempo] = tempo
	env[craft.VarBeatMS] = beat
	num, den := cs.timeline.MeterNum, cs.timeline.MeterDen
	if num <= 0 || den <= 0 {
		num, den = 4, 4
	}
	env[craft.VarBarMS] = beat * float64(num) * 4 / float64(den)

	drums := map[string]bool{}
	for _, tp := range cs.plan.Tracks {
		drums[tp.ID] = tp.Drums
	}
	lowest := -1
	for _, ev := range cs.timeline.Events {
		if ev.Kind != song.NoteOn || ev.Data2 == 0 || drums[ev.Track] {
			continue
		}
		if target.Track != song.MasterTrack && ev.Track != target.Track {
			continue
		}
		if section != "" && (ev.Section < 0 || ev.Section >= len(cs.timeline.Sections) || cs.timeline.Sections[ev.Section].Name != section) {
			continue
		}
		if lowest < 0 || ev.Data1 < lowest {
			lowest = ev.Data1
		}
	}
	if lowest >= 0 {
		env[craft.VarLowestNoteHz] = 440 * math.Pow(2, float64(lowest-69)/12)
	}
	return env
}
