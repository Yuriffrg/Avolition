// Package oracle runs the reference Python implementation from Go tests, so
// every ported function can be compared with the original on the same input.
// Tests skip when python3 with numpy and Pillow is unavailable.
package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// ReplaceJSONPath substitutes an isolated fixture root inside encoded JSON.
// Windows separators are escaped in JSON, so replacing the raw path misses them.
func ReplaceJSONPath(text, oldPath, newPath string) string {
	oldJSON, _ := json.Marshal(oldPath)
	newJSON, _ := json.Marshal(newPath)
	return strings.ReplaceAll(text, string(oldJSON[1:len(oldJSON)-1]), string(newJSON[1:len(newJSON)-1]))
}

var (
	checkOnce sync.Once
	checkErr  error
)

func script() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "oracle", "oracle.py")
}

// Interpreter honors the same Python override in every reference test.
func Interpreter() string {
	if value := os.Getenv("AVOLITION_PYTHON"); value != "" {
		return value
	}
	return "python3"
}

// Available reports whether the Python reference can run here.
func Available() error {
	checkOnce.Do(func() {
		if _, err := os.Stat(script()); err != nil {
			checkErr = err
			return
		}
		checkErr = exec.Command(Interpreter(), "-X", "utf8", "-c", "import numpy, PIL").Run()
	})
	return checkErr
}

// Result is the oracle's reply. On failure Error and Type describe the raised
// Python exception, so tests can compare error messages too.
type Result struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"result"`
	Error string          `json:"error"`
	Type  string          `json:"type"`
}

// Call runs module:function(*args) in Python, e.g. "owpreview.formats:load_asset".
func Call(t testing.TB, function string, args ...any) Result {
	t.Helper()
	return run(t, map[string]any{"call": function, "args": nonNil(args)})
}

// CallKw is Call with keyword arguments.
func CallKw(t testing.TB, function string, kwargs map[string]any, args ...any) Result {
	t.Helper()
	return run(t, map[string]any{"call": function, "args": nonNil(args), "kwargs": kwargs})
}

// Exec runs Python code that assigns `result`; `args` and `kwargs` are bound.
func Exec(t testing.TB, code string, args ...any) Result {
	t.Helper()
	return run(t, map[string]any{"exec": code, "args": nonNil(args)})
}

func nonNil(args []any) []any {
	if args == nil {
		return []any{}
	}
	return args
}

func run(t testing.TB, request map[string]any) Result {
	t.Helper()
	if err := Available(); err != nil {
		t.Skipf("Python oracle unavailable: %v", err)
	}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	// -I ignores PYTHONUTF8; select UTF-8 explicitly for Windows pipe I/O.
	cmd := exec.Command(Interpreter(), "-I", "-X", "utf8", script())
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\n%s", err, stderr.String())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("oracle output: %v\n%s", err, stdout.String())
	}
	return result
}

// MustValue returns the successful result or fails the test.
func (r Result) MustValue(t testing.TB) json.RawMessage {
	t.Helper()
	if !r.OK {
		t.Fatalf("oracle raised %s: %s", r.Type, r.Error)
	}
	return r.Value
}

func (r Result) String() string {
	if r.OK {
		return string(r.Value)
	}
	return fmt.Sprintf("%s: %s", r.Type, r.Error)
}
