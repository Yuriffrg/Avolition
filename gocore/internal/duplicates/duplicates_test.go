package duplicates

// Ported from tests/test_duplicates.py. test_scan_reports_duplicate_counts_
// and_keeps_each_paths_annotation goes through BrowserAPI.scan_root and
// belongs to the api package port.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avolition/gocore/internal/oracle"
)

// clipBytes mirrors test_formats.clip_bytes: a 16-byte header and key data.
func clipBytes(duration uint32) []byte {
	blob := binary.LittleEndian.AppendUint16(nil, 2)
	blob = binary.LittleEndian.AppendUint16(blob, 0)
	blob = binary.LittleEndian.AppendUint32(blob, 1)
	blob = binary.LittleEndian.AppendUint32(blob, math.Float32bits(30))
	blob = binary.LittleEndian.AppendUint32(blob, duration)
	blob = append(blob, "\x09\x00\x00\x00bone_0002"...)
	for i := 0; i < 24; i++ {
		blob = binary.LittleEndian.AppendUint32(blob, uint32(i*7))
	}
	return blob
}

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture { return &fixture{t, t.TempDir()} }

// clip writes a file and returns the row read_clip_header would produce.
func (f *fixture) clip(relative string, content []byte) Clip {
	f.t.Helper()
	if content == nil {
		content = clipBytes(31)
	}
	path := filepath.Join(f.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		f.t.Fatal(err)
	}
	major, minor := binary.LittleEndian.Uint16(content), binary.LittleEndian.Uint16(content[2:])
	return Clip{
		Path: path, Size: int64(len(content)), Version: fmt.Sprintf("%d.%d", major, minor),
		FPS:            float64(math.Float32frombits(binary.LittleEndian.Uint32(content[8:]))),
		DurationFrames: int64(binary.LittleEndian.Uint32(content[12:])),
		BoneCount:      int64(binary.LittleEndian.Uint32(content[4:])),
	}
}

func keys(annotations []Annotation) map[string]bool {
	set := map[string]bool{}
	for _, a := range annotations {
		set[a.DuplicateKey] = true
	}
	return set
}

func counts(annotations []Annotation) []int {
	var result []int
	for _, a := range annotations {
		result = append(result, a.DuplicateCount)
	}
	return result
}

func failOpen(t *testing.T) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		t.Errorf("unnecessary full-file read of %s", path)
		return nil, errors.New("unexpected read")
	}
}

func TestIdenticalSkinExportsAndRenamedCopyShareOneKey(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("Default/001.owanimclip", nil), f.clip("Skin/001.owanimclip", nil), f.clip("Other Skin/reload.owanimclip", nil)}
	annotations, warnings := New(DefaultMaxEntries).Annotate(rows)
	if len(warnings) != 0 || len(keys(annotations)) != 1 || fmt.Sprint(counts(annotations)) != "[3 3 3]" {
		t.Fatalf("%v %v", annotations, warnings)
	}
	if !strings.HasPrefix(annotations[0].DuplicateKey, "sha256:") {
		t.Fatal(annotations[0].DuplicateKey)
	}
}

func TestSameNameSizeAndHeadersWithDifferentMotionAreDistinct(t *testing.T) {
	f := newFixture(t)
	contents := clipBytes(31)
	contents[len(contents)-1] ^= 1
	rows := []Clip{f.clip("Default/001.owanimclip", nil), f.clip("Skin/001.owanimclip", contents)}
	annotations, warnings := New(DefaultMaxEntries).Annotate(rows)
	if len(warnings) != 0 || annotations[0].DuplicateKey == annotations[1].DuplicateKey || fmt.Sprint(counts(annotations)) != "[1 1]" {
		t.Fatalf("%v", annotations)
	}
}

func TestUniqueSizeOrHeaderDoesNotRequireFullFileRead(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", clipBytes(32)),
		f.clip("003.owanimclip", append(clipBytes(31), "padding"...))}
	index := New(DefaultMaxEntries)
	index.open = failOpen(t)
	annotations, warnings := index.Annotate(rows)
	if len(warnings) != 0 || len(keys(annotations)) != 3 {
		t.Fatalf("%v", annotations)
	}
}

func TestRepeatedScanUsesCacheAndSameSizeModificationInvalidates(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil)}
	index := New(DefaultMaxEntries)
	index.Annotate(rows)
	index.open = failOpen(t)
	if _, warnings := index.Annotate(rows); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	index.open = nil
	info, _ := os.Stat(rows[1].Path)
	contents, _ := os.ReadFile(rows[1].Path)
	contents[len(contents)-1] ^= 1
	os.WriteFile(rows[1].Path, contents, 0o644)
	later := info.ModTime().Add(time.Second)
	os.Chtimes(rows[1].Path, later, later)
	annotations, warnings := index.Annotate(rows)
	if len(warnings) != 0 || annotations[0].DuplicateKey == annotations[1].DuplicateKey || fmt.Sprint(counts(annotations)) != "[1 1]" {
		t.Fatalf("%v", annotations)
	}
}

func TestUnreadableFileIsRetainedAsUniqueWithoutAborting(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil), f.clip("003.owanimclip", nil)}
	index := New(DefaultMaxEntries)
	index.open = func(path string) (io.ReadCloser, error) {
		if path == rows[1].Path {
			return nil, errors.New("Access denied")
		}
		return os.Open(path)
	}
	annotations, warnings := index.Annotate(rows)
	if len(warnings) != 1 || warnings[0] != "Could not check duplicates for "+rows[1].Path+": Access denied" {
		t.Fatal(warnings)
	}
	if !strings.Contains(annotations[1].DuplicateError, "Access denied") || annotations[1].DuplicateCount != 1 ||
		annotations[1].DuplicateKey == annotations[0].DuplicateKey || annotations[0].DuplicateKey != annotations[2].DuplicateKey ||
		annotations[0].DuplicateCount != 2 {
		t.Fatalf("%+v", annotations)
	}
	// Access can recover on a later scan; failures are not cached.
	index.open = nil
	annotations, warnings = index.Annotate(rows)
	if len(warnings) != 0 || fmt.Sprint(counts(annotations)) != "[3 3 3]" || annotations[1].DuplicateError != "" {
		t.Fatalf("%+v", annotations)
	}
	if data, _ := json.Marshal(annotations[1]); strings.Contains(string(data), "duplicateError") {
		t.Fatal(string(data))
	}
}

func TestInvalidHeadersAreNotHiddenTogether(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil)}
	for i := range rows {
		rows[i].Error = "Truncated clip"
	}
	index := New(DefaultMaxEntries)
	index.open = failOpen(t)
	annotations, _ := index.Annotate(rows)
	if annotations[0].DuplicateKey == annotations[1].DuplicateKey || fmt.Sprint(counts(annotations)) != "[1 1]" {
		t.Fatalf("%v", annotations)
	}
}

func TestChangedSizeSinceHeaderScanIsNotGrouped(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil)}
	os.WriteFile(rows[1].Path, append(clipBytes(31), "changed"...), 0o644)
	annotations, warnings := New(DefaultMaxEntries).Annotate(rows)
	if len(warnings) != 1 || annotations[0].DuplicateKey == annotations[1].DuplicateKey || !strings.Contains(annotations[1].DuplicateError, "changed") {
		t.Fatalf("%v %v", annotations, warnings)
	}
}

func TestCacheIsBoundedAndEvictionDoesNotChangeResults(t *testing.T) {
	f := newFixture(t)
	index := New(2)
	var rows []Clip
	for number := 0; number < 4; number++ {
		rows = append(rows, f.clip(fmt.Sprintf("%03d.owanimclip", number), nil))
	}
	index.Annotate(rows)
	if index.Len() > 2 {
		t.Fatal(index.Len())
	}
	annotations, _ := index.Annotate(rows)
	if index.Len() > 2 || fmt.Sprint(counts(annotations)) != "[4 4 4 4]" {
		t.Fatalf("%d %v", index.Len(), annotations)
	}
}

type changingFile struct {
	*os.File
	path string
}

func (c changingFile) Close() error {
	err := c.File.Close()
	os.WriteFile(c.path, append(clipBytes(31), "changed"...), 0o644)
	return err
}

func TestFileModifiedDuringHashDoesNotLeaveACachedIdentity(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil)}
	index := New(DefaultMaxEntries)
	index.open = func(path string) (io.ReadCloser, error) {
		file, err := os.Open(path)
		if err != nil || path != rows[1].Path {
			return file, err
		}
		return changingFile{file, path}, nil
	}
	annotations, warnings := index.Annotate(rows)
	if len(warnings) != 1 || annotations[0].DuplicateKey == annotations[1].DuplicateKey {
		t.Fatalf("%v %v", annotations, warnings)
	}
	index.mu.Lock()
	_, cached := index.cache[pathKey(rows[1].Path)]
	index.mu.Unlock()
	if cached {
		t.Fatal("changed file cached")
	}
}

func TestMissingFileMessageMatchesPython(t *testing.T) {
	f := newFixture(t)
	rows := []Clip{f.clip("001.owanimclip", nil), f.clip("002.owanimclip", nil)}
	os.Remove(rows[1].Path)
	_, warnings := New(DefaultMaxEntries).Annotate(rows)
	want := fmt.Sprintf("Could not check duplicates for %s: [Errno 2] No such file or directory: '%s'", rows[1].Path, rows[1].Path)
	if len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("%q\nwant %q", warnings, want)
	}
}

// Compares annotations and warnings with Python's DuplicateIndex on a mixed
// library (identical groups across headers, a changed size, a missing file,
// header errors and unique rows), with more candidates than workers.
func TestAnnotateMatchesPython(t *testing.T) {
	f := newFixture(t)
	var rows []Clip
	for i := 0; i < 40; i++ {
		content := clipBytes(31 + uint32(i%3))
		if i%5 == 0 {
			content = append(content, byte(i))
		}
		if i%7 == 0 {
			content[len(content)-2] ^= 0xff
		}
		rows = append(rows, f.clip(fmt.Sprintf("set%d/%03d.owanimclip", i%4, i), content))
	}
	rows[3].Error = "Truncated clip"
	os.WriteFile(rows[8].Path, append(clipBytes(31), "grown"...), 0o644)
	os.Remove(rows[9].Path)
	type pyRow struct {
		Path           string  `json:"path"`
		Size           int64   `json:"size"`
		Version        string  `json:"version"`
		FPS            float64 `json:"fps"`
		DurationFrames int64   `json:"durationFrames"`
		BoneCount      int64   `json:"boneCount"`
		Error          string  `json:"error,omitempty"`
	}
	var input []pyRow
	for _, row := range rows {
		input = append(input, pyRow(row))
	}
	result := oracle.Exec(t, `
from owpreview.duplicates import DuplicateIndex
rows = args[0]
warnings = DuplicateIndex().annotate(rows)
result = {'warnings': warnings, 'rows': [{k: r[k] for k in ('duplicateKey', 'duplicateCount', 'duplicateError') if k in r} for r in rows]}`, input)
	var want struct {
		Warnings []string     `json:"warnings"`
		Rows     []Annotation `json:"rows"`
	}
	if err := json.Unmarshal(result.MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	annotations, warnings := New(DefaultMaxEntries).Annotate(rows)
	if fmt.Sprint(warnings) != fmt.Sprint(want.Warnings) {
		t.Errorf("warnings\nGo     %q\nPython %q", warnings, want.Warnings)
	}
	for i := range rows {
		if annotations[i] != want.Rows[i] {
			t.Errorf("row %d: Go %+v, Python %+v", i, annotations[i], want.Rows[i])
		}
	}
}

func BenchmarkAnnotate(b *testing.B) {
	root := b.TempDir()
	var rows []Clip
	content := make([]byte, 4<<20)
	for i := 0; i < 16; i++ {
		path := filepath.Join(root, fmt.Sprintf("%02d.owanimclip", i))
		os.WriteFile(path, content, 0o644)
		rows = append(rows, Clip{Path: path, Size: int64(len(content)), Version: "2.0", FPS: 30, DurationFrames: 31, BoneCount: 1})
	}
	b.SetBytes(int64(len(content) * len(rows)))
	for b.Loop() {
		New(0).Annotate(rows)
	}
}
