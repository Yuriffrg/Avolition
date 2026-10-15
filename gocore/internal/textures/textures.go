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

	channels, convert, specErr := channelConverter(spec)
	if specErr != nil {
		// Python reports image errors first (and caches the image).
		if _, _, err := s.image(spec.Path, maxEdge, generation); err != nil {
			return nil, err
		}
		return nil, specErr
	}
	result, err := s.render(spec.Path, maxEdge, generation, channels, convert)
	if err != nil {
		return nil, err
	}

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
	resolved, key, err := imageKeyOf(path, maxEdge)
	if err != nil {
		return nil, "", err
	}
	if entry := s.cachedImage(key); entry != nil {
		return entry.image, entry.warning, nil
	}
	rgba, warning, err := LoadImage(resolved, maxEdge)
	if err != nil {
		return nil, "", err
	}
	rgba, warning = s.storeImage(key, rgba, warning, generation)
	return rgba, warning, nil
}

func imageKeyOf(path string, maxEdge int) (string, imageKey, error) {
	resolved := resolve(path)
	if !ImageExtensions[strings.ToLower(pypath.Suffix(resolved))] {
		return "", imageKey{}, fmt.Errorf("%s: unsupported texture image format", pypath.Name(resolved))
	}
	return resolved, imageKey{resolved, maxEdge}, nil
}

func (s *Store) cachedImage(key imageKey) *imageEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.imageIndex[key]; ok {
		s.images.MoveToBack(element)
		return element.Value.(*imageEntry)
	}
	return nil
}

// storeImage caches a decoded source unless a registration happened since
// generation; a source cached meanwhile by a concurrent load wins.
func (s *Store) storeImage(key imageKey, rgba *Image, warning string, generation uint64) (*Image, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.imageIndex[key]; ok {
		entry := element.Value.(*imageEntry)
		return entry.image, entry.warning
	}
	if s.generation != generation {
		return rgba, warning
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
	return rgba, warning
}

// render produces the preview of the source at path. An uncached source is
// decoded in another goroutine while this one filters and compresses the
// rows decoded so far, so decoding and PNG encoding overlap; the channel
// conversion is applied row by row as rows are encoded.
func (s *Store) render(path string, maxEdge int, generation uint64, channels int, convert func(in, out []byte)) (*Result, error) {
	resolved, key, err := imageKeyOf(path, maxEdge)
	if err != nil {
		return nil, err
	}
	if entry := s.cachedImage(key); entry != nil {
		uri, _ := dataURI(previewRows(entry.image, channels, convert, nil))
		return &Result{DataURI: uri, Width: entry.image.Width, Height: entry.image.Height, Warning: entry.warning}, nil
	}
	stream := newImageStream()
	go func() {
		rgba, warning, err := loadImage(resolved, maxEdge, stream.progress)
		stream.finish(rgba, warning, err)
	}()
	var uri string
	if partial := stream.waitImage(); partial != nil {
		uri, _ = dataURI(previewRows(partial, channels, convert, stream.waitRows))
	}
	rgba, warning, err := stream.result()
	if err != nil {
		return nil, err
	}
	// The image is complete: encode it if it was not published while
	// decoding (a reduced source) or is not the one encoded.
	if uri == "" || stream.partial != rgba {
		uri, _ = dataURI(previewRows(rgba, channels, convert, nil))
	}
	s.storeImage(key, rgba, warning, generation)
	return &Result{DataURI: uri, Width: rgba.Width, Height: rgba.Height, Warning: warning}, nil
}

// previewRows is the preview of an RGBA source: convert applied to each row
// (nil keeps RGBA). wait, when set, waits for source rows.
func previewRows(source *Image, channels int, convert func(in, out []byte), wait func(int) bool) rowSource {
	stride := source.Width * 4
	rows := rowSource{width: source.Width, height: source.Height, channels: channels, wait: wait, sample: previewFilterSample}
	if convert == nil {
		rows.row = func(y int, _ []byte) []byte { return source.Pix[y*stride : (y+1)*stride] }
	} else {
		rows.row = func(y int, scratch []byte) []byte {
			convert(source.Pix[y*stride:(y+1)*stride], scratch)
			return scratch
		}
	}
	return rows
}

// imageStream hands an image that is still being decoded from the decoding
// goroutine to the encoder: rows [0, rows) of partial are final.
type imageStream struct {
	mu      sync.Mutex
	changed sync.Cond
	partial *Image
	rows    int
	done    bool
	image   *Image
	warning string
	err     error
}

func newImageStream() *imageStream {
	stream := &imageStream{}
	stream.changed.L = &stream.mu
	return stream
}

func (s *imageStream) progress(image *Image, rows int) {
	s.mu.Lock()
	s.partial, s.rows = image, rows
	s.mu.Unlock()
	s.changed.Broadcast()
}

func (s *imageStream) finish(image *Image, warning string, err error) {
	s.mu.Lock()
	s.image, s.warning, s.err, s.done = image, warning, err, true
	s.mu.Unlock()
	s.changed.Broadcast()
}

// waitImage returns the image being decoded, or nil when decoding finished
// without publishing one.
func (s *imageStream) waitImage() *Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.partial == nil && !s.done {
		s.changed.Wait()
	}
	return s.partial
}

// waitRows waits until rows [0, n) of the partial image are final; false
// when decoding ended without them.
func (s *imageStream) waitRows(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.rows < n {
		if s.done {
			// A successful decode may finish before its last report.
			return s.err == nil && s.image == s.partial
		}
		s.changed.Wait()
	}
	return true
}

func (s *imageStream) result() (*Image, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.done {
		s.changed.Wait()
	}
	return s.image, s.warning, s.err
}

// LoadImage decodes a resolved texture path to RGBA, applying the preview
// limits, and reduces it so its longest edge is at most maxEdge.
func LoadImage(path string, maxEdge int) (*Image, string, error) {
	return loadImage(path, maxEdge, nil)
}

// loadImage is LoadImage; rowsDone, when set, receives the decoded image
// while it is decoded, if it needs no reduction (see decodeImage).
func loadImage(path string, maxEdge int, rowsDone func(*Image, int)) (*Image, string, error) {
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
	var partial func(*Image, int)
	if rowsDone != nil {
		partial = func(image *Image, rows int) {
			if max(image.Width, image.Height) <= maxEdge { // else the reduction is the result
				rowsDone(image, rows)
			}
		}
	}
	rgba, err := decodeImage(data, func(width, height int) error {
		if width*height > MaxSourcePixels {
			return fmt.Errorf("%s: texture exceeds the 64-megapixel preview limit", name)
		}
		return nil
	}, partial)
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
	return decodeImage(data, check, nil)
}

// decodeImage is DecodeImage; rowsDone, when set, may be called while the
// image is decoded with the image and how many rows from the top are final.
func decodeImage(data []byte, check func(width, height int) error, rowsDone func(*Image, int)) (*Image, error) {
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
	if rowsDone != nil && format.decodeRows != nil {
		return format.decodeRows(data, rowsDone)
	}
	return format.decode(data)
}
