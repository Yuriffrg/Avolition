package pyjson

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The encoder walks Go values directly (no encoding/json round trip), so a
// large inventory streams to a file without holding two copies in memory.
// It follows encoding/json's rules for which fields exist (json tags, "-",
// omitempty, embedded struct promotion with the shallower-field rule, map
// keys sorted) and Python's rules for how they are written:
//
//   - float32/float64 (and Float) are Python floats: repr, e.g. "30.0";
//   - json.Number and integers are written as Python ints/floats would be
//     after json.loads (see Reformat);
//   - json.Marshaler values (including json.RawMessage) are re-emitted
//     through Reformat at the current indentation.

const flushSize = 64 * 1024

type encoder struct {
	buf  []byte
	w    io.Writer
	opts Options
	err  error
}

func (e *encoder) flush() {
	if e.w == nil || e.err != nil || len(e.buf) < flushSize {
		return
	}
	_, e.err = e.w.Write(e.buf)
	e.buf = e.buf[:0]
}

// Write streams json.dumps(value, ...) to w.
func Write(w io.Writer, value any, opts Options) error {
	e := &encoder{buf: make([]byte, 0, 256), w: w, opts: opts}
	if err := e.value(reflect.ValueOf(value), 0); err != nil {
		return err
	}
	if e.err != nil {
		return e.err
	}
	_, err := w.Write(e.buf)
	return err
}

// Dumps returns json.dumps(value, ...) for a Go value.
func Dumps(value any, opts Options) ([]byte, error) {
	// Collect fixed-size chunks and join them once: growing one slice to
	// hundreds of MB would copy and clear it repeatedly.
	var chunks chunkWriter
	if err := Write(&chunks, value, opts); err != nil {
		return nil, err
	}
	if len(chunks.parts) == 1 {
		return chunks.parts[0], nil
	}
	out := make([]byte, 0, chunks.size)
	for _, part := range chunks.parts {
		out = append(out, part...)
	}
	return out, nil
}

type chunkWriter struct {
	parts [][]byte
	size  int
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	c.parts = append(c.parts, append([]byte(nil), p...))
	c.size += len(p)
	return len(p), nil
}

var (
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	numberType        = reflect.TypeFor[json.Number]()
	floatType         = reflect.TypeFor[Float]()
)

func (e *encoder) separator(level int, first bool) {
	if e.opts.Indent < 0 {
		if !first {
			e.buf = append(e.buf, ',', ' ')
		}
		return
	}
	if !first {
		e.buf = append(e.buf, ',')
	}
	e.newline(level)
}

func (e *encoder) newline(level int) {
	e.buf = append(e.buf, '\n')
	for i := 0; i < level*e.opts.Indent; i++ {
		e.buf = append(e.buf, ' ')
	}
}

func (e *encoder) close(level int, c byte) {
	if e.opts.Indent >= 0 {
		e.newline(level)
	}
	e.buf = append(e.buf, c)
}

func (e *encoder) marshaler(m json.Marshaler, level int) error {
	raw, err := m.MarshalJSON()
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		raw = []byte("null")
	}
	em := emitter{src: raw, opts: e.opts}
	em.space()
	if e.buf, err = em.value(e.buf, level); err != nil {
		return err
	}
	return nil
}

func (e *encoder) value(v reflect.Value, level int) error {
	e.flush()
	if !v.IsValid() {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	t := v.Type()
	if t == numberType {
		text := v.String()
		if text == "" {
			text = "0"
		}
		em := emitter{src: []byte(text), opts: e.opts}
		var err error
		e.buf, err = em.number(e.buf)
		return err
	}
	if t == floatType { // Float.MarshalJSON without the round trip.
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("pyjson: unsupported value: non-finite float")
		}
		e.buf = append(e.buf, FloatRepr(f)...)
		return nil
	}
	if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface && t.Implements(marshalerType) {
		if t.Kind() == reflect.Slice && v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		return e.marshaler(v.Interface().(json.Marshaler), level)
	}
	if t.Kind() != reflect.Pointer && v.CanAddr() && reflect.PointerTo(t).Implements(marshalerType) {
		return e.marshaler(v.Addr().Interface().(json.Marshaler), level)
	}
	switch t.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if t.Implements(marshalerType) && !t.Elem().Implements(marshalerType) {
			return e.marshaler(v.Interface().(json.Marshaler), level)
		}
		return e.value(v.Elem(), level)
	case reflect.Interface:
		if v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		return e.value(v.Elem(), level)
	case reflect.Bool:
		if v.Bool() {
			e.buf = append(e.buf, "true"...)
		} else {
			e.buf = append(e.buf, "false"...)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buf = strconv.AppendInt(e.buf, v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.buf = strconv.AppendUint(e.buf, v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("pyjson: unsupported value: non-finite float")
		}
		e.buf = append(e.buf, FloatRepr(f)...)
	case reflect.String:
		e.buf = AppendString(e.buf, v.String(), e.opts.EnsureASCII)
	case reflect.Slice:
		if v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if t.Elem().Kind() == reflect.Uint8 && !reflect.PointerTo(t.Elem()).Implements(marshalerType) {
			e.buf = AppendString(e.buf, base64.StdEncoding.EncodeToString(v.Bytes()), e.opts.EnsureASCII)
			return nil
		}
		return e.array(v, level)
	case reflect.Array:
		return e.array(v, level)
	case reflect.Map:
		if v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		return e.mapValue(v, level)
	case reflect.Struct:
		return e.structValue(v, level)
	default:
		return errors.New("pyjson: unsupported type " + t.String())
	}
	return nil
}

func (e *encoder) array(v reflect.Value, level int) error {
	n := v.Len()
	if n == 0 {
		e.buf = append(e.buf, '[', ']')
		return nil
	}
	e.buf = append(e.buf, '[')
	for i := 0; i < n; i++ {
		e.separator(level+1, i == 0)
		if err := e.value(v.Index(i), level+1); err != nil {
			return err
		}
	}
	e.close(level, ']')
	return nil
}

func (e *encoder) mapValue(v reflect.Value, level int) error {
	if v.Len() == 0 {
		e.buf = append(e.buf, '{', '}')
		return nil
	}
	type entry struct {
		key   string
		value reflect.Value
	}
	entries := make([]entry, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		k := iter.Key()
		var key string
		switch {
		case k.Kind() == reflect.String:
			key = k.String()
		case k.Type().Implements(textMarshalerType):
			text, err := k.Interface().(encoding.TextMarshaler).MarshalText()
			if err != nil {
				return err
			}
			key = string(text)
		case k.CanInt():
			key = strconv.FormatInt(k.Int(), 10)
		case k.CanUint():
			key = strconv.FormatUint(k.Uint(), 10)
		default:
			return errors.New("pyjson: unsupported map key type " + k.Type().String())
		}
		entries = append(entries, entry{key, iter.Value()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	e.buf = append(e.buf, '{')
	for i, en := range entries {
		e.separator(level+1, i == 0)
		e.buf = AppendString(e.buf, en.key, e.opts.EnsureASCII)
		e.buf = append(e.buf, ':', ' ')
		if err := e.value(en.value, level+1); err != nil {
			return err
		}
	}
	e.close(level, '}')
	return nil
}

type field struct {
	name      string
	index     []int
	omitEmpty bool
	tagged    bool
}

var fieldCache sync.Map // reflect.Type → []field

func typeFields(t reflect.Type) []field {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.([]field)
	}
	var all []field
	var walk func(t reflect.Type, index []int)
	walk = func(t reflect.Type, index []int) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			fieldIndex := append(append([]int{}, index...), i)
			ft := sf.Type
			if sf.Anonymous {
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if name == "" && ft.Kind() == reflect.Struct {
					walk(ft, fieldIndex)
					continue
				}
				if !sf.IsExported() && ft.Kind() != reflect.Struct {
					continue
				}
			} else if !sf.IsExported() {
				continue
			}
			tagged := name != ""
			if name == "" {
				name = sf.Name
			}
			all = append(all, field{name: name, index: fieldIndex, omitEmpty: hasOption(opts, "omitempty"), tagged: tagged})
		}
	}
	walk(t, nil)
	// encoding/json's dominance rule: the shallowest field of a name wins;
	// among several at that depth a single tagged one wins, otherwise the
	// name is dropped.
	byName := map[string][]int{}
	for i, f := range all {
		byName[f.name] = append(byName[f.name], i)
	}
	var fields []field
	for i, f := range all {
		candidates := byName[f.name]
		minDepth := len(f.index)
		for _, c := range candidates {
			minDepth = min(minDepth, len(all[c].index))
		}
		var shallow, tagged []int
		for _, c := range candidates {
			if len(all[c].index) == minDepth {
				shallow = append(shallow, c)
				if all[c].tagged {
					tagged = append(tagged, c)
				}
			}
		}
		if (len(shallow) == 1 && shallow[0] == i) || (len(shallow) > 1 && len(tagged) == 1 && tagged[0] == i) {
			fields = append(fields, f)
		}
	}
	fieldCache.Store(t, fields)
	return fields
}

func hasOption(options, name string) bool {
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == name {
			return true
		}
	}
	return false
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

// fieldValue follows index through embedded pointers; ok is false when an
// embedded pointer is nil.
func fieldValue(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, true
}

func (e *encoder) structValue(v reflect.Value, level int) error {
	fields := typeFields(v.Type())
	if e.opts.SortKeys {
		sorted := append([]field{}, fields...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
		fields = sorted
	}
	first := true
	for _, f := range fields {
		fv, ok := fieldValue(v, f.index)
		if !ok || (f.omitEmpty && isEmpty(fv)) {
			continue
		}
		if first {
			e.buf = append(e.buf, '{')
		}
		e.separator(level+1, first)
		first = false
		e.buf = AppendString(e.buf, f.name, e.opts.EnsureASCII)
		e.buf = append(e.buf, ':', ' ')
		if err := e.value(fv, level+1); err != nil {
			return err
		}
	}
	if first {
		e.buf = append(e.buf, '{', '}')
		return nil
	}
	e.close(level, '}')
	return nil
}
