package workflowpaths

// Ports the workflow-path parts of tests/test_workflow_api.py and compares
// absolute_folder/WorkflowPaths with the Python reference.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

func setup(t *testing.T) (root, appDir string, catalog *ordered.Map[any]) {
	root = pycompat.Resolve(t.TempDir())
	appDir = filepath.Join(root, "Whitehound 日本語")
	os.Mkdir(appDir, 0o777)
	return root, appDir, ordered.New[any]()
}

func mustNew(t *testing.T, catalog Catalog, appDir string) *Paths {
	t.Helper()
	paths, err := New(catalog, appDir)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestPortableDefaultsIgnoreCurrentWorkingDirectory(t *testing.T) {
	_, appDir, catalog := setup(t)
	settings := mustNew(t, catalog, appDir).Settings()
	if settings.OutputDirectory != filepath.Join(appDir, "library") || settings.ToolchainDirectory != filepath.Join(appDir, "toolchain") || settings.ActiveTab != "animations" || settings.ToolchainTab != "heroes" {
		t.Fatalf("%+v", settings)
	}
	cwd, _ := os.Getwd()
	if dir, err := ApplicationDirectory(""); err != nil || filepath.Join(dir, "library") == filepath.Join(cwd, "library") && dir == cwd {
		t.Fatalf("%q %v", dir, err)
	}
	if pycompat.Exists(filepath.Join(appDir, "library")) {
		t.Fatal("library created")
	}
}

func TestCustomSettingsSurviveRestart(t *testing.T) {
	root, appDir, catalog := setup(t)
	output := filepath.Join(root, "Exports with spaces 日本語")
	install := filepath.Join(root, "Overwatch café")
	if _, err := mustNew(t, catalog, appDir).Update(map[string]any{"outputDirectory": output, "overwatchInstall": install, "activeTab": "toolchain"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := pycompat.Dumps(catalog, 2)
	restoredCatalog, _ := pycompat.Loads(saved)
	settings := mustNew(t, restoredCatalog.(*ordered.Map[any]), appDir).Settings()
	if settings.OutputDirectory != output || settings.ActiveTab != "toolchain" || settings.OverwatchInstall != install {
		t.Fatalf("%+v", settings)
	}
}

func TestUseLibraryFollowsPortableMoveAndHonorsConfiguredLibrary(t *testing.T) {
	root, appDir, catalog := setup(t)
	paths := mustNew(t, catalog, appDir)
	paths.Update(map[string]any{"outputDirectory": filepath.Join(root, "Custom")})
	settings, err := paths.UseLibrary()
	if err != nil {
		t.Fatal(err)
	}
	paths.Update(map[string]any{"outputDirectory": settings.OutputDirectory})
	workflow, _ := catalog.Get("workflow")
	if pycompat.Get(workflow, "outputDirectory") != "" {
		t.Fatal(pycompat.Repr(workflow))
	}
	moved := mustNew(t, catalog, filepath.Join(root, "Moved app"))
	if got := moved.Settings().OutputDirectory; got != filepath.Join(root, "Moved app", "library") {
		t.Fatal(got)
	}
	catalog.Set("libraryDirectory", filepath.Join(root, "Configured Library"))
	if got := mustNew(t, catalog, appDir).Settings().OutputDirectory; got != filepath.Join(root, "Configured Library") {
		t.Fatal(got)
	}
}

func TestRejectRelativePathsAndUnknownTabsWithoutResettingGoodValues(t *testing.T) {
	_, appDir, catalog := setup(t)
	paths := mustNew(t, catalog, appDir)
	original := paths.Settings()
	for _, change := range []map[string]any{{"outputDirectory": "relative/path"}, {"activeTab": "invented"}, {"toolchainTab": "maps2"}} {
		if _, err := paths.Update(change); err == nil {
			t.Fatalf("%v accepted", change)
		}
	}
	if paths.Settings() != original {
		t.Fatal("settings changed")
	}
	if _, err := paths.Update(nil); err == nil || err.Error() != "Invalid workflow settings." {
		t.Fatal(err)
	}
}

func TestApplicationDirectoryPackagedLayout(t *testing.T) {
	root := pycompat.Resolve(t.TempDir())
	binary := filepath.Join(root, "Whitehound", "resources", "backend", "owio-core.exe")
	os.MkdirAll(filepath.Dir(binary), 0o777)
	os.WriteFile(binary, nil, 0o777)
	defer func() { executable = os.Executable }()
	executable = func() (string, error) { return binary, nil }
	if dir, err := ApplicationDirectory(""); err != nil || dir != filepath.Join(root, "Whitehound") {
		t.Fatal(dir, err)
	}
	loose := filepath.Join(root, "loose", "owio-core")
	os.MkdirAll(filepath.Dir(loose), 0o777)
	executable = func() (string, error) { return loose, nil }
	if dir, _ := ApplicationDirectory(""); dir != filepath.Join(root, "loose") {
		t.Fatal(dir)
	}
	source := filepath.Join(root, "checkout")
	os.MkdirAll(filepath.Join(source, "owpreview"), 0o777)
	os.WriteFile(filepath.Join(source, "app.py"), nil, 0o666)
	os.MkdirAll(filepath.Join(source, "gocore", "bin"), 0o777)
	executable = func() (string, error) { return filepath.Join(source, "gocore", "bin", "owio-core"), nil }
	if dir, _ := ApplicationDirectory(""); dir != source {
		t.Fatal(dir)
	}
	if dir, err := ApplicationDirectory("  '" + root + "/x/../y'  "); err != nil || dir != filepath.Join(root, "y") {
		t.Fatal(dir, err)
	}
	if _, err := ApplicationDirectory("relative"); err == nil || err.Error() != "Application folder: use a full, absolute folder path." {
		t.Fatal(err)
	}
}

func TestOracleAbsoluteFolder(t *testing.T) {
	root := pycompat.Resolve(t.TempDir())
	os.Mkdir(filepath.Join(root, "real"), 0o777)
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link"))
	t.Setenv("AVOL_ROOT", root)
	t.Setenv("HOME", filepath.Join(root, "home dir"))
	inputs := []any{nil, "", "   ", 0, 5, 1.5, true, false, []any{}, []any{1}, map[string]any{}, "relative", "/abs/path", "'/quoted path'",
		"\"" + root + "/x\"", "'", "''", "\"'", "  ' " + root + "/inner '  ", "$AVOL_ROOT/x", "${AVOL_ROOT}/y", "$AVOL_MISSING/x",
		"~", "~/lib", "~root/x", "~no_such_user_zz/x", root + "/a/../b", root + "/a/./b//c/", root + "/link/sub", "  /spaced  ",
		"/x/$AVOL_ROOT", "${}", "$", "/a/~/b", "~$AVOL_MISSING"}
	code := `
from owpreview.workflow_paths import absolute_folder
result = []
for value in args[0]:
    try:
        result.append({'ok': True, 'value': absolute_folder(value, 'Label')})
    except Exception as error:
        result.append({'ok': False, 'value': str(error)})
`
	var want []struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
	}
	json.Unmarshal(oracle.Exec(t, code, inputs).MustValue(t), &want)
	for i, input := range inputs {
		value := input
		if f, ok := input.(float64); ok {
			value = json.Number(pycompat.ReprFloat(f))
		} else if n, ok := input.(int); ok {
			value = json.Number(pycompat.Repr(n))
		}
		got, err := AbsoluteFolder(value, "Label")
		if want[i].OK != (err == nil) || (err == nil && got != want[i].Value) || (err != nil && err.Error() != want[i].Value) {
			t.Errorf("%#v: got %q %v, want %+v", input, got, err, want[i])
		}
	}
}

func TestOracleWorkflowPaths(t *testing.T) {
	root := pycompat.Resolve(t.TempDir())
	appDir := filepath.Join(root, "App 日本語")
	steps := []any{
		map[string]any{"outputDirectory": root + "/Exports"},
		map[string]any{"overwatchInstall": "  '" + root + "/Game café'  ", "activeTab": "maps", "toolchainTab": "maps"},
		map[string]any{"activeTab": "invented"},
		map[string]any{"outputDirectory": appDir + "/library"},
		[]any{"not a dict"},
		map[string]any{"outputDirectory": "relative"},
		map[string]any{"toolchainTab": nil},
		map[string]any{"overwatchInstall": "", "outputDirectory": ""},
		map[string]any{"unknown": 1, "activeTab": "toolchain"},
	}
	rootJSON, _ := json.Marshal(root)
	quotedRoot := string(rootJSON[1 : len(rootJSON)-1])
	catalogs := []string{`{}`, `{"workflow": "broken"}`, `{"workflow": {"overwatchInstall": 5, "outputDirectory": null, "activeTab": 3, "toolchainTab": "maps"}}`,
		`{"libraryPath": "` + quotedRoot + `/Lib Path", "lastRoot": "/elsewhere"}`, `{"libraryDirectory": "", "libraryPath": "` + quotedRoot + `/Lib"}`,
		`{"z": 1, "workflow": {"extra": true}, "a": 2}`}
	code := `
import json
from owpreview.workflow_paths import WorkflowPaths
result = []
for text in args[2]:
    catalog = json.loads(text)
    paths = WorkflowPaths(catalog, args[0])
    out = [paths.settings()]
    for step in args[1]:
        try:
            out.append(paths.update(step))
        except Exception as error:
            out.append(str(error))
    out.append(paths.use_library())
    result.append({'steps': out, 'catalog': json.dumps(catalog, ensure_ascii=False, indent=2)})
`
	var want []struct {
		Steps   []json.RawMessage `json:"steps"`
		Catalog string            `json:"catalog"`
	}
	json.Unmarshal(oracle.Exec(t, code, appDir, steps, catalogs).MustValue(t), &want)
	for i, text := range catalogs {
		value, _ := pycompat.Loads([]byte(text))
		catalog := value.(*ordered.Map[any])
		paths := mustNew(t, catalog, appDir)
		got := []any{paths.Settings()}
		for _, step := range steps {
			if settings, err := paths.Update(step); err != nil {
				got = append(got, err.Error())
			} else {
				got = append(got, settings)
			}
		}
		settings, _ := paths.UseLibrary()
		got = append(got, settings)
		options := jsoncmp.Default
		if i == 2 {
			// A hand-edited non-string install (5) stays raw in Python; Go
			// settings are typed strings ("5"). Both fail path validation alike.
			options.Ignore = []string{"$.overwatchInstall"}
		}
		for j := range got {
			jsoncmp.Equal(t, want[i].Steps[j], got[j], options)
		}
		dumped, _ := pycompat.Dumps(catalog, 2)
		if i != 2 && string(dumped) != want[i].Catalog {
			t.Errorf("catalog %d:\n got %s\nwant %s", i, dumped, want[i].Catalog)
		}
	}
}
