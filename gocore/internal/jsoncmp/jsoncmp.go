// Package jsoncmp compares JSON documents structurally, with a tolerance for
// numbers. Go writes float32 data in its shortest float32 form while Python
// writes the float64 expansion; both decode to the same float32.
package jsoncmp

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

// Options control the comparison.
type Options struct {
	// RelTol and AbsTol bound numeric differences: |a-b| <= AbsTol + RelTol*max(|a|,|b|).
	RelTol, AbsTol float64
	// Ignore lists paths (as reported in differences, e.g. "$.meshes[0].name")
	// or path suffixes beginning with "." to skip.
	Ignore []string
	// MaxDiffs limits reported differences (default 20).
	MaxDiffs int
}

// Default tolerates float32 rounding.
var Default = Options{RelTol: 1e-6, AbsTol: 1e-6}

// Diff returns human-readable differences between two JSON documents.
func Diff(want, got []byte, opts Options) ([]string, error) {
	var a, b any
	if err := unmarshal(want, &a); err != nil {
		return nil, fmt.Errorf("want: %w", err)
	}
	if err := unmarshal(got, &b); err != nil {
		return nil, fmt.Errorf("got: %w", err)
	}
	if opts.MaxDiffs == 0 {
		opts.MaxDiffs = 20
	}
	var diffs []string
	compare("$", a, b, opts, &diffs)
	return diffs, nil
}

func unmarshal(data []byte, value *any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func ignored(path string, opts Options) bool {
	for _, pattern := range opts.Ignore {
		if path == pattern || (strings.HasPrefix(pattern, ".") && strings.HasSuffix(path, pattern)) {
			return true
		}
	}
	return false
}

func compare(path string, a, b any, opts Options, diffs *[]string) {
	if len(*diffs) >= opts.MaxDiffs || ignored(path, opts) {
		return
	}
	add := func(format string, args ...any) {
		*diffs = append(*diffs, path+": "+fmt.Sprintf(format, args...))
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			add("want object, got %s", short(b))
			return
		}
		keys := make([]string, 0, len(av)+len(bv))
		for k := range av {
			keys = append(keys, k)
		}
		for k := range bv {
			if _, seen := av[k]; !seen {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := path + "." + k
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case ignored(child, opts):
			case !inA:
				*diffs = append(*diffs, child+": unexpected key, got "+short(y))
			case !inB:
				*diffs = append(*diffs, child+": missing key, want "+short(x))
			default:
				compare(child, x, y, opts, diffs)
			}
			if len(*diffs) >= opts.MaxDiffs {
				return
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			add("want array, got %s", short(b))
			return
		}
		if len(av) != len(bv) {
			add("want %d items, got %d", len(av), len(bv))
			return
		}
		for i := range av {
			compare(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], opts, diffs)
		}
	case json.Number:
		bn, ok := b.(json.Number)
		if !ok {
			add("want %s, got %s", av, short(b))
			return
		}
		if av == bn {
			return
		}
		x, errA := av.Float64()
		y, errB := bn.Float64()
		if errA != nil || errB != nil {
			add("want %s, got %s", av, bn)
			return
		}
		if math.Abs(x-y) > opts.AbsTol+opts.RelTol*math.Max(math.Abs(x), math.Abs(y)) {
			add("want %s, got %s", av, bn)
		}
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) || (a == nil) != (b == nil) {
			add("want %s, got %s", short(a), short(b))
		}
	}
}

func short(value any) string {
	data, _ := json.Marshal(value)
	if len(data) > 120 {
		return string(data[:117]) + "..."
	}
	return string(data)
}

// Equal fails the test with the differences between want and got. got may be
// a Go value (marshalled first) or raw JSON bytes.
func Equal(t testing.TB, want json.RawMessage, got any, opts Options) {
	t.Helper()
	raw, ok := got.(json.RawMessage)
	if !ok {
		if bytesValue, isBytes := got.([]byte); isBytes {
			raw = bytesValue
		} else {
			var err error
			if raw, err = json.Marshal(got); err != nil {
				t.Fatalf("marshal: %v", err)
			}
		}
	}
	diffs, err := Diff(want, raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) > 0 {
		t.Errorf("differs from the Python reference:\n  %s", strings.Join(diffs, "\n  "))
	}
}
