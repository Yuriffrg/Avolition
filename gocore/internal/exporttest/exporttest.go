// Package exporttest holds fixtures shared by the export packages' tests
// (exportpose, c4dexport, fbx, objectnames): ports of the Python test helpers
// oracle_fixture (tests/test_fbx.py), fixture/world_matrices
// (tests/test_selected_object_export.py) and assigned_fixture
// (tests/test_combined_export.py), plus JSON helpers for the Python oracle.
package exporttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/ordered"
)

// RepoRoot is the repository checkout (the parent of gocore).
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// TestsDir is the Python tests folder.
func TestsDir() string { return filepath.Join(RepoRoot(), "tests") }

// Ptr returns a pointer to a copy of value.
func Ptr[T any](value T) *T { return &value }

// Sample is one Blender oracle sample: a frame and world matrices by bone.
type Sample struct {
	Frame    float64              `json:"frame"`
	Matrices map[string][]float64 `json:"matrices"`
}

// Playback is oracle_fixture(): tests/fixtures/playback_fixture.json as
// skeleton bones and clip tracks.
type Playback struct {
	Bones        []formats.Bone
	Tracks       []formats.ClipBone
	Samples      []Sample
	RestMatrices map[string][]float64
	Tolerance    float64
}

// LoadPlayback is oracle_fixture().
func LoadPlayback(t testing.TB) *Playback {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(TestsDir(), "fixtures", "playback_fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		Frame uint32    `json:"frame"`
		Data  []float64 `json:"data"`
	}
	var raw struct {
		Bones []struct {
			Name        string     `json:"name"`
			Parent      int        `json:"parent"`
			Position    [3]float64 `json:"position"`
			Rotation    [3]float64 `json:"rotation"`
			SourceScale [3]float64 `json:"sourceScale"`
		} `json:"bones"`
		RestMatrices map[string][]float64 `json:"restMatrices"`
		Clip         struct {
			Bones []struct {
				Name      string `json:"name"`
				Positions []key  `json:"positions"`
				Rotations []key  `json:"rotations"`
				Scales    []key  `json:"scales"`
			} `json:"bones"`
		} `json:"clip"`
		Samples   []Sample `json:"samples"`
		Tolerance float64  `json:"matrixTolerance"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	p := &Playback{Samples: raw.Samples, RestMatrices: raw.RestMatrices, Tolerance: raw.Tolerance}
	for _, b := range raw.Bones {
		p.Bones = append(p.Bones, formats.Bone{Name: b.Name, SourceName: b.Name, Parent: b.Parent,
			Pos: b.Position, Rot: formats.EulerXYZ(b.Rotation), Scale: b.SourceScale})
	}
	convert := func(keys []key) *formats.Track {
		track := &formats.Track{Frames: []uint32{}, Values: []float64{}}
		for _, k := range keys {
			track.Frames = append(track.Frames, k.Frame)
			track.Values = append(track.Values, k.Data...)
		}
		return track
	}
	for _, b := range raw.Clip.Bones {
		p.Tracks = append(p.Tracks, formats.ClipBone{Name: b.Name, Positions: convert(b.Positions),
			Rotations: convert(b.Rotations), Scales: convert(b.Scales)})
	}
	return p
}

// Clip wraps tracks in a clip with the given timing.
func Clip(name string, fps float64, lastFrame int64, bones []formats.ClipBone) *formats.Clip {
	if bones == nil {
		bones = []formats.ClipBone{}
	}
	return &formats.Clip{ClipHeader: formats.ClipHeader{Name: name, FPS: fps, LastFrame: lastFrame,
		DurationFrames: lastFrame + 1}, Bones: bones, Warnings: []string{}}
}

// ColumnMajor is compose(p, q, s).T.reshape(-1).tolist().
func ColumnMajor(p [3]float64, q [4]float64, s [3]float64) formats.Mat4 {
	m := exportpose.Compose(p, q, s)
	return m.ColumnMajor()
}

// PosTrack is dict(frames=frames, values=values).
func PosTrack(frames []uint32, values ...float64) *formats.Track {
	return &formats.Track{Frames: frames, Values: values}
}

// SelectedFixture is tests/test_selected_object_export.py fixture(): arms
// with an attached gun with an attached charm, each with one skinned mesh,
// a two-bone rig and one material.
func SelectedFixture(t testing.TB) (*formats.Asset, *formats.Clip) {
	t.Helper()
	textures := ordered.New[*formats.Texture]()
	var models []*formats.Model
	for index, pair := range [][2]string{{"arms", ""}, {"gun", "arms"}, {"charm", "gun"}} {
		mid, parent := pair[0], pair[1]
		materialKey := "mat-" + mid
		mesh := &formats.Mesh{Name: "Submesh_" + mid, MaterialKey: materialKey,
			Positions: []float32{0, 0, 0, 1, 0, 0, 0, 1, 0}, Indices: []uint32{0, 1, 2},
			Normals: []float32{0, 0, .97, 0, 0, .97, 0, 0, .97},
			UVs:     []float32{.1, .2, .3, .4, .5, .6}, UVs2: []float32{.7, .8, .9, 1.1, 1.2, 1.3},
			SkinIndices: []uint16{0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0},
			SkinWeights: []float64{.25, .75, 0, 0, .25, .75, 0, 0, .25, .75, 0, 0}}
		bones := []formats.Bone{
			{Name: "bone_0001", SourceName: "bone_0001", Parent: -1, Pos: [3]float64{float64(index + 1), 0, 0}, Rot: [4]float64{0, 0, 0, 1}, Scale: [3]float64{1, 1, 1}},
			{Name: "bone_004A", SourceName: "bone_004A", Parent: 0, Pos: [3]float64{0, float64(index + 2), 0}, Rot: [4]float64{0, 0, 0, 1}, Scale: [3]float64{1, 1, 1}},
		}
		maps := ordered.New[*formats.MaterialMap]()
		maps.Set("baseColor", &formats.MaterialMap{TextureID: mid, ColorSpace: "srgb", Repeat: [2]float64{1, 1}})
		materials := ordered.New[*formats.Material]()
		materials.Set(materialKey, &formats.Material{Name: materialKey, Maps: maps, Opacity: 1, AlphaMode: "opaque"})
		model := &formats.Model{ID: mid, Name: fmt.Sprintf("%012X.00C", index+1), ModelGUID: Ptr(fmt.Sprintf("%012X", index+1)),
			AttachmentMatrix: ColumnMajor([3]float64{9, 8, 7}, [4]float64{0, 0, 0, 1}, [3]float64{1, 1, 1}),
			IsEntity:         Ptr(true), HasModel: Ptr(true), Bones: bones, Meshes: []*formats.Mesh{mesh},
			Sockets: []formats.Socket{}, Materials: materials}
		if parent != "" {
			model.ParentID = Ptr(parent)
			model.AttachmentBone = Ptr("bone_004A")
			model.EntityName = Ptr(mid + "_Entity")
		} else {
			model.EntityName = Ptr("Gameplay1P")
		}
		models = append(models, model)
		textures.Set(mid, &formats.Texture{Path: mid + ".png", Channel: "rgb"})
	}
	// A no-longer-used material must not cause extra texture output either.
	unusedMaps := ordered.New[*formats.MaterialMap]()
	unusedMaps.Set("baseColor", &formats.MaterialMap{TextureID: "unused"})
	models[1].Materials.Set("unused", &formats.Material{Name: "unused", Maps: unusedMaps})
	textures.Set("unused", &formats.Texture{Path: "unused.png", Channel: "rgb"})
	asset := &formats.Asset{Name: "Gameplay1P", Path: "Gameplay1P.owentity", Models: models, Textures: textures, Warnings: []string{}}
	graph, err := formats.BuildExportHierarchy(asset)
	if err != nil {
		t.Fatal(err)
	}
	asset.ExportHierarchy = graph
	// Exercise a nonidentity top-level placement and an attached hierarchy.
	asset.ExportHierarchy[0].Matrix = ColumnMajor([3]float64{3, 4, 5}, [4]float64{0, 0, .7071067811865475, .7071067811865475}, [3]float64{1, 1, 1})
	clip := Clip("Reload", 30, 2, []formats.ClipBone{
		{Name: "bone_0001", Positions: PosTrack([]uint32{0, 2}, 0, 0, 0, 0, 0, 1)},
		{Name: "bone_004A", Positions: PosTrack([]uint32{0, 2}, 0, 3, 0, 0, 4, 0)},
	})
	return asset, clip
}

// Assignment is one {modelId, clip} record of assigned_fixture().
type Assignment struct {
	ModelID string
	Clip    *formats.Clip
}

// AssignedFixture is tests/test_combined_export.py assigned_fixture().
func AssignedFixture(t testing.TB) (*formats.Asset, []Assignment) {
	asset, arms := SelectedFixture(t)
	arms.Name, arms.FPS, arms.LastFrame, arms.DurationFrames = "Arms reload", 24, 24, 25
	arms.Bones[0].Positions = PosTrack([]uint32{0, 24}, 0, 0, 0, 2, 0, 3)
	arms.Bones[1].Positions = PosTrack([]uint32{0, 24}, 0, 2, 0, 0, 4, 0)
	gun := Clip("Gun reload", 30, 15, []formats.ClipBone{
		{Name: "bone_0001", Positions: PosTrack([]uint32{0, 15}, 0, 0, 0, 0, 1, 0)},
		{Name: "bone_004A", Positions: PosTrack([]uint32{0, 15}, 0, 3, 0, 0, 3, -2)},
		{Name: "bone_0075", Positions: PosTrack([]uint32{0, 15}, 0, 0, 0, 99, 99, 99)},
	})
	return asset, []Assignment{{"arms", arms}, {"gun", gun}}
}

// WorldMatrices is world_matrices(graph, overrides): every node's world
// matrix (row-major), with optional replacement local matrices by node id.
func WorldMatrices(t testing.TB, graph []formats.Node, overrides map[string]formats.Mat4) map[string]exportpose.Mat4 {
	t.Helper()
	worlds := map[string]exportpose.Mat4{}
	pending := graph
	for len(pending) > 0 {
		var rest []formats.Node
		for _, node := range pending {
			values := node.Matrix
			if override, ok := overrides[node.ID]; ok {
				values = override
			}
			local := exportpose.FromColumnMajor(values)
			if node.ParentID == nil || *node.ParentID == "" {
				worlds[node.ID] = local
			} else if parent, ok := worlds[*node.ParentID]; ok {
				worlds[node.ID] = parent.Mul(&local)
			} else {
				rest = append(rest, node)
			}
		}
		if len(rest) == len(pending) {
			t.Fatal("graph contains a missing parent or a cycle")
		}
		pending = rest
	}
	return worlds
}

// AssertClose fails when two matrices differ by more than atol.
func AssertClose(t testing.TB, label string, got, want exportpose.Mat4, atol float64) {
	t.Helper()
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if d := got[i][j] - want[i][j]; d > atol || d < -atol || d != d {
				t.Fatalf("%s: [%d][%d] = %v, want %v (atol %g)\n got  %v\n want %v", label, i, j, got[i][j], want[i][j], atol, got, want)
			}
		}
	}
}

// PyJSON marshals v for the Python oracle. Float32 mesh arrays are written
// as their exact float64 expansions (as Python's load_asset produces them),
// not Go's shortest float32 text, so both languages compute on identical
// values. v is typically an *formats.Asset; any value is accepted.
func PyJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if asset, ok := v.(*formats.Asset); ok && asset != nil {
		data = expandAsset(t, data, asset)
	}
	return negativeZeros(data)
}

// negativeZeros rewrites the number -0 as -0.0: Python's json reads "-0" as
// the integer 0 and would lose the sign that float data carries.
func negativeZeros(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		out = append(out, c)
		switch {
		case inString && c == '\\':
			i++
			if i < len(data) {
				out = append(out, data[i])
			}
		case c == '"':
			inString = !inString
		case !inString && c == '-' && i+1 < len(data) && data[i+1] == '0':
			next := byte(0)
			if i+2 < len(data) {
				next = data[i+2]
			}
			if next != '.' && next != 'e' && next != 'E' {
				out = append(out, '0', '.', '0')
				i++
			}
		}
	}
	return out
}

func expandAsset(t testing.TB, data []byte, asset *formats.Asset) json.RawMessage {
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	expandMeshes := func(target any, meshes []*formats.Mesh) {
		list, _ := target.([]any)
		for i, mesh := range meshes {
			if mesh == nil || i >= len(list) {
				continue
			}
			object := list[i].(map[string]any)
			for key, values := range map[string][]float32{"positions": mesh.Positions, "normals": mesh.Normals,
				"uvs": mesh.UVs, "uvs2": mesh.UVs2} {
				if values == nil {
					continue
				}
				wide := make([]float64, len(values))
				for k, f := range values {
					wide[k] = float64(f)
				}
				object[key] = wide
			}
		}
	}
	if models, ok := doc["models"].([]any); ok {
		for i, model := range asset.Models {
			if model != nil && i < len(models) {
				expandMeshes(models[i].(map[string]any)["meshes"], model.Meshes)
			}
		}
	}
	if prototypes, ok := doc["meshPrototypes"].(map[string]any); ok && asset.MeshPrototypes != nil {
		asset.MeshPrototypes.Range(func(key string, value *formats.MeshPrototype) bool {
			if value != nil {
				if object, ok := prototypes[key].(map[string]any); ok {
					expandMeshes(object["meshes"], value.Meshes)
				}
			}
			return true
		})
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// AttachedEntity writes the multi-object entity of tests/test_fbx.py
// test_original_source_wrappers_names_and_parent_connections into dir:
// main.owentity (model Main.00C) with child.owentity (Child.00C) attached to
// bone_0002, plus a grandchild attached to the child's bone_0002. It returns
// the entity path.
func AttachedEntity(t testing.TB, dir string) string {
	t.Helper()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Main", "Child", "Charm"} {
		model := ft.NewModel()
		model.Name = name + ".00C"
		write(strings.ToLower(name)+".owmdl", model.Bytes())
	}
	main := ft.Entity{Name: "Gameplay1P", Model: "main.owmdl", Minor: 1, Relative: ".",
		Children: []ft.Child{{Reference: "child.owentity", Attachment: "bone_0002"}}}
	child := ft.Entity{Name: "Child header ignored", Model: "child.owmdl", Minor: 1, Relative: ".",
		Children: []ft.Child{{Reference: "charm.owentity", Attachment: "bone_0002"}}}
	charm := ft.Entity{Name: "Charm", Model: "charm.owmdl", Minor: 1, Relative: "."}
	write("main.owentity", main.Bytes())
	write("child.owentity", child.Bytes())
	write("charm.owentity", charm.Bytes())
	return filepath.Join(dir, "main.owentity")
}
