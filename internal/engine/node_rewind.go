package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// RewindNodeWorkspace preserves the current state before restoring a node
// checkpoint. recoveryID selects a previously saved pre-rewind state.
func (e *Engine) RewindNodeWorkspace(ctx context.Context, runID, nodeID string, attempt int, recoveryID string, actorID int64) error {
	run, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("运行不存在")
	}
	if !run.ExecutionContext.NodeCheckpointsEnabled {
		return fmt.Errorf("此运行不支持节点回滚")
	}
	nr := run.Nodes[nodeID]
	if nr == nil {
		return fmt.Errorf("节点不存在")
	}
	var target domain.NodeCheckpoint
	found := false
	if recoveryID != "" {
		for _, cp := range nr.RecoveryCheckpoints {
			if cp.ID == recoveryID {
				target, found = cp, true
				break
			}
		}
	} else {
		for _, cp := range nr.Checkpoints {
			if cp.Attempt == attempt {
				target, found = cp, true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("检查点不存在")
	}
	owner := workspaceRunID(run)
	token, err := e.repo.BeginSharedNodeRewind(run.ID, owner, time.Now().UTC())
	if err != nil {
		return err
	}
	defer e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = context.WithValue(ctx, nodeCheckpointLeaseKey{}, token)
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if e.repo.RenewWorkspaceUse(owner, token, time.Now().UTC()) != nil {
					cancel()
					return
				}
			}
		}
	}()
	current, ok := e.repo.RunState(run.ID)
	if !ok || current.Revision != run.Revision || current.Generation != run.Generation {
		return fmt.Errorf("运行状态已变化，请重试")
	}
	if err := e.repo.ValidateNodeRewind(owner, token, time.Now().UTC()); err != nil {
		return fmt.Errorf("工作区租约已失效：%w", err)
	}
	if err := e.checkpointHasNoOtherNodeChanges(ctx, current, nodeID, target, nil); err != nil {
		return err
	}
	ownerRun, ok := e.repo.RunState(owner)
	if !ok {
		return fmt.Errorf("工作区所属运行不存在")
	}
	if e.workspaceTerminals.HasRunScope(owner) || e.WorkspaceActive != nil && e.WorkspaceActive(ownerRun) {
		return fmt.Errorf("工作区终端正在使用")
	}
	if tasks, err := e.repo.ActiveRunnerTasksForWorkspace(owner); err != nil {
		return fmt.Errorf("检查执行机器任务：%w", err)
	} else if len(tasks) != 0 {
		return fmt.Errorf("执行机器仍在停止任务，请稍后重试")
	}
	if e.defaultExecutionRunner(current) != "" {
		request := domain.RunnerWorkspaceRequest{RunID: current.ID, WorkspaceRunID: workspaceRunID(current), WorkspaceLayout: current.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationNodeCheckpointReserve, CheckpointLease: token}
		response, err := e.requestWorkspaceCheckpoint(ctx, current, request)
		if err != nil {
			return err
		}
		if err := workspaceCheckpointResponseError(response); err != nil {
			return err
		}
		defer func() {
			releaseCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointRelease
			_, _ = e.requestWorkspaceCheckpoint(releaseCtx, current, request)
		}()
	}
	recovery, err := e.createRewindCheckpoint(ctx, current, attempt)
	if err != nil {
		return fmt.Errorf("保存回滚前检查点：%w", err)
	}
	e.stateMu.Lock()
	current, ok = e.repo.RunState(run.ID)
	if !ok || current.Nodes[nodeID] == nil || e.repo.ValidateNodeRewind(owner, token, time.Now().UTC()) != nil {
		e.stateMu.Unlock()
		return fmt.Errorf("工作区租约已失效")
	}
	current.Nodes[nodeID].RecoveryCheckpoints = append(current.Nodes[nodeID].RecoveryCheckpoints, recovery)
	err = e.repo.PutRunAtRevision(current, current.Revision)
	e.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("保存恢复记录：%w", err)
	}
	if err := e.repo.MarkNodeRewindPending(owner, token, time.Now().UTC()); err != nil {
		return fmt.Errorf("保存回滚状态：%w", err)
	}
	if err = e.restoreRewindCheckpoint(ctx, current, target, recovery, token); err != nil {
		return fmt.Errorf("恢复检查点失败，运行已暂停保护；请重试回滚或恢复回滚前检查点（%s）：%w", recovery.ID, err)
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	current, ok = e.repo.RunState(run.ID)
	if !ok || e.repo.ValidateNodeRewind(owner, token, time.Now().UTC()) != nil {
		return fmt.Errorf("恢复完成，但保存运行状态失败；恢复前检查点 %s 仍可用", recovery.ID)
	}
	reset := descendants(current.WorkflowSnapshot, nodeID)
	reset[nodeID] = true
	for id := range reset {
		if node := current.Nodes[id]; node != nil {
			node.Status, node.Error, node.Output, node.Activity = "pending", "", "", ""
			node.FinishedAt = time.Time{}
			node.ResumeSessionID = ""
			node.Rewound = true
			node.RestoreCheckpoint = false
		}
	}
	e.event(&current, nodeID, "workspace.rewound", fmt.Sprintf("回滚节点 %s 第 %d 次检查点；回滚前检查点 %s", nodeID, target.Attempt, recovery.ID))
	current.Events[len(current.Events)-1].ActorID = actorID
	if err := e.repo.FinishNodeRewind(current, token, current.Revision); err != nil {
		return fmt.Errorf("恢复完成，但保存运行状态失败；恢复前检查点 %s 仍可用：%w", recovery.ID, err)
	}
	e.publish(run.ID)
	return nil
}

func (e *Engine) createRewindCheckpoint(ctx context.Context, run domain.Run, attempt int) (domain.NodeCheckpoint, error) {
	if e.defaultExecutionRunner(run) == "" && !e.foreignWorkspaceBind(run) {
		root, reason := e.LocalRunWorkspace(run)
		if reason != "" {
			return domain.NodeCheckpoint{}, fmt.Errorf("%s", reason)
		}
		return runtime.CreateNodeWorkspaceCheckpoint(ctx, root, run.RepositoryContexts(), attempt)
	}
	lease, _ := ctx.Value(nodeCheckpointLeaseKey{}).(string)
	response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationNodeCheckpointCreate, NodeCheckpointAttempt: attempt, Repositories: run.RepositoryContexts(), CheckpointLease: lease})
	if err != nil {
		return domain.NodeCheckpoint{}, err
	}
	if err = workspaceCheckpointResponseError(response); err != nil {
		return domain.NodeCheckpoint{}, err
	}
	var checkpoint domain.NodeCheckpoint
	err = json.Unmarshal(response.Payload, &checkpoint)
	return checkpoint, err
}

func (e *Engine) restoreRewindCheckpoint(ctx context.Context, run domain.Run, checkpoint, recovery domain.NodeCheckpoint, token string) error {
	if err := e.repo.ValidateNodeRewind(workspaceRunID(run), token, time.Now().UTC()); err != nil {
		return err
	}
	if e.defaultExecutionRunner(run) == "" && !e.foreignWorkspaceBind(run) {
		root, reason := e.LocalRunWorkspace(run)
		if reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return runtime.RestoreNodeWorkspaceCheckpointWithRecovery(ctx, root, checkpoint, recovery)
	}
	response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationNodeCheckpointRestore, NodeCheckpoint: &checkpoint, NodeCheckpointEnd: &recovery, CheckpointLease: token, Repositories: run.RepositoryContexts()})
	if err != nil {
		return err
	}
	return workspaceCheckpointResponseError(response)
}
