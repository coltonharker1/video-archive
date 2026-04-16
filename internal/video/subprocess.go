package video

import (
	"os/exec"
	"syscall"
	"time"
)

// applySubprocessSafety configures a command for safe subprocess execution:
// new process group, SIGKILL on cancel, and a wait delay for cleanup.
func applySubprocessSafety(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 30 * time.Second
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
