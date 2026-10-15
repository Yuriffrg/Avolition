package api

import (
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/datatool"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/toolchain"
	"avolition/gocore/internal/workflowpaths"
)

// tests/test_workflow_api.py

type fakeToolchain struct {
	Toolchain
	executable string
	installs   int
}

func (f *fakeToolchain) Executable() string { return f.executable }
func (f *fakeToolchain) InstallAsync() (toolchain.Snapshot, error) {
	f.installs++
	return toolchain.Snapshot{}, nil
}

type fakeDataTool struct {
	DataTool
	exports [][]any
}

func (f *fakeDataTool) StartExport(executable, install, output string, selection any) (datatool.Snapshot, error) {
	f.exports = append(f.exports, []any{executable, install, output, selection})
	return datatool.Snapshot{}, nil
}

type workflowFixture struct {
	root, appDir string
	api          *API
	tool         *fakeToolchain
	jobs         *fakeDataTool
	deps         Deps
}

func newWorkflow(t *testing.T) *workflowFixture {
	isolate(t)
	f := &workflowFixture{root: tempDir(t)}
	f.appDir = filepath.Join(f.root, "Whitehound 日本語")
	if err := os.MkdirAll(f.appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.deps = Default()
	newToolchain, newDataTool := f.deps.NewToolchain, f.deps.NewDataTool
	f.deps.NewToolchain = func(root string, lock toolchain.ActivityLock) Toolchain {
		f.tool = &fakeToolchain{Toolchain: newToolchain(root, lock)}
		f.tool.executable = f.tool.Toolchain.Executable()
		return f.tool
	}
	f.deps.NewDataTool = func(dir string, lock datatool.ActivityLock) DataTool {
		f.jobs = &fakeDataTool{DataTool: newDataTool(dir, lock)}
		return f.jobs
	}
	f.api = newAPI(t, Options{DataDir: filepath.Join(f.root, "settings"), AppDir: f.appDir, Deps: &f.deps})
	return f
}

func TestPortableDefaultsIgnoreCurrentWorkingDirectory(t *testing.T) {
	f := newWorkflow(t)
	settings := m(okMap(t, call(t, f.api, "bootstrap"))["workflow"])
	if settings["outputDirectory"] != filepath.Join(f.appDir, "library") || settings["toolchainDirectory"] != filepath.Join(f.appDir, "toolchain") ||
		settings["activeTab"] != "animations" || exists(filepath.Join(f.appDir, "library")) {
		t.Fatalf("settings: %v", settings)
	}
}

func TestCustomSettingsAndClipDestinationSurviveRestart(t *testing.T) {
	f := newWorkflow(t)
	output := filepath.Join(f.root, "Exports with spaces 日本語")
	clips := filepath.Join(f.root, "Named clips")
	ok(t, call(t, f.api, "set_output_folder", clips))
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"outputDirectory": output,
		"overwatchInstall": filepath.Join(f.root, "Overwatch café"), "activeTab": "toolchain"}))
	restored := newAPI(t, Options{DataDir: filepath.Join(f.root, "settings"), AppDir: f.appDir})
	boot := okMap(t, call(t, restored, "bootstrap"))
	workflow := m(boot["workflow"])
	if workflow["outputDirectory"] != output || workflow["activeTab"] != "toolchain" || boot["outputFolder"] != clips ||
		workflow["overwatchInstall"] != filepath.Join(f.root, "Overwatch café") {
		t.Fatalf("restored: %v", boot)
	}
}

func TestUseLibraryFollowsPortableMoveAndHonorsConfiguredLibrary(t *testing.T) {
	f := newWorkflow(t)
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"outputDirectory": filepath.Join(f.root, "Custom")}))
	settings := okMap(t, call(t, f.api, "use_library_directory"))
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"outputDirectory": settings["outputDirectory"]}))
	catalog := readJSON(t, f.api.CatalogPath())
	if m(catalog["workflow"])["outputDirectory"] != "" {
		t.Fatalf("workflow: %v", catalog["workflow"])
	}
	moved, err := workflowpaths.New(workflowpaths.MapCatalog(catalog), filepath.Join(f.root, "Moved app"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Settings().OutputDirectory != filepath.Join(f.root, "Moved app", "library") {
		t.Fatal(moved.Settings())
	}
}

func TestRejectRelativePathsAndUnknownTabsWithoutResettingGoodValues(t *testing.T) {
	f := newWorkflow(t)
	original := ok(t, call(t, f.api, "workflow_settings"))
	for _, patch := range []any{map[string]any{"outputDirectory": "relative/path"}, map[string]any{"activeTab": "invented"}} {
		failed(t, call(t, f.api, "workflow_settings", patch))
	}
	assertJSON(t, ok(t, call(t, f.api, "workflow_settings")), original)
}

func TestValidationAndOutputRuntimeSeparation(t *testing.T) {
	f := newWorkflow(t)
	missing := okMap(t, call(t, f.api, "toolchain_validate"))
	if missing["ok"] != false || m(missing["errors"])["overwatchInstall"] == nil {
		t.Fatalf("validation: %v", missing)
	}
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"outputDirectory": filepath.Join(f.appDir, "toolchain/runtime/export")}))
	if m(okMap(t, call(t, f.api, "toolchain_validate"))["errors"])["outputDirectory"] == nil {
		t.Fatal("runtime overlap accepted")
	}
	failed(t, call(t, f.api, "toolchain_start", map[string]any{"category": "skin"}))
}

func TestJobReceivesSharedPathsAndOriginalSelection(t *testing.T) {
	f := newWorkflow(t)
	settings := okMap(t, call(t, f.api, "workflow_settings", map[string]any{"overwatchInstall": filepath.Join(f.root, "Game 日本語"),
		"outputDirectory": filepath.Join(f.root, "Exports 日本語")}))
	selection := map[string]any{"hero": "Lúcio", "category": "skin", "asset": "Overwatch 2", "includeAnimations": true}
	f.tool.executable = "DataTool.exe"
	f.api.deps.ValidatePaths = func(string, string) datatool.Validation {
		return datatool.Validation{OK: true, Errors: ordered.New[string]()}
	}
	ok(t, call(t, f.api, "toolchain_start", selection))
	if len(f.jobs.exports) != 1 {
		t.Fatalf("exports: %v", f.jobs.exports)
	}
	got := f.jobs.exports[0]
	if got[0] != "DataTool.exe" || got[1] != settings["overwatchInstall"] || got[2] != settings["outputDirectory"] {
		t.Fatalf("export args: %v", got)
	}
	assertJSON(t, got[3], selection)
}

func TestRuntimeNeverInstalledInsideGame(t *testing.T) {
	f := newWorkflow(t)
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"overwatchInstall": f.root}))
	failed(t, call(t, f.api, "toolchain_install"))
	ok(t, call(t, f.api, "workflow_settings", map[string]any{"overwatchInstall": ""}))
	if err := os.MkdirAll(filepath.Join(f.root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, ".build.info"), []byte("fixture"))
	failed(t, call(t, f.api, "toolchain_install"))
	if f.tool.installs != 0 {
		t.Fatal("install started inside the game folder")
	}
}

func TestBackgroundIndexRefreshDoesNotChangeActiveSource(t *testing.T) {
	f := newWorkflow(t)
	first := demoAssets(t)
	second := filepath.Join(repo(t), "tests", "material-assets")
	ok(t, call(t, f.api, "scan_root", first))
	ok(t, call(t, f.api, "scan_root", second, false))
	if f.api.Root() != first || okMap(t, call(t, f.api, "bootstrap"))["lastRoot"] != first {
		t.Fatalf("root %s", f.api.Root())
	}
}
