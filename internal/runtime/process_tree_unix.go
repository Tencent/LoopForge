//go:build !windows

package runtime

import (
	"os/exec"
	"syscall"

	"agentflow/internal/processgroup"
)

// prepareCommandProcess preserves the existing Unix sandbox process lifecycle.
func prepareCommandProcess(cmd *exec.Cmd) {}

func configureOwnedProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return processgroup.Kill(cmd.Process.Pid)
	}
}

func startCommandProcess(cmd *exec.Cmd) (func(), error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
			_ = processgroup.Kill(cmd.Process.Pid)
		}
	}, nil
}
