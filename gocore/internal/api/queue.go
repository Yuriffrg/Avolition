package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
)

// maxExportQueue is MAX_EXPORT_QUEUE (tests lower it).
var maxExportQueue = MaxExportQueue

var queueIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func nonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func validateMotionClipName(value any) (string, error) {
	name, ok := value.(string)
	if !ok || pycompat.Strip(name) == "" || runeLen(name) > 180 || strings.ContainsRune(name, '\x00') {
		return "", newValueError("Enter a valid motion clip nickname (1–180 characters).")
	}
	return name, nil
}

// Legacy queue records stored only an automatically prefixed scene filename.
// Strip that exact entity prefix once; explicit new nicknames are never guessed.
func queueMotionClipName(entry *ordered.Map[any]) string {
	if name, ok := get(entry, "motionClipName").(string); ok {
		return name
	}
	name := get(entry, "name").(string)
	prefix := pypath.Stem(get(entry, "entityPath").(string)) + "_"
	if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
		return name[len(prefix):]
	}
	return name
}

// exportQueueEntriesLocked is _export_queue_entries(): a validated deep copy.
func (a *API) exportQueueEntriesLocked() ([]any, error) {
	entries, ok := getOr(a.catalog, "exportQueue", []any{}).([]any)
	if !ok {
		return nil, newValueError("The saved export list is invalid. Restore a valid catalog.json before editing it.")
	}
	invalid := newValueError("The saved export list contains invalid entries. Restore a valid catalog.json before editing it.")
	seen := map[string]bool{}
	for _, item := range entries {
		entry, ok := dictOf(item)
		valid := ok
		if valid {
			for _, name := range []string{"id", "name", "entityPath", "rootPath"} {
				if !nonEmptyString(get(entry, name)) {
					valid = false
				}
			}
		}
		var assignments []any
		if valid {
			id := get(entry, "id").(string)
			valid = queueIDPattern.MatchString(id) && !seen[id]
			assignments, ok = get(entry, "assignments").([]any)
			valid = valid && ok && len(assignments) >= 1 && len(assignments) <= 512
		}
		if valid {
			models := map[string]bool{}
			for _, value := range assignments {
				assignment, ok := dictOf(value)
				modelID, isString := get(assignment, "modelId").(string)
				if !ok || !isString || modelID == "" || models[modelID] || !nonEmptyString(get(assignment, "clipPath")) {
					valid = false
					break
				}
				models[modelID] = true
			}
		}
		if !valid {
			return nil, invalid
		}
		if value, present := entry.Get("motionClipName"); present {
			if _, err := validateMotionClipName(value); err != nil {
				return nil, err
			}
		}
		if _, err := a.deps.ObjectNames.ValidateNames(getOr(entry, "objectNames", ordered.New[any]()), nil); err != nil {
			return nil, err
		}
		seen[get(entry, "id").(string)] = true
	}
	// Do not require source files on restore: unavailable entries must remain
	// visible so the user can retry after reconnecting their asset drive.
	return pycompat.Clone(entries).([]any), nil
}

func queueID(value any) (string, error) {
	text, ok := value.(string)
	if !ok || !queueIDPattern.MatchString(text) {
		return "", newValueError("Choose an existing entry from the export list.")
	}
	return text, nil
}

func (a *API) saveExportQueueLocked(entries []any) error {
	previous := getOr(a.catalog, "exportQueue", []any{})
	a.catalog.Set("exportQueue", entries)
	if err := a.save(); err != nil {
		a.catalog.Set("exportQueue", previous)
		return err
	}
	return nil
}

func tokenHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ExportQueueSave is export_queue_save(entry): save a named scene snapshot
// locally without exporting any files.
func (a *API) ExportQueueSave(value any) (any, error) {
	entry, ok := dictOf(value)
	if !ok || !keysSubset(entry, "id", "name", "entityPath", "rootPath", "assignments", "objectNames", "motionClipName") {
		return nil, newValueError("Invalid export list entry.")
	}
	ident := tokenHex()
	if id, found := entry.Get("id"); found {
		var err error
		if ident, err = queueID(id); err != nil {
			return nil, err
		}
	}
	name, isString := get(entry, "name").(string)
	if !isString {
		return nil, newValueError("Enter a name for this animation setup.")
	}
	name = pycompat.Strip(name)
	if len(name) >= 4 && pyLower(name[len(name)-4:]) == ".c4d" {
		name = name[:len(name)-4]
	}
	filename, err := copyName(name)
	if err != nil {
		return nil, err
	}
	name = pypath.Stem(filename)
	if value, present := entry.Get("motionClipName"); present {
		if _, err := validateMotionClipName(value); err != nil {
			return nil, err
		}
	}
	entity, entityOK := get(entry, "entityPath").(string)
	if !entityOK || !pycompat.IsAbs(entity) || !isEntitySuffix(entity) {
		return nil, newValueError("Load an entity or model before adding an export.")
	}
	root, rootOK := get(entry, "rootPath").(string)
	if !rootOK || !pycompat.IsAbs(root) || !pycompat.IsDir(root) {
		return nil, newValueError("Choose an existing Library Source folder before adding an export.")
	}
	assignments, ok := get(entry, "assignments").([]any)
	if !ok || len(assignments) < 1 || len(assignments) > 512 {
		return nil, newValueError("Assign at least one animation to an object before adding an export.")
	}
	captured := []any{}
	seen := map[string]bool{}
	var order []string
	for _, item := range assignments {
		assignment, ok := dictOf(item)
		if !ok || assignment.Len() != 2 || !hasKey(assignment, "modelId") || !hasKey(assignment, "clipPath") {
			return nil, newValueError("Invalid animation assignment.")
		}
		modelID, isString := get(assignment, "modelId").(string)
		if !isString || modelID == "" || runeLen(modelID) > 128 || seen[modelID] {
			return nil, newValueError("Each object must have one unique animation assignment.")
		}
		path, isString := get(assignment, "clipPath").(string)
		if !isString || !pycompat.IsAbs(path) || pyLower(pypath.Suffix(path)) != ".owanimclip" {
			return nil, newValueError("Each assigned animation must be an existing .owanimclip file.")
		}
		if _, err := a.deps.ReadClipHeader(path); err != nil {
			return nil, err
		}
		record := ordered.New[any]()
		record.Set("modelId", modelID)
		record.Set("clipPath", pycompat.Resolve(path))
		captured = append(captured, record)
		seen[modelID] = true
		order = append(order, modelID)
	}
	asset, err := a.deps.LoadAsset(entity, root)
	if err != nil {
		return nil, err
	}
	skeletons := map[string]bool{}
	for _, model := range asset.Models {
		if len(model.Bones) > 0 {
			skeletons[model.ID] = true
		}
	}
	for _, id := range order {
		if !skeletons[id] {
			return nil, newValueError("An assigned object is not a loaded skeleton. Reload the entity and choose its animation again.")
		}
	}
	var names any
	if value, found := entry.Get("objectNames"); found {
		names = value
	} else {
		if err := a.registerNames(entity, asset); err != nil {
			return nil, err
		}
		names = asset.ObjectNames
	}
	validated, err := a.deps.ObjectNames.ValidateNames(names, asset)
	if err != nil {
		return nil, err
	}
	record := ordered.New[any]()
	record.Set("id", ident)
	record.Set("name", name)
	record.Set("entityPath", pycompat.Resolve(entity))
	record.Set("rootPath", pycompat.Resolve(root))
	record.Set("assignments", captured)
	record.Set("objectNames", namesToAny(validated))
	if value, present := entry.Get("motionClipName"); present {
		record.Set("motionClipName", value)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entries, err := a.exportQueueEntriesLocked()
	if err != nil {
		return nil, err
	}
	existing := -1
	for i, item := range entries {
		if get(item.(*ordered.Map[any]), "id") == ident {
			existing = i
			break
		}
	}
	if hasKey(entry, "id") && existing < 0 {
		return nil, newValueError("This export list entry no longer exists. Add it again.")
	}
	if existing < 0 {
		if len(entries) >= maxExportQueue {
			return nil, newValueError(fmt.Sprintf("The export list is limited to %d entries. Remove completed entries first.", maxExportQueue))
		}
		entries = append(entries, record)
	} else {
		entries[existing] = record
	}
	if err := a.saveExportQueueLocked(entries); err != nil {
		return nil, err
	}
	result := ordered.New[any]()
	result.Set("entry", pycompat.Clone(record))
	result.Set("entries", pycompat.Clone(entries))
	return result, nil
}

func isEntitySuffix(path string) bool {
	suffix := pyLower(pypath.Suffix(path))
	return suffix == ".owentity" || suffix == ".owmdl"
}

// ExportQueueRemove is export_queue_remove(ident).
func (a *API) ExportQueueRemove(value any) (any, error) {
	ident, err := queueID(value)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entries, err := a.exportQueueEntriesLocked()
	if err != nil {
		return nil, err
	}
	remaining := []any{}
	for _, item := range entries {
		if get(item.(*ordered.Map[any]), "id") != ident {
			remaining = append(remaining, item)
		}
	}
	if len(remaining) == len(entries) {
		return nil, newValueError("This export list entry no longer exists.")
	}
	if err := a.saveExportQueueLocked(remaining); err != nil {
		return nil, err
	}
	result := ordered.New[any]()
	result.Set("entries", pycompat.Clone(remaining))
	return result, nil
}

// ExportQueueRun is export_queue_run(ident, output_folder=None, options=None):
// explicitly export a stored full-entity setup; never a raw clip copy.
func (a *API) ExportQueueRun(value, outputFolder, options any) (any, error) {
	ident, err := queueID(value)
	if err != nil {
		return nil, err
	}
	if options != nil {
		if _, ok := dictOf(options); !ok {
			return nil, newValueError("Invalid scene export options.")
		}
	}
	// Keep duplicate-job protection separate from the catalog lock, which
	// the native exporter holds while Cinema creates the document.
	a.queueMu.Lock()
	if a.queueRunning[ident] {
		a.queueMu.Unlock()
		return nil, newValueError("This animation setup is already exporting.")
	}
	a.queueRunning[ident] = true
	a.queueMu.Unlock()
	defer func() {
		a.queueMu.Lock()
		delete(a.queueRunning, ident)
		a.queueMu.Unlock()
	}()
	a.mu.Lock()
	entries, err := a.exportQueueEntriesLocked()
	var snapshot *ordered.Map[any]
	if err == nil {
		for _, item := range entries {
			if entry := item.(*ordered.Map[any]); get(entry, "id") == ident {
				snapshot = entry
				break
			}
		}
	}
	destination := outputFolder
	if outputFolder == nil {
		destination = pycompat.Clone(a.commonOutputLocked())
	}
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, newValueError("This export list entry no longer exists.")
	}
	settings := ordered.New[any]()
	if dict, ok := dictOf(options); ok {
		settings = cloneDict(dict)
	}
	settings.Set("rootPath", get(snapshot, "rootPath"))
	settings.Set("objectNames", getOr(snapshot, "objectNames", ordered.New[any]()))
	settings.Set("motionClipName", queueMotionClipName(snapshot))
	exportSet := a.ExportAnimationSet
	if a.exportSetHook != nil {
		exportSet = a.exportSetHook
	}
	return exportSet(get(snapshot, "entityPath"), get(snapshot, "assignments"), get(snapshot, "name"),
		destination, settings)
}
