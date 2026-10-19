package managedcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maintenanceIntentLockName = "maintenance-intent.lock"
	usageLockName             = "usage.lock"
	lockPollInterval          = 25 * time.Millisecond
)

// ClaimLease holds usage across the entire HTTP claim, inventory pin, task and
// completion report. Close only after all of them finish (including no-work
// responses). Lock files are permanent; process exit also releases flock.
type ClaimLease struct {
	mu     sync.Mutex
	intent *os.File // Only maintenance retains intent after acquisition.
	usage  *os.File
}

func BeginClaim(ctx context.Context, root string) (*ClaimLease, error) {
	return acquireLease(ctx, root, false)
}

// Close is idempotent and nil-safe. Release intent last, after publication.
func (l *ClaimLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.Join(closeLock(&l.usage), closeLock(&l.intent))
}

func closeLock(file **os.File) error {
	if *file == nil {
		return nil
	}
	err := (*file).Close()
	*file = nil
	return err
}

// WithMaintenance closes the entry turnstile (intent EX), then drains existing
// claims/tasks (usage EX). New long polls cannot starve this writer. Cancellation
// never releases locks while fn is running; fn must honor its own context.
func WithMaintenance(ctx context.Context, root string, fn func() error) error {
	lease, err := acquireLease(ctx, root, true)
	if err != nil {
		return err
	}
	defer lease.Close()
	return fn()
}

// WithUpdateLock serializes complete updates without blocking claims. Its
// callback may take maintenance only when publishing Current.
func WithUpdateLock(ctx context.Context, root string, fn func() error) error {
	file, err := lock(ctx, root, "updater.lock", true)
	if err != nil {
		return err
	}
	defer file.Close()
	return fn()
}

func acquireLease(ctx context.Context, root string, exclusive bool) (*ClaimLease, error) {
	intent, err := lock(ctx, root, maintenanceIntentLockName, exclusive)
	if err != nil {
		return nil, err
	}
	lease := &ClaimLease{intent: intent}
	lease.usage, err = lock(ctx, root, usageLockName, exclusive)
	if err == nil && !exclusive {
		// Never retain shared intent across HTTP long polls: exclusive intent
		// must be able to stop new readers while current claims finish.
		err = closeLock(&lease.intent)
	}
	if err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
}

func lock(ctx context.Context, root, name string, exclusive bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := prepareDirectory(root, ""); err != nil {
		return nil, err
	}
	file, err := openCoordinatorLock(root, name)
	if err != nil {
		return nil, err
	}
	if err := waitForLock(ctx, file, exclusive); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func waitForLock(ctx context.Context, file *os.File, exclusive bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryCoordinatorLock(file, exclusive)
		if err != nil {
			return fmt.Errorf("lock managed CLI %s: %w", filepath.Base(file.Name()), err)
		}
		if locked {
			return ctx.Err()
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
