package bcenc

import "math"

// BC7 interpolation weights (out of 64) for 2- and 4-bit indices.
var (
	weights2 = []int32{0, 21, 43, 64}
	weights4 = []int32{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}
	// nearest2/4[t] is the index whose weight is closest to t (0..64).
	nearest2, nearest4 = nearestWeights(weights2), nearestWeights(weights4)
)

func nearestWeights(weights []int32) (table [65]uint8) {
	for t := range int32(65) {
		best := 0
		for k, w := range weights {
			if abs(w-t) < abs(weights[best]-t) {
				best = k
			}
		}
		table[t] = uint8(best)
	}
	return table
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// lerp is the BC7 interpolation of one channel.
func lerp(e0, e1, w int32) int32 { return ((64-w)*e0 + w*e1 + 32) >> 6 }

// assignLine picks, for channels [c0, c1) of each pixel, the index on the
// line from e0 to e1 (decoded 8-bit endpoints) with the least error.
//
// The index comes from projecting the pixel on the line (the palette lies on
// it up to rounding), so each pixel costs one projection and one error.
func assignLine(block *pixels, e0, e1 *[4]int32, c0, c1 int, weights []int32, nearest *[65]uint8) (indices [16]uint8, err int32) {
	var mask [4]int32
	for c := c0; c < c1; c++ {
		mask[c] = 1
	}
	var palette [16][4]int32
	for k, w := range weights {
		palette[k] = [4]int32{lerp(e0[0], e1[0], w), lerp(e0[1], e1[1], w), lerp(e0[2], e1[2], w), lerp(e0[3], e1[3], w)}
	}
	dir := [4]int32{(e1[0] - e0[0]) * mask[0], (e1[1] - e0[1]) * mask[1], (e1[2] - e0[2]) * mask[2], (e1[3] - e0[3]) * mask[3]}
	dd := dir[0]*dir[0] + dir[1]*dir[1] + dir[2]*dir[2] + dir[3]*dir[3]
	var scale float32
	if dd > 0 {
		scale = 64 / float32(dd)
	}
	for i := range block {
		px := &block[i]
		r, g, b, a := int32(px[0]), int32(px[1]), int32(px[2]), int32(px[3])
		dot := (r-e0[0])*dir[0] + (g-e0[1])*dir[1] + (b-e0[2])*dir[2] + (a-e0[3])*dir[3]
		t := int32(float32(dot)*scale + 0.5)
		k := nearest[min(max(t, 0), 64)]
		p := &palette[k&15]
		err += sq(r-p[0])*mask[0] + sq(g-p[1])*mask[1] + sq(b-p[2])*mask[2] + sq(a-p[3])*mask[3]
		indices[i] = k
	}
	return indices, err
}

// fitLine solves for the endpoints of channels [c0, c1) that best fit the
// pixels with the given indices (least squares).
func fitLine(block *pixels, indices *[16]uint8, c0, c1 int, weights []int32) (e0, e1 [4]float32, ok bool) {
	var aa, bb, ab float32
	var ax, bx [4]float32
	for i, px := range block {
		b := float32(weights[indices[i]]) / 64
		a := 1 - b
		aa += a * a
		bb += b * b
		ab += a * b
		for c := c0; c < c1; c++ {
			ax[c] += a * float32(px[c])
			bx[c] += b * float32(px[c])
		}
	}
	det := aa*bb - ab*ab
	if abs32(det) < 1e-4 {
		return e0, e1, false
	}
	for c := c0; c < c1; c++ {
		e0[c] = clampF((ax[c]*bb-bx[c]*ab)/det, 0, 255)
		e1[c] = clampF((bx[c]*aa-ax[c]*ab)/det, 0, 255)
	}
	return e0, e1, true
}

// lineEnds returns the extreme points of the block's projection on its
// principal axis over channels [0, n).
func lineEnds(block *pixels, n int) (e0, e1 [4]float32) {
	mean, axis := principalAxis(block, n)
	lo, hi := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	for _, px := range block {
		var d float32
		for c := range n {
			d += (float32(px[c]) - mean[c]) * axis[c]
		}
		lo, hi = min(lo, d), max(hi, d)
	}
	if lo > hi {
		lo, hi = 0, 0
	}
	for c := range n {
		e0[c] = clampF(mean[c]+lo*axis[c], 0, 255)
		e1[c] = clampF(mean[c]+hi*axis[c], 0, 255)
	}
	return e0, e1
}

// quantizeP quantizes an RGBA endpoint to 7 bits per channel plus a shared
// p-bit (mode 6), choosing the p-bit with less error.
func quantizeP(e [4]float32) (q [4]int32, p int32, decoded [4]int32) {
	bestErr := float32(math.MaxFloat32)
	for bit := range int32(2) {
		// Keep fully opaque and fully transparent endpoints exact.
		if bit == 0 && e[3] >= 254.5 || bit == 1 && e[3] <= 0.5 {
			continue
		}
		var cq, cd [4]int32
		var err float32
		for c := range 4 {
			v := int32((e[c]-float32(bit))/2 + 0.5)
			v = min(max(v, 0), 127)
			cq[c], cd[c] = v, v<<1|bit
			d := float32(cd[c]) - e[c]
			err += d * d
		}
		if err < bestErr {
			bestErr, q, p, decoded = err, cq, bit, cd
		}
	}
	return q, p, decoded
}

// quantize7 quantizes channels [0, 3) to 7 bits (mode 5 colour endpoints).
func quantize7(e [4]float32) (q, decoded [4]int32) {
	for c := range 3 {
		base := int32(e[c]*127/255 + 0.5)
		best := float32(math.MaxFloat32)
		for v := max(base-1, 0); v <= min(base+1, 127); v++ {
			d := v<<1 | v>>6
			if diff := abs32(float32(d) - e[c]); diff < best {
				best, q[c], decoded[c] = diff, v, d
			}
		}
	}
	return q, decoded
}

type mode6 struct {
	q0, q1  [4]int32
	p0, p1  int32
	indices [16]uint8
	err     int32
}

func encodeMode6(block *pixels) mode6 {
	e0, e1 := lineEnds(block, 4)
	best := mode6{err: math.MaxInt32}
	for pass := range 2 {
		q0, p0, d0 := quantizeP(e0)
		q1, p1, d1 := quantizeP(e1)
		indices, err := assignLine(block, &d0, &d1, 0, 4, weights4, &nearest4)
		if err >= best.err {
			break
		}
		best = mode6{q0, q1, p0, p1, indices, err}
		// One least-squares refinement, unless the block already fits to
		// about one level per pixel.
		if pass == 1 || err <= 16 {
			break
		}
		var ok bool
		if e0, e1, ok = fitLine(block, &indices, 0, 4, weights4); !ok {
			break
		}
	}
	return best
}

func (m *mode6) write(dst []byte) {
	if m.indices[0] >= 8 { // the anchor index's top bit is implied 0
		m.q0, m.q1, m.p0, m.p1 = m.q1, m.q0, m.p1, m.p0
		for i := range m.indices {
			m.indices[i] = 15 - m.indices[i]
		}
	}
	var b bitWriter
	b.put(1<<6, 7)
	for c := range 4 {
		b.put(uint64(m.q0[c]), 7)
		b.put(uint64(m.q1[c]), 7)
	}
	b.put(uint64(m.p0), 1)
	b.put(uint64(m.p1), 1)
	b.put(uint64(m.indices[0]), 3)
	for _, index := range m.indices[1:] {
		b.put(uint64(index), 4)
	}
	b.write(dst)
}

type mode5 struct {
	q0, q1         [4]int32 // colour (7 bits)
	a0, a1         int32    // alpha (8 bits)
	color, alpha   [16]uint8
	colorErr, aErr int32
}

func encodeMode5(block *pixels) mode5 {
	m := mode5{colorErr: math.MaxInt32}
	e0, e1 := lineEnds(block, 3)
	for range 2 {
		q0, d0 := quantize7(e0)
		q1, d1 := quantize7(e1)
		indices, err := assignLine(block, &d0, &d1, 0, 3, weights2, &nearest2)
		if err >= m.colorErr {
			break
		}
		m.q0, m.q1, m.color, m.colorErr = q0, q1, indices, err
		var ok bool
		if e0, e1, ok = fitLine(block, &indices, 0, 3, weights2); !ok {
			break
		}
	}
	lo, hi := int32(255), int32(0)
	for _, px := range block {
		lo, hi = min(lo, int32(px[3])), max(hi, int32(px[3]))
	}
	m.aErr = math.MaxInt32
	a0, a1 := float32(lo), float32(hi)
	for range 2 {
		d0 := [4]int32{3: int32(a0 + 0.5)}
		d1 := [4]int32{3: int32(a1 + 0.5)}
		indices, err := assignLine(block, &d0, &d1, 3, 4, weights2, &nearest2)
		if err >= m.aErr {
			break
		}
		m.a0, m.a1, m.alpha, m.aErr = d0[3], d1[3], indices, err
		f0, f1, ok := fitLine(block, &indices, 3, 4, weights2)
		if !ok {
			break
		}
		a0, a1 = f0[3], f1[3]
	}
	return m
}

func (m *mode5) write(dst []byte) {
	if m.color[0] >= 2 {
		m.q0, m.q1 = m.q1, m.q0
		for i := range m.color {
			m.color[i] = 3 - m.color[i]
		}
	}
	if m.alpha[0] >= 2 {
		m.a0, m.a1 = m.a1, m.a0
		for i := range m.alpha {
			m.alpha[i] = 3 - m.alpha[i]
		}
	}
	var b bitWriter
	b.put(1<<5, 6)
	b.put(0, 2) // no rotation
	for c := range 3 {
		b.put(uint64(m.q0[c]), 7)
		b.put(uint64(m.q1[c]), 7)
	}
	b.put(uint64(m.a0), 8)
	b.put(uint64(m.a1), 8)
	b.put(uint64(m.color[0]), 1)
	for _, index := range m.color[1:] {
		b.put(uint64(index), 2)
	}
	b.put(uint64(m.alpha[0]), 1)
	for _, index := range m.alpha[1:] {
		b.put(uint64(index), 2)
	}
	b.write(dst)
}

// encodeBC7 writes one BC7 block (16 bytes).
func encodeBC7(dst []byte, block *pixels) {
	m6 := encodeMode6(block)
	alpha := block[0][3]
	varies := false
	for _, px := range block[1:] {
		if px[3] != alpha {
			varies = true
			break
		}
	}
	if varies && m6.err > 0 {
		if m5 := encodeMode5(block); m5.colorErr+m5.aErr < m6.err {
			m5.write(dst)
			return
		}
	}
	m6.write(dst)
}
