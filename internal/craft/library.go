package craft

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
)

// shipped holds the cards that ship with the daemon: cards/techniques/ and
// cards/plugins/, one card per YAML file.
//
//go:embed cards
var shipped embed.FS

// Shipped returns the cards that ship with the daemon, as a file system with
// techniques/ and plugins/ at its top.
func Shipped() fs.FS {
	sub, err := fs.Sub(shipped, "cards")
	if err != nil {
		panic(err) // the embedded tree always has cards/
	}
	return sub
}

// The rig's own files in its craft dir (config dir craft/).
const (
	TechniquesDir = "techniques"
	PluginsDir    = "plugins"
	PreferFile    = "prefer.yaml"
	sourceShipped = "shipped"
)

// Library is every card the daemon knows: the shipped ones, and the rig's own
// in its craft dir, where a card with the id of a shipped one replaces it.
type Library struct {
	Vocab      Vocabulary
	techniques map[string]*Technique
	plugins    map[string]*PluginCard // by component tuple
	prefer     map[string][]string    // capability -> component tuples, most preferred first
	// Problems are the files that did not load, each with what is wrong.
	Problems []string
}

// Load reads the shipped cards and the rig's craft dir (it need not exist),
// checked against the daemon's vocabulary.
func Load(craftDir string) (*Library, error) {
	var rig fs.FS
	if craftDir != "" {
		rig = os.DirFS(craftDir)
	}
	return LoadFrom(Shipped(), rig, Default())
}

// LoadFrom reads the cards of shipped and of rig (nil: none) against vocab.
// A card that does not load is left out and named in Problems, and so in the
// error; the library holds every card that did load, so one broken card of a
// rig leaves the others usable.
func LoadFrom(shipped, rig fs.FS, vocab Vocabulary) (*Library, error) {
	l := &Library{
		Vocab:      vocab,
		techniques: map[string]*Technique{},
		plugins:    map[string]*PluginCard{},
		prefer:     map[string][]string{},
	}
	l.loadTechniques(shipped, sourceShipped)
	l.loadPlugins(shipped, sourceShipped)
	if rig != nil {
		l.loadTechniques(rig, "")
		l.loadPlugins(rig, "")
		l.loadPrefer(rig)
	}
	if len(l.Problems) == 0 {
		return l, nil
	}
	return l, errors.New(strings.Join(l.Problems, "\n"))
}

func (l *Library) problemf(format string, args ...any) {
	l.Problems = append(l.Problems, fmt.Sprintf(format, args...))
}

// cardFiles lists the YAML files of dir in fsys, sorted; a missing dir has
// none.
func (l *Library) cardFiles(fsys fs.FS, dir, origin string) []string {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		l.problemf("%s: %v", where(origin, dir), err)
		return nil
	}
	var out []string
	for _, e := range entries {
		ext := path.Ext(e.Name())
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, path.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// where names a file for a problem or a card's source: "shipped <file>" or
// the file in the craft dir.
func where(origin, file string) string {
	if origin == sourceShipped {
		return "shipped " + file
	}
	return "craft/" + file
}

func (l *Library) loadTechniques(fsys fs.FS, origin string) {
	seen := map[string]string{}
	for _, file := range l.cardFiles(fsys, TechniquesDir, origin) {
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			l.problemf("%s: %v", where(origin, file), err)
			continue
		}
		t, err := ParseTechnique(data, l.Vocab)
		if err != nil {
			l.problemf("%s: %v", where(origin, file), err)
			continue
		}
		if other, dup := seen[t.ID]; dup {
			l.problemf("%s: %s is already the id of %s", where(origin, file), t.ID, other)
			continue
		}
		seen[t.ID] = where(origin, file)
		t.Source = sourceShipped
		if origin != sourceShipped {
			t.Source = where(origin, file)
		}
		l.techniques[t.ID] = t
	}
}

func (l *Library) loadPlugins(fsys fs.FS, origin string) {
	seen := map[string]string{}
	for _, file := range l.cardFiles(fsys, PluginsDir, origin) {
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			l.problemf("%s: %v", where(origin, file), err)
			continue
		}
		c, err := ParsePluginCard(data, l.Vocab)
		if err != nil {
			l.problemf("%s: %v", where(origin, file), err)
			continue
		}
		if other, dup := seen[c.Plugin]; dup {
			l.problemf("%s: %s already has a card, %s", where(origin, file), c.Plugin, other)
			continue
		}
		seen[c.Plugin] = where(origin, file)
		c.Source = sourceShipped
		if origin != sourceShipped {
			c.Source = where(origin, file)
		}
		l.plugins[c.Plugin] = c
	}
}

// loadPrefer reads the rig's order of preference: per capability, the
// plugins by component tuple, most preferred first. Every plugin it names
// needs a card that provides the capability.
func (l *Library) loadPrefer(fsys fs.FS) {
	data, err := fs.ReadFile(fsys, PreferFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	file := where("", PreferFile)
	if err != nil {
		l.problemf("%s: %v", file, err)
		return
	}
	var raw map[string]stringList
	if err := decodeStrict(data, &raw); err != nil {
		l.problemf("%s: %v", file, err)
		return
	}
	for _, capability := range sortedKeys(raw) {
		if _, ok := l.Vocab.Capability(capability); !ok {
			l.problemf("%s: %s", file, unknownCapability(l.Vocab, capability))
			continue
		}
		var order []string
		for _, tuple := range raw[capability] {
			c, err := ParseComponent(tuple)
			if err != nil {
				l.problemf("%s: %s: %v", file, capability, err)
				continue
			}
			tuple = ComponentTuple(c)
			card, ok := l.plugins[tuple]
			switch {
			case !ok:
				l.problemf("%s: %s: %s has no plugin card", file, capability, tuple)
				continue
			case !card.provides(capability):
				l.problemf("%s: %s: the card of %s (%s) does not provide it", file, capability, tuple, card.Name)
				continue
			case slices.Contains(order, tuple):
				l.problemf("%s: %s: %s twice", file, capability, tuple)
				continue
			}
			order = append(order, tuple)
		}
		if len(order) > 0 {
			l.prefer[capability] = order
		}
	}
}

func (c *PluginCard) provides(capability string) bool {
	_, ok := c.Provision(capability)
	return ok
}

// Techniques lists the cards, by craft and id.
func (l *Library) Techniques() []*Technique {
	out := make([]*Technique, 0, len(l.techniques))
	for _, t := range l.techniques {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Craft != out[j].Craft {
			return out[i].Craft < out[j].Craft
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Technique returns the card with id.
func (l *Library) Technique(id string) (*Technique, bool) {
	t, ok := l.techniques[strings.TrimSpace(id)]
	return t, ok
}

// Plugins lists the plugin cards, by name.
func (l *Library) Plugins() []*PluginCard {
	out := make([]*PluginCard, 0, len(l.plugins))
	for _, c := range l.plugins {
		out = append(out, c)
	}
	sortCards(out)
	return out
}

// Plugin returns the card of a component tuple.
func (l *Library) Plugin(tuple string) (*PluginCard, bool) {
	c, ok := l.plugins[tuple]
	return c, ok
}

// Prefer returns the rig's order of preference for a capability, most
// preferred first.
func (l *Library) Prefer(capability string) []string {
	return append([]string(nil), l.prefer[capability]...)
}

// Providers lists the cards that provide a capability in the rig's order:
// the preferred ones as craft/prefer.yaml orders them, then the others by
// name.
func (l *Library) Providers(capability string) []*PluginCard {
	var out, rest []*PluginCard
	preferred := l.prefer[capability]
	for _, tuple := range preferred {
		out = append(out, l.plugins[tuple])
	}
	for _, c := range l.plugins {
		if c.provides(capability) && !slices.Contains(preferred, c.Plugin) {
			rest = append(rest, c)
		}
	}
	sortCards(rest)
	return append(out, rest...)
}

func sortCards(cards []*PluginCard) {
	sort.Slice(cards, func(i, j int) bool {
		if !strings.EqualFold(cards[i].Name, cards[j].Name) {
			return strings.ToLower(cards[i].Name) < strings.ToLower(cards[j].Name)
		}
		return cards[i].Plugin < cards[j].Plugin
	})
}
