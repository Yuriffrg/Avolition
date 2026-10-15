// Command owio-core is the Avolition backend. It speaks the private --stdio
// JSON-lines protocol of the desktop host and serves the developer --serve
// host and --self-test, exactly like app.py did. Every method is answered by
// the Go port of BrowserAPI (internal/api); no Python is started.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"avolition/gocore/internal/api"
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
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["app-dir"] {
		// The packaged layout is <app>/resources/backend/owio-core.exe.
		if executable, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(executable); err == nil {
				executable = resolved
			}
			if dir := filepath.Dir(executable); filepath.Base(dir) == "backend" && filepath.Base(filepath.Dir(dir)) == "resources" {
				*appDir = filepath.Dir(filepath.Dir(dir))
			}
		}
	}

	a, err := api.New(api.Options{DataDir: *dataDir, InitialRoot: *root, DesktopMode: *stdio, AppDir: *appDir})
	if err != nil {
		return err
	}
	if *selfTest != "" {
		return api.SelfTest(a, *root, *selfTest)
	}
	defer a.Shutdown()
	if *stdio {
		return a.ServeStdio(os.Stdin, os.Stdout)
	}
	webRoot, err := api.FindWebRoot()
	if err != nil {
		return err
	}
	server, err := api.NewServer(a, webRoot, *port)
	if err != nil {
		return err
	}
	fmt.Println("Avolition developer server: http://127.0.0.1:" + strconv.Itoa(server.Port()))
	return server.Serve()
}
