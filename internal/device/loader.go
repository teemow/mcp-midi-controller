package device

import (
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// isYAMLName reports whether name has a .yaml/.yml extension (case-insensitive).
func isYAMLName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// Registry holds the loaded device types, keyed by device-type ID. It is
// safe for concurrent use: authoring tools mutate it via AddDefinition while
// MCP handlers read it via Get/All from concurrent streamable-HTTP requests.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*DeviceType
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]*DeviceType{}}
}

// LoadBundled loads the device types embedded in the binary.
func LoadBundled() (*Registry, error) {
	r := NewRegistry()
	if err := r.loadFS(bundledFS, "device-types"); err != nil {
		return nil, fmt.Errorf("load bundled device types: %w", err)
	}
	return r, nil
}

// LoadDir loads (and overrides) device types from a directory of *.yaml/*.yml
// files. A device type with an ID that already exists replaces the bundled one.
//
// A single malformed or invalid user device type is skipped (logged) rather than
// aborting the load, so one bad file in the config dir cannot gate the daemon
// from coming up — consistent with the serve-first startup model. Only a
// directory-level read error (other than "not exist") is returned.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // user dir is optional
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !isYAMLName(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("device: skipping %s: %v", e.Name(), err)
			continue
		}
		if err := r.add(b, e.Name()); err != nil {
			log.Printf("device: skipping invalid device type %s: %v", e.Name(), err)
			continue
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
	var d DeviceType
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // reject misspelled/unknown keys rather than silently dropping them
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if err := d.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", src, err)
	}
	r.mu.Lock()
	r.defs[d.ID] = &d
	r.mu.Unlock()
	return nil
}

// AddDefinition validates and inserts (or replaces) a device type in the
// registry so an authored device type hot-loads without a daemon restart. The
// device type must have an id.
func (r *Registry) AddDefinition(d *DeviceType) error {
	if d == nil {
		return fmt.Errorf("nil device type")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	r.defs[d.ID] = d
	r.mu.Unlock()
	return nil
}

// Remove deletes a device type from the registry (a no-op for unknown ids) —
// the hot-unload counterpart of AddDefinition, used when a session re-import
// retires the previous session's generated types.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.defs, id)
	r.mu.Unlock()
}

// Get returns the device type with the given ID.
func (r *Registry) Get(id string) (*DeviceType, bool) {
	r.mu.RLock()
	d, ok := r.defs[id]
	r.mu.RUnlock()
	return d, ok
}

// All returns every device type, sorted by ID.
func (r *Registry) All() []*DeviceType {
	r.mu.RLock()
	out := make([]*DeviceType, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
