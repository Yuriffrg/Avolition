package bcenc

import "math"

func sqrt32(v float32) float32 { return float32(math.Sqrt(float64(v))) }

func expand5(q int32) int32 { return q<<3 | q>>2 }
func expand6(q int32) int32 { return q<<2 | q>>4 }

func unpack565(c uint16) [3]int32 {
	return [3]int32{expand5(int32(c >> 11)), expand6(int32(c>>5) & 63), expand5(int32(c) & 31)}
}

func quantize(v float32, levels int32) int32 {
	q := int32(clampF(v, 0, 255)*float32(levels)/255 + 0.5)
	return min(max(q, 0), levels)
}

func pack565(r, g, b float32) uint16 {
	return uint16(quantize(r, 31)<<11 | quantize(g, 63)<<5 | quantize(b, 31))
}

// singleColor[bits][v] holds the endpoint pair (hi, lo) whose 2/3 : 1/3 mix
// is closest to the 8-bit value v (stb_dxt's "optimal single color" tables).
var single5, single6 = singleColorTable(5), singleColorTable(6)

func singleColorTable(bits int) (table [256][2]int32) {
	levels := int32(1)<<bits - 1
	expand := expand5
	if bits == 6 {
		expand = expand6
	}
	for v := range 256 {
		best := float32(math.MaxFloat32)
		for hi := range levels + 1 {
			for lo := range levels + 1 {
				a, b := expand(hi), expand(lo)
				mix := float32(2*a+b) / 3
				// Prefer close endpoints: GPUs round the mix differently.
				cost := abs32(mix-float32(v)) + 0.03*abs32(float32(a-b))
				if cost < best {
					best, table[v] = cost, [2]int32{hi, lo}
				}
			}
		}
	}
	return table
}

// colorPalette returns the decoded palette of endpoints c0, c1 and how many
// entries may be used (3-colour mode's fourth entry is black or transparent).
// fourOnly is BC2/BC3, whose colour blocks always use four colours.
func colorPalette(c0, c1 uint16, fourOnly bool) (p [4][3]int32, n int) {
	p[0], p[1] = unpack565(c0), unpack565(c1)
	if c0 > c1 || fourOnly {
		for c := range 3 {
			p[2][c] = (2*p[0][c] + p[1][c]) / 3
			p[3][c] = (p[0][c] + 2*p[1][c]) / 3
		}
		return p, 4
	}
	for c := range 3 {
		p[2][c] = (p[0][c] + p[1][c]) / 2
	}
	return p, 3
}

// matchColors picks the nearest palette entry for each pixel.
func matchColors(block *pixels, c0, c1 uint16, fourOnly bool) (indices [16]uint8, err int32) {
	p, n := colorPalette(c0, c1, fourOnly)
	if n == 4 {
		// The palette lies on the line from c1 to c0 (up to rounding): pick
		// the entry nearest to each pixel's projection on it.
		order := [4]uint8{1, 3, 2, 0}
		d := [3]int32{p[0][0] - p[1][0], p[0][1] - p[1][1], p[0][2] - p[1][2]}
		dd := d[0]*d[0] + d[1]*d[1] + d[2]*d[2]
		var scale float32
		if dd > 0 {
			scale = 3 / float32(dd)
		}
		for i := range block {
			px := &block[i]
			r, g, b := int32(px[0]), int32(px[1]), int32(px[2])
			t := int32(float32((r-p[1][0])*d[0]+(g-p[1][1])*d[1]+(b-p[1][2])*d[2])*scale + 0.5)
			k := order[min(max(t, 0), 3)]
			e := &p[k]
			indices[i] = k
			err += sq(r-e[0]) + sq(g-e[1]) + sq(b-e[2])
		}
		return indices, err
	}
	for i, px := range block {
		best, bestErr := 0, int32(math.MaxInt32)
		for k := range n {
			e := sq(int32(px[0])-p[k][0]) + sq(int32(px[1])-p[k][1]) + sq(int32(px[2])-p[k][2])
			if e < bestErr {
				best, bestErr = k, e
			}
		}
		indices[i] = uint8(best)
		err += bestErr
	}
	return indices, err
}

// refineColors solves for the endpoints that best fit the pixels with the
// given 4-colour indices (least squares), or reports false when the indices
// do not determine them.
func refineColors(block *pixels, indices *[16]uint8) (uint16, uint16, bool) {
	weights := [4]float32{1, 0, 2.0 / 3, 1.0 / 3}
	var aa, bb, ab float32
	var ax, bx [3]float32
	for i, px := range block {
		a := weights[indices[i]]
		b := 1 - a
		aa += a * a
		bb += b * b
		ab += a * b
		for c := range 3 {
			ax[c] += a * float32(px[c])
			bx[c] += b * float32(px[c])
		}
	}
	det := aa*bb - ab*ab
	if abs32(det) < 1e-4 {
		return 0, 0, false
	}
	var e0, e1 [3]float32
	for c := range 3 {
		e0[c] = (ax[c]*bb - bx[c]*ab) / det
		e1[c] = (bx[c]*aa - ax[c]*ab) / det
	}
	return pack565(e0[0], e0[1], e0[2]), pack565(e1[0], e1[1], e1[2]), true
}

// encodeColor writes one BC1 colour block (8 bytes). fourOnly is a BC3
// colour block, which always decodes as four colours.
func encodeColor(dst []byte, block *pixels, fourOnly bool) {
	var c0, c1 uint16
	var indices [16]uint8
	solid := true
	for _, px := range block[1:] {
		if px[0] != block[0][0] || px[1] != block[0][1] || px[2] != block[0][2] {
			solid = false
			break
		}
	}
	if solid {
		r, g, b := block[0][0], block[0][1], block[0][2]
		c0 = uint16(single5[r][0]<<11 | single6[g][0]<<5 | single5[b][0])
		c1 = uint16(single5[r][1]<<11 | single6[g][1]<<5 | single5[b][1])
		index := uint8(2)
		if c0 < c1 {
			c0, c1, index = c1, c0, 3
		} else if c0 == c1 {
			index = 0
		}
		for i := range indices {
			indices[i] = index
		}
		writeColor(dst, c0, c1, &indices)
		return
	}
	mean, axis := principalAxis(block, 3)
	if axis == [4]float32{} {
		axis = [4]float32{0.57735, 0.57735, 0.57735}
	}
	lo, hi := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	var minP, maxP [4]uint8
	for _, px := range block {
		d := (float32(px[0])-mean[0])*axis[0] + (float32(px[1])-mean[1])*axis[1] + (float32(px[2])-mean[2])*axis[2]
		if d < lo {
			lo, minP = d, px
		}
		if d > hi {
			hi, maxP = d, px
		}
	}
	c0 = pack565(float32(maxP[0]), float32(maxP[1]), float32(maxP[2]))
	c1 = pack565(float32(minP[0]), float32(minP[1]), float32(minP[2]))
	if c0 < c1 {
		c0, c1 = c1, c0
	}
	indices, err := matchColors(block, c0, c1, fourOnly)
	for range 2 {
		if c0 == c1 {
			break
		}
		n0, n1, ok := refineColors(block, &indices)
		if !ok {
			break
		}
		if n0 < n1 {
			n0, n1 = n1, n0
		}
		if n0 == n1 && !fourOnly {
			break // 3-colour mode would be selected
		}
		candidate, candidateErr := matchColors(block, n0, n1, fourOnly)
		if candidateErr >= err {
			break
		}
		c0, c1, indices, err = n0, n1, candidate, candidateErr
	}
	writeColor(dst, c0, c1, &indices)
}

func writeColor(dst []byte, c0, c1 uint16, indices *[16]uint8) {
	dst[0], dst[1], dst[2], dst[3] = byte(c0), byte(c0>>8), byte(c1), byte(c1>>8)
	var bits uint32
	for i := 15; i >= 0; i-- {
		bits = bits<<2 | uint32(indices[i])
	}
	dst[4], dst[5], dst[6], dst[7] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
}

// encodeAlpha writes one BC3 alpha / BC4 block (8 bytes) for channel c.
func encodeAlpha(dst []byte, block *pixels, c int) {
	lo, hi := uint8(255), uint8(0)
	// Range of the values other than 0 and 255, for the 6-value mode.
	lo6, hi6 := uint8(255), uint8(0)
	for _, px := range block {
		v := px[c]
		lo, hi = min(lo, v), max(hi, v)
		if v != 0 && v != 255 {
			lo6, hi6 = min(lo6, v), max(hi6, v)
		}
	}
	if lo == hi {
		dst[0], dst[1] = hi, lo
		clear(dst[2:8])
		return
	}
	a0, a1 := hi, lo
	indices, err := matchAlpha(block, c, a0, a1)
	if lo == 0 || hi == 255 {
		if lo6 > hi6 { // only 0 and 255
			lo6, hi6 = 0, 0
		}
		if candidate, candidateErr := matchAlpha(block, c, lo6, hi6); candidateErr < err {
			a0, a1, indices = lo6, hi6, candidate
		}
	}
	dst[0], dst[1] = a0, a1
	var bits uint64
	for i := 15; i >= 0; i-- {
		bits = bits<<3 | uint64(indices[i])
	}
	for i := range 6 {
		dst[2+i] = byte(bits >> (8 * i))
	}
}

// matchAlpha picks the nearest of the 8 alpha values given by a0 and a1
// (a0 > a1: 8 interpolated values; else 6 plus 0 and 255).
func matchAlpha(block *pixels, c int, a0, a1 uint8) (indices [16]uint8, err int32) {
	var palette [8]int32
	palette[0], palette[1] = int32(a0), int32(a1)
	if a0 > a1 {
		for i := int32(2); i < 8; i++ {
			palette[i] = ((8-i)*int32(a0) + (i-1)*int32(a1)) / 7
		}
	} else {
		for i := int32(2); i < 6; i++ {
			palette[i] = ((6-i)*int32(a0) + (i-1)*int32(a1)) / 5
		}
		palette[6], palette[7] = 0, 255
	}
	for i, px := range block {
		v := int32(px[c])
		best, bestErr := 0, int32(math.MaxInt32)
		for k, p := range palette {
			if e := sq(v - p); e < bestErr {
				best, bestErr = k, e
			}
		}
		indices[i] = uint8(best)
		err += bestErr
	}
	return indices, err
}
