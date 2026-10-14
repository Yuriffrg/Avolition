package datatool

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"avolition/gocore/internal/pycompat"
)

// ActivityLock is the lock shared with the Toolchain installer
// (threading.Lock with acquire(blocking=False)). *sync.Mutex satisfies it.
type ActivityLock interface {
	TryLock() bool
	Unlock()
}

// Command is the DataTool process to start. Args[0] is the resolved
// DataTool.exe, Dir its folder and Env the inherited environment without
// DATATOOL_OVERWATCH_DIR. No shell is ever involved.
type Command struct {
	Args []string
	Dir  string
	Env  []string
}

// ProcessFactory builds the unstarted process for a Command (Python's
// process_factory). The job runner then attaches stdin (null device),
// stdout/stderr pipes and the platform's containment attributes (Windows:
// CREATE_SUSPENDED | CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP; elsewhere a
// new session) and starts it. Tests substitute a harmless fixture executable.
type ProcessFactory func(Command) (*exec.Cmd, error)

// DefaultProcessFactory runs the command as given.
func DefaultProcessFactory(command Command) (*exec.Cmd, error) {
	cmd := exec.Command(command.Args[0], command.Args[1:]...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	return cmd, nil
}

// LogLine is one streamed log entry.
type LogLine struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// Snapshot is DataToolJobs.snapshot(); key order matches Python.
type Snapshot struct {
	State      string      `json:"state"`
	Busy       bool        `json:"busy"`
	Kind       *string     `json:"kind"`
	JobID      *string     `json:"jobId"`
	Logs       []LogLine   `json:"logs"`
	Error      *string     `json:"error"`
	Message    string      `json:"message"`
	Result     any         `json:"result"`
	ExitCode   *int        `json:"exitCode"`
	Progress   any         `json:"progress"`
	StartedAt  *string     `json:"startedAt"`
	FinishedAt *string     `json:"finishedAt"`
	Catalog    *Catalog    `json:"catalog"`
	MapCatalog *MapCatalog `json:"mapCatalog"`
}

// CatalogResult is the result of a heroes catalog job.
type CatalogResult struct {
	HeroCount int    `json:"heroCount"`
	Install   string `json:"install"`
	Workflow  string `json:"workflow"`
}

// MapCatalogResult is the result of a maps catalog job.
type MapCatalogResult struct {
	MapCount int    `json:"mapCount"`
	Install  string `json:"install"`
	Workflow string `json:"workflow"`
}

// ExportResult is the result of a hero unlock export.
type ExportResult struct {
	OutputDirectory string `json:"outputDirectory"`
	Hero            string `json:"hero"`
	Asset           string `json:"asset"`
	Category        string `json:"category"`
}

// MapExportResult is the result of a map export. MapID is the selection's
// mapId value ("*" for all maps).
type MapExportResult struct {
	OutputDirectory string `json:"outputDirectory"`
	Workflow        string `json:"workflow"`
	MapID           any    `json:"mapId"`
	MapName         string `json:"mapName"`
	Query           string `json:"query"`
}

var errCancelled = errors.New("cancelled")

// event is threading.Event for one job.
type event struct {
	once sync.Once
	ch   chan struct{}
}

func newEvent() *event { return &event{ch: make(chan struct{})} }
func (e *event) set()  { e.once.Do(func() { close(e.ch) }) }
func (e *event) isSet() bool {
	select {
	case <-e.ch:
		return true
	default:
		return false
	}
}

// Jobs is DataToolJobs: at most one DataTool catalog or export job at a time.
type Jobs struct {
	stateDir string
	activity ActivityLock
	factory  ProcessFactory

	mu         sync.Mutex
	cancel     *event
	done       chan struct{}
	sequence   int
	catalog    *Catalog
	mapCatalog *MapCatalog
	state      Snapshot
}

// NewJobs is DataToolJobs(state_dir, operation_lock, process_factory=...).
// A nil lock or factory selects the defaults.
func NewJobs(stateDir string, activity ActivityLock, factory ProcessFactory) *Jobs {
	if activity == nil {
		activity = &sync.Mutex{}
	}
	if factory == nil {
		factory = DefaultProcessFactory
	}
	return &Jobs{
		stateDir: pycompat.Resolve(stateDir), activity: activity, factory: factory,
		cancel: newEvent(),
		state:  Snapshot{State: "Ready", Logs: []LogLine{}, Message: "Ready"},
	}
}

// Snapshot returns a copy of the job state with the loaded catalogs.
func (j *Jobs) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *Jobs) snapshotLocked() Snapshot {
	snapshot := j.state
	snapshot.Logs = append([]LogLine{}, j.state.Logs...)
	snapshot.Catalog, snapshot.MapCatalog = j.catalog, j.mapCatalog
	return snapshot
}

func (j *Jobs) log(stream, text string) {
	text = strings.Trim(ansi.ReplaceAllString(text, ""), "\r\n")
	if text == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sequence++
	j.state.Logs = append(j.state.Logs, LogLine{Seq: j.sequence, Stream: stream, Text: pycompat.Slice(text, 12000)})
	if len(j.state.Logs) > 800 {
		j.state.Logs = append([]LogLine{}, j.state.Logs[len(j.state.Logs)-800:]...)
	}
}

func strPtr(value string) *string { return &value }

func newJobID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return hex.EncodeToString(value[:])
}

func (j *Jobs) start(kind string, run func(cancel *event) (any, error)) (Snapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Busy {
		return Snapshot{}, dataToolError("A DataTool job is already running. Wait for it or choose Cancel.")
	}
	if !j.activity.TryLock() {
		return Snapshot{}, dataToolError("Toolchain is busy installing, updating or exporting. Wait for the current operation.")
	}
	cancel := newEvent()
	j.cancel = cancel
	j.sequence = 0
	state, message := "Exporting", "Exporting…"
	if kind == "catalog" {
		state, message = "Loading", "Reading installed game assets…"
	}
	j.state = Snapshot{State: state, Busy: true, Kind: strPtr(kind), JobID: strPtr(newJobID()),
		Logs: []LogLine{}, Message: message, StartedAt: strPtr(isoNow())}
	done := make(chan struct{})
	j.done = done
	go j.worker(kind, run, cancel, done)
	return j.snapshotLocked(), nil
}

func (j *Jobs) worker(kind string, run func(*event) (any, error), cancel *event, done chan struct{}) {
	defer close(done)
	result, err := func() (result any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%v", recovered)
			}
		}()
		return run(cancel)
	}()
	if err == nil && cancel.isSet() {
		err = errCancelled
	}
	switch {
	case err == nil:
		j.mu.Lock()
		j.state.State, j.state.Result = "Completed", result
		j.state.Message = "Game assets loaded."
		if kind == "export" {
			j.state.Message = "Export completed."
		}
		j.mu.Unlock()
	case errors.Is(err, errCancelled):
		j.mu.Lock()
		j.state.State, j.state.Message = "Cancelled", "Cancelled. Partial output files may remain."
		j.mu.Unlock()
	default:
		message := err.Error()
		j.mu.Lock()
		j.state.State, j.state.Error, j.state.Message = "Failed", strPtr(message), message
		j.mu.Unlock()
		j.log("avolition", message)
	}
	// Release before publishing idle; a UI can start the next job as soon
	// as it sees busy=false without racing the shared installation lock.
	j.activity.Unlock()
	j.mu.Lock()
	j.state.Busy, j.state.FinishedAt = false, strPtr(isoNow())
	j.mu.Unlock()
}

func validate(install string, output ...string) (Validation, error) {
	checked := ValidatePaths(install, output...)
	if !checked.OK {
		var messages []string
		checked.Errors.Range(func(_ string, message string) bool {
			messages = append(messages, message)
			return true
		})
		return checked, dataToolError(strings.Join(messages, " "))
	}
	return checked, nil
}

// StartCatalog lists heroes and unlocks (workflow "heroes") or maps
// (workflow "maps") with the installed DataTool.
func (j *Jobs) StartCatalog(executable, install, workflow string) (Snapshot, error) {
	if workflow != "heroes" && workflow != "maps" {
		return Snapshot{}, dataToolError("Choose the Animations or Maps Toolchain workflow.")
	}
	checked, err := validate(install)
	if err != nil {
		return Snapshot{}, err
	}
	game := checked.OverwatchInstall
	run := func(cancel *event) (any, error) {
		if err := os.MkdirAll(j.stateDir, 0o777); err != nil {
			return nil, err
		}
		directory, err := os.MkdirTemp(j.stateDir, "catalog-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(directory)
		kinds := []string{"heroes", "unlocks"}
		if workflow == "maps" {
			kinds = []string{"maps"}
		}
		var payloads []any
		for _, kind := range kinds {
			output := filepath.Join(directory, kind+".json")
			args, _ := BuildListArgs(game, kind, output)
			if err := j.runProcess(cancel, executable, args); err != nil {
				return nil, err
			}
			if !pycompat.IsFile(output) {
				return nil, dataToolErrorf("DataTool did not produce %s JSON. Check its version and the log.", kind)
			}
			data, err := os.ReadFile(output)
			var payload any
			if err == nil {
				payload, err = pycompat.Loads(pycompat.StripBOM(data))
			}
			if err != nil {
				return nil, dataToolErrorf("DataTool returned unreadable %s JSON: %v", kind, err)
			}
			payloads = append(payloads, payload)
		}
		if workflow == "maps" {
			catalog, err := ParseMapCatalog(payloads[0], game)
			if err != nil {
				return nil, err
			}
			j.mu.Lock()
			j.mapCatalog = catalog
			j.mu.Unlock()
			return MapCatalogResult{MapCount: len(catalog.Maps), Install: game, Workflow: workflow}, nil
		}
		catalog, err := ParseCatalog(payloads[0], payloads[1], game)
		if err != nil {
			return nil, err
		}
		j.mu.Lock()
		j.catalog = catalog
		j.mu.Unlock()
		return CatalogResult{HeroCount: len(catalog.Heroes), Install: game, Workflow: workflow}, nil
	}
	return j.start("catalog", run)
}

// StartExport exports the selection (a dict: hero/asset/category or
// workflow "maps" with mapId, plus include*/allLods options).
func (j *Jobs) StartExport(executable, install, output string, selection any) (Snapshot, error) {
	if _, ok := pycompat.AsDict(selection); !ok {
		return Snapshot{}, dataToolError("Invalid Toolchain export selection.")
	}
	workflow, _ := getDefault(selection, "workflow", "heroes").(string)
	if workflow != "heroes" && workflow != "maps" {
		return Snapshot{}, dataToolError("Choose the Animations or Maps Toolchain workflow.")
	}
	checked, err := validate(install, output)
	if err != nil {
		return Snapshot{}, err
	}
	game, destination := checked.OverwatchInstall, checked.OutputDirectory
	if workflow == "maps" {
		j.mu.Lock()
		catalog := j.mapCatalog
		j.mu.Unlock()
		if catalog == nil || catalog.Install != game {
			return Snapshot{}, dataToolError("Load maps for this Overwatch installation before selecting an export.")
		}
		mapID := pycompat.Get(selection, "mapId")
		var chosen *Map
		for i := range catalog.Maps {
			if id, ok := mapID.(string); ok && catalog.Maps[i].ID == id {
				chosen = &catalog.Maps[i]
				break
			}
		}
		if mapID != "*" && chosen == nil {
			return Snapshot{}, dataToolError("This map is not in the loaded catalog. Refresh maps and select it again.")
		}
		query, mapName := "*", "All maps"
		if chosen != nil {
			query, mapName = chosen.Query, chosen.Name
		}
		args, err := BuildMapExtractArgs(game, destination, query, selection)
		if err != nil {
			return Snapshot{}, err
		}
		return j.start("export", func(cancel *event) (any, error) {
			if err := os.MkdirAll(destination, 0o777); err != nil {
				return nil, err
			}
			if err := j.runProcess(cancel, executable, args); err != nil {
				return nil, err
			}
			return MapExportResult{OutputDirectory: destination, Workflow: "maps", MapID: mapID, MapName: mapName, Query: query}, nil
		})
	}
	args, err := BuildExtractArgs(game, destination, selection)
	if err != nil {
		return Snapshot{}, err
	}
	hero := pycompat.Str(pycompat.Get(selection, "hero"))
	asset := pycompat.Str(pycompat.Get(selection, "asset"))
	category := pycompat.Str(getDefault(selection, "category", "skin"))
	if hero != "*" {
		j.mu.Lock()
		catalog := j.catalog
		j.mu.Unlock()
		if catalog == nil || catalog.Install != game {
			return Snapshot{}, dataToolError("Load game assets for this Overwatch installation before selecting an export.")
		}
		found := false
		for _, row := range catalog.Heroes {
			found = found || row.Name == hero
		}
		if !found {
			return Snapshot{}, dataToolError("This hero is not in the loaded game catalog. Refresh game assets.")
		}
		if asset != "*" {
			unlocks, _ := catalog.Unlocks.Get(hero)
			found = false
			for _, row := range unlocks {
				found = found || (row.Name == asset && row.Category == category)
			}
			if !found {
				return Snapshot{}, dataToolError("This asset is not in the loaded game catalog. Refresh game assets.")
			}
		}
	} else if asset != "*" {
		return Snapshot{}, dataToolError("Select a hero for a specific asset, or choose all assets in the category.")
	}
	return j.start("export", func(cancel *event) (any, error) {
		if err := os.MkdirAll(destination, 0o777); err != nil {
			return nil, err
		}
		if err := j.runProcess(cancel, executable, args); err != nil {
			return nil, err
		}
		return ExportResult{OutputDirectory: destination, Hero: hero, Asset: asset, Category: category}, nil
	})
}

// Cancel requests cancellation of the running job.
func (j *Jobs) Cancel() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Busy {
		j.cancel.set()
		j.state.Message = "Cancelling…"
	}
	return j.snapshotLocked()
}

// Close cancels the running job and waits up to 20 seconds for it to end.
func (j *Jobs) Close() {
	j.Cancel()
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	}
}

// environment is os.environ without DATATOOL_OVERWATCH_DIR: the upstream
// executable supports an environment override of its install argument;
// remove it so the visible shared directory remains authoritative.
func environment() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "DATATOOL_OVERWATCH_DIR" || (runtime.GOOS == "windows" && strings.EqualFold(name, "DATATOOL_OVERWATCH_DIR")) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// process tracks one started DataTool process.
type process struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func (p *process) pid() int { return p.cmd.Process.Pid }

func (p *process) running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

func (p *process) waitTimeout(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.exited:
		return true
	case <-timer.C:
		return false
	}
}

func (p *process) kill() { _ = p.cmd.Process.Kill() }

// jobObject is the Windows Job Object owning the process tree.
type jobObject interface {
	terminate() bool
	close() error
}

func closeJob(job jobObject) {
	if job != nil {
		_ = job.close()
	}
}

// stopProcess bounds cancellation/cleanup waits; never spin forever after a
// failed kill.
func stopProcess(p *process, job jobObject) error {
	func() {
		defer closeJob(job)
		killTree(p, job)
	}()
	if p.waitTimeout(5 * time.Second) {
		return nil
	}
	p.kill()
	if p.waitTimeout(5 * time.Second) {
		return nil
	}
	return dataToolError("Windows did not finish stopping DataTool. Check the process before starting another export.")
}

type runOutput struct {
	mu        sync.Mutex
	failures  []string
	stderr    []string
	completed bool
}

// readLine is TextIOWrapper.readline with universal newlines: "\n", "\r"
// and "\r\n" all end a line.
func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		c, err := reader.ReadByte()
		if err != nil {
			return line, err
		}
		if c == '\n' {
			return append(line, c), nil
		}
		if c == '\r' {
			if next, err := reader.Peek(1); err == nil && next[0] == '\n' {
				_, _ = reader.ReadByte()
			}
			return append(line, '\n'), nil
		}
		line = append(line, c)
	}
}

func (j *Jobs) read(stream *os.File, label string, out *runOutput) {
	defer stream.Close()
	reader := bufio.NewReaderSize(stream, 64*1024)
	for {
		raw, err := readLine(reader)
		if len(raw) > 0 {
			line := pycompat.Strip(ansi.ReplaceAllString(pycompat.DecodeUTF8(raw), ""))
			j.log(label, line)
			out.mu.Lock()
			if strings.Contains(line, "Execution finished in") {
				out.completed = true
			}
			switch {
			case unsupported.MatchString(line):
				out.failures = append(out.failures, "This Toolchain does not support the installed Overwatch build. Check for a Toolchain update; upstream support may not be available yet.")
			case badRuntime.MatchString(line):
				out.failures = append(out.failures, "DataTool's runtime dependencies are missing or incompatible. Reinstall the complete Toolchain package and follow the runtime message in the log.")
			case badQuery.MatchString(line):
				out.failures = append(out.failures, "DataTool could not match or interpret this asset selection. Refresh game assets and select a supported item. "+line)
			case fatal.MatchString(line):
				out.failures = append(out.failures, line)
			}
			if label == "stderr" && line != "" && !strings.Contains(line, "Failed to detect") {
				out.stderr = append(out.stderr, line)
				if len(out.stderr) > 12 {
					out.stderr = out.stderr[len(out.stderr)-12:]
				}
			}
			out.mu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

func (j *Jobs) runProcess(cancel *event, executable string, args []string) (err error) {
	if cancel.isSet() {
		return errCancelled
	}
	executable = pycompat.Resolve(executable)
	if !pycompat.IsFile(executable) {
		return dataToolError("DataTool is missing. Install or repair Toolchain, then try again.")
	}
	j.log("avolition", "Running "+args[1]+"…")
	startFailed := func(err error) error {
		return dataToolErrorf("Could not start DataTool. Verify that Toolchain and its runtime dependencies are installed: %v", err)
	}
	argv := append([]string{executable}, args...)
	cmd, err := j.factory(Command{Args: argv, Dir: pycompat.Parent(executable), Env: environment()})
	if err != nil {
		return startFailed(err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return startFailed(err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		return startFailed(err)
	}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = stdoutWrite, stderrWrite
	configure(cmd)
	err = cmd.Start()
	stdoutWrite.Close()
	stderrWrite.Close()
	if err != nil {
		stdoutRead.Close()
		stderrRead.Close()
		return startFailed(err)
	}
	proc := &process{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(proc.exited)
	}()
	job, err := contain(proc)
	if err != nil {
		// A Windows process is still suspended if containment failed. Do not
		// run uncontained code and hope to discover its children afterwards.
		_ = stopProcess(proc, job)
		stdoutRead.Close()
		stderrRead.Close()
		return dataToolErrorf("Could not safely contain and start DataTool's process tree: %v", err)
	}
	defer func() {
		if proc.running() {
			_ = stopProcess(proc, job)
		}
		closeJob(job)
	}()
	out := &runOutput{}
	readers := make([]chan struct{}, 2)
	for i, stream := range []struct {
		file  *os.File
		label string
	}{{stdoutRead, "stdout"}, {stderrRead, "stderr"}} {
		finished := make(chan struct{})
		readers[i] = finished
		go func(file *os.File, label string) {
			defer close(finished)
			j.read(file, label, out)
		}(stream.file, stream.label)
	}
	select {
	case <-proc.exited:
	case <-cancel.ch:
		if err := stopProcess(proc, job); err != nil {
			return err
		}
	}
	if !proc.waitTimeout(5 * time.Second) {
		return fmt.Errorf("Command %s timed out after 5 seconds", pycompat.Repr(argv))
	}
	code := exitCode(cmd.ProcessState)
	// Close the job before joining streams: an abandoned child must not keep
	// stdout/stderr open after the main executable has exited.
	closeJob(job)
	if !isWindows {
		killTree(proc, nil)
	}
	for _, finished := range readers {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
		}
	}
	j.mu.Lock()
	j.state.ExitCode = &code
	j.mu.Unlock()
	if cancel.isSet() {
		return errCancelled
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.failures) > 0 {
		return dataToolError(out.failures[0])
	}
	if code != 0 {
		detail := "Open the log for details."
		if len(out.stderr) > 0 {
			detail = out.stderr[len(out.stderr)-1]
		}
		return dataToolErrorf("DataTool failed with exit code %d. %s", code, detail)
	}
	if len(out.stderr) > 0 {
		return dataToolError("DataTool reported errors; the output may be incomplete. " + out.stderr[0])
	}
	if !out.completed {
		return dataToolError("DataTool exited without confirming completion. Check the log for invalid arguments or a missing runtime.")
	}
	return nil
}
