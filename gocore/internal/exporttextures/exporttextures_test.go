package exporttextures

// Ports tests/test_export_textures.py: pixels, UV honesty, resolution and
// immutable sources.

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
	"avolition/gocore/internal/textures"
)

func linear(value float64) float64 {
	value /= 255
	if value <= .04045 {
		return value / 12.92
	}
	return math.Pow((value+.055)/1.055, 2.4)
}

func srgb(value float64) byte {
	if value <= .0031308 {
		return byte(math.RoundToEven(255 * (12.92 * value)))
	}
	return byte(math.RoundToEven(255 * (1.055*math.Pow(value, 1/2.4) - .055)))
}

type fixture struct {
	t        *testing.T
	root     string
	output   string
	asset    *formats.Asset
	material *formats.Material
	result   *Result
}

func newFixture(t *testing.T) *fixture {
	root := pypath.Resolve(t.TempDir())
	f := &fixture{t: t, root: root, output: filepath.Join(root, "Output", "tex")}
	one := 1.0
	f.material = &formats.Material{Name: "Surface", BaseColor: [3]float64{1, 1, 1}, Roughness: 1, Metalness: 1,
		Emissive: [3]float64{1, 1, 1}, EmissiveIntensity: &one, AlphaMode: "OPAQUE", Opacity: 1,
		Maps: ordered.New[*formats.MaterialMap]()}
	materials := ordered.New[*formats.Material]()
	materials.Set("9000", f.material)
	f.asset = &formats.Asset{Path: filepath.Join(root, "model.owmdl"), TextureRoot: root,
		Textures: ordered.New[*formats.Texture](), Warnings: []string{},
		Models: []*formats.Model{{ID: "model-0", Name: "Example", Materials: materials}}}
	return f
}

type imageOptions struct {
	pixel              [4]byte
	width, height      int
	channel, transform string
	uv                 int
	repeat             [2]float64
	space, inputSpace  string
}

func defaults() imageOptions {
	return imageOptions{pixel: [4]byte{128, 128, 128, 255}, width: 1, height: 1, channel: "rgb", transform: "none",
		repeat: [2]float64{1, 1}, space: "linear", inputSpace: "linear"}
}

func (f *fixture) image(role string, configure func(*imageOptions)) string {
	o := defaults()
	if configure != nil {
		configure(&o)
	}
	path := filepath.Join(f.root, role+".png")
	pixel := o.pixel
	ft.Write(f.t, f.root, role+".png", ft.PNGBytes(o.width, o.height, func(int, int) [4]byte { return pixel }))
	f.asset.Textures.Set(role, &formats.Texture{Path: path, Channel: o.channel, Transform: o.transform, InputColorSpace: o.inputSpace})
	f.material.Maps.Set(role, &formats.MaterialMap{TextureID: role, UVIndex: o.uv, Repeat: o.repeat, ColorSpace: o.space})
	return path
}

func (f *fixture) bake(prefix string) *formats.Material {
	f.t.Helper()
	result, err := BakeMaterials(f.asset, f.output, prefix, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.result = result
	materials, _ := result.Materials.Get("model-0")
	material, _ := materials.Get("9000")
	return material
}

func decode(t *testing.T, path string) *textures.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	image, err := textures.DecodeImage(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func mapOf(t *testing.T, material *formats.Material, role string) *formats.MaterialMap {
	t.Helper()
	m, ok := material.Maps.Get(role)
	if !ok {
		t.Fatalf("%s map missing", role)
	}
	return m
}

func pixelAt(t *testing.T, material *formats.Material, role string, x int) [3]byte {
	t.Helper()
	image := decode(t, mapOf(t, material, role).Path)
	i := x * 4
	return [3]byte{image.Pix[i], image.Pix[i+1], image.Pix[i+2]}
}

func gray(v byte) [3]byte { return [3]byte{v, v, v} }

func (f *fixture) hasWarning(text string) bool {
	for _, warning := range f.result.Warnings {
		if strings.Contains(warning, text) {
			return true
		}
	}
	return false
}

func TestBaseColorAOBakesInLinearLightWithoutLighting(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	f.image("ao", func(o *imageOptions) { o.channel = "r" })
	material := f.bake("")
	expected := srgb(linear(128) * (128.0 / 255))
	if got := pixelAt(t, material, "baseColor", 0); got != gray(expected) {
		t.Fatalf("pixel %v, want %v", got, expected)
	}
	if _, ok := material.Maps.Get("ao"); ok {
		t.Fatal("ao kept")
	}
	if material.BaseColor != [3]float64{1, 1, 1} || mapOf(t, material, "baseColor").ColorSpace != "srgb" {
		t.Fatalf("%+v", material)
	}
}

func TestConstantBaseColorAndAOCreatePortableColorTexture(t *testing.T) {
	f := newFixture(t)
	f.material.BaseColor = [3]float64{.5, .25, 0}
	f.image("ao", func(o *imageOptions) { o.channel = "r"; o.uv = 1 })
	material := f.bake("")
	want := [3]byte{srgb(.5 * 128 / 255), srgb(.25 * 128 / 255), srgb(0)}
	if got := pixelAt(t, material, "baseColor", 0); got != want {
		t.Fatalf("pixel %v, want %v", got, want)
	}
	if mapOf(t, material, "baseColor").UVIndex != 1 {
		t.Fatal("uv index")
	}
	if _, ok := material.Maps.Get("ao"); ok {
		t.Fatal("ao kept")
	}
}

func TestMismatchedResolutionsResampleColorInLinearLight(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.width = 2; o.space = "srgb" })
	ft.Write(t, f.root, "baseColor.png", ft.PNGBytes(2, 1, func(x, _ int) [4]byte {
		if x == 0 {
			return [4]byte{0, 0, 0, 255}
		}
		return [4]byte{255, 255, 255, 255}
	}))
	f.image("ao", func(o *imageOptions) { o.pixel = [4]byte{255, 255, 255, 255}; o.width = 4; o.channel = "r" })
	material := f.bake("")
	image := decode(t, mapOf(t, material, "baseColor").Path)
	if image.Width != 4 || image.Height != 1 {
		t.Fatalf("size %dx%d", image.Width, image.Height)
	}
	if got := pixelAt(t, material, "baseColor", 1); got != gray(srgb(.25)) {
		t.Fatalf("pixel 1 %v", got)
	}
	if got := pixelAt(t, material, "baseColor", 2); got != gray(srgb(.75)) {
		t.Fatalf("pixel 2 %v", got)
	}
}

func TestMaskedEmissionTintAOBakedWithHDRStrengthPreserved(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.pixel = [4]byte{128, 64, 32, 255}; o.space = "srgb" })
	f.image("ao", func(o *imageOptions) { o.channel = "r" })
	f.image("emissive", func(o *imageOptions) { o.channel = "r" })
	yes, sixteen := true, 16.0
	f.material.EmissiveMode, f.material.EmissiveUsesAO, f.material.EmissiveIntensity = "baseColorMask", &yes, &sixteen
	f.material.Emissive = [3]float64{.5, 1, 1}
	material := f.bake("")
	var want [3]byte
	for i, c := range []float64{128, 64, 32} {
		want[i] = srgb(linear(c) * (128.0 / 255) * (128.0 / 255) * []float64{.5, 1, 1}[i])
	}
	if got := pixelAt(t, material, "emissive", 0); got != want {
		t.Fatalf("pixel %v, want %v", got, want)
	}
	if *material.EmissiveIntensity != 16 || material.Emissive != [3]float64{1, 1, 1} {
		t.Fatalf("%+v", material)
	}
	if material.EmissiveMode != "" || material.EmissiveUsesAO != nil {
		t.Fatal("emission controls kept")
	}
}

func TestEmissionThatIgnoresAOUsesOriginalUnoccludedBaseColor(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	f.image("ao", func(o *imageOptions) { o.pixel = [4]byte{0, 0, 0, 255}; o.channel = "r" })
	f.image("emissive", func(o *imageOptions) { o.pixel = [4]byte{255, 255, 255, 255}; o.channel = "r" })
	no := false
	f.material.EmissiveMode, f.material.EmissiveUsesAO = "baseColorMask", &no
	material := f.bake("")
	if got := pixelAt(t, material, "baseColor", 0); got != gray(0) {
		t.Fatal(got)
	}
	if got := pixelAt(t, material, "emissive", 0); got != gray(128) {
		t.Fatal(got)
	}
}

func TestColorEmissionRetainsRadianceAbovePNGRangeAsFactor(t *testing.T) {
	f := newFixture(t)
	f.image("emissive", func(o *imageOptions) { o.pixel = [4]byte{255, 128, 0, 255}; o.space = "srgb" })
	four := 4.0
	f.material.Emissive, f.material.EmissiveIntensity = [3]float64{2, 1, 1}, &four
	material := f.bake("")
	if *material.EmissiveIntensity != 8 {
		t.Fatal(*material.EmissiveIntensity)
	}
	if got, want := pixelAt(t, material, "emissive", 0), [3]byte{255, srgb(linear(128) / 2), 0}; got != want {
		t.Fatalf("pixel %v, want %v", got, want)
	}
}

func TestAOUVOrRepeatMismatchRetainedInManifestWithWarning(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb"; o.repeat = [2]float64{2, 1} })
	f.image("ao", func(o *imageOptions) { o.pixel = [4]byte{0, 0, 0, 255}; o.channel = "r"; o.uv = 1 })
	material := f.bake("")
	if got := pixelAt(t, material, "baseColor", 0); got != gray(128) {
		t.Fatal(got)
	}
	if mapOf(t, material, "ao").UVIndex != 1 || mapOf(t, material, "baseColor").Repeat != [2]float64{2, 1} {
		t.Fatal("mappings")
	}
	if !f.hasWarning("different UV") {
		t.Fatal(f.result.Warnings)
	}
}

func TestEmissionUVMismatchNotMisrepresentedAsBakedRGB(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.pixel = [4]byte{255, 0, 0, 255}; o.space = "srgb" })
	f.image("emissive", func(o *imageOptions) { o.uv = 1 })
	f.material.EmissiveMode = "baseColorMask"
	material := f.bake("")
	if material.EmissiveMode != "baseColorMask" || len(material.UnbakedMaps) != 1 || material.UnbakedMaps[0] != "emissive" {
		t.Fatalf("%+v", material)
	}
	if mapOf(t, material, "emissive").UVIndex != 1 {
		t.Fatal("uv")
	}
	if !f.hasWarning("requires the source shader") {
		t.Fatal(f.result.Warnings)
	}
}

func TestPackedPBRNormalAndAlphaSharePreviewChannelMath(t *testing.T) {
	f := newFixture(t)
	for _, c := range [][3]string{{"roughness", "g", "invert"}, {"metalness", "r", "ow-metalness"},
		{"normal", "rgb", "ow-normal"}, {"alpha", "a", "none"}} {
		f.image(c[0], func(o *imageOptions) { o.pixel = [4]byte{153, 51, 5, 17}; o.channel = c[1]; o.transform = c[2] })
	}
	f.material.Roughness = .5
	material := f.bake("")
	if got := pixelAt(t, material, "roughness", 0); got != gray(102) {
		t.Fatal(got)
	}
	if got := pixelAt(t, material, "alpha", 0); got != gray(17) {
		t.Fatal(got)
	}
	for _, v := range pixelAt(t, material, "metalness", 0) {
		if v < 50 || v > 52 {
			t.Fatal(v)
		}
	}
	expected, err := textures.ProcessChannels(decode(t, filepath.Join(f.root, "normal.png")), textures.Spec{Transform: "ow-normal"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, material, "normal", 0); !bytes.Equal(got[:], expected.Pix[:3]) {
		t.Fatalf("%v %v", got, expected.Pix[:3])
	}
}

func TestExportKeepsOriginalResolutionAbovePreviewLimit(t *testing.T) {
	f := newFixture(t)
	f.image("normal", func(o *imageOptions) { o.width, o.height = 2050, 2 })
	material := f.bake("")
	image := decode(t, mapOf(t, material, "normal").Path)
	if image.Width != 2050 || image.Height != 2 {
		t.Fatal(image.Width, image.Height)
	}
}

func TestPNGReferencesRelativeAndSourceAndAssetUnchanged(t *testing.T) {
	f := newFixture(t)
	source := f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	original, _ := os.ReadFile(source)
	before, _ := json.Marshal(f.asset)
	material := f.bake("../invalid/prefix:?")
	reference := mapOf(t, material, "baseColor")
	if reference.RelativePath != "tex/"+filepath.Base(reference.Path) {
		t.Fatal(reference.RelativePath)
	}
	if !pypath.IsRelativeTo(reference.Path, f.output) {
		t.Fatal(reference.Path)
	}
	if !strings.HasPrefix(filepath.Base(reference.Path), "invalid_prefix_") {
		t.Fatal(reference.Path)
	}
	if encoded, _ := json.Marshal(reference); strings.Contains(string(encoded), "textureId") {
		t.Fatal(string(encoded))
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(after, original) {
		t.Fatal("source changed")
	}
	if now, _ := json.Marshal(f.asset); !bytes.Equal(now, before) {
		t.Fatal("asset changed")
	}
}

func TestRepeatBakeReusesSameFilesWithoutRewriting(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	path := mapOf(t, f.bake("walk"), "baseColor").Path
	info, _ := os.Stat(path)
	again := f.bake("walk")
	if mapOf(t, again, "baseColor").Path != path {
		t.Fatal("path changed")
	}
	if now, _ := os.Stat(path); !now.ModTime().Equal(info.ModTime()) {
		t.Fatal("rewritten")
	}
	if entries, _ := os.ReadDir(f.output); len(entries) != 1 {
		t.Fatal(len(entries))
	}
}

func TestChangedSourceContentGetsNewNameAndKeepsPreviousExport(t *testing.T) {
	f := newFixture(t)
	source := f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	previous := mapOf(t, f.bake(""), "baseColor").Path
	previousBytes, _ := os.ReadFile(previous)
	os.WriteFile(source, ft.PNGBytes(1, 1, func(int, int) [4]byte { return [4]byte{255, 0, 0, 255} }), 0o644)
	current := mapOf(t, f.bake(""), "baseColor").Path
	if current == previous {
		t.Fatal("same name")
	}
	if now, _ := os.ReadFile(previous); !bytes.Equal(now, previousBytes) {
		t.Fatal("previous changed")
	}
	if entries, _ := os.ReadDir(f.output); len(entries) != 2 {
		t.Fatal(len(entries))
	}
}

func TestConflictingExistingFilenameIsNeverOverwritten(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	first := mapOf(t, f.bake(""), "baseColor").Path
	os.WriteFile(first, []byte("unrelated existing data"), 0o644)
	second := mapOf(t, f.bake(""), "baseColor").Path
	if first == second || !strings.HasSuffix(second, "_2.png") {
		t.Fatal(first, second)
	}
	if data, _ := os.ReadFile(first); string(data) != "unrelated existing data" {
		t.Fatal("overwritten")
	}
	if mapOf(t, f.bake(""), "baseColor").Path != second {
		t.Fatal("not reused")
	}
}

func TestSameMaterialInChildModelReusesFilesButKeepsBindings(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	childMaterials := ordered.New[*formats.Material]()
	childMaterials.Set("9000", f.material.Clone())
	f.asset.Models = append(f.asset.Models, &formats.Model{ID: "model-child", Name: "Example", Materials: childMaterials})
	var calls atomic.Int32
	processChannels = func(source *textures.Image, spec textures.Spec) (*textures.Image, error) {
		calls.Add(1)
		return textures.ProcessChannels(source, spec)
	}
	defer func() { processChannels = textures.ProcessChannels }()
	f.bake("")
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if keys := f.result.Materials.Keys(); len(keys) != 2 || keys[1] != "model-child" {
		t.Fatal(keys)
	}
	if len(f.result.Files) != 1 {
		t.Fatal(f.result.Files)
	}
	child, _ := f.result.Materials.Get("model-child")
	childMaterial, _ := child.Get("9000")
	mapOf(t, childMaterial, "baseColor").Repeat[0] = 99
	parent, _ := f.result.Materials.Get("model-0")
	parentMaterial, _ := parent.Get("9000")
	if mapOf(t, parentMaterial, "baseColor").Repeat != [2]float64{1, 1} {
		t.Fatal("aliased")
	}
}

func TestUnregisteredOrExternalSourceIsOmittedAndReported(t *testing.T) {
	f := newFixture(t)
	f.image("normal", nil)
	texture, _ := f.asset.Textures.Get("normal")
	texture.Path = filepath.Join(filepath.Dir(f.root), "outside.png")
	f.material.Maps.Set("baseColor", &formats.MaterialMap{TextureID: "missing"})
	material := f.bake("")
	if material.Maps.Len() != 0 || len(f.result.Files) != 0 {
		t.Fatal(material.Maps.Keys(), f.result.Files)
	}
	if !f.hasWarning("not registered") || !f.hasWarning("outside the export root") {
		t.Fatal(f.result.Warnings)
	}
}

func TestProgressOrderAndRepeatedMaterials(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.space = "srgb" })
	for i := range 5 {
		materials := ordered.New[*formats.Material]()
		materials.Set("9000", f.material)
		materials.Set("1", &formats.Material{Name: "Plain", Maps: ordered.New[*formats.MaterialMap]()})
		f.asset.Models = append(f.asset.Models, &formats.Model{ID: "m" + string(rune('a'+i)), Materials: materials})
	}
	var seen [][2]int
	result, err := BakeMaterials(f.asset, f.output, "", func(message string, completed, total int) {
		if message != ProgressMessage {
			t.Fatal(message)
		}
		seen = append(seen, [2]int{completed, total})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 12 {
		t.Fatal(seen)
	}
	for i, s := range seen {
		if s != [2]int{i, 11} {
			t.Fatal(seen)
		}
	}
	if len(result.Files) != 1 {
		t.Fatal(result.Files)
	}
}

func TestCombinedSizeLimitRaises(t *testing.T) {
	f := newFixture(t)
	f.image("baseColor", func(o *imageOptions) { o.width, o.height = 8193, 1 })
	f.image("ao", func(o *imageOptions) { o.width, o.height = 1, 8193; o.channel = "r" })
	_, err := BakeMaterials(f.asset, f.output, "", nil)
	if err == nil || err.Error() != "Combined texture dimensions exceed the 64-megapixel export limit." {
		t.Fatal(err)
	}
}

func TestNonFiniteControlsRaiseLikeJSONDumps(t *testing.T) {
	f := newFixture(t)
	f.material.Opacity = math.Inf(-1)
	_, err := BakeMaterials(f.asset, f.output, "", nil)
	if err == nil || err.Error() != "Out of range float values are not JSON compliant: -inf" {
		t.Fatal(err)
	}
}

func TestSanitizePrefix(t *testing.T) {
	for in, want := range map[string]string{"": "", "walk": "walk_", "../invalid/prefix:?": "invalid_prefix_",
		" .._ ": "", "日本 語": "日本_語_", strings.Repeat("a", 60): strings.Repeat("a", 48) + "_"} {
		if got := sanitizePrefix(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
