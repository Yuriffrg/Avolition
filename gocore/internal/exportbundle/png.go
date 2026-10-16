package exportbundle

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// plainPNGHeader limits pixel-based reuse to the metadata-free PNGs produced
// by our bakers. Profiles, gamma, palettes and other chunks retain strict byte
// comparison: equal samples alone would not prove equal rendering for those.
func plainPNGHeader(data []byte) []byte {
	if len(data) < 33 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil
	}
	var header []byte
	for pos := 8; pos+12 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[pos : pos+4]))
		if size > uint64(len(data)-pos-12) {
			return nil
		}
		end := pos + 12 + int(size)
		switch string(data[pos+4 : pos+8]) {
		case "IHDR":
			if pos != 8 || size != 13 {
				return nil
			}
			header = data[pos+8 : pos+21]
		case "IDAT":
		case "IEND":
			if size == 0 && end == len(data) {
				return header
			}
			return nil
		default:
			return nil
		}
		pos = end
	}
	return nil
}

// samePublishedContent keeps the original texture and its timestamps when an
// older Python export and a Go export differ only in PNG encoding. Decoding
// validates the PNG; comparing the full decoded image also preserves hidden
// RGB samples in transparent pixels and 16-bit precision.
func samePublishedContent(source, destination string) (bool, error) {
	same, err := SameBytes(source, destination)
	if err != nil || same || !strings.EqualFold(filepath.Ext(source), ".png") {
		return same, err
	}
	info, err := os.Stat(destination)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	a, err := os.ReadFile(source)
	if err != nil {
		return false, osError(err)
	}
	b, err := os.ReadFile(destination)
	if err != nil {
		return false, osError(err)
	}
	header := plainPNGHeader(a)
	if header == nil || !bytes.Equal(header, plainPNGHeader(b)) {
		return false, nil
	}
	left, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		return false, nil
	}
	right, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return false, nil
	}
	return reflect.DeepEqual(left, right), nil
}
