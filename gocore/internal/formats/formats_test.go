package formats_test

// Port of tests/test_formats.py: format regression fixtures generated from
// OWLib's documented binary layout.

import (
	"errors"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, root: pypath.Resolve(t.TempDir())}
}

func (f *fixture) write(name string, data []byte) string {
	return ft.Write(f.t, f.root, name, data)
}

func vectorAlmostEqual[T float32 | float64](t *testing.T, actual []T, expected ...float64) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("got %d values %v, want %d %v", len(actual), actual, len(expected), expected)
	}
	for i := range actual {
		if math.Abs(float64(actual[i])-expected[i]) >= 5e-7 {
			t.Fatalf("value %d: got %v, want %v (all %v)", i, actual, expected, actual)
		}
	}
}

func equal[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func raisesRegex(t *testing.T, err error, pattern string) {
	t.Helper()
	var formatErr *formats.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("want FormatError matching %q, got %v (%T)", pattern, err, err)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("error %q does not match %q", err, pattern)
	}
}

func anyContains(values []string, part string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.Contains(v, part) })
}

// lookWarnings stands in for materials.MaterialResolver's no-look case, so
// the shared warning list is exercised like in Python.
func lookWarnings(assetPath, rootPath string, warnings *[]string) formats.MaterialResolver {
	return &warningResolver{root: formats.TextureRoot(assetPath, rootPath), warnings: warnings}
}

type warningResolver struct {
	root     string
	warnings *[]string
}

func (w *warningResolver) ForModel(model *formats.Model, look string) (*ordered.Map[*formats.Material], error) {
	model.MaterialLook = formats.Null[string]()
	if *model.MaterialPath == "" && look == "" {
		*w.warnings = append(*w.warnings, pypath.Name(model.Path)+": no material look reference; using neutral shading.")
	}
	return ordered.New[*formats.Material](), nil
}
func (w *warningResolver) Textures() *ordered.Map[*formats.Texture] {
	return ordered.New[*formats.Texture]()
}
func (w *warningResolver) Root() string { return w.root }

func load(t *testing.T, path, root string) *formats.Asset {
	t.Helper()
	asset, err := formats.LoadAsset(path, root)
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func TestMeshYUpAndWeightMerge(t *testing.T) {
	f := newFixture(t)
	path := f.write("model.owmdl", ft.NewModel().Bytes())
	asset, err := formats.LoadAssetWith(path, "", formats.LoadOptions{Materials: lookWarnings})
	if err != nil {
		t.Fatal(err)
	}
	model := asset.Models[0]
	mesh := model.Meshes[0]
	equal(t, slices.Equal(mesh.Positions, []float32{0, 0, 0, 1, 0, 0, 0, 1, 0}), true)
	equal(t, slices.Equal(mesh.Normals, []float32{0, 0, 1, 0, 0, 1, 0, 0, 1}), true)
	equal(t, slices.Equal(mesh.Indices, []uint32{0, 1, 2}), true)
	equal(t, slices.Equal(mesh.SkinIndices, []uint16{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}), true)
	vectorAlmostEqual(t, mesh.SkinWeights, .7, .3, 0, 0, .7, .3, 0, 0, .7, .3, 0, 0)
	equal(t, model.Bones[1].Pos, [3]float64{0, 2, 0})
	equal(t, model.Bones[1].Scale, [3]float64{1, 1, 1})
	vectorAlmostEqual(t, model.Bones[1].Rot[:], 0, 0, math.Sqrt(.5), math.Sqrt(.5))
	equal(t, anyContains(asset.Warnings, "neutral"), true)
}

func TestExportHierarchyRetainsImporterNamesWithoutChangingPreview(t *testing.T) {
	f := newFixture(t)
	body := ft.NewModel()
	body.Name = "Body.00C"
	body.Sockets = []ft.Socket{{Name: "gun_socket", Bone: "bone_0002", Position: [3]float64{8, 9, 10}, Rotation: [4]float64{0, 0, 0, 1}}}
	f.write("Body.owmdl", body.Bytes())
	weapon := ft.NewModel()
	weapon.Name = "Weapon.00C"
	f.write("Weapon.owmdl", weapon.Bytes())
	child := ft.NewEntity()
	child.Name, child.Model, child.Relative = "HeaderChild", "Weapon.owmdl", "."
	f.write("Child.owentity", child.Bytes())
	main := ft.NewEntity()
	main.Name, main.Model, main.Relative = "HeaderRoot", "Body.owmdl", "."
	main.Children = []ft.Child{{Reference: "Child.owentity", Attachment: "gun_socket"}}
	path := f.write("Main.owentity", main.Bytes())
	asset := load(t, path, f.root)
	graph := map[string]formats.Node{}
	for _, node := range asset.ExportHierarchy {
		graph[node.ID] = node
	}
	equal(t, graph["model-0:entity"].Name, "HeaderRoot")
	equal(t, graph["model-1:entity"].Name, "Child")
	equal(t, graph["model-0:model"].Name, "Body.00C")
	equal(t, graph["model-0:skeleton"].Name, "Body_Skeleton")
	equal(t, graph["model-0:bone:1"].Name, "Pelvis")
	equal(t, *graph["model-0:bone:1"].SourceName, "bone_0002")
	equal(t, *graph["model-1:entity"].ParentID, "model-0:bone:1")
	childMatrix := graph["model-1:entity"].Matrix
	equal(t, [3]float64(childMatrix[12:15]), [3]float64{0, 0, 0})
	equal(t, *graph["model-1:mesh:0"].ParentID, "model-1:model")
	// Existing preview attachments and raw animation track names are stable.
	equal(t, asset.Models[0].Bones[1].Name, "bone_0002")
	if [3]float64(asset.Models[1].AttachmentMatrix[12:15]) == [3]float64{0, 0, 0} {
		t.Fatal("attachment matrix translation should not be zero")
	}
}

func TestDirectModelExportHasNoArtificialEntityWrapper(t *testing.T) {
	f := newFixture(t)
	model := ft.NewModel()
	model.Name = "Model header"
	path := f.write("Standalone.owmdl", model.Bytes())
	graph := load(t, path, "").ExportHierarchy
	for _, node := range graph {
		if node.Type == "entity" {
			t.Fatal("unexpected entity node")
		}
	}
	equal(t, graph[0].Name, "Model header")
	equal(t, graph[0].ParentID == nil, true)
}

func TestChildExportNameUsesOriginalReferenceGUID(t *testing.T) {
	f := newFixture(t)
	f.write("Body.owmdl", ft.NewModel().Bytes())
	child := ft.NewEntity()
	child.Name, child.Model, child.Relative = "Different header", "Body.owmdl", "../.."
	f.write("Entities/00000000002A.003/00000000002A.003.owentity", child.Bytes())
	main := ft.NewEntity()
	main.Model, main.Relative = "Body.owmdl", "."
	main.Children = []ft.Child{{Reference: "00000000002A.003"}}
	path := f.write("Main.owentity", main.Bytes())
	asset := load(t, path, f.root)
	for _, node := range asset.ExportHierarchy {
		if node.ID == "model-1:entity" {
			equal(t, node.Name, "00000000002A")
			return
		}
	}
	t.Fatal("child entity node missing")
}

func TestXYZOrderMultiaxis(t *testing.T) {
	// +90 X then +90 Y then +90 Z gives quaternion (.0, .7071, .0, .7071).
	f := newFixture(t)
	model := ft.NewModel()
	model.Bones = []ft.Bone{{Name: "bone_0001", Parent: -1, Position: [3]float64{1, 2, 3},
		Rotation: [3]float64{math.Pi / 2, math.Pi / 2, math.Pi / 2}}}
	model.Mesh = false
	path := f.write("rotation.owmdl", model.Bytes())
	bone := load(t, path, "").Models[0].Bones[0]
	vectorAlmostEqual(t, bone.Rot[:], 0, math.Sqrt(.5), 0, math.Sqrt(.5))
}

func Test128ByteStringAndUTF8(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{strings.Repeat("a", 128), strings.Repeat("é", 64), strings.Repeat("a", 256)} {
		model := ft.NewModel()
		model.Name = name
		path := f.write("long.owmdl", model.Bytes())
		equal(t, load(t, path, "").Models[0].Name, name)
	}
}

func TestClipHeaderAndSortedAbsoluteTracks(t *testing.T) {
	f := newFixture(t)
	path := f.write("000001.owanimclip", ft.NewClip().Bytes())
	header, err := formats.ReadClipHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, header.FPS, 30)
	equal(t, header.DurationFrames, 31)
	equal(t, header.LastFrame, 30)
	equal(t, header.DurationSeconds, 1)
	equal(t, header.BoneCount, 1)
	result, err := formats.LoadClip(path)
	if err != nil {
		t.Fatal(err)
	}
	bone := result.Bones[0]
	equal(t, bone.Name, "bone_0002")
	equal(t, slices.Equal(bone.Positions.Frames, []uint32{0, 30}), true)
	equal(t, slices.Equal(bone.Positions.Values, []float64{0, 2, 0, 0, 4, 0}), true)
	equal(t, slices.Equal(bone.Rotations.Values, []float64{0, 0, 0, 1, 0, 0, 1, 0}), true)
	equal(t, slices.Equal(bone.Scales.Values, []float64{1, 1, 1}), true)
}

func TestOneSampleHasZeroDuration(t *testing.T) {
	f := newFixture(t)
	clip := ft.NewClip()
	clip.Bones = []ft.ClipBone{{Name: "bone_0001", Tracks: []ft.Track{{Kind: 0, Keys: []ft.Key{ft.K(0, 0, 1, 0)}}}}}
	clip.Duration = 1
	path := f.write("still.owanimclip", clip.Bytes())
	header, err := formats.ReadClipHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	full, err := formats.LoadClip(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []formats.ClipHeader{*header, full.ClipHeader} {
		equal(t, metadata.DurationFrames, 1)
		equal(t, metadata.LastFrame, 0)
		equal(t, metadata.DurationSeconds, 0)
	}
}

func TestShortClipHeader(t *testing.T) {
	f := newFixture(t)
	path := f.write("broken.owanimclip", []byte{2, 0})
	_, err := formats.ReadClipHeader(path)
	raisesRegex(t, err, "truncated")
}

func TestBadClipFPSAndVersion(t *testing.T) {
	f := newFixture(t)
	cases := map[string]func(*ft.Clip){
		"fps 0":    func(c *ft.Clip) { c.FPS = 0 },
		"fps nan":  func(c *ft.Clip) { c.FPS = math.NaN() },
		"version":  func(c *ft.Clip) { c.Version = [2]uint16{3, 0} },
		"duration": func(c *ft.Clip) { c.Duration = 0 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			clip := ft.NewClip()
			change(&clip)
			path := f.write("bad.owanimclip", clip.Bytes())
			_, err := formats.LoadClip(path)
			raisesRegex(t, err, "")
		})
	}
}

func TestTruncatedModelAndClip(t *testing.T) {
	f := newFixture(t)
	model := ft.NewModel().Bytes()
	path := f.write("truncated.owmdl", model[:len(model)-3])
	_, err := formats.LoadAsset(path, "")
	raisesRegex(t, err, "truncated")
	clip := ft.NewClip().Bytes()
	path = f.write("truncated.owanimclip", clip[:len(clip)-3])
	_, err = formats.LoadClip(path)
	raisesRegex(t, err, "truncated")
}

func TestCycleInBones(t *testing.T) {
	f := newFixture(t)
	model := ft.NewModel()
	model.Bones = []ft.Bone{{Name: "bone_0001", Parent: 1}, {Name: "bone_0002", Parent: 0}}
	model.Mesh = false
	path := f.write("cycle.owmdl", model.Bytes())
	_, err := formats.LoadAsset(path, "")
	raisesRegex(t, err, "cycle")
}

func TestForwardParentIsSupported(t *testing.T) {
	f := newFixture(t)
	model := ft.NewModel()
	model.Bones = []ft.Bone{{Name: "bone_0001", Parent: 1, Position: [3]float64{0, 1, 0}},
		{Name: "bone_0002", Parent: -1, Position: [3]float64{0, 1, 0}}}
	model.Mesh = false
	model.Sockets = []ft.Socket{{Name: "socket", Bone: "bone_0001", Position: [3]float64{0, 3, 0}, Rotation: [4]float64{0, 0, 0, 1}}}
	path := f.write("forward.owmdl", model.Bytes())
	socket := load(t, path, "").Models[0].Sockets[0]
	vectorAlmostEqual(t, socket.Matrix[12:15], 0, 1, 0)
}

func TestNullHardpointReferenceStaysInModelSpace(t *testing.T) {
	f := newFixture(t)
	model := ft.NewModel()
	model.Bones = []ft.Bone{{Name: "bone_0000", Parent: -1, Position: [3]float64{0, 5, 0}}}
	model.Mesh = false
	model.Sockets = []ft.Socket{{Name: "socket", Bone: "bone_0000", Position: [3]float64{0, 3, 0}, Rotation: [4]float64{0, 0, 0, 1}}}
	path := f.write("unbound.owmdl", model.Bytes())
	socket := load(t, path, "").Models[0].Sockets[0]
	equal(t, socket.Bone == nil, true)
	vectorAlmostEqual(t, socket.Matrix[12:15], 0, 3, 0)
}

func TestEntityChildAttachesInBoneLocalSpace(t *testing.T) {
	// The parent's bone_0002 rest transform is translation(0,2,0) Rz(90).
	// A socket at model (1,2,0), Rz(90) has bone-local (0,-1,0).
	f := newFixture(t)
	body := ft.NewModel()
	body.Sockets = []ft.Socket{{Name: "hardpoint_0001", Bone: "bone_0002", Position: [3]float64{1, 2, 0},
		Rotation: [4]float64{0, 0, math.Sqrt(.5), math.Sqrt(.5)}}}
	f.write("Models/Body.00C/Body.owmdl", body.Bytes())
	weapon := ft.NewModel()
	weapon.Name, weapon.Slots = "Weapon", 0
	f.write("Models/Weapon.00C/Weapon.owmdl", weapon.Bytes())
	hero := ft.NewEntity()
	hero.Children = []ft.Child{{Reference: "Weapon", Attachment: "hardpoint_0001"}}
	path := f.write("Entities/Hero/Hero.owentity", hero.Bytes())
	weaponEntity := ft.NewEntity()
	weaponEntity.Name, weaponEntity.Model = "Weapon", "Weapon.00C"
	f.write("Entities/Weapon/Weapon.owentity", weaponEntity.Bytes())
	asset := load(t, path, "")
	parent, child := asset.Models[0], asset.Models[1]
	equal(t, *child.ParentID, parent.ID)
	equal(t, *child.AttachmentBone, "bone_0002")
	vectorAlmostEqual(t, child.AttachmentMatrix[12:15], 0, -1, 0)
	vectorAlmostEqual(t, child.AttachmentMatrix[:12], 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0)
}

func TestEntity20DefaultLayout(t *testing.T) {
	f := newFixture(t)
	f.write("Models/Body.00C/Body.owmdl", ft.NewModel().Bytes())
	entity := ft.NewEntity()
	entity.Minor = 0
	path := f.write("Entities/Hero/Hero.owentity", entity.Bytes())
	equal(t, len(load(t, path, "").Models), 1)
}

func TestEntityWindowsPathsAndSelectedRoot(t *testing.T) {
	f := newFixture(t)
	f.write("Models/Body.00C/Body.owmdl", ft.NewModel().Bytes())
	entity := ft.NewEntity()
	entity.Relative = `..\..\missing`
	path := f.write("Copied/Hero.owentity", entity.Bytes())
	equal(t, len(load(t, path, f.root).Models), 1)
}

func TestMissingChildrenReportWarning(t *testing.T) {
	f := newFixture(t)
	f.write("Models/Body.00C/Body.owmdl", ft.NewModel().Bytes())
	entity := ft.NewEntity()
	entity.Children = []ft.Child{{Reference: "Missing", Attachment: "null"}}
	path := f.write("Entities/Hero/Hero.owentity", entity.Bytes())
	result := load(t, path, "")
	equal(t, len(result.Models), 1)
	equal(t, anyContains(result.Warnings, "missing child entity Missing"), true)
}

func TestMissingMainModelIsActionableError(t *testing.T) {
	f := newFixture(t)
	path := f.write("Entities/Hero/Hero.owentity", ft.NewEntity().Bytes())
	_, err := formats.LoadAsset(path, "")
	raisesRegex(t, err, "missing model Body.00C")
}

func TestEntityCycleIsWarning(t *testing.T) {
	f := newFixture(t)
	f.write("Models/Body.00C/Body.owmdl", ft.NewModel().Bytes())
	entity := ft.NewEntity()
	entity.Children = []ft.Child{{Reference: "Hero", Attachment: "null"}}
	path := f.write("Entities/Hero/Hero.owentity", entity.Bytes())
	result := load(t, path, "")
	equal(t, len(result.Models), 1)
	equal(t, anyContains(result.Warnings, "cycle"), true)
}

func TestWrongTrackComponentsRejected(t *testing.T) {
	f := newFixture(t)
	clip := ft.NewClip()
	clip.Bones = []ft.ClipBone{{Name: "bone", Tracks: []ft.Track{{Kind: 1, Keys: []ft.Key{ft.K(0, 1, 2, 3)}}}}}
	path := f.write("bad.owanimclip", clip.Bytes())
	_, err := formats.LoadClip(path)
	raisesRegex(t, err, "needs 4 components")
}

func TestNonFiniteTrackRejected(t *testing.T) {
	f := newFixture(t)
	clip := ft.NewClip()
	clip.Bones = []ft.ClipBone{{Name: "bone", Tracks: []ft.Track{{Kind: 0, Keys: []ft.Key{ft.K(0, math.Inf(1), 0, 0)}}}}}
	path := f.write("bad.owanimclip", clip.Bytes())
	_, err := formats.LoadClip(path)
	raisesRegex(t, err, "non-finite")
}

func TestUnknownTrackSkippedAndDurationExtended(t *testing.T) {
	f := newFixture(t)
	clip := ft.NewClip()
	clip.Bones = []ft.ClipBone{{Name: "bone", Tracks: []ft.Track{
		{Kind: 4, Keys: []ft.Key{ft.K(0, 1, 2)}},
		{Kind: 0, Keys: []ft.Key{ft.K(60, 1, 2, 3)}},
	}}}
	path := f.write("future.owanimclip", clip.Bytes())
	result, err := formats.LoadClip(path)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, result.DurationFrames, 61)
	equal(t, result.LastFrame, 60)
	equal(t, result.DurationSeconds, 2)
	equal(t, len(result.Warnings), 2)
}

// Go-specific checks beyond the Python suite.

func TestMissingFileAndExtension(t *testing.T) {
	f := newFixture(t)
	_, err := formats.LoadAsset(f.root+"/nothing.owmdl", "")
	if !errors.Is(err, fs.ErrNotExist) || err.Error() != "File not found: "+f.root+"/nothing.owmdl" {
		t.Fatalf("got %v", err)
	}
	_, err = formats.LoadAsset(f.root+"/x.txt", "")
	equal(t, err.Error(), "Select an .owentity or .owmdl file.")
	_, err = formats.LoadClip(f.root + "/x.txt")
	equal(t, err.Error(), "Select an .owanimclip file: x.txt")
}

func TestPythonFormatting(t *testing.T) {
	for value, want := range map[float64]string{
		0: "0.0", 30: "30.0", 1500: "1500.0", 1e16: "1e+16", 1e15: "1000000000000000.0",
		0.0001: "0.0001", 1e-5: "1e-05", -2.5: "-2.5", math.NaN(): "nan", math.Inf(-1): "-inf",
		float64(float32(0.1)): "0.10000000149011612", 3.4028234663852886e+38: "3.4028234663852886e+38",
	} {
		if got := formats.PythonFloat(value); got != want {
			t.Errorf("PythonFloat(%v) = %q, want %q", value, got, want)
		}
	}
	for value, want := range map[string]string{
		"": "''", "a'b": `"a'b"`, `a'"b`: `'a\'"b'`, `\`: `'\\'`,
		"é\n\x00" + string(rune(0x200b)): `'é\n\x00\` + "u200b'",
	} {
		if got := formats.PythonRepr(value); got != want {
			t.Errorf("PythonRepr(%q) = %s, want %s", value, got, want)
		}
	}
	equal(t, formats.Commas(1234567), "1,234,567")
	equal(t, formats.Commas(-1000), "-1,000")
	equal(t, formats.Commas(999), "999")
}
