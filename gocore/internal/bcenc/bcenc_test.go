package bcenc

import (
	"math"
	"math/rand/v2"
	"testing"
)

// benchImage is a 1024² RGBA image with gradients, edges and some noise.
func benchImage(alpha bool) []byte {
	const size = 1024
	random := rand.New(rand.NewPCG(1, 2))
	pix := make([]byte, size*size*4)
	for y := range size {
		for x := range size {
			o := (y*size + x) * 4
			fx, fy := float64(x)/size, float64(y)/size
			v := [4]float64{128 + 100*math.Sin(fx*9+fy*3), 128 + 90*math.Cos(fy*7-fx*2), 255 * fx * fy, 255}
			if (x/37+y/23)%5 == 0 {
				v[0], v[1], v[2] = 230, 40, 30
			}
			for c := range 3 {
				v[c] += random.NormFloat64() * 4
			}
			if alpha {
				v[3] = 255 * (0.5 + 0.5*math.Sin(fx*13-fy*5))
			}
			for c := range 4 {
				pix[o+c] = byte(min(max(math.Round(v[c]), 0), 255))
			}
		}
	}
	return pix
}

func benchmarkEncode(b *testing.B, format string, alpha bool) {
	pix := benchImage(alpha)
	b.SetBytes(int64(len(pix)))
	for b.Loop() {
		Encode(format, pix, 1024, 1024)
	}
}

func BenchmarkBC1(b *testing.B)      { benchmarkEncode(b, BC1, false) }
func BenchmarkBC3(b *testing.B)      { benchmarkEncode(b, BC3, true) }
func BenchmarkBC7(b *testing.B)      { benchmarkEncode(b, BC7, false) }
func BenchmarkBC7Alpha(b *testing.B) { benchmarkEncode(b, BC7, true) }

func BenchmarkMipChain(b *testing.B) {
	pix := benchImage(false)
	for b.Loop() {
		MipChain(pix, 1024, 1024, true)
	}
}

func TestBitWriter(t *testing.T) {
	var w bitWriter
	w.put(1<<6, 7)
	w.put(0x7f, 7)
	for range 7 {
		w.put(0x55, 7)
	}
	w.put(1, 1)
	w.put(0, 1)
	w.put(5, 3)
	for range 15 {
		w.put(0xa, 4)
	}
	if w.n != 128 {
		t.Fatalf("%d bits", w.n)
	}
	var out [16]byte
	w.write(out[:])
	if out[0] != 0xc0 || out[15]>>4 != 0xa {
		t.Fatalf("%x", out)
	}
}
