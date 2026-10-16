package main

// Runs this command and tools/analyze_library.py with the same arguments
// and compares exit codes, stdout, stderr and the written report. The test
// binary re-executes itself as the command with argv[0] "analyze_library.py",
// so argparse's usage text names the same program.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"avolition/gocore/internal/oracle"
)

func TestMain(m *testing.M) {
	if os.Getenv("ANALYZE_LIBRARY_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type run struct {
	code           int
	stdout, stderr string
	report         string
}

var analyzedAt = regexp.MustCompile(`"analyzedAt": "[^"]*"`)

func execute(t *testing.T, cmd *exec.Cmd, dir, output string) run {
	t.Helper()
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "COLUMNS=80", "ANALYZE_LIBRARY_RUN_MAIN=1")
	err := cmd.Run()
	result := run{stdout: strings.ReplaceAll(stdout.String(), "\r\n", "\n"), stderr: strings.ReplaceAll(stderr.String(), "\r\n", "\n")}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(output); err == nil {
		result.report = analyzedAt.ReplaceAllString(strings.ReplaceAll(string(data), "\r\n", "\n"), `"analyzedAt": ""`)
		os.Remove(output)
	}
	return result
}

func TestCommandMatchesPython(t *testing.T) {
	if err := oracle.Available(); err != nil {
		t.Skip(err)
	}
	repo, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	script := filepath.Join(repo, "tools", "analyze_library.py")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	demo := filepath.Join(repo, "tests", "demo-assets")
	catalog := filepath.Join(dir, "catalog.json")
	clipKey, _ := json.Marshal(filepath.Join(demo, "Animations", "x"))
	os.WriteFile(catalog, []byte(`{"annotations": {`+string(clipKey)+`: {"name": "日本"}}, "other": 1.50}`), 0o644)
	noAnnotations := filepath.Join(dir, "no-annotations.json")
	os.WriteFile(noAnnotations, []byte(`{"other": 1}`), 0o644)
	listAnnotations := filepath.Join(dir, "list-annotations.json")
	os.WriteFile(listAnnotations, []byte(`{"annotations": []}`), 0o644)
	toolchain := filepath.Join(dir, "active.json")
	os.WriteFile(toolchain, []byte(`{"package": {"version": "2.1", "size": 1e3, "count": 3}}`), 0o644)
	output := filepath.Join(dir, "out", "report 日本.json")
	cases := [][]string{
		{"-h"},
		{"--help"},
		{},
		{"--root", demo},
		{"--root"},
		{"--root", demo, "--output", filepath.Join(demo, "x.json")},
		{"--root", demo, "--output", output},
		{"--root=" + demo, "--output=" + output, "--catalog", catalog, "--toolchain", toolchain},
		{"--ro", demo, "--out", output, "--cat", noAnnotations},
		{"--root", demo, "--output", output, "--catalog", listAnnotations},
		{"--root", demo, "--output", output, "--catalog", "missing.json"},
		{"--root", demo, "--output", output, "--bogus"},
		{"--root", demo, "--output", output, "extra", "args"},
		{"--root", demo, "--output", output, "--root", filepath.Join(repo, "tests", "material-assets")},
	}
	for _, args := range cases {
		label := strings.Join(args, " ")
		want := execute(t, exec.Command(oracle.Interpreter(), append([]string{"-X", "utf8", script}, args...)...), dir, output)
		cmd := &exec.Cmd{Path: self, Args: append([]string{"analyze_library.py"}, args...)}
		got := execute(t, cmd, dir, output)
		if want.code != got.code || want.stdout != got.stdout || want.stderr != got.stderr || want.report != got.report {
			t.Errorf("%s:\nwant %d %q %q (%d report bytes)\ngot  %d %q %q (%d report bytes)", label,
				want.code, want.stdout, want.stderr, len(want.report), got.code, got.stdout, got.stderr, len(got.report))
		}
	}
}
