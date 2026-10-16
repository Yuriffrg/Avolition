package materials

// Port of tests/test_materials.py.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/pypath"
)

type fixture struct {
	t        *testing.T
	root     string
	warnings []string
	resolver *Resolver
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, root: pypath.Resolve(t.TempDir())}
	f.resolver = New(filepath.Join(f.root, "model.owmdl"), f.root, &f.warnings)
	return f
}

func (f *fixture) write(name string, data []byte) string {
	return ft.Write(f.t, f.root, name, data)
}

func (f *fixture) texture(name string) string {
	return f.write(name, ft.PNGBytes(2, 2, func(x, y int) [4]byte { return [4]byte{30, 100, 200, 150} }))
}

func (f *fixture) describe(bindings []uint32, inputs []ft.StaticInput, shader uint32, name string) *formats.Material {
	f.t.Helper()
	f.texture("image.png")
	var textures []ft.TextureRef
	for _, key := range bindings {
		textures = append(textures, ft.TextureRef{Reference: "image.png", Binding: key})
	}
	material, err := f.resolver.Descriptor(f.write(name, ft.MaterialBytes(textures, inputs, shader)))
	if err != nil {
		f.t.Fatal(err)
	}
	return material
}

func (f *fixture) mapOf(material *formats.Material, role string) *formats.MaterialMap {
	f.t.Helper()
	m, ok := material.Maps.Get(role)
	if !ok {
		f.t.Fatalf("material %s has no %s map (maps %v)", material.Name, role, material.Maps.Keys())
	}
	return m
}

func (f *fixture) operation(material *formats.Material, role string) *formats.Texture {
	f.t.Helper()
	texture, ok := f.resolver.Textures().Get(f.mapOf(material, role).TextureID)
	if !ok {
		f.t.Fatalf("texture for %s not registered", role)
	}
	return texture
}

func (f *fixture) warned(fragment string) bool {
	return slices.ContainsFunc(f.warnings, func(w string) bool { return strings.Contains(w, fragment) })
}

func hasMap(material *formats.Material, role string) bool {
	_, ok := material.Maps.Get(role)
	return ok
}

func isFormatError(err error) bool {
	var formatErr *formats.FormatError
	return errors.As(err, &formatErr)
}

func TestModelLookUint64KeysAndLongStrings(t *testing.T) {
	f := newFixture(t)
	key := uint64(math.MaxUint64)
	reference := "../" + strings.Repeat("a", 150) + ".owmat"
	record, err := ReadOWMat(f.write("look.owmat", ft.LookBytes(ft.LookEntry{Key: key, Reference: reference})))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.Entries, []LookEntry{{"18446744073709551615", reference}}) {
		t.Fatalf("entries %v", record.Entries)
	}
}

func TestNullLookWriterQuirkAndEmptyLook(t *testing.T) {
	f := newFixture(t)
	var path string
	for _, suffix := range [][]byte{nil, ft.Pack("Q", 0)} {
		path = f.write("empty.owmat", append(ft.LookBytes(), suffix...))
		record, err := ReadOWMat(path)
		if err != nil || len(record.Entries) != 0 {
			t.Fatalf("%v %v", record, err)
		}
	}
	f.write("empty.owmat", append(ft.LookBytes(), ft.Pack("Q", 1)...))
	if _, err := ReadOWMat(path); !isFormatError(err) {
		t.Fatalf("want FormatError, got %v", err)
	}
}

func TestUnknownPaddedStaticInputsKeepRecordAlignment(t *testing.T) {
	f := newFixture(t)
	path := f.write("material.owmat", ft.MaterialBytes(nil, []ft.StaticInput{
		{123, []byte("unknown input\x00\x00")}, {3250491852, append(ft.Pack("f", 3), "padding"...)},
		{565460110, ft.Pack("f", .75)}}, 0))
	record, err := ReadOWMat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.Inputs[123]) != "unknown input\x00\x00" || string(record.Inputs[565460110]) != string(ft.Pack("f", .75)) {
		t.Fatalf("inputs %v", record.Inputs)
	}
	if got := f.resolver.inputFloat(record, 3250491852, 0); got != 3 {
		t.Fatalf("input %v", got)
	}
	if !slices.Equal(record.InputKeys, []uint32{123, 3250491852, 565460110}) {
		t.Fatalf("keys %v", record.InputKeys)
	}
}

func TestShortAndNonfiniteKnownInputsWarnWithoutCrashing(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{3166598269}, []ft.StaticInput{{3250491852, []byte{0}}, {2446772623, ft.Pack("f", math.NaN())}}, 0, "material.owmat")
	if *material.EmissiveIntensity != 16 {
		t.Fatal(*material.EmissiveIntensity)
	}
	if !f.warned("short static") || !f.warned("non-finite static") {
		t.Fatal(f.warnings)
	}
}

func TestMalformedMaterialRecordsFailStrictly(t *testing.T) {
	f := newFixture(t)
	valid := ft.MaterialBytes([]ft.TextureRef{{"a.png", 2903569922}}, []ft.StaticInput{{3, []byte("test")}}, 0)
	var invalid [][]byte
	for _, n := range []int{0, 4, 8, 20, len(valid) - 1} {
		invalid = append(invalid, valid[:n])
	}
	invalid = append(invalid, ft.Pack("HHI", 2, 0, 0), ft.Pack("HHI", 3, 1, 0), ft.Pack("HHI", 3, 0, 2),
		ft.Pack("HHIQQI", 3, 0, 0, 5000, 0, 0), append(append([]byte{}, valid...), "extra"...),
		append(ft.Pack("HHIQQI", 3, 0, 0, 0, 1, 0), ft.Pack("II", 123, 1<<30)...))
	for i, blob := range invalid {
		if _, err := ReadOWMat(f.write("bad.owmat", blob)); !isFormatError(err) {
			t.Errorf("%d: want FormatError, got %v", i, err)
		}
	}
}

func TestWriterModelLookPathsAndReferenceBasedMaterialKeys(t *testing.T) {
	f := newFixture(t)
	root := ft.GenerateMaterialDemo(t, f.root)
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/MaterialRobot/MaterialRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	model := asset.Models[0]
	if keys := model.Materials.Keys(); !slices.Equal(keys, []string{"1", "2", "3"}) {
		t.Fatalf("material keys %v", keys)
	}
	if asset.Textures.Len() != 10 {
		t.Fatalf("textures %d", asset.Textures.Len())
	}
	for _, mesh := range model.Meshes {
		if _, ok := model.Materials.Get(mesh.MaterialKey); !ok {
			t.Fatalf("mesh key %s", mesh.MaterialKey)
		}
	}
	asset.Textures.Range(func(_ string, texture *formats.Texture) bool {
		if !pypath.IsRelativeTo(texture.Path, root) {
			t.Errorf("texture outside root: %s", texture.Path)
		}
		return true
	})
	for _, w := range asset.Warnings {
		if strings.Contains(w, "missing") || strings.Contains(w, "unresolved") {
			t.Errorf("warning %q", w)
		}
	}
	// Paths/descriptors only: images remain lazily fetched, not duplicated in JSON.
	encoded, _ := json.Marshal(asset)
	if strings.Contains(string(encoded), "data:image") {
		t.Fatal("inline image")
	}
	material, _ := model.Materials.Get("1")
	if ao, _ := material.Maps.Get("ao"); ao.UVIndex != 1 {
		t.Fatal(ao.UVIndex)
	}
}

func TestSecondUVSetPreservedWithoutChangingSkinning(t *testing.T) {
	f := newFixture(t)
	asset, err := formats.LoadAsset(f.write("model.owmdl", ft.TexturedModel()), f.root)
	if err != nil {
		t.Fatal(err)
	}
	model := asset.Models[0]
	mesh := model.Meshes[0]
	if !slices.Equal(mesh.UVs[:8], []float32{0, 0, 1, 0, 1, 1, 0, 1}) ||
		!slices.Equal(mesh.UVs2[:8], []float32{.125, .125, .875, .125, .875, .875, .125, .875}) ||
		len(mesh.UVs) != len(mesh.UVs2) ||
		!slices.Equal(mesh.SkinIndices[:4], []uint16{0, 0, 0, 0}) ||
		!slices.Equal(mesh.SkinWeights[:4], []float64{1, 0, 0, 0}) ||
		model.Bones[0].Pos != [3]float64{0, 1, 0} {
		t.Fatalf("mesh %+v", mesh)
	}
}

func TestEntityMaterialLookOverridesModelHeader(t *testing.T) {
	f := newFixture(t)
	root := ft.GenerateMaterialDemo(t, f.root)
	var entries []ft.LookEntry
	for _, key := range []uint64{1, 2, 3} {
		entries = append(entries, ft.LookEntry{Key: key, Reference: "../../Materials/000000B002.owmat"})
	}
	f.write("Models/MaterialRobot.00C/ModelLooks/Alternate.owmat", ft.LookBytes(entries...))
	entity := ft.NewEntity()
	entity.Name, entity.Model = "Alternate", "MaterialRobot.00C"
	blob := entity.Bytes()
	null := ft.String("null")
	index := strings.LastIndex(string(blob), string(null))
	patched := append(append(append([]byte{}, blob[:index]...), ft.String("Alternate.01A")...), blob[index+len(null):]...)
	path := f.write("Entities/Alternate/Alternate.owentity", patched)
	asset, err := formats.LoadAsset(path, root)
	if err != nil {
		t.Fatal(err)
	}
	asset.Models[0].Materials.Range(func(_ string, material *formats.Material) bool {
		if material.Name != "000000B002" {
			t.Errorf("material %s", material.Name)
		}
		return true
	})
	if asset.Models[0].Materials.Len() == 0 {
		t.Fatal("no materials")
	}
	if !pypath.IsDir(filepath.Join(root, "Models/MaterialRobot.00C")) {
		t.Fatal("model folder")
	}
}

func TestMissingMaterialOrTextureKeepsGeometry(t *testing.T) {
	f := newFixture(t)
	root := ft.GenerateMaterialDemo(t, f.root)
	os.Remove(filepath.Join(root, "Models/MaterialRobot.00C/Materials/000000B002.owmat"))
	os.Remove(filepath.Join(root, "Models/MaterialRobot.00C/Textures/000000A001.png"))
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/MaterialRobot/MaterialRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	model := asset.Models[0]
	if len(model.Meshes) != 8 {
		t.Fatal(len(model.Meshes))
	}
	if _, ok := model.Materials.Get("2"); ok {
		t.Fatal("material 2 present")
	}
	first, _ := model.Materials.Get("1")
	if hasMap(first, "baseColor") {
		t.Fatal("baseColor present")
	}
	if !slices.ContainsFunc(asset.Warnings, func(w string) bool { return strings.Contains(w, "missing referenced file") }) {
		t.Fatal(asset.Warnings)
	}
}

func TestOrdinaryAndBlenderFullLookFilenameRelativeResolution(t *testing.T) {
	f := newFixture(t)
	target := f.write("Models/A/Materials/a.owmat", ft.MaterialBytes(nil, nil, 0))
	owner := filepath.Join(f.root, "Models/A/ModelLooks/default.owmat")
	if got := f.resolver.Resolve(owner, "../../Materials/a.owmat", true, ""); got != target {
		t.Fatalf("got %q want %q", got, target)
	}
	texture := f.texture("Models/A/Textures/123.png")
	if got := f.resolver.Resolve(target, `..\Textures\123.png`, false, ""); got != texture {
		t.Fatalf("got %q want %q", got, texture)
	}
}

func TestUniqueExactBasenameFallbackAndAmbiguousRejection(t *testing.T) {
	f := newFixture(t)
	image := f.texture("Moved/123.png")
	owner := filepath.Join(f.root, "Material.owmat")
	if got := f.resolver.Resolve(owner, "old/123.png", false, ""); got != image {
		t.Fatalf("got %q", got)
	}
	f.texture("Another/123.png")
	resolver := New(owner, f.root, &f.warnings)
	if got := resolver.Resolve(owner, "old/123.png", false, ""); got != "" {
		t.Fatalf("got %q", got)
	}
	if !f.warned("ambiguous") {
		t.Fatal(f.warnings)
	}
}

func TestDataToolSameReferenceDDSFallback(t *testing.T) {
	f := newFixture(t)
	target := f.write("Textures/image.dds", []byte("DDS fixture header not decoded here"))
	owner := filepath.Join(f.root, "Materials/material.owmat")
	if got := f.resolver.Resolve(owner, "../Textures/image.png", false, ""); got != target {
		t.Fatalf("got %q", got)
	}
	if !f.warned("DDS fallback") {
		t.Fatal(f.warnings)
	}
}

func TestAbsoluteEscapeIsNeverRegistered(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	image := ft.Write(t, other, "image.png", ft.PNGBytes(1, 1, func(x, y int) [4]byte { return [4]byte{0, 0, 0, 255} }))
	material, err := f.resolver.Descriptor(f.write("mat.owmat", ft.MaterialBytes([]ft.TextureRef{{image, 2903569922}}, nil, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if material.Maps.Len() != 0 || f.resolver.Textures().Len() != 0 || !f.warned("outside export root") {
		t.Fatal(f.warnings)
	}
}

func TestInvalidTexturePathWarnsWithoutAbortingMaterial(t *testing.T) {
	f := newFixture(t)
	material, err := f.resolver.Descriptor(f.write("mat.owmat", ft.MaterialBytes([]ft.TextureRef{{"invalid\x00path.png", 2903569922}}, nil, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if material.Maps.Len() != 0 || !f.warned("invalid referenced path") {
		t.Fatal(f.warnings)
	}
}

func TestRoleHashesDriveChannelsNormalAndPBRNotFilenames(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{2903569922, 378934698, 548341454, 3166598269}, nil, 0, "material.owmat")
	check := func(role, field, got, want string) {
		if got != want {
			t.Errorf("%s %s: got %q want %q", role, field, got, want)
		}
	}
	check("baseColor", "channel", f.operation(material, "baseColor").Channel, "rgb")
	check("normal", "transform", f.operation(material, "normal").Transform, "ow-normal")
	check("roughness", "channel", f.operation(material, "roughness").Channel, "g")
	check("roughness", "transform", f.operation(material, "roughness").Transform, "invert")
	check("metalness", "channel", f.operation(material, "metalness").Channel, "r")
	check("metalness", "transform", f.operation(material, "metalness").Transform, "ow-metalness")
	check("emissive", "channel", f.operation(material, "emissive").Channel, "luminance")
	if material.Metalness != 1 || material.Roughness != 1 || material.EmissiveMode != "baseColorMask" ||
		*material.EmissiveIntensity != 16 || !*material.EmissiveUsesAO || material.AlphaMode != "OPAQUE" || hasMap(material, "alpha") {
		t.Fatalf("%+v", material)
	}
}

func TestAlbedoAlphaIsAOForDetailNotOpacity(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{2903569922}, nil, 217, "material.owmat")
	if f.operation(material, "ao").Channel != "a" || material.AlphaMode != "OPAQUE" || hasMap(material, "alpha") {
		t.Fatalf("%+v", material)
	}
}

func TestExplicitHairAlphaAndGlassAlpha(t *testing.T) {
	f := newFixture(t)
	hair := f.describe([]uint32{1239794147}, nil, 51, "hair.owmat")
	glass := f.describe([]uint32{67369114, 1493127177}, nil, 38, "glass.owmat")
	if f.operation(hair, "alpha").Channel != "a" || hair.AlphaMode != "MASK" || glass.AlphaMode != "BLEND" ||
		*glass.EmissiveIntensity != 0 || glass.EmissiveMode != "" || f.mapOf(glass, "emissive").ColorSpace != "srgb" {
		t.Fatalf("%+v %+v", hair, glass)
	}
}

func TestEmissionStrengthAndShaderSpecificMetalness(t *testing.T) {
	cases := []struct {
		shader   uint32
		inputs   []ft.StaticInput
		strength float64
	}{{0, nil, 16}, {217, nil, 16}, {221, nil, 16}, {167, nil, 32}, {43, []ft.StaticInput{{4081294361, make([]byte, 16)}}, 32}, {34, nil, 128}}
	for _, c := range cases {
		f := newFixture(t)
		material := f.describe([]uint32{548341454, 3166598269}, c.inputs, c.shader, "s.owmat")
		if *material.EmissiveIntensity != c.strength {
			t.Errorf("shader %d strength %v", c.shader, *material.EmissiveIntensity)
		}
		if c.shader == 167 || c.shader == 43 || c.shader == 221 {
			if hasMap(material, "metalness") || material.Metalness != 0 {
				t.Errorf("shader %d metalness", c.shader)
			}
		}
		if c.shader == 221 && (material.Roughness != 0 || hasMap(material, "roughness")) {
			t.Errorf("shader 221 roughness")
		}
		if *material.EmissiveUsesAO != (c.shader == 0 || c.shader == 34) {
			t.Errorf("shader %d usesAO %v", c.shader, *material.EmissiveUsesAO)
		}
	}
}

func TestComplexEmissionOmittedNotGuessed(t *testing.T) {
	f := newFixture(t)
	for _, shader := range []uint32{43, 44, 50, 51} {
		material := f.describe([]uint32{3166598269}, nil, shader, fmt.Sprintf("%d.owmat", shader))
		if hasMap(material, "emissive") || material.Emissive != [3]float64{} {
			t.Errorf("shader %d emissive", shader)
		}
	}
	if !f.warned("layered shader emission") {
		t.Fatal(f.warnings)
	}
}

func TestDecalAlphaRedAndStaticOpacityNotWrongGlobalMultiplier(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{3989656707}, []ft.StaticInput{{565460110, ft.Pack("f", .2)}}, 34, "material.owmat")
	if f.operation(material, "alpha").Channel != "r" || f.operation(material, "alpha").InputColorSpace != "srgb" ||
		material.Opacity != 1 || !f.warned("opacity control") {
		t.Fatalf("%+v %v", material, f.warnings)
	}
}

func TestUVSelectorsAreOneBasedAndBasicScaleExplicit(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{2903569922, 3166598269, 3335614873}, []ft.StaticInput{
		{3344068240, ft.Pack("I", 2)}, {1883253226, ft.Pack("ff", 2, 3)}, {2166182138, ft.Pack("ff", 10, 10)}}, 0, "material.owmat")
	if f.mapOf(material, "emissive").UVIndex != 1 || f.mapOf(material, "ao").UVIndex != 1 ||
		f.mapOf(material, "baseColor").Repeat != [2]float64{2, 3} || f.mapOf(material, "emissive").Repeat != [2]float64{1, 1} {
		t.Fatalf("%+v", material)
	}
}

func TestUnsupportedUVSkipsEmissionWithoutTurningMeshWhite(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{3166598269}, []ft.StaticInput{{3344068240, ft.Pack("I", 5)}}, 0, "material.owmat")
	if hasMap(material, "emissive") || material.Emissive != [3]float64{} || !f.warned("unsupported UV channel") {
		t.Fatalf("%+v", material)
	}
}

func TestOW2HairStrandMapsSwitchAlphaToUV2(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{1239794147, 4101268840, 758934576}, nil, 51, "material.owmat")
	if f.mapOf(material, "alpha").UVIndex != 1 || f.mapOf(material, "baseColor").UVIndex != 0 {
		t.Fatalf("%+v", material)
	}
}

func TestRepeatedImageOperationIDSharedAcrossMaterials(t *testing.T) {
	f := newFixture(t)
	a := f.describe([]uint32{2903569922}, nil, 0, "a.owmat")
	b := f.describe([]uint32{2903569922}, nil, 0, "b.owmat")
	if f.mapOf(a, "baseColor").TextureID != f.mapOf(b, "baseColor").TextureID || f.resolver.Textures().Len() != 1 {
		t.Fatal("texture not shared")
	}
}

func TestUnknownHashWarnsAndDoesNotInferFromImageName(t *testing.T) {
	f := newFixture(t)
	material := f.describe([]uint32{123456}, nil, 0, "material.owmat")
	if material.Maps.Len() != 0 || f.resolver.Textures().Len() != 0 || !f.warned("123456") {
		t.Fatal(f.warnings)
	}
}

func TestCollisionClassificationUsesExactSourceMaterialGUID(t *testing.T) {
	root := t.TempDir()
	if !IsCollisionMaterial(&formats.Material{SourcePath: filepath.Join(root, "0000000007a1.owmat")}) {
		t.Fatal("lower-case GUID")
	}
	if IsCollisionMaterial(&formats.Material{Name: "0000000007A1", ShaderID: 33}) ||
		IsCollisionMaterial(&formats.Material{SourcePath: filepath.Join(root, "My0000000007A1.owmat")}) ||
		IsCollisionMaterial(&formats.Material{SourcePath: filepath.Join(root, "0000000007A1-copy.owmat")}) ||
		IsCollisionMaterial(nil) {
		t.Fatal("false positive")
	}
}

// The ported fixture generator writes tests/material-assets exactly (PNG
// compression aside).
func TestGenerateMaterialDemoMatchesFixture(t *testing.T) {
	fixture := filepath.Join(repoRoot(), "tests", "material-assets")
	root := ft.GenerateMaterialDemo(t, t.TempDir())
	count := 0
	err := filepath.Walk(fixture, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, ".png") {
			return err
		}
		rel, _ := filepath.Rel(fixture, path)
		want, _ := os.ReadFile(path)
		got, readErr := os.ReadFile(filepath.Join(root, rel))
		if strings.HasSuffix(rel, ".txt") {
			want = []byte(strings.ReplaceAll(string(want), "\r\n", "\n"))
			got = []byte(strings.ReplaceAll(string(got), "\r\n", "\n"))
		}
		if readErr != nil || string(got) != string(want) {
			t.Errorf("%s differs from the generated fixture (%v)", rel, readErr)
		}
		count++
		return nil
	})
	if err != nil || count < 8 {
		t.Fatalf("compared %d files: %v", count, err)
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
