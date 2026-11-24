package runtime

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const sandboxLockDir = ".locks"

// TryLockWorkspaceCheckpoint serializes recovery and reclamation across
// processes sharing a workspace root. A nil release means the workspace is busy.
func (m *Manager) TryLockWorkspaceCheckpoint(runID string) (release func(), err error) {
	if m == nil || m.root == "" {
		return nil, os.ErrNotExist
	}
	root := filepath.Join(m.root, sandboxLockDir)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return tryLockSandboxFile(filepath.Join(root, safeID(runID)+".checkpoint.lock"))
}

func tryLockSandboxFile(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, nil
		}
		return nil, err
	}
	return func() { _ = unlockFile(file) }, nil
}

func (m *Manager) lockPath(id string) string {
	return filepath.Join(m.root, sandboxLockDir, safeID(id)+".lock")
}

func (m *Manager) acquireLock(ctx context.Context, id string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Join(m.root, sandboxLockDir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.lockPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(ctx, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func flockExclusive(ctx context.Context, f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func unlockFile(f *os.File) error {
	if f == nil {
		return nil
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}

func (m *Manager) workspaceInUse(name string) bool {
	if m == nil || name == "" || name == sandboxLockDir {
		return false
	}
	path := m.lockPath(name)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
