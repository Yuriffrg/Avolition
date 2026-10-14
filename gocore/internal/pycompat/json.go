package pycompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"avolition/gocore/internal/ordered"
)

// StripBOM removes a UTF-8 byte order mark, as Python's utf-8-sig codec does.
func StripBOM(data []byte) []byte { return bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) }

// Loads is json.loads: objects become *ordered.Map[any] (Python dict order,
// duplicate keys keep their first position and last value), numbers stay
// json.Number so ints and floats remain distinguishable.
func Loads(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, &ValueError{"invalid UTF-8 in JSON document"}
	}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return nil, &ValueError{"Unexpected UTF-8 BOM (decode using utf-8-sig)"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, &ValueError{err.Error()}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, &ValueError{"Extra data"}
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		if err == io.EOF {
			return nil, errors.New("Expecting value")
		}
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			object := ordered.New[any]()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("Expecting property name enclosed in double quotes")
				}
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Set(key, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			list := []any{}
			for decoder.More() {
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// Dumps is json.dumps(value, ensure_ascii=False, indent=indent) (indent < 0
// means no indent, with Python's default ", " and ": " separators). Struct
// values are encoded through encoding/json first, keeping field order.
func Dumps(value any, indent int) ([]byte, error) {
	var b bytes.Buffer
	if err := dump(&b, value, indent, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func newline(b *bytes.Buffer, indent, level int) {
	if indent >= 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", indent*level))
	}
}

func dump(b *bytes.Buffer, value any, indent, level int) error {
	itemSep := ", "
	if indent >= 0 {
		itemSep = ","
	}
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, v)
	case json.Number:
		if IsInt(v) {
			b.WriteString(Repr(v))
		} else {
			f, _ := strconv.ParseFloat(string(v), 64)
			b.WriteString(floatJSON(f))
		}
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float64:
		b.WriteString(floatJSON(v))
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			newline(b, indent, level+1)
			if err := dump(b, item, indent, level+1); err != nil {
				return err
			}
		}
		newline(b, indent, level)
		b.WriteByte(']')
	case []string:
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		return dump(b, list, indent, level)
	case *ordered.Map[any]:
		if v == nil {
			b.WriteString("null")
			return nil
		}
		return dumpObject(b, v, indent, level, itemSep)
	case map[string]any:
		return dumpObject(b, plainDict(v), indent, level, itemSep)
	default:
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			b.WriteString("null")
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		decoded, err := Loads(raw)
		if err != nil {
			return err
		}
		return dump(b, decoded, indent, level)
	}
	return nil
}

func dumpObject(b *bytes.Buffer, d Dict, indent, level int, itemSep string) error {
	keys := d.Keys()
	if len(keys) == 0 {
		b.WriteString("{}")
		return nil
	}
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(itemSep)
		}
		newline(b, indent, level+1)
		writeString(b, k)
		b.WriteString(": ")
		value, _ := d.Get(k)
		if err := dump(b, value, indent, level+1); err != nil {
			return err
		}
	}
	newline(b, indent, level)
	b.WriteByte('}')
	return nil
}

func floatJSON(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return ReprFloat(f)
}

// writeString escapes like Python's json with ensure_ascii=False.
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s { // invalid UTF-8 becomes U+FFFD
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Clone deep-copies a JSON value produced by Loads (copy.deepcopy).
func Clone(value any) any {
	switch v := value.(type) {
	case *ordered.Map[any]:
		if v == nil {
			return v
		}
		out := ordered.New[any]()
		v.Range(func(key string, item any) bool {
			out.Set(key, Clone(item))
			return true
		})
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Clone(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = Clone(v[k])
		}
		return out
	}
	return value
}
