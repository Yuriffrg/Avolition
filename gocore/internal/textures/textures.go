// Package textures ports owpreview/textures.py: decoding registered export
// textures and the Blender importer's packed channels for the preview.
//
// Images are decoded like Pillow 12.3 (see decode.go), downsampled with a
// bit-exact port of Pillow's LANCZOS resampler, channel-processed with
// ImageMath's float32 semantics and encoded as PNG data URIs. Only
// identifiers registered by a successful entity import can read images.
package textures

import (
	"bytes"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"avolition/gocore/internal/pypath"
)

const (
	MaxFileBytes    = 128 * 1024 * 1024
	MaxSourcePixels = 64 * 1024 * 1024
	MaxPreviewEdge  = 2048
	CacheBytes      = 32 * 1024 * 1024
)

// ImageExtensions are the accepted file suffixes (lower case).
var ImageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".tga": true, ".dds": true,
	".tif": true, ".tiff": true, ".bmp": true, ".webp": true,
}

// TextureOwners are the workspaces that register textures.
var TextureOwners = []string{"animations", "maps", "model-browser"}

func validOwner(owner string) bool {
	for _, known := range TextureOwners {
		if owner == known {
			return true
		}
	}
	return false
}

var (
	errUnknownOwner = errors.New("Unknown texture workspace.")
	errUnregistered = errors.New("Texture is not registered for the loaded entity. Reload the entity.")
	errPreviewEdge  = fmt.Errorf("Preview texture size must be an integer from 32 to %d.", MaxPreviewEdge)
	maxPreviewEdge  = MaxPreviewEdge // tests lower it, as Python's tests patch MAX_PREVIEW_EDGE
)

// Result is load_texture's reply.
type Result struct {
	DataURI string `json:"dataUri"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Warning string `json:"warning,omitempty"`
}

type imageKey struct {
	path    string
	maxEdge int
}

type imageEntry struct {
	key     imageKey
	image   *Image
	warning string
}

type resultKey struct {
	id      string
	maxEdge int
}

type resultEntry struct {
	key    resultKey
	result *Result
}

type ownerSpecs struct {
	owner string
	specs map[string]Spec
}

// Store is TextureStore.
type Store struct {
	mu          sync.Mutex
	specs       map[string]Spec
	owners      []ownerSpecs // insertion order, as a Python dict
	images      *list.List   // of *imageEntry, front = least recent
	imageIndex  map[imageKey]*list.Element
	imageBytes  int
	results     *list.List // of *resultEntry
	resultIndex map[resultKey]*list.Element
	resultBytes int
	generation  uint64 // bumped whenever cached data may be stale
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		specs: map[string]Spec{}, images: list.New(), imageIndex: map[imageKey]*list.Element{},
		results: list.New(), resultIndex: map[resultKey]*list.Element{},
	}
}

func (s *Store) mergeOwners() {
	s.specs = map[string]Spec{}
	for _, owned := range s.owners {
		for key, value := range owned.specs {
			s.specs[key] = value
		}
	}
}

// Register replaces the textures of owner ("" replaces every workspace).
func (s *Store) Register(textures map[string]Spec, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.specs
	specs := make(map[string]Spec, len(textures))
	for key, value := range textures {
		specs[key] = value
	}
	if owner == "" {
		s.owners = nil
		s.specs = specs
	} else {
		if !validOwner(owner) {
			return errUnknownOwner
		}
		replaced := false
		for i := range s.owners {
			if s.owners[i].owner == owner {
				s.owners[i].specs, replaced = specs, true
			}
		}
		if !replaced {
			s.owners = append(s.owners, ownerSpecs{owner, specs})
		}
		s.mergeOwners()
	}
	s.generation++
	if owner == "model-browser" {
		// Browsing candidates must not flush the loaded scenes' decoded
		// textures every time the user changes the preview selection.
		s.pruneUnownedCache(previous)
		return nil
	}
	s.images.Init()
	s.results.Init()
	s.imageIndex = map[imageKey]*list.Element{}
	s.resultIndex = map[resultKey]*list.Element{}
	s.imageBytes, s.resultBytes = 0, 0
	return nil
}

func (s *Store) pruneUnownedCache(previous map[string]Spec) {
	for element := s.results.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(*resultEntry)
		current, ok := s.specs[entry.key.id]
		if old, had := previous[entry.key.id]; !ok || !had || old != current {
			s.results.Remove(element)
			delete(s.resultIndex, entry.key)
			s.resultBytes -= len(entry.result.DataURI)
		}
		element = next
	}
	if s.images.Len() > 0 {
		paths := map[string]bool{}
		for _, spec := range s.specs {
			paths[resolve(spec.Path)] = true
		}
		for element := s.images.Front(); element != nil; {
			next := element.Next()
			entry := element.Value.(*imageEntry)
			if !paths[entry.key.path] {
				s.images.Remove(element)
				delete(s.imageIndex, entry.key)
				s.imageBytes -= entry.image.Width * entry.image.Height * 4
			}
			element = next
		}
	}
}

// Release revokes one workspace's texture access without evicting shared data.
func (s *Store) Release(owner string) error {
	if !validOwner(owner) {
		return errUnknownOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.owners {
		if s.owners[i].owner == owner {
			index = i
		}
	}
	if index < 0 {
		return nil
	}
	previous := s.specs
	s.owners = append(s.owners[:index:index], s.owners[index+1:]...)
	s.mergeOwners()
	s.generation++
	s.pruneUnownedCache(previous)
	return nil
}

// resolve is str(Path(path).resolve()) (no "~" expansion).
func resolve(path string) string {
	if strings.HasPrefix(path, "~") {
		path = "." + string(os.PathSeparator) + path
	}
	return pypath.Resolve(path)
}

// ParseMaxEdge validates load_texture's max_edge JSON argument: null or
// absent means 2048; anything but an integer from 32 to 2048 is an error
// (Python rejects booleans and floats such as 64.0).
func ParseMaxEdge(raw json.RawMessage) (int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return maxPreviewEdge, nil
	}
	if bytes.ContainsAny(raw, ".eE\"tfn[{") {
		return 0, errPreviewEdge
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errPreviewEdge
	}
	if value < 32 || value > MaxPreviewEdge {
		return 0, errPreviewEdge
	}
	return int(value), nil
}

// LoadJSON is load_texture with raw JSON arguments (validated in Python's order).
func (s *Store) LoadJSON(textureID, maxEdge json.RawMessage) (*Result, error) {
	edge, err := ParseMaxEdge(maxEdge)
	if err != nil {
		return nil, err
	}
	var id string
	if json.Unmarshal(textureID, &id) != nil {
		return nil, errUnregistered
	}
	return s.Load(id, edge)
}

// Load returns the preview of a registered texture. maxEdge 0 means 2048.
// Decoding runs outside the store lock, so different textures load in
// parallel; results are cached only if no registration happened meanwhile.
func (s *Store) Load(textureID string, maxEdge int) (*Result, error) {
	if maxEdge == 0 {
		maxEdge = maxPreviewEdge
	} else if maxEdge < 32 || maxEdge > MaxPreviewEdge {
		return nil, errPreviewEdge
	}
	s.mu.Lock()
	spec, ok := s.specs[textureID]
	if !ok {
		s.mu.Unlock()
		return nil, errUnregistered
	}
	key := resultKey{textureID, maxEdge}
	if element, ok := s.resultIndex[key]; ok {
		s.results.MoveToBack(element)
		s.mu.Unlock()
		return element.Value.(*resultEntry).result, nil
	}
	generation := s.generation
	s.mu.Unlock()

	source, warning, err := s.image(spec.Path, maxEdge, generation)
	if err != nil {
		return nil, err
	}
	processed, err := ProcessChannels(source, spec)
	if err != nil {
		return nil, err
	}
	result := &Result{DataURI: DataURI(processed), Width: processed.Width, Height: processed.Height, Warning: warning}

	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.resultIndex[key]; ok { // a concurrent load finished first
		return element.Value.(*resultEntry).result, nil
	}
	if s.generation != generation {
		return result, nil
	}
	for s.results.Len() > 0 && s.resultBytes+len(result.DataURI) > CacheBytes {
		oldest := s.results.Front()
		entry := oldest.Value.(*resultEntry)
		s.results.Remove(oldest)
		delete(s.resultIndex, entry.key)
		s.resultBytes -= len(entry.result.DataURI)
	}
	if len(result.DataURI) <= CacheBytes {
		s.resultIndex[key] = s.results.PushBack(&resultEntry{key, result})
		s.resultBytes += len(result.DataURI)
	}
	return result, nil
}

// image returns the decoded (and possibly reduced) RGBA source.
func (s *Store) image(path string, maxEdge int, generation uint64) (*Image, string, error) {
	resolved := resolve(path)
	name := pypath.Name(resolved)
	if !ImageExtensions[strings.ToLower(pypath.Suffix(resolved))] {
		return nil, "", fmt.Errorf("%s: unsupported texture image format", name)
	}
	key := imageKey{resolved, maxEdge}
	s.mu.Lock()
	if element, ok := s.imageIndex[key]; ok {
		s.images.MoveToBack(element)
		entry := element.Value.(*imageEntry)
		s.mu.Unlock()
		return entry.image, entry.warning, nil
	}
	s.mu.Unlock()

	rgba, warning, err := LoadImage(resolved, maxEdge)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.imageIndex[key]; ok {
		entry := element.Value.(*imageEntry)
		return entry.image, entry.warning, nil
	}
	if s.generation != generation {
		return rgba, warning, nil
	}
	needed := rgba.Width * rgba.Height * 4
	for s.images.Len() > 0 && s.imageBytes+needed > CacheBytes {
		oldest := s.images.Front()
		entry := oldest.Value.(*imageEntry)
		s.images.Remove(oldest)
		delete(s.imageIndex, entry.key)
		s.imageBytes -= entry.image.Width * entry.image.Height * 4
	}
	s.imageIndex[key] = s.images.PushBack(&imageEntry{key, rgba, warning})
	s.imageBytes += needed
	return rgba, warning, nil
}

// LoadImage decodes a resolved texture path to RGBA, applying the preview
// limits, and reduces it so its longest edge is at most maxEdge.
func LoadImage(path string, maxEdge int) (*Image, string, error) {
	name := pypath.Name(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", pyOSError(err)
	}
	if info.Size() > MaxFileBytes {
		return nil, "", fmt.Errorf("%s: texture exceeds the 128 MB preview limit", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", pyOSError(err)
	}
	rgba, err := DecodeImage(data, func(width, height int) error {
		if width*height > MaxSourcePixels {
			return fmt.Errorf("%s: texture exceeds the 64-megapixel preview limit", name)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errUnidentified) {
			return nil, "", fmt.Errorf("cannot identify image file %s", pyRepr(path))
		}
		return nil, "", err
	}
	if max(rgba.Width, rgba.Height) <= maxEdge {
		return rgba, "", nil
	}
	ratio := float64(maxEdge) / float64(max(rgba.Width, rgba.Height))
	width := max(1, int(math.RoundToEven(float64(rgba.Width)*ratio)))
	height := max(1, int(math.RoundToEven(float64(rgba.Height)*ratio)))
	// Alpha is often a packed scalar, not opacity. Each channel is resized
	// separately so alpha premultiplication cannot alter normals/masks.
	resized := Resize(rgba, width, height)
	return resized, fmt.Sprintf("%s: preview resized to %d × %d", name, width, height), nil
}

var errUnidentified = errors.New("cannot identify image file")

// DecodeImage decodes image bytes like Image.open(...).convert('RGBA');
// check runs with the image size before the pixels are decoded.
func DecodeImage(data []byte, check func(width, height int) error) (*Image, error) {
	format := detect(data)
	if format == nil {
		return nil, errUnidentified
	}
	width, height, err := format.config(data)
	if err != nil {
		return nil, fmt.Errorf("%s", err.Error())
	}
	if err := bombCheck(width, height); err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(width, height); err != nil {
			return nil, err
		}
	}
	return format.decode(data)
}
