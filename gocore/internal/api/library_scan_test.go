package api

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/mapformats/maptest"
)

// API parts of tests/test_library_scan.py, test_duplicates.py and
// test_bone_counts.py.

type scanFixture struct {
	root, library string
	api           *API
}

func newScanFixture(t *testing.T) *scanFixture {
	isolate(t)
	f := &scanFixture{root: tempDir(t)}
	f.library = filepath.Join(f.root, "library")
	if err := os.MkdirAll(f.library, 0o755); err != nil {
		t.Fatal(err)
	}
	f.api = newAPI(t, Options{DataDir: filepath.Join(f.root, "profile")})
	return f
}

func (f *scanFixture) write(t *testing.T, relative string, content []byte) string {
	if content == nil {
		content = []byte("model fixture")
	}
	return writeFile(t, filepath.Join(f.library, relative), content)
}

func (f *scanFixture) character(t *testing.T, directory string) []string {
	return []string{f.write(t, directory+"/Entities/Gameplay3P.owentity", nil), f.write(t, directory+"/Models/Body.owmdl", nil),
		f.write(t, directory+"/Models/Weapon.owmdl", nil), f.write(t, directory+"/Models/Body/Animations/Idle.owanimclip", formatstest.NewClip().Bytes())}
}

func (f *scanFixture) mapExport(t *testing.T, directory string) string {
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	kings := maptest.NewMap()
	kings.Name = "Kings Row"
	f.write(t, prefix+"Evening.OWMAP", kings.Bytes())
	f.write(t, prefix+"Models/Building/Building.owmdl", nil)
	f.write(t, prefix+"Entities/Prop.owentity", nil)
	f.write(t, prefix+"Effects/Detail.owmdl", nil)
	// Invalid map-only clips must be pruned before header/duplicate parsing.
	f.write(t, prefix+"Models/Building/Animations/Door.owanimclip", []byte("map clip"))
	return filepath.Join(f.library, directory)
}

func (f *scanFixture) scan(t *testing.T, root string) map[string]any {
	t.Helper()
	if root == "" {
		root = f.library
	}
	return okMap(t, call(t, f.api, "scan_root", root))
}

func rowPaths(rows any) []string {
	var out []string
	for _, row := range l(rows) {
		out = append(out, m(row)["path"].(string))
	}
	sort.Strings(out)
	return out
}

func sorted(values ...string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func TestSharedLibraryExcludesMapDependenciesAndKeepsCharacterAssets(t *testing.T) {
	f := newScanFixture(t)
	character := f.character(t, "Heroes/Widow")
	f.mapExport(t, "Maps/Kings Row/6B1")
	f.mapExport(t, "Renamed environment archive/AB2")
	data := f.scan(t, "")
	assertJSON(t, rowPaths(data["entities"]), sorted(character[:3]...))
	assertJSON(t, rowPaths(data["clips"]), character[3:])
	if data["uniqueClipCount"] != float64(1) || data["duplicateClipCount"] != float64(0) || len(l(data["warnings"])) != 0 {
		t.Fatal(data)
	}
	if len(l(okMap(t, call(t, f.api, "scan_maps", f.library))["maps"])) != 2 {
		t.Fatal("maps")
	}
}

func TestMapAssetsArePrunedBeforeWalking(t *testing.T) {
	f := newScanFixture(t)
	mapRoot := f.mapExport(t, "Maps/Kings Row/6B1")
	// An unreadable map branch would produce a warning if it were walked.
	blocked := filepath.Join(mapRoot, "Models", "Blocked")
	if err := os.MkdirAll(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o755) })
	f.character(t, "Heroes/Widow")
	data := f.scan(t, "")
	if len(l(data["warnings"])) != 0 || len(l(data["clips"])) != 1 {
		t.Fatal(data)
	}
}

func TestDirectMapContainerBuildAndNestedModelScansStayEmpty(t *testing.T) {
	f := newScanFixture(t)
	mapRoot := f.mapExport(t, "Maps/Kings Row/6B1")
	for _, folder := range []string{filepath.Join(f.library, "Maps"), mapRoot, filepath.Join(mapRoot, "Models"),
		filepath.Join(mapRoot, "Models/Building"), filepath.Join(mapRoot, "Models/Building/Animations"),
		filepath.Join(mapRoot, "Entities"), filepath.Join(mapRoot, "Effects")} {
		data := f.scan(t, folder)
		if len(l(data["entities"])) != 0 || len(l(data["clips"])) != 0 || len(l(data["warnings"])) != 0 {
			t.Fatalf("%s: %v", folder, data)
		}
	}
}

func TestLooseMapPreservesUnrelatedCharacterSiblings(t *testing.T) {
	f := newScanFixture(t)
	f.mapExport(t, "")
	character := f.character(t, "Heroes/Widow")
	other := f.character(t, "Custom characters/Máp Hero")
	f.write(t, "loose-prop.owmdl", nil)
	data := f.scan(t, "")
	assertJSON(t, rowPaths(data["entities"]), sorted(append(append([]string{}, character[:3]...), other[:3]...)...))
	assertJSON(t, rowPaths(data["clips"]), sorted(character[3], other[3]))
}

func TestCaseInsensitiveMarkersWorkWithoutMapsFolderName(t *testing.T) {
	f := newScanFixture(t)
	f.write(t, "Environment/Night.oWmAp", maptest.NewMap().Bytes())
	model := f.write(t, "Environment/mOdElS/Prop.owmdl", nil)
	f.write(t, "Environment/eNtItIeS/Prop.owentity", nil)
	if len(l(f.scan(t, "")["entities"])) != 0 || len(l(f.scan(t, filepath.Dir(model))["entities"])) != 0 {
		t.Fatal("map dependencies listed")
	}
}

func TestMapsNameAloneDoesNotHideCharacterAssets(t *testing.T) {
	f := newScanFixture(t)
	character := f.character(t, "Maps")
	data := f.scan(t, "")
	assertJSON(t, rowPaths(data["entities"]), sorted(character[:3]...))
	assertJSON(t, rowPaths(data["clips"]), character[3:])
	if len(l(f.scan(t, filepath.Join(f.library, "Maps/Models"))["entities"])) != 2 {
		t.Fatal("models")
	}
}

func TestDirectoryNamedLikeMapIsNotMarker(t *testing.T) {
	f := newScanFixture(t)
	f.character(t, "Custom")
	if err := os.MkdirAll(filepath.Join(f.library, "Custom/Folder.owmap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(l(f.scan(t, "")["entities"])) != 3 {
		t.Fatal("entities hidden")
	}
}

func TestGameplayEntitiesLeadScan(t *testing.T) {
	f := newScanFixture(t)
	order := []string{"Zed/Gameplay1P.owentity", "Alpha/GAMEPLAY1P.OWENTITY", "Alpha/Gameplay3P.owentity", "Zed/Gameplay3P.owentity",
		"Zed/Other.owentity", "Alpha/Other.owentity", "Alpha/Models/Gameplay1P.owmdl", "Alpha/Models/A_model.owmdl", "Zed/Models/Z_model.owmdl"}
	for _, name := range order {
		f.write(t, name, nil)
	}
	var actual []string
	for _, row := range l(f.scan(t, "")["entities"]) {
		actual = append(actual, strings.ReplaceAll(m(row)["relativePath"].(string), `\`, "/"))
	}
	assertJSON(t, actual, []string{order[1], order[0], order[2], order[3], order[5], order[4], order[7], order[6], order[8]})
}

func TestSourceFileSizesAndBoneCountsWithoutLoadingGeometry(t *testing.T) {
	f := newScanFixture(t)
	modelData := formatstest.NewModel().Bytes()
	e := formatstest.NewEntity()
	e.Model, e.Relative = "Body.owmdl", "."
	entityData := e.Bytes()
	model := f.write(t, "Hero/Body.owmdl", modelData)
	entity := f.write(t, "Hero/Gameplay3P.owentity", entityData)
	f.mapExport(t, "Maps/Kings Row/6B1")
	f.api.deps.LoadAsset = nil // a scan must never load geometry
	rows := map[string]map[string]any{}
	for _, row := range l(f.scan(t, "")["entities"]) {
		rows[m(row)["path"].(string)] = m(row)
	}
	if len(rows) != 2 || rows[entity]["size"] != float64(len(entityData)) || rows[model]["size"] != float64(len(modelData)) ||
		rows[entity]["boneCount"] != float64(2) {
		t.Fatal(rows)
	}
	if _, has := rows[entity]["sizeError"]; has {
		t.Fatal("sizeError")
	}
}

func TestScanReportsDuplicateCountsAndKeepsEachPathsAnnotation(t *testing.T) {
	isolate(t)
	root := tempDir(t)
	data := formatstest.NewClip().Bytes()
	first := writeFile(t, filepath.Join(root, "Assets/Default/001.owanimclip"), data)
	second := writeFile(t, filepath.Join(root, "Assets/Skin/001.owanimclip"), data)
	third := writeFile(t, filepath.Join(root, "Assets/Default/002.owanimclip"), clipWithDuration(32))
	a := newAPI(t, Options{DataDir: filepath.Join(root, "state")})
	ok(t, call(t, a, "save_annotation", second, map[string]any{"name": "Reload"}))
	scan := okMap(t, call(t, a, "scan_root", filepath.Join(root, "Assets")))
	if len(l(scan["clips"])) != 3 || scan["uniqueClipCount"] != float64(2) || scan["duplicateClipCount"] != float64(1) {
		t.Fatal(scan)
	}
	byPath := map[string]map[string]any{}
	for _, row := range l(scan["clips"]) {
		byPath[m(row)["path"].(string)] = m(row)
	}
	if m(byPath[second]["annotation"])["name"] != "Reload" || m(byPath[first]["annotation"])["name"] != "" ||
		byPath[first]["duplicateCount"] != float64(2) || byPath[third]["duplicateCount"] != float64(1) {
		t.Fatal(byPath)
	}
}

func TestScanReusesCharacterIndexAndKeepsMissingCountsOnRows(t *testing.T) {
	isolate(t)
	root := tempDir(t)
	model := writeFile(t, filepath.Join(root, "Nested/Elsewhere/Body.owmdl"), formatstest.NewModel().Bytes())
	e := formatstest.NewEntity()
	e.Model = "Body.owmdl"
	main := writeFile(t, filepath.Join(root, "Copied/Gameplay3P.owentity"), e.Bytes())
	broken := writeFile(t, filepath.Join(root, "Broken.owmdl"), []byte("broken"))
	a := newAPI(t, Options{DataDir: filepath.Join(root, "Profile")})
	a.deps.LoadAsset = nil
	data := okMap(t, call(t, a, "scan_root", root))
	if len(l(data["warnings"])) != 0 {
		t.Fatal(data["warnings"])
	}
	rows := map[string]map[string]any{}
	for _, row := range l(data["entities"]) {
		rows[m(row)["path"].(string)] = m(row)
	}
	for _, path := range []string{model, main} {
		if rows[path]["boneCount"] != float64(2) || rows[path]["boneCountError"] != "" {
			t.Fatal(rows[path])
		}
	}
	if rows[broken]["boneCount"] != nil || rows[broken]["boneCountError"] == "" {
		t.Fatal(rows[broken])
	}
}
