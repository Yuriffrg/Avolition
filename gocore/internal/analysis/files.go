package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// Path helpers reproducing pathlib/os.path behavior on the host.

// normcase is os.path.normcase.
func normcase(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	}
	return path
}

// Key is key(path): os.path.normcase(str(Path(path).resolve())).
func Key(path string) string { return normcase(pypath.Resolve(path)) }

// pathEqual is Path.__eq__ (case-insensitive on Windows).
func pathEqual(a, b string) bool { return normcase(a) == normcase(b) }

// comparePaths is Path ordering: the normcased string split at the
// separator, compared as lists.
func comparePaths(a, b string) int {
	sep := string(filepath.Separator)
	pa, pb := strings.Split(normcase(a), sep), strings.Split(normcase(b), sep)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if c := strings.Compare(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	return len(pa) - len(pb)
}

func sortPaths(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool { return comparePaths(paths[i], paths[j]) < 0 })
}

// asPosix is PurePath.as_posix().
func asPosix(path string) string {
	if filepath.Separator == '\\' {
		return strings.ReplaceAll(path, `\`, "/")
	}
	return path
}

// relativeTo is path.relative_to(root) for a path lexically under root.
func relativeTo(path, root string) string {
	if pathEqual(path, root) {
		return "."
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return path[len(prefix):]
}

// parts is PurePath.parts: the anchor (if any) and the components.
func parts(path string) []string {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	var out []string
	anchor := volume
	if rest != "" && os.IsPathSeparator(rest[0]) {
		anchor += string(filepath.Separator)
	}
	if anchor != "" {
		out = append(out, anchor)
	}
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r < 0x80 && os.IsPathSeparator(byte(r)) }) {
		if part != "." {
			out = append(out, part)
		}
	}
	return out
}

// casefold approximates str.casefold(): full case folding for the
// characters whose folding differs from simple lower-casing in common text,
// simple folding otherwise.
func casefold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 'ß', 'ẞ':
			b.WriteString("ss")
		case 'İ':
			b.WriteString("i̇")
		case 'ŉ':
			b.WriteString("ʼn")
		case 'ﬀ':
			b.WriteString("ff")
		case 'ﬁ':
			b.WriteString("fi")
		case 'ﬂ':
			b.WriteString("fl")
		case 'ﬃ':
			b.WriteString("ffi")
		case 'ﬄ':
			b.WriteString("ffl")
		case 'ﬅ', 'ﬆ':
			b.WriteString("st")
		default:
			if r < 0x80 {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToLower(unicode.ToUpper(r)))
			}
		}
	}
	return b.String()
}

var suffixes = map[string]bool{".owanimclip": true, ".owmdl": true, ".owentity": true, ".owanim": true, ".oweffect": true}

var provenanceNames = map[string]bool{".build.info": true, "manifest.json": true, "export-manifest.json": true, "guidnames.csv": true}

const maxFiles = 250000

type valueError string

func (e valueError) Error() string { return string(e) }

type walkedFile struct{ path, resolved string }

// files is _files(root, excluded): relevant files under root, in os.walk
// order filtered like Python, then sorted by as_posix().casefold().
func files(root string, excluded []string) ([]walkedFile, error) {
	exclusions := make([]string, len(excluded))
	for i, p := range excluded {
		exclusions[i] = pypath.Resolve(p)
	}
	excludedPath := func(resolved string) bool {
		for _, p := range exclusions {
			if pypath.IsRelativeTo(resolved, p) {
				return true
			}
		}
		return false
	}
	var result []walkedFile
	count := 0
	var walk func(directory string) error
	walk = func(directory string) error {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return formats.PythonOSError(err)
		}
		var dirs, names []os.DirEntry
		for _, entry := range entries {
			isDir := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				// os.walk classifies with is_dir(), which follows links.
				info, statErr := os.Stat(filepath.Join(directory, entry.Name()))
				isDir = statErr == nil && info.IsDir()
			}
			if isDir {
				dirs = append(dirs, entry)
			} else {
				names = append(names, entry)
			}
		}
		var keep []string
		for _, d := range dirs {
			name := d.Name()
			if name == ".git" || name == "__pycache__" || d.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := pypath.Join(directory, name)
			if len(exclusions) > 0 && excludedPath(pypath.Resolve(child)) {
				continue
			}
			keep = append(keep, child)
		}
		for _, entry := range names {
			count++
			if count > maxFiles {
				return valueError("This export exceeds 250,000 files. Select a more specific library root to analyze.")
			}
			name := entry.Name()
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !suffixes[strings.ToLower(pypath.Suffix(name))] && !provenanceNames[strings.ToLower(name)] {
				continue // Irrelevant either way; skip the resolve.
			}
			path := pypath.Join(directory, name)
			resolved := pypath.Resolve(path)
			if !pypath.IsRelativeTo(resolved, root) || excludedPath(resolved) {
				continue
			}
			result = append(result, walkedFile{path, resolved})
		}
		for _, child := range keep {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	sortKeys := make([]string, len(result))
	order := make([]int, len(result))
	for i, f := range result {
		sortKeys[i] = casefold(asPosix(f.path))
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return sortKeys[order[i]] < sortKeys[order[j]] })
	sorted := make([]walkedFile, len(result))
	for i, o := range order {
		sorted[i] = result[o]
	}
	return sorted, nil
}

// parallel runs fn(0..n-1) on at most runtime.NumCPU() goroutines.
func parallel(n int, fn func(i int)) {
	workers := min(runtime.NumCPU(), n)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var mu sync.Mutex
	next := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

var errChanged = &formats.OSError{Message: "File changed during analysis; analyze again when exports finish."}

func hashFile(path string, buffer []byte) (ManifestItem, error) {
	before, err := os.Stat(path)
	if err != nil {
		return ManifestItem{}, formats.PythonOSError(err)
	}
	file, err := os.Open(path)
	if err != nil {
		return ManifestItem{}, formats.PythonOSError(err)
	}
	sum := sha256.New()
	_, err = io.CopyBuffer(sum, onlyReader{file}, buffer)
	file.Close()
	if err != nil {
		return ManifestItem{}, formats.PythonOSError(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return ManifestItem{}, formats.PythonOSError(err)
	}
	if before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() {
		return ManifestItem{}, errChanged
	}
	size := after.Size()
	mtime := strconv.FormatInt(after.ModTime().UnixNano(), 10)
	digest := hex.EncodeToString(sum.Sum(nil))
	return ManifestItem{Size: &size, MtimeNs: &mtime, SHA256: &digest}, nil
}

// onlyReader hides io.WriterTo so CopyBuffer uses the given buffer.
type onlyReader struct{ io.Reader }

// snapshotWith hashes relevant bytes in the worker, including unparsed
// provenance files (snapshot()). Files are hashed on a bounded worker pool;
// the manifest keeps the sorted path order.
func snapshotWith(root string, excluded []string, progress func(string)) (*Snapshot, map[string]string, error) {
	walked, err := files(root, excluded)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &Snapshot{Paths: make([]string, len(walked)), Manifest: make([]ManifestItem, len(walked))}
	resolved := make(map[string]string, len(walked))
	for i, f := range walked {
		snapshot.Paths[i] = f.path
		resolved[f.path] = f.resolved
	}
	var mu sync.Mutex
	done := 0
	buffers := sync.Pool{New: func() any { b := make([]byte, 1024*1024); return &b }}
	parallel(len(walked), func(i int) {
		path := walked[i].path
		buffer := buffers.Get().(*[]byte)
		item, err := hashFile(path, *buffer)
		buffers.Put(buffer)
		if err != nil {
			message := err.Error()
			item = ManifestItem{Error: &message}
		}
		item.RelativePath = asPosix(relativeTo(path, root))
		snapshot.Manifest[i] = item
		mu.Lock()
		n := done
		done++
		if n%25 == 0 {
			progress("Checking local files: " + strconv.Itoa(n+1) + " / " + strconv.Itoa(len(walked)))
		}
		mu.Unlock()
	})
	return snapshot, resolved, nil
}

// TakeSnapshot is snapshot(root, excluded, progress); root must be resolved.
func TakeSnapshot(root string, excluded []string, progress func(string)) (*Snapshot, error) {
	if progress == nil {
		progress = func(string) {}
	}
	snapshot, _, err := snapshotWith(root, excluded, progress)
	return snapshot, err
}
