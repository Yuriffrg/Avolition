package bcenc

import (
	"math"
	"math/bits"
)

// BC7 mode 1: two subsets chosen by one of 64 partitions, RGB endpoints of
// 6 bits plus a p-bit shared per subset, 3-bit indices, opaque. It fixes the
// blocks a single line cannot (edges between two colours). The partition is
// the one closest to a 2-means split of the block, so only a couple of
// partitions are encoded.

// partitions2[p] has bit i set when pixel i is in subset 1 (Table A.1).
var partitions2 = [64]uint16{
	0xcccc, 0x8888, 0xeeee, 0xecc8, 0xc880, 0xfeec, 0xfec8, 0xec80, 0xc800, 0xffec,
	0xfe80, 0xe800, 0xffe8, 0xff00, 0xfff0, 0xf000, 0xf710, 0x008e, 0x7100, 0x08ce,
	0x008c, 0x7310, 0x3100, 0x8cce, 0x088c, 0x3110, 0x6666, 0x366c, 0x17e8, 0x0ff0,
	0x718e, 0x399c, 0xaaaa, 0xf0f0, 0x5a5a, 0x33cc, 0x3c3c, 0x55aa, 0x9696, 0xa55a,
	0x73ce, 0x13c8, 0x324c, 0x3bdc, 0x6996, 0xc33c, 0x9966, 0x0660, 0x0272, 0x04e4,
	0x4e40, 0x2720, 0xc936, 0x936c, 0x39c6, 0x639c, 0x9336, 0x9cc6, 0x817e, 0xe718,
	0xccf0, 0x0fcc, 0x7744, 0xee22,
}

// anchors2[p] is the anchor pixel of subset 1 (Table A.2).
var anchors2 = [64]uint8{
	15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
	15, 2, 8, 2, 2, 8, 8, 15, 2, 8, 2, 2, 8, 8, 2, 2,
	15, 15, 6, 8, 2, 8, 15, 15, 2, 8, 2, 2, 2, 15, 15, 6,
	6, 2, 6, 8, 15, 15, 2, 2, 15, 15, 15, 15, 15, 2, 2, 15,
}

var (
	weights3 = []int32{0, 9, 18, 27, 37, 46, 55, 64}
	nearest3 = nearestWeights(weights3)
)

// splitBlock returns a 2-means split of the block's colours as a pixel mask.
func splitBlock(block *pixels) uint16 {
	mean, axis := principalAxis(block[:], 3)
	var mask uint16
	for i, px := range block {
		d := (float32(px[0])-mean[0])*axis[0] + (float32(px[1])-mean[1])*axis[1] + (float32(px[2])-mean[2])*axis[2]
		if d > 0 {
			mask |= 1 << i
		}
	}
	// One k-means step from the axis split.
	var sum [2][3]int32
	var count [2]int32
	for i, px := range block {
		s := mask >> i & 1
		count[s]++
		for c := range 3 {
			sum[s][c] += int32(px[c])
		}
	}
	if count[0] == 0 || count[1] == 0 {
		return mask
	}
	var next uint16
	for i, px := range block {
		var d [2]int32
		for s := range 2 {
			for c := range 3 {
				d[s] += sq(int32(px[c])*count[s] - sum[s][c])
			}
			d[s] /= count[s] * count[s]
		}
		if d[1] < d[0] {
			next |= 1 << i
		}
	}
	return next
}

// closestPartitions returns the two partitions nearest to mask (either
// subset numbering).
func closestPartitions(mask uint16) [2]int {
	best := [2]int{0, 1}
	scores := [2]int{17, 17}
	for p, partition := range partitions2 {
		d := bits.OnesCount16(mask ^ partition)
		score := min(d, 16-d)
		if score < scores[0] {
			best[1], scores[1] = best[0], scores[0]
			best[0], scores[0] = p, score
		} else if score < scores[1] {
			best[1], scores[1] = p, score
		}
	}
	return best
}

func expand7(v int32) int32 { return v<<1 | v>>6 }

// quantizeShared quantizes two RGB endpoints to 6 bits per channel plus one
// shared p-bit (mode 1).
func quantizeShared(e0, e1 [4]float32) (q0, q1 [4]int32, p int32, d0, d1 [4]int32) {
	bestErr := float32(math.MaxFloat32)
	for bit := range int32(2) {
		var cq [2][4]int32
		var cd [2][4]int32
		var err float32
		for k, e := range [2][4]float32{e0, e1} {
			for c := range 3 {
				base := int32((e[c]*127/255-float32(bit))/2 + 0.5)
				best := float32(math.MaxFloat32)
				for v := max(base-1, 0); v <= min(base+1, 63); v++ {
					decoded := expand7(v<<1 | bit)
					if diff := abs32(float32(decoded) - e[c]); diff < best {
						best, cq[k][c], cd[k][c] = diff, v, decoded
					}
				}
				err += best * best
			}
		}
		if err < bestErr {
			bestErr, q0, q1, p, d0, d1 = err, cq[0], cq[1], bit, cd[0], cd[1]
		}
	}
	return q0, q1, p, d0, d1
}

type mode1 struct {
	partition int
	q         [4][4]int32 // subset 0: 0, 1; subset 1: 2, 3
	p         [2]int32
	indices   [16]uint8
	err       int32
}

func encodeMode1(block *pixels, partition int) mode1 {
	m := mode1{partition: partition}
	for s := range 2 {
		var sub [16][4]uint8
		var positions [16]int
		n := 0
		for i, px := range block {
			if int(partitions2[partition]>>i&1) == s {
				sub[n], positions[n] = px, i
				n++
			}
		}
		e0, e1 := lineEnds(sub[:n], 3)
		bestErr := int32(math.MaxInt32)
		for pass := range 2 {
			q0, q1, p, d0, d1 := quantizeShared(e0, e1)
			indices, err := assignLine(sub[:n], &d0, &d1, 0, 3, weights3, &nearest3)
			if err >= bestErr {
				break
			}
			bestErr = err
			m.q[2*s], m.q[2*s+1], m.p[s] = q0, q1, p
			for k := range n {
				m.indices[positions[k]] = indices[k]
			}
			if pass == 1 || err <= int32(n) {
				break
			}
			var ok bool
			if e0, e1, ok = fitLine(sub[:n], &indices, 0, 3, weights3); !ok {
				break
			}
		}
		m.err += bestErr
	}
	return m
}

func (m *mode1) write(dst []byte) {
	partition := partitions2[m.partition]
	for s, anchor := range [2]int{0, int(anchors2[m.partition])} {
		if m.indices[anchor] < 4 {
			continue
		}
		m.q[2*s], m.q[2*s+1] = m.q[2*s+1], m.q[2*s]
		for i := range m.indices {
			if int(partition>>i&1) == s {
				m.indices[i] = 7 - m.indices[i]
			}
		}
	}
	var b bitWriter
	b.put(1<<1, 2)
	b.put(uint64(m.partition), 6)
	for c := range 3 {
		for e := range 4 {
			b.put(uint64(m.q[e][c]), 6)
		}
	}
	b.put(uint64(m.p[0]), 1)
	b.put(uint64(m.p[1]), 1)
	anchor := int(anchors2[m.partition])
	for i, index := range m.indices {
		if i == 0 || i == anchor {
			b.put(uint64(index), 2)
		} else {
			b.put(uint64(index), 3)
		}
	}
	b.write(dst)
}

// mode1Threshold is the pixel error (sum of squares over the channels)
// above which mode 1 is tried.
var mode1Threshold int32 = 3 * 8 * 8
