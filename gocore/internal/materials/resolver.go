package materials

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

func init() {
	formats.RegisterMaterialResolver(func(assetPath, rootPath string, warnings *[]string) formats.MaterialResolver {
		return assetResolver{New(assetPath, rootPath, warnings)}
	})
}

// assetResolver adapts Resolver to formats.MaterialResolver (load_asset calls
// for_model without partial).
type assetResolver struct{ *Resolver }

func (a assetResolver) ForModel(model *formats.Model, entityLook string) (*ordered.Map[*formats.Material], error) {
	return a.Resolver.ForModel(model, entityLook, false), nil
}

func (a assetResolver) Root() string { return a.Resolver.Root }

// MaxIndexedFiles bounds the export-root walk of the basename fallback.
const MaxIndexedFiles = 500_000

// Resolver is MaterialResolver: it resolves only referenced assets beneath
// the selected export root and registers texture descriptors. It is not safe
// for concurrent use.
type Resolver struct {
	// Root is the resolved export root.
	Root string

	warnings    *[]string
	seen        map[string]struct{}
	seenLen     int
	textures    *ordered.Map[*formats.Texture]
	records     map[string]*Record
	descriptors map[string]*formats.Material
	index       map[string][]string
	resolved    pypath.ResolveCache
}

// New is MaterialResolver(asset_path, root_path, warnings): rootPath "" is
// None (the root is then derived from the asset's location), and warnings
// nil gives the resolver its own list.
func New(assetPath, rootPath string, warnings *[]string) *Resolver {
	if warnings == nil {
		warnings = &[]string{}
	}
	return &Resolver{
		Root:        formats.TextureRoot(assetPath, rootPath),
		warnings:    warnings,
		seen:        map[string]struct{}{},
		textures:    ordered.New[*formats.Texture](),
		records:     map[string]*Record{},
		descriptors: map[string]*formats.Material{},
	}
}

// Warnings is the resolver's warning list (shared with the caller's, when given).
func (r *Resolver) Warnings() []string { return *r.warnings }

// Textures is the texture registry: texture id → descriptor, in first-use order.
func (r *Resolver) Textures() *ordered.Map[*formats.Texture] { return r.textures }

// Warn appends message unless the warning list already contains it.
func (r *Resolver) Warn(message string) {
	// The list may be appended to by its owner too (load_asset shares it),
	// so catch up on entries added since the last call.
	list := *r.warnings
	for ; r.seenLen < len(list); r.seenLen++ {
		r.seen[list[r.seenLen]] = struct{}{}
	}
	if _, exists := r.seen[message]; exists {
		return
	}
	*r.warnings = append(list, message)
	r.seen[message] = struct{}{}
	r.seenLen = len(*r.warnings)
}

// resolve is Path.resolve() memoized for the resolver's lifetime: one asset
// or map load resolves the same few folders and files thousands of times,
// and each resolution costs an lstat per path component.
func (r *Resolver) resolve(path string) string { return r.resolved.Resolve(path) }

func (r *Resolver) withinRoot(path string) bool {
	if strings.IndexByte(path, 0) >= 0 {
		return false
	}
	return pypath.IsRelativeTo(r.resolve(path), r.Root)
}

// cleanReference is _clean: both separator styles become the host's.
func cleanReference(reference string) string { return formats.CleanReference(reference) }

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Resolve finds reference (named by the file owner) beneath the root, or
// returns "" with a warning. fileRelative also tries the reference relative
// to the owner's full filename (the ModelLook writer prefixes ../.. and
// Blender resolves it from the full look filename). extension ("" for None)
// is the required lower-case suffix.
func (r *Resolver) Resolve(owner, reference string, fileRelative bool, extension string) string {
	if reference == "" {
		return ""
	}
	owner = r.resolve(owner)
	ownerName := pypath.Name(owner)
	reference = cleanReference(reference)
	var candidates []string
	if pypath.IsAbs(reference) {
		candidates = append(candidates, pypath.Clean(reference))
	} else {
		if fileRelative {
			candidates = append(candidates, pypath.Join(owner, reference))
		}
		candidates = append(candidates, pypath.Join(pypath.Parent(owner), reference), pypath.Join(r.Root, reference))
	}
	var normalized []string
	for _, candidate := range candidates {
		if strings.IndexByte(candidate, 0) >= 0 {
			// Path.resolve() raises ValueError for an embedded NUL.
			r.Warn(ownerName + ": invalid referenced path was skipped.")
			continue
		}
		candidate = r.resolve(candidate)
		if !slices.ContainsFunc(normalized, func(p string) bool { return samePath(p, candidate) }) && r.withinRoot(candidate) {
			normalized = append(normalized, candidate)
		}
	}
	if len(normalized) == 0 {
		r.Warn(ownerName + ": reference outside export root was skipped: " + reference)
		return ""
	}
	for _, candidate := range normalized {
		if pypath.IsFile(candidate) && (extension == "" || strings.ToLower(pypath.Suffix(candidate)) == extension) {
			return candidate
		}
	}
	refName := pypath.Name(reference)
	// DataTool falls back to DDS if requested PNG/TIFF conversion fails.
	// This fallback changes only the extension of the exact reference.
	if extension == "" {
		switch strings.ToLower(pypath.Suffix(reference)) {
		case ".png", ".tif", ".tiff":
			for _, candidate := range normalized {
				fallback := pypath.WithSuffix(candidate, ".dds")
				if pypath.IsFile(fallback) && r.withinRoot(fallback) {
					r.Warn(ownerName + ": using DataTool DDS fallback for " + refName + ".")
					return fallback
				}
			}
		}
	}
	// When folders have been copied, accept a unique exact basename, never
	// a filename pattern or an arbitrary duplicate from a different skin.
	if r.index == nil {
		r.buildIndex()
	}
	var matches []string
	for _, path := range r.index[strings.ToLower(refName)] {
		if r.withinRoot(path) {
			matches = append(matches, r.resolve(path))
		}
	}
	if len(matches) == 1 && (extension == "" || strings.ToLower(pypath.Suffix(matches[0])) == extension) {
		return matches[0]
	}
	if len(matches) > 1 {
		r.Warn(ownerName + ": ambiguous referenced file " + refName + "; texture/material skipped.")
	} else {
		r.Warn(ownerName + ": missing referenced file " + reference + ".")
	}
	return ""
}

func (r *Resolver) buildIndex() {
	r.index = map[string][]string{}
	count := 0
	pypath.Walk(r.Root, nil, func(dir string, _ *[]string, files []string) bool {
		for _, name := range files {
			count++
			if count > MaxIndexedFiles {
				r.Warn("Material search stopped after 500,000 files; choose a smaller export root.")
				return false
			}
			suffix := strings.ToLower(pypath.Suffix(name))
			if SupportedImages[suffix] || suffix == ".owmat" {
				key := strings.ToLower(name)
				r.index[key] = append(r.index[key], pypath.Join(dir, name))
			}
		}
		return true
	})
}

func (r *Resolver) read(path string) (*Record, error) {
	path = r.resolve(path)
	if !r.withinRoot(path) {
		return nil, &formats.FormatError{Message: "Material file is outside export root: " + pypath.Name(path)}
	}
	if record, ok := r.records[path]; ok {
		return record, nil
	}
	record, err := readOWMat(path)
	if err != nil {
		return nil, err
	}
	r.records[path] = record
	return record, nil
}

// inputBytes returns a static input that holds at least size bytes, warning
// about a short one.
func (r *Resolver) inputBytes(record *Record, key uint32, size int) ([]byte, bool) {
	raw, ok := record.Inputs[key]
	if !ok {
		return nil, false
	}
	if len(raw) < size {
		r.Warn(pypath.Name(record.Path) + ": short static input " + strconv.FormatUint(uint64(key), 10) + " was ignored.")
		return nil, false
	}
	return raw, true
}

func (r *Resolver) nonFinite(record *Record, key uint32) {
	r.Warn(pypath.Name(record.Path) + ": non-finite static input " + strconv.FormatUint(uint64(key), 10) + " was ignored.")
}

func le32(raw []byte) uint32 {
	return uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24
}

// inputFloat is _input(record, key, 'f', fallback).
func (r *Resolver) inputFloat(record *Record, key uint32, fallback float64) float64 {
	raw, ok := r.inputBytes(record, key, 4)
	if !ok {
		return fallback
	}
	value := float64(math.Float32frombits(le32(raw)))
	if math.IsNaN(value) || math.IsInf(value, 0) {
		r.nonFinite(record, key)
		return fallback
	}
	return value
}

// inputFloat2 is _input(record, key, 'ff', fallback).
func (r *Resolver) inputFloat2(record *Record, key uint32, fallback [2]float64) [2]float64 {
	raw, ok := r.inputBytes(record, key, 8)
	if !ok {
		return fallback
	}
	values := [2]float64{float64(math.Float32frombits(le32(raw))), float64(math.Float32frombits(le32(raw[4:])))}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			r.nonFinite(record, key)
			return fallback
		}
	}
	return values
}

// inputUint is _input(record, key, 'I', fallback).
func (r *Resolver) inputUint(record *Record, key uint32, fallback int64) int64 {
	raw, ok := r.inputBytes(record, key, 4)
	if !ok {
		return fallback
	}
	return int64(le32(raw))
}

// textureID is _texture: register a texture operation under the first 24
// hex digits of the SHA-256 of its canonical (sorted, compact, ASCII) JSON.
func (r *Resolver) textureID(path, channel, transform, inputColorSpace string) string {
	identity := make([]byte, 0, 128+len(path))
	identity = append(identity, `{"channel":`...)
	identity = appendPythonJSONString(identity, channel)
	identity = append(identity, `,"inputColorSpace":`...)
	identity = appendPythonJSONString(identity, inputColorSpace)
	identity = append(identity, `,"path":`...)
	identity = appendPythonJSONString(identity, path)
	identity = append(identity, `,"transform":`...)
	identity = appendPythonJSONString(identity, transform)
	identity = append(identity, '}')
	sum := sha256.Sum256(identity)
	id := hex.EncodeToString(sum[:])[:24]
	if _, exists := r.textures.Get(id); !exists {
		r.textures.Set(id, &formats.Texture{Path: path, Channel: channel, Transform: transform, InputColorSpace: inputColorSpace})
	}
	return id
}

// appendPythonJSONString is json.dumps(s) with ensure_ascii=True: every
// character outside ' '..'~' is escaped, non-BMP characters as surrogate
// pairs. Invalid UTF-8 bytes (possible in POSIX file names) are escaped as
// the lone surrogates Python's surrogateescape decoding produces.
func appendPythonJSONString(b []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	escape := func(b []byte, c rune) []byte {
		return append(b, '\\', 'u', hexDigits[c>>12&15], hexDigits[c>>8&15], hexDigits[c>>4&15], hexDigits[c&15])
	}
	b = append(b, '"')
	for i := 0; i < len(s); {
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			b = escape(b, 0xDC00+rune(s[i]))
			i++
			continue
		}
		i += size
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c >= ' ' && c <= '~':
			b = append(b, byte(c))
		case c > 0xFFFF:
			c -= 0x10000
			b = escape(b, 0xD800+(c>>10))
			b = escape(b, 0xDC00+(c&0x3FF))
		default:
			b = escape(b, c)
		}
	}
	return append(b, '"')
}

func pythonUpper(s string) string { return strings.ToUpper(s) }
