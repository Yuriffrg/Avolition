package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/formats/formatstest"
)

// tests/test_export.py: non-destructive renamed clip copies.

var clipOnly = map[string]any{"clipEnabled": true, "enabled": false, "c4dEnabled": false}

type exportFixture struct {
	workspace, root, sourceDir, state, source string
	data                                      []byte
	sourceTime                                time.Time
	api                                       *API
}

func newExportFixture(t *testing.T) *exportFixture {
	isolate(t)
	f := &exportFixture{workspace: tempDir(t)}
	f.root = filepath.Join(f.workspace, "Extracted Hero")
	f.sourceDir = filepath.Join(f.root, "Models", "Hero", "Animations")
	f.state = filepath.Join(f.workspace, "app-state")
	f.data = formatstest.NewClip().Bytes()
	f.source = writeFile(t, filepath.Join(f.sourceDir, "000000000001.owanimclip"), f.data)
	f.sourceTime = mtime(t, f.source)
	f.api = newAPI(t, Options{DataDir: f.state})
	ok(t, call(t, f.api, "scan_root", f.root))
	return f
}

func (f *exportFixture) export(t *testing.T, nickname, source string, output ...string) map[string]any {
	t.Helper()
	if source == "" {
		source = f.source
	}
	var folder any
	if len(output) > 0 {
		folder = output[0]
	}
	return okMap(t, call(t, f.api, "export_animation", source, nickname, folder, clipOnly))
}

func (f *exportFixture) untouched(t *testing.T) {
	t.Helper()
	if !bytes.Equal(readBytes(t, f.source), f.data) || !mtime(t, f.source).Equal(f.sourceTime) {
		t.Fatal("original changed")
	}
}

func (f *exportFixture) assertCopy(t *testing.T, result map[string]any, path string, expected []byte, reused bool) {
	t.Helper()
	if expected == nil {
		expected = f.data
	}
	if result["path"] != path || result["name"] != filepath.Base(path) || result["reused"] != reused || !bytes.Equal(readBytes(t, path), expected) {
		t.Fatalf("copy %v, want %s reused=%v", result, path, reused)
	}
}

func clipPaths(t *testing.T, scan map[string]any) []string {
	var out []string
	for _, row := range l(scan["clips"]) {
		out = append(out, m(row)["path"].(string))
	}
	sort.Strings(out)
	return out
}

func clipWithDuration(duration uint32) []byte {
	clip := formatstest.NewClip()
	clip.Duration = duration
	return clip.Bytes()
}

func TestExportDefaultFolderAndIdenticalCopy(t *testing.T) {
	f := newExportFixture(t)
	folder := filepath.Join(f.root, "Renamed Animations")
	if okMap(t, call(t, f.api, "bootstrap"))["outputFolder"] != folder {
		t.Fatal("default folder")
	}
	f.assertCopy(t, f.export(t, "Wave", ""), filepath.Join(folder, "Wave.owanimclip"), nil, false)
	f.untouched(t)
}

func TestExportExtensionAppendedOnceAndUnicodeNames(t *testing.T) {
	f := newExportFixture(t)
	result := f.export(t, "Victory.OWANIMCLIP", "")
	if strings.ToLower(filepath.Base(result["path"].(string))) != "victory.owanimclip" {
		t.Fatal(result["path"])
	}
	if f.export(t, "  Salutation café 日本語  ", "")["name"] != "Salutation café 日本語.owanimclip" {
		t.Fatal("unicode name")
	}
	f.untouched(t)
}

func TestExportRepeatReusesExistingCopyWithoutRewriting(t *testing.T) {
	f := newExportFixture(t)
	path := f.export(t, "Wave", "")["path"].(string)
	stamp := mtime(t, path)
	f.assertCopy(t, f.export(t, "Wave", ""), path, nil, true)
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.owanimclip"))
	if !mtime(t, path).Equal(stamp) || len(matches) != 1 {
		t.Fatal("rewritten")
	}
	second := writeFile(t, filepath.Join(f.sourceDir, "000000000002.owanimclip"), f.data)
	f.assertCopy(t, f.export(t, "Wave", second), path, nil, true)
}

func TestExportCollisionPreservesExistingBytesAndNumbersNewFile(t *testing.T) {
	f := newExportFixture(t)
	original := f.export(t, "Jump", "")["path"].(string)
	different := clipWithDuration(32)
	two := writeFile(t, filepath.Join(f.sourceDir, "000000000002.owanimclip"), different)
	second := f.export(t, "Jump", two)
	f.assertCopy(t, second, filepath.Join(filepath.Dir(original), "Jump (2).owanimclip"), different, false)
	repeated := f.export(t, "Jump", two)
	if repeated["path"] != second["path"] || repeated["reused"] != true || !bytes.Equal(readBytes(t, original), f.data) {
		t.Fatal("repeat")
	}
	f.untouched(t)
}

func TestExportExistingDirectoryIsNotOverwritten(t *testing.T) {
	f := newExportFixture(t)
	output := filepath.Join(f.root, "Renamed Animations")
	writeFile(t, filepath.Join(output, "Wave.owanimclip", "keep.txt"), []byte("keep"))
	f.assertCopy(t, f.export(t, "Wave", ""), filepath.Join(output, "Wave (2).owanimclip"), nil, false)
	if string(readBytes(t, filepath.Join(output, "Wave.owanimclip", "keep.txt"))) != "keep" {
		t.Fatal("directory overwritten")
	}
}

func TestExportWindowsInvalidAndReservedNamesAreRejected(t *testing.T) {
	f := newExportFixture(t)
	for _, nickname := range []string{"", "   ", ".", "..", "../escape", `..\escape`, "walk/run", "bad:name", "bad<name", "bad>name",
		`bad"name`, "bad|name", "bad?name", "bad*name", "bad\x00name", "bad\x1fname", "walk.", "CON", "con.owanimclip", "NUL.test",
		"PRN", "AUX", "COM1", "com9", "LPT1", "lpt9", "COM¹", "LPT²", "NUL.owanimclip", strings.Repeat("x", 300)} {
		failed(t, call(t, f.api, "export_animation", f.source, nickname, nil, clipOnly))
	}
	if matches, _ := filepath.Glob(filepath.Join(f.root, "Renamed Animations", "*.owanimclip")); len(matches) != 0 {
		t.Fatal(matches)
	}
	for _, nickname := range []string{"Console", "AUXiliary", "COM10", "LPT10"} {
		if f.export(t, nickname, "")["name"] != nickname+".owanimclip" {
			t.Fatal(nickname)
		}
	}
	f.untouched(t)
}

func TestExportMissingWrongExtensionAndDirectorySourcesFail(t *testing.T) {
	f := newExportFixture(t)
	wrong := writeFile(t, filepath.Join(f.sourceDir, "original.txt"), f.data)
	for _, source := range []string{filepath.Join(f.sourceDir, "missing.owanimclip"), wrong, f.sourceDir} {
		failed(t, call(t, f.api, "export_animation", source, "Wave", nil, clipOnly))
	}
	f.untouched(t)
}

func TestExportExactSourceDestinationIsRejected(t *testing.T) {
	f := newExportFixture(t)
	failed(t, call(t, f.api, "export_animation", f.source, "000000000001", f.sourceDir, clipOnly))
	f.untouched(t)
	assertJSON(t, clipPaths(t, okMap(t, call(t, f.api, "scan_root", f.root))), []string{f.source})
}

func TestExportCustomAbsoluteOutputAndInvalidOutputPaths(t *testing.T) {
	f := newExportFixture(t)
	output := filepath.Join(f.workspace, "Animation Picks", "New Folder")
	if okMap(t, call(t, f.api, "set_output_folder", output))["outputFolder"] != output {
		t.Fatal("output")
	}
	f.assertCopy(t, f.export(t, "Wave", ""), filepath.Join(output, "Wave.owanimclip"), nil, false)
	regular := writeFile(t, filepath.Join(f.workspace, "not-a-directory.txt"), []byte("keep"))
	for _, value := range []string{"", " ", "relative/output", regular} {
		failed(t, call(t, f.api, "set_output_folder", value))
	}
	f.untouched(t)
}

func TestLegacyPerRootDestinationsSurviveButQueueKeepsCommonOutput(t *testing.T) {
	f := newExportFixture(t)
	firstOutput := filepath.Join(f.workspace, "Hero Picks")
	ok(t, call(t, f.api, "set_output_folder", firstOutput))
	secondRoot := filepath.Join(f.workspace, "Other Hero")
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if okMap(t, call(t, f.api, "scan_root", secondRoot))["outputFolder"] != filepath.Join(secondRoot, "Renamed Animations") {
		t.Fatal("second root default")
	}
	secondOutput := filepath.Join(f.workspace, "Other Picks")
	ok(t, call(t, f.api, "set_output_folder", secondOutput))
	if okMap(t, call(t, f.api, "scan_root", f.root))["outputFolder"] != firstOutput {
		t.Fatal("first root output")
	}
	restarted := newAPI(t, Options{DataDir: f.state})
	if okMap(t, call(t, restarted, "bootstrap"))["outputFolder"] != secondOutput ||
		okMap(t, call(t, restarted, "scan_root", secondRoot))["outputFolder"] != secondOutput {
		t.Fatal("restart")
	}
}

func TestExportSubfoldersStayExcludedAfterChangeAndRestart(t *testing.T) {
	f := newExportFixture(t)
	f.export(t, "Wave", "")
	assertJSON(t, clipPaths(t, okMap(t, call(t, f.api, "scan_root", f.root))), []string{f.source})
	custom := filepath.Join(f.root, "Chosen Motions")
	ok(t, call(t, f.api, "set_output_folder", custom))
	f.export(t, "Turn", "")
	scan := okMap(t, call(t, newAPI(t, Options{DataDir: f.state}), "scan_root", f.root))
	assertJSON(t, clipPaths(t, scan), []string{f.source})
	if scan["outputFolder"] != custom {
		t.Fatal(scan["outputFolder"])
	}
}

func TestOutputEqualToRootExcludesOnlyExportedFiles(t *testing.T) {
	f := newExportFixture(t)
	rootOriginal := writeFile(t, filepath.Join(f.root, "000000000003.owanimclip"), clipWithDuration(40))
	ok(t, call(t, f.api, "set_output_folder", f.root))
	f.assertCopy(t, f.export(t, "Wave", ""), filepath.Join(f.root, "Wave.owanimclip"), nil, false)
	want := []string{f.source, rootOriginal}
	sort.Strings(want)
	assertJSON(t, clipPaths(t, okMap(t, call(t, f.api, "scan_root", f.root))), want)
	assertJSON(t, clipPaths(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.state}), "scan_root", f.root))), want)
}

func TestOutputInExistingSourceFolderKeepsOriginalsVisible(t *testing.T) {
	f := newExportFixture(t)
	ok(t, call(t, f.api, "set_output_folder", f.sourceDir))
	assertJSON(t, clipPaths(t, okMap(t, call(t, f.api, "scan_root", f.root))), []string{f.source})
	f.assertCopy(t, f.export(t, "Wave", ""), filepath.Join(f.sourceDir, "Wave.owanimclip"), nil, false)
	assertJSON(t, clipPaths(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.state}), "scan_root", f.root))), []string{f.source})
}

func TestConcurrentRepeatRequestsDoNotOverwriteOrDuplicate(t *testing.T) {
	f := newExportFixture(t)
	results := make([]Reply, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = call(t, f.api, "export_animation", f.source, "Concurrent", nil, clipOnly)
		}()
	}
	wg.Wait()
	paths := map[any]bool{}
	created := 0
	for _, result := range results {
		data := okMap(t, result)
		paths[data["path"]] = true
		if data["reused"] == false {
			created++
		}
	}
	if len(paths) != 1 || created != 1 {
		t.Fatalf("paths %v created %d", paths, created)
	}
	f.untouched(t)
	_ = fmt.Sprint
}
