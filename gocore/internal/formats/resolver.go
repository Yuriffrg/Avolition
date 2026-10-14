package formats

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"avolition/gocore/internal/pypath"
)

// DefaultRelative is the entity-to-root prefix DataTool writes ("../../").
const DefaultRelative = "../../"

// Reference kinds for Resolver.Find.
const (
	KindModel  = "model"
	KindEntity = "entity"
)

// MaxIndexedFiles bounds the export-root walk (Python's 500,000 files).
const MaxIndexedFiles = 500_000

// Resolver finds referenced .owmdl/.owentity files (Python's _Resolver).
// It is not safe for concurrent use.
type Resolver struct {
	// Root is the resolved export root, or "" when none was given.
	Root string
	// Index maps lower-case file names under Root to their paths. It is built
	// lazily by Find; callers that already scanned the root may preset it
	// (bone_counts does), in which case no walk happens.
	Index map[string][]string
}

// NewResolver is _Resolver(root): root is expanded and resolved when given.
func NewResolver(root string) *Resolver {
	if root == "" {
		return &Resolver{}
	}
	return &Resolver{Root: pypath.Resolve(root)}
}

// CleanReference converts both separator styles to the host's.
func CleanReference(reference string) string {
	if filepath.Separator == '\\' {
		return strings.ReplaceAll(reference, "/", `\`)
	}
	return strings.ReplaceAll(reference, `\`, "/")
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// Find resolves reference (from the file owner) to an existing file of kind
// KindModel or KindEntity, trying DataTool's layouts relative to the owner,
// its ancestors and the root, then a unique file-name match under the root.
// It returns "" when nothing matches, and a FormatError for an ambiguous
// name or an oversized root. relative is the entity's root prefix ("" uses
// none; pass DefaultRelative for Python's default argument).
func (res *Resolver) Find(reference, owner, kind, relative string) (string, error) {
	if reference == "" {
		return "", nil
	}
	folder, extension, originalExtension := "Models", ".owmdl", ".00C"
	if kind == KindEntity {
		folder, extension, originalExtension = "Entities", ".owentity", ""
	}
	reference = CleanReference(reference)
	relative = CleanReference(relative)
	identifier := pypath.Name(reference)
	if hasSuffixFold(identifier, extension) {
		identifier = identifier[:len(identifier)-len(extension)]
	}
	filename := identifier
	if originalExtension != "" && hasSuffixFold(filename, originalExtension) {
		filename = filename[:len(filename)-len(originalExtension)]
	}
	filename += extension
	ownerParent := pypath.Parent(owner)
	candidates := []string{pypath.Join(ownerParent, reference)}
	if strings.EqualFold(pypath.Suffix(reference), extension) {
		candidates = append(candidates, pypath.Clean(reference))
	}
	base := pypath.Join(ownerParent, relative)
	candidates = append(candidates,
		pypath.Join(base, folder, identifier, filename),
		pypath.Join(base, folder, filename),
		pypath.Join(base, reference),
		pypath.Join(ownerParent, filename))
	parents := pypath.Parents(owner)
	if len(parents) > 5 {
		parents = parents[:5]
	}
	for _, ancestor := range parents {
		candidates = append(candidates, pypath.Join(ancestor, folder, identifier, filename))
	}
	if res.Root != "" {
		candidates = append(candidates,
			pypath.Join(res.Root, folder, identifier, filename),
			pypath.Join(res.Root, folder, filename),
			pypath.Join(res.Root, reference),
			pypath.Join(res.Root, filename))
	}
	for _, candidate := range candidates {
		if strings.ToLower(pypath.Suffix(candidate)) == extension && pypath.IsFile(candidate) {
			return pypath.Resolve(candidate), nil
		}
	}
	// A copied entity can still work if its DataTool root was supplied.
	// Avoid choosing an arbitrary asset when multiple names collide.
	if res.Root != "" && pypath.IsDir(res.Root) {
		if res.Index == nil {
			if err := res.buildIndex(); err != nil {
				return "", err
			}
		}
		matches := res.Index[strings.ToLower(filename)]
		if len(matches) == 1 {
			return pypath.Resolve(matches[0]), nil
		}
		if len(matches) > 1 {
			return "", formatError("Ambiguous reference " + filename + ": found " +
				strconv.Itoa(len(matches)) + " matches under the root.")
		}
	}
	return "", nil
}

// buildIndex walks Root like os.walk (errors ignored, symlinked directories
// listed but not entered), counting every file.
func (res *Resolver) buildIndex() error {
	index := map[string][]string{}
	count := 0
	tooMany := formatError("Export root exceeds 500,000 files; select a smaller character folder.")
	err := filepath.WalkDir(res.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() && path != res.Root {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			// os.walk classifies with is_dir(), which follows links.
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				return nil
			}
		}
		count++
		if count > MaxIndexedFiles {
			return tooMany
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".owmdl") || strings.HasSuffix(lower, ".owentity") {
			index[lower] = append(index[lower], path)
		}
		return nil
	})
	if err == tooMany {
		res.Index = index // Python keeps the partial index on this error.
		return err
	}
	res.Index = index
	return nil
}

// ReadEntity reads an .owentity (Python's _read_entity).
func ReadEntity(path string) (*Entity, error) {
	r, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	identifier, err := r.String()
	if err != nil {
		return nil, err
	}
	if identifier != "owentity" {
		return nil, r.Fail("invalid entity identifier")
	}
	version, err := r.Unpack("HH")
	if err != nil {
		return nil, err
	}
	major, minor := uint16(version.Uint(0)), uint16(version.Uint(1))
	if err := CheckVersion(r, major, minor, "entity"); err != nil {
		return nil, err
	}
	entity := &Entity{}
	if entity.Name, err = r.String(); err != nil {
		return nil, err
	}
	if entity.Model, err = r.String(); err != nil {
		return nil, err
	}
	if _, err = r.String(); err != nil { // Effect reference.
		return nil, err
	}
	header, err := r.Unpack("IIIi")
	if err != nil {
		return nil, err
	}
	childCount := header.Int(3)
	if err := r.Count(childCount, 4096, "child entity count"); err != nil {
		return nil, err
	}
	entity.Children = make([]EntityChild, 0, childCount)
	for i := int64(0); i < childCount; i++ {
		reference, err := r.String()
		if err != nil {
			return nil, err
		}
		if _, err := r.Take(24); err != nil { // 'QQII': hardpoint, variant and their indexes.
			return nil, err
		}
		attachment, err := r.String()
		if err != nil {
			return nil, err
		}
		entity.Children = append(entity.Children, EntityChild{Reference: reference, Attachment: attachment})
	}
	entity.Relative = DefaultRelative
	if minor >= 1 {
		if entity.MaterialLook, err = r.String(); err != nil {
			return nil, err
		}
		if entity.Relative, err = r.String(); err != nil {
			return nil, err
		}
	}
	return entity, nil
}
