package c4dbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Float is a float64 that marshals like Python's repr(float) ("1.0",
// "1e-05", "1e+16"), so values written for the plugin are typed exactly as
// json.dumps would write them (a whole-number float stays a float).
type Float float64

func (f Float) MarshalJSON() ([]byte, error) {
	value := float64(f)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, outOfRange(value)
	}
	return appendPyFloat(nil, value), nil
}

func outOfRange(value float64) error {
	text := "nan"
	if math.IsInf(value, 1) {
		text = "inf"
	} else if math.IsInf(value, -1) {
		text = "-inf"
	}
	return errors.New("Out of range float values are not JSON compliant: " + text)
}

// appendPyFloat formats like Python's float.__repr__ (shortest round trip;
// scientific notation below 1e-4 and from 1e16).
func appendPyFloat(dst []byte, value float64) []byte {
	if value == 0 {
		if math.Signbit(value) {
			return append(dst, "-0.0"...)
		}
		return append(dst, "0.0"...)
	}
	text := strconv.FormatFloat(value, 'e', -1, 64) // -d.ddde±XX
	negative := text[0] == '-'
	if negative {
		text = text[1:]
		dst = append(dst, '-')
	}
	mark := strings.IndexByte(text, 'e')
	digits := strings.Replace(text[:mark], ".", "", 1)
	exponent, _ := strconv.Atoi(text[mark+1:])
	if -4 <= exponent && exponent < 16 {
		if exponent < 0 {
			dst = append(dst, "0."...)
			dst = append(dst, strings.Repeat("0", -exponent-1)...)
			return append(dst, digits...)
		}
		if len(digits) <= exponent+1 {
			dst = append(dst, digits...)
			dst = append(dst, strings.Repeat("0", exponent+1-len(digits))...)
			return append(dst, ".0"...)
		}
		dst = append(dst, digits[:exponent+1]...)
		dst = append(dst, '.')
		return append(dst, digits[exponent+1:]...)
	}
	dst = append(dst, digits[0])
	if len(digits) > 1 {
		dst = append(dst, '.')
		dst = append(dst, digits[1:]...)
	}
	dst = append(dst, 'e')
	if exponent < 0 {
		dst = append(dst, '-')
		exponent = -exponent
	} else {
		dst = append(dst, '+')
	}
	if exponent < 10 {
		dst = append(dst, '0')
	}
	return strconv.AppendInt(dst, int64(exponent), 10)
}

// Marshal encodes like json.dumps(value, ensure_ascii=False, allow_nan=False,
// separators=(',', ':')): no HTML escaping and raw U+2028/U+2029. Use Float
// for float64 values that Python would write as floats even when integral,
// and ordered.Map for dict-like values whose key order matters.
func Marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		var unsupported *json.UnsupportedValueError
		if errors.As(err, &unsupported) && unsupported.Value.CanFloat() {
			return nil, outOfRange(unsupported.Value.Float())
		}
		var marshaler *json.MarshalerError
		if errors.As(err, &marshaler) && strings.HasPrefix(marshaler.Unwrap().Error(), "Out of range float") {
			return nil, marshaler.Unwrap()
		}
		return nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return unescapeLineSeparators(data), nil
}

// unescapeLineSeparators turns Go's   and   escapes back into the
// raw characters Python writes with ensure_ascii=False.
func unescapeLineSeparators(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u202`)) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			out = append(out, data[i])
			continue
		}
		if i+5 < len(data) && data[i+1] == 'u' && string(data[i+2:i+5]) == "202" && (data[i+5] == '8' || data[i+5] == '9') {
			out = utf8.AppendRune(out, rune(0x2020+int(data[i+5]-'0')))
			i += 5
			continue
		}
		// Copy the escaped character verbatim so `\\u2028` stays literal.
		out = append(out, data[i], data[i+1])
		i++
	}
	return out
}

// withSuffix mirrors PurePath.with_suffix for a simple file name.
func withSuffix(path, suffix string) string {
	dir, name := filepath.Split(path)
	if i := strings.LastIndexByte(name, '.'); 0 < i && i < len(name)-1 {
		name = name[:i]
	}
	return dir + name + suffix
}

// WriteJSON writes value through a temporary file and an atomic replace, so
// the Cinema 4D plugin never reads a partial mailbox file.
func WriteJSON(path string, value any) error {
	var data []byte
	switch raw := value.(type) {
	case json.RawMessage:
		data = raw
	default:
		encoded, err := Marshal(value)
		if err != nil {
			return err
		}
		data = encoded
	}
	temporary := withSuffix(path, ".tmp")
	if err := os.WriteFile(temporary, data, 0o666); err != nil {
		return pyOSError(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return pyOSError(err)
	}
	return nil
}

// ErrTooLarge is read_json's size-limit error.
var ErrTooLarge = errors.New("Cinema 4D export request is too large.")

// ReadJSON reads a mailbox file of at most limit bytes and returns its JSON
// text after validating it.
func ReadJSON(path string, limit int64) (json.RawMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, pyOSError(err)
	}
	if info.Size() > limit {
		return nil, ErrTooLarge
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, pyOSError(err)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("'utf-8' codec can't decode %s", filepath.Base(path))
	}
	if !json.Valid(data) {
		var probe any
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(bytes.TrimSpace(data)), nil
}
