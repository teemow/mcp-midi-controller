package probestaging

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"
)

const (
	// Two units that share a subtype: before the component id both were
	// staged as ab12.json.
	synthDump = `{"component":{"type":"aumu","subtype":"Ab12","manufacturer":"XyZw"},"name":"Synth","parameters":[]}`
	midiDump  = `{"component":{"type":"aumi","subtype":"Ab12","manufacturer":"XyZw"},"name":"Synth MIDI","parameters":[]}`
	delayDump = `{"component":{"type":"aufx","subtype":"dely","manufacturer":"acme"},"name":"Delay","parameters":[]}`
	// No component type: its id is still the sanitized subtype.
	typelessDump = `{"component":{"subtype":"dist"},"name":"Distortion","parameters":[]}`
)

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

func TestMigrateRenamesSubtypeNamedDumps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write(t, dir, "ab12.json", synthDump, now)
	write(t, dir, "aumi-_ab12-_xy_zw.json", midiDump, now) // already current
	write(t, dir, "dist.json", typelessDump, now)          // type-less: id unchanged
	write(t, dir, "broken.json", "{not json", now)
	write(t, dir, "notes.txt", "x", now)
	if err := os.Mkdir(filepath.Join(dir, "_diagnostics"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res != (Result{Renamed: 1}) {
		t.Fatalf("result = %+v, want 1 renamed", res)
	}
	want := []string{"_diagnostics", "aumi-_ab12-_xy_zw.json", "aumu-_ab12-_xy_zw.json", "broken.json", "dist.json", "notes.txt"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Fatalf("dir = %v, want %v", got, want)
	}
	if got := readFile(t, dir, "aumu-_ab12-_xy_zw.json"); got != synthDump {
		t.Fatalf("renamed dump = %s, want the synth dump", got)
	}

	// A second run finds nothing to do.
	res, err = Migrate(dir)
	if err != nil || res != (Result{}) {
		t.Fatalf("second run = %+v, %v; want no change", res, err)
	}
}

func TestMigrateKeepsTheNewerDumpOfOneUnit(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	newer := time.Now()

	t.Run("current name is newer", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "dely.json", delayDump, old)
		fresh := `{"component":{"type":"aufx","subtype":"dely","manufacturer":"acme"},"name":"Delay v2","parameters":[]}`
		write(t, dir, "aufx-dely-acme.json", fresh, newer)

		res, err := Migrate(dir)
		if err != nil || res != (Result{Removed: 1}) {
			t.Fatalf("result = %+v, %v; want 1 removed", res, err)
		}
		if got := listDir(t, dir); !slices.Equal(got, []string{"aufx-dely-acme.json"}) {
			t.Fatalf("dir = %v", got)
		}
		if got := readFile(t, dir, "aufx-dely-acme.json"); got != fresh {
			t.Fatalf("kept %s, want the newer dump", got)
		}
	})

	t.Run("old name is newer", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "aufx-dely-acme.json", `{"component":{"type":"aufx","subtype":"dely","manufacturer":"acme"},"name":"Delay v1","parameters":[]}`, old)
		write(t, dir, "dely.json", delayDump, newer)

		res, err := Migrate(dir)
		if err != nil || res != (Result{Renamed: 1}) {
			t.Fatalf("result = %+v, %v; want 1 renamed", res, err)
		}
		if got := listDir(t, dir); !slices.Equal(got, []string{"aufx-dely-acme.json"}) {
			t.Fatalf("dir = %v", got)
		}
		if got := readFile(t, dir, "aufx-dely-acme.json"); got != delayDump {
			t.Fatalf("kept %s, want the newer dump", got)
		}
	})
}

func TestMigrateMissingDir(t *testing.T) {
	res, err := Migrate(filepath.Join(t.TempDir(), "absent"))
	if err != nil || res != (Result{}) {
		t.Fatalf("missing dir = %+v, %v; want no change, no error", res, err)
	}
}
