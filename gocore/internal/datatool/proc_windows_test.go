//go:build windows

package datatool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func assertProcessEnded(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return
		}
		var code uint32
		err = windows.GetExitCodeProcess(handle, &code)
		windows.CloseHandle(handle)
		if err != nil || code != stillActive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("Managed process %d is still alive", pid)
}

func assertContainedStart(t *testing.T, f *fixture) {
	t.Helper()
	attr := f.calls[0].cmd.SysProcAttr
	if attr == nil || attr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("Process must start suspended")
	}
}

func TestContainmentFailureNeverResumesChild(t *testing.T) {
	f := setup(t)
	previousJob, previousResume := newWindowsJob, resumePrimaryThread
	defer func() { newWindowsJob, resumePrimaryThread = previousJob, previousResume }()
	var resumed atomic.Bool
	newWindowsJob = func(int) (*windowsJob, error) { return nil, errors.New("fixture containment failure") }
	resumePrimaryThread = func(int) error { resumed.Store(true); return nil }
	f.export(t, "Overwatch Classic", nil)
	result := f.wait(t)
	if resumed.Load() {
		t.Fatal("resumed an uncontained process")
	}
	if result.State != "Failed" || !strings.Contains(errText(result), "safely contain") {
		t.Fatal(asJSON(result))
	}
	if _, err := os.Stat(filepath.Join(f.output, "fixture.owanimclip")); err == nil {
		t.Fatal("uncontained process ran")
	}
	assertProcessEnded(t, f.calls[0].cmd.Process.Pid)
}

func TestFailedNativeTerminationAndTaskkillTimeoutStillCloseJobTree(t *testing.T) {
	f := setup(t)
	f.export(t, "Wait", nil)
	child := f.waitForChild(t)
	previousTerminate, previousTaskkill := terminateJob, runTaskkill
	defer func() { terminateJob, runTaskkill = previousTerminate, previousTaskkill }()
	var called atomic.Bool
	terminateJob = func(*windowsJob) bool { return false }
	runTaskkill = func(int) error { called.Store(true); return errors.New("taskkill timed out after 5 seconds") }
	f.jobs.Cancel()
	result := f.wait(t)
	if !called.Load() {
		t.Fatal("taskkill fallback not used")
	}
	if result.State != "Cancelled" {
		t.Fatal(asJSON(result))
	}
	assertProcessEnded(t, child)
}
