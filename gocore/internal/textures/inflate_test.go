package textures

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/rand/v2"
	"testing"

	"avolition/gocore/internal/oracle"
)

func samples() [][]byte {
	rng := rand.New(rand.NewPCG(1, 2))
	var result [][]byte
	for _, size := range []int{0, 1, 7, 300, 70000, 1 << 20} {
		random := make([]byte, size)
		for i := range random {
			random[i] = byte(rng.IntN(256))
		}
		text := make([]byte, size)
		for i := range text {
			text[i] = "aaaabbbcd efghij\n"[rng.IntN(17)]
		}
		runs := make([]byte, size)
		for i := range runs {
			runs[i] = byte(i / 1000)
		}
		result = append(result, random, text, runs)
	}
	return result
}

func TestInflateMatchesCompressFlate(t *testing.T) {
	for _, data := range samples() {
		for _, level := range []int{zlib.HuffmanOnly, zlib.NoCompression, zlib.BestSpeed, 2, 5, zlib.BestCompression} {
			var buffer bytes.Buffer
			writer, _ := zlib.NewWriterLevel(&buffer, level)
			writer.Write(data)
			writer.Close()
			out := make([]byte, len(data))
			n, err := inflateZlib(buffer.Bytes(), out, nil)
			if err != nil || n != len(data) || !bytes.Equal(out, data) {
				t.Fatalf("level %d size %d: n=%d err=%v", level, len(data), n, err)
			}
			// A full output buffer stops decoding without error.
			if len(data) > 10 {
				short := make([]byte, len(data)/2)
				if n, err := inflateZlib(buffer.Bytes(), short, nil); err != nil || n != len(short) || !bytes.Equal(short, data[:len(short)]) {
					t.Fatalf("short output: %d %v", n, err)
				}
				// Truncated input is an error.
				if _, err := inflateZlib(buffer.Bytes()[:buffer.Len()/2], out, nil); err == nil && len(data) > 1000 && level != zlib.NoCompression {
					t.Fatalf("level %d: truncated input accepted", level)
				}
			}
		}
	}
}

func TestInflateMatchesPythonZlibStrategies(t *testing.T) {
	inputs := samples()
	var encoded []string
	for _, data := range inputs {
		encoded = append(encoded, base64.StdEncoding.EncodeToString(data))
	}
	result := oracle.Exec(t, `
import base64, zlib
out = []
for item in args[0]:
    data = base64.b64decode(item)
    row = []
    for level, strategy in ((1, zlib.Z_DEFAULT_STRATEGY), (6, zlib.Z_FILTERED), (9, zlib.Z_HUFFMAN_ONLY), (6, zlib.Z_RLE), (6, zlib.Z_FIXED), (0, 0)):
        c = zlib.compressobj(level, zlib.DEFLATED, 15, 9, strategy)
        row.append(base64.b64encode(c.compress(data) + c.flush()).decode())
    out.append(row)
result = out`, encoded)
	var streams [][]string
	if err := json.Unmarshal(result.MustValue(t), &streams); err != nil {
		t.Fatal(err)
	}
	for i, row := range streams {
		for j, item := range row {
			stream, _ := base64.StdEncoding.DecodeString(item)
			out := make([]byte, len(inputs[i]))
			if n, err := inflateZlib(stream, out, nil); err != nil || n != len(out) || !bytes.Equal(out, inputs[i]) {
				t.Fatalf("input %d strategy %d: %d %v", i, j, n, err)
			}
		}
	}
}

func FuzzInflate(f *testing.F) {
	f.Add([]byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		out := make([]byte, 4096)
		inflateZlib(data, out, nil) // must not panic
	})
}

func BenchmarkInflateStdlib(b *testing.B) {
	data := samples()[16]
	var buffer bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&buffer, 6)
	writer.Write(data)
	writer.Close()
	out := make([]byte, len(data))
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		reader, _ := zlib.NewReader(bytes.NewReader(buffer.Bytes()))
		io.ReadFull(reader, out)
	}
}

func BenchmarkInflate(b *testing.B) {
	data := samples()[16] // 1 MiB text
	var buffer bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&buffer, 6)
	writer.Write(data)
	writer.Close()
	out := make([]byte, len(data))
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		inflateZlib(buffer.Bytes(), out, nil)
	}
}
