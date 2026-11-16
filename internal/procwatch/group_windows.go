//go:build windows

package procwatch

import (
	"errors"
	"os"
	"os/exec"

	"agentflow/internal/processgroup"
)

func runWatchedCommand(cmd *exec.Cmd) error {
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			err := processgroup.Kill(cmd.Process.Pid)
			if errors.Is(err, os.ErrProcessDone) {
				// Cancellation can reach the suspended root before job assignment.
				return cmd.Process.Kill()
			}
			return err
		}
	}
	cleanup, err := processgroup.Start(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	// The job reaps descendants holding pipes. A remaining WaitDelay is an error.
	return cmd.Wait()
}
