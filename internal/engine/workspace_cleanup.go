package engine

import (
	"agentflow/internal/domain"
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

func (e *Engine) RunWorkspace(runID string) string {
	if e.sandboxes == nil {
		return ""
	}
	return e.sandboxes.RunWorkspace(runID)
}

// RunWorkspaceFacts returns the workspace facts last reported by the run's
// execution machine; ok is false when the engine has none and must inspect
// RunWorkspace itself.
func (e *Engine) RunWorkspaceFacts(runID string) (domain.RunnerWorkspaceFacts, bool) {
	e.runEnvMu.Lock()
	defer e.runEnvMu.Unlock()
	facts, ok := e.workspaceFacts[runID]
	return facts, ok
}

func (e *Engine) recordWorkspaceFacts(runID string, outputs *domain.RunnerTaskOutputs) {
	if outputs == nil || outputs.Facts == nil {
		return
	}
	e.runEnvMu.Lock()
	e.workspaceFacts[runID] = *outputs.Facts
	e.runEnvMu.Unlock()
}

func (e *Engine) QueuePreviewArchive(run domain.Run, spec domain.RunnerPreviewArchiveSpec) error {
	return e.queueRunnerPreviewArchive(run, spec)
}

func (e *Engine) CancelPreviewArchive(runID string) {
	tasks, err := e.repo.ActiveRunnerTasksForRun(runID)
	if err != nil {
		log.Printf("list preview archive tasks %s: %v", runID, err)
		return
	}
	now := time.Now().UTC()
	for _, task := range tasks {
		if task.Action != domain.RunnerTaskActionArchivePreview {
			continue
		}
		if err := e.repo.CancelRunnerTask(task.ID, now); err != nil {
			log.Printf("cancel preview archive %s: %v", task.ID, err)
			continue
		}
		e.NotifyRunnerTaskChanged(task.ID)
		if updated, ok := e.repo.RunnerTask(task.ID); ok {
			e.notifyRunnerControl(updated.LeaseOwner, updated.ID, updated.DesiredState)
		}
	}
}

func (e *Engine) ReleaseWorkspace(runID string) {
	e.cleanupRunWorkspace(runID)
}

func (e *Engine) sandboxSweep(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.preview != nil {
				e.recoverBuiltPreviewArchives()
				e.recoverMissingPreviewFinalizers()
				e.preview.Reconcile()
			}
			e.reconcileTerminalWorkspaceCleanup()
			e.reclaimSandboxes()
		}
	}
}

func (e *Engine) reconcileTerminalWorkspaceCleanup() {
	now := time.Now().UTC()
	seen := map[string]bool{}
	for _, run := range e.repo.TerminalWorkspaceCleanupRuns() {
		seen[run.ID] = true
		e.cleanupWorkspace(run, now)
	}
	for _, id := range e.repo.CheckpointWorkspaceRunIDs() {
		if !seen[id] {
			if run, ok := e.repo.RunState(id); ok {
				e.cleanupWorkspace(run, now)
			}
		}
	}
}

// recoverBuiltPreviewArchives covers a finished preview build that never
// wrote a preview row: the run is still waiting on a later gate, or the
// process restarted before the archive was queued. It runs before the
// succeeded-run sweep so a runner workspace is not judged from the empty
// control-plane checkout and then skipped.
func (e *Engine) recoverBuiltPreviewArchives() {
	if e.preview == nil {
		return
	}
	for _, runID := range e.repo.RunsAwaitingBuiltPreview() {
		run, ok := e.repo.RunState(runID)
		if !ok || !run.WorkflowSnapshot.Preview.Static() || !run.WorkspaceCleanedAt.IsZero() {
			continue
		}
		if _, ok := e.repo.PreviewByRun(run.ID); ok {
			continue
		}
		_ = e.preview.ArchiveBuiltPreview(run)
	}
}

// recoverMissingPreviewFinalizers covers the crash window after a run is
// persisted as succeeded but before BeginFinalization wrote the preview row
// and cleanup hold. Sweep must run this before terminal workspace cleanup.
func (e *Engine) recoverMissingPreviewFinalizers() {
	if e.preview == nil {
		return
	}
	for _, run := range e.repo.TerminalWorkspaceCleanupRuns() {
		if run.Status != "succeeded" || !run.WorkspaceCleanedAt.IsZero() {
			continue
		}
		if !run.WorkflowSnapshot.Preview.Static() {
			continue
		}
		if _, ok := e.repo.PreviewByRun(run.ID); ok {
			continue
		}
		_ = e.preview.BeginFinalization(run)
	}
}

func (e *Engine) cleanupRunWorkspace(runID string) {
	run, ok := e.repo.RunState(runID)
	if !ok {
		return
	}
	e.cleanupWorkspace(run, time.Now().UTC())
}

func (e *Engine) cleanupWorkspace(run domain.Run, now time.Time) {
	managed, checkpointClaimed, releaseCheckpoint, checkpointErr := e.claimIdleConversationWorkspace(run, now)
	if releaseCheckpoint != nil {
		defer releaseCheckpoint()
	}
	if checkpointErr != nil {
		log.Printf("preserve workspace %s: %v", run.ID, checkpointErr)
		return
	}
	if managed && !checkpointClaimed {
		return
	}
	runnerID := e.defaultExecutionRunner(run)
	if !run.WorkspaceCleanedAt.IsZero() {
		if runnerID == "" {
			return
		}
		task, exists := e.repo.RunnerTask("rt-cleanup-" + safeRunnerPath(run.ID))
		if !exists {
			// A removed Runner has already cleaned its workspace as part of
			// deregistration; otherwise allow the request to be queued again.
			if _, runnerExists := e.repo.Runner(runnerID); runnerExists {
				_ = e.repo.ResetRunWorkspaceCleanup(run.ID)
			}
			return
		}
		if task.Status == domain.RunnerTaskFailed || task.Status == domain.RunnerTaskCanceled {
			_ = e.repo.ResetRunWorkspaceCleanup(run.ID)
		}
		return
	}
	claimed, err := checkpointClaimed, error(nil)
	if !managed {
		claimed, err = e.repo.ClaimRunWorkspaceCleanup(run.ID, now)
	}
	if err != nil {
		log.Printf("claim terminal workspace cleanup %s: %v", run.ID, err)
		return
	}
	if !claimed {
		return
	}
	reset := func(cleanupErr error) {
		log.Printf("cleanup terminal workspace %s: %v", run.ID, cleanupErr)
		if resetErr := e.repo.ResetRunWorkspaceCleanup(run.ID); resetErr != nil {
			log.Printf("reset terminal workspace cleanup %s: %v", run.ID, resetErr)
		}
	}
	if e.foreignWorkspaceBind(run) {
		if !checkpointClaimed {
			reset(fmt.Errorf("workspace belongs to %s:%s", run.WorkspaceHost, run.WorkspaceRoot))
			return
		}
		meta, _ := e.repo.WorkspaceCheckpoint(run.ID)
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationReclaim, CheckpointLease: meta.LeaseToken})
		if err != nil {
			reset(err)
		} else if response.Error != "" {
			reset(fmt.Errorf("%s", response.Error))
		}
		return
	}
	if err := e.releaseRunEnvironment(run.ID, true); err != nil {
		reset(err)
		return
	}
	if runnerID != "" {
		if err := e.queueRunnerRunCleanup(run); err != nil {
			reset(err)
			return
		}
	}
	log.Printf("cleaned terminal workspace %s", run.ID)
}

func (e *Engine) liveSandboxRunIDs() []string {
	ids := e.repo.LiveSandboxRunIDs()
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	// A not-yet-reclaimed managed workspace, including one with a failed save,
	// stays protected from the lower-level orphan directory/container sweeps.
	for _, session := range e.repo.ListSharedAssistantSessions() {
		if seen[session.RunID] {
			continue
		}
		if run, ok := e.repo.RunState(session.RunID); ok && run.WorkspaceCleanedAt.IsZero() {
			ids = append(ids, run.ID)
			seen[run.ID] = true
		}
	}
	for _, id := range e.repo.CheckpointWorkspaceRunIDs() {
		if !seen[id] {
			if run, ok := e.repo.RunState(id); ok && run.WorkspaceCleanedAt.IsZero() {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}

// SupersedeIssueRuns releases the sandbox containers of an issue's earlier
// settled runs once a new dispatch has replaced them.
//
// Re-dispatching from scratch is not a retry: it allocates a new run ID and a
// new container, and never touches the old one. The old run stays failed, and
// failed runs deliberately keep their workspace so they can be retried — which
// also kept their container alive indefinitely, each holding a writable layer
// that had grown to gigabytes. The workspace is left in place so the previous
// attempt can still be inspected; only the container is reclaimable.
func (e *Engine) SupersedeIssueRuns(issueID, keepRunID string) {
	if !e.executes() {
		return
	}
	kept, keptFound := e.repo.RunState(keepRunID)
	for _, runID := range e.repo.SupersededIssueRunIDs(issueID, keepRunID) {
		run, ok := e.repo.RunState(runID)
		if !ok || e.foreignWorkspaceBind(run) {
			continue
		}
		// Another SOP of the same issue does not replace this run; it may still be retried.
		if keptFound && run.WorkflowID != kept.WorkflowID {
			continue
		}
		if err := e.releaseRunEnvironment(runID, false); err != nil {
			log.Printf("supersede run sandbox %s: %v", runID, err)
		}
		e.sandboxes.DiscardRunContainer(context.Background(), runID)
	}
}

func (e *Engine) destroySandbox(runID string) {
	if err := e.releaseRunEnvironment(runID, true); err != nil {
		log.Printf("destroy sandbox %s: %v", runID, err)
	}
}

// DeleteRun removes a settled run only after its local execution environment
// has been destroyed and any runner-owned workspace cleanup has been durably
// queued. Failed runs normally keep their workspace for retry; an explicit
// delete must therefore clean both hosts.
func (e *Engine) DeleteRun(runID string) error {
	run, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if run.Status == "running" || run.Status == "waiting" || run.Status == "queued" {
		return fmt.Errorf("run %s is %s: cancel it before deleting", runID, run.Status)
	}
	token, err := e.beginWorkspaceUseAfterCleanup(context.Background(), runID)
	if err != nil {
		return err
	}
	defer func() { _ = e.repo.FinishWorkspaceUse(runID, token, time.Now().UTC(), nil, "") }()
	if e.WorkspaceActive != nil && e.WorkspaceActive(run) || e.conversationGenerationRunning(run) {
		return fmt.Errorf("工作区仍在使用，请先停止")
	}
	if err := e.repo.ClaimWorkspaceDeletion(runID, token, run.Revision, time.Now().UTC()); err != nil {
		return err
	}
	// Preserve the terminal outcome on the durable Task before removing the
	// Run. Otherwise a later task sync can only see a dangling RunID and has no
	// way to distinguish a deleted success/failure from a deleted cancellation.
	if err := e.settleDeletedRunTasks(run); err != nil {
		return err
	}
	if e.preview != nil {
		if err := e.preview.DeleteForRun(runID); err != nil {
			return fmt.Errorf("delete run preview: %w", err)
		}
	}
	if err := e.queueRunnerRunCleanup(run); err != nil {
		return fmt.Errorf("queue runner workspace cleanup: %w", err)
	}
	if err := e.releaseRunEnvironment(runID, true); err != nil {
		return fmt.Errorf("destroy run sandbox: %w", err)
	}
	return e.repo.DeleteRun(runID)
}

func (e *Engine) settleDeletedRunTasks(run domain.Run) error {
	status := map[string]string{"succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}[run.Status]
	if status == "" {
		return nil
	}
	contextTaskID := strings.TrimSpace(run.ExecutionContext.TaskID)
	for _, task := range e.repo.Tasks() {
		if task.RunID != run.ID && (contextTaskID == "" || task.ID != contextTaskID) {
			continue
		}
		if task.Status == status {
			continue
		}
		task.Status = status
		if err := e.repo.PutTask(task); err != nil {
			return fmt.Errorf("settle task %s before deleting run %s: %w", task.ID, run.ID, err)
		}
	}
	return nil
}

func (e *Engine) reclaimSandboxes() {
	live := e.liveSandboxRunIDs()
	n, err := e.sandboxes.ReclaimExcept(live)
	if err != nil {
		log.Printf("reclaim sandboxes: %v", err)
	} else if n > 0 {
		log.Printf("reclaimed %d finished sandbox workspace(s)", n)
	}
	// Containers outlive the directory sweep above: a run whose workspace was
	// already reclaimed, or one deleted outright, still has a container holding
	// gigabytes of writable layer that only an explicit docker rm can release.
	containers, err := e.sandboxes.ReclaimDockerSandboxes(context.Background(), live)
	if err != nil {
		log.Printf("reclaim docker sandboxes: %v", err)
		return
	}
	if containers > 0 {
		log.Printf("reclaimed %d finished sandbox container(s)", containers)
	}
	if idle := e.discardIdleRunContainers(context.Background(), time.Now().UTC()); idle > 0 {
		log.Printf("discarded %d idle failed/waiting sandbox container(s)", idle)
	}
	images, err := e.sandboxes.ReclaimCachedDockerImages(context.Background(), append(e.repo.Dockerfiles(), e.repo.LiveSandboxDockerfiles()...))
	if err != nil {
		log.Printf("reclaim cached docker images: %v", err)
		return
	}
	if images > 0 {
		log.Printf("reclaimed %d unused docker image cache tag(s)", images)
	}
}

// Failed and waiting runs keep their workspace for a retry or a human
// decision, so the live set never releases them. Their containers only sit on
// `sleep infinity` holding memory and a writable layer; after these idle
// periods the container goes and the workspace stays. The next retry, approval
// or resume acquires the environment again, which recreates the container and
// reruns environment installation.
const (
	idleFailedRunContainerTTL  = 30 * time.Minute
	idleWaitingRunContainerTTL = 2 * time.Hour
)

func (e *Engine) discardIdleRunContainers(ctx context.Context, now time.Time) int {
	if !e.executes() || e.sandboxes == nil {
		return 0
	}
	candidates := e.repo.IdleSandboxRunIDs(now.Add(-idleFailedRunContainerTTL), now.Add(-idleWaitingRunContainerTTL))
	if len(candidates) == 0 {
		return 0
	}
	discarded := map[string]struct{}{}
	// An in-process handle holds the workspace flock, which the docker scan
	// below treats as busy, so those runs are released first.
	for _, runID := range candidates {
		e.runEnvMu.Lock()
		_, held := e.runEnvs[runID]
		e.runEnvMu.Unlock()
		if held && e.discardIdleRunContainer(ctx, runID, now) {
			discarded[runID] = struct{}{}
		}
	}
	withContainer, err := e.sandboxes.RunsWithIdleDockerSandbox(ctx, candidates)
	if err != nil {
		log.Printf("list idle run sandboxes: %v", err)
	}
	for _, runID := range withContainer {
		if _, done := discarded[runID]; done {
			continue
		}
		if e.discardIdleRunContainer(ctx, runID, now) {
			discarded[runID] = struct{}{}
		}
	}
	return len(discarded)
}

// discardIdleRunContainer re-reads the run under stateMu, the lock Retry and
// Approve take before they flip a run back to running.
func (e *Engine) discardIdleRunContainer(ctx context.Context, runID string, now time.Time) bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	run, ok := e.repo.RunState(runID)
	if meta, managed := e.repo.WorkspaceCheckpoint(runID); managed && (meta.Error != "" || meta.LeaseUntil != nil && meta.LeaseUntil.After(now) || meta.FollowUp && now.Before(meta.LastUsedAt.Add(ConversationWorkspaceIdleTTL))) {
		return false
	}
	if e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		return false
	}
	if !ok || !runContainerIdle(run, now) || e.foreignWorkspaceBind(run) || e.defaultExecutionRunner(run) != "" {
		return false
	}
	if err := e.releaseRunEnvironment(runID, false); err != nil {
		log.Printf("release idle run sandbox %s: %v", runID, err)
	}
	e.sandboxes.DiscardRunContainer(ctx, runID)
	return true
}

func runContainerIdle(run domain.Run, now time.Time) bool {
	if !run.WorkspaceCleanedAt.IsZero() {
		return false
	}
	switch run.Status {
	case "failed":
		if run.UpdatedAt.After(now.Add(-idleFailedRunContainerTTL)) {
			return false
		}
	case "waiting":
		if run.UpdatedAt.After(now.Add(-idleWaitingRunContainerTTL)) {
			return false
		}
	default:
		return false
	}
	for _, node := range run.Nodes {
		if node != nil && (node.Status == "running" || node.Status == "pending_input") {
			return false
		}
	}
	return true
}

// settleOrphan marks an unowned run and its in-flight nodes as failed.
// updateRun already serialises on stateMu and publishes the new snapshot.
func (e *Engine) settleOrphan(runID, reason string) {
	e.stopRunScheduler(runID)
	status := orphanSettleStatus(reason)
	e.updateRun(runID, func(r *domain.Run) {
		now := time.Now().UTC()
		for _, nr := range r.Nodes {
			if nr.Status == "running" || nr.Status == "pending_input" || (status == "cancelled" && nr.Status == "waiting_approval") {
				if status == "cancelled" {
					nr.Status, nr.Error, nr.FinishedAt = "cancelled", "", now
					nr.Activity = "已由用户取消"
				} else {
					nr.Status, nr.Error, nr.FinishedAt = "failed", reason, now
				}
			}
		}
		r.Status = status
		e.event(r, "", "run."+status, reason)
	})
	if status == "cancelled" {
		e.cleanupRunWorkspace(runID)
	}
}

func isLiveStatus(status string) bool {
	return status == "running" || status == "waiting"
}
