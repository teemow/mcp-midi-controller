package sound

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// DefaultPatch is the name a plugin's sound without any preset loaded is
// kept under.
const DefaultPatch = "(default patch)"

// Plugin is everything measured of one plugin.
type Plugin struct {
	// Plugin is the staged probe id and Name what the plugin calls itself.
	Plugin  string            `yaml:"plugin" json:"plugin"`
	Name    string            `yaml:"name,omitempty" json:"name,omitempty"`
	Presets map[string]*Sound `yaml:"presets" json:"presets"`
}

// Catalog is the rig's sound catalogue: one YAML file per plugin in a
// directory of the rig's configuration. The files are meant to be read and
// corrected by hand as well.
type Catalog struct {
	dir string
	mu  sync.Mutex
}

// NewCatalog returns the catalogue kept in dir. The directory is created with
// the first measurement.
func NewCatalog(dir string) *Catalog { return &Catalog{dir: dir} }

func (c *Catalog) path(plugin string) (string, error) {
	if plugin == "" || strings.ContainsAny(plugin, `/\`) || plugin == "." || plugin == ".." {
		return "", fmt.Errorf("%q is not a plugin id", plugin)
	}
	return filepath.Join(c.dir, plugin+".yaml"), nil
}

// Plugin returns what is measured of a plugin, nil when nothing is.
func (c *Catalog) Plugin(plugin string) (*Plugin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.load(plugin)
}

func (c *Catalog) load(plugin string) (*Plugin, error) {
	path, err := c.path(plugin)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Plugin
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Plugin = plugin
	if p.Presets == nil {
		p.Presets = map[string]*Sound{}
	}
	for name, s := range p.Presets {
		if s == nil {
			delete(p.Presets, name)
		}
	}
	return &p, nil
}

func (c *Catalog) save(p *Plugin) error {
	path, err := c.path(p.Plugin)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// find returns a preset's entry, matching its name exactly and then without
// regard to case.
func (p *Plugin) find(preset string) (string, *Sound) {
	if preset == "" {
		preset = DefaultPatch
	}
	if s, ok := p.Presets[preset]; ok {
		return preset, s
	}
	for name, s := range p.Presets {
		if strings.EqualFold(name, preset) {
			return name, s
		}
	}
	return preset, nil
}

// Sound returns a preset's measurement ("" is the default patch), nil when it
// was never measured.
func (c *Catalog) Sound(plugin, preset string) *Sound {
	p, err := c.Plugin(plugin)
	if err != nil || p == nil {
		return nil
	}
	_, s := p.find(preset)
	return s
}

// Put stores a measurement of a preset ("" is the default patch) and returns
// the entry as kept: what was set by hand before (its pinned fields and its
// comment) stays.
func (c *Catalog) Put(plugin, name, preset string, s Sound) (*Sound, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.load(plugin)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = &Plugin{Plugin: plugin, Presets: map[string]*Sound{}}
	}
	if name != "" {
		p.Name = name
	}
	key, old := p.find(preset)
	if old != nil {
		s.Comment, s.Pinned = old.Comment, old.Pinned
		for _, field := range old.Pinned {
			switch field {
			case "transpose":
				s.Transpose = old.Transpose
			case "lanes":
				s.Lanes = old.Lanes
				byNote := map[int]string{}
				for lane, note := range old.Lanes {
					byNote[note] = lane
				}
				for i := range s.Pads {
					s.Pads[i].Lane = byNote[s.Pads[i].Note]
				}
			}
		}
	}
	p.Presets[key] = &s
	return &s, c.save(p)
}

// Correct changes a measured preset by hand: edit receives the entry and
// returns the fields it set ("transpose", "lanes"), which a later measurement
// then leaves alone.
func (c *Catalog) Correct(plugin, preset string, edit func(*Sound) []string) (*Sound, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.load(plugin)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("nothing is measured of %s yet", plugin)
	}
	_, s := p.find(preset)
	if s == nil {
		return nil, fmt.Errorf("%s has no measured preset %q", plugin, preset)
	}
	for _, field := range edit(s) {
		pinned := false
		for _, have := range s.Pinned {
			pinned = pinned || have == field
		}
		if !pinned {
			s.Pinned = append(s.Pinned, field)
		}
	}
	sort.Strings(s.Pinned)
	return s, c.save(p)
}

// Plugins returns every plugin with measurements, by id.
func (c *Catalog) Plugins() ([]*Plugin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Plugin
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p, err := c.load(strings.TrimSuffix(e.Name(), ".yaml"))
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out, nil
}
