package textures

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNoisePNG writes a w x h RGBA PNG whose rows use every filter type.
func writeNoisePNG(t *testing.T, path string, w, h int) *Image {
	t.Helper()
	rng := rand.New(rand.NewSource(int64(w*h + 1)))
	img := &Image{w, h, 4, make([]byte, w*h*4)}
	for i := range img.Pix {
		img.Pix[i] = byte(i/4%w*3 + i/(4*w) + rng.Intn(9))
	}
	if err := os.WriteFile(path, EncodePNG(img), 0o644); err != nil {
		t.Fatal(err)
	}
	return img
}

func decodeURI(t *testing.T, uri string) *Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, dataURIPrefix))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return toRGBA(decoded)
}

// The streamed load (decoding overlapped with encoding or reduction) gives
// the pixels and errors of decode, Resize and ProcessChannels run in turn.
func TestStreamedLoadMatchesSequentialPipeline(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "noise.png")
	writeNoisePNG(t, path, 700, 450)
	for _, spec := range []Spec{{Channel: "rgb"}, {}, {Channel: "a", Transform: "invert"}, {Transform: "ow-normal"}} {
		for _, edge := range []int{2048, 700, 699, 333, 32} {
			source, warning, err := LoadImage(path, edge)
			if err != nil {
				t.Fatal(err)
			}
			want, err := ProcessChannels(source, spec)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			spec.Path = path
			store.Register(map[string]Spec{"t": spec}, "")
			result, err := store.Load("t", edge)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeURI(t, result.DataURI())
			if result.Width != want.Width || result.Height != want.Height || result.Warning != warning {
				t.Fatalf("%+v edge %d: %dx%d %q, want %dx%d %q", spec, edge, result.Width, result.Height, result.Warning, want.Width, want.Height, warning)
			}
			for p := 0; p < want.Width*want.Height; p++ {
				if !bytes.Equal(got.Pix[p*4:p*4+want.Channels], want.Pix[p*want.Channels:(p+1)*want.Channels]) {
					t.Fatalf("%+v edge %d: pixel %d differs", spec, edge, p)
				}
			}
			// A second load encodes the cached source.
			again, err := store.Load("t", edge)
			if err != nil || again != result {
				t.Fatalf("cached result: %v", err)
			}
		}
	}
}

func TestStreamedLoadReportsTruncatedData(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "noise.png")
	writeNoisePNG(t, path, 600, 600)
	data, _ := os.ReadFile(path)
	// Cut the image data in the middle of the IDAT stream.
	broken := filepath.Join(folder, "broken.png")
	os.WriteFile(broken, data[:len(data)/2], 0o644)
	for _, edge := range []int{2048, 100} {
		_, _, wantErr := LoadImage(broken, edge)
		if wantErr == nil {
			t.Fatal("expected an error")
		}
		store := NewStore()
		store.Register(map[string]Spec{"t": {Path: broken, Channel: "rgb"}}, "")
		if _, err := store.Load("t", edge); err == nil || err.Error() != wantErr.Error() {
			t.Fatalf("edge %d: %v, want %v", edge, err, wantErr)
		}
	}
}
