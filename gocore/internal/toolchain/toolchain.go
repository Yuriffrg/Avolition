// Package toolchain ports owpreview/toolchain_runtime.py: a managed,
// independently updateable OWLib Toolchain installation.
//
// The caller supplies Avolition's toolchain directory and, optionally, a lock
// shared with DataTool jobs. Packages are immutable after validation.
// Publishing one small atomic pointer (active.json) activates a package, so an
// interrupted installation never overwrites the runtime a previous session
// used.
package toolchain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"avolition/gocore/internal/pycompat"
)

const (
	ReleaseURL  = "https://api.github.com/repos/overtools/OWLib/releases/latest"
	AssetName   = "toolchain-release.zip"
	MaxDownload = 2 * 1024 * 1024 * 1024
	MaxExpanded = 8 * 1024 * 1024 * 1024
	MaxEntries  = 50_000
	UserAgent   = "Avolition-Toolchain-Manager"
	maxMetadata = 4 * 1024 * 1024
	chunkSize   = 1024 * 1024
	timeout     = 30 * time.Second
)

// Opener performs one GET request with the given headers and returns the
// response body (urllib's urlopen). Non-2xx responses must be returned as
// *HTTPError and connection failures as *URLError (or net errors), so the UI
// receives the same actionable messages as the Python core.
type Opener func(url string, header http.Header) (io.ReadCloser, error)

// HTTPError is urllib.error.HTTPError.
type HTTPError struct {
	URL    string
	Code   int
	Header http.Header
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP Error %d: %s", e.Code, http.StatusText(e.Code))
}

// URLError is urllib.error.URLError (also used for timeouts and resets).
type URLError struct{ Reason error }

func (e *URLError) Error() string { return fmt.Sprintf("<urlopen error %v>", e.Reason) }
func (e *URLError) Unwrap() error { return e.Reason }

// ActivityLock is the lock shared with DataTool jobs (threading.Lock with
// acquire(blocking=False)). *sync.Mutex satisfies it.
type ActivityLock interface {
	TryLock() bool
	Unlock()
}

var defaultClient = &http.Client{Transport: func() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.TLSHandshakeTimeout = timeout
	return transport
}()}

// HTTPOpener is the default Opener: proxies from the environment, redirects
// followed, and urlopen(timeout=30) semantics (each wait on the network, not
// the whole download, is limited to 30 seconds).
func HTTPOpener(address string, header http.Header) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		cancel()
		return nil, &URLError{err}
	}
	request.Header = header.Clone()
	timer := time.AfterFunc(timeout, cancel)
	response, err := defaultClient.Do(request)
	if err != nil {
		timer.Stop()
		cancel()
		return nil, &URLError{err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		timer.Stop()
		cancel()
		response.Body.Close()
		return nil, &HTTPError{URL: address, Code: response.StatusCode, Header: response.Header}
	}
	return &idleBody{body: response.Body, timer: timer, cancel: cancel}, nil
}

type idleBody struct {
	body   io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.timer.Reset(timeout)
	n, err := b.body.Read(p)
	if err != nil && err != io.EOF {
		err = &URLError{err}
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	defer b.cancel()
	return b.body.Close()
}

func isNetworkError(err error) bool {
	var urlErr *URLError
	var netErr net.Error
	var goURLErr *url.Error
	return errors.As(err, &urlErr) || errors.As(err, &netErr) || errors.As(err, &goURLErr) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE)
}

func isOSError(err error) bool {
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var syscallErr *os.SyscallError
	var errno syscall.Errno
	return errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &syscallErr) || errors.As(err, &errno)
}

// ErrorMessage is _error_message: the UI text for a failed operation.
func ErrorMessage(err error) string {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case 403, 429:
			retry := ""
			reset := ""
			if httpErr.Header != nil {
				reset = httpErr.Header.Get("X-RateLimit-Reset")
			}
			if reset != "" && isDigits(reset) {
				if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
					// datetime supports years 1..9999 only.
					if moment := time.Unix(seconds, 0).UTC(); moment.Year() <= 9999 && seconds < 253402300800 {
						retry = " Retry after " + moment.Format("2006-01-02 15:04") + " UTC."
					}
				}
			}
			return "GitHub denied the request or its rate limit was reached." + retry + " The installed copy is still available."
		case 404:
			return "The OWLib release or package is unavailable on GitHub. Check for updates again later."
		}
		return fmt.Sprintf("GitHub returned HTTP %d. Try again later; the installed copy is unchanged.", httpErr.Code)
	}
	var zipErr *badZipError
	switch {
	case isNetworkError(err):
		return "Cannot reach GitHub. Check your internet connection and try again; the installed copy is unchanged."
	case errors.Is(err, fs.ErrPermission):
		return "Avolition cannot write to its toolchain folder. Check folder permissions and available disk space."
	case errors.As(err, &zipErr):
		return "The Toolchain download is not a valid ZIP archive. Try installing again; the installed copy is unchanged."
	case isOSError(err):
		return fmt.Sprintf("Toolchain file operation failed: %v. Check permissions and free disk space; the installed copy is unchanged.", err)
	}
	if message := err.Error(); message != "" {
		return message
	}
	return "The Toolchain operation failed. The installed copy is unchanged."
}

func isDigits(text string) bool {
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Options configure a Manager. Zero values select the defaults.
type Options struct {
	ActivityLock ActivityLock
	Opener       Opener
}

// Snapshot is ToolchainManager.snapshot(); key order matches Python.
type Snapshot struct {
	Root            string   `json:"root"`
	Installed       *Package `json:"installed"`
	Latest          *Package `json:"latest"`
	Executable      *string  `json:"executable"`
	UpdateAvailable *bool    `json:"updateAvailable"`
	CheckedAt       *string  `json:"checkedAt"`
	State           string   `json:"state"`
	Busy            bool     `json:"busy"`
	Error           *string  `json:"error"`
	CheckError      *string  `json:"checkError"`
	Message         string   `json:"message"`
}

// Manager is ToolchainManager.
type Manager struct {
	root     string
	activity ActivityLock
	opener   Opener
	rename   func(oldpath, newpath string) error // os.replace; tests inject failures

	cancelled atomic.Bool

	mu         sync.Mutex
	done       chan struct{}
	busy       bool
	state      string
	message    string
	err        *string
	checkErr   *string
	latest     *Package
	checkedAt  *string
	installed  *Package
	executable string
}

// NewManager is ToolchainManager(root, activity_lock=..., opener=...).
func NewManager(root string, options Options) *Manager {
	expanded, err := pycompat.ExpandUser(root)
	if err != nil {
		expanded = root
	}
	m := &Manager{
		root:     pycompat.Resolve(expanded),
		activity: options.ActivityLock,
		opener:   options.Opener,
		rename:   os.Rename,
		state:    "ready",
	}
	if m.activity == nil {
		m.activity = &sync.Mutex{}
	}
	if m.opener == nil {
		m.opener = HTTPOpener
	}
	m.loadInstalled()
	return m
}

// Root is the resolved toolchain directory.
func (m *Manager) Root() string { return m.root }

// ActivityLock returns the lock shared with DataTool jobs.
func (m *Manager) ActivityLock() ActivityLock { return m.activity }

func strPtr(value string) *string { return &value }

func (m *Manager) loadInstalled() {
	pointer := pycompat.Join(m.root, "active.json")
	if pycompat.Exists(pointer) {
		if err := m.loadPointer(pointer); err != nil {
			m.err = strPtr(fmt.Sprintf("The installed Toolchain could not be loaded: %s. Install a fresh copy.", err))
		}
		return
	}
	// A previously unpacked Toolchain is usable without inventing a release
	// version or date. Never adopt an abandoned staging/new package folder.
	if !pycompat.Exists(m.root) {
		return
	}
	candidates := findDataTool(m.root, true)
	if len(candidates) != 1 {
		return
	}
	valid, err := startsWithMZ(candidates[0])
	if err != nil {
		m.err = strPtr(ErrorMessage(err))
		return
	}
	if valid {
		m.executable = pycompat.Resolve(candidates[0])
		legacy := &Package{}
		for _, key := range []string{"version", "releaseDate", "packageUpdatedAt", "installedAt", "assetId", "digest"} {
			legacy.Set(key, nil)
		}
		legacy.Set("legacy", true)
		m.installed = legacy
	}
}

func (m *Manager) loadPointer(pointer string) error {
	invalid := toolchainError("Toolchain installation metadata is invalid.")
	info, err := os.Stat(pointer)
	if err != nil {
		return err
	}
	if info.Size() > 1024*1024 {
		return invalid
	}
	data, err := os.ReadFile(pointer)
	if err != nil {
		return err
	}
	value, err := pycompat.Loads(data)
	if err != nil {
		return err
	}
	if _, ok := pycompat.AsDict(value); !ok {
		// Python raises AttributeError here (value.get); report it as metadata.
		return fmt.Errorf("'%s' object has no attribute 'get'", pycompat.TypeName(value))
	}
	pkg, isPackage := pycompat.Get(value, "package").(*Package)
	if !pycompat.Equal(pycompat.Get(value, "schema"), 1) || !isPackage {
		return invalid
	}
	if !pycompat.Has(value, "executable") {
		return errors.New("'executable'") // str(KeyError('executable'))
	}
	relative, ok := pycompat.Get(value, "executable").(string)
	if !ok {
		return fmt.Errorf("'%s' object has no attribute 'replace'", pycompat.TypeName(pycompat.Get(value, "executable")))
	}
	parts, err := SafeMember(relative)
	if err != nil {
		return err
	}
	executable := pycompat.Resolve(pycompat.Join(m.root, parts...))
	if !pycompat.IsRelativeTo(executable, m.root) {
		return toolchainError("Toolchain installation metadata points outside its folder.")
	}
	valid, err := startsWithMZ(executable)
	if err != nil {
		return err
	}
	if !valid {
		return toolchainError("The installed DataTool executable is invalid.")
	}
	m.installed = pkg
	m.executable = executable
	return nil
}

func startsWithMZ(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, 2)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	return n == 2 && string(header) == "MZ", nil
}

func (m *Manager) requireOwnedPath(path string) (string, error) {
	resolved := pycompat.Resolve(path)
	if (pycompat.IsRelativeTo(resolved, m.root) && pycompat.IsRelativeTo(m.root, resolved)) || !pycompat.IsRelativeTo(resolved, m.root) {
		return "", toolchainError("The toolchain staging or package folder points outside Avolition's toolchain directory.")
	}
	return resolved, nil
}

// Executable returns the active DataTool.exe, or "" when none is installed.
func (m *Manager) Executable() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.executableLocked()
}

func (m *Manager) executableLocked() string {
	if m.executable != "" && pycompat.IsFile(m.executable) {
		return m.executable
	}
	return ""
}

func clonePackage(p *Package) *Package {
	if p == nil {
		return nil
	}
	return pycompat.Clone(p).(*Package)
}

// Snapshot returns the state shown by the Toolchain page.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() Snapshot {
	executable := m.executableLocked()
	snapshot := Snapshot{
		Root: m.root, Installed: clonePackage(m.installed), Latest: clonePackage(m.latest),
		CheckedAt: m.checkedAt, State: m.state, Busy: m.busy, Error: m.err,
		CheckError: m.checkErr, Message: m.message,
	}
	if executable != "" {
		snapshot.Executable = strPtr(executable)
	}
	if m.latest != nil && m.latest.Len() > 0 && m.checkErr == nil {
		available := executable == "" || PackageDiffers(m.installed, m.latest)
		snapshot.UpdateAvailable = &available
	}
	return snapshot
}

func (m *Manager) setState(state, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state, m.message = state, message
}

func (m *Manager) start(operation func() error, install bool) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return Snapshot{}, toolchainError("A Toolchain operation is already in progress.")
	}
	if install && !m.activity.TryLock() {
		return Snapshot{}, toolchainError("Wait for the active DataTool job to finish before installing or updating Toolchain.")
	}
	m.busy = true
	m.err = nil
	m.cancelled.Store(false)
	if install {
		m.state, m.message = "installing", "Preparing Toolchain installation…"
	} else {
		m.state, m.message = "checking", "Checking the official OWLib release…"
	}
	done := make(chan struct{})
	m.done = done
	go func() {
		defer close(done)
		if err := protect(operation); err != nil {
			message := ErrorMessage(err)
			m.mu.Lock()
			m.err = strPtr(message)
			m.state = "failed"
			m.message = message
			if !install {
				m.checkErr = strPtr(message)
			}
			m.mu.Unlock()
		}
		if install {
			m.activity.Unlock()
		}
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
	}()
	return m.snapshotLocked(), nil
}

func protect(operation func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return operation()
}

// CheckAsync starts a release check in the background.
func (m *Manager) CheckAsync() (Snapshot, error) { return m.start(m.check, false) }

// InstallAsync starts an installation or update in the background. It holds
// the shared activity lock until it finishes.
func (m *Manager) InstallAsync() (Snapshot, error) { return m.start(m.install, true) }

// Wait waits for the current operation (timeout <= 0 waits indefinitely).
func (m *Manager) Wait(timeout time.Duration) Snapshot {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done != nil {
		if timeout <= 0 {
			<-done
		} else {
			timer := time.NewTimer(timeout)
			select {
			case <-done:
			case <-timer.C:
			}
			timer.Stop()
		}
	}
	return m.Snapshot()
}

// Close requests cancellation. A closed app can abandon a staging directory,
// but never its last working package. Chunk boundaries also permit orderly
// cancellation.
func (m *Manager) Close() { m.cancelled.Store(true) }

func (m *Manager) request(address string, jsonResponse bool) (io.ReadCloser, error) {
	header := http.Header{}
	header.Set("User-Agent", UserAgent)
	if jsonResponse {
		header.Set("Accept", "application/vnd.github+json")
		header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	return m.opener(address, header)
}

func (m *Manager) check() error {
	response, err := m.request(ReleaseURL, true)
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(response, maxMetadata+1))
	response.Close()
	if err != nil {
		return err
	}
	if len(payload) > maxMetadata {
		return toolchainError("GitHub returned unexpectedly large release metadata.")
	}
	invalid := toolchainError("GitHub returned invalid release information. Try checking again later.")
	release, err := pycompat.Loads(pycompat.StripBOM(payload))
	if err != nil {
		return invalid
	}
	metadata, err := ReleaseMetadata(release)
	if err != nil {
		var typeErr *pycompat.TypeError
		var valueErr *pycompat.ValueError
		if errors.As(err, &typeErr) || errors.As(err, &valueErr) {
			return invalid
		}
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest = metadata
	m.checkedAt = strPtr(utcNow())
	m.checkErr = nil
	m.state = "ready"
	if PackageDiffers(m.installed, metadata) {
		m.message = "Toolchain package available."
	} else {
		m.message = "The installed package matches the latest official release."
	}
	return nil
}

func (m *Manager) checkCancelled() error {
	if m.cancelled.Load() {
		return toolchainError("Toolchain installation was interrupted. The installed copy is unchanged.")
	}
	return nil
}

func uuidHex() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return hex.EncodeToString(value[:])
}

func (m *Manager) install() (err error) {
	m.mu.Lock()
	needCheck := m.latest == nil || m.checkErr != nil
	m.mu.Unlock()
	if needCheck {
		if err := m.check(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	metadata := clonePackage(m.latest)
	m.mu.Unlock()
	if err := m.checkCancelled(); err != nil {
		return err
	}
	identifier := uuidHex()
	staging := pycompat.Join(m.root, ".staging", identifier)
	packageDir := pycompat.Join(m.root, "packages", identifier)
	temporaryPointer := pycompat.Join(m.root, ".active-"+identifier+".tmp")
	activated := false
	defer func() {
		// These are internally generated UUID paths under our toolchain
		// root. No cleanup ever touches the active or a legacy runtime.
		if pycompat.Exists(staging) {
			if owned, ownedErr := m.requireOwnedPath(staging); ownedErr == nil {
				_ = os.RemoveAll(owned)
			} else if err == nil {
				err = ownedErr
			}
		}
		if !activated && pycompat.Exists(packageDir) {
			if owned, ownedErr := m.requireOwnedPath(packageDir); ownedErr == nil {
				_ = os.RemoveAll(owned)
			} else if err == nil {
				err = ownedErr
			}
		}
		if removeErr := os.Remove(temporaryPointer); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && err == nil {
			err = removeErr
		}
	}()
	if _, err := m.requireOwnedPath(staging); err != nil {
		return err
	}
	if _, err := m.requireOwnedPath(packageDir); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o777); err != nil {
		return err
	}
	m.setState("downloading", "Downloading the complete Toolchain package…")
	archive := pycompat.Join(staging, AssetName)
	verified, err := m.download(metadata, archive)
	if err != nil {
		return err
	}
	m.setState("extracting", "Extracting Toolchain and its dependencies…")
	if err := m.extract(archive, pycompat.Join(staging, "runtime")); err != nil {
		return err
	}
	m.setState("validating", "Validating the DataTool runtime…")
	executable, err := runtimeExecutable(pycompat.Join(staging, "runtime"))
	if err != nil {
		return err
	}
	relativeExecutable, _ := pycompat.RelativeTo(executable, staging)
	metadata.Set("installedAt", utcNow())
	metadata.Set("digestVerified", verified)
	installation, err := pycompat.Dumps(metadata, 2)
	if err != nil {
		return err
	}
	if err := os.WriteFile(pycompat.Join(staging, "installation.json"), installation, 0o666); err != nil {
		return err
	}
	if err := m.checkCancelled(); err != nil {
		return err
	}
	if err := os.MkdirAll(pycompat.Parent(packageDir), 0o777); err != nil {
		return err
	}
	if err := m.rename(staging, packageDir); err != nil {
		return err
	}
	executable = pycompat.Join(packageDir, relativeExecutable)
	pointerExecutable, _ := pycompat.RelativeTo(executable, m.root)
	pointer := &Package{}
	pointer.Set("schema", 1)
	pointer.Set("executable", pointerExecutable)
	pointer.Set("package", metadata)
	if err := writeExclusive(temporaryPointer, pointer); err != nil {
		return err
	}
	if err := m.checkCancelled(); err != nil {
		return err
	}
	if err := m.rename(temporaryPointer, pycompat.Join(m.root, "active.json")); err != nil {
		return err
	}
	activated = true
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installed = metadata
	m.executable = executable
	m.state = "ready"
	if verified {
		m.message = "Toolchain installed and ready."
	} else {
		m.message = "Toolchain installed and ready. GitHub did not provide a digest for this package."
	}
	return nil
}

// writeExclusive is open(path, 'x') + json.dump(indent=2) + fsync.
func writeExclusive(path string, value any) error {
	data, err := pycompat.Dumps(value, 2)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
