package textures

// Targa, as Pillow's TgaImagePlugin and TgaRleDecode.c read it.

var tgaDecoder = decoder{name: "TGA", config: tgaConfig, decode: decodeTGA}

type tgaHeader struct {
	idLength, colorMapType, imageType int
	mapStart, mapLength, mapDepth     int
	width, height, depth, flags       int
}

func parseTGA(data []byte) (tgaHeader, error) {
	if len(data) < 18 {
		return tgaHeader{}, errNotThisFormat
	}
	h := tgaHeader{
		idLength: int(data[0]), colorMapType: int(data[1]), imageType: int(data[2]),
		mapStart: u16(data, 3), mapLength: u16(data, 5), mapDepth: int(data[7]),
		width: u16(data, 12), height: u16(data, 14), depth: int(data[16]), flags: int(data[17]),
	}
	if h.colorMapType > 1 || h.width <= 0 || h.height <= 0 {
		return h, errNotThisFormat
	}
	switch h.depth {
	case 1, 8, 16, 24, 32:
	default:
		return h, errNotThisFormat
	}
	switch h.imageType {
	case 1, 2, 3, 9, 10, 11:
	default:
		return h, errNotThisFormat
	}
	return h, nil
}

func tgaConfig(data []byte) (int, int, error) {
	h, err := parseTGA(data)
	return h.width, h.height, err
}

func decodeTGA(data []byte) (*Image, error) {
	h, err := parseTGA(data)
	if err != nil {
		return nil, errorf("cannot identify image file")
	}
	var mode string // Pillow image mode
	switch h.imageType {
	case 3, 11:
		mode = "L"
		if h.depth == 1 {
			mode = "1"
		} else if h.depth == 16 {
			mode = "LA"
		}
	case 1, 9:
		mode = "L"
		if h.colorMapType == 1 {
			mode = "P"
		}
	case 2, 10:
		mode = "RGBA"
		if h.depth == 24 {
			mode = "RGB"
		}
	}
	r := &reader{data: data, pos: 18}
	if _, ok := r.take(h.idLength); !ok {
		return nil, errTruncated
	}
	// Palette as RGBA entries (Pillow initialises unset entries to opaque black).
	palette := make([]byte, 256*4)
	for i := range 256 {
		palette[i*4+3] = 255
	}
	if h.colorMapType == 1 {
		var entry int
		switch h.mapDepth {
		case 16:
			entry = 2
		case 24:
			entry = 3
		case 32:
			entry = 4
		default:
			return nil, errorf("unknown TGA map depth")
		}
		raw, _ := r.take(entry * h.mapLength)
		if raw == nil {
			raw = r.data[r.pos:]
			r.pos = len(r.data)
		}
		for i := 0; (i+1)*entry <= len(raw); i++ {
			index := h.mapStart + i
			if index >= 256 {
				break
			}
			p := palette[index*4 : index*4+4]
			e := raw[i*entry:]
			switch entry {
			case 2:
				p[0], p[1], p[2], p[3] = bgra15z(e)
			case 3:
				p[0], p[1], p[2] = e[2], e[1], e[0]
			case 4:
				// Read as a "BGRA" raw palette in an RGB palette: alpha is dropped.
				p[0], p[1], p[2] = e[2], e[1], e[0]
			}
		}
	}
	// MODES[(imagetype & 7, depth)] -> raw mode and bytes per pixel.
	var unpack func(dst, src []byte)
	var pixelBytes int
	switch [2]int{h.imageType & 7, h.depth} {
	case [2]int{1, 8}:
		if mode != "P" {
			return nil, errorf("unknown raw mode for given image mode")
		}
		pixelBytes = 1
		unpack = func(dst, src []byte) { copy(dst, palette[int(src[0])*4:int(src[0])*4+4]) }
	case [2]int{3, 1}:
		pixelBytes = 0 // bit packed
	case [2]int{3, 8}:
		pixelBytes = 1
		unpack = func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[0], src[0], src[0], 255 }
	case [2]int{3, 16}:
		pixelBytes = 2
		unpack = func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[0], src[0], src[0], src[1] }
	case [2]int{2, 16}:
		pixelBytes = 2
		unpack = func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = bgra15z(src) }
	case [2]int{2, 24}:
		pixelBytes = 3
		unpack = func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[2], src[1], src[0], 255 }
	case [2]int{2, 32}:
		pixelBytes = 4
		unpack = func(dst, src []byte) { dst[0], dst[1], dst[2], dst[3] = src[2], src[1], src[0], src[3] }
	default:
		return nil, errorf("cannot decode image data")
	}
	width, height := h.width, h.height
	out := NewRGBA(width, height)
	lineBytes := width * pixelBytes
	if pixelBytes == 0 {
		lineBytes = (width + 7) / 8
	}
	unpackLine := func(y int, line []byte) {
		dst := out.Pix[y*width*4 : (y+1)*width*4]
		if pixelBytes == 0 {
			for x := range width {
				v := byte(0)
				if line[x/8]&(0x80>>(x%8)) != 0 {
					v = 255
				}
				dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = v, v, v, 255
			}
			return
		}
		for x := range width {
			unpack(dst[x*4:x*4+4], line[x*pixelBytes:x*pixelBytes+pixelBytes])
		}
	}
	// Orientation: 0x20 means top-down rows.
	y, step := height-1, -1
	if h.flags&0x20 != 0 {
		y, step = 0, 1
	}
	if h.imageType&8 == 0 {
		for range height {
			line, ok := r.take(lineBytes)
			if !ok {
				return nil, errTruncated
			}
			unpackLine(y, line)
			y += step
		}
	} else if err := tgaRLE(r, h.depth, lineBytes, height, func(line []byte) bool {
		unpackLine(y, line)
		y += step
		return y >= 0 && y < height
	}); err != nil {
		return nil, err
	}
	if mode == "RGBA" && len(data) >= 26 && string(data[len(data)-18:]) == "TRUEVISION-XFILE.\x00" {
		// Version 2 footer: an attributes type of 0 means no alpha.
		footer := data[len(data)-26:]
		if offset := int(u32(footer, 0)); offset != 0 && offset+495 <= len(data) && data[offset+494] == 0 {
			for i := 3; i < len(out.Pix); i += 4 {
				out.Pix[i] = 255
			}
		}
	}
	if h.flags&0x10 != 0 { // flip horizontally
		for y := range height {
			row := out.Pix[y*width*4 : (y+1)*width*4]
			for a, b := 0, width-1; a < b; a, b = a+1, b-1 {
				for c := range 4 {
					row[a*4+c], row[b*4+c] = row[b*4+c], row[a*4+c]
				}
			}
		}
	}
	return out, nil
}

// bgra15z is Pillow's BGRA;15Z unpacker (5-bit channels, inverted alpha bit).
func bgra15z(e []byte) (byte, byte, byte, byte) {
	pixel := int(e[0]) | int(e[1])<<8
	a := byte(255)
	if pixel>>15 != 0 {
		a = 0
	}
	return byte(((pixel >> 10) & 31) * 255 / 31), byte(((pixel >> 5) & 31) * 255 / 31), byte((pixel & 31) * 255 / 31), a
}

// tgaRLE is TgaRleDecode.c: runs may not cross a scanline (overrun error),
// literal packets may. emit returns false after the last line.
func tgaRLE(r *reader, depth, lineBytes, height int, emit func(line []byte) bool) error {
	pixel := (depth + 7) / 8
	buffer := make([]byte, lineBytes)
	x := 0
	data := r.data
	pos := r.pos
	for {
		if pos >= len(data) {
			return errTruncated
		}
		n := pixel * (int(data[pos]&0x7f) + 1)
		extra := 0
		if data[pos]&0x80 != 0 {
			if len(data)-pos < 1+pixel {
				return errTruncated
			}
			if x+n > lineBytes {
				return errorf("image buffer overrun error when reading image file")
			}
			for i := 0; i < n; i += pixel {
				copy(buffer[x+i:x+i+pixel], data[pos+1:pos+1+pixel])
			}
			pos += 1 + pixel
		} else {
			if len(data)-pos < 1+n {
				return errTruncated
			}
			literal := data[pos+1 : pos+1+n]
			if x+n > lineBytes {
				extra = n
				n = lineBytes - x
				extra -= n
			}
			copy(buffer[x:], literal[:n])
			pos += 1 + n // extra bytes continue on the next lines
		}
		for {
			x += n
			if x >= lineBytes {
				if !emit(buffer) {
					return nil
				}
				x = 0
			}
			if extra == 0 {
				break
			}
			if x > 0 {
				break
			}
			n = min(extra, lineBytes)
			copy(buffer[x:], data[pos:pos+n])
			pos += n
			extra -= n
		}
	}
}
