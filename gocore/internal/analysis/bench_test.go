package analysis

import (
	"os"
	"path/filepath"
	"testing"
)

// benchmarkLibrary is the synthetic library from gocore/bench/make_library.py
// (AVOLITION_BENCH_LIBRARY), or tests/demo-assets.
func benchmarkLibrary(b *testing.B) string {
	if root := os.Getenv("AVOLITION_BENCH_LIBRARY"); root != "" {
		return root
	}
	return filepath.Join("..", "..", "..", "tests", "demo-assets")
}

func BenchmarkAnalyze(b *testing.B) {
	root := benchmarkLibrary(b)
	for i := 0; i < b.N; i++ {
		if _, err := Analyze(root, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeAndEncode(b *testing.B) {
	root := benchmarkLibrary(b)
	for i := 0; i < b.N; i++ {
		inventory, err := Analyze(root, Options{})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := Encode(inventory); err != nil {
			b.Fatal(err)
		}
	}
}
