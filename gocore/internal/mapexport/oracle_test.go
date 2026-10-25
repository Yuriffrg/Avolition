package mapexport

// Oracle tests: make_static_payload and export_map against the Python
// modules. export_map runs through the real Go bridge client and the real
// Python BridgeService (owpreview/c4d_bridge.py) in a temporary queue, whose
// scene builder is c4d_scene.build_scene on tests/test_c4d_scene.py's fake
// Cinema 4D API; Python's export_map calls the same builder directly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/mapformats"
	"avolition/gocore/internal/mapformats/maptest"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pypath"
)

const payloadPython = `
import time
from owpreview.map_formats import load_map
from owpreview.map_export import make_static_payload
out = []
for path, mutation, name in args[0]:
    try:
        asset = load_map(path)
        if mutation == 'normals':
            key = asset['models'][0]['meshPrototype']
            asset['meshPrototypes'][key]['meshes'][0]['normals'][:3] = [0, 0, 0]
            asset['warnings'].append('Keep original map warning')
        elif mutation == 'zero-scale':
            asset['exportHierarchy'][0]['matrix'][0] = 0
        elif mutation == 'shear':
            asset['exportHierarchy'][0]['matrix'][1] = .4
        elif mutation == 'missing-prototype':
            asset['models'][0]['meshPrototype'] = 'missing'
        elif mutation == 'not-map':
            del asset['map']
        elif mutation == 'no-meshes':
            for prototype in asset['meshPrototypes'].values():
                prototype['meshes'] = []
        start = time.perf_counter()
        payload = make_static_payload(asset, name=name)
        out.append({'ok': True, 'result': payload, 'seconds': time.perf_counter() - start})
    except Exception as error:
        out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
result = out
`

func mutate(asset *formats.Asset, mutation string) {
	switch mutation {
	case "normals":
		prototype, _ := asset.MeshPrototypes.Get(asset.Models[0].MeshPrototype)
		mesh := prototype.Meshes[0]
		mesh.Normals[0], mesh.Normals[1], mesh.Normals[2] = 0, 0, 0
		asset.Warnings = append(asset.Warnings, "Keep original map warning")
	case "zero-scale":
		asset.ExportHierarchy[0].Matrix[0] = 0
	case "shear":
		asset.ExportHierarchy[0].Matrix[1] = .4
	case "missing-prototype":
		asset.Models[0].MeshPrototype = "missing"
	case "not-map":
		asset.Map = nil
	case "no-meshes":
		asset.MeshPrototypes.Range(func(_ string, prototype *formats.MeshPrototype) bool {
			prototype.Meshes = []*formats.Mesh{}
			return true
		})
	}
}

type payloadCase struct {
	path, mutation, name string
}

func comparePayloads(t *testing.T, cases []payloadCase) {
	t.Helper()
	var calls [][]string
	for _, c := range cases {
		calls = append(calls, []string{c.path, c.mutation, c.name})
	}
	var python []struct {
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
		Error   string          `json:"error"`
		Seconds float64         `json:"seconds"`
	}
	if err := json.Unmarshal(oracle.Exec(t, payloadPython, calls).MustValue(t), &python); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		label := fmt.Sprintf("%s/%s", filepath.Base(c.path), c.mutation)
		asset, err := mapformats.LoadMap(c.path, "")
		if err != nil {
			t.Fatal(err)
		}
		mutate(asset, c.mutation)
		start := time.Now()
		payload, err := MakeStaticPayload(asset, nil, c.name)
		elapsed := time.Since(start)
		want := python[i]
		if err != nil || !want.OK {
			if err == nil || want.OK || err.Error() != want.Error {
				t.Errorf("%s: Go error %v, Python %q (ok=%v)", label, err, want.Error, want.OK)
			}
			continue
		}
		got, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		diffs, err := jsoncmp.Diff(want.Result, got, jsoncmp.Default)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range diffs {
			t.Errorf("%s: %s", label, d)
		}
		t.Logf("%s: make_static_payload Python %.1f ms, Go %.1f ms (%d JSON bytes)", label, want.Seconds*1000,
			float64(elapsed.Microseconds())/1000, len(got))
	}
}

func TestOracleStaticPayload(t *testing.T) {
	root := pypath.Resolve(t.TempDir())
	path := maptest.WriteFixture(t, filepath.Join(root, "source"))
	var cases []payloadCase
	for _, mutation := range []string{"", "normals", "zero-scale", "shear", "missing-prototype", "not-map", "no-meshes"} {
		cases = append(cases, payloadCase{path, mutation, ""})
	}
	cases = append(cases, payloadCase{path, "", "Named"})
	comparePayloads(t, cases)
}

// benchMap is AVOLITION_BENCH_MAP (an .owmap) when set.
func benchMap(t *testing.T) string {
	path := os.Getenv("AVOLITION_BENCH_MAP")
	if path == "" || testing.Short() {
		t.Skip("set AVOLITION_BENCH_MAP to an .owmap to compare a real map")
	}
	return path
}

func TestOracleStaticPayloadBenchMap(t *testing.T) {
	comparePayloads(t, []payloadCase{{benchMap(t), "", ""}})
}

const servicePython = `
import json, sys, time
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0, args[0])
from test_c4d_scene import make_api
from owpreview.c4d_bridge import BridgeService
from owpreview.c4d_scene import build_scene
queue = Path(args[1])
seen = {}
def build(payload, path):
    native = make_api()
    with patch.dict(sys.modules, {'c4d': native, 'c4d.modules': native.modules}):
        report = build_scene(payload, path) if not args[2] else {'models': len(payload['asset']['models'])}
    if not path.exists():
        path.write_bytes(b'Native writer test fixture')
    seen['payload'] = payload if not args[2] else {'materials': payload['materials'], 'warnings': payload['warnings']}
    seen['report'] = report
    seen['textures'] = sorted(p.name for p in (path.parent / 'tex').glob('*.png'))
    return {'warnings': ['Fixture only']}
service = BridgeService(build, lambda: {'octane': True, 'mapInstances': True, 'mapTiming24': True, 'bridgeVersion': '0.7.7'}, queue)
deadline = time.monotonic() + 120
while time.monotonic() < deadline and 'report' not in seen:
    service.tick()
    time.sleep(0.01)
end = time.monotonic() + 1
while time.monotonic() < end:
    service.tick()
    time.sleep(0.01)
result = seen
`

const exportPython = `
import sys, time
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0, args[0])
from test_c4d_scene import make_api
from owpreview.c4d_scene import build_scene
from owpreview.map_formats import load_map
from owpreview.map_export import export_map
seen = {}
class LocalBridge:
    def status(self):
        return {'connected': True, 'octane': True, 'mapInstances': True, 'mapTiming24': True, 'bridgeVersion': '0.7.7', 'message': 'Ready'}
    def export(self, payload, destination, texture_folder, progress=None):
        native = make_api()
        path = Path(destination)
        with patch.dict(sys.modules, {'c4d': native, 'c4d.modules': native.modules}):
            report = build_scene(payload, path) if not args[4] else {'models': len(payload['asset']['models'])}
        if not path.exists():
            path.write_bytes(b'Native writer test fixture')
        seen['payload'] = payload if not args[4] else {'materials': payload['materials'], 'warnings': payload['warnings']}
        seen['report'] = report
        seen['textures'] = sorted(p.name for p in Path(texture_folder).glob('*.png'))
        return {'warnings': ['Fixture only']}
progress = []
asset = load_map(args[1])
start = time.perf_counter()
exported = export_map(asset, args[2], 'Map export', bake_textures=args[3], bridge=LocalBridge(),
                      progress=lambda message, completed=None, total=None: progress.append([message, completed, total]))
seen['seconds'] = time.perf_counter() - start
seen['result'] = exported
seen['progress'] = progress
result = seen
`

var stage = regexp.MustCompile(`\.whitehound-export-[^/\\"]+`)

func compareExport(t *testing.T, mapPath string, bake, light bool) {
	if err := oracle.Available(); err != nil {
		t.Skipf("Python oracle unavailable: %v", err)
	}
	tests := exporttest.TestsDir()
	root := pypath.Resolve(t.TempDir())
	queue := filepath.Join(root, "queue")
	pyOut, goOut := filepath.Join(root, "python-out"), filepath.Join(root, "go-out")

	var python struct {
		Result   json.RawMessage `json:"result"`
		Payload  json.RawMessage `json:"payload"`
		Report   json.RawMessage `json:"report"`
		Textures []string        `json:"textures"`
		Progress [][]any         `json:"progress"`
		Seconds  float64         `json:"seconds"`
	}
	if err := json.Unmarshal(oracle.Exec(t, exportPython, tests, mapPath, pyOut, bake, light).MustValue(t), &python); err != nil {
		t.Fatal(err)
	}

	var service oracle.Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		service = oracle.Exec(t, servicePython, tests, queue, light)
	}()
	client := c4dbridge.NewClient(queue)
	deadline := time.Now().Add(30 * time.Second)
	for !client.Status().MapInstances() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	asset, err := mapformats.LoadMap(mapPath, "")
	if err != nil {
		t.Fatal(err)
	}
	var progress [][]any
	name := "Map export"
	opts := Options{BakeTextures: bake, Bridge: client, Progress: func(message string, completed, total *int) {
		entry := []any{message, nil, nil}
		if completed != nil {
			entry[1], entry[2] = float64(*completed), float64(*total)
		}
		progress = append(progress, entry)
	}}
	start := time.Now()
	result, exportErr := ExportMap(asset, goOut, &name, opts)
	elapsed := time.Since(start)
	wg.Wait()
	if exportErr != nil {
		t.Fatalf("Go export: %v (service %s)", exportErr, service)
	}
	var seen struct {
		Payload  json.RawMessage `json:"payload"`
		Report   json.RawMessage `json:"report"`
		Textures []string        `json:"textures"`
	}
	if err := json.Unmarshal(service.MustValue(t), &seen); err != nil {
		t.Fatal(err)
	}

	got, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	want := oracle.ReplaceJSONPath(string(python.Result), pyOut, goOut)
	jsoncmp.Equal(t, json.RawMessage(want), json.RawMessage(got), jsoncmp.Options{})
	jsoncmp.Equal(t, python.Report, seen.Report, jsoncmp.Options{})
	if fmt.Sprint(python.Textures) != fmt.Sprint(seen.Textures) {
		t.Errorf("textures: Python %v, Go %v", python.Textures, seen.Textures)
	}
	// The payload as Cinema 4D receives it; texture paths point into each
	// side's own staging folder.
	pyPayload := stage.ReplaceAllString(string(python.Payload), ".stage")
	goPayload := stage.ReplaceAllString(string(seen.Payload), ".stage")
	pyPayload = oracle.ReplaceJSONPath(pyPayload, pyOut, "OUT")
	goPayload = oracle.ReplaceJSONPath(oracle.ReplaceJSONPath(goPayload, goOut, "OUT"), queue, "QUEUE")
	diffs, err := jsoncmp.Diff([]byte(pyPayload), []byte(goPayload), jsoncmp.Options{RelTol: 1e-6, AbsTol: 1e-6,
		Ignore: []string{".path"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		t.Error(d)
	}
	// Progress: the Python LocalBridge reports no bridge stages; the real Go
	// client does ("Waiting for Cinema 4D…" and similar). Compare the rest.
	var own [][]any
	for _, entry := range progress {
		message := entry[0].(string)
		if message == "Preparing map geometry" || message == "Baking map materials" ||
			strings.HasPrefix(message, "Publishing") {
			own = append(own, entry)
		}
	}
	if fmt.Sprint(own) != fmt.Sprint(python.Progress) {
		t.Errorf("progress:\n Go %v\n Python %v", own, python.Progress)
	}
	t.Logf("export_map (bake=%v): Python %.2fs (local builder), Go %.2fs (through the queue and the Python service)",
		bake, python.Seconds, elapsed.Seconds())
}

func TestOracleExportMapWithPythonService(t *testing.T) {
	root := pypath.Resolve(t.TempDir())
	path := maptest.WriteFixture(t, filepath.Join(root, "source"))
	for _, bake := range []bool{true, false} {
		t.Run(fmt.Sprint(bake), func(t *testing.T) { compareExport(t, path, bake, false) })
	}
}

func TestOracleExportBenchMapWithPythonService(t *testing.T) {
	compareExport(t, benchMap(t), true, true)
}
