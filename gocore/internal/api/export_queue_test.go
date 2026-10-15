package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/formats/formatstest"
)

// tests/test_export_queue_api.py: persistent named export setups.

type queueFixture struct {
	base, root, profile, entity, arms, gun, output string
	api                                            *API
	bridge                                         *mockBridge
	entry                                          map[string]any
	options                                        map[string]any
}

func newQueueFixture(t *testing.T) *queueFixture {
	isolate(t)
	f := &queueFixture{base: tempDir(t)}
	f.root = filepath.Join(f.base, "Export sources 日本語")
	write := func(name string, data []byte) string { return writeFile(t, filepath.Join(f.root, name), data) }
	for _, name := range []string{"Body", "Weapon", "Charm"} {
		model := formatstest.NewModel()
		model.Name = name + ".00C"
		write(name+".owmdl", model.Bytes())
	}
	entity := func(model string, children ...formatstest.Child) []byte {
		e := formatstest.NewEntity()
		e.Model, e.Relative, e.Children = model, ".", children
		return e.Bytes()
	}
	write("Charm.owentity", entity("Charm.owmdl"))
	write("Weapon.owentity", entity("Weapon.owmdl", formatstest.Child{Reference: "Charm.owentity", Attachment: "bone_0002"}))
	f.entity = write("Main.owentity", entity("Body.owmdl", formatstest.Child{Reference: "Weapon.owentity", Attachment: "bone_0002"}))
	clip := func(bone string, from, to []float64) []byte {
		return formatstest.Clip{Bones: []formatstest.ClipBone{{Name: bone, Tracks: []formatstest.Track{{Kind: 0,
			Keys: []formatstest.Key{formatstest.K(0, from...), formatstest.K(2, to...)}}}}}, Duration: 3, FPS: 30, Version: [2]uint16{2, 0}}.Bytes()
	}
	f.arms = write("Arms.owanimclip", clip("bone_0001", []float64{0, 0, 0}, []float64{1, 0, 0}))
	f.gun = write("Gun.owanimclip", clip("bone_0002", []float64{0, 2, 0}, []float64{0, 4, 0}))
	f.profile = filepath.Join(f.base, "Profile")
	f.bridge = newMockBridge()
	f.api = newAPI(t, Options{DataDir: f.profile, Deps: withBridge(f.bridge)})
	call(t, f.api, "scan_root", f.root)
	f.output = filepath.Join(f.base, "Common output")
	call(t, f.api, "set_output_folder", f.output)
	f.entry = map[string]any{"name": "Reload 日本語", "entityPath": f.entity, "rootPath": f.root,
		"assignments": []any{map[string]any{"modelId": "model-0", "clipPath": f.arms}, map[string]any{"modelId": "model-1", "clipPath": f.gun}}}
	f.options = map[string]any{"includeMesh": true, "bakeTextures": false, "rootMotion": true, "clipEnabled": true, "enabled": true, "c4dEnabled": false}
	return f
}

func (f *queueFixture) save(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	if entry == nil {
		entry = f.entry
	}
	return m(okMap(t, call(t, f.api, "export_queue_save", entry))["entry"])
}

func (f *queueFixture) queue(t *testing.T, a *API) []any {
	t.Helper()
	if a == nil {
		a = f.api
	}
	return l(okMap(t, call(t, a, "bootstrap"))["exportQueue"])
}

func TestQueueAddPersistsSnapshotWithoutExportOrCinema(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(saved["id"].(string)) || exists(f.output) || len(f.bridge.calls) != 0 || f.bridge.statusCalls != 0 {
		t.Fatalf("saved: %v", saved)
	}
	assertJSON(t, saved["assignments"], f.entry["assignments"])
	if _, has := saved["checked"]; has {
		t.Fatal("checked flag")
	}
	queue := f.queue(t, newAPI(t, Options{DataDir: f.profile}))
	if m(queue[0])["name"] != "Reload 日本語" || len(l(m(queue[0])["assignments"])) != 2 {
		t.Fatalf("queue: %v", queue)
	}
}

func TestQueueDuplicateNamesAddEntriesAndOnlyExplicitIDUpdates(t *testing.T) {
	f := newQueueFixture(t)
	first, second := f.save(t, nil), f.save(t, nil)
	if first["id"] == second["id"] {
		t.Fatal("same id")
	}
	changed := f.save(t, with(first, "name", "New name", "assignments", l(first["assignments"])[:1]))
	queue := f.queue(t, nil)
	if len(queue) != 2 || changed["id"] != first["id"] {
		t.Fatalf("queue: %v", queue)
	}
	assertJSON(t, queue[0], changed)
	assertJSON(t, queue[1], second)
	if message := failed(t, call(t, f.api, "export_queue_save", with(f.entry, "id", strings.Repeat("a", 32)))); !strings.Contains(message, "no longer exists") {
		t.Fatal(message)
	}
}

func TestQueueRemoveOnlyRemovesSelectedEntry(t *testing.T) {
	f := newQueueFixture(t)
	first, second := f.save(t, nil), f.save(t, nil)
	assertJSON(t, okMap(t, call(t, f.api, "export_queue_remove", first["id"])), map[string]any{"entries": []any{second}})
	if !exists(f.arms) || !exists(f.gun) || exists(f.output) {
		t.Fatal("files changed")
	}
	assertJSON(t, f.queue(t, newAPI(t, Options{DataDir: f.profile})), []any{second})
	failed(t, call(t, f.api, "export_queue_remove", first["id"]))
}

func TestQueueExplicitRunExportsFullEntity(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, with(f.entry, "assignments", l(f.entry["assignments"])[:1]))
	before := f.queue(t, nil)
	data := okMap(t, call(t, f.api, "export_queue_run", saved["id"], nil, f.options))
	if data["path"] != filepath.Join(f.output, "Reload 日本語.c4d") || !bytes.Equal(readBytes(t, data["path"].(string)), mockC4DBytes) {
		t.Fatalf("data: %v", data)
	}
	if clips, _ := filepath.Glob(filepath.Join(f.output, "*.owanimclip")); len(clips) != 0 {
		t.Fatal("clip published")
	}
	payload := f.bridge.last(t).Payload
	models := l(m(payload["asset"])["models"])
	bones := 0
	for _, model := range models {
		bones += len(l(m(model)["bones"]))
	}
	if len(models) != 3 || bones != 6 {
		t.Fatalf("models %d bones %d", len(models), bones)
	}
	for _, track := range l(m(payload["animation"])["tracks"]) {
		if m(track)["modelId"] != "model-0" {
			t.Fatal("unassigned model animated")
		}
	}
	assertJSON(t, f.queue(t, nil), before)
}

func TestQueueStoredPathsOverrideNewPreviewRootAndCallerRoot(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	elsewhere := filepath.Join(f.base, "Other library")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	ok(t, call(t, f.api, "scan_root", elsewhere))
	if okMap(t, call(t, f.api, "bootstrap"))["outputFolder"] != f.output ||
		okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "bootstrap"))["outputFolder"] != f.output {
		t.Fatal("common output replaced")
	}
	data := okMap(t, call(t, f.api, "export_queue_run", saved["id"], nil, with(f.options, "rootPath", elsewhere)))
	payload := f.bridge.last(t).Payload
	if m(payload["asset"])["path"] != f.entity || filepath.Dir(data["path"].(string)) != f.output {
		t.Fatalf("payload path %v", m(payload["asset"])["path"])
	}
	models := map[any]bool{}
	for _, track := range l(m(payload["animation"])["tracks"]) {
		models[m(track)["modelId"]] = true
	}
	if !models["model-0"] || !models["model-1"] || len(models) != 2 {
		t.Fatalf("tracks: %v", models)
	}
}

func TestQueueMissingFilesOrFailedSaveKeepQueue(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	queue := f.queue(t, nil)
	original := readBytes(t, f.gun)
	os.Remove(f.gun)
	failed(t, call(t, f.api, "export_queue_run", saved["id"], nil, f.options))
	if exists(f.output) {
		t.Fatal("output created")
	}
	assertJSON(t, f.queue(t, nil), queue)
	writeFile(t, f.gun, original)
	existing := writeFile(t, filepath.Join(f.output, "User project.c4d"), []byte("user project"))
	f.bridge.fail = true
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	failed(t, call(t, f.api, "export_queue_run", saved["id"], nil, f.options))
	entries, _ := os.ReadDir(f.output)
	if len(entries) != 1 || string(readBytes(t, existing)) != "user project" || !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("outputs changed")
	}
	assertJSON(t, f.queue(t, nil), queue)
}

func TestQueueConcurrentDuplicateRunIsRejected(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	started, release := make(chan struct{}), make(chan struct{})
	var captured []any
	f.api.exportSetHook = func(entity, assignments, name, output, options any) (any, error) {
		captured = append(captured, name)
		close(started)
		<-release
		return map[string]any{"path": "fixture.c4d", "name": "x.c4d"}, nil
	}
	var wg sync.WaitGroup
	var result Reply
	wg.Add(1)
	go func() { defer wg.Done(); result = call(t, f.api, "export_queue_run", saved["id"]) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	if message := failed(t, call(t, f.api, "export_queue_run", saved["id"])); !strings.Contains(message, "already exporting") {
		t.Fatal(message)
	}
	f.save(t, with(saved, "name", "Updated while exporting"))
	if captured[0] != "Reload 日本語" {
		t.Fatal(captured)
	}
	close(release)
	wg.Wait()
	ok(t, result)
}

func TestQueueCatalogFailureRollsBackEditsInMemory(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	queue := f.queue(t, nil)
	f.api.saveHook = func() error { return errors.New("Disk full") }
	for _, c := range []struct {
		method string
		arg    any
	}{{"export_queue_save", with(f.entry, "name", "New")}, {"export_queue_save", with(saved, "name", "Changed")}, {"export_queue_remove", saved["id"]}} {
		if message := failed(t, call(t, f.api, c.method, c.arg)); !strings.Contains(message, "Disk full") {
			t.Fatal(message)
		}
		assertJSON(t, f.queue(t, nil), queue)
	}
	if !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
		t.Fatal("catalog written")
	}
}

func TestQueueBadInputAndCapDoNotAlterSavedEntries(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	before := readBytes(t, filepath.Join(f.profile, "catalog.json"))
	first := l(f.entry["assignments"])[0]
	repeat := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = first
		}
		return out
	}
	bad := []any{nil, []any{}, with(f.entry, "name", ""), with(f.entry, "name", 42), with(f.entry, "name", "../outside"),
		with(f.entry, "name", "CON"), with(f.entry, "name", strings.Repeat("a", 181)), with(f.entry, "entityPath", "relative.owentity"),
		with(f.entry, "entityPath", filepath.Join(f.root, "missing.owentity")), with(f.entry, "rootPath", "relative"),
		with(f.entry, "rootPath", f.arms), with(f.entry, "assignments", []any{}), with(f.entry, "assignments", map[string]any{}),
		with(f.entry, "assignments", repeat(513)), with(f.entry, "assignments", repeat(2)),
		with(f.entry, "assignments", []any{map[string]any{"modelId": "model-missing", "clipPath": f.arms}}),
		with(f.entry, "assignments", []any{map[string]any{"modelId": "model-0", "clipPath": filepath.Join(f.root, "missing.owanimclip")}}),
		with(f.entry, "checked", true)}
	for _, value := range bad {
		failed(t, call(t, f.api, "export_queue_save", value))
		if !bytes.Equal(before, readBytes(t, filepath.Join(f.profile, "catalog.json"))) {
			t.Fatalf("%v changed the catalog", value)
		}
	}
	maxExportQueue = 1
	defer func() { maxExportQueue = MaxExportQueue }()
	failed(t, call(t, f.api, "export_queue_save", f.entry))
	ok(t, call(t, f.api, "export_queue_save", with(saved, "name", "Update existing")))
	for _, ident := range []any{nil, 42, "", "../outside", "unknown"} {
		failed(t, call(t, f.api, "export_queue_remove", ident))
		failed(t, call(t, f.api, "export_queue_run", ident))
	}
	if exists(f.output) {
		t.Fatal("output created")
	}
}

func TestOpenOutputReturnsOnlySavedExistingCommonDirectory(t *testing.T) {
	f := newQueueFixture(t)
	failed(t, call(t, f.api, "open_export_output"))
	if err := os.MkdirAll(f.output, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok(t, call(t, f.api, "open_export_output")) != f.output {
		t.Fatal("wrong folder")
	}
}

func TestOldDefaultSortMigratesOnceAndNewChoiceSurvives(t *testing.T) {
	f := newQueueFixture(t)
	path := filepath.Join(f.profile, "catalog.json")
	catalog := readJSON(t, path)
	delete(catalog, "librarySortRevision")
	catalog["libraryOptions"] = map[string]any{"sort": "tracks-desc", "hideDuplicates": false}
	catalog["annotations"] = map[string]any{"source": map[string]any{"name": "Keep nickname"}}
	encoded, _ := json.Marshal(catalog)
	writeFile(t, path, encoded)
	migrated := newAPI(t, Options{DataDir: f.profile})
	assertJSON(t, ok(t, call(t, migrated, "library_settings")), map[string]any{"sort": "object-match", "hideDuplicates": false})
	saved := readJSON(t, path)
	if saved["librarySortRevision"] != float64(1) {
		t.Fatal(saved["librarySortRevision"])
	}
	assertJSON(t, saved["annotations"], catalog["annotations"])
	ok(t, call(t, migrated, "library_settings", map[string]any{"sort": "tracks-desc"}))
	if okMap(t, call(t, newAPI(t, Options{DataDir: f.profile}), "library_settings"))["sort"] != "tracks-desc" {
		t.Fatal("choice lost")
	}
}

func TestMalformedSavedQueueDoesNotHideLabels(t *testing.T) {
	f := newQueueFixture(t)
	path := filepath.Join(f.profile, "catalog.json")
	catalog := readJSON(t, path)
	catalog["exportQueue"] = map[string]any{"wrong": "shape"}
	catalog["annotations"] = map[string]any{"source": map[string]any{"name": "Keep nickname"}}
	encoded, _ := json.Marshal(catalog)
	writeFile(t, path, encoded)
	a := newAPI(t, Options{DataDir: f.profile})
	boot := okMap(t, call(t, a, "bootstrap"))
	assertJSON(t, boot["catalog"], catalog["annotations"])
	assertJSON(t, boot["exportQueue"], []any{})
	if !strings.Contains(boot["warning"].(string), "export list") {
		t.Fatal(boot["warning"])
	}
	failed(t, call(t, a, "export_queue_save", f.entry))
	if !bytes.Equal(readBytes(t, path), encoded) {
		t.Fatal("catalog rewritten")
	}
}

func TestMalformedSavedEntryCannotCrashRestore(t *testing.T) {
	f := newQueueFixture(t)
	valid := f.save(t, nil)
	path := filepath.Join(f.profile, "catalog.json")
	original := readJSON(t, path)
	candidate := with(valid, "id", strings.Repeat("a", 32))
	first := l(candidate["assignments"])[0]
	repeat := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = first
		}
		return out
	}
	malformed := []any{nil, []any{}, map[string]any{"id": candidate["id"]}, with(candidate, "id", "invalid"), with(candidate, "id", valid["id"]),
		with(candidate, "name", nil), with(candidate, "name", []any{}), with(candidate, "name", ""), with(candidate, "entityPath", nil),
		with(candidate, "rootPath", []any{}), with(candidate, "assignments", nil), with(candidate, "assignments", map[string]any{}),
		with(candidate, "assignments", []any{}), with(candidate, "assignments", []any{nil}), with(candidate, "assignments", []any{map[string]any{}}),
		with(candidate, "assignments", []any{map[string]any{"modelId": nil, "clipPath": f.arms}}),
		with(candidate, "assignments", []any{map[string]any{"modelId": "", "clipPath": f.arms}}),
		with(candidate, "assignments", []any{map[string]any{"modelId": "model-0", "clipPath": []any{}}}),
		with(candidate, "assignments", repeat(2)), with(candidate, "assignments", repeat(513))}
	for _, damaged := range malformed {
		catalog := with(original, "exportQueue", []any{valid, damaged}, "annotations", map[string]any{"source": map[string]any{"name": "Keep nickname"}})
		encoded, _ := json.Marshal(catalog)
		writeFile(t, path, encoded)
		a := newAPI(t, Options{DataDir: f.profile})
		boot := okMap(t, call(t, a, "bootstrap"))
		assertJSON(t, boot["exportQueue"], []any{})
		assertJSON(t, boot["catalog"], catalog["annotations"])
		if !strings.Contains(boot["warning"].(string), "export list") {
			t.Fatal(boot["warning"])
		}
		failed(t, call(t, a, "export_queue_save", f.entry))
		failed(t, call(t, a, "export_queue_remove", valid["id"]))
		failed(t, call(t, a, "export_queue_run", valid["id"]))
		if !bytes.Equal(readBytes(t, path), encoded) {
			t.Fatalf("%v: catalog rewritten", damaged)
		}
	}
	if exists(f.output) {
		t.Fatal("output created")
	}
}

func TestStructurallyValidEntriesAreRevalidatedBeforeExport(t *testing.T) {
	f := newQueueFixture(t)
	saved := f.save(t, nil)
	missingRoot := filepath.Join(f.base, "Offline sources 日本語")
	missing := with(saved, "rootPath", missingRoot, "entityPath", filepath.Join(missingRoot, "Main.owentity"),
		"assignments", []any{map[string]any{"modelId": "model-0", "clipPath": filepath.Join(missingRoot, "Arms.owanimclip")}})
	path := filepath.Join(f.profile, "catalog.json")
	catalog := readJSON(t, path)
	for _, entry := range []map[string]any{missing, with(saved, "name", "../outside")} {
		catalog["exportQueue"] = []any{entry}
		encoded, _ := json.Marshal(catalog)
		writeFile(t, path, encoded)
		a := newAPI(t, Options{DataDir: f.profile, Deps: withBridge(f.bridge)})
		boot := okMap(t, call(t, a, "bootstrap"))
		assertJSON(t, boot["exportQueue"], []any{entry})
		if boot["warning"] != nil {
			t.Fatal(boot["warning"])
		}
		failed(t, call(t, a, "export_queue_run", entry["id"]))
		if !bytes.Equal(readBytes(t, path), encoded) {
			t.Fatal("catalog changed")
		}
		ok(t, call(t, a, "export_queue_remove", entry["id"]))
		assertJSON(t, l(okMap(t, call(t, a, "bootstrap"))["exportQueue"]), []any{})
	}
	if exists(missingRoot) || exists(f.output) || exists(filepath.Join(f.base, "outside.c4d")) {
		t.Fatal("files created")
	}
}
