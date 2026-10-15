package api

import (
	"encoding/base64"
	"io"
	"strconv"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/textures"
)

// Binary frames (negotiated per request; see README "Binary arrays").
//
// A host that can decode them asks per request: a stdio request object with
// "binary": true, or an HTTP request with the X-Avolition-Binary: 1 header.
// The Python core ignores both and always answers in JSON, so hosts must
// accept either form. When a reply has bulk arrays (load_entity,
// preview_entity, load_map, load_animation, load_texture), the core answers
// with a frame instead of a plain JSON reply:
//
//	{"id":7,"result":{"ok":true,"data":{... "positions":{"$blob":0} ...}},"blobs":[["f32",1200],["u16",600]]}   \n
//	<blob 0 bytes><zero padding to 8><blob 1 bytes><zero padding to 8>...
//
// The JSON line (spaces before its newline pad it to a multiple of 8 bytes)
// is the reply object plus "blobs", listing each blob's typed-array type
// (f32, f64, u32, u16, u8) and byte length. Each blob follows, little-endian,
// zero-padded to a multiple of 8 bytes, so every blob starts 8-byte aligned
// relative to the frame start. {"$blob":N} placeholders in the result stand
// for blob N as a typed array. Replies without blobs stay plain JSON lines.
// Over HTTP the frame has no "id" and its Content-Type is FrameContentType.

// FrameContentType is the HTTP Content-Type of a binary frame.
const FrameContentType = "application/x-avolition-frame"

// BinaryAppender is reply data whose bulk arrays can be sent as blobs.
type BinaryAppender interface {
	AppendBinary(dst []byte, blobs *formats.Blobs) ([]byte, error)
}

func padding(n int) int { return (8 - n%8) % 8 }

// appendResultFrame is AppendResult, with bulk arrays as blobs when data
// supports them. blobs is nil when the result has none.
func appendResultFrame(dst []byte, data any, err error) ([]byte, *formats.Blobs) {
	if appender, ok := data.(BinaryAppender); ok && err == nil {
		start := len(dst)
		blobs := &formats.Blobs{}
		dst = append(dst, `{"ok":true,"data":`...)
		encoded, encodeErr := appender.AppendBinary(dst, blobs)
		if encodeErr == nil {
			if len(blobs.Items) == 0 {
				blobs = nil
			}
			return append(encoded, '}'), blobs
		}
		dst, err = dst[:start], encodeErr
	}
	return AppendResult(dst, data, err), nil
}

// appendFrameTail closes a frame's JSON object (open after its "result"),
// listing the blobs, and pads the line to a multiple of 8 bytes relative to
// start.
func appendFrameTail(dst []byte, start int, blobs *formats.Blobs) []byte {
	dst = append(dst, `,"blobs":[`...)
	for i, blob := range blobs.Items {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, `["`...)
		dst = append(dst, blob.Type...)
		dst = append(dst, `",`...)
		dst = strconv.AppendInt(dst, int64(len(blob.Data)), 10)
		dst = append(dst, ']')
	}
	dst = append(dst, "]}"...)
	for range padding(len(dst) - start + 1) {
		dst = append(dst, ' ')
	}
	return append(dst, '\n')
}

var zeros [8]byte

// writeBlobs writes a frame's blobs after its JSON line.
func writeBlobs(out io.Writer, blobs *formats.Blobs) error {
	for _, blob := range blobs.Items {
		if _, err := out.Write(blob.Data); err != nil {
			return err
		}
		if _, err := out.Write(zeros[:padding(len(blob.Data))]); err != nil {
			return err
		}
	}
	return nil
}

// frameSize is the byte length of a frame's blob section.
func frameSize(blobs *formats.Blobs) int {
	size := 0
	for _, blob := range blobs.Items {
		size += len(blob.Data) + padding(len(blob.Data))
	}
	return size
}

// textureData is a load_texture reply. Its binary form sends the PNG bytes as
// a "png" blob (Uint8Array) instead of the base64 "dataUri", sparing a third
// of the bytes and the renderer's base64 decoding.
type textureData struct{ result *textures.Result }

func (d textureData) AppendJSON(dst []byte) ([]byte, error) { return appendValue(dst, d.result) }

const pngDataURIPrefix = "data:image/png;base64,"

func (d textureData) AppendBinary(dst []byte, blobs *formats.Blobs) ([]byte, error) {
	result := d.result
	if result == nil || !strings.HasPrefix(result.DataURI, pngDataURIPrefix) {
		return d.AppendJSON(dst)
	}
	png, err := base64.StdEncoding.DecodeString(result.DataURI[len(pngDataURIPrefix):])
	if err != nil {
		return d.AppendJSON(dst)
	}
	dst = append(dst, `{"png":`...)
	dst = blobs.Add(dst, formats.BlobUint8, png)
	dst = append(dst, `,"width":`...)
	dst = strconv.AppendInt(dst, int64(result.Width), 10)
	dst = append(dst, `,"height":`...)
	dst = strconv.AppendInt(dst, int64(result.Height), 10)
	if result.Warning != "" {
		dst = append(dst, `,"warning":`...)
		dst = appendString(dst, result.Warning)
	}
	return append(dst, '}'), nil
}

// AppendBinary encodes an asset reply with formats.AppendAssetBinary.
func (d assetData) AppendBinary(dst []byte, blobs *formats.Blobs) ([]byte, error) {
	return formats.AppendAssetBinary(dst, d.asset, blobs)
}

// AppendBinary encodes a clip reply with formats.AppendClipBinary.
func (d clipData) AppendBinary(dst []byte, blobs *formats.Blobs) ([]byte, error) {
	return formats.AppendClipBinary(dst, d.clip, blobs)
}
