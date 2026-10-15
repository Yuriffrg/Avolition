package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/formats/formatstest"
)

// tests/test_c4d_export.py test_api_offline_c4d_does_not_publish_even_if_fbx_enabled
func TestAPIOfflineC4DDoesNotPublishEvenIfFBXEnabled(t *testing.T) {
	isolate(t)
	base := tempDir(t)
	root := filepath.Join(repo(t), "tests", "material-assets")
	a := newAPI(t, Options{DataDir: filepath.Join(base, "profile")})
	message := failed(t, call(t, a, "export_animation", filepath.Join(root, "Animations/001_Wave.owanimclip"), "Wave", filepath.Join(base, "output"),
		map[string]any{"enabled": true, "c4dEnabled": true, "entityPath": filepath.Join(root, "Entities/MaterialRobot/MaterialRobot.owentity"), "rootPath": root}))
	if !strings.Contains(message, "Avolition C4D Bridge") || exists(filepath.Join(base, "output")) {
		t.Fatal(message)
	}
}

// API plumbing of test_c4d_root_motion_comes_from_export_option_not_preview_state.
func TestC4DRootMotionComesFromExportOptionNotPreviewState(t *testing.T) {
	isolate(t)
	base := tempDir(t)
	root := demoAssets(t)
	entity := filepath.Join(root, "Entities/PreviewRobot/PreviewRobot.owentity")
	source := filepath.Join(root, "Animations/003_Root_Move.owanimclip")
	bridge := newMockBridge()
	a := newAPI(t, Options{DataDir: filepath.Join(base, "profile"), Deps: withBridge(bridge)})
	options := map[string]any{"enabled": false, "c4dEnabled": true, "clipEnabled": false, "entityPath": entity, "rootPath": root,
		"includeMesh": false, "bakeTextures": false, "rootMotion": true}
	for _, c := range []struct {
		name       string
		lock, root bool
	}{{"Free", false, true}, {"In-place preview", true, true}, {"Root motion disabled", false, false}} {
		okMap(t, call(t, a, "export_animation", source, c.name, filepath.Join(base, "output"), with(options, "lockRoot", c.lock, "rootMotion", c.root)))
	}
	assertJSON(t, bridge.calls[0].Payload["animation"], bridge.calls[1].Payload["animation"])
	if jsonEqual(t, bridge.calls[0].Payload["animation"], bridge.calls[2].Payload["animation"]) {
		t.Fatal("rootMotion option ignored")
	}
}

// API tests of tests/test_animation_analysis.py.

func waitAnalysis(t *testing.T, a *API, root string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := okMap(t, call(t, a, "analysis_status", root))
		if status["busy"] != true {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("analysis did not finish")
	return nil
}

func TestAPIFreshScanDoesNotAutomaticallyAnalyze(t *testing.T) {
	isolate(t)
	base := tempDir(t)
	root := filepath.Join(base, "root")
	writeFile(t, filepath.Join(root, "animations/00000000007B.owanimclip"), formatstest.NewClip().Bytes())
	a := newAPI(t, Options{DataDir: filepath.Join(base, "state"), AppDir: filepath.Join(base, "app")})
	ok(t, call(t, a, "scan_root", root))
	if waitAnalysis(t, a, root)["state"] != "Ready" || ok(t, call(t, a, "analysis_index", root)) != nil {
		t.Fatal("scan analyzed")
	}
}

func TestAPIRescanLeavesAnalysisIdleUntilRequested(t *testing.T) {
	isolate(t)
	base := tempDir(t)
	root := filepath.Join(base, "root")
	path := writeFile(t, filepath.Join(root, "animations/00000000007B.owanimclip"), formatstest.NewClip().Bytes())
	a := newAPI(t, Options{DataDir: filepath.Join(base, "state"), AppDir: filepath.Join(base, "app")})
	ok(t, call(t, a, "analysis_start", root))
	previous := waitAnalysis(t, a, root)
	if previous["state"] != "Completed" {
		t.Fatal(previous)
	}
	writeFile(t, path, clipWithDuration(61))
	ok(t, call(t, a, "scan_root", root))
	if after := okMap(t, call(t, a, "analysis_status", root)); after["revision"] != previous["revision"] || after["busy"] == true {
		t.Fatal("scan restarted analysis")
	}
	ok(t, call(t, a, "analysis_start", root))
	current := waitAnalysis(t, a, root)
	if current["state"] != "Completed" || current["revision"] == previous["revision"] {
		t.Fatal(current)
	}
	detail := okMap(t, call(t, a, "analysis_detail", path, root))
	if m(detail["occurrence"])["path"] != path {
		t.Fatal(detail)
	}
	failed(t, call(t, a, "analysis_start", filepath.Join(base, "missing")))
}

// tests/test_selected_object_api.py

type selectedFixture struct {
	base, root, entity, source, output string
	api                                *API
	bridge                             *mockBridge
	options                            map[string]any
	asset                              *formats.Asset
}

func newSelectedFixture(t *testing.T) *selectedFixture {
	isolate(t)
	f := &selectedFixture{base: tempDir(t)}
	f.root = filepath.Join(f.base, "Source")
	for _, c := range []struct {
		label, guid string
		color       [4]byte
	}{{"Arms", "00000000013E", [4]byte{180, 40, 70, 255}}, {"Weapon", "0000000001D4", [4]byte{40, 150, 220, 255}}} {
		model := formatstest.NewModel()
		model.Name = c.guid + ".00C"
		data := model.Bytes()
		// Replace the fixture's empty material-look reference with a real one.
		data = append(append(append([]byte{}, data[:4]...), formatstest.String(c.label+"-look.owmat")...), data[5:]...)
		writeFile(t, filepath.Join(f.root, c.label+".owmdl"), data)
		writeFile(t, filepath.Join(f.root, c.label+"-look.owmat"), formatstest.LookBytes(formatstest.LookEntry{4000, c.label + "-material.owmat"}))
		writeFile(t, filepath.Join(f.root, c.label+"-material.owmat"), formatstest.MaterialBytes([]formatstest.TextureRef{{c.label + ".png", 2903569922}}, nil, 0))
		color := c.color
		writeFile(t, filepath.Join(f.root, c.label+".png"), formatstest.PNGBytes(2, 2, func(int, int) [4]byte { return color }))
	}
	e := formatstest.NewEntity()
	e.Name, e.Model, e.Relative, e.Children = "Gameplay1P", "Arms.owmdl", ".", []formatstest.Child{{Reference: "Weapon.owentity", Attachment: "bone_0002"}}
	f.entity = writeFile(t, filepath.Join(f.root, "Gameplay1P.owentity"), e.Bytes())
	weapon := formatstest.NewEntity()
	weapon.Name, weapon.Model, weapon.Relative = "Weapon", "Weapon.owmdl", "."
	writeFile(t, filepath.Join(f.root, "Weapon.owentity"), weapon.Bytes())
	f.source = writeFile(t, filepath.Join(f.root, "source.owanimclip"), formatstest.NewClip().Bytes())
	f.bridge = newMockBridge()
	f.bridge.writer = func(payload map[string]any, destination string) {
		encoded, _ := json.Marshal(payload)
		os.WriteFile(destination, encoded, 0o644)
	}
	f.api = newAPI(t, Options{DataDir: filepath.Join(f.base, "Profile"), Deps: withBridge(f.bridge)})
	ok(t, call(t, f.api, "scan_root", f.root))
	f.output = filepath.Join(f.base, "Output")
	var err error
	if f.asset, err = formats.LoadAsset(f.entity, f.root); err != nil {
		t.Fatal(err)
	}
	if f.asset.Models[0].Name != "00000000013E.00C" || f.asset.Models[1].Name != "0000000001D4.00C" {
		t.Fatal("fixture models")
	}
	f.options = map[string]any{"clipEnabled": true, "enabled": false, "c4dEnabled": true, "includeMesh": true, "bakeTextures": true,
		"rootMotion": true, "entityPath": f.entity, "rootPath": f.root, "selectedModelId": "model-0", "rigId": "auto"}
	return f
}

func (f *selectedFixture) export(t *testing.T, options map[string]any) map[string]any {
	t.Helper()
	if options == nil {
		options = f.options
	}
	return okMap(t, call(t, f.api, "export_animation", f.source, "reload", f.output, options))
}

func TestSelectedC4DAndClipUseModelPrefixAndOnlySelectedMaterials(t *testing.T) {
	f := newSelectedFixture(t)
	requireBake(t, f.api)
	result := f.export(t, nil)
	if filepath.Base(result["clipPath"].(string)) != "00000000013E_reload.owanimclip" || filepath.Base(result["c4dPath"].(string)) != "00000000013E_reload.c4d" {
		t.Fatal(result)
	}
}

func TestSelectedRawClipOnlyHasPrefixWithoutBridgeOrTextures(t *testing.T) {
	f := newSelectedFixture(t)
	result := f.export(t, with(f.options, "c4dEnabled", false, "selectedModelId", "model-1"))
	if filepath.Base(result["path"].(string)) != "0000000001D4_reload.owanimclip" || !bytes.Equal(readBytes(t, result["path"].(string)), readBytes(t, f.source)) ||
		result["textureCount"] != float64(0) || exists(filepath.Join(f.output, "tex")) || f.bridge.statusCalls != 0 || len(f.bridge.calls) != 0 {
		t.Fatal(result)
	}
	entries, _ := os.ReadDir(f.output)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}

func TestSelectedC4DOnlyDoesNotPublishRawClip(t *testing.T) {
	f := newSelectedFixture(t)
	result := f.export(t, with(f.options, "clipEnabled", false, "bakeTextures", false))
	if filepath.Base(result["path"].(string)) != "00000000013E_reload.c4d" || result["textureCount"] != float64(0) {
		t.Fatal(result)
	}
	if _, has := result["clipPath"]; has {
		t.Fatal("clipPath")
	}
	payload := f.bridge.last(t).Payload
	materials := m(payload["materials"])
	if len(materials) != 1 || materials["model-0"] == nil {
		t.Fatal(materials)
	}
	for _, material := range m(materials["model-0"]) {
		if len(m(m(material)["maps"])) != 0 {
			t.Fatal("maps")
		}
	}
	models := l(m(payload["asset"])["models"])
	if len(models) != 1 || m(models[0])["id"] != "model-0" || payload["name"] != "00000000013E_reload" {
		t.Fatal(models)
	}
}

func TestSelectedSameClipExportsIndependentObjects(t *testing.T) {
	f := newSelectedFixture(t)
	arms := f.export(t, with(f.options, "bakeTextures", false))
	weapon := f.export(t, with(f.options, "bakeTextures", false, "selectedModelId", "model-1", "rigId", "model-0"))
	if filepath.Base(arms["c4dPath"].(string)) != "00000000013E_reload.c4d" || filepath.Base(weapon["c4dPath"].(string)) != "0000000001D4_reload.c4d" ||
		filepath.Base(weapon["clipPath"].(string)) != "0000000001D4_reload.owanimclip" {
		t.Fatal(weapon)
	}
	payload := f.bridge.last(t).Payload
	models := l(m(payload["asset"])["models"])
	if len(models) != 1 || m(models[0])["id"] != "model-1" {
		t.Fatal(models)
	}
	assertJSON(t, m(models[0])["bones"], f.asset.Models[1].Bones)
	for _, track := range l(m(payload["animation"])["tracks"]) {
		if m(track)["modelId"] != "model-1" {
			t.Fatal("foreign track")
		}
	}
}

func TestSelectedLegacyUnscopedExportKeepsOldFilenameAndWholeEntity(t *testing.T) {
	f := newSelectedFixture(t)
	result := f.export(t, with(f.options, "selectedModelId", deleteKey, "bakeTextures", false))
	if filepath.Base(result["clipPath"].(string)) != "reload.owanimclip" || filepath.Base(result["c4dPath"].(string)) != "reload.c4d" {
		t.Fatal(result)
	}
	payload := f.bridge.last(t).Payload
	if len(l(m(payload["asset"])["models"])) != 2 || len(m(payload["materials"])) != 2 {
		t.Fatal(payload["materials"])
	}
	assertJSON(t, m(payload["asset"])["exportHierarchy"], f.asset.ExportHierarchy)
}

func TestSelectedInvalidObjectCannotFallBackToWholeEntity(t *testing.T) {
	f := newSelectedFixture(t)
	for _, c4d := range []bool{false, true} {
		message := failed(t, call(t, f.api, "export_animation", f.source, "reload", f.output, with(f.options, "selectedModelId", "missing", "c4dEnabled", c4d)))
		if !strings.Contains(message, "valid object") || exists(f.output) {
			t.Fatal(message)
		}
	}
	if len(f.bridge.calls) != 0 {
		t.Fatal("bridge called")
	}
}

func TestSelectedCapturedObjectIsIndependentOfLaterViewportLoad(t *testing.T) {
	f := newSelectedFixture(t)
	ok(t, call(t, f.api, "load_entity", filepath.Join(f.root, "Weapon.owmdl")))
	result := f.export(t, with(f.options, "bakeTextures", false))
	if filepath.Base(result["c4dPath"].(string)) != "00000000013E_reload.c4d" ||
		m(l(m(f.bridge.last(t).Payload["asset"])["models"])[0])["name"] != "00000000013E.00C" {
		t.Fatal(result)
	}
}

// tests/test_entity_preview.py

type previewFixture struct {
	root, library, active, candidate, profile string
	api                                       *API
	activeAsset                               map[string]any
	loads                                     *loadRecorder
}

type loadRecorder struct {
	mu    sync.Mutex
	calls [][2]string
	hold  func(path string)
}

func newPreviewFixture(t *testing.T) *previewFixture {
	isolate(t)
	f := &previewFixture{root: tempDir(t)}
	f.library = filepath.Join(f.root, "Library")
	activeRoot := formatstest.GenerateMaterialDemo(t, filepath.Join(f.library, "Active"))
	previewRoot := formatstest.GenerateMaterialDemo(t, filepath.Join(f.library, "Candidates"))
	f.active = filepath.Join(activeRoot, "Entities/MaterialRobot/MaterialRobot.owentity")
	f.candidate = filepath.Join(previewRoot, "Entities/MaterialRobot/MaterialRobot.owentity")
	f.profile = filepath.Join(f.root, "Profile")
	f.loads = &loadRecorder{}
	deps := Default()
	load := deps.LoadAsset
	deps.LoadAsset = func(path, root string) (*formats.Asset, error) {
		f.loads.mu.Lock()
		f.loads.calls = append(f.loads.calls, [2]string{path, root})
		hold := f.loads.hold
		f.loads.mu.Unlock()
		if hold != nil {
			hold(path)
		}
		return load(path, root)
	}
	f.api = newAPI(t, Options{DataDir: f.profile, AppDir: filepath.Join(f.root, "App"), Deps: &deps})
	ok(t, call(t, f.api, "scan_root", f.library))
	f.activeAsset = okMap(t, call(t, f.api, "load_entity", f.active))
	return f
}

func firstKey(value any) string {
	for k := range m(value) {
		return k
	}
	return ""
}

func TestManyPreviewsApplySavedNamesWithoutEvictingActiveNames(t *testing.T) {
	f := newPreviewFixture(t)
	ok(t, call(t, f.api, "load_entity", f.candidate))
	ok(t, call(t, f.api, "save_object_name", f.candidate, "scene", "Saved preview 日本語"))
	ok(t, call(t, f.api, "save_object_name", f.candidate, "model:model-0", "Saved preview body"))
	ok(t, call(t, f.api, "load_entity", f.active))
	var candidates []string
	for i := range 10 {
		candidates = append(candidates, writeFile(t, filepath.Join(filepath.Dir(f.candidate), fmt.Sprintf("Candidate %d.owentity", i)), readBytes(t, f.candidate)))
	}
	beforeFile := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	f.api.mu.Lock()
	beforeNames := f.api.naming.Keys()
	f.api.mu.Unlock()
	root := f.api.Root()
	textureID := firstKey(f.activeAsset["textures"])
	activeTexture := ok(t, call(t, f.api, "load_texture", textureID))
	preview := okMap(t, call(t, f.api, "preview_entity", f.candidate))
	if preview["name"] != "Saved preview 日本語" || m(l(preview["models"])[0])["name"] != "Saved preview body" || m(preview["objectNames"])["scene"] != "Saved preview 日本語" {
		t.Fatal(preview["name"])
	}
	for _, candidate := range candidates {
		ok(t, call(t, f.api, "preview_entity", candidate))
	}
	f.api.mu.Lock()
	afterNames := f.api.naming.Keys()
	f.api.mu.Unlock()
	if !bytes.Equal(beforeFile, readBytes(t, filepath.Join(f.profile, "catalog.json"))) || fmt.Sprint(beforeNames) != fmt.Sprint(afterNames) || f.api.Root() != root {
		t.Fatal("preview changed the scene")
	}
	assertJSON(t, ok(t, call(t, f.api, "load_texture", textureID)), activeTexture)
	ok(t, call(t, f.api, "save_object_name", f.active, "model:model-0", "Still editable"))
}

func TestPreviewUsesCapturedRootAndReleasesOnlyItsTextureOwner(t *testing.T) {
	f := newPreviewFixture(t)
	animationID := firstKey(f.activeAsset["textures"])
	animationTexture := ok(t, call(t, f.api, "load_texture", animationID))
	f.loads.mu.Lock()
	f.loads.calls = nil
	f.loads.mu.Unlock()
	preview := okMap(t, call(t, f.api, "preview_entity", f.candidate))
	if len(f.loads.calls) != 1 || f.loads.calls[0] != [2]string{f.candidate, f.library} {
		t.Fatal(f.loads.calls)
	}
	previewID := firstKey(preview["textures"])
	if previewID == animationID {
		t.Fatal("same texture id")
	}
	ok(t, call(t, f.api, "load_texture", previewID))
	assertJSON(t, ok(t, call(t, f.api, "release_entity_preview")), map[string]any{"released": true})
	failed(t, call(t, f.api, "load_texture", previewID))
	assertJSON(t, ok(t, call(t, f.api, "load_texture", animationID)), animationTexture)
}

func TestClosingOrReplacingPreviewDiscardsInflightResult(t *testing.T) {
	f := newPreviewFixture(t)
	for _, replace := range []bool{false, true} {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		f.loads.mu.Lock()
		f.loads.hold = func(path string) {
			if path == f.candidate {
				once.Do(func() { close(entered) })
				<-release
			}
		}
		f.loads.mu.Unlock()
		var response Reply
		done := make(chan struct{})
		go func() { response = call(t, f.api, "preview_entity", f.candidate); close(done) }()
		<-entered
		var replacement map[string]any
		if replace {
			replacement = okMap(t, call(t, f.api, "preview_entity", f.active))
		} else {
			ok(t, call(t, f.api, "release_entity_preview"))
		}
		close(release)
		<-done
		f.loads.mu.Lock()
		f.loads.hold = nil
		f.loads.mu.Unlock()
		if message := failed(t, response); !strings.Contains(message, "closed or replaced") {
			t.Fatal(message)
		}
		if replace {
			ok(t, call(t, f.api, "load_texture", firstKey(replacement["textures"])))
		}
		ok(t, call(t, f.api, "release_entity_preview"))
		ok(t, call(t, f.api, "load_texture", firstKey(f.activeAsset["textures"])))
	}
}

func TestFailedPreviewDoesNotChangeSceneOrCatalog(t *testing.T) {
	f := newPreviewFixture(t)
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	failed(t, call(t, f.api, "preview_entity", filepath.Join(f.root, "Missing.owentity")))
	failed(t, call(t, f.api, "preview_entity", "relative.owentity"))
	if !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("catalog changed")
	}
	ok(t, call(t, f.api, "load_texture", firstKey(f.activeAsset["textures"])))
}
