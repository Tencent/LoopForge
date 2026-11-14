//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartRunner replaces the daemon while preserving its arguments and environment.
func restartRunner(binary string) error {
	return syscall.Exec(binary, os.Args, os.Environ())
}
