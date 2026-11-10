// Package probestaging keeps the AUv3 probe staging dir (config.AUv3ProbesDir)
// named the way the receiver names it: one <device.ProbeID>.json per audio
// unit.
package probestaging

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/teemow/midi-device/device"
)

// Result counts what Migrate changed.
type Result struct {
	// Renamed counts dumps moved to <ProbeID>.json.
	Renamed int
	// Removed counts dumps dropped because the same unit's dump under
	// <ProbeID>.json was at least as new.
	Removed int
}

// Migrate renames every staged dump whose filename is not its current
// device.ProbeID to <ProbeID>.json, so the ids that list_auv3_probes,
// get_auv3_probe, import_auv3_probe, discover and the AUM tools derive from a
// dump resolve to its file. Receivers before midi-device's component id named
// a dump by its subtype alone; those files are renamed here.
//
// When <ProbeID>.json already exists it holds the same unit (the name is the
// unit's id), so the older of the two files is dropped. Files that are not a
// decodable dump, or have no id, are left alone, as are directories (the
// receiver's _diagnostics). A missing dir is not an error. Per-file failures
// do not stop the run; they are returned joined.
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
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		oldPath := filepath.Join(dir, name)
		id, ok := stagedID(oldPath)
		if !ok || id+".json" == name {
			continue
		}
		newPath := filepath.Join(dir, id+".json")

		existing, err := os.Stat(newPath)
		switch {
		case err == nil:
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

// stagedID decodes the dump at path and returns its ProbeID, or false when
// the file is not a dump with an id.
func stagedID(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var dump device.ProbeDump
	if err := json.Unmarshal(b, &dump); err != nil {
		return "", false
	}
	id := device.ProbeID(dump)
	return id, id != ""
}
