package textures

// Ported from tests/test_textures.py.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/oracle"
)

func writePNG(t testing.TB, path string, width, height int, fill color.NRGBA, opaque bool) {
	t.Helper()
	var img image.Image
	if opaque {
		rgba := image.NewRGBA(image.Rect(0, 0, width, height))
		for i := 0; i < len(rgba.Pix); i += 4 {
			rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3] = fill.R, fill.G, fill.B, 255
		}
		img = rgba
	} else {
		nrgba := image.NewNRGBA(image.Rect(0, 0, width, height))
		for i := 0; i < len(nrgba.Pix); i += 4 {
			nrgba.Pix[i], nrgba.Pix[i+1], nrgba.Pix[i+2], nrgba.Pix[i+3] = fill.R, fill.G, fill.B, fill.A
		}
		img = nrgba
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// unpack decodes a data URI to RGBA bytes.
func unpack(t testing.TB, result *Result) *Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result.DataURI(), dataURIPrefix))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return toRGBA(decoded)
}

func pixel(img *Image, x, y int) []byte {
	i := (y*img.Width + x) * img.Channels
	return img.Pix[i : i+img.Channels]
}

func mustLoad(t testing.TB, store *Store, id string, edge int) *Result {
	t.Helper()
	result, err := store.Load(id, edge)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestModelBrowserReplacementAndReleasePreserveSceneOwnersAndCaches(t *testing.T) {
	folder := t.TempDir()
	var specs []Spec
	for _, name := range []string{"animation", "map", "preview", "replacement"} {
		path := filepath.Join(folder, name+".png")
		writePNG(t, path, 2, 2, color.NRGBA{20, 40, 60, 255}, true)
		specs = append(specs, Spec{Path: path, Channel: "rgb"})
	}
	store := NewStore()
	store.Register(map[string]Spec{"animation": specs[0], "shared": specs[0]}, "animations")
	store.Register(map[string]Spec{"map": specs[1]}, "maps")
	animation, mapImage, shared := mustLoad(t, store, "animation", 0), mustLoad(t, store, "map", 0), mustLoad(t, store, "shared", 0)
	store.Register(map[string]Spec{"preview": specs[2], "shared": specs[0]}, "model-browser")
	if mustLoad(t, store, "preview", 0).Width != 2 {
		t.Fatal("preview width")
	}
	store.Register(map[string]Spec{"replacement": specs[3], "shared": specs[0]}, "model-browser")
	if _, err := store.Load("preview", 0); err == nil {
		t.Fatal("stale preview loaded")
	}
	if mustLoad(t, store, "replacement", 0).Width != 2 {
		t.Fatal("replacement")
	}
	check := func() {
		if mustLoad(t, store, "animation", 0) != animation || mustLoad(t, store, "map", 0) != mapImage || mustLoad(t, store, "shared", 0) != shared {
			t.Fatal("scene caches were evicted")
		}
	}
	check()
	store.Release("model-browser")
	store.Release("model-browser") // Closing an already closed browser is harmless.
	if _, err := store.Load("replacement", 0); err == nil {
		t.Fatal("released texture loaded")
	}
	check()
	if len(store.owners) != 2 || store.owners[0].owner != "animations" || store.owners[1].owner != "maps" {
		t.Fatalf("owners %v", store.owners)
	}
	if store.imageBytes != 2*2*4*2 {
		t.Fatalf("image bytes %d", store.imageBytes)
	}
	if store.resultBytes != len(animation.PNG)+len(mapImage.PNG)+len(shared.PNG) {
		t.Fatalf("result bytes %d", store.resultBytes)
	}
	if err := store.Release("other"); err == nil || err.Error() != "Unknown texture workspace." {
		t.Fatal(err)
	}
	if err := store.Register(nil, "other"); err == nil || err.Error() != "Unknown texture workspace." {
		t.Fatal(err)
	}
}

func TestMapPreviewSizeIsSeparateFromCharacterCacheAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "color.png")
	writePNG(t, path, 256, 128, color.NRGBA{240, 180, 60, 0}, false)
	original, _ := os.ReadFile(path)
	store := NewStore()
	store.Register(map[string]Spec{"color": {Path: path, Channel: "rgb"}}, "")
	small, full := mustLoad(t, store, "color", 64), mustLoad(t, store, "color", 0)
	if small.Width != 64 || small.Height != 32 || full.Width != 256 || full.Height != 128 {
		t.Fatalf("sizes %+v %+v", small, full)
	}
	if mustLoad(t, store, "color", 64) != small {
		t.Fatal("not cached")
	}
	if got := pixel(unpack(t, small), 0, 0); !bytes.Equal(got, []byte{240, 180, 60, 255}) {
		t.Fatalf("pixel %v", got)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, original) {
		t.Fatal("source modified")
	}
	for _, edge := range []string{"true", "0", "31", "2049", `"128"`, "64.5", "64.0", "1e2", "[64]"} {
		if _, err := store.LoadJSON([]byte(`"color"`), []byte(edge)); err == nil || err.Error() != "Preview texture size must be an integer from 32 to 2048." {
			t.Errorf("edge %s: %v", edge, err)
		}
	}
	if result, err := store.LoadJSON([]byte(`"color"`), []byte("null")); err != nil || result != full {
		t.Fatal("null edge")
	}
	if _, err := store.LoadJSON([]byte(`7`), nil); err == nil || err != errUnregistered {
		t.Fatal("non-string id")
	}
}

func TestMapAndAnimationTextureRegistrationsRemainIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "color.png")
	writePNG(t, path, 2, 2, color.NRGBA{255, 0, 0, 255}, true)
	spec := Spec{Path: path, Channel: "rgb"}
	store := NewStore()
	store.Register(map[string]Spec{"map": spec}, "maps")
	store.Register(map[string]Spec{"character": spec}, "animations")
	mustLoad(t, store, "map", 0)
	mustLoad(t, store, "character", 0)
	store.Register(map[string]Spec{"new_map": spec}, "maps")
	if _, err := store.Load("map", 0); err == nil {
		t.Fatal("old map texture loaded")
	}
	mustLoad(t, store, "character", 0)
	mustLoad(t, store, "new_map", 0)
}

func singlePixel(r, g, b, a byte) *Image { return &Image{1, 1, 4, []byte{r, g, b, a}} }

func process(t *testing.T, source *Image, spec Spec) *Image {
	t.Helper()
	result, err := ProcessChannels(source, spec)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func near(a, b, tolerance int) bool { return a-b <= tolerance && b-a <= tolerance }

func TestExactPackedPBRScalarChannels(t *testing.T) {
	source := singlePixel(153, 51, 90, 17)
	if got := process(t, source, Spec{Channel: "g", Transform: "invert"}).Pix; !bytes.Equal(got, []byte{204, 204, 204}) {
		t.Fatal(got)
	}
	for _, v := range process(t, source, Spec{Channel: "r", Transform: "ow-metalness"}).Pix {
		if !near(int(v), 51, 1) {
			t.Fatal(v)
		}
	}
}

func TestMetalnessThresholdAndFullRange(t *testing.T) {
	source := &Image{4, 1, 4, []byte{0, 0, 0, 255, 133, 0, 0, 255, 134, 0, 0, 255, 255, 0, 0, 255}}
	values := process(t, source, Spec{Channel: "r", Transform: "ow-metalness"}).Pix
	if !bytes.Equal(values[0:6], []byte{0, 0, 0, 0, 0, 0}) || !bytes.Equal(values[9:12], []byte{255, 255, 255}) {
		t.Fatal(values)
	}
	for _, v := range values[6:9] {
		if v < 12 || v > 14 {
			t.Fatal(values)
		}
	}
}

func TestStereographicNormalNotConventionalRGReconstruction(t *testing.T) {
	actual := process(t, singlePixel(191, 64, 5, 0), Spec{Transform: "ow-normal"}).Pix
	r, g, b := 191.0/255, 64.0/255, 5.0/255
	f := 31.875*b + 1
	x, y := (2*r-1)/f, (2*g-1)/f
	q := 2 / (1 + x*x + y*y)
	expected := []float64{(q*x + 1) * 127.5, (q*y + 1) * 127.5, q * 127.5}
	for i := range expected {
		if d := float64(actual[i]) - expected[i]; d > 1 || d < -1 {
			t.Fatal(actual, expected)
		}
	}
	if actual[1] >= 128 {
		t.Fatal("Blender does not invert this normal Y channel")
	}
}

func TestLuminanceRespectsSourceColorSpace(t *testing.T) {
	source := singlePixel(128, 128, 128, 64)
	linear := process(t, source, Spec{Channel: "luminance", InputColorSpace: "linear"}).Pix
	srgb := process(t, source, Spec{Channel: "luminance", InputColorSpace: "srgb"}).Pix
	alpha := process(t, source, Spec{Channel: "a", InputColorSpace: "srgb"}).Pix
	red := process(t, source, Spec{Channel: "r", InputColorSpace: "srgb"}).Pix
	if !near(int(linear[0]), 128, 1) || !near(int(srgb[0]), 55, 1) || !bytes.Equal(alpha, []byte{64, 64, 64}) || !near(int(red[0]), 55, 1) {
		t.Fatal(linear, srgb, alpha, red)
	}
}

func TestProcessErrors(t *testing.T) {
	source := singlePixel(1, 2, 3, 4)
	for spec, message := range map[Spec]string{
		{Channel: "x"}:                               "Unsupported texture channel: x",
		{Channel: "rgb", Transform: "invert"}:        "Inverted packed channels must be scalar.",
		{Channel: "rgba", Transform: "ow-metalness"}: "Packed metalness must be a scalar channel.",
		{Channel: "r", Transform: "spin"}:            "Unsupported texture transform: spin",
		{Channel: "rgb", Transform: "spin"}:          "Unsupported texture transform: spin",
	} {
		if _, err := ProcessChannels(source, spec); err == nil || err.Error() != message {
			t.Errorf("%+v: %v", spec, err)
		}
	}
}

func TestPNGTGAAndDDSDecodePreservePixelsAndOriginals(t *testing.T) {
	folder := t.TempDir()
	result := oracle.Exec(t, `
from PIL import Image
for extension in ('png', 'tga', 'dds'):
    image = Image.new('RGBA', (2, 2))
    image.putdata([(255, 0, 0, 128), (0, 255, 0, 255), (0, 0, 255, 0), (15, 27, 39, 255)])
    image.save(args[0] + '/texture.' + extension)
result = True`, folder)
	result.MustValue(t)
	want := []byte{255, 0, 0, 128, 0, 255, 0, 255, 0, 0, 255, 0, 15, 27, 39, 255}
	for _, extension := range []string{"png", "tga", "dds"} {
		path := filepath.Join(folder, "texture."+extension)
		original, _ := os.ReadFile(path)
		store := NewStore()
		store.Register(map[string]Spec{"known": {Path: path, Channel: "rgba"}}, "")
		if got := unpack(t, mustLoad(t, store, "known", 0)); !bytes.Equal(got.Pix, want) {
			t.Errorf("%s: %v", extension, got.Pix)
		}
		if now, _ := os.ReadFile(path); !bytes.Equal(now, original) {
			t.Errorf("%s modified", extension)
		}
	}
}

func TestUnregisteredPathAndStaleIdentifierRejected(t *testing.T) {
	store := NewStore()
	if _, err := store.Load("C:/private/example.png", 0); err != errUnregistered {
		t.Fatal(err)
	}
	store.Register(map[string]Spec{"old": {Path: "unused.png"}}, "")
	store.Register(map[string]Spec{}, "")
	if _, err := store.Load("old", 0); err != errUnregistered {
		t.Fatal(err)
	}
}

func TestDecodedCacheResetsWhenAssetReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "texture.png")
	writePNG(t, path, 2, 2, color.NRGBA{255, 0, 0, 255}, true)
	store := NewStore()
	specs := map[string]Spec{"color": {Path: path, Channel: "rgb"}}
	store.Register(specs, "")
	first := mustLoad(t, store, "color", 0)
	if mustLoad(t, store, "color", 0) != first {
		t.Fatal("not cached")
	}
	writePNG(t, path, 2, 2, color.NRGBA{0, 255, 0, 255}, true)
	store.Register(specs, "")
	if got := pixel(unpack(t, mustLoad(t, store, "color", 0)), 0, 0); !bytes.Equal(got, []byte{0, 255, 0, 255}) {
		t.Fatal(got)
	}
}

func TestDownsamplingDoesNotUsePackedAlphaAsOpacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packed.png")
	writePNG(t, path, 8, 4, color.NRGBA{240, 180, 60, 0}, false)
	store := NewStore()
	store.Register(map[string]Spec{"known": {Path: path, Channel: "rgb"}}, "")
	defer func(saved int) { maxPreviewEdge = saved }(maxPreviewEdge)
	maxPreviewEdge = 2
	result := mustLoad(t, store, "known", 0)
	if result.Width != 2 || result.Height != 1 {
		t.Fatalf("%+v", result)
	}
	if got := pixel(unpack(t, result), 0, 0); !bytes.Equal(got, []byte{240, 180, 60, 255}) {
		t.Fatal(got)
	}
	if !strings.Contains(result.Warning, "resized") || result.Warning != "packed.png: preview resized to 2 × 1" {
		t.Fatal(result.Warning)
	}
}

func TestLoadErrorsMatchPython(t *testing.T) {
	folder := t.TempDir()
	os.WriteFile(filepath.Join(folder, "junk.png"), []byte("not an image at all"), 0o644)
	os.WriteFile(filepath.Join(folder, "note.txt"), []byte("x"), 0o644)
	cases := []string{"missing.png", "junk.png", "note.txt", "noext"}
	specs := map[string]Spec{}
	for _, name := range cases {
		specs[name] = Spec{Path: filepath.Join(folder, name), Channel: "rgb"}
	}
	store := NewStore()
	store.Register(specs, "")
	for _, name := range cases {
		result := oracle.Exec(t, `
from owpreview.textures import TextureStore
store = TextureStore()
store.register({'x': {'path': args[0], 'channel': 'rgb'}})
store.load('x')`, filepath.Join(folder, name))
		_, err := store.Load(name, 0)
		if result.OK || err == nil || err.Error() != result.Error {
			t.Errorf("%s: Go %v, Python %s", name, err, result)
		}
	}
}
