package textures

import (
	"compress/flate"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"avolition/gocore/internal/oracle"
)

// Benchmarks: one uncached texture load (read + decode + Lanczos reduction to
// 2048 + channel processing + PNG + base64), for the same files Python loads.
// Run with: go test -run '^$' -bench Load ./internal/textures/

var (
	benchOnce   sync.Once
	benchFolder string
	benchPython map[string]pythonTiming
)

type pythonTiming struct {
	Seconds float64 `json:"seconds"`
	Bytes   int     `json:"bytes"`
}

var benchFiles = []string{"rgba2048.png", "rgba4096.png", "dxt5-2048.dds", "dxt5-4096.dds"}

func benchSetup(b *testing.B) {
	benchOnce.Do(func() {
		folder, err := os.MkdirTemp("", "texture-bench")
		if err != nil {
			b.Fatal(err)
		}
		benchFolder = folder
		result := oracle.Exec(b, `
import time
import numpy as np
from PIL import Image
from owpreview.textures import TextureStore
folder, files = args[0], args[1]
rng = np.random.default_rng(3)
def pattern(size):
    yy, xx = np.mgrid[0:size, 0:size]
    base = np.stack([(xx * 255) // (size - 1), (yy * 255) // (size - 1), ((xx // 64 + yy // 64) % 2) * 255, (xx ^ yy) & 255], -1)
    noise = rng.integers(-12, 13, (size, size, 4))
    return np.clip(base + noise, 0, 255).astype(np.uint8)
for size in (2048, 4096):
    image = Image.fromarray(pattern(size), 'RGBA')
    image.save('%s/rgba%d.png' % (folder, size))
    image.save('%s/dxt5-%d.dds' % (folder, size), pixel_format='DXT5')
timings = {}
for name in files:
    best = None
    for _ in range(3):
        store = TextureStore()
        store.register({'t': {'path': folder + '/' + name, 'channel': 'rgb'}})
        start = time.perf_counter()
        value = store.load('t')
        elapsed = time.perf_counter() - start
        best = elapsed if best is None else min(best, elapsed)
    timings[name] = {'seconds': best, 'bytes': len(value['dataUri'])}
result = timings`, benchFolder, benchFiles)
		if err := json.Unmarshal(result.MustValue(b), &benchPython); err != nil {
			b.Fatal(err)
		}
	})
	if benchFolder == "" {
		b.Skip("oracle unavailable")
	}
}

func loadOnce(b *testing.B, path string) *Result {
	store := NewStore()
	store.Register(map[string]Spec{"t": {Path: path, Channel: "rgb"}}, "")
	result, err := store.Load("t", 0)
	if err != nil {
		b.Fatal(err)
	}
	return result
}

func BenchmarkLoad(b *testing.B) {
	benchSetup(b)
	for _, name := range benchFiles {
		b.Run(name, func(b *testing.B) {
			path := filepath.Join(benchFolder, name)
			var result *Result
			b.ReportAllocs()
			for b.Loop() {
				result = loadOnce(b, path)
			}
			python := benchPython[name]
			perOp := b.Elapsed().Seconds() / float64(b.N)
			b.ReportMetric(python.Seconds*1e3, "python-ms")
			b.ReportMetric(python.Seconds/perOp, "speedup")
			b.ReportMetric(float64(len(result.DataURI))/float64(python.Bytes), "size-vs-python")
		})
	}
}

// BenchmarkCompressionLevel compares flate levels on the 2048 preview.
func BenchmarkCompressionLevel(b *testing.B) {
	benchSetup(b)
	source, _, err := LoadImage(filepath.Join(benchFolder, "rgba2048.png"), MaxPreviewEdge)
	if err != nil {
		b.Fatal(err)
	}
	rgb, _ := ProcessChannels(source, Spec{Channel: "rgb"})
	saved := CompressionLevel
	defer func() { CompressionLevel = saved; stripPool = sync.Pool{New: stripPool.New} }()
	for _, level := range []int{flate.BestSpeed, 2, 3, flate.DefaultCompression} {
		b.Run(fmt.Sprintf("level%d", level), func(b *testing.B) {
			CompressionLevel = level
			stripPool = sync.Pool{New: stripPool.New}
			var size int
			for b.Loop() {
				size = len(EncodePNG(rgb))
			}
			b.ReportMetric(float64(size)/1e6, "MB")
			b.ReportMetric(float64(benchPython["rgba2048.png"].Bytes)*3/4/1e6, "python-MB")
		})
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if benchFolder != "" {
		os.RemoveAll(benchFolder)
	}
	os.Exit(code)
}
