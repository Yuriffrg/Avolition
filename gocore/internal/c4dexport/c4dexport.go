// Package c4dexport is the Go port of owpreview/c4d_export.py: it prepares
// native Cinema 4D scene data outside Cinema's Python runtime. The payload is
// JSON consumed by the unchanged Cinema 4D plugin (owpreview/c4d_scene.py,
// bundled into the Avolition C4D Bridge), so its keys and values follow the
// Python dicts exactly.
//
// The package also ports the two pure helpers of c4d_scene.py that the
// backend imports (map_export.py uses them): _ordered_graph (OrderedGraph,
// with _matrix_values as MatrixValues) and _asset_models (AssetModels).
// c4d_scene.py itself stays Python; it runs inside Cinema 4D.
//
// # Python → Go map
//
//	select_model_asset           SelectModelAsset
//	_automatic_vertex_normals    automaticVertexNormals
//	_normalized_normal_asset     NormalizedNormalAsset (+ NormalizeMeshLists)
//	make_payload                 MakePayload
//	make_set_payload             MakeSetPayload
//	c4d_scene._matrix_values     MatrixValues
//	c4d_scene._ordered_graph     OrderedGraph
//	c4d_scene._asset_models      AssetModels
//
// # Differences
//
// Normalized normals stay float32 (formats.Mesh.Normals); Python sends the
// float64 values. Cinema 4D's NormalTag stores 16-bit components, so the
// native scene is identical. Matrix products, inverses and norms are plain
// float64 loops; NumPy's BLAS/LAPACK kernels may differ in the last bit, so
// baked tracks agree with Python to ~1e-12.
package c4dexport

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"

	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

// Materials is the payload's materials: model id → material key → descriptor
// (export_textures.bake_materials()['materials']).
type Materials = ordered.Map[*ordered.Map[*formats.Material]]

// MaxSampledBoneFrames limits (frames × bones) of a native export.
const MaxSampledBoneFrames = 250_000

// Track is one baked joint: local matrices (column-major) for every frame.
type Track struct {
	ModelID   string         `json:"modelId"`
	BoneIndex int            `json:"boneIndex"`
	Frames    []formats.Mat4 `json:"frames"`
}

// AssignmentSummary describes one clip of an animation set.
type AssignmentSummary struct {
	ModelID            string   `json:"modelId"`
	ClipName           string   `json:"clipName"`
	FPS                float64  `json:"fps"`
	LastFrame          int64    `json:"lastFrame"`
	DurationSeconds    float64  `json:"durationSeconds"`
	MatchedBones       int      `json:"matchedBones"`
	UnmatchedBoneNames []string `json:"unmatchedBoneNames"`
}

// Animation is the payload's animation. DurationSeconds and Assignments are
// present only in set payloads (make_set_payload).
type Animation struct {
	FPS             float64             `json:"fps"`
	LastFrame       int64               `json:"lastFrame"`
	DurationSeconds *float64            `json:"durationSeconds,omitempty"`
	Tracks          []Track             `json:"tracks"`
	Assignments     []AssignmentSummary `json:"assignments,omitempty"`
	MatchedBones    int                 `json:"matchedBones"`
}

// Payload is the bridge protocol 1 scene payload.
type Payload struct {
	Version        int            `json:"version"`
	Asset          *formats.Asset `json:"asset"`
	Materials      *Materials     `json:"materials"`
	Warnings       []string       `json:"warnings"`
	IncludeMesh    bool           `json:"includeMesh"`
	Name           string         `json:"name"`
	MotionClip     bool           `json:"motionClip"`
	MotionClipName string         `json:"motionClipName"`
	Gameplay1P     bool           `json:"gameplay1P"`
	HeroName       string         `json:"heroName"`
	Animation      Animation      `json:"animation"`
}

// RequiredBridgeCapabilities prevents older companions creating separate
// sources per skeleton instead of one combined source named with the nickname.
func (p *Payload) RequiredBridgeCapabilities() []string {
	var required []string
	if p.MotionClip {
		required = append(required, "combinedMotionClips", "exactFrameTimes")
	}
	if p.Gameplay1P {
		required = append(required, "optimizedGameplay1P", "povHeightZero")
	}
	return required
}

func valueError(message string) error { return errors.New(message) }

func orEmpty(m *Materials) *Materials {
	if m == nil {
		return ordered.New[*ordered.Map[*formats.Material]]()
	}
	return m
}

func copyStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func hasParent(id *string) bool { return id != nil && *id != "" }

// SelectModelAsset is select_model_asset: project an entity to one model and
// its complete native skeleton.
//
// This is an object export, not a subtree export: attached models and the
// parent's rig are excluded. An attached object's exported root receives its
// existing native hierarchy's *rest* world matrix, so detaching it does not
// move the mesh or change any joint's local bind/animation transforms. Parent
// animation is deliberately not baked into this independent object's clip.
//
// Call before texture baking to avoid generating maps for unrelated objects.
// The projection owns its selected data and never changes the loaded asset.
func SelectModelAsset(asset *formats.Asset, modelID string) (*formats.Asset, error) {
	var original *formats.Model
	matches := 0
	for _, model := range asset.Models {
		if model != nil && model.ID == modelID {
			original = model
			matches++
		}
	}
	if matches != 1 {
		return nil, valueError("Choose a valid object from the loaded entity for C4D export.")
	}
	graph := asset.ExportHierarchy
	if len(graph) == 0 {
		return nil, valueError("Entity hierarchy metadata is missing. Reload the entity before C4D export.")
	}
	byID := make(map[string]*formats.Node, len(graph))
	for i := range graph {
		byID[graph[i].ID] = &graph[i]
	}
	if len(byID) != len(graph) {
		return nil, valueError("The export hierarchy contains duplicate node identifiers.")
	}
	var selected []formats.Node
	selectedIDs := map[string]bool{}
	for i := range graph {
		if graph[i].ModelID == modelID {
			selected = append(selected, graph[i].Clone())
			selectedIDs[graph[i].ID] = true
		}
	}
	if len(selected) == 0 {
		return nil, valueError("The selected object is missing from the export hierarchy. Reload the entity.")
	}
	worlds := map[string]exportpose.Mat4{}
	restWorld := func(nodeID string) (exportpose.Mat4, error) {
		var pending []string
		seen := map[string]bool{}
		current, has := nodeID, true
		for has && current != "" {
			if _, done := worlds[current]; done {
				break
			}
			node, ok := byID[current]
			if !ok || seen[current] {
				return exportpose.Mat4{}, valueError("The export hierarchy contains a missing parent or a cycle.")
			}
			pending = append(pending, current)
			seen[current] = true
			has = node.ParentID != nil
			if has {
				current = *node.ParentID
			}
		}
		world := exportpose.Identity4
		if has && current != "" {
			world = worlds[current]
		}
		for i := len(pending) - 1; i >= 0; i-- {
			ident := pending[i]
			values := byID[ident].Matrix
			for _, v := range values {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return exportpose.Mat4{}, valueError("The export hierarchy contains an invalid rest transform.")
				}
			}
			matrix := exportpose.FromColumnMajor(values)
			// np.allclose(matrix[3], [0, 0, 0, 1], atol=1e-7, rtol=0)
			for j, want := range [4]float64{0, 0, 0, 1} {
				if !(math.Abs(matrix[3][j]-want) <= 1e-7) {
					return exportpose.Mat4{}, valueError("The export hierarchy contains an invalid affine rest transform.")
				}
			}
			world = world.Mul(&matrix)
			worlds[ident] = world
		}
		return worlds[nodeID], nil
	}
	for i := range selected {
		node := &selected[i]
		if node.ParentID != nil && selectedIDs[*node.ParentID] {
			continue
		}
		matrix, err := restWorld(node.ID)
		if err != nil {
			return nil, err
		}
		if _, _, _, err := exportpose.Decompose(&matrix); err != nil {
			return nil, valueError("The selected object has a rest attachment transform that Cinema 4D " +
				"cannot preserve without its parent hierarchy.")
		}
		node.ParentID = nil
		node.Matrix = matrix.ColumnMajor()
	}

	model := original.Clone()
	model.ParentID, model.AttachmentBone, model.AttachmentMatrix = nil, nil, formats.Identity
	materialKeys := map[string]bool{}
	for _, mesh := range model.Meshes {
		if mesh != nil {
			materialKeys[mesh.MaterialKey] = true
		}
	}
	materials := ordered.New[*formats.Material]()
	textureIDs := map[string]bool{}
	if model.Materials != nil {
		model.Materials.Range(func(key string, value *formats.Material) bool {
			if materialKeys[key] {
				materials.Set(key, value)
				if value != nil && value.Maps != nil {
					value.Maps.Range(func(_ string, mapping *formats.MaterialMap) bool {
						if mapping != nil && mapping.TextureID != "" {
							textureIDs[mapping.TextureID] = true
						}
						return true
					})
				}
			}
			return true
		})
	}
	model.Materials = materials
	textures := ordered.New[*formats.Texture]()
	if asset.Textures != nil {
		asset.Textures.Range(func(key string, value *formats.Texture) bool {
			if textureIDs[key] {
				textures.Set(key, value.Clone())
			}
			return true
		})
	}
	result := *asset
	result.Models = []*formats.Model{model}
	result.ExportHierarchy = selected
	result.Textures = textures
	result.Warnings = copyStrings(asset.Warnings)
	return &result, nil
}

// automaticVertexNormals is _automatic_vertex_normals: Blender's zero custom
// normals use the incident smooth surface normal.
//
// Accumulate corner-angle-weighted face directions by vertex index, never by
// position: coincident split vertices must keep their own shading boundaries.
// Only visit faces touching missing normals; leave geometry and valid normals
// untouched. Thin nondegenerate triangles are valid, including map trim.
// It returns the directions of the missing vertices in vertex order and the
// number of vertices without a nondegenerate incident polygon.
func automaticVertexNormals(mesh *formats.Mesh, missing []bool) ([][3]float64, int, error) {
	label := mesh.Name
	invalid := valueError(label + ": invalid geometry for automatic normals.")
	if len(mesh.Positions)%3 != 0 {
		return nil, 0, fmt.Errorf("cannot reshape array of size %d into shape (3)", len(mesh.Positions))
	}
	count := len(mesh.Positions) / 3
	positions := make([][3]float64, count)
	for i := range positions {
		for c := 0; c < 3; c++ {
			v := float64(mesh.Positions[i*3+c])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, 0, invalid
			}
			positions[i][c] = v
		}
	}
	if len(mesh.Indices)%3 != 0 {
		return nil, 0, invalid
	}
	for _, index := range mesh.Indices {
		if int(index) >= count {
			return nil, 0, invalid
		}
	}
	type face struct {
		v         [3]int
		points    [3][3]float64
		direction [3]float64
		area      float64
	}
	var faces []face
	for f := 0; f+2 < len(mesh.Indices); f += 3 {
		v := [3]int{int(mesh.Indices[f]), int(mesh.Indices[f+1]), int(mesh.Indices[f+2])}
		if !missing[v[0]] && !missing[v[1]] && !missing[v[2]] {
			continue
		}
		p := [3][3]float64{positions[v[0]], positions[v[1]], positions[v[2]]}
		a, b := sub(p[1], p[0]), sub(p[2], p[0])
		cross := [3]float64{
			float64(a[1]*b[2]) - float64(a[2]*b[1]),
			float64(a[2]*b[0]) - float64(a[0]*b[2]),
			float64(a[0]*b[1]) - float64(a[1]*b[0]),
		}
		area := norm(cross)
		if math.IsNaN(area) || math.IsInf(area, 0) || !(area > 0) {
			continue
		}
		faces = append(faces, face{v: v, points: p, area: area,
			direction: [3]float64{cross[0] / area, cross[1] / area, cross[2] / area}})
	}
	accumulated := make([][3]float64, count)
	strongest := make([][3]float64, count)
	largest := make([]float64, count)
	for corner := 0; corner < 3; corner++ {
		for i := range faces {
			f := &faces[i]
			vertex := f.v[corner]
			edgeA := sub(f.points[(corner+1)%3], f.points[corner])
			edgeB := sub(f.points[(corner+2)%3], f.points[corner])
			angle := math.Atan2(f.area, float64(edgeA[0]*edgeB[0])+float64(edgeA[1]*edgeB[1])+float64(edgeA[2]*edgeB[2]))
			for c := 0; c < 3; c++ {
				accumulated[vertex][c] += float64(f.direction[c] * angle)
			}
		}
		// Opposite-winding faces can cancel. Prefer a real incident direction
		// from the largest face in that case, with stable ties.
		for i := range faces {
			f := &faces[i]
			vertex := f.v[corner]
			if missing[vertex] && f.area > largest[vertex] {
				largest[vertex] = f.area
				strongest[vertex] = f.direction
			}
		}
	}
	var result [][3]float64
	unused := 0
	for vertex := 0; vertex < count; vertex++ {
		if !missing[vertex] {
			continue
		}
		var value [3]float64
		switch length := norm(accumulated[vertex]); {
		case largest[vertex] == 0:
			// No nondegenerate incident polygon, so the placeholder cannot
			// affect a rendered surface. C4D still requires a unit direction.
			value = [3]float64{0, 1, 0}
			unused++
		case length > 1e-12:
			a := accumulated[vertex]
			value = [3]float64{a[0] / length, a[1] / length, a[2] / length}
		default:
			value = strongest[vertex]
		}
		result = append(result, value)
	}
	return result, unused, nil
}

func sub(a, b [3]float64) [3]float64 { return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }

func norm(v [3]float64) float64 {
	return math.Sqrt(float64(v[0]*v[0]) + float64(v[1]*v[1]) + float64(v[2]*v[2]))
}

// normalizeMesh returns the mesh with unit normals (a new mesh when the
// normals change) and appends repair warnings.
func normalizeMesh(mesh *formats.Mesh, warnings *[]string) (*formats.Mesh, error) {
	if mesh == nil || len(mesh.Normals) == 0 {
		return mesh, nil
	}
	label := mesh.Name
	normals := mesh.Normals
	if len(normals) != len(mesh.Positions) || len(normals)%3 != 0 {
		return nil, valueError(label + ": invalid normal array length.")
	}
	count := len(normals) / 3
	vectors := make([][3]float64, count)
	lengths := make([]float64, count)
	missing := make([]bool, count)
	anyMissing := false
	for i := range vectors {
		v := [3]float64{float64(normals[i*3]), float64(normals[i*3+1]), float64(normals[i*3+2])}
		for _, c := range v {
			if math.IsNaN(c) || math.IsInf(c, 0) {
				return nil, valueError(label + ": normals must contain finite directions for C4D export.")
			}
		}
		vectors[i], lengths[i] = v, norm(v)
	}
	for i, length := range lengths {
		if math.IsInf(length, 0) {
			return nil, valueError(label + ": normals must contain finite directions for C4D export.")
		}
		if length < 1e-12 {
			missing[i], anyMissing = true, true
		} else {
			v := vectors[i]
			vectors[i] = [3]float64{v[0] / length, v[1] / length, v[2] / length}
		}
	}
	if anyMissing {
		repaired, unused, err := automaticVertexNormals(mesh, missing)
		if err != nil {
			return nil, err
		}
		k, count := 0, 0
		for i := range vectors {
			if missing[i] {
				vectors[i] = repaired[k]
				k++
				count++
			}
		}
		if count > unused {
			*warnings = append(*warnings, label+": generated "+strconv.Itoa(count-unused)+
				" zero-length vertex normal(s) from mesh geometry for C4D export.")
		}
		if unused > 0 {
			*warnings = append(*warnings, label+": used placeholder normals for "+strconv.Itoa(unused)+
				" vertex/vertices without a nondegenerate polygon.")
		}
	}
	out := *mesh
	out.Normals = make([]float32, len(normals))
	for i, v := range vectors {
		out.Normals[i*3], out.Normals[i*3+1], out.Normals[i*3+2] = float32(v[0]), float32(v[1]), float32(v[2])
	}
	return &out, nil
}

// dedupe is list(dict.fromkeys(values)).
func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// NormalizeMeshLists is the core of _normalized_normal_asset for callers that
// hold meshes outside models (map_export normalizes compact map prototypes
// with {'models': [prototypes...], 'warnings': ...}). It returns new lists
// (meshes without normals are shared, the others copied) and the deduplicated
// warnings, starting with warnings.
func NormalizeMeshLists(lists [][]*formats.Mesh, warnings []string) ([][]*formats.Mesh, []string, error) {
	collected := copyStrings(warnings)
	out := make([][]*formats.Mesh, len(lists))
	for i, meshes := range lists {
		// model.get('meshes', []): an absent list becomes an empty one.
		out[i] = make([]*formats.Mesh, len(meshes))
		for j, mesh := range meshes {
			normalized, err := normalizeMesh(mesh, &collected)
			if err != nil {
				return nil, nil, err
			}
			out[i][j] = normalized
		}
	}
	return out, dedupe(collected), nil
}

// NormalizedNormalAsset is _normalized_normal_asset: match Blender
// custom-normal normalization without changing shared assets.
//
// OW's packed normals are not exactly unit length. Blender normalizes them
// when installing custom split normals; Cinema's NormalTag expects unit
// vectors. Normalize before the bridge so existing bridge 0.7.2 installations
// receive corrected data too. Zero custom normals request automatic shading
// in Blender. Resolve those from geometry; keep authored directions and split
// vertices. Malformed/nonfinite data is still rejected.
func NormalizedNormalAsset(asset *formats.Asset) (*formats.Asset, error) {
	lists := make([][]*formats.Mesh, len(asset.Models))
	for i, model := range asset.Models {
		if model != nil {
			lists[i] = model.Meshes
		}
	}
	normalized, warnings, err := NormalizeMeshLists(lists, asset.Warnings)
	if err != nil {
		return nil, err
	}
	result := *asset
	result.Models = make([]*formats.Model, len(asset.Models))
	for i, model := range asset.Models {
		if model == nil {
			continue
		}
		copied := *model
		copied.Meshes = normalized[i]
		result.Models[i] = &copied
	}
	result.Warnings = warnings
	return &result, nil
}

// PayloadOptions are make_payload's keyword arguments. Python's defaults are
// IncludeMesh and RootMotion true, RigID "auto"; an empty RigID means "auto".
type PayloadOptions struct {
	IncludeMesh bool
	RootMotion  bool
	// ClipName overrides clip.Name when not empty (clip_name or clip['name']).
	ClipName string
	// MotionClipName is the user nickname, independently of the scene filename.
	MotionClipName string
	RigID          string
	// ScopeModelID selects one object (select_model_asset) when not nil.
	ScopeModelID *string
}

// DefaultPayloadOptions are Python's keyword defaults.
func DefaultPayloadOptions() PayloadOptions {
	return PayloadOptions{IncludeMesh: true, RootMotion: true, RigID: "auto"}
}

func countMatches(bones []formats.Bone, names map[string]bool) int {
	n := 0
	for i := range bones {
		if names[bones[i].Name] {
			n++
		}
	}
	return n
}

// lastFrame is int(clip.get('lastFrame', clip.get('durationFrames', 1) - 1)).
func lastFrame(clip *formats.Clip) int64 { return clip.LastFrame }

// bakeTracks samples every frame of a model's rig into local matrices.
// frameSource maps an output frame to the clip frame.
func bakeTracks(model *formats.Model, evaluator *exportpose.Evaluator, last int64, rootMotion bool,
	frameSource func(int64) float64, fail func(index int, frame int64) error) ([]Track, error) {
	tracks := make([]Track, len(model.Bones))
	for i := range tracks {
		tracks[i] = Track{ModelID: model.ID, BoneIndex: i, Frames: make([]formats.Mat4, 0, last+1)}
	}
	pose := evaluator.NewPose()
	for frame := int64(0); frame <= last; frame++ {
		evaluator.EvaluateInto(pose, frameSource(frame), !rootMotion)
		for i := range tracks {
			parent := evaluator.Parents[i]
			local := pose.World[i]
			if parent >= 0 {
				inverse, err := pose.World[parent].Inverse()
				if err != nil {
					return nil, err
				}
				local = inverse.Mul(&pose.World[i])
			}
			if _, _, _, err := exportpose.Decompose(&local); err != nil {
				return nil, fail(i, frame)
			}
			tracks[i].Frames = append(tracks[i].Frames, local.ColumnMajor())
		}
	}
	return tracks, nil
}

// MakePayload is make_payload(asset, clip, materials, ...).
func MakePayload(asset *formats.Asset, clip *formats.Clip, materials *Materials, opts PayloadOptions) (*Payload, error) {
	rigID := opts.RigID
	if rigID == "" {
		rigID = "auto"
	}
	if opts.ScopeModelID != nil {
		scope := *opts.ScopeModelID
		if rigID != "auto" && rigID != scope {
			return nil, valueError("The animation target must be the object selected for C4D export.")
		}
		scoped, err := SelectModelAsset(asset, scope)
		if err != nil {
			return nil, err
		}
		asset, rigID = scoped, scope
		materialKeys := map[string]bool{}
		for _, mesh := range asset.Models[0].Meshes {
			if mesh != nil {
				materialKeys[mesh.MaterialKey] = true
			}
		}
		filtered := ordered.New[*formats.Material]()
		if materials != nil {
			if source, ok := materials.Get(scope); ok && source != nil {
				source.Range(func(key string, value *formats.Material) bool {
					if materialKeys[key] {
						filtered.Set(key, value)
					}
					return true
				})
			}
		}
		materials = ordered.New[*ordered.Map[*formats.Material]]()
		materials.Set(scope, filtered)
	}
	names := map[string]bool{}
	for i := range clip.Bones {
		names[clip.Bones[i].Name] = true
	}
	var selected *formats.Model
	bestMatches, bestBones := -1, -1
	for _, model := range asset.Models {
		if model == nil || len(model.Bones) == 0 {
			continue
		}
		if rigID == "auto" {
			// max() keeps the first of equal keys.
			matches := countMatches(model.Bones, names)
			if matches > bestMatches || (matches == bestMatches && len(model.Bones) > bestBones) {
				selected, bestMatches, bestBones = model, matches, len(model.Bones)
			}
		} else if model.ID == rigID {
			selected = model
			break
		}
	}
	if selected == nil {
		return nil, valueError("Choose a model with a skeleton for C4D export.")
	}
	matched := countMatches(selected.Bones, names)
	if len(names) > 0 && matched == 0 {
		return nil, valueError("The selected animation has no matching bones on this rig.")
	}
	fps, last := clip.FPS, lastFrame(clip)
	if math.IsNaN(fps) || math.IsInf(fps, 0) || !(0 < fps && fps <= 1000) || last < 0 {
		return nil, valueError("Invalid animation timing.")
	}
	if (last+1)*int64(len(selected.Bones)) > MaxSampledBoneFrames {
		return nil, valueError("Native C4D export exceeds 250,000 sampled bone frames. Choose a shorter clip.")
	}
	if len(asset.ExportHierarchy) == 0 {
		return nil, valueError("Entity hierarchy metadata is missing. Reload the entity before C4D export.")
	}
	native := asset
	if opts.IncludeMesh {
		var err error
		if native, err = NormalizedNormalAsset(asset); err != nil {
			return nil, err
		}
	}
	evaluator, err := exportpose.NewEvaluator(selected.Bones, clip.Bones)
	if err != nil {
		return nil, err
	}
	tracks, err := bakeTracks(selected, evaluator, last, opts.RootMotion,
		func(frame int64) float64 { return float64(frame) },
		func(index int, frame int64) error {
			return valueError("C4D cannot represent this clip's nonuniform scale without extra hierarchy nodes (" +
				selected.Bones[index].Name + ", frame " + strconv.FormatInt(frame, 10) + "). " +
				"This clip cannot be represented as C4D PSR animation without changing the original hierarchy.")
		})
	if err != nil {
		return nil, err
	}
	name := opts.ClipName
	if name == "" {
		name = clip.Name
	}
	motionName := opts.MotionClipName
	if motionName == "" {
		motionName = name
	}
	gameplay1P, heroName := SourceIdentity(asset.Path)
	return &Payload{Version: 1, Asset: native, Materials: orEmpty(materials), Warnings: copyStrings(native.Warnings),
		IncludeMesh: opts.IncludeMesh, Name: name, MotionClip: len(tracks) > 0, MotionClipName: motionName,
		Gameplay1P: gameplay1P, HeroName: heroName,
		Animation: Animation{FPS: fps, LastFrame: last, Tracks: tracks, MatchedBones: matched}}, nil
}

// Assignment is one make_set_payload record: a model and its parsed clip
// (load_clip result).
type Assignment struct {
	ModelID string
	Clip    *formats.Clip
}

// SetOptions are make_set_payload's keyword arguments (Python defaults:
// IncludeMesh and RootMotion true, Name "Animation set").
type SetOptions struct {
	IncludeMesh    bool
	RootMotion     bool
	Name           string
	MotionClipName string
}

// DefaultSetOptions are Python's keyword defaults.
func DefaultSetOptions() SetOptions {
	return SetOptions{IncludeMesh: true, RootMotion: true, Name: "Animation set"}
}

type preparedClip struct {
	model   *formats.Model
	clip    *formats.Clip
	fps     float64
	last    int64
	matched int
	missing []string
}

// MakeSetPayload is make_set_payload: bake explicit per-model clips into one
// existing native entity hierarchy.
//
// Each assignment's clip is matched by bone name only within its model;
// shared names on another skeleton never acquire its animation implicitly.
// All source models remain in the scene. Unassigned models retain their rest
// pose and inherit movement through the existing parent-joint attachment.
//
// Each clip begins at zero seconds at its own frame rate. Sample at the
// highest assigned frame rate through the longest duration, holding shorter
// clips at their last frame. The final common frame rounds up when necessary,
// adding at most one held frame instead of stretching any clip. This remains
// bridge protocol 1 and requires no Cinema plugin replacement.
func MakeSetPayload(asset *formats.Asset, assignments []Assignment, materials *Materials, opts SetOptions) (*Payload, error) {
	if len(assignments) == 0 {
		return nil, valueError("Assign an animation to at least one object before exporting the set.")
	}
	byID := make(map[string]*formats.Model, len(asset.Models))
	for _, model := range asset.Models {
		byID[model.ID] = model
	}
	if len(byID) != len(asset.Models) {
		return nil, valueError("The loaded entity contains duplicate object identifiers.")
	}
	if len(asset.ExportHierarchy) == 0 {
		return nil, valueError("Entity hierarchy metadata is missing. Reload the entity before C4D export.")
	}
	graph, err := OrderedGraph(asset.ExportHierarchy)
	if err != nil {
		return nil, err
	}
	type boneKey struct {
		model string
		index int
	}
	graphBones := map[boneKey]bool{}
	for _, node := range graph {
		if node.Type == "bone" && node.BoneIndex != nil {
			graphBones[boneKey{node.ModelID, *node.BoneIndex}] = true
		}
	}
	var prepared []preparedClip
	seen := map[string]bool{}
	for _, assignment := range assignments {
		mid, clip := assignment.ModelID, assignment.Clip
		model := byID[mid]
		if model == nil || len(model.Bones) == 0 {
			return nil, valueError("Choose an object with a skeleton for every animation assignment.")
		}
		if seen[mid] {
			return nil, valueError("Each object can have only one assigned animation in a set.")
		}
		seen[mid] = true
		if clip == nil {
			return nil, valueError("An animation assignment is missing its clip data.")
		}
		for index := range model.Bones {
			if !graphBones[boneKey{mid, index}] {
				return nil, valueError("An assigned object is missing joints from the export hierarchy. Reload the entity.")
			}
		}
		names := map[string]bool{}
		for i := range clip.Bones {
			names[clip.Bones[i].Name] = true
		}
		modelNames := map[string]bool{}
		for i := range model.Bones {
			modelNames[model.Bones[i].Name] = true
		}
		matched := 0
		var missing []string
		for name := range names {
			if modelNames[name] {
				matched++
			} else {
				missing = append(missing, name)
			}
		}
		if matched == 0 {
			return nil, valueError("The animation assigned to " + model.Name + " has no matching bones.")
		}
		sort.Strings(missing)
		if missing == nil {
			missing = []string{}
		}
		fps, last := clip.FPS, lastFrame(clip)
		if math.IsNaN(fps) || math.IsInf(fps, 0) || !(0 < fps && fps <= 1000) || last < 0 {
			return nil, valueError("Invalid animation timing in the set.")
		}
		prepared = append(prepared, preparedClip{model: model, clip: clip, fps: fps, last: last,
			matched: matched, missing: missing})
	}

	fps, duration := prepared[0].fps, float64(prepared[0].last)/prepared[0].fps
	for _, item := range prepared[1:] {
		fps = math.Max(fps, item.fps)
		duration = math.Max(duration, float64(item.last)/item.fps)
	}
	// Suppress numerical noise for a source endpoint already on this grid.
	last := int64(math.Max(0, math.Ceil(duration*fps-1e-9)))
	totalBones := int64(0)
	for _, item := range prepared {
		totalBones += int64(len(item.model.Bones))
	}
	if (last+1)*totalBones > MaxSampledBoneFrames {
		return nil, valueError("Native C4D export exceeds 250,000 sampled bone frames. Choose shorter clips.")
	}
	var tracks []Track
	summaries := make([]AssignmentSummary, 0, len(prepared))
	matchedTotal := 0
	for _, item := range prepared {
		model, clip := item.model, item.clip
		evaluator, err := exportpose.NewEvaluator(model.Bones, clip.Bones)
		if err != nil {
			return nil, err
		}
		itemFPS, itemLast := item.fps, float64(item.last)
		modelTracks, err := bakeTracks(model, evaluator, last, opts.RootMotion,
			func(frame int64) float64 { return math.Min(itemLast, float64(frame)/fps*itemFPS) },
			func(index int, frame int64) error {
				return valueError("C4D cannot represent this clip's nonuniform scale without extra hierarchy nodes (" +
					model.Name + ", " + model.Bones[index].Name + ", frame " + strconv.FormatInt(frame, 10) +
					"). The original hierarchy has been retained.")
			})
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, modelTracks...)
		summaries = append(summaries, AssignmentSummary{ModelID: model.ID, ClipName: clip.Name, FPS: item.fps,
			LastFrame: item.last, DurationSeconds: float64(item.last) / item.fps, MatchedBones: item.matched,
			UnmatchedBoneNames: item.missing})
		matchedTotal += item.matched
	}
	native := asset
	if opts.IncludeMesh {
		if native, err = NormalizedNormalAsset(asset); err != nil {
			return nil, err
		}
	}
	motionName := opts.MotionClipName
	if motionName == "" {
		motionName = opts.Name
	}
	gameplay1P, heroName := SourceIdentity(asset.Path)
	return &Payload{Version: 1, Asset: native, Materials: orEmpty(materials), Warnings: copyStrings(native.Warnings),
		IncludeMesh: opts.IncludeMesh, Name: opts.Name, MotionClip: len(tracks) > 0, MotionClipName: motionName,
		Gameplay1P: gameplay1P, HeroName: heroName,
		Animation: Animation{FPS: fps, LastFrame: last, DurationSeconds: &duration, Tracks: tracks,
			Assignments: summaries, MatchedBones: matchedTotal}}, nil
}

// MatrixValues is c4d_scene._matrix_values: validate TRS representability
// before Cinema 4D can discard shear. label prefixes the error messages.
func MatrixValues(values formats.Mat4, label string) error {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return valueError(label + ": non-finite matrix.")
		}
	}
	if math.Abs(values[3]) > 1e-7 || math.Abs(values[7]) > 1e-7 || math.Abs(values[11]) > 1e-7 || math.Abs(values[15]-1) > 1e-7 {
		return valueError(label + ": invalid affine matrix.")
	}
	var columns [3][3]float64
	var lengths [3]float64
	for c := 0; c < 3; c++ {
		copy(columns[c][:], values[c*4:c*4+3])
		// sum() starts from integer 0 and adds left to right.
		sum := 0.0
		for _, v := range columns[c] {
			sum += float64(v * v)
		}
		lengths[c] = math.Sqrt(sum)
	}
	if slices.Min(lengths[:]) < 1e-10 {
		return valueError(label + ": zero scale cannot be exported to native joint PSR.")
	}
	for _, pair := range [3][2]int{{0, 1}, {0, 2}, {1, 2}} {
		a, b := pair[0], pair[1]
		sum := 0.0
		for k := 0; k < 3; k++ {
			sum += float64(columns[a][k] * columns[b][k])
		}
		if math.Abs(sum/float64(lengths[a]*lengths[b])) > 1e-5 {
			return valueError(label + ": this animation needs a sheared local joint transform. " +
				"Cinema 4D cannot retain that transform as native PSR without changing the hierarchy.")
		}
	}
	return nil
}

// OrderedGraph is c4d_scene._ordered_graph: the export hierarchy in
// breadth-first parent order (roots in input order), with every node's
// matrix validated by MatrixValues. The result points into graph.
func OrderedGraph(graph []formats.Node) ([]*formats.Node, error) {
	seen := make(map[string]bool, len(graph))
	for i := range graph {
		seen[graph[i].ID] = true
	}
	if len(seen) != len(graph) {
		return nil, valueError("The export hierarchy contains duplicate node identifiers.")
	}
	children := map[string][]*formats.Node{}
	ready := make([]*formats.Node, 0, len(graph))
	for i := range graph {
		node := &graph[i]
		if !hasParent(node.ParentID) {
			ready = append(ready, node)
		} else {
			children[*node.ParentID] = append(children[*node.ParentID], node)
		}
	}
	// Linear traversal matters for maps with thousands of placed objects.
	ordered := make([]*formats.Node, 0, len(graph))
	for k := 0; k < len(ready); k++ {
		node := ready[k]
		if err := MatrixValues(node.Matrix, node.Name); err != nil {
			return nil, err
		}
		ordered = append(ordered, node)
		ready = append(ready, children[node.ID]...)
	}
	if len(ordered) != len(graph) {
		return nil, valueError("The export hierarchy contains a missing parent or a cycle.")
	}
	return ordered, nil
}

// AssetModels is c4d_scene._asset_models: resolve compact map geometry while
// retaining individual native objects. Each result model is a shallow copy
// whose Meshes are shallow mesh copies carrying the per-instance mesh names
// (meshNames); vertex arrays stay shared.
func AssetModels(asset *formats.Asset) (*ordered.Map[*formats.Model], error) {
	models := ordered.New[*formats.Model]()
	for _, model := range asset.Models {
		if _, exists := models.Get(model.ID); exists {
			return nil, valueError("The scene contains duplicate model identifiers.")
		}
		meshes := model.Meshes
		if model.MeshPrototype != "" {
			meshes = nil
			if asset.MeshPrototypes != nil {
				if prototype, ok := asset.MeshPrototypes.Get(model.MeshPrototype); ok && prototype != nil {
					meshes = prototype.Meshes
				}
			}
		}
		if meshes == nil {
			return nil, valueError("Map object references a missing mesh prototype. Update the C4D bridge and reload the map.")
		}
		resolved := make([]*formats.Mesh, len(meshes))
		for index, mesh := range meshes {
			copied := *mesh
			if model.MeshNames != nil {
				if name, ok := model.MeshNames.Get(strconv.Itoa(index)); ok {
					copied.Name = name
				}
			}
			resolved[index] = &copied
		}
		copied := *model
		copied.Meshes = resolved
		models.Set(model.ID, &copied)
	}
	return models, nil
}
