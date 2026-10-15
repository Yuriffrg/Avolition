package api

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// parallelFor runs fn(0..n-1) on at most runtime.NumCPU() goroutines.
func parallelFor(n int, fn func(i int)) {
	workers := min(runtime.NumCPU(), n)
	if workers <= 1 {
		for i := range n {
			fn(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int(next.Add(1) - 1); i < n; i = int(next.Add(1) - 1) {
				fn(i)
			}
		}()
	}
	wg.Wait()
}
