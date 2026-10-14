package formats

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// Commas formats an integer like Python's f"{value:,}" (e.g. "1,234,567").
func Commas[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64](value T) string {
	negative := value < 0
	var digits string
	if negative {
		digits = strconv.FormatUint(uint64(-int64(value)), 10)
	} else {
		digits = strconv.FormatUint(uint64(value), 10)
	}
	if len(digits) > 3 {
		var b strings.Builder
		head := len(digits) % 3
		if head > 0 {
			b.WriteString(digits[:head])
		}
		for i := head; i < len(digits); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(digits[i : i+3])
		}
		digits = b.String()
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// PythonFloat formats a float64 like Python's repr()/str() of a float:
// shortest round-trip digits, "1500.0", "1e-05", "1e+16", "nan", "inf".
func PythonFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
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

// pythonPrintable is str.isprintable for one character: everything except
// the Other (C*) and Separator (Z*) categories, ASCII space excepted.
func pythonPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zs, unicode.Zl, unicode.Zp) {
		return false
	}
	// Unassigned code points (Cn) are in no category.
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}

// PythonRepr formats a string like Python's repr(str), as used by {value!r}
// in error messages: single quotes unless the text contains ' and no ".
func PythonRepr(s string) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case pythonPrintable(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pythonSpace is str.isspace for one character.
func pythonSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	return r > 0x7f && (unicode.Is(unicode.Zs, r) || r == 0x2028 || r == 0x2029)
}

// PythonStrip is str.strip() with no arguments.
func PythonStrip(s string) string { return strings.TrimFunc(s, pythonSpace) }
