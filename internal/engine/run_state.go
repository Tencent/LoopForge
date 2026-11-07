package engine

import (
	"agentflow/internal/domain"
	"context"
	"fmt"
	"log"
	"time"
)

func (e *Engine) updateRun(id string, mutate func(*domain.Run)) {
	_ = e.updateRunAt(id, false, mutate)
}

func (e *Engine) updateRunSettling(id string, mutate func(*domain.Run)) bool {
	return e.updateRunAt(id, true, mutate)
}

func (e *Engine) updateRunAt(id string, allowExpiredLease bool, mutate func(*domain.Run)) bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(id)
	if !ok {
		_ = e.takePendingObservations(id)
		return false
	}
	if isLiveStatus(r.Status) && r.OwnerInstanceID != "" && !e.ownsRun(r) && !allowExpiredLease {
		_ = e.takePendingObservations(id)
		return false
	}
	pending := e.takePendingObservations(id)
	if mutate == nil && len(pending) == 0 {
		return true
	}
	for _, item := range pending {
		e.applyObservation(&r, item.nodeID, item.event)
	}
	if mutate != nil {
		mutate(&r)
	}
	if err := e.repo.PutRun(r); err != nil {
		log.Printf("put run %s: %v", id, err)
		return false
	}
	if current, ok := e.repo.RunStreamView(id); ok {
		e.publishRun(current)
	}
	return true
}

// Subscribe provides provider-neutral live run snapshots. Slow clients receive
// the newest state rather than blocking execution workers.
func (e *Engine) Subscribe(runID string) (<-chan domain.Run, func(), error) {
	current, ok := e.repo.RunStreamView(runID)
	if !ok {
		return nil, nil, fmt.Errorf("run not found")
	}
	ch := make(chan domain.Run, 1)
	e.subMu.Lock()
	if e.subs[runID] == nil {
		e.subs[runID] = map[chan domain.Run]struct{}{}
	}
	e.subs[runID][ch] = struct{}{}
	e.subMu.Unlock()
	ch <- current
	stop := make(chan struct{})
	go e.pollRun(runID, stop, current.Revision)
	cancel := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		e.subMu.Lock()
		if _, exists := e.subs[runID][ch]; exists {
			delete(e.subs[runID], ch)
			close(ch)
		}
		if len(e.subs[runID]) == 0 {
			delete(e.subs, runID)
		}
		e.subMu.Unlock()
	}
	return ch, cancel, nil
}

func (e *Engine) pollRun(runID string, stop <-chan struct{}, revision int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			latestRevision, ok := e.repo.RunRevision(runID)
			if !ok {
				return
			}
			if latestRevision == revision {
				continue
			}
			current, ok := e.repo.RunStreamView(runID)
			if !ok {
				return
			}
			if current.Revision == revision {
				continue
			}
			revision = current.Revision
			e.publishRun(current)
		}
	}
}

func (e *Engine) StartClaimLoop(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		e.ResumeInterruptedRuns()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !e.executes() {
					return
				}
				if resumed := e.ResumeInterruptedRuns(); len(resumed) > 0 {
					log.Printf("claimed %d run(s): %v", len(resumed), resumed)
				}
			}
		}
	}()
}

func (e *Engine) publish(runID string) {
	current, ok := e.repo.RunState(runID)
	if !ok {
		return
	}
	e.publishRun(current)
}

func (e *Engine) publishRun(current domain.Run) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	for ch := range e.subs[current.ID] {
		select {
		case ch <- current:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- current:
			default:
			}
		}
	}
}
