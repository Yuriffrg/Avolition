package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

// Each selected record is a simultaneous set. Loading the entity once gives
// every set exactly the same rest skeleton, attachments, weights and basis.
func (a *API) stagerSelection(ids []any) ([]*ordered.Map[any], *formats.Asset, error) {
	if len(ids) == 0 || len(ids) > MaxExportQueue {
		return nil, nil, newValueError("Select one or more saved animation setups.")
	}
	a.mu.Lock()
	entries, err := a.exportQueueEntriesLocked()
	a.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*ordered.Map[any], len(entries))
	for _, item := range entries {
		entry := item.(*ordered.Map[any])
		byID[get(entry, "id").(string)] = entry
	}
	var selected []*ordered.Map[any]
	var entity, root string
	for _, value := range ids {
		id, err := queueID(value)
		if err != nil {
			return nil, nil, err
		}
		entry := byID[id]
		if entry == nil {
			return nil, nil, newValueError("A selected setup is missing or was selected more than once. Review the export list.")
		}
		delete(byID, id)
		path, library := get(entry, "entityPath").(string), get(entry, "rootPath").(string)
		if len(selected) == 0 {
			entity, root = path, library
		} else if key(entity) != key(path) || key(root) != key(library) {
			return nil, nil, newValueError("Select setups saved for the same entity in the same asset library, so their skeleton and attachments match.")
		}
		selected = append(selected, entry)
	}
	asset, err := a.deps.LoadAsset(entity, root)
	if err != nil {
		return nil, nil, err
	}
	// Saved display aliases may differ across snapshots. Pick the same
	// snapshot independent of checkbox order when building the shared rig.
	naming := selected[0]
	for _, entry := range selected[1:] {
		if get(entry, "id").(string) < get(naming, "id").(string) {
			naming = entry
		}
	}
	asset, err = a.deps.ObjectNames.Apply(asset, get(naming, "objectNames"))
	if err != nil {
		return nil, nil, err
	}
	return selected, asset, nil
}

// Match the Motion System owner's choice in c4d_motion.create_motion_clips.
// Disjoint skeletons receive a shared Joints null when the native rig is built.
func stagerTarget(asset *formats.Asset) string {
	if pov, _ := c4dexport.SourceIdentity(asset.Path); pov {
		return "Joints"
	}
	// The native library fills every unassigned skeleton with rest keys so
	// a previous action cannot leave an attachment in its animated pose.
	nodes := map[string]formats.Node{}
	var roots []formats.Node
	for _, n := range asset.ExportHierarchy {
		nodes[n.ID] = n
		if n.Type == "skeleton" {
			roots = append(roots, n)
		}
	}
	for _, candidate := range roots {
		all := true
		for _, r := range roots {
			for r.ID != candidate.ID && r.ParentID != nil {
				r = nodes[*r.ParentID]
			}
			if r.ID != candidate.ID {
				all = false
				break
			}
		}
		if all {
			return candidate.Name
		}
	}
	return "Joints"
}

func stagerRigID(kind string, entry *ordered.Map[any]) string {
	return stagerDigest([]byte(kind + "\x00" + key(get(entry, "rootPath").(string)) + "\x00" + key(get(entry, "entityPath").(string))))
}

func (a *API) StagerRigDestination(value any) (any, error) {
	ids, _ := value.([]any)
	entries, asset, err := a.stagerSelection(ids)
	if err != nil {
		return nil, err
	}
	bridge := a.bridge.MapBridge()
	instance := bridge.Status().Instance()
	if instance == "" {
		return nil, newValueError(c4dbridge.SetupMessage)
	}
	request := map[string]any{"operation": "stagerDestination", "rigIds": map[string]string{
		"actor": stagerRigID("actor", entries[0]), "pov": stagerRigID("pov", entries[0]),
	}}
	raw, err := bridge.Export(request, "", "", c4dbridge.ExportOptions{Stager: true, Instance: instance})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result == nil || !nonEmptyString(result["documentId"]) || !nonEmptyString(result["documentName"]) {
		return nil, newValueError("Cinema 4D did not return an active destination document.")
	}
	options := []map[string]string{}
	for _, n := range asset.ExportHierarchy {
		if n.Type == "bone" || n.Type == "skeleton" || n.Type == "entity" || n.Type == "model" {
			options = append(options, map[string]string{"id": n.ID, "name": n.Name})
		}
	}
	result["rootMotionObjects"] = options
	result["motionTargetName"] = stagerTarget(asset)
	result["instance"] = instance
	return result, nil
}

func stagerActions(req *ordered.Map[any], kind string) (ids []any, rows []*ordered.Map[any], err error) {
	actions, _ := get(req, "actions").([]any)
	seen := map[string]bool{}
	valid := map[string]bool{"idle": true, "action": true}
	if kind == "pov" {
		for _, r := range []string{"adsUp", "adsDown", "reload"} {
			valid[r] = true
		}
	} else {
		for _, r := range []string{"forward", "backward", "left", "right", "death"} {
			valid[r] = true
		}
	}
	for _, v := range actions {
		row, ok := dictOf(v)
		if !ok || !keysSubset(row, "id", "role", "binding", "edge") {
			return nil, nil, newValueError("Invalid Stager action assignment.")
		}
		role, _ := get(row, "role").(string)
		if !valid[role] {
			return nil, nil, newValueError("Choose an explicit role for every selected setup.")
		}
		if seen[role] && role != "action" {
			return nil, nil, newValueError("Use each named Stager role only once.")
		}
		seen[role] = true
		if edge, exists := row.Get("edge"); exists && edge != "press" && edge != "release" {
			return nil, nil, newValueError("Choose a press or release edge.")
		}
		if binding, exists := row.Get("binding"); exists {
			text, ok := binding.(string)
			if !ok || len(text) > 64 {
				return nil, nil, newValueError("Invalid input binding.")
			}
		}
		ids = append(ids, get(row, "id"))
		rows = append(rows, cloneDict(row))
	}
	if !seen["idle"] {
		return nil, nil, newValueError("Assign an Idle source for this rig.")
	}
	return ids, rows, nil
}

func stagerDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// Baked relative filenames already incorporate the texture content, channel
// transforms and material controls. Remove scratch locations from the identity.
func clearScratchTexturePaths(materials Materials) {
	if materials == nil {
		return
	}
	materials.Range(func(_ string, group *ordered.Map[*formats.Material]) bool {
		if group != nil {
			group.Range(func(_ string, m *formats.Material) bool {
				if m != nil && m.Maps != nil {
					m.Maps.Range(func(_ string, spec *formats.MaterialMap) bool {
						if spec != nil {
							spec.Path = ""
						}
						return true
					})
				}
				return true
			})
		}
		return true
	})
}

func (a *API) CreateStagerRig(value any) (any, error) {
	req, ok := dictOf(value)
	if !ok || !keysSubset(req, "instance", "documentId", "kind", "cameraId", "rootMotionId", "actions") {
		return nil, newValueError("Invalid Create Stager rig request.")
	}
	kind, _ := get(req, "kind").(string)
	instance, _ := get(req, "instance").(string)
	document, _ := get(req, "documentId").(string)
	if instance == "" || document == "" {
		return nil, newValueError("Review the destination Cinema 4D document first.")
	}
	if kind != "actor" && kind != "pov" {
		return nil, newValueError("Choose POV or Actor.")
	}
	camera, _ := get(req, "cameraId").(string)
	if kind == "pov" && camera == "" {
		return nil, newValueError("Choose a POV camera from the destination project.")
	}
	ids, rows, err := stagerActions(req, kind)
	if err != nil {
		return nil, err
	}
	entries, asset, err := a.stagerSelection(ids)
	if err != nil {
		return nil, err
	}
	rootMotion, _ := get(req, "rootMotionId").(string)
	if rootMotion != "" {
		found := false
		for _, node := range asset.ExportHierarchy {
			if node.ID == rootMotion && node.Type != "mesh" {
				found = true
				break
			}
		}
		if !found {
			return nil, newValueError("The selected root-motion object is missing. Review the rig again.")
		}
	}
	native, err := c4dexport.NormalizedNormalAsset(asset)
	if err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp(a.dataDir, "stager-bake-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	textureFolder := filepath.Join(temp, "tex")
	materials, warnings, err := a.bake(asset, textureFolder)
	if err != nil {
		return nil, err
	}
	clearScratchTexturePaths(materials)
	digest := sha256.New()
	enc := json.NewEncoder(digest)
	if err := enc.Encode(native); err != nil {
		return nil, err
	}
	if err := enc.Encode(materials); err != nil {
		return nil, err
	}
	rigFingerprint := hex.EncodeToString(digest.Sum(nil))
	rigId := stagerRigID(kind, entries[0])
	pov, hero := c4dexport.SourceIdentity(asset.Path)
	warnings = append(warnings, native.Warnings...)
	boneCount := int64(0)
	for _, model := range asset.Models {
		boneCount += int64(len(model.Bones))
	}
	used := 0
	for index, entry := range entries {
		assignments, err := a.loadSetAssignments(get(entry, "assignments").([]any))
		if err != nil {
			return nil, err
		}
		// A saved assignment list is keyed by model identity, independent of
		// the order in which the user originally selected the tracks.
		sort.Slice(assignments, func(i, j int) bool { return assignments[i].ModelID < assignments[j].ModelID })
		name := queueMotionClipName(entry)
		// Geometry is already normalized once. Only the independently sampled
		// animation is needed from the ordinary combined-export builder here.
		payload, err := c4dexport.MakeSetPayload(asset, assignments, nil, c4dexport.SetOptions{RootMotion: true, Name: name, MotionClipName: name, BakeScaleToJointPositions: kind == "actor"})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if (payload.Animation.LastFrame+1)*boneCount > c4dexport.MaxSampledBoneFrames {
			return nil, newValueError(name + " exceeds the native export limit of 250,000 sampled bone frames when unassigned joints are reset.")
		}
		data, err := json.Marshal(payload.Animation)
		if err != nil {
			return nil, err
		}
		used += len(data)
		if used > c4dbridge.MaxPayload {
			return nil, newValueError("The selected animations exceed the bridge's 256 MB job limit.")
		}
		rows[index].Set("animation", json.RawMessage(data))
		rows[index].Set("fingerprint", stagerDigest(data))
		rows[index].Set("name", name)
		warnings = append(warnings, payload.Warnings...)
		for _, assignment := range assignments {
			warnings = append(warnings, assignment.Clip.Warnings...)
		}
	}
	payload := map[string]any{"version": 1, "operation": "createStagerRig", "documentId": document, "kind": kind,
		"cameraId": camera, "rootMotionId": rootMotion, "rigId": rigId, "rigFingerprint": rigFingerprint, "actions": rows, "asset": native,
		"materials": materials, "includeMesh": true, "gameplay1P": pov, "heroName": hero, "name": asset.Name, "warnings": dedupe(warnings)}
	timeout := float64(c4dbridge.MapExportTimeout)
	return a.bridge.MapBridge().Export(payload, "", textureFolder, c4dbridge.ExportOptions{Stager: true, Instance: instance, Timeout: &timeout})
}
