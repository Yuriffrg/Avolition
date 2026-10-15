package api

import (
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// namesToAny converts validated names to a catalog dict.
func namesToAny(names *ordered.Map[string]) *ordered.Map[any] {
	out := ordered.New[any]()
	if names != nil {
		names.Range(func(k, v string) bool { out.Set(k, v); return true })
	}
	return out
}

func cloneNames(names *ordered.Map[string]) *ordered.Map[string] {
	out := ordered.New[string]()
	if names != nil {
		names.Range(func(k, v string) bool { out.Set(k, v); return true })
	}
	return out
}

// filterNames is {node: name for node, name in names.items() if node in known}.
func filterNames(names *ordered.Map[string], known map[string]bool) *ordered.Map[string] {
	out := ordered.New[string]()
	if names != nil {
		names.Range(func(k, v string) bool {
			if known[k] {
				out.Set(k, v)
			}
			return true
		})
	}
	return out
}

// namingSkeleton keeps only editable names, never a second copy of loaded
// geometry (_register_names).
func namingSkeleton(asset *formats.Asset) *formats.Asset {
	skeleton := &formats.Asset{Name: asset.Name, Models: []*formats.Model{}}
	for _, model := range asset.Models {
		meshes := model.Meshes
		if meshes == nil && model.MeshPrototype != "" && asset.MeshPrototypes != nil {
			if prototype, ok := asset.MeshPrototypes.Get(model.MeshPrototype); ok && prototype != nil {
				meshes = prototype.Meshes
			}
		}
		copied := &formats.Model{ID: model.ID, Name: model.Name, Meshes: make([]*formats.Mesh, 0, len(meshes))}
		for index, mesh := range meshes {
			source := index
			name := ""
			if mesh != nil {
				name = mesh.Name
				if mesh.SourceMeshIndex != nil {
					source = *mesh.SourceMeshIndex
				}
			}
			copied.Meshes = append(copied.Meshes, &formats.Mesh{Name: name, SourceMeshIndex: &source})
		}
		skeleton.Models = append(skeleton.Models, copied)
	}
	return skeleton
}

// registerNames is _register_names(path, asset): it records the editable
// names and sets asset.ObjectNames to the accepted saved names.
func (a *API) registerNames(path string, asset *formats.Asset) error {
	skeleton := namingSkeleton(asset)
	name := key(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.naming.Set(name, skeleton)
	for a.naming.Len() > 8 {
		a.naming.Delete(a.naming.Keys()[0])
	}
	names, err := a.objectNamesLocked(path)
	if err != nil {
		return err
	}
	known, err := a.deps.ObjectNames.Keys(skeleton)
	if err != nil {
		return err
	}
	// Changed source structure must not block previewing or current exports.
	asset.ObjectNames = filterNames(names, known)
	return nil
}

// objectNamesLocked is object_names(path).
func (a *API) objectNamesLocked(path any) (*ordered.Map[string], error) {
	if !isAbsArg(path) {
		return nil, newValueError("Load an asset before editing object names.")
	}
	saved := getOr(a.catalog, "objectNames", ordered.New[any]())
	dict, ok := dictOf(saved)
	if !ok {
		return nil, newValueError("Saved object names are invalid. Restore the catalog before editing names.")
	}
	return a.deps.ObjectNames.ValidateNames(getOr(dict, key(path.(string)), ordered.New[any]()), nil)
}

// ObjectNames is object_names(path).
func (a *API) ObjectNames(path any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objectNamesLocked(path)
}

// SaveObjectName is save_object_name(path, node, name).
func (a *API) SaveObjectName(path, node, name any) (any, error) {
	clean, err := a.deps.ObjectNames.ValidateName(name)
	if err != nil {
		return nil, err
	}
	pathKey, err := keyOf(path)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	skeleton, ok := a.naming.Get(pathKey)
	if !ok {
		return nil, newValueError("Reload this asset before editing its object names.")
	}
	names, err := a.objectNamesLocked(path)
	if err != nil {
		return nil, err
	}
	var change any
	switch n := node.(type) {
	case string:
		pair := ordered.New[any]()
		pair.Set(n, clean)
		change = pair
	case []any, *ordered.Map[any]:
		return nil, &typeError{"unhashable type: '" + pycompat.TypeName(node) + "'"}
	default:
		return nil, newValueError("Only scene, model and mesh display names can be changed.")
	}
	update, err := a.deps.ObjectNames.ValidateNames(change, skeleton)
	if err != nil {
		return nil, err
	}
	update.Range(func(k, v string) bool { names.Set(k, v); return true })
	previous := getOr(a.catalog, "objectNames", ordered.New[any]())
	next := cloneDict(previous.(*ordered.Map[any]))
	next.Set(pathKey, namesToAny(names))
	a.catalog.Set("objectNames", next)
	if err := a.save(); err != nil {
		a.catalog.Set("objectNames", previous)
		return nil, err
	}
	return cloneNames(names), nil
}
