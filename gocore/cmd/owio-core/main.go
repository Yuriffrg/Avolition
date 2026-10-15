// Command owio-core is the Go core of Avolition. It speaks the same private
// --stdio JSON-lines protocol as app.py and serves the developer --serve host
// and --self-test.
//
// When every module BrowserAPI depends on is ported (api.Missing is empty),
// the Go API answers everything and the Python core is never started.
// Otherwise only the stateless methods (api.Stateless) are answered in Go and
// the rest is forwarded to the Python core, which then keeps sole ownership
// of catalog.json and the loaded-asset registries. AVOLITION_GO_API=1 forces
// the Go API for every mode (methods whose modules are missing then fail).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"avolition/gocore/internal/api"
	"avolition/gocore/internal/pycore"
	"avolition/gocore/internal/router"
)

func main() {
	if err := run(); err != nil {
		var exit exitCode
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "owio-core:", err)
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
	serve := flags.Bool("serve", false, "Developer-only loopback test server; never opens a browser")
	port := flags.Int("port", 8765, "Developer server port")
	root := flags.String("root", "", "Initial library root")
	dataDir := flags.String("data-dir", "", "Profile directory")
	appDir := flags.String("app-dir", "", "Portable app directory supplied by the desktop host")
	selfTest := flags.String("self-test", "", "Write a self-test report to this path")
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
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["app-dir"] {
		if dir := filepath.Dir(executable); filepath.Base(dir) == "backend" && filepath.Base(filepath.Dir(dir)) == "resources" {
			*appDir = filepath.Dir(filepath.Dir(dir))
		}
	}
	options := api.Options{DataDir: *dataDir, InitialRoot: *root, DesktopMode: *stdio, AppDir: *appDir}

	if len(api.Missing(api.Default())) == 0 || os.Getenv("AVOLITION_GO_API") == "1" {
		return runGo(options, *stdio, *serve, *port, *root, *selfTest)
	}

	// Hybrid phase: Python owns all state.
	command, locateErr := pycore.Locate(filepath.Dir(executable))
	var pyOptions []string
	if set["root"] {
		pyOptions = append(pyOptions, "--root", *root)
	}
	if set["data-dir"] {
		pyOptions = append(pyOptions, "--data-dir", *dataDir)
	}
	if *appDir != "" {
		// app.py derives the portable folder from its own location, which
		// moves to resources/backend/python-core behind the Go core.
		pyOptions = append(pyOptions, "--app-dir", *appDir)
	}
	if !*stdio {
		if locateErr != nil {
			return locateErr
		}
		if *serve {
			pyOptions = append(pyOptions, "--serve", "--port", strconv.Itoa(*port))
		}
		if *selfTest != "" {
			pyOptions = append(pyOptions, "--self-test", *selfTest)
		}
		return delegate(command, pyOptions)
	}
	stateless := api.NewStateless(options)
	native := map[string]router.Handler{}
	for _, name := range api.Stateless {
		if stateless.Unavailable(name) != nil {
			continue
		}
		native[name] = handler(stateless, name)
	}
	backend := &python{command: command, locateErr: locateErr, args: append([]string{"--stdio"}, pyOptions...)}
	return router.New(os.Stdout, backend, native).Serve(os.Stdin)
}

func handler(a *api.API, name string) router.Handler {
	return func(raw []json.RawMessage) (any, error) {
		args, err := api.DecodeArgs(raw)
		if err != nil {
			return nil, err
		}
		data, err := a.Call(name, args)
		if err != nil {
			return nil, errors.New(api.ErrorText(err))
		}
		return data, nil
	}
}

func runGo(options api.Options, stdio, serve bool, port int, root, selfTest string) error {
	a, err := api.New(options)
	if err != nil {
		return err
	}
	if selfTest != "" {
		return api.SelfTest(a, root, selfTest)
	}
	if stdio {
		defer a.Shutdown()
		return a.ServeStdio(os.Stdin, os.Stdout)
	}
	_ = serve
	webRoot, err := api.FindWebRoot()
	if err != nil {
		return err
	}
	server, err := api.NewServer(a, webRoot, port)
	if err != nil {
		return err
	}
	defer a.Shutdown()
	fmt.Println("Avolition developer server: http://127.0.0.1:" + strconv.Itoa(server.Port()))
	return server.Serve()
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
