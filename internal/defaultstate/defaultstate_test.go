package defaultstate

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/teemow/midi-device/device"
)

const (
	// Two units that share a subtype: before the component id a capture of
	// either was written to ab12.yaml, replacing the other.
	synthState = "component:\n  type: aumu\n  subtype: Ab12\n  manufacturer: XyZw\nname: Synth\nstate:\n  cfg:\n    text: synth\n"
	midiState  = "component:\n  type: aumi\n  subtype: Ab12\n  manufacturer: XyZw\nname: Synth MIDI\nstate:\n  cfg:\n    text: midi\n"
	delayState = "component:\n  type: aufx\n  subtype: dely\n  manufacturer: acme\nname: Delay\nstate:\n  cfg:\n    text: v1\n"
	// No component type: its id is still the sanitized subtype.
	typelessState = "component:\n  subtype: dist\nname: Distortion\nstate:\n  cfg:\n    text: x\n"
)

func TestID(t *testing.T) {
	synth := device.ProbeComponent{Type: "aumu", Subtype: "Ab12", Manufacturer: "XyZw"}
	midi := device.ProbeComponent{Type: "aumi", Subtype: "Ab12", Manufacturer: "XyZw"}
	other := device.ProbeComponent{Type: "aumu", Subtype: "Ab12", Manufacturer: "Qrst"}
	for _, tc := range []struct {
		c    device.ProbeComponent
		name string
		want string
	}{
		{synth, "Synth", "aumu-_ab12-_xy_zw"},
		{midi, "Synth MIDI", "aumi-_ab12-_xy_zw"},
		{other, "Synth", "aumu-_ab12-_qrst"},
		{device.ProbeComponent{Subtype: "Ab12"}, "Synth", "ab12"},
		{device.ProbeComponent{}, "My Synth", "my_synth"},
	} {
		if got := ID(tc.c, tc.name); got != tc.want {
			t.Errorf("ID(%+v, %q) = %q, want %q", tc.c, tc.name, got, tc.want)
		}
		if tc.c.Type != "" {
			if got := device.ProbeID(device.ProbeDump{Component: tc.c, Name: tc.name}); got != tc.want {
				t.Errorf("ProbeID = %q, want the default-state id %q", got, tc.want)
			}
		}
	}
}

func write(t *testing.T, dir, name, body string, mod time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrateRenamesSubtypeNamedFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write(t, dir, "ab12.yaml", synthState, now)
	write(t, dir, "my-midi.yaml", midiState, now)  // id given by hand: kept
	write(t, dir, "dist.yaml", typelessState, now) // type-less: id unchanged
	write(t, dir, "dely.yml", delayState, now)     // not written by capture: kept
	write(t, dir, "broken.yaml", "component: [", now)
	write(t, dir, "notes.txt", "x", now)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res != (Result{Renamed: 1}) {
		t.Fatalf("result = %+v, want 1 renamed", res)
	}
	want := []string{"aumu-_ab12-_xy_zw.yaml", "broken.yaml", "dely.yml", "dist.yaml", "my-midi.yaml", "notes.txt", "sub"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Fatalf("dir = %v, want %v", got, want)
	}
	if got := readFile(t, dir, "aumu-_ab12-_xy_zw.yaml"); got != synthState {
		t.Fatalf("renamed file = %s, want the synth's state", got)
	}

	// A second run finds nothing to do.
	res, err = Migrate(dir)
	if err != nil || res != (Result{}) {
		t.Fatalf("second run = %+v, %v; want no change", res, err)
	}
}

// TestMigrateThenSharedSubtypeKeepsBoth is the collision the component id
// fixes: after the old file moves, the other unit with the same subtype gets
// a file of its own instead of replacing it.
func TestMigrateThenSharedSubtypeKeepsBoth(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ab12.yaml", synthState, time.Now())
	if _, err := Migrate(dir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	midi := device.ProbeComponent{Type: "aumi", Subtype: "Ab12", Manufacturer: "XyZw"}
	write(t, dir, ID(midi, "Synth MIDI")+".yaml", midiState, time.Now())

	want := []string{"aumi-_ab12-_xy_zw.yaml", "aumu-_ab12-_xy_zw.yaml"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Fatalf("dir = %v, want %v", got, want)
	}
	if got := readFile(t, dir, "aumu-_ab12-_xy_zw.yaml"); got != synthState {
		t.Fatalf("synth file = %s, want the synth's state", got)
	}
}

func TestMigrateNeverOverwritesANewerFile(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	newer := time.Now()
	fresh := strings.Replace(delayState, "text: v1", "text: v2", 1)

	t.Run("current name is newer", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "dely.yaml", delayState, old)
		write(t, dir, "aufx-dely-acme.yaml", fresh, newer)

		res, err := Migrate(dir)
		if err != nil || res != (Result{Removed: 1}) {
			t.Fatalf("result = %+v, %v; want 1 removed", res, err)
		}
		if got := listDir(t, dir); !slices.Equal(got, []string{"aufx-dely-acme.yaml"}) {
			t.Fatalf("dir = %v", got)
		}
		if got := readFile(t, dir, "aufx-dely-acme.yaml"); got != fresh {
			t.Fatalf("kept %s, want the newer file", got)
		}
	})

	t.Run("same age keeps the current name", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "dely.yaml", delayState, newer)
		write(t, dir, "aufx-dely-acme.yaml", fresh, newer)

		res, err := Migrate(dir)
		if err != nil || res != (Result{Removed: 1}) {
			t.Fatalf("result = %+v, %v; want 1 removed", res, err)
		}
		if got := readFile(t, dir, "aufx-dely-acme.yaml"); got != fresh {
			t.Fatalf("kept %s, want the file under the current name", got)
		}
	})

	t.Run("old name is newer", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "aufx-dely-acme.yaml", delayState, old)
		write(t, dir, "dely.yaml", fresh, newer)

		res, err := Migrate(dir)
		if err != nil || res != (Result{Renamed: 1}) {
			t.Fatalf("result = %+v, %v; want 1 renamed", res, err)
		}
		if got := listDir(t, dir); !slices.Equal(got, []string{"aufx-dely-acme.yaml"}) {
			t.Fatalf("dir = %v", got)
		}
		if got := readFile(t, dir, "aufx-dely-acme.yaml"); got != fresh {
			t.Fatalf("kept %s, want the newer file", got)
		}
	})
}

func TestMigrateLeavesAnotherUnitsFileAlone(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	newer := time.Now()
	// A file given the synth's component id by hand, holding another unit.
	other := strings.Replace(synthState, "manufacturer: XyZw", "manufacturer: Qrst", 1)

	for _, tc := range []struct {
		name   string
		target string
	}{
		{"another unit", other},
		{"undecodable", "component: ["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "aumu-_ab12-_xy_zw.yaml", tc.target, old)
			write(t, dir, "ab12.yaml", synthState, newer)

			res, err := Migrate(dir)
			if err == nil || !strings.Contains(err.Error(), "both left in place") {
				t.Fatalf("err = %v, want a conflict", err)
			}
			if res != (Result{}) {
				t.Fatalf("result = %+v, want no change", res)
			}
			if got := readFile(t, dir, "aumu-_ab12-_xy_zw.yaml"); got != tc.target {
				t.Fatalf("target = %s, want it untouched", got)
			}
			if got := readFile(t, dir, "ab12.yaml"); got != synthState {
				t.Fatalf("old file = %s, want it untouched", got)
			}
		})
	}
}

func TestMigrateMissingDir(t *testing.T) {
	res, err := Migrate(filepath.Join(t.TempDir(), "absent"))
	if err != nil || res != (Result{}) {
		t.Fatalf("missing dir = %+v, %v; want no change, no error", res, err)
	}
}
