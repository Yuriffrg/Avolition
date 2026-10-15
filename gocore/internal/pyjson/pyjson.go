// Package pyjson writes JSON text byte for byte like Python's json.dumps, so
// files and digests the Python backend persists stay identical when Go
// produces them:
//
//	json.dumps(v, sort_keys=True, ensure_ascii=False)  Canonical / Digest
//	json.dumps(v, ensure_ascii=False, indent=2)         Indented
//	json.dumps(v, indent=2)                             Dumps(v, Options{Indent: 2, EnsureASCII: true})
//
// Go values are encoded directly (see encode.go): struct field order is the
// Python dict order, and the text uses Python's separators (", " and ": ",
// or "," plus newlines when indenting), Python's string escaping and
// Python's number formatting. Raw JSON (json.RawMessage, MarshalJSON output)
// is re-emitted the same way. Number literals are normalized the way
// json.loads followed by json.dumps would: a literal with a fraction or an
// exponent is a Python float and is written with float repr ("30.0",
// "1e-05", "1e+16"); any other literal is an int. Python floats held in Go
// structs should use Float, whose JSON form is already the Python repr, so a
// float with an integral value keeps its ".0" (encoding/json would write
// float64(30) as "30", which Python reads back as an int).
package pyjson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Float is a Python float: it marshals as Python's repr (shortest round-trip
// digits, always with a fraction or an exponent). Non-finite values cannot be
// marshaled (Python would write the non-standard NaN/Infinity tokens).
type Float float64

// MarshalJSON writes Python's repr of the value.
func (f Float) MarshalJSON() ([]byte, error) {
	value := float64(f)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("pyjson: unsupported value: non-finite float")
	}
	return []byte(FloatRepr(value)), nil
}

// FloatPtr returns a pointer to Float(value), for nullable float fields.
func FloatPtr(value float64) *Float {
	f := Float(value)
	return &f
}

// FloatRepr formats a finite float64 like Python's repr(float).
func FloatRepr(value float64) string {
	formatted := strconv.FormatFloat(value, 'e', -1, 64) // d.ddde±XX
	mantissa, exponentText, _ := strings.Cut(formatted, "e")
	exponent, _ := strconv.Atoi(exponentText)
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	if -4 <= exponent && exponent < 16 {
		var whole, fraction string
		if exponent >= 0 {
			if len(digits) <= exponent+1 {
				whole = digits + strings.Repeat("0", exponent+1-len(digits))
			} else {
				whole, fraction = digits[:exponent+1], digits[exponent+1:]
			}
		} else {
			whole, fraction = "0", strings.Repeat("0", -exponent-1)+digits
		}
		if fraction == "" {
			fraction = "0"
		}
		return sign + whole + "." + fraction
	}
	result := digits[:1]
	if len(digits) > 1 {
		result += "." + digits[1:]
	}
	expSign := "+"
	if exponent < 0 {
		expSign, exponent = "-", -exponent
	}
	return fmt.Sprintf("%s%se%s%02d", sign, result, expSign, exponent)
}

// Options select json.dumps arguments.
type Options struct {
	// Indent is Python's indent: < 0 means None (one line with ", " and
	// ": " separators); n >= 0 puts every member on its own line indented by
	// n spaces per level, with "," and ": " separators.
	Indent int
	// SortKeys is sort_keys=True.
	SortKeys bool
	// EnsureASCII is ensure_ascii=True: non-ASCII characters (and DEL) are
	// written as \uXXXX escapes.
	EnsureASCII bool
}

// Canonical is json.dumps(value, sort_keys=True, ensure_ascii=False).
func Canonical(value any) ([]byte, error) {
	return Dumps(value, Options{Indent: -1, SortKeys: true})
}

// Indented is json.dumps(value, ensure_ascii=False, indent=2).
func Indented(value any) ([]byte, error) {
	return Dumps(value, Options{Indent: 2})
}

// Digest is hashlib.sha256(json.dumps(value, sort_keys=True,
// ensure_ascii=False).encode('utf-8')).hexdigest().
func Digest(value any) (string, error) {
	text, err := Canonical(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:]), nil
}

// MustDigest is Digest for values that always marshal (strings, numbers and
// slices or structs of them); it panics on a marshaling error.
func MustDigest(value any) string {
	digest, err := Digest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

// Reformat re-emits a JSON document like json.dumps(json.loads(src), ...).
func Reformat(src []byte, opts Options) ([]byte, error) {
	e := emitter{src: src, opts: opts}
	e.space()
	out, err := e.value(make([]byte, 0, len(src)+len(src)/4), 0)
	if err != nil {
		return nil, err
	}
	e.space()
	if e.pos != len(src) {
		return nil, e.errorf("trailing data")
	}
	return out, nil
}

type emitter struct {
	src  []byte
	pos  int
	opts Options
}

func (e *emitter) errorf(format string, args ...any) error {
	return fmt.Errorf("pyjson: offset %d: %s", e.pos, fmt.Sprintf(format, args...))
}

func (e *emitter) space() {
	for e.pos < len(e.src) {
		switch e.src[e.pos] {
		case ' ', '\t', '\n', '\r':
			e.pos++
		default:
			return
		}
	}
}

func (e *emitter) newline(dst []byte, level int) []byte {
	dst = append(dst, '\n')
	for i := 0; i < level*e.opts.Indent; i++ {
		dst = append(dst, ' ')
	}
	return dst
}

// separator writes the item separator before a non-first member.
func (e *emitter) separator(dst []byte, level int, first bool) []byte {
	if e.opts.Indent < 0 {
		if !first {
			dst = append(dst, ',', ' ')
		}
		return dst
	}
	if !first {
		dst = append(dst, ',')
	}
	return e.newline(dst, level)
}

func (e *emitter) value(dst []byte, level int) ([]byte, error) {
	if e.pos >= len(e.src) {
		return nil, e.errorf("unexpected end of input")
	}
	switch c := e.src[e.pos]; {
	case c == '{':
		return e.object(dst, level)
	case c == '[':
		return e.array(dst, level)
	case c == '"':
		text, err := e.string()
		if err != nil {
			return nil, err
		}
		return e.appendString(dst, text), nil
	case c == 't' && bytes.HasPrefix(e.src[e.pos:], []byte("true")):
		e.pos += 4
		return append(dst, "true"...), nil
	case c == 'f' && bytes.HasPrefix(e.src[e.pos:], []byte("false")):
		e.pos += 5
		return append(dst, "false"...), nil
	case c == 'n' && bytes.HasPrefix(e.src[e.pos:], []byte("null")):
		e.pos += 4
		return append(dst, "null"...), nil
	case bytes.HasPrefix(e.src[e.pos:], []byte("Infinity")), bytes.HasPrefix(e.src[e.pos:], []byte("NaN")):
		// Python's json module reads and writes these non-standard tokens.
		token := "NaN"
		if c == 'I' {
			token = "Infinity"
		}
		e.pos += len(token)
		return append(dst, token...), nil
	case bytes.HasPrefix(e.src[e.pos:], []byte("-Infinity")):
		e.pos += len("-Infinity")
		return append(dst, "-Infinity"...), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return e.number(dst)
	}
	return nil, e.errorf("unexpected character %q", e.src[e.pos])
}

func (e *emitter) array(dst []byte, level int) ([]byte, error) {
	e.pos++ // [
	e.space()
	if e.pos < len(e.src) && e.src[e.pos] == ']' {
		e.pos++
		return append(dst, '[', ']'), nil
	}
	dst = append(dst, '[')
	for first := true; ; first = false {
		dst = e.separator(dst, level+1, first)
		var err error
		e.space()
		if dst, err = e.value(dst, level+1); err != nil {
			return nil, err
		}
		e.space()
		if e.pos >= len(e.src) {
			return nil, e.errorf("unterminated array")
		}
		if e.src[e.pos] == ',' {
			e.pos++
			continue
		}
		if e.src[e.pos] != ']' {
			return nil, e.errorf("expected , or ]")
		}
		e.pos++
		break
	}
	if e.opts.Indent >= 0 {
		dst = e.newline(dst, level)
	}
	return append(dst, ']'), nil
}

type member struct {
	key   string
	value []byte
}

func (e *emitter) object(dst []byte, level int) ([]byte, error) {
	e.pos++ // {
	e.space()
	if e.pos < len(e.src) && e.src[e.pos] == '}' {
		e.pos++
		return append(dst, '{', '}'), nil
	}
	var members []member
	if !e.opts.SortKeys {
		dst = append(dst, '{')
	}
	for first := true; ; first = false {
		e.space()
		if e.pos >= len(e.src) || e.src[e.pos] != '"' {
			return nil, e.errorf("expected object key")
		}
		key, err := e.string()
		if err != nil {
			return nil, err
		}
		e.space()
		if e.pos >= len(e.src) || e.src[e.pos] != ':' {
			return nil, e.errorf("expected :")
		}
		e.pos++
		e.space()
		if e.opts.SortKeys {
			value, err := e.value(nil, level+1)
			if err != nil {
				return nil, err
			}
			members = append(members, member{key, value})
		} else {
			dst = e.separator(dst, level+1, first)
			dst = e.appendString(dst, key)
			dst = append(dst, ':', ' ')
			if dst, err = e.value(dst, level+1); err != nil {
				return nil, err
			}
		}
		e.space()
		if e.pos >= len(e.src) {
			return nil, e.errorf("unterminated object")
		}
		if e.src[e.pos] == ',' {
			e.pos++
			continue
		}
		if e.src[e.pos] != '}' {
			return nil, e.errorf("expected , or }")
		}
		e.pos++
		break
	}
	if e.opts.SortKeys {
		// Python sorts str keys by code point, which is UTF-8 byte order.
		// A repeated key keeps its last value, like a dict.
		sort.SliceStable(members, func(i, j int) bool { return members[i].key < members[j].key })
		unique := members[:0]
		for i, m := range members {
			if i+1 < len(members) && members[i+1].key == m.key {
				continue
			}
			unique = append(unique, m)
		}
		dst = append(dst, '{')
		for i, m := range unique {
			dst = e.separator(dst, level+1, i == 0)
			dst = e.appendString(dst, m.key)
			dst = append(dst, ':', ' ')
			dst = append(dst, m.value...)
		}
	}
	if e.opts.Indent >= 0 {
		dst = e.newline(dst, level)
	}
	return append(dst, '}'), nil
}

func (e *emitter) number(dst []byte) ([]byte, error) {
	start := e.pos
	isFloat := false
	for e.pos < len(e.src) {
		c := e.src[e.pos]
		if c == '.' || c == 'e' || c == 'E' {
			isFloat = true
		} else if !(c == '-' || c == '+' || (c >= '0' && c <= '9')) {
			break
		}
		e.pos++
	}
	literal := string(e.src[start:e.pos])
	if isFloat {
		value, err := strconv.ParseFloat(literal, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, e.errorf("invalid number %q", literal)
		}
		switch {
		case math.IsInf(value, 1):
			return append(dst, "Infinity"...), nil
		case math.IsInf(value, -1):
			return append(dst, "-Infinity"...), nil
		}
		return append(dst, FloatRepr(value)...), nil
	}
	digits := strings.TrimPrefix(literal, "-")
	if digits == "" {
		return nil, e.errorf("invalid number %q", literal)
	}
	for _, c := range []byte(digits) {
		if c < '0' || c > '9' {
			return nil, e.errorf("invalid number %q", literal)
		}
	}
	if strings.TrimLeft(digits, "0") == "" {
		return append(dst, '0'), nil // int("-0") == 0
	}
	return append(dst, literal...), nil
}

// string decodes a JSON string literal at pos.
func (e *emitter) string() (string, error) {
	e.pos++ // "
	start := e.pos
	// Fast path: no escapes.
	for e.pos < len(e.src) {
		c := e.src[e.pos]
		if c == '"' {
			text := string(e.src[start:e.pos])
			e.pos++
			return text, nil
		}
		if c == '\\' {
			break
		}
		e.pos++
	}
	var b strings.Builder
	b.Write(e.src[start:e.pos])
	for e.pos < len(e.src) {
		c := e.src[e.pos]
		switch {
		case c == '"':
			e.pos++
			return b.String(), nil
		case c != '\\':
			b.WriteByte(c)
			e.pos++
			continue
		}
		if e.pos+1 >= len(e.src) {
			break
		}
		escape := e.src[e.pos+1]
		e.pos += 2
		switch escape {
		case '"', '\\', '/':
			b.WriteByte(escape)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, ok := e.hex4()
			if !ok {
				return "", e.errorf("invalid \\u escape")
			}
			if utf16.IsSurrogate(r) {
				// A lone surrogate cannot be encoded as UTF-8 (Python would
				// fail to encode it); write U+FFFD like encoding/json.
				if e.pos+1 < len(e.src) && e.src[e.pos] == '\\' && e.src[e.pos+1] == 'u' {
					save := e.pos
					e.pos += 2
					if low, ok := e.hex4(); ok {
						if combined := utf16.DecodeRune(r, low); combined != utf8.RuneError {
							b.WriteRune(combined)
							continue
						}
					}
					e.pos = save
				}
				r = utf8.RuneError
			}
			b.WriteRune(r)
		default:
			return "", e.errorf("invalid escape")
		}
	}
	return "", e.errorf("unterminated string")
}

func (e *emitter) hex4() (rune, bool) {
	if e.pos+4 > len(e.src) {
		return 0, false
	}
	value, err := strconv.ParseUint(string(e.src[e.pos:e.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	e.pos += 4
	return rune(value), true
}

const hexDigits = "0123456789abcdef"

// appendString writes s like Python's json encoder (lower-case \u escapes).
func (e *emitter) appendString(dst []byte, s string) []byte {
	return AppendString(dst, s, e.opts.EnsureASCII)
}

// AppendString appends s as a JSON string literal escaped like Python's
// json.dumps (ensure_ascii selects \uXXXX escapes for non-ASCII characters).
func AppendString(dst []byte, s string, ensureASCII bool) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch {
			case c == '"':
				dst = append(dst, '\\', '"')
			case c == '\\':
				dst = append(dst, '\\', '\\')
			case c == '\n':
				dst = append(dst, '\\', 'n')
			case c == '\r':
				dst = append(dst, '\\', 'r')
			case c == '\t':
				dst = append(dst, '\\', 't')
			case c == '\b':
				dst = append(dst, '\\', 'b')
			case c == '\f':
				dst = append(dst, '\\', 'f')
			case c < 0x20 || (ensureASCII && c == 0x7f):
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			default:
				dst = append(dst, c)
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if !ensureASCII {
			if r == utf8.RuneError && size == 1 {
				dst = append(dst, "�"...)
			} else {
				dst = utf8.AppendRune(dst, r)
			}
			continue
		}
		if r > 0xffff {
			high, low := utf16.EncodeRune(r)
			dst = appendU(dst, high)
			dst = appendU(dst, low)
		} else {
			dst = appendU(dst, r)
		}
	}
	return append(dst, '"')
}

func appendU(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u', hexDigits[r>>12&0xf], hexDigits[r>>8&0xf], hexDigits[r>>4&0xf], hexDigits[r&0xf])
}
