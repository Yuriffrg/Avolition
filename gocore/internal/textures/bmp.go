package textures

// Windows bitmaps, as Pillow's BmpImagePlugin reads them: 32-bit BI_RGB
// ignores the fourth byte, BITFIELDS supports Pillow's fixed mask layouts,
// grey palettes become L/1 images, RLE4/RLE8 follow BmpRleDecoder.

var bmpDecoder = decoder{name: "BMP", config: bmpConfig, decode: decodeBMP}

type bmpInfo struct {
	headerSize, width, height, bits, compression int
	colors, paletteStart, paletteEntry           int
	dataOffset                                   int
	topDown                                      bool
	masks                                        [4]uint32
	hasAlphaMask                                 bool
}

func parseBMP(data []byte) (bmpInfo, error) {
	var info bmpInfo
	if len(data) < 18 {
		return info, errTruncated
	}
	info.dataOffset = int(u32(data, 10))
	info.headerSize = int(u32(data, 14))
	if len(data) < 14+info.headerSize {
		return info, errTruncated
	}
	header := data[18 : 14+info.headerSize]
	switch info.headerSize {
	case 12:
		info.width, info.height = u16(header, 0), u16(header, 2)
		info.bits = u16(header, 6)
		info.paletteEntry = 3
	case 40, 52, 56, 64, 108, 124:
		info.topDown = header[7] == 0xff
		info.width = int(int64(u32(header, 0)))
		info.height = int(u32(header, 4))
		if info.topDown {
			info.height = int(1<<32 - int64(u32(header, 4)))
		}
		info.bits = u16(header, 10)
		info.compression = int(u32(header, 12))
		info.colors = int(u32(header, 28))
		info.paletteEntry = 4
		info.paletteStart = 14 + info.headerSize
		if info.compression == 3 {
			if len(header) >= 48 {
				count := 3
				if len(header) >= 52 {
					count = 4
					info.hasAlphaMask = true
				}
				for i := range count {
					info.masks[i] = u32(header, 36+i*4)
				}
			} else {
				if len(data) < 14+info.headerSize+12 {
					return info, errTruncated
				}
				for i := range 3 {
					info.masks[i] = u32(data, 14+info.headerSize+i*4)
				}
				info.paletteStart += 12
			}
		}
	default:
		return info, errorf("Unsupported BMP header type (%d)", info.headerSize)
	}
	if info.paletteStart == 0 {
		info.paletteStart = 14 + info.headerSize
	}
	if info.colors == 0 {
		info.colors = 1 << info.bits
	}
	if info.dataOffset == 14+info.headerSize && info.bits <= 8 {
		info.dataOffset += info.paletteEntry * info.colors
	}
	return info, nil
}

func bmpConfig(data []byte) (int, int, error) {
	info, err := parseBMP(data)
	return info.width, info.height, err
}

func decodeBMP(data []byte) (*Image, error) {
	info, err := parseBMP(data)
	if err != nil {
		return nil, err
	}
	width, height := info.width, info.height
	var unpack func(dst []byte, line []byte, x int)
	switch info.bits {
	case 1, 4, 8, 16, 24, 32:
	default:
		return nil, errorf("Unsupported BMP pixel depth (%d)", info.bits)
	}
	rle := false
	switch info.compression {
	case 0:
		switch info.bits {
		case 16:
			unpack = func(dst, line []byte, x int) { dst[0], dst[1], dst[2], dst[3] = bgr15(line[x*2:]) }
		case 24:
			unpack = func(dst, line []byte, x int) {
				dst[0], dst[1], dst[2], dst[3] = line[x*3+2], line[x*3+1], line[x*3], 255
			}
		case 32:
			unpack = func(dst, line []byte, x int) {
				dst[0], dst[1], dst[2], dst[3] = line[x*4+2], line[x*4+1], line[x*4], 255
			}
		}
	case 3:
		masks := info.masks
		if !info.hasAlphaMask {
			masks[3] = 0
		}
		switch info.bits {
		case 32:
			if masks == [4]uint32{} {
				unpack = func(dst, line []byte, x int) {
					dst[0], dst[1], dst[2], dst[3] = line[x*4+2], line[x*4+1], line[x*4], line[x*4+3]
				}
				break
			}
			supported := [][4]uint32{
				{0xFF0000, 0xFF00, 0xFF, 0x0}, {0xFF000000, 0xFF0000, 0xFF00, 0x0},
				{0xFF000000, 0xFF00, 0xFF, 0x0}, {0xFF000000, 0xFF0000, 0xFF00, 0xFF},
				{0xFF, 0xFF00, 0xFF0000, 0xFF000000}, {0xFF0000, 0xFF00, 0xFF, 0xFF000000},
				{0xFF000000, 0xFF00, 0xFF, 0xFF0000},
			}
			ok := false
			for _, layout := range supported {
				ok = ok || layout == masks
			}
			if !ok {
				return nil, errorf("Unsupported BMP bitfields layout")
			}
			var shift [4]int
			for c := range 4 {
				for shift[c] = 0; shift[c] < 4 && masks[c] != 0 && masks[c] != 0xFF<<(8*shift[c]); shift[c]++ {
				}
			}
			unpack = func(dst, line []byte, x int) {
				p := line[x*4 : x*4+4]
				for c := range 4 {
					if masks[c] == 0 {
						dst[c] = 255
					} else {
						dst[c] = p[shift[c]]
					}
				}
			}
		case 24:
			if masks[0] != 0xFF0000 || masks[1] != 0xFF00 || masks[2] != 0xFF {
				return nil, errorf("Unsupported BMP bitfields layout")
			}
			unpack = func(dst, line []byte, x int) {
				dst[0], dst[1], dst[2], dst[3] = line[x*3+2], line[x*3+1], line[x*3], 255
			}
		case 16:
			switch {
			case masks[0] == 0xF800 && masks[1] == 0x7E0 && masks[2] == 0x1F:
				unpack = func(dst, line []byte, x int) {
					pixel := int(line[x*2]) | int(line[x*2+1])<<8
					dst[0], dst[1], dst[2], dst[3] = byte(((pixel>>11)&31)*255/31), byte(((pixel>>5)&63)*255/63), byte((pixel&31)*255/31), 255
				}
			case masks[0] == 0x7C00 && masks[1] == 0x3E0 && masks[2] == 0x1F:
				unpack = func(dst, line []byte, x int) { dst[0], dst[1], dst[2], dst[3] = bgr15(line[x*2:]) }
			default:
				return nil, errorf("Unsupported BMP bitfields layout")
			}
		default:
			return nil, errorf("Unsupported BMP bitfields layout")
		}
	case 1, 2:
		rle = true
	default:
		return nil, errorf("Unsupported BMP compression (%d)", info.compression)
	}
	var palette []byte
	if info.bits <= 8 {
		if !(0 < info.colors && info.colors <= 65536) {
			return nil, errorf("Unsupported BMP Palette size (%d)", info.colors)
		}
		raw := data[min(len(data), info.paletteStart):min(len(data), info.paletteStart+info.paletteEntry*info.colors)]
		// Grey palettes are dropped (mode L / 1); the values are the same
		// as looking the indices up, so one table serves both.
		palette = make([]byte, 256*4)
		for i := range 256 {
			palette[i*4+3] = 255
		}
		for i := 0; i < 256 && (i+1)*info.paletteEntry <= len(raw); i++ {
			e := raw[i*info.paletteEntry:]
			palette[i*4], palette[i*4+1], palette[i*4+2] = e[2], e[1], e[0]
		}
		bits := info.bits
		unpack = func(dst, line []byte, x int) {
			var index int
			switch bits {
			case 8:
				index = int(line[x])
			case 4:
				index = int(line[x/2]>>(4-4*(x%2))) & 15
			case 1:
				index = int(line[x/8]>>(7-x%8)) & 1
			}
			copy(dst, palette[index*4:index*4+4])
		}
	}
	out := NewRGBA(width, height)
	if rle {
		indices, err := bmpRLE(data, info)
		if err != nil {
			return nil, err
		}
		for row := range height {
			y := height - 1 - row
			if info.topDown {
				y = row
			}
			line := indices[row*width : (row+1)*width]
			for x, index := range line {
				copy(out.Pix[(y*width+x)*4:(y*width+x)*4+4], palette[int(index)*4:int(index)*4+4])
			}
		}
		return out, nil
	}
	stride := ((width*info.bits + 31) >> 3) &^ 3
	if info.dataOffset > len(data) || len(data)-info.dataOffset < stride*height {
		return nil, errTruncated
	}
	body := data[info.dataOffset:]
	parallelRows(height, width, func(r0, r1 int) {
		for row := r0; row < r1; row++ {
			y := height - 1 - row
			if info.topDown {
				y = row
			}
			line := body[row*stride : (row+1)*stride]
			dst := out.Pix[y*width*4 : (y+1)*width*4]
			for x := range width {
				unpack(dst[x*4:x*4+4], line, x)
			}
		}
	})
	return out, nil
}

func bgr15(e []byte) (byte, byte, byte, byte) {
	pixel := int(e[0]) | int(e[1])<<8
	return byte(((pixel >> 10) & 31) * 255 / 31), byte(((pixel >> 5) & 31) * 255 / 31), byte((pixel & 31) * 255 / 31), 255
}

// bmpRLE is BmpRleDecoder: indices in file row order.
func bmpRLE(data []byte, info bmpInfo) ([]byte, error) {
	width, height := info.width, info.height
	rle4 := info.compression == 2
	pos := info.dataOffset
	read := func(n int) []byte {
		if pos >= len(data) {
			return nil
		}
		end := min(len(data), pos+n)
		chunk := data[pos:end]
		pos = end
		return chunk
	}
	destLength := width * height
	out := make([]byte, 0, destLength)
	x := 0
	for len(out) < destLength {
		pair := read(2)
		if len(pair) < 2 {
			break
		}
		count, value := int(pair[0]), pair[1]
		if count != 0 {
			if x+count > width {
				count = max(0, width-x)
			}
			for i := range count {
				if rle4 {
					if i%2 == 0 {
						out = append(out, value>>4)
					} else {
						out = append(out, value&0x0f)
					}
				} else {
					out = append(out, value)
				}
			}
			x += count
			continue
		}
		switch value {
		case 0: // end of line
			for len(out)%width != 0 {
				out = append(out, 0)
			}
			x = 0
		case 1: // end of bitmap
			return finishRLE(out, destLength)
		case 2: // delta
			delta := read(2)
			if len(delta) < 2 {
				return finishRLE(out, destLength)
			}
			for range int(delta[0]) + int(delta[1])*width {
				out = append(out, 0)
			}
			x = len(out) % width
		default: // absolute mode
			var byteCount int
			var chunk []byte
			if rle4 {
				byteCount = int(value) / 2
				chunk = read(byteCount)
				for _, b := range chunk {
					out = append(out, b>>4, b&0x0f)
				}
			} else {
				byteCount = int(value)
				chunk = read(byteCount)
				out = append(out, chunk...)
			}
			if len(chunk) < byteCount {
				return finishRLE(out, destLength)
			}
			x += int(value)
			// align to 16-bit word boundary
			if pos%2 != 0 {
				pos++
			}
		}
	}
	return finishRLE(out, destLength)
}

func finishRLE(out []byte, destLength int) ([]byte, error) {
	if len(out) < destLength {
		return nil, errorf("not enough image data")
	}
	return out[:destLength], nil
}
