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

func runPreparedCommand(cmd *exec.Cmd) error {
	cleanup, err := processgroup.Start(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	return cmd.Wait()
}
