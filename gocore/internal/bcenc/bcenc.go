// Package bcenc encodes RGBA8 images into GPU block-compressed formats for
// the renderer's preview textures: BC1 (DXT1), BC3 (DXT5) and BC7 (BPTC).
//
// The encoders aim for speed with reasonable quality, not for the best
// possible result:
//
//   - BC1 colour blocks follow stb_dxt's approach: principal axis endpoints,
//     nearest-palette indices and two least-squares refinements; single
//     colours use exact lookup tables.
//   - BC3 alpha blocks use the min/max 8-value mode, or the 6-value mode with
//     explicit 0 and 255 when that fits better.
//   - BC7 uses mode 6 (one RGBA line, 7.7.7.7 endpoints with per-endpoint
//     p-bits, 4-bit indices) for every block, and also tries mode 5 (separate
//     RGB and alpha lines) for blocks whose alpha varies, keeping the better.
//
// Images are processed in rows of 4×4 blocks spread over all CPU cores.
// Edge blocks of images whose sides are not multiples of 4 repeat the last
// row/column. Block layouts follow the Direct3D / Khronos Data Format specs
// (little-endian, bit 0 first).
package bcenc

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Formats.
const (
	BC1 = "bc1"
	BC3 = "bc3"
	BC7 = "bc7"
)

// BlockBytes is the size of one 4×4 block of format (0 for unknown formats).
func BlockBytes(format string) int {
	switch format {
	case BC1:
		return 8
	case BC3, BC7:
		return 16
	}
	return 0
}

// Size is the byte length of a width×height image in format.
func Size(format string, width, height int) int {
	return ((width + 3) / 4) * ((height + 3) / 4) * BlockBytes(format)
}

// pixels is one 4×4 block, row-major, RGBA.
type pixels [16][4]uint8

// fetch reads the block at block coordinates (bx, by), clamping to the edge.
func fetch(dst *pixels, pix []byte, width, height, bx, by int) {
	for j := range 4 {
		y := min(by*4+j, height-1)
		row := pix[y*width*4:]
		for i := range 4 {
			x := min(bx*4+i, width-1)
			copy(dst[j*4+i][:], row[x*4:x*4+4])
		}
	}
}

// Encode compresses an RGBA image (4 bytes per pixel, rows top to bottom)
// into format's blocks, left to right and top to bottom. It panics on an
// unknown format.
func Encode(format string, pix []byte, width, height int) []byte {
	return encode(format, pix, width, height, false)
}

// EncodeOpaque is Encode for an image whose alpha is all 255 and unused: BC7
// may then decode alpha as 254 where that keeps the colour exact (BC7 mode 6
// shares one low bit between colour and alpha, so exact opaque alpha would
// turn black into 1/255).
func EncodeOpaque(format string, pix []byte, width, height int) []byte {
	return encode(format, pix, width, height, true)
}

func encode(format string, pix []byte, width, height int, ignoreAlpha bool) []byte {
	blockSize := BlockBytes(format)
	if blockSize == 0 {
		panic("bcenc: unknown format " + format)
	}
	if width <= 0 || height <= 0 {
		return nil
	}
	blocksX, blocksY := (width+3)/4, (height+3)/4
	out := make([]byte, blocksX*blocksY*blockSize)
	encodeRow := func(by int) {
		var block pixels
		for bx := range blocksX {
			fetch(&block, pix, width, height, bx, by)
			dst := out[(by*blocksX+bx)*blockSize:][:blockSize]
			switch format {
			case BC1:
				encodeColor(dst, &block, false)
			case BC3:
				encodeAlpha(dst[:8], &block, 3)
				encodeColor(dst[8:], &block, true)
			case BC7:
				encodeBC7(dst, &block, ignoreAlpha)
			}
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if workers == 1 || blocksX*blocksY < 256 {
		for by := range blocksY {
			encodeRow(by)
		}
		return out
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(workers, blocksY) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				by := int(next.Add(1) - 1)
				if by >= blocksY {
					return
				}
				encodeRow(by)
			}
		}()
	}
	wg.Wait()
	return out
}

// bitWriter packs fields into a 128-bit block, least significant bit first.
type bitWriter struct {
	lo, hi uint64
	n      uint
}

func (b *bitWriter) put(v uint64, count uint) {
	v &= 1<<count - 1
	if b.n < 64 {
		b.lo |= v << b.n
		if b.n+count > 64 {
			b.hi |= v >> (64 - b.n)
		}
	} else {
		b.hi |= v << (b.n - 64)
	}
	b.n += count
}

func (b *bitWriter) write(dst []byte) {
	for i := range 8 {
		dst[i] = byte(b.lo >> (8 * i))
		dst[8+i] = byte(b.hi >> (8 * i))
	}
}

func sq(v int32) int32 { return v * v }

func clampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// principalAxis returns the block's mean and the dominant direction of its
// first n channels (power iteration on the covariance matrix); the axis is
// zero when the block is flat.
func principalAxis(block [][4]uint8, n int) (mean, axis [4]float32) {
	var sum [4]int32
	var products [10]int32 // upper triangle of Σ p pᵀ
	lo, hi := [4]int32{255, 255, 255, 255}, [4]int32{}
	for _, p := range block {
		r, g, b, a := int32(p[0]), int32(p[1]), int32(p[2]), int32(p[3])
		sum[0] += r
		sum[1] += g
		sum[2] += b
		sum[3] += a
		products[0] += r * r
		products[1] += r * g
		products[2] += r * b
		products[3] += r * a
		products[4] += g * g
		products[5] += g * b
		products[6] += g * a
		products[7] += b * b
		products[8] += b * a
		products[9] += a * a
		lo = [4]int32{min(lo[0], r), min(lo[1], g), min(lo[2], b), min(lo[3], a)}
		hi = [4]int32{max(hi[0], r), max(hi[1], g), max(hi[2], b), max(hi[3], a)}
	}
	var cov [4][4]float32
	k := 0
	for i := range 4 {
		for j := i; j < 4; j++ {
			// N·Σ(p-mean)(p-mean)ᵀ, exact in integers.
			c := float32(int32(len(block))*products[k] - sum[i]*sum[j])
			cov[i][j], cov[j][i] = c, c
			k++
		}
	}
	// Start along the bounding box diagonal, signed by the covariance with
	// the widest channel; it is usually close to the answer.
	var v [4]float32
	widest := 0
	for c := range n {
		mean[c] = float32(sum[c]) / float32(len(block))
		v[c] = float32(hi[c] - lo[c])
		if cov[c][c] > cov[widest][widest] {
			widest = c
		}
	}
	for c := range n {
		if cov[widest][c] < 0 {
			v[c] = -v[c]
		}
	}
	for range 4 {
		var w [4]float32
		var length float32
		for i := range n {
			for j := range n {
				w[i] += cov[i][j] * v[j]
			}
			length = max(length, abs32(w[i]))
		}
		if length < 1e-3 {
			return mean, [4]float32{}
		}
		inv := 1 / length
		for i := range n {
			v[i] = w[i] * inv
		}
	}
	var norm float32
	for c := range n {
		norm += v[c] * v[c]
	}
	if norm < 1e-12 {
		return mean, [4]float32{}
	}
	inv := 1 / sqrt32(norm)
	for c := range n {
		axis[c] = v[c] * inv
	}
	return mean, axis
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
