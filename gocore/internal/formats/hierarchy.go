package formats

import (
	"strconv"

	"avolition/gocore/internal/bonenames"
	"avolition/gocore/internal/pypath"
)

type hierarchyState struct {
	entity, model *string
	bones         map[string]string
}

// BuildExportHierarchy is build_export_hierarchy: a source-compatible
// entity/model/skeleton graph in raw Y-up metres.
//
// The working Cinema 4D importer aliases a hardpoint directly to its joint.
// It creates no socket object or socket offset, so export wrappers have
// identity transforms. Preview AttachmentMatrix deliberately remains intact.
// Node identifiers are internal only; every displayed name is source-derived.
func BuildExportHierarchy(asset *Asset) ([]Node, error) {
	nodes := []Node{}
	states := map[string]*hierarchyState{}
	pending := append([]*Model(nil), asset.Models...)
	assetName := asset.Name

	add := func(node Node) *string {
		nodes = append(nodes, node)
		return ptr(node.ID)
	}
	hasParent := func(m *Model) bool { return m.ParentID != nil && *m.ParentID != "" }

	for len(pending) > 0 {
		var ready []*Model
		for _, m := range pending {
			if !hasParent(m) || states[*m.ParentID] != nil {
				ready = append(ready, m)
			}
		}
		if len(ready) == 0 {
			return nil, formatError("Model attachments contain a missing parent or cycle.")
		}
		for _, model := range ready {
			for i, m := range pending {
				if m == model {
					pending = append(pending[:i:i], pending[i+1:]...)
					break
				}
			}
			mid := model.ID
			var parentID *string
			if hasParent(model) {
				previous := states[*model.ParentID]
				parentID = previous.entity
				if parentID == nil {
					parentID = previous.model
				}
				if model.AttachmentBone != nil {
					if bone, ok := previous.bones[*model.AttachmentBone]; ok {
						parentID = ptr(bone)
					}
				}
			}
			// Metadata-free assets are supported for callers constructing a
			// scene directly; real parsed .owmdl files do not gain an extra root.
			var entityID *string
			if model.EntityWrapper() {
				name := ""
				if model.EntityName != nil {
					name = *model.EntityName
				}
				if name == "" {
					if !hasParent(model) {
						name = assetName
					} else {
						name = model.Name
					}
				}
				entityID = add(Node{ID: mid + ":entity", Type: "entity", Name: name, ParentID: parentID,
					ModelID: mid, Matrix: Identity})
				parentID = entityID
			}
			var group *string
			boneIDs := map[string]string{}
			if model.ModelPresent() {
				group = add(Node{ID: mid + ":model", Type: "model", Name: model.Name, ParentID: parentID,
					ModelID: mid, Matrix: Identity})
				if len(model.Bones) > 0 {
					guid := ""
					if model.ModelGUID != nil {
						guid = *model.ModelGUID
					}
					if guid == "" {
						source := model.Path
						if source == "" {
							source = model.Name
						}
						guid = pypath.Stem(source)
					}
					skeleton := add(Node{ID: mid + ":skeleton", Type: "skeleton", Name: guid + "_Skeleton",
						ParentID: group, ModelID: mid, Matrix: Identity})
					for index := range model.Bones {
						bone := &model.Bones[index]
						ident := mid + ":bone:" + strconv.Itoa(index)
						parent := skeleton
						if bone.Parent >= 0 {
							parent = ptr(mid + ":bone:" + strconv.Itoa(bone.Parent))
						}
						add(Node{ID: ident, Type: "bone", Name: bonenames.BoneName(bone.Name), ParentID: parent,
							ModelID: mid, Matrix: Compose(bone.Pos, bone.Rot),
							BoneIndex: ptr(index), SourceName: ptr(bone.Name)})
						boneIDs[bone.Name] = ident
					}
				}
				for index, mesh := range model.Meshes {
					add(Node{ID: mid + ":mesh:" + strconv.Itoa(index), Type: "mesh", Name: mesh.Name,
						ParentID: group, ModelID: mid, Matrix: Identity, MeshIndex: ptr(index)})
				}
			}
			states[mid] = &hierarchyState{entity: entityID, model: group, bones: boneIDs}
		}
	}
	return nodes, nil
}
