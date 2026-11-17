package c4dexport_test

// Ports of tests/test_c4d_export.py (payload parts), tests/test_export_normals.py
// (ExportNormalTests), tests/test_selected_object_export.py and
// tests/test_combined_export.py. Checks that need the fake Cinema 4D API of
// tests/test_c4d_scene.py run the unchanged c4d_scene.py on the Go payload
// through the oracle (oracle_test.go).

import (
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/ordered"
)

func ptr[T any](v T) *T { return &v }

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func expectError(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error matching %q", pattern)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("error %q does not match %q", err, pattern)
	}
}

func defaults() c4dexport.PayloadOptions { return c4dexport.DefaultPayloadOptions() }

func materialFixture(t *testing.T) (*formats.Asset, *formats.Clip) {
	root := filepath.Join(exporttest.TestsDir(), "material-assets")
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/MaterialRobot/MaterialRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	clip, err := formats.LoadClip(filepath.Join(root, "Animations/001_Wave.owanimclip"))
	if err != nil {
		t.Fatal(err)
	}
	return asset, clip
}

func frameMatrix(values formats.Mat4) exportpose.Mat4 { return exportpose.FromColumnMajor(values) }

// --- tests/test_c4d_export.py ----------------------------------------------

func TestPayloadPreservesBoneIndicesHierarchyAndEvaluatedAnimation(t *testing.T) {
	asset, clip := materialFixture(t)
	opts := defaults()
	opts.ClipName = "Wave"
	payload, err := c4dexport.MakePayload(asset, clip, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, payload.Asset.ExportHierarchy) != mustJSON(t, asset.ExportHierarchy) {
		t.Fatal("export hierarchy changed")
	}
	if payload.Name != "Wave" {
		t.Fatal(payload.Name)
	}
	ev, err := exportpose.NewEvaluator(asset.Models[0].Bones, clip.Bones)
	if err != nil {
		t.Fatal(err)
	}
	frame := clip.LastFrame / 4
	expected := ev.Evaluate(float64(frame), false).World
	world := make([]exportpose.Mat4, len(ev.Bones))
	for _, i := range ev.Order {
		local := frameMatrix(payload.Animation.Tracks[i].Frames[frame])
		if ev.Parents[i] < 0 {
			world[i] = local
		} else {
			world[i] = world[ev.Parents[i]].Mul(&local)
		}
		exporttest.AssertClose(t, "world", world[i], expected[i], 1e-8)
	}
}

// Payload-level part of test_c4d_root_motion_comes_from_export_option_not_preview_state
// (the BrowserAPI plumbing is ported with the API).
func TestRootMotionComesFromExportOption(t *testing.T) {
	root := filepath.Join(exporttest.TestsDir(), "demo-assets")
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/PreviewRobot/PreviewRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	clip, err := formats.LoadClip(filepath.Join(root, "Animations/003_Root_Move.owanimclip"))
	if err != nil {
		t.Fatal(err)
	}
	opts := defaults()
	opts.IncludeMesh = false
	free, err := c4dexport.MakePayload(asset, clip, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.RootMotion = false
	locked, err := c4dexport.MakePayload(asset, clip, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := exportpose.NewEvaluator(asset.Models[0].Bones, clip.Bones)
	if err != nil {
		t.Fatal(err)
	}
	rootIndex := -1
	for i, p := range evaluator.Parents {
		if p == -1 {
			rootIndex = i
			break
		}
	}
	for _, c := range []struct {
		payload *c4dexport.Payload
		locked  bool
	}{{free, false}, {locked, true}} {
		var track *c4dexport.Track
		for i := range c.payload.Animation.Tracks {
			if c.payload.Animation.Tracks[i].BoneIndex == rootIndex {
				track = &c.payload.Animation.Tracks[i]
			}
		}
		for _, frame := range []int64{0, clip.LastFrame / 2, clip.LastFrame} {
			expected := evaluator.Evaluate(float64(frame), c.locked).World[rootIndex]
			exporttest.AssertClose(t, "root", frameMatrix(track.Frames[frame]), expected, 1e-8)
		}
	}
	largest := 0.0
	for frame := range free.Animation.Tracks[rootIndex].Frames {
		a, b := free.Animation.Tracks[rootIndex].Frames[frame], locked.Animation.Tracks[rootIndex].Frames[frame]
		largest = math.Max(largest, math.Hypot(a[12]-b[12], a[14]-b[14]))
	}
	if largest <= .1 {
		t.Fatalf("root motion difference %v", largest)
	}
}

func setNormals(asset *formats.Asset) {
	sample := [3][3]float32{{.1, .2, .96}, {-.3, .9, .1}, {.7, -.2, -.6}}
	for _, model := range asset.Models {
		for _, mesh := range model.Meshes {
			mesh.Normals = make([]float32, len(mesh.Positions))
			for i := 0; i < len(mesh.Positions)/3; i++ {
				copy(mesh.Normals[i*3:], sample[i%3][:])
			}
		}
	}
}

func TestCustomNormalsMatchBlenderUnitDirectionsWithoutMutatingAssets(t *testing.T) {
	asset, clip := materialFixture(t)
	setNormals(asset)
	original := mustJSON(t, asset)
	payload, err := c4dexport.MakePayload(asset, clip, nil, defaults())
	if err != nil {
		t.Fatal(err)
	}
	for m, before := range asset.Models {
		after := payload.Asset.Models[m]
		for k, mesh := range before.Meshes {
			got := after.Meshes[k]
			for i := 0; i < len(mesh.Normals); i += 3 {
				raw := [3]float64{float64(mesh.Normals[i]), float64(mesh.Normals[i+1]), float64(mesh.Normals[i+2])}
				length := math.Sqrt(raw[0]*raw[0] + raw[1]*raw[1] + raw[2]*raw[2])
				actual := [3]float64{float64(got.Normals[i]), float64(got.Normals[i+1]), float64(got.Normals[i+2])}
				// Float32 storage (see the package doc): unit length to 1e-7.
				if l := math.Sqrt(actual[0]*actual[0] + actual[1]*actual[1] + actual[2]*actual[2]); math.Abs(l-1) > 1e-7 {
					t.Fatalf("normal length %v", l)
				}
				for c := 0; c < 3; c++ {
					if math.Abs(actual[c]-raw[c]/length) > 1e-7 {
						t.Fatalf("normal %v, want %v", actual, raw)
					}
				}
			}
			a, b := *mesh, *got
			a.Normals, b.Normals = nil, nil
			if mustJSON(t, a) != mustJSON(t, b) {
				t.Fatal("other mesh data changed")
			}
		}
	}
	if mustJSON(t, asset) != original {
		t.Fatal("the source asset was mutated")
	}
}

func TestInvalidNormalDataIsRejectedOnlyWhenExportingMeshes(t *testing.T) {
	for _, normals := range [][]float32{{float32(math.NaN()), 0, 1}, {float32(math.Inf(1)), 0, 1}, {1, 0}} {
		asset, clip := materialFixture(t)
		mesh := asset.Models[0].Meshes[0]
		count := len(mesh.Positions) / 3
		mesh.Normals = nil
		for i := 0; i < count; i++ {
			mesh.Normals = append(mesh.Normals, normals...)
		}
		_, err := c4dexport.MakePayload(asset, clip, nil, defaults())
		expectError(t, err, "normal")
		opts := defaults()
		opts.IncludeMesh = false
		payload, err := c4dexport.MakePayload(asset, clip, nil, opts)
		if err != nil || payload.IncludeMesh {
			t.Fatal(err)
		}
	}
}

func TestUnrepresentableShearIsExplicitNotSilentlyLost(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	asset := &formats.Asset{Models: []*formats.Model{{ID: "rig", Name: "Rig", Bones: fixture.Bones, Meshes: []*formats.Mesh{}}}}
	graph, err := formats.BuildExportHierarchy(asset)
	if err != nil {
		t.Fatal(err)
	}
	asset.ExportHierarchy = graph
	_, err = c4dexport.MakePayload(asset, exporttest.Clip("", 30, 20, fixture.Tracks), nil, defaults())
	expectError(t, err, "nonuniform scale")
}

func TestActorScaleBakePreservesWorldPivotsAndRotations(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	model := &formats.Model{ID: "rig", Name: "Rig", Bones: fixture.Bones}
	asset := &formats.Asset{Models: []*formats.Model{model}}
	graph, err := formats.BuildExportHierarchy(asset)
	if err != nil {
		t.Fatal(err)
	}
	asset.ExportHierarchy = graph
	clip := exporttest.Clip("Idle", 30, 20, fixture.Tracks)
	assignments := []c4dexport.Assignment{{ModelID: model.ID, Clip: clip}}
	opts := c4dexport.SetOptions{Name: "Idle", RootMotion: true}
	_, err = c4dexport.MakeSetPayload(asset, assignments, nil, opts)
	expectError(t, err, "nonuniform scale")

	opts.BakeScaleToJointPositions = true
	payload, err := c4dexport.MakeSetPayload(asset, assignments, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Warnings) != 1 || !strings.Contains(payload.Warnings[0], "Direct scale deformation is omitted") {
		t.Fatalf("Missing scale warning: %v", payload.Warnings)
	}
	evaluator, err := exportpose.NewEvaluator(model.Bones, clip.Bones)
	if err != nil {
		t.Fatal(err)
	}
	initial := evaluator.Evaluate(0, false)
	for frame := int64(0); frame <= clip.LastFrame; frame++ {
		pose := evaluator.Evaluate(float64(frame), false)
		world := make([]exportpose.Mat4, len(model.Bones))
		for _, i := range evaluator.Order {
			local := frameMatrix(payload.Animation.Tracks[i].Frames[frame])
			if _, _, _, err := exportpose.Decompose(&local); err != nil {
				t.Fatalf("Non-PSR local on joint %d at frame %d: %v", i, frame, err)
			}
			world[i] = local
			if parent := evaluator.Parents[i]; parent >= 0 {
				world[i] = world[parent].Mul(&local)
			}
			for row := 0; row < 3; row++ {
				for col := 0; col < 4; col++ {
					want := pose.World[i][row][col]
					if col < 3 {
						want = want / pose.Scales[i][col] * initial.Scales[i][col]
					}
					if math.Abs(world[i][row][col]-want) > 1e-10 {
						t.Fatalf("Joint %d frame %d [%d,%d]: got %.12f, want %.12f", i, frame, row, col, world[i][row][col], want)
					}
				}
			}
		}
	}
	// A nonuniform, rotated parent already present at frame zero still
	// cannot be represented without changing the original hierarchy.
	copy(clip.Bones[0].Scales.Values[:3], []float64{2, 0.5, 3})
	_, err = c4dexport.MakeSetPayload(asset, assignments, nil, opts)
	expectError(t, err, "nonuniform scale")
}

// --- tests/test_export_normals.py ------------------------------------------

func meshFixture(positions [][3]float64, indices []uint32, normals [][3]float64) *formats.Mesh {
	flat := func(values [][3]float64) []float32 {
		out := []float32{}
		for _, v := range values {
			out = append(out, float32(v[0]), float32(v[1]), float32(v[2]))
		}
		return out
	}
	return &formats.Mesh{Name: "Authored mesh", Positions: flat(positions), Indices: indices, Normals: flat(normals),
		UVs: []float32{}, UVs2: []float32{}, MaterialKey: "4000", SkinIndices: []uint16{}, SkinWeights: []float64{}}
}

func normalAsset(mesh *formats.Mesh) *formats.Asset {
	return &formats.Asset{Models: []*formats.Model{{ID: "body", Name: "Body", Bones: []formats.Bone{},
		Meshes: []*formats.Mesh{mesh}}}, Warnings: []string{"Existing source note"}}
}

func repair(t *testing.T, mesh *formats.Mesh) (*formats.Mesh, *formats.Asset) {
	t.Helper()
	asset := normalAsset(mesh)
	before := mustJSON(t, asset)
	result, err := c4dexport.NormalizedNormalAsset(asset)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, asset) != before {
		t.Fatal("Export preparation must not mutate preview/source arrays.")
	}
	if !contains(result.Warnings, "Existing source note") {
		t.Fatal(result.Warnings)
	}
	repaired := result.Models[0].Meshes[0]
	a, b := *repaired, *mesh
	a.Normals, b.Normals = nil, nil
	if mustJSON(t, a) != mustJSON(t, b) {
		t.Fatal("non-normal data changed")
	}
	for i := 0; i < len(repaired.Normals); i += 3 {
		n := repaired.Normals[i : i+3]
		if l := math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])); math.Abs(l-1) > 1e-6 {
			t.Fatalf("normal %v has length %v", n, l)
		}
	}
	return repaired, result
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func normalWarnings(result *formats.Asset) []string {
	var out []string
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "normal") {
			out = append(out, w)
		}
	}
	return out
}

func assertRepairWarning(t *testing.T, result *formats.Asset, count string) {
	t.Helper()
	warnings := normalWarnings(result)
	if len(warnings) != 1 || !regexp.MustCompile(`(^|\D)`+count+`(\D|$)`).MatchString(warnings[0]) {
		t.Fatalf("warnings %q, want one mentioning %s", warnings, count)
	}
}

func assertNormals(t *testing.T, got []float32, want [][3]float64) {
	t.Helper()
	if len(got) != 3*len(want) {
		t.Fatalf("got %d normal components, want %d", len(got), 3*len(want))
	}
	for i, w := range want {
		for c := 0; c < 3; c++ {
			if math.Abs(float64(got[i*3+c])-w[c]) > 1e-7 {
				t.Fatalf("normal %d = %v, want %v", i, got[i*3:i*3+3], w)
			}
		}
	}
}

func TestValidAuthoredDirectionsAreNormalizedWithoutRepairWarning(t *testing.T) {
	authored := [][3]float64{{.1, .2, .96}, {-.3, .9, .1}, {.7, -.2, -.6}}
	repaired, result := repair(t, meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2}, authored))
	var want [][3]float64
	for _, v := range authored {
		f := [3]float64{float64(float32(v[0])), float64(float32(v[1])), float64(float32(v[2]))}
		l := math.Sqrt(f[0]*f[0] + f[1]*f[1] + f[2]*f[2])
		want = append(want, [3]float64{f[0] / l, f[1] / l, f[2] / l})
	}
	assertNormals(t, repaired.Normals, want)
	if !reflect.DeepEqual(result.Warnings, []string{"Existing source note"}) {
		t.Fatal(result.Warnings)
	}
}

func TestZeroDirectionUsesCornerAnglesNotTriangleArea(t *testing.T) {
	// The huge +Z face contributes 90 degrees; the small -X face contributes
	// 60 degrees. Area weighting would incorrectly make this almost +Z.
	mesh := meshFixture([][3]float64{{0, 0, 0}, {100, 0, 0}, {0, 100, 0}, {0, 0, 1}, {0, math.Sqrt(3) / 2, .5}},
		[]uint32{0, 1, 2, 0, 3, 4}, [][3]float64{{0, 0, 0}, {.2, .3, .4}, {.2, .3, .4}, {.2, .3, .4}, {.2, .3, .4}})
	repaired, result := repair(t, mesh)
	s13, s29 := math.Sqrt(13), math.Sqrt(29)
	assertNormals(t, repaired.Normals, [][3]float64{{-2 / s13, 0, 3 / s13}, {2 / s29, 3 / s29, 4 / s29},
		{2 / s29, 3 / s29, 4 / s29}, {2 / s29, 3 / s29, 4 / s29}, {2 / s29, 3 / s29, 4 / s29}})
	assertRepairWarning(t, result, "1")
}

func TestThinTriangleLikeRealMapStillSuppliesADirection(t *testing.T) {
	mesh := meshFixture([][3]float64{{0, 0, 0}, {0, 1.69530439376831, -2.9802322387695312e-8}, {0, 1.69530439376831, 0}},
		[]uint32{0, 1, 2}, [][3]float64{{0, 0, 0}, {2, 0, 0}, {2, 0, 0}})
	repaired, result := repair(t, mesh)
	assertNormals(t, repaired.Normals, [][3]float64{{1, 0, 0}, {1, 0, 0}, {1, 0, 0}})
	assertRepairWarning(t, result, "1")
}

func TestCoincidentSplitVerticesKeepDistinctSurfaceDirections(t *testing.T) {
	mesh := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 0}, {0, 1, 0}, {0, 0, 1}},
		[]uint32{0, 1, 2, 3, 4, 5}, [][3]float64{{0, 0, 0}, {0, 2, 0}, {0, 2, 0}, {0, 0, 0}, {0, 2, 0}, {0, 2, 0}})
	repaired, result := repair(t, mesh)
	assertNormals(t, repaired.Normals, [][3]float64{{0, 0, 1}, {0, 1, 0}, {0, 1, 0}, {1, 0, 0}, {0, 1, 0}, {0, 1, 0}})
	assertRepairWarning(t, result, "2")
}

func TestCancellingAngleSumUsesLargestIncidentFace(t *testing.T) {
	// Opposite 90-degree contributions cancel; the +Z face has 4x the area.
	mesh := meshFixture([][3]float64{{0, 0, 0}, {2, 0, 0}, {0, 2, 0}, {0, 1, 0}, {1, 0, 0}},
		[]uint32{0, 3, 4, 0, 1, 2}, [][3]float64{{0, 0, 0}, {0, 0, 2}, {0, 0, 2}, {0, 0, 2}, {0, 0, 2}})
	repaired, result := repair(t, mesh)
	assertNormals(t, repaired.Normals[:3], [][3]float64{{0, 0, 1}})
	assertRepairWarning(t, result, "1")
}

func TestOpposingRealMapFacesFollowBlenderAngleWeighting(t *testing.T) {
	// The exported 11770 mesh has almost-cancelling opposing faces. Blender
	// gives vertex 2 a -Y automatic normal; area weighting would give +Y.
	mesh := meshFixture([][3]float64{{7.152557373046875e-6, 24.827350616455078, -17.362180709838867},
		{21.095237731933594, 24.827350616455078, -2.1153931617736816},
		{-.13991379737854004, 24.827350616455078, -17.3621768951416},
		{21.095243453979492, 24.827350616455078, -17.362171173095703}},
		[]uint32{0, 1, 2, 3, 2, 1}, [][3]float64{{0, .9921875, 0}, {0, .9921875, 0}, {0, 0, 0}, {0, .9921875, 0}})
	repaired, result := repair(t, mesh)
	assertNormals(t, repaired.Normals, [][3]float64{{0, 1, 0}, {0, 1, 0}, {0, -1, 0}, {0, 1, 0}})
	assertRepairWarning(t, result, "1")
}

func TestDegenerateOnlyAndUnusedVerticesGetFiniteFallbacks(t *testing.T) {
	mesh := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {2, 0, 0}, {7, 8, 9}},
		[]uint32{0, 1, 2, 0, 0, 1}, [][3]float64{{0, 0, 0}, {0, 0, 2}, {0, 0, 2}, {0, 0, 0}})
	repaired, result := repair(t, mesh)
	assertNormals(t, repaired.Normals, [][3]float64{{0, 1, 0}, {0, 0, 1}, {0, 0, 1}, {0, 1, 0}})
	found := false
	for _, w := range normalWarnings(result) {
		if strings.Contains(w, "2") && strings.Contains(strings.ToLower(w), "placeholder") {
			found = true
		}
	}
	if !found {
		t.Fatal(result.Warnings)
	}
}

func TestAbsentNormalsStillUseTheNativeDefault(t *testing.T) {
	mesh := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2}, nil)
	before := mustJSON(t, mesh)
	result, err := c4dexport.NormalizedNormalAsset(normalAsset(mesh))
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, mesh) != before || len(result.Models[0].Meshes[0].Normals) != 0 || result.Models[0].Meshes[0] != mesh {
		t.Fatal("absent normals changed")
	}
	if !reflect.DeepEqual(result.Warnings, []string{"Existing source note"}) {
		t.Fatal(result.Warnings)
	}
}

func TestNonfiniteNormalsAreNotSilentlyRepaired(t *testing.T) {
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		mesh := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2},
			[][3]float64{{invalid, 0, 0}, {0, 0, 1}, {0, 0, 1}})
		_, err := c4dexport.NormalizedNormalAsset(normalAsset(mesh))
		expectError(t, err, "normal")
	}
}

// Python also rejects negative and fractional indices; uint32 indices cannot
// hold them.
func TestMalformedArraysAndTopologyRemainErrors(t *testing.T) {
	base := func() *formats.Mesh {
		return meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2},
			[][3]float64{{0, 0, 0}, {0, 0, 1}, {0, 0, 1}})
	}
	for _, change := range []func(*formats.Mesh){
		func(m *formats.Mesh) { m.Normals = []float32{0, 0} },
		func(m *formats.Mesh) { m.Normals = make([]float32, 6) },
		func(m *formats.Mesh) { m.Positions = make([]float32, 8) },
		func(m *formats.Mesh) { m.Indices = []uint32{0, 1} },
		func(m *formats.Mesh) { m.Indices = []uint32{0, 1, 3} },
	} {
		mesh := base()
		change(mesh)
		if _, err := c4dexport.NormalizedNormalAsset(normalAsset(mesh)); err == nil {
			t.Fatalf("accepted %+v", mesh)
		}
	}
}

// --- tests/test_selected_object_export.py ----------------------------------

func modelIDs(asset *formats.Asset) []string {
	var ids []string
	for _, m := range asset.Models {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestPrimaryProjectionExcludesChildrenAndKeepsCompleteOriginalRig(t *testing.T) {
	asset, _ := exporttest.SelectedFixture(t)
	projected, err := c4dexport.SelectModelAsset(asset, "arms")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(modelIDs(projected), []string{"arms"}) {
		t.Fatal(modelIDs(projected))
	}
	var want []formats.Node
	for _, node := range asset.ExportHierarchy {
		if node.ModelID == "arms" {
			want = append(want, node)
		}
	}
	if mustJSON(t, projected.ExportHierarchy) != mustJSON(t, want) {
		t.Fatal("hierarchy differs")
	}
	if mustJSON(t, projected.Models[0].Bones) != mustJSON(t, asset.Models[0].Bones) ||
		mustJSON(t, projected.Models[0].Meshes) != mustJSON(t, asset.Models[0].Meshes) {
		t.Fatal("rig or meshes differ")
	}
	if !reflect.DeepEqual(projected.Textures.Keys(), []string{"arms"}) {
		t.Fatal(projected.Textures.Keys())
	}
}

func TestAttachedProjectionPreservesNativeRestWorldPlacementWithoutParentRig(t *testing.T) {
	asset, _ := exporttest.SelectedFixture(t)
	original := mustJSON(t, asset)
	projected, err := c4dexport.SelectModelAsset(asset, "gun")
	if err != nil {
		t.Fatal(err)
	}
	model := projected.Models[0]
	if !reflect.DeepEqual(modelIDs(projected), []string{"gun"}) || model.ParentID != nil || model.AttachmentBone != nil {
		t.Fatal("projection keeps its parent")
	}
	bones := 0
	for _, node := range projected.ExportHierarchy {
		if node.ModelID != "gun" {
			t.Fatal(node.ModelID)
		}
		if node.Type == "bone" {
			bones++
		}
	}
	if bones != 2 {
		t.Fatal(bones)
	}
	before := exporttest.WorldMatrices(t, asset.ExportHierarchy, nil)
	after := exporttest.WorldMatrices(t, projected.ExportHierarchy, nil)
	for _, node := range projected.ExportHierarchy {
		exporttest.AssertClose(t, node.ID, after[node.ID], before[node.ID], 1e-12)
		for _, old := range asset.ExportHierarchy {
			if old.ID == node.ID && old.ParentID != nil {
				if _, inside := after[*old.ParentID]; inside && mustJSON(t, node) != mustJSON(t, old) {
					t.Fatal("Internal hierarchy and authored names stay unchanged.")
				}
			}
		}
	}
	if mustJSON(t, model.Bones) != mustJSON(t, asset.Models[1].Bones) || mustJSON(t, model.Meshes) != mustJSON(t, asset.Models[1].Meshes) {
		t.Fatal("rig or meshes differ")
	}
	if mustJSON(t, asset) != original {
		t.Fatal("asset mutated")
	}
	model.Meshes[0].Positions[0] = 123
	texture, _ := projected.Textures.Get("gun")
	texture.Path = "changed.png"
	model.Materials.Range(func(_ string, m *formats.Material) bool { m.Name = "changed"; return true })
	if mustJSON(t, asset) != original {
		t.Fatal("Export projection must not alias writable mesh/material state.")
	}
}

func TestScopedAnimationUsesOnlySelectedBonesAndFrozenParentRestPlacement(t *testing.T) {
	asset, clip := exporttest.SelectedFixture(t)
	all := ordered.New[*ordered.Map[*formats.Material]]()
	for _, model := range asset.Models {
		all.Set(model.ID, model.Materials)
	}
	opts := defaults()
	opts.ScopeModelID = ptr("gun")
	payload, err := c4dexport.MakePayload(asset, clip, all, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Materials.Keys(), []string{"gun"}) {
		t.Fatal(payload.Materials.Keys())
	}
	gun, _ := payload.Materials.Get("gun")
	if !reflect.DeepEqual(gun.Keys(), []string{"mat-gun"}) {
		t.Fatal(gun.Keys())
	}
	if len(payload.Animation.Tracks) != 2 {
		t.Fatal(len(payload.Animation.Tracks))
	}
	for _, track := range payload.Animation.Tracks {
		if track.ModelID != "gun" {
			t.Fatal(track.ModelID)
		}
	}
	evaluator, err := exportpose.NewEvaluator(asset.Models[1].Bones, clip.Bones)
	if err != nil {
		t.Fatal(err)
	}
	rest := exporttest.WorldMatrices(t, asset.ExportHierarchy, nil)
	for frame := 0; frame < 3; frame++ {
		replacements := map[string]formats.Mat4{}
		for _, track := range payload.Animation.Tracks {
			replacements["gun:bone:"+itoa(track.BoneIndex)] = track.Frames[frame]
		}
		actual := exporttest.WorldMatrices(t, payload.Asset.ExportHierarchy, replacements)
		expected := evaluator.Evaluate(float64(frame), false).World
		skeleton := rest["gun:skeleton"]
		for index := 0; index < 2; index++ {
			exporttest.AssertClose(t, "bone", actual["gun:bone:"+itoa(index)], skeleton.Mul(&expected[index]), 1e-12)
		}
	}
	// Shared raw joint names on the other rigs must not create other tracks.
	if payload.Animation.MatchedBones != 2 {
		t.Fatal(payload.Animation.MatchedBones)
	}
}

var itoa = strconv.Itoa

func TestDefaultWholeEntityExportRemainsCompatible(t *testing.T) {
	asset, clip := exporttest.SelectedFixture(t)
	opts := defaults()
	opts.RigID = "gun"
	payload, err := c4dexport.MakePayload(asset, clip, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Asset.Models) != 3 || mustJSON(t, payload.Asset.ExportHierarchy) != mustJSON(t, asset.ExportHierarchy) {
		t.Fatal("whole entity changed")
	}
}

func TestMissingSelectionAndConflictingTargetFailExplicitly(t *testing.T) {
	asset, clip := exporttest.SelectedFixture(t)
	_, err := c4dexport.SelectModelAsset(asset, "missing")
	expectError(t, err, "valid object")
	opts := defaults()
	opts.RigID, opts.ScopeModelID = "arms", ptr("gun")
	_, err = c4dexport.MakePayload(asset, clip, nil, opts)
	expectError(t, err, "animation target")
	var kept []formats.Node
	for _, node := range asset.ExportHierarchy {
		if node.ModelID != "gun" {
			kept = append(kept, node)
		}
	}
	asset.ExportHierarchy = kept
	_, err = c4dexport.SelectModelAsset(asset, "gun")
	expectError(t, err, "missing from")
}

func nodeByID(asset *formats.Asset, id string) *formats.Node {
	for i := range asset.ExportHierarchy {
		if asset.ExportHierarchy[i].ID == id {
			return &asset.ExportHierarchy[i]
		}
	}
	return nil
}

func TestInvalidExternalAttachmentIsNotSilentlyDiscarded(t *testing.T) {
	asset, _ := exporttest.SelectedFixture(t)
	nodeByID(asset, "gun:entity").ParentID = ptr("missing-parent")
	_, err := c4dexport.SelectModelAsset(asset, "gun")
	expectError(t, err, "missing parent")
	asset, _ = exporttest.SelectedFixture(t)
	nodeByID(asset, "arms:bone:1").Matrix[4] = .4
	_, err = c4dexport.SelectModelAsset(asset, "gun")
	expectError(t, err, "rest attachment transform")
}

// --- tests/test_combined_export.py -----------------------------------------

func assignments(list []exporttest.Assignment) []c4dexport.Assignment {
	out := make([]c4dexport.Assignment, len(list))
	for i, a := range list {
		out[i] = c4dexport.Assignment{ModelID: a.ModelID, Clip: a.Clip}
	}
	return out
}

func setDefaults() c4dexport.SetOptions { return c4dexport.DefaultSetOptions() }

func animatedWorlds(t *testing.T, payload *c4dexport.Payload, frame int) map[string]exportpose.Mat4 {
	overrides := map[string]formats.Mat4{}
	for _, track := range payload.Animation.Tracks {
		overrides[track.ModelID+":bone:"+itoa(track.BoneIndex)] = track.Frames[frame]
	}
	return exporttest.WorldMatrices(t, payload.Asset.ExportHierarchy, overrides)
}

func TestSetKeepsFullHierarchyNamesMeshesAndDoesNotMutateInputs(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	original := mustJSON(t, []any{asset, list[0].Clip, list[1].Clip})
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	for _, model := range asset.Models {
		materials.Set(model.ID, model.Materials)
	}
	opts := setDefaults()
	opts.Name = "Reload set"
	payload, err := c4dexport.MakeSetPayload(asset, assignments(list), materials, opts)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || payload.Name != "Reload set" || payload.Materials != materials {
		t.Fatal("payload header differs")
	}
	if mustJSON(t, payload.Asset.ExportHierarchy) != mustJSON(t, asset.ExportHierarchy) {
		t.Fatal("hierarchy differs")
	}
	if !reflect.DeepEqual(modelIDs(payload.Asset), []string{"arms", "gun", "charm"}) {
		t.Fatal(modelIDs(payload.Asset))
	}
	if mustJSON(t, []any{asset, list[0].Clip, list[1].Clip}) != original {
		t.Fatal("inputs mutated")
	}
	var keys []string
	for _, track := range payload.Animation.Tracks {
		keys = append(keys, track.ModelID+":"+itoa(track.BoneIndex))
	}
	if !reflect.DeepEqual(keys, []string{"arms:0", "arms:1", "gun:0", "gun:1"}) {
		t.Fatal(keys)
	}
	if payload.Animation.MatchedBones != 4 || !reflect.DeepEqual(payload.Animation.Assignments[1].UnmatchedBoneNames, []string{"bone_0075"}) {
		t.Fatal(payload.Animation)
	}
}

func trackFor(payload *c4dexport.Payload, model string, bone int) *c4dexport.Track {
	for i := range payload.Animation.Tracks {
		if payload.Animation.Tracks[i].ModelID == model && payload.Animation.Tracks[i].BoneIndex == bone {
			return &payload.Animation.Tracks[i]
		}
	}
	return nil
}

func modelByID(asset *formats.Asset, id string) *formats.Model {
	for _, m := range asset.Models {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func TestSharedNamesAreScopedAndShorterClipHoldsWithoutStretching(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	payload, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Animation.FPS != 30 || payload.Animation.LastFrame != 30 {
		t.Fatal(payload.Animation.FPS, payload.Animation.LastFrame)
	}
	for _, a := range list {
		bones := modelByID(asset, a.ModelID).Bones
		evaluator, err := exportpose.NewEvaluator(bones, a.Clip.Bones)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range []int{0, 7, 15, 23, 30} {
			expected := evaluator.Evaluate(math.Min(float64(frame)/30*a.Clip.FPS, float64(a.Clip.LastFrame)), false).World
			actual := make([]exportpose.Mat4, len(bones))
			for _, index := range evaluator.Order {
				local := frameMatrix(trackFor(payload, a.ModelID, index).Frames[frame])
				if parent := evaluator.Parents[index]; parent < 0 {
					actual[index] = local
				} else {
					actual[index] = actual[parent].Mul(&local)
				}
				exporttest.AssertClose(t, "world", actual[index], expected[index], 1e-12)
			}
		}
	}
	if trackFor(payload, "gun", 0).Frames[15] != trackFor(payload, "gun", 0).Frames[30] {
		t.Fatal("shorter clip does not hold")
	}
	if trackFor(payload, "arms", 0).Frames[15] == trackFor(payload, "arms", 0).Frames[30] {
		t.Fatal("longer clip holds")
	}
}

func TestMovingParentAttachmentAndUnassignedCharmInheritMotion(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	payload, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	if err != nil {
		t.Fatal(err)
	}
	armsEval, _ := exportpose.NewEvaluator(asset.Models[0].Bones, list[0].Clip.Bones)
	gunEval, _ := exportpose.NewEvaluator(asset.Models[1].Bones, list[1].Clip.Bones)
	charmEval, _ := exportpose.NewEvaluator(asset.Models[2].Bones, nil)
	charmRest := charmEval.Evaluate(0, false).World
	rest := exporttest.WorldMatrices(t, asset.ExportHierarchy, nil)
	for _, frame := range []int{0, 15, 30} {
		actual := animatedWorlds(t, payload, frame)
		arms := armsEval.Evaluate(float64(frame)/30*24, false).World
		gun := gunEval.Evaluate(math.Min(float64(frame), 15), false).World
		skeleton := rest["arms:skeleton"]
		parentHand := skeleton.Mul(&arms[1])
		// Existing native graph attaches gun entity to the animated hand,
		// with each child's complete local skeleton underneath it.
		exporttest.AssertClose(t, "gun entity", actual["gun:entity"], parentHand, 1e-12)
		for index := 0; index < 2; index++ {
			exporttest.AssertClose(t, "gun bone", actual["gun:bone:"+itoa(index)], parentHand.Mul(&gun[index]), 1e-12)
			hand := parentHand.Mul(&gun[1])
			exporttest.AssertClose(t, "charm bone", actual["charm:bone:"+itoa(index)], hand.Mul(&charmRest[index]), 1e-12)
		}
	}
	if animatedWorlds(t, payload, 0)["charm:mesh:0"] == animatedWorlds(t, payload, 30)["charm:mesh:0"] {
		t.Fatal("charm does not move")
	}
}

func TestRootMotionOptionChangesRootsButPreservesParentInheritance(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	free, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	if err != nil {
		t.Fatal(err)
	}
	opts := setDefaults()
	opts.RootMotion = false
	locked, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	freeRoot, lockedRoot := trackFor(free, "arms", 0).Frames, trackFor(locked, "arms", 0).Frames
	if freeRoot[len(freeRoot)-1] == lockedRoot[len(lockedRoot)-1] {
		t.Fatal("root motion has no effect")
	}
	for _, track := range locked.Animation.Tracks {
		if track.BoneIndex != 0 {
			continue
		}
		pos := modelByID(asset, track.ModelID).Bones[0].Pos
		for _, matrix := range track.Frames {
			if matrix[12] != pos[0] || matrix[14] != pos[2] {
				t.Fatalf("locked root moved: %v vs %v", matrix, pos)
			}
		}
	}
	// Locking root translation does not strip the source hand-to-gun link.
	if mustJSON(t, locked.Asset.ExportHierarchy) != mustJSON(t, asset.ExportHierarchy) {
		t.Fatal("hierarchy differs")
	}
	if animatedWorlds(t, locked, 0)["gun:entity"] == animatedWorlds(t, locked, 30)["gun:entity"] {
		t.Fatal("gun does not follow the hand")
	}
}

func TestNonGridDurationRoundsUpAndClampsOnlyTheLastSample(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	list[0].Clip.LastFrame = 23 // 23/24 seconds, not on a 30 FPS grid.
	payload, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Animation.LastFrame != 29 || *payload.Animation.DurationSeconds != 23.0/24 {
		t.Fatal(payload.Animation.LastFrame, *payload.Animation.DurationSeconds)
	}
	evaluator, _ := exportpose.NewEvaluator(asset.Models[0].Bones, list[0].Clip.Bones)
	frames := trackFor(payload, "arms", 0).Frames
	exporttest.AssertClose(t, "final root", frameMatrix(frames[len(frames)-1]), evaluator.Evaluate(23, false).World[0], 1e-12)
}

func TestSelectedObjectExportIsStillIndependent(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	opts := defaults()
	opts.ScopeModelID = ptr("gun")
	payload, err := c4dexport.MakePayload(asset, list[1].Clip, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(modelIDs(payload.Asset), []string{"gun"}) || payload.Animation.LastFrame != 15 {
		t.Fatal("not independent")
	}
	for _, track := range payload.Animation.Tracks {
		if track.ModelID != "gun" {
			t.Fatal(track.ModelID)
		}
	}
}

func TestSetRejectsMissingAmbiguousAndUnmatchedAssignments(t *testing.T) {
	for _, c := range []struct{ name, want string }{{"empty", "at least one"}, {"missing-model", "skeleton"},
		{"duplicate", "one assigned"}, {"missing-clip", "clip data"}, {"no-match", "no matching"},
		{"missing-joint", "missing joints"}} {
		t.Run(c.name, func(t *testing.T) {
			asset, list := exporttest.AssignedFixture(t)
			records := assignments(list)
			switch c.name {
			case "empty":
				records = nil
			case "missing-model":
				records[0].ModelID = "absent"
			case "duplicate":
				records = append(records, records[0])
			case "missing-clip":
				records[0].Clip = nil
			case "no-match":
				records[0].Clip.Bones = []formats.ClipBone{{Name: "bone_FFFF"}}
			case "missing-joint":
				var kept []formats.Node
				for _, node := range asset.ExportHierarchy {
					if node.ID != "gun:bone:0" {
						if node.ParentID != nil && *node.ParentID == "gun:bone:0" {
							node.ParentID = ptr("gun:skeleton")
						}
						kept = append(kept, node)
					}
				}
				asset.ExportHierarchy = kept
			}
			_, err := c4dexport.MakeSetPayload(asset, records, nil, setDefaults())
			expectError(t, err, c.want)
		})
	}
}

// Python's fractional lastFrame (1.2) case cannot occur: formats.Clip holds
// an integer frame count.
func TestSetValidatesTimingAndTotalSampleBudget(t *testing.T) {
	for _, change := range []func(*formats.Clip){
		func(c *formats.Clip) { c.FPS = 0 },
		func(c *formats.Clip) { c.FPS = math.NaN() },
		func(c *formats.Clip) { c.LastFrame = -1 },
	} {
		asset, list := exporttest.AssignedFixture(t)
		change(list[0].Clip)
		_, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
		expectError(t, err, "timing")
	}
	asset, list := exporttest.AssignedFixture(t)
	for _, a := range list {
		a.Clip.FPS, a.Clip.LastFrame = 30, 70_000
	}
	_, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	expectError(t, err, "250,000")
}

// --- c4d_scene helpers ------------------------------------------------------

func TestOrderedGraphAndAssetModels(t *testing.T) {
	asset, _ := exporttest.SelectedFixture(t)
	graph, err := c4dexport.OrderedGraph(asset.ExportHierarchy)
	if err != nil || len(graph) != len(asset.ExportHierarchy) {
		t.Fatal(err)
	}
	position := map[string]int{}
	for i, node := range graph {
		position[node.ID] = i
		if node.ParentID != nil {
			if p, ok := position[*node.ParentID]; !ok || p >= i {
				t.Fatalf("%s precedes its parent", node.ID)
			}
		}
	}
	duplicate := append(formats.CloneNodes(asset.ExportHierarchy), asset.ExportHierarchy[0])
	_, err = c4dexport.OrderedGraph(duplicate)
	expectError(t, err, "duplicate node identifiers")
	orphan := formats.CloneNodes(asset.ExportHierarchy)
	orphan[1].ParentID = ptr("missing")
	_, err = c4dexport.OrderedGraph(orphan)
	expectError(t, err, "missing parent or a cycle")
	sheared := formats.CloneNodes(asset.ExportHierarchy)
	sheared[2].Matrix[4] = .5
	_, err = c4dexport.OrderedGraph(sheared)
	expectError(t, err, "^"+regexp.QuoteMeta(sheared[2].Name)+": this animation needs a sheared local joint transform")

	prototypes := ordered.New[*formats.MeshPrototype]()
	prototypes.Set("p", &formats.MeshPrototype{Meshes: []*formats.Mesh{{Name: "Wall"}, {Name: "Roof"}}})
	names := ordered.New[string]()
	names.Set("1", "Renamed roof")
	compact := &formats.Asset{MeshPrototypes: prototypes, Models: []*formats.Model{
		{ID: "a", MeshPrototype: "p"}, {ID: "b", MeshPrototype: "p", MeshNames: names}}}
	models, err := c4dexport.AssetModels(compact)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := models.Get("b")
	a, _ := models.Get("a")
	if b.Meshes[1].Name != "Renamed roof" || a.Meshes[1].Name != "Roof" || b.Meshes[0].Name != "Wall" {
		t.Fatal("mesh names not resolved")
	}
	compact.Models[1].MeshPrototype = "missing"
	_, err = c4dexport.AssetModels(compact)
	expectError(t, err, "missing mesh prototype")
	compact.Models[1] = compact.Models[0]
	_, err = c4dexport.AssetModels(compact)
	expectError(t, err, "duplicate model identifiers")
}

// The fast encoder writes exactly what encoding/json writes for the struct.
func TestPayloadJSONMatchesReflection(t *testing.T) {
	type plain c4dexport.Payload
	asset, list := exporttest.AssignedFixture(t)
	set, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, setDefaults())
	if err != nil {
		t.Fatal(err)
	}
	single, err := c4dexport.MakePayload(asset, list[0].Clip, nil, defaults())
	if err != nil {
		t.Fatal(err)
	}
	single.Animation.Tracks[0].Frames[0][3] = 1e-7 // exponent formatting
	for _, payload := range []*c4dexport.Payload{set, single} {
		fast, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		slow, err := json.Marshal((*plain)(payload))
		if err != nil {
			t.Fatal(err)
		}
		jsoncmp.Equal(t, slow, json.RawMessage(fast), jsoncmp.Options{})
		var check map[string]any
		if err := json.Unmarshal(fast, &check); err != nil {
			t.Fatal(err)
		}
	}
}
