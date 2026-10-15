package libraryscan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// casefold mirrors Python's str.casefold() closely enough for comparisons
// against ASCII names: ASCII and simple folds map to lowercase, and the full
// foldings that expand to ASCII (ß, ligatures) are expanded.
func casefold(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	var builder strings.Builder
	builder.Grow(len(s))
	for _, r := range s {
		if expansion, ok := fullFolds[r]; ok {
			builder.WriteString(expansion)
			continue
		}
		builder.WriteRune(simpleFold(r))
	}
	return builder.String()
}

// fullFolds are CaseFolding.txt status F entries (Python uses full folding).
var fullFolds = map[rune]string{
	'ß': "ss", 'ẞ': "ss", 'İ': "i̇", 'ŉ': "ʼn", 'ǰ': "ǰ", 'ΐ': "ΐ", 'ΰ': "ΰ",
	'և': "եւ", 'ẖ': "ẖ", 'ẗ': "ẗ", 'ẘ': "ẘ", 'ẙ': "ẙ", 'ẚ': "aʾ",
	'ﬀ': "ff", 'ﬁ': "fi", 'ﬂ': "fl", 'ﬃ': "ffi", 'ﬄ': "ffl", 'ﬅ': "st", 'ﬆ': "st",
	'ﬓ': "մն", 'ﬔ': "մե", 'ﬕ': "մի", 'ﬖ': "վն", 'ﬗ': "մխ",
}

// simpleFold returns the lowercase member of r's case orbit (Unicode simple
// case folding maps to lowercase except for a few Cherokee letters).
func simpleFold(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}
	lower := unicode.ToLower(r)
	if lower != r {
		return lower
	}
	// Characters such as U+017F LONG S or U+212A KELVIN SIGN are lowercase or
	// uncased themselves but fold to an ASCII letter.
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < utf8.RuneSelf && 'a' <= f && f <= 'z' {
			return f
		}
	}
	return r
}

// pyLower mirrors Python's str.lower(): full lowercase mapping, including
// U+0130 and the Final_Sigma context of U+03A3.
func pyLower(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var builder strings.Builder
	builder.Grow(len(s))
	for i, r := range runes {
		switch r {
		case 'İ':
			builder.WriteString("i̇")
		case 'Σ':
			if finalSigma(runes, i) {
				builder.WriteRune('ς')
			} else {
				builder.WriteRune('σ')
			}
		default:
			builder.WriteRune(unicode.ToLower(r))
		}
	}
	return builder.String()
}

func finalSigma(runes []rune, at int) bool {
	before := false
	for i := at - 1; i >= 0; i-- {
		if caseIgnorable(runes[i]) {
			continue
		}
		before = cased(runes[i])
		break
	}
	if !before {
		return false
	}
	for i := at + 1; i < len(runes); i++ {
		if caseIgnorable(runes[i]) {
			continue
		}
		return !cased(runes[i])
	}
	return true
}

func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', '·', '‘', '’', '․', '﹒', '＇', '．', '­':
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}
