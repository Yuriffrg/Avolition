//go:build !windows

package datatool

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// assertProcessEnded checks that pid no longer runs (a zombie awaiting its
// reaper counts as ended).
func assertProcessEnded(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("Managed process %d is still alive", pid)
}

func assertContainedStart(t *testing.T, f *fixture) {
	t.Helper()
	attr := f.calls[0].cmd.SysProcAttr
	if attr == nil || !attr.Setsid {
		t.Fatal("DataTool must start in a new session")
	}
}
