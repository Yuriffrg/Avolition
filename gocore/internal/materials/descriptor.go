package materials

import (
	"slices"
	"strconv"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

func ptr[T any](value T) *T { return &value }

// Descriptor maps a material record to its preview descriptor (Python's
// descriptor). Results are cached per resolved path and shared.
func (r *Resolver) Descriptor(path string) (*formats.Material, error) {
	path = r.resolve(path)
	if cached, ok := r.descriptors[path]; ok {
		return cached, nil
	}
	record, err := r.read(path)
	if err != nil {
		return nil, err
	}
	name := pypath.Name(path)
	if record.Kind != "material" {
		return nil, &formats.FormatError{Message: name + ": a material look cannot be used as a material record"}
	}
	shader := record.Shader
	result := &formats.Material{
		Name: pypath.Stem(path), SourcePath: path, ShaderID: shader,
		BaseColor: [3]float64{0.62, 0.62, 0.62}, Roughness: 0.7, Metalness: 0,
		Emissive: [3]float64{0, 0, 0}, Opacity: 1,
		AlphaMode: "OPAQUE", AlphaCutoff: 0.5, DoubleSided: true, Maps: ordered.New[*formats.MaterialMap](),
	}
	var unsupported []uint32
	_, skinInput := record.Inputs[4081294361]
	ow2Skin := shader == 167 || (shader == 43 && skinInput)
	complexEmission := shader == 36 || shader == 37 || shader == 44 || shader == 50 || shader == 51 || shader == 54 ||
		(shader == 43 && !ow2Skin)
	strength := 16.0
	switch {
	case shader == 38:
		strength = 0
	case ow2Skin:
		strength = 32
	case shader == 34 || shader == 56:
		strength = 128
	}
	for _, key := range EmissionStrength {
		strength = r.inputFloat(record, key, strength)
	}
	strength = min(10000.0, max(0.0, strength))
	if _, ok := record.Inputs[Opacity]; ok {
		r.Warn(name + ": game opacity control is not reproduced; texture alpha is used for preview.")
	}
	if shader == 221 {
		result.Roughness = 0
	}
	bindings := make(map[uint32]bool, len(record.Textures))
	for _, texture := range record.Textures {
		bindings[texture.Binding] = true
	}

	addMap := func(role, texture string, key uint32, channel, transform, colorSpace, sourceSpace string) bool {
		if _, exists := result.Maps.Get(role); exists {
			r.Warn(name + ": additional " + role + " layer is not supported; using the first mapped layer.")
			return false
		}
		uv := int64(0)
		if key == 3335614873 {
			uv = 1
		}
		if key == 3166598269 {
			// Blender's UVMap names start at 1; Three's channels at 0.
			uv = r.inputUint(record, UVEmission, 1) - 1
		}
		if role == "alpha" && key == 1239794147 && shader == 51 && bindings[4101268840] && bindings[758934576] {
			uv = 1
		}
		if uv != 0 && uv != 1 {
			r.Warn(name + ": unsupported UV channel " + strconv.FormatInt(uv, 10) + " for " + role + "; map skipped.")
			return false
		}
		repeat := [2]float64{1, 1}
		if key == 2903569922 || key == 548341454 || key == 378934698 {
			repeat = r.inputFloat2(record, 1883253226, repeat)
			if _, ok := record.Inputs[1883253226]; ok {
				r.Warn(name + ": preview honors the known Basic UV Scale metadata; Blender versions may ignore this control.")
			}
			if abs(repeat[0]) > 10000 || abs(repeat[1]) > 10000 {
				r.Warn(name + ": extreme basic UV scale ignored.")
				repeat = [2]float64{1, 1}
			}
		}
		result.Maps.Set(role, &formats.MaterialMap{
			TextureID: r.textureID(texture, channel, transform, sourceSpace),
			UVIndex:   int(uv), ColorSpace: colorSpace, Repeat: repeat,
		})
		return true
	}

	for _, binding := range record.Textures {
		key := binding.Binding
		supported := BaseColor[key] || Normal[key] || PBR[key] || Emission[key] || Alpha[key] || AO[key] ||
			key == CombinedDecal || key == HairRoughness
		if !supported {
			unsupported = append(unsupported, key)
			continue
		}
		texture := r.Resolve(path, binding.Reference, false, "")
		if texture == "" {
			continue
		}
		if suffix := pypath.Suffix(texture); !SupportedImages[strings.ToLower(suffix)] {
			r.Warn(name + ": unsupported texture format " + suffix + " for " + pypath.Name(texture) + ".")
			continue
		}
		if BaseColor[key] || key == CombinedDecal {
			addMap("baseColor", texture, key, "rgb", "none", "srgb", "srgb")
			result.BaseColor = [3]float64{1, 1, 1}
		}
		if AlbedoAlpha[key] {
			addMap("alpha", texture, key, "a", "none", "linear", "linear")
			if key == 67369114 {
				result.AlphaMode = "BLEND"
			} else {
				result.AlphaMode = "MASK"
			}
		}
		if Normal[key] {
			// Matches OWM: Unpack Blue Channel's stereographic decoding;
			// this is not the usual BC5/sqrt reconstruction.
			addMap("normal", texture, key, "rgb", "ow-normal", "linear", "linear")
		}
		if PBR[key] {
			for _, pbr := range pbrChannels {
				if shader == 221 || (pbr.role == "metalness" && ow2Skin) {
					continue
				}
				addMap(pbr.role, texture, key, pbr.channel, pbr.transform, "linear", "linear")
				if pbr.role == "roughness" {
					result.Roughness = 1
				} else {
					result.Metalness = 1
				}
			}
		}
		if Emission[key] || key == CombinedDecal {
			switch {
			case complexEmission:
				r.Warn(name + ": layered shader emission was omitted from the preview.")
			case key == 1493127177:
				if addMap("emissive", texture, key, "rgb", "none", "srgb", "srgb") {
					result.Emissive = [3]float64{1, 1, 1}
					result.EmissiveIntensity = ptr(strength)
				}
			default:
				source := "linear"
				if key == CombinedDecal {
					source = "srgb"
				}
				if addMap("emissive", texture, key, "luminance", "none", "linear", source) {
					result.Emissive = [3]float64{1, 1, 1}
					result.EmissiveIntensity = ptr(strength)
					result.EmissiveMode = "baseColorMask"
					result.EmissiveUsesAO = ptr(!(ow2Skin || shader == 217 || shader == 221))
				}
			}
		}
		if Alpha[key] || key == CombinedDecal {
			channel := "luminance"
			if shader == 34 || shader == 56 {
				channel = "r"
			}
			source := "linear"
			if key == CombinedDecal {
				source = "srgb"
			}
			addMap("alpha", texture, key, channel, "none", "linear", source)
			result.AlphaMode = "MASK"
		}
		if AO[key] {
			// Three's AO input is scalar; Overwatch's general AO can tint
			// the base color as RGB. The red-channel preview is approximate.
			addMap("ao", texture, key, "r", "none", "linear", "linear")
			if key == 3335614873 {
				r.Warn(name + ": colored ambient occlusion is approximated with its red channel.")
			}
		}
		if key == HairRoughness {
			addMap("roughness", texture, key, "luminance", "none", "linear", "linear")
			result.Roughness = 1
		}
		if key == 2903569922 && (shader == 44 || shader == 217) {
			addMap("ao", texture, key, "a", "none", "linear", "linear")
		}
	}
	if len(unsupported) > 0 {
		slices.Sort(unsupported)
		unsupported = slices.Compact(unsupported)
		values := make([]string, len(unsupported))
		for i, value := range unsupported {
			values[i] = strconv.FormatUint(uint64(value), 10)
		}
		r.Warn(name + ": unsupported texture bindings " + strings.Join(values, ", ") + " were omitted.")
	}
	switch shader {
	case 34, 36, 37, 38, 43, 44, 50, 51, 54, 56, 167, 217, 221:
		r.Warn(name + ": shader " + strconv.FormatUint(uint64(shader), 10) +
			" is approximated with common PBR maps; game-specific layers, refraction and subsurface effects are not reproduced.")
	}
	r.descriptors[path] = result
	return result, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ForModel maps the model look's uint64 material keys to per-material
// descriptors (Python's for_model). It sets model.MaterialLook to the
// effective look reference (or null). entityLook overrides the model
// header's look. With partial (map looks), a present null assignment yields
// a nil descriptor (BLMaterial.bindModelLook explicitly clears it, unlike an
// absent override slot) and unresolved slots are not reported.
func (r *Resolver) ForModel(model *formats.Model, entityLook string, partial bool) *ordered.Map[*formats.Material] {
	result := ordered.New[*formats.Material]()
	modelPath := r.resolve(model.Path)
	reference := ""
	if model.MaterialPath != nil {
		reference = *model.MaterialPath
	}
	if entityLook != "" {
		lookName := pypath.Name(cleanReference(entityLook))
		if strings.HasSuffix(strings.ToLower(lookName), ".owmat") {
			lookName = lookName[:len(lookName)-6]
		}
		if strings.HasSuffix(strings.ToUpper(lookName), ".01A") {
			lookName = lookName[:len(lookName)-4]
		}
		reference = pypath.Join("ModelLooks", lookName+".owmat")
	}
	if reference == "" {
		model.MaterialLook = formats.Null[string]()
		r.Warn(pypath.Name(modelPath) + ": no material look reference; using neutral shading.")
		return result
	}
	model.MaterialLook = formats.Some(reference)
	lookPath := r.Resolve(modelPath, reference, false, ".owmat")
	if lookPath == "" {
		return result
	}
	look, err := r.read(lookPath)
	if err != nil {
		r.Warn("Skipped model look " + pypath.Name(lookPath) + ": " + formats.PythonOSError(err).Error())
		return ordered.New[*formats.Material]()
	}
	if look.Kind != "look" {
		r.Warn(pypath.Name(lookPath) + ": expected a model look; no material-key assignments were inferred.")
		return result
	}
	required := make(map[string]bool, len(model.Meshes))
	for _, mesh := range model.Meshes {
		required[mesh.MaterialKey] = true
	}
	for _, entry := range look.Entries {
		if !required[entry.Key] {
			continue
		}
		if partial && entry.Reference == "" {
			result.Set(entry.Key, nil)
			continue
		}
		materialPath := r.Resolve(lookPath, entry.Reference, true, ".owmat")
		if materialPath == "" {
			continue
		}
		descriptor, err := r.Descriptor(materialPath)
		if err != nil {
			r.Warn("Skipped material " + pypath.Name(materialPath) + ": " + formats.PythonOSError(err).Error())
			continue
		}
		if _, exists := result.Get(entry.Key); !exists {
			result.Set(entry.Key, descriptor)
		}
	}
	if !partial {
		var missing []string
		for key := range required {
			if _, ok := result.Get(key); !ok {
				missing = append(missing, key)
			}
		}
		slices.Sort(missing)
		for _, key := range missing {
			r.Warn(pypath.Name(modelPath) + ": material slot " + key + " is unresolved; using neutral shading for that mesh.")
		}
	}
	return result
}
