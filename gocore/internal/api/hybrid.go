package api

import (
	"sort"

	"avolition/gocore/internal/pycompat"
)

// Missing lists the dependencies of d that are not ported yet.
func Missing(d Deps) []string {
	var missing []string
	for name, present := range map[string]bool{"exporttextures": d.BakeMaterials != nil, "mapexport": d.ExportMap != nil,
		"textures": d.NewTextures != nil, "duplicates": d.NewDuplicates != nil,
		"bonecounts": d.BoneCounts != nil, "clipbindings": d.NewClipBindings != nil, "objectnames": d.ObjectNames != nil,
		"c4dbridge": d.NewBridge != nil, "analysis": d.NewAnalysis != nil, "toolchain": d.NewToolchain != nil,
		"datatool": d.NewDataTool != nil} {
		if !present {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// Stateless lists the methods that touch no shared state (catalog, loaded
// assets, textures or jobs). While other methods still run in the Python
// core, only these may be answered in Go: everything else must stay in the
// one process that owns catalog.json and the loaded-asset registries.
var Stateless = []string{"ping", "close_app", "browse_root", "browse_output", "browse_entity", "browse_map",
	"browse_workflow_directory", "load_animation", "read_clip_bindings", "c4d_status"}

// NewStateless returns an API for the Stateless methods only: it neither
// creates the profile folder nor reads catalog.json.
func NewStateless(options Options) *API {
	deps := Default()
	if options.Deps != nil {
		deps = *options.Deps
	}
	a := &API{deps: deps, desktopMode: options.DesktopMode, mapExports: newMapJobs()}
	if deps.NewClipBindings != nil {
		a.clipBindings = deps.NewClipBindings()
	}
	explicit := options.DataDir
	for _, name := range []string{"AVOLITION_DATA_DIR", "WHITEHOUND_DATA_DIR", "OWIO_DATA_DIR", "OWPREVIEW_DATA_DIR"} {
		if explicit == "" {
			explicit = lookupEnv(name)
		}
	}
	if explicit != "" {
		a.bridgeRoot = pycompat.Join(pycompat.Resolve(explicit), "c4d-bridge")
	} else {
		a.bridgeRoot = defaultBridgeRoot()
	}
	if deps.NewBridge != nil {
		a.bridge = deps.NewBridge(a.bridgeRoot)
	}
	return a
}
