package runner

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type taskLeaseContextKey struct{}

func taskLeaseManaged(ctx context.Context) bool {
	managed, _ := ctx.Value(taskLeaseContextKey{}).(bool)
	return managed
}

// keepTaskLease covers the whole claimed task, including preparation before
// StartTask and artifact collection after the process exits. Its independent
// goroutine is not delayed by output uploads or sandbox setup.
func (d *Daemon) keepTaskLease(parent context.Context, task domain.RunnerTask, interval time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.WithValue(parent, taskLeaseContextKey{}, true))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		until := task.LeaseUntil
		for {
			deadline := time.Now().Add(20 * time.Second)
			if until != nil && until.Before(deadline) {
				deadline = *until
			}
			renewCtx, stop := context.WithDeadline(ctx, deadline)
			response, err := d.client.RenewLease(renewCtx, task.ID)
			stop()
			if err == nil && response != nil {
				if strings.EqualFold(response.DesiredState, domain.RunnerTaskDesiredCancel) {
					cancel()
					return
				}
				if !response.LeaseUntil.IsZero() {
					next := response.LeaseUntil
					until = &next
				}
			} else if err != nil && ctx.Err() == nil {
				log.Printf("task %s renew lease: %v", task.ID, err)
				if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrTaskLeaseLost) || (until != nil && !time.Now().Before(*until)) {
					cancel()
					return
				}
			}
			var expiry <-chan time.Time
			var timer *time.Timer
			if until != nil {
				timer = time.NewTimer(time.Until(*until))
				expiry = timer.C
			}
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-expiry:
				cancel()
				return
			case <-ticker.C:
				if timer != nil {
					timer.Stop()
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		<-done
	}
}
