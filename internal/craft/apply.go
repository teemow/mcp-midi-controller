package craft

// Applying a settings card: the patch that puts the capabilities a technique
// needs into the chain of what it is applied to, each carried by the plugin
// its binding chose, with the card's values worked out for the song and
// converted into the plugin's own units. The patch is id-keyed (ADR 0002):
// patch_song, or apply_technique with apply: true, stores it through
// save_song, which checks the song as always.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/teemow/mcp-midi-controller/internal/song"
)

// ChainEntry is one effect of the chain a technique is applied to: its id in
// the song and the component tuple of its plugin ("" when not known).
type ChainEntry struct {
	ID     string
	Plugin string
}

// Target is what a technique is applied to.
type Target struct {
	// Track is a track's id, or song.MasterTrack for the master's chain (a
	// card for the master or the song).
	Track string
	// Section, for a card that applies to a section, is the section it is
	// applied to: the effect is declared bypassed and the section's scenes
	// switch it on. Empty: everywhere.
	Section string
	// Scenes are the section's scenes now, which the section keeps.
	Scenes []string
	// Chain is the target's effects now, in order.
	Chain []ChainEntry
	// ID names the effect a card of one capability adds (default: the card's
	// name, mastering.air -> air).
	ID string
}

// Applied is what applying a card comes to: the patch, why each change was
// made, what was checked, and the bindings it was made with.
type Applied struct {
	Patch    map[string]any
	Why      []string
	Checks   []string
	Bindings []Binding
	// Effects are the ids of the effects the patch sets, in chain order.
	Effects []string
}

// Apply works out the patch that applies a settings card to target on rig,
// with the song's values in env. It fails, naming why, when the card cannot
// apply to the target, or when a capability has no plugin that carries it:
// then it names the closest plugin and how far off it comes.
func (l *Library) Apply(t *Technique, target Target, rig Rig, env map[string]float64) (*Applied, error) {
	if t.Kind != KindSettings {
		return nil, fmt.Errorf("%s is a writing card: its transform %s is applied by that transform's tool, not here", t.ID, t.Transform)
	}
	master := target.Track == song.MasterTrack
	scope := "track"
	if master {
		scope = "master"
	}
	switch {
	case target.Section != "" && !containsStr(t.AppliesTo, "section"):
		return nil, fmt.Errorf("%s applies to %s, not to a section", t.ID, strings.Join(t.AppliesTo, ", "))
	case master && !containsStr(t.AppliesTo, "master") && !containsStr(t.AppliesTo, "song"):
		return nil, fmt.Errorf("%s applies to %s, not to the master", t.ID, strings.Join(t.AppliesTo, ", "))
	case !master && target.Section == "" && !containsStr(t.AppliesTo, "track"):
		return nil, fmt.Errorf("%s applies to %s, not to a track", t.ID, strings.Join(t.AppliesTo, ", "))
	}
	for _, name := range t.Vars() {
		if _, ok := env[name]; !ok {
			return nil, fmt.Errorf("%s follows the song's %s, which %s does not tell: %s", t.ID, name, describeTarget(target), missingVar(name))
		}
	}

	out := &Applied{Patch: map[string]any{}}
	out.Bindings = l.Bind(t, rig, env)
	for _, b := range out.Bindings {
		if b.Plugin == "" {
			return nil, fmt.Errorf("%s: %s", b.Capability, b.Missing)
		}
	}

	base := t.ID[strings.Index(t.ID, ".")+1:]
	entries := map[string]any{}
	prev := ""
	for i, b := range out.Bindings {
		cand, _ := b.Bound()
		id := base
		switch {
		case target.ID != "" && len(out.Bindings) == 1:
			id = target.ID
		case len(out.Bindings) > 1:
			id = base + "_" + b.Capability[strings.LastIndex(b.Capability, ".")+1:]
		}
		entry, why, checks, err := l.entry(t, b.Capability, cand, env)
		if err != nil {
			return nil, err
		}
		existing := chainHas(target.Chain, id)
		switch {
		case existing:
			// The effect is there already (a card applied again): it is
			// set in place.
		case i == 0:
			if pl := l.place(t, target.Chain); pl != nil {
				entry["place"] = pl
			}
		default:
			entry["place"] = map[string]any{"after": prev}
		}
		switch {
		case target.Section != "":
			entry["bypass"] = true
		case existing:
			// Applied everywhere, it plays everywhere: an effect a section's
			// card declared bypassed is switched on (null drops the key).
			entry["bypass"] = nil
		}
		entries[id] = entry
		out.Effects = append(out.Effects, id)
		verb := "adds"
		if existing {
			verb = "sets"
		}
		out.Why = append(out.Why, fmt.Sprintf("%s %s %s: %s with %s (%s)%s", verb, scope, id, b.Capability, cand.Name, cand.Probe, placedText(entry["place"], existing)))
		out.Why = append(out.Why, why...)
		out.Checks = append(out.Checks, checks...)
		prev = id
	}
	if master {
		out.Patch["master"] = entries
	} else {
		out.Patch["tracks"] = map[string]any{target.Track: map[string]any{"effects": entries}}
	}
	if target.Section != "" {
		scene := strings.ReplaceAll(t.ID, ".", "_")
		on := map[string]any{}
		for _, id := range out.Effects {
			on[id] = map[string]any{"bypass": false}
		}
		out.Patch["scenes"] = map[string]any{scene: map[string]any{target.Track: map[string]any{"effects": on}}}
		scenes := append([]string{}, target.Scenes...)
		if !containsStr(scenes, scene) {
			scenes = append(scenes, scene)
		}
		out.Patch["sections"] = map[string]any{target.Section: map[string]any{"scene": scenes}}
		out.Why = append(out.Why, fmt.Sprintf("bypassed everywhere but in section %s, whose scene %s switches it on", target.Section, scene))
	}
	if t.Verify != nil {
		out.Checks = append(out.Checks, fmt.Sprintf("verify: %s (not run: verify: true plays and measures before and after)", t.Verify))
	}
	return out, nil
}

// entry is the effect entry that carries one capability with cand: its
// plugin, its parameters in the plugin's units, the mapping ranges, the
// enable values and the preset.
func (l *Library) entry(t *Technique, capability string, cand Candidate, env map[string]float64) (map[string]any, []string, []string, error) {
	card, _ := l.Plugin(cand.Plugin)
	pv, _ := card.Provision(capability)
	setting, _ := t.Setting(capability)
	entry := map[string]any{"plugin": cand.Probe}
	params := map[string]any{}
	ranges := map[string]any{}
	var why, checks []string
	switch pv.Write {
	case WritePreset:
		entry["preset"] = pv.Preset
		why = append(why, fmt.Sprintf("  the preset %q holds its settings", pv.Preset))
	case WriteState:
		return nil, nil, nil, fmt.Errorf("%s: %s sets %s through its saved state, which apply_technique does not write yet", capability, cand.Name, capability)
	}
	values := map[string]float64{}
	for _, pvv := range setting.Values {
		if pvv.Value.Expr != nil {
			if x, err := pvv.Value.Expr.Eval(env); err == nil {
				values[pvv.Param] = x
			}
		}
	}
	for _, pvv := range setting.Values {
		m, _ := pv.Param(pvv.Param)
		if m.Fixed != nil || m.Param == "" {
			continue
		}
		var bp BoundParam
		for _, p := range cand.Params {
			if p.Name == pvv.Param {
				bp = p
			}
		}
		if pvv.Value.Expr == nil {
			params[m.Param] = pvv.Value.Label
			why = append(why, fmt.Sprintf("  %s %s: %q = %s", capability, pvv.Param, m.Param, pvv.Value.Label))
			continue
		}
		x, ok := values[pvv.Param]
		if !ok {
			return nil, nil, nil, fmt.Errorf("%s.%s: %s cannot be worked out for this song", capability, pvv.Param, pvv.Value)
		}
		at, err := convert(m, x, values)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s.%s: the conversion %s: %v", capability, pvv.Param, m.Value, err)
		}
		params[m.Param] = roundTo(at)
		line := fmt.Sprintf("  %s %s %s: %q = %s", capability, pvv.Param, valueText(x, pvv.Unit), m.Param, valueText(roundTo(at), bp.Unit))
		if pvv.Value.Expr.String() != fmt.Sprint(x) {
			line += fmt.Sprintf(" (%s)", pvv.Value)
		}
		if len(m.Range) == 2 {
			ranges[m.Param] = []float64{m.Range[0], m.Range[1]}
			line += fmt.Sprintf(", mapped %g..%g", m.Range[0], m.Range[1])
		}
		why = append(why, line)
		if pvv.Precision != nil && bp.Step > 0 && bp.Within > 0 {
			checks = append(checks, fmt.Sprintf("precision: %s %s within %s: a CC step of %.3g %s there, %.2g of the %.3g allowed",
				capability, pvv.Param, pvv.Precision, bp.Step, unitOr(bp.Unit), bp.Step/2/bp.Within, bp.Within))
		}
	}
	for _, e := range pv.Enable {
		v := song.ParseValue(e.Value)
		if v.IsStr {
			params[e.Param] = v.Str
		} else {
			params[e.Param] = v.Num
		}
	}
	if len(params) > 0 {
		entry["params"] = params
	}
	if len(ranges) > 0 {
		entry["range"] = ranges
	}
	return entry, why, checks, nil
}

// place is where a card's first effect goes in the chain: first or last, or
// before or after the effect whose plugin provides the named capability.
// Before one the chain lacks is last; after one it lacks is first.
func (l *Library) place(t *Technique, chain []ChainEntry) any {
	p := t.Place
	if p == nil {
		return "last"
	}
	switch p.Where {
	case "first", "last":
		return p.Where
	}
	idx := -1
	for i, e := range chain {
		if card, ok := l.plugins[e.Plugin]; ok && card.provides(p.Capability) {
			idx = i
			if p.Where == "before" {
				break
			}
		}
	}
	switch {
	case idx >= 0:
		return map[string]any{p.Where: chain[idx].ID}
	case p.Where == "before":
		return "last"
	default:
		return "first"
	}
}

func placedText(pl any, existing bool) string {
	switch v := pl.(type) {
	case string:
		return ", placed " + v
	case map[string]any:
		for k, id := range v {
			return fmt.Sprintf(", placed %s %v", k, id)
		}
	}
	if existing {
		return ", where it is"
	}
	return ""
}

func chainHas(chain []ChainEntry, id string) bool {
	for _, e := range chain {
		if e.ID == id {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func describeTarget(t Target) string {
	switch {
	case t.Track == song.MasterTrack:
		return "the master"
	case t.Section != "":
		return "section " + t.Section
	}
	return "track " + t.Track
}

func missingVar(name string) string {
	switch name {
	case VarLowestNoteHz:
		return "no pitched note plays there"
	case VarSectionEnergy:
		return "name a section, and give it an energy"
	}
	return "the song does not say"
}

// SortedVars lists env's names, for a message.
func SortedVars(env map[string]float64) []string {
	out := make([]string, 0, len(env))
	for k := range env {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// roundTo keeps a value to 6 significant digits, as a song writes it.
func roundTo(x float64) float64 {
	if x == 0 {
		return 0
	}
	p := math.Pow(10, 5-math.Floor(math.Log10(math.Abs(x))))
	return math.Round(x*p) / p
}

func valueText(x float64, unit string) string {
	if unit == "" {
		return fmt.Sprintf("%g", x)
	}
	return fmt.Sprintf("%g %s", x, unit)
}
