package device

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Registry holds the loaded device definitions, keyed by definition ID.
type Registry struct {
	defs map[string]*Definition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]*Definition{}}
}

// LoadBundled loads the definitions embedded in the binary.
func LoadBundled() (*Registry, error) {
	r := NewRegistry()
	if err := r.loadFS(bundledFS, "definitions"); err != nil {
		return nil, fmt.Errorf("load bundled definitions: %w", err)
	}
	return r, nil
}

// LoadDir loads (and overrides) definitions from a directory of *.yaml files.
// A definition with an ID that already exists replaces the bundled one.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // user dir is optional
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := r.add(b, e.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) loadFS(fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		b, err := fs.ReadFile(fsys, filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := r.add(b, e.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) add(b []byte, src string) error {
	var d Definition
	if err := yaml.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if err := d.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", src, err)
	}
	r.defs[d.ID] = &d
	return nil
}

// Get returns the definition with the given ID.
func (r *Registry) Get(id string) (*Definition, bool) {
	d, ok := r.defs[id]
	return d, ok
}

// All returns every definition, sorted by ID.
func (r *Registry) All() []*Definition {
	out := make([]*Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
