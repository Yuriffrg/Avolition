package api

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"sync"
	"time"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// mapJobs is map_jobs.MapExportJobs: one background map export with short,
// independently locked status reads.
type mapJobs struct {
	mu       sync.Mutex
	closed   bool
	started  time.Time
	finished time.Time
	hasStart bool
	hasEnd   bool
	job      mapJob
}

type mapJob struct {
	ID        *string `json:"id"`
	State     string  `json:"state"`
	Busy      bool    `json:"busy"`
	Message   string  `json:"message"`
	Completed any     `json:"completed"`
	Total     any     `json:"total"`
	Result    any     `json:"result"`
	Error     *string `json:"error"`
}

type mapJobSnapshot struct {
	mapJob
	ElapsedSeconds float64 `json:"elapsedSeconds"`
}

func newMapJobs() MapJobs { return &mapJobs{job: mapJob{State: "Idle"}} }

func uuidHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

func (m *mapJobs) snapshotLocked() mapJobSnapshot {
	snapshot := mapJobSnapshot{mapJob: m.job}
	snapshot.Result = pycompat.Clone(m.job.Result)
	elapsed := 0.0
	if m.hasStart {
		end := time.Now()
		if m.hasEnd {
			end = m.finished
		}
		elapsed = end.Sub(m.started).Seconds()
	}
	snapshot.ElapsedSeconds = math.Round(math.Max(0, elapsed)*1000) / 1000
	return snapshot
}

func (m *mapJobs) Snapshot(jobID any) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if jobID != nil {
		current := ""
		if m.job.ID != nil {
			current = *m.job.ID
		}
		if text, ok := jobID.(string); !ok || m.job.ID == nil || text != current {
			return nil, newValueError("This map export job is no longer available.")
		}
	}
	return m.snapshotLocked(), nil
}

func (m *mapJobs) begin() (mapJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return mapJobSnapshot{}, newValueError("The application is closing. Start the map export after reopening it.")
	}
	if m.job.Busy {
		return mapJobSnapshot{}, newValueError("A map export is already running.")
	}
	m.started, m.hasStart, m.hasEnd = time.Now(), true, false
	id := uuidHex()
	m.job = mapJob{ID: &id, State: "Running", Busy: true, Message: "Loading map…"}
	return m.snapshotLocked(), nil
}

func (m *mapJobs) progress(id string) Progress {
	return func(message string, completed, total any) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.job.ID != nil && *m.job.ID == id && m.job.Busy {
			m.job.Message, m.job.Completed, m.job.Total = message, completed, total
		}
	}
}

func (m *mapJobs) finish(id string, result any, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job.ID == nil || *m.job.ID != id {
		return
	}
	m.finished, m.hasEnd = time.Now(), true
	m.job.Busy = false
	if err != nil {
		message := errorText(err)
		m.job.State, m.job.Message, m.job.Result, m.job.Error = "Failed", message, nil, &message
		return
	}
	m.job.State, m.job.Message, m.job.Result, m.job.Error = "Completed", "Map export completed.", pycompat.Clone(result), nil
}

func (m *mapJobs) execute(id string, operation func(Progress) (any, error)) (any, error) {
	result, err := protect(func() (any, error) { return operation(m.progress(id)) })
	m.finish(id, result, err)
	return result, err
}

func (m *mapJobs) Start(operation func(Progress) (any, error)) (any, error) {
	snapshot, err := m.begin()
	if err != nil {
		return nil, err
	}
	go func() { _, _ = m.execute(*snapshot.ID, operation) }()
	// Return the accepted job even if a tiny export completed immediately.
	return snapshot, nil
}

// Run is the compatibility path: same guard and observable status.
func (m *mapJobs) Run(operation func(Progress) (any, error)) (any, error) {
	snapshot, err := m.begin()
	if err != nil {
		return nil, err
	}
	return m.execute(*snapshot.ID, operation)
}

// Close marks the jobs closed. The desktop asks the user to wait before
// closing an active export; a native bridge operation is not joined.
func (m *mapJobs) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

var _ = ordered.New[any]
