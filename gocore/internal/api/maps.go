package api

import (
	"os"
	"sort"
	"strconv"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
	"avolition/gocore/internal/workflowpaths"
)

// mapSettingsLocked is _map_settings().
func (a *API) mapSettingsLocked() *ordered.Map[any] {
	saved, _ := dictOf(a.catalogGet("maps"))
	settings := ordered.New[any]()
	root := get(saved, "root")
	if !pycompat.Truthy(root) {
		root = a.workflow.Library
	}
	settings.Set("root", pycompat.Clone(root))
	output := get(saved, "outputFolder")
	if !pycompat.Truthy(output) {
		output = a.catalogGet("outputFolder")
	}
	if !pycompat.Truthy(output) {
		output = pycompat.Join(a.workflow.Library, "Map exports")
	}
	settings.Set("outputFolder", pycompat.Clone(output))
	settings.Set("bakeTextures", getOr(saved, "bakeTextures", true) != false)
	return settings
}

// MapsSettings is maps_settings(changes=None).
func (a *API) MapsSettings(changes any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mapsSettingsLocked(changes)
}

func (a *API) mapsSettingsLocked(changes any) (any, error) {
	settings := a.mapSettingsLocked()
	if changes == nil {
		return settings, nil
	}
	dict, ok := dictOf(changes)
	if !ok || !keysSubset(dict, "root", "outputFolder", "bakeTextures") {
		return nil, newValueError("Invalid map workspace settings.")
	}
	for _, field := range []string{"root", "outputFolder"} {
		if value, found := dict.Get(field); found {
			folder, err := workflowpaths.AbsoluteFolder(value, field)
			if err != nil {
				return nil, err
			}
			settings.Set(field, folder)
			if folder == "" {
				return nil, newValueError("Choose a full folder path.")
			}
		}
	}
	if value, found := dict.Get("bakeTextures"); found {
		flag, isBool := value.(bool)
		if !isBool {
			return nil, newValueError("Bake textures must be on or off.")
		}
		settings.Set("bakeTextures", flag)
	}
	previous, existed := a.catalog.Get("maps")
	a.catalog.Set("maps", settings)
	if err := a.save(); err != nil {
		if !existed || previous == nil {
			a.catalog.Delete("maps")
		} else {
			a.catalog.Set("maps", previous)
		}
		return nil, err
	}
	return cloneDict(settings), nil
}

type mapEntry struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
}

type scanMapsResult struct {
	Root     any         `json:"root"`
	Maps     []*mapEntry `json:"maps"`
	Warnings []string    `json:"warnings"`
}

// ScanMaps is scan_maps(root).
func (a *API) ScanMaps(root any) (any, error) {
	resolved, err := workflowpaths.AbsoluteFolder(root, "Map library source")
	if err != nil {
		return nil, err
	}
	if resolved == "" {
		return nil, newValueError("Choose a full Map library source folder path.")
	}
	folder := resolved
	if !pycompat.IsDir(folder) {
		return nil, newValueError("Map Library source is unavailable. Choose an existing exported asset folder.")
	}
	maps := []*mapEntry{}
	warnings := []string{}
	a.mu.Lock()
	managed := a.catalogList("managedOutputDirectories")
	a.mu.Unlock()
	var excluded []string
	for _, value := range managed {
		text, err := pathOf(value)
		if err != nil {
			return nil, err
		}
		excluded = append(excluded, pycompat.PathStr(text))
	}
	var limitErr error
	pypath.Walk(folder, func(err error) {
		if len(warnings) < 50 {
			warnings = append(warnings, pyOSError(err).Error())
		}
	}, func(parent string, dirs *[]string, files []string) bool {
		kept := (*dirs)[:0]
		for _, name := range *dirs {
			child := pycompat.Join(parent, name)
			if info, err := os.Lstat(child); err == nil && info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			inside := false
			if len(excluded) > 0 {
				resolvedChild := pycompat.Resolve(child)
				for _, output := range excluded {
					if pycompat.IsRelativeTo(resolvedChild, output) {
						inside = true
						break
					}
				}
			}
			if !inside {
				kept = append(kept, name)
			}
		}
		*dirs = kept
		for _, filename := range files {
			if pyLower(pypath.Suffix(filename)) != ".owmap" {
				continue
			}
			item := pycompat.Join(parent, filename)
			if info, err := os.Stat(item); err == nil {
				maps = append(maps, &mapEntry{Path: pycompat.Resolve(item), Name: pypath.Stem(item),
					RelativePath: relativeTo(item, folder), Size: info.Size()})
			} else {
				warnings = append(warnings, filename+": "+pyOSError(err).Error())
			}
			if len(maps) >= 10000 {
				limitErr = newValueError("This folder has more than 10,000 map files. Choose a smaller Library source.")
				return false
			}
		}
		return true
	})
	if limitErr != nil {
		return nil, limitErr
	}
	change := ordered.New[any]()
	change.Set("root", folder)
	a.mu.Lock()
	settings, err := a.mapsSettingsLocked(change)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(maps, func(i, j int) bool {
		return pycompat.Casefold(maps[i].RelativePath) < pycompat.Casefold(maps[j].RelativePath)
	})
	return &scanMapsResult{Root: get(settings.(*ordered.Map[any]), "root"), Maps: maps, Warnings: warnings}, nil
}

// LoadMap is load_map(path, root=None).
func (a *API) LoadMap(path, root any) (any, error) {
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	if !pycompat.Truthy(root) {
		a.mu.Lock()
		root = get(a.mapSettingsLocked(), "root")
		a.mu.Unlock()
	}
	rootText, err := pathOf(root)
	if err != nil {
		return nil, err
	}
	asset, err := a.deps.LoadMap(text, rootText)
	if err != nil {
		return nil, err
	}
	if err := a.textures.Register(asset.Textures, "maps"); err != nil {
		return nil, err
	}
	if err := a.registerNames(text, asset); err != nil {
		return nil, err
	}
	return assetData{asset}, nil
}

// OpenMapOutput is open_map_output().
func (a *API) OpenMapOutput() (any, error) {
	a.mu.Lock()
	output := get(a.mapSettingsLocked(), "outputFolder")
	a.mu.Unlock()
	folder, err := folderPath(output)
	if err != nil {
		return nil, err
	}
	if !pycompat.IsDir(folder) {
		return nil, newValueError("Map output folder is unavailable. Choose an existing folder or export first.")
	}
	return folder, nil
}

// mapExportRequest captures all user-controlled choices before the worker
// can begin (_map_export_request).
type mapExportRequest struct {
	path, name, root, folder string
	names                    *ordered.Map[string]
	folderExisted            bool
	bakeTextures             bool
}

func (a *API) mapExportRequest(path, name, root, output, bakeTextures any) (*mapExportRequest, error) {
	filename, err := copyName(name)
	if err != nil {
		return nil, err
	}
	bake, isBool := bakeTextures.(bool)
	if !isBool {
		return nil, newValueError("Bake textures must be on or off.")
	}
	a.mu.Lock()
	names, err := a.objectNamesLocked(path)
	settings := a.mapSettingsLocked()
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !pycompat.Truthy(output) {
		output = get(settings, "outputFolder")
	}
	folder, err := folderPath(output)
	if err != nil {
		return nil, err
	}
	if !pycompat.Truthy(root) {
		root = get(settings, "root")
	}
	rootFolder, err := workflowpaths.AbsoluteFolder(root, "Map library source")
	if err != nil {
		return nil, err
	}
	return &mapExportRequest{path: pycompat.Resolve(path.(string)), name: pypath.Stem(filename), names: cloneNames(names),
		root: rootFolder, folder: folder, folderExisted: pycompat.Exists(folder), bakeTextures: bake}, nil
}

func (a *API) runMapExport(request *mapExportRequest, progress Progress) (any, error) {
	progress("Loading map…", nil, nil)
	asset, err := a.deps.LoadMap(request.path, request.root)
	if err != nil {
		return nil, err
	}
	known, err := a.deps.ObjectNames.Keys(asset)
	if err != nil {
		return nil, err
	}
	if asset, err = a.deps.ObjectNames.Apply(asset, filterNames(request.names, known)); err != nil {
		return nil, err
	}
	result, err := a.deps.ExportMap(asset, request.folder, request.name, request.bakeTextures, a.bridge, progress)
	if err != nil {
		return nil, err
	}
	progress("Recording exported map…", nil, nil)
	var files []string
	if list, ok := get(result, "files").([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok {
				files = append(files, text)
			}
		}
	} else if list, ok := get(result, "files").([]string); ok {
		files = list
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.recordExportLocked(request.folder, request.folderExisted, files); err != nil {
		return nil, err
	}
	return result, nil
}

// ExportMap is export_map / export_map_start.
func (a *API) ExportMap(path, name, root, output, bakeTextures any, background bool) (any, error) {
	request, err := a.mapExportRequest(path, name, root, output, bakeTextures)
	if err != nil {
		return nil, err
	}
	operation := func(progress Progress) (any, error) { return a.runMapExport(request, progress) }
	if background {
		return a.mapExports.Start(operation)
	}
	return a.mapExports.Run(operation)
}

var _ = formats.LoadAsset
var _ = strconv.Itoa
