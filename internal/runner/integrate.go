package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// executeIntegrate merges the latest integration branch into the work branch
// of every repository that names one, in the run's shared checkout on this
// machine, and pushes the work branch (plus the integration branch when the
// control plane asked for a direct push). Conflicts are reported in the
// result, not as a failure.
func (d *Daemon) executeIntegrate(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	results, err := integrateTaskRepositories(ctx, d.taskWorkspace(task), task.Invocation, task.IntegrationLabel)
	var lines []domain.RunnerTaskEvent
	for i, r := range results {
		msg := fmt.Sprintf("integrated %s: %s ← %s @ %s", r.Name, r.WorkBranch, r.IntegrateBranch, r.Head)
		if len(r.Conflicts) > 0 {
			msg = fmt.Sprintf("integration conflict %s: %s ← %s: %s", r.Name, r.WorkBranch, r.IntegrateBranch, strings.Join(r.Conflicts, ", "))
		}
		lines = append(lines, domain.RunnerTaskEvent{Seq: int64(i + 1), Type: "system", Data: msg, Ts: time.Now().UTC()})
	}
	if len(lines) > 0 {
		_ = d.client.SendEvents(ctx, task.ID, lines)
	}
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, exitCode: 1, errMsg: err.Error()}
	}
	body, _ := json.Marshal(results)
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: string(body)}
}

func integrateTaskRepositories(ctx context.Context, workspace string, inv *domain.RunnerInvocation, label string) ([]domain.RunnerIntegrationResult, error) {
	results := []domain.RunnerIntegrationResult{}
	if inv == nil {
		return results, nil
	}
	for _, repo := range inv.Repositories {
		work, integrate := strings.TrimSpace(repo.WorkBranch), strings.TrimSpace(repo.IntegrateBranch)
		if work == "" || integrate == "" {
			continue
		}
		dir := workspace
		if rel := safeRunnerRepoDir(repo.Dir); rel != "" {
			dir = filepath.Join(workspace, rel)
		}
		repoCtx := gitwork.WithToken(ctx, repo.GitToken)
		gitwork.EnsureIdentity(repoCtx, dir)
		result := domain.RunnerIntegrationResult{RepositoryID: repo.RepositoryID, Name: repo.Name, WorkBranch: work, IntegrateBranch: integrate}
		conflicts, err := gitwork.MergeLatestIntegration(repoCtx, dir, work, integrate, label)
		if err != nil {
			return results, fmt.Errorf("%s: merge %s into %s: %w", repo.Name, integrate, work, err)
		}
		if len(conflicts) > 0 {
			result.Conflicts = conflicts
			return append(results, result), nil
		}
		if _, err := gitwork.C(repoCtx, dir, "push", "origin", work); err != nil {
			return results, fmt.Errorf("%s: push work branch %s: %w", repo.Name, work, err)
		}
		if repo.PushIntegrate {
			if _, err := gitwork.C(repoCtx, dir, "push", "origin", "HEAD:refs/heads/"+integrate); err != nil {
				return results, fmt.Errorf("%s: push integration branch %s: %w", repo.Name, integrate, err)
			}
			result.PushedIntegrate = true
		}
		result.Head, _ = gitwork.RevParse(repoCtx, dir, "HEAD")
		results = append(results, result)
	}
	return results, nil
}
