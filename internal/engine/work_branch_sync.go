package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// syncWorkBranch pushes a run repository's work branch when policy allows
// intermediate commits on it. It returns the pushed commit, or "".
func syncWorkBranch(ctx context.Context, workspace string, rc domain.RepositoryContext, message string) (string, error) {
	if !workBranchSyncable(rc) {
		return "", nil
	}
	return gitwork.PushWorkBranch(ctx, workspace, rc.WorkBranch, message)
}

// pullWorkBranch brings a reused checkout up to the remote work branch before
// a node runs. It returns the new HEAD, or "".
func pullWorkBranch(ctx context.Context, workspace string, rc domain.RepositoryContext) (string, error) {
	if !workBranchSyncable(rc) {
		return "", nil
	}
	return gitwork.PullWorkBranch(ctx, workspace, rc.WorkBranch)
}

// workBranchSyncable excludes repositories whose branch must never receive
// intermediate commits: no dedicated work branch, or one that is a shared
// base/integration branch.
func workBranchSyncable(rc domain.RepositoryContext) bool {
	branch := strings.TrimSpace(rc.WorkBranch)
	if rc.RepositoryID == "" || rc.AlignOnly || branch == "" {
		return false
	}
	for _, shared := range []string{rc.BaseBranch, rc.DefaultBranch, rc.IntegrateBranch, "master", "main"} {
		if branch == strings.TrimSpace(shared) {
			return false
		}
	}
	return true
}

// syncWorkBranches pushes every run repository after a node succeeded. When
// the run executes on a CLI Runner the checkout lives there, so the push is
// queued to that runner.
func (e *Engine) syncWorkBranches(ctx context.Context, runID string, node domain.WorkflowNode, workspace string, run domain.Run) error {
	if run.WorkflowSnapshot.CollaborationMode == "group" {
		return nil
	}
	name := strings.TrimSpace(node.Name)
	if name == "" {
		name = node.ID
	}
	message := fmt.Sprintf("agentflow: %s (%s)", name, runID)
	if runnerID := e.defaultExecutionRunner(run); runnerID != "" {
		return e.runnerWorkBranchSync(ctx, runnerID, run, node, domain.RunnerTaskActionPushWorkBranch, message)
	}
	for _, rc := range run.RepositoryContexts() {
		head, err := syncWorkBranch(ctx, repositoryWorkspacePath(workspace, rc), rc, message)
		if err != nil {
			return fmt.Errorf("%s: %w", rc.Name, err)
		}
		if head != "" {
			e.observe(runID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.pushed", Phase: "finish", Message: fmt.Sprintf("%s @ %s → %s", rc.Name, rc.WorkBranch, head), Attributes: map[string]any{"repositoryId": rc.RepositoryID, "workBranch": rc.WorkBranch, "commit": head}})
		}
	}
	return nil
}

// pullWorkBranches brings every run repository up to its remote work branch
// before a node runs.
func (e *Engine) pullWorkBranches(ctx context.Context, runID string, node domain.WorkflowNode, workspace string, run domain.Run) error {
	if runnerID := e.defaultExecutionRunner(run); runnerID != "" {
		return e.runnerWorkBranchSync(ctx, runnerID, run, node, domain.RunnerTaskActionPullWorkBranch, "")
	}
	for _, rc := range run.RepositoryContexts() {
		head, err := pullWorkBranch(ctx, repositoryWorkspacePath(workspace, rc), rc)
		if err != nil {
			return fmt.Errorf("%s: %w", rc.Name, err)
		}
		if head != "" {
			e.observe(runID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.pulled", Phase: "finish", Message: fmt.Sprintf("%s @ %s → %s", rc.Name, rc.WorkBranch, head), Attributes: map[string]any{"repositoryId": rc.RepositoryID, "workBranch": rc.WorkBranch, "commit": head}})
		}
	}
	return nil
}

// runnerWorkBranchSync queues a pull/push work-branch action to the runner
// holding the run's checkout and waits for it. Runners older than
// RunnerVersionWorkBranchSync keep the previous behaviour (no sync) so an
// outdated binary is never handed an action it would misinterpret.
func (e *Engine) runnerWorkBranchSync(ctx context.Context, runnerID string, run domain.Run, node domain.WorkflowNode, action, message string) error {
	runner, ok := e.repo.Runner(runnerID)
	if !ok {
		return fmt.Errorf("执行机器 %s 不存在", runnerID)
	}
	invocation := &domain.RunnerInvocation{RunID: run.ID, NodeID: node.ID}
	identity := gitIdentityFrom(ctx)
	bindRunnerRepositories(invocation, run, gitTokenFrom(ctx), identity.name, identity.email)
	syncable := false
	for _, repository := range invocation.Repositories {
		syncable = syncable || repository.SyncWorkBranch
	}
	if !syncable {
		return nil
	}
	if !runner.SupportsWorkBranchSync() {
		e.observe(run.ID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.skipped", Phase: "finish", Message: fmt.Sprintf("执行机器 %s 版本 %s 过旧，未同步工作分支", runnerID, runner.RunnerVersion), Attributes: map[string]any{"runnerId": runnerID, "runnerVersion": runner.RunnerVersion, "action": action}})
		return nil
	}
	suffix := "pull"
	if action == domain.RunnerTaskActionPushWorkBranch {
		suffix = "push"
	}
	task := domain.RunnerTask{
		ID:                fmt.Sprintf("rt-%d-%s-%s", time.Now().UnixNano(), safeRunnerPath(node.ID), suffix),
		RunnerID:          runnerID,
		Provider:          "terminal",
		Action:            action,
		WorkBranchMessage: message,
		Workspace:         runnerWorkspaceRel(run, node.ID),
		Status:            domain.RunnerTaskQueued,
		MaxAttempts:       1,
		RunID:             run.ID,
		NodeID:            node.ID,
		Invocation:        invocation,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return err
	}
	e.notifyRunnerDispatch("terminal")
	final, err := e.awaitRunnerTask(ctx, task.ID, nil)
	if err != nil {
		return err
	}
	switch final.Status {
	case domain.RunnerTaskSuccess:
	case domain.RunnerTaskCanceled:
		return errors.New("work branch sync canceled")
	default:
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = "work branch sync failed on runner"
		}
		return errors.New(msg)
	}
	var heads []domain.RunnerWorkBranchHead
	if strings.TrimSpace(final.Result) != "" {
		if err := json.Unmarshal([]byte(final.Result), &heads); err != nil {
			return fmt.Errorf("decode work branch result: %w", err)
		}
	}
	name := "work_branch.pulled"
	if action == domain.RunnerTaskActionPushWorkBranch {
		name = "work_branch.pushed"
	}
	for _, head := range heads {
		e.observe(run.ID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: name, Phase: "finish", Message: fmt.Sprintf("%s @ %s → %s", head.Name, head.WorkBranch, head.Head), Attributes: map[string]any{"repositoryId": head.RepositoryID, "workBranch": head.WorkBranch, "commit": head.Head, "runnerId": runnerID}})
	}
	return nil
}
