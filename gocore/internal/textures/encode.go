package textures

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// PNG encoding for preview data URIs. The image is split into horizontal
// strips that are filtered and deflated concurrently; each strip ends with a
// sync flush so the strips concatenate into one valid zlib stream (as pigz
// does), and each strip becomes its own IDAT chunk. Row filters are chosen
// with libpng's minimum-sum-of-absolute-differences heuristic, as Pillow and
// image/png do (previews score a sample of each row).
//
// Strips are compressed by deflate.go. CompressionLevel, when set (tests and
// benchmarks), uses compress/flate at that level instead. Pillow writes these
// previews with zlib level 3: on a 2048x2048 RGB preview it takes about
// 0.66 s for 8.8 MB, compress/flate level 1 about 0.08-0.12 s for 8.0 MB
// (levels 2, 3 and 6 are 1.7-1.9x slower for the same size).
var CompressionLevel = 0

// previewFilterSample is the filter heuristic's sampling step for previews.
var previewFilterSample = 12

const stripTarget = 256 * 1024 // filtered bytes per strip

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type stripEncoder struct {
	deflater deflater
	data     []byte
	writer   *flate.Writer // CompressionLevel != 0
	output   bytes.Buffer
	scratch  [1][]byte // zero row for the first image row
}

var stripPool = sync.Pool{New: func() any { return &stripEncoder{} }}

// compress deflates one strip; the result is valid until the next call.
func (e *stripEncoder) compress(filtered []byte, final bool) []byte {
	if CompressionLevel == 0 {
		e.data = e.deflater.compress(e.data[:0], filtered, final)
		return e.data
	}
	if e.writer == nil {
		e.writer, _ = flate.NewWriter(&e.output, CompressionLevel)
	}
	e.output.Reset()
	e.writer.Reset(&e.output)
	e.writer.Write(filtered)
	if final {
		e.writer.Close()
	} else {
		e.writer.Flush()
	}
	return e.output.Bytes()
}

func appendChunk(dst []byte, kind string, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	start := len(dst)
	dst = append(dst, kind...)
	dst = append(dst, data...)
	return binary.BigEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:]))
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs8(v byte) int {
	if v < 128 {
		return int(v)
	}
	return 256 - int(v)
}

func paeth(a, b, c byte) byte {
	return paethInt(int(a), int(b), int(c))
}

// paethInt is the PNG Paeth predictor, written so the compiler emits
// conditional moves: image data makes its branches unpredictable.
func paethInt(a, b, c int) byte {
	pa, pb := b-c, a-c
	pc := max(pa+pb, -pa-pb)
	pa, pb = max(pa, -pa), max(pb, -pb)
	if pb <= pc {
		c = b
	}
	if pa <= min(pb, pc) {
		c = a
	}
	return byte(c)
}

// absTable[v] is |int8(v)|, the libpng filter heuristic's cost of a byte.
var absTable = func() (table [256]uint16) {
	for i := range table {
		table[i] = uint16(abs8(byte(i)))
	}
	return
}()

// filterRow writes the filter type byte and filtered row into dst, choosing
// the filter with the smallest sum of absolute (signed) values. A nil
// previous row (the first row) is treated as zeros.
func (e *stripEncoder) filterRow(dst, row, previous []byte, bpp, sample int) {
	n := len(row)
	if previous == nil {
		if cap(e.scratch[0]) < n {
			e.scratch[0] = make([]byte, n)
		}
		previous = e.scratch[0][:n]
		clear(previous)
	}
	previous = previous[:n]
	var sNone, sSub, sUp, sAverage, sPaeth int
	for i := 0; i < bpp; i++ {
		x, b := row[i], previous[i]
		sNone += int(absTable[x])
		sSub += int(absTable[x])
		sUp += int(absTable[x-b])
		sAverage += int(absTable[x-b/2])
		sPaeth += int(absTable[x-b])
	}
	if sample <= 1 {
		for i := bpp; i < n; i++ {
			x, a, b, c := row[i], row[i-bpp], previous[i], previous[i-bpp]
			sNone += int(absTable[x])
			sSub += int(absTable[x-a])
			sUp += int(absTable[x-b])
			sAverage += int(absTable[x-byte((uint(a)+uint(b))>>1)])
			sPaeth += int(absTable[x-paeth(a, b, c)])
		}
	} else {
		// Score about one byte in sample, with a step prime to bpp so that
		// every channel is sampled.
		step := sample
		for gcd(step, bpp) != 1 {
			step++
		}
		for i := bpp; i < n; i += step {
			x, a, b, c := row[i], row[i-bpp], previous[i], previous[i-bpp]
			sNone += int(absTable[x])
			sSub += int(absTable[x-a])
			sUp += int(absTable[x-b])
			sAverage += int(absTable[x-byte((uint(a)+uint(b))>>1)])
			sPaeth += int(absTable[x-paeth(a, b, c)])
		}
	}
	best, sum := 0, sNone
	for f, candidate := range [5]int{sNone, sSub, sUp, sAverage, sPaeth} {
		if candidate < sum {
			best, sum = f, candidate
		}
	}
	dst[0] = byte(best)
	out := dst[1 : n+1]
	switch best {
	case 0:
		copy(out, row)
	case 1:
		copy(out[:bpp], row[:bpp])
		for i := bpp; i < n; i++ {
			out[i] = row[i] - row[i-bpp]
		}
	case 2:
		for i := range out {
			out[i] = row[i] - previous[i]
		}
	case 3:
		for i := 0; i < bpp; i++ {
			out[i] = row[i] - previous[i]/2
		}
		for i := bpp; i < n; i++ {
			out[i] = row[i] - byte((int(row[i-bpp])+int(previous[i]))/2)
		}
	case 4:
		for i := 0; i < bpp; i++ {
			out[i] = row[i] - previous[i]
		}
		// Same-length slices so the loop runs without bounds checks.
		x, o := row[bpp:n], out[bpp:n]
		a, b, c := row[:len(x)], previous[bpp:n], previous[:len(x)]
		for i := range x {
			o[i] = x[i] - paethInt(int(a[i]), int(b[i]), int(c[i]))
		}
	}
}

// EncodePNG encodes an RGB or RGBA image as a PNG file, choosing each row's
// filter from every byte (exported files are kept as small as before).
func EncodePNG(image *Image) []byte {
	data, _ := encodePNG(imageRows(image, 1))
	return data
}

// rowSource is what encodePNG compresses: an image of width x height pixels
// with channels bytes each, produced row by row.
type rowSource struct {
	width, height, channels int
	// row returns output row y. scratch has room for one row; row may fill
	// and return it or return memory it owns.
	row func(y int, scratch []byte) []byte
	// wait, when set, blocks until output rows [0, n) can be produced and
	// reports false when they never will (the source failed).
	wait func(n int) bool
	// sample is the filter heuristic's sampling step: 1 scores every byte
	// (libpng), n > 1 scores one pixel in n. Sampling picks the same filter
	// for almost every row at a fraction of the cost.
	sample int
}

func imageRows(image *Image, sample int) rowSource {
	stride := image.Width * image.Channels
	return rowSource{
		width: image.Width, height: image.Height, channels: image.Channels, sample: sample,
		row: func(y int, _ []byte) []byte { return image.Pix[y*stride : (y+1)*stride] },
	}
}

// encodePNG encodes source; it returns false when source.wait fails.
func encodePNG(source rowSource) ([]byte, bool) {
	stride := source.width * source.channels
	height := source.height
	rowsPerStrip := max(1, stripTarget/(stride+1))
	strips := (height + rowsPerStrip - 1) / rowsPerStrip
	if strips == 0 {
		strips = 1
	}
	type result struct {
		data  []byte
		sum   uint32 // adler32 of the strip's filtered bytes
		size  int
		chunk []byte
	}
	results := make([]result, strips)
	var wg sync.WaitGroup
	var failed atomic.Bool
	jobs := make(chan int)
	workers := min(runtime.GOMAXPROCS(0), strips)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			encoder := stripPool.Get().(*stripEncoder)
			defer stripPool.Put(encoder)
			filtered := make([]byte, 0, rowsPerStrip*(stride+1))
			scratch := make([]byte, 2*stride)
			for strip := range jobs {
				if failed.Load() {
					continue
				}
				y0 := strip * rowsPerStrip
				y1 := min(height, y0+rowsPerStrip)
				if source.wait != nil && !source.wait(y1) {
					failed.Store(true)
					continue
				}
				filtered = filtered[:(y1-y0)*(stride+1)]
				spare, other := scratch[:stride], scratch[stride:]
				var previous []byte
				if y0 > 0 {
					previous = source.row(y0-1, spare)
					spare, other = other, spare
				}
				for y := y0; y < y1; y++ {
					current := source.row(y, spare)
					encoder.filterRow(filtered[(y-y0)*(stride+1):(y-y0+1)*(stride+1)], current, previous, source.channels, source.sample)
					previous = current
					spare, other = other, spare
				}
				data := encoder.compress(filtered, strip == strips-1)
				// Build the whole IDAT chunk here so its CRC is computed in parallel.
				chunk := make([]byte, 0, len(data)+18)
				payload := data
				if strip == 0 {
					payload = append([]byte{0x78, 0x01}, data...) // zlib header, fastest-level hint
				}
				results[strip] = result{sum: adler32.Checksum(filtered), size: len(filtered), chunk: chunk}
				if strip == strips-1 {
					results[strip].data = append(append([]byte(nil), payload...), 0, 0, 0, 0) // adler32 placeholder
				} else {
					results[strip].chunk = appendChunk(chunk, "IDAT", payload)
				}
			}
		}()
	}
	for strip := range strips {
		jobs <- strip
	}
	close(jobs)
	wg.Wait()
	if failed.Load() {
		return nil, false
	}

	checksum := results[0].sum
	for _, r := range results[1:] {
		checksum = adler32Combine(checksum, r.sum, r.size)
	}
	last := &results[strips-1]
	binary.BigEndian.PutUint32(last.data[len(last.data)-4:], checksum)
	last.chunk = appendChunk(last.chunk, "IDAT", last.data)

	total := len(pngSignature) + 25 + 12
	for _, r := range results {
		total += len(r.chunk)
	}
	out := make([]byte, 0, total)
	out = append(out, pngSignature...)
	var header [13]byte
	binary.BigEndian.PutUint32(header[0:], uint32(source.width))
	binary.BigEndian.PutUint32(header[4:], uint32(source.height))
	header[8] = 8 // bit depth
	header[9] = 2 // truecolor
	if source.channels == 4 {
		header[9] = 6 // truecolor with alpha
	}
	out = appendChunk(out, "IHDR", header[:])
	for _, r := range results {
		out = append(out, r.chunk...)
	}
	return appendChunk(out, "IEND", nil), true
}

// adler32Combine is zlib's adler32_combine.
func adler32Combine(adler1, adler2 uint32, length2 int) uint32 {
	const base = 65521
	rem := uint32(length2 % base)
	sum1 := adler1 & 0xffff
	sum2 := (rem * sum1) % base
	sum1 += (adler2 & 0xffff) + base - 1
	sum2 += (adler1 >> 16) + (adler2 >> 16) + base - rem
	if sum1 >= base {
		sum1 -= base
	}
	if sum1 >= base {
		sum1 -= base
	}
	if sum2 >= base<<1 {
		sum2 -= base << 1
	}
	if sum2 >= base {
		sum2 -= base
	}
	return sum1 | sum2<<16
}

const dataURIPrefix = "data:image/png;base64,"

// DataURI encodes the image as "data:image/png;base64,...".
func DataURI(image *Image) string {
	uri, _ := dataURI(imageRows(image, 1))
	return uri
}

// dataURI encodes source as a PNG data URI; false when source.wait fails.
func dataURI(source rowSource) (string, bool) {
	encoded, ok := encodePNG(source)
	if !ok {
		return "", false
	}
	buffer := make([]byte, len(dataURIPrefix)+base64.StdEncoding.EncodedLen(len(encoded)))
	copy(buffer, dataURIPrefix)
	base64.StdEncoding.Encode(buffer[len(dataURIPrefix):], encoded)
	// The buffer is never modified again; avoid copying several megabytes.
	return unsafe.String(unsafe.SliceData(buffer), len(buffer)), true
}
