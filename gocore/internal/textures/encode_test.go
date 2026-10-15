package textures

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/oracle"
)

func patternImage(width, height, channels int) *Image {
	img := &Image{width, height, channels, make([]byte, width*height*channels)}
	for p := range img.Pix {
		img.Pix[p] = byte((p*7)/(channels+1) + (p*p)%13)
	}
	return img
}

func TestEncodePNGRoundTrip(t *testing.T) {
	for _, size := range [][3]int{{1, 1, 3}, {5, 3, 4}, {1000, 700, 3}, {333, 2049, 4}} {
		img := patternImage(size[0], size[1], size[2])
		decoded, err := png.Decode(bytes.NewReader(EncodePNG(img)))
		if err != nil {
			t.Fatal(err)
		}
		rgba := toRGBA(decoded)
		for p := 0; p < img.Width*img.Height; p++ {
			if !bytes.Equal(rgba.Pix[p*4:p*4+img.Channels], img.Pix[p*img.Channels:(p+1)*img.Channels]) {
				t.Fatalf("%v: pixel %d differs", size, p)
			}
		}
	}
}

// Pillow (zlib) reads the multi-strip PNG files written by EncodePNG.
func TestPillowReadsEncodedPNG(t *testing.T) {
	folder := t.TempDir()
	var paths, digests []string
	for i, size := range [][3]int{{1, 1, 3}, {1000, 700, 3}, {333, 2049, 4}, {4096, 3, 4}} {
		img := patternImage(size[0], size[1], size[2])
		path := filepath.Join(folder, fmt.Sprintf("%d.png", i))
		if err := os.WriteFile(path, EncodePNG(img), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		digests = append(digests, fmt.Sprintf("%x", md5.Sum(img.Pix)))
	}
	result := oracle.Exec(t, `
import hashlib
from PIL import Image
result = []
for path in args[0]:
    with Image.open(path) as im:
        im.load()
        result.append(hashlib.md5(im.tobytes()).hexdigest())`, paths)
	var want []string
	if err := json.Unmarshal(result.MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	for i := range digests {
		if want[i] != digests[i] {
			t.Errorf("image %d: Pillow decoded different pixels", i)
		}
	}
}
