package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// integrationTaskNode attributes integrate actions in runner task lists; the
// integration belongs to the run, not to a workflow node.
const integrationTaskNode = "_integrate"

// integrateOnRunner queues an integrate action for one repository to the
// execution machine holding the run's checkout and waits for it. With
// pushIntegrate the runner also pushes the merged result to the integration
// branch. It returns the conflicted files, if any.
func (e *Engine) integrateOnRunner(ctx context.Context, runner domain.Runner, run domain.Run, rc domain.RepositoryContext, pushIntegrate bool) ([]string, error) {
	invocation := &domain.RunnerInvocation{RunID: run.ID, NodeID: integrationTaskNode}
	identity := gitIdentityFrom(ctx)
	bindRunnerRepositories(invocation, run, gitTokenFrom(ctx), identity.name, identity.email)
	invocation.Repository = nil
	var target *domain.RunnerRepository
	for i := range invocation.Repositories {
		if invocation.Repositories[i].RepositoryID == rc.RepositoryID && invocation.Repositories[i].Dir == rc.WorkspaceDir {
			target = &invocation.Repositories[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("仓库 %s 不在运行 %s 的仓库列表中", rc.Name, run.ID)
	}
	target.IntegrateBranch = strings.TrimSpace(rc.IntegrateBranch)
	target.PushIntegrate = pushIntegrate
	invocation.Repositories = []domain.RunnerRepository{*target}
	suffix := "integrate"
	if pushIntegrate {
		suffix = "push-integrate"
	}
	task := domain.RunnerTask{
		ID:               fmt.Sprintf("rt-%d-%s-%s", time.Now().UnixNano(), safeRunnerPath(rc.RepositoryID), suffix),
		RunnerID:         runner.ID,
		Provider:         "terminal",
		Action:           domain.RunnerTaskActionIntegrate,
		IntegrationLabel: integrationCommitLabel(run),
		Workspace:        runnerWorkspaceRel(run, integrationTaskNode),
		Status:           domain.RunnerTaskQueued,
		MaxAttempts:      1,
		RunID:            run.ID,
		NodeID:           integrationTaskNode,
		Invocation:       invocation,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return nil, err
	}
	e.notifyRunnerDispatch("terminal")
	final, err := e.awaitRunnerTask(ctx, task.ID, nil)
	if err != nil {
		return nil, err
	}
	switch final.Status {
	case domain.RunnerTaskSuccess:
	case domain.RunnerTaskCanceled:
		return nil, errors.New("integrate canceled")
	default:
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = "integrate failed on runner"
		}
		return nil, errors.New(msg)
	}
	var results []domain.RunnerIntegrationResult
	if strings.TrimSpace(final.Result) != "" {
		if err := json.Unmarshal([]byte(final.Result), &results); err != nil {
			return nil, fmt.Errorf("decode integrate result: %w", err)
		}
	}
	var conflicts []string
	for _, result := range results {
		conflicts = append(conflicts, result.Conflicts...)
		if result.Head != "" {
			e.observe(run.ID, "", domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.integrated", Phase: "finish", Message: fmt.Sprintf("%s @ %s ← %s → %s", result.Name, result.WorkBranch, result.IntegrateBranch, result.Head), Attributes: map[string]any{"repositoryId": result.RepositoryID, "workBranch": result.WorkBranch, "integrateBranch": result.IntegrateBranch, "commit": result.Head, "pushedIntegrate": result.PushedIntegrate, "runnerId": runner.ID}})
		}
	}
	return conflicts, nil
}
