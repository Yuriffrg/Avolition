package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/pycompat"
)

// tests/test_api.py

func TestScanPersistenceAndRepeatedLoad(t *testing.T) {
	isolate(t)
	root := demoAssets(t)
	state := tempDir(t)
	a := newAPI(t, Options{DataDir: state})
	failed(t, call(t, a, "scan_root", ""))
	scan := okMap(t, call(t, a, "scan_root", root))
	clips := l(scan["clips"])
	if len(clips) != 3 {
		t.Fatalf("clips: %d", len(clips))
	}
	var entity string
	for _, item := range l(scan["entities"]) {
		if m(item)["kind"] == "entity" {
			entity = m(item)["path"].(string)
			break
		}
	}
	for range 3 {
		ok(t, call(t, a, "load_entity", entity))
		for _, clip := range clips {
			ok(t, call(t, a, "load_animation", m(clip)["path"]))
		}
	}
	clip := m(clips[0])["path"].(string)
	original := readBytes(t, clip)
	note := map[string]any{"name": "Wave <test>", "notes": "Saved locally", "favorite": true}
	assertJSON(t, ok(t, call(t, a, "save_annotation", clip, note)), note)
	restored := okMap(t, call(t, newAPI(t, Options{DataDir: state}), "scan_root", root))
	assertJSON(t, m(l(restored["clips"])[0])["annotation"], note)
	if !bytes.Equal(readBytes(t, clip), original) {
		t.Fatal("source clip changed")
	}
}

func TestBadFileDoesNotAbortScan(t *testing.T) {
	isolate(t)
	root := tempDir(t)
	writeFile(t, filepath.Join(root, "bad.owanimclip"), []byte("bad"))
	a := newAPI(t, Options{DataDir: filepath.Join(root, "state")})
	result := okMap(t, call(t, a, "scan_root", root))
	if _, has := m(l(result["clips"])[0])["error"]; !has {
		t.Fatal("bad clip has no error")
	}
	failed(t, call(t, a, "load_animation", filepath.Join(root, "missing.owanimclip")))
	failed(t, call(t, a, "load_entity", filepath.Join(root, "missing.owentity")))
}

// tests/test_stdio.py

func exchange(t *testing.T, a *API, requests []any) []map[string]any {
	t.Helper()
	var input strings.Builder
	for _, request := range requests {
		if text, isText := request.(string); isText {
			input.WriteString(text)
		} else {
			line, _ := json.Marshal(request)
			input.Write(line)
		}
		input.WriteByte('\n')
	}
	var output bytes.Buffer
	if err := a.ServeStdio(strings.NewReader(input.String()), &output); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("bad reply line %q: %v", line, err)
		}
		replies = append(replies, reply)
	}
	if len(replies) != len(requests) {
		t.Fatalf("%d replies for %d requests:\n%s", len(replies), len(requests), output.String())
	}
	return replies
}

func request(id any, method string, args ...any) map[string]any {
	if args == nil {
		args = []any{}
	}
	return map[string]any{"id": id, "method": method, "args": args}
}

func replyData(t *testing.T, reply map[string]any, id any) any {
	t.Helper()
	if !jsonEqual(t, reply["id"], id) {
		t.Fatalf("id %v, want %v", reply["id"], id)
	}
	result := m(reply["result"])
	if result["ok"] != true {
		t.Fatalf("failed: %v", reply)
	}
	return result["data"]
}

func stdioAPI(t *testing.T) (*API, string) {
	isolate(t)
	workspace := tempDir(t)
	return newAPI(t, Options{DataDir: filepath.Join(workspace, "state"), DesktopMode: true}), workspace
}

func TestStdioNativeBootstrapAndRequestIDs(t *testing.T) {
	a, _ := stdioAPI(t)
	replies := exchange(t, a, []any{request(1, "bootstrap"), request("日本語-id", "bootstrap")})
	for i, id := range []any{1, "日本語-id"} {
		data := m(replyData(t, replies[i], id))
		if data["browserMode"] != false || data["appName"] != "Avolition" {
			t.Fatalf("bootstrap: %v", data)
		}
	}
}

func TestStdioUnknownPrivateMethodsAreRejected(t *testing.T) {
	a, _ := stdioAPI(t)
	var requests []any
	for i, method := range []string{"does_not_exist", "_save", "__getattribute__", "__class__"} {
		requests = append(requests, request(i+1, method))
	}
	requests = append(requests, request("still-alive", "bootstrap"))
	replies := exchange(t, a, requests)
	for _, reply := range replies[:4] {
		result := m(reply["result"])
		if result["ok"] != false || result["error"] == "" {
			t.Fatalf("accepted: %v", reply)
		}
	}
	replyData(t, replies[4], "still-alive")
}

func TestStdioMalformedRequestsDoNotEndSession(t *testing.T) {
	a, workspace := stdioAPI(t)
	malformed := []any{
		"{bad json", "[]", "null", `"not an object"`,
		map[string]any{"id": "object-args", "method": "scan_root", "args": map[string]any{}},
		map[string]any{"id": "null-args", "method": "bootstrap", "args": nil},
		map[string]any{"id": "string-args", "method": "bootstrap", "args": "bad"},
		map[string]any{"id": "numeric-method", "method": 123, "args": []any{}},
		map[string]any{"id": "missing-method", "args": []any{}},
		request("missing-arguments", "save_annotation"),
		request("too-many-arguments", "bootstrap", "extra"),
		request("bad-annotation", "save_annotation", filepath.Join(workspace, "clip.owanimclip"), "text"),
	}
	replies := exchange(t, a, append(malformed, request("recovered", "bootstrap")))
	for i, item := range malformed {
		result := m(replies[i]["result"])
		if result["ok"] != false || result["error"] == "" {
			t.Fatalf("%v accepted: %v", item, replies[i])
		}
		if dict, isDict := item.(map[string]any); isDict && replies[i]["id"] != dict["id"] {
			t.Fatalf("%v: id %v", item, replies[i]["id"])
		}
	}
	replyData(t, replies[len(replies)-1], "recovered")
	if got := m(replies[9]["result"])["error"]; got != "BrowserAPI.save_annotation() missing 2 required positional arguments: 'path' and 'annotation'" {
		t.Fatalf("arity message %q", got)
	}
	if got := m(replies[10]["result"])["error"]; got != "BrowserAPI.bootstrap() takes 1 positional argument but 2 were given" {
		t.Fatalf("arity message %q", got)
	}
}

func TestStdioUTF8LabelsPathsAndExportRoundTrip(t *testing.T) {
	a, workspace := stdioAPI(t)
	root := filepath.Join(workspace, "Héro 日本語")
	source := filepath.Join(root, "00001.owanimclip")
	original := readBytes(t, filepath.Join(demoAssets(t), "Animations", "001_Wave.owanimclip"))
	writeFile(t, source, original)
	output := filepath.Join(workspace, "Choisis 日本語")
	annotation := map[string]any{"name": "Salut café 日本語 👋", "notes": "First line\nПривет — deuxième ligne", "favorite": true}
	replies := exchange(t, a, []any{
		request("scan", "scan_root", root),
		request("name", "save_annotation", source, annotation),
		request("folder", "set_output_folder", output),
		request("copy", "export_animation", source, annotation["name"], output,
			map[string]any{"clipEnabled": true, "enabled": false, "c4dEnabled": false}),
		request("clip", "load_animation", source),
		request("restore", "bootstrap"),
	})
	for i, id := range []string{"scan", "name", "folder", "copy", "clip", "restore"} {
		replyData(t, replies[i], id)
	}
	assertJSON(t, replyData(t, replies[1], "name"), annotation)
	copied := m(replyData(t, replies[3], "copy"))
	if copied["name"] != annotation["name"].(string)+".owanimclip" || !bytes.Equal(readBytes(t, copied["path"].(string)), original) {
		t.Fatalf("copy: %v", copied)
	}
	containsAnnotation := func(catalog map[string]any) bool {
		for _, value := range catalog {
			if jsonEqual(t, value, annotation) {
				return true
			}
		}
		return false
	}
	if !containsAnnotation(m(m(replyData(t, replies[5], "restore"))["catalog"])) {
		t.Fatal("annotation missing from bootstrap")
	}
	restarted := newAPI(t, Options{DataDir: filepath.Join(workspace, "state"), DesktopMode: true})
	if !containsAnnotation(m(okMap(t, call(t, restarted, "bootstrap"))["catalog"])) {
		t.Fatal("annotation missing after restart")
	}
}

func TestStdioRepeatedEntityModelAndAnimationLoads(t *testing.T) {
	a, _ := stdioAPI(t)
	demo := demoAssets(t)
	entity := filepath.Join(demo, "Entities", "PreviewRobot", "PreviewRobot.owentity")
	model := filepath.Join(demo, "Models", "PreviewRobot.00C", "PreviewRobot.owmdl")
	clips, _ := filepath.Glob(filepath.Join(demo, "Animations", "*.owanimclip"))
	if len(clips) != 3 {
		t.Fatalf("clips: %v", clips)
	}
	requests := []any{request("scan", "scan_root", demo)}
	for repeat := range 3 {
		requests = append(requests, request("entity", "load_entity", entity), request("model", "load_entity", model))
		_ = repeat
		for _, clip := range clips {
			requests = append(requests, request("clip", "load_animation", clip))
		}
	}
	replies := exchange(t, a, requests)
	for i, item := range requests {
		req := item.(map[string]any)
		data := m(replyData(t, replies[i], req["id"]))
		switch req["method"] {
		case "load_entity":
			if len(l(data["models"])) == 0 {
				t.Fatal("no models")
			}
		case "load_animation":
			if data["boneCount"].(float64) <= 0 || data["durationFrames"].(float64) <= 1 {
				t.Fatalf("clip: %v", data["boneCount"])
			}
		}
	}
}

func TestStdioLoadFailureDoesNotPoisonFollowingRequests(t *testing.T) {
	a, workspace := stdioAPI(t)
	bad := writeFile(t, filepath.Join(workspace, "truncated.owanimclip"), []byte("not a clip"))
	valid := filepath.Join(demoAssets(t), "Animations", "001_Wave.owanimclip")
	replies := exchange(t, a, []any{
		request("broken", "load_animation", bad),
		request("missing", "load_entity", filepath.Join(workspace, "missing.owentity")),
		request("valid", "load_animation", valid),
		request("valid-again", "load_animation", valid),
	})
	if m(replies[0]["result"])["ok"] != false || m(replies[1]["result"])["ok"] != false {
		t.Fatal("broken loads succeeded")
	}
	assertJSON(t, replyData(t, replies[2], "valid"), replyData(t, replies[3], "valid-again"))
}

// tests/test_library_search_api.py

func libraryAPI(t *testing.T) (*API, string, string) {
	isolate(t)
	base := tempDir(t)
	profile := filepath.Join(base, "Profile")
	return newAPI(t, Options{DataDir: profile}), base, profile
}

func TestNativeAndBackendAllowlistsIncludeEveryMethod(t *testing.T) {
	for _, script := range []string{"desktop/main.cjs", "desktop/preload.cjs"} {
		content := string(readBytes(t, filepath.Join(repo(t), script)))
		for name := range methods {
			if !strings.Contains(content, "'"+name+"'") {
				t.Errorf("%s does not allow %s", script, name)
			}
		}
	}
}

func TestBindingAPIReturnsRealNamesAndUnknownErrorsInRequestOrder(t *testing.T) {
	a, base, _ := libraryAPI(t)
	clip := formatstest.Clip{Bones: []formatstest.ClipBone{{Name: "joint.Custom 日本語"},
		{Name: "bone_0075", Tracks: []formatstest.Track{{Kind: 0}}}}, Duration: 31, FPS: 30, Version: [2]uint16{2, 0}}
	source := writeFile(t, filepath.Join(base, "日本語 names.owanimclip"), clip.Bytes())
	requested := []any{source, filepath.Join(base, "missing.owanimclip"), "relative.owanimclip", nil}
	clips := l(okMap(t, call(t, a, "read_clip_bindings", requested))["clips"])
	assertJSON(t, clips[0], map[string]any{"path": source, "boneNames": []any{"joint.Custom 日本語", "bone_0075"}})
	for i, item := range clips {
		if i > 0 && (m(item)["boneNames"] != nil || m(item)["error"] == nil) {
			t.Fatalf("clip %d: %v", i, item)
		}
		if m(item)["path"] != requested[i] {
			t.Fatalf("path %d: %v", i, m(item)["path"])
		}
	}
}

func TestInvalidBindingBatchIsWrappedAsAPIError(t *testing.T) {
	a, _, _ := libraryAPI(t)
	many := make([]any, 129)
	for i := range many {
		many[i] = "relative"
	}
	for _, value := range []any{nil, map[string]any{}, "path.owanimclip", many} {
		if message := failed(t, call(t, a, "read_clip_bindings", value)); !strings.Contains(message, "128") {
			t.Fatalf("%v: %q", value, message)
		}
	}
	assertJSON(t, okMap(t, call(t, a, "read_clip_bindings", []any{})), map[string]any{"clips": []any{}})
}

func TestFreshLibraryDefaultsAgreeAndDoNotWrite(t *testing.T) {
	a, _, profile := libraryAPI(t)
	expected := map[string]any{"sort": "object-match", "hideDuplicates": true}
	assertJSON(t, ok(t, call(t, a, "library_settings")), expected)
	assertJSON(t, okMap(t, call(t, a, "bootstrap"))["libraryOptions"], expected)
	if exists(filepath.Join(profile, "catalog.json")) {
		t.Fatal("reading defaults wrote the catalog")
	}
}

func TestLibraryPartialUpdatesPersistWithoutChangingOtherSettings(t *testing.T) {
	a, base, profile := libraryAPI(t)
	source := writeFile(t, filepath.Join(base, "Clip.owanimclip"), formatstest.NewClip().Bytes())
	annotation := map[string]any{"name": "User reload", "notes": "Keep this label", "favorite": false}
	ok(t, call(t, a, "save_annotation", source, annotation))
	ok(t, call(t, a, "workflow_settings", map[string]any{"activeTab": "toolchain"}))
	original := readJSON(t, filepath.Join(profile, "catalog.json"))
	assertJSON(t, ok(t, call(t, a, "library_settings", map[string]any{"sort": "tracks-desc"})),
		map[string]any{"sort": "tracks-desc", "hideDuplicates": true})
	second := ok(t, call(t, a, "library_settings", map[string]any{"hideDuplicates": false}))
	assertJSON(t, second, map[string]any{"sort": "tracks-desc", "hideDuplicates": false})
	saved := readJSON(t, filepath.Join(profile, "catalog.json"))
	delete(saved, "libraryOptions")
	assertJSON(t, saved, original)
	restarted := newAPI(t, Options{DataDir: profile})
	assertJSON(t, ok(t, call(t, restarted, "library_settings")), second)
	assertJSON(t, okMap(t, call(t, restarted, "bootstrap"))["catalog"], original["annotations"])
}

func TestAllSupportedSortsAreAccepted(t *testing.T) {
	a, _, _ := libraryAPI(t)
	for _, sort := range librarySorts {
		assertJSON(t, ok(t, call(t, a, "library_settings", map[string]any{"sort": sort})),
			map[string]any{"sort": sort, "hideDuplicates": true})
	}
}

func TestBadPreferenceValuesPreserveState(t *testing.T) {
	a, _, profile := libraryAPI(t)
	ok(t, call(t, a, "library_settings", map[string]any{"sort": "name", "hideDuplicates": false}))
	original := readBytes(t, filepath.Join(profile, "catalog.json"))
	expected := ok(t, call(t, a, "library_settings"))
	for _, changes := range []any{[]any{}, "name", 42, map[string]any{"unknown": true}, map[string]any{"sort": "guess-action"},
		map[string]any{"sort": nil}, map[string]any{"sort": []any{}}, map[string]any{"sort": map[string]any{}},
		map[string]any{"hideDuplicates": 0}, map[string]any{"hideDuplicates": 1}, map[string]any{"hideDuplicates": "false"},
		map[string]any{"hideDuplicates": nil}, map[string]any{"sort": "tracks-desc", "hideDuplicates": "true"}} {
		failed(t, call(t, a, "library_settings", changes))
		assertJSON(t, ok(t, call(t, a, "library_settings")), expected)
		if !bytes.Equal(readBytes(t, filepath.Join(profile, "catalog.json")), original) {
			t.Fatalf("%v changed the catalog", changes)
		}
	}
}

func TestMissingOrInvalidLegacyPreferencesUseDefaults(t *testing.T) {
	isolate(t)
	base := tempDir(t)
	for i, saved := range []any{"missing", nil, []any{}, "tracks-desc",
		map[string]any{"sort": "deleted-mode", "hideDuplicates": nil}, map[string]any{"sort": []any{}, "hideDuplicates": "false"}} {
		profile := filepath.Join(base, "Legacy-"+string(rune('a'+i)))
		catalog := map[string]any{"annotations": map[string]any{"source": map[string]any{"name": "Existing nickname"}},
			"outputFolder": filepath.Join(base, "Existing output")}
		if saved != "missing" {
			catalog["libraryOptions"] = saved
		}
		encoded, _ := json.Marshal(catalog)
		path := writeFile(t, filepath.Join(profile, "catalog.json"), encoded)
		a := newAPI(t, Options{DataDir: profile})
		assertJSON(t, ok(t, call(t, a, "library_settings")), map[string]any{"sort": "object-match", "hideDuplicates": true})
		boot := okMap(t, call(t, a, "bootstrap"))
		assertJSON(t, boot["catalog"], catalog["annotations"])
		assertJSON(t, boot["outputFolder"], catalog["outputFolder"])
		if !bytes.Equal(readBytes(t, path), encoded) {
			t.Fatalf("%v: catalog rewritten", saved)
		}
	}
}

// Catalog formatting is json.dumps(indent=2, ensure_ascii=False) and unknown
// keys keep their order.
func TestCatalogPreservesUnknownKeysOrderAndFormatting(t *testing.T) {
	isolate(t)
	profile := filepath.Join(tempDir(t), "Profile")
	saved := `{"zeta": {"b": 1, "a": [1.5, 2e3, -0, true, null, "日本語 é \"q\""]}, "annotations": {}, "lastRoot": "", "custom": 3}`
	writeFile(t, filepath.Join(profile, "catalog.json"), []byte(saved))
	a := newAPI(t, Options{DataDir: profile})
	ok(t, call(t, a, "save_annotation", "/clip.owanimclip", map[string]any{"name": "x"}))
	got := string(readBytes(t, filepath.Join(profile, "catalog.json")))
	value, err := pycompat.LoadsText(saved)
	if err != nil {
		t.Fatal(err)
	}
	_ = value
	for _, fragment := range []string{
		"\"exportQueue\": [],\n  \"zeta\": {\n    \"b\": 1,\n    \"a\": [\n      1.5,\n      2000.0,\n      0,\n      true,\n      null,\n      \"日本語 é \\\"q\\\"\"\n    ]\n  },",
		"\"custom\": 3,\n  \"librarySortRevision\": 1\n}",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("catalog formatting:\n%s\nmissing:\n%s", got, fragment)
		}
	}
	if _, err := os.Stat(filepath.Join(profile, "catalog.tmp")); err == nil {
		t.Fatal("temporary file left behind")
	}
}
