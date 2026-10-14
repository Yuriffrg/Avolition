package datatool

// Test fixtures shared by the ports of tests/test_datatool.py and
// tests/test_datatool_maps.py. The DataTool process is this test binary
// running fakeDataTool: a harmless child which exercises argument transport,
// stdout/stderr streaming and process-tree cancellation without Overwatch.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

const fakeEnv = "AVOLITION_FAKE_DATATOOL"

const mapsJSON = `{"000000000001.09F": {"GUID": "000000000001.09F", "Name": "King's Row", "VariantName": "King’s Row 日本語", "MapGUID": "000000000D86.002", "MapType": "PVP", "Description": "Fixture map", "StateA": "Night", "StateB": null, "GameModes": [{"Name": "Hybrid"}]}, "000000000002.09F": {"Name": "King's Row", "MapGUID": "000000000D87.002", "MapType": "PVE"}}`

func TestMain(m *testing.M) {
	switch os.Getenv(fakeEnv) {
	case "run":
		os.Exit(fakeDataTool(os.Args[1:]))
	case "sleep":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func spawnChild() int {
	self, _ := os.Executable()
	child := exec.Command(self)
	child.Env = append(os.Environ(), fakeEnv+"=sleep")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	return child.Process.Pid
}

func fakeDataTool(args []string) int {
	mode := args[1]
	cwd, _ := os.Getwd()
	var override any
	if value, ok := os.LookupEnv("DATATOOL_OVERWATCH_DIR"); ok {
		override = value
	}
	payload, _ := json.Marshal(struct {
		Argv     []string `json:"argv"`
		Cwd      string   `json:"cwd"`
		Override any      `json:"override"`
	}{args, cwd, override})
	fmt.Println(string(payload))
	switch {
	case strings.HasPrefix(mode, "list-"):
		destination := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--out=") {
				destination = arg[6:]
			}
		}
		value := `{"Lúcio": [{"Name": "Overwatch Classic", "Type": "Skin", "GUID": "002.0A5"}, {"Name": "Dance", "Type": "Emote", "GUID": "003.0A5"}]}`
		switch mode {
		case "list-maps":
			value = mapsJSON
		case "list-heroes":
			value = `{"001.075": {"Name": "Lúcio", "IsHero": true}}`
		}
		os.WriteFile(destination, []byte(value), 0o666)
	case strings.Contains(args[3], "FailCode"):
		fmt.Fprintln(os.Stderr, "fixture extraction error")
		return 7
	case strings.Contains(args[3], "FailZero"):
		fmt.Fprintln(os.Stderr, `[Query] Found nothing matching your query of "bad"`)
	case strings.Contains(args[3], "Unsupported"):
		fmt.Fprintln(os.Stderr, "[CASC] This version of DataTool does not support this version of Overwatch.")
		return 1
	case strings.Contains(args[3], "NoMarker"):
		return 0
	case strings.Contains(args[3], "ParentExit"):
		fmt.Printf("CHILD=%d\n", spawnChild())
	case strings.Contains(args[3], "Wait"):
		fmt.Printf("CHILD=%d\n", spawnChild())
		time.Sleep(60 * time.Second)
	default:
		name := "fixture.owanimclip"
		if mode == "extract-maps" {
			name = "fixture.owmap"
		}
		os.WriteFile(filepath.Join(args[2], name), []byte("fixture only"), 0o666)
	}
	fmt.Println("\x1b[32m[Core] Execution finished in 00:00:00.01\x1b[0m")
	return 0
}

type call struct {
	command Command
	cmd     *exec.Cmd
}

type fixture struct {
	root, game, output, exe string
	lock                    *sync.Mutex
	jobs                    *Jobs

	mu    sync.Mutex
	calls []call
}

func setup(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		root:   root,
		game:   filepath.Join(root, "Overwatch 安装 with spaces"),
		output: filepath.Join(root, "Export 输出 files"),
		exe:    filepath.Join(root, "runtime complete", "DataTool.exe"),
		lock:   &sync.Mutex{},
	}
	os.MkdirAll(filepath.Join(f.game, "data"), 0o777)
	os.WriteFile(filepath.Join(f.game, ".build.info"), []byte("Product!STRING:0|Version!STRING:0\npro|2.0\n"), 0o666)
	os.Mkdir(f.output, 0o777)
	os.Mkdir(filepath.Dir(f.exe), 0o777)
	os.WriteFile(f.exe, []byte("fixture placeholder; never executed"), 0o666)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	factory := func(command Command) (*exec.Cmd, error) {
		cmd := exec.Command(self, command.Args[1:]...)
		cmd.Dir = command.Dir
		cmd.Env = append(append([]string{}, command.Env...), fakeEnv+"=run")
		f.mu.Lock()
		f.calls = append(f.calls, call{command, cmd})
		f.mu.Unlock()
		return cmd, nil
	}
	f.jobs = NewJobs(filepath.Join(root, "job state"), f.lock, factory)
	t.Cleanup(f.jobs.Close)
	return f
}

func (f *fixture) lastCall() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fixture) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fixture) wait(t *testing.T) Snapshot {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for f.jobs.Snapshot().Busy && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	result := f.jobs.Snapshot()
	if result.Busy {
		t.Fatalf("still busy: %s", asJSON(result))
	}
	return result
}

func (f *fixture) seed(names ...string) {
	if len(names) == 0 {
		names = []string{"Overwatch Classic"}
	}
	unlocks := ordered.New[[]Unlock]()
	var rows []Unlock
	for i, name := range names {
		rows = append(rows, Unlock{ID: fmt.Sprint(i), Name: name, Category: "skin"})
	}
	unlocks.Set("Lúcio", rows)
	f.jobs.mu.Lock()
	f.jobs.catalog = &Catalog{Install: f.game, Heroes: []Hero{{"001", "Lúcio"}}, Unlocks: unlocks}
	f.jobs.mu.Unlock()
}

func (f *fixture) seedMaps(t *testing.T, name string) {
	value, _ := pycompat.Loads([]byte(mapsJSON))
	if name != "" {
		row, _ := value.(*ordered.Map[any]).Get("000000000001.09F")
		row.(*ordered.Map[any]).Set("VariantName", name)
	}
	catalog, err := ParseMapCatalog(value, f.game)
	if err != nil {
		t.Fatal(err)
	}
	f.jobs.mu.Lock()
	f.jobs.mapCatalog = catalog
	f.jobs.mu.Unlock()
}

func (f *fixture) export(t *testing.T, asset string, options map[string]any) (Snapshot, error) {
	f.seed(asset)
	selection := map[string]any{"hero": "Lúcio", "asset": asset, "category": "skin"}
	for k, v := range options {
		selection[k] = v
	}
	return f.jobs.StartExport(f.exe, f.game, f.output, selection)
}

func (f *fixture) waitForChild(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range f.jobs.Snapshot().Logs {
			if strings.HasPrefix(line.Text, "CHILD=") {
				var pid int
				fmt.Sscan(strings.TrimPrefix(line.Text, "CHILD="), &pid)
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("No child launched: %s", asJSON(f.jobs.Snapshot()))
	return 0
}

func asJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func errText(s Snapshot) string {
	if s.Error == nil {
		return ""
	}
	return *s.Error
}
