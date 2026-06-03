package aum

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpusRoundTrip is the Phase-0 fidelity harness over a real local corpus
// of AUM files. It is gated on AUM_CORPUS (a directory, searched recursively
// for *.aumproj and *.aum_midimap) because the corpus is a PRIVATE rig snapshot
// that is never committed (see .cursor/rules/public-vs-private.mdc); without it
// the test is skipped and only the synthetic round-trip in archive_test.go runs.
//
// For every file it asserts the writer invariant: decode(encode(decode(f)))
// graph-equals decode(f). It never asserts byte equality — the binary encoder
// rebuilds the offset table.
//
// The on-device acceptance gate (does AUM still open a re-encoded session?)
// cannot be automated here. Set AUM_ROUNDTRIP_OUT to a directory to materialize
// re-encoded copies of every file, then copy one to the iPad's AUM folder and
// confirm it opens unchanged.
func TestCorpusRoundTrip(t *testing.T) {
	dir := os.Getenv("AUM_CORPUS")
	if dir == "" {
		t.Skip("set AUM_CORPUS to a directory of .aumproj/.aum_midimap files to run the fidelity harness")
	}

	outDir := os.Getenv("AUM_ROUNDTRIP_OUT")
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("create AUM_ROUNDTRIP_OUT %s: %v", outDir, err)
		}
	}

	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".aumproj", ".aum_midimap":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Skipf("no .aumproj/.aum_midimap files under %s", dir)
	}
	t.Logf("round-tripping %d AUM file(s) under %s", len(files), dir)

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			a1, err := DecodeFile(path)
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			data, err := a1.Encode()
			if err != nil {
				t.Fatalf("encode %s: %v", path, err)
			}
			a2, err := Decode(data)
			if err != nil {
				t.Fatalf("re-decode %s: %v", path, err)
			}
			if !GraphEqual(a1, a2) {
				t.Fatalf("round-trip changed the graph: %s", path)
			}

			if outDir != "" {
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					rel = filepath.Base(path)
				}
				dst := filepath.Join(outDir, rel)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatalf("mkdir for %s: %v", dst, err)
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					t.Fatalf("write re-encoded %s: %v", dst, err)
				}
			}
		})
	}
}
