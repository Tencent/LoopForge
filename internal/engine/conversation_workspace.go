package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/workspaceprep"
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
	return e.repo.BeginWorkspaceUse(id, time.Now().UTC())
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
	if run.Status == "running" || run.Status == "queued" || hasRunningNode(run) {
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
	job, ok := e.repo.AssistantGeneration(session.UserID, session.ID)
	return ok && job.Status == "running"
}
func (e *Engine) EndConversationWorkspaceUse(ctx context.Context, id, token string) error {
	now := time.Now().UTC()
	run, ok := e.repo.RunState(id)
	var err error
	if ok && run.WorkspaceCleanedAt.IsZero() && e.conversationWorkspaceIdle(run) {
		err = e.saveConversationWorkspace(ctx, run, token)
		if os.IsNotExist(err) {
			err = nil
		} // a fresh task may not have materialized files yet
	}
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
	_, err := e.PrepareConversationWorkspace(ctx, id, token)
	return err
}

// MaterializeConversationWorkspace checks out an idle conversation workspace that
// has never had a workspace, so Main Bot can read and edit it before any
// command ran. A workspace that was saved or reclaimed is restored by
// PrepareConversationWorkspace instead.
func (e *Engine) MaterializeConversationWorkspace(ctx context.Context, id string) (result error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	if !run.IsWorkspace() || sharesWorkspace(run) || !run.WorkspaceCleanedAt.IsZero() || e.foreignWorkspaceBind(run) {
		return nil
	}
	if meta, exists := e.repo.WorkspaceCheckpoint(id); exists && meta.SavedAt != nil {
		// A snapshot of a previously ready workspace is authoritative. A
		// snapshot taken after the very first open failed can contain only a
		// partial checkout and must still finish preparation on retry.
		if run.LatestWorkspaceOpening() == nil {
			return nil
		}
		for _, event := range run.Events {
			if event.WorkspaceOpening != nil && event.WorkspaceOpening.State == "ready" {
				return nil
			}
		}
	}
	ctx = workspaceprep.WithSink(ctx, func(p domain.WorkspaceOpening) { e.RecordWorkspaceOpening(id, p) })
	workspaceprep.Report(ctx, domain.WorkspaceOpening{State: "running", Phase: "prepare", Message: "正在准备工作区"})
	defer func() { workspaceprep.Result(ctx, result) }()
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
			if _, statErr := os.Stat(filepath.Join(dir, ".git", "agentflow-baseline")); statErr != nil {
				missing = true
			}
		}
		previous := run.LatestWorkspaceOpening()
		if !missing && (previous == nil || previous.State == "ready") {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var env *runEnvironment
	err := workspaceprep.Do(ctx, "build", "正在准备沙箱环境", func(ctx context.Context) error {
		var err error
		env, err = e.acquireConversationEnvironment(ctx, run)
		return err
	})
	if err != nil {
		return err
	}
	env.nodeMu.Lock()
	defer env.nodeMu.Unlock()
	// Same checkout as an SOP node: the responsible user's Git Token, the
	// frozen base branch, and a fresh work branch.
	gitToken := env.credentialSnapshot().GitToken
	ctx = withGitToken(e.withPreparationProgress(ctx, run.ID), gitToken)
	return env.prepareRepositoriesOnce(func() error {
		if err := prepareRepositories(ctx, env.Workspace(), run.RepositoryContexts()); err != nil {
			return explainGitAuthFailure(fmt.Errorf("repository checkout: %w", err), strings.TrimSpace(gitToken) != "")
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
			return explainGitAuthFailure(fmt.Errorf("%s", response.Error), strings.TrimSpace(e.resolveRunCredentials(run).GitToken) != "")
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
	if err := e.ensureConversationWorkspace(ctx, id, token); err != nil {
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
	if hasRunningNode(run) || !run.IsWorkspace() && (run.Status == "running" || run.Status == "queued") {
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
		if run.WorkspaceCleanedAt.IsZero() {
			return nil
		}
		if err := e.restoreOrReuseRemoteConversationCheckpoint(ctx, run); err != nil {
			return err
		}
		return e.repo.RestoreCheckpointWorkspaceBinding(id, token, "", "", time.Now().UTC())
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
	root := e.sandboxes.RunWorkspace(id)
	if info, statErr := os.Lstat(root); statErr == nil && info.IsDir() && !e.foreignWorkspaceBind(run) {
		if err = e.verifyStoredCheckpoint(ctx, id); err != nil {
			return err
		}
		return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
	}
	if err = e.restoreConversationCheckpoint(ctx, run, root); err != nil {
		return err
	}
	return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
}

// claimIdleConversationWorkspace returns managed=true even on save failure, so
// legacy immediate cleanup can never delete files that were not persisted.
func (e *Engine) claimIdleConversationWorkspace(run domain.Run, now time.Time) (managed, claimed bool, release func(), err error) {
	if sharesWorkspace(run) {
		return false, false, nil, nil
	}
	meta, exists := e.repo.WorkspaceCheckpoint(run.ID)
	session, shared := e.repo.SharedAssistantSession(repo.RunAssistantSessionID(run.ID))
	if !exists && !run.IsWorkspace() && run.Status != "succeeded" && run.Status != "cancelled" && (!shared || session.Archived) {
		return false, false, nil, nil
	}
	if !run.WorkspaceCleanedAt.IsZero() {
		return false, false, nil, nil
	}
	if !e.conversationWorkspaceIdle(run) || e.conversationGenerationRunning(run) {
		return true, false, nil, nil
	}
	// Recent conversation/IDE activity must not compete with maintenance for
	// the lease. Foreground completion saves changes; reclamation waits an hour.
	idleSinceBeforeLease := meta.LastUsedAt
	if run.UpdatedAt.After(idleSinceBeforeLease) {
		idleSinceBeforeLease = run.UpdatedAt
	}
	released := exists && meta.Released()
	if exists && meta.FollowUp && !released && now.Before(idleSinceBeforeLease.Add(ConversationWorkspaceIdleTTL)) {
		return true, false, nil, nil
	}
	token, err := e.repo.BeginWorkspaceCheckpointSave(run.ID, now)
	if err != nil {
		return true, false, nil, nil
	}
	meta, _ = e.repo.WorkspaceCheckpoint(run.ID)
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
			return true, false, nil, err
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
	defer func() {
		if !claimed {
			finish()
		}
	}()
	if released || meta.SavedAt == nil || run.UpdatedAt.After(*meta.SavedAt) || meta.Error != "" || !now.Before(idleSince.Add(ConversationWorkspaceIdleTTL)) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		captureErr := e.saveConversationWorkspace(ctx, run, token)
		if captureErr != nil {
			saveError = captureErr.Error()
			return true, false, nil, captureErr
		}
	}
	if meta.FollowUp && !released && now.Before(idleSince.Add(ConversationWorkspaceIdleTTL)) {
		return true, false, nil, nil
	}
	// Revision and node checks serialize the deletion claim against SOP or group dispatch.
	claimed, err = e.repo.ClaimCheckpointWorkspaceCleanup(run.ID, token, run.Revision, now)
	if claimed {
		release = finish
	}
	return true, claimed, release, err
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
