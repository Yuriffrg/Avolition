package datatool

// Port of tests/test_datatool_maps.py: verified map argv/catalog contracts;
// the subprocess is a harmless fixture.

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

func TestListAndExtractArgumentsUseVerifiedModesAndLiteralMapNames(t *testing.T) {
	f := setup(t)
	destination := filepath.Join(f.output, "Map metadata 日本語.json")
	listed := mustArgs(t)(BuildListArgs(f.game, "maps", destination))
	if !slices.Equal(listed, []string{f.game, "list-maps", "--json", "--out=" + destination, "--online=false"}) {
		t.Fatal(listed)
	}
	query := "Watchpoint: Gibraltar:D86"
	args := mustArgs(t)(BuildMapExtractArgs(f.game, f.output, query, map[string]any{"includeTextures": false, "includeAnimations": false, "includeSound": true, "allLods": true}))
	if !slices.Equal(args[:4], []string{f.game, "extract-maps", f.output, query}) {
		t.Fatal(args)
	}
	for _, want := range []string{"--skip-textures=true", "--skip-animations=true", "--skip-sound=false", "--all-lods=true"} {
		if !slices.Contains(args, want) {
			t.Fatal(want)
		}
	}
	for _, bad := range []string{"", "Dorado|skin=*", "Dorado\nOther", "Dora*do", `Dorado" --raw=true`} {
		var valueErr *ValueError
		if _, err := BuildMapExtractArgs(f.game, f.output, bad, map[string]any{}); !errors.As(err, &valueErr) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestRealJSONSubprocessPreservesHeroCatalogAndMapMetadata(t *testing.T) {
	f := setup(t)
	f.jobs.StartCatalog(f.exe, f.game, "heroes")
	heroes := f.wait(t).Catalog
	if _, err := f.jobs.StartCatalog(f.exe, f.game, "maps"); err != nil {
		t.Fatal(err)
	}
	state := f.wait(t)
	if state.State != "Completed" || asJSON(state.Catalog) != asJSON(heroes) {
		t.Fatal(asJSON(state))
	}
	if asJSON(state.Result) != asJSON(MapCatalogResult{MapCount: 2, Workflow: "maps", Install: f.game}) {
		t.Fatal(asJSON(state.Result))
	}
	maps := state.MapCatalog
	if maps.Install != f.game || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T`).MatchString(maps.LoadedAt) {
		t.Fatal(asJSON(maps))
	}
	var selected Map
	for _, row := range maps.Maps {
		if row.ID == "000000000001.09F" {
			selected = row
		}
	}
	if selected.Name != "King’s Row 日本語" || selected.Query != "King’s Row 日本語:D86" || !slices.Equal(selected.GameModes, []string{"Hybrid"}) ||
		selected.MapType != "PVP" || selected.Description != "Fixture map" || selected.StateA != "Night" {
		t.Fatal(asJSON(selected))
	}
	count := 0
	for _, mode := range modes(f) {
		if mode == "list-maps" {
			count++
		}
	}
	if count != 1 || len(catalogDirs(t, f)) != 0 {
		t.Fatal(count)
	}
	f.jobs.StartCatalog(f.exe, f.game, "heroes")
	if got := f.wait(t).MapCatalog; asJSON(got) != asJSON(maps) {
		t.Fatal("Refreshing heroes must keep map choices.")
	}
}

func TestMapIDResolvesExactQueryAndRealArgvWithoutShell(t *testing.T) {
	f := setup(t)
	f.seedMaps(t, "")
	if _, err := f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": "000000000001.09F", "includeSound": true, "allLods": true}); err != nil {
		t.Fatal(err)
	}
	result := f.wait(t)
	if result.State != "Completed" || !pycompat.Exists(filepath.Join(f.output, "fixture.owmap")) {
		t.Fatal(asJSON(result))
	}
	last := f.lastCall()
	if !slices.Equal(last.command.Args[1:5], []string{f.game, "extract-maps", f.output, "King’s Row 日本語:D86"}) || last.command.Dir != filepath.Dir(f.exe) {
		t.Fatal(last.command)
	}
	if !slices.Contains(last.command.Args, "--skip-sound=false") {
		t.Fatal(last.command.Args)
	}
	mapResult := result.Result.(MapExportResult)
	if mapResult.MapID != "000000000001.09F" || mapResult.Workflow != "maps" {
		t.Fatal(asJSON(mapResult))
	}
	finished := false
	for _, line := range result.Logs {
		finished = finished || regexp.MustCompile(`Execution finished`).MatchString(line.Text)
	}
	if !finished {
		t.Fatal("no completion line")
	}
}

func TestCatalogQueryUsesLow32MapIndexAndSafeNumericFallback(t *testing.T) {
	f := setup(t)
	value, _ := pycompat.Loads([]byte(mapsJSON))
	rows := value.(*ordered.Map[any])
	first, _ := rows.Get("000000000001.09F")
	first.(*ordered.Map[any]).Set("VariantName", "A|B")
	first.(*ordered.Map[any]).Set("MapGUID", "012300000D86.002")
	second, _ := rows.Get("000000000002.09F")
	second.(*ordered.Map[any]).Set("Name", nil)
	second.(*ordered.Map[any]).Set("VariantName", nil)
	catalog, err := ParseMapCatalog(rows, f.game)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range catalog.Maps {
		if row.ID == "000000000001.09F" && row.Query != "D86" || row.ID == "000000000002.09F" && row.Query != "Title Screen:D87" {
			t.Fatal(asJSON(row))
		}
	}
	for _, payload := range []string{`[]`, `null`, `{}`, `{"bad": {}}`, `{"x": {"GUID": "not-a-guid", "MapGUID": "000000000D86.002"}}`} {
		decoded, _ := pycompat.Loads([]byte(payload))
		var dataErr *Error
		if _, err := ParseMapCatalog(decoded, f.game); !errors.As(err, &dataErr) {
			t.Fatalf("%s: %v", payload, err)
		}
	}
}

func TestUnknownIDsWrongInstallAndBadWorkflowsFailBeforeLaunch(t *testing.T) {
	f := setup(t)
	var dataErr *Error
	for _, selection := range []any{nil, []any{}, map[string]any{"workflow": "other"}, map[string]any{"workflow": "maps", "mapId": "*"}} {
		if _, err := f.jobs.StartExport(f.exe, f.game, f.output, selection); !errors.As(err, &dataErr) {
			t.Fatalf("%v: %v", selection, err)
		}
	}
	f.seedMaps(t, "")
	for _, ident := range []any{"unknown", nil, "Dorado", "000000000001.09F|*"} {
		if _, err := f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": ident}); !errors.As(err, &dataErr) {
			t.Fatalf("%v: %v", ident, err)
		}
	}
	f.jobs.mu.Lock()
	moved := *f.jobs.mapCatalog
	moved.Install = filepath.Join(f.game, "other")
	f.jobs.mapCatalog = &moved
	f.jobs.mu.Unlock()
	if _, err := f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": "*"}); !errors.As(err, &dataErr) {
		t.Fatal(err)
	}
	if _, err := f.jobs.StartCatalog(f.exe, f.game, "other"); !errors.As(err, &dataErr) {
		t.Fatal(err)
	}
	if f.callCount() != 0 {
		t.Fatal("process started")
	}
}

func TestAllMapsAndFailureUseSameJobStateMachine(t *testing.T) {
	f := setup(t)
	f.seedMaps(t, "")
	f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": "*"})
	if result := f.wait(t).Result.(MapExportResult); result.MapName != "All maps" {
		t.Fatal(asJSON(result))
	}
	if got := f.lastCall().command.Args[4]; got != "*" {
		t.Fatal(got)
	}
	f.seedMaps(t, "FailZero")
	f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": "000000000001.09F"})
	failed := f.wait(t)
	if failed.State != "Failed" || errText(failed) == "" || len(failed.MapCatalog.Maps) != 2 {
		t.Fatal(asJSON(failed))
	}
}

func TestMapCancellationStopsRealFixtureChildAndKeepsCatalog(t *testing.T) {
	f := setup(t)
	f.seedMaps(t, "Wait")
	f.jobs.StartExport(f.exe, f.game, f.output, map[string]any{"workflow": "maps", "mapId": "000000000001.09F"})
	child := f.waitForChild(t)
	f.jobs.Cancel()
	if state := f.wait(t).State; state != "Cancelled" {
		t.Fatal(state)
	}
	assertProcessEnded(t, child)
	if len(f.jobs.Snapshot().MapCatalog.Maps) != 2 {
		t.Fatal("catalog lost")
	}
}
