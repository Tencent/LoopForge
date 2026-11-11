package runtime

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryExclusiveFileLock(f *os.File) error {
	// A fixed byte range makes independently opened handles contend, even on
	// an empty lock file. LockFileEx also releases it when the process exits.
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
}

func fileLockContended(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func releaseFileLock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
