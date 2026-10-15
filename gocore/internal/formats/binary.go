package formats

import (
	"encoding/binary"
	"math"
	"strconv"
	"unsafe"
)

// Binary replies. AppendAssetBinary and AppendClipBinary write the same JSON
// object as their JSON counterparts, except that each bulk numeric array is a
// placeholder {"$blob":N} for the Nth entry of Blobs: raw little-endian
// values that the transport sends after the JSON and the host turns back
// into a typed array of the blob's Type. Values are the ones JavaScript
// would see: positions/normals/uvs and skin weights as Float32Array (the
// renderer converts the JSON numbers to float32 anyway), clip values as
// Float64Array, indices as Uint16Array when every index fits, else
// Uint32Array. A non-finite float fails the encoding like the JSON encoder.

// Blob types, named after the JavaScript typed arrays they become.
const (
	BlobFloat32 = "f32"
	BlobFloat64 = "f64"
	BlobUint32  = "u32"
	BlobUint16  = "u16"
	BlobUint8   = "u8"
)

// Blob is one binary array of a reply.
type Blob struct {
	Type string
	Data []byte // little-endian; may alias the source slice
}

// Blobs collects the blobs of one reply in placeholder order.
type Blobs struct {
	Items []Blob
	err   error
}

// Err is the first encoding error (a non-finite float).
func (b *Blobs) Err() error { return b.err }

// Size is the total byte length of the blobs.
func (b *Blobs) Size() int {
	size := 0
	for _, item := range b.Items {
		size += len(item.Data)
	}
	return size
}

// Add appends a blob and returns the placeholder that references it.
func (b *Blobs) Add(dst []byte, kind string, data []byte) []byte {
	b.Items = append(b.Items, Blob{Type: kind, Data: data})
	dst = append(dst, `{"$blob":`...)
	dst = strconv.AppendInt(dst, int64(len(b.Items)-1), 10)
	return append(dst, '}')
}

var littleEndian = func() bool {
	probe := uint16(1)
	return *(*byte)(unsafe.Pointer(&probe)) == 1
}()

// rawBytes views a numeric slice as its in-memory bytes (no copy).
func rawBytes[T float32 | float64 | uint16 | uint32](values []T) []byte {
	if len(values) == 0 {
		return []byte{}
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(values))), len(values)*int(unsafe.Sizeof(values[0])))
}

// leBytes is rawBytes on little-endian machines and a little-endian copy
// elsewhere.
func leBytes[T float32 | float64 | uint16 | uint32](values []T) []byte {
	if littleEndian || len(values) == 0 {
		return rawBytes(values)
	}
	size := int(unsafe.Sizeof(values[0:1][0]))
	out := make([]byte, len(values)*size)
	for i, v := range values {
		switch x := any(v).(type) {
		case float32:
			binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
		case float64:
			binary.LittleEndian.PutUint64(out[i*8:], math.Float64bits(x))
		case uint16:
			binary.LittleEndian.PutUint16(out[i*2:], x)
		case uint32:
			binary.LittleEndian.PutUint32(out[i*4:], x)
		}
	}
	return out
}

func (b *Blobs) checkFinite(values []float64) {
	if b.err != nil {
		return
	}
	for _, v := range values {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			b.err = errNonFinite
			return
		}
	}
}

func (b *Blobs) appendFloat32s(dst []byte, values []float32) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	if b.err == nil {
		for _, v := range values {
			if v-v != 0 { // NaN or ±Inf
				b.err = errNonFinite
				break
			}
		}
	}
	return b.Add(dst, BlobFloat32, leBytes(values))
}

func (b *Blobs) appendFloat64s(dst []byte, values []float64) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	b.checkFinite(values)
	return b.Add(dst, BlobFloat64, leBytes(values))
}

// appendFloat64sAs32 sends float64 values as float32, the precision the
// renderer stores them in.
func (b *Blobs) appendFloat64sAs32(dst []byte, values []float64) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	b.checkFinite(values)
	narrow := make([]float32, len(values))
	for i, v := range values {
		narrow[i] = float32(v)
	}
	return b.Add(dst, BlobFloat32, leBytes(narrow))
}

func (b *Blobs) appendUint16s(dst []byte, values []uint16) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	return b.Add(dst, BlobUint16, leBytes(values))
}

func (b *Blobs) appendUint32s(dst []byte, values []uint32) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	return b.Add(dst, BlobUint32, leBytes(values))
}

// appendIndices sends an index buffer as Uint16Array when every index fits,
// halving the bytes of most meshes.
func (b *Blobs) appendIndices(dst []byte, values []uint32) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	for _, v := range values {
		if v > math.MaxUint16 {
			return b.Add(dst, BlobUint32, leBytes(values))
		}
	}
	narrow := make([]uint16, len(values))
	for i, v := range values {
		narrow[i] = uint16(v)
	}
	return b.Add(dst, BlobUint16, leBytes(narrow))
}
