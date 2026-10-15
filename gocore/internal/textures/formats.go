package textures

import (
	"bytes"
	"image"
	"image/jpeg"
	"io"

	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

var (
	tiffDecoder = decoder{name: "TIFF", config: configOf(tiff.DecodeConfig), decode: decodeTIFF}
	webpDecoder = decoder{name: "WEBP", config: configOf(webp.DecodeConfig), decode: decodeWebP}
	jpegDecoder = decoder{name: "JPEG", config: configOf(jpeg.DecodeConfig), decode: decodeJPEG}
)

func configOf(fn func(io.Reader) (image.Config, error)) func([]byte) (int, int, error) {
	return func(data []byte) (int, int, error) {
		config, err := fn(bytes.NewReader(data))
		return config.Width, config.Height, err
	}
}

// decodeTIFF uses x/image/tiff (uncompressed, LZW, Deflate, PackBits, CCITT;
// Pillow's libtiff also reads JPEG-compressed TIFF, which is rejected here).
// Associated alpha is unpremultiplied like Pillow's RGBa unpacker.
func decodeTIFF(data []byte) (*Image, error) {
	decoded, err := tiff.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("%s", err.Error())
	}
	if src, ok := decoded.(*image.RGBA); ok {
		width, height := src.Rect.Dx(), src.Rect.Dy()
		out := NewRGBA(width, height)
		for y := range height {
			row := src.Pix[y*src.Stride:]
			o := out.Pix[y*width*4:]
			for x := range width {
				p := row[x*4 : x*4+4]
				a := int(p[3])
				switch a {
				case 0:
					o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = 0, 0, 0, 0
				case 255:
					copy(o[x*4:x*4+4], p)
				default:
					o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = clip8i(int(p[0])*255/a), clip8i(int(p[1])*255/a), clip8i(int(p[2])*255/a), byte(a)
				}
			}
		}
		return out, nil
	}
	return toRGBA(decoded), nil
}
