package textures

import (
	"bytes"
	"image"
	"image/jpeg"

	"golang.org/x/image/webp"
)

// ---- JPEG ----
//
// image/jpeg decodes the DCT coefficients; the chroma upsampling and color
// conversion below follow libjpeg-turbo (Pillow's decoder): "fancy"
// triangular upsampling (jdsample.c h2v1/h1v2/h2v2_fancy_upsample) and the
// fixed-point YCbCr->RGB tables of jdcolor.c. image/jpeg's IDCT is not
// libjpeg's jidctint, so samples can still differ by a few levels.

const (
	scaleBits = 16
	oneHalf   = 1 << (scaleBits - 1)
)

func fix(x float64) int { return int(x*(1<<scaleBits) + 0.5) }

var crR, cbB, crG, cbG [256]int

func init() {
	for i := range 256 {
		x := i - 128
		crR[i] = (fix(1.40200)*x + oneHalf) >> scaleBits
		cbB[i] = (fix(1.77200)*x + oneHalf) >> scaleBits
		crG[i] = -fix(0.71414) * x
		cbG[i] = -fix(0.34414)*x + oneHalf
	}
}

func yccToRGB(y, cb, cr byte, dst []byte) {
	Y := int(y)
	dst[0] = clip8i(Y + crR[cr])
	dst[1] = clip8i(Y + ((cbG[cb] + crG[cr]) >> scaleBits))
	dst[2] = clip8i(Y + cbB[cb])
}

// upsampleRowH2 is h2v1_fancy_upsample for one row of n chroma samples.
func upsampleRowH2(in []byte, out []byte) {
	n := len(in)
	if n == 1 {
		out[0], out[1] = in[0], in[0]
		return
	}
	out[0] = in[0]
	out[1] = byte((int(in[0])*3 + int(in[1]) + 2) >> 2)
	for i := 1; i < n-1; i++ {
		v := int(in[i]) * 3
		out[2*i] = byte((v + int(in[i-1]) + 1) >> 2)
		out[2*i+1] = byte((v + int(in[i+1]) + 2) >> 2)
	}
	v := int(in[n-1])
	out[2*n-2] = byte((v*3 + int(in[n-2]) + 1) >> 2)
	out[2*n-1] = byte(v)
}

// upsampleRowH2V2 is h2v2_fancy_upsample for one output row: near is the
// chroma row closest to it, far the adjacent one (replicated at edges).
func upsampleRowH2V2(near, far []byte, out []byte) {
	n := len(near)
	this := int(near[0])*3 + int(far[0])
	if n == 1 {
		out[0] = byte((this*4 + 8) >> 4)
		out[1] = byte((this*4 + 7) >> 4)
		return
	}
	next := int(near[1])*3 + int(far[1])
	out[0] = byte((this*4 + 8) >> 4)
	out[1] = byte((this*3 + next + 7) >> 4)
	last := this
	this = next
	for col := 1; col < n-1; col++ {
		next = int(near[col+1])*3 + int(far[col+1])
		out[2*col] = byte((this*3 + last + 8) >> 4)
		out[2*col+1] = byte((this*3 + next + 7) >> 4)
		last, this = this, next
	}
	out[2*n-2] = byte((this*3 + last + 8) >> 4)
	out[2*n-1] = byte((this*4 + 7) >> 4)
}

// chromaUpsampler writes the full-width chroma row for output row y.
type chromaUpsampler func(plane []byte, stride, cw, ch, y int, out []byte)

func upsampler(ratio image.YCbCrSubsampleRatio) (hs, vs int, fn chromaUpsampler) {
	switch ratio {
	case image.YCbCrSubsampleRatio444:
		return 1, 1, func(plane []byte, stride, cw, ch, y int, out []byte) {
			copy(out, plane[y*stride:y*stride+cw])
		}
	case image.YCbCrSubsampleRatio422:
		return 2, 1, func(plane []byte, stride, cw, ch, y int, out []byte) {
			upsampleRowH2(plane[y*stride:y*stride+cw], out)
		}
	case image.YCbCrSubsampleRatio420:
		return 2, 2, func(plane []byte, stride, cw, ch, y int, out []byte) {
			row := y / 2
			other := row - 1
			if y%2 == 1 {
				other = row + 1
			}
			other = max(0, min(ch-1, other))
			upsampleRowH2V2(plane[row*stride:row*stride+cw], plane[other*stride:other*stride+cw], out)
		}
	case image.YCbCrSubsampleRatio440:
		return 1, 2, func(plane []byte, stride, cw, ch, y int, out []byte) {
			row := y / 2
			other := row - 1
			bias := 1
			if y%2 == 1 {
				other = row + 1
				bias = 2
			}
			other = max(0, min(ch-1, other))
			near, far := plane[row*stride:row*stride+cw], plane[other*stride:other*stride+cw]
			for i := range near {
				out[i] = byte((int(near[i])*3 + int(far[i]) + bias) >> 2)
			}
		}
	}
	return 0, 0, nil
}

// yCbCrToRGBA converts a JPEG's planes with libjpeg's fancy upsampling.
func yCbCrToRGBA(src *image.YCbCr) *Image {
	width, height := src.Rect.Dx(), src.Rect.Dy()
	hs, vs, fn := upsampler(src.SubsampleRatio)
	if fn == nil { // 4:1:1 and 4:1:0 use plain replication in libjpeg too
		return toRGBA(fallbackNRGBA(src))
	}
	cw, ch := (width+hs-1)/hs, (height+vs-1)/vs
	if cw <= 2 && (hs > 1 || vs > 1) {
		// jinit_upsampler: do_fancy requires downsampled_width > 2; otherwise
		// samples are replicated.
		box := hs
		fn = func(plane []byte, stride, cw, ch, y int, out []byte) {
			row := plane[(y/vs)*stride:]
			for x := range out {
				out[x] = row[x/box]
			}
		}
	}
	out := NewRGBA(width, height)
	parallelRows(height, width, func(y0, y1 int) {
		cb := make([]byte, cw*hs)
		cr := make([]byte, cw*hs)
		for y := y0; y < y1; y++ {
			fn(src.Cb, src.CStride, cw, ch, y, cb)
			fn(src.Cr, src.CStride, cw, ch, y, cr)
			row := src.Y[y*src.YStride:]
			o := out.Pix[y*width*4:]
			for x := range width {
				yccToRGB(row[x], cb[x], cr[x], o[x*4:x*4+3])
				o[x*4+3] = 255
			}
		}
	})
	return out
}

func fallbackNRGBA(src image.Image) *image.NRGBA {
	bounds := src.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(x, y, src.At(x, y))
		}
	}
	return out
}

func decodeJPEG(data []byte) (*Image, error) {
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("%s", err.Error())
	}
	if src, ok := decoded.(*image.YCbCr); ok {
		return yCbCrToRGBA(src), nil
	}
	return toRGBA(decoded), nil
}

// ---- WebP ----
//
// x/image/webp decodes VP8 exactly; libwebp (Pillow's decoder) then applies
// its fancy upsampler (upsampling.c UpsampleRgbaLinePair) and the 14-bit
// fixed-point conversion of yuv.h, reproduced here. Lossless files are exact.

func multHi(v, coeff int) int { return (v * coeff) >> 8 }

func vp8Clip8(v int) byte {
	const yuvMask2 = (256 << 6) - 1
	if v&^yuvMask2 == 0 {
		return byte(v >> 6)
	}
	if v < 0 {
		return 0
	}
	return 255
}

func vp8YUVToRGB(y, u, v int, dst []byte) {
	dst[0] = vp8Clip8(multHi(y, 19077) + multHi(v, 26149) - 14234)
	dst[1] = vp8Clip8(multHi(y, 19077) - multHi(u, 6419) - multHi(v, 13320) + 8708)
	dst[2] = vp8Clip8(multHi(y, 19077) + multHi(u, 33050) - 17685)
}

// upsampleLinePair is UpsampleRgbaLinePair: topY/bottomY are luma rows
// (bottomY may be nil), topU/V the chroma row above, curU/V the one below.
func upsampleLinePair(topY, bottomY, topU, topV, curU, curV []byte, topDst, bottomDst []byte, length int) {
	load := func(u, v byte) uint32 { return uint32(u) | uint32(v)<<16 }
	lastPair := (length - 1) >> 1
	tl := load(topU[0], topV[0])
	l := load(curU[0], curV[0])
	put := func(y byte, uv uint32, dst []byte) {
		vp8YUVToRGB(int(y), int(uv&0xff), int(uv>>16), dst)
	}
	uv0 := (3*tl + l + 0x00020002) >> 2
	put(topY[0], uv0, topDst[0:])
	if bottomY != nil {
		uv0 = (3*l + tl + 0x00020002) >> 2
		put(bottomY[0], uv0, bottomDst[0:])
	}
	for x := 1; x <= lastPair; x++ {
		t := load(topU[x], topV[x])
		uv := load(curU[x], curV[x])
		avg := tl + t + l + uv + 0x00080008
		diag12 := (avg + 2*(t+l)) >> 3
		diag03 := (avg + 2*(tl+uv)) >> 3
		a := (diag12 + tl) >> 1
		b := (diag03 + t) >> 1
		put(topY[2*x-1], a, topDst[(2*x-1)*4:])
		put(topY[2*x], b, topDst[(2*x)*4:])
		if bottomY != nil {
			a = (diag03 + l) >> 1
			b = (diag12 + uv) >> 1
			put(bottomY[2*x-1], a, bottomDst[(2*x-1)*4:])
			put(bottomY[2*x], b, bottomDst[(2*x)*4:])
		}
		tl, l = t, uv
	}
	if length&1 == 0 {
		uv0 = (3*tl + l + 0x00020002) >> 2
		put(topY[length-1], uv0, topDst[(length-1)*4:])
		if bottomY != nil {
			uv0 = (3*l + tl + 0x00020002) >> 2
			put(bottomY[length-1], uv0, bottomDst[(length-1)*4:])
		}
	}
}

// webpToRGBA follows io_dec.c EmitFancyRGB over the whole picture: row 0
// alone, then pairs (2k-1, 2k) between chroma rows k-1 and k, and the last
// row alone when the height is even.
func webpToRGBA(src *image.YCbCr, alpha []byte, alphaStride int) *Image {
	width, height := src.Rect.Dx(), src.Rect.Dy()
	out := NewRGBA(width, height)
	uvWidth := (width + 1) / 2
	yRow := func(y int) []byte { return src.Y[y*src.YStride : y*src.YStride+width] }
	cRow := func(plane []byte, row int) []byte { return plane[row*src.CStride : row*src.CStride+uvWidth] }
	dst := func(y int) []byte { return out.Pix[y*width*4 : (y+1)*width*4] }
	upsampleLinePair(yRow(0), nil, cRow(src.Cb, 0), cRow(src.Cr, 0), cRow(src.Cb, 0), cRow(src.Cr, 0), dst(0), nil, width)
	pairs := (height - 1) / 2
	parallelRows(pairs, width*2, func(p0, p1 int) {
		for k := p0 + 1; k <= p1; k++ {
			upsampleLinePair(yRow(2*k-1), yRow(2*k), cRow(src.Cb, k-1), cRow(src.Cr, k-1), cRow(src.Cb, k), cRow(src.Cr, k),
				dst(2*k-1), dst(2*k), width)
		}
	})
	if height%2 == 0 && height > 1 {
		last := height/2 - 1
		upsampleLinePair(yRow(height-1), nil, cRow(src.Cb, last), cRow(src.Cr, last), cRow(src.Cb, last), cRow(src.Cr, last), dst(height-1), nil, width)
	}
	for y := range height {
		o := out.Pix[y*width*4:]
		if alpha == nil {
			for x := range width {
				o[x*4+3] = 255
			}
			continue
		}
		a := alpha[y*alphaStride:]
		for x := range width {
			o[x*4+3] = a[x]
		}
	}
	return out
}

func decodeWebP(data []byte) (*Image, error) {
	decoded, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("%s", err.Error())
	}
	switch src := decoded.(type) {
	case *image.YCbCr:
		return webpToRGBA(src, nil, 0), nil
	case *image.NYCbCrA:
		return webpToRGBA(&src.YCbCr, src.A, src.AStride), nil
	}
	return toRGBA(decoded), nil
}
