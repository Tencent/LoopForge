//go:build !windows

// Package processgroup manages child process trees without shell interpolation.
package processgroup

import (
	"errors"
	"os/exec"
	"syscall"
)

// configure gives a child and its descendants a separate process group.
func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Start preserves Unix startup and leaves lifecycle management with the caller.
func Start(cmd *exec.Cmd) (func(), error) {
	configure(cmd)
	return func() {}, cmd.Start()
}

// Terminate requests graceful termination of the process group.
func Terminate(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) }

// Kill terminates every process in the group immediately.
func Kill(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }

// Alive treats permission-denied processes as alive, preserving their ownership.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
