package song

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/teemow/midi-device/device/sanitize"
)

// Revision returns the identity of a stored song document: the first 8 bytes
// of its SHA-256 digest as 16 lowercase hex characters. Any change to the
// stored bytes gives another revision.
func Revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Store keeps songs as YAML files in a directory, one file per song, so a
// song is part of the rig's configuration like a scene is. A song is stored as
// the text it was written in: comments and layout are the author's.
type Store struct {
	dir string
}

// NewStore returns a store backed by dir (created on the first save).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// ID normalizes a song name into the id it is stored under.
func ID(name string) string { return sanitize.ID(name) }

func (s *Store) path(id string) (string, error) {
	clean := ID(id)
	if clean == "" {
		return "", fmt.Errorf("song id %q has no usable characters", id)
	}
	return filepath.Join(s.dir, clean+".yaml"), nil
}

// Save checks a song document and writes it under id, returning the parsed
// song.
func (s *Store) Save(id string, data []byte) (*Song, error) {
	sg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p, data, 0o644); err != nil {
		return nil, err
	}
	return sg, nil
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place so concurrent readers never see a torn song file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Load reads a song by id, returning it and the text it is stored as.
func (s *Store) Load(id string) (*Song, []byte, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("no song %q", ID(id))
		}
		return nil, nil, err
	}
	sg, err := Parse(data)
	if err != nil {
		return nil, data, fmt.Errorf("song %q: %w", ID(id), err)
	}
	return sg, data, nil
}

// Delete removes a song.
func (s *Store) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no song %q", ID(id))
		}
		return err
	}
	return nil
}

// List returns the ids of the stored songs.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		// Only files Load can find: a song is stored under its normalized id.
		if id := strings.TrimSuffix(e.Name(), ".yaml"); ID(id) == id {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
