// Package exporttextures ports owpreview/export_textures.py: portable
// common-PBR texture baking without scene lighting or game shaders.
//
// The input is load_asset()'s registered material/texture manifest. Output maps
// keep explicit UV sets, repeat and color space; incompatible UV layouts are not
// silently flattened. Pixel decoding is shared with the preview, but export uses
// the original image resolution rather than TextureStore's 2048px preview cache.
//
// Output file names are the same as Python's (the identity digests are built
// from json.dumps(..., sort_keys=True, separators=(',', ':')) text, byte for
// byte) and every PNG decodes to the same pixels as Python's (pillow.go). The
// PNG bytes themselves differ: Pillow writes zlib level 6, Go writes
// textures.EncodePNG's faster stream.
//
// Independent materials are baked concurrently by a bounded pool; results are
// published (files written, warnings and progress reported) in Python's order.
package exporttextures

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
	"avolition/gocore/internal/textures"
)

// Roles are the material map roles in baking order (_ROLES).
var Roles = []string{"baseColor", "normal", "roughness", "metalness", "emissive", "alpha", "ao"}

// Materials is model id -> material key -> baked descriptor, the payload
// shape of c4dexport.Materials.
type Materials = ordered.Map[*ordered.Map[*formats.Material]]

// Result is bake_materials' reply.
type Result struct {
	Materials *Materials `json:"materials"`
	Files     []string   `json:"files"`
	Warnings  []string   `json:"warnings"`
}

// Progress receives ("Baking map materials", completed, total) before every
// material and once at the end, like the Python callback.
type Progress func(message string, completed, total int)

// ProgressMessage is the message passed to Progress.
const ProgressMessage = "Baking map materials"

// MaxWorkers bounds how many materials are decoded and baked at once. Each
// worker holds one material's full-resolution images; pixel loops inside a
// material are parallel as well.
var MaxWorkers = min(runtime.NumCPU(), 4)

// processChannels is replaced by tests that count decodes.
var processChannels = textures.ProcessChannels

var errOverflow = errors.New("cannot convert float infinity to integer")

// BakeMaterials is bake_materials(asset, target_tex_dir, name_prefix,
// progress=progress): it returns {materials:{modelId:{key:descriptor}},
// files, warnings}.
//
// Map descriptors contain path, relativePath (tex/...), uvIndex, repeat and
// colorSpace. Color/AO and compatible emission masks are baked in linear
// light. Emission intensity remains an explicit scalar so HDR brightness is
// not clipped into an 8-bit PNG. unbakedMaps marks incompatible shader math;
// consumers should not treat those retained source maps as complete bakes.
// The caller owns staging/publishing the returned files and FBX transaction.
// The asset is never modified.
func BakeMaterials(asset *formats.Asset, targetTexDir, namePrefix string, progress Progress) (*Result, error) {
	target := pypath.Resolve(targetTexDir)
	if err := os.MkdirAll(target, 0o777); err != nil {
		return nil, formats.PythonOSError(err)
	}
	rootPath := asset.TextureRoot
	if rootPath == "" {
		rootPath = pypath.Parent(asset.Path)
	}
	b := &baker{
		root:   pypath.Resolve(rootPath),
		target: target,
		specs:  asset.Textures,
		prefix: sanitizePrefix(namePrefix),
	}
	output := &Result{Materials: ordered.New[*ordered.Map[*formats.Material]](), Files: []string{}, Warnings: []string{}}
	for _, warning := range asset.Warnings {
		if !slices.Contains(output.Warnings, warning) {
			output.Warnings = append(output.Warnings, warning)
		}
	}

	// Enumerate in Python's order; only the first occurrence of an identical
	// source material is baked.
	type entry struct {
		modelID, key string
		material     *formats.Material
		job          int // index into jobs; -1 for a repeat or nil material
		label        string
	}
	var entries []entry
	var jobs []*job
	first := map[string]int{}
	for _, model := range asset.Models {
		if model == nil || model.Materials == nil {
			continue
		}
		var err error
		model.Materials.Range(func(key string, material *formats.Material) bool {
			e := entry{modelID: model.ID, key: key, material: material, job: -1}
			if material != nil {
				var identity string
				identity, err = sourceIdentity(material)
				if err != nil {
					return false
				}
				if index, ok := first[identity]; ok {
					e.job = -2 - index // repeat of jobs[index]
				} else {
					first[identity] = len(jobs)
					e.job = len(jobs)
					name := model.Name
					jobs = append(jobs, &job{material: material,
						label: name + " / " + material.Name, done: make(chan struct{})})
				}
			}
			entries = append(entries, e)
			return true
		})
		if err != nil {
			return nil, err
		}
	}
	total := len(entries)

	stop := make(chan struct{})
	defer close(stop)
	workers := max(1, MaxWorkers)
	b.window = make(chan struct{}, 2*workers)
	go b.run(jobs, workers, stop)

	warn := func(message string) {
		if !slices.Contains(output.Warnings, message) {
			output.Warnings = append(output.Warnings, message)
		}
	}
	baked := make([]*formats.Material, len(jobs))
	completed := 0
	lastModel := ""
	var materials *ordered.Map[*formats.Material]
	for i, e := range entries {
		if i == 0 || e.modelID != lastModel {
			var ok bool
			if materials, ok = output.Materials.Get(e.modelID); !ok {
				materials = ordered.New[*formats.Material]()
				output.Materials.Set(e.modelID, materials)
			}
			lastModel = e.modelID
		}
		if progress != nil {
			progress(ProgressMessage, completed, total)
		}
		completed++
		switch {
		case e.job == -1:
			// A cleared (None) slot; Python would fail on it. Keep it empty.
			materials.Set(e.key, nil)
		case e.job <= -2:
			materials.Set(e.key, baked[-2-e.job].Clone())
		default:
			j := jobs[e.job]
			<-j.done
			b.release()
			if j.err != nil {
				return nil, j.err
			}
			for _, message := range j.warnings {
				warn(message)
			}
			material := j.result
			material.Maps = ordered.New[*formats.MaterialMap]()
			for _, emitted := range j.emitted {
				path, err := writePNG(target, b.prefix+j.digest+"_"+emitted.role, emitted.png)
				if err != nil {
					return nil, err
				}
				mapping := emitted.mapping
				mapping.Path = path
				mapping.RelativePath = "tex/" + pypath.Name(path)
				material.Maps.Set(emitted.role, &mapping)
				if !slices.Contains(output.Files, path) {
					output.Files = append(output.Files, path)
				}
			}
			j.emitted = nil
			materials.Set(e.key, material)
			baked[e.job] = material
		}
	}
	if progress != nil {
		progress(ProgressMessage, completed, total)
	}
	return output, nil
}

// sanitizePrefix is re.sub(r'[^\w.-]+', '_', prefix).strip(' ._')[:48] + '_'.
func sanitizePrefix(prefix string) string {
	var builder strings.Builder
	replacing := false
	for _, r := range prefix {
		if r == '_' || r == '.' || r == '-' || unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			replacing = false
		} else if !replacing {
			builder.WriteByte('_')
			replacing = true
		}
	}
	result := strings.Trim(builder.String(), " ._")
	if utf8.RuneCountInString(result) > 48 {
		result = string([]rune(result)[:48])
	}
	if result == "" {
		return ""
	}
	return result + "_"
}

// ---------------------------------------------------------------------------
// Scheduling: a bounded pool bakes ahead of the in-order consumer.

type emittedMap struct {
	role    string
	mapping formats.MaterialMap
	png     []byte
}

type job struct {
	material *formats.Material
	label    string
	done     chan struct{}

	result   *formats.Material
	digest   string
	emitted  []emittedMap
	warnings []string
	err      error
}

type baker struct {
	root, target, prefix string
	specs                *ordered.Map[*formats.Texture]
	window               chan struct{}
}

// run bakes jobs with MaxWorkers goroutines, at most 2*MaxWorkers ahead of
// the consumer, which calls release after taking each result.
func (b *baker) run(jobs []*job, workers int, stop chan struct{}) {
	sem := make(chan struct{}, workers)
	for _, j := range jobs {
		select {
		case b.window <- struct{}{}:
		case <-stop:
			return
		}
		select {
		case sem <- struct{}{}:
		case <-stop:
			return
		}
		go func(j *job) {
			defer func() { <-sem }()
			defer close(j.done)
			b.bake(j)
		}(j)
	}
}

func (b *baker) release() { <-b.window }

// ---------------------------------------------------------------------------
// One material.

type source struct {
	image    *textures.Image
	identity string // the texture's identity JSON (spec minus path, + sourceContent)
}

// mapping is _mapping(value): uvIndex, repeat and colorSpace.
func mapping(value *formats.MaterialMap) formats.MaterialMap {
	colorSpace := value.ColorSpace
	if colorSpace == "" {
		colorSpace = "linear"
	}
	return formats.MaterialMap{UVIndex: value.UVIndex, Repeat: value.Repeat, ColorSpace: colorSpace}
}

func sameUV(a, b formats.MaterialMap) bool {
	return a.UVIndex == b.UVIndex && math.Abs(a.Repeat[0]-b.Repeat[0]) < 1e-7 && math.Abs(a.Repeat[1]-b.Repeat[1]) < 1e-7
}

// finite is _finite(value, default=1.0).
func finite(value float64) float64 {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 1
	}
	return value
}

// color is _color(value, default) for a three-component value.
func color(value [3]float64) [3]float64 {
	var out [3]float64
	for i, v := range value {
		out[i] = max(0, finite(v))
	}
	return out
}

func combinedSize(images ...*textures.Image) (int, int, error) {
	width, height := 0, 0
	for _, image := range images {
		if image != nil {
			width, height = max(width, image.Width), max(height, image.Height)
		}
	}
	if width*height > textures.MaxSourcePixels {
		return 0, 0, errors.New("Combined texture dimensions exceed the 64-megapixel export limit.")
	}
	return width, height, nil
}

func (b *baker) bake(j *job) {
	defer func() {
		if r := recover(); r != nil {
			j.err = fmt.Errorf("%v", r)
		}
	}()
	j.err = b.bakeMaterial(j)
}

func (b *baker) bakeMaterial(j *job) error {
	sourceMaterial := j.material
	material := sourceMaterial.Clone()
	var originalMaps *ordered.Map[*formats.MaterialMap]
	if sourceMaterial.Maps != nil {
		originalMaps = sourceMaterial.Maps
	} else {
		originalMaps = ordered.New[*formats.MaterialMap]()
	}
	original := func(role string) *formats.MaterialMap {
		value, _ := originalMaps.Get(role)
		return value
	}
	warn := func(message string) {
		if !slices.Contains(j.warnings, message) {
			j.warnings = append(j.warnings, message)
		}
	}
	images := map[string]*source{}
	maps := ordered.New[formats.MaterialMap]() // successful reads in _ROLES order
	cache := map[string]*source{}
	for _, role := range Roles {
		reference, ok := originalMaps.Get(role)
		if !ok {
			continue
		}
		if reference == nil {
			warn(j.label + ": " + role + " map could not be exported: texture is not registered in this asset")
			continue
		}
		m := mapping(reference)
		read, err := b.readMap(reference.TextureID, cache)
		if err != nil {
			warn(j.label + ": " + role + " map could not be exported: " + err.Error())
			continue
		}
		images[role] = read
		maps.Set(role, m)
	}

	// Identity includes all material controls and source contents, while
	// excluding local paths and ephemeral registry IDs.
	digest, err := materialDigest(sourceMaterial, maps, images)
	if err != nil {
		return err
	}
	j.digest = digest

	image := func(role string) *textures.Image {
		if s := images[role]; s != nil {
			return s.image
		}
		return nil
	}
	type output struct {
		role    string
		image   *textures.Image
		mapping formats.MaterialMap
	}
	var emitted []output
	has := func(role string) bool {
		for _, e := range emitted {
			if e.role == role {
				return true
			}
		}
		return false
	}

	baseFactor := color(material.BaseColor)
	baseImage := image("baseColor")
	baseMap, hasBase := maps.Get("baseColor")
	baseSpace := "linear"
	if hasBase {
		baseSpace = baseMap.ColorSpace
	}
	ao := image("ao")
	if baseImage != nil || ao != nil {
		aoMap, _ := maps.Get("ao")
		m := aoMap
		if hasBase {
			m = baseMap
		}
		var masks []*textures.Image
		if ao != nil {
			if !hasBase || sameUV(baseMap, aoMap) {
				masks = append(masks, ao)
			} else {
				warn(j.label + ": AO uses a different UV set or repeat; retained as a separate AO map instead of baking into base color.")
			}
		}
		width, height, err := combinedSize(append([]*textures.Image{baseImage}, masks...)...)
		if err != nil {
			return err
		}
		m.ColorSpace = "srgb"
		emitted = append(emitted, output{"baseColor", compositeColor(baseImage, baseSpace, baseFactor, masks, width, height, 1), m})
		material.BaseColor = [3]float64{1, 1, 1}
		if len(masks) > 0 {
			maps.Delete("ao")
		}
	}

	if emissive := image("emissive"); emissive != nil {
		emitMap, _ := maps.Get("emissive")
		tint := color(material.Emissive)
		intensity := 1.0
		if material.EmissiveIntensity != nil {
			intensity = *material.EmissiveIntensity
		}
		intensity = max(0, finite(intensity))
		if material.EmissiveMode == "baseColorMask" {
			compatible := !hasBase || sameUV(baseMap, emitMap)
			useAO := material.EmissiveUsesAO != nil && *material.EmissiveUsesAO && ao != nil
			if useAO {
				compatible = compatible && sameUV(mapping(original("ao")), emitMap)
			}
			if compatible {
				var factor [3]float64
				for i := range factor {
					factor[i] = baseFactor[i] * tint[i]
				}
				gain := max(1, factor[0], factor[1], factor[2])
				masks := []*textures.Image{emissive}
				if useAO {
					masks = append(masks, ao)
				}
				width, height, err := combinedSize(append([]*textures.Image{baseImage}, masks...)...)
				if err != nil {
					return err
				}
				m := emitMap
				m.ColorSpace = "srgb"
				emitted = append(emitted, output{"emissive", compositeColor(baseImage, baseSpace, factor, masks, width, height, gain), m})
				material.Emissive = [3]float64{1, 1, 1}
				value := intensity * gain
				material.EmissiveIntensity = &value
				material.EmissiveMode = ""
				material.EmissiveUsesAO = nil
			} else {
				material.UnbakedMaps = append(material.UnbakedMaps, "emissive")
				warn(j.label + ": emission mask, base color or AO uses different UV coordinates; retained emission mask requires the source shader and is not a baked color map.")
			}
		} else {
			gain := max(1, tint[0], tint[1], tint[2])
			m := emitMap
			m.ColorSpace = "srgb"
			emitted = append(emitted, output{"emissive", compositeColor(emissive, emitMap.ColorSpace, tint, nil,
				emissive.Width, emissive.Height, gain), m})
			material.Emissive = [3]float64{1, 1, 1}
			value := intensity * gain
			material.EmissiveIntensity = &value
		}
	}

	for _, role := range []string{"roughness", "metalness"} {
		if scalar := image(role); scalar != nil {
			factor := material.Roughness
			if role == "metalness" {
				factor = material.Metalness
			}
			scaled, err := scaleScalar(scalar, max(0, finite(factor)))
			if err != nil {
				return err
			}
			m, _ := maps.Get(role)
			emitted = append(emitted, output{role, scaled, m})
			if role == "roughness" {
				material.Roughness = 1
			} else {
				material.Metalness = 1
			}
		}
	}
	maps.Range(func(role string, m formats.MaterialMap) bool {
		if !has(role) {
			emitted = append(emitted, output{role, image(role), m})
		}
		return true
	})

	// Encode in parallel; the consumer writes the files in order.
	j.emitted = make([]emittedMap, len(emitted))
	var wg sync.WaitGroup
	for i, e := range emitted {
		j.emitted[i] = emittedMap{role: e.role, mapping: e.mapping}
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.emitted[i].png = textures.EncodePNG(e.image)
		}()
	}
	wg.Wait()
	material.Maps = nil
	j.result = material
	return nil
}

// readMap is read_map: validate, read, decode and process one texture.
func (b *baker) readMap(textureID string, cache map[string]*source) (*source, error) {
	var spec *formats.Texture
	if b.specs != nil {
		spec, _ = b.specs.Get(textureID)
	}
	if spec == nil {
		return nil, errors.New("texture is not registered in this asset")
	}
	if cached := cache[textureID]; cached != nil {
		return cached, nil
	}
	path := pypath.Resolve(spec.Path)
	if !pypath.IsRelativeTo(path, b.root) {
		return nil, errors.New("texture reference is outside the export root")
	}
	if !textures.ImageExtensions[strings.ToLower(pypath.Suffix(path))] {
		return nil, errors.New("unsupported texture image format")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	if info.Size() > textures.MaxFileBytes {
		return nil, errors.New("texture exceeds the 128 MB source limit")
	}
	// Digest exactly the immutable bytes that are decoded, so stale caches or
	// a source modified in place cannot reuse the previous exported name.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	sum := sha256.Sum256(raw)
	rgba, err := textures.DecodeImage(raw, func(width, height int) error {
		if width*height > textures.MaxSourcePixels {
			return errors.New("texture exceeds the 64-megapixel source limit")
		}
		return nil
	})
	if err != nil {
		if err.Error() == "cannot identify image file" {
			// Python names the in-memory stream it decoded.
			return nil, fmt.Errorf("cannot identify image file <_io.BytesIO object at %p>", &raw)
		}
		return nil, err
	}
	image, err := processChannels(rgba, textures.Spec{Path: spec.Path, Channel: spec.Channel,
		Transform: spec.Transform, InputColorSpace: spec.InputColorSpace})
	if err != nil {
		return nil, err
	}
	identity := object{
		{"channel", pyString(spec.Channel)},
		{"inputColorSpace", pyString(spec.InputColorSpace)},
		{"sourceContent", pyString(hex.EncodeToString(sum[:]))},
		{"transform", pyString(spec.Transform)},
	}
	result := &source{image: image, identity: identity.json()}
	cache[textureID] = result
	return result, nil
}

// ---------------------------------------------------------------------------
// Identities: json.dumps(value, sort_keys=True, separators=(',', ':'),
// allow_nan=False) text (ensure_ascii=True).

type member struct {
	key   string
	value string
}

type object []member

func (o object) json() string {
	sorted := slices.Clone(o)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].key < sorted[b].key })
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range sorted {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(pyString(m.key))
		buf.WriteByte(':')
		buf.WriteString(m.value)
	}
	buf.WriteByte('}')
	return buf.String()
}

func pyString(s string) string { return string(pyjson.AppendString(nil, s, true)) }

type floatError struct{ value float64 }

func (e *floatError) Error() string {
	repr := "nan"
	if math.IsInf(e.value, 1) {
		repr = "inf"
	} else if math.IsInf(e.value, -1) {
		repr = "-inf"
	}
	return "Out of range float values are not JSON compliant: " + repr
}

func pyFloat(value float64, err *error) string {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		if *err == nil {
			*err = &floatError{value}
		}
		return "null"
	}
	return pyjson.FloatRepr(value)
}

func pyFloats(values []float64, err *error) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = pyFloat(v, err)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func pyBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// controls are the material's keys other than maps and sourcePath.
func controls(m *formats.Material, err *error) object {
	o := object{
		{"name", pyString(m.Name)},
		{"shaderId", strconv.FormatUint(uint64(m.ShaderID), 10)},
		{"baseColor", pyFloats(m.BaseColor[:], err)},
		{"roughness", pyFloat(m.Roughness, err)},
		{"metalness", pyFloat(m.Metalness, err)},
		{"emissive", pyFloats(m.Emissive[:], err)},
		{"opacity", pyFloat(m.Opacity, err)},
		{"alphaMode", pyString(m.AlphaMode)},
		{"alphaCutoff", pyFloat(m.AlphaCutoff, err)},
		{"doubleSided", pyBool(m.DoubleSided)},
	}
	if m.EmissiveIntensity != nil {
		o = append(o, member{"emissiveIntensity", pyFloat(*m.EmissiveIntensity, err)})
	}
	if m.EmissiveMode != "" {
		o = append(o, member{"emissiveMode", pyString(m.EmissiveMode)})
	}
	if m.EmissiveUsesAO != nil {
		o = append(o, member{"emissiveUsesAO", pyBool(*m.EmissiveUsesAO)})
	}
	if m.UnbakedMaps != nil {
		parts := make([]string, len(m.UnbakedMaps))
		for i, v := range m.UnbakedMaps {
			parts[i] = pyString(v)
		}
		o = append(o, member{"unbakedMaps", "[" + strings.Join(parts, ",") + "]"})
	}
	return o
}

// sourceIdentity is json.dumps(source_material, ...): the key under which a
// completed bake is reused within one invocation.
func sourceIdentity(m *formats.Material) (string, error) {
	var err error
	o := controls(m, &err)
	o = append(o, member{"sourcePath", pyString(m.SourcePath)})
	maps := object{}
	if m.Maps != nil {
		m.Maps.Range(func(role string, value *formats.MaterialMap) bool {
			if value == nil {
				maps = append(maps, member{role, "null"})
				return true
			}
			entry := object{
				{"textureId", pyString(value.TextureID)},
				{"uvIndex", strconv.Itoa(value.UVIndex)},
				{"colorSpace", pyString(value.ColorSpace)},
				{"repeat", pyFloats(value.Repeat[:], &err)},
			}
			if value.Path != "" {
				entry = append(entry, member{"path", pyString(value.Path)})
			}
			if value.RelativePath != "" {
				entry = append(entry, member{"relativePath", pyString(value.RelativePath)})
			}
			if value.Source != "" {
				entry = append(entry, member{"source", pyString(value.Source)})
			}
			maps = append(maps, member{role, entry.json()})
			return true
		})
	}
	o = append(o, member{"maps", maps.json()})
	if err != nil {
		return "", err
	}
	return o.json(), nil
}

// materialDigest is the 24-hex-digit sha256 of the material identity: its
// controls plus, for every read map, its mapping and source identity.
func materialDigest(m *formats.Material, maps *ordered.Map[formats.MaterialMap], images map[string]*source) (string, error) {
	var err error
	o := controls(m, &err)
	identities := object{}
	for _, role := range Roles {
		read := images[role]
		if read == nil {
			continue
		}
		value, _ := maps.Get(role)
		entry := object{
			{"uvIndex", strconv.Itoa(value.UVIndex)},
			{"repeat", pyFloats(value.Repeat[:], &err)},
			{"colorSpace", pyString(value.ColorSpace)},
			{"source", read.identity},
		}
		identities = append(identities, member{role, entry.json()})
	}
	o = append(o, member{"maps", identities.json()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(o.json()))
	return hex.EncodeToString(sum[:])[:24], nil
}

// ---------------------------------------------------------------------------

// writePNG is _write_png: exclusive creation protects an existing unrelated
// file, even when it has the expected hash-derived name. Repeating this bake
// reuses identical data.
func writePNG(folder, stem string, content []byte) (string, error) {
	for index := 1; index <= 10000; index++ {
		name := stem
		if index != 1 {
			name += "_" + strconv.Itoa(index)
		}
		candidate := pypath.Join(folder, name+".png")
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			_, err = file.Write(content)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				os.Remove(candidate)
				return "", formats.PythonOSError(err)
			}
			return candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", formats.PythonOSError(err)
		}
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			existing, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return "", formats.PythonOSError(readErr)
			}
			if bytes.Equal(existing, content) {
				return candidate, nil
			}
		}
	}
	return "", errors.New("Too many conflicting exported texture filenames.")
}
