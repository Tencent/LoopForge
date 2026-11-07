//go:build linux || darwin

package managedcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Permanent regular files only: unlinking a lock creates independent domains.
func openCoordinatorLock(root, name string) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open managed CLI root: %w", err)
	}
	defer unix.Close(fd)
	// Separate first creation: concurrent O_CREAT|O_NOFOLLOW can return ENOENT
	// on macOS, whereas O_EXCL gives the losing creator an unambiguous EEXIST.
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	lockFD, err := unix.Openat(fd, name, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if errors.Is(err, unix.EEXIST) {
		lockFD, err = unix.Openat(fd, name, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open managed CLI lock %s: %w", name, err)
	}
	file := os.NewFile(uintptr(lockFD), filepath.Join(root, name))
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err == nil {
			err = errors.New("lock must be a regular file")
		}
		return nil, err
	}
	return file, nil
}

func tryCoordinatorLock(file *os.File, exclusive bool) (bool, error) {
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	err := unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}
