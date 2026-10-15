package api

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/exportbundle"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/mapexport"
	"avolition/gocore/internal/mapformats"
	"avolition/gocore/internal/mapformats/maptest"
)

// tests/test_map_jobs.py: background map exports keep serial requests
// responsive. Map parsing and catalog persistence are real; the native
// export is held/simulated.

type capturedMap struct {
	asset        *formats.Asset
	folder, name string
	bake         bool
}

type mapJobsFixture struct {
	root, mapRoot, mapPath, output string
	api                            *API
	mu                             sync.Mutex
	captured                       []capturedMap
	hold                           func(path, root string) error // nil: load immediately
	exportHook                     func(options mapexport.Options) error
}

func newMapJobsFixture(t *testing.T) *mapJobsFixture {
	isolate(t)
	f := &mapJobsFixture{root: tempDir(t)}
	f.mapRoot = filepath.Join(f.root, "Maps 日本語")
	f.mapPath = maptest.WriteFixture(t, f.mapRoot)
	f.output = filepath.Join(f.root, "Output café")
	deps := Default()
	deps.LoadMap = func(path, root string) (*formats.Asset, error) {
		f.mu.Lock()
		hold := f.hold
		f.mu.Unlock()
		if hold != nil {
			if err := hold(path, root); err != nil {
				return nil, err
			}
		}
		return mapformats.LoadMap(path, root)
	}
	deps.ExportMap = func(asset *formats.Asset, folder, name string, options mapexport.Options) (*exportbundle.Result, error) {
		f.mu.Lock()
		hook := f.exportHook
		f.mu.Unlock()
		if hook != nil {
			if err := hook(options); err != nil {
				return nil, err
			}
		}
		f.mu.Lock()
		f.captured = append(f.captured, capturedMap{asset.Clone(), folder, name, options.BakeTextures})
		f.mu.Unlock()
		if options.Progress != nil {
			one := 1
			options.Progress("Saving test scene…", &one, &one)
		}
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return nil, err
		}
		destination := filepath.Join(folder, name+".c4d")
		if err := os.WriteFile(destination, []byte("FAKE NATIVE FILE; BACKEND TEST ONLY"), 0o644); err != nil {
			return nil, err
		}
		return &exportbundle.Result{Path: &destination, C4DPath: &destination, Files: []string{destination},
			Warnings: []string{"Test native export only"}}, nil
	}
	f.api = newAPI(t, Options{DataDir: filepath.Join(f.root, "Profile"), DesktopMode: true, AppDir: filepath.Join(f.root, "App"), Deps: &deps})
	ok(t, call(t, f.api, "maps_settings", map[string]any{"root": f.mapRoot, "outputFolder": f.output}))
	return f
}

// held blocks map loading until release is closed and signals entered.
func (f *mapJobsFixture) held(entered, release chan struct{}) {
	var once sync.Once
	f.mu.Lock()
	f.hold = func(string, string) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("Test loader was not released.")
		}
	}
	f.mu.Unlock()
}

func (f *mapJobsFixture) waitFinished(t *testing.T, id any) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var args []any
		if id != nil {
			args = []any{id}
		}
		snapshot := okMap(t, call(t, f.api, "export_map_status", args...))
		if snapshot["busy"] != true {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the test map export did not finish")
	return nil
}

func waitChan(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func TestSerialStdioSettingsStatusAndBootstrapRespondWhileLoading(t *testing.T) {
	f := newMapJobsFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.held(entered, release)
	replies := exchange(t, f.api, []any{
		map[string]any{"id": "start", "method": "export_map_start", "args": []any{f.mapPath, "Serial map", nil, nil, false}},
		map[string]any{"id": "status", "method": "export_map_status"},
		map[string]any{"id": "settings", "method": "maps_settings", "args": []any{map[string]any{"bakeTextures": false}}},
		map[string]any{"id": "workflow", "method": "workflow_settings", "args": []any{map[string]any{"activeTab": "maps"}}},
		map[string]any{"id": "restore", "method": "bootstrap"},
	})
	waitChan(t, entered, "loader not entered")
	start := m(replyData(t, replies[0], "start"))
	status := m(replyData(t, replies[1], "status"))
	restore := m(replyData(t, replies[4], "restore"))
	if start["state"] != "Running" || status["id"] != start["id"] || status["busy"] != true || status["message"] != "Loading map…" ||
		m(replyData(t, replies[2], "settings"))["bakeTextures"] != false || m(replyData(t, replies[3], "workflow"))["activeTab"] != "maps" ||
		m(restore["mapExport"])["id"] != start["id"] || m(restore["mapExport"])["busy"] != true {
		t.Fatalf("replies: %v", replies)
	}
	close(release)
	if final := f.waitFinished(t, nil); final["state"] != "Completed" {
		t.Fatal(final)
	}
}

func TestMapJobChoicesNamesProgressAndResultAreCaptured(t *testing.T) {
	f := newMapJobsFixture(t)
	original := okMap(t, call(t, f.api, "load_map", f.mapPath))
	modelID := m(l(original["models"])[0])["id"].(string)
	ok(t, call(t, f.api, "save_object_name", f.mapPath, "scene", "Scene at start"))
	ok(t, call(t, f.api, "save_object_name", f.mapPath, "model:"+modelID, "Object at start"))
	entered, release := make(chan struct{}), make(chan struct{})
	exporting, finish := make(chan struct{}), make(chan struct{})
	var roots []string
	var once sync.Once
	f.mu.Lock()
	f.hold = func(path, root string) error {
		roots = append(roots, root)
		once.Do(func() { close(entered) })
		<-release
		return nil
	}
	f.exportHook = func(options mapexport.Options) error {
		three, twelve := 3, 12
		options.Progress("Building Cinema 4D scene…", &three, &twelve)
		close(exporting)
		<-finish
		return nil
	}
	f.mu.Unlock()
	started := okMap(t, call(t, f.api, "export_map_start", f.mapPath, "Captured name", nil, nil, false))
	waitChan(t, entered, "loader not entered")
	otherRoot, otherOutput := filepath.Join(f.root, "Other source"), filepath.Join(f.root, "Other output")
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ok(t, call(t, f.api, "maps_settings", map[string]any{"root": otherRoot, "outputFolder": otherOutput, "bakeTextures": true}))
	ok(t, call(t, f.api, "save_object_name", f.mapPath, "scene", "Scene after start"))
	ok(t, call(t, f.api, "save_object_name", f.mapPath, "model:"+modelID, "Object after start"))
	close(release)
	waitChan(t, exporting, "export not reached")
	running := okMap(t, call(t, f.api, "export_map_status", started["id"]))
	if running["message"] != "Building Cinema 4D scene…" || running["completed"] != float64(3) || running["total"] != float64(12) ||
		running["result"] != nil || running["error"] != nil || running["elapsedSeconds"].(float64) < 0 {
		t.Fatal(running)
	}
	failed(t, call(t, f.api, "export_map_status", "unknown"))
	close(finish)
	final := f.waitFinished(t, started["id"])
	if final["state"] != "Completed" {
		t.Fatal(final)
	}
	captured := f.captured[0]
	if len(roots) != 1 || roots[0] != f.mapRoot || captured.folder != f.output || captured.name != "Captured name" || captured.bake ||
		captured.asset.Name != "Scene at start" || captured.asset.Models[0].Name != "Object at start" {
		t.Fatalf("captured %+v roots %v", captured, roots)
	}
	result := m(final["result"])
	if filepath.Dir(result["path"].(string)) != f.output {
		t.Fatal(result)
	}
	f.api.mu.Lock()
	recorded := listContains(f.api.catalogList("exportedFiles"), key(result["path"].(string)))
	f.api.mu.Unlock()
	if !recorded {
		t.Fatal("not recorded")
	}
	assertJSON(t, okMap(t, call(t, f.api, "bootstrap"))["mapExport"], final)
}

func TestAsyncAndLegacySyncExportsShareDuplicateGuard(t *testing.T) {
	f := newMapJobsFixture(t)
	for _, legacyFirst := range []bool{false, true} {
		entered, release := make(chan struct{}), make(chan struct{})
		f.held(entered, release)
		var legacy Reply
		var wg sync.WaitGroup
		if legacyFirst {
			wg.Add(1)
			go func() { defer wg.Done(); legacy = call(t, f.api, "export_map", f.mapPath, "Legacy", nil, nil, false) }()
		} else {
			ok(t, call(t, f.api, "export_map_start", f.mapPath, "Background", nil, nil, false))
		}
		waitChan(t, entered, "loader not entered")
		for _, method := range []string{"export_map_start", "export_map"} {
			if message := failed(t, call(t, f.api, method, f.mapPath, "Duplicate", nil, nil, false)); !strings.Contains(message, "already running") {
				t.Fatal(message)
			}
		}
		close(release)
		if f.waitFinished(t, nil)["state"] != "Completed" {
			t.Fatal("not completed")
		}
		wg.Wait()
		if legacyFirst {
			ok(t, legacy)
		}
		f.mu.Lock()
		f.hold = nil
		f.mu.Unlock()
	}
}

func TestFailedMapJobIsObservableAndLaterExportCompletes(t *testing.T) {
	f := newMapJobsFixture(t)
	idle := okMap(t, call(t, f.api, "export_map_status"))
	if idle["id"] != nil || idle["state"] != "Idle" || idle["busy"] != false || idle["elapsedSeconds"] != float64(0) {
		t.Fatal(idle)
	}
	f.hold = func(string, string) error { return newValueError("Broken map fixture") }
	started := okMap(t, call(t, f.api, "export_map_start", f.mapPath, "Failed map"))
	failedJob := f.waitFinished(t, started["id"])
	if failedJob["state"] != "Failed" || failedJob["error"] != "Broken map fixture" || failedJob["result"] != nil || exists(f.output) {
		t.Fatal(failedJob)
	}
	f.mu.Lock()
	f.hold = nil
	f.mu.Unlock()
	recovered := okMap(t, call(t, f.api, "export_map_start", f.mapPath, "Recovered map", nil, nil, false))
	final := f.waitFinished(t, recovered["id"])
	if recovered["id"] == started["id"] || final["state"] != "Completed" || final["error"] != nil || !exists(m(final["result"])["path"].(string)) {
		t.Fatal(final)
	}
}

func TestShutdownDoesNotJoinHeldNativeWorker(t *testing.T) {
	f := newMapJobsFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.held(entered, release)
	started := okMap(t, call(t, f.api, "export_map_start", f.mapPath, "Shutdown test", nil, nil, false))
	waitChan(t, entered, "loader not entered")
	closed := make(chan struct{})
	go func() { f.api.Shutdown(); close(closed) }()
	waitChan(t, closed, "shutdown waited for a held native operation")
	if message := failed(t, call(t, f.api, "export_map_start", f.mapPath, "Too late")); !strings.Contains(message, "closing") {
		t.Fatal(message)
	}
	close(release)
	if f.waitFinished(t, started["id"])["state"] != "Completed" {
		t.Fatal("not completed")
	}
}

// Map export parts of tests/test_maps_api.py with the real map exporter and
// a mocked native writer.

func newMapBridge() *mockBridge {
	bridge := newMockBridge()
	bridge.status.extra = map[string]any{"mapInstances": true}
	return bridge
}

func TestMapExportPreservesCompactInstancesAndSavedNames(t *testing.T) {
	f := newQueueFixture(t)
	bridge := newMapBridge()
	a := newAPI(t, Options{DataDir: f.profile, Deps: withBridge(bridge)})
	mapRoot := filepath.Join(f.base, "Maps café")
	mapPath := maptest.WriteFixture(t, mapRoot)
	exportFolder := filepath.Join(f.base, "Map export")
	ok(t, call(t, a, "maps_settings", map[string]any{"root": mapRoot, "outputFolder": exportFolder}))
	before := okMap(t, call(t, a, "bootstrap"))
	sources := snapshotFiles(t, mapRoot)
	asset := okMap(t, call(t, a, "load_map", mapPath))
	modelID := m(l(asset["models"])[0])["id"].(string)
	for node, name := range map[string]string{"scene": "Test map", "model:" + modelID: "First prop", "mesh:" + modelID + ":0": "Unique shell"} {
		ok(t, call(t, a, "save_object_name", mapPath, node, name))
	}
	result := okMap(t, call(t, a, "export_map", mapPath, "Map 日本語", nil, nil, false))
	if !bytes.Equal(readBytes(t, result["path"].(string)), mockC4DBytes) || filepath.Dir(result["path"].(string)) != exportFolder {
		t.Fatal(result)
	}
	payload := bridge.last(t).Payload
	exported := m(payload["asset"])
	models := l(exported["models"])
	if payload["mapInstances"] != true || len(l(m(payload["animation"])["tracks"])) != 0 || exported["name"] != "Test map" ||
		m(models[0])["name"] != "First prop" || len(m(exported["meshPrototypes"])) != 3 {
		t.Fatal(exported["name"])
	}
	assertJSON(t, m(models[0])["meshNames"], map[string]any{"0": "Unique shell"})
	if _, has := m(models[1])["meshNames"]; has {
		t.Fatal("meshNames on unnamed model")
	}
	if len(sources) != len(snapshotFiles(t, mapRoot)) {
		t.Fatal("sources changed")
	}
	after := okMap(t, call(t, a, "bootstrap"))
	for _, name := range []string{"lastRoot", "outputFolder", "exportQueue"} {
		assertJSON(t, after[name], before[name])
	}
	a.mu.Lock()
	recorded := listContains(a.catalogList("exportedFiles"), key(result["path"].(string))) &&
		listContains(a.catalogList("managedOutputDirectories"), key(exportFolder))
	a.mu.Unlock()
	if !recorded {
		t.Fatal("export not recorded")
	}
	writeFile(t, result["path"].(string), []byte("User edited map scene"))
	collision := okMap(t, call(t, a, "export_map", mapPath, "Map 日本語", nil, nil, false))
	if collision["path"] == result["path"] || string(readBytes(t, result["path"].(string))) != "User edited map scene" {
		t.Fatal(collision)
	}
}

func TestFailedMapSaveAndOldBridgePreserveExistingExports(t *testing.T) {
	f := newQueueFixture(t)
	bridge := newMapBridge()
	bridge.fail = true
	a := newAPI(t, Options{DataDir: f.profile, Deps: withBridge(bridge)})
	mapRoot := filepath.Join(f.base, "Maps")
	mapPath := maptest.WriteFixture(t, mapRoot)
	output := filepath.Join(f.base, "Map output")
	existing := writeFile(t, filepath.Join(output, "Existing.c4d"), []byte("user scene"))
	ok(t, call(t, a, "maps_settings", map[string]any{"root": mapRoot, "outputFolder": output}))
	stored := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	if message := failed(t, call(t, a, "export_map", mapPath, "Failed", nil, nil, false)); !strings.Contains(message, "Mock native writer failed") {
		t.Fatal(message)
	}
	entries, _ := os.ReadDir(output)
	if len(entries) != 1 || string(readBytes(t, existing)) != "user scene" || !bytes.Equal(stored, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("outputs changed")
	}
	bridge.fail = false
	bridge.status.extra = nil // an old bridge without map support
	calls := len(bridge.calls)
	if message := failed(t, call(t, a, "export_map", mapPath, "Old bridge", nil, nil, false)); !strings.Contains(message, "Bridge") {
		t.Fatal(message)
	}
	if len(bridge.calls) != calls {
		t.Fatal("old bridge used")
	}
	bridge.status.extra = map[string]any{"mapInstances": true}
	ok(t, call(t, a, "export_map", mapPath, "Recovered", nil, nil, false))
}

func TestStaleMapNamesAreIgnoredAtExport(t *testing.T) {
	f := newQueueFixture(t)
	bridge := newMapBridge()
	a := newAPI(t, Options{DataDir: f.profile, Deps: withBridge(bridge)})
	mapRoot := filepath.Join(f.base, "Changed maps")
	mapPath := maptest.WriteFixture(t, mapRoot)
	ok(t, call(t, a, "maps_settings", map[string]any{"root": mapRoot, "outputFolder": filepath.Join(f.base, "Map output")}))
	modelID := m(l(okMap(t, call(t, a, "load_map", mapPath))["models"])[0])["id"].(string)
	valid := map[string]any{"scene": "Saved scene", "model:" + modelID: "Surviving prop", "mesh:" + modelID + ":0": "Surviving mesh"}
	stored := with(valid, "model:removed-model", "Old placement", "mesh:"+modelID+":999", "Old mesh")
	setObjectNames(t, a, mapPath, stored)
	result := okMap(t, call(t, a, "export_map", mapPath, "Changed map", nil, nil, false))
	if !bytes.Equal(readBytes(t, result["path"].(string)), mockC4DBytes) {
		t.Fatal(result)
	}
	exported := m(bridge.last(t).Payload["asset"])
	if exported["name"] != "Saved scene" || m(l(exported["models"])[0])["name"] != "Surviving prop" {
		t.Fatal(exported["name"])
	}
	assertJSON(t, m(l(exported["models"])[0])["meshNames"], map[string]any{"0": "Surviving mesh"})
	assertJSON(t, ok(t, call(t, a, "object_names", mapPath)), stored)
}

// API part of tests/test_selected_object_export.py
// test_texture_baking_processes_only_selected_mesh_materials: an object
// export bakes only the selected model's materials.
func TestSelectedObjectBakesOnlySelectedMaterials(t *testing.T) {
	f := newSelectedFixture(t)
	result := f.export(t, nil)
	c := f.bridge.last(t)
	materials := m(c.Payload["materials"])
	if len(materials) != 1 || materials["model-0"] == nil || result["textureCount"] != float64(1) || len(c.Textures) != 1 {
		t.Fatalf("materials %v textures %d", materials, len(c.Textures))
	}
	textures := m(m(c.Payload["asset"])["textures"])
	for _, spec := range textures {
		if filepath.Base(m(spec)["path"].(string)) != "Arms.png" {
			t.Fatal(spec)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.output, "tex"))
	if len(entries) != len(c.Textures) {
		t.Fatal(entries)
	}
}
