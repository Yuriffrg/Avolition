package exportbundle

// Port of tests/test_export_bundle.py, the export_bundle-level checks of
// tests/test_export.py (whose BrowserAPI plumbing is ported with the API),
// and oracle comparisons with owpreview/export_bundle.py.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
)

type fixture struct {
	t        *testing.T
	base     string
	source   string
	output   string
	fbxBytes []byte
}

func newFixture(t *testing.T) *fixture {
	base := t.TempDir()
	f := &fixture{t: t, base: base, source: filepath.Join(base, "source.owanimclip"),
		output: filepath.Join(base, "Output"), fbxBytes: []byte("FBX fixture")}
	write(t, f.source, "original animation bytes")
	return f
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) build(destination, textures string) (*Details, error) {
	if err := os.WriteFile(destination, f.fbxBytes, 0o666); err != nil {
		return nil, err
	}
	if err := os.Mkdir(textures, 0o777); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(textures, "shared_color.png"), []byte("PNG fixture"), 0o666); err != nil {
		return nil, err
	}
	return &Details{Warnings: []string{"Test preview note"}}, nil
}

func (f *fixture) export(builder func(string, string) (*Details, error)) (*Result, error) {
	if builder == nil {
		builder = f.build
	}
	return ExportPair(f.source, f.output, "Wave.owanimclip", builder)
}

func (f *fixture) mustExport() *Result {
	f.t.Helper()
	result, err := f.export(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func glob(t *testing.T, pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func mtimes(t *testing.T, files []string) map[string]int64 {
	out := map[string]int64{}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		out[file] = info.ModTime().UnixNano()
	}
	return out
}

func TestMatchingNamesRelativeTexturesAndSourceUntouched(t *testing.T) {
	f := newFixture(t)
	result := f.mustExport()
	if filepath.Base(*result.Path) != "Wave.owanimclip" || filepath.Base(*result.FBXPath) != "Wave.fbx" ||
		*result.TextureFolder != filepath.Join(f.output, "tex") {
		t.Fatal(result)
	}
	if read(t, filepath.Join(f.output, "Wave.owanimclip")) != read(t, f.source) {
		t.Fatal("clip differs")
	}
	if result.TextureCount != 1 || !reflect.DeepEqual(result.Warnings, []string{"Test preview note"}) || result.Reused {
		t.Fatal(result)
	}
	if len(glob(t, filepath.Join(f.output, ".whitehound-export-*"))) != 0 {
		t.Fatal("staging folder left behind")
	}
}

func TestRepeatReusesBothFilesWithoutTouchingTimestamps(t *testing.T) {
	f := newFixture(t)
	first := f.mustExport()
	times := mtimes(t, first.Files)
	repeated := f.mustExport()
	if !repeated.Reused || !reflect.DeepEqual(first.Files, repeated.Files) || !reflect.DeepEqual(times, mtimes(t, repeated.Files)) {
		t.Fatal(repeated)
	}
}

func TestFBXCollisionNumbersBothEvenWhenRawClipMatches(t *testing.T) {
	f := newFixture(t)
	first := f.mustExport()
	f.fbxBytes = []byte("FBX with different axis or rig")
	second := f.mustExport()
	if filepath.Base(*second.Path) != "Wave (2).owanimclip" || filepath.Base(*second.FBXPath) != "Wave (2).fbx" {
		t.Fatal(second)
	}
	if read(t, *first.FBXPath) != "FBX fixture" || len(glob(t, filepath.Join(f.output, "tex", "*.png"))) != 1 {
		t.Fatal("first export changed")
	}
}

func TestExistingIdenticalRawCopyCanGainMatchingFBX(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(f.output, "Wave.owanimclip")
	write(t, target, read(t, f.source))
	before := mtimes(t, []string{target})
	result := f.mustExport()
	if *result.Path != target || result.Reused || !reflect.DeepEqual(before, mtimes(t, []string{target})) {
		t.Fatal(result)
	}
}

func TestExistingFBXAloneIsNeverReplaced(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.output, "Wave.fbx"), "User artwork")
	result := f.mustExport()
	if filepath.Base(*result.FBXPath) != "Wave (2).fbx" || read(t, filepath.Join(f.output, "Wave.fbx")) != "User artwork" {
		t.Fatal(result)
	}
}

func TestBuildFailurePublishesNoFiles(t *testing.T) {
	f := newFixture(t)
	_, err := f.export(func(destination, textures string) (*Details, error) {
		os.WriteFile(destination, []byte("partial FBX"), 0o666)
		return nil, errors.New("Bad skeleton")
	})
	if err == nil || err.Error() != "Bad skeleton" {
		t.Fatal(err)
	}
	if names := listDir(t, f.output); len(names) != 0 {
		t.Fatal(names)
	}
}

func failCopyFor(t *testing.T, suffix string) {
	original := copyData
	t.Cleanup(func() { copyData = original })
	copyData = func(target io.Writer, source io.Reader) error {
		if file, ok := target.(*os.File); ok && strings.HasSuffix(file.Name(), suffix) {
			file.Write([]byte("partial"))
			return errors.New("Disk full")
		}
		return original(target, source)
	}
}

func TestPublishFailureRemovesOnlyNewFiles(t *testing.T) {
	f := newFixture(t)
	prior := filepath.Join(f.output, "keep.fbx")
	write(t, prior, "User work")
	failCopyFor(t, ".fbx")
	if _, err := f.export(nil); err == nil || err.Error() != "Disk full" {
		t.Fatal(err)
	}
	if read(t, prior) != "User work" {
		t.Fatal("prior changed")
	}
	for _, name := range []string{"Wave.fbx", "Wave.owanimclip"} {
		if _, err := os.Stat(filepath.Join(f.output, name)); err == nil {
			t.Fatal(name)
		}
	}
	if len(glob(t, filepath.Join(f.output, "tex", "*.png"))) != 0 {
		t.Fatal("texture left behind")
	}
}

func TestChangedExistingTextureCausesErrorWithoutOverwriting(t *testing.T) {
	f := newFixture(t)
	prior := filepath.Join(f.output, "tex", "shared_color.png")
	write(t, prior, "Edited by user")
	_, err := f.export(nil)
	var exists *FileExistsError
	if !errors.As(err, &exists) || !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	wantError := oracle.Exec(t, "raise FileExistsError(17, 'An existing file has different content', args[0])", prior)
	if err.Error() != wantError.Error {
		t.Fatal(err)
	}
	if read(t, prior) != "Edited by user" {
		t.Fatal("texture overwritten")
	}
	if _, err := os.Stat(filepath.Join(f.output, "Wave.owanimclip")); err == nil {
		t.Fatal("clip published")
	}
}

func TestOptionsHaveYUpAndKeepMotionDefaults(t *testing.T) {
	options, err := FBXOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(options)
	if string(data) != `{"enabled":false,"upAxis":"Y","includeMesh":true,"bakeTextures":true,"rootMotion":true,"c4dEnabled":true,"clipEnabled":false}` {
		t.Fatal(string(data))
	}
	if options, err := FBXOptions(map[string]any{"upAxis": "Z"}); err != nil || options.UpAxis != "Z" {
		t.Fatal(options, err)
	}
	for _, invalid := range []any{map[string]any{"upAxis": "X"}, map[string]any{"enabled": "yes"}, map[string]any{"rootMotion": 1.0},
		map[string]any{"clipEnabled": 1.0}, map[string]any{"c4dEnabled": "yes"}, []any{}} {
		if _, err := FBXOptions(invalid); err == nil {
			t.Errorf("%v accepted", invalid)
		}
	}
}

func writeScenes(prefix string) Builder {
	return func(scenes map[string]string, textures string) (*Details, error) {
		for kind, path := range scenes {
			if err := os.WriteFile(path, []byte(prefix+kind), 0o666); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
}

func TestThreeFormatsPublishWithOneCollisionSuffix(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.output, "Wave.c4d"), "Existing C4D project")
	result, err := ExportBundle(f.source, f.output, "Wave.owanimclip", writeScenes("New "), []string{"clip", "fbx", "c4d"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*string{result.Path, result.FBXPath, result.C4DPath} {
		if stem := strings.TrimSuffix(filepath.Base(*p), filepath.Ext(*p)); stem != "Wave (2)" {
			t.Fatal(*p)
		}
	}
	if read(t, filepath.Join(f.output, "Wave.c4d")) != "Existing C4D project" {
		t.Fatal("existing scene replaced")
	}
}

func TestC4DFailureDoesNotPublishFBXOrClip(t *testing.T) {
	f := newFixture(t)
	_, err := ExportBundle(f.source, f.output, "Wave.owanimclip", func(scenes map[string]string, textures string) (*Details, error) {
		os.WriteFile(scenes["fbx"], []byte("Completed FBX"), 0o666)
		os.WriteFile(scenes["c4d"], []byte("Partial native scene"), 0o666)
		return nil, errors.New("Octane missing")
	}, []string{"clip", "fbx", "c4d"})
	if err == nil || err.Error() != "Octane missing" || len(listDir(t, f.output)) != 0 {
		t.Fatal(err)
	}
}

func TestDisabledFormatsDoNotInfluenceCollisionOrGetTouched(t *testing.T) {
	f := newFixture(t)
	clip, fbx := filepath.Join(f.output, "Wave.owanimclip"), filepath.Join(f.output, "Wave.fbx")
	write(t, clip, "User clip: intentionally different")
	write(t, fbx, "User FBX: intentionally different")
	before := mtimes(t, []string{clip, fbx})
	result, err := ExportBundle(f.source, f.output, "Wave.owanimclip", func(scenes map[string]string, textures string) (*Details, error) {
		if len(scenes) != 1 || scenes["c4d"] == "" {
			t.Errorf("scenes %v", scenes)
		}
		return nil, os.WriteFile(scenes["c4d"], []byte("Native scene fixture"), 0o666)
	}, []string{"c4d"})
	if err != nil {
		t.Fatal(err)
	}
	if *result.Name != "Wave.c4d" || *result.Path != *result.C4DPath || !reflect.DeepEqual(result.Files, []string{filepath.Join(f.output, "Wave.c4d")}) ||
		result.ClipPath != nil || result.FBXPath != nil {
		t.Fatal(result)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "clipPath") || strings.Contains(string(data), "fbxPath") {
		t.Fatal(string(data))
	}
	if !reflect.DeepEqual(before, mtimes(t, []string{clip, fbx})) || read(t, clip) != "User clip: intentionally different" {
		t.Fatal("disabled files touched")
	}
}

func TestSceneOnlyCanUseSourceFolderAndSourceStem(t *testing.T) {
	f := newFixture(t)
	result, err := ExportBundle(f.source, f.base, filepath.Base(f.source), func(scenes map[string]string, textures string) (*Details, error) {
		return nil, os.WriteFile(scenes["fbx"], []byte("FBX fixture"), 0o666)
	}, []string{"fbx"})
	if err != nil {
		t.Fatal(err)
	}
	if *result.Path != strings.TrimSuffix(f.source, ".owanimclip")+".fbx" || read(t, f.source) != "original animation bytes" {
		t.Fatal(result)
	}
}

func TestEmptySelectionDoesNotInvokeBuilderOrCreateFolder(t *testing.T) {
	f := newFixture(t)
	result, err := ExportBundle(f.source, f.output, "Wave.owanimclip", func(map[string]string, string) (*Details, error) {
		t.Fatal("Empty selection called the builder")
		return nil, nil
	}, nil)
	if err != nil || !result.Skipped || len(result.Files) != 0 || result.Path != nil {
		t.Fatal(result, err)
	}
	data, _ := json.Marshal(result)
	if string(data) != `{"skipped":true,"files":[],"path":null,"name":null}` {
		t.Fatal(string(data))
	}
	if _, err := os.Stat(f.output); err == nil {
		t.Fatal("output created")
	}
}

func TestSceneOnlyFailureRollsBackNewScenesWithoutTouchingDisabledClip(t *testing.T) {
	f := newFixture(t)
	original := filepath.Join(f.output, "Wave.owanimclip")
	write(t, original, "Existing unrelated clip")
	failCopyFor(t, ".c4d")
	_, err := ExportBundle(f.source, f.output, "Wave.owanimclip", writeScenes(""), []string{"fbx", "c4d"})
	if err == nil || err.Error() != "Disk full" {
		t.Fatal(err)
	}
	if names := listDir(t, f.output); !reflect.DeepEqual(names, []string{"Wave.owanimclip"}) || read(t, original) != "Existing unrelated clip" {
		t.Fatal(names)
	}
}

func TestInvalidFormatAndMissingSceneFile(t *testing.T) {
	f := newFixture(t)
	if _, err := ExportBundle(f.source, f.output, "Wave.owanimclip", nil, []string{"obj"}); err == nil ||
		err.Error() != "Choose .owanimclip, FBX or C4D as an export format." {
		t.Fatal(err)
	}
	_, err := ExportBundle(f.source, f.output, "Wave.owanimclip", func(map[string]string, string) (*Details, error) {
		return nil, nil
	}, []string{"clip", "c4d"})
	if err == nil || err.Error() != "C4D export did not produce a file." {
		t.Fatal(err)
	}
}

// --- export_bundle-level checks from tests/test_export.py --------------------

func clipOnly(t *testing.T, source, folder, filename string) *Result {
	t.Helper()
	result, err := ExportBundle(source, folder, filename, nil, []string{"clip"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCollisionPreservesExistingBytesAndNumbersNewFile(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.output, "Wave.owanimclip"), "different")
	write(t, filepath.Join(f.output, "Wave (2).owanimclip"), "also different")
	result := clipOnly(t, f.source, f.output, "Wave.owanimclip")
	if filepath.Base(*result.Path) != "Wave (3).owanimclip" || result.Reused {
		t.Fatal(result)
	}
	if read(t, filepath.Join(f.output, "Wave.owanimclip")) != "different" {
		t.Fatal("existing changed")
	}
}

func TestExistingDirectoryIsNotOverwrittenByExport(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.output, "Wave.owanimclip"), 0o777); err != nil {
		t.Fatal(err)
	}
	result := clipOnly(t, f.source, f.output, "Wave.owanimclip")
	if filepath.Base(*result.Path) != "Wave (2).owanimclip" {
		t.Fatal(result)
	}
}

func TestExactSourceDestinationIsSkippedWithoutModifyingSource(t *testing.T) {
	f := newFixture(t)
	result := clipOnly(t, f.source, f.base, filepath.Base(f.source))
	if filepath.Base(*result.Path) != "source (2).owanimclip" || read(t, f.source) != "original animation bytes" {
		t.Fatal(result)
	}
}

func TestFailedWriteCleansOnlyNewIncompleteCopy(t *testing.T) {
	f := newFixture(t)
	failCopyFor(t, ".owanimclip")
	if _, err := ExportBundle(f.source, f.output, "Wave.owanimclip", nil, []string{"clip"}); err == nil || err.Error() != "Disk full" {
		t.Fatal(err)
	}
	if names := listDir(t, f.output); len(names) != 0 {
		t.Fatal(names)
	}
}

// Like the Python module, ExportBundle is serialized by its caller (the API
// holds its lock around every export); concurrent API requests then reuse
// the first copy.
func TestConcurrentRepeatRequestsDoNotOverwriteOrDuplicate(t *testing.T) {
	f := newFixture(t)
	var lock sync.Mutex
	var wg sync.WaitGroup
	results := make([]*Result, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lock.Lock()
			defer lock.Unlock()
			results[i], errs[i] = ExportBundle(f.source, f.output, "Wave.owanimclip", nil, []string{"clip"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		if *results[i].Path != filepath.Join(f.output, "Wave.owanimclip") {
			t.Fatal(*results[i].Path)
		}
	}
	if names := listDir(t, f.output); !reflect.DeepEqual(names, []string{"Wave.owanimclip"}) {
		t.Fatal(names)
	}
}

func TestSameBytes(t *testing.T) {
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	big := strings.Repeat("x", 3*1024*1024+5)
	write(t, a, big)
	write(t, b, big)
	write(t, c, big[:len(big)-1]+"y")
	for _, check := range []struct {
		dst  string
		want bool
	}{{b, true}, {c, false}, {filepath.Join(dir, "missing"), false}, {dir, false}} {
		if got, err := SameBytes(a, check.dst); err != nil || got != check.want {
			t.Fatalf("%s: %v %v", check.dst, got, err)
		}
	}
	if _, err := SameBytes(filepath.Join(dir, "missing"), b); err == nil {
		t.Fatal("missing source accepted")
	}
}

// --- Oracle comparison ------------------------------------------------------

// Each scenario prepares the same files in a Python and a Go folder, runs a
// sequence of exports and compares results (paths relative to the folder)
// and the final folder contents.
const bundlePython = `
import os
from pathlib import Path
from owpreview.export_bundle import export_bundle, fbx_options
root, steps = Path(args[0]), args[1]
out = []
for step in steps:
    def build(paths, textures, step=step):
        for kind, path in paths.items():
            path.write_bytes(step['scenes'][kind].encode())
        if step.get('textures'):
            textures.mkdir()
            for name, content in step['textures'].items():
                (textures / name).write_bytes(content.encode())
        return {'warnings': step.get('warnings', [])}
    for path, content in step.get('prepare', {}).items():
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        if content is None:
            target.mkdir()
        else:
            target.write_bytes(content.encode())
    try:
        out.append({'ok': True, 'result': export_bundle(root / step['source'], root / step['folder'], step['filename'], build, step['formats'])})
    except Exception as error:
        out.append({'ok': False, 'error': str(error).replace(str(root), '<root>')})
files = {}
for dirpath, dirnames, filenames in os.walk(root):
    for name in filenames:
        path = Path(dirpath) / name
        files[str(path.relative_to(root))] = path.read_text()
options = []
for value in args[2]:
    try:
        options.append({'ok': True, 'result': fbx_options(value)})
    except Exception as error:
        options.append({'ok': False, 'error': str(error)})
result = {'steps': out, 'files': files, 'options': options}
`

type step struct {
	Source   string             `json:"source"`
	Folder   string             `json:"folder"`
	Filename string             `json:"filename"`
	Formats  []string           `json:"formats"`
	Scenes   map[string]string  `json:"scenes"`
	Textures map[string]string  `json:"textures,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
	Prepare  map[string]*string `json:"prepare,omitempty"`
}

func goSteps(t *testing.T, root string, steps []step) (results []map[string]any, files map[string]string) {
	for _, s := range steps {
		for path, content := range s.Prepare {
			target := filepath.Join(root, path)
			os.MkdirAll(filepath.Dir(target), 0o777)
			if content == nil {
				os.Mkdir(target, 0o777)
			} else {
				os.WriteFile(target, []byte(*content), 0o666)
			}
		}
		s := s
		result, err := ExportBundle(filepath.Join(root, s.Source), filepath.Join(root, s.Folder), s.Filename,
			func(scenes map[string]string, textures string) (*Details, error) {
				for kind, path := range scenes {
					os.WriteFile(path, []byte(s.Scenes[kind]), 0o666)
				}
				if len(s.Textures) > 0 {
					os.Mkdir(textures, 0o777)
					for name, content := range s.Textures {
						os.WriteFile(filepath.Join(textures, name), []byte(content), 0o666)
					}
				}
				warnings := s.Warnings
				if warnings == nil {
					warnings = []string{}
				}
				return &Details{Warnings: warnings}, nil
			}, s.Formats)
		if err != nil {
			results = append(results, map[string]any{"ok": false, "error": strings.ReplaceAll(err.Error(), root, "<root>")})
		} else {
			results = append(results, map[string]any{"ok": true, "result": result})
		}
	}
	files = map[string]string{}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			data, _ := os.ReadFile(path)
			files[rel] = string(data)
		}
		return nil
	})
	return results, files
}

func TestExportBundleMatchesPython(t *testing.T) {
	text := func(s string) *string { return &s }
	scenes := map[string]string{"fbx": "FBX one", "c4d": "C4D one"}
	steps := []step{
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"clip", "fbx"},
			Scenes: scenes, Textures: map[string]string{"b.png": "B", "a.png": "A", "notes.txt": "skip"}, Warnings: []string{"note"},
			Prepare: map[string]*string{"src/source.owanimclip": text("clip bytes")}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"clip", "fbx"},
			Scenes: scenes, Textures: map[string]string{"a.png": "A", "b.png": "B"}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"fbx", "clip", "c4d"},
			Scenes: map[string]string{"fbx": "FBX two", "c4d": "C4D two"}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"c4d"},
			Scenes: scenes, Prepare: map[string]*string{"out/Wave.c4d": text("user")}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"clip", "fbx"},
			Scenes: map[string]string{"fbx": "FBX three"}, Textures: map[string]string{"a.png": "changed"}},
		{Source: "src/source.owanimclip", Folder: "src", Filename: "source.owanimclip", Formats: []string{"clip"}},
		{Source: "src/source.owanimclip", Folder: "src", Filename: "source.owanimclip", Formats: []string{"fbx"},
			Scenes: scenes},
		{Source: "src/source.owanimclip", Folder: "dir", Filename: "Wave.owanimclip", Formats: []string{"clip"},
			Prepare: map[string]*string{"dir/Wave.owanimclip": nil}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{}},
		{Source: "src/source.owanimclip", Folder: "out", Filename: "Wave.owanimclip", Formats: []string{"obj"}},
		{Source: "src/missing.owanimclip", Folder: "out2", Filename: "Wave.owanimclip", Formats: []string{"clip"}},
	}
	options := []any{nil, map[string]any{"upAxis": "Z", "enabled": true, "other": 1}, map[string]any{"upAxis": "X"},
		map[string]any{"enabled": "yes"}, map[string]any{"rootMotion": 1}, map[string]any{"clipEnabled": 0},
		[]any{}, map[string]any{"bakeTextures": false, "includeMesh": false, "c4dEnabled": false, "clipEnabled": true}}
	pyRoot := t.TempDir()
	raw := oracle.Exec(t, bundlePython, pyRoot, steps, options).MustValue(t)
	var python struct {
		Steps   []json.RawMessage `json:"steps"`
		Files   map[string]string `json:"files"`
		Options []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(raw, &python); err != nil {
		t.Fatal(err)
	}
	goRoot := t.TempDir()
	results, files := goSteps(t, goRoot, steps)
	for i, result := range results {
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		want := oracle.ReplaceJSONPath(string(python.Steps[i]), pyRoot, "<root>")
		got := oracle.ReplaceJSONPath(string(data), goRoot, "<root>")
		jsoncmp.Equal(t, json.RawMessage(want), json.RawMessage(got), jsoncmp.Options{})
		var a, b ordered.Map[any]
		_ = json.Unmarshal([]byte(want), &a)
		_ = json.Unmarshal([]byte(got), &b)
		if inner := func(m ordered.Map[any]) string { v, _ := m.Get("result"); d, _ := json.Marshal(v); return string(d) }; inner(a) != inner(b) {
			// Same keys and values must also come in Python's order.
			t.Errorf("step %d key order:\n got  %s\n want %s", i, inner(b), inner(a))
		}
	}
	if !reflect.DeepEqual(files, python.Files) {
		t.Errorf("folder contents differ:\n go     %v\n python %v", keys(files), keys(python.Files))
	}
	for i, value := range options {
		result, err := FBXOptions(convertOption(value))
		var got any = map[string]any{"ok": true, "result": result}
		if err != nil {
			got = map[string]any{"ok": false, "error": err.Error()}
		}
		jsoncmp.Equal(t, python.Options[i], got, jsoncmp.Options{})
	}
}

// convertOption gives Go the JSON-decoded form of an option value (numbers
// are float64), as the API receives it.
func convertOption(value any) any {
	data, _ := json.Marshal(value)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, fmt.Sprintf("%s=%q", k, m[k]))
	}
	sort.Strings(out)
	return out
}
