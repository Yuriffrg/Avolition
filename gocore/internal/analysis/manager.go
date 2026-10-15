package analysis

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
)

// ManagerOptions are AnalysisManager's callbacks. They are called from the
// background goroutine (and Annotations also from Detail), so they must be
// safe for concurrent use. Nil callbacks use Python's defaults: {}, None,
// [] and True.
type ManagerOptions struct {
	Annotations     func() any
	Installed       func() any
	Excluded        func() []string
	LabelsAvailable func() bool
}

// Status is a record without its inventory (status()).
type Status struct {
	Root     string  `json:"root"`
	State    string  `json:"state"`
	Busy     bool    `json:"busy"`
	Message  string  `json:"message"`
	Error    *string `json:"error"`
	Revision *string `json:"revision"`
}

type record struct {
	status    Status
	inventory *Inventory
	// occurrence lookup by Key(path), built on first Detail.
	occurrenceKeys map[string]int
}

// Manager runs background parsing and cache validation; status polling
// performs no file I/O beyond resolving the root. It is the port of
// AnalysisManager.
type Manager struct {
	cacheDir string
	opts     ManagerOptions

	mu      sync.Mutex
	records map[string]*record
	order   []string
	wg      sync.WaitGroup

	// Replaceable in tests (Python patches the module functions).
	snapshot func(root string, excluded []string, progress func(string)) (*Snapshot, map[string]string, error)
	analyze  func(root string, opts Options, resolved map[string]string) (*Inventory, error)
}

// NewManager is AnalysisManager(cache_dir, ...).
func NewManager(cacheDir string, opts ManagerOptions) *Manager {
	if opts.Annotations == nil {
		opts.Annotations = func() any { return map[string]any{} }
	}
	if opts.Installed == nil {
		opts.Installed = func() any { return nil }
	}
	if opts.Excluded == nil {
		opts.Excluded = func() []string { return nil }
	}
	if opts.LabelsAvailable == nil {
		opts.LabelsAvailable = func() bool { return true }
	}
	return &Manager{cacheDir: pypath.Clean(cacheDir), opts: opts, records: map[string]*record{},
		snapshot: snapshotWith, analyze: analyzeWith}
}

// recordLocked is _record(root): the record for root, created when absent.
func (m *Manager) recordLocked(root string) *record {
	key := Key(root)
	if r, ok := m.records[key]; ok {
		return r
	}
	r := &record{status: Status{Root: pypath.Resolve(root), State: "Ready",
		Message: "Analyze local metadata without changing assets."}}
	m.records[key] = r
	m.order = append(m.order, key)
	return r
}

// directory is _directory(root): the cache folder of a root.
func (m *Manager) directory(root string) string {
	return pypath.Join(m.cacheDir, pyjson.MustDigest(Key(root))[:24])
}

// Start is start(root, cached_only).
func (m *Manager) Start(root string, cachedOnly bool) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked(root, cachedOnly)
}

func (m *Manager) startLocked(root string, cachedOnly bool) (Status, error) {
	root = pypath.Resolve(root)
	if !pypath.IsDir(root) {
		return Status{}, valueError("The library folder is unavailable. Scan an existing folder first.")
	}
	r := m.recordLocked(root)
	if r.status.Busy {
		return r.status, nil
	}
	r.status.Busy, r.status.Error, r.status.Revision = true, nil, nil
	r.inventory, r.occurrenceKeys = nil, nil
	r.status.State = "Analyzing"
	if cachedOnly {
		r.status.State = "Checking cache"
	}
	m.wg.Add(1)
	go m.run(root, cachedOnly)
	return r.status, nil
}

func (m *Manager) update(root string, fn func(r *record)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.recordLocked(root))
}

func (m *Manager) run(root string, cachedOnly bool) {
	defer m.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			m.fail(root, fmt.Errorf("%v", recovered))
		}
	}()
	if err := m.work(root, cachedOnly); err != nil {
		m.fail(root, err)
	}
}

func (m *Manager) fail(root string, err error) {
	message := err.Error()
	// A failed refresh never presents old evidence as current.
	m.update(root, func(r *record) {
		r.status.Busy, r.status.State, r.status.Error, r.status.Revision = false, "Failed", &message, nil
		r.inventory, r.occurrenceKeys = nil, nil
	})
}

func (m *Manager) work(root string, cachedOnly bool) error {
	progress := func(message string) {
		m.update(root, func(r *record) { r.status.Message = message })
	}
	destination := pypath.Join(m.directory(root), "animation-index.json")
	if cachedOnly && !pypath.IsFile(destination) {
		m.update(root, func(r *record) {
			r.status.Busy, r.status.State, r.status.Message = false, "Ready", "Analyze this library to inspect local evidence."
		})
		return nil
	}
	annotations, err := newAnnotationSet(m.opts.Annotations())
	if err != nil {
		return err
	}
	installed, err := normalizeJSON(m.opts.Installed())
	if err != nil {
		return err
	}
	excluded := append([]string{}, m.opts.Excluded()...)
	labelsAvailable := m.opts.LabelsAvailable()
	prepared, resolved, err := m.snapshot(root, excluded, progress)
	if err != nil {
		return err
	}
	revision, err := revisionDigest(prepared.Manifest, annotations.raw, installed, labelsAvailable)
	if err != nil {
		return err
	}
	var inventory *Inventory
	if pypath.IsFile(destination) {
		if data, err := os.ReadFile(destination); err == nil {
			var cached Inventory
			if json.Unmarshal(data, &cached) == nil && cached.Revision == revision && cached.ParserVersion == ParserVersion {
				inventory = &cached
			}
		}
	}
	if inventory == nil {
		inventory, err = m.analyze(root, Options{Annotations: annotations.raw, InstalledToolchain: installed,
			Excluded: excluded, Progress: progress, Prepared: prepared, LabelsUnavailable: !labelsAvailable}, resolved)
		if err != nil {
			return err
		}
		// Do not retain a result if files changed while being parsed.
		for _, item := range inventory.Manifest {
			if item.MtimeNs == nil {
				continue
			}
			info, err := os.Stat(pypath.Join(root, item.RelativePath))
			if err != nil {
				return formats.PythonOSError(err)
			}
			if strconv.FormatInt(info.ModTime().UnixNano(), 10) != *item.MtimeNs || item.Size == nil || info.Size() != *item.Size {
				return &formats.OSError{Message: "Files changed during analysis. Wait for extraction to finish, then analyze again."}
			}
		}
		if err := writeInventory(destination, inventory); err != nil {
			return err
		}
	}
	m.update(root, func(r *record) {
		r.inventory, r.occurrenceKeys = inventory, nil
		r.status.Revision = &revision
		r.status.Busy, r.status.State = false, "Completed"
		r.status.Message = fmt.Sprintf("%d local assets · %d occurrences · %d parse errors",
			inventory.AssetCount, inventory.OccurrenceCount, len(inventory.ParseErrors))
	})
	return nil
}

// writeInventory writes json.dumps(inventory, ensure_ascii=False, indent=2)
// through a temporary file (destination.with_suffix('.tmp')).
func writeInventory(destination string, inventory *Inventory) error {
	if err := os.MkdirAll(pypath.Parent(destination), 0o777); err != nil {
		return formats.PythonOSError(err)
	}
	temporary := pypath.WithSuffix(destination, ".tmp")
	if err := WriteFile(temporary, inventory); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return formats.PythonOSError(err)
	}
	return nil
}

// Status is status(root, check_cache). An unseen root starts a cache check
// when checkCache is set (which fails like Start for a missing folder).
func (m *Manager) Status(root string, checkCache bool) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, seen := m.records[Key(root)]
	r := m.recordLocked(root)
	if !seen && checkCache {
		if _, err := m.startLocked(root, true); err != nil {
			return Status{}, err
		}
	}
	return r.status, nil
}

// Index is index(root): the summary of the completed analysis, or nil.
func (m *Manager) Index(root string) *IndexSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inventory := m.recordLocked(root).inventory; inventory != nil {
		return Summarize(inventory)
	}
	return nil
}

// DetailCache describes the inventory a detail came from.
type DetailCache struct {
	ParserVersion string `json:"parserVersion"`
	AnalyzedAt    string `json:"analyzedAt"`
	Path          string `json:"path"`
	Revision      string `json:"revision"`
}

// Detail is detail()'s result. Values are shared with the inventory except
// the occurrence, which is a copy carrying the current nickname; callers must
// not modify them.
type Detail struct {
	Asset       *Asset        `json:"asset"`
	Occurrence  *Occurrence   `json:"occurrence"`
	Occurrences []*Occurrence `json:"occurrences"`
	Limitations []string      `json:"limitations"`
	Cache       DetailCache   `json:"cache"`
}

// Detail is detail(path, root): the analysis of one clip, from the first
// completed record (of root, when given) that contains it, or nil.
func (m *Manager) Detail(path string, root *string) *Detail {
	pathKey := Key(path)
	var rootKey string
	if root != nil {
		rootKey = Key(*root)
	}
	m.mu.Lock()
	var detail *Detail
	for _, key := range m.order {
		r := m.records[key]
		if root != nil && Key(r.status.Root) != rootKey {
			continue
		}
		inventory := r.inventory
		if inventory == nil {
			continue
		}
		if r.occurrenceKeys == nil {
			r.occurrenceKeys = make(map[string]int, len(inventory.Occurrences))
			for i := len(inventory.Occurrences) - 1; i >= 0; i-- {
				r.occurrenceKeys[Key(inventory.Occurrences[i].Path)] = i
			}
		}
		index, ok := r.occurrenceKeys[pathKey]
		if !ok {
			continue
		}
		occurrence := *inventory.Occurrences[index]
		var asset *Asset
		for _, a := range inventory.Assets {
			if a.ID == occurrence.AssetID {
				asset = a
				break
			}
		}
		occurrences := []*Occurrence{}
		for _, o := range inventory.Occurrences {
			if o.AssetID == occurrence.AssetID {
				occurrences = append(occurrences, o)
			}
		}
		detail = &Detail{Asset: asset, Occurrence: &occurrence, Occurrences: occurrences,
			Limitations: inventory.Limitations, Cache: DetailCache{ParserVersion: inventory.ParserVersion,
				AnalyzedAt: inventory.AnalyzedAt, Revision: inventory.Revision,
				Path: pypath.Join(m.directory(inventory.Root), "animation-index.json")}}
		break
	}
	m.mu.Unlock()
	if detail == nil {
		return nil
	}
	// The callback runs outside the manager lock: it may take the caller's
	// own locks.
	annotations, err := newAnnotationSet(m.opts.Annotations())
	if err == nil {
		detail.Occurrence.Nickname = nickname(annotations.name(pathKey))
	}
	return detail
}

// Wait blocks until no background job is running (for tests and shutdown).
func (m *Manager) Wait() { m.wg.Wait() }
