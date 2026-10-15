package bonecounts

// Port of tests/test_bone_counts.py. test_scan_reuses_character_index_and_
// keeps_missing_counts_on_individual_rows drives BrowserAPI.scan_root and is
// ported with the API.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
)

type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture { return &fixture{t, t.TempDir()} }

func (f *fixture) write(name string, data []byte) string { return ft.Write(f.t, f.root, name, data) }

// rglob is list(root.rglob('*')): every file and directory below root.
func rglob(t testing.TB, root string) []string {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func (f *fixture) index() *Index { return New(f.root, rglob(f.t, f.root)) }

func entity(model string, relative string, children ...string) []byte {
	e := ft.NewEntity()
	e.Model = model
	if relative != "" {
		e.Relative = relative
	}
	for _, c := range children {
		e.Children = append(e.Children, ft.Child{Reference: c})
	}
	return e.Bytes()
}

func modelWithBones(bones []ft.Bone, slots int) []byte {
	m := ft.NewModel()
	m.Bones, m.Slots = bones, slots
	return m.Bytes()
}

func assertCount(t *testing.T, x *Index, path string, count int) {
	t.Helper()
	got := x.Metadata(path)
	if got.BoneCount == nil || *got.BoneCount != count || got.BoneCountError != "" {
		t.Fatalf("%s: want %d, got %+v", path, count, got)
	}
}

func assertUnavailable(t *testing.T, x *Index, path, message string) {
	t.Helper()
	got := x.Metadata(path)
	if got.BoneCount != nil || !strings.Contains(got.BoneCountError, message) {
		t.Fatalf("%s: want error containing %q, got %+v", path, message, got)
	}
}

func TestHeaderStopsBeforeBonesGeometryAndMaterials(t *testing.T) {
	f := newFixture(t)
	m := ft.NewModel()
	m.Name = strings.Repeat("Módèle 日本語", 15)
	blob := m.Bytes()
	path := f.write("Body.owmdl", blob)
	bones, consumed, err := readHeader(path)
	if err != nil || bones != 2 {
		t.Fatal(bones, err)
	}
	header := append(ft.Pack("HH", 2, 0), ft.String("")...)
	header = append(header, ft.String(m.Name)...)
	header = append(header, ft.Pack("IHII", 123, 2, 1, 0)...)
	if consumed != len(header) || consumed >= len(blob) {
		t.Fatalf("consumed %d of %d, header %d", consumed, len(blob), len(header))
	}
}

func TestRepeatedAttachmentInstancesCountAndCachedHeadersAreReused(t *testing.T) {
	f := newFixture(t)
	body := f.write("Body.owmdl", ft.NewModel().Bytes())
	weapon := f.write("Weapon.owmdl", modelWithBones([]ft.Bone{{Name: "root", Parent: -1}}, 5))
	child := f.write("Child.owentity", entity("Weapon.owmdl", "."))
	main := f.write("Gameplay3P.owentity", entity("Body.owmdl", ".", "Child.owentity", "Child.owentity"))
	for _, batch := range []bool{false, true} {
		x := f.index()
		calls := 0
		x.readHeader = func(path string) (int, error) {
			calls++
			return ReadModelBoneCount(path)
		}
		if batch {
			got := x.MetadataAll([]string{main, child, body, weapon})
			want := []int{4, 1, 2, 1}
			for i := range got {
				if got[i].BoneCount == nil || *got[i].BoneCount != want[i] {
					t.Fatalf("%d: %+v", i, got[i])
				}
			}
		} else {
			assertCount(t, x, main, 4)
			assertCount(t, x, child, 1)
			assertCount(t, x, body, 2)
			assertCount(t, x, weapon, 1)
		}
		if calls != 2 {
			t.Fatalf("batch=%v: %d header reads", batch, calls)
		}
	}
}

func TestNullModelWrappersAndZeroBoneModelsAreKnownZeroNotErrors(t *testing.T) {
	f := newFixture(t)
	model := f.write("Static.owmdl", modelWithBones([]ft.Bone{}, 0))
	empty := f.write("Empty.owentity", entity("null", ""))
	f.write("Child.owentity", entity("Body.owmdl", "."))
	f.write("Body.owmdl", ft.NewModel().Bytes())
	wrapper := f.write("Wrapper.owentity", entity("null", "", "Child.owentity"))
	x := f.index()
	assertCount(t, x, model, 0)
	assertCount(t, x, empty, 0)
	assertCount(t, x, wrapper, 2)
}

func TestMissingReferencesNeverReturnPartialTotal(t *testing.T) {
	f := newFixture(t)
	f.write("Body.owmdl", ft.NewModel().Bytes())
	missingModel := f.write("NoModel.owentity", entity("Absent.owmdl", ""))
	missingChild := f.write("NoChild.owentity", entity("Body.owmdl", ".", "Absent.owentity"))
	x := f.index()
	assertUnavailable(t, x, missingModel, "missing model")
	assertUnavailable(t, x, missingChild, "missing child entity")
	assertUnavailable(t, x, filepath.Join(f.root, "Absent.owmdl"), "Absent.owmdl")
}

func TestCycleCorruptAttachmentAndCorruptModelHeadersAreUnavailable(t *testing.T) {
	f := newFixture(t)
	f.write("Body.owmdl", ft.NewModel().Bytes())
	cycle := f.write("Cycle.owentity", entity("Body.owmdl", ".", "Cycle.owentity"))
	f.write("Broken.owentity", []byte("broken entity"))
	parent := f.write("Parent.owentity", entity("Body.owmdl", ".", "Broken.owentity"))
	assertUnavailable(t, f.index(), cycle, "cycle")
	assertUnavailable(t, f.index(), parent, "truncated")
	old := ft.NewModel()
	old.Version = [2]uint16{1, 0}
	for _, c := range []struct {
		name  string
		blob  []byte
		error string
	}{
		{"truncated", append(ft.Pack("HH", 2, 0), ft.String("")...), "truncated"},
		{"version", old.Bytes(), "unsupported model version"},
		{"utf8", append(ft.Pack("HH", 2, 0), 0x01, 0xff), "UTF-8"},
		{"meshes", append(append(append(ft.Pack("HH", 2, 0), ft.String("")...), ft.String("")...), ft.Pack("IHII", 1, 2, 20000, 0)...), "mesh count"},
	} {
		path := f.write(c.name+".owmdl", c.blob)
		assertUnavailable(t, f.index(), path, c.error)
	}
}

func TestLimitsAndFailedDeepRootDoNotPoisonValidSubtreeCounts(t *testing.T) {
	f := newFixture(t)
	f.write("Body.owmdl", ft.NewModel().Bytes())
	child := f.write("Child.owentity", entity("Body.owmdl", "."))
	children := make([]string, 512)
	for i := range children {
		children[i] = "Child.owentity"
	}
	broad := f.write("Broad.owentity", entity("Body.owmdl", ".", children...))
	for n := 0; n < 50; n++ {
		var next []string
		if n < 49 {
			next = []string{fmt.Sprintf("Depth%d.owentity", n+1)}
		}
		f.write(fmt.Sprintf("Depth%d.owentity", n), entity("Body.owmdl", ".", next...))
	}
	x := f.index()
	assertUnavailable(t, x, broad, "512 model nodes")
	assertUnavailable(t, x, filepath.Join(f.root, "Depth0.owentity"), "48 levels")
	assertCount(t, x, filepath.Join(f.root, "Depth10.owentity"), 80)
	assertCount(t, x, child, 2)
}

func TestAmbiguousReferenceDoesNotPickArbitrarySkeleton(t *testing.T) {
	f := newFixture(t)
	f.write("Elsewhere/A/Body.owmdl", ft.NewModel().Bytes())
	f.write("Elsewhere/B/Body.owmdl", modelWithBones([]ft.Bone{}, 0))
	main := f.write("Copied/Main.owentity", entity("Body.owmdl", ""))
	assertUnavailable(t, f.index(), main, "Ambiguous reference")
}

func TestRootWalkFallbackWithoutScannedPaths(t *testing.T) {
	f := newFixture(t)
	f.write("Nested/Elsewhere/Body.owmdl", ft.NewModel().Bytes())
	main := f.write("Copied/Gameplay3P.owentity", entity("Body.owmdl", ""))
	x := New(f.root, nil)
	got := x.MetadataAll([]string{main})
	if got[0].BoneCount == nil || *got[0].BoneCount != 2 {
		t.Fatalf("%+v", got[0])
	}
	assertUnavailable(t, New("", nil), main, "missing model")
}

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
}

func TestGeneratedMaterialFixtureCountsMatchRealLoadedAssetStats(t *testing.T) {
	root, _ := filepath.Abs(repoPath("tests", "material-assets"))
	x := New(root, rglob(t, root))
	checked := 0
	for _, path := range rglob(t, root) {
		suffix := strings.ToLower(filepath.Ext(path))
		if suffix != ".owmdl" && suffix != ".owentity" {
			continue
		}
		asset, err := formats.LoadAsset(path, root)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, model := range asset.Models {
			total += len(model.Bones)
		}
		assertCount(t, x, path, total)
		checked++
	}
	if checked == 0 {
		t.Fatal("no fixtures")
	}
}

const bonePython = `
from pathlib import Path
from owpreview.bone_counts import BoneCountIndex
root = Path(args[0])
paths = sorted(str(p) for p in root.rglob('*') if p.suffix.lower() in ('.owmdl', '.owentity'))
index = BoneCountIndex(root, root.rglob('*'))
result = [[p, index.metadata(p)] for p in paths]
`

func libraries() []string {
	roots := []string{repoPath("tests", "demo-assets"), repoPath("tests", "material-assets")}
	if lib := os.Getenv("AVOLITION_BENCH_LIBRARY"); lib != "" {
		roots = append(roots, lib)
	}
	return roots
}

func TestMetadataMatchesPython(t *testing.T) {
	// A synthetic library with every failure mode, plus the real fixtures.
	f := newFixture(t)
	f.write("Body.owmdl", ft.NewModel().Bytes())
	f.write("Weapon.owmdl", modelWithBones([]ft.Bone{{Name: "root", Parent: -1}}, 5))
	f.write("Child.owentity", entity("Weapon.owmdl", "."))
	f.write("Main.owentity", entity("Body.owmdl", ".", "Child.owentity", "Child.owentity", ""))
	f.write("Cycle.owentity", entity("Body.owmdl", ".", "Cycle.owentity"))
	f.write("Broken.owentity", []byte("broken"))
	f.write("Parent.owentity", entity("Body.owmdl", ".", "Broken.owentity"))
	f.write("NoModel.owentity", entity("Absent.owmdl", ""))
	f.write("Broken.owmdl", []byte("broken"))
	f.write("A/Dup.owmdl", ft.NewModel().Bytes())
	f.write("B/Dup.owmdl", ft.NewModel().Bytes())
	f.write("Ambiguous.owentity", entity("Dup.owmdl", ""))
	f.write("Null.owentity", entity("null", "", "Main.owentity"))
	for _, root := range append([]string{f.root}, libraries()...) {
		root, _ = filepath.Abs(root)
		var want [][2]json.RawMessage
		if err := json.Unmarshal(oracle.Exec(t, bonePython, root).MustValue(t), &want); err != nil {
			t.Fatal(err)
		}
		paths := make([]string, len(want))
		for i, w := range want {
			_ = json.Unmarshal(w[0], &paths[i])
		}
		sort.Strings(paths)
		got := New(root, rglob(t, root)).MetadataAll(paths)
		for i, w := range want {
			data, _ := json.Marshal(got[i])
			if diffs, err := jsoncmp.Diff(w[1], data, jsoncmp.Options{}); err != nil || len(diffs) > 0 {
				t.Errorf("%s: want %s, got %s", paths[i], w[1], data)
			}
		}
		t.Logf("%s: %d rows", root, len(want))
	}
}

func BenchmarkMetadataAll(b *testing.B) {
	root, _ := filepath.Abs(libraries()[len(libraries())-1])
	all := rglob(b, root)
	var rows []string
	for _, p := range all {
		if s := strings.ToLower(filepath.Ext(p)); s == ".owmdl" || s == ".owentity" {
			rows = append(rows, p)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		New(root, rows).MetadataAll(rows)
	}
}
