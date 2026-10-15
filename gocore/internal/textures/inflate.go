package textures

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// A DEFLATE decoder (RFC 1951) that writes into a preallocated buffer of the
// known decompressed size, used for PNG image data. compress/flate decodes
// through a sliding window and a bufio reader and is about twice as slow as
// zlib; here the output buffer is the window, the bit buffer is refilled 8
// bytes at a time and Huffman codes are decoded with an 11-bit (literal/
// length) or 9-bit (distance) lookup table. Like zlib inside Pillow,
// decoding stops once the output is full; trailing data is ignored.

var errCorrupt = errors.New("image file is truncated or corrupt (invalid deflate data)")

const (
	litBits  = 11
	distBits = 9
)

// huffman is a canonical code: table entries are symbol<<4 | length for codes
// up to the table width; longer codes fall back to canonical decoding.
type huffman struct {
	table    []uint32
	tableLen uint
	counts   [16]uint16
	symbols  []uint16 // sorted by code
	maxLen   uint
}

func (h *huffman) build(lengths []uint8, tableBits uint) error {
	h.counts = [16]uint16{}
	for _, l := range lengths {
		h.counts[l]++
	}
	h.counts[0] = 0
	left := 1
	for l := 1; l < 16; l++ {
		left <<= 1
		left -= int(h.counts[l])
		if left < 0 {
			return errCorrupt // over-subscribed
		}
	}
	var offsets [16]uint16
	for l := 1; l < 15; l++ {
		offsets[l+1] = offsets[l] + h.counts[l]
	}
	if cap(h.symbols) < len(lengths) {
		h.symbols = make([]uint16, len(lengths))
	}
	h.symbols = h.symbols[:len(lengths)]
	h.maxLen = 0
	for symbol, l := range lengths {
		if l != 0 {
			h.symbols[offsets[l]] = uint16(symbol)
			offsets[l]++
			h.maxLen = max(h.maxLen, uint(l))
		}
	}
	size := 1 << tableBits
	if cap(h.table) < size {
		h.table = make([]uint32, size)
	}
	h.table = h.table[:size]
	clear(h.table)
	h.tableLen = tableBits
	// Assign canonical codes and fill the table with bit-reversed codes.
	code := 0
	index := 0
	for l := uint(1); l <= tableBits && l < 16; l++ {
		for i := 0; i < int(h.counts[l]); i++ {
			symbol := h.symbols[index]
			index++
			reversed := int(bits.Reverse16(uint16(code)) >> (16 - l))
			for j := reversed; j < size; j += 1 << l {
				h.table[j] = uint32(symbol)<<4 | uint32(l)
			}
			code++
		}
		code <<= 1
	}
	return nil
}

type inflater struct {
	in      []byte
	pos     int
	bits    uint64
	nbits   uint
	out     []byte
	outPos  int
	lit     huffman
	dist    huffman
	codeLen huffman
	// progress, when set, is called with outPos about every reportStep bytes.
	progress   func(int)
	nextReport int
}

const reportStep = 256 * 1024

func (f *inflater) refill() {
	if f.pos+8 <= len(f.in) {
		f.bits |= binary.LittleEndian.Uint64(f.in[f.pos:]) << f.nbits
		consumed := (63 - f.nbits) >> 3
		f.pos += int(consumed)
		f.nbits |= 56
		return
	}
	for f.nbits <= 56 && f.pos < len(f.in) {
		f.bits |= uint64(f.in[f.pos]) << f.nbits
		f.pos++
		f.nbits += 8
	}
}

func (f *inflater) need(n uint) error {
	if f.nbits < n {
		f.refill()
		if f.nbits < n {
			return errCorrupt
		}
	}
	return nil
}

func (f *inflater) take(n uint) (uint32, error) {
	if err := f.need(n); err != nil {
		return 0, err
	}
	v := uint32(f.bits & (1<<n - 1))
	f.bits >>= n
	f.nbits -= n
	return v, nil
}

// decode reads one symbol. The bit buffer must hold at least 15 bits unless
// the input is nearly exhausted.
func (f *inflater) decode(h *huffman) (int, error) {
	if f.nbits < 15 {
		f.refill()
	}
	entry := h.table[f.bits&(1<<h.tableLen-1)]
	if l := uint(entry & 15); l != 0 && l <= f.nbits {
		f.bits >>= l
		f.nbits -= l
		return int(entry >> 4), nil
	}
	// Canonical decoding, one bit at a time (long codes only).
	code, first, index := 0, 0, 0
	for l := uint(1); l < 16; l++ {
		if f.nbits == 0 {
			return 0, errCorrupt
		}
		code |= int(f.bits & 1)
		f.bits >>= 1
		f.nbits--
		count := int(h.counts[l])
		if code-count < first {
			return int(h.symbols[index+(code-first)]), nil
		}
		index += count
		first += count
		first <<= 1
		code <<= 1
	}
	return 0, errCorrupt
}

var (
	lengthBase  = [29]uint16{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	lengthExtra = [29]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	distBase    = [30]uint32{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	distExtra   = [30]uint8{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}

	fixedLit, fixedDist huffman
)

func init() {
	var lengths [288]uint8
	for i := range lengths {
		switch {
		case i < 144:
			lengths[i] = 8
		case i < 256:
			lengths[i] = 9
		case i < 280:
			lengths[i] = 7
		default:
			lengths[i] = 8
		}
	}
	fixedLit.build(lengths[:], litBits)
	var distances [30]uint8
	for i := range distances {
		distances[i] = 5
	}
	fixedDist.build(distances[:], distBits)
}

var codeLengthOrder = [19]int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}

// inflate decodes a zlib stream (2-byte header) into out, returning the
// number of bytes written. It stops early, without error, when out is full.
func inflateZlib(in []byte, out []byte, progress func(int)) (int, error) {
	if len(in) < 2 || in[0]&0x0f != 8 || (uint(in[0])<<8|uint(in[1]))%31 != 0 || in[1]&0x20 != 0 {
		return 0, errCorrupt
	}
	f := &inflater{in: in, pos: 2, out: out, progress: progress, nextReport: reportStep}
	err := f.run()
	return f.outPos, err
}

func (f *inflater) run() error {
	for {
		if f.outPos == len(f.out) {
			return nil
		}
		header, err := f.take(3)
		if err != nil {
			return err
		}
		final := header&1 != 0
		switch header >> 1 {
		case 0:
			err = f.stored()
		case 1:
			err = f.codes(&fixedLit, &fixedDist)
		case 2:
			if err = f.dynamic(); err == nil {
				err = f.codes(&f.lit, &f.dist)
			}
		default:
			err = errCorrupt
		}
		if err != nil {
			return err
		}
		if final {
			return nil
		}
	}
}

func (f *inflater) stored() error {
	// Drop to a byte boundary and return whole unread bytes to the input.
	f.bits >>= f.nbits & 7
	f.nbits -= f.nbits & 7
	f.pos -= int(f.nbits >> 3)
	f.bits, f.nbits = 0, 0
	if f.pos+4 > len(f.in) {
		return errCorrupt
	}
	length := int(binary.LittleEndian.Uint16(f.in[f.pos:]))
	if ^uint16(length) != binary.LittleEndian.Uint16(f.in[f.pos+2:]) {
		return errCorrupt
	}
	f.pos += 4
	n := min(length, len(f.in)-f.pos, len(f.out)-f.outPos)
	copy(f.out[f.outPos:], f.in[f.pos:f.pos+n])
	f.outPos += n
	f.pos += n
	if n < length && f.outPos < len(f.out) {
		return errCorrupt
	}
	return nil
}

func (f *inflater) dynamic() error {
	header, err := f.take(14)
	if err != nil {
		return err
	}
	nlit, ndist, nclen := int(header&31)+257, int(header>>5&31)+1, int(header>>10)+4
	if nlit > 286 || ndist > 30 {
		return errCorrupt
	}
	var clens [19]uint8
	for i := range nclen {
		v, err := f.take(3)
		if err != nil {
			return err
		}
		clens[codeLengthOrder[i]] = uint8(v)
	}
	if err := f.codeLen.build(clens[:], 7); err != nil {
		return err
	}
	var lengths [286 + 30]uint8
	for i := 0; i < nlit+ndist; {
		symbol, err := f.decode(&f.codeLen)
		if err != nil {
			return err
		}
		if symbol < 16 {
			lengths[i] = uint8(symbol)
			i++
			continue
		}
		var repeat int
		var value uint8
		switch symbol {
		case 16:
			if i == 0 {
				return errCorrupt
			}
			v, err := f.take(2)
			if err != nil {
				return err
			}
			repeat, value = 3+int(v), lengths[i-1]
		case 17:
			v, err := f.take(3)
			if err != nil {
				return err
			}
			repeat = 3 + int(v)
		default:
			v, err := f.take(7)
			if err != nil {
				return err
			}
			repeat = 11 + int(v)
		}
		if i+repeat > nlit+ndist {
			return errCorrupt
		}
		for ; repeat > 0; repeat-- {
			lengths[i] = value
			i++
		}
	}
	if lengths[256] == 0 {
		return errCorrupt
	}
	if err := f.lit.build(lengths[:nlit], litBits); err != nil {
		return err
	}
	return f.dist.build(lengths[nlit:nlit+ndist], distBits)
}

// fastCodes decodes while at least 8 input bytes and 258 output bytes
// remain, keeping the decoder state in registers. It returns done at the end
// of the block, or ok=false to let codes handle one symbol the slow way.
func (f *inflater) fastCodes(lit, dist *huffman) (done bool, err error) {
	var report int
	in, out := f.in, f.out
	bitBuffer, nbits, pos, outPos := f.bits, f.nbits, f.pos, f.outPos
	litTable, distTable := lit.table, dist.table
	litMask, distMask := uint64(1)<<lit.tableLen-1, uint64(1)<<dist.tableLen-1
	report = f.nextReport
	for pos+8 <= len(in) && outPos+258 <= len(out) {
		bitBuffer |= binary.LittleEndian.Uint64(in[pos:]) << nbits
		pos += int((63 - nbits) >> 3)
		nbits |= 56
		entry := litTable[bitBuffer&litMask]
		l := uint(entry & 15)
		if l == 0 {
			goto exit
		}
		symbol := int(entry >> 4)
		if symbol < 256 {
			bitBuffer >>= l
			nbits -= l
			out[outPos] = byte(symbol)
			outPos++
			continue
		}
		if symbol == 256 {
			bitBuffer >>= l
			nbits -= l
			done = true
			goto exit
		}
		symbol -= 257
		if symbol >= 29 {
			err = errCorrupt
			goto exit
		}
		// Peek the distance code before consuming anything, so a long code
		// can still be handled by the slow path from a clean state.
		extra := uint(lengthExtra[symbol])
		lengthBits := l + extra
		distEntry := distTable[(bitBuffer>>lengthBits)&distMask]
		dl := uint(distEntry & 15)
		if dl == 0 {
			goto exit
		}
		length := int(lengthBase[symbol]) + int((bitBuffer>>l)&(1<<extra-1))
		bitBuffer >>= lengthBits + dl
		nbits -= lengthBits + dl
		symbol = int(distEntry >> 4)
		if symbol >= 30 {
			err = errCorrupt
			goto exit
		}
		extra = uint(distExtra[symbol])
		distance := int(distBase[symbol]) + int(bitBuffer&(1<<extra-1))
		bitBuffer >>= extra
		nbits -= extra
		if distance > outPos {
			err = errCorrupt
			goto exit
		}
		from := outPos - distance
		if distance >= length {
			copy(out[outPos:outPos+length], out[from:from+length])
		} else {
			for i := range length {
				out[outPos+i] = out[from+i]
			}
		}
		outPos += length
		if outPos >= report {
			report = f.report(outPos)
		}
	}
exit:
	f.bits, f.nbits, f.pos, f.outPos = bitBuffer, nbits, pos, outPos
	return done, err
}

// report calls progress and returns the next reporting threshold.
func (f *inflater) report(outPos int) int {
	if f.progress != nil {
		f.progress(outPos)
	}
	f.nextReport = outPos + reportStep
	return f.nextReport
}

func (f *inflater) codes(lit, dist *huffman) error {
	out := f.out
	for {
		if done, err := f.fastCodes(lit, dist); done || err != nil {
			return err
		}
		symbol, err := f.decode(lit)
		if err != nil {
			return err
		}
		if symbol < 256 {
			if f.outPos >= len(out) {
				return nil
			}
			out[f.outPos] = byte(symbol)
			f.outPos++
			continue
		}
		if symbol == 256 {
			return nil
		}
		symbol -= 257
		if symbol >= 29 {
			return errCorrupt
		}
		length := int(lengthBase[symbol])
		if extra := uint(lengthExtra[symbol]); extra != 0 {
			v, err := f.take(extra)
			if err != nil {
				return err
			}
			length += int(v)
		}
		symbol, err = f.decode(dist)
		if err != nil {
			return err
		}
		if symbol >= 30 {
			return errCorrupt
		}
		distance := int(distBase[symbol])
		if extra := uint(distExtra[symbol]); extra != 0 {
			v, err := f.take(extra)
			if err != nil {
				return err
			}
			distance += int(v)
		}
		if distance > f.outPos {
			return errCorrupt
		}
		full := false
		if remaining := len(out) - f.outPos; length > remaining {
			length, full = remaining, true
		}
		from := f.outPos - distance
		if distance >= length {
			copy(out[f.outPos:f.outPos+length], out[from:from+length])
		} else {
			for i := range length {
				out[f.outPos+i] = out[from+i]
			}
		}
		f.outPos += length
		if full {
			return nil
		}
		if f.outPos >= f.nextReport && f.progress != nil {
			f.progress(f.outPos)
			f.nextReport = f.outPos + reportStep
		}
	}
}
