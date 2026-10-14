package pycompat

import (
	"strings"
	"unicode/utf8"
)

// DecodeUTF8 is bytes.decode('utf-8', errors='replace'): each maximal invalid
// subsequence becomes one U+FFFD, exactly like CPython's decoder.
func DecodeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data))
	for i := 0; i < len(data); {
		c := data[i]
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		if r, size := utf8.DecodeRune(data[i:]); r != utf8.RuneError || size > 1 {
			b.Write(data[i : i+size])
			i += size
			continue
		}
		// Invalid: consume the lead byte plus every continuation byte that
		// still forms a valid prefix, then emit a single replacement.
		lo, hi, count := byte(0x80), byte(0xbf), 0
		switch {
		case c >= 0xc2 && c <= 0xdf:
			count = 1
		case c == 0xe0:
			lo, count = 0xa0, 2
		case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
			count = 2
		case c == 0xed:
			hi, count = 0x9f, 2
		case c == 0xf0:
			lo, count = 0x90, 3
		case c >= 0xf1 && c <= 0xf3:
			count = 3
		case c == 0xf4:
			hi, count = 0x8f, 3
		}
		j := i + 1
		for k := 0; k < count && j < len(data); k++ {
			if data[j] < lo || data[j] > hi {
				break
			}
			lo, hi = 0x80, 0xbf
			j++
		}
		b.WriteRune(utf8.RuneError)
		i = j
	}
	return b.String()
}
