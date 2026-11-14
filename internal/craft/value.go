package craft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Value is one value a card gives a parameter: a number or arithmetic for a
// numeric parameter, one of its labels for an enumerated one.
type Value struct {
	Expr  *Expr
	Label string
}

// String renders the value as a card writes it.
func (v Value) String() string {
	if v.Expr != nil {
		return v.Expr.String()
	}
	return v.Label
}

// MarshalJSON writes a constant as its number, anything else as a string.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.Expr != nil {
		return v.Expr.MarshalJSON()
	}
	return json.Marshal(v.Label)
}

// Const returns a numeric value that uses no song values.
func (v Value) Const() (float64, bool) {
	if v.Expr == nil {
		return 0, false
	}
	return v.Expr.Const()
}

// Eval computes a numeric value with the song's values and checks it against
// the parameter's range.
func (v Value) Eval(p Param, env map[string]float64) (float64, error) {
	if v.Expr == nil {
		return 0, fmt.Errorf("%s takes a label, not a number", p.Name)
	}
	x, err := v.Expr.Eval(env)
	if err != nil {
		return 0, err
	}
	if x < p.Min || x > p.Max {
		return 0, fmt.Errorf("%s comes to %g, outside %s's range %s", v.Expr, x, p.Name, p.Range())
	}
	return x, nil
}

// compileValue checks a value written in a card against the parameter it
// sets. allowed are the names its arithmetic may use.
func compileValue(raw any, p Param, allowed []string) (Value, error) {
	if p.Enumerated() {
		s, ok := raw.(string)
		if !ok {
			return Value{}, fmt.Errorf("%s takes one of %s, not %v", p.Name, strings.Join(p.Values, ", "), raw)
		}
		for _, l := range p.Values {
			if strings.EqualFold(strings.TrimSpace(s), l) {
				return Value{Label: l}, nil
			}
		}
		return Value{}, fmt.Errorf("%q is not a value of %s (%s)", s, p.Name, strings.Join(p.Values, ", "))
	}
	e, err := compileNumber(raw, allowed)
	if err != nil {
		return Value{}, err
	}
	if x, ok := e.Const(); ok && (x < p.Min || x > p.Max) {
		return Value{}, fmt.Errorf("%s is outside %s's range %s", e, p.Name, p.Range())
	}
	return Value{Expr: e}, nil
}

// compileNumber reads a number or the arithmetic of one.
func compileNumber(raw any, allowed []string) (*Expr, error) {
	switch x := raw.(type) {
	case int:
		return Number(float64(x)), nil
	case int64:
		return Number(float64(x)), nil
	case uint64:
		return Number(float64(x)), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%v is not a finite number", x)
		}
		return Number(x), nil
	case string:
		e, err := ParseExpr(x, allowed)
		if err != nil {
			return nil, err
		}
		if len(e.Vars()) == 0 {
			if _, err := e.Eval(nil); err != nil {
				return nil, fmt.Errorf("%q: %v", x, err)
			}
		}
		return e, nil
	case nil:
		return nil, errors.New("no value")
	default:
		return nil, fmt.Errorf("%v is not a number", raw)
	}
}

// scalarString renders a plain value (a number, a label, on or off) of a
// plugin's own parameter.
func scalarString(raw any) (string, error) {
	switch x := raw.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return "", errors.New("empty value")
		}
		return x, nil
	case bool:
		if x {
			return "on", nil
		}
		return "off", nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("%v is not a number or a label", raw)
	}
}

// decodeStrict decodes one YAML document into out and refuses fields out does
// not have, so a misspelt field is an error rather than a setting that is
// quietly ignored.
func decodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("is empty")
		}
		return err
	}
	var rest any
	if err := dec.Decode(&rest); err == nil {
		return errors.New("holds more than one card; write one card per file")
	}
	return nil
}

// problems collects what is wrong with a card, so one load reports all of it.
type problems []string

func (p *problems) addf(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New(strings.Join(p, "; "))
}
