package api

import (
	"strings"

	"avolition/gocore/internal/datatool"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// WorkflowSettings is workflow_settings(changes=None).
func (a *API) WorkflowSettings(changes any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if changes != nil {
		result, err := a.workflow.Update(changes)
		if err != nil {
			return nil, err
		}
		if err := a.save(); err != nil {
			return nil, err
		}
		return result, nil
	}
	return a.workflow.Settings(), nil
}

// UseLibraryDirectory is use_library_directory().
func (a *API) UseLibraryDirectory() (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result, err := a.workflow.UseLibrary()
	if err != nil {
		return nil, err
	}
	if err := a.save(); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *API) workflowSettings() (install, output string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	settings := a.workflow.Settings()
	return settings.OverwatchInstall, settings.OutputDirectory
}

// OpenToolchainOutput is open_toolchain_output(): the desktop host opens only
// this configured directory with shell.openPath.
func (a *API) OpenToolchainOutput() (any, error) {
	_, output := a.workflowSettings()
	path := pycompat.PathStr(output)
	if !pycompat.IsDir(path) {
		return nil, newValueError("Output Directory is not available. Choose an existing folder or export first.")
	}
	return path, nil
}

type toolchainSnapshot struct {
	Runtime      any            `json:"runtime"`
	Job          any            `json:"job"`
	Capabilities map[string]any `json:"capabilities"`
}

func (a *API) toolchainSnapshot() *toolchainSnapshot {
	return &toolchainSnapshot{Runtime: a.toolchain.Snapshot(), Job: a.datatool.Snapshot(),
		Capabilities: map[string]any{"categories": datatool.Categories}}
}

// ToolchainCheck is toolchain_check().
func (a *API) ToolchainCheck() (any, error) {
	if _, err := a.toolchain.CheckAsync(); err != nil {
		return nil, err
	}
	return a.toolchainSnapshot(), nil
}

// checkToolchainLocation is _check_toolchain_location().
func (a *API) checkToolchainLocation() error {
	configured, _ := a.workflowSettings()
	root := a.workflow.Toolchain
	insideGame := false
	if configured != "" {
		game := pycompat.PathStr(configured)
		if pycompat.IsFile(pycompat.Join(game, ".flavor.info")) {
			game = pycompat.Parent(game)
		}
		insideGame = pycompat.IsRelativeTo(root, game)
	}
	if !insideGame {
		for parent := pycompat.Parent(root); ; parent = pycompat.Parent(parent) {
			if pycompat.IsDir(pycompat.Join(parent, "data")) && (pycompat.IsFile(pycompat.Join(parent, ".build.info")) ||
				pycompat.IsFile(pycompat.Join(parent, "data/.build.config"))) {
				insideGame = true
				break
			}
			if pycompat.Parent(parent) == parent {
				break
			}
		}
	}
	if insideGame {
		return newValueError("Move the Avolition folder outside the Overwatch installation before using Toolchain.")
	}
	return nil
}

// ToolchainInstall is toolchain_install().
func (a *API) ToolchainInstall() (any, error) {
	if err := a.checkToolchainLocation(); err != nil {
		return nil, err
	}
	if _, err := a.toolchain.InstallAsync(); err != nil {
		return nil, err
	}
	return a.toolchainSnapshot(), nil
}

// ToolchainValidate is toolchain_validate().
func (a *API) ToolchainValidate() (any, error) {
	install, outputDirectory := a.workflowSettings()
	validation := a.deps.ValidatePaths(install, outputDirectory)
	output := pycompat.PathStr(outputDirectory)
	toolchain := a.workflow.Toolchain
	if pycompat.IsRelativeTo(output, toolchain) || pycompat.IsRelativeTo(toolchain, output) {
		validation.OK = false
		if validation.Errors == nil {
			validation.Errors = ordered.New[string]()
		}
		validation.Errors.Set("outputDirectory", "Choose the library or another asset folder separate from the Toolchain runtime.")
	}
	return validation, nil
}

func (a *API) datatoolExecutable() (string, error) {
	if err := a.checkToolchainLocation(); err != nil {
		return "", err
	}
	executable := a.toolchain.Executable()
	if executable == "" {
		return "", newValueError("Install Toolchain before loading assets or exporting.")
	}
	return executable, nil
}

// ToolchainList is toolchain_list(options=None).
func (a *API) ToolchainList(options any) (any, error) {
	if options != nil {
		if _, ok := dictOf(options); !ok {
			return nil, newValueError("Invalid Toolchain listing options.")
		}
	}
	dict, _ := dictOf(options)
	workflow, _ := getOr(dict, "workflow", "heroes").(string)
	executable, err := a.datatoolExecutable()
	if err != nil {
		return nil, err
	}
	install, _ := a.workflowSettings()
	if _, err := a.datatool.StartCatalog(executable, install, workflow); err != nil {
		return nil, err
	}
	return a.toolchainSnapshot(), nil
}

// ToolchainStart is toolchain_start(selection).
func (a *API) ToolchainStart(selection any) (any, error) {
	install, output := a.workflowSettings()
	result, err := a.ToolchainValidate()
	if err != nil {
		return nil, err
	}
	validation := result.(datatool.Validation)
	if !validation.OK {
		var messages []string
		if validation.Errors != nil {
			validation.Errors.Range(func(_ string, message string) bool {
				messages = append(messages, message)
				return true
			})
		}
		return nil, newValueError(strings.Join(messages, " "))
	}
	executable, err := a.datatoolExecutable()
	if err != nil {
		return nil, err
	}
	if _, err := a.datatool.StartExport(executable, install, output, selection); err != nil {
		return nil, err
	}
	return a.toolchainSnapshot(), nil
}

// ToolchainCancel is toolchain_cancel().
func (a *API) ToolchainCancel() (any, error) {
	a.datatool.Cancel()
	return a.toolchainSnapshot(), nil
}
