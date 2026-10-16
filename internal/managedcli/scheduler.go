package managedcli

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"log"
	"time"
)

// Fixed UTC+08 works even on images without a timezone database.
var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

// Start never blocks worker startup on network access. The returned stop
// function cancels lock waits/commands and joins the loop; repeated and
// concurrent calls are safe. No configuration or state is polled while idle.
func Start(ctx context.Context, root string) func() {
	return start(ctx, func() (*Manager, error) { return NewManager(root) })
}

func start(ctx context.Context, factory func() (*Manager, error)) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if ctx.Err() != nil {
			return
		}
		m, err := factory()
		if err != nil {
			log.Printf("managed CLI startup: %v", err)
			return
		}
		// Past windows fire immediately for startup catch-up. Calculate the
		// target before waiting so crossing the boundary cannot skip today.
		next := scheduledAt(m.Root, m.Now())
		for ctx.Err() == nil {
			timer := time.NewTimer(next.Sub(m.Now()))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			report, err := m.Check(ctx)
			if report.Action != "" {
				log.Printf("managed CLI daily check: %+v", report.Results)
			}
			if err != nil && ctx.Err() == nil {
				log.Printf("managed CLI check: %v; waiting for next daily window", err)
			}
			// Compute from completion time: long updates never replay missed days.
			next = nextScheduledAt(m.Root, m.Now())
		}
	}()
	return func() { cancel(); <-done }
}

// Only today's window is eligible: startup catches up once, never replays a
// backlog. Hashing root and date spreads hosts over the fixed 0..600s window.
func scheduledAt(root string, now time.Time) time.Time {
	now = now.In(beijing)
	sum := sha256.Sum256([]byte(root + "\x00" + now.Format("2006-01-02")))
	jitter := time.Duration(binary.BigEndian.Uint64(sum[:8])%601) * time.Second
	return time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, beijing).Add(jitter)
}

// nextScheduledAt is strictly future, including at the exact window boundary.
func nextScheduledAt(root string, now time.Time) time.Time {
	at := scheduledAt(root, now)
	if !at.After(now) {
		at = scheduledAt(root, now.In(beijing).AddDate(0, 0, 1))
	}
	return at
}
