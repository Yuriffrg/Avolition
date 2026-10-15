// Package exportbundle is the Go port of owpreview/export_bundle.py: publish
// independently selected animation formats without overwriting files.
//
// # Python → Go map
//
//	DEFAULT_FBX_OPTIONS   DefaultOptions
//	fbx_options           FBXOptions
//	same_bytes            SameBytes
//	_publish              publish
//	export_pair           ExportPair
//	export_bundle         ExportBundle
//
// Errors keep Python's text: *FileExistsError for an existing file with
// different content ("[Errno 17] An existing file has different content:
// '<path>'"), *formats.OSError for other file-system failures, and plain
// errors for ValueError messages.
package exportbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

// Options are the saved FBX/C4D export settings (catalog fbxOptions).
type Options struct {
	Enabled      bool   `json:"enabled"`
	UpAxis       string `json:"upAxis"`
	IncludeMesh  bool   `json:"includeMesh"`
	BakeTextures bool   `json:"bakeTextures"`
	RootMotion   bool   `json:"rootMotion"`
	C4DEnabled   bool   `json:"c4dEnabled"`
	ClipEnabled  bool   `json:"clipEnabled"`
}

// DefaultOptions is DEFAULT_FBX_OPTIONS.
func DefaultOptions() Options {
	return Options{Enabled: false, UpAxis: "Y", IncludeMesh: true, BakeTextures: true, RootMotion: true,
		C4DEnabled: true, ClipEnabled: false}
}

// FBXOptions is fbx_options(value): defaults overridden by the boolean keys
// and upAxis of value. value is nil (defaults), a map[string]any or an
// *ordered.Map[any] (a decoded JSON object); anything else is invalid.
// Unknown keys are ignored.
func FBXOptions(value any) (Options, error) {
	result := DefaultOptions()
	if value == nil {
		return result, nil
	}
	var get func(string) (any, bool)
	switch m := value.(type) {
	case map[string]any:
		get = func(k string) (any, bool) { v, ok := m[k]; return v, ok }
	case *ordered.Map[any]:
		if m == nil {
			return result, nil
		}
		get = m.Get
	default:
		return Options{}, errors.New("Invalid FBX export options.")
	}
	for _, field := range []struct {
		key    string
		target *bool
	}{{"enabled", &result.Enabled}, {"includeMesh", &result.IncludeMesh}, {"bakeTextures", &result.BakeTextures},
		{"rootMotion", &result.RootMotion}, {"c4dEnabled", &result.C4DEnabled}, {"clipEnabled", &result.ClipEnabled}} {
		if v, ok := get(field.key); ok {
			b, isBool := v.(bool)
			if !isBool {
				return Options{}, errors.New("Invalid FBX option: " + field.key)
			}
			*field.target = b
		}
	}
	if v, ok := get("upAxis"); ok {
		axis, _ := v.(string)
		if axis != "Y" && axis != "Z" {
			return Options{}, errors.New("Choose Y or Z as the FBX up axis.")
		}
		result.UpAxis = axis
	}
	return result, nil
}

// FileExistsError is Python's FileExistsError (errno 17) for a destination
// that cannot be reused. Filename is the destination as str(Path).
type FileExistsError struct {
	Message  string // strerror
	Filename string
}

func (e *FileExistsError) Error() string {
	return "[Errno 17] " + e.Message + ": " + formats.PythonRepr(e.Filename)
}

// Is makes errors.Is(err, fs.ErrExist) true.
func (e *FileExistsError) Is(target error) bool { return target == fs.ErrExist }

func osError(err error) error { return formats.PythonOSError(err) }

// SameBytes is same_bytes(source, destination): destination is a regular
// file with exactly source's content. A missing destination is not an error;
// an unreadable source is.
func SameBytes(source, destination string) (bool, error) {
	if !pypath.IsFile(destination) {
		return false, nil
	}
	left, err := os.Stat(source)
	if err != nil {
		return false, osError(err)
	}
	right, err := os.Stat(destination)
	if err != nil {
		return false, osError(err)
	}
	if left.Size() != right.Size() {
		return false, nil
	}
	a, err := os.Open(source)
	if err != nil {
		return false, osError(err)
	}
	defer a.Close()
	b, err := os.Open(destination)
	if err != nil {
		return false, osError(err)
	}
	defer b.Close()
	const block = 1024 * 1024
	bufA, bufB := make([]byte, block), make([]byte, block)
	for {
		n, errA := io.ReadFull(a, bufA)
		m, errB := io.ReadFull(b, bufB)
		if errA != nil && errA != io.EOF && errA != io.ErrUnexpectedEOF {
			return false, osError(errA)
		}
		if errB != nil && errB != io.EOF && errB != io.ErrUnexpectedEOF {
			return false, osError(errB)
		}
		if !bytes.Equal(bufA[:n], bufB[:m]) {
			return false, nil
		}
		if n == 0 {
			return true, nil
		}
	}
}

// copyData is shutil.copyfileobj(source, target, 1 MiB); tests replace it to
// simulate a failed write.
var copyData = func(target io.Writer, source io.Reader) error {
	_, err := io.CopyBuffer(target, source, make([]byte, 1024*1024))
	return err
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// publish is _publish: exclusive create, tracking only files owned by this
// transaction. It reports whether a new file was written (false: reused).
func publish(source, destination string, created *[]string) (bool, error) {
	if exists(destination) {
		same, err := SameBytes(source, destination)
		if err != nil {
			return false, err
		}
		if same {
			return false, nil
		}
		return false, &FileExistsError{Message: "An existing file has different content", Filename: destination}
	}
	handle, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			same, sameErr := SameBytes(source, destination)
			if sameErr != nil {
				return false, sameErr
			}
			if same {
				return false, nil
			}
			return false, &FileExistsError{Message: "File exists", Filename: destination}
		}
		return false, osError(err)
	}
	*created = append(*created, destination)
	defer handle.Close()
	original, err := os.Open(source)
	if err != nil {
		return false, osError(err)
	}
	defer original.Close()
	if err := copyData(handle, original); err != nil {
		return false, osError(err)
	}
	if err := handle.Close(); err != nil {
		return false, osError(err)
	}
	return true, nil
}

// Details is the builder's result ({warnings: [...], ...}).
type Details struct {
	Warnings []string
}

// Builder writes the requested scene files. scenes maps a format ("fbx",
// "c4d") to its staging destination; textures is the staging folder for
// tex/*.png dependencies (absent until the builder creates it). A nil
// *Details means no warnings.
type Builder func(scenes map[string]string, textures string) (*Details, error)

// Result is export_bundle's reply. A skipped (empty) selection has only
// Skipped, Files, Path and Name; otherwise ClipPath/FBXPath/C4DPath are set
// for the selected formats. Extra holds keys added by callers (the API adds
// assignments, fps and lastFrame); they are encoded last.
type Result struct {
	Skipped       bool
	Path          *string
	Name          *string
	ClipPath      *string
	FBXPath       *string
	C4DPath       *string
	TextureFolder *string
	TextureCount  int
	Reused        bool
	Warnings      []string
	Files         []string
	Extra         *ordered.Map[any]
}

// MarshalJSON writes the Python dict with its key order.
func (r *Result) MarshalJSON() ([]byte, error) {
	m := ordered.New[any]()
	if r.Skipped {
		m.Set("skipped", true)
		m.Set("files", nonNil(r.Files))
		m.Set("path", r.Path)
		m.Set("name", r.Name)
	} else {
		m.Set("path", r.Path)
		m.Set("name", r.Name)
		for _, p := range []struct {
			key   string
			value *string
		}{{"clipPath", r.ClipPath}, {"fbxPath", r.FBXPath}, {"c4dPath", r.C4DPath}} {
			if p.value != nil {
				m.Set(p.key, *p.value)
			}
		}
		m.Set("textureFolder", r.TextureFolder)
		m.Set("textureCount", r.TextureCount)
		m.Set("reused", r.Reused)
		m.Set("warnings", nonNil(r.Warnings))
		m.Set("files", nonNil(r.Files))
	}
	if r.Extra != nil {
		r.Extra.Range(func(k string, v any) bool { m.Set(k, v); return true })
	}
	return json.Marshal(m)
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// ExportPair is export_pair(source, folder, filename, builder):
// builder(stageFBX, stageTex) writes the FBX.
//
// FBX references must use relative tex/<filename> paths. The FBX is completed
// before either user-facing animation file appears. Existing equal files are
// reused; a collision numbers both animation files together.
func ExportPair(source, folder, filename string, builder func(fbx, textures string) (*Details, error)) (*Result, error) {
	return ExportBundle(source, folder, filename, func(scenes map[string]string, textures string) (*Details, error) {
		return builder(scenes["fbx"], textures)
	}, []string{"clip", "fbx"})
}

var allFormats = []string{"clip", "fbx", "c4d"}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pngs is sorted(stage_tex.glob('*.png')): case-insensitive on Windows like
// pathlib.
func pngs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, osError(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".png") || (runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(name), ".png")) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if runtime.GOOS == "windows" {
			return strings.ToLower(names[i]) < strings.ToLower(names[j])
		}
		return names[i] < names[j]
	})
	return names, nil
}

func mkdirExistOK(path string, parents bool) error {
	var err error
	if parents {
		err = os.MkdirAll(path, 0o777)
	} else {
		err = os.Mkdir(path, 0o777)
	}
	if err == nil || (errors.Is(err, fs.ErrExist) && pypath.IsDir(path)) {
		return nil
	}
	if pypath.IsDir(path) {
		return nil
	}
	if exists(path) {
		return &FileExistsError{Message: "File exists", Filename: path}
	}
	return osError(err)
}

func str(s string) *string { return &s }

// ExportBundle is export_bundle(source, folder, filename, builder, formats):
// publish only the requested formats ("clip", "fbx", "c4d"); the builder
// receives scene destinations only.
//
// All selected animation files share a collision suffix. Unselected files have
// no bearing on that suffix and are never touched. Clip-only exports do not
// call the scene builder. An empty selection performs no filesystem work.
func ExportBundle(source, folder, filename string, builder Builder, requested []string) (*Result, error) {
	want := map[string]bool{}
	for _, value := range requested {
		if value != "clip" && value != "fbx" && value != "c4d" {
			return nil, errors.New("Choose .owanimclip, FBX or C4D as an export format.")
		}
		want[value] = true
	}
	var kinds []string
	for _, kind := range allFormats {
		if want[kind] {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return &Result{Skipped: true, Files: []string{}}, nil
	}
	source, folder = pypath.Clean(source), pypath.Clean(folder)
	if err := mkdirExistOK(folder, true); err != nil {
		return nil, err
	}
	desired := pypath.Join(folder, filename)
	stage, err := os.MkdirTemp(folder, ".whitehound-export-")
	if err != nil {
		return nil, osError(err)
	}
	defer os.RemoveAll(stage)
	scenes := map[string]string{}
	for _, kind := range kinds {
		if kind != "clip" {
			scenes[kind] = pypath.Join(stage, "animation."+kind)
		}
	}
	stageTex := pypath.Join(stage, "tex")
	details := &Details{}
	if len(scenes) > 0 {
		result, err := builder(scenes, stageTex)
		if err != nil {
			return nil, err
		}
		if result != nil {
			details = result
		}
	}
	for _, kind := range kinds {
		if scene, ok := scenes[kind]; ok {
			info, err := os.Stat(scene)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return nil, errors.New(strings.ToUpper(kind) + " export did not produce a file.")
			}
		}
	}
	textures, err := pngs(stageTex)
	if err != nil {
		return nil, err
	}
	texFolder := pypath.Join(folder, "tex")
	if len(textures) > 0 {
		if err := mkdirExistOK(texFolder, false); err != nil {
			return nil, err
		}
	}
	resolvedSource := pypath.Resolve(source)
	type pair struct{ original, target string }
	for number := 1; number <= 10000; number++ {
		clipPath := desired
		if number > 1 {
			clipPath = pypath.Join(pypath.Parent(desired), pypath.Stem(desired)+" ("+strconv.Itoa(number)+").owanimclip")
		}
		if want["clip"] && samePath(pypath.Resolve(clipPath), resolvedSource) {
			continue
		}
		targets := map[string]string{}
		var pairs []pair
		for _, kind := range kinds {
			target := clipPath
			original := source
			if kind != "clip" {
				target = pypath.WithSuffix(clipPath, "."+kind)
				original = scenes[kind]
			}
			targets[kind] = target
			pairs = append(pairs, pair{original, target})
		}
		collision := false
		for _, p := range pairs {
			if exists(p.target) {
				same, err := SameBytes(p.original, p.target)
				if err != nil {
					return nil, err
				}
				if !same {
					collision = true
					break
				}
			}
		}
		if collision {
			continue
		}
		var created []string
		rollback := func() {
			for i := len(created) - 1; i >= 0; i-- {
				os.Remove(created[i])
			}
		}
		err := func() error {
			// Publish textures before the FBX, so a completed FBX always has
			// its dependencies. Never replace an externally modified texture.
			for _, name := range textures {
				if _, err := publish(pypath.Join(stageTex, name), pypath.Join(texFolder, name), &created); err != nil {
					return err
				}
			}
			for _, p := range pairs {
				if _, err := publish(p.original, p.target, &created); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			rollback()
			var exists *FileExistsError
			if errors.As(err, &exists) {
				for _, p := range pairs {
					if p.target == exists.Filename {
						collision = true
					}
				}
				if collision {
					continue
				}
			}
			return nil, err
		}
		first := targets[kinds[0]]
		result := &Result{Path: str(first), Name: str(pypath.Name(first)), TextureCount: len(textures),
			Reused: len(created) == 0, Warnings: details.Warnings}
		for kind, target := range targets {
			switch kind {
			case "clip":
				result.ClipPath = str(target)
			case "fbx":
				result.FBXPath = str(target)
			case "c4d":
				result.C4DPath = str(target)
			}
		}
		if len(textures) > 0 {
			result.TextureFolder = str(texFolder)
		}
		if result.Warnings == nil {
			result.Warnings = []string{}
		}
		for _, p := range pairs {
			result.Files = append(result.Files, p.target)
		}
		for _, name := range textures {
			result.Files = append(result.Files, pypath.Join(texFolder, name))
		}
		return result, nil
	}
	return nil, errors.New("Too many files share that nickname. Choose another name.")
}
