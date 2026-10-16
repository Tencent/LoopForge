package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// executeAlignBaseline merges a repository's default branch into its
// integration branch in a throwaway clone and pushes the result. Conflicts
// abort the merge and are reported with their hunks, not as a failure.
func (d *Daemon) executeAlignBaseline(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	dir := d.taskWorkspace(task)
	defer os.RemoveAll(dir)
	result, err := alignBaseline(ctx, dir, task.Invocation)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, exitCode: 1, errMsg: err.Error()}
	}
	body, _ := json.Marshal(result)
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: string(body)}
}

func alignBaseline(ctx context.Context, dir string, inv *domain.RunnerInvocation) (domain.RunnerAlignResult, error) {
	if inv == nil || len(inv.Repositories) != 1 {
		return domain.RunnerAlignResult{}, errors.New("align_baseline needs exactly one repository")
	}
	repo := inv.Repositories[0]
	integrate, def := strings.TrimSpace(repo.IntegrateBranch), strings.TrimSpace(repo.DefaultBranch)
	if integrate == "" || def == "" {
		return domain.RunnerAlignResult{}, errors.New("align_baseline needs integrate and default branches")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return domain.RunnerAlignResult{}, err
	}
	clone := repo
	clone.Ref, clone.WorkBranch = integrate, ""
	if err := checkoutRepository(ctx, dir, &clone); err != nil {
		return domain.RunnerAlignResult{}, err
	}
	repoCtx := gitwork.WithToken(ctx, repo.GitToken)
	gitwork.EnsureIdentity(repoCtx, dir)
	conflicts, err := gitwork.MergeLatestRemoteBranch(repoCtx, dir, def)
	if err != nil {
		return domain.RunnerAlignResult{}, fmt.Errorf("merge %s into %s: %w", def, integrate, err)
	}
	if len(conflicts) > 0 {
		blocks := domain.ExtractConflictBlocks(dir, conflicts)
		_, _ = gitwork.C(repoCtx, dir, "merge", "--abort")
		return domain.RunnerAlignResult{Conflicts: conflicts, Blocks: blocks}, nil
	}
	if _, err := gitwork.C(repoCtx, dir, "push", "origin", "HEAD:refs/heads/"+integrate); err != nil {
		return domain.RunnerAlignResult{}, fmt.Errorf("push aligned integration branch %s: %w", integrate, err)
	}
	head, _ := gitwork.RevParse(repoCtx, dir, "HEAD")
	return domain.RunnerAlignResult{Head: head}, nil
}
