//go:build windows

package datatool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const isWindows = true

// configure starts DataTool suspended, hidden and in its own process group.
// Its primary thread is resumed only after contain() has placed it in a
// kill-on-close Job Object.
func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
}

// windowsJob owns child processes even after DataTool exits; closing it
// kills the job tree (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE).
type windowsJob struct {
	mu     sync.Mutex
	handle windows.Handle
}

// newWindowsJob, resumePrimaryThread, terminateJob and runTaskkill are
// variables so tests can simulate containment and termination failures.
var newWindowsJob = func(pid int) (*windowsJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(handle, process); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsJob{handle: handle}, nil
}

var terminateJob = func(j *windowsJob) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.handle != 0 && windows.TerminateJobObject(j.handle, 1) == nil
}

func (j *windowsJob) terminate() bool { return terminateJob(j) }

func (j *windowsJob) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(j.handle)
	j.handle = 0
	return err
}

// resumePrimaryThread resumes only after containment, using documented Win32
// APIs. os/exec closes CreateProcess's primary-thread handle. CREATE_SUSPENDED
// leaves that thread unable to execute, so find its ID with Toolhelp and
// reopen it with THREAD_SUSPEND_RESUME. No process code can spawn a child
// before the Job Object has been assigned. A fresh suspended process has one
// thread.
// https://learn.microsoft.com/windows/win32/procthread/process-creation-flags
// https://learn.microsoft.com/windows/win32/api/processthreadsapi/nf-processthreadsapi-resumethread
var resumePrimaryThread = func(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	var threads []uint32
	func() {
		defer windows.CloseHandle(snapshot)
		var entry windows.ThreadEntry32
		entry.Size = uint32(unsafe.Sizeof(entry))
		for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
			if entry.OwnerProcessID == uint32(pid) {
				threads = append(threads, entry.ThreadID)
			}
			entry.Size = uint32(unsafe.Sizeof(entry))
		}
	}()
	if len(threads) != 1 {
		return errors.New("Could not identify DataTool's suspended primary thread.")
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threads[0])
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	previous, err := windows.ResumeThread(thread)
	if previous == 0xFFFFFFFF {
		return err
	}
	if previous != 1 {
		return errors.New("DataTool's primary thread was not in its expected suspended state.")
	}
	return nil
}

// runTaskkill runs taskkill /PID <pid> /T /F hidden, limited to 5 seconds.
// Only a numeric PID is passed; neither a shell nor a user command is run.
var runTaskkill = func(pid int) error {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "System32", "taskkill.exe"), "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_, err := cmd.Output()
	return err
}

// contain places the suspended process in a new Job Object, then resumes it.
func contain(p *process) (jobObject, error) {
	job, err := newWindowsJob(p.pid())
	if err != nil {
		return nil, err
	}
	if err := resumePrimaryThread(p.pid()); err != nil {
		return job, err
	}
	return job, nil
}

// killTree terminates the job; when that fails it falls back to taskkill.
// Closing a contained job also kills descendants if taskkill cannot find a
// parent which has already exited. Check the native termination result rather
// than silently assuming that an attempted cancellation succeeded.
func killTree(p *process, job jobObject) {
	if job != nil && job.terminate() {
		return
	}
	defer closeJob(job)
	// A non-zero exit, a start failure or the timeout all fall back to
	// killing the process itself.
	if err := runTaskkill(p.pid()); err != nil && p.running() {
		p.kill()
	}
}

func exitCode(state *os.ProcessState) int { return state.ExitCode() }
