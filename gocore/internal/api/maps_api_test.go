package api

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/mapformats/maptest"
	"avolition/gocore/internal/ordered"
)

// tests/test_maps_api.py: maps/settings/name wiring.

func requireMapExport(t testing.TB, a *API) {
	t.Helper()
	if a.deps.ExportMap == nil {
		t.Skip("waiting for mapexport (map_export.export_map)")
	}
}

func TestMapSettingsDefaultsAndCustomPathsAreSeparate(t *testing.T) {
	f := newQueueFixture(t)
	fresh := newAPI(t, Options{DataDir: filepath.Join(f.base, "Fresh profile"), AppDir: filepath.Join(f.base, "Application")})
	assertJSON(t, okMap(t, call(t, fresh, "bootstrap"))["maps"], map[string]any{
		"root": filepath.Join(f.base, "Application", "library"), "outputFolder": filepath.Join(f.base, "Application", "library", "Map exports"),
		"bakeTextures": true})
	before := okMap(t, call(t, f.api, "bootstrap"))
	custom := map[string]any{"root": filepath.Join(f.base, "Maps 日本語"), "outputFolder": filepath.Join(f.base, "Map output café"), "bakeTextures": false}
	assertJSON(t, ok(t, call(t, f.api, "maps_settings", custom)), custom)
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"activeTab": "maps", "toolchainTab": "maps"}))
	restarted := newAPI(t, Options{DataDir: f.profile})
	after := okMap(t, call(t, restarted, "bootstrap"))
	assertJSON(t, after["maps"], custom)
	if m(after["workflow"])["activeTab"] != "maps" || m(after["workflow"])["toolchainTab"] != "maps" {
		t.Fatal(after["workflow"])
	}
	for _, name := range []string{"lastRoot", "outputFolder", "catalog", "exportQueue", "libraryOptions"} {
		assertJSON(t, after[name], before[name])
	}
	ok(t, call(t, restarted, "workflow_settings", map[string]any{"activeTab": "animations", "toolchainTab": "heroes"}))
	assertJSON(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "bootstrap"))["maps"], custom)
	if exists(custom["outputFolder"].(string)) {
		t.Fatal("output created")
	}
}

func TestInvalidMapSettingsAndFailedSavePreserveSettings(t *testing.T) {
	f := newQueueFixture(t)
	ok(t, call(t, f.api, "maps_settings", map[string]any{"root": f.root, "bakeTextures": false}))
	before := ok(t, call(t, f.api, "maps_settings"))
	path := filepath.Join(f.profile, "catalog.json")
	stored := readBytes(t, path)
	for _, changes := range []any{[]any{}, "", map[string]any{"root": ""}, map[string]any{"root": "relative"}, map[string]any{"outputFolder": ""},
		map[string]any{"bakeTextures": "false"}, map[string]any{"bakeTextures": 0}, map[string]any{"other": true}} {
		failed(t, call(t, f.api, "maps_settings", changes))
		assertJSON(t, ok(t, call(t, f.api, "maps_settings")), before)
		if !bytes.Equal(readBytes(t, path), stored) {
			t.Fatalf("%v changed the catalog", changes)
		}
	}
	f.api.saveHook = func() error { return errors.New("Disk full") }
	failed(t, call(t, f.api, "maps_settings", map[string]any{"outputFolder": filepath.Join(f.base, "New output")}))
	f.api.saveHook = nil
	assertJSON(t, ok(t, call(t, f.api, "maps_settings")), before)
	if !bytes.Equal(readBytes(t, path), stored) {
		t.Fatal("catalog changed")
	}
}

func TestScanMapsUsesRealFolderAndLeavesAnimationSelection(t *testing.T) {
	f := newQueueFixture(t)
	mapRoot := filepath.Join(f.base, "Map sources 日本語")
	for _, relative := range []string{"B map.owmap", "Nested/A 日本語.OWMAP", "Generated/Hidden.owmap", "Nested/not.txt"} {
		writeFile(t, filepath.Join(mapRoot, relative), []byte("map index fixture"))
	}
	f.api.mu.Lock()
	f.api.catalog.Set("managedOutputDirectories", append(f.api.catalogList("managedOutputDirectories"), filepath.Join(mapRoot, "Generated")))
	f.api.mu.Unlock()
	before := okMap(t, call(t, f.api, "bootstrap"))
	root := f.api.Root()
	result := okMap(t, call(t, f.api, "scan_maps", mapRoot))
	var relatives []any
	for _, entry := range l(result["maps"]) {
		relatives = append(relatives, m(entry)["relativePath"])
		if !filepath.IsAbs(m(entry)["path"].(string)) || m(entry)["size"].(float64) <= 0 {
			t.Fatal(entry)
		}
	}
	assertJSON(t, relatives, []any{"B map.owmap", filepath.Join("Nested", "A 日本語.OWMAP")})
	assertJSON(t, result["warnings"], []any{})
	after := okMap(t, call(t, f.api, "bootstrap"))
	if f.api.Root() != root || after["lastRoot"] != before["lastRoot"] || after["outputFolder"] != before["outputFolder"] || m(after["maps"])["root"] != mapRoot {
		t.Fatal(after)
	}
	for _, invalid := range []string{"", "relative", filepath.Join(f.base, "Not present")} {
		failed(t, call(t, f.api, "scan_maps", invalid))
	}
	if okMap(t, call(t, f.api, "maps_settings"))["root"] != mapRoot {
		t.Fatal("root changed")
	}
}

func TestMapOpenOutputUsesSeparateMapDestination(t *testing.T) {
	f := newQueueFixture(t)
	output := filepath.Join(f.base, "Map output")
	ok(t, call(t, f.api, "maps_settings", map[string]any{"outputFolder": output}))
	failed(t, call(t, f.api, "open_map_output"))
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok(t, call(t, f.api, "open_map_output")) != output || okMap(t, call(t, f.api, "bootstrap"))["outputFolder"] != f.output {
		t.Fatal("wrong destination")
	}
}

func TestSavedObjectNamesSurviveRestartAndFailedSaveRollsBack(t *testing.T) {
	f := newQueueFixture(t)
	sources := snapshotFiles(t, f.root)
	asset := okMap(t, call(t, f.api, "load_entity", f.entity))
	original := asset["models"]
	names := map[string]any{"scene": "Reload / scène", "model:model-0": "Arms 日本語", "mesh:model-1:0": "Weapon shell"}
	for _, node := range []string{"scene", "model:model-0", "mesh:model-1:0"} {
		ok(t, call(t, f.api, "save_object_name", f.entity, node, names[node]))
	}
	restarted := newAPI(t, Options{DataDir: f.profile})
	loaded := okMap(t, call(t, restarted, "load_entity", f.entity))
	assertJSON(t, loaded["objectNames"], names)
	assertJSON(t, loaded["models"], original)
	assertJSON(t, ok(t, call(t, restarted, "object_names", f.entity)), names)
	if len(sources) != len(snapshotFiles(t, f.root)) {
		t.Fatal("sources changed")
	}
	stored := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	for _, c := range [][2]string{{"bone:bone_0001", "Rename"}, {"model:missing", "Rename"}, {"mesh:model-0:99", "Rename"},
		{"model:model-0", ""}, {"model:model-0", "Bad\nname"}} {
		failed(t, call(t, restarted, "save_object_name", f.entity, c[0], c[1]))
	}
	restarted.saveHook = func() error { return errors.New("Disk full") }
	failed(t, call(t, restarted, "save_object_name", f.entity, "model:model-0", "Unsaved"))
	restarted.saveHook = nil
	assertJSON(t, ok(t, call(t, restarted, "object_names", f.entity)), names)
	if !bytes.Equal(readBytes(t, filepath.Join(f.profile, "catalog.json")), stored) {
		t.Fatal("catalog changed")
	}
}

func setObjectNames(t *testing.T, a *API, path string, names map[string]any) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := ordered.New[any]()
	saved.Set(key(path), pyValue(t, names))
	a.catalog.Set("objectNames", saved)
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleNameKeysAreIgnoredInPreviewWithoutErasingSavedLabels(t *testing.T) {
	f := newQueueFixture(t)
	names := map[string]any{"model:model-0": "Arms", "mesh:model-1:0": "Shell", "model:removed": "Old model", "mesh:model-0:999": "Removed mesh"}
	setObjectNames(t, f.api, f.entity, names)
	stored := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	assertJSON(t, okMap(t, call(t, f.api, "load_entity", f.entity))["objectNames"], map[string]any{"model:model-0": "Arms", "mesh:model-1:0": "Shell"})
	assertJSON(t, ok(t, call(t, f.api, "object_names", f.entity)), names)
	if !bytes.Equal(readBytes(t, filepath.Join(f.profile, "catalog.json")), stored) {
		t.Fatal("catalog changed")
	}
}

func TestQueueUsesNamesCapturedAtAdd(t *testing.T) {
	f := newQueueFixture(t)
	ok(t, call(t, f.api, "load_entity", f.entity))
	names := map[string]any{"scene": "Reload setup", "model:model-0": "Arms at add", "mesh:model-1:0": "Gun shell at add"}
	for _, node := range []string{"scene", "model:model-0", "mesh:model-1:0"} {
		ok(t, call(t, f.api, "save_object_name", f.entity, node, names[node]))
	}
	saved := f.save(t, nil)
	assertJSON(t, saved["objectNames"], names)
	ok(t, call(t, f.api, "save_object_name", f.entity, "model:model-0", "Arms after add"))
	result := okMap(t, call(t, f.api, "export_queue_run", saved["id"], nil, f.options))
	if !bytes.Equal(readBytes(t, result["path"].(string)), mockC4DBytes) {
		t.Fatal("not exported")
	}
	models := l(m(f.bridge.last(t).Payload["asset"])["models"])
	if m(models[0])["name"] != "Arms at add" || m(l(m(models[1])["meshes"])[0])["name"] != "Gun shell at add" || m(models[0])["entityName"] != "Reload setup" {
		t.Fatal(models[0])
	}
	original, _ := formats.LoadAsset(f.entity, f.root)
	for i, model := range original.Models {
		if m(models[i])["id"] != model.ID || !jsonEqual(t, m(models[i])["bones"], model.Bones) {
			t.Fatal("skeleton changed")
		}
	}
	assertJSON(t, m(l(okMap(t, call(t, f.api, "bootstrap"))["exportQueue"])[0])["objectNames"], names)
	if okMap(t, call(t, f.api, "object_names", f.entity))["model:model-0"] != "Arms after add" {
		t.Fatal("saved name lost")
	}
}

func TestMapLoadPreservesCompactInstancesAndSavedNames(t *testing.T) {
	f := newQueueFixture(t)
	mapRoot := filepath.Join(f.base, "Maps café")
	mapPath := maptest.WriteFixture(t, mapRoot)
	ok(t, call(t, f.api, "maps_settings", map[string]any{"root": mapRoot, "outputFolder": filepath.Join(f.base, "Map export")}))
	asset := okMap(t, call(t, f.api, "load_map", mapPath))
	if len(l(asset["models"])) != 5 || len(m(asset["meshPrototypes"])) != 3 {
		t.Fatalf("models %d", len(l(asset["models"])))
	}
	modelID := m(l(asset["models"])[0])["id"].(string)
	names := map[string]any{"scene": "Test map", "model:" + modelID: "First prop", "mesh:" + modelID + ":0": "Unique shell"}
	for _, node := range []string{"scene", "model:" + modelID, "mesh:" + modelID + ":0"} {
		ok(t, call(t, f.api, "save_object_name", mapPath, node, names[node]))
	}
	assertJSON(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "load_map", mapPath))["objectNames"], names)
	requireMapExport(t, f.api)
}

func TestStaleMapNamesAreIgnoredWithoutDiscardingSavedNames(t *testing.T) {
	f := newQueueFixture(t)
	mapRoot := filepath.Join(f.base, "Changed maps")
	mapPath := maptest.WriteFixture(t, mapRoot)
	ok(t, call(t, f.api, "maps_settings", map[string]any{"root": mapRoot, "outputFolder": filepath.Join(f.base, "Map output")}))
	asset := okMap(t, call(t, f.api, "load_map", mapPath))
	modelID := m(l(asset["models"])[0])["id"].(string)
	valid := map[string]any{"scene": "Saved scene", "model:" + modelID: "Surviving prop", "mesh:" + modelID + ":0": "Surviving mesh"}
	stored := with(valid, "model:removed-model", "Old placement", "mesh:"+modelID+":999", "Old mesh")
	setObjectNames(t, f.api, mapPath, stored)
	assertJSON(t, okMap(t, call(t, f.api, "load_map", mapPath))["objectNames"], valid)
	assertJSON(t, ok(t, call(t, f.api, "object_names", mapPath)), stored)
	assertJSON(t, ok(t, call(t, newAPI(t, Options{DataDir: f.profile}), "object_names", mapPath)), stored)
	requireMapExport(t, f.api)
}

func TestMapExportFailuresPreserveExistingExports(t *testing.T) {
	f := newQueueFixture(t)
	requireMapExport(t, f.api)
}
