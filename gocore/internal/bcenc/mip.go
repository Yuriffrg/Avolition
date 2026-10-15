package bcenc

import (
	"math"
	"runtime"
	"sync"
)

// Level is one mip level of an RGBA image.
type Level struct {
	Width, Height int
	Pix           []byte
}

// FlipRows reverses the row order of an RGBA image in place (block
// compressed textures cannot be flipped by the GPU on upload).
func FlipRows(pix []byte, width, height int) {
	stride := width * 4
	scratch := make([]byte, stride)
	for top, bottom := 0, height-1; top < bottom; top, bottom = top+1, bottom-1 {
		a, b := pix[top*stride:][:stride], pix[bottom*stride:][:stride]
		copy(scratch, a)
		copy(a, b)
		copy(b, scratch)
	}
}

// BlockAligned reports whether a mip level of this size can be uploaded as a
// whole block-compressed level everywhere: WebGL's S3TC rules ask for sides
// that are multiples of 4, except for the 1- and 2-pixel tail levels.
func BlockAligned(width, height int) bool {
	ok := func(v int) bool { return v%4 == 0 || v == 1 || v == 2 }
	return ok(width) && ok(height)
}

// MipChain returns level 0 followed by successively halved levels (2×2 box
// filter, as WebGL's generateMipmap does) down to 1×1, stopping early at the
// first level that is not BlockAligned. srgb filters the RGB channels in
// linear light, as GPUs do for sRGB textures; alpha is always linear.
func MipChain(pix []byte, width, height int, srgb bool) []Level {
	levels := []Level{{width, height, pix}}
	for width > 1 || height > 1 {
		next := Downsample(levels[len(levels)-1], srgb)
		if !BlockAligned(next.Width, next.Height) {
			break
		}
		levels = append(levels, next)
		width, height = next.Width, next.Height
	}
	return levels
}

var (
	srgbToLinear [256]float32
	linearToSRGB [4097]uint8 // indexed by round(linear * 4096)
)

func init() {
	for i := range srgbToLinear {
		v := float64(i) / 255
		if v <= 0.04045 {
			v /= 12.92
		} else {
			v = math.Pow((v+0.055)/1.055, 2.4)
		}
		srgbToLinear[i] = float32(v)
	}
	for i := range linearToSRGB {
		v := float64(i) / 4096
		if v <= 0.0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		linearToSRGB[i] = uint8(math.Round(min(max(v, 0), 1) * 255))
	}
}

// Downsample halves a level (odd sides round down, minimum 1) with a 2×2
// box filter.
func Downsample(level Level, srgb bool) Level {
	width, height := max(1, level.Width/2), max(1, level.Height/2)
	out := make([]byte, width*height*4)
	src, stride := level.Pix, level.Width*4
	rows := func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			r0 := min(2*y, level.Height-1) * stride
			r1 := min(2*y+1, level.Height-1) * stride
			for x := range width {
				x0, x1 := min(2*x, level.Width-1)*4, min(2*x+1, level.Width-1)*4
				o := (y*width + x) * 4
				for c := range 4 {
					a, b, cc, d := src[r0+x0+c], src[r0+x1+c], src[r1+x0+c], src[r1+x1+c]
					if srgb && c < 3 {
						sum := srgbToLinear[a] + srgbToLinear[b] + srgbToLinear[cc] + srgbToLinear[d]
						out[o+c] = linearToSRGB[int(sum*1024+0.5)]
					} else {
						out[o+c] = uint8((int(a) + int(b) + int(cc) + int(d) + 2) >> 2)
					}
				}
			}
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if width*height < 1<<15 || workers == 1 {
		rows(0, height)
	} else {
		var wg sync.WaitGroup
		for i := range workers {
			y0, y1 := height*i/workers, height*(i+1)/workers
			wg.Add(1)
			go func() { defer wg.Done(); rows(y0, y1) }()
		}
		wg.Wait()
	}
	return Level{width, height, out}
}
