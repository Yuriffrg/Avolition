// Package duplicates ports owpreview/duplicates.py: non-destructive,
// exact-content duplicate indexing for large clip libraries.
//
// Animation IDs and joint counts are not identities: the same filename can
// contain different motion, while the same motion may be exported under
// different names. Only identical bytes are grouped. Headers prune candidates
// before streaming the files, and a bounded stat cache makes repeated folder
// scans inexpensive. Candidate files are hashed in parallel by a bounded
// worker pool; results and warnings keep Python's order.
package duplicates

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
)

// DefaultMaxEntries is DuplicateIndex's default cache bound.
const DefaultMaxEntries = 32768

const maxWarnings = 50

// Clip is the scan row data annotate reads.
type Clip struct {
	Path           string
	Size           int64
	Version        string
	FPS            float64
	DurationFrames int64
	BoneCount      int64
	// Error is the header error; rows with an error are never grouped.
	Error string
}

// Annotation is what annotate stores into each row. DuplicateError is
// omitted (the key is popped in Python) when the file was checked.
type Annotation struct {
	DuplicateKey   string `json:"duplicateKey"`
	DuplicateCount int    `json:"duplicateCount"`
	DuplicateError string `json:"duplicateError,omitempty"`
}

type cacheEntry struct {
	key         string
	fingerprint fingerprint
	digest      string
}

// Index caches content digests by path and file identity.
type Index struct {
	maxEntries int

	mu    sync.Mutex
	cache map[string]*list.Element
	order *list.List // front = least recently used

	// open is replaceable in tests (Python's tests patch Path.open).
	open func(path string) (io.ReadCloser, error)
}

// New returns an index whose cache holds at most maxEntries digests (a
// negative bound is 0, which disables caching).
func New(maxEntries int) *Index {
	if maxEntries < 0 {
		maxEntries = 0
	}
	return &Index{maxEntries: maxEntries, cache: map[string]*list.Element{}, order: list.New()}
}

func (ix *Index) openFile(path string) (io.ReadCloser, error) {
	if ix.open != nil {
		return ix.open(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, pyOSError(err)
	}
	return file, nil
}

// Len reports the number of cached digests.
func (ix *Index) Len() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.cache)
}

func (ix *Index) cached(key string, current fingerprint) (string, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	element, ok := ix.cache[key]
	if !ok {
		return "", false
	}
	entry := element.Value.(*cacheEntry)
	if entry.fingerprint != current {
		return "", false
	}
	ix.order.MoveToBack(element)
	return entry.digest, true
}

func (ix *Index) store(key string, current fingerprint, digest string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.maxEntries == 0 {
		return
	}
	if element, ok := ix.cache[key]; ok {
		entry := element.Value.(*cacheEntry)
		entry.fingerprint, entry.digest = current, digest
		ix.order.MoveToBack(element)
	} else {
		ix.cache[key] = ix.order.PushBack(&cacheEntry{key, current, digest})
	}
	for len(ix.cache) > ix.maxEntries {
		oldest := ix.order.Front()
		ix.order.Remove(oldest)
		delete(ix.cache, oldest.Value.(*cacheEntry).key)
	}
}

var bufferPool = sync.Pool{New: func() any { buffer := make([]byte, 1024*1024); return &buffer }}

var (
	errChangedDuringScan = errors.New("File changed during the scan; scan again to check duplicates.")
	errChangedWhileHash  = errors.New("File changed while checking duplicates; scan again.")
)

func (ix *Index) digest(path string, expectedSize int64) (string, error) {
	key := pathKey(path)
	before, size, err := stat(path)
	if err != nil {
		return "", err
	}
	if size != expectedSize {
		return "", errChangedDuringScan
	}
	if digest, ok := ix.cached(key, before); ok {
		return digest, nil
	}
	source, err := ix.openFile(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	buffer := bufferPool.Get().(*[]byte)
	_, err = io.CopyBuffer(struct{ io.Writer }{hash}, struct{ io.Reader }{source}, *buffer)
	bufferPool.Put(buffer)
	closeErr := source.Close()
	if err != nil {
		return "", pyOSError(err)
	}
	if closeErr != nil {
		return "", pyOSError(closeErr)
	}
	after, _, err := stat(path)
	if err != nil {
		return "", err
	}
	if after != before {
		return "", errChangedWhileHash
	}
	result := hex.EncodeToString(hash.Sum(nil))
	ix.store(key, before, result)
	return result, nil
}

type groupKey struct {
	size           int64
	version        string
	fps            float64
	durationFrames int64
	boneCount      int64
}

type digestResult struct {
	digest string
	err    error
}

// Annotate returns one annotation per clip (in order) and at most 50
// diagnostic strings. Unreadable or changing files keep a unique path key so
// hiding duplicates never conceals an unchecked file.
func (ix *Index) Annotate(clips []Clip) ([]Annotation, []string) {
	annotations := make([]Annotation, len(clips))
	var order []groupKey
	groups := map[groupKey][]int{}
	for i, clip := range clips {
		annotations[i] = Annotation{DuplicateKey: "path:" + pathKey(clip.Path), DuplicateCount: 1}
		if clip.Error != "" {
			continue
		}
		key := groupKey{clip.Size, clip.Version, clip.FPS, clip.DurationFrames, clip.BoneCount}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	var candidates []int
	for _, key := range order {
		if len(groups[key]) >= 2 {
			candidates = append(candidates, groups[key]...)
		}
	}
	results := ix.digestAll(clips, candidates)
	warnings := []string{}
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		var digests []string
		identical := map[string][]int{}
		for _, i := range group {
			result := results[i]
			if result.err != nil {
				annotations[i].DuplicateError = result.err.Error()
				if len(warnings) < maxWarnings {
					warnings = append(warnings, "Could not check duplicates for "+clips[i].Path+": "+result.err.Error())
				}
				continue
			}
			annotations[i].DuplicateKey = "sha256:" + result.digest
			if _, seen := identical[result.digest]; !seen {
				digests = append(digests, result.digest)
			}
			identical[result.digest] = append(identical[result.digest], i)
		}
		for _, digest := range digests {
			for _, i := range identical[digest] {
				annotations[i].DuplicateCount = len(identical[digest])
			}
		}
	}
	return annotations, warnings
}

// digestAll hashes the candidate clips with a bounded worker pool.
func (ix *Index) digestAll(clips []Clip, candidates []int) map[int]digestResult {
	results := make(map[int]digestResult, len(candidates))
	if len(candidates) == 0 {
		return results
	}
	workers := min(runtime.NumCPU(), len(candidates))
	jobs := make(chan int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				digest, err := ix.digest(clips[i].Path, clips[i].Size)
				mu.Lock()
				results[i] = digestResult{digest, err}
				mu.Unlock()
			}
		}()
	}
	for _, i := range candidates {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}
