package mapformats

// Port of tests/test_map_formats.py: synthetic OWLib map records, real
// model/material parsing, no game extraction.

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	mt "avolition/gocore/internal/mapformats/maptest"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

func tempRoot(t *testing.T) string { return pypath.Resolve(t.TempDir()) }

func write(t *testing.T, root, name string, data []byte) string { return ft.Write(t, root, name, data) }

func plainModel() []byte {
	m := ft.NewModel()
	m.Bones, m.Slots = nil, 0
	return m.Bytes()
}

func requireFormatError(t *testing.T, err error, fragment string) {
	t.Helper()
	var formatErr *formats.FormatError
	if !errors.As(err, &formatErr) || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("want FormatError containing %q, got %v", fragment, err)
	}
}

func mustLoad(t *testing.T, path string) *formats.Asset {
	t.Helper()
	asset, err := LoadMap(path, "")
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func summary(asset *formats.Asset) *Summary { return asset.Map.(*Summary) }

func material(t *testing.T, model *formats.Model, key string) *formats.Material {
	t.Helper()
	m, ok := model.Materials.Get(key)
	if !ok {
		t.Fatalf("model %s has no material %s (%v)", model.ID, key, model.Materials.Keys())
	}
	return m
}

func hasWarning(asset *formats.Asset, fragment string) bool {
	return slices.ContainsFunc(asset.Warnings, func(w string) bool { return strings.Contains(w, fragment) })
}

func TestHeaderAndEverySectionForCurrentVersion(t *testing.T) {
	root := tempRoot(t)
	light := ft.Pack("7fIf3ffQQ", 1, 2, 3, 0, 0, 0, 1, 1, 45, .1, .2, .3, 100, 12, 13)
	m := mt.NewMap()
	m.Name = "Máp " + strings.Repeat("x", 128)
	m.Objects = []mt.Object{{Model: "Models/Prop.owmdl", Groups: []mt.Group{{Look: "Look.owmat", Records: [][]byte{mt.RecordBytes()}}}}}
	m.Details = []mt.Detail{{Model: "Entity.owentity", Look: "null", Record: mt.At(1, 2, 3).Bytes()}}
	m.Lights = [][]byte{light}
	m.Sounds = []mt.Sound{{Position: [3]float64{3, 4, 5}, Files: []string{"Sounds/é.ogg", "Sounds/b.ogg"}}}
	path := write(t, root, "Tést café.owmap", m.Bytes())
	header, err := ReadMapHeader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if header.ObjectCount != 1 || header.DetailCount != 1 || header.LightCount != 1 {
		t.Fatalf("%+v", header)
	}
	if encoded, _ := json.Marshal(header); strings.Contains(string(encoded), "instances") {
		t.Fatal("header has instances")
	}
	parsed, err := ReadMap(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Instances) != 2 || parsed.Instances[1].Material != "" || parsed.Instances[1].Category != "Details" ||
		!slices.Equal(parsed.Lights[0].ProjectionTextures, []uint64{12, 13}) ||
		!slices.Equal(parsed.Sounds[0].Files, []string{"Sounds/é.ogg", "Sounds/b.ogg"}) {
		t.Fatalf("%+v", parsed)
	}
}

func TestLegacyLightLayoutPreservesFollowingSounds(t *testing.T) {
	root := tempRoot(t)
	prefix := ft.Pack("7fIf3f", 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1)
	extras := make([]byte, 0)
	for _, format := range []string{"IIBBBBII", "fff", "ffff", "fff", "ffff", "fff", "ffff", "ffIHHII"} {
		extras = append(extras, make([]byte, formats.FormatSize(format))...)
	}
	m := mt.NewMap()
	m.Version = [2]uint16{2, 0}
	m.Lights = [][]byte{append(prefix, extras...)}
	m.Sounds = []mt.Sound{{Position: [3]float64{9, 8, 7}, Files: []string{"sound.ogg"}}}
	parsed, err := ReadMap(write(t, root, "legacy.owmap", m.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "2.0" || parsed.Lights[0].Intensity != nil || parsed.Sounds[0].Position != [3]float64{9, 8, 7} {
		t.Fatalf("%+v", parsed)
	}
}

func TestTruncationCorruptionAndFutureVersionAreRejected(t *testing.T) {
	root := tempRoot(t)
	valid := func() mt.Map {
		m := mt.NewMap()
		m.Objects = []mt.Object{{Model: "a.owmdl", Groups: []mt.Group{{Look: "", Records: [][]byte{mt.RecordBytes()}}}}}
		return m
	}().Bytes()
	var bad [][]byte
	for _, n := range []int{0, 3, 8, 20, len(valid) - 1} {
		bad = append(bad, valid[:n])
	}
	future := mt.NewMap()
	future.Version = [2]uint16{2, 2}
	zero := mt.NewRecord()
	zero.Rotation = [4]float64{}
	nan := mt.At(math.NaN(), 0, 0)
	empty := mt.NewMap().Bytes()
	bad = append(bad, append(append([]byte{}, valid...), "extra"...), future.Bytes(),
		mt.Details(mt.Detail{Model: "a.owmdl", Record: zero.Bytes()}),
		mt.Details(mt.Detail{Model: "a.owmdl", Record: nan.Bytes()}),
		append(append(append([]byte{}, empty[:len(empty)-4]...), ft.Pack("I", 1)...), ft.Pack("3fi", 0, 0, 0, -1)...))
	for i, blob := range bad {
		_, err := ReadMap(write(t, root, "bad.owmap", blob), nil)
		var formatErr *formats.FormatError
		if !errors.As(err, &formatErr) {
			t.Errorf("%d: want FormatError, got %v", i, err)
		}
	}
}

func TestCountsAndFileSizeFailBeforeGeometryLoading(t *testing.T) {
	root := tempRoot(t)
	m := mt.NewMap()
	m.Objects = []mt.Object{{Model: "a.owmdl", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes(), mt.RecordBytes(), mt.RecordBytes()}}}}}
	path := write(t, root, "large.owmap", m.Bytes())
	limits := DefaultLimits
	limits.Instances = 2
	_, err := ReadMap(path, &limits)
	requireFormatError(t, err, "instances")
	limits = DefaultLimits
	limits.FileBytes = 8
	_, err = ReadMapHeader(path, &limits)
	requireFormatError(t, err, "byte limit")
	bomb := append(ft.Pack("HH", 2, 1), ft.String("Bad")...)
	bomb = append(bomb, ft.Pack("III", 1, 0, 0)...)
	bomb = append(bomb, ft.String("a.owmdl")...)
	bomb = append(bomb, ft.Pack("I", 1)...)
	bomb = append(bomb, ft.String("")...)
	bomb = append(bomb, ft.Pack("I", uint32(math.MaxUint32))...)
	_, err = ReadMap(write(t, root, "bomb.owmap", bomb), nil)
	requireFormatError(t, err, "instances")
}

func TestCompactPrototypesShareGeometryAcrossPlacementsAndLooks(t *testing.T) {
	root := tempRoot(t)
	asset := mustLoad(t, mt.WriteFixture(t, root))
	s := summary(asset)
	if s.InstanceCount != 4 || s.ModelCount != 5 || s.PrototypeCount != 3 || s.MeshCount != 5 || s.PolygonCount != 5 {
		t.Fatalf("%+v", s)
	}
	for _, model := range asset.Models {
		if model.Meshes != nil {
			t.Fatal("compact model has meshes")
		}
	}
	if asset.Models[0].MeshPrototype != asset.Models[1].MeshPrototype || asset.Models[1].MeshPrototype != asset.Models[2].MeshPrototype {
		t.Fatal("prototype not shared")
	}
	expanded, err := ExpandMapAsset(asset)
	if err != nil {
		t.Fatal(err)
	}
	if &expanded.Models[0].Meshes[0].Positions[0] != &expanded.Models[2].Meshes[0].Positions[0] {
		t.Fatal("expanded positions copied")
	}
	a, b, c := asset.Models[0], asset.Models[1], asset.Models[2]
	if a.Materials != b.Materials {
		t.Fatal("materials not shared between placements of one variant")
	}
	if material(t, a, "4000").SourcePath == material(t, c, "4000").SourcePath {
		t.Fatal("looks not applied")
	}
	if hasWarning(asset, "Prop.owmdl: no material look reference") {
		t.Fatal(asset.Warnings)
	}
	if expanded.MeshPrototypes != nil {
		t.Fatal("expanded keeps prototypes")
	}
	for _, model := range expanded.Models {
		if model.MeshPrototype != "" {
			t.Fatal("expanded model keeps meshPrototype")
		}
	}
}

func TestMeshNamesArePerInstanceNotSharedPrototype(t *testing.T) {
	root := tempRoot(t)
	asset := mustLoad(t, mt.WriteFixture(t, root))
	names := ordered.New[string]()
	names.Set("0", "Named first mesh")
	asset.Models[0].MeshNames = names
	expanded, err := ExpandMapAsset(asset)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := asset.MeshPrototypes.Get(asset.MeshPrototypes.Keys()[0])
	if expanded.Models[0].Meshes[0].Name != "Named first mesh" || expanded.Models[1].Meshes[0].Name != "Triangle" ||
		first.Meshes[0].Name != "Triangle" {
		t.Fatal("mesh names leaked")
	}
}

func TestBlenderCollisionMaterialsRemovedPerLookWithStableSourceIndices(t *testing.T) {
	root := tempRoot(t)
	ft.GenerateMaterialDemo(t, root)
	folder := "Models/MaterialRobot.00C/"
	write(t, root, folder+"Materials/0000000007A1.owmat", ft.MaterialBytes(nil, nil, 33))
	write(t, root, folder+"ModelLooks/Collision.owmat", ft.LookBytes(
		ft.LookEntry{Key: 1, Reference: "../../Materials/0000000007A1.owmat"},
		ft.LookEntry{Key: 2, Reference: "../../Materials/000000B002.owmat"},
		ft.LookEntry{Key: 3, Reference: "../../Materials/000000B003.owmat"}))
	m := mt.NewMap()
	m.Objects = []mt.Object{{Model: folder + "MaterialRobot.owmdl", Groups: []mt.Group{
		{Look: folder + "ModelLooks/Collision.owmat", Records: [][]byte{mt.RecordBytes()}},
		{Look: folder + "ModelLooks/Default.owmat", Records: [][]byte{mt.RecordBytes()}}}}}
	asset := mustLoad(t, write(t, root, "collision.owmap", m.Bytes()))
	first, second := asset.Models[0], asset.Models[1]
	if first.MeshPrototype == second.MeshPrototype {
		t.Fatal("prototype shared across collision filtering")
	}
	prototype, _ := asset.MeshPrototypes.Get(first.MeshPrototype)
	var indices []int
	for _, mesh := range prototype.Meshes {
		indices = append(indices, *mesh.SourceMeshIndex)
	}
	other, _ := asset.MeshPrototypes.Get(second.MeshPrototype)
	s := summary(asset)
	if !slices.Equal(indices, []int{4, 5, 6, 7}) || len(other.Meshes) != 8 || s.OmittedCollisionMeshCount != 4 ||
		s.OmittedCollisionPolygonCount != 48 || s.MeshCount != 12 {
		t.Fatalf("%v %+v", indices, s)
	}
	if keys := first.Materials.Keys(); !slices.Equal(keys, []string{"2", "3"}) {
		t.Fatal(keys)
	}
	var pairs [][2]int
	for _, node := range asset.ExportHierarchy {
		if node.ModelID == first.ID && node.Type == "mesh" {
			pairs = append(pairs, [2]int{*node.SourceMeshIndex, *node.MeshIndex})
		}
	}
	if !slices.Equal(pairs, [][2]int{{4, 0}, {5, 1}, {6, 2}, {7, 3}}) || !hasWarning(asset, "engine hit-test") {
		t.Fatal(pairs, asset.Warnings)
	}
}

func TestMapEntityLookAppliesToChildrenLikeBlender(t *testing.T) {
	root := tempRoot(t)
	path := mt.WriteFixture(t, root)
	write(t, root, "BodyLook.owmat", ft.LookBytes(ft.LookEntry{Key: 4000, Reference: "BodyMaterial.owmat"}))
	write(t, root, "BodyMaterial.owmat", ft.MaterialBytes(nil, nil, 33))
	write(t, root, "Example.owmap", mt.Details(mt.Detail{Model: "Body.owentity", Look: "BodyLook.owmat", Record: mt.RecordBytes()}))
	asset := mustLoad(t, path)
	if len(asset.Models) != 2 {
		t.Fatal(len(asset.Models))
	}
	for _, model := range asset.Models {
		if material(t, model, "4000").Name != "BodyMaterial" {
			t.Fatal(model.ID)
		}
	}
}

func TestPartialMapEntityLookRetainsUnmentionedChildSlots(t *testing.T) {
	root := tempRoot(t)
	ft.GenerateMaterialDemo(t, root)
	path := write(t, root, "partial-look.owmap", mt.Details(mt.Detail{
		Model: "Entities/MaterialRobot/MaterialRobot.owentity", Look: "Partial.owmat", Record: mt.RecordBytes()}))
	write(t, root, "Partial.owmat", ft.LookBytes(ft.LookEntry{Key: 2, Reference: "Override.owmat"}))
	write(t, root, "Override.owmat", ft.MaterialBytes(nil, nil, 55))
	model := mustLoad(t, path).Models[0]
	if keys := model.Materials.Keys(); !slices.Equal(keys, []string{"1", "2", "3"}) ||
		material(t, model, "2").Name != "Override" || material(t, model, "1").Name != "000000B001" {
		t.Fatal(keys)
	}
}

func TestPresentNullMapLookSlotClearsOnlyThatBinding(t *testing.T) {
	root := tempRoot(t)
	ft.GenerateMaterialDemo(t, root)
	path := write(t, root, "null-slot.owmap", mt.Details(mt.Detail{
		Model: "Entities/MaterialRobot/MaterialRobot.owentity", Look: "Partial.owmat", Record: mt.RecordBytes()}))
	write(t, root, "Partial.owmat", ft.LookBytes(ft.LookEntry{Key: 2, Reference: "null"}))
	model := mustLoad(t, path).Models[0]
	if keys := model.Materials.Keys(); !slices.Equal(keys, []string{"1", "3"}) || material(t, model, "1").Name != "000000B001" {
		t.Fatal(keys)
	}
}

func TestTRSNonuniformMirrorAndSocketAttachmentsMatchPreviewAndNativeGraph(t *testing.T) {
	root := tempRoot(t)
	asset := mustLoad(t, mt.WriteFixture(t, root))
	graph := map[string]formats.Node{}
	for _, node := range asset.ExportHierarchy {
		graph[node.ID] = node
	}
	model := asset.Models[2]
	matrix := model.AttachmentMatrix
	expected := []float64{0, -2, 0, 0, -3, 0, 0, 0, 0, 0, 4, 0, -1, 2, 3, 1}
	for i, want := range expected {
		if math.Abs(matrix[i]-want) > 5e-7 {
			t.Fatalf("matrix %v", matrix)
		}
	}
	if graph[model.ID+":model"].Matrix != matrix {
		t.Fatal("graph matrix")
	}
	body, child := asset.Models[3], asset.Models[4]
	if *child.ParentID != body.ID || *child.AttachmentBone != "bone_0002" ||
		graph[body.ID+":entity"].Matrix != body.AttachmentMatrix ||
		graph[child.ID+":entity"].Matrix != child.AttachmentMatrix ||
		child.AttachmentMatrix == formats.Identity ||
		*graph[child.ID+":entity"].ParentID != body.ID+":bone:1" {
		t.Fatal("attachments")
	}
}

func TestMissingAssetsAreReportedAndEmptyMapErrors(t *testing.T) {
	root := tempRoot(t)
	write(t, root, "a.owmdl", plainModel())
	path := write(t, root, "partial.owmap", mt.Details(
		mt.Detail{Model: "a.owmdl", Look: "Missing.owmat", Record: mt.RecordBytes()},
		mt.Detail{Model: "missing.owmdl", Record: mt.RecordBytes()}))
	asset := mustLoad(t, path)
	if summary(asset).MissingInstanceCount != 1 || asset.Models[0].Materials.Len() != 0 ||
		!hasWarning(asset, "material look is missing") || !hasWarning(asset, "could not be loaded") {
		t.Fatal(asset.Warnings)
	}
	write(t, root, "partial.owmap", mt.Details(mt.Detail{Model: "missing.owmdl", Record: mt.RecordBytes()}))
	_, err := LoadMap(path, "")
	requireFormatError(t, err, "No map meshes")
}

func TestMapReferencePathsAreLiteralAndCannotEscape(t *testing.T) {
	root := tempRoot(t)
	write(t, root, "model space/é.owmdl", plainModel())
	for _, reference := range []string{"model space/é.owmdl", `model space\é.owmdl`} {
		asset := mustLoad(t, write(t, root, "valid.owmap", mt.Details(mt.Detail{Model: reference, Record: mt.RecordBytes()})))
		if len(asset.Models) != 1 {
			t.Fatal(reference)
		}
	}
	for _, reference := range []string{"../outside.owmdl", `C:\outside.owmdl`, "//host/a.owmdl", "/absolute.owmdl",
		"a:stream.owmdl", "models/../../outside.owmdl", "bad.exe"} {
		_, err := LoadMap(write(t, root, "bad.owmap", mt.Details(mt.Detail{Model: reference, Record: mt.RecordBytes()})), "")
		requireFormatError(t, err, "")
	}
}

func TestAggregateGeometryInstanceAndHierarchyLimitsAreEnforced(t *testing.T) {
	root := tempRoot(t)
	path := mt.WriteFixture(t, root)
	for _, change := range []func(*Limits){
		func(l *Limits) { l.UniqueVertices = 2 }, func(l *Limits) { l.Models = 2 },
		func(l *Limits) { l.MeshInstances = 2 }, func(l *Limits) { l.GraphNodes = 2 },
	} {
		limits := DefaultLimits
		change(&limits)
		_, err := LoadMapWith(path, "", Options{Limits: &limits})
		requireFormatError(t, err, "limit")
	}
}

func TestRepeatedSourceIsParsedOnlyOnce(t *testing.T) {
	root := tempRoot(t)
	write(t, root, "a.owmdl", plainModel())
	records := make([][]byte, 100)
	for i := range records {
		records[i] = mt.RecordBytes()
	}
	m := mt.NewMap()
	m.Objects = []mt.Object{{Model: "a.owmdl", Groups: []mt.Group{{Records: records}}}}
	path := write(t, root, "many.owmap", m.Bytes())
	var calls atomic.Int32
	loadAsset = func(path, root string) (*formats.Asset, error) {
		calls.Add(1)
		return formats.LoadAsset(path, root)
	}
	defer func() { loadAsset = formats.LoadAsset }()
	asset := mustLoad(t, path)
	if calls.Load() != 1 {
		t.Fatalf("load_asset called %d times", calls.Load())
	}
	if asset.MeshPrototypes.Len() != 1 || len(asset.Models) != 100 {
		t.Fatal("prototypes")
	}
	compact, _ := formats.AppendAssetJSON(nil, asset)
	expanded, _ := ExpandMapAsset(asset)
	full, _ := formats.AppendAssetJSON(nil, expanded)
	if len(compact) >= len(full) {
		t.Fatalf("compact %d >= expanded %d", len(compact), len(full))
	}
}

func TestScanKeepsInvalidMapErrorsAndNeverInventsAssets(t *testing.T) {
	root := tempRoot(t)
	path := mt.WriteFixture(t, root)
	write(t, root, "broken.owmap", []byte("bad"))
	write(t, root, "ignored.txt", []byte("not a map"))
	result, err := ScanMaps(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Maps) != 1 || result.Maps[0].Path != pypath.Resolve(path) || len(result.Errors) != 1 {
		t.Fatalf("%+v", result)
	}
	_, err = ScanMaps(root, 1)
	var valueErr *ValueError
	if !errors.As(err, &valueErr) || !strings.Contains(err.Error(), "smaller") {
		t.Fatal(err)
	}
	if _, err := ScanMaps(filepath.Join(root, "missing"), 0); !errors.As(err, &valueErr) {
		t.Fatal(err)
	}
}
