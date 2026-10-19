//go:build !linux && !darwin

package managedcli

import (
	"errors"
	"os"
)

func openCoordinatorLock(root, name string) (*os.File, error) {
	return nil, errors.New("managed CLI coordination is supported only on Linux and macOS")
}

func tryCoordinatorLock(file *os.File, exclusive bool) (bool, error) {
	return false, errors.New("managed CLI coordination is unsupported on this platform")
}
