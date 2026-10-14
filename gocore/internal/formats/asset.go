package formats

import (
	"strconv"
	"strings"
	"sync"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

// MaterialResolver is the hook for materials.MaterialResolver, which load_asset
// constructs per asset. The materials package imports this one, so it
// registers itself with RegisterMaterialResolver instead of being called
// directly.
type MaterialResolver interface {
	// ForModel is for_model(model, entityLook): it returns the model's
	// material descriptors and sets model.MaterialLook. Warnings go to the
	// list given to the factory. An error is treated like a Python
	// OSError/FormatError escaping for_model.
	ForModel(model *Model, entityLook string) (*ordered.Map[*Material], error)
	// Textures is the shared texture registry (Asset.Textures).
	Textures() *ordered.Map[*Texture]
	// Root is the resolved texture root (Asset.TextureRoot).
	Root() string
}

// MaterialResolverFactory is MaterialResolver(asset_path, root_path, warnings).
// assetPath is resolved; rootPath is as given to LoadAsset ("" for None).
// The resolver appends its warnings to *warnings (shared with LoadAsset).
type MaterialResolverFactory func(assetPath, rootPath string, warnings *[]string) MaterialResolver

var (
	materialsMu      sync.RWMutex
	materialsFactory MaterialResolverFactory
)

// RegisterMaterialResolver installs the factory LoadAsset uses by default
// (the materials package calls it from init). Passing nil restores the
// neutral resolver.
func RegisterMaterialResolver(factory MaterialResolverFactory) {
	materialsMu.Lock()
	defer materialsMu.Unlock()
	materialsFactory = factory
}

func defaultMaterialResolver() MaterialResolverFactory {
	materialsMu.RLock()
	defer materialsMu.RUnlock()
	if materialsFactory != nil {
		return materialsFactory
	}
	return NeutralMaterials
}

// NeutralMaterials is the resolver used until a real one is registered: no
// materials, no textures, and materials.py's texture root rule.
func NeutralMaterials(assetPath, rootPath string, _ *[]string) MaterialResolver {
	return &neutralMaterials{root: TextureRoot(assetPath, rootPath), textures: ordered.New[*Texture]()}
}

type neutralMaterials struct {
	root     string
	textures *ordered.Map[*Texture]
}

func (n *neutralMaterials) ForModel(*Model, string) (*ordered.Map[*Material], error) {
	return ordered.New[*Material](), nil
}
func (n *neutralMaterials) Textures() *ordered.Map[*Texture] { return n.textures }
func (n *neutralMaterials) Root() string                     { return n.root }

// TextureRoot is materials.MaterialResolver's root: the resolved rootPath
// when given, else the parent of the nearest "Models"/"Entities" ancestor of
// the asset (case-insensitive), else the asset's folder.
func TextureRoot(assetPath, rootPath string) string {
	if rootPath != "" {
		return pypath.Resolve(rootPath)
	}
	asset := pypath.Resolve(assetPath)
	for _, ancestor := range pypath.Parents(asset) {
		name := strings.ToLower(pypath.Name(ancestor))
		if name == "models" || name == "entities" {
			return pypath.Parent(ancestor)
		}
	}
	return pypath.Parent(asset)
}

// LoadOptions customise LoadAssetWith.
type LoadOptions struct {
	// Materials overrides the registered MaterialResolver factory.
	Materials MaterialResolverFactory
}

// LoadAsset reads one .owmdl or recursively assembles an .owentity and its
// children (Python's load_asset). rootPath is the DataTool export root, or "".
//
// Models form a hierarchy by ID/ParentID. AttachmentBone is a raw bone name on
// the parent model, or nil for the model root. AttachmentMatrix is relative
// to that parent/bone in Three.js column-major order. Missing child assets
// become visible warnings while the remaining mesh loads.
func LoadAsset(path, rootPath string) (*Asset, error) {
	return LoadAssetWith(path, rootPath, LoadOptions{})
}

// LoadAssetWith is LoadAsset with options.
func LoadAssetWith(path, rootPath string, opts LoadOptions) (*Asset, error) {
	path = pypath.Resolve(path)
	suffix := strings.ToLower(pypath.Suffix(path))
	if suffix != ".owmdl" && suffix != ".owentity" {
		return nil, formatError("Select an .owentity or .owmdl file.")
	}
	if !pypath.IsFile(path) {
		return nil, notFound(path)
	}
	factory := opts.Materials
	if factory == nil {
		factory = defaultMaterialResolver()
	}
	loader := &assetLoader{resolver: NewResolver(rootPath), active: map[string]bool{}}
	loader.materials = factory(path, rootPath, &loader.warnings)
	if err := loader.add(path, nil, "", 0, ""); err != nil {
		return nil, err
	}
	loaded := false
	for _, model := range loader.models {
		if len(model.Meshes) > 0 || len(model.Bones) > 0 {
			loaded = true
			break
		}
	}
	if !loaded {
		detail := loader.warnings
		if len(detail) > 5 {
			detail = detail[:5]
		}
		return nil, formatError(PythonStrip("No model or skeleton could be loaded from " + pypath.Name(path) +
			".\n" + strings.Join(detail, "\n")))
	}
	asset := &Asset{
		Name: pypath.Stem(path), Path: path, Models: loader.models,
		Textures: loader.materials.Textures(), TextureRoot: loader.materials.Root(),
		Warnings: dedupe(loader.warnings),
	}
	if asset.Textures == nil {
		asset.Textures = ordered.New[*Texture]()
	}
	hierarchy, err := BuildExportHierarchy(asset)
	if err != nil {
		return nil, err
	}
	asset.ExportHierarchy = hierarchy
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

type assetLoader struct {
	resolver  *Resolver
	materials MaterialResolver
	models    []*Model
	warnings  []string
	active    map[string]bool
}

func (l *assetLoader) warn(message string) { l.warnings = append(l.warnings, message) }

func (l *assetLoader) add(current string, parent *Model, attachment string, depth int, referenceName string) error {
	current = pypath.Resolve(current)
	if depth > MaxEntityDepth {
		return formatError("Entity hierarchy exceeds 48 levels.")
	}
	if l.active[current] {
		return formatError("Entity reference cycle: " + pypath.Name(current))
	}
	if len(l.models) >= MaxModels {
		return formatError("Entity exceeds 512 model nodes.")
	}
	l.active[current] = true
	defer delete(l.active, current)

	var entity *Entity
	modelPath := current
	if strings.ToLower(pypath.Suffix(current)) == ".owentity" {
		var err error
		if entity, err = ReadEntity(current); err != nil {
			return err
		}
		if modelPath, err = l.resolver.Find(entity.Model, current, KindModel, entity.Relative); err != nil {
			return err
		}
		if entity.Model != "" && modelPath == "" {
			l.warn(pypath.Name(current) + ": missing model " + entity.Model +
				"; keep the original Models folder beside Entities or set its export root.")
		}
	}
	var model *Model
	if modelPath != "" {
		var err error
		if model, err = LoadModel(modelPath, &l.warnings); err != nil {
			return err
		}
		look := ""
		if entity != nil {
			look = entity.MaterialLook
		}
		materials, err := l.materials.ForModel(model, look)
		if err != nil {
			return err
		}
		if materials == nil {
			materials = ordered.New[*Material]()
		}
		model.Materials = materials
	} else {
		name := ""
		if entity != nil {
			name = entity.Name
		}
		if name == "" {
			name = pypath.Stem(current)
		}
		model = &Model{Name: name, Path: current, Bones: []Bone{}, Meshes: []*Mesh{}, Sockets: []Socket{},
			Materials: ordered.New[*Material]()}
	}
	model.ID = "model-" + strconv.Itoa(len(l.models))
	if parent != nil {
		model.ParentID = ptr(parent.ID)
	}
	model.AttachmentMatrix = Identity
	// Preserve the source scene's wrappers separately from the compact
	// preview model list. C4D's importer names entity children from their
	// reference filename and skeletons from the model filename, not its
	// numeric internal index. Exporters must not manufacture bone names.
	model.IsEntity = ptr(entity != nil)
	if entity != nil {
		name := entity.Name
		if parent != nil {
			name = referenceName
		}
		if name == "" {
			name = pypath.Stem(current)
		}
		model.EntityName = ptr(name)
		model.EntityPath = ptr(current)
	}
	if modelPath != "" {
		model.ModelGUID = ptr(pypath.Stem(modelPath))
	}
	model.HasModel = ptr(modelPath != "")
	if parent != nil && attachment != "" {
		attached := false
		for i := range parent.Sockets {
			if parent.Sockets[i].Name == attachment {
				model.AttachmentBone = parent.Sockets[i].Bone
				model.AttachmentMatrix = parent.Sockets[i].Matrix
				attached = true
				break
			}
		}
		if !attached {
			for i := range parent.Bones {
				if parent.Bones[i].Name == attachment {
					model.AttachmentBone = ptr(attachment)
					attached = true
					break
				}
			}
		}
		if !attached {
			l.warn(pypath.Name(current) + ": attachment " + attachment + " not found; attached to parent root.")
		}
	}
	l.models = append(l.models, model)
	if entity == nil {
		return nil
	}
	for _, child := range entity.Children {
		if child.Reference == "" {
			continue
		}
		childPath, err := l.resolver.Find(child.Reference, current, KindEntity, DefaultRelative)
		if err == nil {
			if childPath != "" {
				err = l.add(childPath, model, child.Attachment, depth+1, pypath.Stem(CleanReference(child.Reference)))
			} else {
				l.warn(pypath.Name(current) + ": missing child entity " + child.Reference + ".")
			}
		}
		if err != nil {
			if !IsOSOrFormatError(err) {
				return err
			}
			l.warn("Skipped child " + child.Reference + ": " + PythonOSError(err).Error())
		}
	}
	return nil
}
