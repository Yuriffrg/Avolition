package textures

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkStages logs the time of each pipeline stage once per file.
func BenchmarkStages(b *testing.B) {
	benchSetup(b)
	for b.Loop() {
		for _, name := range benchFiles {
			data, _ := os.ReadFile(filepath.Join(benchFolder, name))
			t0 := time.Now()
			img, err := DecodeImage(data, nil)
			if err != nil {
				b.Fatal(err)
			}
			t1 := time.Now()
			small := img
			if img.Width > MaxPreviewEdge {
				small = Resize(img, MaxPreviewEdge, MaxPreviewEdge)
			}
			t2 := time.Now()
			rgb, _ := ProcessChannels(small, Spec{Channel: "rgb"})
			t3 := time.Now()
			uri := DataURI(rgb)
			t4 := time.Now()
			b.Logf("%s: decode %v, resize %v, process %v, png+base64 %v (%d bytes)", name, t1.Sub(t0), t2.Sub(t1), t3.Sub(t2), t4.Sub(t3), len(uri))
		}
	}
}
