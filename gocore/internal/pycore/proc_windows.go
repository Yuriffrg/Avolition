//go:build windows

package pycore

import (
	"io"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The desktop host starts the core hidden; keep the console child hidden too.
func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

type job windows.Handle

func (j job) Close() error { return windows.CloseHandle(windows.Handle(j)) }

// bind places the Python core in a kill-on-close job, so terminating the Go
// core (Electron's stop() fallback) also ends Python and its DataTool jobs.
// Nested jobs are supported since Windows 8; datatool.py adds its own.
func bind(cmd *exec.Cmd) (io.Closer, error) {
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
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(handle, process); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return job(handle), nil
}
