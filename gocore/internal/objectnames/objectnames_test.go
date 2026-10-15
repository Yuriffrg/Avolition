package objectnames_test

// Port of tests/test_object_names.py plus oracle comparisons with
// owpreview/object_names.py.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/objectnames"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
)

func ptr[T any](v T) *T { return &v }

func names(pairs ...string) *ordered.Map[string] {
	m := ordered.New[string]()
	for i := 0; i+1 < len(pairs); i += 2 {
		m.Set(pairs[i], pairs[i+1])
	}
	return m
}

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fixture() *formats.Asset {
	return &formats.Asset{Name: "Gameplay1P", Path: "source/Gameplay1P.owentity", ExportHierarchy: []formats.Node{}, Models: []*formats.Model{
		{ID: "model-0", Name: "Source arms", IsEntity: ptr(true), EntityName: ptr("Gameplay1P"),
			Path: "source/Arms.owmdl", ModelGUID: ptr("00000013E"),
			Bones:  []formats.Bone{{Name: "bone_0001", Parent: -1}},
			Meshes: []*formats.Mesh{{Name: "Submesh_0", MaterialKey: "original-material", Indices: []uint32{0, 1, 2}}}},
		{ID: "model-1", Name: "Source weapon", ParentID: ptr("model-0"), IsEntity: ptr(true),
			EntityName: ptr("Gun"), AttachmentBone: ptr("bone_0001"), Path: "source/Gun.owmdl",
			Bones: []formats.Bone{{Name: "bone_0001", Parent: -1}}, Meshes: []*formats.Mesh{{Name: "Submesh_0"}}},
	}}
}

func keysOf(t *testing.T, asset *formats.Asset) []string {
	t.Helper()
	set, err := objectnames.ObjectNameKeys(asset)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func apply(t *testing.T, asset *formats.Asset, n any) *formats.Asset {
	t.Helper()
	result, err := objectnames.ApplyObjectNames(asset, n)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFilteredMapMeshNamesKeepTheOriginalMeshIndex(t *testing.T) {
	prototypes := ordered.New[*formats.MeshPrototype]()
	prototypes.Set("p", &formats.MeshPrototype{Meshes: []*formats.Mesh{{Name: "Visible", SourceMeshIndex: ptr(2)}}})
	asset := &formats.Asset{Map: map[string]any{}, MeshPrototypes: prototypes,
		Models: []*formats.Model{{ID: "map-0-model-0", MeshPrototype: "p"}},
		ExportHierarchy: []formats.Node{{Type: "mesh", ModelID: "map-0-model-0", MeshIndex: ptr(0),
			SourceMeshIndex: ptr(2), Name: "Visible"}}}
	keys := keysOf(t, asset)
	if !contains(keys, "mesh:map-0-model-0:2") || contains(keys, "mesh:map-0-model-0:0") {
		t.Fatal(keys)
	}
	renamed := apply(t, asset, names("mesh:map-0-model-0:2", "Facade"))
	if mustJSON(t, renamed.Models[0].MeshNames) != `{"0":"Facade"}` || renamed.ExportHierarchy[0].Name != "Facade" {
		t.Fatal("not renamed")
	}
	p, _ := asset.MeshPrototypes.Get("p")
	if p.Meshes[0].Name != "Visible" {
		t.Fatal("prototype renamed")
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestNamesAreTrimmedUnicodeDisplayValuesNotFilenames(t *testing.T) {
	for input, want := range map[string]string{
		"  Arms / modèle: 日本語 😀  ": "Arms / modèle: 日本語 😀",
		strings.Repeat("a", 160):   strings.Repeat("a", 160),
		strings.Repeat("😀", 80):    strings.Repeat("😀", 80),
	} {
		got, err := objectnames.ValidateName(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
}

var invalidNames = []any{nil, 42, "", "   ", "a\n", "\tArms", "a\x00b", "a\x7fb", "a\u0085b", "\xed\xa0\x80",
	strings.Repeat("a", 161), strings.Repeat("😀", 81)}

func TestInvalidNamesRejected(t *testing.T) {
	for _, value := range invalidNames {
		if _, err := objectnames.ValidateName(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}

func TestStableKeysPreserveBonesAndSourceIdentifiers(t *testing.T) {
	asset := fixture()
	if keys := keysOf(t, asset); !reflect.DeepEqual(keys, []string{"mesh:model-0:0", "mesh:model-1:0", "model:model-0", "model:model-1", "scene"}) {
		t.Fatal(keys)
	}
	for _, key := range []string{"bone:model-0:0", "skeleton:model-0", "model:missing", "mesh:model-0:99", "__proto__"} {
		if _, err := objectnames.ValidateNames(names(key, "New"), asset); err == nil {
			t.Errorf("%s accepted", key)
		}
	}
	if result, err := objectnames.ValidateNames(nil, asset); err != nil || result.Len() != 0 {
		t.Fatal(result, err)
	}
	if _, err := objectnames.ValidateNames([]any{"invalid"}, asset); err == nil {
		t.Fatal("a list was accepted")
	}
}

func TestAppliesToExportCopyWithoutChangingSourceOrBindings(t *testing.T) {
	asset := fixture()
	original := mustJSON(t, asset)
	changed := apply(t, asset, names("scene", "Reload setup", "model:model-0", "Arms", "mesh:model-1:0", "Weapon shell"))
	if mustJSON(t, asset) != original {
		t.Fatal("source changed")
	}
	if changed.Name != "Reload setup" || *changed.Models[0].EntityName != "Reload setup" ||
		changed.Models[0].Name != "Arms" || changed.Models[1].Meshes[0].Name != "Weapon shell" {
		t.Fatal(mustJSON(t, changed))
	}
	for i, before := range asset.Models {
		after := changed.Models[i]
		if before.ID != after.ID || mustJSON(t, before.ParentID) != mustJSON(t, after.ParentID) || before.Path != after.Path ||
			mustJSON(t, before.Bones) != mustJSON(t, after.Bones) || mustJSON(t, before.ModelGUID) != mustJSON(t, after.ModelGUID) ||
			mustJSON(t, before.AttachmentBone) != mustJSON(t, after.AttachmentBone) {
			t.Fatal("bindings changed")
		}
	}
	changed.Models[0].Meshes[0].Indices[0] = 99
	changed.Models[0].Bones[0].Name = "changed"
	if mustJSON(t, asset) != original {
		t.Fatal("The export copy does not share nested mutable source data.")
	}
}

func TestStandaloneSceneNamesRootModelAndSpecificModelWins(t *testing.T) {
	asset := fixture()
	asset.Models = asset.Models[:1]
	asset.Models[0].IsEntity = ptr(false)
	if changed := apply(t, asset, names("scene", "New scene")); changed.Models[0].Name != "New scene" {
		t.Fatal(changed.Models[0].Name)
	}
	changed := apply(t, asset, names("scene", "New scene", "model:model-0", "Specific model"))
	if changed.Name != "New scene" || changed.Models[0].Name != "Specific model" {
		t.Fatal(changed.Name, changed.Models[0].Name)
	}
}

func TestMultipleRootsAreNotAllRenamedToScene(t *testing.T) {
	asset := fixture()
	asset.Models[1].ParentID = nil
	changed := apply(t, asset, names("scene", "Map"))
	if changed.Name != "Map" || *changed.Models[0].EntityName != "Gameplay1P" || *changed.Models[1].EntityName != "Gun" {
		t.Fatal(mustJSON(t, changed))
	}
}

func compactMap() *formats.Asset {
	prototypes := ordered.New[*formats.MeshPrototype]()
	prototypes.Set("shared", &formats.MeshPrototype{Meshes: []*formats.Mesh{{Name: "Wall", Positions: []float32{1, 2, 3}}}})
	return &formats.Asset{Name: "Map", MeshPrototypes: prototypes, ExportHierarchy: []formats.Node{}, Models: []*formats.Model{
		{ID: "model-0", Name: "Instance A", MeshPrototype: "shared"},
		{ID: "model-1", Name: "Instance B", MeshPrototype: "shared"}}}
}

func TestCompactMapMeshNamesArePerInstance(t *testing.T) {
	asset := compactMap()
	original := mustJSON(t, asset)
	if !contains(keysOf(t, asset), "mesh:model-1:0") {
		t.Fatal("missing key")
	}
	changed := apply(t, asset, names("mesh:model-1:0", "Wall by spawn"))
	if mustJSON(t, changed.Models[1].MeshNames) != `{"0":"Wall by spawn"}` || changed.Models[0].MeshNames != nil {
		t.Fatal(mustJSON(t, changed.Models))
	}
	if changed.MeshPrototypes != asset.MeshPrototypes {
		t.Fatal("Large immutable prototype arrays remain shared.")
	}
	if mustJSON(t, asset) != original {
		t.Fatal("source changed")
	}
	if _, err := objectnames.ApplyObjectNames(asset, names("mesh:model-1:1", "Missing")); err == nil {
		t.Fatal("unknown mesh accepted")
	}
	asset.Models[0].MeshPrototype = "missing"
	if _, err := objectnames.ObjectNameKeys(asset); err == nil || !strings.Contains(err.Error(), "missing mesh prototype") {
		t.Fatal(err)
	}
}

func TestMapSceneNamePreservesSingleInstanceWrapperAndGeometryArrays(t *testing.T) {
	asset := fixture()
	asset.Models = asset.Models[:1]
	asset.Map = map[string]any{"instanceCount": 1}
	asset.ExportHierarchy = []formats.Node{{ID: "instance", Type: "entity", ModelID: "model-0", Name: "Source instance"}}
	changed := apply(t, asset, names("scene", "Map label", "mesh:model-0:0", "Wall"))
	if changed.Name != "Map label" || *changed.Models[0].EntityName != "Gameplay1P" ||
		changed.ExportHierarchy[0].Name != "Source instance" || changed.Models[0].Meshes[0].Name != "Wall" ||
		asset.Models[0].Meshes[0].Name != "Submesh_0" {
		t.Fatal(mustJSON(t, changed))
	}
	if &changed.Models[0].Meshes[0].Indices[0] != &asset.Models[0].Meshes[0].Indices[0] {
		t.Fatal("geometry arrays copied")
	}
	if &changed.ExportHierarchy[0] == &asset.ExportHierarchy[0] {
		t.Fatal("graph node shared")
	}
}

func identity() formats.Mat4 {
	var m formats.Mat4
	for i := range m {
		m[i] = float64(i)
	}
	return m
}

func graphFixture() *formats.Asset {
	asset := fixture()
	asset.ExportHierarchy = []formats.Node{
		{ID: "model-0:entity", Type: "entity", ModelID: "model-0", Name: "Gameplay1P"},
		{ID: "model-0:model", Type: "model", ModelID: "model-0", ParentID: ptr("model-0:entity"), Name: "Source arms"},
		{ID: "model-0:bone:0", Type: "bone", ModelID: "model-0", ParentID: ptr("model-0:model"), Name: "bone_0001",
			SourceName: ptr("bone_0001"), Matrix: identity()},
		{ID: "model-1:mesh:0", Type: "mesh", ModelID: "model-1", MeshIndex: ptr(0), ParentID: ptr("model-0:bone:0"),
			Name: "Submesh_0", Matrix: identity()},
	}
	return asset
}

func TestExportGraphNamesChangeWithoutChangingIdentifiersOrTransforms(t *testing.T) {
	asset := graphFixture()
	original := mustJSON(t, asset)
	changed := apply(t, asset, names("scene", "Named scene", "model:model-0", "Named model", "mesh:model-1:0", "Named mesh"))
	var got []string
	for i, node := range changed.ExportHierarchy {
		got = append(got, node.Name)
		before := asset.ExportHierarchy[i]
		before.Name = node.Name
		if mustJSON(t, before) != mustJSON(t, node) {
			t.Fatal("node changed")
		}
	}
	if !reflect.DeepEqual(got, []string{"Named scene", "Named model", "bone_0001", "Named mesh"}) {
		t.Fatal(got)
	}
	if mustJSON(t, asset) != original {
		t.Fatal("source changed")
	}
}

// --- Oracle comparisons -----------------------------------------------------

const namesPython = `
from owpreview import object_names as o
out = []
for kind, call_args in args[0]:
    try:
        if kind == 'name':
            value = o.validate_name(*call_args)
        elif kind == 'keys':
            value = sorted(o.object_name_keys(*call_args))
        elif kind == 'validate':
            value = o.validate_names(*call_args)
        else:
            value = o.apply_object_names(*call_args)
        out.append({'ok': True, 'result': value})
    except Exception as error:
        out.append({'ok': False, 'error': str(error)})
result = out
`

type pyResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func TestObjectNamesMatchPython(t *testing.T) {
	type call struct {
		label string
		kind  string
		args  []any
		run   func() (any, error)
	}
	var calls []call
	for _, value := range append([]any{"  Arms / modèle: 日本語 😀  ", strings.Repeat("a", 160), strings.Repeat("😀", 80),
		" Em space　", "Tab inside"}, invalidNames...) {
		if s, ok := value.(string); ok && strings.Contains(s, "\xed") {
			continue // a lone surrogate cannot be sent as JSON text
		}
		value := value
		calls = append(calls, call{"name", "name", []any{value}, func() (any, error) { return objectnames.ValidateName(value) }})
	}
	compact := compactMap()
	assets := map[string]*formats.Asset{"entity": fixture(), "graph": graphFixture(), "compact": compact}
	mapAsset := fixture()
	mapAsset.Models = mapAsset.Models[:1]
	mapAsset.Map = map[string]any{"instanceCount": 1}
	mapAsset.ExportHierarchy = []formats.Node{{ID: "instance", Type: "entity", ModelID: "model-0", Name: "Source instance"}}
	assets["map"] = mapAsset
	standalone := fixture()
	standalone.Models = standalone.Models[:1]
	standalone.Models[0].IsEntity = ptr(false)
	assets["standalone"] = standalone
	broken := compactMap()
	broken.Models[0].MeshPrototype = "missing"
	assets["broken"] = broken
	root := exporttest.TestsDir() + "/demo-assets"
	demo, err := formats.LoadAsset(root+"/Entities/PreviewRobot/PreviewRobot.owentity", root)
	if err != nil {
		t.Fatal(err)
	}
	assets["demo"] = demo
	nameSets := []*ordered.Map[string]{
		names("scene", " Reload setup ", "model:model-0", "Arms", "mesh:model-1:0", "Weapon shell"),
		names("scene", "New scene"),
		names("scene", "Named", "model:model-0", "Specific", "mesh:model-0:0", "Wall"),
		names("mesh:model-1:0", "Wall by spawn"),
		names("bone:model-0:0", "New"),
		names("model:missing", "New"),
		names("scene", "a\nb"),
		names("mesh:model-0:99", "Nope"),
		nil,
	}
	labels := make([]string, 0, len(assets))
	for label := range assets {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		asset := assets[label]
		py := exporttest.PyJSON(t, asset)
		calls = append(calls, call{label + " keys", "keys", []any{py}, func() (any, error) {
			set, err := objectnames.ObjectNameKeys(asset)
			var keys []string
			for k := range set {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys, err
		}})
		for i, set := range nameSets {
			set := set
			var pyNames any = set
			if set == nil {
				pyNames = nil
			}
			calls = append(calls, call{label + " validate " + string(rune('0'+i)), "validate", []any{pyNames, py},
				func() (any, error) {
					if set == nil {
						return objectnames.ValidateNames(nil, asset)
					}
					return objectnames.ValidateNames(set, asset)
				}})
			if set == nil {
				continue
			}
			calls = append(calls, call{label + " apply " + string(rune('0'+i)), "apply", []any{py, set},
				func() (any, error) { return objectnames.ApplyObjectNames(asset, set) }})
		}
	}
	calls = append(calls, call{"not a dict", "validate", []any{[]any{"x"}}, func() (any, error) {
		return objectnames.ValidateNames([]any{"x"}, nil)
	}})
	calls = append(calls, call{"no asset", "validate", []any{names("model:anything", "x")}, func() (any, error) {
		return objectnames.ValidateNames(names("model:anything", "x"), nil)
	}})
	var requests []any
	for _, c := range calls {
		requests = append(requests, []any{c.kind, c.args})
	}
	raw := oracle.Exec(t, namesPython, requests).MustValue(t)
	var results []pyResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, c := range calls {
		want := results[i]
		got, err := c.run()
		if !want.OK {
			if err == nil || err.Error() != want.Error {
				t.Errorf("%s: got %v, want error %q", c.label, err, want.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.label, err)
			continue
		}
		jsoncmp.Equal(t, want.Result, got, jsoncmp.Default)
	}
}
