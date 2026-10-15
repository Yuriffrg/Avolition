package textures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/oracle"
)

func readDecoded(t testing.TB, path string) *Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := DecodeImage(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

var processSpecs = []Spec{
	{Transform: "ow-normal"},
	{Channel: "luminance", InputColorSpace: "linear"},
	{Channel: "luminance", InputColorSpace: "srgb"},
	{Channel: "luminance", InputColorSpace: "srgb", Transform: "invert"},
	{Channel: "luminance", Transform: "ow-metalness"},
	{Channel: "r", InputColorSpace: "srgb"},
	{Channel: "g", InputColorSpace: "srgb", Transform: "ow-metalness"},
	{Channel: "b", Transform: "invert"},
	{Channel: "a", InputColorSpace: "srgb"},
	{Channel: "rgb"},
	{Channel: "rgba"},
	{},
}

// Every 24-bit RGB combination through Python's process_channels (ImageMath
// float32 expressions) must give exactly the same bytes in Go.
func TestProcessChannelsExhaustiveMatchesPython(t *testing.T) {
	if testing.Short() {
		t.Skip("exhaustive 16.7 M pixel comparison")
	}
	folder := t.TempDir()
	result := oracle.Exec(t, `
import numpy as np
from PIL import Image
from owpreview.textures import process_channels
folder, specs = args[0], args[1]
i = np.arange(4096 * 4096, dtype=np.uint32)
pixels = np.stack([i & 255, (i >> 8) & 255, i >> 16, (i * 7) & 255], axis=-1).astype(np.uint8).reshape(4096, 4096, 4)
source = Image.fromarray(pixels, 'RGBA')
source.save(folder + '/source.png', compress_level=1)
for n, spec in enumerate(specs):
    spec = {k: v for k, v in spec.items() if v}
    process_channels(source, spec).save(folder + '/out%d.png' % n, compress_level=1)
result = True`, folder, processSpecs)
	result.MustValue(t)
	source := readDecoded(t, filepath.Join(folder, "source.png"))
	for n, spec := range processSpecs {
		got, err := ProcessChannels(source, spec)
		if err != nil {
			t.Fatal(err)
		}
		want := readDecoded(t, filepath.Join(folder, fmt.Sprintf("out%d.png", n)))
		mismatches := 0
		for p := 0; p < got.Width*got.Height; p++ {
			if !bytes.Equal(got.Pix[p*got.Channels:(p+1)*got.Channels], want.Pix[p*4:p*4+got.Channels]) {
				if mismatches < 5 {
					t.Errorf("%+v: rgba %v: Go %v, Python %v", spec, source.Pix[p*4:p*4+4],
						got.Pix[p*got.Channels:(p+1)*got.Channels], want.Pix[p*4:p*4+4])
				}
				mismatches++
			}
		}
		if mismatches > 0 {
			t.Errorf("%+v: %d of %d pixels differ", spec, mismatches, got.Width*got.Height)
		}
	}
}

// TextureStore.load in Python and Store.Load in Go return the same pixels,
// sizes and warnings, including Lanczos reductions to several edges.
func TestLoadMatchesPythonTextureStore(t *testing.T) {
	folder := t.TempDir()
	type request struct {
		ID   string `json:"id"`
		Edge int    `json:"edge"`
	}
	specs := map[string]Spec{}
	var requests []request
	names := []string{"wide.png", "tall.tga", "packed.dds", "small.webp", "odd.tif", "photo.jpg"}
	for i, name := range names {
		for j, spec := range processSpecs[:9] {
			id := fmt.Sprintf("t%d-%d", i, j)
			spec.Path = filepath.Join(folder, name)
			specs[id] = spec
			for _, edge := range []int{0, 2048, 777, 100, 33} {
				requests = append(requests, request{id, edge})
			}
		}
	}
	result := oracle.Exec(t, `
import numpy as np
from PIL import Image
from owpreview.textures import TextureStore
folder, specs, requests = args[0], args[1], args[2]
rng = np.random.default_rng(7)
def pattern(w, h):
    yy, xx = np.mgrid[0:h, 0:w]
    base = np.stack([(xx * 255) // max(1, w - 1), (yy * 255) // max(1, h - 1), (xx ^ yy) & 255, ((xx + yy) * 3) & 255], -1)
    noise = rng.integers(0, 256, (h, w, 4))
    return np.where(rng.random((h, w, 1)) < 0.2, noise, base).astype(np.uint8)
Image.fromarray(pattern(2500, 1000), 'RGBA').save(folder + '/wide.png')
Image.fromarray(pattern(301, 2100), 'RGBA').save(folder + '/tall.tga', compression='tga_rle')
Image.fromarray(pattern(1030, 517), 'RGBA').save(folder + '/packed.dds', pixel_format='DXT5')
Image.fromarray(pattern(257, 129), 'RGBA').save(folder + '/small.webp', quality=80)
Image.fromarray(pattern(999, 37), 'RGBA').convert('RGB').save(folder + '/odd.tif', compression='tiff_lzw')
Image.fromarray(pattern(640, 480), 'RGBA').convert('RGB').save(folder + '/photo.jpg', quality=92)
store = TextureStore()
store.register({k: {key: value for key, value in v.items() if value} for k, v in specs.items()})
out = []
for r in requests:
    try:
        out.append(store.load(r['id'], r['edge'] or None))
    except Exception as e:
        out.append({'error': str(e)})
result = out`, folder, specs, requests)
	var want []struct {
		Result
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	store.Register(specs, "")
	jpegDelta := 0
	for i, r := range requests {
		got, err := store.Load(r.ID, r.Edge)
		if want[i].Error != "" || err != nil {
			if err == nil || err.Error() != want[i].Error {
				t.Errorf("%v: Go %v, Python %q", r, err, want[i].Error)
			}
			continue
		}
		if got.Width != want[i].Width || got.Height != want[i].Height || got.Warning != want[i].Warning {
			t.Errorf("%v: Go %dx%d %q, Python %dx%d %q", r, got.Width, got.Height, got.Warning, want[i].Width, want[i].Height, want[i].Warning)
			continue
		}
		a, b := unpack(t, got), unpack(t, &want[i].Result)
		// JPEG samples differ by up to jpegTolerance (IDCT); nonlinear
		// transforms (normals, thresholds, sRGB) amplify that, so only the
		// linear outputs are held to the decode tolerance.
		spec := specs[r.ID]
		isJPEG := spec.Path == filepath.Join(folder, "photo.jpg")
		linear := spec.Transform != "ow-normal" && spec.Transform != "ow-metalness" && spec.InputColorSpace != "srgb"
		for p := range a.Pix {
			d := int(a.Pix[p]) - int(b.Pix[p])
			if d < 0 {
				d = -d
			}
			if isJPEG {
				jpegDelta = max(jpegDelta, d)
				if !linear {
					continue
				}
				d = max(0, d-jpegTolerance)
			}
			if d != 0 {
				t.Errorf("%v (%+v): pixel %d differs: Go %d, Python %d", r, specs[r.ID], p/4, a.Pix[p], b.Pix[p])
				break
			}
		}
	}
	t.Logf("%d loads compared; JPEG max output delta %d (nonlinear transforms of decode differences), all other formats exact", len(requests), jpegDelta)
}
