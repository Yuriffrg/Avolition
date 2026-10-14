package formats

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"

	"avolition/gocore/internal/pypath"
)

// Reader decodes little-endian DataTool records (Python's _Reader). Every
// failure is a *FormatError whose text is "{name}, byte {offset}: {message}",
// with name the file's base name and offset the position after any bytes
// already consumed, exactly as in Python.
//
// A Reader is either in memory (NewReader, ReadFile: Take returns sub-slices
// of the buffer without copying) or streaming (NewStreamReader: Python's
// _ClipBindingReader and bone_counts._HeaderReader, which read structural
// fields and seek over bulk data). A Reader is not safe for concurrent use.
type Reader struct {
	// Path is the file the data came from; messages use its base name.
	Path string
	// Offset is the number of bytes consumed so far.
	Offset int

	name   string
	data   []byte
	size   int
	stream *streamSource
}

// streamReadThrough is the largest Skip served by reading instead of seeking.
const streamReadThrough = 128 * 1024

type streamSource struct {
	file   io.Reader
	seeker io.Seeker
	buffer *bufio.Reader
	opts   StreamOptions
}

// StreamOptions describe how a streaming Reader reports a short read, which
// differs between the Python subclasses.
type StreamOptions struct {
	// ShortReadMessage is the failure when the source ends before the declared
	// size (e.g. "truncated header", or "file changed or was truncated while
	// reading animation metadata").
	ShortReadMessage string
	// AdvanceOnShortRead counts the bytes actually read before failing
	// (_ClipBindingReader); otherwise Offset is left unchanged (_HeaderReader).
	AdvanceOnShortRead bool
}

// NewReader wraps in-memory data, like _Reader(path, data).
func NewReader(path string, data []byte) *Reader {
	return &Reader{Path: path, name: pypath.Name(path), data: data, size: len(data)}
}

// NewStreamReader reads from src, whose declared total size is size. Skip
// seeks when src implements io.Seeker.
func NewStreamReader(path string, src io.Reader, size int64, opts StreamOptions) *Reader {
	s := &streamSource{file: src, buffer: bufio.NewReaderSize(src, 32*1024), opts: opts}
	s.seeker, _ = src.(io.Seeker)
	return &Reader{Path: path, name: pypath.Name(path), size: int(size), stream: s}
}

// ReadFile is _Reader.file(path): one stat for the 512 MB limit, then one read.
func ReadFile(path string) (*Reader, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, PythonOSError(err)
	}
	if info.Size() > MaxFileBytes {
		return nil, formatError(pypath.Name(path) + ": file exceeds the 512 MB preview limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, PythonOSError(err)
	}
	return NewReader(path, data), nil
}

// Name is the base name used in messages.
func (r *Reader) Name() string { return r.name }

// Size is the total (declared) size of the data.
func (r *Reader) Size() int { return r.size }

// Data returns the whole in-memory buffer (nil for a streaming Reader).
func (r *Reader) Data() []byte { return r.data }

// Remaining is the number of unread bytes.
func (r *Reader) Remaining() int { return r.size - r.Offset }

// Fail returns the positioned FormatError for message (Python's fail()).
func (r *Reader) Fail(message string) error {
	return formatError(r.name + ", byte " + itoa(int64(r.Offset)) + ": " + message)
}

// Failf is Fail with fmt formatting.
func (r *Reader) Failf(format string, args ...any) error {
	return r.Fail(fmt.Sprintf(format, args...))
}

// Count validates 0 <= count <= maximum (Python's count()).
func (r *Reader) Count(count int64, maximum int64, label string) error {
	if count < 0 || count > maximum {
		return r.Fail("invalid " + label + ": " + Commas(count) + " (preview limit " + Commas(maximum) + ")")
	}
	return nil
}

func (r *Reader) truncated(size int) error {
	return r.Fail("truncated data (need " + Commas(size) + " bytes, have " + Commas(r.Remaining()) + ")")
}

// Take consumes size bytes. In memory the result aliases the buffer; callers
// must copy it before modifying it.
func (r *Reader) Take(size int) ([]byte, error) {
	if size < 0 || size > r.Remaining() {
		return nil, r.truncated(size)
	}
	if r.stream != nil {
		return r.stream.take(r, size)
	}
	start := r.Offset
	r.Offset += size
	return r.data[start:r.Offset:r.Offset], nil
}

func (s *streamSource) take(r *Reader, size int) ([]byte, error) {
	data := make([]byte, size)
	n, err := io.ReadFull(s.buffer, data)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, PythonOSError(err)
	}
	if n != size {
		if s.opts.AdvanceOnShortRead {
			r.Offset += n
		}
		return nil, r.Fail(s.opts.ShortReadMessage)
	}
	r.Offset += size
	return data, nil
}

// Skip consumes size bytes without decoding them; a streaming Reader seeks
// over them. It fails like Take when fewer bytes remain.
func (r *Reader) Skip(size int) error {
	if size < 0 || size > r.Remaining() {
		return r.truncated(size)
	}
	if s := r.stream; s != nil {
		buffered := s.buffer.Buffered()
		// Reading through short gaps is cheaper than a seek plus a refill.
		if size <= buffered || size <= streamReadThrough || s.seeker == nil {
			if _, err := s.buffer.Discard(size); err != nil && !errors.Is(err, io.EOF) {
				return PythonOSError(err)
			}
		} else {
			_, _ = s.buffer.Discard(buffered)
			if _, err := s.seeker.Seek(int64(size-buffered), io.SeekCurrent); err != nil {
				return PythonOSError(err)
			}
			s.buffer.Reset(s.file)
		}
	}
	r.Offset += size
	return nil
}

// U8 reads one unsigned byte ('B').
func (r *Reader) U8() (uint8, error) {
	b, err := r.Take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// U16 reads 'H'.
func (r *Reader) U16() (uint16, error) {
	b, err := r.Take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// I16 reads 'h'.
func (r *Reader) I16() (int16, error) {
	v, err := r.U16()
	return int16(v), err
}

// U32 reads 'I'.
func (r *Reader) U32() (uint32, error) {
	b, err := r.Take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// I32 reads 'i'.
func (r *Reader) I32() (int32, error) {
	v, err := r.U32()
	return int32(v), err
}

// U64 reads 'Q'.
func (r *Reader) U64() (uint64, error) {
	b, err := r.Take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// F32 reads one 'f' without a finiteness check (like unpack('f')).
func (r *Reader) F32() (float32, error) {
	v, err := r.U32()
	return math.Float32frombits(v), err
}

// Fields is the result of Unpack. Accessors convert like Python's struct
// module: integers by Int/Uint, 'f' and 'd' by Float (float32 widened exactly).
type Fields struct {
	kinds  []byte
	values []uint64
}

// Len is the number of fields.
func (f Fields) Len() int { return len(f.values) }

// Uint returns field i as an unsigned integer (raw bits for floats).
func (f Fields) Uint(i int) uint64 { return f.values[i] }

// Int returns field i as a signed integer, sign-extending b/h/i/q fields.
func (f Fields) Int(i int) int64 {
	v := f.values[i]
	switch f.kinds[i] {
	case 'b':
		return int64(int8(v))
	case 'h':
		return int64(int16(v))
	case 'i', 'l':
		return int64(int32(v))
	}
	return int64(v)
}

// Float returns field i as a float64 ('f' widened, 'd' as stored, integers converted).
func (f Fields) Float(i int) float64 {
	switch f.kinds[i] {
	case 'f':
		return float64(math.Float32frombits(uint32(f.values[i])))
	case 'd':
		return math.Float64frombits(f.values[i])
	case 'b', 'h', 'i', 'l', 'q':
		return float64(f.Int(i))
	}
	return float64(f.values[i])
}

func fieldSize(kind byte) int {
	switch kind {
	case 'B', 'b', 'x', '?':
		return 1
	case 'H', 'h':
		return 2
	case 'I', 'i', 'L', 'l', 'f':
		return 4
	case 'Q', 'q', 'd':
		return 8
	}
	panic("formats: unsupported struct format character " + string(kind))
}

// FormatSize is struct.calcsize('<' + format) for the supported characters
// (x ? B b H h I i L l Q q f d, with optional repeat counts such as "9f").
func FormatSize(format string) int {
	size, repeat := 0, 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c >= '0' && c <= '9' {
			repeat = repeat*10 + int(c-'0')
			continue
		}
		if repeat == 0 {
			repeat = 1
		}
		size += fieldSize(c) * repeat
		repeat = 0
	}
	return size
}

// Unpack is struct.unpack('<' + format, take(calcsize)): all fields are taken
// at once, so a truncation reports the whole record's size like Python.
// Padding bytes ('x') produce no field. Use the typed readers in hot loops.
func (r *Reader) Unpack(format string) (Fields, error) {
	data, err := r.Take(FormatSize(format))
	if err != nil {
		return Fields{}, err
	}
	var fields Fields
	offset, repeat := 0, 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c >= '0' && c <= '9' {
			repeat = repeat*10 + int(c-'0')
			continue
		}
		if repeat == 0 {
			repeat = 1
		}
		for ; repeat > 0; repeat-- {
			size := fieldSize(c)
			if c != 'x' {
				var v uint64
				switch size {
				case 1:
					v = uint64(data[offset])
				case 2:
					v = uint64(binary.LittleEndian.Uint16(data[offset:]))
				case 4:
					v = uint64(binary.LittleEndian.Uint32(data[offset:]))
				case 8:
					v = binary.LittleEndian.Uint64(data[offset:])
				}
				fields.kinds = append(fields.kinds, c)
				fields.values = append(fields.values, v)
			}
			offset += size
		}
	}
	return fields, nil
}

// String reads a .NET BinaryWriter string: a 7-bit encoded UTF-8 byte count
// (up to five bytes) and strict UTF-8. The literal "null" becomes "", as the
// DataTool writer uses it for absent references. Unlike the legacy reader
// this also handles exact 128-byte strings correctly.
func (r *Reader) String() (string, error) {
	length := 0
	terminated := false
	for shift := 0; shift < 35; shift += 7 {
		value, err := r.U8()
		if err != nil {
			return "", err
		}
		length |= int(value&0x7f) << shift
		if value&0x80 == 0 {
			terminated = true
			break
		}
	}
	if !terminated {
		return "", r.Fail("invalid string length")
	}
	if length > 1024*1024 {
		return "", r.Fail("string exceeds 1 MB")
	}
	raw, err := r.Take(length)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", r.Fail("invalid UTF-8 string")
	}
	if string(raw) == "null" {
		return "", nil
	}
	return string(raw), nil
}

func (r *Reader) arrayBytes(width, count int) ([]byte, error) {
	if count < 0 {
		return nil, r.Fail("negative array count")
	}
	if count > (math.MaxInt-1)/width {
		return nil, r.truncated(math.MaxInt)
	}
	return r.Take(width * count)
}

func finite32(bits uint32) bool { return bits&0x7f800000 != 0x7f800000 }

// Float32s is values('f', count): count floats, all of which must be finite
// ("non-finite numeric value", reported after the array was consumed).
func (r *Reader) Float32s(count int) ([]float32, error) {
	raw, err := r.arrayBytes(4, count)
	if err != nil {
		return nil, err
	}
	out := make([]float32, count)
	ok := true
	for i := range out {
		bits := binary.LittleEndian.Uint32(raw[i*4:])
		ok = ok && finite32(bits)
		out[i] = math.Float32frombits(bits)
	}
	if !ok {
		return nil, r.Fail("non-finite numeric value")
	}
	return out, nil
}

// Float64s is values('f', count) widened to float64 (Python floats).
func (r *Reader) Float64s(count int) ([]float64, error) {
	raw, err := r.arrayBytes(4, count)
	if err != nil {
		return nil, err
	}
	out := make([]float64, count)
	ok := true
	for i := range out {
		bits := binary.LittleEndian.Uint32(raw[i*4:])
		ok = ok && finite32(bits)
		out[i] = float64(math.Float32frombits(bits))
	}
	if !ok {
		return nil, r.Fail("non-finite numeric value")
	}
	return out, nil
}

// Uint16s is values('H', count).
func (r *Reader) Uint16s(count int) ([]uint16, error) {
	raw, err := r.arrayBytes(2, count)
	if err != nil {
		return nil, err
	}
	out := make([]uint16, count)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return out, nil
}

// Uint32s is values('I', count).
func (r *Reader) Uint32s(count int) ([]uint32, error) {
	raw, err := r.arrayBytes(4, count)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, count)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return out, nil
}

// yUpFloat32s is _to_y_up(values('f', count*3)) decoded in one pass:
// (x, y, z) becomes (x, z, -y).
func (r *Reader) yUpFloat32s(vertices int) ([]float32, error) {
	raw, err := r.arrayBytes(12, vertices)
	if err != nil {
		return nil, err
	}
	out := make([]float32, vertices*3)
	ok := true
	for i := 0; i < len(out); i += 3 {
		x := binary.LittleEndian.Uint32(raw[i*4:])
		y := binary.LittleEndian.Uint32(raw[i*4+4:])
		z := binary.LittleEndian.Uint32(raw[i*4+8:])
		ok = ok && finite32(x) && finite32(y) && finite32(z)
		out[i] = math.Float32frombits(x)
		out[i+1] = math.Float32frombits(z)
		out[i+2] = -math.Float32frombits(y)
	}
	if !ok {
		return nil, r.Fail("non-finite numeric value")
	}
	return out, nil
}

// CheckVersion is _version(): OW2 major 2, and minor 0 or 1 for entities.
func CheckVersion(r *Reader, major, minor uint16, kind string) error {
	if major != 2 {
		return r.Failf("unsupported %s version %d.%d; expected OW2 version 2.x", kind, major, minor)
	}
	if kind == "entity" && minor != 0 && minor != 1 {
		return r.Failf("unsupported entity version %d.%d; expected 2.0 or 2.1", major, minor)
	}
	return nil
}
