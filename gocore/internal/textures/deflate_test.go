package textures

import (
	"bytes"
	"compress/flate"
	"io"
	"math/rand"
	"testing"
)

func deflateSamples() [][]byte {
	rng := rand.New(rand.NewSource(1))
	random := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}
	runs := make([]byte, 300000)
	for i := range runs {
		runs[i] = byte(i / 1000)
	}
	mixed := make([]byte, 200000)
	for i := range mixed {
		switch (i / 5000) % 3 {
		case 0:
			mixed[i] = byte(rng.Intn(4))
		case 1:
			mixed[i] = byte(i % 7)
		default:
			mixed[i] = byte(rng.Intn(256))
		}
	}
	text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog; "), 5000)
	skewed := make([]byte, 100000) // a few very frequent symbols and many rare ones (long codes)
	for i := range skewed {
		v := rng.ExpFloat64() * 3
		skewed[i] = byte(min(255, int(v*v)))
	}
	fib := make([]byte, 0, 70000) // Fibonacci frequencies force the 15-bit limit
	a, b := 1, 1
	for s := 0; s < 24 && len(fib) < 70000; s++ {
		for range min(a, 70000-len(fib)) {
			fib = append(fib, byte(s))
		}
		a, b = b, a+b
	}
	rng.Shuffle(len(fib), func(i, j int) { fib[i], fib[j] = fib[j], fib[i] })
	return [][]byte{{}, {7}, {1, 2, 3}, make([]byte, 70000), random(10), random(65535), random(65536), random(200000),
		runs, mixed, text, skewed, fib}
}

func inflateAll(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDeflaterRoundTrip(t *testing.T) {
	var d deflater
	for i, sample := range deflateSamples() {
		encoded := d.compress(nil, sample, true)
		if got := inflateAll(t, encoded); !bytes.Equal(got, sample) {
			t.Fatalf("sample %d (%d bytes): round trip differs", i, len(sample))
		}
		// Ours must also read it (it decodes PNG previews).
		if len(sample) > 0 {
			out := make([]byte, len(sample))
			if n, err := inflateZlib(append([]byte{0x78, 0x01}, encoded...), out, nil); err != nil || n != len(sample) || !bytes.Equal(out, sample) {
				t.Fatalf("sample %d: inflate.go: %d bytes, %v", i, n, err)
			}
		}
	}
}

// Strips that are not final end with a sync flush and concatenate.
func TestDeflaterConcatenatesStrips(t *testing.T) {
	var d deflater
	samples := deflateSamples()
	var stream, want []byte
	for i, sample := range samples {
		stream = d.compress(stream, sample, i == len(samples)-1)
		want = append(want, sample...)
	}
	if got := inflateAll(t, stream); !bytes.Equal(got, want) {
		t.Fatal("concatenated strips differ")
	}
}

func TestBuildLengthsLimit(t *testing.T) {
	freq := make([]uint32, 286)
	a, b := uint32(1), uint32(1)
	for i := range 30 {
		freq[i] = a
		a, b = b, a+b
	}
	lengths := make([]uint8, 286)
	buildLengths(freq, lengths, 15, nil)
	kraft := 0.0
	for s, l := range lengths {
		if freq[s] != 0 && (l == 0 || l > 15) {
			t.Fatalf("symbol %d has length %d", s, l)
		}
		if l != 0 {
			kraft += 1 / float64(int(1)<<l)
		}
	}
	if kraft != 1 {
		t.Fatalf("code is not complete: %v", kraft)
	}
}

func BenchmarkDeflater(b *testing.B) {
	data := deflateSamples()[9]
	var d deflater
	b.SetBytes(int64(len(data)))
	var out []byte
	for b.Loop() {
		out = d.compress(out[:0], data, true)
	}
}
