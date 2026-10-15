package libraryscan

// Ported from tests/test_library_scan.py. The cases that only exercise
// BrowserAPI.scan_root / scan_maps (pruning during os.walk, entity rows, sizes,
// bone counts) belong to the api package port:
//   test_shared_library_excludes_map_dependencies_and_keeps_character_assets
//   test_map_assets_are_pruned_before_walking_into_large_branches
//   test_direct_map_container_build_and_nested_model_scans_stay_empty (scan part)
//   test_loose_map_preserves_unrelated_character_siblings
//   test_case_insensitive_markers_work_without_a_maps_folder_name (scan part)
//   test_maps_name_alone_does_not_hide_character_assets
//   test_source_file_sizes_preserve_rows_and_counts_when_stat_is_unavailable
// Their library_scan.py logic is covered below and by the oracle tests.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"avolition/gocore/internal/oracle"
)

func write(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mapExport(t *testing.T, library, directory string) string {
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	write(t, library, prefix+"Evening.OWMAP")
	write(t, library, prefix+"Models/Building/Building.owmdl")
	write(t, library, prefix+"Entities/Prop.owentity")
	write(t, library, prefix+"Effects/Detail.owmdl")
	write(t, library, prefix+"Models/Building/Animations/Door.owanimclip")
	return filepath.Join(library, filepath.FromSlash(directory))
}

func TestMapMarkersAndFolders(t *testing.T) {
	if !HasMapMarker([]string{"a.txt", "Night.oWmAp"}) || HasMapMarker([]string{"owmap", "x.owmap.bak"}) {
		t.Fatal("HasMapMarker")
	}
	for _, name := range []string{"Models", "mOdElS", "modelſ", "SOUND", "ModelLooKs"} {
		if !IsMapAssetFolder(name) {
			t.Errorf("%q should be a map asset folder", name)
		}
	}
	for _, name := range []string{"Model", "Heroes", "Maps", ""} {
		if IsMapAssetFolder(name) {
			t.Errorf("%q should not be a map asset folder", name)
		}
	}
}

func TestDirectMapContainerAndNestedFoldersAreMapAssets(t *testing.T) {
	library := t.TempDir()
	mapRoot := mapExport(t, library, "Maps/Kings Row/6B1")
	for _, folder := range []string{"Models", "Models/Building", "Models/Building/Animations", "Entities", "Effects"} {
		if !InsideMapAssets(filepath.Join(mapRoot, folder)) {
			t.Errorf("%s should be inside map assets", folder)
		}
	}
	for _, folder := range []string{filepath.Join(library, "Maps"), mapRoot, library} {
		if InsideMapAssets(folder) {
			t.Errorf("%s should not be inside map assets", folder)
		}
	}
}

func TestCaseInsensitiveMarkerWithoutMapsFolderName(t *testing.T) {
	library := t.TempDir()
	write(t, library, "Environment/Night.oWmAp")
	model := write(t, library, "Environment/mOdElS/Prop.owmdl")
	if !InsideMapAssets(filepath.Dir(model)) {
		t.Fatal("mOdElS beside a .oWmAp marker is a map asset branch")
	}
}

func TestDirectoryNamedLikeAMapIsNotAnExportMarker(t *testing.T) {
	library := t.TempDir()
	write(t, library, "Custom/Models/Body.owmdl")
	if err := os.Mkdir(filepath.Join(library, "Custom", "Folder.owmap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if InsideMapAssets(filepath.Join(library, "Custom", "Models")) {
		t.Fatal("a directory named *.owmap is not a marker")
	}
}

func TestUnreadableAncestorDoesNotGuessMapOwnership(t *testing.T) {
	library := t.TempDir()
	defer func(saved func(string) ([]os.DirEntry, error)) { readDir = saved }(readDir)
	readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("not readable") }
	if InsideMapAssets(filepath.Join(library, "Models", "Character")) {
		t.Fatal("unreadable parent must not count as a map")
	}
}

func TestPriorityHelperHandlesWindowsAndPosixPaths(t *testing.T) {
	rows := []Item{
		{Kind: "model", RelativePath: "Models/Gameplay1P.owmdl"},
		{Kind: "entity", RelativePath: "Heroes\\日本語\\GAMEPLAY3P.OWENTITY"},
		{Kind: "entity", RelativePath: "Heroes/日本語/Gameplay1P.owentity"},
		{Kind: "entity", RelativePath: "Hero/Other.owentity"},
		{Kind: "entity", RelativePath: "Hero/Gameplay1P_extra.owentity"},
	}
	sorted := append([]Item(nil), rows...)
	SortEntities(sorted, func(item Item) Item { return item })
	want := []Item{rows[2], rows[1], rows[4], rows[3], rows[0]}
	if !reflect.DeepEqual(sorted, want) {
		t.Fatalf("got %v, want %v", sorted, want)
	}
}

func TestGameplayEntitiesLeadWithoutChangingRemainingOrder(t *testing.T) {
	order := []string{"Zed/Gameplay1P.owentity", "Alpha/GAMEPLAY1P.OWENTITY",
		"Alpha/Gameplay3P.owentity", "Zed/Gameplay3P.owentity",
		"Zed/Other.owentity", "Alpha/Other.owentity", "Alpha/Models/Gameplay1P.owmdl",
		"Alpha/Models/A_model.owmdl", "Zed/Models/Z_model.owmdl"}
	var rows []Item
	for _, relative := range order {
		kind := "model"
		if filepath.Ext(relative) != ".owmdl" {
			kind = "entity"
		}
		rows = append(rows, Item{Kind: kind, RelativePath: relative, Name: filepath.Base(relative)})
	}
	SortEntities(rows, func(item Item) Item { return item })
	var got []string
	for _, row := range rows {
		got = append(got, row.RelativePath)
	}
	want := []string{order[1], order[0], order[2], order[3], order[5], order[4], order[7], order[6], order[8]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSortKeysMatchPython(t *testing.T) {
	type row struct {
		Kind         string `json:"kind,omitempty"`
		RelativePath string `json:"relativePath,omitempty"`
		Path         string `json:"path,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	rows := []row{
		{Kind: "entity", Name: "GAMEPLAY1P.OWENTITY"},
		{Kind: "entity", Path: "/x/ΟΔΟΣ/gameplay3p.owentity"},
		{Kind: "entity", RelativePath: "İstanbul/b.owentity"},
		{Kind: "entity", RelativePath: "ΣΑΣ.owentity"},
		{Kind: "model", RelativePath: "Zeta/Straße.owmdl"},
		{Kind: "model", RelativePath: "zeta/strasse.owmdl"},
		{Kind: "entity", RelativePath: "a/GAMEPLAY1P.OWENTITY", Name: "other.owentity"},
		{Kind: "entity", RelativePath: "a\\Gameplay3ſ.owentity"},
		{Kind: "entity", RelativePath: "gameplay1p.owentity"},
		{Kind: "entity"},
		{Kind: "model", Name: "Ünïcode.owmdl"},
	}
	result := oracle.Exec(t, `
from owpreview.library_scan import entity_sort_key
result = [list(entity_sort_key(row)) for row in args[0]]`, rows)
	var want [][3]any
	if err := json.Unmarshal(result.MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		got := EntitySortKey(Item(r))
		if want[i][0] != got.NotEntity || int(want[i][1].(float64)) != got.Priority || want[i][2] != got.Relative {
			t.Errorf("row %d %+v: Python %v, Go %+v", i, r, want[i], got)
		}
	}
}

func TestInsideMapAssetsMatchesPython(t *testing.T) {
	library := t.TempDir()
	mapRoot := mapExport(t, library, "Maps/Kings Row/6B1")
	write(t, library, "Loose.owmap")
	write(t, library, "Heroes/Widow/Models/Body.owmdl")
	write(t, library, "Env/sounds/x.wav")
	write(t, library, "Env/marker.OwMaP")
	if err := os.Symlink(filepath.Join(library, "Loose.owmap"), filepath.Join(library, "Env", "linked.owmap")); err != nil {
		t.Log("symlink unsupported:", err)
	}
	paths := []string{
		filepath.Join(mapRoot, "Models"), filepath.Join(mapRoot, "Models", "Building", "Animations"),
		filepath.Join(mapRoot, "Models", "..", "Models"), mapRoot + "/./Entities/",
		filepath.Join(library, "Heroes", "Widow", "Models"), filepath.Join(library, "Models"),
		filepath.Join(library, "Env", "sounds"), filepath.Join(library, "Env", "nothing"),
		library, "/", ".", "", "models", "Models/x",
	}
	result := oracle.Exec(t, `
from owpreview.library_scan import inside_map_assets
result = [inside_map_assets(p) for p in args[0]]`, paths)
	var want []bool
	if err := json.Unmarshal(result.MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		if got := InsideMapAssets(path); got != want[i] {
			t.Errorf("InsideMapAssets(%q) = %v, Python %v", path, got, want[i])
		}
	}
}
