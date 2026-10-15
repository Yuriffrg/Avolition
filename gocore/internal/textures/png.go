package textures

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

var pngDecoder = decoder{name: "PNG", config: pngConfig, decode: decodePNG}

type pngInfo struct {
	width, height int
	depth         byte
	colorType     byte
	palette       []byte // PLTE
	transparency  []byte // tRNS
	hasTRNS       bool
}

func parsePNGInfo(data []byte) (pngInfo, error) {
	var info pngInfo
	if len(data) < 33 || string(data[12:16]) != "IHDR" {
		return info, errorf("cannot identify image file")
	}
	info.width = int(binary.BigEndian.Uint32(data[16:]))
	info.height = int(binary.BigEndian.Uint32(data[20:]))
	info.depth, info.colorType = data[24], data[25]
	for pos := 8; pos+8 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[pos:]))
		kind := string(data[pos+4 : pos+8])
		if length < 0 || pos+12+length > len(data) {
			break
		}
		body := data[pos+8 : pos+8+length]
		switch kind {
		case "PLTE":
			info.palette = body
		case "tRNS":
			info.transparency, info.hasTRNS = body, true
		case "IDAT", "IEND":
			return info, nil
		}
		pos += 12 + length
	}
	return info, nil
}

func pngConfig(data []byte) (int, int, error) {
	info, err := parsePNGInfo(data)
	return info.width, info.height, err
}

func be16(b []byte, offset int) int {
	if len(b) < offset+2 {
		return 0
	}
	return int(binary.BigEndian.Uint16(b[offset:]))
}

func decodePNG(data []byte) (*Image, error) {
	info, err := parsePNGInfo(data)
	if err != nil {
		return nil, err
	}
	if image, handled, err := decodePNGFast(data, info); handled {
		return image, err
	}
	// Interlaced (and malformed) files go through image/png.
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("%s", err.Error())
	}
	switch info.colorType {
	case 3:
		paletted, ok := decoded.(*image.Paletted)
		if !ok {
			return toRGBA(decoded), nil
		}
		// Pillow: PLTE entries, the rest opaque black; tRNS sets alphas.
		table := make([]byte, 256*4)
		for i := range 256 {
			table[i*4+3] = 255
			if i*3+3 <= len(info.palette) {
				copy(table[i*4:i*4+3], info.palette[i*3:i*3+3])
			}
			if i < len(info.transparency) {
				table[i*4+3] = info.transparency[i]
			}
		}
		width, height := info.width, info.height
		out := NewRGBA(width, height)
		for y := range height {
			row := paletted.Pix[y*paletted.Stride : y*paletted.Stride+width]
			o := out.Pix[y*width*4:]
			for x, index := range row {
				copy(o[x*4:x*4+4], table[int(index)*4:int(index)*4+4])
			}
		}
		return out, nil
	case 0, 2:
		out := toRGBA(decoded)
		if info.colorType == 0 {
			// I;16 -> L clamps; toRGBA clamps Gray16 but not tRNS NRGBA64.
			if src, ok := decoded.(*image.NRGBA64); ok {
				for y := range info.height {
					row := src.Pix[y*src.Stride:]
					o := out.Pix[y*info.width*4:]
					for x := range info.width {
						v := row[x*8+1]
						if row[x*8] != 0 {
							v = 255
						}
						o[x*4], o[x*4+1], o[x*4+2] = v, v, v
					}
				}
			}
		}
		// Go applies tRNS itself (correctly for low bit depths); recompute
		// the alpha as Pillow does: pixels equal to the key's low bytes.
		for i := 3; i < len(out.Pix); i += 4 {
			out.Pix[i] = 255
		}
		if !info.hasTRNS {
			return out, nil
		}
		var key [3]byte
		if info.colorType == 0 {
			t := be16(info.transparency, 0)
			k := byte(t)
			if info.depth == 1 {
				k = 0
				if t != 0 {
					k = 255
				}
			}
			key = [3]byte{k, k, k}
		} else {
			key = [3]byte{byte(be16(info.transparency, 0)), byte(be16(info.transparency, 2)), byte(be16(info.transparency, 4))}
		}
		for i := 0; i < len(out.Pix); i += 4 {
			if out.Pix[i] == key[0] && out.Pix[i+1] == key[1] && out.Pix[i+2] == key[2] {
				out.Pix[i+3] = 0
			}
		}
		return out, nil
	}
	return toRGBA(decoded), nil
}
