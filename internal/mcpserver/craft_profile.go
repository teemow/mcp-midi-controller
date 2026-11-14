package mcpserver

// profile_plugin for an effect: its probe dump's parameters matched to the
// capabilities by name, unit and range (craft.Propose), or the capabilities
// an agent gives; then every capability swept at the tap. A lab song plays a
// noise source through the plugin, a section per value of the sweep, and the
// capability's measurement is read in each. A capability whose reading moves
// the way it says is written into a plugin card in the config dir
// (craft/plugins/), with what was measured and when; the others are reported
// and left out.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teemow/midi-device/device"
	"gopkg.in/yaml.v3"

	"github.com/teemow/mcp-midi-controller/internal/config"
	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/measure"
	"github.com/teemow/mcp-midi-controller/internal/song"
	"github.com/teemow/midi-transport/midicontrol"
)

const (
	// pluginLab is the song the sweeps are played in.
	pluginLab = "plugin_lab"
	// profileBars is how many bars a value of a sweep is played.
	profileBars = 2
	// labNoise is the default source: Model D's white noise in bursts.
	labNoise = "aumu-mmdx-_moog"
)

// labNoiseParams make Model D a white noise source: every oscillator off, the
// noise on and white, its filter open, its loudness held while a key is.
var labNoiseParams = map[string]any{
	"Osc 1 Enabled": 0, "Osc 2 Enabled": 0, "Osc 3 Enabled": 0, "Noise Enabled": 1, "Noise Color": 0, "Noise Volume": 0.7,
	"Cutoff Frequency": 1, "Filter Emphasis": 0, "Amount Of Contour": 0, "Loudness Contour Attack": 0, "Loudness Contour Sustain": 1,
	"Delay Enabled": 0, "Glide Enabled": 0,
}

func (s *Server) registerProfilePlugin() {
	s.mcp.AddTool(&mcp.Tool{
		Name: "profile_plugin",
		Description: "Measure what an effect plugin can do and write its plugin card. Its probe dump's parameters are matched to the capabilities by name, unit and range (eq.highpass, dynamics.compressor …), each with the conversion its units need and, where a capability uses only part of a parameter's range, that part as the mapping range; or pass card with the capabilities yourself (plugin-card form: {capability: {write: cc, params: {param: {param, value, range}}, enable: {…}}}), as a multi-band EQ needs to say which band and shape. " +
			"Then every capability is swept at the tap: a lab song (stored as \"plugin_lab\" and loaded like load_song, so it needs confirm: true) plays white noise in bursts through the plugin, a section per value of the swept parameter, and the capability's measurement has to move the way it says (a high-pass's cutoff lowers the band below it, a compressor's threshold raises the crest factor, a limiter's ceiling the true peak). " +
			"The capabilities that pass are written to craft/plugins/ of the config dir as the plugin's card, with what was measured and when and each parameter's step size over its mapping range (it replaces the rig's earlier card of the plugin, keeping that card's notes and the capabilities not swept); the others are reported with what was read. Runs in the background: get_craft_job follows it, stop_song ends it. dry_run: true only proposes the card.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugin": {"type": "string", "description": "The effect: a staged probe id or a name that fits one plugin (list_song_plugins)."},
				"capabilities": {"type": "array", "items": {"type": "string"}, "description": "Only these capabilities (default: every one matched or given)."},
				"card": {"type": "object", "description": "The capabilities, in plugin-card form, instead of the matched ones."},
				"source": {"type": "object", "description": "The lab's source instead of Model D's white noise: {plugin, params}.", "properties": {"plugin": {"type": "string"}, "params": {"type": "object"}}},
				"dry_run": {"type": "boolean", "description": "Only propose the card: nothing is loaded, played or written."},
				"confirm": {"type": "boolean", "description": "Required (true) to sweep: the lab's session replaces what AUM has open."}
			},
			"required": ["plugin"]
		}`),
	}, s.handleProfilePlugin)
}

type profileArgs struct {
	Plugin       string                         `json:"plugin"`
	Capabilities []string                       `json:"capabilities"`
	Card         map[string]craft.ProvisionSpec `json:"card"`
	Source       *struct {
		Plugin string         `json:"plugin"`
		Params map[string]any `json:"params"`
	} `json:"source"`
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

// profileReport is what profile_plugin found.
type profileReport struct {
	Plugin string          `json:"plugin"`
	Probe  string          `json:"probe"`
	Name   string          `json:"name"`
	Sweeps []profileSweep  `json:"sweeps"`
	Card   *craft.CardSpec `json:"card,omitempty"`
	File   string          `json:"file,omitempty"`
	// Steps are the step sizes of the card's parameters (craft.Steps).
	Steps []string `json:"steps,omitempty"`
	// Kept are the capabilities of the rig's earlier card that were not
	// swept, which the card written keeps.
	Kept    []string `json:"kept,omitempty"`
	Seconds float64  `json:"seconds"`
}

type profileSweep struct {
	craft.Sweep
	Readings []float64 `json:"readings"`
	Passed   bool      `json:"passed"`
	Said     string    `json:"said"`
}

func (r *profileReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "profile of %s (%s, %s)\n", r.Name, r.Probe, r.Plugin)
	for _, sw := range r.Sweeps {
		mark := "FAILED"
		if sw.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  %-6s %s\n", mark, sw.Said)
	}
	for _, st := range r.Steps {
		fmt.Fprintf(&b, "  %-6s %s\n", "step", st)
	}
	switch {
	case r.File != "" && len(r.Kept) > 0:
		fmt.Fprintf(&b, "card written: %s (%s; kept from the rig's earlier card: %s)\n", r.File, strings.Join(r.Card.CapabilityNames(), ", "), strings.Join(r.Kept, ", "))
	case r.File != "":
		fmt.Fprintf(&b, "card written: %s (%s)\n", r.File, strings.Join(r.Card.CapabilityNames(), ", "))
	case r.Card != nil:
		b.WriteString("no card written: no capability passed its sweep\n")
	}
	fmt.Fprintf(&b, "measured in %.0f s", r.Seconds)
	return b.String()
}

func (s *Server) handleProfilePlugin(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args profileArgs
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return textResult("invalid arguments: "+err.Error(), true), nil
	}
	cat := songCatalog()
	probe, err := cat.Resolve(args.Plugin)
	if err != nil {
		return textResult("/plugin: "+err.Error(), true), nil
	}
	dump := cat[probe]
	if dump.Component.Type != "aufx" && dump.Component.Type != "aumf" {
		return textResult(fmt.Sprintf("/plugin: %s is not an effect (%s); profile_plugin measures effects", dump.Name, dump.Component.Type), true), nil
	}
	lib, _ := loadCraft()
	card, err := profileCard(dump, args, lib.Vocab)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if args.DryRun {
		data, _ := yaml.Marshal(card)
		return structResult(fmt.Sprintf("proposed card of %s (not measured):\n%s", dump.Name, data), map[string]any{"plugin": probe, "card": card}), nil
	}
	src, srcParams := labNoise, labNoiseParams
	if args.Source != nil && strings.TrimSpace(args.Source.Plugin) != "" {
		src, srcParams = args.Source.Plugin, args.Source.Params
	}
	if _, err := cat.Resolve(src); err != nil {
		return textResult(fmt.Sprintf("/source: the lab's source %s: %v; name another with source: {plugin, params}", src, err), true), nil
	}
	switch {
	case s.ipad != nil && !args.Confirm:
		return textResult("profile_plugin needs confirm: true: the lab's session replaces the session open in AUM", true), nil
	case s.midi == nil:
		return textResult("profile_plugin: "+midicontrol.ErrNoBrain.Error(), true), nil
	case s.audio == nil:
		return textResult("profile_plugin: no audio tap is configured", true), nil
	}
	lab, sweeps, err := labSong(dump, probe, card, lib.Vocab, src, srcParams)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if _, fail := s.startCraftJob("profile_plugin", pluginLab, func(ctx context.Context, job *craftJob) (string, any, error) {
		rep, err := s.runProfile(ctx, job, dump, probe, card, lab, sweeps, lib)
		if rep == nil {
			return "", nil, err
		}
		return rep.text(), rep, err
	}); fail != nil {
		return fail, nil
	}
	return structResult(fmt.Sprintf("profiling %s in the background: %d capabilit(ies), %d sections of %d bars in the lab (get_craft_job follows it, stop_song ends it)",
		dump.Name, len(sweeps), countValues(sweeps), profileBars), map[string]any{"plugin": probe, "capabilities": card.CapabilityNames(), "job": "profile_plugin"}), nil
}

// profileCard is the card profile_plugin starts from: the matched one, or the
// capabilities the agent gave, limited to the ones asked for.
func profileCard(dump device.ProbeDump, args profileArgs, vocab craft.Vocabulary) (craft.CardSpec, error) {
	card := craft.Propose(dump, vocab)
	if len(args.Card) > 0 {
		card.Capabilities = args.Card
	}
	if len(args.Capabilities) > 0 {
		for name := range card.Capabilities {
			if !slices.Contains(args.Capabilities, name) {
				delete(card.Capabilities, name)
			}
		}
		for _, name := range args.Capabilities {
			if _, ok := card.Capabilities[name]; !ok {
				return card, fmt.Errorf("/capabilities: %s is not among the capabilities matched to %s (%s); pass card to say how it provides it", name, dump.Name, strings.Join(craft.Propose(dump, vocab).CapabilityNames(), ", "))
			}
		}
	}
	if len(card.Capabilities) == 0 {
		return card, fmt.Errorf("no capability matches %s's parameters by name, unit and range; pass card to say which of them provide which capability", dump.Name)
	}
	if _, err := card.Render(vocab); err != nil {
		return card, err
	}
	return card, nil
}

func countValues(sweeps []craft.Sweep) int {
	n := 0
	for _, sw := range sweeps {
		n += len(sw.Values)
	}
	return n
}

// labSong is the lab's song document: the source through the plugin, and
// for every capability a section per value of its sweep, whose scene sets
// the swept parameter, the held ones and the enable values.
func labSong(dump device.ProbeDump, probe string, card craft.CardSpec, vocab craft.Vocabulary, src string, srcParams map[string]any) ([]byte, []craft.Sweep, error) {
	data, err := card.Render(vocab)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := craft.ParsePluginCard(data, vocab)
	if err != nil {
		return nil, nil, err
	}
	declared := map[string]any{}
	ranges := map[string]any{}
	scenes := map[string]any{}
	sections := map[string]any{}
	var arrangement []string
	var sweeps []craft.Sweep
	for _, name := range card.CapabilityNames() {
		c, _ := vocab.Capability(name)
		pv, _ := parsed.Provision(name)
		sw := craft.SweepFor(c)
		for _, m := range pv.Params {
			if len(m.Range) == 2 && m.Param != "" {
				ranges[m.Param] = []float64{m.Range[0], m.Range[1]}
			}
		}
		enable := map[string]any{}
		for _, e := range pv.Enable {
			v := song.ParseValue(e.Value)
			if v.IsStr {
				enable[e.Param] = v.Str
			} else {
				enable[e.Param] = v.Num
			}
		}
		for i, v := range sw.Values {
			values := map[string]float64{}
			for k, x := range sw.Hold {
				values[k] = x
			}
			values[sw.Param] = v
			pvals, err := pv.PluginValues(values)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %v", name, err)
			}
			params := map[string]any{}
			for k, x := range enable {
				params[k] = x
			}
			for k, x := range pvals {
				params[k] = clampToParam(dump, k, x, ranges)
			}
			for k, x := range params {
				if _, ok := declared[k]; !ok {
					declared[k] = x
				}
			}
			id := fmt.Sprintf("%s_%d", strings.ReplaceAll(name, ".", "_"), i+1)
			scenes[id] = map[string]any{"source": map[string]any{"effects": map[string]any{"dut": map[string]any{"params": params}}}}
			sections[id] = map[string]any{"bars": profileBars, "scene": id, "play": map[string]any{"source": "bursts"}}
			arrangement = append(arrangement, id)
		}
		sweeps = append(sweeps, sw)
	}
	dut := map[string]any{"id": "dut", "plugin": probe, "params": declared}
	if len(ranges) > 0 {
		dut["range"] = ranges
	}
	doc := map[string]any{
		"title":       "Plugin Lab",
		"description": fmt.Sprintf("profile_plugin's lab: white noise in bursts through %s, a section per value of each capability's sweep.", dump.Name),
		"tempo":       120,
		"tracks":      []any{map[string]any{"id": "source", "instrument": map[string]any{"plugin": src, "params": srcParams}, "effects": []any{dut}}},
		"patterns":    map[string]any{"bursts": map[string]any{"step": "1/4", "gate": 0.5, "notes": "C3 C3 C3 C3"}},
		"scenes":      scenes,
		"sections":    sections,
		"arrangement": arrangement,
	}
	out, err := yaml.Marshal(doc)
	return out, sweeps, err
}

// clampToParam keeps a value within the plugin parameter's range, or its
// mapping range when the card gives one.
func clampToParam(dump device.ProbeDump, name string, x float64, ranges map[string]any) float64 {
	lo, hi := math.Inf(-1), math.Inf(1)
	for _, p := range dump.Parameters {
		if strings.EqualFold(p.DisplayName, name) || strings.EqualFold(p.Identifier, name) {
			lo, hi = p.Min, p.Max
		}
	}
	if r, ok := ranges[name].([]float64); ok && len(r) == 2 {
		lo, hi = math.Max(lo, r[0]), math.Min(hi, r[1])
	}
	return math.Min(math.Max(x, lo), hi)
}

func (s *Server) runProfile(ctx context.Context, job *craftJob, dump device.ProbeDump, probe string, card craft.CardSpec, lab []byte, sweeps []craft.Sweep, lib *craft.Library) (*profileReport, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	began := time.Now()
	rep := &profileReport{Plugin: card.Plugin, Probe: probe, Name: dump.Name}
	defer func() { rep.Seconds = math.Round(time.Since(began).Seconds()) }()

	job.setPhase("storing the lab")
	s.songsMu.Lock()
	_, _, _, err := s.saveSongLocked(pluginLab, lab, false)
	s.songsMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("the lab song: %v", err)
	}
	job.setPhase("loading the lab")
	cs, fail := s.loadSong(pluginLab)
	if fail != nil {
		return nil, fmt.Errorf("%s", callText(fail))
	}
	lr, err := s.runLoadSong(ctx, cs, loadSongArgs{Check: false})
	if err != nil {
		return nil, fmt.Errorf("loading the lab: %w", err)
	}
	if !lr.OK {
		return nil, fmt.Errorf("loading the lab: %s", lr.text("not loaded"))
	}
	tap, _ := s.songTap(lr.Session)
	if tap == nil {
		return nil, fmt.Errorf("no ProbeAudioTap streams in the lab")
	}

	passed := craft.CardSpec{Plugin: card.Plugin, Name: card.Name, Capabilities: map[string]craft.ProvisionSpec{}, Notes: card.Notes}
	var said []string
	section := 0
	for _, sw := range sweeps {
		ps := profileSweep{Sweep: sw}
		for i := range sw.Values {
			job.setPhase("%s: %s at %g (%d of %d)", sw.Capability, sw.Param, sw.Values[i], section+1, countValues(sweeps))
			sig, err := s.grabPass(ctx, cs, tap, verifyPass{from: section, bars: profileBars})
			section++
			if err != nil {
				return rep, fmt.Errorf("%s at %g: %w", sw.Capability, sw.Values[i], err)
			}
			v, err := measure.Measure(sig, string(sw.Measure), sw.Band)
			if err != nil {
				return rep, fmt.Errorf("%s at %g: %w", sw.Capability, sw.Values[i], err)
			}
			ps.Readings = append(ps.Readings, round2(v))
		}
		ps.Passed, ps.Said = sw.Judge(ps.Readings)
		if ps.Passed {
			passed.Capabilities[sw.Capability] = card.Capabilities[sw.Capability]
		}
		said = append(said, ps.Said)
		rep.Sweeps = append(rep.Sweeps, ps)
	}
	if len(passed.Capabilities) == 0 {
		rep.Card = &card
		return rep, nil
	}
	// The step size of every parameter the card sets over CC, over its
	// mapping range, goes with what was measured.
	spec, err := passed.Render(lib.Vocab)
	if err != nil {
		return rep, err
	}
	parsed, err := craft.ParsePluginCard(spec, lib.Vocab)
	if err != nil {
		return rep, err
	}
	rep.Steps = craft.Steps(parsed, dump)
	passed.Measured = &craft.Measured{At: time.Now().UTC().Format(time.RFC3339), By: "profile_plugin", Checks: append(said, rep.Steps...)}
	passed.Notes = append(passed.Notes, "Profiled at the tap with white noise in bursts; parameters matched by name, unit and range. A CC's step follows the parameter's travel (linear, or its log scale).")
	file := filepath.Join(config.CraftDir(), craft.PluginsDir, probe+".yaml")
	var prev []byte
	if old, ok := lib.Plugin(card.Plugin); ok && strings.HasPrefix(old.Source, "craft/") {
		// The rig's earlier card of the plugin is replaced, not doubled; what
		// it says of what was not swept now stays.
		file = filepath.Join(config.CraftDir(), strings.TrimPrefix(old.Source, "craft/"))
		if prev, err = os.ReadFile(file); err != nil {
			return rep, fmt.Errorf("the rig's card of %s: %v", dump.Name, err)
		}
	}
	swept := make([]string, 0, len(sweeps))
	for _, sw := range sweeps {
		swept = append(swept, sw.Capability)
	}
	data, kept, err := passed.Over(prev, swept, lib.Vocab)
	if err != nil {
		return rep, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return rep, err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return rep, err
	}
	rep.Card, rep.File, rep.Kept = &passed, file, kept
	return rep, nil
}
