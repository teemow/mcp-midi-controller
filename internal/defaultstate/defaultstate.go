// Package defaultstate names the user-defined AUv3 default-state files
// (config.AUv3DefaultStatesDir): one <ID>.yaml per audio unit, and Migrate
// moves files named the old way to that name.
package defaultstate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/teemow/midi-device/device"
	"github.com/teemow/midi-device/device/sanitize"
)

// ID is the file id (the filename without .yaml) a default state captured
// from an audio unit gets. A component with a type is named by
// device.ComponentID, "<type>-<subtype>-<manufacturer>", which differs for
// every distinct component, so two audio units that share a subtype keep a
// file each; it is also the id the unit's staged probe dump has
// (device.ProbeID). A component without a type keeps the old key: the
// sanitized subtype, else the sanitized name.
func ID(c device.ProbeComponent, name string) string {
	if c.Type != "" {
		return device.ComponentID(c)
	}
	return device.FirstNonEmpty(sanitize.ID(c.Subtype), sanitize.ID(name))
}

// Result counts what Migrate changed.
type Result struct {
	// Renamed counts files moved to <ID>.yaml.
	Renamed int
	// Removed counts files dropped because <ID>.yaml already held the same
	// audio unit's default state and was at least as new.
	Removed int
}

// Migrate renames every default-state file that capture_auv3_default_state
// named by its sanitized subtype alone (<subtype>.yaml) to <ID>.yaml, so a
// later capture of the same unit writes the file that already holds its
// state, and a capture of another unit with the same subtype no longer
// replaces it.
//
// Only files named exactly <sanitized subtype>.yaml whose component has a
// type move. A file with any other name was given its id by hand (out_id,
// set_auv3_default_state) and is left alone, as are type-less components
// (their ID is still the old key), files that do not decode, and directories.
//
// A newer file is never overwritten. When <ID>.yaml already exists and holds
// the same unit, the newer of the two is kept under <ID>.yaml and the older
// dropped: the two would match the same nodes, and the older one would win
// the lookup by sorting first. When <ID>.yaml holds another unit or does not
// decode, both files stay and the conflict is returned as an error.
//
// A missing dir is not an error. Per-file failures do not stop the run; they
// are returned joined.
func Migrate(dir string) (Result, error) {
	var res Result
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || filepath.Ext(name) != ".yaml" {
			continue
		}
		oldPath := filepath.Join(dir, name)
		def, err := load(oldPath)
		if err != nil {
			continue
		}
		c := def.Component
		legacy := sanitize.ID(c.Subtype)
		if c.Type == "" || legacy == "" || name != legacy+".yaml" {
			continue
		}
		newName := ID(c, def.Name) + ".yaml"
		if newName == name {
			continue
		}
		newPath := filepath.Join(dir, newName)

		existing, err := os.Stat(newPath)
		switch {
		case err == nil:
			current, lerr := load(newPath)
			if lerr != nil || !sameUnit(current.Component, c) {
				errs = append(errs, fmt.Errorf("%s: %s already exists and does not hold the same audio unit; both left in place", name, newName))
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				errs = append(errs, ierr)
				continue
			}
			if !info.ModTime().After(existing.ModTime()) {
				if err := os.Remove(oldPath); err != nil {
					errs = append(errs, err)
					continue
				}
				res.Removed++
				continue
			}
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			errs = append(errs, err)
			continue
		}
		res.Renamed++
	}
	return res, errors.Join(errs...)
}

// sameUnit reports whether a and b name the same audio unit: the tuple the
// AUM author matches a default state to a node by.
func sameUnit(a, b device.ProbeComponent) bool {
	return a.Type == b.Type && a.Subtype == b.Subtype && a.Manufacturer == b.Manufacturer
}

func load(path string) (device.AUv3DefaultState, error) {
	var def device.AUv3DefaultState
	b, err := os.ReadFile(path)
	if err != nil {
		return def, err
	}
	err = yaml.Unmarshal(b, &def)
	return def, err
}
