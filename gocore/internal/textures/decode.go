package textures

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strconv"
)

// Decoding follows Pillow 12.3 for the formats textures.py accepts: the file
// type is detected from its content, not its extension, and every result is
// what Image.open(path).convert('RGBA') returns, including Pillow's mode
// conversions (16-bit grey clamps to 255, 16-bit color keeps the high byte,
// tRNS colour keys compare 8-bit pixels with the key's low byte, palette
// entries missing from PLTE are opaque black, ...).

// maxImagePixels is PIL.Image.MAX_IMAGE_PIXELS (decompression bomb limit).
const maxImagePixels = 89478485

// decoder decodes one format: config returns the size without decoding.
type decoder struct {
	name   string
	config func(data []byte) (width, height int, err error)
	decode func(data []byte) (*Image, error)
}

var errNotThisFormat = errors.New("not this format")

func detect(data []byte) *decoder {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return &pngDecoder
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return &jpegDecoder
	case bytes.HasPrefix(data, []byte("BM")):
		return &bmpDecoder
	case bytes.HasPrefix(data, []byte("MM\x00\x2a")), bytes.HasPrefix(data, []byte("II\x2a\x00")):
		return &tiffDecoder
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return &webpDecoder
	case bytes.HasPrefix(data, []byte("DDS ")):
		return &ddsDecoder
	}
	// Targa has no signature; Pillow tries it after the other formats.
	if _, _, err := tgaDecoder.config(data); err == nil {
		return &tgaDecoder
	}
	return nil
}

// errorf builds an error whose text is the Python message.
func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// bombCheck is Image._decompression_bomb_check with warnings turned into
// errors, as textures.py does.
func bombCheck(width, height int) error {
	pixels := max(1, width) * max(1, height)
	if pixels > 2*maxImagePixels {
		return errorf("Image size (%d pixels) exceeds limit of %d pixels, could be decompression bomb DOS attack.", pixels, 2*maxImagePixels)
	}
	if pixels > maxImagePixels {
		return errorf("Image size (%d pixels) exceeds limit of %d pixels, could be decompression bomb DOS attack.", pixels, maxImagePixels)
	}
	return nil
}

// toRGBA converts a decoded Go image, as Pillow's convert('RGBA') would for
// the matching Pillow mode. Gray16 clamps (I;16 -> L), 16-bit color keeps
// the high byte, CMYK uses Pillow's cmyk2rgb, YCbCr uses libjpeg's
// conversion with fancy upsampling (see jpeg.go).
func toRGBA(source image.Image) *Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	out := NewRGBA(width, height)
	switch src := source.(type) {
	case *image.NRGBA:
		for y := range height {
			copy(out.Pix[y*width*4:(y+1)*width*4], src.Pix[y*src.Stride:y*src.Stride+width*4])
		}
	case *image.RGBA:
		// Only produced for opaque images (or premultiplied TIFF, handled there).
		for y := range height {
			copy(out.Pix[y*width*4:(y+1)*width*4], src.Pix[y*src.Stride:y*src.Stride+width*4])
		}
	case *image.Gray:
		parallelRows(height, width, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride : y*src.Stride+width]
				o := out.Pix[y*width*4:]
				for x, v := range row {
					o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = v, v, v, 255
				}
			}
		})
	case *image.Gray16:
		for y := range height {
			row := src.Pix[y*src.Stride : y*src.Stride+width*2]
			o := out.Pix[y*width*4:]
			for x := range width {
				v := row[x*2+1]
				if row[x*2] != 0 {
					v = 255
				}
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = v, v, v, 255
			}
		}
	case *image.NRGBA64:
		for y := range height {
			row := src.Pix[y*src.Stride:]
			o := out.Pix[y*width*4:]
			for x := range width {
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = row[x*8], row[x*8+2], row[x*8+4], row[x*8+6]
			}
		}
	case *image.RGBA64:
		for y := range height {
			row := src.Pix[y*src.Stride:]
			o := out.Pix[y*width*4:]
			for x := range width {
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = row[x*8], row[x*8+2], row[x*8+4], row[x*8+6]
			}
		}
	case *image.CMYK:
		for y := range height {
			row := src.Pix[y*src.Stride:]
			o := out.Pix[y*width*4:]
			for x := range width {
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = cmykToRGB(row[x*4], row[x*4+1], row[x*4+2], row[x*4+3])
			}
		}
	case *image.Paletted:
		palette := paletteTable(src.Palette)
		for y := range height {
			row := src.Pix[y*src.Stride : y*src.Stride+width]
			o := out.Pix[y*width*4:]
			for x, index := range row {
				copy(o[x*4:x*4+4], palette[int(index)*4:int(index)*4+4])
			}
		}
	default:
		for y := range height {
			for x := range width {
				c := color.NRGBAModel.Convert(source.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
				o := out.Pix[(y*width+x)*4:]
				o[0], o[1], o[2], o[3] = c.R, c.G, c.B, c.A
			}
		}
	}
	return out
}

func paletteTable(palette color.Palette) []byte {
	table := make([]byte, 256*4)
	for i := range 256 {
		table[i*4+3] = 255 // missing entries are opaque black
	}
	for i, c := range palette {
		if i >= 256 {
			break
		}
		n := color.NRGBAModel.Convert(c).(color.NRGBA)
		table[i*4], table[i*4+1], table[i*4+2], table[i*4+3] = n.R, n.G, n.B, n.A
	}
	return table
}

func muldiv255(a, b int) int {
	tmp := a*b + 128
	return ((tmp >> 8) + tmp) >> 8
}

func clip8i(v int) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v)
}

// cmykToRGB is Pillow's cmyk2rgb.
func cmykToRGB(c, m, y, k byte) (byte, byte, byte, byte) {
	nk := 255 - int(k)
	return clip8i(nk - muldiv255(int(c), nk)), clip8i(nk - muldiv255(int(m), nk)), clip8i(nk - muldiv255(int(y), nk)), 255
}

// ---- helpers shared by the hand-written decoders ----

type reader struct {
	data []byte
	pos  int
}

func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || r.pos+n > len(r.data) {
		return nil, false
	}
	chunk := r.data[r.pos : r.pos+n]
	r.pos += n
	return chunk, true
}

func u16(b []byte, offset int) int    { return int(binary.LittleEndian.Uint16(b[offset:])) }
func u32(b []byte, offset int) uint32 { return binary.LittleEndian.Uint32(b[offset:]) }

var errTruncated = errors.New("image file is truncated")

func itoa(v int) string { return strconv.Itoa(v) }
