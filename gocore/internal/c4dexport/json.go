package c4dexport

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"avolition/gocore/internal/formats"
)

// MarshalJSON encodes the payload in Python's key order. The asset and the
// baked tracks (most of a large payload) are written without reflection.
func (p *Payload) MarshalJSON() ([]byte, error) { return AppendPayloadJSON(nil, p) }

// AppendPayloadJSON appends the JSON encoding of p to dst.
func AppendPayloadJSON(dst []byte, p *Payload) ([]byte, error) {
	if p == nil {
		return append(dst, "null"...), nil
	}
	if dst == nil {
		frames := 0
		for i := range p.Animation.Tracks {
			frames += len(p.Animation.Tracks[i].Frames)
		}
		size := 4096 + frames*16*12
		if p.Asset != nil {
			size += 400 * len(p.Asset.ExportHierarchy)
			for _, model := range p.Asset.Models {
				if model == nil {
					continue
				}
				size += 2048 + 400*len(model.Bones)
				for _, mesh := range model.Meshes {
					if mesh != nil {
						size += 12*(len(mesh.Positions)+len(mesh.Normals)+len(mesh.UVs)+len(mesh.UVs2)) +
							7*len(mesh.Indices) + 2*len(mesh.SkinIndices) + 12*len(mesh.SkinWeights)
					}
				}
			}
		}
		dst = make([]byte, 0, size)
	}
	var err error
	dst = append(dst, `{"version":`...)
	dst = strconv.AppendInt(dst, int64(p.Version), 10)
	dst = append(dst, `,"asset":`...)
	if dst, err = formats.AppendAssetJSON(dst, p.Asset); err != nil {
		return nil, err
	}
	dst = append(dst, `,"materials":`...)
	if p.Materials == nil {
		dst = append(dst, `{}`...)
	} else if dst, err = appendValue(dst, p.Materials); err != nil {
		return nil, err
	}
	dst = append(dst, `,"warnings":`...)
	if dst, err = appendValue(dst, p.Warnings); err != nil {
		return nil, err
	}
	dst = append(dst, `,"includeMesh":`...)
	dst = strconv.AppendBool(dst, p.IncludeMesh)
	dst = append(dst, `,"name":`...)
	if dst, err = appendValue(dst, p.Name); err != nil {
		return nil, err
	}
	a := &p.Animation
	dst = append(dst, `,"animation":{"fps":`...)
	if dst, err = appendFloat(dst, a.FPS); err != nil {
		return nil, err
	}
	dst = append(dst, `,"lastFrame":`...)
	dst = strconv.AppendInt(dst, a.LastFrame, 10)
	if a.DurationSeconds != nil {
		dst = append(dst, `,"durationSeconds":`...)
		if dst, err = appendFloat(dst, *a.DurationSeconds); err != nil {
			return nil, err
		}
	}
	dst = append(dst, `,"tracks":`...)
	if dst, err = appendTracks(dst, a.Tracks); err != nil {
		return nil, err
	}
	if len(a.Assignments) > 0 {
		dst = append(dst, `,"assignments":`...)
		if dst, err = appendValue(dst, a.Assignments); err != nil {
			return nil, err
		}
	}
	dst = append(dst, `,"matchedBones":`...)
	dst = strconv.AppendInt(dst, int64(a.MatchedBones), 10)
	return append(dst, `}}`...), nil
}

func appendTracks(dst []byte, tracks []Track) ([]byte, error) {
	if tracks == nil {
		return append(dst, `null`...), nil
	}
	var err error
	dst = append(dst, '[')
	for i := range tracks {
		track := &tracks[i]
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, `{"modelId":`...)
		if dst, err = appendValue(dst, track.ModelID); err != nil {
			return nil, err
		}
		dst = append(dst, `,"boneIndex":`...)
		dst = strconv.AppendInt(dst, int64(track.BoneIndex), 10)
		dst = append(dst, `,"frames":`...)
		if track.Frames == nil {
			dst = append(dst, `null`...)
		} else {
			dst = append(dst, '[')
			for f := range track.Frames {
				if f > 0 {
					dst = append(dst, ',')
				}
				dst = append(dst, '[')
				for k, v := range &track.Frames[f] {
					if k > 0 {
						dst = append(dst, ',')
					}
					if dst, err = appendFloat(dst, v); err != nil {
						return nil, err
					}
				}
				dst = append(dst, ']')
			}
			dst = append(dst, ']')
		}
		dst = append(dst, '}')
	}
	return append(dst, ']'), nil
}

func appendValue(dst []byte, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}

// appendFloat is encoding/json's float64 encoder (ES6 number formatting).
func appendFloat(b []byte, f float64) ([]byte, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errors.New("json: unsupported value: " + strconv.FormatFloat(f, 'g', -1, 64))
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// Clean up e-09 to e-9.
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b, nil
}
