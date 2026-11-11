//go:build !windows

package runtime

import "os/exec"

// prepareCommandProcess preserves the existing Unix sandbox process lifecycle.
func prepareCommandProcess(cmd *exec.Cmd) {}

func runPreparedCommand(cmd *exec.Cmd) error { return cmd.Run() }
