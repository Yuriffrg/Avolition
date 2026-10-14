// Package pycompat reproduces the small pieces of Python semantics that ported
// modules depend on for exact parity: str()/repr() of JSON values, truthiness
// and equality, str.strip/casefold/splitlines, json.loads/json.dumps (ordered,
// ensure_ascii=False) and pathlib/os.path behaviour (resolve, expanduser,
// expandvars, is_relative_to). Values follow the shapes produced by Loads:
// nil, bool, string, json.Number, []any and *ordered.Map[any].
package pycompat

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"avolition/gocore/internal/ordered"
)

// TypeError mirrors a Python TypeError raised by the reference code.
type TypeError struct{ Message string }

func (e *TypeError) Error() string { return e.Message }

// ValueError mirrors a Python ValueError raised by the reference code.
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

// IsSpace reports Python's str.isspace for one character.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Strip is Python's str.strip() with no arguments.
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// Casefold approximates Python's str.casefold: full lower-casing plus the
// common multi-character folds. It is used for sorting and duplicate checks.
func Casefold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 'ß', 'ẞ':
			b.WriteString("ss")
		case 'ς':
			b.WriteRune('σ')
		case 'ſ':
			b.WriteRune('s')
		case 'ﬀ':
			b.WriteString("ff")
		case 'ﬁ':
			b.WriteString("fi")
		case 'ﬂ':
			b.WriteString("fl")
		case 'ﬃ':
			b.WriteString("ffi")
		case 'ﬄ':
			b.WriteString("ffl")
		case 'ﬅ', 'ﬆ':
			b.WriteString("st")
		case 'İ':
			b.WriteString("i̇")
		case 'ᏸ', 'ᏹ', 'ᏺ', 'ᏻ', 'ᏼ', 'ᏽ':
			// Cherokee folds to the capital letters.
			b.WriteRune(r - 8)
		default:
			if r >= 0xab70 && r <= 0xabbf {
				b.WriteRune(r - 0xab70 + 0x13a0)
				continue
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// SplitLines is Python's str.splitlines() without keepends.
func SplitLines(s string) []string {
	var lines []string
	runes := []rune(s)
	start := 0
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, string(runes[start:i]))
			start = i + 1
		case '\r':
			lines = append(lines, string(runes[start:i]))
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// Dict is a read view of a Python dict: *ordered.Map[any] or map[string]any.
type Dict interface {
	Get(key string) (any, bool)
	Keys() []string
	Len() int
}

type plainDict map[string]any

func (d plainDict) Get(key string) (any, bool) { v, ok := d[key]; return v, ok }
func (d plainDict) Len() int                   { return len(d) }
func (d plainDict) Keys() []string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AsDict reports whether v is a dict and returns a view of it.
func AsDict(v any) (Dict, bool) {
	switch d := v.(type) {
	case *ordered.Map[any]:
		if d == nil {
			return nil, false
		}
		return d, true
	case map[string]any:
		if d == nil {
			return nil, false
		}
		return plainDict(d), true
	}
	return nil, false
}

// Get is dict.get(key) for any dict view (nil when absent or not a dict).
func Get(v any, key string) any {
	if d, ok := AsDict(v); ok {
		value, _ := d.Get(key)
		return value
	}
	return nil
}

// Has reports `key in v` for a dict.
func Has(v any, key string) bool {
	if d, ok := AsDict(v); ok {
		_, found := d.Get(key)
		return found
	}
	return false
}

// AsList returns v as a list when it is one.
func AsList(v any) ([]any, bool) {
	switch l := v.(type) {
	case []any:
		return l, true
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// TypeName is type(v).__name__ for JSON values.
func TypeName(v any) string {
	switch n := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if IsInt(n) {
			return "int"
		}
		return "float"
	case int, int64, int32, uint32, uint64:
		return "int"
	case float64, float32:
		return "float"
	}
	if _, ok := AsList(v); ok {
		return "list"
	}
	if _, ok := AsDict(v); ok {
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// Iterate is `for item in v`: dict keys, list items or string characters.
func Iterate(v any) ([]any, error) {
	if l, ok := AsList(v); ok {
		return l, nil
	}
	if d, ok := AsDict(v); ok {
		keys := d.Keys()
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = k
		}
		return out, nil
	}
	if s, ok := v.(string); ok {
		out := make([]any, 0, len(s))
		for _, r := range s {
			out = append(out, string(r))
		}
		return out, nil
	}
	return nil, &TypeError{fmt.Sprintf("'%s' object is not iterable", TypeName(v))}
}

// IsInt reports whether a JSON number literal decodes to a Python int.
func IsInt(n json.Number) bool { return !strings.ContainsAny(string(n), ".eE") }

// Int returns the integer value of a Python int (JSON integer literal).
func Int(v any) (*big.Int, bool) {
	switch n := v.(type) {
	case json.Number:
		if !IsInt(n) {
			return nil, false
		}
		value, ok := new(big.Int).SetString(string(n), 10)
		return value, ok
	case int:
		return big.NewInt(int64(n)), true
	case int64:
		return big.NewInt(n), true
	}
	return nil, false
}

// number returns a numeric view (Python bool is an int subclass).
func number(v any) (*big.Float, bool) {
	switch n := v.(type) {
	case bool:
		if n {
			return big.NewFloat(1), true
		}
		return big.NewFloat(0), true
	case json.Number:
		if IsInt(n) {
			i, ok := new(big.Int).SetString(string(n), 10)
			if !ok {
				return nil, false
			}
			return new(big.Float).SetPrec(2048).SetInt(i), true
		}
		f, err := strconv.ParseFloat(string(n), 64)
		if err != nil && !math.IsInf(f, 0) {
			return nil, false
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, false
		}
		return big.NewFloat(f), true
	case int:
		return new(big.Float).SetInt64(int64(n)), true
	case int64:
		return new(big.Float).SetInt64(n), true
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, false
		}
		return big.NewFloat(n), true
	}
	return nil, false
}

// Truthy is Python's bool(v).
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number, int, int64, float64:
		n, ok := number(x)
		return !ok || n.Sign() != 0
	}
	if l, ok := AsList(v); ok {
		return len(l) > 0
	}
	if d, ok := AsDict(v); ok {
		return d.Len() > 0
	}
	return true
}

// Equal is Python's == for JSON values (1 == 1.0 == True).
func Equal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x.Cmp(y) == 0
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	if x, ok := AsList(a); ok {
		y, ok := AsList(b)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	if x, ok := AsDict(a); ok {
		y, ok := AsDict(b)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.Keys() {
			yv, found := y.Get(k)
			xv, _ := x.Get(k)
			if !found || !Equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

// Str is Python's str(v).
func Str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return Repr(v)
}

// Repr is Python's repr(v) for JSON values.
func Repr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return reprString(x)
	case json.Number:
		if IsInt(x) {
			if i, ok := new(big.Int).SetString(string(x), 10); ok {
				return i.String()
			}
			return string(x)
		}
		f, _ := strconv.ParseFloat(string(x), 64)
		return ReprFloat(f)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return ReprFloat(x)
	}
	if l, ok := AsList(v); ok {
		parts := make([]string, len(l))
		for i, item := range l {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if d, ok := AsDict(v); ok {
		keys := d.Keys()
		parts := make([]string, len(keys))
		for i, k := range keys {
			value, _ := d.Get(k)
			parts[i] = reprString(k) + ": " + Repr(value)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// ReprFloat is Python's repr(float).
func ReprFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // -d.ddde±XX
	sign := ""
	if sci[0] == '-' {
		sign, sci = "-", sci[1:]
	}
	mantissa, expText, _ := strings.Cut(sci, "e")
	exponent, _ := strconv.Atoi(expText)
	digits := strings.Replace(mantissa, ".", "", 1)
	if exponent >= -4 && exponent < 16 {
		var out string
		if exponent < 0 {
			out = "0." + strings.Repeat("0", -exponent-1) + digits
		} else if len(digits) <= exponent+1 {
			out = digits + strings.Repeat("0", exponent+1-len(digits)) + ".0"
		} else {
			out = digits[:exponent+1] + "." + digits[exponent+1:]
		}
		return sign + out
	}
	out := digits[:1]
	if len(digits) > 1 {
		out += "." + digits[1:]
	}
	expSign := "+"
	if exponent < 0 {
		expSign, exponent = "-", -exponent
	}
	return fmt.Sprintf("%s%se%s%02d", sign, out, expSign, exponent)
}

func reprString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Go strings may hold bytes Python would have decoded with
			// surrogateescape; show them as such.
			fmt.Fprintf(&b, "\\udc%02x", s[i])
			i++
			continue
		}
		i += size
		switch {
		case r == quote || r == '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		case r < 0x7f:
			b.WriteRune(r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, "\\x%02x", r)
		case r <= 0xffff:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			fmt.Fprintf(&b, "\\U%08x", r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// Slice returns the first n code points of s (Python s[:n]).
func Slice(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
