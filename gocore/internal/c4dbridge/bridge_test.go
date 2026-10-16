package c4dbridge

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func obj(pairs ...any) *ordered.Map[any] {
	m := ordered.New[any]()
	for i := 0; i < len(pairs); i += 2 {
		m.Set(pairs[i].(string), pairs[i+1])
	}
	return m
}

func mustWrite(t *testing.T, path string, value any) {
	t.Helper()
	if err := WriteJSON(path, value); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestOfflineBridgeIsClearAndCreatesNoJobs(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	bridge := NewClient(queue)
	if bridge.Status().Connected() {
		t.Fatal("connected without a status file")
	}
	_, err := bridge.Export(obj(), filepath.Join(root, "out.c4d"), filepath.Join(root, "tex"), ExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "Avolition C4D Bridge") {
		t.Fatalf("error %v", err)
	}
	if exists(queue) {
		t.Fatal("queue created")
	}
	data, _ := json.Marshal(bridge.Status())
	if string(data) != `{"connected":false,"message":"`+SetupMessage+`"}` {
		t.Fatalf("status %s", data)
	}
}

func TestStaleAndWrongProtocolStatusAreIgnored(t *testing.T) {
	queue := t.TempDir()
	mustWrite(t, filepath.Join(queue, "status-old.json"), obj("protocol", 1, "heartbeat", Float(now()-30)))
	mustWrite(t, filepath.Join(queue, "status-wrong.json"), obj("protocol", 99, "heartbeat", Float(now())))
	if NewClient(queue).Status().Connected() {
		t.Fatal("stale or wrong protocol status accepted")
	}
}

func TestMalformedHeartbeatFilesDoNotHideALiveBridge(t *testing.T) {
	queue := t.TempDir()
	for index, value := range []any{[]any{}, nil, obj("protocol", 1, "heartbeat", Float(now())),
		obj("protocol", 1, "heartbeat", "invalid", "instance", "broken")} {
		mustWrite(t, filepath.Join(queue, "status-invalid-"+string(rune('0'+index))+".json"), value)
	}
	os.WriteFile(filepath.Join(queue, "status-garbage.json"), []byte("{not json"), 0o644)
	mustWrite(t, filepath.Join(queue, "status-live.json"), obj("protocol", 1, "instance", "live",
		"heartbeat", Float(now()), "bridgeVersion", "0.7.4", "octane", true, "mapInstances", true))
	status := NewClient(queue).Status()
	if status.Instance() != "live" || !strings.Contains(status.Message(), "0.7.4") || !strings.Contains(status.Message(), "map exports ready") {
		t.Fatalf("status %+v", status)
	}
}

func TestMapCapableOctaneInstanceWinsOverNewerOldBridge(t *testing.T) {
	queue := t.TempDir()
	current := now()
	for _, row := range []struct {
		instance, version string
		maps, octane      bool
		age               float64
	}{{"new", "0.7.4", true, true, 2}, {"old", "0.7.2", false, true, 1}, {"no-octane", "0.7.4", true, false, 0}} {
		mustWrite(t, filepath.Join(queue, "status-"+row.instance+".json"), obj("protocol", 1, "instance", row.instance,
			"heartbeat", Float(current-row.age), "bridgeVersion", row.version, "mapInstances", row.maps, "octane", row.octane))
	}
	if got := NewClient(queue).Status().Instance(); got != "new" {
		t.Fatalf("instance %q", got)
	}
}

func TestMapDispatchRechecksCapabilityBeforeCreatingAJob(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	os.Mkdir(queue, 0o755)
	mustWrite(t, filepath.Join(queue, "status-old.json"), obj("protocol", 1, "instance", "old",
		"heartbeat", Float(now()), "bridgeVersion", "0.7.2", "octane", true))
	bridge := NewClient(queue)
	if !bridge.Status().Connected() || !strings.Contains(bridge.Status().Message(), "0.7.2") {
		t.Fatal("status")
	}
	_, err := bridge.Export(obj("mapInstances", true), filepath.Join(root, "out.c4d"), filepath.Join(root, "tex"), ExportOptions{})
	if err == nil || !regexp.MustCompile(`bridge 0\.7\.2.*Avolition C4D Bridge 0\.7\.5`).MatchString(err.Error()) {
		t.Fatalf("error %v", err)
	}
	if exists(filepath.Join(queue, "jobs")) || exists(filepath.Join(root, "out.c4d")) {
		t.Fatal("job or output created")
	}
}

func TestQueueAliasKeepsExistingCompanionsDiscoverable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("AVOLITION_C4D_QUEUE", "")
	t.Setenv("WHITEHOUND_C4D_QUEUE", "")
	if got := QueueRoot(); got != filepath.Join(root, "Whitehound", "c4d-bridge") {
		t.Fatal(got)
	}
	t.Setenv("WHITEHOUND_C4D_QUEUE", filepath.Join(root, "legacy"))
	if got := QueueRoot(); got != filepath.Join(root, "legacy") {
		t.Fatal(got)
	}
	t.Setenv("AVOLITION_C4D_QUEUE", filepath.Join(root, "official"))
	if got := QueueRoot(); got != filepath.Join(root, "official") {
		t.Fatal(got)
	}
	result := oracle.Exec(t, `
import os
from owpreview.c4d_bridge import queue_root
os.environ.clear(); os.environ['LOCALAPPDATA'] = args[0]
first = str(queue_root())
os.environ['WHITEHOUND_C4D_QUEUE'] = args[1]
result = [first, str(queue_root())]`, root, filepath.Join(root, "legacy"))
	var want []string
	json.Unmarshal(result.MustValue(t), &want)
	if want[0] != filepath.Join(root, "Whitehound", "c4d-bridge") || want[1] != filepath.Join(root, "legacy") {
		t.Fatalf("Python %v", want)
	}
}

func fakeStatus(maps bool) func() *Status {
	return func() *Status {
		status := &Status{}
		status.Set("connected", true)
		status.Set("octane", true)
		status.Set("mapInstances", maps)
		status.Set("instance", "test")
		return status
	}
}

func TestLargeMapNativeSaveCanFinishAfterTheOldTimeout(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	clock := 0.0
	var stages []string
	bridge := NewClient(queue)
	bridge.statusFunc = fakeStatus(true)
	bridge.monotonic = func() float64 { return clock }
	bridge.sleep = func(float64) {
		clock += 40
		jobs := listDir(t, filepath.Join(queue, "jobs"))
		job := filepath.Join(queue, "jobs", jobs[0])
		os.WriteFile(filepath.Join(job, "processing"), nil, 0o644)
		if clock >= 200 {
			os.WriteFile(filepath.Join(job, "scene.c4d"), []byte("Finished large map"), 0o644)
			mustWrite(t, filepath.Join(job, "result.json"), obj("ok", true, "data", obj("verifiedNativeScene", true)))
		}
	}
	result, err := bridge.Export(obj("mapInstances", true), filepath.Join(root, "large.c4d"), filepath.Join(root, "tex"),
		ExportOptions{Progress: func(message string) { stages = append(stages, message) }})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"verifiedNativeScene":true}` || clock <= 180 {
		t.Fatalf("result %s clock %v", result, clock)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "large.c4d")); string(data) != "Finished large map" {
		t.Fatal("scene not copied")
	}
	building := false
	for _, stage := range stages {
		building = building || strings.Contains(stage, "building")
	}
	if !building || len(listDir(t, filepath.Join(queue, "jobs"))) != 0 {
		t.Fatalf("stages %v", stages)
	}
}

func TestUnclaimedMapJobKeepsABoundedStartWait(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	clock := 0.0
	bridge := NewClient(queue)
	bridge.statusFunc = fakeStatus(true)
	bridge.monotonic = func() float64 { return clock }
	bridge.sleep = func(float64) { clock += 50 }
	_, err := bridge.Export(obj("mapInstances", true), filepath.Join(root, "never.c4d"), filepath.Join(root, "tex"), ExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("error %v", err)
	}
	if clock != 150 || exists(filepath.Join(root, "never.c4d")) || len(listDir(t, filepath.Join(queue, "jobs"))) != 0 {
		t.Fatalf("clock %v", clock)
	}
}

func TestClaimedJobThatTimesOutIsCancelledNotDeleted(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	clock := 0.0
	bridge := NewClient(queue)
	bridge.statusFunc = fakeStatus(false)
	bridge.monotonic = func() float64 { return clock }
	bridge.sleep = func(float64) {
		clock += 1
		job := filepath.Join(queue, "jobs", listDir(t, filepath.Join(queue, "jobs"))[0])
		os.WriteFile(filepath.Join(job, "processing"), nil, 0o644)
	}
	timeout := 3.0
	_, err := bridge.Export(obj(), filepath.Join(root, "out.c4d"), filepath.Join(root, "tex"), ExportOptions{Timeout: &timeout})
	if err == nil || err.Error() != "Cinema 4D did not finish the export within 0.05 minutes." {
		t.Fatalf("error %v", err)
	}
	jobs := listDir(t, filepath.Join(queue, "jobs"))
	if len(jobs) != 1 || !exists(filepath.Join(queue, "jobs", jobs[0], "cancelled")) {
		t.Fatalf("jobs %v", jobs)
	}
}

func TestMarshalMatchesPythonDumps(t *testing.T) {
	values := []any{
		obj("name", "Wave 日本語", "html", "<a href='x'>&</a>", "controls", "\x00\x01\b\f\n\r\t\x1f\x7f\"\\",
			"separators", "   \\u2028", "emoji", "🙂", "nested", obj("list", []any{1, -2, 3.5, true, false, nil}, "empty", []any{})),
		[]any{Float(1), Float(0.1), Float(1e16), Float(1.5e-5), Float(-0.0), Float(123456.789), Float(1e-4), Float(9999999999999998), Float(1e22), Float(5e-324)},
		"plain", 42, nil,
	}
	for _, value := range values {
		data, err := Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		result := oracle.Exec(t, `
import json
result = json.dumps(json.loads(args[0]), ensure_ascii=False, allow_nan=False, separators=(',', ':'))`, string(data))
		var want string
		json.Unmarshal(result.MustValue(t), &want)
		if want != string(data) {
			t.Errorf("Go     %s\nPython %s", data, want)
		}
	}
	if _, err := Marshal(obj("x", math.NaN())); err == nil || err.Error() != "Out of range float values are not JSON compliant: nan" {
		t.Fatalf("NaN error %v", err)
	}
	if _, err := Marshal(Float(math.Inf(-1))); err == nil || err.Error() != "Out of range float values are not JSON compliant: -inf" {
		t.Fatalf("-inf error %v", err)
	}
}

func TestWriteJSONIsAtomicAndReadJSONEnforcesLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json")
	mustWrite(t, path, obj("a", 1))
	if names := listDir(t, dir); len(names) != 1 || names[0] != "payload.json" {
		t.Fatalf("files %v", names)
	}
	if withSuffix(filepath.Join(dir, "status-x.json"), ".tmp") != filepath.Join(dir, "status-x.tmp") ||
		withSuffix(filepath.Join(dir, ".json"), ".tmp") != filepath.Join(dir, ".json.tmp") ||
		withSuffix(filepath.Join(dir, "a.b.json"), ".tmp") != filepath.Join(dir, "a.b.tmp") {
		t.Fatal("with_suffix")
	}
	if _, err := ReadJSON(path, 3); err != ErrTooLarge {
		t.Fatalf("limit error %v", err)
	}
	missing := filepath.Join(dir, "missing.json")
	wantMissing := oracle.Exec(t, "from owpreview.c4d_bridge import read_json\nresult = read_json(args[0])", missing)
	if _, err := ReadJSON(missing, 10); err == nil || err.Error() != wantMissing.Error {
		t.Fatalf("missing error %v", err)
	}
	result := oracle.Exec(t, `
from owpreview.c4d_bridge import read_json
result = read_json(args[0])`, path)
	jsoncmp.Equal(t, result.MustValue(t), json.RawMessage(`{"a":1}`), jsoncmp.Default)
}

// The Python plugin's own status reader agrees with Go on files Go wrote.
func TestStatusMatchesPythonClient(t *testing.T) {
	queue := t.TempDir()
	current := now()
	mustWrite(t, filepath.Join(queue, "status-a.json"), obj("protocol", 1, "instance", "a", "heartbeat", Float(current-1),
		"bridgeVersion", "0.7.5 ✓", "octane", true, "mapInstances", true, "connected", "stale", "extra", obj("x", []any{1, 2})))
	mustWrite(t, filepath.Join(queue, "status-b.json"), obj("protocol", Float(1), "instance", "b", "heartbeat", Float(current),
		"octane", 1, "mapInstances", 1))
	mustWrite(t, filepath.Join(queue, "status-c.json"), obj("protocol", true, "instance", "c", "heartbeat", Float(current),
		"bridgeVersion", 0.75, "octane", false))
	for _, scenario := range []string{"all", "no-a", "only-c"} {
		switch scenario {
		case "no-a":
			os.Remove(filepath.Join(queue, "status-a.json"))
		case "only-c":
			os.Remove(filepath.Join(queue, "status-b.json"))
		}
		result := oracle.Exec(t, `
from owpreview.c4d_bridge import BridgeClient
import json
result = json.dumps(BridgeClient(args[0]).status(), ensure_ascii=False, separators=(',', ':'))`, queue)
		var want string
		json.Unmarshal(result.MustValue(t), &want)
		got, _ := json.Marshal(NewClient(queue).Status())
		if string(got) != want {
			t.Errorf("%s: Go %s\nPython %s", scenario, got, want)
		}
	}
}

const pythonService = `
import time
from pathlib import Path
from owpreview.c4d_bridge import BridgeService
queue, mode = Path(args[0]), args[1]
seen = {}
def build(payload, path):
    seen['payload'] = payload
    seen['texture'] = (path.parent/'tex/a.png').read_bytes().decode()
    seen['request'] = (path.parent/'request.json').read_text(encoding='utf-8')
    if mode == 'fail':
        path.write_bytes(b'Partial')
        raise ValueError('Native scene failed')
    path.write_bytes(b'Native writer test fixture')
    return {'warnings': ['Fixture only'], 'echo': payload['name']}
service = BridgeService(build, lambda: {'octane': True, 'mapInstances': True, 'bridgeVersion': '0.7.5'}, queue)
deadline = time.monotonic() + 30
while time.monotonic() < deadline and 'payload' not in seen:
    service.tick()
    time.sleep(0.01)
end = time.monotonic() + 1
while time.monotonic() < end:
    service.tick()
    time.sleep(0.01)
result = seen`

// Round-trips a job between the Go client and the real Python BridgeService.
func runWithPythonService(t *testing.T, mode string) (json.RawMessage, error, map[string]any, string) {
	if err := oracle.Available(); err != nil {
		t.Skipf("Python oracle unavailable: %v", err)
	}
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	textures := filepath.Join(root, "tex")
	os.Mkdir(textures, 0o755)
	os.WriteFile(filepath.Join(textures, "a.png"), []byte("Texture fixture"), 0o644)
	os.WriteFile(filepath.Join(textures, "skip.jpg"), []byte("not copied"), 0o644)
	var service oracle.Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		service = oracle.Exec(t, pythonService, queue, mode)
	}()
	bridge := NewClient(queue)
	deadline := time.Now().Add(20 * time.Second)
	for !bridge.Status().Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !bridge.Status().MapInstances() {
		wg.Wait()
		t.Fatalf("Python service status not seen: %s", service)
	}
	target := filepath.Join(root, "out.c4d")
	timeout := 10.0
	payload := obj("name", "Wave 日本語  ", "mapInstances", true, "values", []any{float32(0.1), 1, Float(2)})
	result, err := bridge.Export(payload, target, textures, ExportOptions{Timeout: &timeout})
	wg.Wait()
	var seen map[string]any
	if err := json.Unmarshal(service.MustValue(t), &seen); err != nil {
		t.Fatal(err)
	}
	if len(listDir(t, filepath.Join(queue, "jobs"))) != 0 {
		t.Error("job directory left behind")
	}
	return result, err, seen, target
}

func TestBridgeHandoffWithPythonService(t *testing.T) {
	result, err, seen, target := runWithPythonService(t, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "Native writer test fixture" {
		t.Fatalf("scene %q", data)
	}
	jsoncmp.Equal(t, json.RawMessage(`{"warnings":["Fixture only"],"echo":"Wave 日本語  "}`), result, jsoncmp.Options{})
	jsoncmp.Equal(t, json.RawMessage(`{"name":"Wave 日本語  ","mapInstances":true,"values":[0.1,1,2.0]}`),
		mustJSON(seen["payload"]), jsoncmp.Default)
	if seen["texture"] != "Texture fixture" {
		t.Fatalf("texture %v", seen["texture"])
	}
	if !regexp.MustCompile(`^\{"protocol":1,"instance":"[0-9a-f]{32}","expiresAt":\d+\.\d+\}$`).MatchString(seen["request"].(string)) {
		t.Fatalf("request %s", seen["request"])
	}
}

func TestBridgeNativeFailureDoesNotLeaveAnOutput(t *testing.T) {
	_, err, _, target := runWithPythonService(t, "fail")
	if err == nil || err.Error() != "Native scene failed" {
		t.Fatalf("error %v", err)
	}
	if exists(target) {
		t.Fatal("output published after failure")
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
