package runtime

import (
	"errors"
	"os"
	"os/exec"

	"agentflow/internal/processgroup"
)

// prepareCommandProcess makes local sandbox cancellation include shell and MCP descendants.
func prepareCommandProcess(cmd *exec.Cmd) {
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := processgroup.Kill(cmd.Process.Pid)
			if errors.Is(err, os.ErrProcessDone) {
				// Cancellation can arrive before Start assigns the job.
				return cmd.Process.Kill()
			}
			return err
		}
	}
}

// Every Windows command gets a Job regardless of OwnProcessGroup.
func configureOwnedProcessGroup(cmd *exec.Cmd) {}

func startCommandProcess(cmd *exec.Cmd) (func(), error) {
	return processgroup.Start(cmd)
}
