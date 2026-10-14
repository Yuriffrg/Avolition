// Command owio-core is the Go core of Avolition. It speaks the same private
// --stdio JSON-lines protocol as app.py, answers ported methods itself and
// forwards the rest to the Python core, which it starts on first use.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"avolition/gocore/internal/pycore"
	"avolition/gocore/internal/router"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "owio-core:", err)
		var exit exitCode
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return "exit code " + strconv.Itoa(int(e)) }

func run() error {
	flags := flag.NewFlagSet("owio-core", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Avolition asset backend; launch Avolition.exe for the desktop app.")
		flags.PrintDefaults()
	}
	stdio := flags.Bool("stdio", false, "Private transport used by the Avolition desktop host")
	serve := flags.Bool("serve", false, "Developer-only loopback test server (handled by the Python core)")
	port := flags.Int("port", 8765, "Developer server port")
	root := flags.String("root", "", "Initial library root")
	dataDir := flags.String("data-dir", "", "Profile directory")
	appDir := flags.String("app-dir", "", "Portable app directory supplied by the desktop host")
	selfTest := flags.String("self-test", "", "Write a self-test report to this path (handled by the Python core)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return exitCode(2)
	}
	if !*stdio && !*serve && *selfTest == "" {
		flags.Usage()
		return exitCode(2)
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	command, locateErr := pycore.Locate(filepath.Dir(executable))

	// Same options, in app.py's spelling, for whichever core receives them.
	var options []string
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["root"] {
		options = append(options, "--root", *root)
	}
	if set["data-dir"] {
		options = append(options, "--data-dir", *dataDir)
	}
	if set["app-dir"] {
		options = append(options, "--app-dir", *appDir)
	} else if dir := filepath.Dir(executable); filepath.Base(dir) == "backend" && filepath.Base(filepath.Dir(dir)) == "resources" {
		// app.py derives the portable folder from its own location, which
		// moves to resources/backend/python-core behind the Go core.
		options = append(options, "--app-dir", filepath.Dir(filepath.Dir(dir)))
	}

	if !*stdio {
		// The developer server and self-test are not ported yet: run Python.
		if locateErr != nil {
			return locateErr
		}
		if *serve {
			options = append(options, "--serve", "--port", strconv.Itoa(*port))
		}
		if *selfTest != "" {
			options = append(options, "--self-test", *selfTest)
		}
		return delegate(command, options)
	}

	backend := &python{command: command, locateErr: locateErr, args: append([]string{"--stdio"}, options...)}
	return router.New(os.Stdout, backend, router.Native()).Serve(os.Stdin)
}

// python adapts pycore to the router's lazily started Backend.
type python struct {
	command   pycore.Command
	locateErr error
	args      []string
	process   *pycore.Process
}

func (p *python) Start() (io.WriteCloser, io.Reader, error) {
	if p.locateErr != nil {
		return nil, nil, p.locateErr
	}
	process, err := pycore.Start(p.command, p.args)
	if err != nil {
		return nil, nil, err
	}
	p.process = process
	return process.Stdin, process.Stdout, nil
}

func (p *python) Wait() error { return p.process.Wait() }

func delegate(command pycore.Command, options []string) error {
	cmd := exec.Command(command.Path, append(append([]string{}, command.Args...), options...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitCode(exit.ExitCode())
	}
	return err
}
