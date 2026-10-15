package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end: the built core replaying the desktop scenarios of
// tests/test_stdio.py against tests/demo-assets over the real stdio pipe.
func TestStdioEndToEnd(t *testing.T) {
	binary, repo := buildCore(t)
	demo := filepath.Join(repo, "tests", "demo-assets")
	entity := filepath.Join(demo, "Entities", "PreviewRobot", "PreviewRobot.owentity")
	clip := filepath.Join(demo, "Animations", "001_Wave.owanimclip")
	requests := []map[string]any{
		{"id": 1, "method": "bootstrap"},
		{"id": "日本語-id", "method": "ping", "args": []any{}},
		{"id": "scan", "method": "scan_root", "args": []any{demo}},
		{"id": "entity", "method": "load_entity", "args": []any{entity}},
		{"id": "clip", "method": "load_animation", "args": []any{clip}},
		{"id": "broken", "method": "load_entity", "args": []any{filepath.Join(demo, "missing.owentity")}},
		{"id": "private", "method": "_save"},
		{"id": "too-many-arguments", "method": "bootstrap", "args": []any{"extra"}},
		{"id": "recovered", "method": "bootstrap"},
	}
	var input strings.Builder
	for _, request := range requests {
		line, _ := json.Marshal(request)
		input.Write(line)
		input.WriteByte('\n')
	}
	input.WriteString("{bad json\n")

	cmd := exec.Command(binary, "--stdio", "--data-dir", filepath.Join(t.TempDir(), "state"), "--app-dir", repo)
	// No Python is involved: an unusable interpreter must not matter.
	cmd.Env = append(os.Environ(), "AVOLITION_PYTHON_CORE=/nonexistent/app.py", "PATH=/nonexistent")
	cmd.Stdin = strings.NewReader(input.String())
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	replies := map[string]map[string]any{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(nil, 256<<20)
	for scanner.Scan() {
		var reply struct {
			ID     json.RawMessage
			Result map[string]any
		}
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatalf("unparseable reply: %v", err)
		}
		replies[string(reply.ID)] = reply.Result
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("core exited with %v\n%s", err, stderr.String())
	}
	if len(replies) != len(requests)+1 {
		t.Fatalf("got %d replies for %d requests", len(replies), len(requests)+1)
	}
	ok := func(id string) map[string]any {
		t.Helper()
		result := replies[id]
		if result["ok"] != true {
			t.Fatalf("%s failed: %v", id, result)
		}
		data, _ := result["data"].(map[string]any)
		return data
	}
	if data := ok("1"); data["appName"] != "Avolition" || data["browserMode"] != false {
		t.Errorf("bootstrap: %v", data)
	}
	if replies[`"日本語-id"`]["data"] != true {
		t.Errorf("ping: %v", replies[`"日本語-id"`])
	}
	ok(`"scan"`)
	if models, _ := ok(`"entity"`)["models"].([]any); len(models) == 0 {
		t.Error("entity loaded without models")
	}
	if count, _ := ok(`"clip"`)["boneCount"].(float64); count <= 0 {
		t.Error("clip loaded without bones")
	}
	for _, id := range []string{`"broken"`, `"private"`, `"too-many-arguments"`, "null"} {
		if replies[id]["ok"] != false || replies[id]["error"] == "" {
			t.Errorf("%s: %v", id, replies[id])
		}
	}
	ok(`"recovered"`)
}
