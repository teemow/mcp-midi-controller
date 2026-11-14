package craft

// The arithmetic of a card's values. A value is a number or a small
// expression: numbers, + - * /, parentheses, min, max and clamp, and the names
// the context allows (in a technique card what the song tells: tempo, beat_ms,
// bar_ms, lowest_note_hz, section.energy). It is parsed and checked when the
// card loads, so a card with a typo never reaches a rig. There is deliberately
// nothing more: no comparisons, no conditions, no other functions.

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Song values a technique card's values may use.
const (
	VarTempo         = "tempo"          // beats per minute
	VarBeatMS        = "beat_ms"        // one beat in milliseconds
	VarBarMS         = "bar_ms"         // one bar in milliseconds
	VarLowestNoteHz  = "lowest_note_hz" // the lowest note of the target, in Hz
	VarSectionEnergy = "section.energy" // the section's energy, 1 to 10
)

// SongVars are the names a technique card's values may use.
func SongVars() []string {
	return []string{VarTempo, VarBeatMS, VarBarMS, VarLowestNoteHz, VarSectionEnergy}
}

// Expr is a parsed card value.
type Expr struct {
	src  string
	root node
	vars []string
}

// ParseExpr parses src. allowed are the names it may use; any other name, and
// any function but min, max and clamp, is an error.
func ParseExpr(src string, allowed []string) (*Expr, error) {
	p := &exprParser{src: src, allowed: map[string]bool{}}
	for _, a := range allowed {
		p.allowed[a] = true
	}
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("empty value")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	root, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tokEOF {
		return nil, p.errorf("unexpected %s", p.tok)
	}
	e := &Expr{src: strings.TrimSpace(src), root: root}
	seen := map[string]bool{}
	root.collect(seen)
	for v := range seen {
		e.vars = append(e.vars, v)
	}
	sort.Strings(e.vars)
	return e, nil
}

// Number returns the expression of a plain number.
func Number(v float64) *Expr {
	return &Expr{src: strconv.FormatFloat(v, 'g', -1, 64), root: numNode(v)}
}

// String returns the value as it was written.
func (e *Expr) String() string { return e.src }

// Vars are the names the expression uses, sorted.
func (e *Expr) Vars() []string { return append([]string(nil), e.vars...) }

// Const returns the expression's value when it uses no names.
func (e *Expr) Const() (float64, bool) {
	if len(e.vars) > 0 {
		return 0, false
	}
	v, err := e.Eval(nil)
	return v, err == nil
}

// Eval computes the expression with the names' values from env. A name env
// does not hold, a division by zero or a result that is not a finite number is
// an error.
func (e *Expr) Eval(env map[string]float64) (float64, error) {
	v, err := e.root.eval(env)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s is not a finite number", e.src)
	}
	return v, nil
}

// MarshalJSON writes a constant as its number and anything else as written.
func (e *Expr) MarshalJSON() ([]byte, error) {
	if v, ok := e.Const(); ok && e.src == strconv.FormatFloat(v, 'g', -1, 64) {
		return []byte(e.src), nil
	}
	return []byte(strconv.Quote(e.src)), nil
}

// --- the tree -------------------------------------------------------------------

type node interface {
	eval(env map[string]float64) (float64, error)
	collect(names map[string]bool)
}

type numNode float64

func (n numNode) eval(map[string]float64) (float64, error) { return float64(n), nil }
func (numNode) collect(map[string]bool)                    {}

type varNode string

func (n varNode) eval(env map[string]float64) (float64, error) {
	v, ok := env[string(n)]
	if !ok {
		return 0, fmt.Errorf("needs %s, which is not known here", string(n))
	}
	return v, nil
}
func (n varNode) collect(names map[string]bool) { names[string(n)] = true }

type negNode struct{ x node }

func (n negNode) eval(env map[string]float64) (float64, error) {
	v, err := n.x.eval(env)
	return -v, err
}
func (n negNode) collect(names map[string]bool) { n.x.collect(names) }

type binNode struct {
	op   byte
	l, r node
}

func (n binNode) eval(env map[string]float64) (float64, error) {
	l, err := n.l.eval(env)
	if err != nil {
		return 0, err
	}
	r, err := n.r.eval(env)
	if err != nil {
		return 0, err
	}
	switch n.op {
	case '+':
		return l + r, nil
	case '-':
		return l - r, nil
	case '*':
		return l * r, nil
	default:
		if r == 0 {
			return 0, errors.New("division by zero")
		}
		return l / r, nil
	}
}

func (n binNode) collect(names map[string]bool) {
	n.l.collect(names)
	n.r.collect(names)
}

type callNode struct {
	fn   string
	args []node
}

func (n callNode) eval(env map[string]float64) (float64, error) {
	vals := make([]float64, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(env)
		if err != nil {
			return 0, err
		}
		vals[i] = v
	}
	switch n.fn {
	case "min":
		out := vals[0]
		for _, v := range vals[1:] {
			out = math.Min(out, v)
		}
		return out, nil
	case "max":
		out := vals[0]
		for _, v := range vals[1:] {
			out = math.Max(out, v)
		}
		return out, nil
	default: // clamp
		if vals[1] > vals[2] {
			return 0, fmt.Errorf("clamp's low end %g is above its high end %g", vals[1], vals[2])
		}
		return math.Min(math.Max(vals[0], vals[1]), vals[2]), nil
	}
}

func (n callNode) collect(names map[string]bool) {
	for _, a := range n.args {
		a.collect(names)
	}
}

// functions are the only ones a card may call, with the number of arguments
// each takes (-1: two or more).
var functions = map[string]int{"min": -1, "max": -1, "clamp": 3}

// --- the parser -----------------------------------------------------------------

type tokKind int

const (
	tokEOF tokKind = iota
	tokNum
	tokName
	tokOp // + - * / ( ) ,
)

type token struct {
	kind tokKind
	text string
	num  float64
	pos  int
}

func (t token) String() string {
	if t.kind == tokEOF {
		return "end"
	}
	return strconv.Quote(t.text)
}

type exprParser struct {
	src     string
	pos     int
	tok     token
	allowed map[string]bool
}

func (p *exprParser) errorf(format string, args ...any) error {
	return fmt.Errorf("%q at %d: %s", p.src, p.tok.pos+1, fmt.Sprintf(format, args...))
}

func isNameStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isNameChar(c byte) bool  { return isNameStart(c) || (c >= '0' && c <= '9') }
func isDigit(c byte) bool     { return c >= '0' && c <= '9' }

func (p *exprParser) next() error {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
	start := p.pos
	if p.pos >= len(p.src) {
		p.tok = token{kind: tokEOF, pos: start}
		return nil
	}
	c := p.src[p.pos]
	switch {
	case isDigit(c) || (c == '.' && p.pos+1 < len(p.src) && isDigit(p.src[p.pos+1])):
		for p.pos < len(p.src) && (isDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
			p.pos++
		}
		if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
			q := p.pos + 1
			if q < len(p.src) && (p.src[q] == '+' || p.src[q] == '-') {
				q++
			}
			if q < len(p.src) && isDigit(p.src[q]) {
				for q < len(p.src) && isDigit(p.src[q]) {
					q++
				}
				p.pos = q
			}
		}
		text := p.src[start:p.pos]
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			p.tok = token{kind: tokNum, text: text, pos: start}
			return p.errorf("%q is not a number", text)
		}
		p.tok = token{kind: tokNum, text: text, num: v, pos: start}
	case isNameStart(c):
		for p.pos < len(p.src) && (isNameChar(p.src[p.pos]) ||
			(p.src[p.pos] == '.' && p.pos+1 < len(p.src) && isNameStart(p.src[p.pos+1]))) {
			p.pos++
		}
		p.tok = token{kind: tokName, text: p.src[start:p.pos], pos: start}
	case strings.IndexByte("+-*/(),", c) >= 0:
		p.pos++
		p.tok = token{kind: tokOp, text: string(c), pos: start}
	default:
		p.tok = token{kind: tokOp, text: string(c), pos: start}
		return p.errorf("%q is not part of a card's arithmetic (numbers, + - * /, parentheses, min, max, clamp)", string(c))
	}
	return nil
}

func (p *exprParser) isOp(op string) bool { return p.tok.kind == tokOp && p.tok.text == op }

// expr := term (('+' | '-') term)*
func (p *exprParser) expr() (node, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.tok.text[0]
		if err := p.next(); err != nil {
			return nil, err
		}
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = binNode{op: op, l: l, r: r}
	}
	return l, nil
}

// term := unary (('*' | '/') unary)*
func (p *exprParser) term() (node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") {
		op := p.tok.text[0]
		if err := p.next(); err != nil {
			return nil, err
		}
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = binNode{op: op, l: l, r: r}
	}
	return l, nil
}

// unary := ('-' | '+') unary | primary
func (p *exprParser) unary() (node, error) {
	if p.isOp("-") || p.isOp("+") {
		neg := p.isOp("-")
		if err := p.next(); err != nil {
			return nil, err
		}
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		if neg {
			return negNode{x: x}, nil
		}
		return x, nil
	}
	return p.primary()
}

// primary := number | name | function '(' expr (',' expr)* ')' | '(' expr ')'
func (p *exprParser) primary() (node, error) {
	switch {
	case p.tok.kind == tokNum:
		v := p.tok.num
		return numNode(v), p.next()
	case p.isOp("("):
		if err := p.next(); err != nil {
			return nil, err
		}
		x, err := p.expr()
		if err != nil {
			return nil, err
		}
		if !p.isOp(")") {
			return nil, p.errorf("expected \")\", found %s", p.tok)
		}
		return x, p.next()
	case p.tok.kind == tokName:
		name := p.tok.text
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.isOp("(") {
			return p.call(name)
		}
		if !p.allowed[name] {
			return nil, p.unknownName(name)
		}
		return varNode(name), nil
	default:
		return nil, p.errorf("expected a number, a name or \"(\", found %s", p.tok)
	}
}

func (p *exprParser) call(name string) (node, error) {
	arity, ok := functions[name]
	if !ok {
		return nil, p.errorf("%s is not a function of a card's arithmetic (min, max, clamp)", name)
	}
	if err := p.next(); err != nil { // the "("
		return nil, err
	}
	var args []node
	for {
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.isOp(",") {
			if err := p.next(); err != nil {
				return nil, err
			}
			continue
		}
		if !p.isOp(")") {
			return nil, p.errorf("expected \",\" or \")\", found %s", p.tok)
		}
		break
	}
	switch {
	case arity < 0 && len(args) < 2:
		return nil, p.errorf("%s takes two values or more", name)
	case arity > 0 && len(args) != arity:
		return nil, p.errorf("%s takes %d values, not %d", name, arity, len(args))
	}
	return callNode{fn: name, args: args}, p.next()
}

func (p *exprParser) unknownName(name string) error {
	if len(p.allowed) == 0 {
		return fmt.Errorf("%q: %s is not known here; write a number", p.src, name)
	}
	names := make([]string, 0, len(p.allowed))
	for n := range p.allowed {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("%q: %s is not known here (known: %s)", p.src, name, strings.Join(names, ", "))
}
