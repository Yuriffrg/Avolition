package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildCore(t *testing.T) (binary, repo string) {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(t.TempDir(), "owio-core")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binary, repo
}

// The developer server: URL line, token bridge injection, token-protected
// POST /api/<method>, close_app shutdown.
func TestDeveloperServer(t *testing.T) {
	binary, repo := buildCore(t)
	cmd := exec.Command(binary, "--serve", "--port", "0", "--data-dir", filepath.Join(t.TempDir(), "profile"))
	cmd.Env = append(os.Environ(), "AVOLITION_WEB_ROOT="+filepath.Join(repo, "web"))
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`^Avolition developer server: (http://127\.0\.0\.1:\d+)\n$`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("URL line %q", line)
	}
	base := match[1]
	response, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	token := regexp.MustCompile(`"X-Preview-Token":"([A-Za-z0-9_-]{43})"`).FindStringSubmatch(string(page))
	if token == nil || !strings.Contains(string(page), `for(const method of ["bootstrap", "browse_root",`) {
		t.Fatalf("bridge not injected: %.300s", page)
	}
	post := func(method, token, body string) (int, map[string]any) {
		request, _ := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("X-Preview-Token", token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var reply map[string]any
		_ = json.NewDecoder(response.Body).Decode(&reply)
		return response.StatusCode, reply
	}
	if status, _ := post("ping", "", "[]"); status != http.StatusForbidden {
		t.Fatalf("missing token: %d", status)
	}
	if status, _ := post("_save", token[1], "[]"); status != http.StatusForbidden {
		t.Fatalf("private method: %d", status)
	}
	if status, reply := post("ping", token[1], "[]"); status != 200 || reply["ok"] != true || reply["data"] != true {
		t.Fatalf("ping: %d %v", status, reply)
	}
	if _, reply := post("bootstrap", token[1], "{}"); reply["ok"] != false || reply["error"] != "Expected arguments" {
		t.Fatalf("bad arguments: %v", reply)
	}
	if _, reply := post("bootstrap", token[1], "[]"); reply["ok"] != true || reply["data"].(map[string]any)["browserMode"] != true {
		t.Fatalf("bootstrap: %v", reply)
	}
	if response, err := http.Get(base + "/styles.css"); err != nil || response.StatusCode != 200 {
		t.Fatalf("static file: %v", err)
	}
	post("close_app", token[1], "[]")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("close_app did not stop the server")
	}
}

func TestSelfTestReport(t *testing.T) {
	binary, repo := buildCore(t)
	report := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command(binary, "--self-test", report, "--root", filepath.Join(repo, "tests", "demo-assets"),
		"--data-dir", filepath.Join(t.TempDir(), "profile"))
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var checks map[string]any
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	if checks["ok"] != true || checks["scan"].(map[string]any)["ok"] != true || checks["bootstrap"].(map[string]any)["ok"] != true {
		t.Fatalf("report: %.300s", data)
	}
	if !strings.HasPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "{\n  \"ok\": true,\n  \"bootstrap\": {\n    \"ok\": true,") {
		t.Fatalf("formatting: %.200s", data)
	}
}
