package clipbindings

// Port of tests/test_clip_bindings.py. The read_clip_binding_metadata
// checks (exact names, seeking over key arrays, structural corruption) are
// repeated here through the index; the reader itself is formats'.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/oracle"
)

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t, root}
}

func (f *fixture) write(name string, data []byte) string { return ft.Write(f.t, f.root, name, data) }

func clip(duration uint32, bones ...ft.ClipBone) []byte {
	c := ft.NewClip()
	c.Bones, c.Duration = bones, duration
	if bones == nil {
		c.Bones = []ft.ClipBone{}
	}
	return c.Bytes()
}

func bone(name string, tracks ...ft.Track) ft.ClipBone {
	return ft.ClipBone{Name: name, Tracks: tracks}
}

func names(t *testing.T, x *Index, path string) []string {
	t.Helper()
	result, err := x.Read([]any{path})
	if err != nil {
		t.Fatal(err)
	}
	return result.Clips[0].BoneNames
}

func counting(x *Index) *int {
	calls := 0
	x.readMetadata = func(path string) (*formats.ClipBindingMetadata, error) {
		calls++
		return formats.ReadClipBindingMetadata(path)
	}
	return &calls
}

func TestExactArbitraryNamesAndEmptyRecordsMatchFullParser(t *testing.T) {
	f := newFixture(t)
	list := []string{"bone_0075", "bone_004A", "custom.Root 日本語", strings.Repeat("a", 128), "cloth_0002"}
	var bones []ft.ClipBone
	for i, name := range list {
		if i%2 == 1 {
			bones = append(bones, bone(name))
		} else {
			bones = append(bones, bone(name, ft.Track{Kind: 0, Keys: []ft.Key{ft.K(0, 0, 0, 0)}}))
		}
	}
	source := f.write("Names 日本語.owanimclip", clip(31, bones...))
	if got := names(t, NewDefault(), source); !reflect.DeepEqual(got, list) {
		t.Fatal(got)
	}
}

func TestSuccessfulEmptyClipIsNotAParseError(t *testing.T) {
	f := newFixture(t)
	source := f.write("Empty.owanimclip", clip(1))
	result, err := NewDefault().Read([]any{source})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	want := `{"clips":[{"path":` + string(must(json.Marshal(source))) + `,"boneNames":[]}]}`
	if string(data) != want {
		t.Fatalf("%s != %s", data, want)
	}
}

func TestKeyArraysAreSoughtOverWithoutDecoding(t *testing.T) {
	f := newFixture(t)
	keys := make([]ft.Key, 5000)
	for i := range keys {
		keys[i] = ft.K(uint32(i), 1, 2, 3)
	}
	source := f.write("Many keys.owanimclip", clip(5000, bone("bone_0001", ft.Track{Kind: 0, Keys: keys}),
		bone("bone_0002", ft.Track{Kind: 0, Keys: keys})))
	if got := names(t, NewDefault(), source); !reflect.DeepEqual(got, []string{"bone_0001", "bone_0002"}) {
		t.Fatal(got)
	}
}

func TestUnknownTracksAndTrailingExtensionsPreserveBindingNames(t *testing.T) {
	f := newFixture(t)
	source := f.write("Extensions.owanimclip", append(clip(31, bone("raw_foo", ft.Track{Kind: 7, Keys: []ft.Key{ft.K(2, 1, 2)}})), "extra"...))
	full, err := formats.LoadClip(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(t, NewDefault(), source); len(got) != 1 || got[0] != full.Bones[0].Name {
		t.Fatal(got)
	}
}

func corruptCases() map[string][]byte {
	header := ft.Pack("HHIfI", 2, 0, 1, 30, 1)
	withBone := append(append([]byte{}, header...), ft.String("bone_0001")...)
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	return map[string][]byte{
		"truncated-header":         []byte("bad"),
		"bad-version":              ft.Pack("HHIfI", 1, 0, 0, 30, 1),
		"invalid-fps":              ft.Pack("HHIfI", 2, 0, 0, math.NaN(), 1),
		"invalid-duration":         ft.Pack("HHIfI", 2, 0, 0, 30, 0),
		"too-many-bones":           ft.Pack("HHIfI", 2, 0, 65537, 30, 1),
		"no-bone-record":           header,
		"truncated-string":         cat(header, []byte("\x05abc")),
		"invalid-utf8":             cat(header, []byte{0x01, 0xff}, ft.Pack("I", 0)),
		"invalid-string-length":    cat(header, []byte{0xff, 0xff, 0xff, 0xff, 0xff}),
		"empty-bone-name":          cat(header, ft.String(""), ft.Pack("I", 0)),
		"duplicate-bone":           clip(31, bone("same"), bone("same")),
		"too-many-channels":        cat(withBone, ft.Pack("I", 17)),
		"truncated-channel-header": cat(withBone, ft.Pack("I", 1), []byte("abcd")),
		"wrong-components":         cat(withBone, ft.Pack("IIII", 1, 1, 0, 3)),
		"too-many-components":      cat(withBone, ft.Pack("IIII", 1, 9, 0, 17)),
		"too-many-keys":            cat(withBone, ft.Pack("IIII", 1, 0, 10000001, 3)),
		"truncated-keys":           cat(withBone, ft.Pack("IIII", 1, 0, 2, 3), ft.Pack("I3f", 0, 0, 0, 0)),
		"truncated-unknown-keys":   cat(withBone, ft.Pack("IIII", 1, 9, 2, 4)),
		"duplicate-channel":        clip(31, bone("same", ft.Track{Kind: 0}, ft.Track{Kind: 0})),
	}
}

func TestStructuralCorruptionIsAnErrorNotZeroMatches(t *testing.T) {
	f := newFixture(t)
	x := NewDefault()
	for label, data := range corruptCases() {
		source := f.write(label+".owanimclip", data)
		var formatError *formats.FormatError
		if _, err := formats.ReadClipBindingMetadata(source); !errors.As(err, &formatError) {
			t.Errorf("%s: %v", label, err)
		}
		result, err := x.Read([]any{source})
		if err != nil {
			t.Fatal(err)
		}
		clip := result.Clips[0]
		if clip.BoneNames != nil || clip.Error == "" || clip.Path == nil || *clip.Path != source {
			t.Errorf("%s: %+v", label, clip)
		}
	}
}

func TestCacheReusesNamesButReturnsIndependentLists(t *testing.T) {
	f := newFixture(t)
	source := f.write("Cached.owanimclip", clip(31, bone("first"), bone("second")))
	x := NewDefault()
	calls := counting(x)
	result, _ := x.Read([]any{source, source})
	if *calls != 1 {
		t.Fatal(*calls)
	}
	result.Clips[0].BoneNames[0] = "mutated"
	result.Clips[0].BoneNames = append(result.Clips[0].BoneNames, "mutated")
	if !reflect.DeepEqual(result.Clips[1].BoneNames, []string{"first", "second"}) {
		t.Fatal(result.Clips[1].BoneNames)
	}
	if got := names(t, x, source); !reflect.DeepEqual(got, []string{"first", "second"}) || *calls != 1 {
		t.Fatal(got, *calls)
	}
}

func TestChangedFileInvalidatesCacheIncludingReplacementWithSameMtimeSize(t *testing.T) {
	f := newFixture(t)
	source := f.write("Changed.owanimclip", clip(31, bone("first")))
	x := NewDefault()
	if got := names(t, x, source); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatal(got)
	}
	old, _ := os.Stat(source)
	if err := os.WriteFile(source, clip(31, bone("other")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, time.Now(), old.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := names(t, x, source); !reflect.DeepEqual(got, []string{"other"}) {
		t.Fatal(got)
	}
	old, _ = os.Stat(source)
	replacement := f.write("Replacement.owanimclip", clip(31, bone("third")))
	if err := os.Chtimes(replacement, time.Now(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatal(err)
	}
	now, _ := os.Stat(source)
	if now.Size() != old.Size() || !now.ModTime().Equal(old.ModTime()) {
		t.Fatal("fixture did not keep size and mtime")
	}
	if got := names(t, x, source); !reflect.DeepEqual(got, []string{"third"}) {
		t.Fatal(got)
	}
}

func TestReadDuringChangeReturnsUnknownAndDoesNotCacheStaleNames(t *testing.T) {
	f := newFixture(t)
	source := f.write("Race.owanimclip", clip(31, bone("first")))
	x := NewDefault()
	x.readMetadata = func(path string) (*formats.ClipBindingMetadata, error) {
		result, err := formats.ReadClipBindingMetadata(path)
		if err := os.WriteFile(source, clip(31, bone("longer replacement")), 0o644); err != nil {
			t.Fatal(err)
		}
		return result, err
	}
	result, _ := x.Read([]any{source})
	if result.Clips[0].BoneNames != nil || !strings.Contains(result.Clips[0].Error, "changed") {
		t.Fatalf("%+v", result.Clips[0])
	}
	x.readMetadata = formats.ReadClipBindingMetadata
	if got := names(t, x, source); !reflect.DeepEqual(got, []string{"longer replacement"}) {
		t.Fatal(got)
	}
}

func TestFailuresDoNotPoisonCacheAndMixedRequestsKeepOrder(t *testing.T) {
	f := newFixture(t)
	source := f.write("Good.owanimclip", clip(31, bone("bone_A")))
	missing := filepath.Join(f.root, "Missing.owanimclip")
	wrongType := f.write("File.txt", []byte("not a clip"))
	x := NewDefault()
	requested := []any{source, missing, wrongType, "relative.owanimclip", nil, 42.0}
	result, err := x.Read(requested)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Clips[0].BoneNames, []string{"bone_A"}) {
		t.Fatal(result.Clips[0])
	}
	for i, clip := range result.Clips {
		if i < 4 && (clip.Path == nil || *clip.Path != requested[i]) || i >= 4 && clip.Path != nil {
			t.Errorf("%d: path %v", i, clip.Path)
		}
		if i > 0 && (clip.BoneNames != nil || clip.Error == "") {
			t.Errorf("%d: %+v", i, clip)
		}
	}
	if err := os.WriteFile(missing, clip(31, bone("bone_B")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := names(t, x, missing); !reflect.DeepEqual(got, []string{"bone_B"}) {
		t.Fatal(got)
	}
}

func TestCacheHasLRUCountAndMemoryBounds(t *testing.T) {
	f := newFixture(t)
	var sources []any
	for n := 0; n < 3; n++ {
		sources = append(sources, f.write(fmt.Sprintf("%d.owanimclip", n), clip(31, bone(fmt.Sprintf("bone_%d", n)))))
	}
	x := New(2, DefaultMaxBytes)
	calls := counting(x)
	read := func(paths ...any) {
		if _, err := x.Read(paths); err != nil {
			t.Fatal(err)
		}
	}
	read(sources[0], sources[1])
	read(sources[0], sources[2])
	if *calls != 3 {
		t.Fatal(*calls)
	}
	read(sources[0])
	if *calls != 3 {
		t.Fatal(*calls)
	}
	read(sources[1])
	if *calls != 4 {
		t.Fatal(*calls)
	}
	for _, x := range []*Index{New(0, DefaultMaxBytes), New(DefaultMaxEntries, 1)} {
		calls := counting(x)
		if _, err := x.Read([]any{sources[0], sources[0]}); err != nil {
			t.Fatal(err)
		}
		if *calls != 2 || x.Len() != 0 {
			t.Fatal(*calls, x.Len())
		}
	}
}

func TestBatchBoundAndEmptyRequest(t *testing.T) {
	x := NewDefault()
	result, err := x.Read([]any{})
	if err != nil || len(result.Clips) != 0 {
		t.Fatal(result, err)
	}
	data, _ := json.Marshal(result)
	if string(data) != `{"clips":[]}` {
		t.Fatal(string(data))
	}
	tooMany := make([]any, 129)
	for i := range tooMany {
		tooMany[i] = "missing"
	}
	for _, paths := range []any{nil, "path.owanimclip", map[string]any{}, tooMany} {
		if _, err := x.Read(paths); err == nil || !strings.Contains(err.Error(), "128") {
			t.Errorf("%T: %v", paths, err)
		}
	}
	batch := make([]any, 128)
	for i := range batch {
		batch[i] = "relative.owanimclip"
	}
	if result, err := x.Read(batch); err != nil || len(result.Clips) != 128 {
		t.Fatal(err)
	}
}

func TestConcurrentReadsAreSafe(t *testing.T) {
	f := newFixture(t)
	var sources []any
	for n := 0; n < 20; n++ {
		sources = append(sources, f.write(fmt.Sprintf("%d.owanimclip", n), clip(31, bone(fmt.Sprintf("bone_%d", n)))))
	}
	x := New(8, DefaultMaxBytes)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 20; i++ {
				result, err := x.Read(sources)
				if err != nil {
					t.Error(err)
					return
				}
				for n, c := range result.Clips {
					if len(c.BoneNames) != 1 || c.BoneNames[0] != fmt.Sprintf("bone_%d", n) {
						t.Errorf("%d: %+v", n, c)
					}
				}
			}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	if x.Len() > 8 {
		t.Fatal(x.Len())
	}
}

const readPython = `
from owpreview.clip_bindings import ClipBindingIndex
result = ClipBindingIndex().read(args[0])
`

func TestReadMatchesPython(t *testing.T) {
	f := newFixture(t)
	requested := []any{f.write("Good.owanimclip", ft.NewClip().Bytes()), f.write("Empty.owanimclip", clip(1)),
		filepath.Join(f.root, "Missing.owanimclip"), f.write("File.txt", []byte("x")), "relative.owanimclip", nil, 42, ""}
	for label, data := range corruptCases() {
		requested = append(requested, f.write(label+".owanimclip", data))
	}
	for _, root := range []string{filepath.Join("..", "..", "..", "tests", "demo-assets"), os.Getenv("AVOLITION_BENCH_LIBRARY")} {
		if root == "" {
			continue
		}
		root, _ = filepath.Abs(root)
		count := 0
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(path, ".owanimclip") && count < 60 {
				requested = append(requested, path)
				count++
			}
			return nil
		})
	}
	for start := 0; start < len(requested); start += MaxBatch {
		batch := requested[start:min(len(requested), start+MaxBatch)]
		want := oracle.Exec(t, readPython, batch).MustValue(t)
		result, err := NewDefault().Read(batch)
		if err != nil {
			t.Fatal(err)
		}
		var wantResult, gotResult Result
		got, _ := json.Marshal(result)
		_ = json.Unmarshal(want, &wantResult)
		_ = json.Unmarshal(got, &gotResult)
		if !reflect.DeepEqual(wantResult, gotResult) {
			for i := range wantResult.Clips {
				if !reflect.DeepEqual(wantResult.Clips[i], gotResult.Clips[i]) {
					t.Errorf("%v: want %+v, got %+v", batch[i], wantResult.Clips[i], gotResult.Clips[i])
				}
			}
		}
	}
}

func must(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}
