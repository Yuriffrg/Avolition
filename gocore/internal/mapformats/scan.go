package mapformats

import (
	"sort"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// DefaultMaxScanFiles is scan_maps' default max_files.
const DefaultMaxScanFiles = 500_000

// ScanEntry is one map found by ScanMaps: its header plus its path relative
// to the scanned root.
type ScanEntry struct {
	HeaderInfo
	RelativePath string `json:"relativePath"`
}

// ScanResult is scan_maps' result.
type ScanResult struct {
	Root   string      `json:"root"`
	Maps   []ScanEntry `json:"maps"`
	Errors []string    `json:"errors"`
}

// casefold approximates str.casefold() (full Unicode case folding): lower
// case plus the folds that change length for common Latin text.
func casefold(s string) string {
	s = strings.ToLower(s)
	if strings.ContainsAny(s, "ßẞ") {
		s = strings.NewReplacer("ß", "ss", "ẞ", "ss").Replace(s)
	}
	return s
}

// ScanMaps finds actual map headers without decoding geometry or following
// directory links (Python's scan_maps). maxFiles <= 0 means
// DefaultMaxScanFiles. Unreadable folders and invalid maps are reported in
// Errors, in os.walk order.
func ScanMaps(root string, maxFiles int) (*ScanResult, error) {
	if maxFiles <= 0 {
		maxFiles = DefaultMaxScanFiles
	}
	root = pypath.Resolve(root)
	if !pypath.IsDir(root) {
		return nil, &ValueError{"Choose an existing map source folder."}
	}
	result := &ScanResult{Root: root, Maps: []ScanEntry{}, Errors: []string{}}
	count := 0
	var walkErr error
	pypath.Walk(root, func(err error) {
		result.Errors = append(result.Errors, formats.PythonOSError(err).Error())
	}, func(dir string, _ *[]string, files []string) bool {
		for _, name := range files {
			count++
			if count > maxFiles {
				walkErr = &ValueError{"Map scan exceeds " + formats.Commas(maxFiles) + " files. Choose a smaller source folder."}
				return false
			}
			if strings.ToLower(pypath.Suffix(name)) != ".owmap" {
				continue
			}
			path := pypath.Join(dir, name)
			if !pypath.IsRelativeTo(pypath.Resolve(path), root) {
				result.Errors = append(result.Errors, name+": map reference leaves the source folder.")
				continue
			}
			header, err := ReadMapHeader(path, nil)
			if err != nil {
				result.Errors = append(result.Errors, formats.PythonOSError(err).Error())
				continue
			}
			result.Maps = append(result.Maps, ScanEntry{HeaderInfo: *header, RelativePath: relativeTo(path, root)})
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.SliceStable(result.Maps, func(i, j int) bool {
		a, b := casefold(result.Maps[i].Name), casefold(result.Maps[j].Name)
		if a != b {
			return a < b
		}
		return casefold(result.Maps[i].Path) < casefold(result.Maps[j].Path)
	})
	return result, nil
}

// relativeTo is str(path.relative_to(root)) for a path the walk built under root.
func relativeTo(path, root string) string {
	rest := path[len(root):]
	for len(rest) > 0 && (rest[0] == '/' || rest[0] == '\\') {
		rest = rest[1:]
	}
	return rest
}
