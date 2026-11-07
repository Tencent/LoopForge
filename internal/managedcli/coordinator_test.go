//go:build linux || darwin

package managedcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func claimAndClose(ctx context.Context, root string) error {
	lease, err := BeginClaim(ctx, root)
	lease.Close()
	return err
}

func TestLockCancellationAndPermanentFiles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exclusive bool
		wait      func(context.Context, string) error
	}{
		{usageLockName, true, claimAndClose},
		{usageLockName, false, func(ctx context.Context, root string) error {
			return WithMaintenance(ctx, root, func() error { panic("bypassed usage") })
		}},
		{"updater.lock", true, func(ctx context.Context, root string) error {
			return WithUpdateLock(ctx, root, func() error { panic("bypassed updater") })
		}},
	} {
		root := t.TempDir()
		held, err := lock(context.Background(), root, tc.name, tc.exclusive)
		must(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		err = tc.wait(ctx, root)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s wait=%v", tc.name, err)
		}
		intent, err := lock(context.Background(), root, maintenanceIntentLockName, true)
		must(t, err) // Partial acquisition/cancellation must not leak intent.
		must(t, intent.Close())
		before, err := held.Stat()
		must(t, err)
		must(t, held.Close())
		after, err := os.Stat(filepath.Join(root, tc.name))
		must(t, err)
		if !os.SameFile(before, after) {
			t.Fatal("lock inode changed")
		}
	}
}

func TestCallbacksReleaseAndUpdaterDoesNotBlockClaims(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want := errors.New("callback failed")
	if err := WithUpdateLock(ctx, root, func() error {
		must(t, claimAndClose(ctx, root))
		return WithMaintenance(ctx, root, func() error { return want })
	}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing panic")
			}
		}()
		_ = WithMaintenance(ctx, root, func() error { panic("test") })
	}()
	must(t, claimAndClose(ctx, root))
	must(t, WithUpdateLock(ctx, root, func() error { return nil }))
}

func TestRejectNonRegularLocks(t *testing.T) {
	for _, name := range []string{maintenanceIntentLockName, usageLockName, "updater.lock"} {
		for _, kind := range []string{"symlink", "directory"} {
			root := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			writeTestFile(t, target, "untouched")
			path := filepath.Join(root, name)
			if kind == "symlink" {
				must(t, os.Symlink(target, path))
			} else {
				must(t, os.Mkdir(path, 0700))
			}
			if f, err := lock(context.Background(), root, name, true); err == nil {
				f.Close()
				t.Fatalf("accepted %s %s", name, kind)
			}
			data, err := os.ReadFile(target)
			must(t, err)
			if string(data) != "untouched" {
				t.Fatal("modified external lock target")
			}
		}
	}
}

func waitForMaintenanceIntent(t *testing.T, root string) {
	t.Helper()
	waitUntil(t, func() bool {
		intent, err := openCoordinatorLock(root, maintenanceIntentLockName)
		must(t, err)
		locked, err := tryCoordinatorLock(intent, false)
		intent.Close()
		must(t, err)
		return !locked
	})
}

func TestLongPollReadersCannotStarveMaintenance(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var readers sync.WaitGroup
	defer func() { cancel(); readers.Wait() }()
	ready := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func(i int) {
			defer readers.Done()
			first := true
			for ctx.Err() == nil {
				lease, err := BeginClaim(ctx, root)
				if err != nil {
					return
				}
				if first {
					ready <- struct{}{}
					first = false
				}
				timer := time.NewTimer(time.Duration(80+i*13) * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				timer.Stop()
				lease.Close()
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-ready
	}
	deadline, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	must(t, WithMaintenance(deadline, root, func() error { return nil }))
}
