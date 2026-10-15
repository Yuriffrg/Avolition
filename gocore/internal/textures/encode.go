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
	"unsafe"
)

// PNG encoding for preview data URIs. The image is split into horizontal
// strips that are filtered and deflated concurrently; each strip ends with a
// sync flush so the strips concatenate into one valid zlib stream (as pigz
// does), and each strip becomes its own IDAT chunk. Row filters are chosen
// with libpng's minimum-sum-of-absolute-differences heuristic, as Pillow and
// image/png do.
//
// CompressionLevel is compress/flate level 1 (BestSpeed). Pillow writes these
// previews with zlib level 3. Measured on a 2048x2048 RGB preview
// (BenchmarkCompressionLevel): level 1 encodes in about 0.08-0.12 s and
// 8.0 MB, levels 2, 3 and 6 take 1.7-1.9x longer for 8.0 MB, and Pillow
// takes about 0.66 s for 8.8 MB. Level 1 data URIs are 0.9-1.07x Pillow's
// size (BenchmarkLoad).
var CompressionLevel = flate.BestSpeed

const stripTarget = 256 * 1024 // filtered bytes per strip

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type stripEncoder struct {
	writer  *flate.Writer
	output  bytes.Buffer
	scratch [1][]byte // zero row for the first image row
}

var stripPool = sync.Pool{New: func() any {
	encoder := &stripEncoder{}
	encoder.writer, _ = flate.NewWriter(&encoder.output, CompressionLevel)
	return encoder
}}

func appendChunk(dst []byte, kind string, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	start := len(dst)
	dst = append(dst, kind...)
	dst = append(dst, data...)
	return binary.BigEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:]))
}

func abs8(v byte) int {
	if v < 128 {
		return int(v)
	}
	return 256 - int(v)
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := p-int(a), p-int(b), p-int(c)
	if pa < 0 {
		pa = -pa
	}
	if pb < 0 {
		pb = -pb
	}
	if pc < 0 {
		pc = -pc
	}
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
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
func (e *stripEncoder) filterRow(dst, row, previous []byte, bpp int) {
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
	for i := bpp; i < n; i++ {
		x, a, b, c := row[i], row[i-bpp], previous[i], previous[i-bpp]
		sNone += int(absTable[x])
		sSub += int(absTable[x-a])
		sUp += int(absTable[x-b])
		sAverage += int(absTable[x-byte((int(a)+int(b))/2)])
		sPaeth += int(absTable[x-paeth(a, b, c)])
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
		for i := bpp; i < n; i++ {
			out[i] = row[i] - paeth(row[i-bpp], previous[i], previous[i-bpp])
		}
	}
}

// EncodePNG encodes an RGB or RGBA image as a PNG file.
func EncodePNG(image *Image) []byte {
	stride := image.Width * image.Channels
	rowsPerStrip := max(1, stripTarget/(stride+1))
	strips := (image.Height + rowsPerStrip - 1) / rowsPerStrip
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
	jobs := make(chan int)
	workers := min(runtime.GOMAXPROCS(0), strips)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			encoder := stripPool.Get().(*stripEncoder)
			defer stripPool.Put(encoder)
			filtered := make([]byte, 0, rowsPerStrip*(stride+1))
			for strip := range jobs {
				y0 := strip * rowsPerStrip
				y1 := min(image.Height, y0+rowsPerStrip)
				filtered = filtered[:(y1-y0)*(stride+1)]
				for y := y0; y < y1; y++ {
					var previous []byte
					if y > 0 {
						previous = image.Pix[(y-1)*stride : y*stride]
					}
					encoder.filterRow(filtered[(y-y0)*(stride+1):(y-y0+1)*(stride+1)], image.Pix[y*stride:(y+1)*stride], previous, image.Channels)
				}
				encoder.output.Reset()
				encoder.writer.Reset(&encoder.output)
				encoder.writer.Write(filtered)
				if strip == strips-1 {
					encoder.writer.Close()
				} else {
					encoder.writer.Flush()
				}
				data := encoder.output.Bytes()
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
	binary.BigEndian.PutUint32(header[0:], uint32(image.Width))
	binary.BigEndian.PutUint32(header[4:], uint32(image.Height))
	header[8] = 8 // bit depth
	header[9] = 2 // truecolor
	if image.Channels == 4 {
		header[9] = 6 // truecolor with alpha
	}
	out = appendChunk(out, "IHDR", header[:])
	for _, r := range results {
		out = append(out, r.chunk...)
	}
	return appendChunk(out, "IEND", nil)
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
	encoded := EncodePNG(image)
	buffer := make([]byte, len(dataURIPrefix)+base64.StdEncoding.EncodedLen(len(encoded)))
	copy(buffer, dataURIPrefix)
	base64.StdEncoding.Encode(buffer[len(dataURIPrefix):], encoded)
	// The buffer is never modified again; avoid copying several megabytes.
	return unsafe.String(unsafe.SliceData(buffer), len(buffer))
}
