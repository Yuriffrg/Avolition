// Package formatstest builds DataTool .owmdl, .owentity and .owanimclip files
// for tests, following OWLib's documented binary layout. It ports the
// struct.pack fixture builders of tests/test_formats.py (pack, string,
// model_bytes, entity_bytes, clip_bytes) so every package can generate the
// same inputs, plus a synthetic large-model generator for benchmarks.
package formatstest

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Pack is struct.pack('<' + format, values...). Supported characters:
// B b H h I i Q q f d with optional repeat counts ("9f"). Values may be any
// Go integer or float type.
func Pack(format string, values ...any) []byte {
	var out []byte
	next := 0
	repeat := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c >= '0' && c <= '9' {
			repeat = repeat*10 + int(c-'0')
			continue
		}
		if repeat == 0 {
			repeat = 1
		}
		for ; repeat > 0; repeat-- {
			if next >= len(values) {
				panic(fmt.Sprintf("formatstest.Pack(%q): not enough values", format))
			}
			value := values[next]
			next++
			switch c {
			case 'B', 'b':
				out = append(out, byte(asInt(value)))
			case 'H', 'h':
				out = binary.LittleEndian.AppendUint16(out, uint16(asInt(value)))
			case 'I', 'i':
				out = binary.LittleEndian.AppendUint32(out, uint32(asInt(value)))
			case 'Q', 'q':
				out = binary.LittleEndian.AppendUint64(out, uint64(asInt(value)))
			case 'f':
				out = binary.LittleEndian.AppendUint32(out, math.Float32bits(float32(asFloat(value))))
			case 'd':
				out = binary.LittleEndian.AppendUint64(out, math.Float64bits(asFloat(value)))
			default:
				panic(fmt.Sprintf("formatstest.Pack: unsupported format %q", c))
			}
		}
	}
	if next != len(values) {
		panic(fmt.Sprintf("formatstest.Pack(%q): %d values left over", format, len(values)-next))
	}
	return out
}

func asInt(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	}
	panic(fmt.Sprintf("formatstest: not an integer: %T", value))
}

func asFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	}
	return float64(asInt(value))
}

// String is a .NET BinaryWriter string: 7-bit encoded UTF-8 length + bytes.
func String(value string) []byte {
	length := len(value)
	var prefix []byte
	for length >= 128 {
		prefix = append(prefix, byte(length&127|128))
		length >>= 7
	}
	prefix = append(prefix, byte(length))
	return append(prefix, value...)
}

// Bone is one model_bytes bone: name, parent, position and Euler rotation.
type Bone struct {
	Name     string
	Parent   int
	Position [3]float64
	Rotation [3]float64
}

// Socket is one model_bytes hardpoint.
type Socket struct {
	Name, Bone string
	Position   [3]float64
	Rotation   [4]float64
}

// Model is model_bytes' arguments. Start from NewModel for Python's defaults.
type Model struct {
	Bones   []Bone
	Mesh    bool
	Sockets []Socket
	Name    string
	Version [2]uint16
	Slots   int
}

// DefaultBones is model_bytes' default skeleton: bone_0002 at (0, 2, 0)
// rotated 90 degrees about Z under the root bone_0001.
func DefaultBones() []Bone {
	return []Bone{
		{Name: "bone_0001", Parent: -1},
		{Name: "bone_0002", Parent: 0, Position: [3]float64{0, 2, 0}, Rotation: [3]float64{0, 0, math.Pi / 2}},
	}
}

// NewModel returns model_bytes' defaults: two bones, one triangle mesh with
// five skin slots, no sockets, name "Fixture", version 2.0.
func NewModel() Model {
	return Model{Bones: DefaultBones(), Mesh: true, Name: "Fixture", Version: [2]uint16{2, 0}, Slots: 5}
}

// Bytes is model_bytes(...).
func (m Model) Bytes() []byte {
	blob := Pack("HH", m.Version[0], m.Version[1])
	blob = append(blob, String("")...)
	blob = append(blob, String(m.Name)...)
	meshCount := 0
	if m.Mesh {
		meshCount = 1
	}
	blob = append(blob, Pack("IHII", 123, len(m.Bones), meshCount, len(m.Sockets))...)
	for _, bone := range m.Bones {
		// Exported non-unit scale is deliberately ignored like BLModel.
		blob = append(blob, String(bone.Name)...)
		blob = append(blob, Pack("h", bone.Parent)...)
		blob = append(blob, Pack("9f", bone.Position[0], bone.Position[1], bone.Position[2], 2, 3, 4,
			bone.Rotation[0], bone.Rotation[1], bone.Rotation[2])...)
	}
	if m.Mesh {
		blob = append(blob, String("Triangle")...)
		blob = append(blob, Pack("QBIIB", 4000, 1, 3, 1, m.Slots)...)
		blob = append(blob, Pack("9f", 0, 0, 0, 1, 0, 0, 0, 0, 1)...)    // raw Y-up: (0,1,0)
		blob = append(blob, Pack("9f", 0, -1, 0, 0, -1, 0, 0, -1, 0)...) // raw normal +Z
		blob = append(blob, Pack("12f", 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1)...)
		blob = append(blob, Pack("6f", 0, 0, 1, 0, 0, 1)...)
		if m.Slots > 0 {
			var indices, weights []any
			if m.Slots == 5 {
				for v := 0; v < 3; v++ {
					indices = append(indices, 0, 1, -1, 0, 1)
					weights = append(weights, .1, .2, .9, .2, .5)
				}
			} else {
				for i := 0; i < 3*m.Slots; i++ {
					indices = append(indices, 0)
					weights = append(weights, 1/float64(m.Slots))
				}
			}
			blob = append(blob, Pack(fmt.Sprintf("%dh", len(indices)), indices...)...)
			blob = append(blob, Pack(fmt.Sprintf("%df", len(weights)), weights...)...)
		}
		blob = append(blob, make([]byte, 24*4)...)
		blob = append(blob, Pack("III", 0, 1, 2)...)
	}
	for _, socket := range m.Sockets {
		blob = append(blob, String(socket.Name)...)
		blob = append(blob, String(socket.Bone)...)
		blob = append(blob, Pack("7f", socket.Position[0], socket.Position[1], socket.Position[2],
			socket.Rotation[0], socket.Rotation[1], socket.Rotation[2], socket.Rotation[3])...)
	}
	return blob
}

// Child is one entity_bytes child: reference and attachment.
type Child struct{ Reference, Attachment string }

// Entity is entity_bytes' arguments. Start from NewEntity for the defaults.
type Entity struct {
	Name, Model string
	Children    []Child
	Minor       uint16
	Relative    string
}

// NewEntity returns entity_bytes' defaults: "Hero" using "Body.00C",
// version 2.1, relative "../../".
func NewEntity() Entity {
	return Entity{Name: "Hero", Model: "Body.00C", Minor: 1, Relative: "../../"}
}

// Bytes is entity_bytes(...).
func (e Entity) Bytes() []byte {
	blob := String("owentity")
	blob = append(blob, Pack("HH", 2, e.Minor)...)
	blob = append(blob, String(e.Name)...)
	blob = append(blob, String(e.Model)...)
	blob = append(blob, String("null")...)
	blob = append(blob, Pack("IIIi", 1, 2, 0, len(e.Children))...)
	for _, child := range e.Children {
		blob = append(blob, String(child.Reference)...)
		blob = append(blob, Pack("QQII", 1, 2, 1, 2)...)
		blob = append(blob, String(child.Attachment)...)
	}
	if e.Minor >= 1 {
		blob = append(blob, String("null")...)
		blob = append(blob, String(e.Relative)...)
	}
	return blob
}

// Key is one keyframe: frame number and component values.
type Key struct {
	Frame  uint32
	Values []float64
}

// K builds a Key: K(frame, values...).
func K(frame uint32, values ...float64) Key { return Key{Frame: frame, Values: values} }

// Track is one track: kind (0 positions, 1 rotations, 2 scales) and keys.
type Track struct {
	Kind uint32
	Keys []Key
}

// ClipBone is one bone record.
type ClipBone struct {
	Name   string
	Tracks []Track
}

// Clip is clip_bytes' arguments. Start from NewClip for the defaults.
type Clip struct {
	Bones    []ClipBone
	Duration uint32
	FPS      float64
	Version  [2]uint16
}

// NewClip returns clip_bytes' defaults: bone_0002 with out-of-order
// positions (frames 30, 0), rotations and one scale key; 31 frames at 30 fps.
func NewClip() Clip {
	return Clip{
		Bones: []ClipBone{{Name: "bone_0002", Tracks: []Track{
			{Kind: 0, Keys: []Key{{30, []float64{0, 4, 0}}, {0, []float64{0, 2, 0}}}},
			{Kind: 1, Keys: []Key{{0, []float64{0, 0, 0, 2}}, {30, []float64{0, 0, 1, 0}}}},
			{Kind: 2, Keys: []Key{{0, []float64{1, 1, 1}}}},
		}}},
		Duration: 31, FPS: 30, Version: [2]uint16{2, 0},
	}
}

// Bytes is clip_bytes(...).
func (c Clip) Bytes() []byte {
	blob := Pack("HHIfI", c.Version[0], c.Version[1], len(c.Bones), c.FPS, c.Duration)
	for _, bone := range c.Bones {
		blob = append(blob, String(bone.Name)...)
		blob = append(blob, Pack("I", len(bone.Tracks))...)
		for _, track := range bone.Tracks {
			components := 3
			if len(track.Keys) > 0 {
				components = len(track.Keys[0].Values)
			} else if track.Kind == 1 {
				components = 4
			}
			blob = append(blob, Pack("III", track.Kind, len(track.Keys), components)...)
			for _, key := range track.Keys {
				blob = append(blob, Pack("I", key.Frame)...)
				for _, v := range key.Values {
					blob = append(blob, Pack("f", v)...)
				}
			}
		}
	}
	return blob
}

// Write writes data to dir/name (creating parent folders) like the Python
// tests' write() helper, and returns the path.
func Write(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// LargeModel generates a valid .owmdl with the given bone count and meshes
// totalling vertices vertices (split into meshes of at most 65,536 vertices,
// a grid of quads), two UV channels and four skin slots — a realistic shape
// for benchmarks. Output is deterministic.
func LargeModel(vertices, bones int) []byte {
	if bones < 1 {
		bones = 1
	}
	blob := Pack("HH", 2, 0)
	blob = append(blob, String("")...)
	blob = append(blob, String("Synthetic")...)
	var meshSizes []int
	for remaining := vertices; remaining > 0; remaining -= 65536 {
		meshSizes = append(meshSizes, min(remaining, 65536))
	}
	blob = append(blob, Pack("IHII", 7, bones, len(meshSizes), 1)...)
	for b := 0; b < bones; b++ {
		blob = append(blob, String(fmt.Sprintf("bone_%04X", b+1))...)
		blob = append(blob, Pack("h", b-1)...)
		blob = append(blob, Pack("9f", 0, 0.1, 0, 1, 1, 1, 0.01*float64(b%7), 0.02*float64(b%5), 0.03*float64(b%3))...)
	}
	f32 := func(v float64) { blob = binary.LittleEndian.AppendUint32(blob, math.Float32bits(float32(v))) }
	for m, count := range meshSizes {
		width := 256
		quadsX, quadsY := width-1, (count/width)-1
		faces := 0
		if quadsY > 0 {
			faces = quadsX * quadsY * 2
		}
		blob = append(blob, String(fmt.Sprintf("Mesh_%d", m))...)
		blob = append(blob, Pack("QBIIB", 1000+m, 2, count, faces, 4)...)
		for v := 0; v < count; v++ { // positions
			f32(float64(v%width) * 0.01)
			f32(float64(m) * 0.5)
			f32(float64(v/width) * 0.01)
		}
		for v := 0; v < count; v++ { // normals
			f32(0)
			f32(-1)
			f32(0)
		}
		blob = append(blob, make([]byte, count*16)...) // tangents
		for uv := 0; uv < 2; uv++ {
			for v := 0; v < count; v++ {
				f32(float64(v%width) / float64(width))
				f32(float64(v/width) / float64(count/width+1))
			}
		}
		for v := 0; v < count; v++ { // skin indices
			base := v % bones
			blob = binary.LittleEndian.AppendUint16(blob, uint16(base))
			blob = binary.LittleEndian.AppendUint16(blob, uint16((base+1)%bones))
			blob = binary.LittleEndian.AppendUint16(blob, uint16((base+2)%bones))
			blob = binary.LittleEndian.AppendUint16(blob, 0xffff)
		}
		for v := 0; v < count; v++ { // skin weights
			f32(0.5)
			f32(0.3)
			f32(0.2)
			f32(0)
		}
		blob = append(blob, make([]byte, count*32)...) // vertex colours
		for y := 0; y < quadsY; y++ {
			for x := 0; x < quadsX; x++ {
				a := uint32(y*width + x)
				b, c, d := a+1, a+uint32(width), a+uint32(width)+1
				for _, index := range [6]uint32{a, c, b, b, c, d} {
					blob = binary.LittleEndian.AppendUint32(blob, index)
				}
			}
		}
	}
	blob = append(blob, String("hardpoint_0001")...)
	blob = append(blob, String(fmt.Sprintf("bone_%04X", bones))...)
	blob = append(blob, Pack("7f", 0, 1, 0, 0, 0, 0, 1)...)
	return blob
}

// LargeClip generates a valid .owanimclip with bones bones, each with frames
// position, rotation and scale keys.
func LargeClip(bones, frames int) []byte {
	clip := Clip{Duration: uint32(frames), FPS: 30, Version: [2]uint16{2, 0}}
	for b := 0; b < bones; b++ {
		var positions, rotations, scales []Key
		for f := 0; f < frames; f++ {
			t := float64(f) / float64(frames)
			positions = append(positions, Key{uint32(f), []float64{t, float64(b) * 0.1, -t}})
			rotations = append(rotations, Key{uint32(f), []float64{0, math.Sin(t), 0, math.Cos(t)}})
			scales = append(scales, Key{uint32(f), []float64{1, 1, 1}})
		}
		clip.Bones = append(clip.Bones, ClipBone{Name: fmt.Sprintf("bone_%04X", b+1), Tracks: []Track{
			{Kind: 0, Keys: positions}, {Kind: 1, Keys: rotations}, {Kind: 2, Keys: scales}}})
	}
	return clip.Bytes()
}
