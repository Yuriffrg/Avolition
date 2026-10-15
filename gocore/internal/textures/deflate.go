package textures

import (
	"encoding/binary"
	"math/bits"
	"slices"
)

// A DEFLATE encoder (RFC 1951) for the filtered rows of one PNG strip,
// about twice as fast as compress/flate's BestSpeed at a similar size.
// Matches are found greedily with a one-entry hash table of 4-byte prefixes
// (like compress/flate's level 1 and Snappy), skipping ahead on data that
// does not compress. Each block of up to 64 KiB of input is written with
// its own dynamic Huffman codes, or stored when that is smaller. Huffman
// code lengths are built with Moffat and Katajainen's in-place algorithm and
// limited to 15 bits as miniz does.
//
// compress appends whole strips: a strip that is not the last ends with an
// empty stored block (a zlib sync flush), so the strips of an image
// concatenate into one valid stream.

const (
	deflateHashBits = 14
	deflateWindow   = 32768
	deflateMinMatch = 4
	deflateMaxMatch = 258
	deflateBlock    = 65535 // input bytes per block; also the stored block limit
)

type deflater struct {
	table    [1 << deflateHashBits]int32 // last position + 1 per hash
	tokens   []uint32                    // literal run length, or matchFlag | length<<16 | distance
	litFreq  [286]uint32
	distFreq [30]uint32
	litLen   [286]uint8
	distLen  [30]uint8
	litCode  [286]uint16 // bit-reversed canonical codes
	distCode [30]uint16
	clFreq   [19]uint32
	clLen    [19]uint8
	clCode   [19]uint16
	lengths  []uint8 // literal/length then distance code lengths
	rle      []uint8 // code length symbols; repeat counts follow 16, 17, 18
	symbols  []symbolFreq
	out      bitWriter
}

const matchFlag = 1 << 31

type symbolFreq struct {
	key    uint32 // frequency, then code length
	symbol uint16
}

type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

// write appends the low n bits of v (n <= 32, v has no higher bits).
func (w *bitWriter) write(v uint64, n uint) {
	w.acc |= v << w.n
	w.n += n
	if w.n >= 32 {
		w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(w.acc))
		w.acc >>= 32
		w.n -= 32
	}
}

// align pads with zero bits to a byte boundary and flushes.
func (w *bitWriter) align() {
	for w.n > 0 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.n -= min(8, w.n)
	}
	w.acc = 0
}

// Length symbols 257..285 and distance codes, as in RFC 1951 3.2.5 (the
// bases and extra bits are shared with inflate.go).
var (
	lengthSymbol [deflateMaxMatch + 1]uint8 // length -> symbol - 257
	distSymbol   [512]uint8                 // (distance-1) < 256: [distance-1]; else [256 + (distance-1)>>7]
	clOrder      = [19]uint8{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}
)

func init() {
	for code := range 29 {
		next := deflateMaxMatch + 1
		if code < 28 {
			next = int(lengthBase[code+1])
		}
		for length := int(lengthBase[code]); length < next && length <= deflateMaxMatch; length++ {
			lengthSymbol[length] = uint8(code)
		}
	}
	lengthSymbol[deflateMaxMatch] = 28
	for code := range 30 {
		for d := int(distBase[code]); d < int(distBase[code])+1<<distExtra[code]; d++ {
			if d <= 256 {
				distSymbol[d-1] = uint8(code)
			} else {
				distSymbol[256+(d-1)>>7] = uint8(code)
			}
		}
	}
}

func distanceSymbol(distance int) int {
	if distance <= 256 {
		return int(distSymbol[distance-1])
	}
	return int(distSymbol[256+(distance-1)>>7])
}

// compress appends src as DEFLATE blocks to dst; final ends the stream,
// otherwise the output ends byte-aligned with a sync flush.
func (d *deflater) compress(dst, src []byte, final bool) []byte {
	clear(d.table[:])
	d.out = bitWriter{buf: dst}
	for start := 0; start < len(src); start += deflateBlock {
		end := min(len(src), start+deflateBlock)
		d.tokenize(src, start, end)
		d.writeBlock(src[start:end], final && end == len(src))
	}
	if len(src) == 0 && final {
		d.out.write(1|1<<1, 3) // final block, fixed Huffman codes
		d.out.write(0, 7)      // end of block
	}
	if !final {
		d.out.write(0, 3) // empty stored block
		d.out.align()
		d.out.buf = append(d.out.buf, 0, 0, 0xff, 0xff)
	}
	d.out.align()
	return d.out.buf
}

func hash4(v uint32) uint32 { return (v * 0x1e35a7bd) >> (32 - deflateHashBits) }

// tokenize finds matches for src[start:end]; matches stay inside it but may
// refer back to earlier bytes of src.
func (d *deflater) tokenize(src []byte, start, end int) {
	d.tokens = d.tokens[:0]
	d.litFreq = [286]uint32{}
	d.distFreq = [30]uint32{}
	misses := 0
	i, literals := start, start // literals: start of the pending literal run
	for i+deflateMinMatch <= end {
		x := binary.LittleEndian.Uint32(src[i:])
		h := hash4(x)
		candidate := int(d.table[h]) - 1
		d.table[h] = int32(i + 1)
		if candidate >= 0 && i-candidate <= deflateWindow && binary.LittleEndian.Uint32(src[candidate:]) == x {
			d.literalRun(src[literals:i])
			length := deflateMinMatch + matchLength(src[candidate+deflateMinMatch:], src[i+deflateMinMatch:min(end, i+deflateMaxMatch)])
			distance := i - candidate
			d.tokens = append(d.tokens, matchFlag|uint32(length)<<16|uint32(distance))
			d.litFreq[257+int(lengthSymbol[length])]++
			d.distFreq[distanceSymbol(distance)]++
			// Index the match's last position so a run continues cheaply.
			if last := i + length - 1; last+deflateMinMatch <= end && length > deflateMinMatch {
				d.table[hash4(binary.LittleEndian.Uint32(src[last:]))] = int32(last + 1)
			}
			i += length
			literals = i
			misses = 0
			continue
		}
		// Skip ahead faster through data that does not compress.
		i += 1 + misses>>5
		misses++
	}
	d.literalRun(src[literals:end])
	d.litFreq[256] = 1 // end of block
}

// literalRun records the literal bytes run as tokens of up to 1<<16 - 1
// bytes (the bytes themselves are read back from the block when written).
func (d *deflater) literalRun(run []byte) {
	for len(run) > 0 {
		n := min(len(run), 1<<16-1)
		d.tokens = append(d.tokens, uint32(n))
		for _, b := range run[:n] {
			d.litFreq[b]++
		}
		run = run[n:]
	}
}

// matchLength is the length of the common prefix of a and b (len(b) <= len(a)).
func matchLength(a, b []byte) int {
	n := 0
	for len(b)-n >= 8 {
		x := binary.LittleEndian.Uint64(a[n:]) ^ binary.LittleEndian.Uint64(b[n:])
		if x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
		n += 8
	}
	for n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func (d *deflater) writeBlock(src []byte, final bool) {
	var last uint64
	if final {
		last = 1
	}
	d.symbols = buildLengths(d.litFreq[:], d.litLen[:], 15, d.symbols)
	d.symbols = buildLengths(d.distFreq[:], d.distLen[:], 15, d.symbols)
	numLit, numDist := 286, 30
	for numLit > 257 && d.litLen[numLit-1] == 0 {
		numLit--
	}
	for numDist > 1 && d.distLen[numDist-1] == 0 {
		numDist--
	}
	d.lengths = append(append(d.lengths[:0], d.litLen[:numLit]...), d.distLen[:numDist]...)
	d.encodeLengths()
	d.symbols = buildLengths(d.clFreq[:], d.clLen[:], 7, d.symbols)
	numCL := 19
	for numCL > 4 && d.clLen[clOrder[numCL-1]] == 0 {
		numCL--
	}

	size := 3 + 5 + 5 + 4 + 3*numCL
	for symbol, freq := range d.clFreq {
		size += int(freq) * int(d.clLen[symbol])
	}
	size += int(d.clFreq[16])*2 + int(d.clFreq[17])*3 + int(d.clFreq[18])*7
	for symbol, freq := range d.litFreq {
		size += int(freq) * int(d.litLen[symbol])
	}
	for code, extra := range lengthExtra {
		size += int(d.litFreq[257+code]) * int(extra)
	}
	for code, freq := range d.distFreq {
		size += int(freq) * (int(d.distLen[code]) + int(distExtra[code]))
	}
	if stored := 3 + 7 + 32 + 8*len(src); stored <= size {
		d.out.write(last, 3) // stored block
		d.out.align()
		d.out.buf = binary.LittleEndian.AppendUint16(d.out.buf, uint16(len(src)))
		d.out.buf = binary.LittleEndian.AppendUint16(d.out.buf, ^uint16(len(src)))
		d.out.buf = append(d.out.buf, src...)
		return
	}

	canonicalCodes(d.litLen[:], d.litCode[:])
	canonicalCodes(d.distLen[:], d.distCode[:])
	canonicalCodes(d.clLen[:], d.clCode[:])
	w := &d.out
	w.write(last|2<<1, 3) // dynamic Huffman codes
	w.write(uint64(numLit-257), 5)
	w.write(uint64(numDist-1), 5)
	w.write(uint64(numCL-4), 4)
	for _, symbol := range clOrder[:numCL] {
		w.write(uint64(d.clLen[symbol]), 3)
	}
	for i := 0; i < len(d.rle); i++ {
		symbol := d.rle[i]
		w.write(uint64(d.clCode[symbol]), uint(d.clLen[symbol]))
		switch symbol {
		case 16:
			i++
			w.write(uint64(d.rle[i]), 2)
		case 17:
			i++
			w.write(uint64(d.rle[i]), 3)
		case 18:
			i++
			w.write(uint64(d.rle[i]), 7)
		}
	}
	position := 0
	for _, token := range d.tokens {
		if token&matchFlag == 0 {
			run := src[position : position+int(token)]
			position += len(run)
			// Two literal codes (at most 30 bits) per write.
			for len(run) >= 2 {
				a, b := run[0], run[1]
				w.write(uint64(d.litCode[a])|uint64(d.litCode[b])<<d.litLen[a], uint(d.litLen[a])+uint(d.litLen[b]))
				run = run[2:]
			}
			if len(run) == 1 {
				w.write(uint64(d.litCode[run[0]]), uint(d.litLen[run[0]]))
			}
			continue
		}
		length, distance := int(token>>16&0x1ff), int(token&0xffff)
		position += length
		code := int(lengthSymbol[length])
		symbol := 257 + code
		w.write(uint64(d.litCode[symbol])|uint64(length-int(lengthBase[code]))<<d.litLen[symbol], uint(d.litLen[symbol])+uint(lengthExtra[code]))
		dcode := distanceSymbol(distance)
		w.write(uint64(d.distCode[dcode])|uint64(distance-int(distBase[dcode]))<<d.distLen[dcode], uint(d.distLen[dcode])+uint(distExtra[dcode]))
	}
	w.write(uint64(d.litCode[256]), uint(d.litLen[256]))
}

// encodeLengths run-length encodes d.lengths into d.rle with symbols 16
// (repeat the previous length 3-6 times), 17 (3-10 zeros) and 18 (11-138
// zeros), counting the symbols in d.clFreq.
func (d *deflater) encodeLengths() {
	d.rle = d.rle[:0]
	d.clFreq = [19]uint32{}
	lengths := d.lengths
	for i := 0; i < len(lengths); {
		value := lengths[i]
		run := 1
		for i+run < len(lengths) && lengths[i+run] == value {
			run++
		}
		i += run
		if value == 0 {
			for run >= 11 {
				n := min(run, 138)
				d.rle = append(d.rle, 18, uint8(n-11))
				d.clFreq[18]++
				run -= n
			}
			if run >= 3 {
				d.rle = append(d.rle, 17, uint8(run-3))
				d.clFreq[17]++
				run = 0
			}
		} else {
			d.rle = append(d.rle, value)
			d.clFreq[value]++
			run--
			for run >= 3 {
				n := min(run, 6)
				d.rle = append(d.rle, 16, uint8(n-3))
				d.clFreq[16]++
				run -= n
			}
		}
		for ; run > 0; run-- {
			d.rle = append(d.rle, value)
			d.clFreq[value]++
		}
	}
}

// buildLengths sets lengths[s] to the Huffman code length of each symbol
// with a non-zero frequency (0 otherwise), at most maxBits. At least two
// symbols get a code so that every code is complete.
func buildLengths(freq []uint32, lengths []uint8, maxBits int, symbols []symbolFreq) []symbolFreq {
	symbols = symbols[:0]
	for s, f := range freq {
		if f != 0 {
			symbols = append(symbols, symbolFreq{f, uint16(s)})
		}
	}
	clear(lengths)
	for s := 0; len(symbols) < 2; s++ {
		if freq[s] == 0 {
			symbols = append(symbols, symbolFreq{1, uint16(s)})
		}
	}
	slices.SortFunc(symbols, func(a, b symbolFreq) int {
		if a.key != b.key {
			if a.key < b.key {
				return -1
			}
			return 1
		}
		return int(a.symbol) - int(b.symbol)
	})
	minimumRedundancy(symbols)
	// Count codes per length and limit them to maxBits (miniz's
	// tdefl_huffman_enforce_max_code_size).
	var counts [320]int
	for _, s := range symbols {
		counts[min(int(s.key), len(counts)-1)]++
	}
	for length := maxBits + 1; length < len(counts); length++ {
		counts[maxBits] += counts[length]
	}
	total := uint32(0)
	for length := maxBits; length > 0; length-- {
		total += uint32(counts[length]) << (maxBits - length)
	}
	for total != 1<<maxBits {
		counts[maxBits]--
		for length := maxBits - 1; length > 0; length-- {
			if counts[length] != 0 {
				counts[length]--
				counts[length+1] += 2
				break
			}
		}
		total--
	}
	// The most frequent symbols (at the end) get the shortest codes.
	j := len(symbols)
	for length := 1; length <= maxBits; length++ {
		for n := counts[length]; n > 0; n-- {
			j--
			lengths[symbols[j].symbol] = uint8(length)
		}
	}
	return symbols
}

// minimumRedundancy replaces the frequencies of symbols (sorted ascending)
// by optimal code lengths (Moffat and Katajainen, as in miniz).
func minimumRedundancy(a []symbolFreq) {
	n := len(a)
	if n == 0 {
		return
	}
	if n == 1 {
		a[0].key = 1
		return
	}
	a[0].key += a[1].key
	root, leaf := 0, 2
	for next := 1; next < n-1; next++ {
		if leaf >= n || a[root].key < a[leaf].key {
			a[next].key = a[root].key
			a[root].key = uint32(next)
			root++
		} else {
			a[next].key = a[leaf].key
			leaf++
		}
		if leaf >= n || (root < next && a[root].key < a[leaf].key) {
			a[next].key += a[root].key
			a[root].key = uint32(next)
			root++
		} else {
			a[next].key += a[leaf].key
			leaf++
		}
	}
	a[n-2].key = 0
	for next := n - 3; next >= 0; next-- {
		a[next].key = a[a[next].key].key + 1
	}
	avbl, used, depth := 1, 0, 0
	root, next := n-2, n-1
	for avbl > 0 {
		for root >= 0 && int(a[root].key) == depth {
			used++
			root--
		}
		for avbl > used {
			a[next].key = uint32(depth)
			next--
			avbl--
		}
		avbl = 2 * used
		depth++
		used = 0
	}
}

// canonicalCodes assigns RFC 1951 canonical codes, bit-reversed for
// LSB-first output.
func canonicalCodes(lengths []uint8, codes []uint16) {
	var count [16]uint16
	for _, l := range lengths {
		count[l]++
	}
	count[0] = 0
	var next [16]uint16
	code := uint16(0)
	for l := 1; l < 16; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	for s, l := range lengths {
		if l != 0 {
			codes[s] = bits.Reverse16(next[l]) >> (16 - l)
			next[l]++
		}
	}
}
