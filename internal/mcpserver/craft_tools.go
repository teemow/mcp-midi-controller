package mcpserver

// This file is the MCP surface over internal/craft: the technique cards a
// craft works with, how each would be carried by the plugins of this rig,
// patch_song for applying id-keyed patches against a song's revision, and the
// shared craft.Answer runner that every craft tool speaks through.
// The tools are registered only when the config switches them on
// (craft_tools: true). See docs/craft-tools.md and docs/craft-cards.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/song"
)

func (s *Server) registerCraftTools() {
	if !s.craftTools {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "list_techniques",
		Description: "List the technique cards: what a craft does, as data. A settings card names the capabilities it needs (eq.highpass, dynamics.limiter), never a plugin; a writing card names a transform. " +
			"Each card comes with the plugin of this rig that would carry each capability it needs (the first in the rig's order of preference, craft/prefer.yaml, that has a plugin card and a staged probe dump), or why none can. " +
			"Filter by craft (production, composition, drums, sound_design, orchestration, mixing, mastering), what a card applies to (applies_to: track, section, master, song), capability, kind (settings, writing) or a word. " +
			"The cards ship with the daemon; the rig's own in craft/ of the config dir replace a shipped card of the same id. get_technique shows one in full.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"craft": {"type": "string", "enum": ["production", "composition", "drums", "sound_design", "orchestration", "mixing", "mastering"], "description": "Only this craft's cards."},
				"applies_to": {"type": "string", "enum": ["track", "section", "master", "song"], "description": "Only cards that apply to this."},
				"capability": {"type": "string", "description": "Only cards that need this capability (e.g. dynamics.limiter)."},
				"kind": {"type": "string", "enum": ["settings", "writing"], "description": "Only this kind of card."},
				"query": {"type": "string", "description": "Only cards whose id, title, intent or when contain this text."}
			}
		}`),
	}, s.handleListTechniques)

	s.mcp.AddTool(&mcp.Tool{
		Name: "get_technique",
		Description: "One technique card in full: its intent, when to use it and when not, what it sets (values may follow the song: tempo, beat_ms, bar_ms, lowest_note_hz, section.energy), the precision a setting needs, where it goes in the chain and how its result is checked; for a writing card its transform and options. " +
			"With it the capabilities it needs (parameters, units, ranges) and the bindings it would get on this rig: per capability every plugin with a card for it in the rig's order of preference, the one it would use, how each parameter reaches that plugin (its parameter, mapping range and the step of a CC there) and why any other cannot carry it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "The card's id (see list_techniques), e.g. mastering.ceiling."}
			},
			"required": ["id"]
		}`),
	}, s.handleGetTechnique)

	s.mcp.AddTool(&mcp.Tool{
		Name: "patch_song",
		Description: "Apply an id-keyed merge patch to a stored song through save_song, refusing a patch whose revision is no longer current (ADR 0002). " +
			"Maps merge recursively and null removes a key. Lists whose entries carry an id — tracks, tracks.<id>.effects, tracks.<id>.modulators, master and stems.<stem>.effects — are keyed by that id: " +
			"null removes an entry, an existing id merges in place, and a new entry carries place (\"first\", \"last\" by default, \"after: <id>\" or \"before: <id>\"). " +
			"Lists of values (play lists, arrangement, notes, automation, events, pedal, depth, locks, control_channels) are replaced whole. " +
			"The patched document is checked, compiled and reviewed like save_song, and returns the new revision.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"song": {"type": "string", "description": "Song id (see list_songs)."},
				"revision": {"type": "string", "description": "The song's revision the patch was made against (from get_song, save_song, patch_song or a craft tool)."},
				"patch": {"type": "object", "description": "Merge patch on the song document, with id-bearing lists keyed by id."},
				"dry_run": {"type": "boolean", "description": "Check and compile the patched song without storing it."}
			},
			"required": ["song", "revision", "patch"]
		}`),
	}, s.handlePatchSong)
}

// loadCraft reads the cards and the rig the bindings are made on: the probe
// dumps staged for it.
func loadCraft() (*craft.Library, craft.Rig) {
	lib, _ := craft.Load(config.CraftDir()) // lib.Problems carries the error
	return lib, craft.Rig(loadStagedProbeDumps())
}

// carrier is the plugin that would carry one capability of a card.
type carrier struct {
	Capability string `json:"capability"`
	Plugin     string `json:"plugin,omitempty"`
	Name       string `json:"name,omitempty"`
	Probe      string `json:"probe,omitempty"`
	Missing    string `json:"missing,omitempty"`
}

type techniqueSummary struct {
	ID        string    `json:"id"`
	Craft     string    `json:"craft"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	AppliesTo []string  `json:"applies_to"`
	Needs     []string  `json:"needs,omitempty"`
	Transform string    `json:"transform,omitempty"`
	CarriedBy []carrier `json:"carried_by,omitempty"`
	Source    string    `json:"source"`
}

func (s *Server) handleListTechniques(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Craft      string `json:"craft"`
		AppliesTo  string `json:"applies_to"`
		Capability string `json:"capability"`
		Kind       string `json:"kind"`
		Query      string `json:"query"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return textResult("invalid arguments: "+err.Error(), true), nil
		}
	}
	lib, rig := loadCraft()
	if args.Capability != "" {
		if _, ok := lib.Vocab.Capability(args.Capability); !ok {
			return textResult(fmt.Sprintf("%q is not a capability (known: %s)", args.Capability, strings.Join(lib.Vocab.CapabilityNames(), ", ")), true), nil
		}
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	var list []techniqueSummary
	var b strings.Builder
	for _, t := range lib.Techniques() {
		switch {
		case args.Craft != "" && t.Craft != args.Craft,
			args.AppliesTo != "" && !slices.Contains(t.AppliesTo, args.AppliesTo),
			args.Capability != "" && !slices.Contains(t.Needs, args.Capability),
			args.Kind != "" && string(t.Kind) != args.Kind,
			query != "" && !strings.Contains(strings.ToLower(t.ID+"\n"+t.Title+"\n"+t.Intent+"\n"+t.When), query):
			continue
		}
		sum := techniqueSummary{ID: t.ID, Craft: t.Craft, Kind: string(t.Kind), Title: t.Title, AppliesTo: t.AppliesTo, Needs: t.Needs, Transform: t.Transform, Source: t.Source}
		var carried []string
		for _, bd := range lib.Bind(t, rig) {
			c := carrier{Capability: bd.Capability, Plugin: bd.Plugin, Missing: bd.Missing}
			if cand, ok := bd.Bound(); ok {
				c.Name, c.Probe = cand.Name, cand.Probe
				carried = append(carried, fmt.Sprintf("%s: %s (%s)", bd.Capability, cand.Name, cand.Probe))
			} else {
				carried = append(carried, bd.Capability+": no plugin")
			}
			sum.CarriedBy = append(sum.CarriedBy, c)
		}
		list = append(list, sum)
		fmt.Fprintf(&b, "%s [%s, %s] %s", t.ID, t.Kind, strings.Join(t.AppliesTo, "/"), t.Title)
		switch {
		case len(carried) > 0:
			fmt.Fprintf(&b, " — %s", strings.Join(carried, "; "))
		case t.Transform != "":
			fmt.Fprintf(&b, " — transform %s", t.Transform)
		}
		if t.Source != "shipped" {
			fmt.Fprintf(&b, " (%s)", t.Source)
		}
		b.WriteString("\n")
	}
	summary := fmt.Sprintf("%d technique card(s)", len(list))
	if len(list) == 0 {
		summary = "no technique card matches"
	}
	text := summary + "\n" + b.String() + problemsText(lib)
	out := map[string]any{"techniques": list}
	if len(lib.Problems) > 0 {
		out["problems"] = lib.Problems
	}
	return structResult(strings.TrimRight(text, "\n"), out), nil
}

func (s *Server) handleGetTechnique(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	lib, rig := loadCraft()
	t, ok := lib.Technique(args.ID)
	if !ok {
		return textResult(fmt.Sprintf("no technique card %q%s%s", args.ID, suggestTechniques(lib, args.ID), problemsText(lib)), true), nil
	}
	var b strings.Builder
	writeTechnique(&b, t)
	out := map[string]any{"technique": t}
	if vars := t.Vars(); len(vars) > 0 {
		out["song_values"] = vars
		fmt.Fprintf(&b, "uses from the song: %s\n", strings.Join(vars, ", "))
	}
	switch t.Kind {
	case craft.KindSettings:
		var caps []craft.Capability
		for _, name := range t.Needs {
			c, _ := lib.Vocab.Capability(name)
			caps = append(caps, c)
		}
		bindings := lib.Bind(t, rig)
		out["capabilities"] = caps
		out["bindings"] = bindings
		b.WriteString("\non this rig:\n")
		for _, bd := range bindings {
			writeBinding(&b, bd)
		}
	case craft.KindWriting:
		out["transform"] = lib.Vocab.Transforms[t.Transform]
	}
	if len(lib.Problems) > 0 {
		out["problems"] = lib.Problems
	}
	text := b.String() + problemsText(lib)
	return structResult(strings.TrimRight(text, "\n"), out), nil
}

// writeTechnique renders a card for a person.
func writeTechnique(b *strings.Builder, t *craft.Technique) {
	fmt.Fprintf(b, "%s (%s, %s card for %s; %s)\n%s\n", t.ID, t.Craft, t.Kind, strings.Join(t.AppliesTo, ", "), t.Source, t.Title)
	if t.Intent != "" {
		fmt.Fprintf(b, "intent: %s\n", t.Intent)
	}
	if t.When != "" {
		fmt.Fprintf(b, "when: %s\n", t.When)
	}
	for _, w := range t.WhenNot {
		fmt.Fprintf(b, "not when: %s\n", w)
	}
	if t.Kind == craft.KindWriting {
		fmt.Fprintf(b, "transform: %s\n", t.Transform)
		for _, o := range t.Options {
			fmt.Fprintf(b, "  %s: %s\n", o.Option, o.Value)
		}
		return
	}
	fmt.Fprintf(b, "needs: %s, placed %s\n", strings.Join(t.Needs, ", "), t.Place)
	for _, st := range t.Settings {
		var vals []string
		for _, pv := range st.Values {
			v := strings.TrimSpace(fmt.Sprintf("%s %s", pv.Value, pv.Unit))
			if pv.Precision != nil {
				v += " (within " + pv.Precision.String() + ")"
			}
			vals = append(vals, pv.Param+" "+v)
		}
		fmt.Fprintf(b, "set %s: %s\n", st.Capability, strings.Join(vals, ", "))
	}
	if t.Verify != nil {
		fmt.Fprintf(b, "verify: %s\n", t.Verify)
	}
}

// writeBinding renders how one capability would be carried.
func writeBinding(b *strings.Builder, bd craft.Binding) {
	if bd.Missing != "" {
		fmt.Fprintf(b, "  %s: no plugin. %s\n", bd.Capability, bd.Missing)
	}
	for _, c := range bd.Candidates {
		mark := "   "
		if c.Plugin == bd.Plugin {
			mark = " ->"
		}
		fmt.Fprintf(b, " %s %s: %s (%s", mark, bd.Capability, c.Name, c.Plugin)
		if c.Probe != "" {
			fmt.Fprintf(b, ", probe %s", c.Probe)
		}
		if c.Preferred > 0 {
			fmt.Fprintf(b, ", preferred #%d", c.Preferred)
		}
		fmt.Fprintf(b, "), write %s", c.Write)
		if c.Preset != "" {
			fmt.Fprintf(b, " %q", c.Preset)
		}
		b.WriteString("\n")
		for _, p := range c.Params {
			fmt.Fprintf(b, "        %s", p.Name)
			switch {
			case p.Fixed != nil:
				fmt.Fprintf(b, ": fixed at %g", *p.Fixed)
			case p.Param != "":
				fmt.Fprintf(b, " -> %q", p.Param)
			case p.Field != "":
				fmt.Fprintf(b, " -> state field %q", p.Field)
			}
			if p.Value != nil {
				fmt.Fprintf(b, " = %s", p.Value)
			}
			if len(p.Range) == 2 {
				fmt.Fprintf(b, ", mapped %g..%g", p.Range[0], p.Range[1])
			}
			if p.Step > 0 {
				fmt.Fprintf(b, ", a CC step %.3g %s", p.Step, p.Unit)
			}
			b.WriteString("\n")
		}
		for _, e := range c.Enable {
			fmt.Fprintf(b, "        with %q = %s\n", e.Param, e.Value)
		}
		for _, p := range c.Problems {
			fmt.Fprintf(b, "        cannot: %s\n", p)
		}
	}
}

// problemsText names the cards that did not load.
func problemsText(lib *craft.Library) string {
	if len(lib.Problems) == 0 {
		return ""
	}
	return fmt.Sprintf("\n%d card file(s) did not load:\n  %s\n", len(lib.Problems), strings.Join(lib.Problems, "\n  "))
}

// suggestTechniques names the cards whose id shares a word with want, leaving
// out the craft, which every card of the craft shares.
func suggestTechniques(lib *craft.Library, want string) string {
	var ids []string
	for _, t := range lib.Techniques() {
		ids = append(ids, t.ID)
	}
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(want), func(r rune) bool { return r == '.' || r == '_' || r == ' ' }) {
		if !slices.Contains(craft.Crafts, w) {
			words = append(words, w)
		}
	}
	var hits []string
	for _, id := range ids {
		for _, w := range words {
			if len(w) >= 3 && strings.Contains(id, w) {
				hits = append(hits, id)
				break
			}
		}
	}
	if len(hits) > 0 {
		return " (did you mean: " + strings.Join(hits, ", ") + "?)"
	}
	return fmt.Sprintf(" (%d cards; see list_techniques)", len(ids))
}

func (s *Server) handlePatchSong(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Song     string          `json:"song"`
		Revision string          `json:"revision"`
		Patch    json.RawMessage `json:"patch"`
		DryRun   bool            `json:"dry_run"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	if strings.TrimSpace(args.Song) == "" {
		return textResult("/song: name the song (see list_songs)", true), nil
	}
	wantRev := strings.TrimSpace(args.Revision)
	if wantRev == "" {
		return textResult("/revision: name the song's revision the patch was made against (see get_song)", true), nil
	}
	rawPatch := bytes.TrimSpace(args.Patch)
	if len(rawPatch) == 0 || string(rawPatch) == "null" {
		return textResult("/patch: provide the patch object", true), nil
	}

	s.songsMu.Lock()
	defer s.songsMu.Unlock()

	_, raw, err := s.songs.Load(args.Song)
	if err != nil && raw == nil {
		return textResult("/song: "+err.Error(), true), nil
	}
	id := song.ID(args.Song)
	curRev := song.Revision(raw)
	if !strings.EqualFold(curRev, wantRev) {
		return textResult(fmt.Sprintf("/revision: song %q is at revision %s, not %s: read it again with get_song and make the patch against %s",
			id, curRev, wantRev, curRev), true), nil
	}
	patched, err := song.Patch(raw, rawPatch)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	text, sum, newRev, err := s.saveSongLocked(id, patched, args.DryRun)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if args.DryRun {
		return structResult("checked "+text, map[string]any{
			"id":                id,
			"saved":             false,
			"previous_revision": curRev,
			"summary":           sum,
		}), nil
	}
	text = strings.Replace(text, "\nrevision: "+newRev, fmt.Sprintf("\nrevision: %s (from %s)", newRev, curRev), 1)
	return structResult("patched "+text, map[string]any{
		"id":                id,
		"saved":             true,
		"previous_revision": curRev,
		"revision":          newRev,
		"summary":           sum,
	}), nil
}

// CraftPlan is what a craft tool computes from a song before RunCraftTool
// checks (or, with apply, stores) its patch and formats the shared craft.Answer.
type CraftPlan struct {
	Techniques []string
	Patch      map[string]any
	Why        []string
	Checks     []string
}

// RunCraftTool executes a craft tool against songID and returns the one shape
// every craft tool answers in (craft.Answer: craft, techniques, patch,
// revision, why, checks, applied).
//
// With apply true, it holds s.songsMu across reading the stored song, building
// the patch, applying it and storing the result through save_song, so no
// concurrent edit can land between read and store. With apply false, it reads
// the song, builds and dry-run checks the patch against save_song, and returns
// the answer naming the base revision without storing.
func (s *Server) RunCraftTool(
	craftID, songID string,
	apply bool,
	build func(id string, sg *song.Song, raw []byte) (CraftPlan, error),
) (*mcp.CallToolResult, error) {
	if !slices.Contains(craft.Crafts, craftID) {
		return textResult(fmt.Sprintf("unknown craft %q", craftID), true), nil
	}
	if strings.TrimSpace(songID) == "" {
		return textResult("/song: name the song (see list_songs)", true), nil
	}
	id := song.ID(songID)

	if apply {
		s.songsMu.Lock()
		defer s.songsMu.Unlock()

		sg, raw, err := s.songs.Load(songID)
		if err != nil {
			return textResult("/song: "+err.Error(), true), nil
		}
		plan, err := build(id, sg, raw)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		patchMap := plan.Patch
		if patchMap == nil {
			patchMap = map[string]any{}
		}
		patchJSON, err := json.Marshal(patchMap)
		if err != nil {
			return textResult("marshal patch: "+err.Error(), true), nil
		}
		patched, err := song.Patch(raw, patchJSON)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		saveText, _, newRev, err := s.saveSongLocked(id, patched, false)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		ans := craft.NewAnswer(craftID, plan.Techniques, patchMap, newRev, plan.Why, plan.Checks, true)
		return structResult(ans.Summary()+"\n\nsaved "+saveText, ans), nil
	}

	s.songsMu.Lock()
	sg, raw, err := s.songs.Load(songID)
	s.songsMu.Unlock()
	if err != nil {
		return textResult("/song: "+err.Error(), true), nil
	}
	baseRev := song.Revision(raw)
	plan, err := build(id, sg, raw)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	patchMap := plan.Patch
	if patchMap == nil {
		patchMap = map[string]any{}
	}
	patchJSON, err := json.Marshal(patchMap)
	if err != nil {
		return textResult("marshal patch: "+err.Error(), true), nil
	}
	patched, err := song.Patch(raw, patchJSON)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if _, _, _, err := s.saveSongLocked(id, patched, true); err != nil {
		return textResult(err.Error(), true), nil
	}
	ans := craft.NewAnswer(craftID, plan.Techniques, patchMap, baseRev, plan.Why, plan.Checks, false)
	return structResult(ans.Summary(), ans), nil
}
