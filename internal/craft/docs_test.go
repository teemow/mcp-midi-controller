package craft

import (
	"os"
	"strings"
	"testing"
)

// TestDocsNameEverything keeps docs/craft-cards.md in step with the code: it
// names every capability with its parameters, every measurement and every
// shipped card.
func TestDocsNameEverything(t *testing.T) {
	data, err := os.ReadFile("../../docs/craft-cards.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, c := range Capabilities() {
		row := ""
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, "| `"+c.Name+"` |") {
				row = line
			}
		}
		if row == "" {
			t.Errorf("docs/craft-cards.md has no row for %s", c.Name)
			continue
		}
		for _, p := range c.Params {
			if !strings.Contains(row, "`"+p.Name+"` "+strings.TrimSuffix(p.Range(), " ")) {
				t.Errorf("the row of %s does not give %s as %s: %s", c.Name, p.Name, p.Range(), row)
			}
		}
	}
	for _, m := range Measurements() {
		if !strings.Contains(doc, "`"+string(m)+"`") {
			t.Errorf("docs/craft-cards.md does not name the measurement %s", m)
		}
	}
	lib, _ := Load("")
	for _, tc := range lib.Techniques() {
		if !strings.Contains(doc, "| `"+tc.ID+"` |") {
			t.Errorf("docs/craft-cards.md does not list the shipped card %s", tc.ID)
		}
	}
}
