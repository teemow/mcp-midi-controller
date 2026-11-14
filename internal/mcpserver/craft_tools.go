package mcpserver

// This file is the MCP surface over internal/craft: the technique cards a
// craft works with, and how each would be carried by the plugins of this rig.
// The tools are registered only when the config switches them on
// (craft_tools: true). See docs/craft-cards.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
)

func (s *Server) registerCraftTools() {
	if !s.craftTools {
		return
	}
	s.mcp.AddTool(&mcp.Tool{
		Name: "list_techniques",
		Description: "List the technique cards: what a craft does, as data. A settings card names the capabilities it needs (eq.highpass, dynamics.limiter), never a plugin; a writing card names a transform. " +
			"Each card comes with the plugin of this rig that would carry each capability it needs (the first in the rig's order of preference, craft/prefer.yaml, that has a plugin card and a staged probe dump), or why none can. " +
			"Filter by craft (production, composition, drums, sound_design, orchestration, mixing, mastering), target (track, section, master, song), capability, kind (settings, writing) or a word. " +
			"The cards ship with the daemon; the rig's own in craft/ of the config dir replace a shipped card of the same id. get_technique shows one in full.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"craft": {"type": "string", "enum": ["production", "composition", "drums", "sound_design", "orchestration", "mixing", "mastering"], "description": "Only this craft's cards."},
				"target": {"type": "string", "enum": ["track", "section", "master", "song"], "description": "Only cards that apply to this."},
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
	Targets   []string  `json:"targets"`
	Needs     []string  `json:"needs,omitempty"`
	Transform string    `json:"transform,omitempty"`
	CarriedBy []carrier `json:"carried_by,omitempty"`
	Source    string    `json:"source"`
}

func (s *Server) handleListTechniques(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Craft      string `json:"craft"`
		Target     string `json:"target"`
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
			args.Target != "" && !slices.Contains(t.Targets, args.Target),
			args.Capability != "" && !slices.Contains(t.Needs, args.Capability),
			args.Kind != "" && string(t.Kind) != args.Kind,
			query != "" && !strings.Contains(strings.ToLower(t.ID+"\n"+t.Title+"\n"+t.Intent+"\n"+t.When), query):
			continue
		}
		sum := techniqueSummary{ID: t.ID, Craft: t.Craft, Kind: string(t.Kind), Title: t.Title, Targets: t.Targets, Needs: t.Needs, Transform: t.Transform, Source: t.Source}
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
		fmt.Fprintf(&b, "%s [%s, %s] %s", t.ID, t.Kind, strings.Join(t.Targets, "/"), t.Title)
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
	fmt.Fprintf(b, "%s (%s, %s card for %s; %s)\n%s\n", t.ID, t.Craft, t.Kind, strings.Join(t.Targets, ", "), t.Source, t.Title)
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
