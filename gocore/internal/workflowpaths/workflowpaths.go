// Package workflowpaths ports owpreview/workflow_paths.py: portable
// application folders and shared workflow settings.
//
// Electron supplies the directory containing Whitehound.exe. Source runs use
// the source directory, never the shell's current working directory. The
// user's library/toolchain folders therefore keep their established portable
// locations.
package workflowpaths

import (
	"errors"
	"os"
	"path/filepath"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// Catalog is the mutable catalog.json document WorkflowPaths reads and
// updates in place (the Python code shares the BrowserAPI's dict).
// *ordered.Map[any] satisfies it; MapCatalog adapts a plain map.
type Catalog interface {
	Get(key string) (any, bool)
	Set(key string, value any)
}

// MapCatalog adapts a map[string]any to Catalog.
type MapCatalog map[string]any

func (m MapCatalog) Get(key string) (any, bool) { v, ok := m[key]; return v, ok }
func (m MapCatalog) Set(key string, value any)  { m[key] = value }

var (
	workflowTabs  = []string{"animations", "maps", "toolchain"}
	toolchainTabs = []string{"heroes", "maps"}
)

func oneOf(value any, allowed []string) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	for _, candidate := range allowed {
		if text == candidate {
			return text, true
		}
	}
	return "", false
}

// AbsoluteFolder is absolute_folder(value, label): strip, unquote, expand
// variables and ~, require an absolute path and resolve it. Empty input
// returns "".
func AbsoluteFolder(value any, label string) (string, error) {
	clean := ""
	if pycompat.Truthy(value) {
		clean = pycompat.Strip(pycompat.Str(value))
	}
	runes := []rune(clean)
	if len(runes) > 1 && runes[0] == runes[len(runes)-1] && (runes[0] == '"' || runes[0] == '\'') {
		clean = pycompat.Strip(string(runes[1 : len(runes)-1]))
	}
	if clean == "" {
		return "", nil
	}
	path, err := pycompat.ExpandUser(pycompat.ExpandVars(clean))
	if err != nil {
		return "", err
	}
	if !pycompat.IsAbs(path) {
		return "", errors.New(label + ": use a full, absolute folder path.")
	}
	return pycompat.Resolve(path), nil
}

// executable is overridden in tests.
var executable = os.Executable

// ApplicationDirectory is application_directory(explicit). Without an
// explicit folder the Go core is the frozen backend: the packaged layout is
// Whitehound/resources/backend/owio-core.exe. A development build inside a
// source checkout (app.py up to three folders above the binary) uses the
// source directory, like a Python source run.
func ApplicationDirectory(explicit string) (string, error) {
	if explicit != "" {
		folder, err := AbsoluteFolder(explicit, "Application folder")
		if err != nil {
			return "", err
		}
		return pycompat.PathStr(folder), nil
	}
	binary, err := executable()
	if err != nil {
		return "", err
	}
	binary = pycompat.Resolve(binary)
	parent := pycompat.Parent(binary)
	if pycompat.Name(parent) == "backend" && pycompat.Name(pycompat.Parent(parent)) == "resources" {
		return pycompat.Parent(pycompat.Parent(parent)), nil
	}
	folder := parent
	for range 4 {
		if pycompat.IsFile(filepath.Join(folder, "app.py")) && pycompat.IsDir(filepath.Join(folder, "owpreview")) {
			return folder, nil
		}
		next := pycompat.Parent(folder)
		if next == folder {
			break
		}
		folder = next
	}
	return parent, nil
}

// Settings is WorkflowPaths.settings(); key order matches Python.
type Settings struct {
	OverwatchInstall   string `json:"overwatchInstall"`
	OutputDirectory    string `json:"outputDirectory"`
	LibraryDirectory   string `json:"libraryDirectory"`
	ToolchainDirectory string `json:"toolchainDirectory"`
	ActiveTab          string `json:"activeTab"`
	ToolchainTab       string `json:"toolchainTab"`
}

// Paths is WorkflowPaths. It is not synchronised: like the Python class, the
// caller (BrowserAPI) serialises access with its own lock.
type Paths struct {
	catalog Catalog
	// AppDir, Library and Toolchain are str(self.app_dir), str(self.library)
	// and str(self.toolchain).
	AppDir    string
	Library   string
	Toolchain string
}

// New is WorkflowPaths(catalog, app_dir); appDir "" means None.
func New(catalog Catalog, appDir string) (*Paths, error) {
	dir, err := ApplicationDirectory(appDir)
	if err != nil {
		return nil, err
	}
	p := &Paths{catalog: catalog, AppDir: dir}
	// Honor a configured library location. lastRoot is a scan selection,
	// not the portable library: it can point at any exported asset folder.
	configured, _ := catalog.Get("libraryDirectory")
	if !pycompat.Truthy(configured) {
		configured, _ = catalog.Get("libraryPath")
	}
	if pycompat.Truthy(configured) {
		library, err := AbsoluteFolder(configured, "Library")
		if err != nil {
			return nil, err
		}
		p.Library = pycompat.PathStr(library)
	} else {
		p.Library = pycompat.Join(dir, "library")
	}
	p.Toolchain = pycompat.Join(dir, "toolchain")
	return p, nil
}

func text(value any) string {
	if value == nil {
		return ""
	}
	return pycompat.Str(value)
}

// Settings returns the effective workflow settings.
func (p *Paths) Settings() Settings {
	saved, _ := p.catalog.Get("workflow")
	if _, ok := pycompat.AsDict(saved); !ok {
		saved = nil
	}
	install := ""
	if pycompat.Has(saved, "overwatchInstall") {
		install = text(pycompat.Get(saved, "overwatchInstall"))
	}
	output := p.Library
	if value := pycompat.Get(saved, "outputDirectory"); pycompat.Truthy(value) {
		output = text(value)
	}
	active, ok := oneOf(pycompat.Get(saved, "activeTab"), workflowTabs)
	if !ok {
		active = "animations"
	}
	toolchainTab, ok := oneOf(pycompat.Get(saved, "toolchainTab"), toolchainTabs)
	if !ok {
		toolchainTab = "heroes"
	}
	return Settings{
		OverwatchInstall: install, OutputDirectory: output,
		LibraryDirectory: p.Library, ToolchainDirectory: p.Toolchain,
		ActiveTab: active, ToolchainTab: toolchainTab,
	}
}

// Update applies changes (a dict) and stores the workflow block in the
// catalog. Nothing is changed when validation fails.
func (p *Paths) Update(changes any) (Settings, error) {
	dict, ok := pycompat.AsDict(changes)
	if !ok {
		return Settings{}, errors.New("Invalid workflow settings.")
	}
	updated := p.Settings()
	for _, field := range []struct {
		key, label string
		target     *string
	}{
		{"overwatchInstall", "Overwatch Install", &updated.OverwatchInstall},
		{"outputDirectory", "Output Directory", &updated.OutputDirectory},
	} {
		if value, found := dict.Get(field.key); found {
			folder, err := AbsoluteFolder(value, field.label)
			if err != nil {
				return Settings{}, err
			}
			*field.target = folder
		}
	}
	if value, found := dict.Get("activeTab"); found {
		tab, ok := oneOf(value, workflowTabs)
		if !ok {
			return Settings{}, errors.New("Unknown workflow tab.")
		}
		updated.ActiveTab = tab
	}
	if value, found := dict.Get("toolchainTab"); found {
		tab, ok := oneOf(value, toolchainTabs)
		if !ok {
			return Settings{}, errors.New("Unknown Toolchain export tab.")
		}
		updated.ToolchainTab = tab
	}
	workflow := ordered.New[any]()
	workflow.Set("overwatchInstall", updated.OverwatchInstall)
	workflow.Set("outputDirectory", updated.OutputDirectory)
	workflow.Set("activeTab", updated.ActiveTab)
	workflow.Set("toolchainTab", updated.ToolchainTab)
	if updated.OutputDirectory != "" && pycompat.NormCase(updated.OutputDirectory) == pycompat.NormCase(p.Library) {
		workflow.Set("outputDirectory", "")
	}
	p.catalog.Set("workflow", workflow)
	return p.Settings(), nil
}

// UseLibrary resets the output directory to the portable library.
func (p *Paths) UseLibrary() (Settings, error) {
	return p.Update(map[string]any{"outputDirectory": ""})
}
