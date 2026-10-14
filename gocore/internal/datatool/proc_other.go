//go:build !windows

package datatool

import (
	"os"
	"os/exec"
	"syscall"
)

const isWindows = false

// configure is start_new_session=True: the process tree shares one process
// group, so cancellation can signal every descendant.
func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// contain has nothing to do outside Windows (_WindowsJob has no handle).
func contain(*process) (jobObject, error) { return nil, nil }

// killTree is os.killpg(process.pid, SIGKILL); a missing group is ignored.
func killTree(p *process, _ jobObject) {
	_ = syscall.Kill(-p.pid(), syscall.SIGKILL)
}

// exitCode is Popen.returncode: negative signal numbers for killed processes.
func exitCode(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return -int(status.Signal())
	}
	return state.ExitCode()
}
