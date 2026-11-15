package httpapi

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockAssistantNativeFile uses one fixed byte so separate handles and processes contend.
func lockAssistantNativeFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errAssistantNativeBusy
	}
	return err
}

func unlockAssistantNativeFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
