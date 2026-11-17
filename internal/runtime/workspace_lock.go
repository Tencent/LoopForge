package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const sandboxLockDir = ".locks"

func (m *Manager) lockPath(id string) string {
	return filepath.Join(m.root, sandboxLockDir, safeID(id)+".lock")
}

// A shared workspace lock is independent of the per-environment exclusive
// lock. Parent and child sandboxes may share a checkout, but no process may
// remove that checkout while any of them holds it.
func (m *Manager) workspaceLockPath(id string) string {
	return filepath.Join(m.root, sandboxLockDir, "workspace-"+safeID(id)+".lock")
}

func (m *Manager) acquireWorkspaceUse(ctx context.Context, id string, exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Join(m.root, sandboxLockDir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.workspaceLockPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), operation|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) tryAcquireWorkspaceDeletion(id string) (*os.File, bool, error) {
	if err := os.MkdirAll(filepath.Join(m.root, sandboxLockDir), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(m.workspaceLockPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
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

// tryAcquireLock holds the same host lock as a live sandbox throughout a
// destructive operation. A check followed by an unlock leaves a race in which
// a new sandbox can start before deletion completes.
func (m *Manager) tryAcquireLock(id string) (*os.File, bool, error) {
	if err := os.MkdirAll(filepath.Join(m.root, sandboxLockDir), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(m.lockPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
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
	f, err := os.OpenFile(m.lockPath(name), os.O_RDWR, 0o600)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return true
	}
	_ = unlockFile(f)
	return false
}
