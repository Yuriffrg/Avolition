package api

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/formats/formatstest"
)

// tests/test_c4d_api.py: API transactions with real assets/FBX/textures and
// an explicitly mocked C4D writer.

var (
	fbxMagic = []byte("Kaydara FBX Binary  \x00\x1a\x00")
	pngMagic = []byte("\x89PNG\r\n\x1a\n")
)

type c4dFixture struct {
	base, root, profile, entity, source, output string
	api                                         *API
	bridge                                      *mockBridge
	options                                     map[string]any
}

func newC4DFixture(t *testing.T) *c4dFixture {
	isolate(t)
	f := &c4dFixture{base: tempDir(t)}
	f.root = formatstest.GenerateMaterialDemo(t, filepath.Join(f.base, "Exports"))
	f.profile = filepath.Join(f.base, "Profile")
	f.bridge = newMockBridge()
	f.api = newAPI(t, Options{DataDir: f.profile, Deps: withBridge(f.bridge)})
	ok(t, call(t, f.api, "scan_root", f.root))
	f.entity = filepath.Join(f.root, "Entities/MaterialRobot/MaterialRobot.owentity")
	f.source = filepath.Join(f.root, "Animations/001_Wave.owanimclip")
	f.output = filepath.Join(f.base, "Output")
	f.options = map[string]any{"clipEnabled": true, "enabled": false, "c4dEnabled": true, "upAxis": "Y",
		"includeMesh": true, "bakeTextures": true, "rootMotion": true,
		"entityPath": f.entity, "rootPath": f.root, "rigId": "auto"}
	return f
}

func with(base map[string]any, changes ...any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(changes); i += 2 {
		if changes[i+1] == deleteKey {
			delete(out, changes[i].(string))
			continue
		}
		out[changes[i].(string)] = changes[i+1]
	}
	return out
}

type deleteMarker struct{}

var deleteKey = deleteMarker{}

func (f *c4dFixture) export(t *testing.T, nickname string, options map[string]any, source string) map[string]any {
	t.Helper()
	if options == nil {
		options = f.options
	}
	if source == "" {
		source = f.source
	}
	return okMap(t, call(t, f.api, "export_animation", source, nickname, f.output, options))
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			data, _ := os.ReadFile(path)
			out[path] = string(data)
		}
		return nil
	})
	return out
}

func TestC4DOnlyAndCombinedExportsPublishMatchingNamesAndSharedTextures(t *testing.T) {
	f := newC4DFixture(t)
	requireBake(t, f.api)
	original := snapshotFiles(t, f.root)
	for _, fbxEnabled := range []bool{false, true} {
		nickname := "Native only"
		if fbxEnabled {
			nickname = "Combined"
		}
		result := f.export(t, nickname, with(f.options, "enabled", fbxEnabled), "")
		c := f.bridge.last(t)
		if filepath.Base(result["path"].(string)) != nickname+".owanimclip" || !bytes.Equal(readBytes(t, result["path"].(string)), readBytes(t, f.source)) {
			t.Fatalf("clip: %v", result)
		}
		if filepath.Base(result["c4dPath"].(string)) != nickname+".c4d" || !bytes.Equal(readBytes(t, result["c4dPath"].(string)), mockC4DBytes) {
			t.Fatalf("c4d: %v", result)
		}
		_, hasFBX := result["fbxPath"]
		if hasFBX != fbxEnabled {
			t.Fatalf("fbxPath presence: %v", result)
		}
		if fbxEnabled {
			if !bytes.HasPrefix(readBytes(t, result["fbxPath"].(string)), fbxMagic) || !bytes.HasPrefix(c.StagedFBX, fbxMagic) {
				t.Fatal("FBX not staged/published")
			}
		} else if c.StagedFBX != nil || exists(filepath.Join(f.output, nickname+".fbx")) {
			t.Fatal("unexpected FBX")
		}
		count := int(result["textureCount"].(float64))
		if count == 0 || count != len(c.Textures) || result["textureFolder"] != filepath.Join(f.output, "tex") {
			t.Fatalf("textures: %v", result)
		}
		for name, data := range c.Textures {
			if !bytes.HasPrefix(data, pngMagic) || !bytes.Equal(readBytes(t, filepath.Join(f.output, "tex", name)), data) {
				t.Fatalf("texture %s", name)
			}
		}
		for _, materials := range m(c.Payload["materials"]) {
			for _, material := range m(materials) {
				for _, mapping := range m(m(material)["maps"]) {
					relative := m(mapping)["relativePath"].(string)
					if !strings.HasPrefix(relative, "tex") || !exists(filepath.Join(f.output, relative)) {
						t.Fatalf("map %s", relative)
					}
				}
			}
		}
		if c.Payload["name"] != nickname || c.Payload["includeMesh"] != true || m(c.Payload["animation"])["matchedBones"].(float64) <= 0 {
			t.Fatalf("payload: %v %v", c.Payload["name"], c.Payload["includeMesh"])
		}
		found := false
		for _, warning := range l(result["warnings"]) {
			found = found || strings.Contains(warning.(string), "Mock native writer")
		}
		if !found {
			t.Fatal("bridge warning missing")
		}
		persisted := readJSON(t, filepath.Join(f.profile, "catalog.json"))
		for _, file := range l(result["files"]) {
			if !listContains(l(persisted["exportedFiles"]), key(file.(string))) {
				t.Fatalf("%s not recorded", file)
			}
		}
		if exists(filepath.Dir(c.Destination)) {
			t.Fatal("staging directory retained")
		}
	}
	if fmt.Sprint(original) != fmt.Sprint(snapshotFiles(t, f.root)) {
		t.Fatal("source files changed")
	}
}

func TestIndependentFormatSelectionsPublishOnlySelectedFiles(t *testing.T) {
	f := newC4DFixture(t)
	for _, combo := range [][3]bool{{false, false, false}, {false, false, true}, {false, true, false}, {false, true, true},
		{true, false, false}, {true, false, true}, {true, true, false}, {true, true, true}} {
		clip, fbx, c4d := combo[0], combo[1], combo[2]
		f.output = filepath.Join(f.base, fmt.Sprintf("Output-%d%d%d", b2i(clip), b2i(fbx), b2i(c4d)))
		before := len(f.bridge.calls)
		result := f.export(t, "Wave", with(f.options, "clipEnabled", clip, "enabled", fbx, "c4dEnabled", c4d, "bakeTextures", false), "")
		var extensions []string
		for _, e := range []struct {
			ext string
			on  bool
		}{{"owanimclip", clip}, {"fbx", fbx}, {"c4d", c4d}} {
			if e.on {
				extensions = append(extensions, e.ext)
			}
		}
		if len(extensions) == 0 {
			if result["skipped"] != true || result["path"] != nil || exists(f.output) {
				t.Fatalf("skip: %v", result)
			}
		} else {
			if result["name"] != "Wave."+extensions[0] || result["path"] != filepath.Join(f.output, "Wave."+extensions[0]) {
				t.Fatalf("result: %v", result)
			}
			entries, _ := os.ReadDir(f.output)
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			var want []string
			for _, ext := range extensions {
				want = append(want, "Wave."+ext)
			}
			sort.Strings(names)
			sort.Strings(want)
			if fmt.Sprint(names) != fmt.Sprint(want) || len(l(result["files"])) != len(want) {
				t.Fatalf("files %v want %v", names, want)
			}
		}
		for _, kind := range []struct {
			name     string
			selected bool
		}{{"clip", clip}, {"fbx", fbx}, {"c4d", c4d}} {
			if _, has := result[kind.name+"Path"]; has != kind.selected {
				t.Fatalf("%sPath: %v", kind.name, result)
			}
		}
		if len(f.bridge.calls)-before != b2i(c4d) {
			t.Fatal("bridge calls")
		}
		if clip && !bytes.Equal(readBytes(t, result["clipPath"].(string)), readBytes(t, f.source)) {
			t.Fatal("clip bytes")
		}
	}
}

func b2i(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestAllOffSkipsValidationWithoutTouchingCatalogOrBridge(t *testing.T) {
	f := newC4DFixture(t)
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	result := okMap(t, call(t, f.api, "export_animation", "missing.owanimclip", "", "invalid relative folder",
		map[string]any{"clipEnabled": false, "enabled": false, "c4dEnabled": false}))
	assertJSON(t, result, map[string]any{"skipped": true, "files": []any{}, "path": nil, "name": nil})
	if !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) || exists(f.output) || f.bridge.statusCalls != 0 {
		t.Fatal("state changed")
	}
}

func TestRawClipOnlyNeedsNoEntityOrCinema(t *testing.T) {
	f := newC4DFixture(t)
	result := f.export(t, "Wave", map[string]any{"clipEnabled": true, "enabled": false, "c4dEnabled": false}, "")
	if result["path"] != result["clipPath"] || !bytes.Equal(readBytes(t, result["clipPath"].(string)), readBytes(t, f.source)) || f.bridge.statusCalls != 0 {
		t.Fatalf("result: %v", result)
	}
	assertJSON(t, result["files"], []any{result["clipPath"]})
}

func TestC4DOnlyIgnoresDisabledClipCollisionAndCatalogNeverHidesOriginal(t *testing.T) {
	f := newC4DFixture(t)
	f.output = filepath.Dir(f.source)
	before := readBytes(t, f.source)
	beforeTime := mtime(t, f.source)
	result := f.export(t, "001_Wave", with(f.options, "clipEnabled", false, "bakeTextures", false), "")
	if result["path"] != strings.TrimSuffix(f.source, ".owanimclip")+".c4d" {
		t.Fatalf("path %v", result["path"])
	}
	if _, has := result["clipPath"]; has {
		t.Fatal("clipPath present")
	}
	f.api.mu.Lock()
	recorded := listContains(f.api.catalogList("exportedFiles"), key(f.source))
	f.api.mu.Unlock()
	if recorded || !bytes.Equal(before, readBytes(t, f.source)) || !beforeTime.Equal(mtime(t, f.source)) {
		t.Fatal("original hidden or changed")
	}
}

func TestCapturedEntityAndSharedSettingsReachNativePayload(t *testing.T) {
	f := newC4DFixture(t)
	asset, err := formats.LoadAsset(f.entity, f.root)
	if err != nil {
		t.Fatal(err)
	}
	rig := asset.Models[0]
	rootIndex := 0
	for i, bone := range rig.Bones {
		if bone.Parent < 0 {
			rootIndex = i
			break
		}
	}
	moving := filepath.Join(f.root, "Animations/Root motion.owanimclip")
	clip := formatstest.Clip{Bones: []formatstest.ClipBone{{Name: rig.Bones[rootIndex].Name, Tracks: []formatstest.Track{
		{Kind: 0, Keys: []formatstest.Key{formatstest.K(0, 0, 0, 0), formatstest.K(2, 4, 2, 6)}}}}},
		Duration: 3, FPS: 30, Version: [2]uint16{2, 0}}
	writeFile(t, moving, clip.Bytes())
	other := formatstest.NewModel()
	other.Name = "Other preview model"
	otherPath := writeFile(t, filepath.Join(f.root, "Other.owmdl"), other.Bytes())
	ok(t, call(t, f.api, "load_entity", otherPath))
	ok(t, call(t, f.api, "set_export_settings", map[string]any{"includeMesh": true, "bakeTextures": true, "rootMotion": true, "c4dEnabled": false}))
	for _, c := range [][2]bool{{false, true}, {true, false}} {
		includeMesh, bake := c[0], c[1]
		result := f.export(t, fmt.Sprintf("Captured %v", includeMesh), with(f.options, "upAxis", "Z", "includeMesh", includeMesh,
			"bakeTextures", bake, "rootMotion", false, "rigId", rig.ID), moving)
		payload := f.bridge.last(t).Payload
		if m(payload["asset"])["name"] != asset.Name || m(l(m(payload["asset"])["models"])[0])["name"] != rig.Name {
			t.Fatal("wrong asset")
		}
		if strings.Contains(fmt.Sprint(payload), "Other preview model") || payload["includeMesh"] != includeMesh {
			t.Fatal("wrong payload")
		}
		animation := m(payload["animation"])
		if animation["lastFrame"] != float64(2) {
			t.Fatalf("lastFrame %v", animation["lastFrame"])
		}
		var rootTrack map[string]any
		for _, track := range l(animation["tracks"]) {
			if m(track)["modelId"] != rig.ID {
				t.Fatal("foreign track")
			}
			if m(track)["boneIndex"] == float64(rootIndex) {
				rootTrack = m(track)
			}
		}
		frames := l(rootTrack["frames"])
		first, last := l(frames[0]), l(frames[len(frames)-1])
		if math.Abs(first[12].(float64)-last[12].(float64)) > 1e-6 || math.Abs(first[14].(float64)-last[14].(float64)) > 1e-6 ||
			math.Abs(last[13].(float64)-first[13].(float64)-2) > 1e-6 {
			t.Fatalf("root motion: %v %v", first, last)
		}
		if result["textureCount"] != float64(0) || result["textureFolder"] != nil || len(f.bridge.last(t).Textures) != 0 {
			t.Fatalf("textures: %v", result)
		}
		if len(m(payload["materials"])) == 0 {
			t.Fatal("material factors discarded")
		}
		for _, materials := range m(payload["materials"]) {
			for _, material := range m(materials) {
				if len(m(m(material)["maps"])) != 0 {
					t.Fatal("maps without baking")
				}
			}
		}
	}
	if exists(filepath.Join(f.output, "tex")) {
		t.Fatal("tex folder")
	}
	f.export(t, "Moving root", with(f.options, "bakeTextures", false, "rootMotion", true), moving)
	for _, track := range l(m(f.bridge.last(t).Payload["animation"])["tracks"]) {
		if m(track)["boneIndex"] == float64(rootIndex) {
			frames := l(m(track)["frames"])
			first, last := l(frames[0]), l(frames[len(frames)-1])
			if math.Abs(last[12].(float64)-first[12].(float64)-4) > 1e-6 || math.Abs(last[14].(float64)-first[14].(float64)-6) > 1e-6 {
				t.Fatalf("root motion lost: %v %v", first, last)
			}
		}
	}
}

func TestNativeFailureDiscardsStagedFilesWithoutChangingExisting(t *testing.T) {
	f := newC4DFixture(t)
	requireBake(t, f.api)
	writeFile(t, filepath.Join(f.output, "Wave.owanimclip"), []byte("Unrelated existing clip"))
	writeFile(t, filepath.Join(f.output, "Wave.fbx"), []byte("Unrelated existing FBX"))
	writeFile(t, filepath.Join(f.output, "Wave.c4d"), []byte("Unrelated existing native project"))
	writeFile(t, filepath.Join(f.output, "tex/user.png"), []byte("Unrelated existing texture"))
	existing := snapshotFiles(t, f.output)
	original := snapshotFiles(t, f.root)
	catalogBefore := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	f.bridge.fail = true
	message := failed(t, call(t, f.api, "export_animation", f.source, "Wave", f.output, with(f.options, "enabled", true)))
	if !strings.Contains(message, "Mock native writer failed") {
		t.Fatal(message)
	}
	c := f.bridge.last(t)
	if !bytes.HasPrefix(c.StagedFBX, fbxMagic) || len(c.Textures) == 0 || exists(filepath.Dir(c.Destination)) {
		t.Fatal("failure path not exercised")
	}
	if fmt.Sprint(existing) != fmt.Sprint(snapshotFiles(t, f.output)) || fmt.Sprint(original) != fmt.Sprint(snapshotFiles(t, f.root)) ||
		!bytes.Equal(catalogBefore, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("files changed")
	}
}
