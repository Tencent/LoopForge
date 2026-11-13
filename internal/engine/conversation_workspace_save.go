package engine

import (
	"context"
	"errors"
	"os"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func (e *Engine) saveConversationWorkspaceAfterUse(ctx context.Context, id, token string) error {
	run, ok := e.repo.RunState(id)
	if !ok || !run.WorkspaceCleanedAt.IsZero() || !e.conversationWorkspaceIdle(run) {
		return nil
	}
	err := e.saveConversationWorkspace(ctx, run, token)
	if os.IsNotExist(err) {
		return nil // a fresh task may not have materialized files yet
	}
	return err
}

// EndConversationWorkspaceUseAsync only transfers the lease on the foreground
// path. Compression, upload and publication run independently of the turn.
// Foreground use can supersede this lease, even from another API instance.
func (e *Engine) EndConversationWorkspaceUseAsync(ctx context.Context, id, token string) (<-chan error, error) {
	usedAt := time.Now().UTC()
	saveToken, err := e.repo.HandoffWorkspaceUseToSave(id, token, usedAt)
	if err != nil {
		return nil, err
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	saveCtx = WithConversationWorkspaceToken(saveCtx, saveToken, id)
	e.conversationSaves.Store(saveToken, context.CancelFunc(cancel))
	done := make(chan error, 1)
	go func() {
		defer close(done)
		defer cancel()
		defer e.conversationSaves.Delete(saveToken)
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-saveCtx.Done():
					return
				case <-ticker.C:
					meta, exists := e.repo.WorkspaceCheckpoint(id)
					if !exists || meta.LeaseToken != saveToken {
						cancel()
						return
					}
				}
			}
		}()
		for {
			err = e.saveConversationWorkspaceAfterUse(saveCtx, id, saveToken)
			meta, exists := e.repo.WorkspaceCheckpoint(id)
			if !exists || meta.LeaseToken != saveToken {
				err = nil // superseded: never publish an error or clear the new lease
				break
			}
			if errors.Is(err, repo.ErrWorkspaceInUse) && saveCtx.Err() == nil {
				// A preceding export may still be releasing its runner gate.
				select {
				case <-saveCtx.Done():
				case <-time.After(100 * time.Millisecond):
				}
				continue
			}
			message := ""
			if err != nil {
				message = err.Error()
			}
			finishErr := e.repo.FinishWorkspaceUse(id, saveToken, usedAt, nil, message)
			if errors.Is(finishErr, repo.ErrWorkspaceInUse) {
				err = nil
			} else {
				err = errors.Join(err, finishErr)
			}
			break
		}
		cancel()
		<-watchDone
		done <- err
	}()
	return done, nil
}

func (e *Engine) waitForBackgroundWorkspaceSave(id string) error {
	run, ok := e.repo.RunState(id)
	if !ok || e.defaultExecutionRunner(run) == "" && !e.foreignWorkspaceBind(run) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: id, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpointStatus})
		if err != nil || !runnerWorkspaceBusy(response) {
			return nil // ordinary execution preflight handles missing/offline owners
		}
		select {
		case <-ctx.Done():
			return repo.ErrWorkspaceInUse
		case <-time.After(25 * time.Millisecond):
		}
	}
}
