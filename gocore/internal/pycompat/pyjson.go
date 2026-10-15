package pycompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"avolition/gocore/internal/ordered"
)

// JSONDecodeError is json.JSONDecodeError (a ValueError): str() is
// "<msg>: line L column C (char N)" with positions in code points.
type JSONDecodeError struct {
	Msg              string
	Pos, Line, Col   int
}

func (e *JSONDecodeError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Line, e.Col, e.Pos)
}

// UnicodeDecodeError is the error of bytes.decode('utf-8') (a ValueError).
type UnicodeDecodeError struct{ Message string }

func (e *UnicodeDecodeError) Error() string { return e.Message }

// DecodeUTF8Strict is bytes.decode('utf-8') with Python's error text.
func DecodeUTF8Strict(data []byte) (string, error) {
	for i := 0; i < len(data); {
		c := data[i]
		if c < 0x80 {
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || size > 1 {
			i += size
			continue
		}
		// Classify like CPython's decoder.
		need := 0
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c >= 0xE0 && c <= 0xEF:
			need = 2
		case c >= 0xF0 && c <= 0xF4:
			need = 3
		default:
			return "", &UnicodeDecodeError{fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid start byte", c, i)}
		}
		valid := 0
		for k := 1; k <= need && i+k < len(data); k++ {
			b := data[i+k]
			low, high := byte(0x80), byte(0xBF)
			if k == 1 {
				switch c {
				case 0xE0:
					low = 0xA0
				case 0xED:
					high = 0x9F
				case 0xF0:
					low = 0x90
				case 0xF4:
					high = 0x8F
				}
			}
			if b < low || b > high {
				break
			}
			valid++
		}
		if i+valid+1 >= len(data) && valid < need {
			if valid == 0 {
				return "", &UnicodeDecodeError{fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: unexpected end of data", c, i)}
			}
			return "", &UnicodeDecodeError{fmt.Sprintf("'utf-8' codec can't decode bytes in position %d-%d: unexpected end of data", i, i+valid)}
		}
		if valid == 0 {
			return "", &UnicodeDecodeError{fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid continuation byte", c, i)}
		}
		return "", &UnicodeDecodeError{fmt.Sprintf("'utf-8' codec can't decode bytes in position %d-%d: invalid continuation byte", i, i+valid)}
	}
	return string(data), nil
}

// UniversalNewlines is text-mode reading: "\r\n" and "\r" become "\n".
func UniversalNewlines(text string) string {
	if !strings.Contains(text, "\r") {
		return text
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// LoadsText is json.loads(text) with CPython's exact error messages. Values
// have the same shapes as Loads: objects are *ordered.Map[any] (duplicate
// keys keep the first position and the last value), numbers json.Number.
// NaN and ±Infinity are accepted like Python and kept as json.Number
// ("NaN", "Infinity", "-Infinity").
func LoadsText(text string) (any, error) {
	s := []rune(text)
	p := &pyParser{s: s}
	if len(s) > 0 && s[0] == '\ufeff' {
		return nil, p.fail("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	value, end, err := p.scan(p.ws(0))
	if err != nil {
		return nil, err
	}
	end = p.ws(end)
	if end != len(s) {
		return nil, p.fail("Extra data", end)
	}
	return value, nil
}

type pyParser struct{ s []rune }

// stopIteration marks scan_once's StopIteration (no value at idx).
type stopIteration struct{ idx int }

func (stopIteration) Error() string { return "StopIteration" }

func (p *pyParser) fail(msg string, pos int) error {
	line := 1
	last := -1
	for i := 0; i < pos && i < len(p.s); i++ {
		if p.s[i] == '\n' {
			line++
			last = i
		}
	}
	return &JSONDecodeError{Msg: msg, Pos: pos, Line: line, Col: pos - last}
}

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (p *pyParser) ws(i int) int {
	if i > len(p.s) {
		i = len(p.s)
	}
	for i < len(p.s) && isWS(p.s[i]) {
		i++
	}
	return i
}

func (p *pyParser) at(i int) rune {
	if i < 0 || i >= len(p.s) {
		return 0
	}
	return p.s[i]
}

func (p *pyParser) has(i int, word string) bool {
	for k, r := range []rune(word) {
		if p.at(i+k) != r || i+k >= len(p.s) {
			return false
		}
	}
	return true
}

func (p *pyParser) scan(idx int) (any, int, error) {
	value, end, err := p.scanOnce(idx)
	if stop, ok := err.(stopIteration); ok {
		return nil, 0, p.fail("Expecting value", stop.idx)
	}
	return value, end, err
}

func (p *pyParser) scanOnce(idx int) (any, int, error) {
	if idx >= len(p.s) {
		return nil, 0, stopIteration{idx}
	}
	switch c := p.s[idx]; {
	case c == '"':
		return p.str(idx + 1)
	case c == '{':
		return p.object(idx + 1)
	case c == '[':
		return p.array(idx + 1)
	case c == 'n' && p.has(idx, "null"):
		return nil, idx + 4, nil
	case c == 't' && p.has(idx, "true"):
		return true, idx + 4, nil
	case c == 'f' && p.has(idx, "false"):
		return false, idx + 5, nil
	case c == 'N' && p.has(idx, "NaN"):
		return json.Number("NaN"), idx + 3, nil
	case c == 'I' && p.has(idx, "Infinity"):
		return json.Number("Infinity"), idx + 8, nil
	case c == '-' && p.has(idx, "-Infinity"):
		return json.Number("-Infinity"), idx + 9, nil
	}
	return p.number(idx)
}

func digit(r rune) bool { return r >= '0' && r <= '9' }

func (p *pyParser) number(idx int) (any, int, error) {
	i := idx
	if p.at(i) == '-' && i < len(p.s) {
		i++
	}
	switch {
	case i < len(p.s) && p.s[i] == '0':
		i++
	case i < len(p.s) && p.s[i] >= '1' && p.s[i] <= '9':
		for i < len(p.s) && digit(p.s[i]) {
			i++
		}
	default:
		return nil, 0, stopIteration{idx}
	}
	if i+1 < len(p.s) && p.s[i] == '.' && digit(p.s[i+1]) {
		i += 2
		for i < len(p.s) && digit(p.s[i]) {
			i++
		}
	}
	if i < len(p.s) && (p.s[i] == 'e' || p.s[i] == 'E') {
		j := i + 1
		if j < len(p.s) && (p.s[j] == '-' || p.s[j] == '+') {
			j++
		}
		if j < len(p.s) && digit(p.s[j]) {
			for j < len(p.s) && digit(p.s[j]) {
				j++
			}
			i = j
		}
	}
	return json.Number(string(p.s[idx:i])), i, nil
}

func hexValue(r rune) (rune, bool) {
	switch {
	case r >= '0' && r <= '9':
		return r - '0', true
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10, true
	case r >= 'A' && r <= 'F':
		return r - 'A' + 10, true
	}
	return 0, false
}

// str is scanstring(s, end) with end just after the opening quote.
func (p *pyParser) str(end int) (any, int, error) {
	begin := end - 1
	var b strings.Builder
	n := len(p.s)
	for {
		next := end
		var c rune
		for ; next < n; next++ {
			c = p.s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return nil, 0, p.fail("Invalid control character at", next)
			}
		}
		if next >= n {
			return nil, 0, p.fail("Unterminated string starting at", begin)
		}
		b.WriteString(string(p.s[end:next]))
		if c == '"' {
			return b.String(), next + 1, nil
		}
		next++
		if next == n {
			return nil, 0, p.fail("Unterminated string starting at", begin)
		}
		c = p.s[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			default:
				return nil, 0, p.fail("Invalid \\escape", end-2)
			}
			b.WriteRune(c)
			continue
		}
		next++
		end = next + 4
		if end >= n {
			return nil, 0, p.fail("Invalid \\uXXXX escape", next-1)
		}
		c = 0
		for ; next < end; next++ {
			h, ok := hexValue(p.s[next])
			if !ok {
				return nil, 0, p.fail("Invalid \\uXXXX escape", end-5)
			}
			c = c<<4 | h
		}
		if c >= 0xD800 && c <= 0xDBFF && end+6 < n && p.s[next] == '\\' && p.s[next+1] == 'u' {
			next += 2
			end += 6
			var c2 rune
			for ; next < end; next++ {
				h, ok := hexValue(p.s[next])
				if !ok {
					return nil, 0, p.fail("Invalid \\uXXXX escape", end-5)
				}
				c2 = c2<<4 | h
			}
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				c = 0x10000 + (c-0xD800)<<10 + (c2 - 0xDC00)
			} else {
				end -= 6
			}
		}
		// Lone surrogates cannot be represented in a Go string.
		b.WriteRune(c)
	}
}

func (p *pyParser) object(idx int) (any, int, error) {
	pairs := ordered.New[any]()
	idx = p.ws(idx)
	if idx < len(p.s) && p.s[idx] == '}' {
		return pairs, idx + 1, nil
	}
	for {
		if idx >= len(p.s) || p.s[idx] != '"' {
			return nil, 0, p.fail("Expecting property name enclosed in double quotes", idx)
		}
		keyValue, next, err := p.str(idx + 1)
		if err != nil {
			return nil, 0, err
		}
		idx = p.ws(next)
		if idx >= len(p.s) || p.s[idx] != ':' {
			return nil, 0, p.fail("Expecting ':' delimiter", idx)
		}
		idx = p.ws(idx + 1)
		value, next, err := p.scan(idx)
		if err != nil {
			return nil, 0, err
		}
		// Python dict: a repeated key keeps its first position, last value.
		pairs.Set(keyValue.(string), value)
		idx = p.ws(next)
		if idx < len(p.s) && p.s[idx] == '}' {
			return pairs, idx + 1, nil
		}
		if idx >= len(p.s) || p.s[idx] != ',' {
			return nil, 0, p.fail("Expecting ',' delimiter", idx)
		}
		comma := idx
		idx = p.ws(idx + 1)
		if idx < len(p.s) && p.s[idx] == '}' {
			return nil, 0, p.fail("Illegal trailing comma before end of object", comma)
		}
	}
}

func (p *pyParser) array(idx int) (any, int, error) {
	values := []any{}
	idx = p.ws(idx)
	if idx < len(p.s) && p.s[idx] == ']' {
		return values, idx + 1, nil
	}
	for {
		value, next, err := p.scan(idx)
		if err != nil {
			return nil, 0, err
		}
		values = append(values, value)
		idx = p.ws(next)
		if idx < len(p.s) && p.s[idx] == ']' {
			return values, idx + 1, nil
		}
		if idx >= len(p.s) || p.s[idx] != ',' {
			return nil, 0, p.fail("Expecting ',' delimiter", idx)
		}
		comma := idx
		idx = p.ws(idx + 1)
		if idx < len(p.s) && p.s[idx] == ']' {
			return nil, 0, p.fail("Illegal trailing comma before end of array", comma)
		}
	}
}
