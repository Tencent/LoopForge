package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// WorkspaceRestoreStatus is durable, so a refresh or another API instance sees
// the same background restore instead of launching a competing transfer.
func (e *Engine) WorkspaceRestoreStatus(id string) (domain.WorkspaceRestoreStatus, error) {
	// Read the lease before the run. A restore clears the run's cleanup mark
	// before releasing its lease, so a released lease is never paired with a
	// stale cleanup mark (which would be reported as "idle" instead of "ready").
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	run, ok := e.repo.RunState(id)
	if !ok {
		return domain.WorkspaceRestoreStatus{}, fmt.Errorf("run not found")
	}
	if sharesWorkspace(run) {
		return e.WorkspaceRestoreStatus(workspaceRunID(run))
	}
	state := domain.WorkspaceRestoreStatus{Status: "idle", StartedAt: meta.RestoreStarted}
	if meta.RestoreStatus == "restoring" && meta.LeaseUntil != nil && meta.LeaseUntil.After(time.Now()) {
		state.Status = "restoring"
	} else if run.WorkspaceCleanedAt.IsZero() || meta.SavedAt == nil {
		state.Status = "ready"
	} else if meta.RestoreStatus == "failed" {
		state.Status, state.Error = "failed", meta.RestoreError
	} else if meta.RestoreStatus == "restoring" {
		state.Status, state.Error = "failed", "工作区恢复已中断，请重新发起重跑"
	}
	return state, nil
}

// StartWorkspaceRestore acknowledges promptly. The bounded background operation
// owns its DB lease until restoration and binding acknowledgement have finished;
// closing an HTTP request cannot release it while the Runner is still working.
func (e *Engine) StartWorkspaceRestore(id string) (domain.WorkspaceRestoreStatus, error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return domain.WorkspaceRestoreStatus{}, fmt.Errorf("run not found")
	}
	if sharesWorkspace(run) {
		return e.StartWorkspaceRestore(workspaceRunID(run))
	}
	state, err := e.WorkspaceRestoreStatus(id)
	if err != nil || state.Status == "restoring" || state.Status == "ready" {
		return state, err
	}
	token, err := e.repo.BeginWorkspaceRestore(id, time.Now().UTC())
	if errors.Is(err, repo.ErrWorkspaceInUse) {
		// Another API may have claimed the restore between our read and claim.
		state, readErr := e.WorkspaceRestoreStatus(id)
		if readErr == nil && (state.Status == "restoring" || state.Status == "ready") {
			return state, nil
		}
	}
	if err != nil {
		return domain.WorkspaceRestoreStatus{}, err
	}
	if token == "" {
		return e.WorkspaceRestoreStatus(id)
	}
	go e.restoreWorkspaceInBackground(id, token)
	return e.WorkspaceRestoreStatus(id)
}

func (e *Engine) restoreWorkspaceInBackground(id, token string) {
	// Workspace-use leases last 15 minutes. Keep the whole operation bounded
	// below that TTL, including retries while an older Runner RPC finishes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ctx = WithConversationWorkspaceToken(ctx, token, id)
	var err error
	for {
		err = e.EnsureConversationWorkspace(ctx, id, token)
		if !errors.Is(err, repo.ErrWorkspaceInUse) {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(time.Second):
			continue
		}
		break
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if finishErr := e.repo.FinishWorkspaceRestore(id, token, time.Now().UTC(), message); finishErr != nil {
		log.Printf("finish workspace restore %s: %v", id, finishErr)
	}
}
