//go:build !windows

package httpapi

import (
	"errors"
	"os"
	"syscall"
)

// lockAssistantNativeFile keeps shared continuation writes exclusive without waiting.
func lockAssistantNativeFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errAssistantNativeBusy
	}
	return err
}

func unlockAssistantNativeFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
