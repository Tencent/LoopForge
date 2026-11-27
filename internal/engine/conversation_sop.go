package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func borrowsConversationWorkspace(run domain.Run) bool {
	return run.UsesConversationWorkspace() && run.ExecutionContext.ParentRunID == ""
}

func (e *Engine) sopConversationWorkspace(ec domain.TelemetryContext) (domain.Run, error) {
	owner, ok := e.repo.RunState(ec.WorkspaceRunID)
	if !ok || !owner.IsWorkspace() || owner.ExecutionContext.ProjectID == "" || owner.ExecutionContext.ProjectID != ec.ProjectID {
		return domain.Run{}, fmt.Errorf("当前项目的对话工作区不可用")
	}
	return owner, nil
}

// The LOOP's persisted running status keeps new conversation turns out of the
// checkout. Wait for the previous turn's lease before restoring or running
// nodes. This includes a parent whose first node launches a child LOOP.
func (e *Engine) prepareBorrowedConversationWorkspace(ctx context.Context, run domain.Run) error {
	id := workspaceRunID(run)
	notified := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, previous, err := e.repo.BeginSOPWorkspacePreparation(id, time.Now().UTC())
		token, err = e.finishTakingWorkspaceLease(id, token, previous, err)
		if err == nil {
			err = e.openBorrowedConversationWorkspace(ctx, run, token)
			if finishErr := e.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, ""); finishErr != nil {
				return errors.Join(err, finishErr)
			}
		}
		if !errors.Is(err, repo.ErrWorkspaceInUse) {
			return err
		}
		if !notified {
			e.AppendRunEvent(run.ID, "workspace.waiting", "等待对话释放工作区")
			notified = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (e *Engine) openBorrowedConversationWorkspace(ctx context.Context, run domain.Run, token string) error {
	owner, err := e.sopConversationWorkspace(run.ExecutionContext)
	if err != nil {
		return err
	}
	if e.workspaceTerminals.HasRunScope(owner.ID) || e.WorkspaceActive != nil && e.WorkspaceActive(owner) {
		return errWorkspaceTerminalActive
	}
	ctx = WithConversationWorkspaceToken(ctx, token, owner.ID)
	if err := e.EnsureConversationWorkspace(ctx, owner.ID, token); err != nil {
		return err
	}
	if e.defaultExecutionRunner(owner) != "" && owner.WorkspaceCleanedAt.IsZero() {
		response, err := e.requestWorkspaceCheckpoint(ctx, owner, domain.RunnerWorkspaceRequest{
			RunID: owner.ID, WorkspaceLayout: owner.ExecutionContext.RunnerWorkspaceLayout,
			Operation: domain.RunnerWorkspaceOperationCheckpointStatus,
		})
		if err != nil {
			return err
		}
		if err := workspaceCheckpointResponseError(response); errors.Is(err, os.ErrNotExist) {
			if err := e.restoreConversationCheckpoint(ctx, owner, ""); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if len(run.RepositoryContexts()) == 0 {
		return nil
	}
	return e.bindConversationCheckouts(ctx, run, owner)
}

func (e *Engine) bindConversationCheckouts(ctx context.Context, run, owner domain.Run) error {
	var checkouts []gitwork.Checkout
	if e.defaultExecutionRunner(owner) != "" {
		request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationCheckouts})
		if err != nil {
			return err
		}
		request.WorkspaceLayout = run.ExecutionContext.RunnerWorkspaceLayout
		response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
		if err != nil {
			return err
		}
		if response.Error != "" {
			return runnerWorkspaceResponseError(response)
		}
		var inventory struct {
			Checkouts []gitwork.Checkout `json:"checkouts"`
		}
		if err := json.Unmarshal(response.Payload, &inventory); err != nil {
			return err
		}
		checkouts = inventory.Checkouts
	} else {
		profile, _, err := e.workspaceSandboxProfile(run)
		if err != nil {
			return err
		}
		requirements, err := workflowSandboxRequirements(e.repo, run.WorkflowSnapshot)
		if err != nil {
			return err
		}
		env, _, err := e.acquireRunEnvironment(ctx, run, profile, requirements)
		if err != nil {
			return err
		}
		checkouts, err = runtime.SandboxCheckouts(e.withPreparationProgress(ctx, run.ID), env.sandbox)
		if err != nil {
			return err
		}
	}
	current := run.RepositoryContexts()
	repositories := slices.Clone(current)
	for i, repository := range repositories {
		remote := repositoryCheckoutRemote(repository)
		var matches []string
		for _, checkout := range checkouts {
			dir := path.Clean(checkout.Dir)
			if !gitwork.SameCheckoutOrigin(checkout.Origin, remote) {
				continue
			}
			if path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") ||
				dir != strings.TrimSpace(dir) || strings.ContainsAny(dir, "\r\n\t\\") {
				return fmt.Errorf("已检出仓库 %s 的目录不能用于 LOOP，请整理路径后重试", repository.Name)
			}
			if dir == "." {
				dir = ""
			}
			if dir == repository.WorkspaceDir {
				matches = []string{dir}
				break
			}
			matches = append(matches, dir)
		}
		if len(matches) > 1 {
			return fmt.Errorf("工作区中有多份 %s 检出，请保留要交给 LOOP 的检出后重试", repository.Name)
		}
		if len(matches) == 1 {
			repositories[i].WorkspaceDir = matches[0]
		}
	}
	if slices.Equal(current, repositories) {
		return nil
	}
	return e.updateRunErr(run.ID, func(r *domain.Run) {
		r.Repositories, r.Repository = repositories, repositories[0]
	})
}
