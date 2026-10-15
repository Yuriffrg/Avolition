package api

import (
	"avolition/gocore/internal/bonecounts"
	"avolition/gocore/internal/datatool"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/toolchain"
)

// The interfaces below are what BrowserAPI needs from the other ported
// modules. They are deliberately small: wire.go adapts each package to them,
// and tests substitute fakes (the Python tests patch the same seams).

// TextureStore is textures.TextureStore.
type TextureStore interface {
	// Register is register(textures, owner=owner).
	Register(textures *ordered.Map[*formats.Texture], owner string) error
	// Release is release(owner).
	Release(owner string) error
	// Load is load(texture_id, max_edge) with the raw request values (the
	// store validates their types like Python). maxEdge is nil for None.
	Load(textureID, maxEdge any) (any, error)
}

// DuplicateIndex is duplicates.DuplicateIndex.
type DuplicateIndex interface {
	// Annotate sets DuplicateKey, DuplicateCount and DuplicateError on every
	// clip in place and returns at most 50 warnings.
	Annotate(clips []*ClipEntry) []string
}

// ClipBindings is clip_bindings.ClipBindingIndex.
type ClipBindings interface {
	// Read is read(paths) with the raw request value.
	Read(paths any) (any, error)
}

// ObjectNames is object_names.
type ObjectNames interface {
	ValidateName(name any) (string, error)
	// ValidateNames is validate_names(names, asset); asset nil checks keys
	// syntactically only. names is a JSON value (nil → {}).
	ValidateNames(names any, asset *formats.Asset) (*ordered.Map[string], error)
	// Keys is object_name_keys(asset).
	Keys(asset *formats.Asset) (map[string]bool, error)
	// Apply is apply_object_names(asset, names): a renamed copy.
	Apply(asset *formats.Asset, names *ordered.Map[string]) (*formats.Asset, error)
}

// BridgeStatus is BridgeClient.status(); it encodes as the status dict.
type BridgeStatus interface {
	Connected() bool
	Octane() bool
	Message() string
}

// Bridge is c4d_bridge.BridgeClient.
type Bridge interface {
	Root() string
	Status() BridgeStatus
	// Export is export(payload, destination, texture_folder); it returns the
	// warnings of the result.
	Export(payload any, destination, textureFolder string) ([]string, error)
}

// AnalysisHooks are AnalysisManager's callbacks into the API.
type AnalysisHooks struct {
	Annotations     func() *ordered.Map[any]
	Installed       func() any
	Excluded        func() []any
	LabelsAvailable func() bool
}

// Analysis is animation_analysis.AnalysisManager.
type Analysis interface {
	Start(root any) (any, error)
	Status(root any) (any, error)
	Index(root any) (any, error)
	Detail(path, root any) (any, error)
}

// Materials is a baked or neutral material table: model id → material key →
// descriptor (export_textures.bake_materials()['materials']).
type Materials = *ordered.Map[*ordered.Map[*formats.Material]]

// Payload is a C4D export payload (c4d_export.make_payload).
type Payload interface {
	SetMaterials(materials Materials)
	Warnings() []string
}

// SetPayload is c4d_export.make_set_payload's payload.
type SetPayload interface {
	Payload
	// Animation returns payload['animation'].get('assignments', []), ['fps']
	// and ['lastFrame'].
	Animation() (assignments, fps, lastFrame any)
}

// AssignedClip is one {'modelId', 'clip'} entry of make_set_payload.
type AssignedClip struct {
	ModelID string
	Clip    *formats.Clip
}

// BundleBuilder is export_bundle's builder(destinations, texture_folder):
// destinations maps 'fbx'/'c4d' to staging paths. It returns the warnings.
type BundleBuilder func(destinations map[string]string, textureFolder string) ([]string, error)

// SceneExporter bundles export_bundle, c4d_export, export_textures and fbx.
type SceneExporter interface {
	// Bundle is export_bundle(source, folder, filename, builder, formats).
	Bundle(source, folder, filename string, build BundleBuilder, formats []string) (*ordered.Map[any], error)
	SelectModelAsset(asset *formats.Asset, modelID string) (*formats.Asset, error)
	MakePayload(asset *formats.Asset, clip *formats.Clip, materials Materials, includeMesh, rootMotion bool, clipName, rigID string) (Payload, error)
	MakeSetPayload(asset *formats.Asset, clips []AssignedClip, includeMesh, rootMotion bool, name string) (SetPayload, error)
	// BakeMaterials is bake_materials(asset, folder): materials and warnings.
	BakeMaterials(asset *formats.Asset, textureFolder string) (Materials, []string, error)
	// ExportFBX is export_fbx(...); it returns the result's warnings.
	ExportFBX(destination string, asset *formats.Asset, clip *formats.Clip, upAxis string, includeMesh, rootMotion bool,
		materials Materials, clipName, rigID string) ([]string, error)
}

// Progress is a map export progress callback (message, completed, total).
type Progress func(message string, completed, total any)

// MapExporter is map_export.export_map.
type MapExporter func(asset *formats.Asset, folder, name string, bakeTextures bool, bridge Bridge, progress Progress) (*ordered.Map[any], error)

// MapJobs is map_jobs.MapExportJobs.
type MapJobs interface {
	Snapshot(jobID any) (any, error)
	Start(operation func(Progress) (any, error)) (any, error)
	Run(operation func(Progress) (any, error)) (any, error)
	Close()
}

// Toolchain is toolchain.Manager.
type Toolchain interface {
	Snapshot() toolchain.Snapshot
	CheckAsync() (toolchain.Snapshot, error)
	InstallAsync() (toolchain.Snapshot, error)
	Executable() string
	Close()
}

// DataTool is datatool.Jobs.
type DataTool interface {
	Snapshot() datatool.Snapshot
	StartCatalog(executable, install, workflow string) (datatool.Snapshot, error)
	StartExport(executable, install, output string, selection any) (datatool.Snapshot, error)
	Cancel() datatool.Snapshot
	Close()
}

// Deps are the collaborators of the API. Nil fields are modules that are not
// ported yet: methods needing them fail with ErrUnavailable, and the Go core
// forwards those methods to the Python core (see Unavailable).
type Deps struct {
	LoadAsset       func(path, root string) (*formats.Asset, error)
	LoadClip        func(path string) (*formats.Clip, error)
	ReadClipHeader  func(path string) (*formats.ClipHeader, error)
	LoadMap         func(path, root string) (*formats.Asset, error)
	ValidatePaths   func(install, output string) datatool.Validation
	NewTextures     func() TextureStore
	NewDuplicates   func() DuplicateIndex
	// BoneCounts is BoneCountIndex(root, paths).metadata(path) for every path.
	BoneCounts      func(root string, entityPaths []string) []bonecounts.Metadata
	NewClipBindings func() ClipBindings
	ObjectNames     ObjectNames
	NewBridge       func(root string) Bridge
	NewAnalysis     func(cacheDir string, hooks AnalysisHooks) Analysis
	Scene           SceneExporter
	ExportMap       MapExporter
	NewMapJobs      func() MapJobs
	NewToolchain    func(root string, activity toolchain.ActivityLock) Toolchain
	NewDataTool     func(stateDir string, activity datatool.ActivityLock) DataTool
}
