package textures

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"avolition/gocore/internal/bcenc"
)

// The block encoders are checked by decoding their output with the Pillow
// port in bcn.go.

func decodeFormat(t testing.TB, format string, data []byte, width, height int) *Image {
	t.Helper()
	name := map[string]string{bcenc.BC1: "BC1", bcenc.BC3: "BC3", bcenc.BC7: "BC7"}[format]
	image, err := decodeBCn(data, width, height, name)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

// psnr compares the first channels of two RGBA images.
func psnr(a, b []byte, channels int) float64 {
	var sum float64
	n := 0
	for i := 0; i < len(a); i += 4 {
		for c := range channels {
			d := float64(a[i+c]) - float64(b[i+c])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}

// testImage is a mix of smooth gradients, hard edges and flat areas, plus a
// noisy quarter for even seeds; alpha holds an independent pattern when
// withAlpha.
func testImage(width, height int, withAlpha bool, seed uint64) *Image {
	random := rand.New(rand.NewPCG(seed, 7))
	image := NewRGBA(width, height)
	for y := range height {
		for x := range width {
			o := (y*width + x) * 4
			fx, fy := float64(x)/float64(width), float64(y)/float64(height)
			r := 128 + 100*math.Sin(fx*9+fy*3)
			g := 128 + 90*math.Cos(fy*7-fx*2)
			b := 255 * fx * fy
			if (x/37+y/23)%5 == 0 { // hard-edged patches
				r, g, b = 230, 40, 30
			}
			if seed%2 == 0 && x > width*3/4 { // noisy region (even seeds)
				r += random.NormFloat64() * 20
				g += random.NormFloat64() * 20
				b += random.NormFloat64() * 20
			}
			a := 255.0
			if withAlpha {
				a = 255 * (0.5 + 0.5*math.Sin(fx*13-fy*5))
				if (x/16+y/16)%7 == 0 {
					a = 0
				}
			}
			for c, v := range []float64{r, g, b, a} {
				image.Pix[o+c] = byte(min(max(math.Round(v), 0), 255))
			}
		}
	}
	return image
}

func TestBlockEncodersRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		format    string
		alpha     bool
		minPSNR   float64
		minAlphaP float64
	}{
		{bcenc.BC1, false, 33, 0},
		{bcenc.BC3, true, 33, 38},
		{bcenc.BC7, false, 38, 0},
		{bcenc.BC7, true, 36, 36},
	} {
		t.Run(fmt.Sprintf("%s-alpha=%v", tc.format, tc.alpha), func(t *testing.T) {
			image := testImage(256, 128, tc.alpha, 1)
			data := bcenc.Encode(tc.format, image.Pix, image.Width, image.Height)
			if len(data) != bcenc.Size(tc.format, image.Width, image.Height) {
				t.Fatalf("size %d", len(data))
			}
			decoded := decodeFormat(t, tc.format, data, image.Width, image.Height)
			rgb := psnr(image.Pix, decoded.Pix, 3)
			if rgb < tc.minPSNR {
				t.Errorf("RGB PSNR %.2f dB < %.0f", rgb, tc.minPSNR)
			}
			if tc.alpha {
				var a, b []byte
				for i := 3; i < len(image.Pix); i += 4 {
					a = append(a, image.Pix[i], 0, 0, 0)
					b = append(b, decoded.Pix[i], 0, 0, 0)
				}
				if alpha := psnr(a, b, 1); alpha < tc.minAlphaP {
					t.Errorf("alpha PSNR %.2f dB < %.0f", alpha, tc.minAlphaP)
				}
			} else {
				for i := 3; i < len(decoded.Pix); i += 4 {
					if decoded.Pix[i] < 254 {
						t.Fatalf("opaque image decoded with alpha %d", decoded.Pix[i])
					}
				}
			}
			t.Logf("%s: RGB PSNR %.2f dB", tc.format, rgb)
		})
	}
}

// Flat blocks, two-colour blocks and odd sizes (edge blocks repeat the last
// row and column) decode exactly or nearly so.
func TestBlockEncodersSimpleBlocks(t *testing.T) {
	for _, format := range CompressedFormats {
		for _, size := range [][2]int{{4, 4}, {1, 1}, {2, 2}, {6, 10}} {
			image := NewRGBA(size[0], size[1])
			for i := range size[0] * size[1] {
				copy(image.Pix[i*4:], []byte{200, 17, 99, 255})
			}
			decoded := decodeFormat(t, format, bcenc.Encode(format, image.Pix, size[0], size[1]), size[0], size[1])
			for i := 0; i < len(image.Pix); i++ {
				limit := 3
				if format == bcenc.BC7 {
					limit = 1
				}
				if d := int(image.Pix[i]) - int(decoded.Pix[i]); d > limit || d < -limit {
					t.Fatalf("%s %v: byte %d is %d, want %d", format, size, i, decoded.Pix[i], image.Pix[i])
				}
			}
		}
		// Black and white checker: two exact endpoints.
		image := NewRGBA(8, 8)
		for i := range 64 {
			v := byte(255 * ((i/8 + i%8) % 2))
			copy(image.Pix[i*4:], []byte{v, v, v, 255})
		}
		decoded := decodeFormat(t, format, bcenc.Encode(format, image.Pix, 8, 8), 8, 8)
		// BC7 mode 6 keeps opaque endpoints' alpha exact, so its shared
		// p-bit may round black to 1.
		for i := range decoded.Pix {
			if d := int(decoded.Pix[i]) - int(image.Pix[i]); d > 1 || d < 0 || d == 1 && format != bcenc.BC7 {
				t.Fatalf("%s: checker not exact: %v", format, decoded.Pix[:16])
			}
		}
	}
}

func TestMipChain(t *testing.T) {
	image := testImage(64, 16, false, 2)
	levels := bcenc.MipChain(image.Pix, 64, 16, false)
	var sizes [][2]int
	for _, level := range levels {
		sizes = append(sizes, [2]int{level.Width, level.Height})
	}
	if fmt.Sprint(sizes) != "[[64 16] [32 8] [16 4] [8 2] [4 1] [2 1] [1 1]]" {
		t.Fatalf("levels %v", sizes)
	}
	// Box filter: level 1 pixel (0,0) averages the 2×2 corner.
	want := (int(image.Pix[0]) + int(image.Pix[4]) + int(image.Pix[64*4]) + int(image.Pix[64*4+4]) + 2) / 4
	if int(levels[1].Pix[0]) != want {
		t.Errorf("level 1 red %d, want %d", levels[1].Pix[0], want)
	}
	// sRGB filtering averages in linear light: black and white give 188.
	bw := []byte{0, 0, 0, 255, 255, 255, 255, 255, 0, 0, 0, 255, 255, 255, 255, 255}
	if got := bcenc.Downsample(bcenc.Level{Width: 2, Height: 2, Pix: bw}, true).Pix; got[0] != 188 || got[3] != 255 {
		t.Errorf("sRGB mix %v", got)
	}
	if got := bcenc.Downsample(bcenc.Level{Width: 2, Height: 2, Pix: bw}, false).Pix; got[0] != 128 {
		t.Errorf("linear mix %v", got)
	}
	// A size that would need a 6-pixel level stops before it.
	levels = bcenc.MipChain(make([]byte, 12*12*4), 12, 12, false)
	if len(levels) != 1 {
		t.Errorf("12×12: %d levels", len(levels))
	}
}

// writeImagePNG stores image as a PNG file and returns its path.
func writeImagePNG(t *testing.T, dir, name string, image *Image) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, EncodePNG(image), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCompressed(t *testing.T) {
	dir := t.TempDir()
	opaque := testImage(64, 32, false, 3)
	alpha := testImage(64, 32, true, 7)
	store := NewStore()
	if err := store.Register(map[string]Spec{
		"color":  {Path: writeImagePNG(t, dir, "c.png", opaque), Channel: "rgb", Transform: "none", InputColorSpace: "srgb"},
		"alpha":  {Path: writeImagePNG(t, dir, "a.png", alpha), Channel: "rgba", Transform: "none", InputColorSpace: "srgb"},
		"rough":  {Path: writeImagePNG(t, dir, "r.png", opaque), Channel: "g", Transform: "invert", InputColorSpace: "linear"},
		"odd":    {Path: writeImagePNG(t, dir, "o.png", testImage(30, 30, false, 5)), Channel: "rgb", Transform: "none"},
		"broken": {Path: writeImagePNG(t, dir, "b.png", opaque), Channel: "rgb", Transform: "invert"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, accepted, format string }{
		{"color", "bc7,bc3,bc1", "bc7"},
		{"color", "bc3,bc1", "bc1"},
		{"alpha", "bc3,bc1", "bc3"},
		{"alpha", "bc1", ""}, // alpha matters and only BC1: PNG
		{"rough", "bc1", "bc1"},
		{"odd", "bc7", ""}, // not a multiple of 4: PNG
		{"color", "", ""},
	} {
		result, err := store.LoadCompressed(tc.id, 64, tc.accepted)
		if err != nil {
			t.Fatal(tc.id, err)
		}
		if result.Format != tc.format {
			t.Fatalf("%s/%s: format %q, want %q", tc.id, tc.accepted, result.Format, tc.format)
		}
		if tc.format == "" {
			if result.PNG == nil || result.Mips != nil {
				t.Fatalf("%s: expected the PNG preview", tc.id)
			}
			continue
		}
		png, err := store.Load(tc.id, 64)
		if err != nil {
			t.Fatal(err)
		}
		reference, err := DecodeImage(png.PNG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Width != png.Width || result.Height != png.Height || result.PNG != nil {
			t.Fatalf("%s: %d×%d", tc.id, result.Width, result.Height)
		}
		if len(result.Mips) != 7 { // 64×32 … 1×1
			t.Fatalf("%s: %d mips", tc.id, len(result.Mips))
		}
		for i, mip := range result.Mips {
			if mip.Width != max(1, 64>>i) || mip.Height != max(1, 32>>i) || len(mip.Data) != bcenc.Size(tc.format, mip.Width, mip.Height) {
				t.Fatalf("%s: mip %d is %d×%d, %d bytes", tc.id, i, mip.Width, mip.Height, len(mip.Data))
			}
		}
		// Level 0 is the PNG preview flipped bottom to top.
		decoded := decodeFormat(t, tc.format, result.Mips[0].Data, result.Width, result.Height)
		stride := result.Width * 4
		bcenc.FlipRows(decoded.Pix, result.Width, result.Height)
		if got := psnr(reference.Pix, decoded.Pix, 3); got < 30 {
			t.Errorf("%s/%s: PSNR %.1f dB against the PNG preview", tc.id, tc.format, got)
		}
		if tc.id == "alpha" {
			var a, b []byte
			for i := 3; i < len(decoded.Pix); i += 4 {
				a = append(a, reference.Pix[i], 0, 0, 0)
				b = append(b, decoded.Pix[i], 0, 0, 0)
			}
			if got := psnr(a, b, 1); got < 30 {
				t.Errorf("alpha PSNR %.1f", got)
			}
		}
		_ = stride
		again, _ := store.LoadCompressed(tc.id, 64, tc.accepted)
		if again != result {
			t.Errorf("%s: not cached", tc.id)
		}
	}
	// Errors match Load's.
	if _, err := store.LoadCompressed("broken", 64, "bc7"); err == nil || err.Error() != "Inverted packed channels must be scalar." {
		t.Errorf("broken: %v", err)
	}
	if _, err := store.LoadCompressed("missing", 64, "bc7"); err != errUnregistered {
		t.Errorf("missing: %v", err)
	}
	if _, err := store.LoadCompressed("color", 16, "bc7"); err != errPreviewEdge {
		t.Errorf("edge: %v", err)
	}
	if CanonicalFormats([]string{"bc1", "x", "bc7", "bc1"}) != "bc7,bc1" || CanonicalFormats(nil) != "" {
		t.Error("CanonicalFormats")
	}
}

// TestCompressedSpeed reports encode time and PSNR for 2048² previews
// (go test -run CompressedSpeed -v; AVOLITION_BENCH_LIBRARY adds the
// benchmark library's textures).
func TestCompressedSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("timing report")
	}
	type sample struct {
		name  string
		image *Image
	}
	samples := []sample{{"synthetic-opaque", testImage(2048, 2048, false, 9)}, {"synthetic-alpha", testImage(2048, 2048, true, 10)}}
	if root := os.Getenv("AVOLITION_BENCH_LIBRARY"); root != "" {
		matches, _ := filepath.Glob(filepath.Join(root, "Heroes/Big/Skin0/Models/*/Textures/Big00[0-1].png"))
		for _, path := range matches {
			image, _, err := LoadImage(path, 2048)
			if err != nil {
				t.Fatal(err)
			}
			samples = append(samples, sample{filepath.Base(path), image})
		}
	}
	for _, s := range samples {
		start := time.Now()
		png := EncodePNG(s.image)
		pngTime := time.Since(start)
		for _, format := range CompressedFormats {
			start := time.Now()
			pix := append([]byte(nil), s.image.Pix...)
			levels := bcenc.MipChain(pix, s.image.Width, s.image.Height, false)
			mipTime := time.Since(start)
			var level0 []byte
			total := 0
			for i, level := range levels {
				data := bcenc.Encode(format, level.Pix, level.Width, level.Height)
				total += len(data)
				if i == 0 {
					level0 = data
				}
			}
			elapsed := time.Since(start)
			decoded := decodeFormat(t, format, level0, s.image.Width, s.image.Height)
			t.Logf("%-18s %s: %6.1f ms (mips %5.1f ms) %5.2f MB, RGB PSNR %.2f dB; PNG %6.1f ms %5.2f MB",
				s.name, format, float64(elapsed.Microseconds())/1000, float64(mipTime.Microseconds())/1000,
				float64(total)/1e6, psnr(s.image.Pix, decoded.Pix, 3), float64(pngTime.Microseconds())/1000, float64(len(png))/1e6)
		}
	}
}
