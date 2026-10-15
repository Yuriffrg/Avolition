package textures

import (
	"fmt"
	"image/color"
	"path/filepath"
	"sync"
	"testing"
)

// Loads run outside the store lock; concurrent loads, registrations and
// releases must stay consistent (run with -race).
func TestConcurrentLoadsAndRegistrations(t *testing.T) {
	folder := t.TempDir()
	specs := map[string]Spec{}
	for i := range 6 {
		path := filepath.Join(folder, fmt.Sprintf("%d.png", i))
		writePNG(t, path, 64+i, 40, color.NRGBA{byte(i * 40), 20, 30, 255}, true)
		specs[fmt.Sprintf("t%d", i)] = Spec{Path: path, Channel: "rgb"}
	}
	store := NewStore()
	store.Register(specs, "animations")
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 30 {
				id := fmt.Sprintf("t%d", (worker+i)%6)
				result, err := store.Load(id, 32+i%3)
				if err != nil {
					t.Error(err)
					return
				}
				if result.Width != 32+i%3 {
					t.Errorf("%s: width %d", id, result.Width)
				}
				if i%10 == 0 {
					store.Register(map[string]Spec{"preview": specs["t0"]}, "model-browser")
					store.Release("model-browser")
				}
			}
		}()
	}
	wg.Wait()
	first, _ := store.Load("t1", 0)
	second, _ := store.Load("t1", 0)
	if first != second {
		t.Fatal("result not cached after concurrent loads")
	}
}
