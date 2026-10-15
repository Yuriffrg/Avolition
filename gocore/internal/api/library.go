package api

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
)

var (
	windowsUnsafe   = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	windowsReserved = regexp.MustCompile(`^(?i:CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])$`)
)

// folderPath is _folder_path(value).
func folderPath(value any) (string, error) {
	clean := pycompat.Strip(strOrEmpty(value))
	if runes := []rune(clean); len(runes) >= 2 && runes[0] == runes[len(runes)-1] && (runes[0] == '"' || runes[0] == '\'') {
		clean = pycompat.Strip(string(runes[1 : len(runes)-1]))
	}
	if clean == "" {
		return "", newValueError("Set an output folder before saving a renamed copy.")
	}
	path, err := pycompat.ExpandUser(pycompat.ExpandVars(clean))
	if err != nil {
		return "", &runtimeError{err.Error()}
	}
	if !pycompat.IsAbs(path) {
		return "", newValueError("Use the full path to the output folder.")
	}
	path = pycompat.Resolve(path)
	if pycompat.Exists(path) && !pycompat.IsDir(path) {
		return "", newValueError("The output path is a file. Choose a folder.")
	}
	return path, nil
}

// copyName is _copy_name(nickname).
func copyName(nickname any) (string, error) {
	name := pycompat.Strip(strOrEmpty(nickname))
	runes := []rune(name)
	if len(runes) >= 11 && pyLower(string(runes[len(runes)-11:])) == ".owanimclip" {
		name = string(runes[:len(runes)-11])
	}
	if name == "" || name == "." || name == ".." {
		return "", newValueError("Enter a nickname to name the animation copy.")
	}
	if windowsUnsafe.MatchString(name) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "", newValueError("The nickname contains characters that Windows cannot use in a filename.")
	}
	if windowsReserved.MatchString(strings.SplitN(name, ".", 2)[0]) {
		return "", newValueError("That nickname is a reserved Windows filename. Choose another name.")
	}
	if utf16Len(name) > 180 {
		return "", newValueError("Keep the nickname within 180 characters for the exported filename.")
	}
	return name + ".owanimclip", nil
}

// Bootstrap is bootstrap().
func (a *API) Bootstrap() (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var queueWarning string
	queue, err := a.exportQueueEntriesLocked()
	if err != nil {
		var ve *valueError
		if !asValueError(err, &ve) {
			return nil, err
		}
		// A damaged new export list must not hide the user's existing
		// library and annotations. Mutating queue APIs still reject it.
		queue, queueWarning = []any{}, err.Error()
	}
	var warnings []string
	if a.catalogWarning != nil && *a.catalogWarning != "" {
		warnings = append(warnings, *a.catalogWarning)
	}
	if queueWarning != "" {
		warnings = append(warnings, queueWarning)
	}
	var warning any
	if len(warnings) > 0 {
		warning = strings.Join(warnings, " ")
	}
	fbx, err := fbxOptions(a.catalogGet("fbxOptions"))
	if err != nil {
		return nil, err
	}
	mapExport, _ := a.mapExports.Snapshot(nil)
	result := ordered.New[any]()
	result.Set("lastRoot", pycompat.Clone(a.catalogGet("lastRoot")))
	result.Set("catalog", pycompat.Clone(a.catalogGet("annotations")))
	result.Set("outputFolder", pycompat.Clone(a.commonOutputLocked()))
	result.Set("warning", warning)
	result.Set("fbxOptions", fbx)
	result.Set("workflow", a.workflow.Settings())
	result.Set("libraryOptions", a.libraryOptionsLocked())
	result.Set("exportQueue", queue)
	result.Set("maps", a.mapSettingsLocked())
	result.Set("mapExport", mapExport)
	result.Set("version", Version)
	result.Set("appName", "Avolition")
	result.Set("browserMode", !a.desktopMode)
	return result, nil
}

var librarySorts = []string{"object-match", "tracks-desc", "tracks-asc", "name", "nicknamed-first",
	"size-desc", "size-asc", "duration-desc", "duration-asc"}

func isLibrarySort(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, sort := range librarySorts {
		if text == sort {
			return true
		}
	}
	return false
}

func (a *API) libraryOptionsLocked() *ordered.Map[any] {
	saved, _ := dictOf(a.catalogGet("libraryOptions"))
	options := ordered.New[any]()
	if sortValue := get(saved, "sort"); isLibrarySort(sortValue) {
		options.Set("sort", sortValue)
	} else {
		options.Set("sort", "object-match")
	}
	options.Set("hideDuplicates", getOr(saved, "hideDuplicates", true) != false)
	return options
}

// LibrarySettings is library_settings(changes=None).
func (a *API) LibrarySettings(changes any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.libraryOptionsLocked()
	if changes != nil {
		dict, ok := dictOf(changes)
		if !ok || !keysSubset(dict, "sort", "hideDuplicates") {
			return nil, newValueError("Invalid animation library settings.")
		}
		if value, found := dict.Get("sort"); found && !isLibrarySort(value) {
			return nil, newValueError("Unknown animation sorting option.")
		}
		if value, found := dict.Get("hideDuplicates"); found {
			if _, isBool := value.(bool); !isBool {
				return nil, newValueError("Hide duplicates must be on or off.")
			}
		}
		dict.Range(func(k string, v any) bool { options.Set(k, v); return true })
		a.catalog.Set("libraryOptions", options)
		if err := a.save(); err != nil {
			return nil, err
		}
		return cloneDict(options), nil
	}
	return options, nil
}

// SetExportSettings is set_export_settings(options).
func (a *API) SetExportSettings(options any) (any, error) {
	value, err := fbxOptions(options)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.catalog.Set("fbxOptions", value)
	if err := a.save(); err != nil {
		return nil, err
	}
	return cloneDict(value), nil
}

// SetOutputFolder is set_output_folder(value).
func (a *API) SetOutputFolder(value any) (any, error) {
	path, err := folderPath(value)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.catalog.Set("outputFolder", path)
	if a.root != "" {
		if folders := a.catalogDict("outputFolders"); folders != nil {
			folders.Set(key(a.root), path)
		}
	}
	if err := a.save(); err != nil {
		return nil, err
	}
	result := ordered.New[any]()
	result.Set("outputFolder", path)
	return result, nil
}

// OpenExportOutput is open_export_output().
func (a *API) OpenExportOutput() (any, error) {
	a.mu.Lock()
	destination := pycompat.Clone(a.commonOutputLocked())
	a.mu.Unlock()
	folder, err := folderPath(destination)
	if err != nil {
		return nil, err
	}
	if !pycompat.IsDir(folder) {
		return nil, newValueError("The export output folder is unavailable. Choose an existing folder or export first.")
	}
	return folder, nil
}

// Ping is ping().
func (a *API) Ping() bool {
	a.pingMu.Lock()
	defer a.pingMu.Unlock()
	a.lastPing, a.hasPing = time.Now(), true
	return true
}

// CloseApp is close_app(): the developer server stops shortly afterwards.
func (a *API) CloseApp() bool {
	a.serverMu.Lock()
	shutdown := a.shutdown
	a.serverMu.Unlock()
	if shutdown != nil {
		time.AfterFunc(300*time.Millisecond, shutdown)
	}
	return true
}

// SaveAnnotation is save_annotation(path, annotation).
func (a *API) SaveAnnotation(path, annotation any) (any, error) {
	dict, ok := dictOf(annotation)
	if !ok {
		return nil, newValueError("Invalid annotation")
	}
	value := ordered.New[any]()
	value.Set("name", pycompat.Slice(pycompat.Str(getOr(dict, "name", "")), 200))
	value.Set("notes", pycompat.Slice(pycompat.Str(getOr(dict, "notes", "")), 10000))
	value.Set("favorite", pycompat.Truthy(getOr(dict, "favorite", false)))
	a.mu.Lock()
	defer a.mu.Unlock()
	name, err := keyOf(path)
	if err != nil {
		return nil, err
	}
	annotations := a.catalogDict("annotations")
	if annotations == nil {
		return nil, &typeError{"'NoneType' object does not support item assignment"}
	}
	annotations.Set(name, value)
	if err := a.save(); err != nil {
		return nil, err
	}
	return cloneDict(value), nil
}

// LoadAnimation is load_animation(path).
func (a *API) LoadAnimation(path any) (any, error) {
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	clip, err := a.deps.LoadClip(pycompat.Resolve(text))
	if err != nil {
		return nil, err
	}
	return clipData{clip}, nil
}

// LoadEntity is load_entity(path).
func (a *API) LoadEntity(path any) (any, error) {
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	asset, err := a.deps.LoadAsset(pycompat.Resolve(text), a.Root())
	if err != nil {
		return nil, err
	}
	if err := a.textures.Register(asset.Textures, "animations"); err != nil {
		return nil, err
	}
	if err := a.registerNames(text, asset); err != nil {
		return nil, err
	}
	return assetData{asset}, nil
}

// PreviewEntity is preview_entity(path).
func (a *API) PreviewEntity(path any) (any, error) {
	a.previewMu.Lock()
	a.previewGeneration++
	generation := a.previewGeneration
	a.previewMu.Unlock()
	a.mu.Lock()
	root := a.root
	names, err := a.objectNamesLocked(path)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	asset, err := a.deps.LoadAsset(pycompat.Resolve(text), root)
	if err != nil {
		return nil, err
	}
	known, err := a.deps.ObjectNames.Keys(asset)
	if err != nil {
		return nil, err
	}
	accepted := filterNames(names, known)
	if accepted.Len() > 0 {
		if asset, err = a.deps.ObjectNames.Apply(asset, accepted); err != nil {
			return nil, err
		}
	}
	asset.ObjectNames = accepted
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	if generation != a.previewGeneration {
		return nil, newValueError("The model preview was closed or replaced.")
	}
	// Previewing candidates never registers editable scene names: doing so
	// would evict the active scene when browsing more than eight models.
	if err := a.textures.Register(asset.Textures, "model-browser"); err != nil {
		return nil, err
	}
	return assetData{asset}, nil
}

// ReleaseEntityPreview is release_entity_preview().
func (a *API) ReleaseEntityPreview() (any, error) {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	a.previewGeneration++
	if err := a.textures.Release("model-browser"); err != nil {
		return nil, err
	}
	result := ordered.New[any]()
	result.Set("released", true)
	return result, nil
}

// ClipEntry is one clip row of scan_root (read_clip_header's dict plus the
// library fields).
type ClipEntry struct {
	// Header is nil when the header could not be read (ErrorText is set).
	Header       *formats.ClipHeader
	Path, Name   string
	Size         int64
	ErrorText    string
	RelativePath string
	Annotation   any

	DuplicateKey   string
	DuplicateCount int
	DuplicateError *string
}

// MarshalJSON writes the keys in Python's insertion order.
func (c *ClipEntry) MarshalJSON() ([]byte, error) {
	out := ordered.New[any]()
	if c.Header != nil {
		h := c.Header
		out.Set("name", h.Name)
		out.Set("path", h.Path)
		out.Set("fps", h.FPS)
		out.Set("durationFrames", h.DurationFrames)
		out.Set("lastFrame", h.LastFrame)
		out.Set("durationSeconds", h.DurationSeconds)
		out.Set("boneCount", h.BoneCount)
		out.Set("size", h.Size)
		out.Set("version", h.Version)
	} else {
		out.Set("path", c.Path)
		out.Set("name", c.Name)
		out.Set("size", c.Size)
		out.Set("fps", 0)
		out.Set("durationFrames", 0)
		out.Set("durationSeconds", 0)
		out.Set("boneCount", 0)
		out.Set("error", c.ErrorText)
	}
	out.Set("relativePath", c.RelativePath)
	out.Set("annotation", c.Annotation)
	out.Set("duplicateKey", c.DuplicateKey)
	out.Set("duplicateCount", c.DuplicateCount)
	if c.DuplicateError != nil {
		out.Set("duplicateError", *c.DuplicateError)
	}
	return out.MarshalJSON()
}

// FilePath is clip['path'].
func (c *ClipEntry) FilePath() string {
	if c.Header != nil {
		return c.Header.Path
	}
	return c.Path
}

// EntityEntry is one entity/model row of scan_root.
type EntityEntry struct {
	Path, Name, RelativePath, Kind string
	BoneCount                      *int
	BoneCountError                 string
	Size                           *int64
	SizeError                      *string
}

// MarshalJSON writes the keys in Python's insertion order.
func (e *EntityEntry) MarshalJSON() ([]byte, error) {
	out := ordered.New[any]()
	out.Set("path", e.Path)
	out.Set("name", e.Name)
	out.Set("relativePath", e.RelativePath)
	out.Set("kind", e.Kind)
	out.Set("boneCount", e.BoneCount)
	out.Set("boneCountError", e.BoneCountError)
	out.Set("size", e.Size)
	if e.SizeError != nil {
		out.Set("sizeError", *e.SizeError)
	}
	return out.MarshalJSON()
}

type scanResult struct {
	Root               string         `json:"root"`
	OutputFolder       any            `json:"outputFolder"`
	Entities           []*EntityEntry `json:"entities"`
	Clips              []*ClipEntry   `json:"clips"`
	UniqueClipCount    int            `json:"uniqueClipCount"`
	DuplicateClipCount int            `json:"duplicateClipCount"`
	Warnings           []string       `json:"warnings"`
}

// mapAssetFolders is library_scan.MAP_ASSET_FOLDERS.
var mapAssetFolders = map[string]bool{"models": true, "entities": true, "effects": true, "textures": true,
	"sound": true, "sounds": true, "voicesets": true, "animations": true, "materials": true, "modellooks": true}

func hasMapMarker(files []string) bool {
	for _, name := range files {
		if strings.HasSuffix(pycompat.Casefold(name), ".owmap") {
			return true
		}
	}
	return false
}

// insideMapAssets is library_scan.inside_map_assets(path).
func insideMapAssets(path string) bool {
	child := path
	for _, parent := range pypath.Parents(path) {
		if mapAssetFolders[pycompat.Casefold(pycompat.Name(child))] {
			if entries, err := readDirNames(parent); err == nil {
				for _, entry := range entries {
					if strings.HasSuffix(pycompat.Casefold(entry.Name()), ".owmap") && entryIsFile(parent, entry) {
						return true
					}
				}
			}
			// Unreadable parent metadata is not evidence of map ownership.
		}
		child = parent
	}
	return false
}

func entryIsFile(parent string, entry os.DirEntry) bool {
	if entry.Type()&os.ModeSymlink != 0 {
		return pycompat.IsFile(pycompat.Join(parent, entry.Name()))
	}
	return entry.Type().IsRegular()
}

func readDirNames(dir string) ([]os.DirEntry, error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}

type entitySortKey struct {
	notEntity bool
	priority  int
	relative  string
}

// entitySortKeyOf is library_scan.entity_sort_key(item).
func entitySortKeyOf(item *EntityEntry) entitySortKey {
	entity := item.Kind == "entity"
	relative := item.RelativePath
	if relative == "" {
		relative = item.Path
	}
	if relative == "" {
		relative = item.Name
	}
	filename := item.Name
	if filename == "" {
		parts := strings.Split(strings.ReplaceAll(relative, `\`, "/"), "/")
		filename = parts[len(parts)-1]
	}
	filename = pycompat.Casefold(filename)
	priority := 2
	if entity {
		switch filename {
		case "gameplay1p.owentity":
			priority = 0
		case "gameplay3p.owentity":
			priority = 1
		}
	}
	return entitySortKey{!entity, priority, pyLower(relative)}
}

func (k entitySortKey) less(o entitySortKey) bool {
	if k.notEntity != o.notEntity {
		return !k.notEntity
	}
	if k.priority != o.priority {
		return k.priority < o.priority
	}
	return k.relative < o.relative
}

// setOfStrings is set(list) for a catalog list (unhashable items fail).
func setOfStrings(list []any) (map[string]bool, error) {
	set := make(map[string]bool, len(list))
	for _, item := range list {
		switch v := item.(type) {
		case string:
			set[v] = true
		case []any, *ordered.Map[any]:
			return nil, &typeError{"unhashable type: '" + pycompat.TypeName(item) + "'"}
		}
	}
	return set, nil
}

// ScanRoot is scan_root(value, activate=True).
func (a *API) ScanRoot(value, activate any) (any, error) {
	clean := strings.Trim(pycompat.Strip(pycompat.Str(value)), `"`)
	if clean == "" {
		return nil, newValueError("Paste the full path to your extracted files first.")
	}
	expanded, err := pycompat.ExpandUser(pycompat.ExpandVars(clean))
	if err != nil {
		return nil, &runtimeError{err.Error()}
	}
	path := pycompat.Resolve(expanded)
	if !pycompat.IsDir(path) {
		return nil, newValueError("Root folder does not exist. Paste the full path to your extracted files.")
	}
	activated := pycompat.Truthy(activate)
	var outputFolder any
	var outputDirectories, exportedFiles map[string]bool
	err = func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		firstRoot := a.root == ""
		rootKey := key(path)
		folders := a.catalogDict("outputFolders")
		if activated {
			a.root = path
			a.catalog.Set("lastRoot", path)
			if _, ok := folders.Get(rootKey); !ok {
				common := a.catalogGet("outputFolder")
				if firstRoot && pycompat.Truthy(common) {
					folders.Set(rootKey, common)
				} else {
					folders.Set(rootKey, pycompat.Join(path, "Renamed Animations"))
				}
			}
		}
		outputFolder = getOr(folders, rootKey, pycompat.Join(path, "Renamed Animations"))
		// The export list has one explicit shared destination. Scanning
		// another library must not replace that destination on restart.
		if activated && !pycompat.Truthy(a.catalogGet("outputFolder")) {
			a.catalog.Set("outputFolder", outputFolder)
		}
		outputFolder = pycompat.Clone(outputFolder)
		// A pre-existing destination may also contain original assets.
		// Prune only folders this app created; otherwise filter our copies.
		var err error
		if outputDirectories, err = setOfStrings(a.catalogList("managedOutputDirectories")); err != nil {
			return err
		}
		if exportedFiles, err = setOfStrings(a.catalogList("exportedFiles")); err != nil {
			return err
		}
		if activated {
			return a.save()
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	entities := []*EntityEntry{}
	clips := []*ClipEntry{}
	warnings := []string{}
	count := 0
	unreadable := func(err error) {
		if len(warnings) < 50 {
			warnings = append(warnings, pyOSError(err).Error())
		}
	}
	var annotations *ordered.Map[any]
	a.mu.Lock()
	if dict := a.catalogDict("annotations"); dict != nil {
		annotations = cloneDict(dict)
	}
	a.mu.Unlock()
	annotation := func(item string) any {
		if annotations != nil {
			if value, ok := annotations.Get(key(item)); ok {
				return value
			}
		}
		return defaultAnnotation()
	}
	// Map variants own their neighboring exported asset branches. Do not
	// decode those thousands of model/clip dependencies as character assets.
	if !insideMapAssets(path) {
		pypath.Walk(path, unreadable, func(parent string, dirs *[]string, files []string) bool {
			mapExport := hasMapMarker(files)
			kept := (*dirs)[:0]
			for _, d := range *dirs {
				if d == ".git" || d == "__pycache__" || (mapExport && mapAssetFolders[pycompat.Casefold(d)]) {
					continue
				}
				if len(outputDirectories) > 0 && outputDirectories[key(pycompat.Join(parent, d))] {
					continue
				}
				kept = append(kept, d)
			}
			*dirs = kept
			if mapExport {
				return true
			}
			for _, name := range files {
				count++
				if count > 250000 {
					warnings = append(warnings, "Scan stopped after 250,000 files. Select a more specific root folder.")
					break
				}
				item := pycompat.Join(parent, name)
				suffix := pyLower(pypath.Suffix(item))
				if suffix == ".owanimclip" && len(exportedFiles) > 0 && exportedFiles[key(item)] {
					continue
				}
				switch suffix {
				case ".owentity", ".owmdl":
					kind := "model"
					if suffix == ".owentity" {
						kind = "entity"
					}
					entities = append(entities, &EntityEntry{Path: item, Name: name, RelativePath: relativeTo(item, path), Kind: kind})
				case ".owanimclip":
					entry := &ClipEntry{}
					if header, err := a.deps.ReadClipHeader(item); err == nil {
						entry.Header = header
					} else {
						var size int64
						if info, statErr := os.Stat(item); statErr == nil {
							size = info.Size()
						}
						entry.Path, entry.Name, entry.Size, entry.ErrorText = item, name, size, errorText(err)
					}
					entry.RelativePath = relativeTo(item, path)
					entry.Annotation = annotation(item)
					clips = append(clips, entry)
				}
			}
			return count <= 250000
		})
	}
	sort.SliceStable(entities, func(i, j int) bool { return entitySortKeyOf(entities[i]).less(entitySortKeyOf(entities[j])) })
	paths := make([]string, len(entities))
	for i, item := range entities {
		paths[i] = item.Path
	}
	for i, metadata := range a.deps.BoneCounts(path, paths) {
		item := entities[i]
		item.BoneCount, item.BoneCountError = metadata.BoneCount, metadata.BoneCountError
		if info, err := os.Stat(item.Path); err == nil {
			size := info.Size()
			item.Size = &size
		} else {
			text := pyOSError(err).Error()
			item.SizeError = &text
		}
	}
	sort.SliceStable(clips, func(i, j int) bool { return pyLower(clips[i].RelativePath) < pyLower(clips[j].RelativePath) })
	duplicateWarnings := a.duplicates.Annotate(clips)
	if room := 50 - len(warnings); room > 0 {
		if len(duplicateWarnings) > room {
			duplicateWarnings = duplicateWarnings[:room]
		}
		warnings = append(warnings, duplicateWarnings...)
	}
	unique := map[string]bool{}
	for _, clip := range clips {
		unique[clip.DuplicateKey] = true
	}
	// Evidence analysis is currently an opt-in backend research capability;
	// ordinary library scans do not start or restore analysis jobs.
	return &scanResult{Root: path, OutputFolder: outputFolder, Entities: entities, Clips: clips,
		UniqueClipCount: len(unique), DuplicateClipCount: len(clips) - len(unique), Warnings: warnings}, nil
}

func asValueError(err error, target **valueError) bool {
	if ve, ok := err.(*valueError); ok {
		*target = ve
		return true
	}
	switch err.(type) {
	case *pycompat.ValueError, *formats.FormatError:
		*target = &valueError{err.Error()}
		return true
	}
	return false
}

var _ = json.Marshal
