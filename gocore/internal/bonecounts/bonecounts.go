// Package bonecounts computes cheap skeleton totals for the model browser,
// without decoding model geometry. It is the Go port of
// owpreview/bone_counts.py.
//
//	read_model_bone_count   ReadModelBoneCount
//	BoneCountIndex          Index (New)
//	BoneCountIndex.metadata Index.Metadata; Index.MetadataAll for a whole scan
//
// MetadataAll returns exactly what calling Metadata for each path in order
// returns, but first reads the entity files, resolves their references and
// reads the model headers with a bounded worker pool. The results are the
// same because every read is cached by the same key Python uses and depends
// only on the file contents.
package bonecounts

import (
	"os"
	"runtime"
	"strings"
	"sync"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pypath"
)

// readHeader reads the declared skeleton size through a streaming reader
// (Python's _HeaderReader): mesh, skeleton and material data stay unread.
// It returns the number of header bytes consumed too, for tests.
func readHeader(path string) (bones int, consumed int, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, formats.PythonOSError(err)
	}
	defer file.Close()
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, formats.PythonOSError(err)
	}
	r := formats.NewStreamReader(path, file, info.Size(), formats.StreamOptions{ShortReadMessage: "truncated header"})
	if info.Size() > formats.MaxFileBytes {
		return 0, 0, r.Fail("file exceeds the 512 MB preview limit")
	}
	version, err := r.Unpack("HH")
	if err != nil {
		return 0, r.Offset, err
	}
	if err := formats.CheckVersion(r, uint16(version.Uint(0)), uint16(version.Uint(1)), "model"); err != nil {
		return 0, r.Offset, err
	}
	if _, err := r.String(); err != nil { // Material reference.
		return 0, r.Offset, err
	}
	if _, err := r.String(); err != nil { // Model name.
		return 0, r.Offset, err
	}
	fields, err := r.Unpack("IHII")
	if err != nil {
		return 0, r.Offset, err
	}
	if err := r.Count(int64(fields.Uint(2)), 16384, "mesh count"); err != nil {
		return 0, r.Offset, err
	}
	if err := r.Count(int64(fields.Uint(3)), 65536, "socket count"); err != nil {
		return 0, r.Offset, err
	}
	return int(fields.Uint(1)), r.Offset, nil
}

// ReadModelBoneCount returns the declared skeleton size of an .owmdl;
// mesh/skeleton/material data stays unread.
func ReadModelBoneCount(path string) (int, error) {
	bones, _, err := readHeader(pypath.Clean(path))
	return bones, err
}

// Metadata is the per-row result merged into the library scan.
type Metadata struct {
	BoneCount      *int   `json:"boneCount"`
	BoneCountError string `json:"boneCountError"`
}

type headerResult struct {
	bones int
	err   error
}

type entityResult struct {
	entity *formats.Entity
	err    error
}

type referenceKey struct{ reference, owner, kind, relative string }

type referenceResult struct {
	path string
	err  error
}

type summary struct{ bones, nodes, height int }

// Index holds one scan's cached references and totals, counting every
// attachment instance.
//
// An incomplete hierarchy has no total: missing references must not look like
// a smaller valid rig. Explicit null-model entities remain legitimate
// wrappers. Passing scanned paths avoids another root walk and excludes map
// dependencies already pruned by the animation library scan.
//
// An Index is not safe for concurrent use.
type Index struct {
	resolver   *formats.Resolver
	headers    map[string]headerResult
	entities   map[string]entityResult
	references map[referenceKey]referenceResult
	totals     map[string]summary

	// readHeader is replaceable in tests.
	readHeader func(string) (int, error)
}

// New is BoneCountIndex(root_path, paths): root may be "" (no root), and
// paths, when non-nil, replaces the resolver's root walk with the scanned
// files.
func New(root string, paths []string) *Index {
	index := &Index{
		resolver:   formats.NewResolver(root),
		headers:    map[string]headerResult{},
		entities:   map[string]entityResult{},
		references: map[referenceKey]referenceResult{},
		totals:     map[string]summary{},
		readHeader: ReadModelBoneCount,
	}
	if paths != nil {
		index.resolver.Index = map[string][]string{}
		for _, value := range paths {
			name := strings.ToLower(pypath.Name(value))
			index.resolver.Index[name] = append(index.resolver.Index[name], pypath.Clean(value))
		}
	}
	return index
}

func (x *Index) header(path string) (int, error) {
	result, ok := x.headers[path]
	if !ok {
		bones, err := x.readHeader(path)
		result = headerResult{bones, err}
		x.headers[path] = result
	}
	return result.bones, result.err
}

func (x *Index) entity(path string) (*formats.Entity, error) {
	result, ok := x.entities[path]
	if !ok {
		entity, err := formats.ReadEntity(path)
		result = entityResult{entity, err}
		x.entities[path] = result
	}
	return result.entity, result.err
}

func (x *Index) find(reference, owner, kind, relative string) (string, error) {
	key := referenceKey{reference, owner, kind, relative}
	result, ok := x.references[key]
	if !ok {
		path, err := x.resolver.Find(reference, owner, kind, relative)
		result = referenceResult{path, err}
		x.references[key] = result
	}
	return result.path, result.err
}

func formatError(message string) error { return &formats.FormatError{Message: message} }

var errDepth = formatError("Entity hierarchy exceeds 48 levels.")

func (x *Index) summary(path string, active map[string]bool, depth int) (summary, error) {
	if depth > formats.MaxEntityDepth {
		return summary{}, errDepth
	}
	if active[path] {
		return summary{}, formatError("Entity reference cycle: " + pypath.Name(path))
	}
	if cached, ok := x.totals[path]; ok {
		if depth+cached.height > formats.MaxEntityDepth {
			return summary{}, errDepth
		}
		return cached, nil
	}
	active[path] = true
	defer delete(active, path)
	var entity *formats.Entity
	model := ""
	switch strings.ToLower(pypath.Suffix(path)) {
	case ".owentity":
		var err error
		if entity, err = x.entity(path); err != nil {
			return summary{}, err
		}
		if model, err = x.find(entity.Model, path, formats.KindModel, entity.Relative); err != nil {
			return summary{}, err
		}
		if entity.Model != "" && model == "" {
			return summary{}, formatError(pypath.Name(path) + ": missing model " + entity.Model + ".")
		}
	case ".owmdl":
		model = path
	default:
		return summary{}, formatError("Select an .owentity or .owmdl file.")
	}
	result := summary{nodes: 1}
	if model != "" {
		bones, err := x.header(model)
		if err != nil {
			return summary{}, err
		}
		result.bones = bones
	}
	if entity != nil {
		for _, child := range entity.Children {
			if child.Reference == "" {
				continue
			}
			childPath, err := x.find(child.Reference, path, formats.KindEntity, formats.DefaultRelative)
			if err != nil {
				return summary{}, err
			}
			if childPath == "" {
				return summary{}, formatError(pypath.Name(path) + ": missing child entity " + child.Reference + ".")
			}
			sub, err := x.summary(childPath, active, depth+1)
			if err != nil {
				return summary{}, err
			}
			result.bones += sub.bones
			result.nodes += sub.nodes
			result.height = max(result.height, sub.height+1)
			if result.nodes > formats.MaxModels {
				return summary{}, formatError("Entity exceeds 512 model nodes.")
			}
		}
	}
	x.totals[path] = result
	return result, nil
}

// Metadata returns {boneCount, boneCountError} for an .owentity or .owmdl.
func (x *Index) Metadata(path string) Metadata { return x.metadata(pypath.Resolve(path)) }

func (x *Index) metadata(resolved string) Metadata {
	total, err := x.summary(resolved, map[string]bool{}, 0)
	if err != nil {
		return Metadata{BoneCountError: err.Error()}
	}
	return Metadata{BoneCount: &total.bones}
}

// MetadataAll returns Metadata(path) for every path, in order. File reads
// and reference lookups run on a bounded worker pool first.
func (x *Index) MetadataAll(paths []string) []Metadata {
	resolved := x.prefetch(paths)
	out := make([]Metadata, len(paths))
	for i, path := range resolved {
		out[i] = x.metadata(path)
	}
	return out
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
	var next sync.Mutex
	index := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next.Lock()
				i := index
				index++
				next.Unlock()
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// prefetch fills the caches that Metadata would fill for paths, using the
// same keys: resolved file paths and (reference, owner, kind, relative).
func (x *Index) prefetch(paths []string) (resolved []string) {
	resolved = make([]string, len(paths))
	parallel(len(paths), func(i int) { resolved[i] = pypath.Resolve(paths[i]) })
	var entityPaths, modelPaths []string
	seen := map[string]bool{}
	for _, path := range resolved {
		if seen[path] {
			continue
		}
		seen[path] = true
		switch strings.ToLower(pypath.Suffix(path)) {
		case ".owentity":
			if _, ok := x.entities[path]; !ok {
				entityPaths = append(entityPaths, path)
			}
		case ".owmdl":
			if _, ok := x.headers[path]; !ok {
				modelPaths = append(modelPaths, path)
			}
		}
	}
	x.readFiles(entityPaths, modelPaths)

	// Find walks the root lazily when a name must be searched; that walk is
	// not safe to run concurrently, so references resolve in parallel only
	// when no walk can happen (scanned paths given, or no root).
	if x.resolver.Index == nil && x.resolver.Root != "" {
		return resolved
	}
	var keys []referenceKey
	queued := map[referenceKey]bool{}
	for _, path := range entityPaths {
		result := x.entities[path]
		if result.err != nil {
			continue
		}
		add := func(key referenceKey) {
			if _, ok := x.references[key]; !ok && !queued[key] && key.reference != "" {
				queued[key] = true
				keys = append(keys, key)
			}
		}
		add(referenceKey{result.entity.Model, path, formats.KindModel, result.entity.Relative})
		for _, child := range result.entity.Children {
			add(referenceKey{child.Reference, path, formats.KindEntity, formats.DefaultRelative})
		}
	}
	results := make([]referenceResult, len(keys))
	parallel(len(keys), func(i int) {
		k := keys[i]
		path, err := x.resolver.Find(k.reference, k.owner, k.kind, k.relative)
		results[i] = referenceResult{path, err}
	})
	entityPaths, modelPaths = nil, nil
	for i, key := range keys {
		x.references[key] = results[i]
		target := results[i].path
		if results[i].err != nil || target == "" || seen[target] {
			continue
		}
		seen[target] = true
		if key.kind == formats.KindModel {
			if _, ok := x.headers[target]; !ok {
				modelPaths = append(modelPaths, target)
			}
		} else if _, ok := x.entities[target]; !ok {
			entityPaths = append(entityPaths, target)
		}
	}
	x.readFiles(entityPaths, modelPaths)
	return resolved
}

func (x *Index) readFiles(entityPaths, modelPaths []string) {
	entities := make([]entityResult, len(entityPaths))
	headers := make([]headerResult, len(modelPaths))
	parallel(len(entityPaths)+len(modelPaths), func(i int) {
		if i < len(entityPaths) {
			entity, err := formats.ReadEntity(entityPaths[i])
			entities[i] = entityResult{entity, err}
			return
		}
		i -= len(entityPaths)
		bones, err := x.readHeader(modelPaths[i])
		headers[i] = headerResult{bones, err}
	})
	for i, path := range entityPaths {
		x.entities[path] = entities[i]
	}
	for i, path := range modelPaths {
		x.headers[path] = headers[i]
	}
}
