package formats

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"

	"avolition/gocore/internal/ordered"
)

func binaryFixture() *Asset {
	big := make([]uint32, 6)
	for i := range big {
		big[i] = uint32(70000 + i)
	}
	index := 2
	mesh := &Mesh{Name: "a", MaterialKey: "m", Positions: []float32{0.1, -2, 3.5e-7, 1, 2, 3},
		Normals: []float32{0, 1, 0, 0, 1, 0}, UVs: []float32{0.25, 0.5, 1, 1}, UVs2: nil,
		Indices: []uint32{0, 1, 1}, SkinIndices: []uint16{1, 2, 3, 4, 5, 6, 7, 8},
		SkinWeights: []float64{0.1, 0.2, 0.3, 0.4, 1, 0, 0, 0}}
	wide := &Mesh{Name: "b", Positions: []float32{}, Indices: big, SourceMeshIndex: &index}
	prototypes := ordered.New[*MeshPrototype]()
	prototypes.Set("p1", &MeshPrototype{Meshes: []*Mesh{mesh}})
	prototypes.Set("p2", &MeshPrototype{Meshes: []*Mesh{wide}})
	prototypes.Set("p3", nil)
	return &Asset{Name: "n", Path: "p", Models: []*Model{{Name: "m", Meshes: []*Mesh{mesh, wide}}, {Name: "compact"}},
		Warnings: []string{}, MeshPrototypes: prototypes}
}

// expand replaces {"$blob":N} placeholders with the blob's values as JSON
// numbers, which must then equal the JSON encoding (float32 blobs of float64
// sources compare after rounding to float32).
func expand(t *testing.T, value any, blobs []Blob) any {
	switch v := value.(type) {
	case []any:
		for i := range v {
			v[i] = expand(t, v[i], blobs)
		}
	case map[string]any:
		if index, ok := v["$blob"]; ok {
			blob := blobs[int(index.(float64))]
			var out []any
			for i := 0; i < len(blob.Data); {
				switch blob.Type {
				case BlobFloat32:
					out = append(out, float64(math.Float32frombits(binary.LittleEndian.Uint32(blob.Data[i:]))))
					i += 4
				case BlobFloat64:
					out = append(out, math.Float64frombits(binary.LittleEndian.Uint64(blob.Data[i:])))
					i += 8
				case BlobUint32:
					out = append(out, float64(binary.LittleEndian.Uint32(blob.Data[i:])))
					i += 4
				case BlobUint16:
					out = append(out, float64(binary.LittleEndian.Uint16(blob.Data[i:])))
					i += 2
				default:
					t.Fatalf("blob type %q", blob.Type)
				}
			}
			if out == nil {
				out = []any{}
			}
			return out
		}
		for key := range v {
			v[key] = expand(t, v[key], blobs)
		}
	}
	return value
}

func roundFloat32(value any) any {
	switch v := value.(type) {
	case []any:
		for i := range v {
			v[i] = roundFloat32(v[i])
		}
	case map[string]any:
		for key, item := range v {
			if key == "skinWeights" || key == "positions" || key == "normals" || key == "uvs" || key == "uvs2" {
				if list, ok := item.([]any); ok {
					for i, n := range list {
						list[i] = float64(float32(n.(float64)))
					}
					continue
				}
			}
			v[key] = roundFloat32(item)
		}
	}
	return value
}

func TestAppendAssetBinaryMatchesJSON(t *testing.T) {
	asset := binaryFixture()
	plain, err := AppendAssetJSON(nil, asset)
	if err != nil {
		t.Fatal(err)
	}
	blobs := &Blobs{}
	framed, err := AppendAssetBinary(nil, asset, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(framed, []byte("70000")) || len(blobs.Items) != 2*(6+2) {
		t.Fatalf("%d blobs: %s", len(blobs.Items), framed)
	}
	types := map[string]int{}
	for _, blob := range blobs.Items {
		types[blob.Type]++
	}
	// Small index buffers become u16, the >65535 one stays u32.
	if types[BlobUint32] != 2 || types[BlobUint16] != 4 {
		t.Fatalf("types %v", types)
	}
	var want, got any
	if err := json.Unmarshal(plain, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(framed, &got); err != nil {
		t.Fatal(err)
	}
	got = roundFloat32(expand(t, got, blobs.Items))
	want = roundFloat32(want)
	gotText, _ := json.Marshal(got)
	wantText, _ := json.Marshal(want)
	if !bytes.Equal(gotText, wantText) {
		t.Fatalf("binary %s\njson   %s", gotText, wantText)
	}
}

func TestAppendClipBinaryMatchesJSON(t *testing.T) {
	clip := &Clip{ClipHeader: ClipHeader{Name: "c", FPS: 30}, Warnings: []string{},
		Bones: []ClipBone{{Name: "root", Positions: &Track{Frames: []uint32{0, 5}, Values: []float64{0.1, 0.2, 0.3, 1e-9, 2, 3}}},
			{Name: "empty", Rotations: &Track{Frames: []uint32{}, Values: []float64{}}}}}
	plain, err := AppendClipJSON(nil, clip)
	if err != nil {
		t.Fatal(err)
	}
	blobs := &Blobs{}
	framed, err := AppendClipBinary(nil, clip, blobs)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal(plain, &want)
	_ = json.Unmarshal(framed, &got)
	gotText, _ := json.Marshal(expand(t, got, blobs.Items))
	wantText, _ := json.Marshal(want)
	if !bytes.Equal(gotText, wantText) || blobs.Items[1].Type != BlobFloat64 {
		t.Fatalf("binary %s\njson   %s", gotText, wantText)
	}
}

func TestAppendBinaryRejectsNonFinite(t *testing.T) {
	asset := binaryFixture()
	asset.Models[0].Meshes[0].Positions[1] = float32(math.Inf(1))
	if _, err := AppendAssetBinary(nil, asset, &Blobs{}); err == nil {
		t.Fatal("non-finite position accepted")
	}
	clip := &Clip{Bones: []ClipBone{{Name: "b", Scales: &Track{Frames: []uint32{0}, Values: []float64{math.NaN(), 1, 1}}}}}
	if _, err := AppendClipBinary(nil, clip, &Blobs{}); err == nil {
		t.Fatal("NaN clip value accepted")
	}
}
