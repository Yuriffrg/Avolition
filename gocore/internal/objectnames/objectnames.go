// Package objectnames is the Go port of owpreview/object_names.py: validated
// display names applied to export copies, never source bindings.
//
// # Python → Go map
//
//	MAX_NAME_UNITS / MAX_OVERRIDES   MaxNameUnits / MaxOverrides
//	validate_name                    ValidateName
//	object_name_keys                 ObjectNameKeys
//	validate_names                   ValidateNames
//	apply_object_names               ApplyObjectNames
//
// Python strings can hold lone surrogates (category Cs); Go strings decoded
// from JSON cannot (encoding/json substitutes U+FFFD). Invalid UTF-8 in a Go
// string, the only way such text can reach this package, is rejected as Cs.
package objectnames

import (
	"errors"
	"regexp"
	"sort"
	"strconv"
	"unicode"
	"unicode/utf8"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

const (
	MaxNameUnits = 160
	MaxOverrides = 100000
)

var keyPattern = regexp.MustCompile(`^(?:scene|model:[A-Za-z0-9_.-]+|mesh:[A-Za-z0-9_.-]+:[0-9]+)$`)

// ValidateName is validate_name: a trimmed display name of at most 160 UTF-16
// units without control characters. name is any JSON value; only strings are
// names.
func ValidateName(name any) (string, error) {
	text, ok := name.(string)
	if !ok {
		return "", errors.New("Enter an object name.")
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if (r == utf8.RuneError && size == 1) || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cs, r) {
			return "", errors.New("Object names cannot contain control characters.")
		}
		i += size
	}
	text = formats.PythonStrip(text)
	if text == "" {
		return "", errors.New("Object names cannot be empty.")
	}
	units := 0
	for _, r := range text {
		units++
		if r > 0xFFFF {
			units++
		}
	}
	if units > MaxNameUnits {
		return "", errors.New("Keep object names within " + strconv.Itoa(MaxNameUnits) + " characters.")
	}
	return text, nil
}

// meshes is _meshes(asset, model): a model's own meshes, or its compact map
// prototype's.
func meshes(asset *formats.Asset, model *formats.Model) ([]*formats.Mesh, error) {
	if model.MeshPrototype == "" {
		return model.Meshes, nil
	}
	if asset.MeshPrototypes != nil {
		if source, ok := asset.MeshPrototypes.Get(model.MeshPrototype); ok && source != nil && source.Meshes != nil {
			return source.Meshes, nil
		}
	}
	return nil, errors.New("The map object references a missing mesh prototype.")
}

func meshKey(model string, index int, mesh *formats.Mesh) string {
	if mesh != nil && mesh.SourceMeshIndex != nil {
		index = *mesh.SourceMeshIndex
	}
	return "mesh:" + model + ":" + strconv.Itoa(index)
}

// ObjectNameKeys is object_name_keys: the stable names supported by both
// entity and map preview assets, as a set.
func ObjectNameKeys(asset *formats.Asset) (map[string]bool, error) {
	keys := map[string]bool{"scene": true}
	for _, model := range asset.Models {
		keys["model:"+model.ID] = true
		list, err := meshes(asset, model)
		if err != nil {
			return nil, err
		}
		for index, mesh := range list {
			keys[meshKey(model.ID, index, mesh)] = true
		}
	}
	return keys, nil
}

type entry struct {
	key   string
	value any
}

// entries flattens the supported dict representations in iteration order.
// A plain Go map has no order: its keys are sorted.
func entries(names any) ([]entry, bool) {
	switch m := names.(type) {
	case *ordered.Map[string]:
		if m == nil {
			return nil, true
		}
		out := make([]entry, 0, m.Len())
		m.Range(func(k, v string) bool { out = append(out, entry{k, v}); return true })
		return out, true
	case *ordered.Map[any]:
		if m == nil {
			return nil, true
		}
		out := make([]entry, 0, m.Len())
		m.Range(func(k string, v any) bool { out = append(out, entry{k, v}); return true })
		return out, true
	case map[string]any:
		out := make([]entry, 0, len(m))
		for k, v := range m {
			out = append(out, entry{k, v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
		return out, true
	case map[string]string:
		out := make([]entry, 0, len(m))
		for k, v := range m {
			out = append(out, entry{k, v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
		return out, true
	}
	return nil, false
}

// ValidateNames is validate_names: return a clean mapping; reject unknown and
// bone keys when an asset is supplied. names may be nil (no names), an
// *ordered.Map[string], *ordered.Map[any], map[string]any or
// map[string]string; anything else is not a dict.
func ValidateNames(names any, asset *formats.Asset) (*ordered.Map[string], error) {
	cleaned := ordered.New[string]()
	if names == nil {
		return cleaned, nil
	}
	list, ok := entries(names)
	if !ok || len(list) > MaxOverrides {
		return nil, errors.New("Invalid saved object names.")
	}
	var allowed map[string]bool
	if asset != nil {
		var err error
		if allowed, err = ObjectNameKeys(asset); err != nil {
			return nil, err
		}
	}
	for _, item := range list {
		if utf8.RuneCountInString(item.key) > 512 || !keyPattern.MatchString(item.key) {
			return nil, errors.New("Only scene, model and mesh display names can be changed.")
		}
		if allowed != nil && !allowed[item.key] {
			return nil, errors.New("The renamed object is not in this asset. Reload it and try again.")
		}
		name, err := ValidateName(item.value)
		if err != nil {
			return nil, err
		}
		cleaned.Set(item.key, name)
	}
	return cleaned, nil
}

func hasParent(id *string) bool { return id != nil && *id != "" }

// ApplyObjectNames is apply_object_names: copy an asset and change only names
// consumed by scene exporters.
//
// Entity wrappers use entityName. A standalone model has no entity wrapper,
// so a scene override names its root model unless that model has its own name.
// Paths, GUIDs, IDs, joints, attachments, matrices and material keys are intact.
// Maps retain shared immutable geometry arrays; only named wrappers are copied.
func ApplyObjectNames(asset *formats.Asset, names any) (*formats.Asset, error) {
	cleaned, err := ValidateNames(names, asset)
	if err != nil {
		return nil, err
	}
	get := func(key string) (string, bool) { return cleaned.Get(key) }
	isMap := asset.Map != nil || asset.MeshPrototypes != nil
	var result *formats.Asset
	if isMap {
		copied := *asset
		copied.Models = make([]*formats.Model, 0, len(asset.Models))
		for _, model := range asset.Models {
			named := *model
			if model.Meshes != nil {
				named.Meshes = make([]*formats.Mesh, len(model.Meshes))
				for i, mesh := range model.Meshes {
					meshCopy := *mesh
					named.Meshes[i] = &meshCopy
				}
			}
			if model.MeshNames != nil {
				named.MeshNames = ordered.New[string]()
				model.MeshNames.Range(func(k, v string) bool { named.MeshNames.Set(k, v); return true })
			}
			copied.Models = append(copied.Models, &named)
		}
		copied.ExportHierarchy = make([]formats.Node, len(asset.ExportHierarchy))
		copy(copied.ExportHierarchy, asset.ExportHierarchy)
		result = &copied
	} else {
		result = asset.Clone()
	}
	if scene, ok := get("scene"); ok {
		result.Name = scene
		var roots []*formats.Model
		for _, model := range result.Models {
			if !hasParent(model.ParentID) {
				roots = append(roots, model)
			}
		}
		if !isMap && len(roots) == 1 {
			root := roots[0]
			if root.EntityWrapper() {
				root.EntityName = &scene
			} else if _, own := get("model:" + root.ID); !own {
				root.Name = scene
			}
		}
	}
	for _, model := range result.Models {
		if name, ok := get("model:" + model.ID); ok {
			model.Name = name
		}
		list, err := meshes(result, model)
		if err != nil {
			return nil, err
		}
		for index, mesh := range list {
			name, ok := get(meshKey(model.ID, index, mesh))
			if !ok {
				continue
			}
			if model.MeshPrototype != "" {
				if model.MeshNames == nil {
					model.MeshNames = ordered.New[string]()
				}
				model.MeshNames.Set(strconv.Itoa(index), name)
			} else {
				mesh.Name = name
			}
		}
	}
	// Compact maps carry a prebuilt export graph so repeated source models keep
	// their exact attachment/instance transforms. Only graph labels may change.
	graph := result.ExportHierarchy
	for i := range graph {
		node := &graph[i]
		var key string
		switch node.Type {
		case "model":
			key = "model:" + node.ModelID
		case "mesh":
			index := node.SourceMeshIndex
			if index == nil {
				index = node.MeshIndex
			}
			if index == nil {
				continue
			}
			key = "mesh:" + node.ModelID + ":" + strconv.Itoa(*index)
		default:
			continue
		}
		if name, ok := get(key); ok {
			node.Name = name
		}
	}
	if scene, ok := get("scene"); ok && !isMap {
		var roots []*formats.Node
		for i := range graph {
			if !hasParent(graph[i].ParentID) {
				roots = append(roots, &graph[i])
			}
		}
		if len(roots) == 1 && roots[0].Type == "entity" {
			roots[0].Name = scene
		}
	}
	return result, nil
}
