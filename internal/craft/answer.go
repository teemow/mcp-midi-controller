package craft

import (
	"fmt"
	"slices"
	"strings"
)

// Answer is the one shape every craft tool answers in (ADR 0002): which craft
// spoke, the technique cards it used, the id-keyed merge patch on the song,
// the revision the patch was made against, why each change was made, what
// checks found, and whether the song was stored.
//
// Revision names the revision the patch was made against whether or not it
// was applied, so Patch and Revision always belong together: handed to
// patch_song, an answer that was not applied stores its patch while the song
// is unchanged, and one that was applied is refused as stale instead of being
// applied twice (unless its patch changed nothing, leaving the stored revision
// at the base). An applied answer's text names the revision the song was
// stored at, too ("revision: <stored> (from <revision>)").
type Answer struct {
	Craft      string         `json:"craft" yaml:"craft"`
	Techniques []string       `json:"techniques" yaml:"techniques"`
	Patch      map[string]any `json:"patch" yaml:"patch"`
	Revision   string         `json:"revision" yaml:"revision"`
	Why        []string       `json:"why" yaml:"why"`
	Checks     []string       `json:"checks" yaml:"checks"`
	Applied    bool           `json:"applied" yaml:"applied"`
}

// NewAnswer builds a normalized Answer whose slice and map fields are never nil
// so JSON encoding always emits all seven keys with non-null collections.
func NewAnswer(craftID string, techniques []string, patch map[string]any, revision string, why, checks []string, applied bool) Answer {
	return (Answer{
		Craft:      craftID,
		Techniques: techniques,
		Patch:      patch,
		Revision:   revision,
		Why:        why,
		Checks:     checks,
		Applied:    applied,
	}).Normalized()
}

// Normalized returns a copy of a with empty non-nil slices and patch map when
// any of them were nil.
func (a Answer) Normalized() Answer {
	if a.Techniques == nil {
		a.Techniques = []string{}
	} else {
		a.Techniques = slices.Clone(a.Techniques)
	}
	if a.Patch == nil {
		a.Patch = map[string]any{}
	}
	if a.Why == nil {
		a.Why = []string{}
	} else {
		a.Why = slices.Clone(a.Why)
	}
	if a.Checks == nil {
		a.Checks = []string{}
	} else {
		a.Checks = slices.Clone(a.Checks)
	}
	return a
}

// Validate checks that the answer names a known craft and a song revision.
func (a Answer) Validate() error {
	if !slices.Contains(Crafts, a.Craft) {
		return fmt.Errorf("unknown craft %q (known: %s)", a.Craft, strings.Join(Crafts, ", "))
	}
	if strings.TrimSpace(a.Revision) == "" {
		return fmt.Errorf("craft answer needs a song revision")
	}
	return nil
}

// Summary renders the human-readable header and notes of a craft answer.
func (a Answer) Summary() string {
	var b strings.Builder
	state := "patch ready (not applied)"
	if a.Applied {
		state = "applied"
	}
	fmt.Fprintf(&b, "%s: %s, made against revision %s", a.Craft, state, a.Revision)
	if len(a.Techniques) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(a.Techniques, ", "))
	}
	b.WriteByte('\n')
	for _, w := range a.Why {
		fmt.Fprintf(&b, "  why: %s\n", w)
	}
	for _, c := range a.Checks {
		fmt.Fprintf(&b, "  check: %s\n", c)
	}
	return strings.TrimRight(b.String(), "\n")
}
