package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"avolition/gocore/internal/pycompat"
)

// tests/test_catalog_migration.py: migration never touches the real profile.

type migration struct {
	workspace, local, legacy, current, root, clip, output string
	annotation                                            map[string]any
	catalog                                               map[string]any
}

func newMigration(t *testing.T) *migration {
	isolate(t)
	s := &migration{workspace: tempDir(t)}
	s.local = filepath.Join(s.workspace, "LocalAppData")
	if err := os.MkdirAll(s.local, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", s.local)
	s.legacy = filepath.Join(s.local, "OWAnimationBrowser", "catalog.json")
	s.current = filepath.Join(s.local, "Avolition", "catalog.json")
	s.root = filepath.Join(s.workspace, "Exported Hero")
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatal(err)
	}
	s.clip = filepath.Join(s.root, "00001.owanimclip")
	s.output = filepath.Join(s.workspace, "Renamed 日本語")
	s.annotation = map[string]any{"name": "Salut café 日本語", "notes": "Keep these notes\nПривет", "favorite": true}
	s.catalog = map[string]any{
		"lastRoot":                 s.root,
		"annotations":              map[string]any{key(s.clip): s.annotation},
		"outputFolder":             s.output,
		"outputFolders":            map[string]any{key(s.root): s.output},
		"exportDirectories":        []any{key(s.output)},
		"exportedFiles":            []any{key(filepath.Join(s.output, "Saved.owanimclip"))},
		"managedOutputDirectories": []any{key(s.output)},
	}
	return s
}

// write is json.dumps(value, ensure_ascii=False, indent=2) with Python's
// dict order as given by the keys list.
func (s *migration) write(t *testing.T, path string, value map[string]any, order ...string) []byte {
	t.Helper()
	if order == nil {
		order = []string{"lastRoot", "annotations", "outputFolder", "outputFolders", "exportDirectories",
			"exportedFiles", "managedOutputDirectories"}
	}
	var parts []string
	for _, name := range order {
		if v, ok := value[name]; ok {
			encoded, err := pycompat.Dumps(pyValue(t, v), 2)
			if err != nil {
				t.Fatal(err)
			}
			nameText, _ := pycompat.Dumps(name, -1)
			parts = append(parts, "  "+string(nameText)+": "+strings.ReplaceAll(string(encoded), "\n", "\n  "))
		}
	}
	data := []byte("{\n" + strings.Join(parts, ",\n") + "\n}")
	// Match the reference fixture's Path.write_text newline conversion.
	if runtime.GOOS == "windows" {
		data = []byte(strings.ReplaceAll(string(data), "\n", "\r\n"))
	}
	writeFile(t, path, data)
	return data
}

func (s *migration) data(t *testing.T, a *API) map[string]any {
	t.Helper()
	return okMap(t, call(t, a, "bootstrap"))
}

func desktop(t *testing.T, dataDir string) *API {
	return newAPI(t, Options{DataDir: dataDir, DesktopMode: true})
}

func mtime(t *testing.T, path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

func TestMigrationPreservesLabelsNotesSettingsAndOriginal(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	legacyTime := mtime(t, s.legacy)
	a := desktop(t, "")
	restored := s.data(t, a)
	if restored["lastRoot"] != s.root || restored["outputFolder"] != s.output || restored["browserMode"] != false || restored["warning"] != nil {
		t.Fatalf("restored: %v", restored)
	}
	assertJSON(t, m(restored["catalog"])[key(s.clip)], s.annotation)
	if !bytes.Equal(readBytes(t, s.current), legacyBytes) {
		t.Fatal("migrated catalog differs from the legacy bytes")
	}
	updated := map[string]any{"name": "New nickname", "notes": "New notes", "favorite": false}
	ok(t, call(t, a, "save_annotation", s.clip, updated))
	assertJSON(t, m(s.data(t, desktop(t, ""))["catalog"])[key(s.clip)], updated)
	if !bytes.Equal(readBytes(t, s.legacy), legacyBytes) || !mtime(t, s.legacy).Equal(legacyTime) {
		t.Fatal("legacy catalog changed")
	}
}

func TestExistingAvolitionCatalogWinsOverLegacy(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	newer := map[string]any{}
	for k, v := range s.catalog {
		newer[k] = v
	}
	newer["annotations"] = map[string]any{key(s.clip): map[string]any{"name": "Newer OWIO label", "notes": "Do not overwrite", "favorite": false}}
	currentBytes := s.write(t, s.current, newer)
	currentTime := mtime(t, s.current)
	assertJSON(t, s.data(t, desktop(t, ""))["catalog"], newer["annotations"])
	if !bytes.Equal(readBytes(t, s.current), currentBytes) || !mtime(t, s.current).Equal(currentTime) {
		t.Fatal("current catalog changed")
	}
	if !bytes.Equal(readBytes(t, s.legacy), legacyBytes) {
		t.Fatal("legacy changed")
	}
}

func TestExplicitDataDirectorySkipsMigration(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	state := filepath.Join(s.workspace, "Isolated State")
	restored := s.data(t, desktop(t, state))
	assertJSON(t, restored["catalog"], map[string]any{})
	if restored["lastRoot"] != "" || exists(filepath.Join(state, "catalog.json")) || exists(s.current) {
		t.Fatal("explicit profile migrated")
	}
	if !bytes.Equal(readBytes(t, s.legacy), legacyBytes) {
		t.Fatal("legacy changed")
	}
}

func TestEnvironmentDataDirectoriesSkipMigration(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	for _, variable := range []string{"AVOLITION_DATA_DIR", "WHITEHOUND_DATA_DIR", "OWIO_DATA_DIR", "OWPREVIEW_DATA_DIR"} {
		isolated := filepath.Join(s.workspace, variable)
		t.Setenv(variable, isolated)
		a := desktop(t, "")
		assertJSON(t, s.data(t, a)["catalog"], map[string]any{})
		ok(t, call(t, a, "save_annotation", s.clip, map[string]any{"name": variable}))
		os.Unsetenv(variable)
		if !exists(filepath.Join(isolated, "catalog.json")) || exists(s.current) {
			t.Fatalf("%s: wrong profile", variable)
		}
	}
	if !bytes.Equal(readBytes(t, s.legacy), legacyBytes) {
		t.Fatal("legacy changed")
	}
}

func TestOWIOCatalogIsPreferredAndPreserved(t *testing.T) {
	s := newMigration(t)
	oldest := s.write(t, s.legacy, s.catalog)
	owio := filepath.Join(s.local, "OWIO", "catalog.json")
	newer := map[string]any{}
	for k, v := range s.catalog {
		newer[k] = v
	}
	newer["annotations"] = map[string]any{key(s.clip): map[string]any{"name": "Latest nickname", "notes": "Retain legacy notes", "favorite": true}}
	previous := s.write(t, owio, newer)
	assertJSON(t, s.data(t, desktop(t, ""))["catalog"], newer["annotations"])
	if !bytes.Equal(readBytes(t, s.current), previous) || !bytes.Equal(readBytes(t, owio), previous) || !bytes.Equal(readBytes(t, s.legacy), oldest) {
		t.Fatal("catalog bytes changed")
	}
}

func TestEnvironmentOverridePrecedence(t *testing.T) {
	s := newMigration(t)
	for _, c := range []struct{ selected, other string }{
		{"WHITEHOUND_DATA_DIR", "OWIO_DATA_DIR"}, {"AVOLITION_DATA_DIR", "WHITEHOUND_DATA_DIR"}, {"OWIO_DATA_DIR", "OWPREVIEW_DATA_DIR"},
	} {
		selected := filepath.Join(s.workspace, "sel-"+c.selected)
		other := filepath.Join(s.workspace, "other-"+c.selected)
		t.Setenv(c.selected, selected)
		t.Setenv(c.other, other)
		a := desktop(t, "")
		ok(t, call(t, a, "save_annotation", s.clip, s.annotation))
		os.Unsetenv(c.selected)
		os.Unsetenv(c.other)
		if !exists(filepath.Join(selected, "catalog.json")) || exists(other) {
			t.Fatalf("%s did not win over %s", c.selected, c.other)
		}
	}
}

func TestExplicitArgumentWinsOverEnvironment(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	explicit := filepath.Join(s.workspace, "argument")
	newerEnv := filepath.Join(s.workspace, "owio-env")
	legacyEnv := filepath.Join(s.workspace, "preview-env")
	t.Setenv("AVOLITION_DATA_DIR", newerEnv)
	t.Setenv("WHITEHOUND_DATA_DIR", legacyEnv)
	t.Setenv("OWIO_DATA_DIR", newerEnv)
	t.Setenv("OWPREVIEW_DATA_DIR", legacyEnv)
	ok(t, call(t, desktop(t, explicit), "save_annotation", s.clip, s.annotation))
	if !exists(filepath.Join(explicit, "catalog.json")) || exists(newerEnv) || exists(legacyEnv) || exists(s.current) {
		t.Fatal("wrong profile")
	}
	if !bytes.Equal(readBytes(t, s.legacy), legacyBytes) {
		t.Fatal("legacy changed")
	}
}

func TestInvalidLegacyCatalogDoesNotReplaceOrDeleteOriginal(t *testing.T) {
	s := newMigration(t)
	invalid := []byte(`{"annotations": not valid JSON`)
	writeFile(t, s.legacy, invalid)
	restored := s.data(t, desktop(t, ""))
	assertJSON(t, restored["catalog"], map[string]any{})
	if restored["warning"] != "Could not migrate saved labels: Expecting value: line 1 column 17 (char 16)" {
		t.Fatalf("warning %q", restored["warning"])
	}
	if exists(s.current) || !bytes.Equal(readBytes(t, s.legacy), invalid) {
		t.Fatal("files changed")
	}
}

func TestInvalidExistingCatalogIsNotOverwrittenWithLegacy(t *testing.T) {
	s := newMigration(t)
	legacyBytes := s.write(t, s.legacy, s.catalog)
	broken := []byte(`{"incomplete":`)
	writeFile(t, s.current, broken)
	restored := s.data(t, desktop(t, ""))
	assertJSON(t, restored["catalog"], map[string]any{})
	if restored["warning"] != "Could not read saved labels: Expecting value: line 1 column 15 (char 14)" {
		t.Fatalf("warning %q", restored["warning"])
	}
	if !bytes.Equal(readBytes(t, s.current), broken) || !bytes.Equal(readBytes(t, s.legacy), legacyBytes) {
		t.Fatal("files changed")
	}
}

func TestNoLegacyCatalogStartsClean(t *testing.T) {
	s := newMigration(t)
	a := desktop(t, "")
	restored := s.data(t, a)
	assertJSON(t, restored["catalog"], map[string]any{})
	if restored["browserMode"] != false || restored["appName"] != "Avolition" || restored["version"] != "0.8.24" {
		t.Fatalf("bootstrap: %v", restored)
	}
	ok(t, call(t, a, "save_annotation", s.clip, s.annotation))
	if !exists(s.current) || exists(s.legacy) {
		t.Fatal("wrong files")
	}
}

func TestWhitehoundIsFirstMigrationSourceAndKeepsSettings(t *testing.T) {
	s := newMigration(t)
	whitehound := filepath.Join(s.local, "Whitehound", "catalog.json")
	older := s.write(t, s.legacy, s.catalog)
	owio := s.write(t, filepath.Join(s.local, "OWIO", "catalog.json"), s.catalog)
	customLibrary := filepath.Join(s.workspace, "Shared assets 日本語")
	install := filepath.Join(s.workspace, "Overwatch Install")
	toolchainOutput := filepath.Join(s.workspace, "Toolchain exports café")
	queue := []any{map[string]any{"id": strings.Repeat("a", 32), "name": "Gameplay1P_reload 日本語",
		"entityPath": filepath.Join(s.root, "Gameplay1P.owentity"), "rootPath": s.root,
		"assignments": []any{map[string]any{"modelId": "model-0", "clipPath": s.clip}}}}
	previous := map[string]any{}
	for k, v := range s.catalog {
		previous[k] = v
	}
	previous["libraryDirectory"] = customLibrary
	previous["exportQueue"] = queue
	previous["libraryOptions"] = map[string]any{"sort": "tracks-desc", "hideDuplicates": false}
	previous["librarySortRevision"] = 1
	previous["workflow"] = map[string]any{"overwatchInstall": install, "outputDirectory": toolchainOutput, "activeTab": "toolchain"}
	previous["fbxOptions"] = map[string]any{"clipEnabled": false, "enabled": false, "c4dEnabled": true,
		"includeMesh": false, "bakeTextures": false, "rootMotion": false}
	annotation := map[string]any{}
	for k, v := range s.annotation {
		annotation[k] = v
	}
	annotation["name"] = "Newest Whitehound nickname"
	previous["annotations"] = map[string]any{key(s.clip): annotation}
	original := s.write(t, whitehound, previous, "lastRoot", "annotations", "outputFolder", "outputFolders",
		"exportDirectories", "exportedFiles", "managedOutputDirectories", "libraryDirectory", "exportQueue",
		"libraryOptions", "librarySortRevision", "workflow", "fbxOptions")
	originalTime := mtime(t, whitehound)
	a := desktop(t, "")
	restored := s.data(t, a)
	assertJSON(t, restored["catalog"], previous["annotations"])
	assertJSON(t, restored["exportQueue"], queue)
	assertJSON(t, restored["libraryOptions"], previous["libraryOptions"])
	workflow := m(restored["workflow"])
	if restored["outputFolder"] != s.output || workflow["libraryDirectory"] != customLibrary || workflow["outputDirectory"] != toolchainOutput ||
		workflow["overwatchInstall"] != install || workflow["activeTab"] != "toolchain" {
		t.Fatalf("restored: %v", restored)
	}
	fbx := m(restored["fbxOptions"])
	if fbx["rootMotion"] != false || fbx["includeMesh"] != false || fbx["bakeTextures"] != false {
		t.Fatalf("fbx: %v", fbx)
	}
	if !bytes.Equal(readBytes(t, s.current), original) {
		t.Fatal("migrated bytes differ")
	}
	if a.BridgeRoot() != filepath.Join(s.local, "Whitehound", "c4d-bridge") {
		t.Fatalf("bridge root %s", a.BridgeRoot())
	}
	ok(t, call(t, a, "save_annotation", s.clip, map[string]any{"name": "Changed in Avolition"}))
	restarted := s.data(t, desktop(t, ""))
	if m(m(restarted["catalog"])[key(s.clip)])["name"] != "Changed in Avolition" || restarted["outputFolder"] != s.output {
		t.Fatalf("restart: %v", restarted)
	}
	assertJSON(t, restarted["exportQueue"], queue)
	if !bytes.Equal(readBytes(t, whitehound), original) || !mtime(t, whitehound).Equal(originalTime) ||
		!bytes.Equal(readBytes(t, s.legacy), older) || !bytes.Equal(readBytes(t, filepath.Join(s.local, "OWIO", "catalog.json")), owio) {
		t.Fatal("legacy catalogs changed")
	}
}

func TestExistingAvolitionNeverRecopiesWhitehound(t *testing.T) {
	s := newMigration(t)
	whitehound := filepath.Join(s.local, "Whitehound", "catalog.json")
	old := map[string]any{}
	for k, v := range s.catalog {
		old[k] = v
	}
	old["annotations"] = map[string]any{"old": map[string]any{"name": "Whitehound"}}
	previous := s.write(t, whitehound, old)
	old["annotations"] = map[string]any{"new": map[string]any{"name": "Avolition"}}
	current := s.write(t, s.current, old)
	assertJSON(t, s.data(t, desktop(t, ""))["catalog"], map[string]any{"new": map[string]any{"name": "Avolition"}})
	if !bytes.Equal(readBytes(t, s.current), current) || !bytes.Equal(readBytes(t, whitehound), previous) {
		t.Fatal("catalog bytes changed")
	}
}

func TestExplicitProfileDoesNotCopyWhitehoundOrUseItsBridgeQueue(t *testing.T) {
	s := newMigration(t)
	whitehound := filepath.Join(s.local, "Whitehound", "catalog.json")
	previous := s.write(t, whitehound, s.catalog)
	profile := filepath.Join(s.workspace, "Explicit isolated profile")
	a := desktop(t, profile)
	restored := s.data(t, a)
	assertJSON(t, restored["catalog"], map[string]any{})
	assertJSON(t, restored["exportQueue"], []any{})
	if a.BridgeRoot() != filepath.Join(profile, "c4d-bridge") || exists(filepath.Join(profile, "catalog.json")) || exists(s.current) ||
		!bytes.Equal(readBytes(t, whitehound), previous) {
		t.Fatal("explicit profile not isolated")
	}
}

// fbxOptions without clipEnabled are migrated once and saved; library sort
// tracks-desc moves to object-match once (librarySortRevision).
func TestFormatAndSortMigrationsAreSavedOnce(t *testing.T) {
	isolate(t)
	profile := filepath.Join(tempDir(t), "Profile")
	writeFile(t, filepath.Join(profile, "catalog.json"), []byte(`{"fbxOptions": {"upAxis": "Z", "includeMesh": false}, "libraryOptions": {"sort": "tracks-desc", "extra": 1}}`))
	a := newAPI(t, Options{DataDir: profile})
	boot := okMap(t, call(t, a, "bootstrap"))
	assertJSON(t, boot["fbxOptions"], map[string]any{"enabled": false, "upAxis": "Z", "includeMesh": false, "bakeTextures": true,
		"rootMotion": true, "c4dEnabled": true, "clipEnabled": false})
	assertJSON(t, boot["libraryOptions"], map[string]any{"sort": "object-match", "hideDuplicates": true})
	saved := readJSON(t, filepath.Join(profile, "catalog.json"))
	if saved["librarySortRevision"] != float64(1) || m(saved["libraryOptions"])["extra"] != float64(1) {
		t.Fatalf("saved: %v", saved)
	}
	ok(t, call(t, a, "library_settings", map[string]any{"sort": "tracks-desc"}))
	again := okMap(t, call(t, newAPI(t, Options{DataDir: profile}), "library_settings"))
	if again["sort"] != "tracks-desc" {
		t.Fatalf("explicit choice lost: %v", again)
	}
	// Invalid saved FBX options reset with a warning.
	other := filepath.Join(tempDir(t), "Other")
	writeFile(t, filepath.Join(other, "catalog.json"), []byte(`{"fbxOptions": {"clipEnabled": true, "upAxis": "X"}}`))
	warning := okMap(t, call(t, newAPI(t, Options{DataDir: other}), "bootstrap"))["warning"]
	if warning != "Saved FBX options were invalid and have been reset to their defaults." {
		t.Fatalf("warning %v", warning)
	}
	var unused json.RawMessage
	_ = unused
}
