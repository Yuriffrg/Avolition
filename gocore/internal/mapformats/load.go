package mapformats

import (
	"runtime"
	"strconv"
	"strings"
	"sync"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/materials"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

// Summary is the "map" object of a loaded map asset.
type Summary struct {
	Version                      string   `json:"version"`
	InstanceCount                int      `json:"instanceCount"`
	LoadedInstanceCount          int      `json:"loadedInstanceCount"`
	MissingInstanceCount         int      `json:"missingInstanceCount"`
	ModelCount                   int      `json:"modelCount"`
	PrototypeCount               int      `json:"prototypeCount"`
	MeshCount                    int64    `json:"meshCount"`
	PolygonCount                 int64    `json:"polygonCount"`
	UniqueVertexCount            int64    `json:"uniqueVertexCount"`
	LightCount                   int64    `json:"lightCount"`
	SoundCount                   int      `json:"soundCount"`
	OmittedCollisionMeshCount    int64    `json:"omittedCollisionMeshCount"`
	OmittedCollisionPolygonCount int64    `json:"omittedCollisionPolygonCount"`
	Unsupported                  []string `json:"unsupported"`
}

// MaxWarnings bounds a loaded map's warning list.
const MaxWarnings = 500

// reference is _reference: OWLib emits map-relative paths, so absolute or
// traversing map references are invalid. It returns "" for an empty value.
func reference(owner, value, root string, cache *pypath.ResolveCache, extensions ...string) (string, error) {
	if value == "" {
		return "", nil
	}
	ownerName := pypath.Name(owner)
	parts := strings.Split(strings.ReplaceAll(value, `\`, "/"), "/")
	invalid := value[0] == '/' || value[0] == '\\' || strings.IndexByte(value, 0) >= 0
	for _, part := range parts {
		// A drive ("C:") or alternate data stream contains ':'; PureWindowsPath
		// drives and roots otherwise start with a separator.
		if part == ".." || strings.Contains(part, ":") {
			invalid = true
		}
	}
	if invalid {
		return "", &formats.FormatError{Message: ownerName + ": map reference must stay within its export folder: " + value}
	}
	folder := pypath.Parent(owner)
	path := cache.Resolve(pypath.Join(folder, parts...))
	if !pypath.IsRelativeTo(path, root) || !pypath.IsRelativeTo(path, folder) {
		return "", &formats.FormatError{Message: ownerName + ": map reference leaves its export folder: " + value}
	}
	suffix := strings.ToLower(pypath.Suffix(path))
	for _, extension := range extensions {
		if suffix == extension {
			return path, nil
		}
	}
	return "", &formats.FormatError{Message: ownerName + ": unsupported referenced file: " + value}
}

// referenceCache memoizes reference for one map load: placements repeat a
// few hundred distinct references thousands of times, and resolving each
// costs an lstat per path component.
type referenceCache struct {
	owner, root string
	cache       *pypath.ResolveCache
	extensions  []string
	results     map[string]placementRefs
}

func newReferenceCache(owner, root string, cache *pypath.ResolveCache, extensions ...string) *referenceCache {
	return &referenceCache{owner: owner, root: root, cache: cache, extensions: extensions, results: map[string]placementRefs{}}
}

func (c *referenceCache) get(value string) (string, error) {
	if cached, ok := c.results[value]; ok {
		return cached.source, cached.err
	}
	path, err := reference(c.owner, value, c.root, c.cache, c.extensions...)
	c.results[value] = placementRefs{source: path, err: err}
	return path, err
}

// ExpandMapAsset resolves compact geometry without copying numeric arrays or
// changing prototypes: every model gets its prototype's meshes (named from
// its own meshNames), and meshPrototypes is dropped.
func ExpandMapAsset(asset *formats.Asset) (*formats.Asset, error) {
	result := *asset
	result.MeshPrototypes = nil
	result.Models = make([]*formats.Model, 0, len(asset.Models))
	for _, source := range asset.Models {
		geometry := source.Meshes
		if source.MeshPrototype != "" {
			geometry = nil
			if asset.MeshPrototypes != nil {
				if prototype, ok := asset.MeshPrototypes.Get(source.MeshPrototype); ok && prototype != nil {
					geometry = prototype.Meshes
				}
			}
		}
		if geometry == nil {
			return nil, &formats.FormatError{Message: "Map object references a missing mesh prototype."}
		}
		meshes := make([]*formats.Mesh, len(geometry))
		for index, mesh := range geometry {
			copied := *mesh
			if source.MeshNames != nil {
				if name, ok := source.MeshNames.Get(strconv.Itoa(index)); ok {
					copied.Name = name
				}
			}
			meshes[index] = &copied
		}
		model := *source
		model.Meshes = meshes
		model.MeshPrototype = ""
		result.Models = append(result.Models, &model)
	}
	return &result, nil
}

// Options customise LoadMapWith.
type Options struct {
	// Expanded returns the expanded asset (load_map(compact=False)).
	Expanded bool
	// Limits are the preview bounds; nil means DefaultLimits.
	Limits *Limits
	// Workers bounds parallel model loading; <= 0 means runtime.NumCPU().
	Workers int
}

// LoadMap is load_map(path, root_path) with compact prototypes. rootPath ""
// is None (the map's folder).
func LoadMap(path, rootPath string) (*formats.Asset, error) {
	return LoadMapWith(path, rootPath, Options{})
}

type variantModel struct {
	model             *formats.Model
	meshIndexMap      map[int]int
	collisionMeshes   int64
	collisionPolygons int64
	prototypeKey      string
}

type loadedSource struct {
	asset *formats.Asset
	err   error
}

type mapLoader struct {
	warnings   []string
	warningSet map[string]struct{}
}

func (l *mapLoader) warn(message string) {
	if _, exists := l.warningSet[message]; exists {
		return
	}
	l.warningSet[message] = struct{}{}
	if len(l.warnings) < MaxWarnings {
		l.warnings = append(l.warnings, message)
	}
}

type placementRefs struct {
	source, look string
	err          error
}

// LoadMapWith loads referenced models once, retaining each placement's
// complete local TRS (Python's load_map).
//
// The normal entity/model loader still handles bones, submeshes and
// materials. Every placed instance has independent model IDs and attachment
// links. Compact prototypes contain geometry only; material bindings remain
// per instance.
func LoadMapWith(path, rootPath string, opts Options) (*formats.Asset, error) {
	path, err := formats.CheckPath(path, ".owmap")
	if err != nil {
		return nil, err
	}
	limits, err := checkLimits(opts.Limits)
	if err != nil {
		return nil, err
	}
	root := pypath.Parent(path)
	if rootPath != "" {
		root = pypath.Resolve(rootPath)
	}
	if !pypath.IsDir(root) || !pypath.IsRelativeTo(path, root) {
		return nil, &ValueError{"The selected map must be inside its existing map source folder."}
	}
	parsed, err := ReadMap(path, limits)
	if err != nil {
		return nil, err
	}

	// Validate references in placement order up to the first invalid one,
	// and load every distinct model referenced before it in parallel. The
	// sequential pass below consumes these results exactly where Python
	// would call load_asset, so output and errors are unchanged.
	refs := make([]placementRefs, len(parsed.Instances))
	resolveCache := &pypath.ResolveCache{}
	sourceRefs := newReferenceCache(path, root, resolveCache, ".owmdl", ".owentity")
	lookRefs := newReferenceCache(path, root, resolveCache, ".owmat")
	var order []string
	for i := range parsed.Instances {
		instance := &parsed.Instances[i]
		ref := &refs[i]
		if ref.source, ref.err = sourceRefs.get(instance.Source); ref.err != nil {
			break
		}
		if ref.look, ref.err = lookRefs.get(instance.Material); ref.err != nil {
			break
		}
		if ref.source != "" {
			order = append(order, ref.source)
		}
	}
	sources := loadSources(dedupe(order), root, opts.Workers)

	l := &mapLoader{warnings: []string{}, warningSet: map[string]struct{}{}}
	var models []*formats.Model
	var graph []formats.Node
	prototypes := ordered.New[*formats.MeshPrototype]()
	textures := ordered.New[*formats.Texture]()
	loaded := map[string]*formats.Asset{}
	variants := map[[2]string][]*variantModel{}
	prototypeKeys := map[string]string{}
	resolver := materials.New(path, root, nil)
	var vertices, meshInstances, polygonInstances, collisionMeshes, collisionPolygons int64
	loadedInstances := 0

	for index := range parsed.Instances {
		instance := &parsed.Instances[index]
		ref := refs[index]
		if ref.err != nil {
			return nil, ref.err
		}
		if ref.source == "" {
			l.warn("Placement " + strconv.Itoa(index+1) + ": empty model reference; object was not loaded.")
			continue
		}
		key := ref.source
		source, seen := loaded[key]
		if !seen {
			result := sources[key]
			if result.err != nil {
				if !formats.IsOSOrFormatError(result.err) {
					return nil, result.err
				}
				l.warn(instance.Source + ": object could not be loaded: " + formats.PythonOSError(result.err).Error())
			} else {
				source = result.asset
				if source.Textures != nil {
					source.Textures.Range(func(id string, texture *formats.Texture) bool {
						textures.Set(id, texture)
						return true
					})
				}
			}
			loaded[key] = source
		}
		if source == nil {
			continue
		}
		variantKey := [2]string{key, ref.look}
		variant, ok := variants[variantKey]
		if !ok {
			variant = buildVariant(l, resolver, source, ref.look, instance.Material)
			variants[variantKey] = variant
		}
		if int64(len(models)+len(variant)) > limits.Models || int64(len(graph)+len(source.ExportHierarchy)) > limits.GraphNodes {
			return nil, &formats.FormatError{Message: "Map exceeds the model/hierarchy preview limit. Choose a smaller map variant."}
		}
		prefix := "map-" + strconv.Itoa(index) + "-"
		identifiers := make(map[string]string, len(variant))
		originals := make(map[string]*variantModel, len(variant))
		for _, v := range variant {
			identifiers[v.model.ID] = prefix + v.model.ID
			originals[v.model.ID] = v
		}
		for _, original := range variant {
			model := *original.model
			collisionMeshes += original.collisionMeshes
			collisionPolygons += original.collisionPolygons
			meshInstances += int64(len(model.Meshes))
			if meshInstances > limits.MeshInstances {
				return nil, &formats.FormatError{Message: "Map exceeds the placed-mesh preview limit. Choose a smaller map variant."}
			}
			for _, mesh := range model.Meshes {
				polygonInstances += int64(len(mesh.Indices) / 3)
			}
			// Models referenced directly and through entities share geometry.
			prototypeID, exists := prototypeKeys[original.prototypeKey]
			if !exists {
				prototypeID = "geometry-" + strconv.Itoa(prototypes.Len())
				prototypeKeys[original.prototypeKey] = prototypeID
				for _, mesh := range model.Meshes {
					vertices += int64(len(mesh.Positions) / 3)
				}
				if vertices > limits.UniqueVertices {
					return nil, &formats.FormatError{Message: "Map exceeds the unique-geometry preview limit. Choose a smaller map variant."}
				}
				prototypes.Set(prototypeID, &formats.MeshPrototype{Meshes: model.Meshes})
			}
			model.ID = identifiers[original.model.ID]
			model.ParentID = nil
			if original.model.ParentID != nil {
				if id, ok := identifiers[*original.model.ParentID]; ok {
					model.ParentID = &id
				}
			}
			model.MeshPrototype = prototypeID
			model.MapInstanceID = "map-" + strconv.Itoa(index)
			model.MapCategory = instance.Category
			model.Meshes = nil
			if original.model.ParentID == nil || *original.model.ParentID == "" {
				model.AttachmentMatrix = formats.Multiply(&instance.Matrix, &original.model.AttachmentMatrix)
			}
			models = append(models, &model)
		}
		for i := range source.ExportHierarchy {
			originalNode := &source.ExportHierarchy[i]
			original := originals[originalNode.ModelID]
			isMesh := originalNode.Type == "mesh"
			var newIndex int
			if isMesh {
				var kept bool
				newIndex, kept = original.meshIndexMap[*originalNode.MeshIndex]
				if !kept {
					continue
				}
			}
			node := *originalNode
			node.ID = prefix + originalNode.ID
			node.ParentID = nil
			if originalNode.ParentID != nil && *originalNode.ParentID != "" {
				node.ParentID = ptr(prefix + *originalNode.ParentID)
			}
			node.ModelID = identifiers[originalNode.ModelID]
			if isMesh {
				node.SourceMeshIndex = ptr(*originalNode.MeshIndex)
				node.MeshIndex = ptr(newIndex)
			}
			suffix := ":model"
			if original.model.IsEntity != nil && *original.model.IsEntity {
				suffix = ":entity"
			}
			if originalNode.ID == original.model.ID+suffix {
				attachment := original.model.AttachmentMatrix
				if original.model.ParentID == nil || *original.model.ParentID == "" {
					node.Matrix = formats.Multiply(&instance.Matrix, &attachment)
				} else {
					node.Matrix = attachment
				}
			}
			graph = append(graph, node)
		}
		loadedInstances++
	}
	resolver.Textures().Range(func(id string, texture *formats.Texture) bool {
		textures.Set(id, texture)
		return true
	})
	used := map[string]bool{}
	for _, model := range models {
		if model.Materials == nil {
			continue
		}
		model.Materials.Range(func(_ string, material *formats.Material) bool {
			if material != nil && material.Maps != nil {
				material.Maps.Range(func(_ string, mapping *formats.MaterialMap) bool {
					used[mapping.TextureID] = true
					return true
				})
			}
			return true
		})
	}
	usedTextures := ordered.New[*formats.Texture]()
	textures.Range(func(id string, texture *formats.Texture) bool {
		if used[id] {
			usedTextures.Set(id, texture)
		}
		return true
	})
	for _, warning := range resolver.Warnings() {
		l.warn(warning)
	}
	if parsed.LightCount > 0 {
		l.warn(formats.Commas(parsed.LightCount) + " map lights were read but are not included in preview or C4D export.")
	}
	if parsed.SoundCount > 0 {
		l.warn(formats.Commas(parsed.SoundCount) + " map sound emitters were read but are not included in preview or C4D export.")
	}
	if collisionMeshes > 0 {
		summary := formats.Commas(collisionMeshes) + " engine hit-test meshes (" + formats.Commas(collisionPolygons) +
			" polygons) were omitted, matching the Blender map importer."
		l.warningSet[summary] = struct{}{}
		// Keep this scene-level result visible even when individual shader
		// diagnostics have filled the bounded warning list.
		l.warnings = append([]string{summary}, l.warnings...)
		if len(l.warnings) > MaxWarnings {
			l.warnings = l.warnings[:MaxWarnings]
		}
	}
	if len(l.warningSet) > len(l.warnings) {
		l.warnings = append(l.warnings, formats.Commas(len(l.warningSet)-len(l.warnings))+" additional asset warnings were omitted.")
	}
	if meshInstances == 0 {
		detail := "This map contains no model placements."
		if len(l.warnings) > 0 {
			detail = strings.Join(l.warnings[:min(3, len(l.warnings))], " ")
		}
		return nil, &formats.FormatError{Message: "No map meshes could be loaded. Keep the map beside its exported Models/Entities folders. " + detail}
	}
	if models == nil {
		models = []*formats.Model{}
	}
	if graph == nil {
		graph = []formats.Node{}
	}
	asset := &formats.Asset{
		Name: parsed.Name, Path: path, Models: models, MeshPrototypes: prototypes, ExportHierarchy: graph,
		Textures: usedTextures, TextureRoot: root, Warnings: l.warnings,
		Map: &Summary{
			Version: parsed.Version, InstanceCount: len(parsed.Instances), LoadedInstanceCount: loadedInstances,
			MissingInstanceCount: len(parsed.Instances) - loadedInstances, ModelCount: len(models),
			PrototypeCount: prototypes.Len(), MeshCount: meshInstances, PolygonCount: polygonInstances,
			UniqueVertexCount: vertices, LightCount: parsed.LightCount, SoundCount: parsed.SoundCount,
			OmittedCollisionMeshCount: collisionMeshes, OmittedCollisionPolygonCount: collisionPolygons,
			Unsupported: []string{"lights", "sounds", "effects"},
		},
	}
	if opts.Expanded {
		return ExpandMapAsset(asset)
	}
	return asset, nil
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

// loadAsset is formats.LoadAsset; tests replace it to count loads.
var loadAsset = formats.LoadAsset

// loadSources runs formats.LoadAsset for every path with a bounded worker
// pool. Each load is independent (its own resolvers and warning list).
func loadSources(paths []string, root string, workers int) map[string]loadedSource {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	workers = min(workers, len(paths))
	results := make([]loadedSource, len(paths))
	var next sync.Mutex
	cursor := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next.Lock()
				i := cursor
				cursor++
				next.Unlock()
				if i >= len(paths) {
					return
				}
				asset, err := loadAsset(paths[i], root)
				results[i] = loadedSource{asset, err}
			}
		}()
	}
	wg.Wait()
	out := make(map[string]loadedSource, len(paths))
	for i, path := range paths {
		out[path] = results[i]
	}
	return out
}

// buildVariant applies a placement's look to every model of source, removes
// collision meshes and records each kept mesh's source index.
func buildVariant(l *mapLoader, resolver *materials.Resolver, source *formats.Asset, lookPath, lookReference string) []*variantModel {
	replacedNeutral := map[string]bool{}
	if lookPath != "" {
		for _, model := range source.Models {
			replacedNeutral[pypath.Name(model.Path)+": no material look reference; using neutral shading."] = true
		}
	}
	for _, warning := range source.Warnings {
		if !replacedNeutral[warning] {
			l.warn(warning)
		}
	}
	variant := make([]*variantModel, 0, len(source.Models))
	for _, sourceModel := range source.Models {
		model := *sourceModel
		materialSet := sourceModel.Materials
		// BLMaterial.bindEntityLook visits every child recursively.
		// Slots absent from that look retain the model's prior binding.
		if lookPath != "" && model.ModelPresent() {
			if pypath.IsFile(lookPath) {
				materialModel := model
				materialModel.MaterialPath = &lookPath
				copied := ordered.New[*formats.Material]()
				if materialSet != nil {
					materialSet.Range(func(slot string, descriptor *formats.Material) bool {
						copied.Set(slot, descriptor)
						return true
					})
				}
				resolver.ForModel(&materialModel, "", true).Range(func(slot string, descriptor *formats.Material) bool {
					if descriptor == nil {
						copied.Delete(slot)
					} else {
						copied.Set(slot, descriptor)
					}
					return true
				})
				materialSet = copied
				model.MaterialLook = formats.Some(lookPath)
			} else {
				l.warn(lookReference + ": material look is missing; retaining available source materials.")
			}
		}
		v := &variantModel{meshIndexMap: map[int]int{}}
		meshes := make([]*formats.Mesh, 0, len(model.Meshes))
		var indexKey strings.Builder
		indexKey.WriteString(model.Path)
		if model.ModelPresent() {
			indexKey.WriteString("\x00T")
		} else {
			indexKey.WriteString("\x00F")
		}
		for i, mesh := range model.Meshes {
			var descriptor *formats.Material
			if materialSet != nil {
				descriptor, _ = materialSet.Get(mesh.MaterialKey)
			}
			if materials.IsCollisionMaterial(descriptor) {
				v.collisionMeshes++
				v.collisionPolygons += int64(len(mesh.Indices) / 3)
				continue
			}
			v.meshIndexMap[i] = len(meshes)
			copied := *mesh
			copied.SourceMeshIndex = ptr(i)
			meshes = append(meshes, &copied)
			indexKey.WriteByte(',')
			indexKey.WriteString(strconv.Itoa(i))
		}
		model.Meshes = meshes
		// Collision-only descriptors must not become native materials or
		// consume texture decoding time after their meshes are removed.
		usedKeys := make(map[string]bool, len(meshes))
		for _, mesh := range meshes {
			usedKeys[mesh.MaterialKey] = true
		}
		filtered := ordered.New[*formats.Material]()
		if materialSet != nil {
			materialSet.Range(func(slot string, descriptor *formats.Material) bool {
				if usedKeys[slot] {
					filtered.Set(slot, descriptor)
				}
				return true
			})
		}
		model.Materials = filtered
		v.model = &model
		v.prototypeKey = indexKey.String()
		variant = append(variant, v)
	}
	return variant
}
