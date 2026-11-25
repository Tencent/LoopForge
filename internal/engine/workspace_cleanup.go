package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func (e *Engine) RunWorkspace(runID string) string {
	if e.sandboxes == nil {
		return ""
	}
	if run, ok := e.repo.RunState(runID); ok {
		return e.sandboxes.RunWorkspace(workspaceRunID(run))
	}
	return e.sandboxes.RunWorkspace(runID)
}

// WorkspaceRunID returns the run whose workspace directory run works in; a
// child SOP shares the checkout of the run that started its SOP chain.
func WorkspaceRunID(run domain.Run) string {
	return workspaceRunID(run)
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
			e.checkpointTransfers.Close()
			return
		case <-ticker.C:
			e.checkpointTransfers.Sweep(time.Now())
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
	visit := func(run domain.Run) {
		if seen[run.ID] {
			return
		}
		seen[run.ID] = true
		if run.WorkspaceCleanedAt.IsZero() {
			var err error
			run, err = e.repo.RunWithNodes(run.ID)
			if err != nil || run.ID == "" {
				return
			}
		}
		e.cleanupWorkspace(run, now)
	}
	for _, run := range e.repo.TerminalWorkspaceCleanupRuns() {
		visit(run)
	}
	for _, id := range e.repo.CheckpointWorkspaceRunIDs() {
		visit(domain.Run{ID: id})
	}
	for _, run := range e.repo.IdleReclaimableSOPRuns(now.Add(-ConversationWorkspaceIdleTTL)) {
		visit(run)
	}
	for _, run := range e.repo.CleanedSOPRunsHoldingAdmission() {
		visit(run)
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
	if run.Status == "running" || run.Status == "queued" {
		return
	}
	runnerID := e.defaultExecutionRunner(run)
	if !run.WorkspaceCleanedAt.IsZero() {
		e.reconcileClaimedWorkspaceCleanup(run, runnerID, now)
		// A checkpoint claim marks the workspace before its files are removed.
		// Only its lease holder may release the slot while cleanup is in flight.
		// A claim reset above leaves the row uncleaned, so release is a no-op.
		if meta, ok := e.repo.WorkspaceCheckpoint(run.ID); ok && (meta.LeaseUntil != nil && meta.LeaseUntil.After(now) ||
			// A local deletion with an unknown outcome may still leave the
			// checkout on disk. Keep the slot until the recheck confirms it.
			runnerID == "" && meta.CleanupRetryAt != nil) {
			return
		}
		e.releaseCleanedRunAdmission(run, "")
		return
	}
	if borrowsConversationWorkspace(run) && sharesWorkspace(run) && idleSOPStatus(run) {
		if run.AdmissionState == "released" || run.AdmissionState == "unstarted" ||
			now.Before(run.UpdatedAt.Add(ConversationWorkspaceIdleTTL)) || now.Before(run.WorkspaceCleanupNotBefore) || !e.sopWorkspaceTreeIdle(run) {
			return
		}
		released, err := e.repo.ReleaseIdleBorrowedSOPAdmission(run.ID, run.Revision, now)
		if err != nil {
			log.Printf("release borrowed SOP admission %s: %v", run.ID, err)
		} else if released {
			e.publish(run.ID)
			e.wakeRunClaimLoop()
		}
		return
	}
	managed, checkpointLease, releaseCheckpoint, checkpointErr := e.claimIdleConversationWorkspace(run, now)
	if releaseCheckpoint != nil {
		defer releaseCheckpoint()
	}
	if checkpointErr != nil {
		log.Printf("preserve workspace %s: %v", run.ID, checkpointErr)
		return
	}
	checkpointClaimed := checkpointLease != ""
	if managed && !checkpointClaimed {
		return
	}
	claimed, err := checkpointClaimed, error(nil)
	if !managed {
		claimed, err = e.repo.ClaimRunWorkspaceCleanup(run.ID, now)
	}
	if err != nil {
		log.Printf("claim workspace cleanup %s: %v", run.ID, err)
		return
	}
	if !claimed {
		return
	}
	confirmReclaimed := func() {
		if err := e.repo.ConfirmCheckpointWorkspaceReclaimed(run.ID, checkpointLease, time.Now().UTC()); err != nil {
			log.Printf("record reclaimed workspace %s: %v", run.ID, err)
		}
	}
	reset := func(cleanupErr error) {
		log.Printf("cleanup workspace %s: %v", run.ID, cleanupErr)
		var resetErr error
		if checkpointClaimed {
			resetErr = e.repo.ResetCheckpointWorkspaceCleanup(run.ID, checkpointLease, time.Now().UTC())
		} else {
			resetErr = e.repo.ResetRunWorkspaceCleanup(run.ID)
		}
		if resetErr != nil {
			log.Printf("reset workspace cleanup %s: %v", run.ID, resetErr)
		}
	}
	if runnerID == "" && !sharesWorkspace(run) && e.foreignWorkspaceBind(run) {
		if !checkpointClaimed {
			reset(fmt.Errorf("workspace belongs to %s:%s", run.WorkspaceHost, run.WorkspaceRoot))
			return
		}
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationReclaim, CheckpointLease: checkpointLease})
		if err == nil {
			err = workspaceCheckpointResponseError(response)
		}
		// A transport error can follow a successful deletion. A success reply
		// can also come from a Worker that skipped a locked directory.
		probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		probe, probeErr := e.probeRemoteCheckpointRoot(probeCtx, run)
		switch {
		case probeErr == nil && probe.Code == domain.WorkspaceErrorMissing:
			confirmReclaimed()
		case probeErr == nil && probe.Status == http.StatusOK && probe.Error == "":
			if err == nil {
				err = errors.New("workspace directory survived reclamation")
			}
			reset(err)
			return
		default:
			// The next sweep resolves the claim once the outcome is observable.
			log.Printf("cleanup outcome for workspace %s is unknown: %v", run.ID, errors.Join(err, probeErr, workspaceCheckpointResponseError(probe)))
			return
		}
	} else {
		if checkpointClaimed {
			err = e.reclaimCheckpointWorkspace(run.ID, checkpointLease)
		} else {
			err = e.reclaimLocalRunWorkspace(run.ID)
		}
		if err != nil {
			if checkpointClaimed {
				log.Printf("cleanup outcome for workspace %s is unknown: %v", run.ID, err)
			} else {
				reset(err)
			}
			return
		}
		if runnerID != "" {
			if err := e.queueRunnerRunCleanup(run); err != nil {
				reset(err)
				return
			}
		} else if checkpointClaimed {
			if root, reason := e.LocalRunWorkspace(run); reason == "" {
				status := runtime.WorkspaceCheckpointStatus(root)
				switch {
				case status.Code == domain.WorkspaceErrorMissing:
					confirmReclaimed()
				case status.Status == http.StatusOK:
					reset(errors.New("workspace directory survived reclamation"))
					return
				}
			}
		}
	}
	message := ""
	if idleSOPStatus(run) {
		message = "工作区已空闲超过 1 小时，已归档回收并释放运行名额"
	}
	e.releaseCleanedRunAdmission(run, message)
	log.Printf("cleaned workspace %s", run.ID)
}

func (e *Engine) reclaimLocalRunWorkspace(runID string) error {
	if err := e.releaseRunEnvironment(runID, true); err != nil {
		return err
	}
	if e.sandboxes == nil {
		return nil
	}
	// The sandbox manager skips directories still locked by another process.
	// A skipped directory has not finished reclamation yet.
	if _, err := os.Lstat(e.sandboxes.RunWorkspace(runID)); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return repo.ErrWorkspaceInUse
	}
	return nil
}

func (e *Engine) releaseCleanedRunAdmission(run domain.Run, message string) {
	if run.IsWorkspace() || run.WorkflowID == domain.FreeCollaborationWorkflowID || run.ExecutionContext.ParentRunID != "" ||
		run.AdmissionState == "released" || run.AdmissionState == "unstarted" {
		return
	}
	released, err := e.repo.ReleaseCleanedRunAdmission(run.ID, message)
	if err != nil {
		log.Printf("release cleaned run admission %s: %v", run.ID, err)
	} else if released {
		e.publish(run.ID)
		e.wakeRunClaimLoop()
	}
}

func (e *Engine) reconcileClaimedWorkspaceCleanup(run domain.Run, runnerID string, now time.Time) {
	if runnerID != "" {
		task, exists := e.repo.RunnerTask("rt-cleanup-" + safeRunnerPath(run.ID))
		if !exists {
			// A removed Runner cleans its workspace as part of deregistration.
			if _, runnerExists := e.repo.Runner(runnerID); runnerExists {
				e.resetInterruptedRunnerCleanup(run, now)
				return
			}
		} else if task.Status == domain.RunnerTaskFailed || task.Status == domain.RunnerTaskCanceled {
			e.resetInterruptedRunnerCleanup(run, now)
			return
		} else if task.Status != domain.RunnerTaskSuccess {
			return
		}
	}
	checkpoint, ok := e.repo.WorkspaceCheckpoint(run.ID)
	if !ok || checkpoint.CleanupRetryAt == nil {
		e.reclaimCleanedWorkspaceLeftover(run)
		return
	}
	if checkpoint.CleanupDeferred(now) || checkpoint.LeaseUntil != nil && !now.After(*checkpoint.LeaseUntil) {
		return
	}

	observed := repo.WorkspaceCleanupAbsent
	if runnerID == "" {
		observed = repo.WorkspaceCleanupUnknown
		var status domain.RunnerWorkspaceResponse
		var err error
		if e.foreignWorkspaceBind(run) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			status, err = e.probeRemoteCheckpointRoot(ctx, run)
		} else if root, reason := e.LocalRunWorkspace(run); reason == "" {
			status = e.workspaceCheckpointRootStatus(run, root, false)
		}
		switch {
		case err == nil && status.Code == domain.WorkspaceErrorMissing:
			observed = repo.WorkspaceCleanupAbsent
		case err == nil && status.Status == http.StatusOK && status.Error == "":
			observed = repo.WorkspaceCleanupPresent
		}
	}
	if err := e.repo.ResolveCheckpointWorkspaceCleanup(run, *checkpoint.CleanupRetryAt, now, observed); err != nil {
		log.Printf("recheck workspace cleanup %s: %v", run.ID, err)
	}
}

// reclaimCleanedWorkspaceLeftover removes a checkout that survived a committed
// cleanup claim, e.g. when the process stopped before DestroyRun. The orphan
// sweep keeps registered runs, so this is the only path that frees it.
//
// A run that was rebound to another host or root also leaves its old checkout
// here. Sandbox roots are host-local (rebindWorkspaceHere discards the same
// kind of leftover), so that copy is reclaimed under the same checks.
func (e *Engine) reclaimCleanedWorkspaceLeftover(expected domain.Run) {
	root := e.sandboxes.RunWorkspace(expected.ID)
	if root == "" {
		return
	}
	if _, err := os.Lstat(root); err != nil {
		return
	}
	// A restore holds this lock until its binding is published.
	release, err := e.tryLockCheckpointWorkspace(expected.ID)
	if err != nil {
		return
	}
	defer release()
	run, ok := e.repo.RunState(expected.ID)
	if !ok || run.WorkspaceCleanedAt.IsZero() || !run.WorkspaceCleanedAt.Equal(expected.WorkspaceCleanedAt) ||
		run.Revision != expected.Revision || run.WorkspaceHost != expected.WorkspaceHost || run.WorkspaceRoot != expected.WorkspaceRoot ||
		run.Status == "running" || run.Status == "queued" || run.HasActiveNode() || e.workspaceTerminals.HasRunScope(run.ID) ||
		e.WorkspaceActive != nil && e.WorkspaceActive(run) || e.conversationGenerationRunning(run) {
		return
	}
	// SOP runs dispatched from a conversation borrow its checkout.
	if run.IsWorkspace() {
		if busy, err := e.repo.HasWorkspaceSOPRuns(run.ID, "running", "queued", "waiting"); err != nil || busy {
			return
		}
	}
	if checkpoint, ok := e.repo.WorkspaceCheckpoint(run.ID); ok &&
		(checkpoint.CleanupRetryAt != nil || checkpoint.LeaseUntil != nil && !checkpoint.LeaseUntil.Before(time.Now())) {
		return
	}
	if err := e.releaseRunEnvironment(run.ID, true); err != nil {
		log.Printf("reclaim leftover workspace %s: %v", run.ID, err)
		return
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		log.Printf("reclaimed leftover workspace of cleaned run %s", run.ID)
	}
}

func (e *Engine) resetInterruptedRunnerCleanup(run domain.Run, now time.Time) {
	if err := e.repo.ResetInterruptedRunnerCleanup(run, now); err != nil {
		log.Printf("reset runner workspace cleanup %s: %v", run.ID, err)
	}
}

func (e *Engine) tryLockCheckpointWorkspace(runID string) (func(), error) {
	release, err := e.sandboxes.TryLockWorkspaceCheckpoint(runID)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, repo.ErrWorkspaceInUse
	}
	return release, nil
}

func (e *Engine) reclaimCheckpointWorkspace(runID, token string) error {
	release, err := e.tryLockCheckpointWorkspace(runID)
	if err != nil {
		return err
	}
	defer release()
	// Validate the claim after taking the filesystem lock. A delayed RPC
	// might arrive after another instance has restored or rebound this run.
	run, exists := e.repo.RunState(runID)
	meta, saved := e.repo.WorkspaceCheckpoint(runID)
	if !exists || !saved || meta.SavedAt == nil || meta.Error != "" || run.WorkspaceCleanedAt.IsZero() ||
		token == "" || meta.LeaseToken != token || meta.LeaseUntil == nil || !meta.LeaseUntil.After(time.Now()) ||
		e.defaultExecutionRunner(run) == "" && e.foreignWorkspaceBind(run) || !e.conversationWorkspaceIdle(run) {
		return repo.ErrWorkspaceInUse
	}
	// A Runner owns its checkout; only the local staging copy is removed here
	// and the queued Runner cleanup task removes the rest.
	return e.releaseRunEnvironment(runID, true)
}

// workspaceCheckpointRootStatus probes the checkout root under the checkpoint
// lock. An admitted run restoring before its first node may probe while busy.
func (e *Engine) workspaceCheckpointRootStatus(run domain.Run, root string, restoreDuringExecution bool) domain.RunnerWorkspaceResponse {
	if !restoreDuringExecution && !e.conversationWorkspaceIdle(run) {
		return checkpointWorkspaceLockError(repo.ErrWorkspaceInUse)
	}
	release, err := e.tryLockCheckpointWorkspace(run.ID)
	if err != nil {
		return checkpointWorkspaceLockError(err)
	}
	defer release()
	return runtime.WorkspaceCheckpointStatus(root)
}

func checkpointWorkspaceLockError(err error) domain.RunnerWorkspaceResponse {
	response := domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: err.Error()}
	if errors.Is(err, repo.ErrWorkspaceInUse) {
		response.Code = domain.WorkspaceErrorBusy
	}
	return response
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
	if run.HasActiveNode() {
		return fmt.Errorf("运行 %s 仍有未结束的节点，请先停止后再删除", runID)
	}
	if run.IsWorkspace() {
		inUse, err := e.repo.HasWorkspaceSOPRuns(run.ID)
		if err != nil {
			return err
		}
		if inUse {
			return fmt.Errorf("工作区仍有关联的 SOP 运行：%w", repo.ErrWorkspaceInUse)
		}
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
	if err := e.repo.DeleteRun(runID); err != nil {
		return err
	}
	if run.AdmissionState == "held" && run.ExecutionContext.ParentRunID == "" && !run.IsWorkspace() {
		e.PromoteQueuedRunsAsync()
	}
	return nil
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
		if err := e.repo.UpdateTaskStatus(task, status); err != nil {
			return fmt.Errorf("settle task %s before deleting run %s: %w", task.ID, run.ID, err)
		}
	}
	return nil
}

func (e *Engine) reclaimSandboxes() {
	n, err := e.sandboxes.ReclaimExcept(e.repo.SandboxSweepKeepRunIDs)
	if err != nil {
		log.Printf("reclaim sandboxes: %v", err)
	} else if n > 0 {
		log.Printf("reclaimed %d orphaned sandbox workspace(s)", n)
	}
	// Containers can retain a writable layer after their workspace is gone.
	containers, err := e.sandboxes.ReclaimDockerSandboxes(context.Background(), func() ([]string, error) {
		return e.repo.DockerSandboxSweepKeepRunIDs(time.Now().UTC())
	})
	if err != nil {
		log.Printf("reclaim docker sandboxes: %v", err)
		return
	}
	if containers > 0 {
		log.Printf("reclaimed %d orphaned sandbox container(s)", containers)
	}
	if idle := e.discardIdleRunContainers(context.Background(), time.Now().UTC()); idle > 0 {
		log.Printf("discarded %d idle failed/waiting sandbox container(s)", idle)
	}
	keep := e.repo.LiveSandboxDockerfiles()
	for _, profile := range e.repo.Sandboxes() {
		if spec := domain.RunnerSandbox(profile); spec.Dockerfile != nil {
			keep = append(keep, *spec.Dockerfile)
		}
	}
	images, err := e.sandboxes.ReclaimCachedDockerImages(context.Background(), keep)
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
// discardLocalSandbox removes this host's sandbox container after the run
// moves to a pool runner. The workspace stays for inspection.
func (e *Engine) discardLocalSandbox(runID string) {
	if err := e.releaseRunEnvironment(runID, false); err != nil {
		log.Printf("release local sandbox %s after pool bind: %v", runID, err)
	}
	if e.sandboxes != nil {
		e.sandboxes.DiscardRunContainer(context.Background(), runID)
	}
}

func (e *Engine) discardIdleRunContainer(ctx context.Context, runID string, now time.Time) bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	run, ok := e.repo.RunState(runID)
	// Keep preview's local-container reclamation for runs moved to a runner,
	// while preserving active workspace operations and the new idle deadline.
	if meta, managed := e.repo.WorkspaceCheckpoint(runID); managed && (meta.Error != "" || meta.LeaseUntil != nil && meta.LeaseUntil.After(now) || meta.FollowUp && now.Before(meta.LastUsedAt.Add(ConversationWorkspaceIdleTTL))) {
		return false
	}
	if e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		return false
	}
	if !ok || !runContainerIdle(run, now) || e.foreignWorkspaceBind(run) {
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
