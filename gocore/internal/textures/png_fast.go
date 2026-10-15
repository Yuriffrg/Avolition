package textures

import (
	"encoding/binary"
	"sync"
)

// Fast path for non-interlaced PNG files: IDAT data is inflated by
// inflate.go in one goroutine while this one unfilters each completed row
// and converts it to RGBA with Pillow's mode rules. rowsDone, when set, is
// called from the calling goroutine each time more rows are complete.

func decodePNGFast(data []byte, info pngInfo, rowsDone func(image *Image, rows int)) (*Image, bool, error) {
	if len(data) < 29 || data[28] != 0 { // interlaced
		return nil, false, nil
	}
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[info.colorType]
	depth := int(info.depth)
	if channels == 0 || !validDepth(info.colorType, depth) {
		return nil, false, nil
	}
	width, height := info.width, info.height
	bitsPerPixel := channels * depth
	stride := (width*bitsPerPixel + 7) / 8
	bpp := max(1, bitsPerPixel/8)

	// Collect IDAT payloads (one copy unless there is a single chunk).
	var compressed []byte
	var chunks [][]byte
	for pos := 8; pos+8 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[pos:]))
		kind := string(data[pos+4 : pos+8])
		if length < 0 || pos+12+length > len(data) {
			if kind == "IDAT" && pos+8 < len(data) {
				chunks = append(chunks, data[pos+8:])
			}
			break
		}
		if kind == "IDAT" {
			chunks = append(chunks, data[pos+8:pos+8+length])
		} else if kind == "IEND" || (len(chunks) > 0 && kind != "IDAT") {
			break
		}
		pos += 12 + length
	}
	if len(chunks) == 1 {
		compressed = chunks[0]
	} else {
		total := 0
		for _, c := range chunks {
			total += len(c)
		}
		compressed = make([]byte, 0, total)
		for _, c := range chunks {
			compressed = append(compressed, c...)
		}
	}

	raw := make([]byte, height*(stride+1))
	out := NewRGBA(width, height)
	convert := pngRowConverter(info, width)

	progress := make(chan int, 64)
	var inflateErr error
	written := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(progress)
		written, inflateErr = inflateZlib(compressed, raw, func(n int) {
			select {
			case progress <- n:
			default: // the consumer catches up at the next report
			}
		})
	}()
	// raw is also the inflater's history window, so rows are unfiltered in a
	// separate pair of row buffers rather than in place.
	row := 0
	rows := make([]byte, 2*stride)
	previous, current := rows[:stride], rows[stride:]
	process := func(available int) error {
		for ; row < height && (row+1)*(stride+1) <= available; row++ {
			line := raw[row*(stride+1) : (row+1)*(stride+1)]
			copy(current, line[1:])
			if !unfilter(line[0], current, previous, bpp) {
				return errorf("unrecognized PNG filter type %d", line[0])
			}
			convert(out.Pix[row*width*4:(row+1)*width*4], current)
			previous, current = current, previous
		}
		return nil
	}
	var processErr error
	for n := range progress {
		if processErr == nil {
			before := row
			processErr = process(n)
			if rowsDone != nil && row > before {
				rowsDone(out, row)
			}
		}
	}
	wg.Wait()
	if processErr != nil {
		return nil, true, processErr
	}
	if err := process(written); err != nil {
		return nil, true, err
	}
	if row < height {
		if inflateErr != nil {
			return nil, true, inflateErr
		}
		return nil, true, errTruncated
	}
	return out, true, nil
}

// unfilterPaeth4 is the Paeth case for 4 bytes per pixel (RGBA, the most
// common texture layout) after the first pixel: the left and upper-left
// bytes stay in registers and the slices are bounds-checked once per pixel.
func unfilterPaeth4(current, previous []byte) {
	a0, a1, a2, a3 := int(current[0]), int(current[1]), int(current[2]), int(current[3])
	c0, c1, c2, c3 := int(previous[0]), int(previous[1]), int(previous[2]), int(previous[3])
	previous = previous[:len(current)]
	for i := 4; i+4 <= len(current); i += 4 {
		x := current[i : i+4 : i+4]
		p := previous[i : i+4 : i+4]
		b0, b1, b2, b3 := int(p[0]), int(p[1]), int(p[2]), int(p[3])
		x[0] += paethInt(a0, b0, c0)
		x[1] += paethInt(a1, b1, c1)
		x[2] += paethInt(a2, b2, c2)
		x[3] += paethInt(a3, b3, c3)
		a0, a1, a2, a3 = int(x[0]), int(x[1]), int(x[2]), int(x[3])
		c0, c1, c2, c3 = b0, b1, b2, b3
	}
}

func validDepth(colorType byte, depth int) bool {
	switch colorType {
	case 0:
		return depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16
	case 3:
		return depth == 1 || depth == 2 || depth == 4 || depth == 8
	default:
		return depth == 8 || depth == 16
	}
}

// unfilter reverses one PNG row filter in place.
func unfilter(filter byte, current, previous []byte, bpp int) bool {
	n := len(current)
	previous = previous[:n]
	switch filter {
	case 0:
	case 1:
		for i := bpp; i < n; i++ {
			current[i] += current[i-bpp]
		}
	case 2:
		for i, p := range previous {
			current[i] += p
		}
	case 3:
		for i := 0; i < bpp && i < n; i++ {
			current[i] += previous[i] / 2
		}
		for i := bpp; i < n; i++ {
			current[i] += byte((int(current[i-bpp]) + int(previous[i])) / 2)
		}
	case 4:
		for i := 0; i < bpp && i < n; i++ {
			current[i] += previous[i]
		}
		if bpp == 4 && n%4 == 0 && n >= 4 {
			unfilterPaeth4(current, previous)
			break
		}
		for i := bpp; i < n; i++ {
			current[i] += paethInt(int(current[i-bpp]), int(previous[i]), int(previous[i-bpp]))
		}
	default:
		return false
	}
	return true
}

// pngRowConverter converts one unfiltered row to RGBA like Pillow:
// low-depth grey is scaled, 16-bit grey clamps (I;16 -> L), 16-bit colour
// keeps the high byte, tRNS keys compare 8-bit pixels with the key's low
// byte, palette entries missing from PLTE are opaque black.
func pngRowConverter(info pngInfo, width int) func(dst, src []byte) {
	depth := int(info.depth)
	switch info.colorType {
	case 0:
		key := -1
		if info.hasTRNS {
			t := be16(info.transparency, 0)
			key = t & 0xff
			if depth == 1 {
				key = 0
				if t != 0 {
					key = 255
				}
			}
		}
		emit := func(dst []byte, x int, v byte) {
			alpha := byte(255)
			if int(v) == key {
				alpha = 0
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = v, v, v, alpha
		}
		switch depth {
		case 16:
			return func(dst, src []byte) {
				for x := range width {
					v := src[x*2+1]
					if src[x*2] != 0 {
						v = 255
					}
					emit(dst, x, v)
				}
			}
		case 8:
			return func(dst, src []byte) {
				for x := range width {
					emit(dst, x, src[x])
				}
			}
		default:
			scale := byte(255 / (1<<depth - 1))
			perByte := 8 / depth
			mask := byte(1<<depth - 1)
			return func(dst, src []byte) {
				for x := range width {
					shift := uint(8 - depth*(x%perByte+1))
					emit(dst, x, (src[x/perByte]>>shift&mask)*scale)
				}
			}
		}
	case 2:
		var key [3]int
		hasKey := info.hasTRNS
		if hasKey {
			key = [3]int{be16(info.transparency, 0) & 0xff, be16(info.transparency, 2) & 0xff, be16(info.transparency, 4) & 0xff}
		}
		step := 3 * depth / 8
		hi := 0 // offset of the high byte
		return func(dst, src []byte) {
			for x := range width {
				r, g, b := src[x*step+hi], src[x*step+hi+step/3], src[x*step+hi+2*step/3]
				alpha := byte(255)
				if hasKey && int(r) == key[0] && int(g) == key[1] && int(b) == key[2] {
					alpha = 0
				}
				dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = r, g, b, alpha
			}
		}
	case 3:
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
		perByte := 8 / depth
		mask := byte(1<<depth - 1)
		return func(dst, src []byte) {
			for x := range width {
				var index int
				if depth < 8 {
					index = int(src[x/perByte] >> uint(8-depth*(x%perByte+1)) & mask)
				} else {
					index = int(src[x])
				}
				copy(dst[x*4:x*4+4], table[index*4:index*4+4])
			}
		}
	case 4:
		step := 2 * depth / 8
		return func(dst, src []byte) {
			for x := range width {
				v, a := src[x*step], src[x*step+step/2]
				dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = v, v, v, a
			}
		}
	default: // 6
		if depth == 8 {
			return func(dst, src []byte) { copy(dst, src[:width*4]) }
		}
		return func(dst, src []byte) {
			for x := range width {
				dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = src[x*8], src[x*8+2], src[x*8+4], src[x*8+6]
			}
		}
	}
}
