// Package jpath reads values out of decoded JSON (map[string]any / []any)
// with dot paths such as "data.items.0.name".
//
// Extras on top of plain dot paths:
//   - ""          the whole document
//   - "items.#"   length of an array, object or string
//   - "items.-1"  negative indexes count from the end
package jpath

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Get walks path into v.
func Get(v any, path string) (any, error) {
	if path == "" || path == "." {
		return v, nil
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		if part == "#" {
			switch x := cur.(type) {
			case []any:
				cur = float64(len(x))
			case map[string]any:
				cur = float64(len(x))
			case string:
				cur = float64(len([]rune(x)))
			default:
				return nil, fmt.Errorf("%s: # needs an array", path)
			}
			continue
		}
		switch x := cur.(type) {
		case map[string]any:
			nv, ok := x[part]
			if !ok {
				return nil, fmt.Errorf("%s: no %q", path, part)
			}
			cur = nv
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not an index", path, part)
			}
			if i < 0 {
				i += len(x)
			}
			if i < 0 || i >= len(x) {
				return nil, fmt.Errorf("%s: index %s out of range", path, part)
			}
			cur = x[i]
		default:
			return nil, fmt.Errorf("%s: cannot descend into %T at %q", path, cur, part)
		}
	}
	return cur, nil
}

// Num reads a number; numeric strings ("12.5") and booleans (1/0) are accepted.
func Num(v any, path string) (float64, error) {
	x, err := Get(v, path)
	if err != nil {
		return 0, err
	}
	if f, ok := ToNum(x); ok {
		return f, nil
	}
	return 0, fmt.Errorf("%s: %v is not a number", path, x)
}

// ToNum converts a decoded JSON scalar to a float.
func ToNum(x any) (float64, bool) {
	switch n := x.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		// "NaN" and "Inf" parse but cannot be encoded as JSON again.
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// Str reads a value as display text.
func Str(v any, path string) (string, error) {
	x, err := Get(v, path)
	if err != nil {
		return "", err
	}
	return ToStr(x), nil
}

// ToStr formats a decoded JSON value for display.
func ToStr(x any) string {
	switch s := x.(type) {
	case nil:
		return ""
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// Normalize round-trips any Go value through JSON so it can be walked
// with Get (structs become map[string]any).
func Normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
