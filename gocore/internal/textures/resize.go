package textures

import "math"

// Lanczos resampling, bit-identical to Pillow's Resample.c for 8-bit bands
// (Image.resize(size, Image.Resampling.LANCZOS) on mode "L"): float64 filter
// weights normalised per output pixel, converted to 22-bit fixed point, then
// a horizontal pass followed by a vertical pass, each rounding and clamping
// to 8 bits. Every channel of the interleaved image is filtered separately,
// as textures.py resizes each band on its own: alpha is often a packed
// scalar, not opacity, so it must not premultiply the color channels.

const precisionBits = 32 - 8 - 2

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	x = x * math.Pi
	return math.Sin(x) / x
}

func lanczos(x float64) float64 {
	// truncated sinc
	if -3 <= x && x < 3 {
		return sinc(x) * sinc(x/3)
	}
	return 0
}

// coefficients is Pillow's precompute_coeffs + normalize_coeffs_8bpc for the
// full-image box (0, inSize).
func coefficients(inSize, outSize int) (ksize int, bounds []int, kk []int32) {
	// (double)(in1 - in0) / outSize with float box values
	scale := float64(float32(inSize)-0) / float64(outSize)
	filterscale := scale
	if filterscale < 1 {
		filterscale = 1
	}
	support := 3 * filterscale
	ksize = int(math.Ceil(support))*2 + 1
	bounds = make([]int, outSize*2)
	kk = make([]int32, outSize*ksize)
	weights := make([]float64, ksize)
	for xx := range outSize {
		center := 0 + float64((float64(xx)+0.5)*scale)
		ss := 1 / filterscale
		// Round the value (C truncates toward zero).
		xmin := int(center - support + 0.5)
		if xmin < 0 {
			xmin = 0
		}
		xmax := int(center + support + 0.5)
		if xmax > inSize {
			xmax = inSize
		}
		xmax -= xmin
		ww := 0.0
		for x := range xmax {
			w := lanczos(float64((float64(x+xmin) - center + 0.5) * ss))
			weights[x] = w
			ww += w
		}
		k := kk[xx*ksize : (xx+1)*ksize]
		for x := range xmax {
			w := weights[x]
			if ww != 0 {
				w /= ww
			}
			if w < 0 {
				k[x] = int32(-0.5 + float64(w*(1<<precisionBits)))
			} else {
				k[x] = int32(0.5 + float64(w*(1<<precisionBits)))
			}
		}
		bounds[xx*2], bounds[xx*2+1] = xmin, xmax
	}
	return ksize, bounds, kk
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

// Resize resamples an image to width x height with Pillow's LANCZOS filter.
func Resize(source *Image, width, height int) *Image {
	if source.Width == width && source.Height == height {
		return &Image{width, height, source.Channels, append([]byte(nil), source.Pix...)}
	}
	channels := source.Channels
	needHorizontal := width != source.Width
	needVertical := height != source.Height
	ksizeH, boundsH, kkH := coefficients(source.Width, width)
	ksizeV, boundsV, kkV := coefficients(source.Height, height)
	// First and last used rows of the source image.
	yFirst := boundsV[0]
	yLast := boundsV[height*2-2] + boundsV[height*2-1]

	current := source
	if needHorizontal {
		for i := range height {
			boundsV[i*2] -= yFirst
		}
		rows := yLast - yFirst
		temp := &Image{width, rows, channels, make([]byte, width*rows*channels)}
		parallelRows(rows, width*ksizeH, func(y0, y1 int) {
			horizontal(temp, source, yFirst, y0, y1, ksizeH, boundsH, kkH)
		})
		current = temp
	}
	if needVertical {
		out := &Image{current.Width, height, channels, make([]byte, current.Width*height*channels)}
		parallelRows(height, current.Width*ksizeV, func(y0, y1 int) {
			vertical(out, current, y0, y1, ksizeV, boundsV, kkV)
		})
		current = out
	}
	return current
}

func horizontal(out, in *Image, offset, y0, y1, ksize int, bounds []int, kk []int32) {
	channels := in.Channels
	inStride, outStride := in.Width*channels, out.Width*channels
	for y := y0; y < y1; y++ {
		line := in.Pix[(y+offset)*inStride : (y+offset+1)*inStride]
		target := out.Pix[y*outStride : (y+1)*outStride]
		for xx := 0; xx < out.Width; xx++ {
			xmin, xmax := bounds[xx*2], bounds[xx*2+1]
			k := kk[xx*ksize : xx*ksize+xmax]
			pixels := line[xmin*channels : (xmin+xmax)*channels]
			if channels == 4 {
				s0, s1, s2, s3 := int32(1<<(precisionBits-1)), int32(1<<(precisionBits-1)), int32(1<<(precisionBits-1)), int32(1<<(precisionBits-1))
				for x, weight := range k {
					p := pixels[x*4 : x*4+4 : x*4+4]
					s0 += int32(p[0]) * weight
					s1 += int32(p[1]) * weight
					s2 += int32(p[2]) * weight
					s3 += int32(p[3]) * weight
				}
				o := target[xx*4 : xx*4+4 : xx*4+4]
				o[0], o[1], o[2], o[3] = clip8(s0), clip8(s1), clip8(s2), clip8(s3)
				continue
			}
			for c := range channels {
				sum := int32(1 << (precisionBits - 1))
				for x, weight := range k {
					sum += int32(pixels[x*channels+c]) * weight
				}
				target[xx*channels+c] = clip8(sum)
			}
		}
	}
}

func vertical(out, in *Image, y0, y1, ksize int, bounds []int, kk []int32) {
	stride := in.Width * in.Channels
	sums := make([]int32, stride)
	for yy := y0; yy < y1; yy++ {
		ymin, ymax := bounds[yy*2], bounds[yy*2+1]
		k := kk[yy*ksize : yy*ksize+ymax]
		for i := range sums {
			sums[i] = 1 << (precisionBits - 1)
		}
		// Accumulate whole rows: the same per-pixel sums in a cache-friendly order.
		for y, weight := range k {
			row := in.Pix[(y+ymin)*stride : (y+ymin+1)*stride]
			row = row[:len(sums)]
			for i, value := range row {
				sums[i] += int32(value) * weight
			}
		}
		target := out.Pix[yy*stride : (yy+1)*stride]
		for i, sum := range sums {
			target[i] = clip8(sum)
		}
	}
}
