// Package mapjobs ports owpreview/map_jobs.py: one background map export
// with short, independently locked status reads.
package mapjobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"time"
)

// Progress is the callback an operation receives: progress(message,
// completed=None, total=None).
type Progress func(message string, completed, total *int)

// Operation is the export work. Its result is stored as JSON, so every
// snapshot owns an independent copy (copy.deepcopy in Python).
type Operation func(progress Progress) (any, error)

// Snapshot is the job status dict. ElapsedSeconds is rounded to
// milliseconds; Result and Error are null until the job ends.
type Snapshot struct {
	ID             *string         `json:"id"`
	State          string          `json:"state"`
	Busy           bool            `json:"busy"`
	Message        string          `json:"message"`
	Completed      *int            `json:"completed"`
	Total          *int            `json:"total"`
	Result         json.RawMessage `json:"result"`
	Error          *string         `json:"error"`
	ElapsedSeconds float64         `json:"elapsedSeconds"`
}

// Jobs is MapExportJobs.
type Jobs struct {
	mu       sync.Mutex
	closed   bool
	started  time.Time
	finished time.Time
	running  bool // started is set
	done     bool // finished is set
	job      Snapshot
	worker   chan struct{} // closed when the latest background worker ends
}

// New returns an idle job manager.
func New() *Jobs {
	return &Jobs{job: Snapshot{State: "Idle", Result: json.RawMessage("null")}}
}

func (j *Jobs) snapshot() Snapshot {
	out := j.job
	out.ID = clonePtr(j.job.ID)
	out.Completed = clonePtr(j.job.Completed)
	out.Total = clonePtr(j.job.Total)
	out.Error = clonePtr(j.job.Error)
	out.Result = append(json.RawMessage(nil), j.job.Result...)
	elapsed := 0.0
	if j.running {
		end := time.Now()
		if j.done {
			end = j.finished
		}
		elapsed = end.Sub(j.started).Seconds()
	}
	out.ElapsedSeconds = math.RoundToEven(max(0, elapsed)*1000) / 1000
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Snapshot returns the current status. A non-nil jobID must name the
// current job.
func (j *Jobs) Snapshot(jobID *string) (Snapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if jobID != nil && (j.job.ID == nil || *jobID != *j.job.ID) {
		return Snapshot{}, errors.New("This map export job is no longer available.")
	}
	return j.snapshot(), nil
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	raw[6] = raw[6]&0x0f | 0x40 // uuid4
	raw[8] = raw[8]&0x3f | 0x80
	return hex.EncodeToString(raw[:])
}

func (j *Jobs) begin() (Snapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Snapshot{}, errors.New("The application is closing. Start the map export after reopening it.")
	}
	if j.job.Busy {
		return Snapshot{}, errors.New("A map export is already running.")
	}
	j.started, j.running, j.done = time.Now(), true, false
	id := newID()
	j.job = Snapshot{ID: &id, State: "Running", Busy: true, Message: "Loading map…", Result: json.RawMessage("null")}
	return j.snapshot(), nil
}

func (j *Jobs) progress(id string, message string, completed, total *int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job.ID != nil && *j.job.ID == id && j.job.Busy {
		j.job.Message, j.job.Completed, j.job.Total = message, clonePtr(completed), clonePtr(total)
	}
}

func (j *Jobs) finish(id string, result any, failure *string) {
	// Encode outside the lock; the result is owned by the job from here on.
	encoded := json.RawMessage("null")
	if failure == nil && result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			message := err.Error()
			failure = &message
		} else {
			encoded = data
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job.ID == nil || *j.job.ID != id {
		return
	}
	j.finished, j.done = time.Now(), true
	j.job.Busy = false
	if failure != nil {
		j.job.State, j.job.Message, j.job.Error, j.job.Result = "Failed", *failure, failure, json.RawMessage("null")
	} else {
		j.job.State, j.job.Message, j.job.Error, j.job.Result = "Completed", "Map export completed.", nil, encoded
	}
}

// errorText is str(error) or type(error).__name__.
func errorText(err error) string {
	if text := err.Error(); text != "" {
		return text
	}
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// panicked turns a panic in an operation into a failure, like Python's
// "except Exception" around the operation.
type panicked struct{ value any }

func (p *panicked) Error() string { return fmt.Sprint(p.value) }

func (j *Jobs) execute(id string, operation Operation) (result any, err error) {
	func() {
		defer func() {
			if r := recover(); r != nil {
				result, err = nil, &panicked{r}
			}
		}()
		result, err = operation(func(message string, completed, total *int) {
			j.progress(id, message, completed, total)
		})
	}()
	if err != nil {
		text := errorText(err)
		j.finish(id, nil, &text)
		return nil, err
	}
	j.finish(id, result, nil)
	return result, nil
}

// Start runs operation in the background and returns the accepted job's
// snapshot, even if a tiny export completed immediately.
func (j *Jobs) Start(operation Operation) (Snapshot, error) {
	snapshot, err := j.begin()
	if err != nil {
		return Snapshot{}, err
	}
	done := make(chan struct{})
	j.mu.Lock()
	j.worker = done
	j.mu.Unlock()
	go func() {
		defer close(done)
		j.execute(*snapshot.ID, operation)
	}()
	return snapshot, nil
}

// Run is the compatibility path: it shares the same guard and observable
// status, runs operation on the caller's goroutine and returns its result
// or error.
func (j *Jobs) Run(operation Operation) (any, error) {
	snapshot, err := j.begin()
	if err != nil {
		return nil, err
	}
	return j.execute(*snapshot.ID, operation)
}

// Close refuses new exports. The desktop asks the user to wait before
// closing an active export; a native bridge operation is not joined.
func (j *Jobs) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
}

// Wait blocks until the latest background worker has ended (tests).
func (j *Jobs) Wait() {
	j.mu.Lock()
	worker := j.worker
	j.mu.Unlock()
	if worker != nil {
		<-worker
	}
}
