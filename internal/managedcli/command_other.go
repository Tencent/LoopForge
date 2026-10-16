//go:build !linux && !darwin

package managedcli

import (
	"errors"
	"os/exec"
)

func configureCommandGroup(cmd *exec.Cmd) error {
	return errors.New("managed CLI updates are supported only on Linux and macOS")
}
