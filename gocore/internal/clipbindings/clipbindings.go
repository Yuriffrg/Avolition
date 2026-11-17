// Package clipbindings serves small read-only batches of clip binding names
// for the animation browser. It is the Go port of owpreview/clip_bindings.py
// (ClipBindingIndex → Index).
package clipbindings

import (
	"container/list"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// MaxBatch is the largest number of paths one Read accepts.
const MaxBatch = 128

// stamp is the file fingerprint that validates a cache entry.
type stamp struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
	change       int64 // Windows change time (zero elsewhere)
}

type entry struct {
	key   string
	stamp stamp
	names []string
	cost  int
}

// Index caches exact bone names, never motion or guesses based on track
// counts.
//
// Failed files return boneNames null so they cannot look like a checked
// zero-match clip. LRU entries are bounded by both count and estimated
// memory. No data is written beside the source files or persisted in the
// catalog. An Index is safe for concurrent use; one Read processes its paths
// in order, like Python.
type Index struct {
	maxEntries int
	maxBytes   int

	mu    sync.Mutex
	lru   *list.List // front = least recently used
	byKey map[string]*list.Element
	bytes int

	// readMetadata is replaceable in tests.
	readMetadata func(string) (*formats.ClipBindingMetadata, error)
}

// DefaultMaxEntries and DefaultMaxBytes are Python's constructor defaults.
const (
	DefaultMaxEntries = 32768
	DefaultMaxBytes   = 64 * 1024 * 1024
)

// New is ClipBindingIndex(max_entries, max_bytes); negative bounds become 0
// (no caching).
func New(maxEntries, maxBytes int) *Index {
	return &Index{
		maxEntries:   max(0, maxEntries),
		maxBytes:     max(0, maxBytes),
		lru:          list.New(),
		byKey:        map[string]*list.Element{},
		readMetadata: formats.ReadClipBindingMetadata,
	}
}

// NewDefault is ClipBindingIndex().
func NewDefault() *Index { return New(DefaultMaxEntries, DefaultMaxBytes) }

// Len is the number of cached entries.
func (x *Index) Len() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.lru.Len()
}

type valueError string

func (e valueError) Error() string { return string(e) }

const errFullPath = valueError("Use the full path to an .owanimclip file.")

// normcase is os.path.normcase.
func normcase(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	}
	return path
}

func sourcePath(path string) (string, error) {
	if path == "" || utf8.RuneCountInString(path) > 32767 {
		return "", errFullPath
	}
	if !pypath.IsAbs(path) || strings.ToLower(pypath.Suffix(path)) != ".owanimclip" {
		return "", errFullPath
	}
	return filepath.Clean(path), nil
}

func (x *Index) readOne(source string) ([]string, error) {
	// Cache hits need one stat, not another filesystem path-resolution walk.
	// Symlink spellings may occupy separate entries; fingerprints still
	// describe the target file and each request retains its original key.
	key := normcase(source)
	before, err := fingerprint(source)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	if element, ok := x.byKey[key]; ok {
		cached := element.Value.(*entry)
		if cached.stamp == before {
			x.lru.MoveToBack(element)
			names := append([]string{}, cached.names...)
			x.mu.Unlock()
			return names, nil
		}
		x.remove(element)
	}
	x.mu.Unlock()
	metadata, err := x.readMetadata(source)
	if err != nil {
		return nil, err
	}
	if after, err := fingerprint(source); err != nil {
		return nil, err
	} else if after != before {
		return nil, &formats.OSError{Message: "File changed while reading animation bindings; scan again."}
	}
	names := append([]string{}, metadata.BoneNames...)
	cost := len(key) + 128
	for _, name := range names {
		cost += len(name) + 64
	}
	x.mu.Lock()
	if x.maxEntries > 0 && cost <= x.maxBytes {
		// Another reader may have completed this path concurrently.
		if previous, ok := x.byKey[key]; ok {
			x.remove(previous)
		}
		x.byKey[key] = x.lru.PushBack(&entry{key: key, stamp: before, names: names, cost: cost})
		x.bytes += cost
		for x.lru.Len() > x.maxEntries || x.bytes > x.maxBytes {
			x.remove(x.lru.Front())
		}
	}
	x.mu.Unlock()
	return append([]string{}, names...), nil
}

func (x *Index) remove(element *list.Element) {
	removed := x.lru.Remove(element).(*entry)
	delete(x.byKey, removed.key)
	x.bytes -= removed.cost
}

// Clip is one result: the original path string (null for a non-string
// request) and its bone names, or null names and an error.
type Clip struct {
	FirstPose *[]formats.ClipBone `json:"firstPose,omitempty"`
	Path      *string             `json:"path"`
	BoneNames []string            `json:"boneNames"`
	Error     string              `json:"error,omitempty"`
}

// readClip returns just the cached names on an ordinary lookup. A pose request
// streams its chosen ancestors and returns one sample per populated channel.
func (x *Index) readClip(path string, wanted map[string]bool) (Clip, error) {
	source, err := sourcePath(path)
	if err != nil {
		return Clip{}, err
	}
	if len(wanted) == 0 {
		names, err := x.readOne(source)
		return Clip{Path: &path, BoneNames: names}, err
	}
	metadata, err := formats.ReadClipBindingPose(source, wanted)
	if err != nil {
		return Clip{}, err
	}
	return Clip{Path: &path, BoneNames: metadata.BoneNames, FirstPose: &metadata.FirstPose}, nil
}

// Result is {clips: [...]} in request order.
type Result struct {
	Clips []Clip `json:"clips"`
}

// Read returns {clips: [{path, boneNames, error?}]} in request order.
//
// paths is the decoded JSON argument: a []any (or []string) of at most
// MaxBatch items; other types are rejected like a non-list in Python. The
// caller can submit batches asynchronously. Original path strings are
// retained as UI keys while cache lookup normalizes filesystem identity.
func (x *Index) Read(paths any) (*Result, error) {
	return x.ReadWithPose(paths, nil)
}

func (x *Index) ReadWithPose(paths any, poseBones any) (*Result, error) {
	var wanted map[string]bool
	if poseBones != nil {
		names, ok := poseBones.([]any)
		if !ok || len(names) > 1024 {
			return nil, valueError("Invalid frame-zero bone selection.")
		}
		wanted = make(map[string]bool, len(names))
		for _, item := range names {
			name, ok := item.(string)
			if !ok || name == "" {
				return nil, valueError("Invalid frame-zero bone selection.")
			}
			wanted[name] = true
		}
	}
	var items []any
	switch v := paths.(type) {
	case []any:
		items = v
	case []string:
		items = make([]any, len(v))
		for i, s := range v {
			items[i] = s
		}
	default:
		return nil, batchError()
	}
	if len(items) > MaxBatch {
		return nil, batchError()
	}
	result := &Result{Clips: make([]Clip, 0, len(items))}
	for _, item := range items {
		path, isString := item.(string)
		if !isString {
			result.Clips = append(result.Clips, Clip{Error: errFullPath.Error()})
			continue
		}
		clip, err := x.readClip(path, wanted)
		if err == nil {
			result.Clips = append(result.Clips, clip)
			continue
		}
		clip = Clip{Path: &path, Error: err.Error()}
		if clip.Error == "" {
			clip.Error = "OSError"
		}
		result.Clips = append(result.Clips, clip)
	}
	return result, nil
}

func batchError() error {
	return valueError(fmt.Sprintf("Request animation bindings in batches of at most %d paths.", MaxBatch))
}
