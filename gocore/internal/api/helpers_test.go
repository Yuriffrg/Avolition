package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"avolition/gocore/internal/pycompat"
)

// repo is the source checkout (tests/, web/, desktop/).
func repo(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func demoAssets(t testing.TB) string { return filepath.Join(repo(t), "tests", "demo-assets") }

// isolate clears the profile environment variables for one test.
func isolate(t testing.TB) {
	t.Helper()
	for _, name := range []string{"AVOLITION_DATA_DIR", "WHITEHOUND_DATA_DIR", "OWIO_DATA_DIR", "OWPREVIEW_DATA_DIR"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

// tempDir is a resolved temporary directory (Path.resolve() in Python tests).
func tempDir(t testing.TB) string {
	t.Helper()
	return pycompat.Resolve(t.TempDir())
}

func newAPI(t testing.TB, options Options) *API {
	t.Helper()
	if options.AppDir == "" {
		options.AppDir = filepath.Join(tempDir(t), "App")
	}
	a, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Shutdown)
	return a
}

// pyValue converts a Go literal (maps, slices, strings, numbers) to the
// shapes the API receives from json.loads.
func pyValue(t testing.TB, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := pycompat.LoadsText(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// Reply is a decoded {ok, data, error} result.
type Reply struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data"`
	Error string `json:"error"`
	raw   []byte
}

// call runs a method like BrowserAPI's public methods and decodes the reply
// exactly as the desktop host would.
func call(t testing.TB, a *API, method string, args ...any) Reply {
	t.Helper()
	converted := make([]any, len(args))
	for i, arg := range args {
		converted[i] = pyValue(t, arg)
	}
	data, err := a.Call(method, converted)
	raw := AppendResult(nil, data, err)
	var reply Reply
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("%s: undecodable reply %s: %v", method, raw, err)
	}
	reply.raw = raw
	return reply
}

func ok(t testing.TB, reply Reply) any {
	t.Helper()
	if !reply.OK {
		t.Fatalf("expected ok, got error %q", reply.Error)
	}
	return reply.Data
}

func okMap(t testing.TB, reply Reply) map[string]any {
	t.Helper()
	data, isMap := ok(t, reply).(map[string]any)
	if !isMap {
		t.Fatalf("data is %T", reply.Data)
	}
	return data
}

func failed(t testing.TB, reply Reply) string {
	t.Helper()
	if reply.OK {
		t.Fatalf("expected an error, got %s", reply.raw)
	}
	if reply.Error == "" {
		t.Fatal("error reply without message")
	}
	return reply.Error
}

func m(value any) map[string]any { out, _ := value.(map[string]any); return out }
func l(value any) []any          { out, _ := value.([]any); return out }

func jsonEqual(t testing.TB, a, b any) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	_ = json.Unmarshal(left, &x)
	_ = json.Unmarshal(right, &y)
	l2, _ := json.Marshal(x)
	r2, _ := json.Marshal(y)
	return string(l2) == string(r2)
}

func assertJSON(t testing.TB, got, want any) {
	t.Helper()
	if !jsonEqual(t, got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("got %s\nwant %s", g, w)
	}
}

func readJSON(t testing.TB, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t testing.TB, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readBytes(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }
