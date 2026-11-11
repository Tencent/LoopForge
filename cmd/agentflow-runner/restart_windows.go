package main

import (
	"os"
	"os/exec"
	"syscall"
)

// restartRunner starts the replacement daemon before the old Windows process exits.
func restartRunner(binary string) error {
	cmd := exec.Command(binary, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
