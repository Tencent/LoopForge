package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const ConversationWorkspaceIdleTTL = time.Hour

type conversationWorkspaceTokenKey struct{}

type conversationWorkspaceLease struct {
	token string
	runID string
}

// WithConversationWorkspaceToken carries an already acquired workspace lease.
// Supplying the run ID limits reuse to that run, including during child recovery.
// The optional scope preserves legacy callers that only inspect the raw token;
// execution restoration requires an explicitly scoped lease.
func WithConversationWorkspaceToken(ctx context.Context, token string, runID ...string) context.Context {
	lease := conversationWorkspaceLease{token: token}
	if len(runID) > 0 {
		lease.runID = runID[0]
	}
	return context.WithValue(ctx, conversationWorkspaceTokenKey{}, lease)
}
func ConversationWorkspaceToken(ctx context.Context, runID ...string) string {
	lease, _ := ctx.Value(conversationWorkspaceTokenKey{}).(conversationWorkspaceLease)
	if len(runID) > 0 && (lease.runID == "" || lease.runID != runID[0]) {
		return ""
	}
	return lease.token
}
func (e *Engine) BeginConversationWorkspaceUse(id string) (string, error) {
	token, previous, err := e.repo.BeginWorkspaceUsePreemptingSave(id, time.Now().UTC())
	if err != nil || previous == "" {
		return token, err
	}
	if value, ok := e.conversationSaves.Load(previous); ok {
		value.(context.CancelFunc)()
	}
	// Another API instance may own the save. Its lease watcher cancels the
	// export; wait only for the runner's read-only gate probe, never its archive.
	if err = e.waitForBackgroundWorkspaceSave(id); err != nil {
		_ = e.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, "")
		return "", err
	}
	return token, nil
}
func (e *Engine) RenewConversationWorkspaceUse(id, token string) error {
	return e.repo.RenewWorkspaceUse(id, token, time.Now().UTC())
}

// ReleaseConversationWorkspace hands a workspace a conversation switched away
// from to the idle sweep, which saves and reclaims it once nothing runs there.
func (e *Engine) ReleaseConversationWorkspace(id string) error {
	return e.repo.ReleaseWorkspace(id, time.Now().UTC())
}
func (e *Engine) ConversationWorkspaceStatus(id string) (repo.WorkspaceCheckpoint, bool) {
	return e.repo.WorkspaceCheckpoint(id)
}
func (e *Engine) conversationWorkspaceIdle(run domain.Run) bool {
	if sharesWorkspace(run) || e.workspaceTerminals.HasRunScope(run.ID) {
		return false
	}
	if e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		return false
	}
	if run.Status == "running" || run.Status == "queued" || run.HasActiveNode() {
		return false
	}
	if run.Status == "waiting" && !run.IsWorkspace() {
		return false
	}
	tasks, err := e.repo.ActiveRunnerTasksForRun(run.ID)
	return err == nil && len(tasks) == 0
}
func (e *Engine) conversationGenerationRunning(run domain.Run) bool {
	session, ok := e.repo.SharedAssistantSession(repo.RunAssistantSessionID(run.ID))
	if !ok {
		return false
	}
	job, ok := e.repo.PeekAssistantGeneration(session.UserID, session.ID)
	return ok && job.Status == "running"
}
func (e *Engine) EndConversationWorkspaceUse(ctx context.Context, id, token string) error {
	now := time.Now().UTC()
	err := e.saveConversationWorkspaceAfterUse(ctx, id, token)
	message := ""
	if err != nil {
		message = err.Error()
	}
	if finishErr := e.repo.FinishWorkspaceUse(id, token, now, nil, message); finishErr != nil {
		return finishErr
	}
	return err
}
func (e *Engine) EnsureConversationWorkspace(ctx context.Context, id, token string) error {
	_, err := e.prepareConversationWorkspace(ctx, id, token, true)
	return err
}

// MaterializeConversationWorkspace checks out an idle conversation workspace that
// has never had a workspace, so Main Bot can read and edit it before any
// command ran. A workspace that was saved or reclaimed is restored by
// PrepareConversationWorkspace instead.
func (e *Engine) MaterializeConversationWorkspace(ctx context.Context, id string) error {
	run, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	if !run.IsWorkspace() || sharesWorkspace(run) || !run.WorkspaceCleanedAt.IsZero() || e.foreignWorkspaceBind(run) {
		return nil
	}
	if meta, exists := e.repo.WorkspaceCheckpoint(id); exists && meta.SavedAt != nil {
		return nil
	}
	if e.defaultExecutionRunner(run) != "" {
		return e.materializeRunnerConversationWorkspace(ctx, run)
	}
	root, reason := e.LocalRunWorkspace(run)
	if reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if _, err := os.Lstat(root); err == nil {
		// Retry a checkout that failed earlier (e.g. before the Git Token was
		// configured); never touch a repository that is already checked out.
		missing := false
		for _, dir := range repositoryWorkspaceDirs(root, run.RepositoryContexts()) {
			if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
				missing = true
			}
		}
		if !missing {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	env, err := e.acquireConversationEnvironment(ctx, run)
	if err != nil {
		return err
	}
	env.nodeMu.Lock()
	defer env.nodeMu.Unlock()
	// Same checkout as an SOP node: the responsible user's Git Token, the
	// frozen base branch, and a fresh work branch.
	credentials := env.credentialSnapshot()
	ctx = e.withPreparationProgress(ctx, run.ID)
	ctx = withRunGitCredentials(ctx, credentials)
	ctx = gitwork.WithSSHKeys(e.withSSHKnownHosts(ctx), credentials.GitSSHKeys)
	ctx = runtime.WithRepositoryCacheIn(ctx, env.Unwrap())
	return env.prepareRepositoriesOnce(func() error {
		if err := prepareRepositories(ctx, env.Workspace(), run.RepositoryContexts()); err != nil {
			return explainGitAuthFailure(fmt.Errorf("repository checkout: %w", err), len(credentials.GitHTTPCredentials) > 0)
		}
		return nil
	})
}

// runnerMaterializeBusyWait bounds how long opening a workspace waits for a
// concurrent preparation or snapshot export on the runner.
var runnerMaterializeBusyWait = 30 * time.Second

// A runner only checks repositories out for a prepare request, so a task that
// never ran a command has no directory there yet. Refreshing is disabled: this
// creates missing checkouts and never replaces one that may already be in use.
func (e *Engine) materializeRunnerConversationWorkspace(ctx context.Context, run domain.Run) error {
	request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationPrepare})
	if err != nil {
		return err
	}
	request.RefreshCleanRepositories = false
	deadline := time.Now().Add(runnerMaterializeBusyWait)
	for {
		response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
		if err != nil {
			return err
		}
		if response.Error == "" {
			return nil
		}
		if !runnerWorkspaceBusy(response) || time.Now().After(deadline) {
			return explainGitAuthFailure(fmt.Errorf("%s", response.Error), len(e.resolveRunCredentials(run).GitHTTPCredentials) > 0)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// PrepareConversationWorkspace also repairs a legacy checkout that predates
// snapshots. A real snapshot stays authoritative, including intentional deletes.
func (e *Engine) PrepareConversationWorkspace(ctx context.Context, id, token string) ([]string, error) {
	return e.prepareConversationWorkspace(ctx, id, token, false)
}

func (e *Engine) prepareConversationWorkspace(ctx context.Context, id, token string, requireLocal bool) ([]string, error) {
	if err := e.ensureConversationWorkspace(ctx, id, token); err != nil && (requireLocal || !errors.Is(err, ErrConversationWorkspaceBound)) {
		return nil, err
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt != nil {
		return nil, nil
	}
	run, ok := e.repo.RunState(id)
	if !ok {
		return nil, fmt.Errorf("运行不存在")
	}
	// Readers stay available during execution; never inject prior outputs while
	// a node is currently expected to generate its own fresh deliverables.
	if run.HasActiveNode() || !run.IsWorkspace() && (run.Status == "running" || run.Status == "queued") {
		return nil, nil
	}
	return e.restoreArchivedWorkspaceFiles(ctx, run)
}

func (e *Engine) ensureConversationWorkspace(ctx context.Context, id, token string) error {
	if run, ok := e.repo.RunState(id); ok && sharesWorkspace(run) {
		return fmt.Errorf("子 SOP 共享父任务工作区，请从父任务操作")
	}
	meta, ok := e.repo.WorkspaceCheckpoint(id)
	if !ok || meta.LeaseToken != token || meta.LeaseUntil == nil || meta.LeaseUntil.Before(time.Now()) {
		return repo.ErrWorkspaceInUse
	}
	if err := e.repo.MarkWorkspaceFollowUp(id, token, time.Now().UTC()); err != nil {
		return err
	}
	run, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	runner := e.defaultExecutionRunner(run)
	if runner == "" && e.foreignWorkspaceBind(run) {
		if !run.WorkspaceCleanedAt.IsZero() {
			if err := e.restoreOrReuseRemoteConversationCheckpoint(ctx, run); err != nil {
				return err
			}
			return e.repo.RestoreCheckpointWorkspaceBinding(id, token, "", "", time.Now().UTC())
		}
		// An uncleaned checkout still belongs to a live instance. Returning
		// success here used to let the chat start, then fail in
		// LocalRunWorkspace with a generic generation error.
		if e.workspaceBlocked(run) {
			return ErrConversationWorkspaceBound
		}
		return e.rebindOfflineConversationWorkspace(ctx, id, token, run)
	}
	if runner == "" && run.WorkspaceCleanedAt.IsZero() {
		root, reason := e.LocalRunWorkspace(run)
		if reason != "" {
			return fmt.Errorf("%s", reason)
		}
		if info, err := os.Lstat(root); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("工作区路径不是目录")
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if runner != "" && run.WorkspaceCleanedAt.IsZero() {
		return nil
	}
	var err error
	if runner != "" {
		if task, exists := e.repo.RunnerTask("rt-cleanup-" + safeRunnerPath(id)); exists {
			if task.Status == domain.RunnerTaskQueued {
				if err = e.repo.CancelRunnerTask(task.ID, time.Now().UTC()); err != nil {
					return err
				}
				// A claim may have raced cancellation. In that case cancellation
				// only requests a stop; wait for the cleanup to actually finish.
				task, _ = e.repo.RunnerTask(task.ID)
			}
			if task.Status == domain.RunnerTaskLeased || task.Status == domain.RunnerTaskRunning {
				return fmt.Errorf("工作区尚在回收，请稍后恢复")
			}
		}
		if err = e.restoreOrReuseRemoteConversationCheckpoint(ctx, run); err != nil {
			return err
		}
		return e.repo.RestoreCheckpointWorkspaceBinding(id, token, "", "", time.Now().UTC())
	}
	release, err := e.tryLockCheckpointWorkspace(id)
	if err != nil {
		return err
	}
	defer release()
	root := e.sandboxes.RunWorkspace(id)
	if info, statErr := os.Lstat(root); statErr == nil && info.IsDir() && !e.foreignWorkspaceBind(run) {
		hasContent, contentErr := runtime.WorkspaceDirHasContent(root)
		if contentErr != nil {
			return contentErr
		}
		if hasContent {
			if err = e.verifyStoredCheckpoint(ctx, id); err != nil {
				return err
			}
			return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
		}
		// An empty or .agentflow-only directory created before restoration
		// holds no user files; tear down any container bound to its old inode
		// so restoreConversationCheckpoint can atomically replace it and the
		// next container bind sees the restored files.
		if err = e.releaseRunEnvironment(id, false); err != nil {
			return err
		}
		e.sandboxes.DiscardRunContainer(ctx, id)
	}
	if err = e.restoreConversationCheckpoint(ctx, run, root); err != nil {
		return err
	}
	return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
}

// ErrConversationWorkspaceBound means this process must not open a checkout
// that a live instance still serves.
var ErrConversationWorkspaceBound = errors.New("工作区绑定在其他 LoopForge 实例")

// ErrConversationWorkspaceLost means the bound instance is gone and neither
// the checkout nor a snapshot could be recovered. The workspace is already
// bound to the current instance, so the next turn can start.
var ErrConversationWorkspaceLost = errors.New("原工作区实例已下线，未保存的改动无法恢复。工作区已换到当前实例，请重试")

// rebindOfflineConversationWorkspace moves a conversation workspace off an
// instance that is no longer alive. A checkout still present under this
// sandbox root is kept if it has user files. Publish the new binding only
// after the recovered checkout is ready so a failed restore can be retried.
func (e *Engine) rebindOfflineConversationWorkspace(ctx context.Context, id, token string, run domain.Run) error {
	release, err := e.tryLockCheckpointWorkspace(id)
	if err != nil {
		return err
	}
	defer release()
	_, boundRoot := e.workspaceBind(run)
	local := e.sandboxes.RunWorkspace(id)
	sameRoot := boundRoot == e.sandboxes.Root()
	hasCheckout := false
	if sameRoot {
		hasCheckout, err = runtime.WorkspaceDirHasContent(local)
		if err != nil {
			return err
		}
	}
	message := "原工作区实例已下线，当前实例继续使用同一目录中的检出"
	var lost error
	if !hasCheckout {
		if err := e.releaseRunEnvironment(id, !sameRoot); err != nil {
			return err
		}
		e.sandboxes.DiscardRunContainer(ctx, id)
		meta, _ := e.repo.WorkspaceCheckpoint(id)
		if meta.SavedAt != nil && meta.SHA256 != "" {
			if err := e.restoreConversationCheckpoint(ctx, run, local); err != nil {
				return err
			}
			message = "原工作区实例已下线，已从最近一次快照恢复；快照之后的未提交改动已丢失"
		} else {
			if err := os.MkdirAll(local, 0o700); err != nil {
				return err
			}
			lost = ErrConversationWorkspaceLost
			message = lost.Error()
		}
	}
	if err := e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC()); err != nil {
		return err
	}
	e.noteConversationWorkspaceRebound(id, message)
	return lost
}

func (e *Engine) noteConversationWorkspaceRebound(id, message string) {
	e.updateRunSettling(id, func(r *domain.Run) {
		e.event(r, "", "workspace.rebound", message)
	})
}

// claimIdleConversationWorkspace returns managed=true even on save failure, so
// legacy immediate cleanup can never delete files that were not persisted.
func (e *Engine) claimIdleConversationWorkspace(run domain.Run, now time.Time) (managed bool, lease string, release func(), err error) {
	if sharesWorkspace(run) {
		return false, "", nil, nil
	}
	meta, exists := e.repo.WorkspaceCheckpoint(run.ID)
	session, shared := e.repo.SharedAssistantSession(repo.RunAssistantSessionID(run.ID))
	if !exists && !run.IsWorkspace() && run.Status != "succeeded" && run.Status != "cancelled" && (!shared || session.Archived) {
		return false, "", nil, nil
	}
	if !run.WorkspaceCleanedAt.IsZero() {
		return false, "", nil, nil
	}
	if !e.conversationWorkspaceIdle(run) || e.conversationGenerationRunning(run) {
		return true, "", nil, nil
	}
	// Recent conversation/IDE activity must not compete with maintenance for
	// the lease. Foreground completion saves changes; reclamation waits an hour.
	idleSinceBeforeLease := meta.LastUsedAt
	if run.UpdatedAt.After(idleSinceBeforeLease) {
		idleSinceBeforeLease = run.UpdatedAt
	}
	released := exists && meta.Released()
	retryDue := meta.SaveRetryAt != nil && !now.Before(*meta.SaveRetryAt)
	if exists && meta.FollowUp && !retryDue && !released && now.Before(idleSinceBeforeLease.Add(ConversationWorkspaceIdleTTL)) {
		return true, "", nil, nil
	}
	cleanupHeld := now.Before(run.WorkspaceCleanupNotBefore)
	if (cleanupHeld || meta.CleanupDeferred(now)) && !heldWorkspaceCheckpointNeedsSave(meta, idleSinceBeforeLease, now) {
		// A cleanup hold alone is not a reason to take a lease or rearchive.
		return true, "", nil, nil
	}
	token, err := e.repo.BeginWorkspaceCheckpointSave(run.ID, now)
	if err != nil {
		return true, "", nil, nil
	}
	meta, _ = e.repo.WorkspaceCheckpoint(run.ID)
	// The sweep may have loaded the run before another instance restored or
	// rebound it. Recheck the current owner before reading checkout files.
	current, ok := e.repo.RunState(run.ID)
	if !ok || current.Revision != run.Revision || !current.WorkspaceCleanedAt.IsZero() ||
		current.WorkspaceHost != run.WorkspaceHost || current.WorkspaceRoot != run.WorkspaceRoot ||
		!e.conversationWorkspaceIdle(current) || e.conversationGenerationRunning(current) {
		_ = e.repo.FinishWorkspaceCheckpointSave(run.ID, token, meta.LastUsedAt, now, "")
		return true, "", nil, nil
	}
	run = current
	released = meta.Released()
	cleanupHeld = now.Before(run.WorkspaceCleanupNotBefore)
	cleanupHeld = cleanupHeld || meta.CleanupDeferred(now)
	idleSince := meta.LastUsedAt
	if !exists {
		idleSince = run.UpdatedAt
		if idleSince.IsZero() {
			idleSince = now
		}
	}
	if run.UpdatedAt.After(idleSince) {
		idleSince = run.UpdatedAt
	}
	// An idle group is still an ongoing conversation, even if Main Bot has
	// only delegated and never invoked a workspace tool. Give it the same hour.
	if run.IsWorkspace() && !meta.FollowUp {
		if err := e.repo.MarkWorkspaceFollowUp(run.ID, token, idleSince); err != nil {
			_ = e.repo.FinishWorkspaceCheckpointSave(run.ID, token, idleSince, now, err.Error())
			return true, "", nil, err
		}
		meta.FollowUp = true
	}
	saveError := ""
	finish := func() {
		attemptedAt := time.Now().UTC()
		if now.After(attemptedAt) {
			attemptedAt = now
		}
		_ = e.repo.FinishWorkspaceCheckpointSave(run.ID, token, idleSince, attemptedAt, saveError)
	}
	claimed := false
	defer func() {
		if !claimed {
			finish()
		}
	}()
	// Capture once immediately before reclaiming to include edits made
	// outside platform activity tracking, including during a cleanup hold.
	readyToReclaim := !cleanupHeld && (!meta.FollowUp || released || !now.Before(idleSince.Add(ConversationWorkspaceIdleTTL)))
	needsSave := readyToReclaim || workspaceCheckpointNeedsSave(meta, run.UpdatedAt, now)
	if cleanupHeld {
		needsSave = heldWorkspaceCheckpointNeedsSave(meta, idleSince, now)
	}
	if needsSave {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var previous *repo.WorkspaceCheckpoint
		if !cleanupHeld && meta.CleanupRetryAt != nil && meta.SavedAt != nil && meta.SHA256 != "" {
			previous = &meta
		}
		captureErr := e.saveConversationWorkspace(ctx, run, token, previous)
		if captureErr != nil {
			if errors.Is(captureErr, repo.ErrWorkspaceCheckpointStale) {
				return true, "", nil, nil
			}
			saveError = captureErr.Error()
			return true, "", nil, captureErr
		}
	}
	if !readyToReclaim {
		return true, "", nil, nil
	}
	// Serialize the deletion claim against a rebind or new execution.
	claimed, err = e.repo.ClaimCheckpointWorkspaceCleanup(run, token, now)
	if claimed {
		return true, token, finish, err
	}
	return true, "", nil, err
}

// Preserve the first checkpoint after release or idle expiry, even while
// reclamation is held. Subsequent sweeps can reuse that checkpoint.
func heldWorkspaceCheckpointNeedsSave(meta repo.WorkspaceCheckpoint, idleSince, now time.Time) bool {
	if meta.Released() {
		if meta.ReleasedAt.After(idleSince) {
			idleSince = *meta.ReleasedAt
		}
	} else if deadline := idleSince.Add(ConversationWorkspaceIdleTTL); !now.Before(deadline) {
		idleSince = deadline
	}
	return workspaceCheckpointNeedsSave(meta, idleSince, now)
}

func workspaceCheckpointNeedsSave(meta repo.WorkspaceCheckpoint, changedAt, now time.Time) bool {
	return meta.SavedAt == nil || meta.SHA256 == "" || meta.Error != "" ||
		changedAt.After(*meta.SavedAt) || meta.SaveRetryAt != nil && !now.Before(*meta.SaveRetryAt)
}

func (e *Engine) restoreCheckpointBeforeExecution(ctx context.Context, run domain.Run) error {
	if sharesWorkspace(run) {
		return e.RestoreConversationWorkspaceIfNeeded(ctx, workspaceRunID(run))
	}
	checkpoint, exists := e.repo.WorkspaceCheckpoint(run.ID)
	// A Main Bot turn or IDE heartbeat also creates this metadata row. Old
	// reclaimed runs may therefore have a lease but no saved archive. Execution
	// must still be able to prepare repositories and archived upstream inputs;
	// EnsureConversationWorkspace remains strict for exact history restoration.
	if !exists {
		return nil
	}
	if checkpoint.SavedAt == nil {
		if checkpoint.LeaseUntil != nil && checkpoint.LeaseUntil.After(time.Now()) && checkpoint.LeaseToken != ConversationWorkspaceToken(ctx, run.ID) {
			return repo.ErrWorkspaceInUse
		}
		return nil
	}
	token := ConversationWorkspaceToken(ctx, run.ID)
	owned := token != ""
	if !owned {
		var err error
		token, err = e.beginWorkspaceUseAfterCleanup(ctx, run.ID)
		if err != nil {
			return err
		}
		// Reconnecting reuses immutable saved files; recapturing them here would
		// add another full archive/upload before the retry can even start.
		defer func() { _ = e.repo.FinishWorkspaceUse(run.ID, token, time.Now().UTC(), nil, "") }()
	}
	return e.EnsureConversationWorkspace(ctx, run.ID, token)
}

// restoreCheckpointForExecution waits for a concurrent workspace operation at
// actual node startup. A chat turn may activate a legacy run before releasing
// its lease; that temporary contention must not consume a node attempt as a
// failure. API activation preflights keep their bounded wait instead.
func (e *Engine) restoreCheckpointForExecution(ctx context.Context, run domain.Run, onBusy func()) error {
	notified := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := e.restoreCheckpointBeforeExecution(ctx, run)
		if !errors.Is(err, repo.ErrWorkspaceInUse) {
			return err
		}
		if !notified && onBusy != nil {
			onBusy()
			notified = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// RestoreConversationWorkspaceIfNeeded is called before activation changes a
// task's status or revision. A generation may reuse its existing workspace lease.
func (e *Engine) RestoreConversationWorkspaceIfNeeded(ctx context.Context, id string) error {
	run, ok := e.repo.RunState(id)
	if !ok {
		return nil // normal activation validation reports the missing run
	}
	if run.WorkspaceCleanedAt.IsZero() {
		return nil
	}
	return e.restoreCheckpointBeforeExecution(ctx, run)
}

func (e *Engine) beginWorkspaceUseAfterCleanup(ctx context.Context, id string) (string, error) {
	var token string
	var err error
	// Reclamation marks the directory before physical deletion; retry once its
	// short-lived save/delete lease completes instead of racing the deletion.
	for attempt := 0; attempt < 50; attempt++ {
		token, err = e.BeginConversationWorkspaceUse(id)
		if !errors.Is(err, repo.ErrWorkspaceInUse) {
			return token, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", err
}
