// Package api is the Go port of app.py's BrowserAPI: every desktop API
// method, catalog.json ownership (unknown keys and key order preserved),
// annotations, output folders, the export list, object names, entity preview
// generations and texture ownership, plus the --stdio transport and the
// developer --serve host.
//
// Methods take the decoded JSON arguments exactly as Python's method(*args)
// receives them and reproduce Python's argument errors and coercions, so the
// reply wrapper ({ok, data} | {ok: false, error}) matches app.py's reply().
//
// # Locking
//
// BrowserAPI._lock is a threading.RLock. Here mu is a plain mutex: each
// method takes it once where Python's outermost `with self._lock` is, and
// helpers that Python calls with the lock already held are the *Locked
// variants. Background work (map exports, analysis, toolchain and DataTool
// jobs) runs in goroutines and takes mu only for catalog access.
package api

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/mapjobs"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/workflowpaths"
)

// Version is the application version reported by bootstrap.
const Version = "0.8.23"

// MaxExportQueue is MAX_EXPORT_QUEUE.
const MaxExportQueue = 1000

// Options are BrowserAPI's constructor arguments.
type Options struct {
	DataDir     string // data_dir; "" is None
	InitialRoot string // initial_root; "" is None
	DesktopMode bool
	AppDir      string // app_dir; "" is None
	// Deps overrides the ported modules (tests); nil uses Default().
	Deps *Deps
}

// API is BrowserAPI.
type API struct {
	deps        Deps
	desktopMode bool

	serverMu sync.Mutex
	shutdown func() // the --serve server's shutdown (close_app)

	pingMu   sync.Mutex
	lastPing time.Time
	hasPing  bool

	mu           sync.Mutex // BrowserAPI._lock
	queueMu      sync.Mutex // _queue_activity_lock
	queueRunning map[string]bool
	naming       *ordered.Map[*formats.Asset] // editable-name skeletons

	previewMu         sync.Mutex // _entity_preview_lock
	previewGeneration int

	mapExports   *mapjobs.Jobs
	textures     TextureStore
	duplicates   DuplicateIndex
	clipBindings ClipBindings

	dataDir        string
	catalogPath    string
	bridge         Bridge
	bridgeRoot     string
	catalog        *ordered.Map[any]
	catalogWarning *string
	root           string // "" is None

	workflow      *workflowpaths.Paths
	toolchainLock sync.Mutex
	toolchain     Toolchain
	datatool      DataTool
	analysis      Analysis

	// Test seams (the Python tests patch these attributes).
	saveHook      func() error
	exportSetHook func(entityPath, assignments, sceneName, outputFolder, options any) (any, error)
}

func lookupEnv(name string) string {
	value, _ := os.LookupEnv(name)
	return value
}

func homeDir() string {
	home, err := pycompat.ExpandUser("~")
	if err != nil {
		return "~"
	}
	return home
}

// localData is Path(os.environ.get('LOCALAPPDATA', str(Path.home()))).
func localData() string {
	if value, ok := os.LookupEnv("LOCALAPPDATA"); ok {
		return pycompat.PathStr(value)
	}
	return homeDir()
}

// defaultBridgeRoot is c4d_bridge.queue_root().
func defaultBridgeRoot() string {
	if override := lookupEnv("AVOLITION_C4D_QUEUE"); override != "" {
		return pycompat.PathStr(override)
	}
	if override := lookupEnv("WHITEHOUND_C4D_QUEUE"); override != "" {
		return pycompat.PathStr(override)
	}
	base := lookupEnv("LOCALAPPDATA")
	if base == "" {
		base = homeDir()
	}
	return pycompat.Join(base, "Whitehound/c4d-bridge")
}

// New is BrowserAPI.__init__.
func New(options Options) (*API, error) {
	deps := Default()
	if options.Deps != nil {
		deps = *options.Deps
	}
	a := &API{
		deps: deps, desktopMode: options.DesktopMode, lastPing: time.Now(),
		queueRunning: map[string]bool{}, naming: ordered.New[*formats.Asset](),
	}
	a.mapExports = mapjobs.New()
	if deps.NewTextures != nil {
		a.textures = deps.NewTextures()
	}
	if deps.NewDuplicates != nil {
		a.duplicates = deps.NewDuplicates()
	}
	if deps.NewClipBindings != nil {
		a.clipBindings = deps.NewClipBindings()
	}
	explicit := options.DataDir
	for _, name := range []string{"AVOLITION_DATA_DIR", "WHITEHOUND_DATA_DIR", "OWIO_DATA_DIR", "OWPREVIEW_DATA_DIR"} {
		if explicit == "" {
			explicit = lookupEnv(name)
		}
	}
	local := localData()
	if explicit != "" {
		a.dataDir = pycompat.Resolve(explicit)
	} else {
		a.dataDir = pycompat.Resolve(pycompat.Join(local, "Avolition"))
	}
	if err := os.MkdirAll(a.dataDir, 0o777); err != nil {
		return nil, pyOSError(err)
	}
	// Keep the installed companion plugin's shared queue; explicit profiles remain isolated.
	if explicit != "" {
		a.bridgeRoot = pycompat.Join(a.dataDir, "c4d-bridge")
	} else {
		a.bridgeRoot = defaultBridgeRoot()
	}
	if deps.NewBridge != nil {
		a.bridge = deps.NewBridge(a.bridgeRoot)
	}
	a.catalogPath = pycompat.Join(a.dataDir, "catalog.json")
	a.catalog = ordered.New[any]()
	a.catalog.Set("lastRoot", "")
	a.catalog.Set("annotations", ordered.New[any]())
	a.catalog.Set("outputFolders", ordered.New[any]())
	a.catalog.Set("outputFolder", "")
	a.catalog.Set("exportDirectories", []any{})
	a.catalog.Set("exportedFiles", []any{})
	a.catalog.Set("managedOutputDirectories", []any{})
	a.catalog.Set("fbxOptions", fbxOptionsDefault())
	a.catalog.Set("exportQueue", []any{})

	// Copy the previous catalog once. Originals remain available to the old app.
	if explicit == "" && !pycompat.Exists(a.catalogPath) {
		a.migrateLegacy(local)
	}
	if pycompat.Exists(a.catalogPath) {
		if err := a.readCatalog(); err != nil {
			a.warn("Could not read saved labels: " + err.Error())
		}
	}
	migrateFormats := false
	saved, _ := a.catalog.Get("fbxOptions")
	if dict, ok := saved.(*ordered.Map[any]); ok && !hasKey(dict, "clipEnabled") {
		// The old raw clip copy was implicit. Introduce the three explicit
		// choices once without losing mesh/axis settings or library data.
		migrateFormats = true
		copied := cloneDict(dict)
		copied.Set("clipEnabled", false)
		copied.Set("enabled", false)
		copied.Set("c4dEnabled", true)
		saved = copied
	}
	if value, err := fbxOptions(saved); err != nil {
		a.catalog.Set("fbxOptions", fbxOptionsDefault())
		a.warn("Saved FBX options were invalid and have been reset to their defaults.")
		migrateFormats = false
	} else {
		a.catalog.Set("fbxOptions", value)
	}
	// Move the previous default once. Subsequent explicit sort choices must
	// survive restart, including a user choosing Most tracks first again.
	revision, hasRevision := a.catalog.Get("librarySortRevision")
	if !hasRevision {
		revision = 0
	}
	revisionInt, isInt := pyInt(revision)
	oldSort, _ := a.catalog.Get("libraryOptions")
	migrateSort := false
	if oldDict, ok := oldSort.(*ordered.Map[any]); ok && (!isInt || revisionInt < 1) {
		if sortValue, _ := oldDict.Get("sort"); sortValue == "tracks-desc" {
			migrateSort = true
			copied := cloneDict(oldDict)
			copied.Set("sort", "object-match")
			a.catalog.Set("libraryOptions", copied)
		}
	}
	if isInt && revisionInt >= 1 {
		a.catalog.Set("librarySortRevision", revision)
	} else {
		a.catalog.Set("librarySortRevision", 1)
	}
	root := options.InitialRoot
	var rootValue any = root
	if root == "" {
		rootValue, _ = a.catalog.Get("lastRoot")
	}
	if pycompat.Truthy(rootValue) {
		text, err := pathOf(rootValue)
		if err != nil {
			return nil, err
		}
		a.root = pycompat.Resolve(text)
	}
	if options.InitialRoot != "" {
		a.catalog.Set("lastRoot", options.InitialRoot)
	}
	workflow, err := workflowpaths.New(a.catalog, options.AppDir)
	if err != nil {
		return nil, err
	}
	a.workflow = workflow
	if deps.NewToolchain != nil {
		a.toolchain = deps.NewToolchain(workflow.Toolchain, &a.toolchainLock)
	}
	if deps.NewDataTool != nil {
		a.datatool = deps.NewDataTool(pycompat.Join(a.dataDir, "datatool"), &a.toolchainLock)
	}
	if deps.NewAnalysis != nil {
		a.analysis = deps.NewAnalysis(pycompat.Join(a.dataDir, "animation-analysis"), AnalysisHooks{
			Annotations: func() *ordered.Map[any] {
				a.mu.Lock()
				defer a.mu.Unlock()
				annotations, _ := a.catalog.Get("annotations")
				if dict, ok := annotations.(*ordered.Map[any]); ok {
					return cloneDict(dict)
				}
				return ordered.New[any]()
			},
			Installed: func() any {
				if a.toolchain == nil {
					return nil
				}
				if installed := a.toolchain.Snapshot().Installed; installed != nil {
					return installed
				}
				return nil
			},
			Excluded: func() []any {
				a.mu.Lock()
				defer a.mu.Unlock()
				managed, _ := a.catalog.Get("managedOutputDirectories")
				exported, _ := a.catalog.Get("exportedFiles")
				left, _ := managed.([]any)
				right, _ := exported.([]any)
				return append(append([]any{}, left...), right...)
			},
			LabelsAvailable: func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return a.catalogWarning == nil || !strings.HasPrefix(*a.catalogWarning, "Could not read saved labels")
			},
		})
	}
	if migrateFormats || migrateSort {
		if err := a.save(); err != nil {
			a.warn("Could not save updated export defaults: " + err.Error())
		}
	}
	return a, nil
}

// warn replaces the catalog warning, like assigning self._catalog_warning.
func (a *API) warn(message string) { a.catalogWarning = &message }

// readText is Path.read_text(encoding='utf-8'): strict UTF-8 with universal
// newlines.
func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", pyOSError(err)
	}
	text, err := pycompat.DecodeUTF8Strict(data)
	if err != nil {
		return "", err
	}
	return pycompat.UniversalNewlines(text), nil
}

// writeText is Path.write_text(text, encoding='utf-8') (text mode: "\n" is
// written as "\r\n" on Windows).
func writeText(path, text string, exclusive bool) error {
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o666)
	if err != nil {
		return pyOSError(err)
	}
	_, err = file.WriteString(text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return pyOSError(err)
}

func (a *API) migrateLegacy(local string) {
	var legacy string
	for _, candidate := range []string{"Whitehound/catalog.json", "OWIO/catalog.json", "OWAnimationBrowser/catalog.json"} {
		if path := pycompat.Join(local, candidate); pycompat.IsFile(path) {
			legacy = path
			break
		}
	}
	if legacy == "" {
		return
	}
	err := func() error {
		previous, err := readText(legacy)
		if err != nil {
			return err
		}
		saved, err := pycompat.LoadsText(previous)
		if err != nil {
			return err
		}
		dict, ok := saved.(*ordered.Map[any])
		if !ok || !isDictOrMissing(dict, "annotations") {
			return errors.New("Invalid legacy library metadata")
		}
		return writeText(a.catalogPath, previous, true)
	}()
	if err != nil && !errors.Is(err, os.ErrExist) {
		a.warn("Could not migrate saved labels: " + err.Error())
	}
}

func isDictOrMissing(dict *ordered.Map[any], key string) bool {
	value, ok := dict.Get(key)
	if !ok {
		return true
	}
	_, isDict := value.(*ordered.Map[any])
	return isDict
}

func isListOrMissing(dict *ordered.Map[any], key string) bool {
	value, ok := dict.Get(key)
	if !ok {
		return true
	}
	_, isList := value.([]any)
	return isList
}

func (a *API) readCatalog() error {
	text, err := readText(a.catalogPath)
	if err != nil {
		return err
	}
	saved, err := pycompat.LoadsText(text)
	if err != nil {
		return err
	}
	dict, ok := saved.(*ordered.Map[any])
	if !ok || !isDictOrMissing(dict, "annotations") {
		return errors.New("Invalid library metadata")
	}
	if !isDictOrMissing(dict, "outputFolders") {
		return errors.New("Invalid output-folder metadata")
	}
	for _, field := range []string{"exportDirectories", "exportedFiles", "managedOutputDirectories"} {
		if !isListOrMissing(dict, field) {
			return errors.New("Invalid exported-file metadata")
		}
	}
	dict.Range(func(key string, value any) bool {
		a.catalog.Set(key, value)
		return true
	})
	return nil
}

// save is _save: an atomic replace of catalog.json. Callers hold mu.
func (a *API) save() error {
	if a.saveHook != nil { // tests: patch.object(api, '_save')
		return a.saveHook()
	}
	encoded, err := pycompat.Dumps(a.catalog, 2)
	if err != nil {
		return err
	}
	temporary := pypathWithSuffix(a.catalogPath, ".tmp")
	if err := writeText(temporary, string(encoded), false); err != nil {
		return err
	}
	if err := os.Rename(temporary, a.catalogPath); err != nil {
		return pyOSError(err)
	}
	return nil
}

// key is _key: os.path.normcase(str(Path(path).resolve())).
func key(path string) string { return pycompat.NormCase(pycompat.Resolve(path)) }

// keyOf is _key for an arbitrary argument (Path() type errors included).
func keyOf(value any) (string, error) {
	text, err := pathOf(value)
	if err != nil {
		return "", err
	}
	return key(text), nil
}

func (a *API) catalogGet(name string) any {
	value, _ := a.catalog.Get(name)
	return value
}

func (a *API) catalogDict(name string) *ordered.Map[any] {
	value, _ := a.catalogGet(name).(*ordered.Map[any])
	return value
}

func (a *API) catalogList(name string) []any {
	value, _ := a.catalog.Get(name)
	list, _ := value.([]any)
	return list
}

func defaultAnnotation() *ordered.Map[any] {
	value := ordered.New[any]()
	value.Set("name", "")
	value.Set("notes", "")
	value.Set("favorite", false)
	return value
}

// annotationLocked is _annotation(path).
func (a *API) annotationLocked(path string) any {
	if annotations := a.catalogDict("annotations"); annotations != nil {
		if value, ok := annotations.Get(key(path)); ok {
			return pycompat.Clone(value)
		}
	}
	return defaultAnnotation()
}

// outputFolderLocked is _output_folder().
func (a *API) outputFolderLocked() any {
	if a.root != "" {
		if folders := a.catalogDict("outputFolders"); folders != nil {
			if value, ok := folders.Get(key(a.root)); ok {
				return value
			}
		}
		return pycompat.Join(a.root, "Renamed Animations")
	}
	return a.catalogGet("outputFolder")
}

// commonOutputLocked is `self._catalog.get('outputFolder') or self._output_folder()`.
func (a *API) commonOutputLocked() any {
	if value := a.catalogGet("outputFolder"); pycompat.Truthy(value) {
		return value
	}
	return a.outputFolderLocked()
}

// Shutdown is shutdown(): stop background work.
func (a *API) Shutdown() {
	a.mapExports.Close()
	if a.datatool != nil {
		a.datatool.Close()
	}
	if a.toolchain != nil {
		a.toolchain.Close()
	}
}

// SetServerShutdown registers the --serve server's shutdown for close_app.
func (a *API) SetServerShutdown(shutdown func()) {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	a.shutdown = shutdown
}

// DataDir is the resolved profile directory.
func (a *API) DataDir() string { return a.dataDir }

// CatalogPath is the catalog.json path.
func (a *API) CatalogPath() string { return a.catalogPath }

// BridgeRoot is the C4D bridge queue directory.
func (a *API) BridgeRoot() string { return a.bridgeRoot }

// Root is the active library root ("" when none).
func (a *API) Root() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.root
}
