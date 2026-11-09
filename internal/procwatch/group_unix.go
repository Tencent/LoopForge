//go:build !windows

package procwatch

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// killGroup makes a kill reach the whole process tree. git hands the transfer
// to git-remote-https and docker build to its own helpers; killing only the
// direct child would leave them downloading after the step was stopped.
func killGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
