package textures

import (
	"fmt"
	"math"
	"runtime"
	"sync"
)

// Image is an 8-bit image with interleaved channels: 4 (RGBA, not
// premultiplied) or 3 (RGB).
type Image struct {
	Width, Height int
	Channels      int
	Pix           []byte
}

// NewRGBA allocates an RGBA image.
func NewRGBA(width, height int) *Image {
	return &Image{width, height, 4, make([]byte, width*height*4)}
}

// Spec describes a registered texture. Empty strings stand for missing keys
// (Python's spec.get defaults apply).
type Spec struct {
	Path            string `json:"path"`
	Channel         string `json:"channel,omitempty"`
	Transform       string `json:"transform,omitempty"`
	InputColorSpace string `json:"inputColorSpace,omitempty"`
}

// The expressions below reproduce PIL.ImageMath on mode "F" images exactly:
// every operation is a separate float32 operation (one rounding each, never
// fused: each product is converted to float32 before it is added), constants
// are float32, ** is powf, and convert('L') clamps to [0, 255] and truncates.

var (
	linearTable [256]float32 // [i/255 for i in range(256)] as float32
	srgbTable   [256]float32 // sRGB-decoded values (float64 math) as float32
)

func init() {
	for i := range 256 {
		v := float64(i) / 255
		linearTable[i] = float32(v)
		if v <= 0.04045 {
			srgbTable[i] = float32(v / 12.92)
		} else {
			srgbTable[i] = float32(math.Pow((v+0.055)/1.055, 2.4))
		}
	}
}

// toL is Pillow's F->L conversion (Convert.c f2l): clamp, then truncate.
func toL(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v)
}

// div is ImageMath's DIV_F: division by zero yields 0.
func div(a, b float32) float32 {
	if b != 0 {
		return a / b
	}
	return 0
}

// square is powf(v, 2), which is correctly rounded: v*v in float64 is exact
// for a float32 v, so one rounding to float32 gives the same result.
func square(v float32) float32 { return float32(float64(v) * float64(v)) }

// owNormal decodes OWM's stereographic normal packing to an 8-bit normal.
func owNormal(r, g, b byte) (byte, byte, byte) {
	red, green, blue := linearTable[r], linearTable[g], linearTable[b]
	denominator := float32(float32(31.875)*blue) + 1
	x := div(float32(float32(2)*red)-1, denominator)
	y := div(float32(float32(2)*green)-1, denominator)
	q := div(2, float32(float32(1)+square(x))+square(y))
	nx := toL(float32(float32(float32(q*x)+1) * 127.5))
	ny := toL(float32(float32(float32(q*y)+1) * 127.5))
	nz := toL(float32(q * 127.5))
	return nx, ny, nz
}

var (
	lumaR = float32(0.21263909)
	lumaG = float32(0.71516913)
	lumaB = float32(0.07219274)
	// 0.525 as a float32 image constant.
	metalThreshold = float32(0.525)
)

func luminance(table *[256]float32, r, g, b byte) byte {
	sum := float32(float32(lumaR*table[r]) + float32(lumaG*table[g]))
	sum = float32(sum + float32(lumaB*table[b]))
	return toL(float32(255 * sum))
}

func metalness(v byte) byte {
	value := linearTable[v]
	m := float32(float32(2)*value) - 1
	if m < 0 {
		m = 0
	}
	gate := float32(0)
	if value > metalThreshold {
		gate = 1
	}
	return toL(float32(float32(m*gate) * 255))
}

// ProcessChannels keeps color bytes unchanged and decodes sRGB for scalar
// channels when specified. The source must be RGBA; the result is RGB, or
// RGBA for the "rgba" channel.
func ProcessChannels(source *Image, spec Spec) (*Image, error) {
	channel := spec.Channel
	if channel == "" {
		channel = "rgba"
	}
	transform := spec.Transform
	if transform == "" {
		transform = "none"
	}
	if transform == "ow-normal" {
		// OWM: Normal in the original Blender library uses stereographic
		// packing, not the usual reconstruct-Z-from-RG convention.
		return mapPixels(source, func(in, out []byte) {
			for i, o := 0, 0; i < len(in); i, o = i+4, o+3 {
				out[o], out[o+1], out[o+2] = owNormal(in[i], in[i+1], in[i+2])
			}
		}), nil
	}
	switch channel {
	case "rgb":
		if err := checkTransformOnColor(transform); err != nil {
			return nil, err
		}
		return mapPixels(source, func(in, out []byte) {
			for i, o := 0, 0; i < len(in); i, o = i+4, o+3 {
				out[o], out[o+1], out[o+2] = in[i], in[i+1], in[i+2]
			}
		}), nil
	case "rgba":
		if err := checkTransformOnColor(transform); err != nil {
			return nil, err
		}
		result := &Image{source.Width, source.Height, 4, make([]byte, len(source.Pix))}
		copy(result.Pix, source.Pix)
		return result, nil
	case "r", "g", "b", "a", "luminance":
	default:
		return nil, fmt.Errorf("Unsupported texture channel: %s", channel)
	}
	var post [256]byte // the scalar transform as a lookup table
	for i := range post {
		switch transform {
		case "none":
			post[i] = byte(i)
		case "invert":
			post[i] = 255 - byte(i)
		case "ow-metalness":
			post[i] = metalness(byte(i))
		default:
			return nil, fmt.Errorf("Unsupported texture transform: %s", transform)
		}
	}
	// Three's AO/roughness/metalness maps sample different RGB components.
	// Duplicating the selected scalar into RGB makes their meaning explicit.
	if channel == "luminance" {
		table := &linearTable
		if spec.InputColorSpace == "srgb" { // spec.get('inputColorSpace', 'linear')
			table = &srgbTable
		}
		return mapPixels(source, func(in, out []byte) {
			for i, o := 0, 0; i < len(in); i, o = i+4, o+3 {
				v := post[luminance(table, in[i], in[i+1], in[i+2])]
				out[o], out[o+1], out[o+2] = v, v, v
			}
		}), nil
	}
	var lut [256]byte
	for i := range lut {
		if channel != "a" && spec.InputColorSpace == "srgb" {
			lut[i] = post[toL(float32(srgbTable[i]*255))]
		} else {
			lut[i] = post[i]
		}
	}
	band := indexOf(channel)
	return mapPixels(source, func(in, out []byte) {
		for i, o := band, 0; i < len(in); i, o = i+4, o+3 {
			v := lut[in[i]]
			out[o], out[o+1], out[o+2] = v, v, v
		}
	}), nil
}

func indexOf(channel string) int {
	switch channel {
	case "r":
		return 0
	case "g":
		return 1
	case "b":
		return 2
	}
	return 3
}

// checkTransformOnColor mirrors the errors Python raises when a scalar-only
// transform is applied to an RGB(A) result.
func checkTransformOnColor(transform string) error {
	switch transform {
	case "none":
		return nil
	case "invert":
		return fmt.Errorf("Inverted packed channels must be scalar.")
	case "ow-metalness":
		return fmt.Errorf("Packed metalness must be a scalar channel.")
	}
	return fmt.Errorf("Unsupported texture transform: %s", transform)
}

// mapPixels converts an RGBA image into an RGB image in parallel row bands;
// convert receives matching RGBA and RGB row ranges.
func mapPixels(source *Image, convert func(in, out []byte)) *Image {
	result := &Image{source.Width, source.Height, 3, make([]byte, source.Width*source.Height*3)}
	parallelRows(source.Height, source.Width, func(y0, y1 int) {
		convert(source.Pix[y0*source.Width*4:y1*source.Width*4], result.Pix[y0*source.Width*3:y1*source.Width*3])
	})
	return result
}

// parallelRows splits [0, height) into bands processed concurrently.
func parallelRows(height, width int, fn func(y0, y1 int)) {
	workers := runtime.GOMAXPROCS(0)
	if height*width < 1<<15 || workers == 1 || height < 2 {
		fn(0, height)
		return
	}
	bands := min(workers*2, height)
	var wg sync.WaitGroup
	for i := range bands {
		y0, y1 := height*i/bands, height*(i+1)/bands
		if y0 == y1 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}
