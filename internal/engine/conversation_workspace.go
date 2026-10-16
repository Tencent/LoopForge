package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const ConversationWorkspaceIdleTTL = time.Hour

type conversationWorkspaceTokenKey struct{}

func WithConversationWorkspaceToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, conversationWorkspaceTokenKey{}, token)
}
func ConversationWorkspaceToken(ctx context.Context) string {
	token, _ := ctx.Value(conversationWorkspaceTokenKey{}).(string)
	return token
}
func (e *Engine) BeginConversationWorkspaceUse(id string, handoffID ...string) (string, error) {
	return e.repo.BeginWorkspaceUse(id, time.Now().UTC(), handoffID...)
}
func (e *Engine) RenewConversationWorkspaceUse(id, token string) error {
	return e.repo.RenewWorkspaceUse(id, token, time.Now().UTC())
}
func (e *Engine) ConversationWorkspaceStatus(id string) (repo.WorkspaceCheckpoint, bool) {
	return e.repo.WorkspaceCheckpoint(id)
}
func (e *Engine) conversationWorkspaceIdle(run domain.Run) bool {
	if e.workspaceTerminals.HasRunScope(run.ID) {
		return false
	}
	if e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		return false
	}
	if run.Status == "running" || run.Status == "queued" || hasRunningNode(run) {
		return false
	}
	if run.Status == "waiting" && run.WorkflowSnapshot.CollaborationMode != "group" {
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
	var archive []byte
	var err error
	if ok && run.WorkspaceCleanedAt.IsZero() && e.conversationWorkspaceIdle(run) {
		archive, err = e.captureConversationWorkspace(ctx, run)
		if os.IsNotExist(err) {
			err = nil
		} // a fresh task may not have materialized files yet
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if finishErr := e.repo.FinishWorkspaceUse(id, token, now, archive, message); finishErr != nil {
		return finishErr
	}
	return err
}
func (e *Engine) captureConversationWorkspace(ctx context.Context, run domain.Run) ([]byte, error) {
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpoint})
		if err != nil {
			return nil, err
		}
		if response.Error != "" {
			return nil, fmt.Errorf("%s", response.Error)
		}
		var item struct {
			Checkpoint []byte `json:"checkpoint"`
		}
		if err = json.Unmarshal(response.Payload, &item); err != nil {
			return nil, err
		}
		if len(item.Checkpoint) == 0 || len(item.Checkpoint) > runtime.WorkspaceCheckpointMaxBytes {
			return nil, fmt.Errorf("执行机器返回无效工作区快照")
		}
		if err = runtime.ValidateWorkspaceCheckpoint(item.Checkpoint); err != nil {
			return nil, err
		}
		return item.Checkpoint, nil
	}
	root, reason := e.LocalRunWorkspace(run)
	if reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	archive, err := runtime.CreateWorkspaceCheckpoint(root)
	if os.IsNotExist(err) && run.WorkspaceHost == "" {
		return runtime.EmptyWorkspaceCheckpoint(), nil
	}
	return archive, err
}
func (e *Engine) EnsureConversationWorkspace(ctx context.Context, id, token string) error {
	_, err := e.PrepareConversationWorkspace(ctx, id, token)
	return err
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
	if hasRunningNode(run) || run.WorkflowSnapshot.CollaborationMode != "group" && (run.Status == "running" || run.Status == "queued") {
		return nil, nil
	}
	return e.restoreArchivedWorkspaceFiles(ctx, run)
}

func (e *Engine) ensureConversationWorkspace(ctx context.Context, id, token string) error {
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
		archive, err := e.repo.WorkspaceCheckpointArchive(id)
		if err != nil {
			return err
		}
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: id, Operation: domain.RunnerWorkspaceOperationRestore, Checkpoint: archive})
		if err != nil {
			return err
		}
		if response.Error != "" {
			return fmt.Errorf("%s", response.Error)
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
	archive, err := e.repo.WorkspaceCheckpointArchive(id)
	if err != nil {
		return err
	}
	if runner != "" {
		if task, exists := e.repo.RunnerTask("rt-cleanup-" + safeRunnerPath(id)); exists {
			if task.Status == domain.RunnerTaskQueued {
				if err = e.repo.CancelRunnerTask(task.ID, time.Now().UTC()); err != nil {
					return err
				}
				// The durable cleanup has not started. Preserve a still-existing
				// authoritative checkout instead of overwriting it with an older snapshot.
				probe, probeErr := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: id, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpoint})
				if probeErr == nil && probe.Error == "" {
					return e.repo.RestoreCheckpointWorkspaceBinding(id, token, "", "", time.Now().UTC())
				}
			} else if task.Status == domain.RunnerTaskLeased || task.Status == domain.RunnerTaskRunning {
				return fmt.Errorf("工作区尚在回收，请稍后恢复")
			}
		}
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: id, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationRestore, Checkpoint: archive})
		if err != nil {
			return err
		}
		if response.Error != "" {
			return fmt.Errorf("%s", response.Error)
		}
		return e.repo.RestoreCheckpointWorkspaceBinding(id, token, "", "", time.Now().UTC())
	}
	root := e.sandboxes.RunWorkspace(id)
	if info, statErr := os.Lstat(root); statErr == nil && info.IsDir() && !e.foreignWorkspaceBind(run) {
		return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
	}
	if err = runtime.RestoreWorkspaceCheckpoint(root, archive); err != nil {
		return err
	}
	return e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC())
}

// claimIdleConversationWorkspace returns managed=true even on save failure, so
// legacy immediate cleanup can never delete files that were not persisted.
func (e *Engine) claimIdleConversationWorkspace(run domain.Run, now time.Time) (managed, claimed bool, release func(), err error) {
	meta, exists := e.repo.WorkspaceCheckpoint(run.ID)
	session, shared := e.repo.SharedAssistantSession(repo.RunAssistantSessionID(run.ID))
	if !exists && run.WorkflowSnapshot.CollaborationMode != "group" && run.Status != "succeeded" && run.Status != "cancelled" && (!shared || session.Archived) {
		return false, false, nil, nil
	}
	if !run.WorkspaceCleanedAt.IsZero() {
		return false, false, nil, nil
	}
	if !e.conversationWorkspaceIdle(run) || e.conversationGenerationRunning(run) {
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
	if run.WorkflowSnapshot.CollaborationMode == "group" && !meta.FollowUp {
		if err := e.repo.MarkWorkspaceFollowUp(run.ID, token, idleSince); err != nil {
			_ = e.repo.FinishWorkspaceUse(run.ID, token, idleSince, nil, err.Error())
			return true, false, nil, err
		}
		meta.FollowUp = true
	}
	saveError := ""
	finish := func() { _ = e.repo.FinishWorkspaceUse(run.ID, token, idleSince, nil, saveError) }
	defer func() {
		if !claimed {
			finish()
		}
	}()
	if meta.SavedAt == nil || run.UpdatedAt.After(*meta.SavedAt) || meta.Error != "" || !now.Before(idleSince.Add(ConversationWorkspaceIdleTTL)) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		archive, captureErr := e.captureConversationWorkspace(ctx, run)
		if captureErr != nil {
			saveError = captureErr.Error()
			return true, false, nil, captureErr
		}
		if err = e.repo.SaveWorkspaceCheckpoint(run.ID, token, now, archive); err != nil {
			saveError = err.Error()
			return true, false, nil, err
		}
	}
	if meta.FollowUp && now.Before(idleSince.Add(ConversationWorkspaceIdleTTL)) {
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
	checkpoint, exists := e.repo.WorkspaceCheckpoint(run.ID)
	// A Main Bot turn or IDE heartbeat also creates this metadata row. Old
	// reclaimed runs may therefore have a lease but no saved archive. Execution
	// must still be able to prepare repositories and archived upstream inputs;
	// EnsureConversationWorkspace remains strict for exact history restoration.
	if !exists {
		return nil
	}
	if checkpoint.SavedAt == nil {
		if checkpoint.LeaseUntil != nil && checkpoint.LeaseUntil.After(time.Now()) && checkpoint.LeaseToken != ConversationWorkspaceToken(ctx) {
			return repo.ErrWorkspaceInUse
		}
		return nil
	}
	token := ConversationWorkspaceToken(ctx)
	owned := token != ""
	if !owned {
		var err error
		token, err = e.beginWorkspaceUseAfterCleanup(ctx, run.ID)
		if err != nil {
			return err
		}
		defer func() { _ = e.EndConversationWorkspaceUse(context.WithoutCancel(ctx), run.ID, token) }()
	}
	return e.EnsureConversationWorkspace(ctx, run.ID, token)
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
