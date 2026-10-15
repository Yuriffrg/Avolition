package exporttextures

// Pixel math, bit-identical to the Pillow 12.3 operations export_textures.py
// composes (verified against Python's output by the oracle tests):
//
//   - point(table, 'F'): a float32 lookup table (the Python doubles rounded);
//   - Image.resize(size, BILINEAR): Resample.c's two passes (horizontal,
//     then vertical). 8-bit bands use 22-bit fixed-point weights and round
//     after each pass; mode "F" accumulates float*double products in double
//     and stores float32. RGBA is premultiplied (RGBa) around the resize;
//   - ImageMath on mode "F": one float32 operation at a time, constants are
//     float32, comparisons yield 1.0/0.0, min/max are the C ternaries, and
//     ** is glibc's powf (ported below);
//   - convert('L') from "F": clamp to [0, 255], then truncate.
//
// Every float32 product/sum is written with an explicit conversion so the
// compiler can never fuse it into an FMA (Go may fuse x*y+z otherwise).

import (
	"math"
	"runtime"
	"sync"

	"avolition/gocore/internal/textures"
)

var (
	linearTable [256]float32 // _LINEAR: value / 255
	srgbTable   [256]float32 // _SRGB: sRGB-decoded value, computed in double
)

func init() {
	for i := range 256 {
		v := float64(i) / 255
		linearTable[i] = float32(v)
		if v <= .04045 {
			srgbTable[i] = float32(v / 12.92)
		} else {
			srgbTable[i] = float32(math.Pow((v+.055)/1.055, 2.4))
		}
	}
}

// f2l is Pillow's F -> L conversion (Convert.c f2l).
func f2l(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v)
}

// ---------------------------------------------------------------------------
// glibc powf (sysdeps/ieee754/flt-32/e_powf.c, from ARM optimized-routines),
// for the non-negative arguments _encode passes. Checked exhaustively against
// glibc 2.39's powf for every float32 in [0, 1] with y = float32(1/2.4), the
// only exponent used here: no differences (the FMA variant glibc selects on
// modern CPUs gives the same results on that domain).

var powfLogTable = [16][2]float64{
	{0x1.661ec79f8f3bep+0, -0x1.efec65b963019p-2},
	{0x1.571ed4aaf883dp+0, -0x1.b0b6832d4fca4p-2},
	{0x1.49539f0f010bp+0, -0x1.7418b0a1fb77bp-2},
	{0x1.3c995b0b80385p+0, -0x1.39de91a6dcf7bp-2},
	{0x1.30d190c8864a5p+0, -0x1.01d9bf3f2b631p-2},
	{0x1.25e227b0b8eap+0, -0x1.97c1d1b3b7afp-3},
	{0x1.1bb4a4a1a343fp+0, -0x1.2f9e393af3c9fp-3},
	{0x1.12358f08ae5bap+0, -0x1.960cbbf788d5cp-4},
	{0x1.0953f419900a7p+0, -0x1.a6f9db6475fcep-5},
	{0x1p+0, 0x0p+0},
	{0x1.e608cfd9a47acp-1, 0x1.338ca9f24f53dp-4},
	{0x1.ca4b31f026aap-1, 0x1.476a9543891bap-3},
	{0x1.b2036576afce6p-1, 0x1.e840b4ac4e4d2p-3},
	{0x1.9c2d163a1aa2dp-1, 0x1.40645f0c6651cp-2},
	{0x1.886e6037841edp-1, 0x1.88e9c2c1b9ff8p-2},
	{0x1.767dcf5534862p-1, 0x1.ce0a44eb17bccp-2},
}

var powfLogPoly = [5]float64{0x1.27616c9496e0bp-2, -0x1.71969a075c67ap-2, 0x1.ec70a6ca7baddp-2,
	-0x1.7154748bef6c8p-1, 0x1.71547652ab82bp0}

var exp2fTable = [32]uint64{
	0x3ff0000000000000, 0x3fefd9b0d3158574, 0x3fefb5586cf9890f, 0x3fef9301d0125b51,
	0x3fef72b83c7d517b, 0x3fef54873168b9aa, 0x3fef387a6e756238, 0x3fef1e9df51fdee1,
	0x3fef06fe0a31b715, 0x3feef1a7373aa9cb, 0x3feedea64c123422, 0x3feece086061892d,
	0x3feebfdad5362a27, 0x3feeb42b569d4f82, 0x3feeab07dd485429, 0x3feea47eb03a5585,
	0x3feea09e667f3bcd, 0x3fee9f75e8ec5f74, 0x3feea11473eb0187, 0x3feea589994cce13,
	0x3feeace5422aa0db, 0x3feeb737b0cdc5e5, 0x3feec49182a3f090, 0x3feed503b23e255d,
	0x3feee89f995ad3ad, 0x3feeff76f2fb5e47, 0x3fef199bdd85529c, 0x3fef3720dcef9069,
	0x3fef5818dcfba487, 0x3fef7c97337b9b5f, 0x3fefa4afa2a490da, 0x3fefd0765b6e4540,
}

var exp2fPoly = [3]float64{0x1.c6af84b912394p-5, 0x1.ebfce50fac4f3p-3, 0x1.62e42ff0c52d6p-1}

const exp2fShift = 0x1.8p+52 / 32

// powf returns powf(x, y) for finite x >= 0 and a finite positive y small
// enough that the result cannot overflow or underflow to zero for x in
// (0, 1] (the special-case branches of glibc that this use cannot reach are
// handled for zero, one and non-finite input only).
func powf(x, y float32) float32 {
	ix := math.Float32bits(x)
	switch {
	case ix == 0:
		return 0
	case ix == 0x3f800000:
		return 1
	case ix >= 0x7f800000:
		return float32(math.Pow(float64(x), float64(y)))
	case ix < 0x00800000:
		// Normalize subnormal x so exponent becomes negative.
		ix = math.Float32bits(float32(x * 0x1p23))
		ix &= 0x7fffffff
		ix -= 23 << 23
	}
	// log2_inline
	tmp := ix - 0x3f330000
	i := (tmp >> 19) % 16
	top := tmp & 0xff800000
	iz := ix - top
	k := int32(top) >> 23
	invc, logc := powfLogTable[i][0], powfLogTable[i][1]
	z := float64(math.Float32frombits(iz))
	r := float64(float64(z*invc) - 1)
	y0 := float64(logc + float64(k))
	r2 := float64(r * r)
	l := float64(float64(powfLogPoly[0]*r) + powfLogPoly[1])
	p := float64(float64(powfLogPoly[2]*r) + powfLogPoly[3])
	r4 := float64(r2 * r2)
	q := float64(float64(powfLogPoly[4]*r) + y0)
	q = float64(float64(p*r2) + q)
	l = float64(float64(l*r4) + q)
	xd := float64(float64(y) * l)
	if math.Abs(xd) >= 126 {
		return float32(math.Pow(float64(x), float64(y)))
	}
	// exp2_inline
	kd := float64(xd + exp2fShift)
	ki := math.Float64bits(kd)
	kd = float64(kd - exp2fShift)
	rr := float64(xd - kd)
	t := exp2fTable[ki%32]
	t += ki << (52 - 5)
	s := math.Float64frombits(t)
	zz := float64(float64(exp2fPoly[0]*rr) + exp2fPoly[1])
	rr2 := float64(rr * rr)
	e := float64(float64(exp2fPoly[2]*rr) + 1)
	e = float64(float64(zz*rr2) + e)
	e = float64(e * s)
	return float32(e)
}

// ---------------------------------------------------------------------------
// Resample.c with the BILINEAR filter.

const precisionBits = 32 - 8 - 2

func bilinear(x float64) float64 {
	if x < 0 {
		x = -x
	}
	if x < 1 {
		return 1 - x
	}
	return 0
}

// coefficients is precompute_coeffs for the full-image box (0, inSize).
// kk holds the normalized double weights, ksize per output pixel.
func coefficients(inSize, outSize int) (ksize int, bounds []int, kk []float64) {
	scale := float64(float32(inSize)) / float64(outSize)
	filterscale := scale
	if filterscale < 1 {
		filterscale = 1
	}
	support := 1 * filterscale
	ksize = int(math.Ceil(support))*2 + 1
	bounds = make([]int, outSize*2)
	kk = make([]float64, outSize*ksize)
	inv := 1 / filterscale
	for xx := range outSize {
		center := float64(float64(float64(xx)+0.5) * scale)
		xmin := int(center - support + 0.5)
		if xmin < 0 {
			xmin = 0
		}
		xmax := int(center + support + 0.5)
		if xmax > inSize {
			xmax = inSize
		}
		xmax -= xmin
		k := kk[xx*ksize : (xx+1)*ksize]
		ww := 0.0
		for x := range xmax {
			w := bilinear(float64(float64(float64(x+xmin)-center+0.5) * inv))
			k[x] = w
			ww += w
		}
		if ww != 0 {
			for x := range xmax {
				k[x] /= ww
			}
		}
		bounds[xx*2], bounds[xx*2+1] = xmin, xmax
	}
	return ksize, bounds, kk
}

// fixed is normalize_coeffs_8bpc.
func fixed(kk []float64) []int32 {
	out := make([]int32, len(kk))
	for i, w := range kk {
		if w < 0 {
			out[i] = int32(-0.5 + float64(w*(1<<precisionBits)))
		} else {
			out[i] = int32(0.5 + float64(w*(1<<precisionBits)))
		}
	}
	return out
}

func clip8(value int32) byte {
	value >>= precisionBits
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return byte(value)
}

// plan is ImagingResampleInner's two-pass schedule.
type plan struct {
	inW, inH, outW, outH int
	needH, needV         bool
	ksizeH, ksizeV       int
	boundsH, boundsV     []int
	kkH, kkV             []float64
	yFirst, rows         int
}

func newPlan(inW, inH, outW, outH int) *plan {
	p := &plan{inW: inW, inH: inH, outW: outW, outH: outH, needH: outW != inW, needV: outH != inH}
	p.ksizeV, p.boundsV, p.kkV = coefficients(inH, outH)
	p.yFirst = p.boundsV[0]
	yLast := p.boundsV[outH*2-2] + p.boundsV[outH*2-1]
	p.rows = inH
	if p.needH {
		p.ksizeH, p.boundsH, p.kkH = coefficients(inW, outW)
		for i := range outH {
			p.boundsV[i*2] -= p.yFirst
		}
		p.rows = yLast - p.yFirst
	} else {
		p.yFirst = 0
	}
	return p
}

// resize8 resamples one 8-bit band (w*h bytes).
func (p *plan) resize8(band []byte) []byte {
	current, width := band, p.inW
	if p.needH {
		kk := fixed(p.kkH)
		temp := make([]byte, p.outW*p.rows)
		parallelRows(p.rows, p.outW*p.ksizeH, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				line := band[(y+p.yFirst)*p.inW : (y+p.yFirst+1)*p.inW]
				out := temp[y*p.outW : (y+1)*p.outW]
				for xx := range p.outW {
					xmin, xmax := p.boundsH[xx*2], p.boundsH[xx*2+1]
					k := kk[xx*p.ksizeH : xx*p.ksizeH+xmax]
					ss := int32(1 << (precisionBits - 1))
					for x, w := range k {
						ss += int32(line[x+xmin]) * w
					}
					out[xx] = clip8(ss)
				}
			}
		})
		current, width = temp, p.outW
	}
	if p.needV {
		kk := fixed(p.kkV)
		out := make([]byte, width*p.outH)
		parallelRows(p.outH, width*p.ksizeV, func(y0, y1 int) {
			for yy := y0; yy < y1; yy++ {
				ymin, ymax := p.boundsV[yy*2], p.boundsV[yy*2+1]
				k := kk[yy*p.ksizeV : yy*p.ksizeV+ymax]
				target := out[yy*width : (yy+1)*width]
				for xx := range width {
					ss := int32(1 << (precisionBits - 1))
					for y, w := range k {
						ss += int32(current[(y+ymin)*width+xx]) * w
					}
					target[xx] = clip8(ss)
				}
			}
		})
		current = out
	}
	if !p.needH && !p.needV {
		current = append([]byte(nil), band...)
	}
	return current
}

// resizeF resamples one mode "F" band.
func (p *plan) resizeF(band []float32) []float32 {
	current, width := band, p.inW
	if p.needH {
		temp := make([]float32, p.outW*p.rows)
		parallelRows(p.rows, p.outW*p.ksizeH, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				line := band[(y+p.yFirst)*p.inW : (y+p.yFirst+1)*p.inW]
				out := temp[y*p.outW : (y+1)*p.outW]
				for xx := range p.outW {
					xmin, xmax := p.boundsH[xx*2], p.boundsH[xx*2+1]
					k := p.kkH[xx*p.ksizeH : xx*p.ksizeH+xmax]
					ss := 0.0
					for x, w := range k {
						ss = ss + float64(float64(line[x+xmin])*w)
					}
					out[xx] = float32(ss)
				}
			}
		})
		current, width = temp, p.outW
	}
	if p.needV {
		out := make([]float32, width*p.outH)
		parallelRows(p.outH, width*p.ksizeV, func(y0, y1 int) {
			for yy := y0; yy < y1; yy++ {
				ymin, ymax := p.boundsV[yy*2], p.boundsV[yy*2+1]
				k := p.kkV[yy*p.ksizeV : yy*p.ksizeV+ymax]
				target := out[yy*width : (yy+1)*width]
				for xx := range width {
					ss := 0.0
					for y, w := range k {
						ss = ss + float64(float64(current[(y+ymin)*width+xx])*w)
					}
					target[xx] = float32(ss)
				}
			}
		})
		current = out
	}
	if !p.needH && !p.needV {
		current = append([]float32(nil), band...)
	}
	return current
}

// channel extracts one band of an interleaved image.
func channel(image *textures.Image, index int) []byte {
	n := image.Width * image.Height
	out := make([]byte, n)
	c := image.Channels
	for i := range n {
		out[i] = image.Pix[i*c+index]
	}
	return out
}

// maskRed is _resized(mask, size).getchannel('R'): the red band after
// Image.resize(size, BILINEAR). RGBA is resized premultiplied (RGBa), as
// Image.resize does, and converted back.
func maskRed(mask *textures.Image, width, height int) []byte {
	if mask.Width == width && mask.Height == height {
		return channel(mask, 0)
	}
	p := newPlan(mask.Width, mask.Height, width, height)
	if mask.Channels != 4 {
		return p.resize8(channel(mask, 0))
	}
	n := mask.Width * mask.Height
	red, alpha := make([]byte, n), make([]byte, n)
	for i := range n {
		a := uint32(mask.Pix[i*4+3])
		tmp := uint32(mask.Pix[i*4])*a + 128 // MULDIV255
		red[i] = byte(((tmp >> 8) + tmp) >> 8)
		alpha[i] = byte(a)
	}
	red, alpha = p.resize8(red), p.resize8(alpha)
	for i, a := range alpha {
		if a != 255 && a != 0 {
			red[i] = byte(min(255, (255*int(red[i]))/int(a)))
		}
	}
	return red
}

// encodeSRGB is _encode(value) with srgb=True, one ImageMath step at a time.
func encodeSRGB(v float32) byte {
	const threshold = float32(.0031308)
	exponent := float32(1 / 2.4)
	// max(min(v, 1), 0)
	if !(v < 1) {
		v = 1
	}
	if !(v > 0) {
		v = 0
	}
	var le, gt float32
	if v <= threshold {
		le = 1
	}
	if v > threshold {
		gt = 1
	}
	low := float32(le * float32(float32(12.92)*v))
	high := float32(gt * float32(float32(float32(1.055)*powf(v, exponent))-float32(.055)))
	value := float32(low + high)
	return f2l(float32(float32(value*255) + float32(.5)))
}

// compositeColor is _composite_color: multiply in linear light and encode
// one portable sRGB map. color may be nil (a constant 1 band).
func compositeColor(color *textures.Image, colorSpace string, factor [3]float64, masks []*textures.Image,
	width, height int, gain float64) *textures.Image {
	n := width * height
	maskBands := make([][]byte, len(masks))
	for i, mask := range masks {
		maskBands[i] = maskRed(mask, width, height)
	}
	table := &linearTable
	if colorSpace == "srgb" {
		table = &srgbTable
	}
	// GPU sRGB sampling filters after conversion to linear light. Resizing
	// encoded RGB first would darken edges.
	var resized [3][]float32
	if color != nil && (color.Width != width || color.Height != height) {
		p := newPlan(color.Width, color.Height, width, height)
		for c := range 3 {
			band := make([]float32, color.Width*color.Height)
			for i := range band {
				band[i] = table[color.Pix[i*color.Channels+c]]
			}
			resized[c] = p.resizeF(band)
		}
	}
	var factors [3]float32
	for c := range 3 {
		factors[c] = float32(factor[c])
	}
	g := float32(gain)
	out := &textures.Image{Width: width, Height: height, Channels: 3, Pix: make([]byte, n*3)}
	parallelRows(height, width*3*(4+len(masks)), func(y0, y1 int) {
		for i := y0 * width; i < y1*width; i++ {
			for c := range 3 {
				var band float32 = 1
				switch {
				case resized[c] != nil:
					band = resized[c][i]
				case color != nil:
					band = table[color.Pix[i*color.Channels+c]]
				}
				value := float32(band * factors[c])
				if g != 0 { // ImageMath DIV_F
					value = float32(value / g)
				} else {
					value = 0
				}
				for _, mask := range maskBands {
					value = float32(value * linearTable[mask[i]])
				}
				out.Pix[i*3+c] = encodeSRGB(value)
			}
		}
	})
	return out
}

// scaleScalar is _scale_scalar: the red band through a rounded LUT, as RGB.
func scaleScalar(image *textures.Image, factor float64) (*textures.Image, error) {
	var lut [256]byte
	for value := range 256 {
		scaled := float64(value) * factor
		if math.IsInf(scaled, 0) || math.IsNaN(scaled) {
			return nil, errOverflow
		}
		lut[value] = byte(min(255, max(0, math.RoundToEven(scaled))))
	}
	n := image.Width * image.Height
	out := &textures.Image{Width: image.Width, Height: image.Height, Channels: 3, Pix: make([]byte, n*3)}
	c := image.Channels
	for i := range n {
		v := lut[image.Pix[i*c]]
		out.Pix[i*3], out.Pix[i*3+1], out.Pix[i*3+2] = v, v, v
	}
	return out, nil
}

// parallelRows splits rows over the CPUs when the work is large enough.
func parallelRows(height, costPerRow int, fn func(y0, y1 int)) {
	workers := runtime.NumCPU()
	if workers > height {
		workers = height
	}
	if workers <= 1 || height*costPerRow < 1<<16 {
		fn(0, height)
		return
	}
	chunk := (height + workers*4 - 1) / (workers * 4)
	var wg sync.WaitGroup
	next := make(chan int, (height+chunk-1)/chunk)
	for y := 0; y < height; y += chunk {
		next <- y
	}
	close(next)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y0 := range next {
				fn(y0, min(height, y0+chunk))
			}
		}()
	}
	wg.Wait()
}
