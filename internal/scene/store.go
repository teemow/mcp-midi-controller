package scene

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store persists scenes as human-readable YAML files in a directory (one file
// per scene), so the rig stays versionable as code.
type Store struct {
	dir string
}

// NewStore returns a scene store backed by dir (created on first save).
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, sanitize(name)+".yaml")
}

// Save writes a scene to disk.
func (s *Store) Save(sc *Scene) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(sc)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(sc.Name), b, 0o644)
}

// Load reads a scene by name.
func (s *Store) Load(name string) (*Scene, error) {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, err
	}
	var sc Scene
	if err := yaml.Unmarshal(b, &sc); err != nil {
		return nil, fmt.Errorf("parse scene %q: %w", name, err)
	}
	return &sc, nil
}

// List returns the names of all stored scenes.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

func sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
}
