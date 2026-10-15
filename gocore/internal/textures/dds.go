package textures

// DirectDraw Surface, as Pillow's DdsImagePlugin reads it: the first surface
// (top mip level) of uncompressed RGB(A)/L/LA/P data, BC1-BC7 including
// BC6H, and DX10 R8G8B8A8.

var ddsDecoder = decoder{name: "DDS", config: ddsConfig, decode: decodeDDS}

const (
	ddpfAlphaPixels     = 0x1
	ddpfFourCC          = 0x4
	ddpfPaletteIndexed8 = 0x20
	ddpfRGB             = 0x40
	ddpfLuminance       = 0x20000
)

func fourCC(s string) uint32 {
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

// DXGI formats Pillow handles.
const (
	dxgiR8G8B8A8Typeless = 27
	dxgiR8G8B8A8Unorm    = 28
	dxgiR8G8B8A8SRGB     = 29
	dxgiBC1Typeless      = 70
	dxgiBC1Unorm         = 71
	dxgiBC2Typeless      = 73
	dxgiBC2Unorm         = 74
	dxgiBC3Typeless      = 76
	dxgiBC3Unorm         = 77
	dxgiBC4Typeless      = 79
	dxgiBC4Unorm         = 80
	dxgiBC5Typeless      = 82
	dxgiBC5Unorm         = 83
	dxgiBC5Snorm         = 84
	dxgiBC6HUF16         = 95
	dxgiBC6HSF16         = 96
	dxgiBC7Typeless      = 97
	dxgiBC7Unorm         = 98
	dxgiBC7SRGB          = 99
)

func ddsConfig(data []byte) (int, int, error) {
	if len(data) < 128 {
		return 0, 0, errorf("Incomplete header: %d bytes", max(0, len(data)-8))
	}
	if u32(data, 4) != 124 {
		return 0, 0, errorf("Unsupported header size %d", u32(data, 4))
	}
	return int(u32(data, 16)), int(u32(data, 12)), nil
}

func decodeDDS(data []byte) (*Image, error) {
	width, height, err := ddsConfig(data)
	if err != nil {
		return nil, err
	}
	header := data[8:128]
	pfFlags, fourcc, bitCount := u32(header, 72), u32(header, 76), int(u32(header, 80))
	body := data[128:]
	switch {
	case pfFlags&ddpfRGB != 0:
		maskCount := 3
		if pfFlags&ddpfAlphaPixels != 0 {
			maskCount = 4
		}
		masks := make([]uint32, maskCount)
		for i := range masks {
			masks[i] = u32(header, 84+i*4)
		}
		return ddsRGB(body, width, height, bitCount, masks)
	case pfFlags&ddpfLuminance != 0:
		if bitCount == 8 {
			return rawPixels(body, width, height, 1, func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[0], src[0], src[0], 255 })
		}
		if bitCount == 16 && pfFlags&ddpfAlphaPixels != 0 {
			return rawPixels(body, width, height, 2, func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[0], src[0], src[0], src[1] })
		}
		return nil, errorf("Unsupported bitcount %d for %d", bitCount, pfFlags)
	case pfFlags&ddpfPaletteIndexed8 != 0:
		if len(body) < 1024 {
			return nil, errTruncated
		}
		palette := body[:1024] // RGBA entries
		return rawPixels(body[1024:], width, height, 1, func(dst, src []byte) { copy(dst, palette[int(src[0])*4:int(src[0])*4+4]) })
	case pfFlags&ddpfFourCC != 0:
		format := ""
		switch fourcc {
		case fourCC("DXT1"):
			format = "BC1"
		case fourCC("DXT3"):
			format = "BC2"
		case fourCC("DXT5"):
			format = "BC3"
		case fourCC("BC4U"), fourCC("ATI1"):
			format = "BC4"
		case fourCC("BC5S"):
			format = "BC5S"
		case fourCC("BC5U"), fourCC("ATI2"):
			format = "BC5"
		case fourCC("DX10"):
			if len(body) < 20 {
				return nil, errTruncated
			}
			dxgi := u32(body, 0)
			body = body[20:]
			switch dxgi {
			case dxgiBC1Unorm, dxgiBC1Typeless:
				format = "BC1"
			case dxgiBC2Typeless, dxgiBC2Unorm:
				format = "BC2"
			case dxgiBC3Typeless, dxgiBC3Unorm:
				format = "BC3"
			case dxgiBC4Typeless, dxgiBC4Unorm:
				format = "BC4"
			case dxgiBC5Typeless, dxgiBC5Unorm:
				format = "BC5"
			case dxgiBC5Snorm:
				format = "BC5S"
			case dxgiBC6HUF16:
				format = "BC6H"
			case dxgiBC6HSF16:
				format = "BC6HS"
			case dxgiBC7Typeless, dxgiBC7Unorm, dxgiBC7SRGB:
				format = "BC7"
			case dxgiR8G8B8A8Typeless, dxgiR8G8B8A8Unorm, dxgiR8G8B8A8SRGB:
				return rawPixels(body, width, height, 4, func(dst, src []byte) { copy(dst, src[:4]) })
			default:
				return nil, errorf("Unimplemented DXGI format %d", dxgi)
			}
		default:
			return nil, errorf("Unimplemented pixel format %s", pyBytesRepr(header[76:80]))
		}
		return decodeBCn(body, width, height, format)
	}
	return nil, errorf("Unknown pixel format flags %d", pfFlags)
}

// pyBytesRepr renders repr(fourcc) for the error text (an int in Pillow).
func pyBytesRepr(b []byte) string { return itoa(int(u32(b, 0))) }

// rawPixels unpacks top-down rows of fixed-size pixels.
func rawPixels(body []byte, width, height, size int, unpack func(dst, src []byte)) (*Image, error) {
	if len(body) < width*height*size {
		return nil, errorf("image file is truncated")
	}
	out := NewRGBA(width, height)
	parallelRows(height, width, func(y0, y1 int) {
		for i := y0 * width; i < y1*width; i++ {
			unpack(out.Pix[i*4:i*4+4], body[i*size:i*size+size])
		}
	})
	return out, nil
}

// ddsRGB is DdsRgbDecoder: each mask's value scaled to 8 bits with
// int(value / maximum * 255).
func ddsRGB(body []byte, width, height, bitCount int, masks []uint32) (*Image, error) {
	byteCount := bitCount / 8
	out := NewRGBA(width, height)
	if byteCount == 0 {
		return nil, errorf("not enough image data")
	}
	if len(body) < width*height*byteCount {
		return nil, errorf("not enough image data")
	}
	var offsets [4]uint
	var totals [4]uint32
	for i, mask := range masks {
		offset := uint(0)
		if mask != 0 {
			for (mask>>(offset+1))<<(offset+1) == mask {
				offset++
			}
		}
		offsets[i], totals[i] = offset, mask>>offset
	}
	scale := func(i int, value uint32) byte {
		if totals[i] == 0 {
			return 0
		}
		return byte(int(float64((value&masks[i])>>offsets[i]) / float64(totals[i]) * 255))
	}
	for p := range width * height {
		var value uint32
		for b := byteCount - 1; b >= 0; b-- {
			if b < 4 {
				value = value<<8 | uint32(body[p*byteCount+b])
			}
		}
		o := out.Pix[p*4 : p*4+4]
		o[0], o[1], o[2], o[3] = scale(0, value), scale(1, value), scale(2, value), 255
		if len(masks) == 4 {
			o[3] = scale(3, value)
		}
	}
	return out, nil
}
