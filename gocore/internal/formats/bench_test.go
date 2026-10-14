package formats_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
)

// Synthetic sizes shared with the Python timing script in the commit notes.
const (
	benchVertices = 300_000
	benchBones    = 120
	benchClipBone = 120
	benchFrames   = 600
)

func largestFixture(b *testing.B, extensions ...string) string {
	var best string
	var size int64
	for _, folder := range []string{"demo-assets", "material-assets"} {
		assets, clips := assetFiles(b, folder)
		for _, path := range append(assets, clips...) {
			for _, extension := range extensions {
				if filepath.Ext(path) != extension {
					continue
				}
				if info, err := os.Stat(path); err == nil && info.Size() > size {
					best, size = path, info.Size()
				}
			}
		}
	}
	return best
}

func benchmarkAsset(b *testing.B, path string) {
	b.Run("load", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := formats.LoadAsset(path, ""); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("load+AppendAssetJSON", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			asset, err := formats.LoadAsset(path, "")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := formats.AppendAssetJSON(nil, asset); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("load+json.Marshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			asset, err := formats.LoadAsset(path, "")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := json.Marshal(asset); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchmarkClip(b *testing.B, path string) {
	b.Run("load", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := formats.LoadClip(path); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("load+AppendClipJSON", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			clip, err := formats.LoadClip(path)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := formats.AppendClipJSON(nil, clip); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("header", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := formats.ReadClipHeader(path); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("binding", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := formats.ReadClipBindingMetadata(path); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkLoadAssetFixture(b *testing.B) {
	benchmarkAsset(b, largestFixture(b, ".owmdl"))
}

func BenchmarkLoadAssetEntityFixture(b *testing.B) {
	benchmarkAsset(b, largestFixture(b, ".owentity"))
}

func BenchmarkLoadAssetSynthetic300k(b *testing.B) {
	path := ft.Write(b, b.TempDir(), "Large.owmdl", ft.LargeModel(benchVertices, benchBones))
	benchmarkAsset(b, path)
}

func BenchmarkLoadClipFixture(b *testing.B) {
	benchmarkClip(b, largestFixture(b, ".owanimclip"))
}

func BenchmarkLoadClipSynthetic(b *testing.B) {
	path := ft.Write(b, b.TempDir(), "Large.owanimclip", ft.LargeClip(benchClipBone, benchFrames))
	benchmarkClip(b, path)
}

// TestWriteBenchmarkFixtures writes the synthetic benchmark inputs to
// $FORMATS_BENCH_DIR so the Python reference can be timed on the same bytes.
func TestWriteBenchmarkFixtures(t *testing.T) {
	dir := os.Getenv("FORMATS_BENCH_DIR")
	if dir == "" {
		t.Skip("set FORMATS_BENCH_DIR to write benchmark fixtures")
	}
	ft.Write(t, dir, "Large.owmdl", ft.LargeModel(benchVertices, benchBones))
	ft.Write(t, dir, "Large.owanimclip", ft.LargeClip(benchClipBone, benchFrames))
}
