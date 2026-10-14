// Package pycore locates and starts the existing Python core (app.py, or its
// PyInstaller build) so the Go core can forward methods it has not ported yet.
package pycore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvCore overrides the Python core: a frozen executable, or a .py script.
const EnvCore = "AVOLITION_PYTHON_CORE"

// EnvInterpreter selects the interpreter used to run a .py core.
const EnvInterpreter = "AVOLITION_PYTHON"

// PackagedDir is the folder beside the Go executable holding the frozen
// Python core in a portable build.
const PackagedDir = "python-core"

// Command is how to launch the Python core, before its --stdio arguments.
type Command struct {
	Path string
	Args []string
}

// Locate finds the Python core: the environment override first, then the
// packaged layout beside exeDir, then app.py in exeDir or up to three parents
// (a source checkout). Like app.py it never consults the working directory.
func Locate(exeDir string) (Command, error) {
	if override := os.Getenv(EnvCore); override != "" {
		return fromPath(override)
	}
	packaged := filepath.Join(exeDir, PackagedDir, "owio-core"+exeSuffix())
	if isFile(packaged) {
		return Command{Path: packaged}, nil
	}
	dir := exeDir
	for range 4 {
		if script := filepath.Join(dir, "app.py"); isFile(script) {
			return fromPath(script)
		}
		dir = filepath.Dir(dir)
	}
	return Command{}, fmt.Errorf("the Python core was not found beside %s; set %s", exeDir, EnvCore)
}

func fromPath(path string) (Command, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Command{}, err
	}
	if !isFile(path) {
		return Command{}, fmt.Errorf("the Python core does not exist: %s", path)
	}
	if !strings.EqualFold(filepath.Ext(path), ".py") {
		return Command{Path: path}, nil
	}
	interpreter := os.Getenv(EnvInterpreter)
	if interpreter == "" {
		interpreter = "python3"
		if runtime.GOOS == "windows" {
			interpreter = "python"
		}
	}
	resolved, err := exec.LookPath(interpreter)
	if err != nil {
		return Command{}, fmt.Errorf("python interpreter %q was not found; set %s", interpreter, EnvInterpreter)
	}
	// -u keeps the JSON-lines stream unbuffered even without PYTHONUNBUFFERED.
	return Command{Path: resolved, Args: []string{"-u", path}}, nil
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// Process is a running Python core with its protocol pipes.
type Process struct {
	cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	guard  io.Closer
}

// Start launches the core with extra arguments (normally --stdio and the
// desktop options). Its stderr is shared so the desktop log keeps capturing it.
func Start(command Command, extra []string) (*Process, error) {
	cmd := exec.Command(command.Path, append(append([]string{}, command.Args...), extra...)...)
	cmd.Dir = filepath.Dir(command.Path)
	if len(command.Args) > 1 {
		cmd.Dir = filepath.Dir(command.Args[len(command.Args)-1])
	}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	cmd.Stderr = os.Stderr
	configure(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start the Python core: %w", err)
	}
	guard, err := bind(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("could not supervise the Python core: %w", err)
	}
	return &Process{cmd: cmd, Stdin: stdin, Stdout: stdout, guard: guard}, nil
}

// Wait waits for exit after Stdout has been drained, then releases the guard.
func (p *Process) Wait() error {
	err := p.cmd.Wait()
	if p.guard != nil {
		_ = p.guard.Close()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("the Python core exited with code %d", exit.ExitCode())
	}
	return err
}
