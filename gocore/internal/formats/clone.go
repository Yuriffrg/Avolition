package formats

import (
	"slices"

	"avolition/gocore/internal/ordered"
)

// Deep copies, the Go counterpart of copy.deepcopy on the asset dicts. Export
// layers (c4d_export.select_model_asset, object_names.apply_object_names)
// return copies that never alias the loaded asset's writable state.

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

func cloneMap[V any](m *ordered.Map[V], clone func(V) V) *ordered.Map[V] {
	if m == nil {
		return nil
	}
	out := ordered.New[V]()
	m.Range(func(key string, value V) bool {
		out.Set(key, clone(value))
		return true
	})
	return out
}

func same[V any](value V) V { return value }

// Clone returns a deep copy of the mesh, including its vertex arrays.
func (m *Mesh) Clone() *Mesh {
	if m == nil {
		return nil
	}
	out := *m
	out.Positions = slices.Clone(m.Positions)
	out.Normals = slices.Clone(m.Normals)
	out.UVs = slices.Clone(m.UVs)
	out.UVs2 = slices.Clone(m.UVs2)
	out.Indices = slices.Clone(m.Indices)
	out.SkinIndices = slices.Clone(m.SkinIndices)
	out.SkinWeights = slices.Clone(m.SkinWeights)
	out.SourceMeshIndex = clonePtr(m.SourceMeshIndex)
	return &out
}

// Clone returns a deep copy of the material map binding.
func (m *MaterialMap) Clone() *MaterialMap { return clonePtr(m) }

// Clone returns a deep copy of the material descriptor.
func (m *Material) Clone() *Material {
	if m == nil {
		return nil
	}
	out := *m
	out.Maps = cloneMap(m.Maps, (*MaterialMap).Clone)
	out.EmissiveIntensity = clonePtr(m.EmissiveIntensity)
	out.EmissiveUsesAO = clonePtr(m.EmissiveUsesAO)
	out.UnbakedMaps = slices.Clone(m.UnbakedMaps)
	return &out
}

// Clone returns a deep copy of the texture descriptor.
func (t *Texture) Clone() *Texture { return clonePtr(t) }

// Clone returns a deep copy of the model, its bones, meshes and materials.
func (m *Model) Clone() *Model {
	if m == nil {
		return nil
	}
	out := *m
	out.Bones = slices.Clone(m.Bones)
	if m.Meshes != nil {
		out.Meshes = make([]*Mesh, len(m.Meshes))
		for i, mesh := range m.Meshes {
			out.Meshes[i] = mesh.Clone()
		}
	}
	if m.Sockets != nil {
		out.Sockets = make([]Socket, len(m.Sockets))
		for i, socket := range m.Sockets {
			out.Sockets[i] = Socket{Name: socket.Name, Bone: clonePtr(socket.Bone), Matrix: socket.Matrix}
		}
	}
	out.MaterialPath = clonePtr(m.MaterialPath)
	if m.MaterialLook.Set {
		out.MaterialLook = Nullable[string]{Set: true, Value: clonePtr(m.MaterialLook.Value)}
	}
	out.Materials = cloneMap(m.Materials, (*Material).Clone)
	out.ParentID = clonePtr(m.ParentID)
	out.AttachmentBone = clonePtr(m.AttachmentBone)
	out.IsEntity = clonePtr(m.IsEntity)
	out.EntityName = clonePtr(m.EntityName)
	out.EntityPath = clonePtr(m.EntityPath)
	out.ModelGUID = clonePtr(m.ModelGUID)
	out.HasModel = clonePtr(m.HasModel)
	out.MeshNames = cloneMap(m.MeshNames, same[string])
	return &out
}

// Clone returns a deep copy of the export hierarchy node.
func (n *Node) Clone() Node {
	out := *n
	out.ParentID = clonePtr(n.ParentID)
	out.BoneIndex = clonePtr(n.BoneIndex)
	out.SourceName = clonePtr(n.SourceName)
	out.MeshIndex = clonePtr(n.MeshIndex)
	out.SourceMeshIndex = clonePtr(n.SourceMeshIndex)
	return out
}

// CloneNodes deep-copies an export hierarchy (nil stays nil).
func CloneNodes(nodes []Node) []Node {
	if nodes == nil {
		return nil
	}
	out := make([]Node, len(nodes))
	for i := range nodes {
		out[i] = nodes[i].Clone()
	}
	return out
}

// Clone returns a deep copy of the asset. Map (the map summary, an opaque
// value owned by the map loader) is shared, not copied.
func (a *Asset) Clone() *Asset {
	if a == nil {
		return nil
	}
	out := *a
	if a.Models != nil {
		out.Models = make([]*Model, len(a.Models))
		for i, model := range a.Models {
			out.Models[i] = model.Clone()
		}
	}
	out.Textures = cloneMap(a.Textures, (*Texture).Clone)
	out.Warnings = slices.Clone(a.Warnings)
	out.ExportHierarchy = CloneNodes(a.ExportHierarchy)
	out.MeshPrototypes = cloneMap(a.MeshPrototypes, func(p *MeshPrototype) *MeshPrototype {
		if p == nil {
			return nil
		}
		meshes := p.Meshes
		if meshes != nil {
			meshes = make([]*Mesh, len(p.Meshes))
			for i, mesh := range p.Meshes {
				meshes[i] = mesh.Clone()
			}
		}
		return &MeshPrototype{Meshes: meshes}
	})
	out.ObjectNames = cloneMap(a.ObjectNames, same[string])
	return &out
}
