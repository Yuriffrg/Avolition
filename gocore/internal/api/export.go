package api

import (
	"fmt"
	"regexp"

	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/exportbundle"
	"avolition/gocore/internal/fbx"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
)

var modelSuffix = regexp.MustCompile(`(?i)\.(?:[0-9a-f]{3}|owmdl)$`)

func listContains(list []any, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// recordExportLocked is _record_export(folder, folder_existed, result).
func (a *API) recordExportLocked(folder string, folderExisted bool, files []string) error {
	folderKey := key(folder)
	if list := a.catalogList("exportDirectories"); !listContains(list, folderKey) {
		a.catalog.Set("exportDirectories", append(list, folderKey))
	}
	for _, path := range files {
		fileKey := key(path)
		if list := a.catalogList("exportedFiles"); !listContains(list, fileKey) {
			a.catalog.Set("exportedFiles", append(list, fileKey))
		}
	}
	if list := a.catalogList("managedOutputDirectories"); !folderExisted && !listContains(list, folderKey) {
		a.catalog.Set("managedOutputDirectories", append(list, folderKey))
	}
	return a.save()
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// neutralMaterials is {model id: {key: dict(material, maps={})}}: material
// colors and factors without references to the original source images.
func neutralMaterials(asset *formats.Asset) Materials {
	out := ordered.New[*ordered.Map[*formats.Material]]()
	for _, model := range asset.Models {
		materials := ordered.New[*formats.Material]()
		if model.Materials != nil {
			model.Materials.Range(func(name string, material *formats.Material) bool {
				if material != nil {
					copied := material.Clone()
					copied.Maps = ordered.New[*formats.MaterialMap]()
					materials.Set(name, copied)
				} else {
					materials.Set(name, nil)
				}
				return true
			})
		}
		out.Set(model.ID, materials)
	}
	return out
}

func (a *API) bake(asset *formats.Asset, folder string) (Materials, []string, error) {
	if a.deps.BakeMaterials == nil {
		return nil, nil, unavailable("exporttextures")
	}
	return a.deps.BakeMaterials(asset, folder)
}

// bridgeReady is the status preflight shared by the scene exports.
func (a *API) bridgeReady() error {
	status := a.bridge.Status()
	if !status.Connected() || !status.Octane() {
		return &runtimeError{status.Message()}
	}
	return nil
}

func skippedExport() *ordered.Map[any] {
	result := ordered.New[any]()
	result.Set("skipped", true)
	result.Set("files", []any{})
	result.Set("path", nil)
	result.Set("name", nil)
	return result
}

// ExportAnimation is export_animation(path, nickname, output_folder=None, options=None).
func (a *API) ExportAnimation(path, nickname, outputFolder, options any) (any, error) {
	settings, err := exportbundle.FBXOptions(options)
	if err != nil {
		return nil, err
	}
	if !settings.ClipEnabled && !settings.Enabled && !settings.C4DEnabled {
		return skippedExport(), nil
	}
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	source := pycompat.Resolve(text)
	if pyLower(pypath.Suffix(source)) != ".owanimclip" {
		return nil, newValueError("Select an .owanimclip animation to copy.")
	}
	if _, err := a.deps.ReadClipHeader(source); err != nil {
		return nil, err
	}
	filename, err := copyName(nickname)
	if err != nil {
		return nil, err
	}
	opts, _ := dictOf(options)
	if opts == nil {
		opts = ordered.New[any]()
	}
	// Capture the user's nickname before a selected object adds its filename prefix.
	motionName := pypath.Stem(filename)
	if value, present := opts.Get("motionClipName"); present {
		if motionName, err = validateMotionClipName(value); err != nil {
			return nil, err
		}
	}
	var scoped *formats.Asset
	if selected := get(opts, "selectedModelId"); pycompat.Truthy(selected) {
		selectedID, ok := selected.(string)
		if !ok || runeLen(selectedID) > 128 {
			return nil, newValueError("Invalid selected object.")
		}
		entityPath := get(opts, "entityPath")
		rootPath := get(opts, "rootPath")
		if !pycompat.Truthy(rootPath) {
			rootPath = a.Root()
		}
		if !isAbsArg(entityPath) {
			return nil, newValueError("Load an entity and select an object before exporting it.")
		}
		root := ""
		if pycompat.Truthy(rootPath) {
			if root, err = pathOf(rootPath); err != nil {
				return nil, err
			}
			if !pycompat.IsAbs(root) {
				return nil, newValueError("Invalid export root.")
			}
		}
		loaded, err := a.deps.LoadAsset(entityPath.(string), root)
		if err != nil {
			return nil, err
		}
		if scoped, err = c4dexport.SelectModelAsset(loaded, selectedID); err != nil {
			return nil, err
		}
		modelName := scoped.Models[0].Name
		if modelName == "" {
			modelName = selectedID
		}
		nicknameText, ok := nickname.(string)
		if !ok {
			return nil, &typeError{fmt.Sprintf(`can only concatenate str (not "%s") to str`, pycompat.TypeName(nickname))}
		}
		if filename, err = copyName(modelSuffix.ReplaceAllString(modelName, "") + "_" + nicknameText); err != nil {
			return nil, err
		}
	}
	destination := outputFolder
	if outputFolder == nil {
		a.mu.Lock()
		destination = pycompat.Clone(a.outputFolderLocked())
		a.mu.Unlock()
	}
	folder, err := folderPath(destination)
	if err != nil {
		return nil, err
	}
	if settings.ClipEnabled && key(source) == key(pycompat.Join(folder, filename)) {
		return nil, newValueError("The destination is the same source file. Choose a different output folder or nickname.")
	}
	if settings.Enabled || settings.C4DEnabled {
		return a.exportFBXAnimation(source, folder, filename, opts, settings, scoped, motionName)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	folderExisted := pycompat.Exists(folder)
	result, err := exportbundle.ExportBundle(source, folder, filename, nil, []string{"clip"})
	if err != nil {
		return nil, err
	}
	if err := a.recordExportLocked(folder, folderExisted, result.Files); err != nil {
		return nil, err
	}
	return result, nil
}

// exportFBXAnimation is _export_fbx_animation.
func (a *API) exportFBXAnimation(source, folder, filename string, options *ordered.Map[any], settings exportbundle.Options,
	scoped *formats.Asset, motionName string) (any, error) {
	entityPath := strOrEmpty(get(options, "entityPath"))
	if entityPath == "" || !pycompat.IsAbs(entityPath) {
		return nil, newValueError("Load an entity or model before saving an FBX or C4D copy.")
	}
	rigValue := getOr(options, "rigId", "auto")
	if scoped != nil {
		rigValue = scoped.Models[0].ID
	}
	rigID, ok := rigValue.(string)
	if !ok {
		return nil, newValueError("Invalid FBX animation target.")
	}
	rootValue := get(options, "rootPath")
	if !pycompat.Truthy(rootValue) {
		if root := a.Root(); root != "" {
			rootValue = root
		} else {
			rootValue = nil
		}
	}
	root := ""
	if pycompat.Truthy(rootValue) {
		text, isString := rootValue.(string)
		if !isString || !pycompat.IsAbs(text) {
			return nil, newValueError("Invalid FBX export root.")
		}
		root = text
	}
	if settings.C4DEnabled {
		if err := a.bridgeReady(); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Reload from the captured path, never whichever entity the user
	// happened to select after starting this export.
	asset := scoped
	if asset == nil {
		var err error
		if asset, err = a.deps.LoadAsset(entityPath, root); err != nil {
			return nil, err
		}
	}
	clip, err := a.deps.LoadClip(source)
	if err != nil {
		return nil, err
	}
	folderExisted := pycompat.Exists(folder)
	clipName := pypath.Stem(filename)
	build := func(destinations map[string]string, textureFolder string) (*exportbundle.Details, error) {
		materials := ordered.New[*ordered.Map[*formats.Material]]()
		var bakeWarnings []string
		if settings.IncludeMesh && settings.BakeTextures {
			var err error
			if materials, bakeWarnings, err = a.bake(asset, textureFolder); err != nil {
				return nil, err
			}
		} else if settings.C4DEnabled {
			// No texture export: retain material colors/factors without
			// writing references to original source images.
			materials = neutralMaterials(asset)
		}
		warnings := append(append(append([]string{}, asset.Warnings...), clip.Warnings...), bakeWarnings...)
		if destination, ok := destinations["fbx"]; ok {
			report, err := fbx.ExportFBX(destination, asset, clip, fbx.Options{UpAxis: settings.UpAxis,
				IncludeMesh: settings.IncludeMesh, RootMotion: settings.RootMotion, Materials: materials,
				ClipName: clipName, RigID: rigID})
			if err != nil {
				return nil, err
			}
			warnings = append(warnings, report.Warnings...)
		}
		if destination, ok := destinations["c4d"]; ok {
			payload, err := c4dexport.MakePayload(asset, clip, materials, c4dexport.PayloadOptions{
				IncludeMesh: settings.IncludeMesh, RootMotion: settings.RootMotion, ClipName: clipName, RigID: rigID, MotionClipName: motionName})
			if err != nil {
				return nil, err
			}
			bridgeWarnings, err := a.bridge.Export(payload, destination, textureFolder)
			if err != nil {
				return nil, err
			}
			warnings = append(append(warnings, payload.Warnings...), bridgeWarnings...)
		}
		return &exportbundle.Details{Warnings: dedupe(warnings)}, nil
	}
	var requested []string
	for _, kind := range []struct {
		name    string
		enabled bool
	}{{"clip", settings.ClipEnabled}, {"fbx", settings.Enabled}, {"c4d", settings.C4DEnabled}} {
		if kind.enabled {
			requested = append(requested, kind.name)
		}
	}
	result, err := exportbundle.ExportBundle(source, folder, filename, build, requested)
	if err != nil {
		return nil, err
	}
	if err := a.recordExportLocked(folder, folderExisted, result.Files); err != nil {
		return nil, err
	}
	return result, nil
}

// ExportAnimationSet is export_animation_set(entity_path, assignments,
// scene_name, output_folder=None, options=None): one complete C4D scene from
// explicit per-object assignments.
//
// This is independent of nickname-triggered selected-object copies. The
// complete entity hierarchy carries attachment motion; source clips are
// sampled only on their own rigs and never merged by shared bone names.
func (a *API) ExportAnimationSet(entityPath, assignments, sceneName, outputFolder, options any) (any, error) {
	if options != nil {
		if _, ok := dictOf(options); !ok {
			return nil, newValueError("Invalid scene export options.")
		}
	}
	opts := ordered.New[any]()
	if dict, ok := dictOf(options); ok {
		opts = cloneDict(dict)
	}
	forced := cloneDict(opts)
	forced.Set("clipEnabled", false)
	forced.Set("enabled", false)
	forced.Set("c4dEnabled", true)
	settings, err := exportbundle.FBXOptions(forced)
	if err != nil {
		return nil, err
	}
	if !isAbsArg(entityPath) {
		return nil, newValueError("Load an entity before exporting a combined animation.")
	}
	list, ok := assignments.([]any)
	if !ok || len(list) == 0 || len(list) > 512 {
		return nil, newValueError("Assign at least one animation to a loaded object before exporting.")
	}
	rootValue := get(opts, "rootPath")
	if !pycompat.Truthy(rootValue) {
		rootValue = a.Root()
	}
	root := ""
	if pycompat.Truthy(rootValue) {
		if root, err = pathOf(rootValue); err != nil {
			return nil, err
		}
		if !pycompat.IsAbs(root) {
			return nil, newValueError("Invalid export root.")
		}
	}
	name := pycompat.Strip(strOrEmpty(sceneName))
	if len(name) >= 4 && pyLower(name[len(name)-4:]) == ".c4d" {
		name = name[:len(name)-4]
	}
	filename, err := copyName(name)
	if err != nil {
		return nil, err
	}
	motionName := pypath.Stem(filename)
	if value, present := opts.Get("motionClipName"); present {
		if motionName, err = validateMotionClipName(value); err != nil {
			return nil, err
		}
	}
	destination := outputFolder
	if outputFolder == nil {
		a.mu.Lock()
		destination = pycompat.Clone(a.outputFolderLocked())
		a.mu.Unlock()
	}
	folder, err := folderPath(destination)
	if err != nil {
		return nil, err
	}
	var captured []c4dexport.Assignment
	seen := map[string]bool{}
	for _, item := range list {
		assignment, ok := dictOf(item)
		if !ok {
			return nil, newValueError("Invalid animation assignment.")
		}
		modelID, isString := get(assignment, "modelId").(string)
		if !isString || modelID == "" || runeLen(modelID) > 128 || seen[modelID] {
			return nil, newValueError("Each loaded object must have one unique animation assignment.")
		}
		clipPath, isString := get(assignment, "clipPath").(string)
		if !isString || !pycompat.IsAbs(clipPath) || pyLower(pypath.Suffix(clipPath)) != ".owanimclip" {
			return nil, newValueError("Each assigned animation must be an existing .owanimclip file.")
		}
		seen[modelID] = true
		clip, err := a.deps.LoadClip(clipPath)
		if err != nil {
			return nil, err
		}
		captured = append(captured, c4dexport.Assignment{ModelID: modelID, Clip: clip})
	}
	entity := entityPath.(string)
	asset, err := a.deps.LoadAsset(entity, root)
	if err != nil {
		return nil, err
	}
	var names any
	if value, ok := opts.Get("objectNames"); ok {
		names = value
	} else {
		if err := a.registerNames(entity, asset); err != nil {
			return nil, err
		}
		names = asset.ObjectNames
	}
	if asset, err = a.deps.ObjectNames.Apply(asset, names); err != nil {
		return nil, err
	}
	// Validate all models, clips, timing and hierarchy before contacting
	// Cinema or creating the output folder. Reuse this sampled payload.
	payload, err := c4dexport.MakeSetPayload(asset, captured, ordered.New[*ordered.Map[*formats.Material]](),
		c4dexport.SetOptions{IncludeMesh: settings.IncludeMesh, RootMotion: settings.RootMotion, Name: pypath.Stem(filename), MotionClipName: motionName})
	if err != nil {
		return nil, err
	}
	if err := a.bridgeReady(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	folderExisted := pycompat.Exists(folder)
	build := func(destinations map[string]string, textureFolder string) (*exportbundle.Details, error) {
		var materials Materials
		var bakeWarnings []string
		if settings.IncludeMesh && settings.BakeTextures {
			if materials, bakeWarnings, err = a.bake(asset, textureFolder); err != nil {
				return nil, err
			}
		} else {
			materials = neutralMaterials(asset)
		}
		payload.Materials = materials
		bridgeWarnings, err := a.bridge.Export(payload, destinations["c4d"], textureFolder)
		if err != nil {
			return nil, err
		}
		warnings := append(append(append([]string{}, payload.Warnings...), bakeWarnings...), bridgeWarnings...)
		for _, entry := range captured {
			warnings = append(warnings, entry.Clip.Warnings...)
		}
		return &exportbundle.Details{Warnings: dedupe(warnings)}, nil
	}
	result, err := exportbundle.ExportBundle(captured[0].Clip.Path, folder, filename, build, []string{"c4d"})
	if err != nil {
		return nil, err
	}
	extra := ordered.New[any]()
	summaries := payload.Animation.Assignments
	if summaries == nil {
		summaries = []c4dexport.AssignmentSummary{}
	}
	extra.Set("assignments", summaries)
	extra.Set("fps", payload.Animation.FPS)
	extra.Set("lastFrame", payload.Animation.LastFrame)
	result.Extra = extra
	if err := a.recordExportLocked(folder, folderExisted, result.Files); err != nil {
		return nil, err
	}
	return result, nil
}
