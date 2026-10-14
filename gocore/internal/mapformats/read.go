// Package mapformats reads bounded OWLib OWMAP 2.0/2.1 records and builds
// reusable preview geometry. It is the Go port of owpreview/map_formats.py
// (SPDX-License-Identifier: GPL-3.0-or-later).
//
// Coordinates remain raw Y-up metres (XYZW quaternions), like package
// formats. See MAP_FORMAT_REFERENCE.md for the writer/reader contract and
// limitations.
//
// # Python → Go map
//
//	MapLimits / DEFAULT_LIMITS  Limits / DefaultLimits (nil *Limits = default)
//	read_map_header             ReadMapHeader (*HeaderInfo)
//	read_map                    ReadMap (*Map)
//	scan_maps                   ScanMaps (*ScanResult)
//	_reference                  (unexported) reference
//	expand_map_asset            ExpandMapAsset
//	load_map                    LoadMap (compact) / LoadMapWith (options)
//
// load_map loads each referenced model once. Go loads the distinct models
// of a map in parallel (bounded by runtime.NumCPU()), then assembles the
// result sequentially in placement order, so ids, prototypes, warnings and
// errors are identical to Python's sequential loop.
//
// Errors: *formats.FormatError (Python FormatError), *formats.OSError, or
// *ValueError for Python's plain ValueError (bad limits, a map outside its
// source folder, a scan over the file limit).
package mapformats

import (
	"io"
	"math"
	"os"
	"strconv"
	"unicode/utf8"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// ValueError is a Python ValueError that is not a FormatError.
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

// Limits is MapLimits: preview bounds checked while reading and loading.
type Limits struct {
	FileBytes      int64 `json:"file_bytes"`
	Groups         int64 `json:"groups"`
	Instances      int64 `json:"instances"`
	Lights         int64 `json:"lights"`
	Sounds         int64 `json:"sounds"`
	SoundFiles     int64 `json:"sound_files"`
	Models         int64 `json:"models"`
	UniqueVertices int64 `json:"unique_vertices"`
	MeshInstances  int64 `json:"mesh_instances"`
	GraphNodes     int64 `json:"graph_nodes"`
}

// DefaultLimits is DEFAULT_LIMITS.
var DefaultLimits = Limits{
	FileBytes: 128 * 1024 * 1024, Groups: 100_000, Instances: 100_000, Lights: 100_000,
	Sounds: 100_000, SoundFiles: 100_000, Models: 100_000, UniqueVertices: 10_000_000,
	MeshInstances: 200_000, GraphNodes: 500_000,
}

// light20Extra is struct.calcsize('<IIBBBBII3f4f3f4f3f4fffIHHII'): the
// version 2.0 light fields this reader skips.
const light20Extra = 128

func checkLimits(value *Limits) (*Limits, error) {
	if value == nil {
		return &DefaultLimits, nil
	}
	for _, v := range [...]int64{value.FileBytes, value.Groups, value.Instances, value.Lights, value.Sounds,
		value.SoundFiles, value.Models, value.UniqueVertices, value.MeshInstances, value.GraphNodes} {
		if v < 0 {
			return nil, &ValueError{"Map limits must be non-negative integers in MapLimits."}
		}
	}
	return value, nil
}

func readString(r *formats.Reader) (string, error) {
	value, err := r.String()
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(value) > 4096 || containsNUL(value) {
		return "", r.Fail("invalid or oversized map name/reference")
	}
	return value, nil
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// Header is the map header.
type Header struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Version     string `json:"version"`
	ObjectCount int64  `json:"objectCount"`
	DetailCount int64  `json:"detailCount"`
	LightCount  int64  `json:"lightCount"`
}

// HeaderInfo is read_map_header's result.
type HeaderInfo struct {
	Header
	Size int64 `json:"size"`
}

func readHeader(r *formats.Reader, limits *Limits) (Header, error) {
	version, err := r.Unpack("HH")
	if err != nil {
		return Header{}, err
	}
	major, minor := version.Uint(0), version.Uint(1)
	if major != 2 || (minor != 0 && minor != 1) {
		return Header{}, r.Fail("unsupported map version " + strconv.FormatUint(major, 10) + "." +
			strconv.FormatUint(minor, 10) + "; expected 2.0 or 2.1")
	}
	name, err := readString(r)
	if err != nil {
		return Header{}, err
	}
	counts, err := r.Unpack("III")
	if err != nil {
		return Header{}, err
	}
	objects, details, lights := int64(counts.Uint(0)), int64(counts.Uint(1)), int64(counts.Uint(2))
	if err := r.Count(objects, limits.Groups, "map object groups"); err != nil {
		return Header{}, err
	}
	if err := r.Count(details, limits.Instances, "map details"); err != nil {
		return Header{}, err
	}
	if err := r.Count(lights, limits.Lights, "map lights"); err != nil {
		return Header{}, err
	}
	if name == "" {
		name = pypath.Stem(r.Path)
	}
	return Header{Name: name, Path: r.Path, Version: strconv.FormatUint(major, 10) + "." + strconv.FormatUint(minor, 10),
		ObjectCount: objects, DetailCount: details, LightCount: lights}, nil
}

func sizeError(path string, limit int64) error {
	return &formats.FormatError{Message: pypath.Name(path) + ": map exceeds the " + formats.Commas(limit) + "-byte limit."}
}

// ReadMapHeader reads only a bounded header for the folder browser; it is
// not a validity verdict. limits nil means DefaultLimits.
func ReadMapHeader(path string, limits *Limits) (*HeaderInfo, error) {
	path, err := formats.CheckPath(path, ".owmap")
	if err != nil {
		return nil, err
	}
	if limits, err = checkLimits(limits); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	if info.Size() > limits.FileBytes {
		return nil, sizeError(path, limits.FileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	defer file.Close()
	data := make([]byte, 1024*1024+24)
	n, err := io.ReadFull(file, data)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, formats.PythonOSError(err)
	}
	header, err := readHeader(formats.NewReader(path, data[:n]), limits)
	if err != nil {
		return nil, err
	}
	return &HeaderInfo{Header: header, Size: info.Size()}, nil
}

// Placement is one local TRS: the exported position and scale, the
// normalized XYZW rotation and the composed column-major matrix.
type Placement struct {
	Position [3]float64   `json:"position"`
	Scale    [3]float64   `json:"scale"`
	Rotation [4]float64   `json:"rotation"`
	Matrix   formats.Mat4 `json:"matrix"`
}

// Instance is one placed model. ObjectIndex and GroupIndex are set for
// object group placements (category "Objects"), absent for details.
type Instance struct {
	Source      string `json:"source"`
	Material    string `json:"material"`
	Category    string `json:"category"`
	ObjectIndex *int   `json:"objectIndex,omitempty"`
	GroupIndex  *int   `json:"groupIndex,omitempty"`
	RecordIndex int    `json:"recordIndex"`
	Placement
}

// Light is one map light. Intensity and ProjectionTextures exist only in
// version 2.1 records.
type Light struct {
	Position           [3]float64 `json:"position"`
	Rotation           [4]float64 `json:"rotation"`
	Type               uint32     `json:"type"`
	FOV                float64    `json:"fov"`
	Color              [3]float64 `json:"color"`
	Intensity          *float64   `json:"intensity,omitempty"`
	ProjectionTextures []uint64   `json:"projectionTextures,omitempty"`
}

// Sound is one sound emitter.
type Sound struct {
	Position [3]float64 `json:"position"`
	Files    []string   `json:"files"`
}

// Map is read_map's result.
type Map struct {
	Header
	Instances  []Instance `json:"instances"`
	Lights     []Light    `json:"lights"`
	Sounds     []Sound    `json:"sounds"`
	SoundCount int        `json:"soundCount"`
}

func values3(r *formats.Reader) ([3]float64, error) {
	values, err := r.Float64s(3)
	if err != nil {
		return [3]float64{}, err
	}
	return [3]float64(values), nil
}

func values4(r *formats.Reader) ([4]float64, error) {
	values, err := r.Float64s(4)
	if err != nil {
		return [4]float64{}, err
	}
	return [4]float64(values), nil
}

func readRecord(r *formats.Reader) (Placement, error) {
	var p Placement
	var err error
	if p.Position, err = values3(r); err != nil {
		return p, err
	}
	if p.Scale, err = values3(r); err != nil {
		return p, err
	}
	rotation, err := values4(r)
	if err != nil {
		return p, err
	}
	sum := 0.0
	for _, v := range rotation {
		sum += float64(v * v)
	}
	length := math.Sqrt(sum)
	if length < 1e-12 {
		return p, r.Fail("map placement has a zero quaternion")
	}
	for i, v := range rotation {
		p.Rotation[i] = v / length
	}
	p.Matrix = formats.Compose(p.Position, p.Rotation)
	for column := 0; column < 3; column++ {
		for row := 0; row < 3; row++ {
			p.Matrix[column*4+row] *= p.Scale[column]
		}
	}
	return p, nil
}

// ReadMap parses every record, including unrendered lights and sounds, with
// strict bounds. limits nil means DefaultLimits.
func ReadMap(path string, limits *Limits) (*Map, error) {
	path, err := formats.CheckPath(path, ".owmap")
	if err != nil {
		return nil, err
	}
	if limits, err = checkLimits(limits); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	if info.Size() > limits.FileBytes {
		return nil, sizeError(path, limits.FileBytes)
	}
	r, err := formats.ReadFile(path)
	if err != nil {
		return nil, err
	}
	header, err := readHeader(r, limits)
	if err != nil {
		return nil, err
	}
	result := &Map{Header: header, Lights: []Light{}, Sounds: []Sound{}}
	result.Instances = make([]Instance, 0, min(header.DetailCount+int64(r.Remaining()/40), limits.Instances))
	groups := int64(0)
	for objectIndex := 0; objectIndex < int(header.ObjectCount); objectIndex++ {
		source, err := readString(r)
		if err != nil {
			return nil, err
		}
		count, err := r.U32()
		if err != nil {
			return nil, err
		}
		groups += int64(count)
		if err := r.Count(groups, limits.Groups, "map material groups"); err != nil {
			return nil, err
		}
		for groupIndex := 0; groupIndex < int(count); groupIndex++ {
			look, err := readString(r)
			if err != nil {
				return nil, err
			}
			records, err := r.U32()
			if err != nil {
				return nil, err
			}
			if err := r.Count(int64(len(result.Instances))+int64(records)+header.DetailCount, limits.Instances, "map instances"); err != nil {
				return nil, err
			}
			if int64(records) > int64(r.Remaining()/40) {
				return nil, r.Fail("truncated map placement records")
			}
			for recordIndex := 0; recordIndex < int(records); recordIndex++ {
				placement, err := readRecord(r)
				if err != nil {
					return nil, err
				}
				result.Instances = append(result.Instances, Instance{
					Source: source, Material: look, Category: "Objects",
					ObjectIndex: ptr(objectIndex), GroupIndex: ptr(groupIndex), RecordIndex: recordIndex,
					Placement: placement,
				})
			}
		}
	}
	for index := 0; index < int(header.DetailCount); index++ {
		source, err := readString(r)
		if err != nil {
			return nil, err
		}
		look, err := readString(r)
		if err != nil {
			return nil, err
		}
		placement, err := readRecord(r)
		if err != nil {
			return nil, err
		}
		result.Instances = append(result.Instances, Instance{Source: source, Material: look, Category: "Details",
			RecordIndex: index, Placement: placement})
	}
	for i := 0; i < int(header.LightCount); i++ {
		var light Light
		if light.Position, err = values3(r); err != nil {
			return nil, err
		}
		if light.Rotation, err = values4(r); err != nil {
			return nil, err
		}
		if light.Type, err = r.U32(); err != nil {
			return nil, err
		}
		fov, err := r.Float64s(1)
		if err != nil {
			return nil, err
		}
		light.FOV = fov[0]
		if light.Color, err = values3(r); err != nil {
			return nil, err
		}
		if header.Version == "2.0" {
			if _, err := r.Take(light20Extra); err != nil {
				return nil, err
			}
		} else {
			intensity, err := r.Float64s(1)
			if err != nil {
				return nil, err
			}
			light.Intensity = &intensity[0]
			textures, err := r.Unpack("QQ")
			if err != nil {
				return nil, err
			}
			light.ProjectionTextures = []uint64{textures.Uint(0), textures.Uint(1)}
		}
		result.Lights = append(result.Lights, light)
	}
	count, err := r.U32()
	if err != nil {
		return nil, err
	}
	if err := r.Count(int64(count), limits.Sounds, "map sounds"); err != nil {
		return nil, err
	}
	fileTotal := int64(0)
	for i := 0; i < int(count); i++ {
		position, err := values3(r)
		if err != nil {
			return nil, err
		}
		files, err := r.I32()
		if err != nil {
			return nil, err
		}
		if err := r.Count(int64(files), limits.SoundFiles, "sound file count"); err != nil {
			return nil, err
		}
		fileTotal += int64(files)
		if err := r.Count(fileTotal, limits.SoundFiles, "map sound files"); err != nil {
			return nil, err
		}
		sound := Sound{Position: position, Files: make([]string, 0, files)}
		for j := 0; j < int(files); j++ {
			file, err := readString(r)
			if err != nil {
				return nil, err
			}
			sound.Files = append(sound.Files, file)
		}
		result.Sounds = append(result.Sounds, sound)
	}
	if r.Remaining() > 0 {
		return nil, r.Fail("unexpected " + formats.Commas(r.Remaining()) + " trailing map bytes")
	}
	result.SoundCount = len(result.Sounds)
	return result, nil
}

func ptr[T any](value T) *T { return &value }
