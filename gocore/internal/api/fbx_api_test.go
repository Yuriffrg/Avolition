package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/formats/formatstest"
)

// tests/test_fbx_api.py

func newFBXFixture(t *testing.T) *c4dFixture {
	f := newC4DFixture(t)
	f.options = map[string]any{"clipEnabled": true, "c4dEnabled": false, "enabled": true, "upAxis": "Y", "includeMesh": true,
		"bakeTextures": true, "rootMotion": true, "entityPath": f.entity, "rootPath": f.root, "rigId": "auto"}
	return f
}

func TestMaterialFixtureExportsRealFBXAndPortablePNGs(t *testing.T) {
	f := newFBXFixture(t)
	requireBake(t, f.api)
	original := snapshotFiles(t, f.root)
	result := f.export(t, "Wave", nil, "")
	data := readBytes(t, result["fbxPath"].(string))
	if !bytes.HasPrefix(data, fbxMagic) || !bytes.Contains(data, []byte("RelativeFilename")) || bytes.Contains(data, []byte(f.root)) {
		t.Fatal("FBX contents")
	}
	if result["textureCount"].(float64) <= 0 || result["textureFolder"] != filepath.Join(f.output, "tex") {
		t.Fatalf("textures: %v", result)
	}
	if fmt.Sprint(original) != fmt.Sprint(snapshotFiles(t, f.root)) {
		t.Fatal("sources changed")
	}
}

func TestRepeatReusesPairAndAxisChangeNumbersBothFiles(t *testing.T) {
	f := newFBXFixture(t)
	f.options["bakeTextures"] = false
	first := f.export(t, "Wave", nil, "")
	stamps := map[string]any{}
	for _, path := range l(first["files"]) {
		stamps[path.(string)] = mtime(t, path.(string))
	}
	repeated := f.export(t, "Wave", nil, "")
	if repeated["reused"] != true {
		t.Fatal("not reused")
	}
	assertJSON(t, repeated["files"], first["files"])
	for path, stamp := range stamps {
		if mtime(t, path) != stamp {
			t.Fatal("file rewritten")
		}
	}
	different := f.export(t, "Wave", with(f.options, "upAxis", "Z"), "")
	if filepath.Base(different["path"].(string)) != "Wave (2).owanimclip" || filepath.Base(different["fbxPath"].(string)) != "Wave (2).fbx" {
		t.Fatalf("numbering: %v", different)
	}
	if bytes.Equal(readBytes(t, first["fbxPath"].(string)), readBytes(t, different["fbxPath"].(string))) {
		t.Fatal("axis ignored")
	}
}

func TestSkeletonOnlyOptionEmitsNoTextureFolder(t *testing.T) {
	f := newFBXFixture(t)
	result := f.export(t, "Wave", with(f.options, "includeMesh", false), "")
	if !bytes.HasPrefix(readBytes(t, result["fbxPath"].(string)), fbxMagic) || result["textureCount"] != float64(0) ||
		result["textureFolder"] != nil || exists(filepath.Join(f.output, "tex")) {
		t.Fatalf("result: %v", result)
	}
}

func TestTextureCheckboxOffEmitsFBXWithoutTextureReferences(t *testing.T) {
	f := newFBXFixture(t)
	result := f.export(t, "Wave", with(f.options, "bakeTextures", false), "")
	if result["textureCount"] != float64(0) || bytes.Contains(readBytes(t, result["fbxPath"].(string)), []byte("RelativeFilename")) {
		t.Fatal("texture references without textures")
	}
}

func TestExportSettingsPersistAndInvalidUpdateKeepsPrevious(t *testing.T) {
	f := newFBXFixture(t)
	settings := map[string]any{"enabled": true, "upAxis": "Z", "includeMesh": false, "bakeTextures": false, "rootMotion": false}
	expected := with(settings, "clipEnabled", false, "c4dEnabled", true)
	assertJSON(t, ok(t, call(t, f.api, "set_export_settings", settings)), expected)
	failed(t, call(t, f.api, "set_export_settings", map[string]any{"upAxis": "X"}))
	assertJSON(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "bootstrap"))["fbxOptions"], expected)
}

func TestInvalidCatalogFBXSettingsRecoverWithoutLosingAnnotations(t *testing.T) {
	f := newFBXFixture(t)
	path := filepath.Join(f.profile, "catalog.json")
	catalog := readJSON(t, path)
	catalog["fbxOptions"] = map[string]any{"enabled": "invalid", "upAxis": "Q"}
	catalog["annotations"] = map[string]any{"preserved": map[string]any{"name": "Keep this nickname", "favorite": true}}
	encoded, _ := json.Marshal(catalog)
	writeFile(t, path, encoded)
	data := okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "bootstrap"))
	if m(data["fbxOptions"])["enabled"] != false || m(data["fbxOptions"])["upAxis"] != "Y" || data["warning"] == nil {
		t.Fatalf("data: %v", data)
	}
	assertJSON(t, data["catalog"], catalog["annotations"])
}

func TestLegacyFormatDefaultsMigrateOncePreservingCatalog(t *testing.T) {
	f := newFBXFixture(t)
	path := filepath.Join(f.profile, "catalog.json")
	original := readJSON(t, path)
	original["fbxOptions"] = map[string]any{"enabled": true, "c4dEnabled": false, "upAxis": "Z", "includeMesh": false,
		"bakeTextures": false, "rootMotion": false}
	original["annotations"] = map[string]any{"preserved": map[string]any{"name": "Keep this nickname", "favorite": true}}
	original["exportedFiles"] = []any{"existing clip path"}
	original["unrelatedFutureSetting"] = map[string]any{"keep": []any{1, 2, 3}}
	encoded, _ := json.Marshal(original)
	writeFile(t, path, encoded)
	migratedAPI := newAPI(t, Options{DataDir: f.profile})
	migrated := okMap(t, call(t, migratedAPI, "bootstrap"))["fbxOptions"]
	assertJSON(t, migrated, with(m(original["fbxOptions"]), "clipEnabled", false, "enabled", false, "c4dEnabled", true))
	persisted := readJSON(t, path)
	assertJSON(t, persisted["fbxOptions"], migrated)
	delete(persisted, "fbxOptions")
	delete(original, "fbxOptions")
	assertJSON(t, persisted, original)
	chosen := with(m(migrated), "clipEnabled", true, "enabled", true, "c4dEnabled", false)
	ok(t, call(t, migratedAPI, "set_export_settings", chosen))
	assertJSON(t, okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "bootstrap"))["fbxOptions"], chosen)
}

func TestFreshProfileDefaultsToC4DWithoutClipCopy(t *testing.T) {
	f := newFBXFixture(t)
	result := m(okMap(t, call(t, f.api, "bootstrap"))["fbxOptions"])
	if result["clipEnabled"] != false || result["enabled"] != false || result["c4dEnabled"] != true {
		t.Fatal(result)
	}
}

func TestNumberedCollisionNeverReusesOriginalSource(t *testing.T) {
	f := newFBXFixture(t)
	f.options["bakeTextures"] = false
	original := writeFile(t, filepath.Join(f.output, "Wave (2).owanimclip"), readBytes(t, f.source))
	writeFile(t, filepath.Join(f.output, "Wave.owanimclip"), []byte("Existing unrelated clip"))
	before := readBytes(t, original)
	for _, enabled := range []bool{false, true} {
		result := f.export(t, "Wave", with(f.options, "enabled", enabled), original)
		if filepath.Base(result["path"].(string)) != "Wave (3).owanimclip" || !bytes.Equal(readBytes(t, original), before) {
			t.Fatalf("result: %v", result)
		}
		f.api.mu.Lock()
		recorded := listContains(f.api.catalogList("exportedFiles"), key(original))
		f.api.mu.Unlock()
		if recorded {
			t.Fatal("original recorded as exported")
		}
	}
}

func TestExportUsesCapturedEntityAfterViewportLoadsAnother(t *testing.T) {
	f := newFBXFixture(t)
	f.options["bakeTextures"] = false
	other := formatstest.NewModel()
	other.Name = "Other preview model"
	ok(t, call(t, f.api, "load_entity", writeFile(t, filepath.Join(f.root, "Other.owmdl"), other.Bytes())))
	data := readBytes(t, f.export(t, "Wave", nil, "")["fbxPath"].(string))
	if !bytes.Contains(data, []byte("Material Robot")) || bytes.Contains(data, []byte("Other preview model")) {
		t.Fatal("wrong entity exported")
	}
}

func TestMissingEntityFailureDoesNotPublishClipCopy(t *testing.T) {
	f := newFBXFixture(t)
	message := failed(t, call(t, f.api, "export_animation", f.source, "Failed", f.output, with(f.options, "entityPath", "")))
	if !strings.Contains(strings.ToLower(message), "entity") || exists(filepath.Join(f.output, "Failed.owanimclip")) || exists(filepath.Join(f.output, "Failed.fbx")) {
		t.Fatal(message)
	}
}

// tests/test_animation_set_api.py

type setFixture struct {
	*c4dFixture
	arms, gun   string
	assignments []any
}

func newSetFixture(t *testing.T) *setFixture {
	isolate(t)
	base := tempDir(t)
	root := formatstest.GenerateMaterialDemo(t, filepath.Join(base, "Exports 日本語"))
	entity := filepath.Join(root, "Entities/MaterialRobot/MaterialRobot.owentity")
	e := formatstest.NewEntity()
	e.Name, e.Model, e.Children = "Material Robot", "MaterialRobot.00C", []formatstest.Child{{Reference: "Weapon.owentity", Attachment: "bone_0003"}}
	writeFile(t, entity, e.Bytes())
	weapon := formatstest.NewEntity()
	weapon.Name, weapon.Model, weapon.Children = "Weapon", "Weapon.00C", []formatstest.Child{{Reference: "Charm.owentity", Attachment: "bone_0002"}}
	writeFile(t, filepath.Join(root, "Entities/Weapon/Weapon.owentity"), weapon.Bytes())
	model := formatstest.NewModel()
	model.Name = "Weapon.00C"
	writeFile(t, filepath.Join(root, "Models/Weapon.00C/Weapon.owmdl"), model.Bytes())
	charm := formatstest.NewEntity()
	charm.Name, charm.Model = "Charm", "Charm.00C"
	writeFile(t, filepath.Join(root, "Entities/Charm/Charm.owentity"), charm.Bytes())
	model.Name = "Charm.00C"
	writeFile(t, filepath.Join(root, "Models/Charm.00C/Charm.owmdl"), model.Bytes())
	position := func(name string, last uint32, end ...float64) formatstest.ClipBone {
		start := []float64{0, 0, 0}
		if name == "bone_0002" {
			start = []float64{0, 2, 0}
		}
		return formatstest.ClipBone{Name: name, Tracks: []formatstest.Track{{Kind: 0, Keys: []formatstest.Key{
			formatstest.K(0, start...), formatstest.K(last, end...)}}}}
	}
	arms := writeFile(t, filepath.Join(root, "Animations/Arms move.owanimclip"), formatstest.Clip{Bones: []formatstest.ClipBone{
		position("bone_0001", 24, 4, 2, 6), position("bone_0003", 24, 0, 1, 0)}, Duration: 25, FPS: 24, Version: [2]uint16{2, 0}}.Bytes())
	gun := writeFile(t, filepath.Join(root, "Animations/Gun move.owanimclip"), formatstest.Clip{Bones: []formatstest.ClipBone{
		position("bone_0002", 15, 0, 4, 0), position("bone_0075", 15, 9, 9, 9)}, Duration: 16, FPS: 30, Version: [2]uint16{2, 0}}.Bytes())
	profile := filepath.Join(base, "Profile")
	bridge := newMockBridge()
	a := newAPI(t, Options{DataDir: profile, Deps: withBridge(bridge)})
	ok(t, call(t, a, "scan_root", root))
	f := &setFixture{c4dFixture: &c4dFixture{base: base, root: root, profile: profile, entity: entity, api: a, bridge: bridge,
		output: filepath.Join(base, "Scene output 日本語")}, arms: arms, gun: gun}
	f.assignments = []any{map[string]any{"modelId": "model-0", "clipPath": arms}, map[string]any{"modelId": "model-1", "clipPath": gun}}
	f.options = map[string]any{"rootPath": root, "includeMesh": true, "bakeTextures": true, "rootMotion": true,
		"clipEnabled": true, "enabled": true, "c4dEnabled": false}
	return f
}

func (f *setFixture) exportSet(t *testing.T, name string, options map[string]any) map[string]any {
	t.Helper()
	if options == nil {
		options = f.options
	}
	return okMap(t, call(t, f.api, "export_animation_set", f.entity, f.assignments, name, f.output, options))
}

func TestSetPublishesOnlyOneC4DWithAllModelsAndRecordsExport(t *testing.T) {
	f := newSetFixture(t)
	requireBake(t, f.api)
	asset, _ := formats.LoadAsset(f.entity, f.root)
	result := f.exportSet(t, "Reload 日本語", nil)
	c := f.bridge.last(t)
	if result["path"] != filepath.Join(f.output, "Reload 日本語.c4d") || result["path"] != result["c4dPath"] || result["fps"] != float64(30) ||
		result["lastFrame"] != float64(30) || len(l(m(c.Payload["asset"])["models"])) != len(asset.Models) {
		t.Fatalf("result: %v", result)
	}
	assertJSON(t, result["assignments"], m(c.Payload["animation"])["assignments"])
}

func TestSetCollisionNeverOverwritesExistingFiles(t *testing.T) {
	f := newSetFixture(t)
	existing := map[string]string{"Reload.c4d": "Existing Cinema project", "Reload.owanimclip": "Existing source copy", "Reload.fbx": "Existing FBX"}
	for name, data := range existing {
		writeFile(t, filepath.Join(f.output, name), []byte(data))
	}
	result := f.exportSet(t, "Reload.c4d", with(f.options, "bakeTextures", false))
	if filepath.Base(result["path"].(string)) != "Reload (2).c4d" {
		t.Fatal(result["path"])
	}
	for name, data := range existing {
		if string(readBytes(t, filepath.Join(f.output, name))) != data {
			t.Fatalf("%s overwritten", name)
		}
	}
	assertJSON(t, result["files"], []any{filepath.Join(f.output, "Reload (2).c4d")})
	if result["fps"] != float64(30) || result["lastFrame"] != float64(30) {
		t.Fatalf("timing: %v %v", result["fps"], result["lastFrame"])
	}
	payload := f.bridge.last(t).Payload
	assignments := l(m(payload["animation"])["assignments"])
	assertJSON(t, m(assignments[1])["unmatchedBoneNames"], []any{"bone_0075"})
	assertJSON(t, result["assignments"], assignments)
	saved := readJSON(t, filepath.Join(f.profile, "catalog.json"))
	if !listContains(l(saved["exportDirectories"]), key(f.output)) || listContains(l(saved["managedOutputDirectories"]), key(f.output)) {
		t.Fatal("export not recorded correctly (folder existed)")
	}
}

func TestSetNativeFailureDiscardsStaging(t *testing.T) {
	f := newSetFixture(t)
	writeFile(t, filepath.Join(f.output, "Existing.c4d"), []byte("Existing scene"))
	catalog := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	f.bridge.fail = true
	message := failed(t, call(t, f.api, "export_animation_set", f.entity, f.assignments, "Broken", f.output, with(f.options, "bakeTextures", false)))
	if !strings.Contains(message, "Mock native writer failed") || exists(filepath.Dir(f.bridge.last(t).Destination)) {
		t.Fatal(message)
	}
	entries, _ := os.ReadDir(f.output)
	if len(entries) != 1 || !bytes.Equal(catalog, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("outputs or catalog changed")
	}
}

func TestSetMeshAndTextureOptionsRetainMaterialFactors(t *testing.T) {
	f := newSetFixture(t)
	ok(t, call(t, f.api, "set_export_settings", map[string]any{"includeMesh": true, "bakeTextures": true, "rootMotion": false}))
	f.api.deps.BakeMaterials = func(*formats.Asset, string) (Materials, []string, error) {
		t.Fatal("unexpected texture bake")
		return nil, nil, nil
	}
	for _, c := range [][2]bool{{false, true}, {true, false}} {
		result := f.exportSet(t, fmt.Sprint(c[0]), with(f.options, "includeMesh", c[0], "bakeTextures", c[1]))
		payload := f.bridge.last(t).Payload
		if payload["includeMesh"] != c[0] || result["textureCount"] != float64(0) || len(m(payload["materials"])) == 0 {
			t.Fatalf("payload: %v", result)
		}
		for _, materials := range m(payload["materials"]) {
			for _, material := range m(materials) {
				if len(m(m(material)["maps"])) != 0 {
					t.Fatal("maps")
				}
			}
		}
	}
}

func TestSetPreviewInPlaceIgnoredAndRootMotionControlsExport(t *testing.T) {
	f := newSetFixture(t)
	var payloads []map[string]any
	for _, c := range []struct {
		name       string
		lock, root bool
	}{{"Free", false, true}, {"Preview locked", true, true}, {"Export locked", false, false}} {
		f.exportSet(t, c.name, with(f.options, "bakeTextures", false, "lockRoot", c.lock, "rootMotion", c.root))
		payloads = append(payloads, f.bridge.last(t).Payload)
	}
	assertJSON(t, payloads[0]["animation"], payloads[1]["animation"])
	rootFrames := func(payload map[string]any) []any {
		for _, track := range l(m(payload["animation"])["tracks"]) {
			if m(track)["modelId"] == "model-0" && m(track)["boneIndex"] == float64(0) {
				return l(m(track)["frames"])
			}
		}
		t.Fatal("no root track")
		return nil
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	free, locked := rootFrames(payloads[0]), rootFrames(payloads[2])
	fv := func(frames []any, i, k int) float64 {
		if i < 0 {
			i = len(frames) + i
		}
		return l(frames[i])[k].(float64)
	}
	if !near(fv(free, -1, 12)-fv(free, 0, 12), 4) || !near(fv(free, -1, 14)-fv(free, 0, 14), 6) ||
		!near(fv(locked, -1, 12), fv(locked, 0, 12)) || !near(fv(locked, -1, 14), fv(locked, 0, 14)) ||
		!near(fv(locked, -1, 13)-fv(locked, 0, 13), 2) {
		t.Fatal("root motion")
	}
}

func TestSetInvalidInputsCreateNoOutput(t *testing.T) {
	f := newSetFixture(t)
	badClip := writeFile(t, filepath.Join(f.root, "Animations/Corrupt.owanimclip"), []byte("broken"))
	noMatch := writeFile(t, filepath.Join(f.root, "Animations/Other rig.owanimclip"), formatstest.Clip{Bones: []formatstest.ClipBone{
		{Name: "bone_FFFF", Tracks: []formatstest.Track{{Kind: 0, Keys: []formatstest.Key{formatstest.K(0, 0, 0, 0)}}}}},
		Duration: 31, FPS: 30, Version: [2]uint16{2, 0}}.Bytes())
	one := func(model, clip string) []any { return []any{map[string]any{"modelId": model, "clipPath": clip}} }
	cases := []map[string]any{
		{"assignments": map[string]any{}}, {"assignments": []any{}}, {"assignments": nil}, {"assignments": []any{"bad"}},
		{"assignments": []any{f.assignments[0], f.assignments[0]}}, {"assignments": one("missing", f.arms)},
		{"assignments": one("model-0", filepath.Join(f.root, "missing.owanimclip"))}, {"assignments": one("model-0", "relative.owanimclip")},
		{"assignments": one("model-0", f.entity)}, {"assignments": one("model-0", badClip)}, {"assignments": one("model-0", noMatch)},
		{"scene_name": ""}, {"scene_name": "../outside"}, {"scene_name": "CON"},
		{"entity_path": "relative.owentity"}, {"entity_path": filepath.Join(f.root, "missing.owentity")},
		{"options": []any{}}, {"options": with(f.options, "rootPath", "relative")}, {"output_folder": "relative"},
	}
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	for _, override := range cases {
		args := map[string]any{"entity_path": f.entity, "assignments": f.assignments, "scene_name": "Invalid",
			"output_folder": f.output, "options": f.options}
		for k, v := range override {
			args[k] = v
		}
		failed(t, call(t, f.api, "export_animation_set", args["entity_path"], args["assignments"], args["scene_name"], args["output_folder"], args["options"]))
		if exists(f.output) || len(f.bridge.calls) != 0 || f.bridge.statusCalls != 0 || !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
			t.Fatalf("%v: side effects", override)
		}
	}
}

func TestSetOfflineBridgeLeavesNoOutput(t *testing.T) {
	f := newSetFixture(t)
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	f.bridge.status = &mockStatus{connected: false, message: "Cinema is not connected."}
	if message := failed(t, call(t, f.api, "export_animation_set", f.entity, f.assignments, "Offline", f.output, f.options)); message != "Cinema is not connected." {
		t.Fatal(message)
	}
	if exists(f.output) || !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("side effects")
	}
}
