package exportbundle

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func encodedPNG(t *testing.T, img image.Image, level png.CompressionLevel) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: level}
	if err := encoder.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPublishPNGEncodingCompatibility(t *testing.T) {
	for _, mode := range []string{"gray8", "gray16", "rgba"} {
		t.Run(mode, func(t *testing.T) {
			var img image.Image
			switch mode {
			case "gray8":
				img = image.NewGray(image.Rect(0, 0, 16, 16))
			case "gray16":
				img = image.NewGray16(image.Rect(0, 0, 16, 16))
			case "rgba":
				img = image.NewNRGBA(image.Rect(0, 0, 16, 16))
			}
			old := encodedPNG(t, img, png.NoCompression)
			fresh := encodedPNG(t, img, png.BestCompression)
			if bytes.Equal(old, fresh) {
				t.Fatal("test needs different PNG bytes")
			}
			f := newFixture(t)
			target := filepath.Join(f.output, "tex", "shared.png")
			write(t, target, string(old))
			before, _ := os.Stat(target)
			result, err := ExportBundle(f.source, f.output, "Map.c4d", func(scenes map[string]string, tex string) (*Details, error) {
				write(t, scenes["c4d"], "native scene referencing tex/shared.png")
				write(t, filepath.Join(tex, "shared.png"), string(fresh))
				return nil, nil
			}, []string{"c4d"})
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(target)
			if read(t, target) != string(old) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("existing texture changed")
			}
			if result.C4DPath == nil || result.TextureCount != 1 {
				t.Fatal(result)
			}
		})
	}
}

func TestPublishPNGRejectsChangedSamplesAndMetadata(t *testing.T) {
	for _, kind := range []string{"pixel", "hidden-rgb", "precision", "metadata", "corrupt", "dimensions"} {
		t.Run(kind, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
			old := encodedPNG(t, img, png.NoCompression)
			var fresh []byte
			switch kind {
			case "pixel":
				img.SetNRGBA(0, 0, color.NRGBA{R: 1, A: 255})
			case "hidden-rgb":
				img.SetNRGBA(0, 0, color.NRGBA{R: 1, A: 0})
			case "precision":
				a := image.NewGray16(image.Rect(0, 0, 16, 16))
				old = encodedPNG(t, a, png.NoCompression)
				a.SetGray16(0, 0, color.Gray16{Y: 1})
				fresh = encodedPNG(t, a, png.BestCompression)
			case "dimensions":
				fresh = encodedPNG(t, image.NewNRGBA(image.Rect(0, 0, 8, 32)), png.BestCompression)
			}
			if fresh == nil {
				fresh = encodedPNG(t, img, png.BestCompression)
			}
			if kind == "metadata" {
				chunk := make([]byte, 16)
				binary.BigEndian.PutUint32(chunk, 4)
				copy(chunk[4:], "gAMA")
				binary.BigEndian.PutUint32(chunk[8:], 45455)
				binary.BigEndian.PutUint32(chunk[12:], crc32.ChecksumIEEE(chunk[4:12]))
				fresh = append(append(append([]byte{}, fresh[:33]...), chunk...), fresh[33:]...)
			}
			if kind == "corrupt" {
				fresh[len(fresh)-1] ^= 1
			}
			f := newFixture(t)
			target := filepath.Join(f.output, "tex", "shared.png")
			write(t, target, string(old))
			_, err := ExportBundle(f.source, f.output, "Map.c4d", func(scenes map[string]string, tex string) (*Details, error) {
				write(t, scenes["c4d"], "scene")
				write(t, filepath.Join(tex, "shared.png"), string(fresh))
				return nil, nil
			}, []string{"c4d"})
			if err == nil {
				t.Fatal("conflicting texture accepted")
			}
			if read(t, target) != string(old) {
				t.Fatal("original modified")
			}
			if _, err := os.Stat(filepath.Join(f.output, "Map.c4d")); !os.IsNotExist(err) {
				t.Fatal("partial scene published")
			}
		})
	}
}
