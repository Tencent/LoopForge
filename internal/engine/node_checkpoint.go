package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type nodeCheckpointLeaseKey struct{}

func (e *Engine) reserveNodeCheckpointWorkspace(ctx context.Context, run domain.Run, cancel context.CancelFunc) (string, func(), error) {
	owner := workspaceRunID(run)
	var token string
	for {
		var err error
		token, err = e.repo.BeginWorkspaceUse(owner, time.Now().UTC())
		if err == nil {
			break
		}
		if !errors.Is(err, repo.ErrWorkspaceInUse) {
			return "", nil, err
		}
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	ownerRun, ok := e.repo.RunState(owner)
	if !ok || e.workspaceTerminals.HasRunScope(owner) || e.WorkspaceActive != nil && e.WorkspaceActive(ownerRun) {
		_ = e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
		return "", nil, fmt.Errorf("workspace terminal is active")
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := e.repo.RenewWorkspaceUse(owner, token, time.Now().UTC()); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	// A cancellation request returns before an executing Runner has reported
	// that its process actually exited. Keep the new attempt from creating a
	// snapshot while such a task is still live.
	for {
		tasks, err := e.repo.ActiveRunnerTasksForWorkspace(owner)
		if err != nil {
			close(done)
			_ = e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
			return "", nil, fmt.Errorf("check runner tasks: %w", err)
		}
		if len(tasks) == 0 {
			if err := e.repo.ValidateWorkspaceUse(owner, token, time.Now().UTC()); err != nil {
				close(done)
				_ = e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
				return "", nil, fmt.Errorf("工作区租约已失效：%w", err)
			}
			break
		}
		select {
		case <-ctx.Done():
			close(done)
			_ = e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
			return "", nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return token, func() {
		close(done)
		if latest, ok := e.repo.RunState(run.ID); ok && e.defaultExecutionRunner(latest) != "" {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = e.requestWorkspaceCheckpoint(releaseCtx, latest, domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceRunID: owner, WorkspaceLayout: latest.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationNodeCheckpointRelease, CheckpointLease: token})
		}
		_ = e.repo.FinishWorkspaceUse(owner, token, time.Now().UTC(), nil, "")
	}, nil
}

func (e *Engine) acquireNodeCheckpointWorkspace(ctx context.Context, run domain.Run, cancel context.CancelFunc) (string, func(), error) {
	unlock := e.execLocks.lock("node-checkpoint-workspace\x00" + workspaceRunID(run))
	if ctx.Err() != nil {
		unlock()
		return "", nil, ctx.Err()
	}
	token, release, err := e.reserveNodeCheckpointWorkspace(ctx, run, cancel)
	if err != nil {
		unlock()
		return "", nil, err
	}
	return token, func() { release(); unlock() }, nil
}

func (e *Engine) createNodeCheckpoint(ctx context.Context, run domain.Run, nodeID string, attempt int, sb *runEnvironment) (domain.NodeCheckpoint, error) {
	if runnerID := e.defaultExecutionRunner(run); runnerID != "" {
		runner, ok := e.repo.Runner(runnerID)
		if !ok || !runner.SupportsNodeCheckpoints() {
			return domain.NodeCheckpoint{}, fmt.Errorf("执行机器需升级至 %s 才能使用节点检查点", domain.RunnerVersionNodeCheckpoints)
		}
		invocation := &domain.RunnerInvocation{RunID: run.ID, NodeID: nodeID}
		credentials := sb.credentialSnapshot()
		bindRunnerRepositories(invocation, run, credentials.GitToken, credentials.GitUserName, credentials.GitUserEmail)
		invocation.GitSSHKeys = credentials.GitSSHKeys
		task := domain.RunnerTask{
			ID:       fmt.Sprintf("rt-%d-%s-node-checkpoint", time.Now().UnixNano(), safeRunnerPath(nodeID)),
			RunnerID: runnerID, Provider: "terminal", Action: domain.RunnerTaskActionNodeCheckpointCreate,
			Workspace: runnerWorkspaceRel(run, nodeID), Status: domain.RunnerTaskQueued, MaxAttempts: 1,
			RunID: run.ID, NodeID: nodeID, Invocation: invocation, NodeCheckpointAttempt: attempt, NodeCheckpointRunID: workspaceRunID(run),
		}
		task.NodeCheckpointLease, _ = ctx.Value(nodeCheckpointLeaseKey{}).(string)
		var err error
		task.Sandbox, err = e.runnerSandboxSpec(run)
		if err != nil {
			return domain.NodeCheckpoint{}, err
		}
		if err := e.repo.PutRunnerTask(task); err != nil {
			return domain.NodeCheckpoint{}, err
		}
		e.notifyRunnerDispatch("terminal")
		final, err := e.awaitRunnerTask(ctx, task.ID, nil)
		if err != nil {
			return domain.NodeCheckpoint{}, err
		}
		if final.Status != domain.RunnerTaskSuccess {
			return domain.NodeCheckpoint{}, fmt.Errorf("runner checkpoint failed: %s", final.Error)
		}
		var checkpoint domain.NodeCheckpoint
		if err := json.Unmarshal([]byte(final.Result), &checkpoint); err != nil {
			return checkpoint, fmt.Errorf("decode runner checkpoint: %w", err)
		}
		return checkpoint, nil
	}
	return runtime.CreateNodeWorkspaceCheckpoint(ctx, sb.Workspace(), run.RepositoryContexts(), attempt)
}

func (e *Engine) restoreNodeCheckpoint(ctx context.Context, run domain.Run, checkpoint, recovery domain.NodeCheckpoint, sb *runEnvironment) error {
	if e.defaultExecutionRunner(run) == "" {
		return runtime.RestoreNodeWorkspaceCheckpointWithRecovery(ctx, sb.Workspace(), checkpoint, recovery)
	}
	request := domain.RunnerWorkspaceRequest{
		RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
		Operation: domain.RunnerWorkspaceOperationNodeCheckpointRestore, NodeCheckpoint: &checkpoint, NodeCheckpointEnd: &recovery,
		Repositories: run.RepositoryContexts(),
	}
	request.CheckpointLease, _ = ctx.Value(nodeCheckpointLeaseKey{}).(string)
	response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
	if err != nil {
		return err
	}
	return workspaceCheckpointResponseError(response)
}

func firstNodeCheckpoint(run domain.Run, nodeID string) (domain.NodeCheckpoint, bool) {
	node := run.Nodes[nodeID]
	if node == nil || len(node.Checkpoints) == 0 {
		return domain.NodeCheckpoint{}, false
	}
	return node.Checkpoints[0], true
}

func (e *Engine) prepareNodeCheckpoint(ctx context.Context, run domain.Run, node domain.WorkflowNode, attempt, generation int, sb *runEnvironment) (resultErr error) {
	current, ok := e.repo.RunState(run.ID)
	if !ok || current.Generation != generation || current.Nodes[node.ID] == nil || current.Nodes[node.ID].Attempt != attempt {
		return fmt.Errorf("node attempt changed before checkpoint")
	}
	for _, existing := range current.Nodes[node.ID].Checkpoints {
		if existing.Attempt == attempt {
			return nil // Reattaching an interrupted attempt keeps its original start.
		}
	}
	restored := false
	restorePending := false
	defer func() {
		if resultErr == nil || !restorePending {
			return
		}
		lease, _ := ctx.Value(nodeCheckpointLeaseKey{}).(string)
		message := "工作区恢复中断，请通过节点回滚恢复之前保存的检查点：" + resultErr.Error()
		if err := e.repo.FailInterruptedNodeCheckpointRestore(run.ID, workspaceRunID(run), lease, node.ID, attempt, generation, message); err == nil {
			e.publish(run.ID)
		}
	}()
	if !current.Nodes[node.ID].Rewound && ((strings.EqualFold(node.RetryStrategy, "full_redo") && attempt > 1) || current.Nodes[node.ID].RestoreCheckpoint) {
		start, exists := firstNodeCheckpoint(current, node.ID)
		if !exists {
			return fmt.Errorf("full_redo requires the first attempt checkpoint")
		}
		// Capture the state before any destructive operation and persist it
		// before attempting a restore, including when a later preflight fails.
		recovery, err := e.createNodeCheckpoint(ctx, current, node.ID, attempt, sb)
		if err != nil {
			return fmt.Errorf("save recovery checkpoint: %w", err)
		}
		saved := false
		if !e.updateRunAt(run.ID, false, func(latest *domain.Run) {
			nr := latest.Nodes[node.ID]
			if latest.Generation == generation && nr != nil && nr.Attempt == attempt && nr.Status == "running" {
				nr.RecoveryCheckpoints = append(nr.RecoveryCheckpoints, recovery)
				saved = true
			}
		}) || !saved {
			return fmt.Errorf("node attempt changed while saving recovery checkpoint")
		}
		// A serialized sibling or a person may have edited the workspace after
		// this node finished. Restoring in that case would silently discard
		// their work, so only restore if the current state still matches the
		// last completed attempt of this node.
		checkpoints := current.Nodes[node.ID].Checkpoints
		if err := e.checkpointHasNoOtherNodeChanges(ctx, current, node.ID, start, sb); err != nil {
			return err
		}
		// The workspace can also change between two attempts of this node.
		// Comparing only the last attempt with the current workspace would
		// otherwise allow a full redo to erase those intervening changes.
		for i := 1; i < len(checkpoints); i++ {
			previous, next := checkpoints[i-1], checkpoints[i]
			if previous.End == nil {
				return fmt.Errorf("无法安全恢复：之前的尝试没有结束检查点")
			}
			end := domain.NodeCheckpoint{Repositories: previous.End}
			if !sameCheckpointHeads(end, next) {
				return fmt.Errorf("无法安全恢复：两次尝试之间工作区 HEAD 或分支发生变化")
			}
			files, err := e.nodeCheckpointChangedFiles(ctx, current, end, next, sb)
			if err != nil {
				return fmt.Errorf("检查尝试之间的修改：%w", err)
			}
			if len(files) != 0 {
				return fmt.Errorf("无法安全恢复：两次尝试之间工作区已有其他修改")
			}
		}
		last := checkpoints[len(checkpoints)-1]
		if last.End == nil {
			return fmt.Errorf("无法安全恢复：上一次尝试没有结束检查点")
		}
		end := domain.NodeCheckpoint{Repositories: last.End}
		if !sameCheckpointHeads(end, recovery) {
			return fmt.Errorf("无法安全恢复：工作区 HEAD 或分支在节点结束后发生变化")
		}
		files, err := e.nodeCheckpointChangedFiles(ctx, current, end, recovery, sb)
		if err != nil {
			return fmt.Errorf("检查恢复冲突：%w", err)
		}
		if len(files) != 0 {
			return fmt.Errorf("无法安全恢复：节点结束后工作区已有其他修改")
		}
		lease, _ := ctx.Value(nodeCheckpointLeaseKey{}).(string)
		if lease == "" {
			return fmt.Errorf("无法安全恢复：工作区租约不存在")
		}
		if err := e.repo.MarkNodeCheckpointRestorePending(workspaceRunID(run), lease, time.Now().UTC()); err != nil {
			return fmt.Errorf("保存恢复状态：%w", err)
		}
		restorePending = true
		if err := e.restoreNodeCheckpoint(ctx, current, start, recovery, sb); err != nil {
			return fmt.Errorf("restore first attempt: %w", err)
		}
		restored = true
	}
	checkpoint, err := e.createNodeCheckpoint(ctx, current, node.ID, attempt, sb)
	if err != nil {
		return err
	}
	if restored {
		lease, _ := ctx.Value(nodeCheckpointLeaseKey{}).(string)
		var reset []string
		if current.Nodes[node.ID].RestoreCheckpoint {
			for id := range descendants(current.WorkflowSnapshot, node.ID) {
				reset = append(reset, id)
			}
		}
		e.stateMu.Lock()
		err := e.repo.CompleteNodeCheckpointRestore(run.ID, workspaceRunID(run), lease, node.ID, attempt, generation, checkpoint, true, reset)
		e.stateMu.Unlock()
		if err != nil {
			return fmt.Errorf("保存恢复结果：%w", err)
		}
		restorePending = false
		e.publish(run.ID)
		return nil
	}
	saved := false
	if !e.updateRunAt(run.ID, false, func(latest *domain.Run) {
		nr := latest.Nodes[node.ID]
		if latest.Generation != generation || nr == nil || nr.Attempt != attempt || nr.Status != "running" {
			return
		}
		nr.Checkpoints = append(nr.Checkpoints, checkpoint)
		nr.RestoreCheckpoint = false
		nr.Rewound = false
		if strings.EqualFold(node.RetryStrategy, "full_redo") || current.Nodes[node.ID].RestoreCheckpoint {
			nr.ResumeSessionID = ""
		}
		saved = true
	}) || !saved {
		return fmt.Errorf("node attempt changed while saving checkpoint")
	}
	return nil
}

// Refuse to discard any other node's changes. Read-only review nodes do not
// prevent a reject from restoring the implementation's earlier checkpoint.
func (e *Engine) checkpointHasNoOtherNodeChanges(ctx context.Context, run domain.Run, nodeID string, target domain.NodeCheckpoint, sb *runEnvironment) error {
	if target.CreatedAt.IsZero() {
		return fmt.Errorf("无法安全恢复：检查点缺少创建时间")
	}
	runs, err := e.repo.WorkspaceRuns(workspaceRunID(run))
	if err != nil {
		return fmt.Errorf("检查共享工作区的运行：%w", err)
	}
	for _, related := range runs {
		for id, node := range related.Nodes {
			if related.ID == run.ID && id == nodeID || node == nil {
				continue
			}
			for _, cp := range node.Checkpoints {
				if cp.CreatedAt.IsZero() {
					return fmt.Errorf("无法安全恢复：节点 %s 的检查点缺少创建时间", id)
				}
				if cp.CreatedAt.Before(target.CreatedAt) && cp.End != nil {
					continue
				}
				if cp.End == nil {
					return fmt.Errorf("无法安全恢复：节点 %s 没有结束检查点", id)
				}
				end := domain.NodeCheckpoint{Repositories: cp.End}
				if !sameCheckpointHeads(cp, end) {
					return fmt.Errorf("无法安全恢复：节点 %s 改变了工作区 HEAD 或分支", id)
				}
				files, err := e.nodeCheckpointChangedFiles(ctx, run, cp, end, sb)
				if err != nil {
					return fmt.Errorf("检查节点 %s 的修改：%w", id, err)
				}
				if len(files) != 0 {
					return fmt.Errorf("无法安全恢复：节点 %s 在目标检查点之后修改了工作区", id)
				}
			}
		}
	}
	return nil
}

func sameCheckpointHeads(a, b domain.NodeCheckpoint) bool {
	if len(a.Repositories) != len(b.Repositories) {
		return false
	}
	for i, left := range a.Repositories {
		right := b.Repositories[i]
		if left.RepositoryID != right.RepositoryID || left.WorkspaceDir != right.WorkspaceDir || left.Head != right.Head || left.Branch != right.Branch {
			return false
		}
	}
	return true
}

func (e *Engine) nodeCheckpointChangedFiles(ctx context.Context, run domain.Run, start, end domain.NodeCheckpoint, sb *runEnvironment) ([]string, error) {
	if e.defaultExecutionRunner(run) == "" && !e.foreignWorkspaceBind(run) {
		root := ""
		if sb != nil {
			root = sb.Workspace()
		} else {
			var reason string
			root, reason = e.LocalRunWorkspace(run)
			if reason != "" {
				return nil, fmt.Errorf("%s", reason)
			}
		}
		return runtime.NodeWorkspaceCheckpointChangedFiles(ctx, root, start, end)
	}
	request := domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
		Operation: domain.RunnerWorkspaceOperationNodeCheckpointDiff, NodeCheckpoint: &start, NodeCheckpointEnd: &end}
	request.CheckpointLease, _ = ctx.Value(nodeCheckpointLeaseKey{}).(string)
	response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
	if err != nil {
		return nil, err
	}
	if err := workspaceCheckpointResponseError(response); err != nil {
		return nil, err
	}
	var result struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return nil, err
	}
	return result.Files, nil
}

// DiffNodeCheckpoints forwards a read-only comparison to the workspace owner.
func (e *Engine) DiffNodeCheckpoints(ctx context.Context, run domain.Run, start, end domain.NodeCheckpoint) (domain.RunnerWorkspaceResponse, error) {
	return e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{
		RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
		Operation: domain.RunnerWorkspaceOperationNodeCheckpointDiff, NodeCheckpoint: &start, NodeCheckpointEnd: &end,
	})
}

func (e *Engine) finishNodeCheckpoint(ctx context.Context, run domain.Run, node domain.WorkflowNode, attempt, generation int, sb *runEnvironment) error {
	checkpoint, err := e.createNodeCheckpoint(ctx, run, node.ID, attempt, sb)
	if err != nil {
		return err
	}
	current, ok := e.repo.RunState(run.ID)
	if !ok || current.Nodes[node.ID] == nil {
		return fmt.Errorf("node disappeared while saving checkpoint")
	}
	var start domain.NodeCheckpoint
	for _, candidate := range current.Nodes[node.ID].Checkpoints {
		if candidate.Attempt == attempt {
			start = candidate
			break
		}
	}
	var files []string
	if e.defaultExecutionRunner(run) == "" {
		files, err = runtime.NodeWorkspaceCheckpointChangedFiles(ctx, sb.Workspace(), start, checkpoint)
	} else {
		request := domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceRunID: workspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
			Operation: domain.RunnerWorkspaceOperationNodeCheckpointDiff, NodeCheckpoint: &start, NodeCheckpointEnd: &checkpoint}
		request.CheckpointLease, _ = ctx.Value(nodeCheckpointLeaseKey{}).(string)
		var response domain.RunnerWorkspaceResponse
		response, err = e.requestWorkspaceCheckpoint(ctx, run, request)
		if err == nil {
			err = workspaceCheckpointResponseError(response)
		}
		if err == nil {
			var result struct {
				Files []string `json:"files"`
			}
			err = json.Unmarshal(response.Payload, &result)
			files = result.Files
		}
	}
	if err != nil {
		return err
	}
	visible := files[:0]
	for _, file := range files {
		if !runtime.WorkspacePathSensitive(file) {
			visible = append(visible, file)
		}
	}
	files = visible
	saved := false
	if !e.updateRunAt(run.ID, false, func(latest *domain.Run) {
		nr := latest.Nodes[node.ID]
		if latest.Generation != generation || nr == nil || nr.Attempt != attempt {
			return
		}
		for i := range nr.Checkpoints {
			if nr.Checkpoints[i].Attempt == attempt {
				nr.Checkpoints[i].End = checkpoint.Repositories
				nr.Checkpoints[i].ChangedFiles = files
				saved = true
				return
			}
		}
	}) || !saved {
		return fmt.Errorf("node attempt changed while saving end checkpoint")
	}
	return nil
}
