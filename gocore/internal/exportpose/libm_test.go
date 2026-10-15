package exportpose

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"

	"avolition/gocore/internal/oracle"
)

// asin and atan2 round exactly like Python's math.asin/math.atan2.
func TestLibmMatchesPython(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var pairs [][2]float64
	for i := 0; i < 20000; i++ {
		a, b := rng.Float64()*2-1, rng.Float64()*2-1
		switch i % 5 {
		case 1:
			a *= 1e-9
		case 2:
			b *= 1e-12
		case 3:
			a, b = math.Copysign(1-rng.Float64()*1e-6, a), b*1e5
		}
		pairs = append(pairs, [2]float64{a, b})
	}
	pairs = append(pairs, [2]float64{1, 0}, [2]float64{-1, 1}, [2]float64{0.5, -0.5}, [2]float64{1e-300, 1e300}, [2]float64{-0.7071067811865476, -0.7071067811865475})
	raw := oracle.Exec(t, `
import math
result = [[math.asin(max(-1.0, min(1.0, a))), math.atan2(a, b)] for a, b in args[0]]
`, pairs).MustValue(t)
	var want [][2]float64
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	badAsin, badAtan2, goAsin, goAtan2 := 0, 0, 0, 0
	for i, p := range pairs {
		clipped := math.Max(-1, math.Min(1, p[0]))
		if got := asin(clipped); got != want[i][0] {
			badAsin++
			if math.Nextafter(got, want[i][0]) != want[i][0] {
				t.Errorf("asin(%v) = %v, want %v", clipped, got, want[i][0])
			}
		}
		if got := atan2(p[0], p[1]); got != want[i][1] {
			badAtan2++
			if math.Nextafter(got, want[i][1]) != want[i][1] {
				t.Errorf("atan2(%v, %v) = %v, want %v", p[0], p[1], got, want[i][1])
			}
		}
		if math.Asin(clipped) != want[i][0] {
			goAsin++
		}
		if math.Atan2(p[0], p[1]) != want[i][1] {
			goAtan2++
		}
	}
	t.Logf("of %d inputs, math.Asin differs from Python on %d and math.Atan2 on %d; asin on %d and atan2 on %d",
		len(pairs), goAsin, goAtan2, badAsin, badAtan2)
	// glibc itself is not correctly rounded on a few inputs (for example
	// asin(0.6601976939273677) is 0.72108193914776036499..., which rounds up,
	// but glibc rounds down); those differ by one unit in the last place.
	if badAsin > len(pairs)/200 || badAtan2 > len(pairs)/200 {
		t.Fatalf("asin differs on %d, atan2 on %d inputs", badAsin, badAtan2)
	}
}

func BenchmarkAtan2(b *testing.B) {
	for i := 0; i < b.N; i++ {
		atan2(0.3+float64(i%100)*1e-3, 0.7)
	}
}
