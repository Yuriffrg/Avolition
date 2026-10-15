package textures

import (
	"encoding/json"
	"slices"
	"strings"

	"avolition/gocore/internal/bcenc"
)

// GPU block-compressed previews (binary replies only; see gocore/README.md
// "Compressed textures"). A host whose renderer can sample BC formats lists
// them per request; the store then answers with a full mip chain in one of
// them instead of a PNG, and falls back to the PNG whenever none fits.

// CompressedFormats are the formats a host may accept, in preference order.
var CompressedFormats = []string{bcenc.BC7, bcenc.BC3, bcenc.BC1}

// CanonicalFormats returns the known formats in names as a canonical,
// comma-separated key ("" when none is known).
func CanonicalFormats(names []string) string {
	var known []string
	for _, format := range CompressedFormats {
		if slices.Contains(names, format) {
			known = append(known, format)
		}
	}
	return strings.Join(known, ",")
}

// chooseFormat picks the format for a preview: BC7 when accepted (it keeps
// alpha too), else BC1 for opaque previews and BC3 when alpha matters.
func chooseFormat(accepted string, hasAlpha bool) string {
	formats := strings.Split(accepted, ",")
	switch {
	case slices.Contains(formats, bcenc.BC7):
		return bcenc.BC7
	case !hasAlpha && slices.Contains(formats, bcenc.BC1):
		return bcenc.BC1
	case slices.Contains(formats, bcenc.BC3):
		return bcenc.BC3
	}
	return ""
}

// LoadJSONCompressed is LoadJSON with the host's accepted compressed
// formats (canonical, see CanonicalFormats).
func (s *Store) LoadJSONCompressed(textureID, maxEdge json.RawMessage, accepted string) (*Result, error) {
	edge, err := ParseMaxEdge(maxEdge)
	if err != nil {
		return nil, err
	}
	var id string
	if json.Unmarshal(textureID, &id) != nil {
		return nil, errUnregistered
	}
	return s.LoadCompressed(id, edge, accepted)
}

// LoadCompressed is Load, answered in one of the accepted block formats
// when possible: the preview's rows flipped bottom to top (block-compressed
// uploads cannot be flipped by WebGL), with every mip level down to 1×1. It
// returns Load's PNG result when no accepted format fits or the preview's
// size is not a multiple of 4. Results are cached per (id, maxEdge,
// accepted).
func (s *Store) LoadCompressed(textureID string, maxEdge int, accepted string) (*Result, error) {
	if accepted == "" {
		return s.Load(textureID, maxEdge)
	}
	if maxEdge == 0 {
		maxEdge = maxPreviewEdge
	} else if maxEdge < 32 || maxEdge > MaxPreviewEdge {
		return nil, errPreviewEdge
	}
	s.mu.Lock()
	spec, ok := s.specs[textureID]
	if !ok {
		s.mu.Unlock()
		return nil, errUnregistered
	}
	key := resultKey{textureID, maxEdge, accepted}
	if element, ok := s.resultIndex[key]; ok {
		s.results.MoveToBack(element)
		s.mu.Unlock()
		return element.Value.(*resultEntry).result, nil
	}
	generation := s.generation
	s.mu.Unlock()

	channels, convert, specErr := channelConverter(spec)
	if specErr != nil {
		return s.Load(textureID, maxEdge) // reports the errors in Python's order
	}
	source, warning, err := s.image(spec.Path, maxEdge, generation)
	if err != nil {
		return nil, err
	}
	if !bcenc.BlockAligned(source.Width, source.Height) {
		return s.Load(textureID, maxEdge)
	}
	pix, hasAlpha := flippedRGBA(source, channels, convert)
	format := chooseFormat(accepted, hasAlpha)
	if format == "" {
		return s.Load(textureID, maxEdge)
	}
	// Colour maps are sampled as sRGB, so their mips average in linear light
	// like the GPU's generateMipmap on an sRGB texture.
	srgb := (spec.Channel == "" || spec.Channel == "rgb" || spec.Channel == "rgba") &&
		(spec.Transform == "" || spec.Transform == "none") && spec.InputColorSpace == "srgb"
	levels := bcenc.MipChain(pix, source.Width, source.Height, srgb)
	result := &Result{Width: source.Width, Height: source.Height, Warning: warning, Format: format,
		Mips: make([]Mip, len(levels))}
	encode := bcenc.Encode
	if !hasAlpha {
		encode = bcenc.EncodeOpaque // keeps black and white exact
	}
	for i, level := range levels {
		result.Mips[i] = Mip{level.Width, level.Height, encode(format, level.Pix, level.Width, level.Height)}
	}
	return s.storeResult(key, result, generation), nil
}

// flippedRGBA is the preview of source as RGBA rows from bottom to top
// (opaque alpha for RGB previews), and whether any alpha is below 255.
func flippedRGBA(source *Image, channels int, convert func(in, out []byte)) ([]byte, bool) {
	width, height := source.Width, source.Height
	stride := width * 4
	out := make([]byte, stride*height)
	alpha := make([]bool, height)
	parallelRows(height, width, func(y0, y1 int) {
		var scratch []byte
		if convert != nil {
			scratch = make([]byte, width*channels)
		}
		for y := y0; y < y1; y++ {
			in := source.Pix[y*stride : (y+1)*stride]
			dst := out[(height-1-y)*stride:][:stride]
			if convert == nil {
				copy(dst, in)
				for x := 3; x < stride; x += 4 {
					if in[x] != 255 {
						alpha[y] = true
						break
					}
				}
				continue
			}
			convert(in, scratch)
			for x, o := 0, 0; x < stride; x, o = x+4, o+3 {
				dst[x], dst[x+1], dst[x+2], dst[x+3] = scratch[o], scratch[o+1], scratch[o+2], 255
			}
		}
	})
	return out, slices.Contains(alpha, true)
}
