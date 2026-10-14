package formats

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// json.Marshal of an Asset or Clip is correct but spends most of its time
// reflecting over millions of mesh/track numbers. AppendAssetJSON and
// AppendClipJSON produce the same JSON object (same keys and values; only the
// position of the bulk "meshes"/"bones"/"meshPrototypes" key differs, which
// JSON consumers ignore) several times faster by encoding numeric arrays
// directly. A compact map asset's mesh prototypes are encoded in parallel.

var errNonFinite = errors.New("json: unsupported value: non-finite number")

// appendFloat is encoding/json's float encoder (ES6 number formatting).
func appendFloat(b []byte, f float64, bits int) ([]byte, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errNonFinite
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	b = strconv.AppendFloat(b, f, format, -1, bits)
	if format == 'e' {
		// Clean up e-09 to e-9.
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b, nil
}

func appendFloats[T float32 | float64](b []byte, values []T, bits int) ([]byte, error) {
	if values == nil {
		return append(b, "null"...), nil
	}
	b = append(b, '[')
	var err error
	for i, v := range values {
		if i > 0 {
			b = append(b, ',')
		}
		if b, err = appendFloat(b, float64(v), bits); err != nil {
			return nil, err
		}
	}
	return append(b, ']'), nil
}

func appendUints[T uint16 | uint32](b []byte, values []T) []byte {
	if values == nil {
		return append(b, "null"...)
	}
	b = append(b, '[')
	for i, v := range values {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendUint(b, uint64(v), 10)
	}
	return append(b, ']')
}

func appendValue(b []byte, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(b, encoded...), nil
}

// appendMesh writes one Mesh exactly as json.Marshal would.
func appendMesh(b []byte, mesh *Mesh) ([]byte, error) {
	if mesh == nil {
		return append(b, "null"...), nil
	}
	var err error
	b = append(b, `{"name":`...)
	if b, err = appendValue(b, mesh.Name); err != nil {
		return nil, err
	}
	b = append(b, `,"materialKey":`...)
	if b, err = appendValue(b, mesh.MaterialKey); err != nil {
		return nil, err
	}
	for _, field := range [...]struct {
		key    string
		values []float32
	}{{"positions", mesh.Positions}, {"normals", mesh.Normals}, {"uvs", mesh.UVs}, {"uvs2", mesh.UVs2}} {
		b = append(b, `,"`...)
		b = append(b, field.key...)
		b = append(b, `":`...)
		if b, err = appendFloats(b, field.values, 32); err != nil {
			return nil, err
		}
	}
	b = append(b, `,"indices":`...)
	b = appendUints(b, mesh.Indices)
	b = append(b, `,"skinIndices":`...)
	b = appendUints(b, mesh.SkinIndices)
	b = append(b, `,"skinWeights":`...)
	if b, err = appendFloats(b, mesh.SkinWeights, 64); err != nil {
		return nil, err
	}
	if mesh.SourceMeshIndex != nil {
		b = append(b, `,"sourceMeshIndex":`...)
		b = strconv.AppendInt(b, int64(*mesh.SourceMeshIndex), 10)
	}
	return append(b, '}'), nil
}

// spliceKey appends `,"key":` + raw inside the JSON object encoded (which must
// end with '}'), or `"key":raw` when the object is empty.
func spliceKey(dst, encoded []byte, key string, raw func([]byte) ([]byte, error)) ([]byte, error) {
	encoded = encoded[:len(encoded)-1]
	dst = append(dst, encoded...)
	if len(encoded) > 1 {
		dst = append(dst, ',')
	}
	dst = append(dst, '"')
	dst = append(dst, key...)
	dst = append(dst, `":`...)
	dst, err := raw(dst)
	if err != nil {
		return nil, err
	}
	return append(dst, '}'), nil
}

func appendModel(b []byte, model *Model) ([]byte, error) {
	if model == nil || model.Meshes == nil {
		return appendValue(b, model)
	}
	shallow := *model
	shallow.Meshes = nil // omitzero: encoded separately below
	encoded, err := json.Marshal(&shallow)
	if err != nil {
		return nil, err
	}
	return spliceKey(b, encoded, "meshes", func(b []byte) ([]byte, error) {
		b = append(b, '[')
		var err error
		for i, mesh := range model.Meshes {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendMesh(b, mesh); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	})
}

func assetSize(asset *Asset) int {
	size := 4096
	for _, model := range asset.Models {
		if model == nil {
			continue
		}
		size += 2048 + 400*len(model.Bones) + meshesSize(model.Meshes)
	}
	if asset.MeshPrototypes != nil {
		asset.MeshPrototypes.Range(func(_ string, prototype *MeshPrototype) bool {
			if prototype != nil {
				size += meshesSize(prototype.Meshes)
			}
			return true
		})
	}
	return size + 400*len(asset.ExportHierarchy)
}

func meshesSize(meshes []*Mesh) int {
	size := 0
	for _, mesh := range meshes {
		if mesh != nil {
			size += 12*(len(mesh.Positions)+len(mesh.Normals)+len(mesh.UVs)+len(mesh.UVs2)) +
				7*len(mesh.Indices) + 2*len(mesh.SkinIndices) + 12*len(mesh.SkinWeights)
		}
	}
	return size
}

// AppendAssetJSON appends the JSON encoding of asset to dst (see above).
func AppendAssetJSON(dst []byte, asset *Asset) ([]byte, error) {
	if asset == nil {
		return append(dst, "null"...), nil
	}
	if dst == nil {
		dst = make([]byte, 0, assetSize(asset))
	}
	shallow := *asset
	shallow.Models = []*Model{}
	shallow.MeshPrototypes = nil // omitempty: encoded separately below
	encoded, err := json.Marshal(&shallow)
	if err != nil {
		return nil, err
	}
	// Name and Path are JSON strings, which cannot contain a raw quote, so the
	// first `"models":[]` is the top-level key.
	marker := []byte(`"models":[]`)
	at := bytes.Index(encoded, marker)
	if at < 0 || asset.Models == nil {
		return appendValue(dst, asset)
	}
	dst = append(dst, encoded[:at]...)
	dst = append(dst, `"models":[`...)
	for i, model := range asset.Models {
		if i > 0 {
			dst = append(dst, ',')
		}
		if dst, err = appendModel(dst, model); err != nil {
			return nil, err
		}
	}
	dst = append(dst, ']')
	dst = append(dst, encoded[at+len(marker):]...)
	if asset.MeshPrototypes == nil {
		return dst, nil
	}
	// Compact map assets: append "meshPrototypes" before the closing brace.
	// Prototypes hold nearly all of a map's numbers, so they are encoded in
	// parallel into separate buffers and joined in order.
	dst = append(dst[:len(dst)-1], `,"meshPrototypes":{`...)
	ids := asset.MeshPrototypes.Keys()
	parts := make([][]byte, len(ids))
	errs := make([]error, len(ids))
	encode := func(i int) {
		prototype, _ := asset.MeshPrototypes.Get(ids[i])
		parts[i], errs[i] = appendPrototype(nil, ids[i], prototype)
	}
	workers := min(runtime.NumCPU(), len(ids))
	if workers <= 1 {
		for i := range ids {
			encode(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := int(next.Add(1) - 1); i < len(ids); i = int(next.Add(1) - 1) {
					encode(i)
				}
			}()
		}
		wg.Wait()
	}
	for i, part := range parts {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, part...)
	}
	return append(dst, "}}"...), nil
}

// appendPrototype appends `"id":{"meshes":[...]}`.
func appendPrototype(dst []byte, id string, prototype *MeshPrototype) ([]byte, error) {
	if prototype != nil && cap(dst)-len(dst) < meshesSize(prototype.Meshes) {
		dst = append(make([]byte, 0, len(dst)+len(id)+meshesSize(prototype.Meshes)+64), dst...)
	}
	dst, err := appendValue(dst, id)
	if err != nil {
		return nil, err
	}
	dst = append(dst, ':')
	if prototype == nil || prototype.Meshes == nil {
		return appendValue(dst, prototype)
	}
	dst = append(dst, `{"meshes":[`...)
	for i, mesh := range prototype.Meshes {
		if i > 0 {
			dst = append(dst, ',')
		}
		if dst, err = appendMesh(dst, mesh); err != nil {
			return nil, err
		}
	}
	return append(dst, "]}"...), nil
}

// AppendClipJSON appends the JSON encoding of clip to dst.
func AppendClipJSON(dst []byte, clip *Clip) ([]byte, error) {
	if clip == nil {
		return append(dst, "null"...), nil
	}
	if dst == nil {
		size := 1024
		for _, bone := range clip.Bones {
			for _, track := range [...]*Track{bone.Positions, bone.Rotations, bone.Scales} {
				if track != nil {
					size += 6*len(track.Frames) + 20*len(track.Values)
				}
			}
		}
		dst = make([]byte, 0, size)
	}
	if clip.Bones == nil {
		return appendValue(dst, clip)
	}
	encoded, err := json.Marshal(struct {
		ClipHeader
		Warnings []string `json:"warnings"`
	}{clip.ClipHeader, clip.Warnings})
	if err != nil {
		return nil, err
	}
	return spliceKey(dst, encoded, "bones", func(b []byte) ([]byte, error) {
		b = append(b, '[')
		for i := range clip.Bones {
			bone := &clip.Bones[i]
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"name":`...)
			var err error
			if b, err = appendValue(b, bone.Name); err != nil {
				return nil, err
			}
			for _, field := range [...]struct {
				key   string
				track *Track
			}{{"positions", bone.Positions}, {"rotations", bone.Rotations}, {"scales", bone.Scales}} {
				if field.track == nil {
					continue
				}
				b = append(b, `,"`...)
				b = append(b, field.key...)
				b = append(b, `":{"frames":`...)
				b = appendUints(b, field.track.Frames)
				b = append(b, `,"values":`...)
				if b, err = appendFloats(b, field.track.Values, 64); err != nil {
					return nil, err
				}
				b = append(b, '}')
			}
			b = append(b, '}')
		}
		return append(b, ']'), nil
	})
}
