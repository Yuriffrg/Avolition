package api

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/protocol"
)

// ErrUnavailable marks a method whose Go dependencies are not ported yet.
type ErrUnavailable struct{ Dependency string }

func (e *ErrUnavailable) Error() string {
	return "This feature is not available in the Go core yet (" + e.Dependency + ")."
}

func unavailable(dependency string) error { return &ErrUnavailable{dependency} }

// method describes one API method's Python signature.
type method struct {
	params   []string // parameter names after self
	required int      // parameters without defaults
	defaults []any    // defaults of the optional parameters
	call     func(a *API, args []any) (any, error)
	// needs lists the dependencies; the method is unavailable while any is nil.
	needs []string
}

var methods map[string]*method

func init() {
	m := func(params string, required int, defaults []any, needs string, call func(a *API, args []any) (any, error)) *method {
		var names []string
		if params != "" {
			names = strings.Split(params, ",")
		}
		var need []string
		if needs != "" {
			need = strings.Split(needs, ",")
		}
		return &method{params: names, required: required, defaults: defaults, call: call, needs: need}
	}
	methods = map[string]*method{
		"bootstrap":         m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.Bootstrap() }),
		"browse_root":       m("", 0, nil, "", func(*API, []any) (any, error) { return nil, folderDialog() }),
		"browse_output":     m("", 0, nil, "", func(*API, []any) (any, error) { return nil, folderDialog() }),
		"set_output_folder": m("value", 1, nil, "", func(a *API, x []any) (any, error) { return a.SetOutputFolder(x[0]) }),
		"set_export_settings": m("options", 1, nil, "", func(a *API, x []any) (any, error) {
			return a.SetExportSettings(x[0])
		}),
		"export_animation": m("path,nickname,output_folder,options", 2, []any{nil, nil}, "bridge,objectNames",
			func(a *API, x []any) (any, error) { return a.ExportAnimation(x[0], x[1], x[2], x[3]) }),
		"export_animation_set": m("entity_path,assignments,scene_name,output_folder,options", 3, []any{nil, nil}, "bridge,objectNames",
			func(a *API, x []any) (any, error) { return a.ExportAnimationSet(x[0], x[1], x[2], x[3], x[4]) }),
		"c4d_status": m("", 0, nil, "bridge", func(a *API, _ []any) (any, error) { return a.bridge.Status(), nil }),
		"browse_entity": m("", 0, nil, "", func(*API, []any) (any, error) {
			return nil, &runtimeError{"File dialogs are provided by the Avolition desktop window."}
		}),
		"scan_root": m("value,activate", 1, []any{true}, "duplicates", func(a *API, x []any) (any, error) {
			return a.ScanRoot(x[0], x[1])
		}),
		"load_entity":            m("path", 1, nil, "textures,objectNames", func(a *API, x []any) (any, error) { return a.LoadEntity(x[0]) }),
		"preview_entity":         m("path", 1, nil, "textures,objectNames", func(a *API, x []any) (any, error) { return a.PreviewEntity(x[0]) }),
		"release_entity_preview": m("", 0, nil, "textures", func(a *API, _ []any) (any, error) { return a.ReleaseEntityPreview() }),
		"load_texture": m("texture_id,max_edge", 1, []any{2048}, "textures", func(a *API, x []any) (any, error) {
			return a.textures.Load(x[0], x[1])
		}),
		"load_animation":  m("path", 1, nil, "", func(a *API, x []any) (any, error) { return a.LoadAnimation(x[0]) }),
		"save_annotation": m("path,annotation", 2, nil, "", func(a *API, x []any) (any, error) { return a.SaveAnnotation(x[0], x[1]) }),
		"ping":            m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.Ping(), nil }),
		"close_app":       m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.CloseApp(), nil }),
		"workflow_settings": m("changes", 0, []any{nil}, "", func(a *API, x []any) (any, error) {
			return a.WorkflowSettings(x[0])
		}),
		"use_library_directory":     m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.UseLibraryDirectory() }),
		"browse_workflow_directory": m("kind", 1, nil, "", func(*API, []any) (any, error) { return nil, folderDialog() }),
		"open_toolchain_output":     m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.OpenToolchainOutput() }),
		"toolchain_status":          m("", 0, nil, "toolchain,datatool", func(a *API, _ []any) (any, error) { return a.toolchainSnapshot(), nil }),
		"toolchain_check":           m("", 0, nil, "toolchain,datatool", func(a *API, _ []any) (any, error) { return a.ToolchainCheck() }),
		"toolchain_install":         m("", 0, nil, "toolchain,datatool", func(a *API, _ []any) (any, error) { return a.ToolchainInstall() }),
		"toolchain_validate":        m("", 0, nil, "toolchain", func(a *API, _ []any) (any, error) { return a.ToolchainValidate() }),
		"toolchain_list": m("options", 0, []any{nil}, "toolchain,datatool", func(a *API, x []any) (any, error) {
			return a.ToolchainList(x[0])
		}),
		"toolchain_start":  m("selection", 1, nil, "toolchain,datatool", func(a *API, x []any) (any, error) { return a.ToolchainStart(x[0]) }),
		"toolchain_cancel": m("", 0, nil, "toolchain,datatool", func(a *API, _ []any) (any, error) { return a.ToolchainCancel() }),
		"analysis_start":   m("root", 1, nil, "analysis", func(a *API, x []any) (any, error) { return a.analysis.Start(x[0]) }),
		"analysis_status":  m("root", 1, nil, "analysis", func(a *API, x []any) (any, error) { return a.analysis.Status(x[0]) }),
		"analysis_index":   m("root", 1, nil, "analysis", func(a *API, x []any) (any, error) { return a.analysis.Index(x[0]) }),
		"analysis_detail": m("path,root", 1, []any{nil}, "analysis", func(a *API, x []any) (any, error) {
			return a.analysis.Detail(x[0], x[1])
		}),
		"read_clip_bindings": m("paths", 1, nil, "clipBindings", func(a *API, x []any) (any, error) { return a.clipBindings.Read(x[0]) }),
		"library_settings": m("changes", 0, []any{nil}, "", func(a *API, x []any) (any, error) {
			return a.LibrarySettings(x[0])
		}),
		"export_queue_save":   m("entry", 1, nil, "objectNames", func(a *API, x []any) (any, error) { return a.ExportQueueSave(x[0]) }),
		"export_queue_remove": m("ident", 1, nil, "", func(a *API, x []any) (any, error) { return a.ExportQueueRemove(x[0]) }),
		"export_queue_run": m("ident,output_folder,options", 1, []any{nil, nil}, "bridge,objectNames",
			func(a *API, x []any) (any, error) { return a.ExportQueueRun(x[0], x[1], x[2]) }),
		"open_export_output": m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.OpenExportOutput() }),
		"maps_settings":      m("changes", 0, []any{nil}, "", func(a *API, x []any) (any, error) { return a.MapsSettings(x[0]) }),
		"scan_maps":          m("root", 1, nil, "", func(a *API, x []any) (any, error) { return a.ScanMaps(x[0]) }),
		"load_map": m("path,root", 1, []any{nil}, "textures,objectNames", func(a *API, x []any) (any, error) {
			return a.LoadMap(x[0], x[1])
		}),
		"export_map": m("path,name,root,output,bake_textures", 2, []any{nil, nil, true}, "exportMap,bridge,objectNames",
			func(a *API, x []any) (any, error) { return a.ExportMap(x[0], x[1], x[2], x[3], x[4], false) }),
		"export_map_start": m("path,name,root,output,bake_textures", 2, []any{nil, nil, true}, "exportMap,bridge,objectNames",
			func(a *API, x []any) (any, error) { return a.ExportMap(x[0], x[1], x[2], x[3], x[4], true) }),
		"export_map_status": m("job_id", 0, []any{nil}, "", func(a *API, x []any) (any, error) { return a.mapExports.Snapshot(x[0]) }),
		"browse_map": m("", 0, nil, "", func(*API, []any) (any, error) {
			return nil, &runtimeError{"Map file dialogs are provided by the Avolition desktop window."}
		}),
		"open_map_output":  m("", 0, nil, "", func(a *API, _ []any) (any, error) { return a.OpenMapOutput() }),
		"object_names":     m("path", 1, nil, "objectNames", func(a *API, x []any) (any, error) { return a.ObjectNames(x[0]) }),
		"save_object_name": m("path,node,name", 3, nil, "objectNames", func(a *API, x []any) (any, error) { return a.SaveObjectName(x[0], x[1], x[2]) }),
	}
	for _, name := range protocol.Methods {
		if methods[name] == nil {
			panic("api: missing method " + name)
		}
	}
}

func folderDialog() error {
	return &runtimeError{"Folder dialogs are provided by the Avolition desktop window."}
}

// arityError is the TypeError Python raises for a wrong argument count.
func arityError(name string, spec *method, given int) error {
	qualified := "BrowserAPI." + name + "()"
	total := len(spec.params) + 1 // self
	if given > len(spec.params) {
		takes := fmt.Sprintf("%d positional argument", total)
		if spec.required < len(spec.params) {
			takes = fmt.Sprintf("from %d to %d positional arguments", spec.required+1, total)
		} else if total != 1 {
			takes += "s"
		}
		were := "were"
		if given+1 == 1 {
			were = "was"
		}
		return &typeError{fmt.Sprintf("%s takes %s but %d %s given", qualified, takes, given+1, were)}
	}
	missing := spec.params[given:spec.required]
	quoted := make([]string, len(missing))
	for i, p := range missing {
		quoted[i] = "'" + p + "'"
	}
	list := quoted[0]
	switch {
	case len(quoted) == 2:
		list = quoted[0] + " and " + quoted[1]
	case len(quoted) > 2:
		list = strings.Join(quoted[:len(quoted)-1], ", ") + ", and " + quoted[len(quoted)-1]
	}
	plural := "argument"
	if len(missing) > 1 {
		plural = "arguments"
	}
	return &typeError{fmt.Sprintf("%s missing %d required positional %s: %s", qualified, len(missing), plural, list)}
}

// Unavailable reports the dependencies a method still lacks (nil when it can
// run in Go).
func (a *API) Unavailable(name string) []string {
	spec := methods[name]
	if spec == nil {
		return nil
	}
	var missing []string
	for _, need := range spec.needs {
		if !a.has(need) {
			missing = append(missing, need)
		}
	}
	return missing
}

func (a *API) has(dependency string) bool {
	switch dependency {
	case "textures":
		return a.textures != nil
	case "duplicates":
		return a.duplicates != nil && a.deps.BoneCounts != nil
	case "clipBindings":
		return a.clipBindings != nil
	case "objectNames":
		return a.deps.ObjectNames != nil
	case "bridge":
		return a.bridge != nil
	case "analysis":
		return a.analysis != nil
	case "scene":
		return a.deps.BakeMaterials != nil
	case "exportMap":
		return a.deps.ExportMap != nil
	case "toolchain":
		return a.toolchain != nil && a.deps.ValidatePaths != nil
	case "datatool":
		return a.datatool != nil
	}
	return false
}

func protect(fn func() (any, error)) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if e, ok := recovered.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", recovered)
			}
			if debugPanics {
				err = fmt.Errorf("%v\n%s", recovered, debug.Stack())
			}
		}
	}()
	return fn()
}

var debugPanics = false

// Call runs one API method like getattr(api, method)(*args) through reply():
// it returns the data, or the error whose errorText is the reply's error.
func (a *API) Call(name string, args []any) (any, error) {
	spec := methods[name]
	if spec == nil {
		return nil, newValueError("Unknown API method")
	}
	if len(args) < spec.required || len(args) > len(spec.params) {
		return nil, arityError(name, spec, len(args))
	}
	if missing := a.Unavailable(name); missing != nil {
		return nil, unavailable(strings.Join(missing, ", "))
	}
	full := make([]any, len(spec.params))
	copy(full, args)
	for i := len(args); i < len(spec.params); i++ {
		full[i] = spec.defaults[i-spec.required]
	}
	return protect(func() (any, error) { return spec.call(a, full) })
}

// IsUnavailable reports whether err is ErrUnavailable.
func IsUnavailable(err error) bool {
	var target *ErrUnavailable
	return errors.As(err, &target)
}

// assetData encodes an asset reply with formats.AppendAssetJSON.
type assetData struct{ asset *formats.Asset }

func (d assetData) AppendJSON(dst []byte) ([]byte, error) {
	return formats.AppendAssetJSON(dst, d.asset)
}

// clipData encodes a clip reply with formats.AppendClipJSON.
type clipData struct{ clip *formats.Clip }

func (d clipData) AppendJSON(dst []byte) ([]byte, error) { return formats.AppendClipJSON(dst, d.clip) }

// Appender is reply data that encodes itself (large assets and clips).
type Appender interface {
	AppendJSON(dst []byte) ([]byte, error)
}
