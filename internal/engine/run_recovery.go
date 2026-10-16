package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

func (e *Engine) ownsRun(r domain.Run) bool {
	e.mu.Lock()
	instanceID, enabled := e.instanceID, e.executeRuns
	e.mu.Unlock()
	return enabled && r.OwnerInstanceID == instanceID && r.Generation > 0 && r.LeaseUntil.After(time.Now().UTC())
}

// foreignWorkspaceBind reports whether the run is already bound to a
// (host, workspace root) pair this instance cannot serve. While that host is
// alive the run stays there: an in-flight node may hold changes that are not
// on the remote work branch yet. See workspaceBlocked for when the bind may
// be taken over.
func (e *Engine) foreignWorkspaceBind(r domain.Run) bool {
	boundRoot := strings.TrimSpace(r.WorkspaceRoot)
	if boundRoot == "" {
		return false
	}
	return boundRoot != e.sandboxes.Root() || r.WorkspaceHost != e.workspaceHost
}

// workspaceBlocked reports whether a foreign bind must still be honoured: the
// bound host is this host (another process under a different root) or some
// live worker still advertises it. A bind to a host that is gone protects
// nothing, so this instance may take the run over and rebuild the checkout
// from the remote work branch, which every successful node pushes to.
func (e *Engine) workspaceBlocked(r domain.Run) bool {
	if !e.foreignWorkspaceBind(r) {
		return false
	}
	if r.WorkspaceHost == e.workspaceHost || e.repo.WorkspaceHostAlive(r.WorkspaceHost) {
		return true
	}
	// Only a run whose code lives on pushed work branches can be rebuilt elsewhere.
	for _, rc := range r.RepositoryContexts() {
		if rc.RepositoryID != "" && !workBranchSyncable(rc) {
			return true
		}
	}
	return e.defaultExecutionRunner(r) != ""
}

func (e *Engine) acquireLease(r domain.Run) (domain.Run, error) {
	e.mu.Lock()
	instanceID, ttl, enabled, draining := e.instanceID, e.leaseTTL, e.executeRuns, e.draining
	e.mu.Unlock()
	if !enabled || draining {
		return domain.Run{}, ErrExecutionDisabled
	}
	if err := e.repo.TryAcquireRunLease(r.ID, instanceID, r.Generation, ttl); err != nil {
		return domain.Run{}, err
	}
	current, ok := e.repo.RunState(r.ID)
	if !ok {
		return domain.Run{}, fmt.Errorf("run not found")
	}
	return current, nil
}

func (e *Engine) renewRunLease(runID string, generation int) error {
	e.mu.Lock()
	instanceID, ttl := e.instanceID, e.leaseTTL
	e.mu.Unlock()
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		last = e.repo.RenewRunLease(runID, instanceID, generation, ttl)
		if last == nil {
			return nil
		}
		if errors.Is(last, repo.ErrRunLeaseLost) || errors.Is(last, repo.ErrRunLeaseHeld) {
			return last
		}
	}
	return last
}

// ResumeInterruptedRuns re-attaches scheduler goroutines to runs left in a live
// state by a previous process. In-flight nodes are reset to pending so execution
// can continue from the last durable checkpoint instead of being marked failed.
// Runs parked on a human gate are left alone: Approve/Retry can still resume them.
func (e *Engine) ResumeInterruptedRuns() []string {
	if !e.executes() {
		return nil
	}
	var resumed []string
	// Promotion is per bucket; a failing bucket still returns the others and
	// must not stop interrupted-run recovery below.
	promoted, err := e.repo.PromoteQueuedRuns()
	if err != nil {
		log.Printf("promote queued runs: %v", err)
	}
	for _, id := range promoted {
		e.publish(id)
	}
	for _, listed := range e.repo.Runs() {
		// Approval and operator-paused Runs normally have no scheduler. A cancel
		// request that raced with the last worker write must therefore be claimed
		// and settled by the recovery loop instead of waiting forever.
		if listed.Status == "waiting" && !listed.CancelRequested && alignRunWaitingApproval(listed) {
			r, ok := e.repo.RunState(listed.ID)
			if !ok || r.Status != "waiting" || r.CancelRequested || !alignRunWaitingApproval(r) {
				continue
			}
			if r.OwnerInstanceID != "" && r.LeaseUntil.After(time.Now().UTC()) {
				continue
			}
			if _, err := e.acquireLease(r); err != nil {
				continue
			}
			e.updateRun(r.ID, func(current *domain.Run) {
				if autoSucceedAlignApprovals(current, time.Now().UTC()) {
					e.event(current, "", "gate.auto_approved", "基线对齐无需人工确认，已自动通过")
				}
			})
			e.resume(r.ID)
			resumed = append(resumed, r.ID)
			continue
		}
		if listed.Status == "waiting" && listed.CancelRequested {
			r, ok := e.repo.RunState(listed.ID)
			if !ok || r.Status != "waiting" || !r.CancelRequested {
				continue
			}
			if r.OwnerInstanceID != "" && r.LeaseUntil.After(time.Now().UTC()) {
				continue
			}
			acquired, err := e.acquireLease(r)
			if err != nil {
				continue
			}
			_ = e.cancelRunnerTasksForRun(r.ID)
			e.settleOrphan(r.ID, "运行已被手动终止")
			_ = e.repo.ReleaseRunLease(r.ID, e.instanceID, acquired.Generation)
			continue
		}
		if listed.Status == "running" {
			fresh, ok := e.repo.RunState(listed.ID)
			if !ok || fresh.Status != "running" {
				continue
			}
			status, msg, done := settleStatusForCompletedGraph(workflowForRun(e.repo, fresh), fresh)
			// A scheduled run still has to push into the integration branch after
			// the last node succeeds. Settling here deletes that checkout mid-push.
			if done && !(status == "succeeded" && scheduledIntegratePending(fresh)) {
				e.finishAt(fresh.ID, fresh.Generation, status, msg)
				continue
			}
		}
		if listed.Status != "running" {
			continue
		}
		r, ok := e.repo.RunState(listed.ID)
		if !ok || r.Status != "running" {
			continue
		}
		if r.OwnerInstanceID != "" && r.LeaseUntil.After(time.Now().UTC()) {
			continue
		}
		e.mu.Lock()
		_, live := e.running[r.ID]
		e.mu.Unlock()
		if live {
			continue
		}
		// A run whose workspace is still served by another live host can never
		// be resumed here, but settling a cancel or pause only writes run
		// state, and cleanupWorkspace already refuses foreign binds. Claiming
		// those two still has to happen or the run stays "running" forever: the
		// API process has execution disabled, so Engine.Cancel cannot settle it
		// either. A bind to a host that is gone is taken over and rebuilt.
		blocked := e.workspaceBlocked(r)
		if blocked && !r.CancelRequested && !r.PauseRequested {
			continue
		}
		previousOwner := r.OwnerInstanceID
		acquired, err := e.acquireLease(r)
		if err != nil {
			continue
		}
		if r.CancelRequested || acquired.CancelRequested {
			_ = e.cancelRunnerTasksForRun(r.ID)
			e.settleOrphan(r.ID, "运行已被手动终止")
			_ = e.repo.ReleaseRunLease(r.ID, e.instanceID, acquired.Generation)
			continue
		}
		if r.PauseRequested || acquired.PauseRequested {
			_ = e.cancelRunnerTasksForRun(r.ID)
			e.pauseAt(r.ID, acquired.Generation, "运行已由用户暂停")
			_ = e.repo.ReleaseRunLease(r.ID, e.instanceID, acquired.Generation)
			continue
		}
		if blocked {
			// The control request was withdrawn between the check above and the
			// lease. Hand the run back instead of resuming another host's bind.
			_ = e.repo.ReleaseRunLease(r.ID, e.instanceID, acquired.Generation)
			continue
		}
		e.updateRun(r.ID, func(current *domain.Run) {
			resetInFlightNodesForResume(current, "执行实例退出，等待恢复")
			current.Status = "running"
			e.event(current, "", "run.resumed", fmt.Sprintf("服务恢复后继续执行（generation=%d, previousOwner=%s）", acquired.Generation, previousOwner))
		})
		e.observe(r.ID, "", domain.TelemetryEvent{Category: "orchestration", Name: "run.resumed", Phase: "start", Message: "过期执行租约已接管", Attributes: map[string]any{"generation": acquired.Generation, "previousOwner": previousOwner}})
		e.resume(r.ID)
		resumed = append(resumed, r.ID)
	}
	return resumed
}

// RecoverInterruptedRuns keeps the old name for callers/tests that still refer to it.
func (e *Engine) RecoverInterruptedRuns() []string {
	return e.ResumeInterruptedRuns()
}

func (e *Engine) ActiveRunCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running)
}

// Drain stops claiming work and parks in-flight runs so a replacement worker
// can resume them. Waiting for CodeBuddy nodes to finish would outlive
// systemd TimeoutStopSec and get the process SIGKILL'd mid-exec.
func (e *Engine) Drain(ctx context.Context) error {
	e.mu.Lock()
	e.draining = true
	instanceID := e.instanceID
	cancels := make([]context.CancelFunc, 0, len(e.running))
	for _, cancel := range e.running {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	e.mu.Unlock()
	_ = e.repo.DeleteEngineInstance(instanceID)
	for _, cancel := range cancels {
		cancel()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if e.ActiveRunCount() == 0 {
			e.detachAllRunEnvironments()
			return nil
		}
		select {
		case <-ctx.Done():
			e.detachAllRunEnvironments()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *Engine) claimRunning(runID string, cancel context.CancelFunc) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.running[runID]; ok {
		return false
	}
	e.running[runID] = cancel
	return true
}

func (e *Engine) lockRunExec(runID string) func() {
	e.runEnvMu.Lock()
	if e.runExec == nil {
		e.runExec = map[string]*sync.Mutex{}
	}
	mu := e.runExec[runID]
	if mu == nil {
		mu = &sync.Mutex{}
		e.runExec[runID] = mu
	}
	e.runEnvMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (e *Engine) clearRunning(runID string) {
	e.mu.Lock()
	delete(e.running, runID)
	e.mu.Unlock()
}

func (e *Engine) resume(runID string) {
	current, ok := e.repo.RunState(runID)
	if !ok || current.Status != "running" || !e.ownsRun(current) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !e.claimRunning(runID, cancel) {
		cancel()
		// A control decision can reopen a run immediately after cancellation,
		// while the cancelled scheduler is still unwinding. Retry once that
		// scheduler releases its slot so the persisted running state is not left
		// without an owner.
		go e.resumeAfterSchedulerRelease(runID)
		return
	}
	go e.execute(ctx, runID)
}

func (e *Engine) resumeAfterSchedulerRelease(runID string) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(e.leaseTTL)
	defer timeout.Stop()
	for {
		select {
		case <-ticker.C:
			if e.isRunSchedulerActive(runID) {
				continue
			}
			current, ok := e.repo.RunState(runID)
			if ok && current.Status == "running" {
				if current.WorkflowSnapshot.CollaborationMode == "group" && !e.ownsRun(current) {
					if _, err := e.acquireLease(current); err != nil {
						return
					}
				}
				if e.ownsRun(current) {
					e.resume(runID)
				}
			}
			return
		case <-timeout.C:
			return
		}
	}
}

func (e *Engine) runControlRequested(runID string) bool {
	return e.repo.RunControlRequested(runID)
}
