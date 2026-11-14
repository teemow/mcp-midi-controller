package craft

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// TestShippedCardsLoad is the check of every card that ships: each loads
// against the daemon's vocabulary, is named after its id, and the first
// mastering cards are there.
func TestShippedCardsLoad(t *testing.T) {
	lib, err := Load("")
	if err != nil {
		t.Fatalf("the shipped cards do not load:\n%v", err)
	}
	var ids []string
	for _, tc := range lib.Techniques() {
		ids = append(ids, tc.ID)
		if tc.Source != "shipped" {
			t.Errorf("%s: source %q", tc.ID, tc.Source)
		}
		if tc.Kind == KindSettings && tc.Verify == nil {
			t.Errorf("%s: a shipped settings card says how its result is checked", tc.ID)
		}
		if tc.Intent == "" || tc.When == "" {
			t.Errorf("%s: a shipped card says what it is for and when", tc.ID)
		}
	}
	for _, want := range []string{
		"mastering.subsonic_highpass", "mastering.mono_low_end", "mastering.air",
		"mastering.glue", "mastering.warmth", "mastering.ceiling",
	} {
		if !slices.Contains(ids, want) {
			t.Errorf("no shipped card %s (have %v)", want, ids)
		}
	}
	for _, dir := range []string{TechniquesDir, PluginsDir} {
		entries, err := fs.ReadDir(Shipped(), dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			data, err := fs.ReadFile(Shipped(), dir+"/"+e.Name())
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSuffix(e.Name(), ".yaml")
			if dir == TechniquesDir {
				if tc, err := ParseTechnique(data, Default()); err == nil && tc.ID != want {
					t.Errorf("%s holds the card %s", e.Name(), tc.ID)
				}
			}
		}
	}
	ceiling, ok := lib.Technique("mastering.ceiling")
	if !ok || ceiling.Place.String() != "last" || ceiling.Verify.Measure != MeasureTruePeak {
		t.Errorf("mastering.ceiling = %+v", ceiling)
	}
}

func TestRigCardsReplaceShippedOnes(t *testing.T) {
	rig := fstest.MapFS{
		"techniques/ceiling.yaml": {Data: []byte(`
id: mastering.ceiling
craft: mastering
title: A ceiling for a loud master
targets: master
needs: [dynamics.limiter]
set:
  dynamics.limiter: {ceiling: -2}
place: last
`)},
		"techniques/hp.yaml":     {Data: []byte(highpassCard)},
		"techniques/broken.yaml": {Data: []byte("id: mastering.air\ncraft: mastering\ntitle: X\ntargets: master\nneeds: [eq.tilt]\n")},
		"techniques/notes.txt":   {Data: []byte("not a card")},
		"plugins/eq.yaml":        {Data: []byte(eqCard)},
	}
	lib, err := LoadFrom(Shipped(), rig, Default())
	if err == nil || len(lib.Problems) != 1 || !strings.Contains(lib.Problems[0], "craft/techniques/broken.yaml") || !strings.Contains(lib.Problems[0], "eq.tilt") {
		t.Fatalf("problems = %v (err %v)", lib.Problems, err)
	}
	c, _ := lib.Technique("mastering.ceiling")
	if c.Title != "A ceiling for a loud master" || c.Source != "craft/techniques/ceiling.yaml" {
		t.Errorf("the rig's card does not replace the shipped one: %+v", c)
	}
	if air, ok := lib.Technique("mastering.air"); !ok || air.Source != "shipped" {
		t.Errorf("a broken rig card leaves the shipped one: %+v", air)
	}
	if hp, ok := lib.Technique("mixing.highpass_non_bass"); !ok || hp.Source != "craft/techniques/hp.yaml" {
		t.Errorf("the rig's own card: %+v", hp)
	}
	techniques := lib.Techniques()
	if techniques[0].Craft != "mastering" || techniques[len(techniques)-1].ID != "mixing.highpass_non_bass" {
		t.Errorf("cards are listed by craft and id: %s .. %s", techniques[0].ID, techniques[len(techniques)-1].ID)
	}
	if p, ok := lib.Plugin("aufx/Eq1x/Test"); !ok || p.Source != "craft/plugins/eq.yaml" {
		t.Errorf("plugin card = %+v", p)
	}
}

func TestRigDuplicates(t *testing.T) {
	card := "id: mastering.x\ncraft: mastering\ntitle: X\ntargets: master\nneeds: [level.gain]\n"
	rig := fstest.MapFS{
		"techniques/a.yaml": {Data: []byte(card)},
		"techniques/b.yaml": {Data: []byte(card)},
		"plugins/a.yaml":    {Data: []byte(eqCard)},
		"plugins/b.yaml":    {Data: []byte(eqCard)},
	}
	lib, _ := LoadFrom(Shipped(), rig, Default())
	if len(lib.Problems) != 2 || !strings.Contains(lib.Problems[0], "mastering.x is already the id of craft/techniques/a.yaml") ||
		!strings.Contains(lib.Problems[1], "aufx/Eq1x/Test already has a card, craft/plugins/a.yaml") {
		t.Errorf("problems = %v", lib.Problems)
	}
}

// limiterCard returns a plugin card that provides a limiter.
func limiterCard(tuple, name string) []byte {
	return []byte("plugin: " + tuple + "\nname: " + name + `
capabilities:
  dynamics.limiter:
    write: cc
    params:
      ceiling: {param: Ceiling, range: [-3, 0]}
      gain: Gain
      release: Release
`)
}

func TestPrefer(t *testing.T) {
	rig := fstest.MapFS{
		"plugins/a.yaml":  {Data: limiterCard("aufx/LimA/Test", "Alpha Limiter")},
		"plugins/b.yaml":  {Data: limiterCard("aufx/LimB/Test", "Beta Limiter")},
		"plugins/c.yaml":  {Data: limiterCard("aufx/LimC/Test", "Gamma Limiter")},
		"plugins/eq.yaml": {Data: []byte(eqCard)},
		"prefer.yaml": {Data: []byte(`
dynamics.limiter: [aufx/LimC/Test, aufx/LimB/Test]
eq.highpass: aufx/Eq1x/Test
`)},
	}
	lib, err := LoadFrom(Shipped(), rig, Default())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range lib.Providers("dynamics.limiter") {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"Gamma Limiter", "Beta Limiter", "Alpha Limiter"}) {
		t.Errorf("providers in the rig's order = %v", names)
	}
	if got := lib.Prefer("eq.highpass"); !slices.Equal(got, []string{"aufx/Eq1x/Test"}) {
		t.Errorf("prefer eq.highpass = %v", got)
	}
	if got := lib.Providers("stereo.width"); len(got) != 0 {
		t.Errorf("no card provides stereo.width: %v", got)
	}

	rig["prefer.yaml"] = &fstest.MapFile{Data: []byte(`
dynamics.limiter: [aufx/LimC/Test, aufx/LimX/Test, aufx/Eq1x/Test, aufx/LimC/Test, nonsense]
eq.tilt: [aufx/Eq1x/Test]
`)}
	lib, _ = LoadFrom(Shipped(), rig, Default())
	for i, want := range []string{
		"dynamics.limiter: aufx/LimX/Test has no plugin card",
		"dynamics.limiter: the card of aufx/Eq1x/Test (Test EQ) does not provide it",
		"dynamics.limiter: aufx/LimC/Test twice",
		`dynamics.limiter: "nonsense" is not a component tuple`,
		`"eq.tilt" is not a capability`,
	} {
		if i >= len(lib.Problems) || !strings.Contains(lib.Problems[i], want) {
			t.Errorf("problems = %v, want %q at %d", lib.Problems, want, i)
		}
	}
	if got := lib.Prefer("dynamics.limiter"); !slices.Equal(got, []string{"aufx/LimC/Test"}) {
		t.Errorf("what is right of a broken order is kept: %v", got)
	}
}

func TestLoadFromConfigDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, TechniquesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TechniquesDir, "hp.yaml"), []byte(highpassCard), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lib.Technique("mixing.highpass_non_bass"); !ok {
		t.Error("the config dir's card is not loaded")
	}
	if _, err := Load(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("a rig without a craft dir: %v", err)
	}
}
