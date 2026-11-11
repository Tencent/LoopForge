//go:build !windows

package procwatch

import (
	"errors"
	"os/exec"
	"syscall"
)

// runWatchedCommand makes a kill reach the whole process tree. git hands the transfer
// to git-remote-https and docker build to its own helpers; killing only the
// direct child would leave them downloading after the step was stopped.
func runWatchedCommand(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}
