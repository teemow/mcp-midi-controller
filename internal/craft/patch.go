package craft

import (
	"bytes"
	"encoding"
	"encoding/json"
	"reflect"
)

// CleanPatch returns the merge patch a craft tool made, without the typed nils
// in it: every nil map, slice or pointer is left out, as if it had not been
// set.
//
// A merge patch removes a key whose value is null, and encoding/json writes a
// typed nil (a nil map[string]any, []string or *float64) as null too, so a
// tool that leaves a map out by not making it would remove what the song has
// there. The one removal is an untyped nil, a key set to nil in a map: it
// stays and is written as null. In a list a typed nil stays null, since a list
// is replaced whole. Maps with string keys and slices come back as
// map[string]any and []any. A struct, a map with other keys, or a value that
// marshals itself is written through encoding/json, and the nulls of the
// objects it gives are left out the same way: none of them is a removal.
func CleanPatch(patch map[string]any) (map[string]any, error) {
	if patch == nil {
		return map[string]any{}, nil
	}
	v, _, err := cleanValue(patch)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

// cleanValue returns v without its typed nils, and whether v is kept at all.
func cleanValue(v any) (any, bool, error) {
	if v == nil {
		return nil, true, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if rv.IsNil() {
			return nil, false, nil
		}
	}
	switch v.(type) {
	case json.Marshaler:
		return viaJSON(v)
	case encoding.TextMarshaler:
		return v, true, nil
	}
	switch rv.Kind() {
	case reflect.Pointer:
		return cleanValue(rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return viaJSON(v)
		}
		out := make(map[string]any, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			cv, keep, err := cleanValue(it.Value().Interface())
			if err != nil {
				return nil, false, err
			}
			if keep {
				out[it.Key().String()] = cv
			}
		}
		return out, true, nil
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			return v, true, nil // []byte: encoding/json writes it as a string
		}
		out := make([]any, rv.Len())
		for i := range rv.Len() {
			cv, _, err := cleanValue(rv.Index(i).Interface())
			if err != nil {
				return nil, false, err
			}
			out[i] = cv
		}
		return out, true, nil
	case reflect.Struct:
		return viaJSON(v)
	default:
		return v, true, nil
	}
}

// viaJSON returns v as encoding/json writes it, decoded again, with the nulls
// of its objects left out. A v written as null is not kept.
func viaJSON(v any) (any, bool, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, false, err
	}
	if out == nil {
		return nil, false, nil
	}
	return dropNulls(out), true, nil
}

func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			if c == nil {
				delete(t, k)
			} else {
				t[k] = dropNulls(c)
			}
		}
	case []any:
		for i, c := range t {
			t[i] = dropNulls(c)
		}
	}
	return v
}
