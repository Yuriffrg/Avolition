package fbx_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"testing"
)

// record is one parsed FBX node (tests/test_fbx.py read_binary_fbx).
type record struct {
	Name     string
	Props    []any
	Children []*record
}

type layout struct {
	// arrays lists [start, end) byte ranges of every array payload, with its
	// kind and whether it is compressed.
	arrays []arrayRange
}

type arrayRange struct {
	start, end int
	kind       byte
	compressed bool
}

// readBinaryFBX reads serialized records independently of the writer for SDK
// schema checks. Arrays are decompressed.
func readBinaryFBX(t testing.TB, data []byte) ([]*record, *layout) {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("Kaydara FBX Binary  \x00\x1a\x00")) || binary.LittleEndian.Uint32(data[23:]) != 7400 {
		t.Fatal("not a binary FBX 7400 file")
	}
	info := &layout{}
	var element func(start int) (*record, int)
	element = func(start int) (*record, int) {
		end := int(binary.LittleEndian.Uint32(data[start:]))
		count := int(binary.LittleEndian.Uint32(data[start+4:]))
		length := int(binary.LittleEndian.Uint32(data[start+8:]))
		nameLength := int(data[start+12])
		if end == 0 {
			return nil, start + 13
		}
		cursor := start + 13
		r := &record{Name: string(data[cursor : cursor+nameLength])}
		cursor += nameLength
		propertiesEnd := cursor + length
		for i := 0; i < count; i++ {
			kind := data[cursor]
			cursor++
			switch kind {
			case 'S', 'R':
				size := int(binary.LittleEndian.Uint32(data[cursor:]))
				cursor += 4
				raw := data[cursor : cursor+size]
				cursor += size
				if kind == 'S' {
					r.Props = append(r.Props, string(raw))
				} else {
					r.Props = append(r.Props, append([]byte(nil), raw...))
				}
			case 'd', 'f', 'i', 'l':
				n := int(binary.LittleEndian.Uint32(data[cursor:]))
				encoding := binary.LittleEndian.Uint32(data[cursor+4:])
				byteCount := int(binary.LittleEndian.Uint32(data[cursor+8:]))
				cursor += 12
				raw := data[cursor : cursor+byteCount]
				info.arrays = append(info.arrays, arrayRange{cursor, cursor + byteCount, kind, encoding == 1})
				cursor += byteCount
				if encoding == 1 {
					reader, err := zlib.NewReader(bytes.NewReader(raw))
					if err != nil {
						t.Fatal(err)
					}
					if raw, err = io.ReadAll(reader); err != nil {
						t.Fatal(err)
					}
				}
				switch kind {
				case 'd':
					values := make([]float64, n)
					for k := range values {
						values[k] = math.Float64frombits(binary.LittleEndian.Uint64(raw[8*k:]))
					}
					r.Props = append(r.Props, values)
				case 'f':
					values := make([]float64, n)
					for k := range values {
						values[k] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*k:])))
					}
					r.Props = append(r.Props, values)
				case 'i':
					values := make([]int64, n)
					for k := range values {
						values[k] = int64(int32(binary.LittleEndian.Uint32(raw[4*k:])))
					}
					r.Props = append(r.Props, values)
				default:
					values := make([]int64, n)
					for k := range values {
						values[k] = int64(binary.LittleEndian.Uint64(raw[8*k:]))
					}
					r.Props = append(r.Props, values)
				}
			case 'I':
				r.Props = append(r.Props, int64(int32(binary.LittleEndian.Uint32(data[cursor:]))))
				cursor += 4
			case 'L':
				r.Props = append(r.Props, int64(binary.LittleEndian.Uint64(data[cursor:])))
				cursor += 8
			case 'D':
				r.Props = append(r.Props, math.Float64frombits(binary.LittleEndian.Uint64(data[cursor:])))
				cursor += 8
			case 'F':
				r.Props = append(r.Props, float64(math.Float32frombits(binary.LittleEndian.Uint32(data[cursor:]))))
				cursor += 4
			case 'C':
				r.Props = append(r.Props, data[cursor] != 0)
				cursor++
			default:
				t.Fatalf("unknown property kind %q", kind)
			}
		}
		if cursor != propertiesEnd {
			t.Fatalf("%s: property length mismatch", r.Name)
		}
		for cursor < end {
			child, next := element(cursor)
			cursor = next
			if child != nil {
				r.Children = append(r.Children, child)
			}
		}
		if cursor != end {
			t.Fatalf("%s: end offset mismatch", r.Name)
		}
		return r, end
	}
	var roots []*record
	cursor := 27
	for {
		value, next := element(cursor)
		cursor = next
		if value == nil {
			return roots, info
		}
		roots = append(roots, value)
	}
}

func child(r *record, name string) *record {
	for _, c := range r.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func properties(r *record) map[string][]any {
	out := map[string][]any{}
	for _, p := range child(r, "Properties70").Children {
		out[p.Props[0].(string)] = p.Props
	}
	return out
}

func byName(roots []*record) map[string]*record {
	out := map[string]*record{}
	for _, r := range roots {
		out[r.Name] = r
	}
	return out
}

// diffRecords compares two parsed trees; numbers within tol (relative and
// absolute) are equal. It returns human-readable differences.
func diffRecords(path string, a, b []*record, tol float64, out *[]string) {
	if len(a) != len(b) {
		*out = append(*out, fmt.Sprintf("%s: %d children, want %d", path, len(b), len(a)))
		return
	}
	for i := range a {
		p := fmt.Sprintf("%s/%s[%d]", path, a[i].Name, i)
		if a[i].Name != b[i].Name {
			*out = append(*out, fmt.Sprintf("%s: name %q, want %q", p, b[i].Name, a[i].Name))
			return
		}
		if len(a[i].Props) != len(b[i].Props) {
			*out = append(*out, fmt.Sprintf("%s: %d props, want %d", p, len(b[i].Props), len(a[i].Props)))
		} else {
			for k := range a[i].Props {
				if !equalProp(a[i].Props[k], b[i].Props[k], tol) {
					*out = append(*out, fmt.Sprintf("%s prop %d: %v, want %v", p, k, short(b[i].Props[k]), short(a[i].Props[k])))
				}
			}
		}
		if len(*out) > 20 {
			return
		}
		diffRecords(p, a[i].Children, b[i].Children, tol, out)
	}
}

func short(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 100 {
		return s[:100] + "..."
	}
	return s
}

func near(x, y, tol float64) bool {
	return x == y || math.Abs(x-y) <= tol*math.Max(1, math.Max(math.Abs(x), math.Abs(y)))
}

func equalProp(a, b any, tol float64) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && near(x, y, tol)
	case []float64:
		y, ok := b.([]float64)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !near(x[i], y[i], tol) {
				return false
			}
		}
		return true
	case []int64:
		y, ok := b.([]int64)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	}
	return a == b
}
