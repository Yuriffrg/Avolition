// Package libraryscan ports owpreview/library_scan.py: it separates OWLib map
// dependencies from the character animation library.
package libraryscan

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// MapAssetFolders are the branches OWLib places beside its .owmap variants.
// Classify them only when the parent has a map marker; character exports use
// some of these names too. Compare casefolded names (see IsMapAssetFolder).
var MapAssetFolders = map[string]struct{}{
	"models": {}, "entities": {}, "effects": {}, "textures": {}, "sound": {}, "sounds": {},
	"voicesets": {}, "animations": {}, "materials": {}, "modellooks": {},
}

// IsMapAssetFolder reports `name.casefold() in MAP_ASSET_FOLDERS`.
func IsMapAssetFolder(name string) bool {
	_, ok := MapAssetFolders[casefold(name)]
	return ok
}

// isMapMarker reports name.casefold().endswith('.owmap').
func isMapMarker(name string) bool {
	if len(name) < 6 {
		return false
	}
	ascii := true
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.EqualFold(name[len(name)-6:], ".owmap")
	}
	return strings.HasSuffix(casefold(name), ".owmap")
}

// HasMapMarker reports whether any file name is an .owmap variant.
func HasMapMarker(filenames []string) bool {
	for _, name := range filenames {
		if isMapMarker(name) {
			return true
		}
	}
	return false
}

// Item holds the scan-row fields EntitySortKey reads. Empty strings stand for
// missing keys, as Python's `or` chain treats both alike.
type Item struct {
	Kind         string
	RelativePath string
	Path         string
	Name         string
}

// SortKey is entity_sort_key's tuple (not entity, priority, relative.lower()).
type SortKey struct {
	NotEntity bool
	Priority  int
	Relative  string
}

// Less orders keys like Python tuples (False < True, code-point strings).
func (k SortKey) Less(other SortKey) bool {
	if k.NotEntity != other.NotEntity {
		return !k.NotEntity
	}
	if k.Priority != other.Priority {
		return k.Priority < other.Priority
	}
	// Byte order of UTF-8 equals code point order.
	return k.Relative < other.Relative
}

// EntitySortKey shows the playable first/third-person entities before other
// scan results.
func EntitySortKey(item Item) SortKey {
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
		slashed := strings.ReplaceAll(relative, "\\", "/")
		filename = slashed[strings.LastIndexByte(slashed, '/')+1:]
	}
	priority := 2
	if entity {
		switch casefold(filename) {
		case "gameplay1p.owentity":
			priority = 0
		case "gameplay3p.owentity":
			priority = 1
		}
	}
	return SortKey{NotEntity: !entity, Priority: priority, Relative: pyLower(relative)}
}

// SortEntities stably sorts rows by EntitySortKey, as entities.sort(key=...).
func SortEntities[T any](rows []T, item func(T) Item) {
	keys := make([]SortKey, len(rows))
	for i, row := range rows {
		keys[i] = EntitySortKey(item(row))
	}
	index := make([]int, len(rows))
	for i := range index {
		index[i] = i
	}
	sort.SliceStable(index, func(a, b int) bool { return keys[index[a]].Less(keys[index[b]]) })
	sorted := make([]T, len(rows))
	for i, j := range index {
		sorted[i] = rows[j]
	}
	copy(rows, sorted)
}

// readDir is replaceable in tests (Python's tests patch os.scandir).
var readDir = os.ReadDir

// InsideMapAssets also excludes dependencies when the user scans inside a map
// export. Only parents of recognized asset branches are inspected: a mixed
// library with a loose map and a Heroes sibling must retain its character
// assets.
func InsideMapAssets(path string) bool {
	anchor, parts := pureParts(path)
	for k := len(parts) - 1; k >= 0; k-- {
		// parts[k] is child.name; the parent holds parts[:k].
		if !IsMapAssetFolder(parts[k]) {
			continue
		}
		parent := joinParts(anchor, parts[:k])
		entries, err := readDir(parent)
		if err != nil {
			// Unreadable parent metadata is not evidence of map ownership.
			continue
		}
		for _, entry := range entries {
			if isMapMarker(entry.Name()) && isFile(parent, entry) {
				return true
			}
		}
	}
	return false
}

// isFile mirrors os.DirEntry.is_file(): symlinks are followed, errors are false.
func isFile(parent string, entry os.DirEntry) bool {
	if entry.Type().IsRegular() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.Mode().IsRegular()
}

// pureParts splits a path like pathlib.PurePath: empty and "." components are
// dropped, ".." is kept (no lexical resolution).
func pureParts(path string) (anchor string, parts []string) {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	isSep := func(r rune) bool { return r == '/' || (runtime.GOOS == "windows" && r == '\\') }
	if rest != "" && isSep(rune(rest[0])) {
		anchor = volume + string(filepath.Separator)
	} else {
		anchor = volume
	}
	for _, part := range strings.FieldsFunc(rest, isSep) {
		if part != "." {
			parts = append(parts, part)
		}
	}
	return anchor, parts
}

func joinParts(anchor string, parts []string) string {
	joined := strings.Join(parts, string(filepath.Separator))
	if anchor == "" {
		if joined == "" {
			return "."
		}
		return joined
	}
	return anchor + joined
}
