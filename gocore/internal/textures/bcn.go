package textures

import "math"

// Block-compressed texture decoding, a port of Pillow's BcnDecode.c (public
// domain, CC0) so decoded pixels match Pillow exactly, including its integer
// interpolation, BC5 blue fill and BC6H tone mapping (clamp to [0, 1] and
// truncate value*255).

type rgba [4]byte

func decode565(x int) rgba {
	r := (x & 0xf800) >> 8
	r |= r >> 5
	g := (x & 0x7e0) >> 3
	g |= g >> 6
	b := (x & 0x1f) << 3
	b |= b >> 5
	return rgba{byte(r), byte(g), byte(b), 0xff}
}

func decodeBC1Color(dst *[16]rgba, src []byte, separateAlpha bool) {
	c0, c1 := u16(src, 0), u16(src, 2)
	var p [4]rgba
	p[0], p[1] = decode565(c0), decode565(c1)
	r0, g0, b0 := int(p[0][0]), int(p[0][1]), int(p[0][2])
	r1, g1, b1 := int(p[1][0]), int(p[1][1]), int(p[1][2])
	// BC2 and BC3 reuse BC1 color blocks but always act like c0 > c1.
	if c0 > c1 || separateAlpha {
		p[2] = rgba{byte((2*r0 + r1) / 3), byte((2*g0 + g1) / 3), byte((2*b0 + b1) / 3), 0xff}
		p[3] = rgba{byte((r0 + 2*r1) / 3), byte((g0 + 2*g1) / 3), byte((b0 + 2*b1) / 3), 0xff}
	} else {
		p[2] = rgba{byte((r0 + r1) / 2), byte((g0 + g1) / 2), byte((b0 + b1) / 2), 0xff}
		p[3] = rgba{}
	}
	for n := range 4 {
		for o := range 4 {
			dst[n*4+o] = p[3&(src[4+n]>>(2*o))]
		}
	}
}

// decodeBC3Alpha writes the 8-value alpha block into channel o.
func decodeBC3Alpha(dst *[16]rgba, src []byte, o int, signed bool) {
	var a0, a1 int
	if signed {
		a0, a1 = int(int8(src[0]))+128, int(int8(src[1]))+128
	} else {
		a0, a1 = int(src[0]), int(src[1])
	}
	lut1 := int(src[2]) | int(src[3])<<8 | int(src[4])<<16
	lut2 := int(src[5]) | int(src[6])<<8 | int(src[7])<<16
	var a [8]byte
	a[0], a[1] = byte(a0), byte(a1)
	if a0 > a1 {
		for i := 2; i < 8; i++ {
			a[i] = byte(((8-i)*a0 + (i-1)*a1) / 7)
		}
	} else {
		for i := 2; i < 6; i++ {
			a[i] = byte(((6-i)*a0 + (i-1)*a1) / 5)
		}
		a[6], a[7] = 0, 0xff
	}
	for n := range 8 {
		dst[n][o] = a[7&(lut1>>(3*n))]
		dst[8+n][o] = a[7&(lut2>>(3*n))]
	}
}

func getBit(src []byte, bit int) int { return int(src[bit>>3]>>(bit&7)) & 1 }

func getBits(src []byte, bit, count int) int {
	if count == 0 {
		return 0
	}
	by := bit >> 3
	bit &= 7
	if bit+count <= 8 {
		return int(src[by]>>bit) & (1<<count - 1)
	}
	x := int(src[by])
	if by+1 < len(src) {
		x |= int(src[by+1]) << 8
	}
	return (x >> bit) & (1<<count - 1)
}

type bc7ModeInfo struct{ ns, pb, rb, isb, cb, ab, epb, spb, ib, ib2 int }

var bc7Modes = [8]bc7ModeInfo{
	{3, 4, 0, 0, 4, 0, 1, 0, 3, 0},
	{2, 6, 0, 0, 6, 0, 0, 1, 3, 0},
	{3, 6, 0, 0, 5, 0, 0, 0, 2, 0},
	{2, 6, 0, 0, 7, 0, 1, 0, 2, 0},
	{1, 0, 2, 1, 5, 6, 0, 0, 2, 3},
	{1, 0, 2, 0, 7, 8, 0, 0, 2, 2},
	{1, 0, 0, 0, 7, 7, 1, 0, 4, 0},
	{2, 6, 0, 0, 5, 5, 1, 0, 2, 0},
}

var bc7SI2 = [64]uint16{
	0xcccc, 0x8888, 0xeeee, 0xecc8, 0xc880, 0xfeec, 0xfec8, 0xec80, 0xc800, 0xffec,
	0xfe80, 0xe800, 0xffe8, 0xff00, 0xfff0, 0xf000, 0xf710, 0x008e, 0x7100, 0x08ce,
	0x008c, 0x7310, 0x3100, 0x8cce, 0x088c, 0x3110, 0x6666, 0x366c, 0x17e8, 0x0ff0,
	0x718e, 0x399c, 0xaaaa, 0xf0f0, 0x5a5a, 0x33cc, 0x3c3c, 0x55aa, 0x9696, 0xa55a,
	0x73ce, 0x13c8, 0x324c, 0x3bdc, 0x6996, 0xc33c, 0x9966, 0x0660, 0x0272, 0x04e4,
	0x4e40, 0x2720, 0xc936, 0x936c, 0x39c6, 0x639c, 0x9336, 0x9cc6, 0x817e, 0xe718,
	0xccf0, 0x0fcc, 0x7744, 0xee22,
}

var bc7SI3 = [64]uint32{
	0xaa685050, 0x6a5a5040, 0x5a5a4200, 0x5450a0a8, 0xa5a50000, 0xa0a05050, 0x5555a0a0,
	0x5a5a5050, 0xaa550000, 0xaa555500, 0xaaaa5500, 0x90909090, 0x94949494, 0xa4a4a4a4,
	0xa9a59450, 0x2a0a4250, 0xa5945040, 0x0a425054, 0xa5a5a500, 0x55a0a0a0, 0xa8a85454,
	0x6a6a4040, 0xa4a45000, 0x1a1a0500, 0x0050a4a4, 0xaaa59090, 0x14696914, 0x69691400,
	0xa08585a0, 0xaa821414, 0x50a4a450, 0x6a5a0200, 0xa9a58000, 0x5090a0a8, 0xa8a09050,
	0x24242424, 0x00aa5500, 0x24924924, 0x24499224, 0x50a50a50, 0x500aa550, 0xaaaa4444,
	0x66660000, 0xa5a0a5a0, 0x50a050a0, 0x69286928, 0x44aaaa44, 0x66666600, 0xaa444444,
	0x54a854a8, 0x95809580, 0x96969600, 0xa85454a8, 0x80959580, 0xaa141414, 0x96960000,
	0xaaaa1414, 0xa05050a0, 0xa0a5a5a0, 0x96000000, 0x40804080, 0xa9a8a9a8, 0xaaaaaa44,
	0x2a4a5254,
}

var bc7AI0 = [64]int{15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 2, 8, 2, 2, 8, 8, 15, 2, 8,
	2, 2, 8, 8, 2, 2, 15, 15, 6, 8, 2, 8, 15,
	15, 2, 8, 2, 2, 2, 15, 15, 6, 6, 2, 6, 8,
	15, 15, 2, 2, 15, 15, 15, 15, 15, 2, 2, 15}

var bc7AI1 = [64]int{3, 3, 15, 15, 8, 3, 15, 15, 8, 8, 6, 6, 6,
	5, 3, 3, 3, 3, 8, 15, 3, 3, 6, 10, 5, 8,
	8, 6, 8, 5, 15, 15, 8, 15, 3, 5, 6, 10, 8,
	15, 15, 3, 15, 5, 15, 15, 15, 15, 3, 15, 5, 5,
	5, 8, 5, 10, 5, 10, 8, 13, 15, 12, 3, 3}

var bc7AI2 = [64]int{15, 8, 8, 3, 15, 15, 3, 8, 15, 15, 15, 15, 15,
	15, 15, 8, 15, 8, 15, 3, 15, 8, 15, 8, 3, 15,
	6, 10, 15, 15, 10, 8, 15, 3, 15, 10, 10, 8, 9,
	10, 6, 15, 8, 15, 3, 6, 6, 8, 15, 3, 15, 15,
	15, 15, 15, 15, 15, 15, 15, 15, 3, 15, 15, 8}

var (
	bc7Weights2 = []int{0, 21, 43, 64}
	bc7Weights3 = []int{0, 9, 18, 27, 37, 46, 55, 64}
	bc7Weights4 = []int{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}
)

func bc7Weights(n int) []int {
	switch n {
	case 2:
		return bc7Weights2
	case 3:
		return bc7Weights3
	}
	return bc7Weights4
}

func bc7Subset(ns, partition, n int) int {
	switch ns {
	case 2:
		return 1 & int(bc7SI2[partition]>>n)
	case 3:
		return 3 & int(bc7SI3[partition]>>(2*n))
	}
	return 0
}

func expandQuantized(v byte, bits int) byte {
	v = v << (8 - bits)
	return v | (v >> bits)
}

func bc7Lerp(e0, e1 rgba, s0, s1 int) rgba {
	t0, t1 := 64-s0, 64-s1
	return rgba{
		byte((t0*int(e0[0]) + s0*int(e1[0]) + 32) >> 6),
		byte((t0*int(e0[1]) + s0*int(e1[1]) + 32) >> 6),
		byte((t0*int(e0[2]) + s0*int(e1[2]) + 32) >> 6),
		byte((t1*int(e0[3]) + s1*int(e1[3]) + 32) >> 6),
	}
}

func decodeBC7Block(col *[16]rgba, src []byte) {
	mode := int(src[0])
	if mode == 0 {
		// degenerate case when no bits set
		for i := range col {
			col[i] = rgba{0, 0, 0, 255}
		}
		return
	}
	bit := 0
	for mode&(1<<bit) == 0 {
		bit++
	}
	bit++
	info := bc7Modes[bit-1]
	cb, ab := info.cb, info.ab
	cw := bc7Weights(info.ib)
	aw := cw
	if ab != 0 && info.ib2 != 0 {
		aw = bc7Weights(info.ib2)
	}
	load := func(n int) byte {
		v := getBits(src, bit, n)
		bit += n
		return byte(v)
	}
	partition := int(load(info.pb))
	rotation := load(info.rb)
	indexSel := load(info.isb)
	numep := info.ns << 1
	var endpoints [6]rgba
	for c := range 3 {
		for i := range numep {
			endpoints[i][c] = load(cb)
		}
	}
	for i := range numep {
		if ab != 0 {
			endpoints[i][3] = load(ab)
		} else {
			endpoints[i][3] = 255
		}
	}
	assignP := func(e *rgba, val byte, alpha bool) {
		for c := range 3 {
			e[c] = e[c]<<1 | val
		}
		if alpha {
			e[3] = e[3]<<1 | val
		}
	}
	if info.epb != 0 { // per endpoint
		cb++
		if ab != 0 {
			ab++
		}
		for i := range numep {
			assignP(&endpoints[i], load(1), ab != 0)
		}
	}
	if info.spb != 0 { // per subset
		cb++
		if ab != 0 {
			ab++
		}
		for i := 0; i < numep; i += 2 {
			val := load(1)
			assignP(&endpoints[i], val, ab != 0)
			assignP(&endpoints[i+1], val, ab != 0)
		}
	}
	for i := range numep {
		for c := range 3 {
			endpoints[i][c] = expandQuantized(endpoints[i][c], cb)
		}
		if ab != 0 {
			endpoints[i][3] = expandQuantized(endpoints[i][3], ab)
		}
	}
	cibit := bit
	aibit := cibit + 16*info.ib - info.ns
	for i := range 16 {
		s := bc7Subset(info.ns, partition, i) << 1
		ib := info.ib
		if i == 0 {
			ib--
		} else if info.ns == 2 {
			if i == bc7AI0[partition] {
				ib--
			}
		} else if info.ns == 3 {
			if i == bc7AI1[partition] {
				ib--
			} else if i == bc7AI2[partition] {
				ib--
			}
		}
		i0 := getBits(src, cibit, ib)
		cibit += ib
		if ab != 0 && info.ib2 != 0 {
			ib2 := info.ib2
			if i == 0 {
				ib2--
			}
			i1 := getBits(src, aibit, ib2)
			aibit += ib2
			if indexSel != 0 {
				col[i] = bc7Lerp(endpoints[s], endpoints[s+1], aw[i1], cw[i0])
			} else {
				col[i] = bc7Lerp(endpoints[s], endpoints[s+1], cw[i0], aw[i1])
			}
		} else {
			col[i] = bc7Lerp(endpoints[s], endpoints[s+1], cw[i0], cw[i0])
		}
		switch rotation {
		case 1:
			col[i][0], col[i][3] = col[i][3], col[i][0]
		case 2:
			col[i][1], col[i][3] = col[i][3], col[i][1]
		case 3:
			col[i][2], col[i][3] = col[i][3], col[i][2]
		}
	}
}

// ---- BC6H ----

type bc6ModeInfo struct{ ns, tr, pb, epb, rb, gb, bb int }

var bc6Modes = [14]bc6ModeInfo{
	{2, 1, 5, 10, 5, 5, 5},
	{2, 1, 5, 7, 6, 6, 6},
	{2, 1, 5, 11, 5, 4, 4},
	{2, 1, 5, 11, 4, 5, 4},
	{2, 1, 5, 11, 4, 4, 5},
	{2, 1, 5, 9, 5, 5, 5},
	{2, 1, 5, 8, 6, 5, 5},
	{2, 1, 5, 8, 5, 6, 5},
	{2, 1, 5, 8, 5, 5, 6},
	{2, 0, 5, 6, 6, 6, 6},
	{1, 0, 0, 10, 10, 10, 10},
	{1, 1, 0, 11, 9, 9, 9},
	{1, 1, 0, 12, 8, 8, 8},
	{1, 1, 0, 16, 4, 4, 4},
}

// bc6BitPackings is Table.F as bit indices: endpoint word (index>>4) and bit.
var bc6BitPackings = [14][]byte{
	{116, 132, 180, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17,
		18, 19, 20, 21, 22, 23, 24, 25, 32, 33, 34, 35, 36, 37, 38,
		39, 40, 41, 48, 49, 50, 51, 52, 164, 112, 113, 114, 115, 64, 65,
		66, 67, 68, 176, 160, 161, 162, 163, 80, 81, 82, 83, 84, 177, 128,
		129, 130, 131, 96, 97, 98, 99, 100, 178, 144, 145, 146, 147, 148, 179},
	{117, 164, 165, 0, 1, 2, 3, 4, 5, 6, 176, 177, 132, 16, 17,
		18, 19, 20, 21, 22, 133, 178, 116, 32, 33, 34, 35, 36, 37, 38,
		179, 181, 180, 48, 49, 50, 51, 52, 53, 112, 113, 114, 115, 64, 65,
		66, 67, 68, 69, 160, 161, 162, 163, 80, 81, 82, 83, 84, 85, 128,
		129, 130, 131, 96, 97, 98, 99, 100, 101, 144, 145, 146, 147, 148, 149},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20,
		21, 22, 23, 24, 25, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41,
		48, 49, 50, 51, 52, 10, 112, 113, 114, 115, 64, 65, 66, 67, 26,
		176, 160, 161, 162, 163, 80, 81, 82, 83, 42, 177, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 178, 144, 145, 146, 147, 148, 179},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20,
		21, 22, 23, 24, 25, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41,
		48, 49, 50, 51, 10, 164, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		26, 160, 161, 162, 163, 80, 81, 82, 83, 42, 177, 128, 129, 130, 131,
		96, 97, 98, 99, 176, 178, 144, 145, 146, 147, 116, 179},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20,
		21, 22, 23, 24, 25, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41,
		48, 49, 50, 51, 10, 132, 112, 113, 114, 115, 64, 65, 66, 67, 26,
		176, 160, 161, 162, 163, 80, 81, 82, 83, 84, 42, 128, 129, 130, 131,
		96, 97, 98, 99, 177, 178, 144, 145, 146, 147, 180, 179},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 132, 16, 17, 18, 19, 20,
		21, 22, 23, 24, 116, 32, 33, 34, 35, 36, 37, 38, 39, 40, 180,
		48, 49, 50, 51, 52, 164, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		176, 160, 161, 162, 163, 80, 81, 82, 83, 84, 177, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 178, 144, 145, 146, 147, 148, 179},
	{0, 1, 2, 3, 4, 5, 6, 7, 164, 132, 16, 17, 18, 19, 20,
		21, 22, 23, 178, 116, 32, 33, 34, 35, 36, 37, 38, 39, 179, 180,
		48, 49, 50, 51, 52, 53, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		176, 160, 161, 162, 163, 80, 81, 82, 83, 84, 177, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 101, 144, 145, 146, 147, 148, 149},
	{0, 1, 2, 3, 4, 5, 6, 7, 176, 132, 16, 17, 18, 19, 20,
		21, 22, 23, 117, 116, 32, 33, 34, 35, 36, 37, 38, 39, 165, 180,
		48, 49, 50, 51, 52, 164, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		69, 160, 161, 162, 163, 80, 81, 82, 83, 84, 177, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 178, 144, 145, 146, 147, 148, 179},
	{0, 1, 2, 3, 4, 5, 6, 7, 177, 132, 16, 17, 18, 19, 20,
		21, 22, 23, 133, 116, 32, 33, 34, 35, 36, 37, 38, 39, 181, 180,
		48, 49, 50, 51, 52, 164, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		176, 160, 161, 162, 163, 80, 81, 82, 83, 84, 85, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 178, 144, 145, 146, 147, 148, 179},
	{0, 1, 2, 3, 4, 5, 164, 176, 177, 132, 16, 17, 18, 19, 20,
		21, 117, 133, 178, 116, 32, 33, 34, 35, 36, 37, 165, 179, 181, 180,
		48, 49, 50, 51, 52, 53, 112, 113, 114, 115, 64, 65, 66, 67, 68,
		69, 160, 161, 162, 163, 80, 81, 82, 83, 84, 85, 128, 129, 130, 131,
		96, 97, 98, 99, 100, 101, 144, 145, 146, 147, 148, 149},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57,
		64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 48, 49, 50, 51, 52, 53, 54, 55, 56, 10,
		64, 65, 66, 67, 68, 69, 70, 71, 72, 26, 80, 81, 82, 83, 84, 85, 86, 87, 88, 42},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 48, 49, 50, 51, 52, 53, 54, 55, 11, 10,
		64, 65, 66, 67, 68, 69, 70, 71, 27, 26, 80, 81, 82, 83, 84, 85, 86, 87, 43, 42},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 48, 49, 50, 51, 15, 14, 13, 12, 11, 10,
		64, 65, 66, 67, 31, 30, 29, 28, 27, 26, 80, 81, 82, 83, 47, 46, 45, 44, 43, 42},
}

func bc6SignExtend(v uint16, prec int) uint16 {
	x := int(v)
	if x&(1<<(prec-1)) != 0 {
		x |= -(1 << prec)
	}
	return uint16(x)
}

func bc6Unquantize(v uint16, prec int, signed bool) int {
	if !signed {
		x := int(v)
		if prec >= 15 {
			return x
		}
		if x == 0 {
			return 0
		}
		if x == (1<<prec)-1 {
			return 0xffff
		}
		return ((x << 15) + 0x4000) >> (prec - 1)
	}
	x := int(int16(v))
	if prec >= 16 {
		return x
	}
	negative := x < 0
	if negative {
		x = -x
	}
	if x != 0 {
		if x >= (1<<(prec-1))-1 {
			x = 0x7fff
		} else {
			x = ((x << 15) + 0x4000) >> (prec - 1)
		}
	}
	if negative {
		return -x
	}
	return x
}

func halfToFloat(h uint16) float32 {
	o := math.Float32frombits(uint32(h&0x7fff) << 13)
	o *= math.Float32frombits(0x77800000)
	bits := math.Float32bits(o)
	if o >= math.Float32frombits(0x47800000) {
		bits |= 255 << 23
	}
	bits |= uint32(h&0x8000) << 16
	return math.Float32frombits(bits)
}

func bc6Finalize(v int, signed bool) float32 {
	if signed {
		if v < 0 {
			v = (-v * 31) / 32
			return halfToFloat(uint16(0x8000 | v))
		}
		return halfToFloat(uint16((v * 31) / 32))
	}
	return halfToFloat(uint16((v * 31) / 64))
}

func bc6Clamp(value float32) byte {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 255
	}
	return byte(float32(value * 255))
}

func decodeBC6Block(col *[16]rgba, src []byte, signed bool) {
	bit, epbits, ib := 5, 75, 3
	mode := int(src[0] & 0x1f)
	switch mode & 3 {
	case 0, 1:
		mode &= 3
		bit = 2
	case 2:
		mode = 2 + (mode >> 2)
		epbits = 72
	default:
		mode = 10 + (mode >> 2)
		epbits = 60
		ib = 4
	}
	if mode >= 14 {
		// invalid block
		*col = [16]rgba{}
		return
	}
	info := bc6Modes[mode]
	cw := bc7Weights(ib)
	numep := 6
	if info.ns == 2 {
		numep = 12
	}
	var endpoints [12]uint16
	for i := 0; i < epbits; i++ {
		di := int(bc6BitPackings[mode][i])
		endpoints[di>>4] |= uint16(getBit(src, bit+i)) << (di & 15)
	}
	bit += epbits
	partition := getBits(src, bit, info.pb)
	bit += info.pb
	mask := uint16(1<<info.epb - 1)
	if signed { // sign-extend e0 if signed
		for i := range 3 {
			endpoints[i] = bc6SignExtend(endpoints[i], info.epb)
		}
	}
	if signed || info.tr != 0 { // sign-extend e1,2,3 if signed or deltas
		for i := 3; i < numep; i += 3 {
			endpoints[i] = bc6SignExtend(endpoints[i], info.rb)
			endpoints[i+1] = bc6SignExtend(endpoints[i+1], info.gb)
			endpoints[i+2] = bc6SignExtend(endpoints[i+2], info.bb)
		}
	}
	if info.tr != 0 { // apply deltas
		for i := 3; i < numep; i += 3 {
			for c := range 3 {
				endpoints[i+c] = (endpoints[i+c] + endpoints[c]) & mask
			}
		}
	}
	var ueps [12]int
	for i := range numep {
		ueps[i] = bc6Unquantize(endpoints[i], info.epb, signed)
	}
	for i := range 16 {
		s := bc7Subset(info.ns, partition, i) * 6
		ib2 := ib
		if i == 0 {
			ib2--
		} else if info.ns == 2 && i == bc7AI0[partition] {
			ib2--
		}
		i0 := getBits(src, bit, ib2)
		bit += ib2
		weight := cw[i0]
		t := 64 - weight
		for c := range 3 {
			v := (ueps[s+c]*t + ueps[s+3+c]*weight) >> 6
			col[i][c] = bc6Clamp(bc6Finalize(v, signed))
		}
	}
}

// decodeBCn decodes blocks left to right, top to bottom; partial edge
// blocks are clipped.
func decodeBCn(body []byte, width, height int, format string) (*Image, error) {
	blockSize := 16
	if format == "BC1" || format == "BC4" {
		blockSize = 8
	}
	blocksX, blocksY := (width+3)/4, (height+3)/4
	if len(body) < blocksX*blocksY*blockSize {
		return nil, errorf("image file is truncated")
	}
	out := NewRGBA(width, height)
	parallelRows(blocksY, blocksX*16, func(by0, by1 int) {
		var col [16]rgba
		for by := by0; by < by1; by++ {
			for bx := range blocksX {
				src := body[(by*blocksX+bx)*blockSize:][:blockSize]
				col = [16]rgba{}
				switch format {
				case "BC1":
					decodeBC1Color(&col, src, false)
				case "BC2":
					decodeBC1Color(&col, src[8:], true)
					for n := range 16 {
						bitI := n * 4
						av := 0xf & (src[bitI>>3] >> (bitI & 7))
						col[n][3] = av<<4 | av
					}
				case "BC3":
					decodeBC1Color(&col, src[8:], true)
					decodeBC3Alpha(&col, src, 3, false)
				case "BC4":
					decodeBC3Alpha(&col, src, 0, false)
					for n := range col {
						col[n] = rgba{col[n][0], col[n][0], col[n][0], 255}
					}
				case "BC5", "BC5S":
					signed := format == "BC5S"
					if signed {
						for n := range col {
							col[n] = rgba{128, 128, 128, 128}
						}
					}
					decodeBC3Alpha(&col, src, 0, signed)
					decodeBC3Alpha(&col, src[8:], 1, signed)
					for n := range col {
						col[n][3] = 255 // mode RGB
					}
				case "BC6H", "BC6HS":
					decodeBC6Block(&col, src, format == "BC6HS")
					for n := range col {
						col[n][3] = 255
					}
				case "BC7":
					decodeBC7Block(&col, src)
				}
				for j := range 4 {
					y := by*4 + j
					if y >= height {
						break
					}
					for i := range 4 {
						x := bx*4 + i
						if x >= width {
							break
						}
						copy(out.Pix[(y*width+x)*4:], col[j*4+i][:])
					}
				}
			}
		}
	})
	return out, nil
}
