package fbx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"sync"
)

// prop is one FBX property record. Scalars: I (int32), L (int64), D
// (float64), F (float32), C (bool). Strings: S and R (raw bytes). Arrays:
// d, f, i, l.
type prop struct {
	kind  byte
	str   []byte
	num   float64
	int   int64
	bool  bool
	f64   []float64
	i64   []int64
	array []byte // little-endian array payload, then (after prepare) its record
}

func sProp(value string) prop        { return prop{kind: 'S', str: []byte(value)} }
func bProp(kind byte, b []byte) prop { return prop{kind: kind, str: b} }
func iProp(value int64) prop         { return prop{kind: 'I', int: value} }
func lProp(value int64) prop         { return prop{kind: 'L', int: value} }
func dProp(value float64) prop       { return prop{kind: 'D', num: value} }
func cProp(value bool) prop          { return prop{kind: 'C', bool: value} }

// arrays: Python converts with np.asarray(value, dtype='<f8'|'<f4'|'<i4'|'<i8').
func doubles(values []float64) prop { return prop{kind: 'd', f64: values} }
func floats(values []float64) prop  { return prop{kind: 'f', f64: values} }
func ints(values []int64) prop      { return prop{kind: 'i', i64: values} }
func longs(values []int64) prop     { return prop{kind: 'l', i64: values} }

func (p *prop) isArray() bool {
	return p.kind == 'd' || p.kind == 'f' || p.kind == 'i' || p.kind == 'l'
}

func (p *prop) count() int {
	if p.kind == 'd' || p.kind == 'f' {
		return len(p.f64)
	}
	return len(p.i64)
}

func (p *prop) raw() []byte {
	n := p.count()
	switch p.kind {
	case 'd':
		out := make([]byte, 8*n)
		for i, v := range p.f64 {
			binary.LittleEndian.PutUint64(out[8*i:], math.Float64bits(v))
		}
		return out
	case 'f':
		out := make([]byte, 4*n)
		for i, v := range p.f64 {
			binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(float32(v)))
		}
		return out
	case 'i':
		out := make([]byte, 4*n)
		for i, v := range p.i64 {
			binary.LittleEndian.PutUint32(out[4*i:], uint32(int32(v)))
		}
		return out
	default:
		out := make([]byte, 8*n)
		for i, v := range p.i64 {
			binary.LittleEndian.PutUint64(out[8*i:], uint64(v))
		}
		return out
	}
}

// Compressor returns the zlib stream of raw. Python uses zlib.compress(raw, 1).
type Compressor func(raw []byte) []byte

// ZlibBestSpeed is the default Compressor: a zlib stream at level 1, like
// Python's zlib.compress(raw, 1). Its bytes differ from C zlib's (both are
// valid streams of the same data); see the package documentation.
func ZlibBestSpeed(raw []byte) []byte {
	var buffer bytes.Buffer
	buffer.Grow(len(raw)/2 + 64)
	// A flate writer holds ~1 MB of state; reuse them across the thousands
	// of small curve arrays.
	w := zlibWriters.Get().(*zlib.Writer)
	w.Reset(&buffer)
	w.Write(raw)
	w.Close()
	zlibWriters.Put(w)
	return buffer.Bytes()
}

var zlibWriters = sync.Pool{New: func() any {
	w, _ := zlib.NewWriterLevel(nil, zlib.BestSpeed)
	return w
}}

// prepare encodes an array property record: kind, count, encoding flag and
// byte length, then the (compressed when smaller) payload.
func (p *prop) prepare(compress Compressor) {
	raw := p.raw()
	compressed := compress(raw)
	encoding := uint32(0)
	if len(compressed) < len(raw) {
		encoding, raw = 1, compressed
	}
	record := make([]byte, 13, 13+len(raw))
	record[0] = p.kind
	binary.LittleEndian.PutUint32(record[1:], uint32(p.count()))
	binary.LittleEndian.PutUint32(record[5:], encoding)
	binary.LittleEndian.PutUint32(record[9:], uint32(len(raw)))
	p.array = append(record, raw...)
}

func (p *prop) appendTo(b []byte) []byte {
	switch p.kind {
	case 'S', 'R':
		b = append(b, p.kind)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(p.str)))
		return append(b, p.str...)
	case 'I':
		return binary.LittleEndian.AppendUint32(append(b, 'I'), uint32(int32(p.int)))
	case 'L':
		return binary.LittleEndian.AppendUint64(append(b, 'L'), uint64(p.int))
	case 'D':
		return binary.LittleEndian.AppendUint64(append(b, 'D'), math.Float64bits(p.num))
	case 'F':
		return binary.LittleEndian.AppendUint32(append(b, 'F'), math.Float32bits(float32(p.num)))
	case 'C':
		if p.bool {
			return append(b, 'C', 1)
		}
		return append(b, 'C', 0)
	}
	return append(b, p.array...)
}

// node is _Node: a named record with properties and children.
type node struct {
	name     string
	props    []prop
	children []*node
}

func newNode(name string, props ...prop) *node { return &node{name: name, props: props} }

func (n *node) add(name string, props ...prop) *node {
	child := newNode(name, props...)
	n.children = append(n.children, child)
	return child
}

var errTooLarge = errors.New("FBX 7.4 export exceeds its 4 GiB file limit.")

// encode appends the node at file offset len(b) (FBX 7.4: 32-bit offsets).
func (n *node) encode(b []byte) ([]byte, error) {
	start := len(b)
	b = append(b, make([]byte, 13)...)
	b = append(b, n.name...)
	propsStart := len(b)
	for i := range n.props {
		b = n.props[i].appendTo(b)
	}
	propsLength := len(b) - propsStart
	var err error
	for _, child := range n.children {
		if b, err = child.encode(b); err != nil {
			return nil, err
		}
	}
	if len(n.children) > 0 || n.name == "AnimationStack" || n.name == "AnimationLayer" {
		b = append(b, make([]byte, 13)...)
	}
	if int64(len(b)) >= 1<<32 {
		return nil, errTooLarge
	}
	binary.LittleEndian.PutUint32(b[start:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[start+4:], uint32(len(n.props)))
	binary.LittleEndian.PutUint32(b[start+8:], uint32(propsLength))
	b[start+12] = byte(len(n.name))
	return b, nil
}

// prepareArrays compresses every array property of the trees with a bounded
// worker pool. Output stays deterministic: each property owns its record.
func prepareArrays(roots []*node, compress Compressor) {
	var arrays []*prop
	var walk func(*node)
	walk = func(n *node) {
		for i := range n.props {
			if n.props[i].isArray() {
				arrays = append(arrays, &n.props[i])
			}
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	jobs := make(chan *prop)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				p.prepare(compress)
			}
		}()
	}
	for _, p := range arrays {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
}
