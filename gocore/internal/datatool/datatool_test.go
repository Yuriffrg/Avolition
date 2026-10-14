package datatool

// Port of tests/test_datatool.py: mocked DataTool contracts plus real
// harmless child-process lifecycle tests. These tests do not extract
// Overwatch assets.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"avolition/gocore/internal/pycompat"
)

func mustArgs(t *testing.T) func([]string, error) []string {
	return func(args []string, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return args
	}
}

func TestVerifiedCommandsKeepLiteralFilterInSingleArgument(t *testing.T) {
	f := setup(t)
	args := mustArgs(t)(BuildExtractArgs(f.game, f.output, map[string]any{"hero": "Lúcio", "asset": "Overwatch Classic", "category": "skin"}))
	if !slices.Equal(args[:4], []string{f.game, "extract-unlocks", f.output, "Lúcio|skin=Overwatch Classic"}) {
		t.Fatal(args)
	}
	for _, want := range []string{"--online=false", "--convert-animations-type=owanimclip", "--convert-textures-type=png", "--skip-animations=false", "--skip-sound=true"} {
		if !slices.Contains(args, want) {
			t.Fatalf("%s missing in %v", want, args)
		}
	}
	for _, category := range Categories {
		args := mustArgs(t)(BuildExtractArgs(f.game, f.output, map[string]any{"hero": "*", "asset": "*", "category": category.ID}))
		if args[1] != "extract-unlocks" || args[3] != "*|"+category.ID+"=*" {
			t.Fatal(args)
		}
	}
}

func TestExplicitOptionsAndQueryMetacharacterRejection(t *testing.T) {
	f := setup(t)
	args := mustArgs(t)(BuildExtractArgs(f.game, f.output, map[string]any{"hero": "Lúcio", "asset": "*", "category": "skin",
		"includeAnimations": false, "includeTextures": false, "includeSound": true, "allLods": true}))
	for _, want := range []string{"--skip-animations=true", "--skip-textures=true", "--skip-sound=false", "--all-lods=true"} {
		if !slices.Contains(args, want) {
			t.Fatalf("%s missing in %v", want, args)
		}
	}
	for _, invalid := range []string{"bad|emote=*", "bad,name", `bad"name`, "!skin", "prefix*", ""} {
		_, err := BuildExtractArgs(f.game, f.output, map[string]any{"hero": "Lúcio", "asset": invalid})
		var valueErr *ValueError
		if !errors.As(err, &valueErr) {
			t.Fatalf("%q: %v", invalid, err)
		}
	}
	if _, err := BuildExtractArgs(f.game, f.output, map[string]any{"hero": "*", "asset": "*", "category": "invented"}); err == nil {
		t.Fatal("invented category accepted")
	}
}

func TestListJSONFileArguments(t *testing.T) {
	f := setup(t)
	destination := filepath.Join(f.root, "目录 with spaces", "unlocks.json")
	args := mustArgs(t)(BuildListArgs(f.game, "unlocks", destination))
	if args[1] != "list-unlocks" || !slices.Contains(args, "--out="+destination) || !slices.Contains(args, "--flatten") || !slices.Contains(args, "--online=false") {
		t.Fatal(args)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	files := map[string]string{}
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			data, _ := os.ReadFile(path)
			files[path] = string(data)
		}
		return nil
	})
	return files
}

func TestInstallValidationAcceptsBattlenetAndSteamWithoutWritingGame(t *testing.T) {
	f := setup(t)
	before := snapshotTree(t, f.game)
	if result := ValidatePaths(f.game, f.output); !result.OK {
		t.Fatal(asJSON(result))
	}
	if after := snapshotTree(t, f.game); asJSON(before) != asJSON(after) {
		t.Fatal("game folder changed")
	}
	os.Remove(filepath.Join(f.game, ".build.info"))
	os.WriteFile(filepath.Join(f.game, "data", ".build.config"), []byte("build-name = fixture"), 0o666)
	if result := ValidatePaths(f.game); !result.OK {
		t.Fatal(asJSON(result))
	}
}

func errorOf(v Validation, key string) string {
	value, _ := v.Errors.Get(key)
	return value
}

func TestValidationErrorsAndRetailNormalization(t *testing.T) {
	f := setup(t)
	for _, bad := range []string{"", "relative", filepath.Join(f.root, "missing"), f.output} {
		if errorOf(ValidatePaths(bad), "overwatchInstall") == "" {
			t.Fatalf("%q accepted", bad)
		}
	}
	if errorOf(ValidatePaths(f.game, filepath.Join(f.game, "Exports")), "outputDirectory") == "" {
		t.Fatal("output inside the game accepted")
	}
	outputFile := filepath.Join(f.output, "file.txt")
	os.WriteFile(outputFile, []byte("existing"), 0o666)
	if errorOf(ValidatePaths(f.game, outputFile), "outputDirectory") == "" {
		t.Fatal("file output accepted")
	}
	retail := filepath.Join(f.game, "_retail_")
	os.Mkdir(retail, 0o777)
	os.WriteFile(filepath.Join(retail, ".flavor.info"), []byte("fixture"), 0o666)
	if got := ValidatePaths(retail).OverwatchInstall; got != f.game {
		t.Fatal(got)
	}
	previous := createProbe
	createProbe = func(string) error { return &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission} }
	message := errorOf(ValidatePaths(f.game, f.output), "outputDirectory")
	createProbe = previous
	if !strings.Contains(message, "write") {
		t.Fatal(message)
	}
	os.WriteFile(filepath.Join(f.game, ".build.info"), []byte("Product!STRING:0|Version!STRING:0\nothergame|1\n"), 0o666)
	if got := errorOf(ValidatePaths(f.game), "overwatchInstall"); !strings.Contains(got, "different Battle.net game") {
		t.Fatal(got)
	}
}

func TestParseCatalogFiltersUnsupportedTypesAndDeduplicatesNames(t *testing.T) {
	f := setup(t)
	heroes, _ := pycompat.Loads([]byte(`{"a": {"Name": "Lúcio", "IsHero": true}, "b": {"Name": "NPC", "IsHero": false}}`))
	unlocks, _ := pycompat.Loads([]byte(`{"Lúcio": [{"Name": "Dance", "Type": "Emote", "GUID": "1"}, {"Name": "Dance", "Type": "Emote", "GUID": "2"}, {"Name": "Icon", "Type": "Icon"}]}`))
	result, err := ParseCatalog(heroes, unlocks, f.game)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Heroes) != 1 || result.Heroes[0].Name != "Lúcio" {
		t.Fatal(asJSON(result))
	}
	if got, _ := result.Unlocks.Get("Lúcio"); asJSON(got) != `[{"id":"1","name":"Dance","category":"emote"}]` {
		t.Fatal(asJSON(got))
	}
	empty, _ := pycompat.Loads([]byte(`{}`))
	var dataErr *Error
	if _, err := ParseCatalog(empty, empty, f.game); !errors.As(err, &dataErr) {
		t.Fatal(err)
	}
	heroes, _ = pycompat.Loads([]byte(`{"a": {"Name": "Lúcio"}}`))
	unlocks, _ = pycompat.Loads([]byte(`{"Lúcio": {}}`))
	if _, err := ParseCatalog(heroes, unlocks, f.game); !errors.As(err, &dataErr) {
		t.Fatal(err)
	}
}

func modes(f *fixture) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.command.Args[2])
	}
	return out
}

func catalogDirs(t *testing.T, f *fixture) []string {
	matches, _ := filepath.Glob(filepath.Join(f.root, "job state", "catalog-*"))
	return matches
}

func TestCatalogExecutesRealSubprocessesAndReadsJSON(t *testing.T) {
	f := setup(t)
	if _, err := f.jobs.StartCatalog(f.exe, f.game, "heroes"); err != nil {
		t.Fatal(err)
	}
	result := f.wait(t)
	if result.State != "Completed" || result.Catalog.Heroes[0].Name != "Lúcio" {
		t.Fatal(asJSON(result))
	}
	if got := modes(f); !slices.Equal(got, []string{"list-heroes", "list-unlocks"}) {
		t.Fatal(got)
	}
	if len(catalogDirs(t, f)) != 0 {
		t.Fatal("temporary catalog folder left")
	}
	if asJSON(result.Result) != `{"heroCount":1,"install":"`+f.game+`","workflow":"heroes"}` {
		t.Fatal(asJSON(result.Result))
	}
	unlocks, _ := result.Catalog.Unlocks.Get("Lúcio")
	if asJSON(unlocks) != `[{"id":"003.0A5","name":"Dance","category":"emote"},{"id":"002.0A5","name":"Overwatch Classic","category":"skin"}]` {
		t.Fatal(asJSON(unlocks))
	}
}

func TestSuccessRealArgumentTransportNoShellAndDependencyCwd(t *testing.T) {
	f := setup(t)
	t.Setenv("DATATOOL_OVERWATCH_DIR", "wrong game")
	if _, err := f.export(t, "Overwatch Classic", nil); err != nil {
		t.Fatal(err)
	}
	result := f.wait(t)
	if result.State != "Completed" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatal(asJSON(result))
	}
	if !pycompat.IsFile(filepath.Join(f.output, "fixture.owanimclip")) {
		t.Fatal("no output")
	}
	var payload struct {
		Argv     []string `json:"argv"`
		Cwd      string   `json:"cwd"`
		Override *string  `json:"override"`
	}
	for _, line := range result.Logs {
		if strings.HasPrefix(line.Text, `{"argv"`) {
			json.Unmarshal([]byte(line.Text), &payload)
		}
	}
	if payload.Argv[3] != "Lúcio|skin=Overwatch Classic" || payload.Argv[2] != f.output || payload.Cwd != filepath.Dir(f.exe) || payload.Override != nil {
		t.Fatalf("%+v", payload)
	}
	if f.calls[0].command.Args[0] != f.exe || f.calls[0].command.Dir != filepath.Dir(f.exe) {
		t.Fatalf("%+v", f.calls[0].command)
	}
	assertContainedStart(t, f)
	if result.Progress != nil {
		t.Fatal("progress")
	}
	for _, line := range result.Logs {
		if strings.Contains(line.Text, "\x1b") {
			t.Fatalf("ANSI kept: %q", line.Text)
		}
	}
	if result.Logs[0].Seq != 1 || result.Logs[0].Stream != "avolition" || result.Logs[0].Text != "Running extract-unlocks…" {
		t.Fatal(asJSON(result.Logs[0]))
	}
	if asJSON(result.Result) != asJSON(ExportResult{f.output, "Lúcio", "Overwatch Classic", "skin"}) {
		t.Fatal(asJSON(result.Result))
	}
}

func TestFailedExitZeroQueryErrorsAndMissingCompletion(t *testing.T) {
	f := setup(t)
	for _, c := range [][2]string{{"FailCode", "exit code 7"}, {"FailZero", "could not match"}, {"Unsupported", "does not support"}, {"NoMarker", "without confirming completion"}} {
		if _, err := f.export(t, c[0], nil); err != nil {
			t.Fatal(err)
		}
		result := f.wait(t)
		if result.State != "Failed" || !strings.Contains(errText(result), c[1]) {
			t.Fatalf("%s: %s", c[0], asJSON(result))
		}
		if !f.lock.TryLock() {
			t.Fatal("lock not released")
		}
		f.lock.Unlock()
	}
	if got := errText(f.jobs.Snapshot()); got != "DataTool exited without confirming completion. Check the log for invalid arguments or a missing runtime." {
		t.Fatal(got)
	}
}

func TestInstallerLockAndDuplicateJobs(t *testing.T) {
	f := setup(t)
	f.lock.Lock()
	if _, err := f.export(t, "Overwatch Classic", nil); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatal(err)
	}
	f.lock.Unlock()
	if _, err := f.export(t, "Wait", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.export(t, "Wait", nil); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	f.jobs.Cancel()
	if state := f.wait(t).State; state != "Cancelled" {
		t.Fatal(state)
	}
}

func TestCancelTerminatesRealChildProcessAndStreamsLogs(t *testing.T) {
	f := setup(t)
	f.export(t, "Wait", nil)
	child := f.waitForChild(t)
	start := time.Now()
	if snapshot := f.jobs.Cancel(); snapshot.Message != "Cancelling…" {
		t.Fatal(snapshot.Message)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cancel blocked")
	}
	result := f.wait(t)
	if result.State != "Cancelled" || result.Message != "Cancelled. Partial output files may remain." {
		t.Fatal(asJSON(result))
	}
	assertProcessEnded(t, child)
}

func TestImmediateParentExitCannotLeaveChildAlive(t *testing.T) {
	f := setup(t)
	f.export(t, "ParentExit", nil)
	child := f.waitForChild(t)
	if result := f.wait(t); result.State != "Completed" {
		t.Fatal(asJSON(result))
	}
	assertProcessEnded(t, child)
}

func TestBackendCloseCancelsProcessAndChild(t *testing.T) {
	f := setup(t)
	f.export(t, "Wait", nil)
	child := f.waitForChild(t)
	f.jobs.Close()
	if snapshot := f.jobs.Snapshot(); snapshot.State != "Cancelled" || snapshot.Busy {
		t.Fatal(asJSON(snapshot))
	}
	assertProcessEnded(t, child)
}

func TestJobRejectsUnknownCatalogItemsAndMissingExecutable(t *testing.T) {
	f := setup(t)
	if _, err := f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"hero": "Lúcio", "asset": "Dance"}); err == nil || !strings.Contains(err.Error(), "Load game assets") {
		t.Fatal(err)
	}
	f.seed()
	if _, err := f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"hero": "Lúcio", "asset": "invented"}); err == nil || !strings.Contains(err.Error(), "asset is not") {
		t.Fatal(err)
	}
	os.Remove(f.exe)
	f.export(t, "Overwatch Classic", nil)
	if got := errText(f.wait(t)); !strings.Contains(got, "DataTool is missing") {
		t.Fatal(got)
	}
	if f.callCount() != 0 {
		t.Fatal("process started")
	}
}

func TestSnapshotShapeMatchesPython(t *testing.T) {
	f := setup(t)
	data := asJSON(f.jobs.Snapshot())
	want := `{"state":"Ready","busy":false,"kind":null,"jobId":null,"logs":[],"error":null,"message":"Ready","result":null,"exitCode":null,"progress":null,"startedAt":null,"finishedAt":null,"catalog":null,"mapCatalog":null}`
	if data != want {
		t.Fatal(data)
	}
	f.jobs.StartCatalog(f.exe, f.game, "heroes")
	snapshot := f.wait(t)
	if *snapshot.Kind != "catalog" || len(*snapshot.JobID) != 32 || !strings.HasSuffix(*snapshot.StartedAt, "+00:00") || !strings.HasSuffix(*snapshot.FinishedAt, "+00:00") {
		t.Fatal(asJSON(snapshot))
	}
}
