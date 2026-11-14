package craft

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestExprEval(t *testing.T) {
	env := map[string]float64{
		VarTempo:         120,
		VarBeatMS:        500,
		VarBarMS:         2000,
		VarLowestNoteHz:  41.2,
		VarSectionEnergy: 6,
	}
	for _, tc := range []struct {
		src  string
		want float64
	}{
		{"12", 12},
		{"-1", -1},
		{"1 + 2 * 3", 7},
		{"(1 + 2) * 3", 9},
		{"10 / 4", 2.5},
		{"10 - 4 - 3", 3},
		{"-2 * -3", 6},
		{"+5", 5},
		{".5", 0.5},
		{"1e3", 1000},
		{"2.5e-1", 0.25},
		{"0.7 * lowest_note_hz", 0.7 * 41.2},
		{"beat_ms * 0.75", 375},
		{"bar_ms / 4", 500},
		{"60000 / tempo", 500},
		{"section.energy * 10", 60},
		{"min(3, 1, 2)", 1},
		{"max(1, 4)", 4},
		{"clamp(0.5 * lowest_note_hz, 20, 35)", 20.6},
		{"clamp(5, 20, 35)", 20},
		{"clamp(50, 20, 35)", 35},
		{"clamp(beat_ms / 2, 50, 400)", 250},
		{"min(max(1, 2), 3) + -(4 - 6)", 4},
	} {
		e, err := ParseExpr(tc.src, SongVars())
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		got, err := e.Eval(env)
		if err != nil {
			t.Errorf("%q: eval: %v", tc.src, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%q = %g, want %g", tc.src, got, tc.want)
		}
	}
}

func TestExprParseErrors(t *testing.T) {
	for _, tc := range []struct {
		src, want string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"foo", "foo is not known here"},
		{"lowest_note", "lowest_note is not known here"},
		{"pow(2, 3)", "pow is not a function"},
		{"clamp(1, 2)", "clamp takes 3 values"},
		{"min(1)", "min takes two values or more"},
		{"1 +", "expected a number"},
		{"(1 + 2", "expected \")\""},
		{"1 2", "unexpected"},
		{"2 % 3", "is not part of a card's arithmetic"},
		{"tempo > 100", "is not part of a card's arithmetic"},
		{"1.2.3", "is not a number"},
		{"min(1, 2", "expected \",\" or \")\""},
	} {
		_, err := ParseExpr(tc.src, SongVars())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want one containing %q", tc.src, err, tc.want)
		}
	}
}

func TestExprEvalErrors(t *testing.T) {
	e, err := ParseExpr("1 / (tempo - 120)", SongVars())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Eval(map[string]float64{VarTempo: 120}); err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Errorf("division by zero: %v", err)
	}
	if _, err := e.Eval(nil); err == nil || !strings.Contains(err.Error(), "needs tempo") {
		t.Errorf("unknown tempo: %v", err)
	}
	e, err = ParseExpr("clamp(1, tempo, 2)", SongVars())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Eval(map[string]float64{VarTempo: 5}); err == nil || !strings.Contains(err.Error(), "low end") {
		t.Errorf("clamp with its ends crossed: %v", err)
	}
}

func TestExprVarsConstAndJSON(t *testing.T) {
	e, err := ParseExpr("clamp(0.5 * lowest_note_hz, 20, beat_ms) + lowest_note_hz", SongVars())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Vars(); !slices.Equal(got, []string{VarBeatMS, VarLowestNoteHz}) {
		t.Errorf("vars = %v", got)
	}
	if _, ok := e.Const(); ok {
		t.Error("an expression with names is not constant")
	}
	b, _ := json.Marshal(e)
	if string(b) != `"clamp(0.5 * lowest_note_hz, 20, beat_ms) + lowest_note_hz"` {
		t.Errorf("json = %s", b)
	}

	c, err := ParseExpr("2 * 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := c.Const(); !ok || v != 6 {
		t.Errorf("const = %g, %v", v, ok)
	}
	b, _ = json.Marshal(Number(-1))
	if string(b) != "-1" {
		t.Errorf("a number marshals as %s", b)
	}
	if _, err := ParseExpr("tempo", nil); err == nil || !strings.Contains(err.Error(), "write a number") {
		t.Errorf("a name where none is allowed: %v", err)
	}
}

func TestBounds(t *testing.T) {
	for src, want := range map[string][2]float64{
		"clamp(0.5 * lowest_note_hz, 20, 35)": {20, 35},
		"clamp(beat_ms / 2, 50, 400)":         {100, 400},
		"log2(clamp(lowest_note_hz, 16, 64))": {4, 6},
		"min(tempo, 100) - 10":                {10, 90},
		"-section.energy * 2":                 {-20, -2},
	} {
		e, err := ParseExpr(src, SongVars())
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		lo, hi, err := e.Bounds(SongSpans())
		if err != nil || math.Abs(lo-want[0]) > 1e-9 || math.Abs(hi-want[1]) > 1e-9 {
			t.Errorf("%s: %g..%g, %v; want %g..%g", src, lo, hi, err, want[0], want[1])
		}
	}
	e, _ := ParseExpr("100 / (section.energy - 5)", SongVars())
	if _, _, err := e.Bounds(SongSpans()); err == nil {
		t.Error("a division by a span that holds 0 has bounds")
	}
	if _, err := ParseExpr("log2(8)\n", nil); err != nil {
		t.Errorf("a trailing newline: %v", err)
	}
	if v, _ := mustExpr(t, "log2(8)").Const(); v != 3 {
		t.Errorf("log2(8) = %g", v)
	}
}

func mustExpr(t *testing.T, src string) *Expr {
	t.Helper()
	e, err := ParseExpr(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
