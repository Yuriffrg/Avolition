// Package materials reads OWMAT 3.0 look/material descriptors for the
// standalone texture preview. It is the Go port of owpreview/materials.py
// (SPDX-License-Identifier: GPL-3.0-or-later).
//
// Binary layouts follow OWLib DataTool/SaveLogic/Model.cs. Texture binding
// hashes come from io_scene_owm shader_metadata.py. File names are
// references, never a source of inferred texture roles. Images are decoded
// lazily by the host API.
//
// # Python → Go map
//
//	SUPPORTED_IMAGES, role hash sets  SupportedImages, BaseColor, Normal, ...
//	COLLISION_MATERIALS               CollisionMaterials
//	is_collision_material             IsCollisionMaterial
//	read_owmat                        ReadOWMat (*Record)
//	MaterialResolver                  Resolver (New)
//	  .warn/.resolve                  Warn / Resolve
//	  ._read/._input/._texture        (unexported)
//	  .descriptor                     Descriptor
//	  .for_model                      ForModel
//	  .textures/.root/.warnings       Textures() / Root / Warnings()
//
// Importing this package registers Resolver with formats.RegisterMaterialResolver,
// so formats.LoadAsset produces load_asset's materials and textures.
//
// SupportedImages duplicates the image extensions of textures.py
// (IMAGE_EXTENSIONS) that materials.py declares itself; it is kept private to
// this package's semantics so it does not depend on the textures port.
package materials

import (
	"os"
	"strconv"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// SupportedImages is SUPPORTED_IMAGES: texture extensions the preview accepts.
var SupportedImages = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".tga": true, ".dds": true,
	".tif": true, ".tiff": true, ".bmp": true, ".webp": true,
}

// Texture binding hashes (io_scene_owm shader_metadata.py).
var (
	BaseColor   = set(2903569922, 3093211343, 67369114, 1716930793, 1239794147, 3411049489)
	Normal      = set(378934698, 229655782, 562391268, 2762290483)
	PBR         = set(548341454, 2018946191, 763550166, 1239777102, 3111105361)
	Emission    = set(3166598269, 1523270506, 1493127177)
	Alpha       = set(1482859648, 1140682086)
	AO          = set(3335614873, 3761386704)
	AlbedoAlpha = set(67369114, 1239794147)
)

const (
	CombinedDecal = 3989656707
	HairRoughness = 1117188170
	UVEmission    = 3344068240
	Opacity       = 565460110
)

// EmissionStrength lists the emission strength inputs; the last present wins.
var EmissionStrength = [2]uint32{3250491852, 2446772623}

func set(values ...uint32) map[uint32]bool {
	out := make(map[uint32]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

// CollisionMaterials is io_scene_owm shader_metadata.CollisionMaterials. These
// exact material GUIDs mark the game's invisible engine hit-test surfaces, not
// visible architecture. The Blender map importer removes them by default
// (SettingTypes.removeCollision).
var CollisionMaterials = map[string]bool{
	"0000000034A0": true, "000000002DD4": true, "000000002D77": true, "0000000034A2": true,
	"000000000796": true, "000000000005": true, "000000000794": true, "0000000048EF": true,
	"0000000034A3": true, "000000000797": true, "0000000007A2": true, "0000000007A0": true,
	"0000000007C0": true, "0000000007A1": true,
}

// IsCollisionMaterial classifies only a referenced material's exact GUID,
// never shader or color.
func IsCollisionMaterial(material *formats.Material) bool {
	if material == nil || material.SourcePath == "" {
		return false
	}
	return CollisionMaterials[pythonUpper(pypath.Stem(material.SourcePath))]
}

// pbrChannel is one PBR_CHANNELS entry. Populated only with channels
// confirmed against the supplied Blender node library. Unrecognized packed
// data is reported, rather than guessed.
type pbrChannel struct{ role, channel, transform string }

var pbrChannels = [...]pbrChannel{{"roughness", "g", "invert"}, {"metalness", "r", "ow-metalness"}}

// LookEntry is one model-look assignment: the uint64 material key in decimal
// and the material reference.
type LookEntry struct {
	Key       string
	Reference string
}

// TextureBinding is one material texture: reference and binding hash.
type TextureBinding struct {
	Reference string
	Binding   uint32
}

// Record is read_owmat's result. Kind is "look" (Entries) or "material"
// (Shader, Textures, Inputs in file order; a repeated key keeps its first
// position and the last value, like a Python dict).
type Record struct {
	Kind     string
	Path     string
	Entries  []LookEntry
	Shader   uint32
	Textures []TextureBinding
	// InputKeys is the key order of Inputs.
	InputKeys []uint32
	Inputs    map[uint32][]byte
}

// MaxMaterialFileBytes is read_owmat's file size limit.
const MaxMaterialFileBytes = 32 * 1024 * 1024

func countU64(r *formats.Reader, count, maximum uint64, label string) error {
	if count > maximum {
		return r.Fail("invalid " + label + ": " + formats.Commas(count) + " (preview limit " + formats.Commas(maximum) + ")")
	}
	return nil
}

// ReadOWMat reads an OWMAT record while respecting each static input's byte
// length (Python's read_owmat).
func ReadOWMat(path string) (*Record, error) {
	return readOWMat(pypath.Resolve(path))
}

// readOWMat is ReadOWMat for a resolved path.
func readOWMat(path string) (*Record, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	if info.Size() > MaxMaterialFileBytes {
		return nil, &formats.FormatError{Message: pypath.Name(path) + ": material file exceeds 32 MB"}
	}
	r, err := formats.ReadFile(path)
	if err != nil {
		return nil, err
	}
	head, err := r.Unpack("HHI")
	if err != nil {
		return nil, err
	}
	major, minor, kind := head.Uint(0), head.Uint(1), head.Uint(2)
	if major != 3 || minor != 0 {
		return nil, r.Fail("unsupported material version " + strconv.FormatUint(major, 10) + "." +
			strconv.FormatUint(minor, 10) + "; expected 3.0")
	}
	if kind == 1 {
		count, err := r.U64()
		if err != nil {
			return nil, err
		}
		if err := countU64(r, count, 16384, "material look entries"); err != nil {
			return nil, err
		}
		entries := make([]LookEntry, 0, count)
		for i := uint64(0); i < count; i++ {
			key, err := r.U64()
			if err != nil {
				return nil, err
			}
			reference, err := r.String()
			if err != nil {
				return nil, err
			}
			entries = append(entries, LookEntry{strconv.FormatUint(key, 10), reference})
		}
		// OWLib writes an extra zero long for a null (not empty) material list.
		if r.Remaining() > 0 {
			nullList := false
			if count == 0 && r.Remaining() == 8 {
				value, err := r.U64()
				if err != nil {
					return nil, err
				}
				nullList = value == 0
			}
			if !nullList {
				return nil, r.Fail("unexpected trailing model-look data")
			}
		}
		return &Record{Kind: "look", Path: path, Entries: entries}, nil
	}
	if kind != 0 {
		return nil, r.Fail("unknown material record type " + strconv.FormatUint(kind, 10))
	}
	header, err := r.Unpack("QQI")
	if err != nil {
		return nil, err
	}
	textureCount, staticCount := header.Uint(0), header.Uint(1)
	record := &Record{Kind: "material", Path: path, Shader: uint32(header.Uint(2)), Inputs: map[uint32][]byte{}}
	if err := countU64(r, textureCount, 4096, "texture count"); err != nil {
		return nil, err
	}
	if err := countU64(r, staticCount, 65536, "static input count"); err != nil {
		return nil, err
	}
	record.Textures = make([]TextureBinding, 0, textureCount)
	for i := uint64(0); i < textureCount; i++ {
		reference, err := r.String()
		if err != nil {
			return nil, err
		}
		binding, err := r.U32()
		if err != nil {
			return nil, err
		}
		record.Textures = append(record.Textures, TextureBinding{reference, binding})
	}
	for i := uint64(0); i < staticCount; i++ {
		fields, err := r.Unpack("II")
		if err != nil {
			return nil, err
		}
		key, length := uint32(fields.Uint(0)), fields.Uint(1)
		if err := countU64(r, length, 1024*1024, "static input byte length"); err != nil {
			return nil, err
		}
		raw, err := r.Take(int(length))
		if err != nil {
			return nil, err
		}
		if _, exists := record.Inputs[key]; !exists {
			record.InputKeys = append(record.InputKeys, key)
		}
		record.Inputs[key] = append([]byte(nil), raw...)
	}
	if r.Remaining() > 0 {
		return nil, r.Fail("unexpected " + strconv.Itoa(r.Remaining()) + " trailing material bytes")
	}
	return record, nil
}
